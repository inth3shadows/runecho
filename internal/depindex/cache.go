package depindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// On-disk memo for parsed package export sets.
//
// This exists because of a measurement, not a hunch. Parsing is exact and cheap
// at the median (p50 0.29ms across 3000 real packages), but net/http costs ~34ms,
// and the guard fires on EVERY edit — so a file that imports net/http pays it
// every time. Measured end to end, the hook went from 40ms to 90ms on such a
// file. That is not a rare tail: if you are editing an HTTP handler, you import
// net/http on every edit of that file.
//
// It is deliberately NOT the background-warming, lock-managed cache subsystem the
// obvious design reaches for. It is a synchronous memo: on a miss, parse and
// write; on a hit, read one small file. First edit pays full cost, every
// subsequent edit pays a file read.
//
// KEYING IS THE WHOLE DESIGN, and the first attempt got it wrong in a way worth
// recording. The key is a hash of the package's actual source BYTES, plus the
// extractor version. It is emphatically NOT the declared module version, and no
// longer the (name, size, mtime) stat triple it started as.
//
// Version-keying is wrong in exactly the cases that matter: a `replace` directive
// points at a LOCAL directory that is mutable, and a dependency can be rebuilt in
// place without its version changing.
//
// Stat-keying is subtler and was shipped before being caught. Filesystem mtime
// granularity is not the nanosecond the API implies: measured on ext4 here it is
// ~4ms (the kernel tick), and it is 1–2 SECONDS on exFAT, HFS+, NFS, and the
// Windows drvfs mounts under /mnt/c. Any same-length rewrite inside that window
// is invisible to a stat triple — which is not exotic, it is write-then-format-on-
// save, generate-then-patch, or extract-then-fix-up. And the failure is not
// symmetric: a stale set with an EXTRA name merely suppresses a violation, but a
// renamed same-length export (Alpha -> Bravo, Setup -> Start) flips straight to a
// FALSE POSITIVE, and nothing evicts it.
//
// Hashing the bytes costs what it should: the files must be read anyway to parse
// them on a miss, and reading plus hashing net/http's 945 KB is ~2ms against a
// ~107ms parse. Staleness then really is structurally impossible, rather than
// asserted by a comment over a heuristic.
//
// Entries are immutable once written, so concurrent guard runs need no locking:
// two processes computing the same key write identical bytes, and the write is a
// temp-file rename. Nothing is ever invalidated or evicted by this code — a stale
// key is simply never read again.

// extractorVersion is mixed into every key. Without it, an entry written by one
// extraction implementation would keep being served after that implementation was
// replaced — concretely: had this memo existed before the line-scanner was swapped
// for go/parser, all seven of the scanner's false-positive classes would have gone
// on firing from cached entries after the fix shipped, forever, because the key
// does not change when the EXTRACTOR does.
//
// Bump this on any change to what GoPackageExports considers an export.
const extractorVersion = "go-parser-v1"

// cacheEntry is the on-disk form. Only Resolved results are cached; abstains are
// cheap to recompute and caching them would risk persisting a transient failure
// (an unreadable directory, a partially-extracted module) as though it were a
// property of the package.
type cacheEntry struct {
	Exports []string `json:"exports"`
}

// goFileStat is one source file's identity, used for the size cap and to order
// reads deterministically. It is NOT the cache key — see the file header.
type goFileStat struct {
	name  string
	size  int64
	mtime int64
}

// packageCacheKey hashes what the extraction will actually see: the extractor
// version, the import path, and every source file's name and full contents.
//
// sources must be in the same order as files; both come from goPackageFiles,
// which sorts by name, so the key is stable across runs.
func packageCacheKey(importPath string, files []goFileStat, sources []string) string {
	if len(files) != len(sources) {
		return "" // refuse to key a mismatched pair; "" disables the memo
	}
	h := sha256.New()
	h.Write([]byte(extractorVersion))
	h.Write([]byte{0})
	h.Write([]byte(importPath))
	h.Write([]byte{0})
	for i, f := range files {
		h.Write([]byte(f.name))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(int64(len(sources[i])), 10)))
		h.Write([]byte{0})
		h.Write([]byte(sources[i]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cacheDir returns the directory holding memo entries, honouring RUNECHO_HOME the
// same way the rest of the tool does. Returns "" when no home can be determined,
// which disables the memo entirely — a missing cache costs latency, never
// correctness.
func cacheDir() string {
	base := os.Getenv("RUNECHO_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".runecho")
	}
	// Absolute, because a RELATIVE RUNECHO_HOME would silently follow the process
	// around: the guard resolves paths against the edited file's directory, so the
	// same logical cache would land in a different place after a chdir. Every
	// lookup after that is a guaranteed miss — safe, but a permanent hidden cost.
	return filepath.Join(absDir(base), "depcache")
}

// readCachedExports returns a memoized export set for key, or ok=false on any
// miss or error. Every failure path is a miss: the caller simply parses.
func readCachedExports(key string) (map[string]struct{}, bool) {
	dir := cacheDir()
	if dir == "" || key == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	// `null` and `{}` unmarshal cleanly into a zero cacheEntry, which would be
	// served as "Resolved with no exports" — flagging EVERY qualified call on that
	// package. The write path never produces such a file, so treating an empty
	// entry as a miss costs nothing.
	//
	// This closes the EMPTY-entry case only, and deliberately claims no more. A
	// planted non-empty entry — `{"exports":["Alpha"]}` under a correctly computed
	// key for a package that also exports Bravo — is still served, and `dep.Bravo()`
	// would be flagged. That needs write access to $RUNECHO_HOME/depcache, i.e. an
	// already-compromised home directory, which is outside this tool's threat
	// model. Saying so plainly beats a comment that implies tamper-resistance the
	// code does not have.
	if len(entry.Exports) == 0 {
		return nil, false
	}
	out := make(map[string]struct{}, len(entry.Exports))
	for _, name := range entry.Exports {
		out[name] = struct{}{}
	}
	return out, true
}

// writeCachedExports stores an export set under key. Best-effort: a failure to
// write costs the next run a re-parse and nothing else, so errors are dropped
// rather than surfaced into a guard verdict.
//
// The write is temp-file-then-rename so a concurrent reader never observes a
// half-written entry. The temp name includes the pid to keep two processes from
// colliding on the scratch file.
func writeCachedExports(key string, exports map[string]struct{}) {
	dir := cacheDir()
	if dir == "" || key == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if len(exports) == 0 {
		// readCachedExports treats an empty entry as a miss (see above), so writing
		// one would leave a file that is re-parsed on every edit forever and never
		// read. A package with no exported symbols is rare but real — an internal
		// package of unexported helpers — so skip the write rather than persist a
		// permanently dead entry.
		return
	}
	names := make([]string, 0, len(exports))
	for name := range exports {
		names = append(names, name)
	}
	sort.Strings(names)
	data, err := json.Marshal(cacheEntry{Exports: names})
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "."+key+"."+strconv.Itoa(os.Getpid())+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, key+".json")); err != nil {
		os.Remove(tmp)
	}
}
