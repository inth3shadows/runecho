package guard

import (
	"regexp"
	"strings"
)

// File-scope resolution: catch a reference to a symbol that exists in the REPO
// but is not resolvable in the EDITED FILE — the "real symbol, wrong scope" class
// (a helper used without importing it, a module function called without its
// qualifier, a constant referenced across module boundaries).
//
// This is the dominant class in real agent transcripts, and the additive check in
// validate.go structurally cannot see it: that check resolves a bare reference
// against the REPO-wide symbol set, so a symbol living anywhere in the repo
// resolves and stays silent no matter which file uses it.
//
// # The firewall
//
// The check only considers a name that is ALREADY in the repo symbol set. That
// single precondition is what keeps it safe:
//
//   - a name absent from the repo is an invented symbol — the additive check's
//     job, and flagging it here would double-report and widen that surface;
//   - a name present in the repo AND in file scope is simply correct;
//   - only present-in-repo-but-absent-from-file-scope is the new signal, and for
//     that case we KNOW the name is a real symbol placed where it cannot resolve.
//
// So this check is strictly additive on a disjoint case. It cannot make the
// invented-symbol check noisier, and it never fires on a typo.
//
// # Why it abstains so aggressively
//
// Zero false positives is the whole product promise, and file-scope resolution is
// exactly where a linter earns its false alarms. Every construct that could bind
// a name outside our view suppresses the check for the ENTIRE file rather than
// risking one bad flag: a miss is free, a false alarm is the adoption-killer.
// This deliberately means the check catches nothing in files using star imports
// or runtime name injection — an accepted, measured trade.
//
// Python only in v1. Go is package-qualified and compiler-checked (an unresolved
// bare identifier is already a build error), and its cross-file same-package
// resolution would false-positive badly — the same reason DroppedImportRefs
// excludes it. JS lands only after Python proves out in dogfood.

var (
	// rePyStarImport matches `from x import *`, which binds an unknowable set.
	rePyStarImport = regexp.MustCompile(`^\s*from\s+[.\w]+\s+import\s+\*`)
	// rePyGlobalDecl matches `global a, b` / `nonlocal a` — the names are bound
	// elsewhere by declaration, so they must count as resolved.
	rePyGlobalDecl = regexp.MustCompile(`^\s*(?:global|nonlocal)\s+(.+)$`)
)

// pyDynamicBinding lists constructs that can introduce a name at runtime, which no
// static scan can enumerate. Their presence anywhere in the file abstains it.
// Deliberately over-broad (`vars(`/`setattr(` are often innocent) — over-
// abstention costs only recall.
var pyDynamicBinding = []string{
	"globals(", "locals(", "exec(", "eval(", "setattr(", "vars(",
	"__import__", "importlib",
}

// FileScopeViolations returns references in fd's added lines that resolve in
// repoKnown but not within the edited file's own binding surface. wholeFile is the
// file's full pre-edit content (empty disables the check — without it the file's
// imports and definitions are unknowable, so it must stay silent).
//
// fd is taken whole rather than as bare lines so the string-state seed travels
// with it: an edit block beginning inside a pre-existing docstring MUST be masked,
// or its prose is scanned as code and ordinary words read as calls. That is the
// guard's largest historical false-positive class (#145, #178), and a check that
// extracted refs with a nil seed would reintroduce it wholesale.
//
// Python only in v1.
func FileScopeViolations(lang Lang, wholeFile []AddedLine, fd FileDiff, repoKnown map[string]struct{}) []Violation {
	if lang != LangPython {
		return nil
	}
	addedLines := fd.AddedLines
	// No whole-file context: the imports and defs that bind these names are
	// invisible, so every reference would look unresolved. Fail silent, matching
	// the guard's degraded-input posture everywhere else.
	if len(wholeFile) == 0 {
		return nil
	}
	if abstainsFileScope(wholeFile) || abstainsFileScope(addedLines) {
		return nil
	}

	// wholeFile is one contiguous run from line 1, so ExtractDefs' own tracking
	// computes correctly on it unseeded — the opening `{` of any dict literal is
	// already IN wholeFile. addedLines is not: it's just the hunk, so a key added
	// to an EXISTING dict (opener in unchanged context, outside the hunk) needs
	// the same open-string AND brace-depth seeds extractRefs uses below — without
	// BOTH, ExtractDefs misreads the key as a definition (missing braceSeed) or
	// desyncs from extractRefs' masking on a hunk beginning inside a docstring
	// (missing openSeed) and pyFileScope wrongly binds it into scope, suppressing
	// the exact file-scope violation this check exists to catch (code-review
	// findings on PR #290, rounds 2 and 3).
	openSeed := seedFunc(lang, fd)
	braceSeed := braceDepthSeedFunc(lang, fd)
	// bracketSeed/defSigSeed are PyDeclaredNames/PyParamNames/LocallyBoundNames'
	// own seeds (#294) — same rationale as openSeed/braceSeed: a hunk-only
	// addedLines slice needs them or a kwarg/parameter added mid-signature
	// misreads as unbound and this check wrongly flags it as out-of-scope.
	bracketSeed := bracketDepthSeedFunc(lang, fd)
	defSigSeed := defSigDepthSeedFunc(lang, fd)
	scope := pyFileScope(wholeFile, nil, nil, nil, nil)
	// The edit itself binds names too — a def or import introduced by this very
	// hunk must resolve, or every newly-added helper would flag on its first use.
	for name := range pyFileScope(addedLines, openSeed, braceSeed, bracketSeed, defSigSeed) {
		scope[name] = struct{}{}
	}

	var violations []Violation
	// extractRefs already excludes builtins, skips qualified selectors, masks
	// string/comment content, and dedupes by name (first occurrence wins) — so the
	// extraction surface here is IDENTICAL to the additive check's, seed included.
	// The only thing this check changes is which set the name is resolved against.
	for _, ref := range extractRefs(lang, addedLines, openSeed, braceSeed) {
		if _, inRepo := repoKnown[ref.Name]; !inRepo {
			continue // the firewall: invented symbols belong to the additive check
		}
		if _, bound := scope[ref.Name]; bound {
			continue // resolves in this file — correct usage
		}
		violations = append(violations, Violation{
			Line:   ref.LineNo,
			Symbol: ref.Name,
			Lang:   lang,
		})
	}
	return violations
}

// abstainsFileScope reports whether the lines contain any construct that makes the
// file's binding surface unknowable — a star import or runtime name injection.
// Scanned on raw text: a match inside a string or comment only causes an extra
// abstain, which is the safe direction.
func abstainsFileScope(lines []AddedLine) bool {
	for _, l := range lines {
		if rePyStarImport.MatchString(l.Text) {
			return true
		}
		for _, marker := range pyDynamicBinding {
			if strings.Contains(l.Text, marker) {
				return true
			}
		}
	}
	return false
}

// pyFileScope collects every name the given lines bind: imports, definitions,
// assignment targets, loop/with/except targets, parameters, and global/nonlocal
// declarations. It reuses the same primitives the additive check and the
// dropped-import check already rely on, so the binding rules stay consistent
// across every guard check rather than drifting per-feature. openSeed/
// braceDepthSeed are threaded straight to extractDefsSeeded — nil,nil is correct
// for a self-contained, contiguous-from-line-1 slice (wholeFile); a hunk slice
// (addedLines) needs the caller's real seeds (BOTH — see extractDefsSeeded's own
// doc for why openSeed alone missing desyncs it from extractRefs), or a dict key
// added to an existing literal misreads as a definition (see FileScopeViolations).
// bracketDepthSeed/defSigDepthSeed are PyDeclaredNames/PyParamNames/
// LocallyBoundNames' own seeds (#294), same nil-for-wholeFile /
// caller's-real-seed-for-addedLines rule.
func pyFileScope(lines []AddedLine, openSeed func(lineNo int) string, braceDepthSeed func(lineNo int) int, bracketDepthSeed func(lineNo int) int, defSigDepthSeed func(lineNo int) int) map[string]struct{} {
	scope := make(map[string]struct{})
	for _, n := range ExtractImports(LangPython, lines) {
		scope[n] = struct{}{}
	}
	for _, n := range extractDefsSeeded(LangPython, lines, openSeed, braceDepthSeed) {
		scope[n] = struct{}{}
	}
	for n := range LocallyBoundNames(LangPython, lines, defSigDepthSeed) {
		scope[n] = struct{}{}
	}
	for _, n := range PyDeclaredNames(lines, bracketDepthSeed) {
		scope[n] = struct{}{}
	}
	for _, n := range PyParamNames(lines, defSigDepthSeed) {
		scope[n] = struct{}{}
	}
	for _, l := range lines {
		m := rePyGlobalDecl.FindStringSubmatch(l.Text)
		if m == nil {
			continue
		}
		for _, part := range strings.Split(m[1], ",") {
			if name := strings.TrimSpace(part); reIdent.MatchString(name) {
				scope[name] = struct{}{}
			}
		}
	}
	return scope
}
