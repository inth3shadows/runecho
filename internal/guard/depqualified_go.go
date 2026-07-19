package guard

import (
	"strings"

	"github.com/inth3shadows/runecho/internal/depindex"
)

// External-dependency qualified-call validation for Go: catch a call to a symbol
// that does not exist on an imported third-party or standard-library package
// (`http.Gett(...)` where net/http exports `Get`).
//
// This is the exact complement of qualified.go. That file validates aliases whose
// import path is UNDER the repo's module path against the repo's own IR; this one
// validates the aliases it deliberately skipped, against the dependency's real
// source. Together they cover every package qualifier in a Go file, and each side
// abstains on what the other owns.
//
// Go's gate stack is shorter than Python's because the language removes two whole
// risks: there is no monkey-patching (a package's symbol set is fixed at compile
// time), and unexported selectors are unreachable across packages, so the
// exported-identifier rule is a real guarantee rather than a convention.
//
//  1. `a` is bound by an import in THIS file whose path is NOT under the repo's
//     module path — external or stdlib. Dot and blank imports bind no usable
//     qualifier and are excluded.
//  2. `a` appears in the whole file ONLY as an `a.` selector, never bare. This is
//     the same shadow gate the same-repo path uses (onlySelectorQualifiers): a
//     local variable named `a` would show up bare somewhere, and `a.Method()`
//     would then be a value method call rather than a package call.
//  3. `Sym` is exported. An unexported selector on a package qualifier cannot
//     compile, so if we see one our qualifier classification is wrong — abstain.
//  4. depindex resolves the import path to Resolved. Unknown (not in go.mod, not
//     in the module cache, behind a `replace`, under a go.work overlay, or too
//     large for the scan budget) and Partial both abstain.
//  5. `Sym` is absent from that package's exported names.

// externalGoAliases parses the file's imports and returns alias → import path for
// every package that is NOT part of this repo. It is the inverse of
// sameRepoGoAliases, and shares its parsing rules: block and single-line forms,
// the default alias is the last path segment, and dot/blank imports are skipped.
func externalGoAliases(wholeFile []AddedLine, modulePath string) map[string]string {
	aliases := map[string]string{}
	inBlock := false
	open := ""
	for _, l := range wholeFile {
		// The import path is itself a quoted string, so the RAW line is parsed;
		// only the multiline-string state is threaded, to skip lines that begin
		// inside one. Identical to sameRepoGoAliases.
		lineStartOpen := open
		_, open = stripLiteralsStateful(LangGo, l.Text, open)
		if lineStartOpen != "" {
			continue
		}
		trimmed := strings.TrimSpace(l.Text)
		if !inBlock {
			if trimmed == "import (" || trimmed == "import(" {
				inBlock = true
				continue
			}
			if rest, ok := strings.CutPrefix(trimmed, "import "); ok {
				addExternalGoAlias(aliases, rest, modulePath)
			}
			continue
		}
		if trimmed == ")" {
			inBlock = false
			continue
		}
		addExternalGoAlias(aliases, trimmed, modulePath)
	}
	return aliases
}

// addExternalGoAlias records one import spec's alias when its path lies outside
// the repo's own module.
func addExternalGoAlias(aliases map[string]string, spec, modulePath string) {
	m := reGoImportSpec.FindStringSubmatch(spec)
	if m == nil {
		return
	}
	alias, path := m[1], m[2]
	if modulePath != "" && (path == modulePath || strings.HasPrefix(path, modulePath+"/")) {
		return // same-repo: qualified.go owns this one
	}
	if alias == "." || alias == "_" {
		return // binds no usable qualifier
	}
	if alias == "" {
		// Go's default alias is the last path segment. A package whose declared
		// name differs from its directory (rare, e.g. gopkg.in/yaml.v3 → yaml)
		// yields a qualifier that never matches any call, so the check simply
		// never fires for it — an abstain, not a false positive.
		alias = path[strings.LastIndexByte(path, '/')+1:]
	}
	if alias != "" {
		aliases[alias] = path
	}
}

// GoDepQualifiedViolations returns violations for calls into imported external or
// standard-library packages whose named symbol does not exist. wholeFile is the
// current file (import parsing and the shadow gate); modulePath is the repo's own
// module path, used only to EXCLUDE same-repo imports; idx resolves an import
// path to its exported names. A nil idx yields no violations. Go only.
func GoDepQualifiedViolations(wholeFile, addedLines []AddedLine, modulePath string, idx depindex.Index) []Violation {
	if idx == nil {
		return nil
	}
	// Gates run over the pre-edit file PLUS the added lines, so an import or a
	// shadowing binding introduced by THIS edit is visible — same reasoning as
	// GoQualifiedViolations.
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	aliases := externalGoAliases(ctx, modulePath)
	if len(aliases) == 0 {
		return nil
	}
	// Reuse the same-repo shadow gate verbatim: it takes a candidate set and
	// keeps only aliases never used as a bare identifier.
	candidates := make(map[string]struct{}, len(aliases))
	for a := range aliases {
		candidates[a] = struct{}{}
	}
	kept := onlySelectorQualifiers(ctx, candidates)
	if len(kept) == 0 {
		return nil
	}

	var violations []Violation
	seen := map[string]struct{}{}
	open := ""
	prevNo := 0
	for i, l := range addedLines {
		if i == 0 || l.LineNo != prevNo+1 {
			open = "" // non-contiguous hunk: string state cannot carry over
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(LangGo, l.Text) {
			continue
		}
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, idxs := range reGoQualifiedCall.FindAllStringSubmatchIndex(scan, -1) {
			qStart, qEnd := idxs[2], idxs[3]
			q := scan[qStart:qEnd]
			sym := scan[idxs[4]:idxs[5]]
			// Left-guard: a preceding '.' or word byte means a deeper selector
			// (`a.q.Sym`), not a package-level call.
			if qStart > 0 {
				if prev := scan[qStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			if _, ok := kept[q]; !ok {
				continue
			}
			importPath, ok := aliases[q]
			if !ok {
				continue
			}
			if sym[0] < 'A' || sym[0] > 'Z' {
				continue // unexported: unreachable cross-package, so we misread it
			}
			pkg := idx.Lookup(importPath)
			if pkg.Res != depindex.Resolved {
				continue
			}
			if pkg.Has(sym) {
				continue
			}
			key := q + "." + sym
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			suggestion, _ := Suggest(sym, pkg.Exports)
			violations = append(violations, Violation{
				Line:       l.LineNo,
				Symbol:     q + "." + sym,
				Lang:       LangGo,
				Suggestion: suggestion,
			})
		}
	}
	return violations
}
