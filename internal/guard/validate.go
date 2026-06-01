package guard

import (
	"bufio"
	"os"
	"strings"
)

// Violation records a single unresolved symbol reference.
type Violation struct {
	File   string
	Line   int
	Symbol string
	Lang   Lang
}

// Run validates diffs against the known symbol set and returns any violations.
// symbols is the set of known symbol names from the IR snapshot.
// ignorePath is the optional path to a .runechoguardignore file (empty = none).
func Run(symbols map[string]struct{}, ignorePath string, diffs []FileDiff) []Violation {
	// Build the known set: IR symbols + ignore list
	known := make(map[string]struct{}, len(symbols))
	for name := range symbols {
		known[name] = struct{}{}
	}
	if ignorePath != "" {
		for _, name := range loadIgnore(ignorePath) {
			known[name] = struct{}{}
		}
	}

	// Pass 1: collect all new definitions across the entire diff and add to known.
	for _, fd := range diffs {
		lang := LangFor(fd.Path)
		for _, def := range ExtractDefs(lang, fd.AddedLines) {
			known[def] = struct{}{}
		}
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
		for _, ref := range ExtractRefs(lang, fd.AddedLines) {
			if _, ok := known[ref.Name]; ok {
				continue
			}
			key := fd.Path + "\x00" + ref.Name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			violations = append(violations, Violation{
				File:   fd.Path,
				Line:   ref.LineNo,
				Symbol: ref.Name,
				Lang:   lang,
			})
		}
	}
	return violations
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
