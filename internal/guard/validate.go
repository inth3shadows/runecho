package guard

import (
	"bufio"
	"os"
	"path"
	"strings"
)

// Violation records a single unresolved symbol reference.
type Violation struct {
	File   string
	Line   int
	Symbol string
	Lang   Lang
	// Suggestion is the closest known symbol by edit distance, if one is near
	// enough to be a likely typo/hallucination of it ("" if none). Model-free.
	Suggestion string
}

// Run validates diffs against the known symbol set and returns any violations.
// symbols is the set of known symbol names from the IR snapshot.
// ignorePath is the optional path to a .runechoguardignore file (empty = none).
func Run(symbols map[string]struct{}, ignorePath string, diffs []FileDiff) []Violation {
	// Build the known set: IR symbols + ignore list. A line containing a glob
	// metacharacter (*, ?, [) is matched against each unresolved reference in
	// pass 2 instead of being added as a literal name — this lets a repo
	// allowlist a whole family of bare global names (`track*` for injected
	// analytics calls) in one line instead of every individual name. Note
	// this only helps bare, unqualified references: a qualified call like
	// `React.useState()` is already exempt regardless of the ignore file
	// (ExtractRefs never emits it as a reference — see TECHNICAL.md).
	known := make(map[string]struct{}, len(symbols))
	for name := range symbols {
		known[name] = struct{}{}
	}
	var ignoreGlobs []string
	if ignorePath != "" {
		for _, name := range loadIgnore(ignorePath) {
			if strings.ContainsAny(name, "*?[") {
				ignoreGlobs = append(ignoreGlobs, name)
				continue
			}
			known[name] = struct{}{}
		}
	}

	// Pass 1: collect all new definitions AND imported names across the entire
	// diff and add to known. An imported name (`from pathlib import Path`,
	// `import {Foo} from './m'`) is a real, bound symbol — a bare call to it is
	// not a hallucination. The hook-mode known-set builder folds imports the same
	// way; without this, the pre-commit path flagged bare calls to any imported
	// symbol whose import line sat outside a hunk's def set (issues #76, #80).
	// A pre-existing import (outside the staged hunk) is resolved separately via
	// the indexed IR: generator.go indexes each file's bound import names under the
	// "import_name" symbol kind, so SymbolsForLatestSnapshot already carries them
	// into `symbols` here — the deeper half of #76/#80, closed by PR #82.
	for _, fd := range diffs {
		lang := LangFor(fd.Path)
		for _, def := range ExtractDefs(lang, fd.AddedLines) {
			known[def] = struct{}{}
		}
		for _, imp := range ExtractImports(lang, fd.AddedLines) {
			known[imp] = struct{}{}
		}
		// JS binds callables by forms ExtractDefs/ExtractImports miss —
		// destructuring (`const [x, setX] = useState()`), object destructure, and
		// computed-assign (`const fn = handlers[k]`). A bare call to one of those is
		// not a hallucination, so fold the declarator binding targets in. JSDeclared-
		// Names (not the over-inclusive LocallyBoundNames) is used precisely so a
		// param type annotation can't leak a type name and mask a real undefined ref.
		// This sees only the added lines here; the hook path additionally folds
		// whole-file bindings via addInFileDefs for pre-existing binding lines.
		// JS-only: Go/Python also use `const`/`var`, and Go already skips bare
		// lowercase refs, so this fold belongs to JS/TS alone.
		if lang == LangJS {
			for _, name := range JSDeclaredNames(fd.AddedLines) {
				known[name] = struct{}{}
			}
		}
		// Ambient test-runner globals (describe/it/expect/…) resolve only inside a
		// spec file, where the runner injects them — see FoldTestGlobals.
		FoldTestGlobals(known, fd.Path)
	}

	// Pass 2: collect references, flag anything not in known set.
	// Dedupe by (file, symbol) — report first line only.
	seen := make(map[string]struct{})
	var violations []Violation
	for _, fd := range diffs {
		lang := LangFor(fd.Path)
		if lang == LangUnknown {
			continue
		}
		// When the file is on disk (pre-commit path), seed the per-hunk string
		// state from the lines above each hunk so a hunk that begins inside a
		// pre-existing docstring is masked, not scanned as code (#145).
		var openSeed func(int) string
		if fd.AbsPath != "" {
			openSeed = openSeedFor(lang, fd.AbsPath)
		}
		for _, ref := range extractRefs(lang, fd.AddedLines, openSeed) {
			if _, ok := known[ref.Name]; ok {
				continue
			}
			if matchesIgnoreGlob(ref.Name, ignoreGlobs) {
				continue
			}
			key := fd.Path + "\x00" + ref.Name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			suggestion, _ := Suggest(ref.Name, known)
			violations = append(violations, Violation{
				File:       fd.Path,
				Line:       ref.LineNo,
				Symbol:     ref.Name,
				Lang:       lang,
				Suggestion: suggestion,
			})
		}
	}
	return violations
}

// maxSeedFileBytes caps the file read for pre-hunk string-state seeding. Past it,
// seeding is skipped (fall back to hunk-only scanning) rather than reading an
// unbounded blob into memory on the pre-commit path.
const maxSeedFileBytes = 8 << 20 // 8 MiB

// openSeedFor reads absPath and returns a function mapping a 1-based new-file line
// number to the unterminated multi-line string delimiter in effect at the START of
// that line — the seed ExtractRefs uses to mask a hunk that begins inside a
// pre-existing string/docstring (issue #145). Returns nil (no seeding, hunk-only
// scanning) when the file can't be read or exceeds maxSeedFileBytes — fail-open,
// matching the guard's degraded-input posture. The working-tree file is read; for
// the unchanged context above a hunk it matches the staged content the diff came
// from (an unstaged edit to that context is a rare corner and stays fail-open).
func openSeedFor(lang Lang, absPath string) func(int) string {
	data, err := os.ReadFile(absPath)
	if err != nil || len(data) > maxSeedFileBytes {
		return nil
	}
	fileLines := strings.Split(string(data), "\n")
	// prefix[k] is the string-open state at the START of 1-based line k+1: prefix[0]
	// is "" (before line 1), and each step threads one line through the same masking
	// ExtractRefs uses, so prefix[k] is exactly the state ExtractRefs would reach had
	// it scanned the file from the top.
	prefix := make([]string, len(fileLines)+1)
	open := ""
	for i, ln := range fileLines {
		prefix[i] = open
		_, open = stripLiteralsStateful(lang, ln, open)
	}
	prefix[len(fileLines)] = open
	return func(lineNo int) string {
		idx := lineNo - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(prefix) {
			idx = len(prefix) - 1
		}
		return prefix[idx]
	}
}

// matchesIgnoreGlob reports whether name matches any of the guardignore glob
// patterns. A malformed pattern (path.ErrBadPattern) never matches rather
// than erroring the whole run — the same fail-open posture as the rest of
// the guard's degraded-input handling.
func matchesIgnoreGlob(name string, globs []string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}

// loadIgnore reads a guardignore file and returns non-comment, non-blank lines.
func loadIgnore(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return names
}
