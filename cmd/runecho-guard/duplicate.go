package main

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
)

// E5 duplicate-symbol: warn when an edit introduces a NEW symbol definition
// (a name not previously defined anywhere in the edited file) whose name is
// already defined in a DIFFERENT file per the latest snapshot's symbol index.
// Ask-posture ("this may be a duplicate/reimplementation"), never a block —
// same discipline as E1 dangling-refs and dropped-import, and gated OFF by
// default so it is dogfooded before becoming default.

// maxDuplicateLocations caps how many other-file locations are listed per
// symbol in the ask message — mirrors maxDanglingReferrers's rationale.
const maxDuplicateLocations = 5

// duplicateEnabled reports whether E5 checking is on (RUNECHO_GUARD_DUPLICATE=1).
// When unset, the whole E5 path is skipped — no extra store read, no behavior
// change — so it is inert until explicitly enabled for dogfooding.
func duplicateEnabled() bool { return os.Getenv("RUNECHO_GUARD_DUPLICATE") == "1" }

// duplicateWarning is one newly-introduced definition that already exists
// elsewhere in the repo's indexed snapshot.
type duplicateWarning struct {
	Symbol    string
	Locations []string // repo-relative paths other than the edited file (capped)
}

// wholeFileText reads filePath's current on-disk (pre-edit) content, capped at
// maxInFileBytes. addedDefs needs the WHOLE file's prior definitions, not just
// the Edit/MultiEdit hunk being replaced (unlike deletedDefs/DroppedImportRefs,
// whose old/new sides both come from the same hunk and so are internally
// consistent) — otherwise a symbol already defined elsewhere in the same,
// untouched part of the file would be misreported as new. Deliberately
// independent of the shared removedText/oldText used by E1/dropped-import:
// broadening THOSE to whole-file would break their hunk-symmetric comparisons.
//
// definitive distinguishes WHY the text came back empty, because the two
// causes need opposite treatment downstream. A missing file (new file being
// created) means "nothing was defined before" — "" is the correct, complete
// answer, safe for addedDefs to treat as ground truth (definitive=true). An
// existing file that's unreadable or exceeds the cap means "we don't know
// what was defined before" — treating that "" as ground truth would make
// addedDefs report every def in the edit as newly added, including ones the
// file already had (false positives on exactly the large/generated files a
// dogfood-first guard should stay quietest on), so the caller must skip the
// check entirely instead (definitive=false).
func wholeFileText(filePath string) (text string, definitive bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", os.IsNotExist(err)
	}
	if len(data) > maxInFileBytes {
		return "", false
	}
	return string(data), true
}

// addedDefs returns the definitions present in newText that are NOT present in
// oldText — the mirror of deletedDefs. Here oldText is the whole pre-edit file
// (see wholeFileText), so "absent from oldText" means "not previously defined
// anywhere in this file," matching the check's stated scope. Returns nil (no
// work) when newText defines nothing, so the common non-definitional edit
// short-circuits before touching the store.
func addedDefs(lang guard.Lang, oldText, newText string) []string {
	nu := defSet(lang, newText)
	if len(nu) == 0 {
		return nil
	}
	old := defSet(lang, oldText)
	var out []string
	for d := range nu {
		if _, ok := old[d]; !ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// checkDuplicateDefs returns one warning per added def that is already defined
// by a file OTHER than the one being edited, per the latest snapshot's symbol
// index. Shares openLatestSnapshot with checkDanglingRefs — only the query
// (DefsOfName vs RefsToName) and the warning shape differ. queryErrs mirrors
// checkDanglingRefs: per-symbol store queries that failed and were skipped, so
// zero warnings + queryErrs > 0 must not read as a definitive clean pass.
// selfConstrained says the edited file is itself under a build constraint; see
// the goBuildTagged block comment for why that changes which candidates count.
func checkDuplicateDefs(lang guard.Lang, dir, filePath string, added []string, selfConstrained bool) (warns []duplicateWarning, queryErrs int) {
	if len(added) == 0 {
		return nil, 0
	}
	// Go only. The whole check rests on duplicateCandidates' same-directory
	// rule, and that rule is a Go-ism: in Go the package IS the directory, so
	// two files in one directory defining the same top-level name genuinely
	// collide — a compile error worth surfacing before it happens.
	//
	// Python and JS/TS have no such collision. Each file is its own module
	// namespace, so `scripts/a.py` and `scripts/b.py` can both define `main`
	// with nothing shared between them — which is exactly how a scripts/
	// directory full of independent entry points is supposed to look. Applying
	// the directory rule there reports a conflict that does not exist.
	//
	// The live decision log is unambiguous: every Python and JS duplicate ask on
	// record is this false positive — `main` most of all (20 of 35), plus the
	// per-script local helpers (`pad`, `parseArgs`, `escapeHtml`, `printValidation`)
	// and re-declared TS types (`TrackBStratum`, `TrackBSignalScore`) that sibling
	// scripts each define for themselves. Zero were real.
	//
	// This does give up flagging a genuine Python/JS reimplementation (two copies
	// of `escapeHtml` in one directory). That is a style concern with no compile
	// or runtime consequence, and this guard is a hallucination gate rather than
	// a DRY linter — not worth one true positive per many false ones.
	if lang != guard.LangGo {
		return nil, 0
	}
	// Test files legitimately reuse symbol names across files (test functions,
	// fixtures, table-driven helpers named after what they cover), so a symbol
	// "introduced" in a test file that also exists in another test file is almost
	// always a false positive, not a reimplementation. Skip the check for them —
	// this was ~95% of the dogfood asks.
	if isTestFile(filePath) {
		return nil, 0
	}
	db, snapID, self, ok, degraded := openLatestSnapshot(dir, filePath)
	if !ok {
		if degraded {
			// Store-level failure (unreadable store, failed List, schema-newer):
			// the check could not run at all — count it so zero warnings does not
			// read as a clean pass (#138), mirroring the per-symbol queryErrs below.
			return nil, 1
		}
		return nil, 0
	}
	defer db.Close()

	// Resolve the repo root only when the edited file is constrained — the
	// common unconstrained edit pays nothing.
	//
	// A root we cannot resolve disables the suppression entirely rather than
	// suppressing everything: the two "cannot tell" paths in this function
	// deliberately resolve in OPPOSITE directions. Failing to resolve the root
	// affects every candidate at once, so defaulting it to silence could mute the
	// check wholesale; failing to read ONE candidate affects only that candidate,
	// and is counted (see dropConstrained) rather than passed off as clean.
	top := ""
	if selfConstrained {
		if t, err := gitutil.TopLevel(dir); err == nil {
			top = t
		} else {
			selfConstrained = false
		}
	}

	for _, a := range added {
		paths, err := db.DefsOfName(snapID, a)
		if err != nil {
			queryErrs++
			continue // fail-open for this symbol, keep checking the rest
		}
		others := duplicateCandidates(excludeSelf(paths, self), self)
		if selfConstrained {
			var unknown int
			others, unknown = dropConstrained(top, others)
			queryErrs += unknown
		}
		if len(others) > 0 {
			if len(others) > maxDuplicateLocations {
				others = others[:maxDuplicateLocations]
			}
			warns = append(warns, duplicateWarning{Symbol: a, Locations: others})
		}
	}
	return warns, queryErrs
}

// duplicateCandidates keeps only the other-file definitions that are a genuine
// duplicate of a symbol added to the edited (Go) file: a GO file, in the SAME
// directory (= the same package), and not itself a test file.
//
// Same-language is required, not just same-directory. The snapshot's DefsOfName
// lookup matches on symbol name across every indexed language, so a Go file
// adding `main` in a directory that also holds a Python `def main():` would
// otherwise be reported as a duplicate of the Python one — a cross-language
// false positive that contradicts this check's whole premise (Go and Python are
// separate namespaces; only same-package Go files actually collide). The Go-only
// gate in checkDuplicateDefs covers the EDITED file's side; this covers the
// candidate side.
//
// A name shared across directories is not a duplicate either — it is an
// unrelated same-named symbol in another package (every Go binary defines
// `main`; many packages define `Load`/`New`/`String`), which the compiler keeps
// distinct and which dominated the dogfood false positives. Within one directory,
// two Go files sharing a top-level name is a real collision (a compile error the
// check surfaces early).
func duplicateCandidates(others []string, self string) []string {
	selfDir := path.Dir(self)
	var out []string
	for _, o := range others {
		if path.Dir(o) == selfDir && !isTestFile(o) && guard.LangFor(o) == guard.LangGo {
			out = append(out, o)
		}
	}
	return out
}

// --- build constraints ----------------------------------------------------
//
// Two Go files in one package defining the same top-level name only collide if
// the compiler ever sees both at once. A complementary constraint pair never
// does: `//go:build unix` / `//go:build !unix`, or the implicit constraint in a
// `_windows.go` / `_linux.go` filename, compiles exactly one side per
// GOOS/GOARCH. This is not hypothetical — it is the ONLY live Go
// duplicate-symbol ask on record (internal/store/lock_unix.go vs lock_other.go,
// both defining WithFileLock, flagged twice and approved both times), so
// without this the check's entire observed true-signal budget is a false
// positive.
//
// The rule is deliberately narrow: suppress only when BOTH files are
// constrained. A constrained file and an unconstrained one DO collide whenever
// the constraint holds, and two files under the same tag (`//go:build linux`
// twice) collide too — both stay flagged. Telling "complementary" from
// "overlapping" needs a real constraint evaluator (go/build/constraint over
// every GOOS×GOARCH pair); for an ask-posture guard the both-constrained
// heuristic buys the whole observed win at none of that cost.

// maxBuildHeaderBytes caps how much of a candidate file is read looking for a
// constraint. Constraints must precede the package clause, so anything past a
// generous license-header allowance cannot be one.
const maxBuildHeaderBytes = 8 << 10

// goBuildConstrained reports whether the file being edited is itself under a
// build constraint. Both sides of the edit are consulted because neither alone
// is sufficient: an Edit hunk rarely carries the file header, and a newly
// created file has no pre-edit text to carry it.
func goBuildConstrained(filePath, oldText, newText string) bool {
	return goFilenameConstrained(filePath) || goBuildTagged(oldText) || goBuildTagged(newText)
}

// goBuildTagged reports whether Go source carries an explicit build constraint
// in its header. The scan stops at the package clause because that is where the
// go tool stops honoring them — a `//go:build` line further down (inside a test
// fixture string, or a heredoc-style literal) is not a constraint and must not
// read as one.
func goBuildTagged(src string) bool {
	rest := src
	for rest != "" {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "//go:build"), strings.HasPrefix(t, "// +build"):
			return true
		case strings.HasPrefix(t, "package "):
			return false
		}
	}
	return false
}

// goFilenameConstrained reports whether a .go filename's trailing underscore
// field is a GOOS or GOARCH name, which the go tool treats as an implicit build
// constraint (lock_windows.go builds only on Windows). Mirrors go/build's rule:
// only the final field is decisive, and a file whose whole name IS the suffix
// (windows.go) is not constrained.
func goFilenameConstrained(p string) bool {
	base := strings.TrimSuffix(path.Base(filepath.ToSlash(p)), ".go")
	i := strings.LastIndex(base, "_")
	if i <= 0 {
		return false
	}
	last := base[i+1:]
	return goosNames[last] || goarchNames[last]
}

// goosNames / goarchNames are the values the go tool accepts as implicit
// filename constraints. Kept as literal sets rather than derived at runtime:
// the guard runs as a hook on the edit path and must not shell out to `go`.
var goosNames = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true, "js": true,
	"linux": true, "nacl": true, "netbsd": true, "openbsd": true, "plan9": true,
	"solaris": true, "wasip1": true, "windows": true, "zos": true,
}

var goarchNames = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "arm64": true,
	"arm64be": true, "armbe": true, "loong64": true, "mips": true,
	"mips64": true, "mips64le": true, "mips64p32": true, "mips64p32le": true,
	"mipsle": true, "ppc": true, "ppc64": true, "ppc64le": true, "riscv": true,
	"riscv64": true, "s390": true, "s390x": true, "sparc": true,
	"sparc64": true, "wasm": true,
}

// dropConstrained removes candidate files that are themselves build-constrained.
// Only ever called when the edited file is constrained too, so what it removes
// is exactly the both-constrained pair described above.
//
// unknown counts candidates whose constraint could not be determined because the
// file could not be read. Those are dropped — on this path "cannot tell" resolves
// toward silence rather than toward an ask the decision log says is wrong — but
// dropping one is a suppressed warning, and this file's own discipline (#138) is
// that a suppressed check must never be indistinguishable from a clean pass. The
// caller folds unknown into queryErrs so it surfaces as degraded.
func dropConstrained(top string, others []string) (kept []string, unknown int) {
	for _, o := range others {
		constrained, known := goFileConstrained(top, o)
		switch {
		case !known:
			unknown++
		case !constrained:
			kept = append(kept, o)
		}
	}
	return kept, unknown
}

// goFileConstrained reports whether the repo-relative Go file at rel is built
// only under some constraint, and whether that could be determined at all.
//
// known is false when the file cannot be opened. That is not merely theoretical:
// the candidate path comes from the snapshot index, so it may have been deleted
// since indexing, and a repo whose enrolled source root differs from its git
// top-level would resolve it against the wrong base. Reporting it as a definite
// "constrained" would silently swallow a real duplicate warning.
func goFileConstrained(top, rel string) (constrained, known bool) {
	if goFilenameConstrained(rel) {
		return true, true
	}
	f, err := os.Open(filepath.Join(top, filepath.FromSlash(rel)))
	if err != nil {
		return false, false
	}
	defer f.Close()
	head := make([]byte, maxBuildHeaderBytes)
	n, _ := io.ReadFull(f, head)
	return goBuildTagged(string(head[:n])), true
}

// isTestFile reports whether a path is a test file for its language — the class
// of file whose symbol names (Test*/spec/fixture helpers) recur across files by
// convention rather than by reimplementation. Accepts absolute or repo-relative
// paths (both use '/' on the supported platforms).
func isTestFile(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"): // Go
		return true
	case strings.HasSuffix(base, "_test.py") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"): // Python
		return true
	}
	// JS/TS: *.test.* / *.spec.* (before the extension).
	for _, infix := range []string{".test.", ".spec."} {
		if strings.Contains(base, infix) {
			return true
		}
	}
	// A conventional tests/ directory anywhere in the path.
	return strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}
