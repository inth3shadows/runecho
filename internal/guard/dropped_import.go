package guard

import (
	"sort"
	"strings"
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
	localDefs := nameSet(ExtractDefs(lang, TextToAddedLines(newText)))

	newLines := TextToAddedLines(newText)
	var out []DroppedImport
	for name := range oldImps {
		if _, still := newImps[name]; still {
			continue // the import survived in the new text
		}
		if _, def := localDefs[name]; def {
			continue // the name is now provided by a local definition
		}
		if ln := firstUnqualifiedUse(lang, newLines, name); ln > 0 {
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

// firstUnqualifiedUse returns the 1-based line number of the first use of name as
// an unqualified identifier in lines, ignoring import lines and string/comment
// content (which are blanked by the same stateful literal stripper the reference
// extractor uses). Returns 0 if name is not used. The import-line skip is what
// keeps the binding line itself from counting as a "use".
func firstUnqualifiedUse(lang Lang, lines []AddedLine, name string) int {
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
		if containsUnqualifiedWord(scan, name) {
			return l.LineNo
		}
	}
	return 0
}

// containsUnqualifiedWord reports whether name appears in s as a whole identifier
// not preceded by '.' (which would make it a qualified attribute, e.g. `x.Name`).
func containsUnqualifiedWord(s, name string) bool {
	for from := 0; from < len(s); {
		rel := strings.Index(s[from:], name)
		if rel < 0 {
			return false
		}
		i := from + rel
		var before, after byte = ' ', ' '
		if i > 0 {
			before = s[i-1]
		}
		if i+len(name) < len(s) {
			after = s[i+len(name)]
		}
		if before != '.' && !isWordByte(before) && !isWordByte(after) {
			return true
		}
		from = i + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
