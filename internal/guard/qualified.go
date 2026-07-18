package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Qualified-reference validation for Go: catch a hallucinated call to a symbol
// on a SAME-REPO internal package (`internalpkg.NoSuchFunc()`), which the bare-
// call extractor deliberately skips (it drops every `.`-qualified selector).
//
// The guard's whole pitch is zero false positives, and `pkg.Sym()` is
// syntactically identical to a value-method call `localVar.Method()`. So this
// only ever flags under a stack of conservative gates, each of which abstains
// (never flags) when it cannot be certain:
//
//   1. `q` must be a package alias imported in THIS file, whose import path is
//      under the repo's own go.mod module path (same-repo internal package).
//      External deps and stdlib are not indexed, so they are never flagged.
//   2. `q` must appear in the whole file ONLY as a `q.` selector — never as a
//      bare identifier. A local variable/param/field named `q` (a shadow of the
//      import) would appear bare somewhere (`q :=`, `func f(q T)`, `return q`),
//      so the only-selector rule provably rules out shadowing without a scope
//      parser. This is stricter than LocallyBoundNames (which misses Go `var x T`,
//      params, and struct fields) precisely because a miss there would be a FP.
//   3. `Sym` must be exported (capitalized) — a cross-package call targets an
//      exported symbol; a lowercase selector is a struct-field/method access we
//      cannot resolve, so abstain.
//   4. `Sym` must be absent from the repo's indexed symbols. The known set is
//      flat (no per-package attribution), so this catches "Sym exists nowhere in
//      the repo", not "Sym is not in package q" (the latter stays a by-design
//      false negative — precision over recall).
//
// Missing/unreadable module path, no imports, or an oversized file all yield no
// violations (fail-open, matching the rest of the guard's degraded-input posture).

// reGoImportSpec matches one import spec's optional alias and quoted path:
//
//	"fmt"                      → alias "",   path "fmt"
//	snap "x/y/snapshot"        → alias snap, path "x/y/snapshot"
//	. "x"  / _ "x"             → alias "."/"_" (dot/blank imports; skipped)
var reGoImportSpec = regexp.MustCompile(`^\s*(?:([A-Za-z_.][\w.]*)\s+)?"([^"]+)"\s*$`)

// reGoQualifiedCall matches a `q.Sym(` call in a scanned (literal-masked) line,
// capturing the qualifier and the selector. The left side is guarded by the
// caller (must not be mid-identifier) the same way extractRefs guards bare calls.
var reGoQualifiedCall = regexp.MustCompile(`([A-Za-z_]\w*)\.([A-Za-z_]\w*)\s*\(`)

// reGoBareIdent matches every identifier occurrence, used to test the
// only-selector shadow gate.
var reGoBareIdent = regexp.MustCompile(`[A-Za-z_]\w*`)

// GoModulePath walks up from startDir to find go.mod and returns its module path
// (the argument of the `module` directive), or "" if none is found within a
// bounded number of parents or the file can't be read. "" disables qualified
// validation (abstain), so a repo without a readable go.mod is never flagged.
func GoModulePath(startDir string) string {
	dir := startDir
	for i := 0; i < 64; i++ { // bounded walk; 64 parents is far past any real tree
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			return parseGoModule(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached filesystem root
			return ""
		}
		dir = parent
	}
	return ""
}

// parseGoModule extracts the module path from go.mod contents (the token after
// the first `module` directive), or "" if absent.
func parseGoModule(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok {
			path := strings.TrimSpace(rest)
			// Strip a surrounding quote (rare `module "x"`) and any trailing comment.
			path = strings.Trim(path, "\"")
			if i := strings.Index(path, "//"); i >= 0 {
				path = strings.TrimSpace(path[:i])
			}
			if path != "" {
				return path
			}
		}
	}
	return ""
}

// sameRepoGoAliases parses the file's Go imports (single-line and block form) and
// returns the set of package aliases whose import path is under modulePath — i.e.
// the repo's own internal packages. Dot and blank imports are excluded (they bind
// no usable qualifier). The alias is the explicit rename when present, else the
// last path segment (Go's default; a package whose name differs from its dir is a
// rare case that at worst causes an abstain, never a FP). String/comment content
// is masked so an import-shaped token inside a literal is not parsed.
func sameRepoGoAliases(wholeFile []AddedLine, modulePath string) map[string]struct{} {
	aliases := make(map[string]struct{})
	if modulePath == "" {
		return aliases
	}
	inBlock := false
	open := ""
	for _, l := range wholeFile {
		// The import PATH is itself a double-quoted string, so parse the RAW line —
		// masking would blank the path. Literal-state tracking is still threaded so a
		// line that STARTS inside a multiline (backtick) string is skipped, and an
		// import-shaped token there is not misread.
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
				addGoImportAlias(aliases, rest, modulePath)
			}
			continue
		}
		if trimmed == ")" {
			inBlock = false
			continue
		}
		addGoImportAlias(aliases, trimmed, modulePath)
	}
	return aliases
}

// addGoImportAlias parses a single import spec (`snap "x/y"` or `"x/y"`) and, if
// its path is under modulePath, records the usable qualifier alias.
func addGoImportAlias(aliases map[string]struct{}, spec, modulePath string) {
	m := reGoImportSpec.FindStringSubmatch(spec)
	if m == nil {
		return
	}
	alias, path := m[1], m[2]
	if path != modulePath && !strings.HasPrefix(path, modulePath+"/") {
		return // external package or stdlib — never flagged
	}
	if alias == "." || alias == "_" {
		return // dot/blank import binds no qualifier
	}
	if alias == "" {
		// Default alias is the last path segment (Go's package-name default).
		alias = path[strings.LastIndexByte(path, '/')+1:]
	}
	if alias != "" {
		aliases[alias] = struct{}{}
	}
}

// onlySelectorQualifiers keeps, from candidates, those aliases that appear in the
// whole (literal-masked) file EXCLUSIVELY as a `q.` selector — never as a bare
// identifier in any other position. Any bare occurrence means a local
// variable/param/field could be shadowing the import, so the alias is dropped
// (abstain) to preserve the zero-FP invariant. This is the shadow gate.
func onlySelectorQualifiers(wholeFile []AddedLine, candidates map[string]struct{}) map[string]struct{} {
	if len(candidates) == 0 {
		return candidates
	}
	disqualified := make(map[string]struct{})
	open := ""
	for _, l := range wholeFile {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, loc := range reGoBareIdent.FindAllStringIndex(scan, -1) {
			name := scan[loc[0]:loc[1]]
			if _, ok := candidates[name]; !ok {
				continue
			}
			// A qualifier use is `name.` — the byte right after the identifier is '.'.
			// Anything else (space, comma, '(', ')', ':=', newline/EOL) is a bare use
			// that could be a shadowing binding or value use → disqualify.
			after := loc[1]
			if after < len(scan) && scan[after] == '.' {
				// Also require it is not itself the tail of a selector (`a.name.x`),
				// where `name` is a field, not the package. Guard the left side.
				if loc[0] > 0 && (scan[loc[0]-1] == '.' || isWordByte(scan[loc[0]-1])) {
					disqualified[name] = struct{}{}
				}
				continue
			}
			disqualified[name] = struct{}{}
		}
	}
	kept := make(map[string]struct{})
	for a := range candidates {
		if _, bad := disqualified[a]; !bad {
			kept[a] = struct{}{}
		}
	}
	return kept
}

// GoQualifiedViolations returns violations for hallucinated same-repo
// internal-package calls in addedLines. wholeFile is the whole current file
// (used for import parsing and the shadow gate); known is the flat repo symbol
// set; modulePath is the repo's go.mod module path ("" → no violations).
// See the file header for the full gate stack. Go only.
func GoQualifiedViolations(wholeFile, addedLines []AddedLine, known map[string]struct{}, modulePath string) []Violation {
	// Import parsing AND the shadow gate run over the pre-edit file PLUS the added
	// lines: a same-repo import or a shadowing binding introduced IN this same edit
	// must be seen. Concatenating (not just wholeFile) is what makes an in-edit
	// `snap := ...` disqualify `snap` — without it the pre-edit file alone wouldn't
	// show the shadow and `snap.Method()` could false-positive.
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	aliases := sameRepoGoAliases(ctx, modulePath)
	if len(aliases) == 0 {
		return nil
	}
	aliases = onlySelectorQualifiers(ctx, aliases)
	if len(aliases) == 0 {
		return nil
	}

	var violations []Violation
	seen := make(map[string]struct{})
	open := ""
	prevNo := 0
	for i, l := range addedLines {
		if i == 0 || l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(LangGo, l.Text) {
			continue
		}
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, idx := range reGoQualifiedCall.FindAllStringSubmatchIndex(scan, -1) {
			qStart, qEnd := idx[2], idx[3]
			symStart, symEnd := idx[4], idx[5]
			q := scan[qStart:qEnd]
			sym := scan[symStart:symEnd]
			// Left-guard the qualifier the same way extractRefs guards bare calls:
			// a preceding '.' or word byte means this is a deeper selector
			// (`a.q.Sym`), not a top-level package call — abstain.
			if qStart > 0 {
				if prev := scan[qStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			if _, ok := aliases[q]; !ok {
				continue
			}
			if sym[0] < 'A' || sym[0] > 'Z' {
				continue // unexported selector — field/method access, abstain
			}
			if _, ok := known[sym]; ok {
				continue // exists somewhere in the repo — not a hallucination
			}
			key := q + "." + sym
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			suggestion, _ := Suggest(sym, known)
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
