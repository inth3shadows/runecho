package guard

// FoldInFileDefs folds every definition the def-extractor finds in fileLines
// (top-level AND indented/nested defs and local arrow consts, since the def
// regexes are `^\s*`-anchored) into the known symbol set. This is the P2
// residual-killer: it makes a hunk-scoped Edit aware of the rest of its own file
// without re-implementing a scope-tracking parser. It mutates symbols in place (a
// fresh per-call map). fileLines is nil for a missing/oversized file (see
// readFileLines in cmd/runecho-guard), in which case this adds nothing.
//
// This lives in the guard package rather than beside the hook that calls it
// because it is part of the definition of "what the guard knew" — any harness
// that measures the guard's false-positive or false-negative rate has to build
// the same known set the hook builds, and a second copy of this fold would drift
// from it silently, turning those rates into fiction. One implementation, both
// callers: cmd/runecho-guard's hook path and the compiler-oracle differential in
// resolve_differential_test.go.
func FoldInFileDefs(symbols map[string]struct{}, fileLines []AddedLine, lang Lang) {
	for _, def := range ExtractDefs(lang, fileLines) {
		symbols[def] = struct{}{}
	}
	// Imported names (`from pathlib import Path`, `import {readFileSync} …`) are
	// real callables bound elsewhere in the file; fold them in too.
	for _, imp := range ExtractImports(lang, fileLines) {
		symbols[imp] = struct{}{}
	}
	// JS binds callables by forms the def/import extractors miss — destructuring
	// (`const [x, setX] = useState()`), object destructure, and computed-assign
	// (`const fn = handlers[k]`). Fold the whole-file declarator binding targets in
	// so a bare call to one is not a false hallucination — crucially the binding
	// line (e.g. a useState destructure) usually sits OUTSIDE the edited hunk, which
	// the hunk-scoped diff never sees. JSDeclaredNames (not the over-inclusive
	// LocallyBoundNames) keeps a param type annotation from leaking a type name and
	// masking a real undefined reference. JS-specific: Go and Python have their
	// own extractors below, each shaped by that language's binding forms.
	if lang == LangJS {
		for _, name := range JSDeclaredNames(fileLines) {
			symbols[name] = struct{}{}
		}
	}
	// Go: locals, parameters, named returns, range and type-switch bindings. A
	// bare `foo()` in Go resolves to any of these without a package-level
	// declaration existing, so folding them is what makes checking unexported Go
	// references safe at all — see godeclared.go.
	if lang == LangGo {
		for _, name := range GoDeclaredNames(fileLines) {
			symbols[name] = struct{}{}
		}
	}
	// Python sibling: a local callable bound by assignment (`handler =
	// HANDLERS[key]; handler(payload)`) is not a hallucination. Fold whole-file
	// assignment targets so a binding on a line outside the edited hunk resolves.
	if lang == LangPython {
		for _, name := range PyDeclaredNames(fileLines) {
			symbols[name] = struct{}{}
		}
		// Parameters used as callables are bound by their signature (a
		// `Callable`-typed param, a lambda arg). Fold the whole file's parameter
		// names — names only, never their type annotations. This was the last
		// surviving Python false-positive class in the live decision log.
		for _, name := range PyParamNames(fileLines) {
			symbols[name] = struct{}{}
		}
	}
}
