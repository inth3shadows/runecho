package bench

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/depindex"
	"github.com/inth3shadows/runecho/internal/guard"
)

// guardConfig selects which guard checks the harness runs. The shipped binary
// gates the qualified-call checks behind default-off env flags
// (RUNECHO_GUARD_QUALIFIED, RUNECHO_GUARD_DEPS_GO), so the corpus must be able to
// score BOTH states: the baseline is what a user gets today, the enhanced state
// is what promoting the flags would catch. The delta is the promotion evidence.
type guardConfig struct {
	// qualified enables the Go qualified-call checks (same-repo #176 and
	// external-dep #175). They require whole-file context and are no-ops on cases
	// that do not carry it, so enabling them never changes an in-scope-only case.
	qualified bool
}

// baselineConfig mirrors the shipped default: qualified checks off. It is what
// the existing scorecards report, so ScoreCaptured/Score keep their old numbers.
var baselineConfig = guardConfig{}

// enhancedConfig turns the qualified checks on — the "if we promoted the flags"
// measurement.
var enhancedConfig = guardConfig{qualified: true}

// Counts holds a confusion matrix for a slice of cases.
//
//	TP: hallucinated ref correctly flagged.
//	FN: hallucinated ref missed (slipped through).
//	FP: real ref wrongly flagged (false alarm).
//	TN: real ref correctly passed.
type Counts struct {
	TP, FN, FP, TN int
}

func (c Counts) recall() float64 { // catch-rate
	if c.TP+c.FN == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FN)
}

func (c Counts) fpRate() float64 {
	if c.FP+c.TN == 0 {
		return 0
	}
	return float64(c.FP) / float64(c.FP+c.TN)
}

func (c Counts) precision() float64 {
	if c.TP+c.FP == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FP)
}

func (c Counts) f1() float64 {
	p, r := c.precision(), c.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func (c Counts) n() int { return c.TP + c.FN + c.FP + c.TN }

// Scorecard aggregates a run overall and along two cuts: language and category.
type Scorecard struct {
	Overall    Counts
	ByLang     map[guard.Lang]*Counts
	ByCategory map[Category]*Counts
}

// Score runs every case through the guard and tallies the confusion matrix. The
// guard is invoked per case (one file, one line) so attribution is unambiguous.
// The synthetic corpus carries no whole-file context, so it always scores at the
// baseline configuration (the qualified checks are no-ops without it).
func Score(cases []Case) Scorecard {
	sc := Scorecard{
		ByLang:     map[guard.Lang]*Counts{},
		ByCategory: map[Category]*Counts{},
	}
	for _, cs := range cases {
		flagged := guardFlags(cs, baselineConfig)
		lang := get(sc.ByLang, cs.Lang)
		cat := get(sc.ByCategory, cs.Category)

		switch {
		case cs.Label == Hallucinated && flagged:
			sc.Overall.TP++
			lang.TP++
			cat.TP++
		case cs.Label == Hallucinated && !flagged:
			sc.Overall.FN++
			lang.FN++
			cat.FN++
		case cs.Label == Real && flagged:
			sc.Overall.FP++
			lang.FP++
			cat.FP++
		default: // Real && !flagged
			sc.Overall.TN++
			lang.TN++
			cat.TN++
		}
	}
	return sc
}

// guardFlags reports whether the guard, in configuration cfg, raised a violation
// naming the case's referenced symbol. The baseline checks (call/const/type, via
// guard.Run) always run; the qualified checks run only when cfg.qualified is set
// AND the case carries the whole-file context they need.
func guardFlags(cs Case, cfg guardConfig) bool {
	knownSet := make(map[string]struct{}, len(cs.Known))
	for _, s := range cs.Known {
		knownSet[s] = struct{}{}
	}
	added := guard.TextToAddedLines(cs.Line)
	diffs := []guard.FileDiff{{Path: cs.Path, AddedLines: added}}
	for _, v := range guard.Run(knownSet, "", diffs) {
		if v.Symbol == cs.Symbol {
			return true
		}
	}

	// Qualified-call checks (Go only). They parse the file's imports and a shadow
	// gate, so they need WholeFile; without it (synthetic corpus, or a captured
	// case that did not opt in) they are correctly skipped and this is a pure
	// baseline score.
	if cfg.qualified && cs.Lang == guard.LangGo && len(cs.WholeFile) > 0 {
		whole := linesToAdded(cs.WholeFile)
		// Same-repo internal-package qualified (#176). ModulePath classifies the
		// qualifier as same-repo; "" abstains inside the guard.
		if cs.ModulePath != "" {
			for _, v := range guard.GoQualifiedViolations(whole, added, knownSet, cs.ModulePath) {
				if qualifiedMatches(v.Symbol, cs.Symbol) {
					return true
				}
			}
		}
		// External-dependency qualified (#175), replayed against the frozen export set.
		if len(cs.DepExports) > 0 {
			idx := newFrozenDepIndex(cs.DepExports)
			for _, v := range guard.GoDepQualifiedViolations(whole, added, cs.ModulePath, idx) {
				if qualifiedMatches(v.Symbol, cs.Symbol) {
					return true
				}
			}
		}
	}
	return false
}

// qualifiedMatches reports whether a qualified-check violation names the case's
// referenced symbol. Those checks report the full `q.Sym` (e.g. "internalpkg.NoSuchFunc"),
// while a case's ReferencedSymbol is the bare selector ("NoSuchFunc"), so a plain
// equality (as the baseline uses) would never match. Accept either the whole
// qualified name or its trailing selector.
func qualifiedMatches(violationSym, want string) bool {
	if violationSym == want {
		return true
	}
	if i := strings.LastIndex(violationSym, "."); i >= 0 {
		return violationSym[i+1:] == want
	}
	return false
}

// linesToAdded turns a whole-file slice into AddedLines with sequential 1-based
// line numbers. The qualified checks use these only for import parsing and the
// shadow gate (which scan text line by line); the numbers need only be monotonic.
func linesToAdded(lines []string) []guard.AddedLine {
	out := make([]guard.AddedLine, len(lines))
	for i, t := range lines {
		out[i] = guard.AddedLine{LineNo: i + 1, Text: t}
	}
	return out
}

// frozenDepIndex is a bench-only depindex.Index backed by the fixture's frozen
// dep_exports. A listed import path resolves to exactly its listed names; an
// unlisted path is Unknown (abstain) — the same shape a real index has for a
// package it has never scanned. Freezing the surface in the fixture keeps replay
// independent of the live module cache and the installed dependency version, the
// same discipline known_symbols follows.
type frozenDepIndex map[string]map[string]struct{}

func newFrozenDepIndex(exports map[string][]string) frozenDepIndex {
	idx := make(frozenDepIndex, len(exports))
	for path, names := range exports {
		set := make(map[string]struct{}, len(names))
		for _, n := range names {
			set[n] = struct{}{}
		}
		idx[path] = set
	}
	return idx
}

func (f frozenDepIndex) Lookup(module string) depindex.PackageSymbols {
	ex, ok := f[module]
	if !ok {
		return depindex.PackageSymbols{Res: depindex.Unknown, Reason: "not in fixture dep_exports"}
	}
	return depindex.PackageSymbols{Res: depindex.Resolved, Exports: ex}
}

func get[K comparable](m map[K]*Counts, k K) *Counts {
	if m[k] == nil {
		m[k] = &Counts{}
	}
	return m[k]
}

// Format renders the scorecard as a fixed, deterministic text block.
func (sc Scorecard) Format() string {
	var b strings.Builder
	o := sc.Overall
	fmt.Fprintf(&b, "RunEcho guard hallucination benchmark (synthetic)\n")
	fmt.Fprintf(&b, "  cases=%d  TP=%d FN=%d FP=%d TN=%d\n", o.n(), o.TP, o.FN, o.FP, o.TN)
	fmt.Fprintf(&b, "  catch-rate(recall)=%.1f%%  false-positive-rate=%.1f%%  precision=%.1f%%  F1=%.1f%%\n",
		100*o.recall(), 100*o.fpRate(), 100*o.precision(), 100*o.f1())

	fmt.Fprintf(&b, "\n  by language:\n")
	for _, k := range sortedLangs(sc.ByLang) {
		c := sc.ByLang[k]
		fmt.Fprintf(&b, "    %-3s  catch=%.1f%%  fp=%.1f%%  (n=%d)\n",
			string(k), 100*c.recall(), 100*c.fpRate(), c.n())
	}

	fmt.Fprintf(&b, "\n  by hallucination type (catch-rate):\n")
	for _, k := range sortedCats(sc.ByCategory) {
		c := sc.ByCategory[k]
		if k == CatReal {
			continue // negatives have no catch-rate
		}
		fmt.Fprintf(&b, "    %-16s  catch=%.1f%%  (n=%d)\n", string(k), 100*c.recall(), c.n())
	}

	fmt.Fprintf(&b, "\n  caveats: synthetic perturbations, not a real LLM error distribution;\n")
	fmt.Fprintf(&b, "           known-set is declared (IR extraction held constant);\n")
	fmt.Fprintf(&b, "           only flags call-position references (guard's scope).\n")
	return b.String()
}

func sortedLangs(m map[guard.Lang]*Counts) []guard.Lang {
	out := make([]guard.Lang, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedCats(m map[Category]*Counts) []Category {
	out := make([]Category, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
