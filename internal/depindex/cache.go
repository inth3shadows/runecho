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
// KEYING IS THE WHOLE DESIGN. The key is a content hash over the package
// directory's file list — names, sizes, and modification times — never the
// declared module version. Version-keying would be wrong in exactly the cases
// that matter: a `replace` directive points at a LOCAL directory that is mutable,
// and a dependency can be rebuilt in place without its version changing. Hashing
// what is actually on disk means a mutated dependency simply produces a different
// key. Staleness becomes structurally impossible rather than something the code
// has to detect and police.
//
// Entries are immutable once written, so concurrent guard runs need no locking:
// two processes computing the same key write identical bytes, and the write is a
// temp-file rename. Nothing is ever invalidated or evicted by this code — a stale
// key is simply never read again.

// cacheEntry is the on-disk form. Only Resolved results are cached; abstains are
// cheap to recompute and caching them would risk persisting a transient failure
// (an unreadable directory, a partially-extracted module) as though it were a
// property of the package.
type cacheEntry struct {
	Exports []string `json:"exports"`
}

// goFileStat is one source file's identity for keying purposes.
type goFileStat struct {
	name  string
	size  int64
	mtime int64
}

// packageCacheKey hashes a package's identity: its import path plus the name,
// size, and mtime of every source file that will be parsed. Any edit to any file
// changes the key.
func packageCacheKey(importPath string, files []goFileStat) string {
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	h := sha256.New()
	h.Write([]byte(importPath))
	h.Write([]byte{0})
	for _, f := range files {
		h.Write([]byte(f.name))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(f.size, 10)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(f.mtime, 10)))
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
	return filepath.Join(base, "depcache")
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
