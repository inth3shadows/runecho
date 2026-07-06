package guard

import (
	"regexp"
	"sort"
)

// DroppedImport names an imported symbol whose import binding an edit removes
// while the new text still uses it unqualified and does not re-define it. This is
// the dropped-import bug seen in real transcripts (an agent rewrites a file,
// drops `from ulid import ULID`, but still calls `ULID()`), which fails at
// runtime (NameError / "X is not defined").
//
// It is the file-scoped mirror of the E1 dangling-refs check (removed *definition*
// still referenced cross-file): here it is a removed *import* still referenced
// in-file. It complements the additive hallucination check rather than
// duplicating it: at edit time the on-disk file still carries the old import, so
// the additive check resolves the name and stays silent — only by diffing the
// edit's old vs new text does the removal become visible.
type DroppedImport struct {
	Name   string
	LineNo int // first surviving-use line within newText (1-based)
}

// DroppedImportRefs returns imported names that are bound in oldText, no longer
// bound in newText, still used unqualified somewhere in newText, and not
// re-provided by a local definition in newText. Deterministic (sorted by name).
//
// Go is excluded: Go imports are used package-qualified (pkg.Foo), so a dropped
// Go import surfaces as a qualified reference, which the guard handles elsewhere
// (and ExtractImports already excludes Go for the same reason).
func DroppedImportRefs(lang Lang, oldText, newText string) []DroppedImport {
	if lang != LangPython && lang != LangJS {
		return nil
	}
	oldImps := nameSet(ExtractImports(lang, TextToAddedLines(oldText)))
	if len(oldImps) == 0 {
		return nil // nothing was imported in the removed text; no work
	}
	newImps := nameSet(ExtractImports(lang, TextToAddedLines(newText)))

	// bound = every name the new text re-provides locally: top-level definitions
	// PLUS any binding form (assignment LHS, for/comprehension target, with/except
	// `as`, walrus, function params, JS const/let/var + destructuring + catch). A
	// dropped import whose name is rebound here is NOT a bug. This is the
	// false-positive guard, and it is deliberately OVER-inclusive: an over-
	// suppressed real drop is a recoverable miss (the additive check or the runtime
	// still catches it), whereas a false alarm trains users to ignore the guard —
	// the adoption-killer. Precision over recall.
	bound := locallyBoundNames(lang, newText)
	for _, d := range ExtractDefs(lang, TextToAddedLines(newText)) {
		bound[d] = struct{}{}
	}

	// Collect the imports that were actually dropped (removed and not rebound)
	// before touching the new text. Most edits drop nothing, so this keeps the
	// common case at zero identifier scans — the per-name check used to be lazy,
	// and we preserve that fast path rather than eagerly indexing every edit.
	var dropped []string
	for name := range oldImps {
		if _, still := newImps[name]; still {
			continue // the import survived in the new text
		}
		if _, b := bound[name]; b {
			continue // the name is now provided by a local definition or binding
		}
		dropped = append(dropped, name)
	}
	if len(dropped) == 0 {
		return nil
	}

	// Index every unqualified identifier use in one pass so each dropped name is an
	// O(1) lookup below. Rescanning the whole new text per dropped import was
	// O(distinct-imports × text-length) — quadratic on a crafted diff.
	uses := firstUnqualifiedUseLines(lang, TextToAddedLines(newText))
	var out []DroppedImport
	for _, name := range dropped {
		if ln := uses[name]; ln > 0 {
			out = append(out, DroppedImport{Name: name, LineNo: ln})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func nameSet(names []string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// firstUnqualifiedUseLines returns, for every identifier used as an unqualified
// (non-attribute) whole word anywhere in lines, the 1-based number of its FIRST
// such use. Import lines and string/comment content are ignored (the latter
// blanked by the same stateful literal stripper the reference extractor uses);
// the import-line skip keeps a binding line from counting as a "use". This is the
// single-pass form: callers resolve many candidate names in O(1) each instead of
// rescanning the text per name, which was quadratic in distinct-imports × length.
//
// A maximal run of identifier bytes already satisfies the word-boundary
// conditions the old per-name check enforced, so the only extra test is that the
// run is not preceded by '.' (which would make it a qualified attribute, e.g.
// `x.Name`) — semantics identical to the former containsUnqualifiedWord.
func firstUnqualifiedUseLines(lang Lang, lines []AddedLine) map[string]int {
	uses := make(map[string]int)
	open := ""
	prevNo := 0
	for i, l := range lines {
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		scan, newOpen := stripLiteralsStateful(lang, l.Text, open)
		open = newOpen
		if isImportLine(lang, l.Text) {
			continue
		}
		for j := 0; j < len(scan); {
			if !isWordByte(scan[j]) {
				j++
				continue
			}
			start := j
			for j < len(scan) && isWordByte(scan[j]) {
				j++
			}
			if start > 0 && scan[start-1] == '.' {
				continue // qualified attribute, e.g. x.Name
			}
			if id := scan[start:j]; uses[id] == 0 {
				uses[id] = l.LineNo
			}
		}
	}
	return uses
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Binding-form patterns for locallyBoundNames. Each captures the binding TARGET
// region; identifiers within are extracted with reIdentAll. Definition-position
// only — none match call-argument parens, so a dropped import passed as an
// argument is still flagged, not suppressed.
var (
	reIdentAll      = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	rePyForTarget   = regexp.MustCompile(`^\s*for\s+(.+?)\s+in\b`)
	reAsBind        = regexp.MustCompile(`\bas\s+([A-Za-z_]\w*)`) // with/except ... as x
	reWalrus        = regexp.MustCompile(`([A-Za-z_]\w*)\s*:=`)
	rePyDefParams   = regexp.MustCompile(`^\s*(?:async\s+)?def\s+\w+\s*\(([^)]*)\)`)
	reJSDeclList    = regexp.MustCompile(`\b(?:const|let|var)\s+(.+)$`)
	reJSFnParams    = regexp.MustCompile(`function\b[^(]*\(([^)]*)\)`)
	reJSArrowParams = regexp.MustCompile(`\(([^)]*)\)\s*=>`)
	reJSForDecl     = regexp.MustCompile(`\bfor\s*\(\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)`)
	reJSCatch       = regexp.MustCompile(`\bcatch\s*\(\s*([A-Za-z_$][\w$]*)`)
)

// locallyBoundNames collects names the new text binds locally by any common
// construct, so a dropped import that is actually rebound (replaced by a local
// assignment, loop variable, with-as, parameter, destructure, …) does not
// false-positive. Over-inclusive by design (see DroppedImportRefs).
func locallyBoundNames(lang Lang, text string) map[string]struct{} {
	m := make(map[string]struct{})
	add := func(s string) {
		for _, id := range reIdentAll.FindAllString(s, -1) {
			m[id] = struct{}{}
		}
	}
	// Thread multi-line string state across lines (like firstUnqualifiedUseLines
	// and ExtractRefs). The stateless stripLiterals scanned each line in isolation,
	// so a `x = Foo()` example inside a multi-line docstring/template read as a real
	// binding — spuriously adding names to the bound set and suppressing genuine
	// dropped-import detections (a false negative). open resets on a line-number gap.
	open := ""
	prevNo := 0
	for i, l := range TextToAddedLines(text) {
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		var s string
		s, open = stripLiteralsStateful(lang, l.Text, open)
		if lhs := assignLHS(s); lhs != "" {
			add(lhs)
		}
		switch lang {
		case LangPython:
			if mm := rePyForTarget.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			for _, mm := range reAsBind.FindAllStringSubmatch(s, -1) {
				m[mm[1]] = struct{}{}
			}
			for _, mm := range reWalrus.FindAllStringSubmatch(s, -1) {
				m[mm[1]] = struct{}{}
			}
			if mm := rePyDefParams.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
		case LangJS:
			// Capture EVERY declarator of a const/let/var statement, not just the
			// first — `const a = f(), Ulid = () => …` rebinds Ulid as the second
			// declarator, which a first-`=`-only match would miss (false positive).
			if mm := reJSDeclList.FindStringSubmatch(s); mm != nil {
				for _, decl := range splitTopLevelCommas(mm[1]) {
					if lhs := assignLHS(decl); lhs != "" {
						add(lhs)
					} else {
						add(decl) // declarator with no initializer (`let a, b;`)
					}
				}
			}
			if mm := reJSFnParams.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			if mm := reJSArrowParams.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			if mm := reJSForDecl.FindStringSubmatch(s); mm != nil {
				m[mm[1]] = struct{}{}
			}
			if mm := reJSCatch.FindStringSubmatch(s); mm != nil {
				m[mm[1]] = struct{}{}
			}
		}
	}
	return m
}

// assignLHS returns the substring left of the first plain assignment '=' on a
// line, or "" if there is none. It excludes comparison and arrow operators
// (==, !=, <=, >=, =>) and the walrus ':=' so only true assignment targets are
// captured.
func assignLHS(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '=' {
			continue
		}
		if i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '>') {
			i++ // ==, =>
			continue
		}
		if i > 0 {
			switch s[i-1] {
			case '=', '!', '<', '>', ':':
				continue // second char of ==, !=, <=, >=, :=
			}
		}
		return s[:i]
	}
	return ""
}

// splitTopLevelCommas splits s on commas that are not nested inside (), [], or
// {}, so a multi-declarator statement (`a = f(x, y), b = g()`) splits into its
// declarators without breaking on commas inside call args, arrays, or objects.
// Operates on literal-stripped text, so no string/comment commas remain.
func splitTopLevelCommas(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}
