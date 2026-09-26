package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
)

// PR-L latency harness (#414/#417/#415 dogfood-window plan, §6.2/§6.4 of
// runecho-seed-consolidation-and-inert-check-dogfood.md).
//
// Run it against one or more real repositories:
//
//	RUNECHO_FLAGCOST_CORPUS=/path/to/repo[:/other/repo] \
//	  go test -count=1 -timeout 30m -run '^TestFlagCost$' -v ./cmd/runecho-guard
//
// The default 10m go test timeout is too short for a corpus the size of this
// repo: every payload runs through up to five hook invocations.
//
// recv-method, var-type, deps-go and dropped-import (E3) were enabled in this
// repo's own dogfood window under a posture-only measurement: the checks
// SKIPPED on every one of 4,325 recorded edits (no env keys were ever set),
// so 0/0/0/0 skipped-fires said nothing about their cost. This harness
// measures the thing that was actually never measured: the marginal wall
// time each flag adds to a real hook invocation, driven through the same
// runHookMode entry point Claude Code's PreToolUse hook calls, against real
// repositories rather than a synthetic corpus (whose IR the checks would
// mostly abstain against, producing a meaningless PASS — see the plan's
// "Alternatives considered").
//
// Skipped unless RUNECHO_FLAGCOST_CORPUS names at least one root (a
// colon-separated list of real repository paths). There is deliberately no
// default fallback corpus the way TestLintDifferential has one: a
// vartype/recv-method/deps-go finding needs the repo's OWN cross-file symbol
// graph to be non-trivial, which a small committed fixture tree cannot
// provide honestly.
//
// Modeled on TestLintDifferential (it shares that file's guardIsolationEnv,
// percentile, tailMinSamples and tailLabel; same package) but the two harnesses differ in
// exactly what #313 warns about: lint's payload is a synthetic path fed
// arbitrary corpus text, because ruff does not care where the file lives.
// recv-method/var-type/deps-go/dropped-import resolve against the repo's own
// IR and import graph, so THIS harness's payload has to be a real edit
// against the real file at its real path inside the enrolled root — a
// fabricated target would make every one of the four checks abstain and the
// measured "cost" would be the cost of an early return, not of the check.
// The decision log's per-check status is read after every run for the same
// reason: a run whose check reported "skipped" bailed, and its time is
// dropped from that arm rather than averaged in as a free sample, and a run
// whose BASELINE took an early exit is dropped from every arm: a paired
// difference against an early return is the cost of the whole hook.

// --- payload construction (pure) ---

// flagcostWindowLines is the size of one identity-edit window. 20 lines is
// large enough to give recv-method/var-type/dropped-import real surrounding
// context (a receiver decl, a var assignment, an import block) without
// requiring the whole file.
const flagcostWindowLines = 20

// flagcostK is the max number of identity payloads built per file.
const flagcostK = 3

// flagcostWindows partitions content into non-overlapping windows of size
// lines each, keeps only windows whose text occurs EXACTLY ONCE in content,
// and returns up to k of them spread evenly across the file (in file order).
//
// Spread, not the first k: the first 60 lines of a file are its package
// clause, imports and top-of-file declarations, which is exactly the region
// where var-type and recv-method have the least to look at. Timing only that
// region would under-measure both.
//
// Uniqueness matters for two reasons. First, old_string is fed straight into
// the same payload machinery a real Edit tool call uses (payloadOld mirrors
// the production tool_input shape) — Edit itself requires old_string to be
// unique in the target file (blockStartLine's doc comment, main.go), and a harness that fed
// a non-unique old_string would be exercising a shape the real tool never
// sends. Second, a non-unique window would let hookBlockIndices resolve the
// block's pre-edit position to the WRONG occurrence, silently corrupting the
// per-block seed state (docstring/brace-depth) a check reads — a corpus
// artifact, not a signal about the flag's cost.
func flagcostWindows(content string, lines, k int) []string {
	ls := strings.Split(content, "\n")
	// A file ending in "\n" splits into a trailing "" that is not a real
	// line; keeping it would let the final chunk be a degenerate
	// all-blank window that can never be unique in a file with more than
	// one trailing blank line.
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	var uniq []string
	for i := 0; i < len(ls); i += lines {
		win := strings.Join(ls[i:min(i+lines, len(ls))], "\n")
		if win == "" {
			continue
		}
		if strings.Count(content, win) != 1 {
			continue // non-unique: skip, don't guess
		}
		uniq = append(uniq, win)
	}
	if len(uniq) <= k {
		return uniq
	}
	// The centre of each of k equal slices: distinct because len(uniq) > k,
	// and k == 1 picks the middle rather than dividing by zero.
	out := make([]string, 0, k)
	for i := range k {
		out = append(out, uniq[(2*i+1)*len(uniq)/(2*k)])
	}
	return out
}

var (
	// Single-line imports only: removing one line of a parenthesised or
	// braced multi-line import leaves a syntax error, which times the
	// parser's error path instead of the check's.
	flagcostPyImportRe = regexp.MustCompile(`^(import [\w., ]+|from [\w.]+ import [\w., ]+)\s*$`)
	flagcostJSImportRe = regexp.MustCompile(`^import [^{}]*(\{[^{}]*\})?[^{}]* from ['"][^'"]+['"];?\s*$`)
)

// flagcostDropImport returns an Edit that blanks the first single-line
// top-level import of content whose "line\n" text is unique, or ok=false.
//
// Blanks, not deletes: an Edit with an empty new_string and every
// removed-text check off is verifyEdit's empty-input early return, so the
// baseline arm would exit in microseconds and the paired marginal would be
// the cost of the entire hook, not of dropped-import.
//
// Identity edits never drop an import, so dropped-import returns before its
// expensive step (the lazy whole-file preBound fold, verify.go) on every one
// of them. This payload is the one that reaches it; without it the harness
// would publish the cost of the fast path as the cost of the check.
func flagcostDropImport(content string, lang guard.Lang) (oldString, newString string, ok bool) {
	var re *regexp.Regexp
	switch lang {
	case guard.LangPython:
		re = flagcostPyImportRe
	case guard.LangJS:
		re = flagcostJSImportRe
	default:
		return "", "", false
	}
	for _, line := range strings.Split(content, "\n") {
		if !re.MatchString(line) {
			continue
		}
		if old := line + "\n"; strings.Count(content, old) == 1 {
			return old, "\n", true
		}
	}
	return "", "", false
}

// flagcostArmsFor returns the single-flag arm names meaningful for lang,
// matching each check's own language gate in verify.go (recv-method/
// var-type/deps-go: `lang == guard.LangGo`; dropped-import:
// guard.DroppedImportSupportedLang). This is the one place the
// stratification promised in the spec ("Python payloads don't count toward
// recv-method's n") is decided, factored out so TestFlagCost_Stratification
// can assert it without running the corpus sweep.
func flagcostArmsFor(lang guard.Lang) []string {
	var arms []string
	if lang == guard.LangGo {
		arms = append(arms, "recv-method", "var-type", "deps-go")
	}
	if guard.DroppedImportSupportedLang(lang) {
		arms = append(arms, "dropped-import")
	}
	return arms
}

// flagcostPayload is one observation target: a real Edit tool_input aimed at
// a real file inside an enrolled corpus root. kind is "identity"
// (old_string == new_string == a unique window of the file's own text) or
// "drop" (one import line deleted — see flagcostDropImport).
type flagcostPayload struct {
	file string // absolute path, for logging only
	lang guard.Lang
	kind string
	body string // rendered PreToolUse JSON, via payloadOld
}

// flagcostGroup is the population a payload's samples are compared within.
// Group names avoid '/', which go test reads as a subtest separator.
// Python and JS pool (dropped-import is the only check either runs), but a
// drop payload is its own group: it takes dropped-import's slow path, so
// pooling it with identity edits would let a median over mostly-fast-path
// samples hide it.
func flagcostGroup(lang guard.Lang, kind string) string {
	switch {
	case lang == guard.LangGo:
		return "go"
	case kind == "drop":
		return "py+js drop"
	default:
		return "py+js"
	}
}

// flagcostBucket names the sample bucket for one arm within one group.
func flagcostBucket(arm, group string) string {
	return arm + " [" + group + "]"
}

// flagcostPayloadsForRoot builds up to flagcostK identity payloads plus at
// most one drop payload for every .go/.py/.js/.ts file the real IR
// generation walk for this root indexed. Reusing that file set (rather than
// an independent filepath.WalkDir) means vendor/venv/node_modules/build/.git
// and everything else the generator's own walk already excludes is excluded
// here too, without a second copy of that skip-list to keep in sync — see
// internal/ir/generator.go's walkSourceFiles.
func flagcostPayloadsForRoot(t *testing.T, top string, files map[string]ir.FileIR) []flagcostPayload {
	t.Helper()
	var paths []string
	for p := range files {
		if len(flagcostArmsFor(guard.LangFor(p))) == 0 {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths) // reproducible payload order, not walk/map order

	var out []flagcostPayload
	for _, rel := range paths {
		abs := filepath.Join(top, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			t.Logf("flagcost: %s: unreadable since IR generation (%v), skipping", rel, err)
			continue
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			continue // empty file: no window to build, nothing to measure
		}
		lang := guard.LangFor(rel)
		for _, win := range flagcostWindows(content, flagcostWindowLines, flagcostK) {
			out = append(out, flagcostPayload{
				file: abs, lang: lang, kind: "identity",
				body: payloadOld(t, "Edit", abs, win, win, "", nil),
			})
		}
		if old, nu, ok := flagcostDropImport(content, lang); ok {
			out = append(out, flagcostPayload{
				file: abs, lang: lang, kind: "drop",
				body: payloadOld(t, "Edit", abs, old, nu, "", nil),
			})
		}
	}
	return out
}

// --- env plumbing ---

// flagcostFlagEnv maps an arm name to the one env var that arm turns on.
var flagcostFlagEnv = map[string]string{
	"recv-method":    "RUNECHO_GUARD_RECVMETHOD",
	"var-type":       "RUNECHO_GUARD_VARTYPE",
	"deps-go":        "RUNECHO_GUARD_DEPS_GO",
	"dropped-import": "RUNECHO_GUARD_DROPPED_IMPORT",
}

// flagcostNeutralize clears guardIsolationEnv (the four flags under test are
// in it; flagcostSetArm turns them back on per arm) plus RUNECHO_GUARD_LINT,
// which lintDiffEnv leaves alone because lint IS its subject — here it is
// noise like everything else. RUNECHO_GUARD_QUALIFIED is pinned off for the
// reason lintDiffEnv documents: it is the one default-on gate.
func flagcostNeutralize(t *testing.T) {
	t.Helper()
	for _, k := range append(slices.Clone(guardIsolationEnv), "RUNECHO_GUARD_LINT") {
		t.Setenv(k, "")
	}
	t.Setenv("RUNECHO_GUARD_QUALIFIED", "0")
}

// flagcostArmChecks returns the check names that arm turns on for lang:
// none for "baseline", every lang-applicable one for "all-four".
func flagcostArmChecks(arm string, lang guard.Lang) []string {
	switch arm {
	case "baseline":
		return nil
	case "all-four":
		return flagcostArmsFor(lang)
	default:
		return []string{arm}
	}
}

// flagcostSetArm sets the four flags for one named arm: "baseline" (all
// off), "all-four" (all on), or one of flagcostFlagEnv's single-flag names.
// Always resets all four first so arms run in any order without a previous
// arm's flag leaking into the next.
func flagcostSetArm(t *testing.T, arm string) {
	t.Helper()
	for _, k := range flagcostFlagEnv {
		t.Setenv(k, "")
	}
	switch arm {
	case "baseline":
	case "all-four":
		for _, k := range flagcostFlagEnv {
			t.Setenv(k, "1")
		}
	default:
		envKey, ok := flagcostFlagEnv[arm]
		if !ok {
			t.Fatalf("flagcost: unknown arm %q", arm)
		}
		t.Setenv(envKey, "1")
	}
}

// flagcostBailReasons are verifyEdit's early exits (verify.go). A run that
// logs one never reached the checks, whichever arm it was.
var flagcostBailReasons = []string{bailEmptyInput, bailBadPath, bailUnknownLang, bailDegradedStore}

// flagcostRun is one timed hook invocation's outcome.
type flagcostRun struct {
	d time.Duration
	// ran: the hook did not take an early exit and every check the arm
	// enabled logged a status other than "skipped".
	ran bool
	// abstained: some enabled check logged "unknown" — it ran, but gave up.
	// Counted, not dropped: the hook pays for an abstain in production too.
	abstained bool
}

// flagcostRunArm sets arm's env, times one in-process hook invocation
// through runHook, and reads back from the decision log whether it ran.
// The decision content is otherwise ignored — this harness measures cost,
// not correctness (that is every OTHER guard test's job).
//
// decisions.jsonl is removed after each read: readLastDecisionLog scans the
// whole file, and a sweep appends thousands of lines.
func flagcostRunArm(t *testing.T, arm string, p flagcostPayload) flagcostRun {
	t.Helper()
	flagcostSetArm(t, arm)
	start := time.Now()
	runHook(t, p.body)
	run := flagcostRun{d: time.Since(start)}

	rec := readLastDecisionLog(t)
	_ = os.Remove(filepath.Join(os.Getenv("RUNECHO_HOME"), "decisions.jsonl"))
	if reason, _ := rec["reason"].(string); rec == nil || slices.Contains(flagcostBailReasons, reason) {
		return run
	}
	checks, _ := rec["checks"].(map[string]any)
	for _, name := range flagcostArmChecks(arm, p.lang) {
		switch st, _ := checks[name].(string); st {
		case "", "skipped":
			return run
		case "unknown":
			run.abstained = true
		}
	}
	run.ran = true
	return run
}

// flagcostArmSequence returns the full run order for one payload: baseline,
// every language-specific single-flag arm, then all-four — rotated by n so
// which arm pays a payload's cold-position cost (page-in, first git call,
// etc.) varies across the corpus instead of always landing on the same arm.
// Generalizes TestLintDifferential's observe/offFirst two-way alternation (there are
// more than two arms here) on the same reasoning: an instrumented run in
// that harness found the effect real but small (11.4ms vs 11.3ms p50), so
// this is cheap insurance, not a correction of a measured bias.
func flagcostArmSequence(lang guard.Lang, n int) []string {
	seq := append([]string{"baseline"}, flagcostArmsFor(lang)...)
	seq = append(seq, "all-four")
	k := n % len(seq)
	return append(append([]string{}, seq[k:]...), seq[:k]...)
}

// --- statistics + gate ---

// flagcostStats is one bucket's samples. on[i], off[i] and marginal[i]
// come from the SAME payload's arm and baseline runs: the gate compares
// paired differences, so a slow file cannot land in one arm and a fast one
// in the other.
type flagcostStats struct {
	on, off, marginal []time.Duration
	bailed            int // the run or its baseline exited early, or a check reported "skipped"
	abstained         int // kept samples where a check reported "unknown"
}

type flagcostOutcome string

const (
	flagcostPass         flagcostOutcome = "PASS"
	flagcostFail         flagcostOutcome = "FAIL"
	flagcostInconclusive flagcostOutcome = "INCONCLUSIVE"
	flagcostInsufficient flagcostOutcome = "INSUFFICIENT"
)

// flagcostResamples is the bootstrap resample count. 2,000 puts the 2.5th
// and 97.5th percentiles 50 resamples from each end, which is stable to well
// under the microsecond resolution the limits are stated in.
const flagcostResamples = 2000

// flagcostMedianCI is a 95% percentile-bootstrap interval on the median of
// m. Seeded, so the same samples always give the same interval and a
// re-run's verdict moves only when the timings do.
func flagcostMedianCI(m []time.Duration) (lo, hi time.Duration) {
	r := rand.New(rand.NewPCG(415, 417))
	meds := make([]time.Duration, flagcostResamples)
	buf := make([]time.Duration, len(m))
	for i := range meds {
		for j := range buf {
			buf[j] = m[r.IntN(len(m))]
		}
		// buf is private scratch, so sort it in place; ceil(n/2) is
		// percentile's nearest-rank p50.
		slices.Sort(buf)
		meds[i] = buf[(len(buf)+1)/2-1]
	}
	slices.Sort(meds)
	// Nearest-rank 2.5th and 97.5th: ranks 50 and 1950 of 2,000.
	return meds[flagcostResamples*25/1000-1], meds[flagcostResamples*975/1000-1]
}

// flagCostVerdict is the PR-L latency gate on paired per-payload marginals.
// It separates the three things a single p50 comparison conflated: too few
// samples to say anything (INSUFFICIENT, below tailMinSamples), a cost
// confidently over the limit (FAIL: the whole interval is above it), and a
// cost confidently under it (PASS: the whole interval is below it). An
// interval that straddles the limit is INCONCLUSIVE — the data cannot
// certify the flag either way.
func flagCostVerdict(marginal []time.Duration, limit time.Duration) (out flagcostOutcome, lo, hi time.Duration) {
	if len(marginal) < tailMinSamples {
		return flagcostInsufficient, 0, 0
	}
	lo, hi = flagcostMedianCI(marginal)
	switch {
	case lo > limit:
		return flagcostFail, lo, hi
	case hi < limit:
		return flagcostPass, lo, hi
	default:
		return flagcostInconclusive, lo, hi
	}
}

// --- corpus roots ---

// flagcostCorpusRoots reads RUNECHO_FLAGCOST_CORPUS (colon-separated), or
// skips the test — there is no default fixture corpus; see the file header.
func flagcostCorpusRoots(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("RUNECHO_FLAGCOST_CORPUS")
	if raw == "" {
		t.Skip("RUNECHO_FLAGCOST_CORPUS not set (colon-separated real repo roots) — skipping PR-L latency harness")
	}
	var roots []string
	for _, r := range strings.Split(raw, ":") {
		r = strings.TrimSpace(r)
		if r != "" {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		t.Fatalf("RUNECHO_FLAGCOST_CORPUS=%q contains no non-empty root", raw)
	}
	return roots
}

func TestFlagCost(t *testing.T) {
	roots := flagcostCorpusRoots(t)

	stats := map[string]*flagcostStats{}
	groupPayloads := map[string]int{}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("resolve corpus root %q: %v", root, err)
		}
		top, err := gitutil.TopLevel(absRoot)
		if err != nil {
			t.Fatalf("gitutil.TopLevel(%s): %v (RUNECHO_FLAGCOST_CORPUS roots must be real git repos)", absRoot, err)
		}

		gen := ir.NewGenerator(ir.GeneratorConfig{})
		irData, _, err := gen.Generate(top)
		if err != nil {
			t.Fatalf("generate IR for %s: %v", top, err)
		}

		// enrolledStoreWithFiles (dangling_test.go) sets a fresh RUNECHO_HOME
		// and saves irData.Files as the one snapshot recv-method/var-type/
		// deps-go/dropped-import resolve symbols against — the "real IR,
		// real enrollment" setup the spec calls for, reusing the exact
		// store-plumbing helper the E1 dangling-refs tests already use.
		enrolledStoreWithFiles(t, top, irData.Files)
		flagcostNeutralize(t)

		payloads := flagcostPayloadsForRoot(t, top, irData.Files)
		if len(payloads) == 0 {
			t.Logf("root %s: no eligible .go/.py/.js/.ts payload, skipping", relOrSelf(top))
			continue
		}

		// Warm internal/depindex's on-disk memo ($RUNECHO_HOME/depcache,
		// fresh per root) untimed, so the first timed deps-go run doesn't
		// pay the one-time first-edit-of-the-session cost (depindex/
		// cache.go: "First edit pays full cost, every subsequent edit pays
		// a file read"). The gate is about the steady-state marginal.
		// The memo is keyed by the file's imports, which every payload from
		// one file shares, so one run per file is enough.
		warmed := map[string]bool{}
		for _, p := range payloads {
			if p.lang == guard.LangGo && !warmed[p.file] {
				warmed[p.file] = true
				flagcostRunArm(t, "all-four", p)
			}
		}

		// Timed passes: baseline + language-specific single-flag arms +
		// all-four, per payload, arm order rotated by the payload's index
		// within its group.
		groupIdx := map[string]int{}
		for _, p := range payloads {
			g := flagcostGroup(p.lang, p.kind)
			groupPayloads[g]++
			runs := map[string]flagcostRun{}
			for _, arm := range flagcostArmSequence(p.lang, groupIdx[g]) {
				runs[arm] = flagcostRunArm(t, arm, p)
			}
			groupIdx[g]++
			base := runs["baseline"]
			for arm, run := range runs {
				if arm == "baseline" {
					continue
				}
				key := flagcostBucket(arm, g)
				st := stats[key]
				if st == nil {
					st = &flagcostStats{}
					stats[key] = st
				}
				if !run.ran || !base.ran {
					st.bailed++
					continue
				}
				if run.abstained {
					st.abstained++
				}
				st.on = append(st.on, run.d)
				st.off = append(st.off, base.d)
				st.marginal = append(st.marginal, run.d-base.d)
			}
		}
	}

	const perFlagLimit = 3 * time.Millisecond
	const combinedLimit = 4 * time.Millisecond
	gates := []struct {
		arm, group string
		limit      time.Duration
	}{
		{"recv-method", "go", perFlagLimit},
		{"var-type", "go", perFlagLimit},
		{"deps-go", "go", perFlagLimit},
		{"dropped-import", "py+js", perFlagLimit},
		{"dropped-import", "py+js drop", perFlagLimit},
		// all-four is gated per group: any one over budget means the
		// combined posture is over budget for that kind of edit.
		{"all-four", "go", combinedLimit},
		{"all-four", "py+js", combinedLimit},
		{"all-four", "py+js drop", combinedLimit},
	}
	for _, g := range gates {
		key := flagcostBucket(g.arm, g.group)
		t.Run(key, func(t *testing.T) {
			if groupPayloads[g.group] == 0 {
				t.Skipf("corpus has no %s payloads", g.group)
			}
			st := stats[key]
			if st == nil {
				st = &flagcostStats{}
			}
			out, lo, hi := flagCostVerdict(st.marginal, g.limit)
			tail := tailLabel(len(st.on))
			t.Logf("%s: payloads=%d n=%d bailed=%d abstained=%d  ON p50=%v %s=%v  OFF p50=%v  paired marginal p50=%+v 95%% CI [%+v, %+v]  limit=%v",
				out, groupPayloads[g.group], len(st.marginal), st.bailed, st.abstained,
				percentile(st.on, 50), tail, percentile(st.on, 99), percentile(st.off, 50),
				percentile(st.marginal, 50), lo, hi, g.limit)
			switch out {
			case flagcostPass:
			case flagcostInsufficient:
				// Payloads exist but too few ran the check: fail rather than
				// skip, or a check that bails on every edit would read as free.
				t.Errorf("only %d of %d runs ran the check (want >= %d)",
					len(st.marginal), len(st.marginal)+st.bailed, tailMinSamples)
			case flagcostFail:
				t.Errorf("marginal cost is over the %v limit with 95%% confidence", g.limit)
			case flagcostInconclusive:
				// Fails closed: the gate's claim is "under budget", and a
				// straddling interval cannot support it. The fix is more
				// corpus or a quieter box, not necessarily code.
				t.Errorf("95%% CI straddles the %v limit; add corpus roots or rerun on a quieter machine", g.limit)
			}
		})
	}
}

// --- always-run unit tests (no corpus) ---

// flagcostConstSamples returns n identical durations — a degenerate but
// valid distribution whose median (and every bootstrap median) is exactly d,
// used to test flagCostVerdict's boundaries without real timing noise.
func flagcostConstSamples(n int, d time.Duration) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d
	}
	return out
}

// TestFlagCost_NonVacuous pins the minimum-sample guard: tailMinSamples-1
// marginals must be INSUFFICIENT however small the cost, and tailMinSamples
// must not. A harness that dropped this guard could publish "0 flags over
// budget" from a single lucky run of each arm — the same "presence is not
// verification" trap TestLintDifferential's doc warns about, applied to
// sample count instead of ordering.
func TestFlagCost_NonVacuous(t *testing.T) {
	limit := 3 * time.Millisecond
	if out, _, _ := flagCostVerdict(flagcostConstSamples(tailMinSamples-1, time.Microsecond), limit); out != flagcostInsufficient {
		t.Errorf("%d samples: got %s, want %s", tailMinSamples-1, out, flagcostInsufficient)
	}
	if out, _, _ := flagCostVerdict(flagcostConstSamples(tailMinSamples, time.Microsecond), limit); out != flagcostPass {
		t.Errorf("%d samples, trivial marginal: got %s, want %s", tailMinSamples, out, flagcostPass)
	}
}

// TestFlagCost_GateBoundary pins the three-way verdict: PASS needs the whole
// interval strictly below the limit, FAIL the whole interval strictly above
// it, and anything touching or straddling it is INCONCLUSIVE.
func TestFlagCost_GateBoundary(t *testing.T) {
	limit := 3 * time.Millisecond
	straddle := append(flagcostConstSamples(tailMinSamples/2, limit-time.Millisecond),
		flagcostConstSamples(tailMinSamples/2, limit+time.Millisecond)...)
	for _, tc := range []struct {
		name string
		m    []time.Duration
		want flagcostOutcome
	}{
		{"just under", flagcostConstSamples(tailMinSamples, limit-time.Microsecond), flagcostPass},
		{"exactly at", flagcostConstSamples(tailMinSamples, limit), flagcostInconclusive},
		{"just over", flagcostConstSamples(tailMinSamples, limit+time.Microsecond), flagcostFail},
		{"straddling", straddle, flagcostInconclusive},
		{"negative marginal", flagcostConstSamples(tailMinSamples, -time.Millisecond), flagcostPass},
	} {
		if out, lo, hi := flagCostVerdict(tc.m, limit); out != tc.want {
			t.Errorf("%s: got %s (CI [%v, %v]), want %s", tc.name, out, lo, hi, tc.want)
		}
	}
}

// TestFlagCost_Stratification pins flagcostArmsFor's language gate: a Python
// (or JS) payload must never contribute to recv-method/var-type/deps-go's n,
// and a Go payload must never contribute to dropped-import's n — matching
// each check's own gate in verify.go. Pooling all payloads into every flag's
// arm (the mutation this test exists to catch) would let a corpus with
// mostly Python files pad recv-method's n with same-zero-effect samples and
// mask a real cost the Go-only n would have shown.
func TestFlagCost_Stratification(t *testing.T) {
	goArms := flagcostArmsFor(guard.LangGo)
	for _, want := range []string{"recv-method", "var-type", "deps-go"} {
		if !slices.Contains(goArms, want) {
			t.Errorf("flagcostArmsFor(LangGo) = %v, missing %q", goArms, want)
		}
	}
	if slices.Contains(goArms, "dropped-import") {
		t.Errorf("flagcostArmsFor(LangGo) = %v, must not include dropped-import", goArms)
	}

	for _, lang := range []guard.Lang{guard.LangPython, guard.LangJS} {
		arms := flagcostArmsFor(lang)
		if !slices.Contains(arms, "dropped-import") {
			t.Errorf("flagcostArmsFor(%v) = %v, missing dropped-import", lang, arms)
		}
		for _, mustNot := range []string{"recv-method", "var-type", "deps-go"} {
			if slices.Contains(arms, mustNot) {
				t.Errorf("flagcostArmsFor(%v) = %v, must not include %q", lang, arms, mustNot)
			}
		}
	}
	if arms := flagcostArmsFor(guard.LangUnknown); len(arms) != 0 {
		t.Errorf("flagcostArmsFor(LangUnknown) = %v, want none", arms)
	}
}

// TestFlagCost_ArmSequence pins the rotation: every payload runs baseline,
// each of its language's arms and all-four exactly once, and over len
// consecutive payloads each arm leads exactly once.
func TestFlagCost_ArmSequence(t *testing.T) {
	for _, lang := range []guard.Lang{guard.LangGo, guard.LangPython, guard.LangJS} {
		base := flagcostArmSequence(lang, 0)
		want := append([]string{"baseline"}, flagcostArmsFor(lang)...)
		want = append(want, "all-four")
		if !slices.Equal(base, want) {
			t.Fatalf("%v: n=0 sequence = %v, want %v", lang, base, want)
		}
		sorted := slices.Sorted(slices.Values(base))
		for n := range 2 * len(base) {
			seq := flagcostArmSequence(lang, n)
			if !slices.Equal(slices.Sorted(slices.Values(seq)), sorted) {
				t.Errorf("%v n=%d: %v is not a permutation of %v", lang, n, seq, base)
			}
			if seq[0] != base[n%len(base)] {
				t.Errorf("%v n=%d: leads with %q, want %q", lang, n, seq[0], base[n%len(base)])
			}
		}
	}
}

// TestFlagCost_Bucketing pins which population each payload's samples join:
// Go alone, Python and JS pooled, and a drop payload in its own group.
func TestFlagCost_Bucketing(t *testing.T) {
	for _, tc := range []struct {
		lang guard.Lang
		kind string
		want string
	}{
		{guard.LangGo, "identity", "go"},
		{guard.LangPython, "identity", "py+js"},
		{guard.LangJS, "identity", "py+js"},
		{guard.LangPython, "drop", "py+js drop"},
		{guard.LangJS, "drop", "py+js drop"},
	} {
		if got := flagcostGroup(tc.lang, tc.kind); got != tc.want {
			t.Errorf("flagcostGroup(%v, %q) = %q, want %q", tc.lang, tc.kind, got, tc.want)
		}
	}
	if got := flagcostBucket("var-type", "go"); got != "var-type [go]" {
		t.Errorf("flagcostBucket = %q", got)
	}
}

// TestFlagCost_WindowsSpread pins that windows come from across the file, not
// its first flagcostK*flagcostWindowLines lines, and that a non-unique window
// is never chosen.
func TestFlagCost_WindowsSpread(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	wins := flagcostWindows(b.String(), 20, 3)
	if len(wins) != 3 {
		t.Fatalf("got %d windows, want 3", len(wins))
	}
	if last := wins[len(wins)-1]; !strings.HasPrefix(last, "line 160\n") {
		t.Errorf("last window starts %q, want line 160 (the file's back half)", strings.SplitN(last, "\n", 2)[0])
	}

	dup := strings.Repeat("same\n", 40) + "tail\n"
	for _, w := range flagcostWindows(dup, 20, 3) {
		if strings.Count(dup, w) != 1 {
			t.Errorf("non-unique window chosen: %q", w)
		}
	}
}

// TestFlagCost_DropImport pins the drop payload: a unique single-line import
// is blanked, and a multi-line one (whose partial removal is a syntax error)
// is never picked.
func TestFlagCost_DropImport(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		lang          guard.Lang
		wantOld       string
	}{
		{"py import", "import os\n\nos.getcwd()\n", guard.LangPython, "import os\n"},
		{"py from", "from os import path\npath.join('a')\n", guard.LangPython, "from os import path\n"},
		{"py paren skipped", "from os import (\n    path,\n)\nimport sys\n", guard.LangPython, "import sys\n"},
		{"js named", "import { a } from './a';\na();\n", guard.LangJS, "import { a } from './a';\n"},
		{"js brace skipped", "import {\n  a,\n} from './a';\n", guard.LangJS, ""},
		{"go never", "import \"fmt\"\n", guard.LangGo, ""},
	} {
		old, nu, ok := flagcostDropImport(tc.content, tc.lang)
		wantNew := ""
		if tc.wantOld != "" {
			wantNew = "\n"
		}
		if old != tc.wantOld || ok != (tc.wantOld != "") || nu != wantNew {
			t.Errorf("%s: got (%q, %q, %v), want old %q", tc.name, old, nu, ok, tc.wantOld)
		}
	}
}

// TestFlagCost_DropPayloadBaselineRuns pins the review finding behind
// flagcostDropImport's non-empty new_string: the drop payload's all-flags-off
// baseline must reach the checks, not verifyEdit's empty-input exit, or every
// drop-group marginal is the cost of the whole hook. The dropped-import arm
// must run the check too.
func TestFlagCost_DropPayloadBaselineRuns(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	top := enrolledStore(t, root, []string{"keep"})
	flagcostNeutralize(t)
	content := "import os\n\ndef keep():\n    return os.getcwd()\n"
	file := filepath.Join(top, "m.py")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old, nu, ok := flagcostDropImport(content, guard.LangPython)
	if !ok {
		t.Fatal("no drop payload built")
	}
	p := flagcostPayload{file: file, lang: guard.LangPython, kind: "drop",
		body: payloadOld(t, "Edit", file, old, nu, "", nil)}
	for _, arm := range []string{"baseline", "dropped-import"} {
		if run := flagcostRunArm(t, arm, p); !run.ran {
			t.Errorf("%s arm on a drop payload did not run (early exit or skipped check)", arm)
		}
	}
}
