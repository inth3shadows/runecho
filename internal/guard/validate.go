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
	// Suggestions are the closest known symbols by edit distance, nearest first,
	// when near enough to be likely typos/hallucinations of Symbol (nil if none).
	// Model-free; capped at suggestMaxCandidates. Suggestions[0] is the best
	// match — callers that want just one should take it rather than re-rank.
	Suggestions []string
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

	// openSeeds/braceSeeds are computed once per diff, up front, and reused across
	// both passes below. Pass 1 (extractDefsSeeded) needs BOTH, not just
	// braceSeed: without the same openSeed Pass 2 uses, Pass 1's own local
	// string-open tracking would start unseeded at each hunk, so a hunk beginning
	// inside a pre-existing docstring has its closing `"""` misread as OPENING a
	// new string — desyncing Pass 1's brace count from Pass 2's correctly-masked
	// one (round-3 review finding on PR #290).
	openSeeds := make([]func(lineNo int) string, len(diffs))
	braceSeeds := make([]func(lineNo int) int, len(diffs))
	// bracketSeeds is PyDeclaredNames' own seed (#294); paramSigSeeds is
	// PyParamNames' own — a DIFFERENT rule from LocallyBoundNames'
	// defSigDepthSeedFunc (see PyParamSigDepthBefore's doc for why the two
	// must not share a seed). Hoisted the same way as openSeeds/braceSeeds.
	bracketSeeds := make([]func(lineNo int) int, len(diffs))
	paramSigSeeds := make([]func(lineNo int) int, len(diffs))
	for i, fd := range diffs {
		lang := LangFor(fd.Path)
		fd = fd.withSeeds()
		openSeeds[i] = seedFunc(lang, fd)
		braceSeeds[i] = braceDepthSeedFunc(lang, fd)
		bracketSeeds[i] = bracketDepthSeedFunc(lang, fd)
		paramSigSeeds[i] = paramSigDepthSeedFunc(lang, fd)
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
	for i, fd := range diffs {
		lang := LangFor(fd.Path)
		// Seeded so a dict key added to an EXISTING multi-line literal (opener
		// unchanged, outside the diff) is not added to known as a definition here
		// — which would silently suppress Pass 2's now-correct read of it as a
		// reference. See extractDefsSeeded.
		for _, def := range extractDefsSeeded(lang, fd.AddedLines, openSeeds[i], braceSeeds[i]) {
			known[def] = struct{}{}
		}
		for _, imp := range ExtractImports(lang, fd.AddedLines) {
			known[imp] = struct{}{}
		}
		// JS binds callables by forms ExtractDefs/ExtractImports miss —
		// destructuring (`const [x, setX] = useState()`), object destructure, and
		// computed-assign (`const fn = handlers[k]`). A bare call to one of those is
		// not a hallucination, so fold the declarator binding targets in. JSDeclared-
		// Names (not the over-inclusive LocallyBoundNames) is used because the latter
		// binds every identifier it sees, including type names.
		//
		// That is a statement about which EXTRACTOR is used, not a guarantee that no
		// type name can ever be folded: JSParamNames below binds parameters, and
		// keeping a TS annotation out of that is an ongoing property of
		// splitJSParamList and jsBindingTargets, not a structural impossibility.
		// This sees only the added lines here; the hook path additionally folds
		// whole-file bindings via addInFileDefs for pre-existing binding lines.
		if lang == LangJS {
			for _, name := range JSDeclaredNames(fd.AddedLines) {
				known[name] = struct{}{}
			}
			// Parameters, the JS sibling of the PyParamNames fold below and the
			// GoDeclaredNames fold above — both of which have bound parameters
			// since they shipped. Without this a destructured callback prop
			// (`function C({onChange}) { … onChange(v) }`) resolves nowhere and
			// its bare call is reported as a hallucination (#302).
			for _, name := range JSParamNames(fd.AddedLines) {
				known[name] = struct{}{}
			}
		}
		// Go's sibling gap, and it is not optional. This branch used to be absent
		// with the note "Go already skips bare lowercase refs, so this fold belongs
		// to JS/TS alone" — true until unexported Go references started being
		// checked, and false the moment they were.
		//
		// The whole-file fold in FoldInFileDefs runs over the PRE-EDIT file, which
		// by definition cannot contain a binding this edit is adding. So a hunk
		// that binds and calls in the same breath — `handler := makeHandler()` then
		// `handler(it)`, the shape a model produces whenever it writes a new
		// function — had no binding anywhere in the known set. Two default-on paths
		// were affected: every Edit hunk, and every Write creating a new Go file
		// (where readFileLines returns nil and nothing is folded at all).
		if lang == LangGo {
			for _, name := range GoDeclaredNames(fd.AddedLines) {
				known[name] = struct{}{}
			}
		}
		// Python's sibling gap: a local callable bound by assignment
		// (`handler = HANDLERS[key]; handler(payload)`) is not a hallucination.
		// PyDeclaredNames binds only assignment targets (not params/annotations),
		// same precision discipline as JSDeclaredNames.
		if lang == LangPython {
			for _, name := range PyDeclaredNames(fd.AddedLines, bracketSeeds[i]) {
				known[name] = struct{}{}
			}
			// A parameter used as a callable (`def f(cb: Handler): cb()`,
			// `lambda x, fetch: fetch()`) is bound by its signature, not a
			// hallucination. Fold parameter NAMES only — never their types.
			//
			// Known widening (accepted, same posture as PyDeclaredNames): the fold
			// is whole-file, so a param in one function suppresses a bare call to
			// that same name in another. Params skew toward exactly the generic
			// callable names a hallucination invents (handler, cb, fn, process,
			// transform), so this widens the false-negative surface more than the
			// def/const folds do. Tightening it would require per-function scope
			// tracking the line-based guard deliberately avoids; the tradeoff
			// favors never falsely flagging a real parameter call.
			for _, name := range PyParamNames(fd.AddedLines, paramSigSeeds[i]) {
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
	for i, fd := range diffs {
		lang := LangFor(fd.Path)
		if lang == LangUnknown {
			continue
		}
		// When the file is on disk (pre-commit path), seed the per-hunk string
		// state from the lines above each hunk so a hunk that begins inside a
		// pre-existing docstring is masked, not scanned as code (#145).
		for _, ref := range extractRefs(lang, fd.AddedLines, openSeeds[i], braceSeeds[i]) {
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
			suggestions, _ := Suggest(ref.Name, known)
			violations = append(violations, Violation{
				File:        fd.Path,
				Line:        ref.LineNo,
				Symbol:      ref.Name,
				Lang:        lang,
				Suggestions: suggestions,
			})
		}
	}
	return violations
}

// seedFunc returns the open-string-state seed for a diff, mapping a line number to
// the unterminated multi-line string delimiter in effect at the START of that line
// (nil when no seed is available — unseeded scanning, the pre-#145 behavior).
//
// Every reference-extracting check must use this, not a nil seed: without it an
// edit block that begins inside a pre-existing docstring or string literal is
// scanned as CODE, and prose inside it reads as calls. That was the single
// largest false-positive source the guard has had (#145 for pre-commit, #178 for
// the hook path, which carries ~all real traffic).
//
// It and the four depth seed funcs below are projections of one SeedState (see
// seedstate.go); they differ only in which field they read and in the four
// depths being Python-only.
func seedFunc(lang Lang, fd FileDiff) func(int) string {
	if lang == LangUnknown {
		// No extractor runs for an unknown language, so a seed would cost a
		// full read and walk of (say) a staged 7 MiB .json for nothing.
		return nil
	}
	if fd.AbsPath != "" {
		t := fd.seedTable(lang)
		if t == nil {
			return nil
		}
		return func(lineNo int) string { return t.at(lineNo).Open }
	}
	if len(fd.SeedByLine) > 0 {
		// Hook path: synthetic line numbers, so the seed is precomputed per block
		// by the caller (see FileDiff.SeedByLine). A block with no entry reads ""
		// — the same unseeded behavior as before.
		seeds := fd.SeedByLine
		return func(lineNo int) string { return seeds[lineNo] }
	}
	return nil
}

// depthSeedFunc is the shared body of the four Python depth seed funcs: pick
// field from the file's seed table on the pre-commit path (AbsPath), else read
// the hook's precomputed per-block map. nil — unseeded, depth 0 — for any
// other language, since those trackers are never consulted there.
func depthSeedFunc(lang Lang, fd FileDiff, field func(SeedState) int, byLine map[int]int) func(int) int {
	if lang != LangPython {
		return nil
	}
	if fd.AbsPath != "" {
		t := fd.seedTable(lang)
		if t == nil {
			return nil
		}
		return func(lineNo int) int { return field(t.at(lineNo)) }
	}
	if len(byLine) > 0 {
		return func(lineNo int) int { return byLine[lineNo] }
	}
	return nil
}

// braceDepthSeedFunc returns the pyBraceDepth seed for a diff: the {}-nesting
// depth in effect at the START of each line (#289), so a hunk that begins
// inside an already-open (unchanged) multi-line dict literal doesn't start
// scanning at depth 0 — the same class of leak seedFunc closes for strings.
func braceDepthSeedFunc(lang Lang, fd FileDiff) func(int) int {
	return depthSeedFunc(lang, fd, func(s SeedState) int { return s.Brace }, fd.PyBraceDepthByLine)
}

// bracketDepthSeedFunc returns PyDeclaredNames/PyParamNames' own
// ()/[]/{}-bracket-depth seed for a diff (#294).
func bracketDepthSeedFunc(lang Lang, fd FileDiff) func(int) int {
	return depthSeedFunc(lang, fd, func(s SeedState) int { return s.Bracket }, fd.PyBracketDepthByLine)
}

// defSigDepthSeedFunc returns the def-signature PAREN-ONLY depth
// LocallyBoundNames tracks (#294). LocallyBoundNames ONLY — PyParamNames needs
// paramSigDepthSeedFunc; see PyParamSigDepthBefore's doc.
func defSigDepthSeedFunc(lang Lang, fd FileDiff) func(int) int {
	return depthSeedFunc(lang, fd, func(s SeedState) int { return s.DefSig }, fd.PyDefSigDepthByLine)
}

// paramSigDepthSeedFunc returns PyParamNames' OWN def-signature depth (#294) —
// ALL of ()/[]/{}, not parens alone.
func paramSigDepthSeedFunc(lang Lang, fd FileDiff) func(int) int {
	return depthSeedFunc(lang, fd, func(s SeedState) int { return s.ParamSig }, fd.PyParamSigDepthByLine)
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
