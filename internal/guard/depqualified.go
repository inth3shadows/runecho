package guard

import (
	"regexp"
	"strings"

	"github.com/inth3shadows/runecho/internal/depindex"
)

// External-dependency qualified-call validation for Python: catch a hallucinated
// call to a symbol that does not exist on an installed third-party module
// (`pl.pearsonr(...)` where `import polars as pl` and polars exposes `corr`).
//
// This is the external mirror of qualified.go, which handles the SAME-REPO case.
// The two share a philosophy — flag only under a stack of gates that each abstain
// when uncertain — but the external case has a failure mode the internal one does
// not: the symbol table comes from outside the repo and may be incomplete. That
// risk is carried by depindex's tri-state Resolution; here we simply refuse to
// act on anything but depindex.Resolved.
//
// The gate stack, in order. Any gate that cannot be certain drops the alias or the
// reference entirely — there is no "probably a hallucination" path:
//
//  1. `a` must be bound by a TOP-LEVEL plain `import M` / `import M as a`. An
//     indented import is conditional (inside try/except ImportError, a function,
//     an `if TYPE_CHECKING`) and may bind a stub or nothing at all, so it is
//     skipped. `from M import x` binds `x`, not a qualifier, and is irrelevant here.
//  2. `a` must appear in the whole file ONLY as an `a.` selector — never bare.
//     A bare occurrence means a local variable could shadow the import, and
//     `a.Method()` would then be an instance call we cannot resolve. Same
//     shadow-gate reasoning as the Go path, minus Go's import-block handling.
//  3. `a` must never be the target of attribute assignment (`a.foo = ...`)
//     anywhere in the file. Monkey-patching adds attributes that no static index
//     can see; one such assignment disqualifies the alias entirely, not just the
//     patched name, because a patched module is not one we understand.
//  4. depindex must return Resolved for the module. Unknown (not installed, no
//     venv, unreadable) and Partial (lazy __getattr__, star-imports, computed
//     __all__) both abstain.
//  5. The symbol must be absent from the resolved export set — which unions
//     __all__, every top-level binding, and every submodule name, so it errs
//     wide on purpose.
//
// Python has no capitalization convention for exported names, so unlike the Go
// path there is no "must be exported" gate; the wide export set does that work
// instead.

// rePyPlainImport matches a top-level `import M` or `import M as a` statement,
// capturing the dotted module path and the optional alias. Multi-module forms
// (`import a, b`) are handled by the caller splitting on commas.
var rePyPlainImport = regexp.MustCompile(`^import\s+(.+)$`)

// rePyQualifiedCall matches `a.Sym(` in a literal-masked line.
var rePyQualifiedCall = regexp.MustCompile(`([A-Za-z_]\w*)\.([A-Za-z_]\w*)\s*\(`)

// rePyBareIdent matches every identifier occurrence, for the shadow gate.
var rePyBareIdent = regexp.MustCompile(`[A-Za-z_]\w*`)

// rePyAttrAssign matches an attribute assignment `a.name =` (but not `==`),
// used by the monkey-patch gate.
var rePyAttrAssign = regexp.MustCompile(`([A-Za-z_]\w*)\.[A-Za-z_]\w*\s*(?::[^=]+)?=[^=]`)

// pyModuleAliases parses the file's top-level plain imports and returns
// alias → dotted module path. Only unindented `import ...` lines count (gate 1);
// `from ... import ...` binds names, not qualifiers, and is ignored.
func pyModuleAliases(wholeFile []AddedLine) map[string]string {
	aliases := map[string]string{}
	open := ""
	for _, l := range wholeFile {
		lineStartOpen := open
		_, open = stripLiteralsStateful(LangPython, l.Text, open)
		if lineStartOpen != "" {
			continue // inside a multi-line string: not code
		}
		// Indentation is the conditional-import gate: an import nested in a
		// try/if/def may bind a fallback stub whose attributes differ from the
		// real module's, so it never qualifies.
		if l.Text == "" || l.Text[0] == ' ' || l.Text[0] == '\t' {
			continue
		}
		m := rePyPlainImport.FindStringSubmatch(strings.TrimSpace(stripPyLineComment(l.Text)))
		if m == nil {
			continue
		}
		for _, part := range strings.Split(m[1], ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			fields := strings.Fields(part)
			switch {
			case len(fields) == 3 && fields[1] == "as":
				aliases[fields[2]] = fields[0]
			case len(fields) == 1 && !strings.Contains(fields[0], "."):
				// `import polars` binds `polars`. A dotted `import a.b` binds the
				// root `a`, whose attribute set we would have to resolve through
				// the submodule — skipped, since gate 5's submodule fold already
				// makes `a.b` unflag­gable and anything deeper is out of scope.
				aliases[fields[0]] = fields[0]
			}
		}
	}
	return aliases
}

// pyOnlySelectorAliases keeps the aliases that appear in the whole file
// exclusively as an `a.` selector (gate 2). Import statements are skipped, since
// the alias there is a binding rather than a bare use.
func pyOnlySelectorAliases(wholeFile []AddedLine, candidates map[string]string) map[string]string {
	if len(candidates) == 0 {
		return candidates
	}
	disqualified := map[string]struct{}{}
	open := ""
	for _, l := range wholeFile {
		lineStartOpen := open
		scan, newOpen := stripLiteralsStateful(LangPython, l.Text, open)
		open = newOpen
		if lineStartOpen == "" {
			if trimmed := strings.TrimSpace(l.Text); strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
				continue
			}
		}
		for _, loc := range rePyBareIdent.FindAllStringIndex(scan, -1) {
			name := scan[loc[0]:loc[1]]
			if _, ok := candidates[name]; !ok {
				continue
			}
			after := loc[1]
			if after < len(scan) && scan[after] == '.' {
				// A selector use — but only if this identifier is not itself the
				// tail of a longer selector (`obj.pl.x`), where it is an attribute.
				if loc[0] > 0 && (scan[loc[0]-1] == '.' || isWordByte(scan[loc[0]-1])) {
					disqualified[name] = struct{}{}
				}
				continue
			}
			disqualified[name] = struct{}{}
		}
	}
	kept := map[string]string{}
	for a, mod := range candidates {
		if _, bad := disqualified[a]; !bad {
			kept[a] = mod
		}
	}
	return kept
}

// pyDropPatchedAliases removes aliases that are ever the target of an attribute
// assignment (gate 3). A monkey-patched module has attributes no static index can
// know about, so the whole alias is abandoned rather than just the patched name.
func pyDropPatchedAliases(wholeFile []AddedLine, candidates map[string]string) map[string]string {
	if len(candidates) == 0 {
		return candidates
	}
	open := ""
	for _, l := range wholeFile {
		scan, newOpen := stripLiteralsStateful(LangPython, l.Text, open)
		open = newOpen
		for _, m := range rePyAttrAssign.FindAllStringSubmatch(scan, -1) {
			delete(candidates, m[1])
		}
	}
	return candidates
}

// PyDepQualifiedViolations returns violations for hallucinated calls into
// installed third-party modules in addedLines. wholeFile is the current file
// (used for import parsing and the shadow/patch gates); idx resolves a module to
// its export set. A nil idx yields no violations. Python only.
func PyDepQualifiedViolations(wholeFile, addedLines []AddedLine, idx depindex.Index) []Violation {
	if idx == nil {
		return nil
	}
	// Gates run over the pre-edit file PLUS the added lines so an import or a
	// shadowing binding introduced by THIS edit is seen — same reasoning as
	// GoQualifiedViolations.
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	aliases := pyModuleAliases(ctx)
	if len(aliases) == 0 {
		return nil
	}
	aliases = pyOnlySelectorAliases(ctx, aliases)
	aliases = pyDropPatchedAliases(ctx, aliases)
	if len(aliases) == 0 {
		return nil
	}

	var violations []Violation
	seen := map[string]struct{}{}
	open := ""
	prevNo := 0
	for i, l := range addedLines {
		if i == 0 || l.LineNo != prevNo+1 {
			open = "" // non-contiguous hunk: string state cannot be carried over
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(LangPython, l.Text) {
			continue
		}
		scan, newOpen := stripLiteralsStateful(LangPython, l.Text, open)
		open = newOpen
		for _, idxs := range rePyQualifiedCall.FindAllStringSubmatchIndex(scan, -1) {
			qStart, qEnd := idxs[2], idxs[3]
			sym := scan[idxs[4]:idxs[5]]
			q := scan[qStart:qEnd]
			// Left-guard: a preceding '.' or word byte means this is a deeper
			// selector (`a.q.Sym`), not a module-level call — abstain.
			if qStart > 0 {
				if prev := scan[qStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			module, ok := aliases[q]
			if !ok {
				continue
			}
			// Private selector: abstain. This is the Python analogue of the Go
			// path's exported-identifier gate. Underscore-prefixed module
			// attributes are disproportionately injected at import time by C
			// extensions (pandas' _pandas_parser_CAPI) or bound dynamically, so
			// they are invisible to a source-level index — and nobody calls a
			// dependency's private API often enough to be worth the FP risk.
			if strings.HasPrefix(sym, "_") {
				continue
			}
			pkg := idx.Lookup(module)
			if pkg.Res != depindex.Resolved {
				continue // Unknown or Partial — never a basis for flagging
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
				Lang:       LangPython,
				Suggestion: suggestion,
			})
		}
	}
	return violations
}
