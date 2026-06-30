package bench

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/guard"
)

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
func Score(cases []Case) Scorecard {
	sc := Scorecard{
		ByLang:     map[guard.Lang]*Counts{},
		ByCategory: map[Category]*Counts{},
	}
	for _, cs := range cases {
		flagged := guardFlags(cs)
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

// guardFlags reports whether guard.Run raised a violation naming the case's
// referenced symbol.
func guardFlags(cs Case) bool {
	knownSet := make(map[string]struct{}, len(cs.Known))
	for _, s := range cs.Known {
		knownSet[s] = struct{}{}
	}
	diffs := []guard.FileDiff{{
		Path:       cs.Path,
		AddedLines: guard.TextToAddedLines(cs.Line),
	}}
	for _, v := range guard.Run(knownSet, "", diffs) {
		if v.Symbol == cs.Symbol {
			return true
		}
	}
	return false
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
