package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/guard"
)

// Stratum separates observed hallucinations (mined from real session
// transcripts) from elicited ones (induced offline). They are scored and
// reported SEPARATELY, never pooled: agreement between strata earns the elicited
// cases credibility; divergence is itself a finding. See Phase 2 plan rule 5.
type Stratum string

const (
	Observed Stratum = "observed" // provenance "transcript:..."
	Elicited Stratum = "elicited" // provenance "elicited:..."
)

// ObservedFloor is the minimum fraction of cases that must be transcript-
// observed. Below it the hybrid corpus has quietly degraded into "mostly
// elicited" — the synthetic corpus rebuilt with extra steps. Format() flags it.
const ObservedFloor = 0.50

// CapturedCase is one hand-labeled real-world hallucination (or real reference),
// frozen as a JSON fixture. The model that produced source_line ran in the past;
// eval is deterministic replay. known_symbols is captured statically (NOT
// re-derived from a live IR) so the score stays deterministic and decoupled from
// IR drift.
type CapturedCase struct {
	ID               string   `json:"id"`
	Lang             string   `json:"lang"`              // "go" | "js" | "ts" | "py"
	Repo             string   `json:"repo"`              // provenance only
	Ref              string   `json:"ref"`               // commit/snapshot the label was verified at
	SourceLine       string   `json:"source_line"`       // verbatim model output containing the reference
	ReferencedSymbol string   `json:"referenced_symbol"` // the callee under judgement
	Label            string   `json:"label"`             // "hallucinated" | "real"
	LabelSource      string   `json:"label_source"`      // how ground truth was established (NOT RunEcho)
	KnownSymbols     []string `json:"known_symbols"`     // frozen symbol universe for the repo snapshot
	Provenance       string   `json:"provenance"`        // "transcript:<id>" | "elicited:<model>"
	Notes            string   `json:"notes"`             // why it's (not) a hallucination

	// Optional whole-file context for qualified-call measurement. Omitting all
	// three replays the case exactly as before (call/const/type checks only). See
	// Case for the field semantics; these are the JSON-fixture mirror.
	FileContext []string            `json:"file_context,omitempty"` // pre-edit whole file, one entry per line
	ModulePath  string              `json:"module_path,omitempty"`  // Go: go.mod module path
	DepExports  map[string][]string `json:"dep_exports,omitempty"`  // import path → frozen exported names
}

// LoadCaptured reads and validates a JSON array of captured cases. Validation is
// strict on purpose: a malformed or self-inconsistent fixture is a labeling bug,
// and a benchmark built on bad labels is worse than none.
func LoadCaptured(path string) ([]CapturedCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []CapturedCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]struct{}{}
	for i, cc := range cases {
		if err := cc.validate(); err != nil {
			return nil, fmt.Errorf("%s[%d] %q: %w", path, i, cc.ID, err)
		}
		if _, dup := seen[cc.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate id %q", path, cc.ID)
		}
		seen[cc.ID] = struct{}{}
	}
	return cases, nil
}

func (cc CapturedCase) validate() error {
	switch {
	case cc.ID == "":
		return fmt.Errorf("missing id")
	case cc.SourceLine == "":
		return fmt.Errorf("missing source_line")
	case cc.ReferencedSymbol == "":
		return fmt.Errorf("missing referenced_symbol")
	case len(cc.KnownSymbols) == 0:
		return fmt.Errorf("empty known_symbols")
	case cc.Label != "hallucinated" && cc.Label != "real":
		return fmt.Errorf("bad label %q (want hallucinated|real)", cc.Label)
	}
	if _, err := langOf(cc.Lang); err != nil {
		return err
	}
	if _, err := stratumOf(cc.Provenance); err != nil {
		return err
	}
	// The line must actually contain the symbol it claims to reference.
	if !strings.Contains(cc.SourceLine, cc.ReferencedSymbol) {
		return fmt.Errorf("source_line does not contain referenced_symbol %q", cc.ReferencedSymbol)
	}
	// Self-consistency against the frozen known-set: a "real" case must reference
	// a symbol that exists; a "hallucinated" case must reference one that does
	// not. Catches mislabels at load time.
	inKnown := false
	for _, s := range cc.KnownSymbols {
		if s == cc.ReferencedSymbol {
			inKnown = true
			break
		}
	}
	if cc.Label == "real" && !inKnown {
		return fmt.Errorf("labeled real but %q absent from known_symbols", cc.ReferencedSymbol)
	}
	if cc.Label == "hallucinated" && inKnown {
		return fmt.Errorf("labeled hallucinated but %q present in known_symbols", cc.ReferencedSymbol)
	}
	// The qualified checks are Go-only (GoQualifiedViolations / GoDepQualifiedViolations).
	// module_path/dep_exports on a non-Go case is a fixture bug: it would be silently
	// ignored, giving a false sense that the case exercises the dependency path.
	if cc.Lang != "go" {
		if cc.ModulePath != "" {
			return fmt.Errorf("module_path set on non-go case (lang %q); qualified checks are Go-only", cc.Lang)
		}
		if len(cc.DepExports) > 0 {
			return fmt.Errorf("dep_exports set on non-go case (lang %q); qualified checks are Go-only", cc.Lang)
		}
	}
	// If module_path/dep_exports are set, they drive whole-file checks that need the
	// file — without file_context they can never fire, which is a mislabel, not an abstain.
	if len(cc.FileContext) == 0 && (cc.ModulePath != "" || len(cc.DepExports) > 0) {
		return fmt.Errorf("module_path/dep_exports set without file_context; the qualified checks need the whole file")
	}
	return nil
}

func (cc CapturedCase) toCase() Case {
	lang, _ := langOf(cc.Lang) // validated already
	label := Real
	if cc.Label == "hallucinated" {
		label = Hallucinated
	}
	strat, _ := stratumOf(cc.Provenance) // validated already
	return Case{
		Lang:       lang,
		Path:       pathFor(lang),
		Line:       cc.SourceLine,
		Symbol:     cc.ReferencedSymbol,
		Known:      cc.KnownSymbols,
		Label:      label,
		Category:   Category(strat),
		WholeFile:  cc.FileContext,
		ModulePath: cc.ModulePath,
		DepExports: cc.DepExports,
	}
}

// CapturedReport holds the stratified confusion matrices. Strata are never
// merged into one headline number (rule 5); language and reference-position are
// secondary cuts. ByPosition is the load-bearing one: it shows WHICH syntactic
// positions the guard covers vs misses, which is the real finding.
type CapturedReport struct {
	ByStratum  map[Stratum]*Counts
	ByLang     map[guard.Lang]*Counts
	ByPosition map[string]*Counts
	N          int
}

// ScoreCaptured replays each case at the shipped baseline configuration
// (qualified checks off) and tallies counts per stratum, language, and reference
// position. This is the quotable number and the one existing callers expect;
// ScoreCapturedDual adds the flags-on comparison.
func ScoreCaptured(cases []CapturedCase) CapturedReport {
	return scoreCapturedWith(cases, baselineConfig)
}

// scoreCapturedWith is ScoreCaptured parameterized by guard configuration.
func scoreCapturedWith(cases []CapturedCase, cfg guardConfig) CapturedReport {
	r := CapturedReport{
		ByStratum:  map[Stratum]*Counts{},
		ByLang:     map[guard.Lang]*Counts{},
		ByPosition: map[string]*Counts{},
		N:          len(cases),
	}
	for _, cc := range cases {
		c := cc.toCase()
		strat, _ := stratumOf(cc.Provenance)
		flagged := guardFlags(c, cfg)
		tally(get(r.ByStratum, strat), c.Label, flagged)
		tally(get(r.ByLang, c.Lang), c.Label, flagged)
		tally(getStr(r.ByPosition, parsePosition(cc.Notes)), c.Label, flagged)
	}
	return r
}

// DualCapturedReport pairs the shipped-default score with the qualified-checks-on
// score over the SAME corpus. The two are never pooled into one headline: the
// baseline is what a user gets today, the enhanced number is the promotion case,
// and their delta on the observed hallucinated stratum is the evidence for or
// against flipping RUNECHO_GUARD_QUALIFIED / RUNECHO_GUARD_DEPS_GO on by default.
type DualCapturedReport struct {
	Baseline CapturedReport // qualified checks off (shipped default)
	Enhanced CapturedReport // qualified checks on
}

// ScoreCapturedDual scores the corpus twice and returns both reports.
func ScoreCapturedDual(cases []CapturedCase) DualCapturedReport {
	return DualCapturedReport{
		Baseline: scoreCapturedWith(cases, baselineConfig),
		Enhanced: scoreCapturedWith(cases, enhancedConfig),
	}
}

// Format renders both configurations and the observed-stratum delta between them.
func (d DualCapturedReport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== BASELINE (shipped default: qualified checks off) ===\n")
	fmt.Fprintf(&b, "%s", d.Baseline.Format())
	fmt.Fprintf(&b, "\n=== ENHANCED (RUNECHO_GUARD_QUALIFIED + DEPS_GO on) ===\n")
	fmt.Fprintf(&b, "%s", d.Enhanced.Format())

	base, enh := d.Baseline.ByStratum[Observed], d.Enhanced.ByStratum[Observed]
	fmt.Fprintf(&b, "\n=== PROMOTION DELTA (observed stratum) ===\n")
	if base == nil || enh == nil {
		fmt.Fprintf(&b, "  no observed cases to compare\n")
		return b.String()
	}
	caughtDelta := enh.TP - base.TP
	fpDelta := enh.FP - base.FP
	fmt.Fprintf(&b, "  hallucinations caught: %d/%d → %d/%d  (%+d)\n",
		base.TP, base.TP+base.FN, enh.TP, enh.TP+enh.FN, caughtDelta)
	fmt.Fprintf(&b, "  false positives:       %d/%d → %d/%d  (%+d)\n",
		base.FP, base.FP+base.TN, enh.FP, enh.FP+enh.TN, fpDelta)
	switch {
	case fpDelta > 0:
		fmt.Fprintf(&b, "  VERDICT: enhanced introduces false positives — do NOT promote as-is\n")
	case caughtDelta > 0:
		fmt.Fprintf(&b, "  VERDICT: enhanced catches %+d more with no new FP — promotion candidate\n", caughtDelta)
	default:
		fmt.Fprintf(&b, "  VERDICT: no measurable delta on this corpus — needs qualified cases (see #171)\n")
	}
	return b.String()
}

func getStr(m map[string]*Counts, k string) *Counts {
	if m[k] == nil {
		m[k] = &Counts{}
	}
	return m[k]
}

// parsePosition pulls the "position=<kind>" tag out of a fixture's notes field
// ("unknown" if absent). This is how the report shows the guard's coverage by
// syntactic position without a dedicated schema field.
func parsePosition(notes string) string {
	i := strings.Index(notes, "position=")
	if i < 0 {
		return "unknown"
	}
	rest := notes[i+len("position="):]
	for j, r := range rest {
		if r == ';' || r == ' ' || r == ',' {
			return rest[:j]
		}
	}
	return rest
}

func tally(c *Counts, label Label, flagged bool) {
	switch {
	case label == Hallucinated && flagged:
		c.TP++
	case label == Hallucinated && !flagged:
		c.FN++
	case label == Real && flagged:
		c.FP++
	default:
		c.TN++
	}
}

// observedFraction returns the share of cases drawn from real transcripts.
func (r CapturedReport) observedFraction() float64 {
	if r.N == 0 {
		return 0
	}
	obs := 0
	if c := r.ByStratum[Observed]; c != nil {
		obs = c.n()
	}
	return float64(obs) / float64(r.N)
}

// Format renders the captured scorecard in COUNTS, not percentages — small N
// means wide error bars, so "caught 11/13" is honest where "84.6%" is not
// (rule 4). It flags a breach of the observed-majority floor (rule 5).
func (r CapturedReport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "RunEcho guard hallucination benchmark (captured-LLM)\n")
	frac := r.observedFraction()
	floor := "OK"
	if frac < ObservedFloor {
		floor = fmt.Sprintf("BELOW FLOOR %.0f%% — corpus is mostly elicited; treat as semi-synthetic", 100*ObservedFloor)
	}
	fmt.Fprintf(&b, "  N=%d  observed=%.0f%% (floor %.0f%% %s)\n",
		r.N, 100*frac, 100*ObservedFloor, floor)

	fmt.Fprintf(&b, "\n  by stratum (reported separately — never pooled):\n")
	for _, s := range []Stratum{Observed, Elicited} {
		c := r.ByStratum[s]
		if c == nil {
			fmt.Fprintf(&b, "    %-8s  (none)\n", string(s))
			continue
		}
		fmt.Fprintf(&b, "    %-8s  hallucinations caught %d/%d ; false positives %d/%d\n",
			string(s), c.TP, c.TP+c.FN, c.FP, c.FP+c.TN)
	}

	fmt.Fprintf(&b, "\n  by language:\n")
	for _, k := range sortedLangs(r.ByLang) {
		c := r.ByLang[k]
		fmt.Fprintf(&b, "    %-3s  caught %d/%d ; fp %d/%d\n",
			string(k), c.TP, c.TP+c.FN, c.FP, c.FP+c.TN)
	}

	fmt.Fprintf(&b, "\n  by reference position (the coverage map):\n")
	positions := make([]string, 0, len(r.ByPosition))
	for k := range r.ByPosition {
		positions = append(positions, k)
	}
	sort.Strings(positions)
	for _, k := range positions {
		c := r.ByPosition[k]
		fmt.Fprintf(&b, "    %-16s  caught %d/%d ; fp %d/%d  (n=%d)\n",
			k, c.TP, c.TP+c.FN, c.FP, c.FP+c.TN, c.n())
	}

	fmt.Fprintf(&b, "\n  caveats: small N — counts, not rates; strata kept separate;\n")
	fmt.Fprintf(&b, "           refs labeled by hand against repo source, NOT by RunEcho;\n")
	fmt.Fprintf(&b, "           misses here (esp. extraction misses / qualified calls /\n")
	fmt.Fprintf(&b, "           unexported Go) are real findings, not noise.\n")
	return b.String()
}

func langOf(s string) (guard.Lang, error) {
	switch s {
	case "go":
		return guard.LangGo, nil
	case "js", "ts", "jsx", "tsx":
		return guard.LangJS, nil
	case "py":
		return guard.LangPython, nil
	}
	return guard.LangUnknown, fmt.Errorf("unsupported lang %q", s)
}

func pathFor(lang guard.Lang) string {
	switch lang {
	case guard.LangGo:
		return "captured.go"
	case guard.LangJS:
		return "captured.ts"
	case guard.LangPython:
		return "captured.py"
	}
	return "captured.txt"
}

func stratumOf(provenance string) (Stratum, error) {
	switch {
	case strings.HasPrefix(provenance, "transcript:"):
		return Observed, nil
	case strings.HasPrefix(provenance, "elicited:"):
		return Elicited, nil
	}
	return "", fmt.Errorf("provenance %q must start with transcript: or elicited:", provenance)
}
