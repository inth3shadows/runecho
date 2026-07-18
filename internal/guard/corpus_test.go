package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// corpusCase is one recorded edit scenario replayed through the guard exactly
// as hook mode runs it: IR symbols + on-disk file defs/imports → Run.
//
// The corpus is the FP/FN regression gate: `expect` lists symbols the guard
// MUST flag (a miss is a false negative — test failure), anything flagged
// outside `expect`+`known_fp` is an unexpected false positive (test failure).
// `known_fp` documents false positives the current extractor is known to
// produce; they are tracked and reported but do not fail, so the corpus stays
// green while honestly recording the FP surface. When a fix lands and a
// known_fp stops firing, the run logs it so the case can be promoted to a
// plain true negative.
type corpusCase struct {
	Name    string            `json:"name"`
	Desc    string            `json:"desc,omitempty"`
	File    string            `json:"file"`
	Known   []string          `json:"known,omitempty"`    // IR snapshot symbols
	InFile  string            `json:"infile,omitempty"`   // on-disk file content (defs+imports folded, as hook mode does)
	Edit    string            `json:"edit"`               // new content being written
	Expect  []string          `json:"expect"`             // symbols the guard must flag
	KnownFP []string          `json:"known_fp,omitempty"` // symbols flagged today that are not real violations
	Suggest map[string]string `json:"suggest,omitempty"`  // expected did-you-mean per flagged symbol
}

// replayCase runs one case through the same pipeline as hook mode:
// known = IR symbols + in-file defs + in-file import bindings, then Run.
func replayCase(c corpusCase) []Violation {
	known := make(map[string]struct{}, len(c.Known))
	for _, s := range c.Known {
		known[s] = struct{}{}
	}
	lang := LangFor(c.File)
	if c.InFile != "" {
		lines := TextToAddedLines(c.InFile)
		for _, d := range ExtractDefs(lang, lines) {
			known[d] = struct{}{}
		}
		for _, im := range ExtractImports(lang, lines) {
			known[im] = struct{}{}
		}
		// Mirror addInFileDefs: JS folds whole-file declarator binding targets
		// (destructuring, object destructure, computed-assign) so a setter/callable
		// bound outside the edited hunk resolves — the useState-destructure-on-an-
		// untouched-line case.
		if lang == LangJS {
			for _, name := range JSDeclaredNames(lines) {
				known[name] = struct{}{}
			}
		}
		// Mirror addInFileDefs: Python folds whole-file assignment targets so a
		// local callable bound outside the edited hunk resolves.
		if lang == LangPython {
			for _, name := range PyDeclaredNames(lines) {
				known[name] = struct{}{}
			}
		}
	}
	diffs := []FileDiff{{Path: c.File, AddedLines: TextToAddedLines(c.Edit)}}
	return Run(known, "", diffs)
}

func TestGuardCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files found: %v", err)
	}

	type tally struct{ cases, tp, fn, unexpectedFP, trackedFP, fixedFP int }
	totals := make(map[string]*tally)

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var cases []corpusCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}

		for _, c := range cases {
			c := c
			t.Run(c.Name, func(t *testing.T) {
				lang := string(LangFor(c.File))
				if totals[lang] == nil {
					totals[lang] = &tally{}
				}
				tl := totals[lang]
				tl.cases++

				violations := replayCase(c)
				flagged := make(map[string]Violation, len(violations))
				for _, v := range violations {
					flagged[v.Symbol] = v
				}
				expect := setOf(c.Expect...)
				knownFP := setOf(c.KnownFP...)

				for sym, v := range flagged {
					switch {
					case contains(expect, sym):
						tl.tp++
						if want := c.Suggest[sym]; want != "" && v.Suggestion != want {
							t.Errorf("suggestion for %q = %q, want %q", sym, v.Suggestion, want)
						}
					case contains(knownFP, sym):
						tl.trackedFP++
					default:
						tl.unexpectedFP++
						t.Errorf("unexpected false positive: %q flagged at %s:%d (suggestion %q)",
							sym, v.File, v.Line, v.Suggestion)
					}
				}
				for sym := range expect {
					if _, ok := flagged[sym]; !ok {
						tl.fn++
						t.Errorf("false negative: %q expected but not flagged", sym)
					}
				}
				for sym := range knownFP {
					if _, ok := flagged[sym]; !ok {
						tl.fixedFP++
						t.Logf("known FP %q no longer fires — promote %s to a plain true-negative case", sym, c.Name)
					}
				}
			})
		}
	}

	// Baseline summary — the number the corpus exists to track.
	langs := make([]string, 0, len(totals))
	for l := range totals {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	summary := "corpus baseline:"
	for _, l := range langs {
		tl := totals[l]
		summary += fmt.Sprintf(" [%s: %d cases, %d TP, %d FN, %d unexpected-FP, %d tracked-FP]",
			l, tl.cases, tl.tp, tl.fn, tl.unexpectedFP, tl.trackedFP)
	}
	t.Log(summary)
}

func contains(set map[string]struct{}, s string) bool {
	_, ok := set[s]
	return ok
}
