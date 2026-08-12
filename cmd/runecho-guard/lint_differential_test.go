package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Oracle-differential harness for the gated pre-write lint substrate (#333 P2-2).
//
// The check itself (lint.go) shells out to ruff; a test that also shells out to
// ruff and compares the two would only prove ruff is deterministic. What is
// actually unvalidated is the PLUMBING — whether the payload the hook hands ruff
// is the payload the agent proposed, and whether the finding survives
// suppressAlreadyReported and the ask rendering intact. So this harness drives
// the real runHookMode, parses the emitted ask, and adjudicates against ruff
// invoked independently, on the file as a PATH ARGUMENT rather than through
// stdin. The two invocation shapes are deliberately different: if both sides
// went through --stdin-filename, a stdin-plumbing bug would cancel out on both
// sides of the comparison and read as agreement.
//
// #313 records the trap this is built against: that harness's first cut passed
// the whole file as both the added text and the fold source, and so "reported
// zero false positives while two default-on paths were broken." Two defenses
// here: postures are stratified (see lintPostures) rather than one standing in
// for the other, and every test fails on a vacuous corpus instead of passing.
//
// Lives in package main, not internal/guard where #333 names it. lint.go shipped
// into cmd/runecho-guard, so internal/guard cannot reach lintFindingsWithReason,
// suppressAlreadyReported or runHookMode — a harness there could only exercise a
// second ruff call written for the test, which is the copy-not-the-shipped-path
// failure above. Revisit if #313 moves the lint core into internal/guard.
//
// Corpus: testdata/lintcorpus by default (non-vacuous with zero setup); point
// RUNECHO_LINT_CORPUS at a real Python repository to run it at scale.

// lintPosture is which Write shape a corpus file is fed as. All three send the
// IDENTICAL payload and differ only in what sits on disk at the target path,
// because readFileLines folds an existing file's own definitions into the
// additive check's known set — so the pre-edit file, which ruff never sees, can
// move one side of the comparison and not the other.
//
//   - postureCreate: target absent. The additive check sees only the payload.
//   - postureOverwriteSame: target holds byte-identical content. Folding a file's
//     defs in when the payload already contains those same defs adds nothing, so
//     this MUST come out equal to postureCreate — asserted, not assumed. It is
//     the control that stops the third posture's divergence from being written
//     off as run-to-run noise.
//   - postureStaleFold: target defines exactly the names the payload references
//     but no longer defines. This models an agent rewriting a file and dropping
//     a definition it still calls: the additive check resolves those names out
//     of the stale on-disk copy and goes quiet, while ruff reads the payload as
//     it will actually land and still fires. It is the posture where this
//     check's marginal value is visible at all.
//
// An earlier cut of this harness used only the first two. They agree on every
// file by construction, which reads as "stratification covered" while measuring
// one posture twice — the #313 failure mode, reproduced. The third posture is
// the one that actually separates them.
type lintPosture string

const (
	postureCreate        lintPosture = "create"
	postureOverwriteSame lintPosture = "overwrite-same"
	postureStaleFold     lintPosture = "stale-fold"
)

var lintPostures = []lintPosture{postureCreate, postureOverwriteSame, postureStaleFold}

// lintCorpusMaxDefault caps the default file count so a run against a large
// repository stays inside a normal `go test` timeout. Whatever the cap drops is
// logged by lintCorpusFiles — a silent truncation reads as full coverage.
const lintCorpusMaxDefault = 300

// oracleFinding is one adjudicated ruff result. Symbol is extracted with the
// SAME parser the check uses (lintSymbolFromMessage) on purpose: the oracle's
// job is to adjudicate which findings exist, not to independently re-derive the
// symbol, and a second extractor here would turn a message-format change into a
// harness failure instead of the degrade lint.go documents.
type oracleFinding struct {
	Line   int
	Rule   string
	Symbol string
}

func (f oracleFinding) key() string { return fmt.Sprintf("%d:%s:%s", f.Line, f.Rule, f.Symbol) }

// requireRuff skips when ruff is absent, matching oracle_gopls_client_test.go's
// posture: an external-oracle harness is not a reason for a clean checkout to
// fail. CI installs ruff explicitly (.github/workflows/ci.yml), so the skip does
// not silently drop coverage there.
func requireRuff(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH — the lint differential has no oracle to adjudicate against")
	}
}

// ruffOracle runs ruff against the file on disk, as a path argument. Flags match
// lintFindingsWithReason's except for the input shape, and the same
// lintSelectedRules filter is applied for the same reason (--select does not
// bound ruff's output; it emits invalid-syntax regardless).
//
// The bool reports whether this file could be adjudicated at all. A per-file
// failure drops that ONE file rather than aborting the sweep, matching
// lintCorpusFiles's posture on unreadable subtrees: one permission-denied .py in
// a 750-observation corpus is a property of the corpus, not a harness failure.
// The caller logs what it dropped, and the vacuity guards still fail loudly if a
// systemic break (a ruff output-format change, say) makes EVERY file unusable.
//
// The exit code is NOT how ruff reports an unreadable file, which is the trap
// here. Measured against ruff 0.16.1: a chmod-000 .py that genuinely contains an
// F821 produces exit 0, `[]` on stdout, and `warning: Failed to lint <path>` on
// stderr. Read by exit code alone that is indistinguishable from a clean file,
// so the observation would be counted as real while the oracle never read a byte
// of it — a file silently entering the denominator of "0 false positives over
// 750 observations", the same false-denominator defect as the zero-byte files
// lintCorpusFiles drops, one layer down. stderr is therefore load-bearing and is
// captured explicitly rather than discarded.
// ruffBin is the oracle's executable. A var, not a const, for exactly one
// reason: the unexpected-exit branch below is unreachable through real ruff — it
// warns and exits 0 on I/O errors, and the harness always passes an ABSOLUTE
// path, so no filename can be misread as a flag. Without a substitutable binary
// that branch's claim ("one oddity cannot take the sweep down") is untestable,
// and an untestable claim in this harness is the thing the harness exists to
// object to. Not safe for parallel tests; nothing in this file uses t.Parallel.
var ruffBin = "ruff"

func ruffOracle(t *testing.T, path string) ([]oracleFinding, bool) {
	t.Helper()
	cmd := exec.Command(ruffBin, "check", "--no-cache", "--isolated",
		"--select", lintSelect, "--output-format", "json", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.Bytes()
	if err != nil {
		var exitErr *exec.ExitError
		// Exit 1 is "found violations", ruff's normal reporting exit. Anything
		// else is a harness-level fault (a bad flag, a missing binary) rather
		// than a property of this file, but it is still dropped per-file so one
		// oddity cannot take the sweep down with it.
		if !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
			t.Logf("oracle ruff on %s: %v — file NOT adjudicated, dropped from the corpus", relOrSelf(path), err)
			return nil, false
		}
	}
	if s := strings.TrimSpace(stderr.String()); strings.Contains(s, "Failed to lint") {
		t.Logf("oracle ruff could not read %s (%s) — file NOT adjudicated, dropped from the corpus", relOrSelf(path), s)
		return nil, false
	}
	var raw []struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Location struct {
			Row int `json:"row"`
		} `json:"location"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Logf("oracle ruff output on %s is not JSON: %v — file NOT adjudicated, dropped from the corpus", relOrSelf(path), err)
		return nil, false
	}
	var findings []oracleFinding
	for _, r := range raw {
		if _, ok := lintSelectedRules[r.Code]; !ok {
			continue
		}
		findings = append(findings, oracleFinding{Line: r.Location.Row, Rule: r.Code, Symbol: lintSymbolFromMessage(r.Message)})
	}
	return findings, true
}

// lintCorpusRoot resolves the corpus: RUNECHO_LINT_CORPUS, else the committed
// default. The default exists so the harness says something with zero setup —
// #333 assumed only the env-var form, which would have left this test skipped
// on every machine that never set it.
// The bool reports whether this is the committed default, which is the
// difference between "the oracle is silent because the code is clean" (normal
// for a real repository) and "the oracle is silent because something broke"
// (the only reading available for a corpus built to contain findings).
func lintCorpusRoot(t *testing.T) (string, bool) {
	t.Helper()
	if r := os.Getenv("RUNECHO_LINT_CORPUS"); r != "" {
		abs, err := filepath.Abs(r)
		if err != nil {
			t.Fatalf("resolve RUNECHO_LINT_CORPUS: %v", err)
		}
		return abs, false
	}
	abs, err := filepath.Abs(filepath.Join("testdata", "lintcorpus"))
	if err != nil {
		t.Fatalf("resolve default corpus: %v", err)
	}
	return abs, true
}

// lintCorpusSkipDirs are the trees a real Python repository carries that are not
// its own code. Linting vendored or virtualenv sources would measure the
// packaging ecosystem, not the check.
var lintCorpusSkipDirs = map[string]bool{
	".venv": true, "venv": true, "env": true, ".git": true, "node_modules": true,
	"__pycache__": true, "build": true, "dist": true, ".tox": true,
	".mypy_cache": true, ".ruff_cache": true, "site-packages": true, "vendor": true,
}

// walkLintCorpus walks root for candidate .py files, returning them unsorted
// alongside the count of zero-byte files skipped.
//
// Split out of lintCorpusFiles so the root-error path is reachable from a test.
// lintCorpusFiles reports every failure through t.Fatalf, and a test cannot tell
// one fatal from another — which is exactly how the swallowed root error stayed
// invisible: it surfaced as the vacuous-corpus fatal, a message about a
// different fault entirely.
func walkLintCorpus(root string) ([]string, int, error) {
	var all []string
	empty := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// The ROOT failing is a different fault from a subtree failing, and
			// swallowing it sent a typo'd RUNECHO_LINT_CORPUS all the way to
			// "contains no .py files — the harness would pass vacuously", which
			// points the reader at the corpus when the fault is the env var.
			if path == root {
				return err
			}
			return nil // unreadable subtree: skip, don't abort the corpus
		}
		if d.IsDir() {
			if lintCorpusSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		// Zero-byte files are dropped, not measured. `payload` omits
		// tool_input.content when the string is empty, so runHookMode takes its
		// `empty-input` fast return and never reaches ruff, guard.Run or the ask
		// — the observation costs ~130us instead of ~25ms and adjudicates
		// nothing. Empty `__init__.py` is ubiquitous (18 of 381 .py files in
		// one real corpus, 21 of 453 in another — counted with the exclusions
		// this walker actually applies, not a bare `find`, which inflated the
		// second figure by counting the vendored trees skipped above), so
		// keeping them would pad the
		// observation count with non-observations and drag the latency
		// distribution toward zero. Counted and logged rather than silently
		// filtered: a corpus that is mostly empty files should say so.
		if info, statErr := d.Info(); statErr == nil && info.Size() == 0 {
			empty++
			return nil
		}
		all = append(all, path)
		return nil
	})
	return all, empty, err
}

// lintCorpusFiles walks the corpus for .py files, capped at
// RUNECHO_LINT_CORPUS_MAX (default lintCorpusMaxDefault). Sorted so a capped run
// is reproducible rather than filesystem-order-dependent, and the drop count is
// logged.
func lintCorpusFiles(t *testing.T, root string) []string {
	t.Helper()
	all, empty, err := walkLintCorpus(root)
	if err != nil {
		t.Fatalf("walk corpus %s: %v", root, err)
	}
	if empty > 0 {
		t.Logf("corpus %s: skipped %d zero-byte .py file(s) — the hook's empty-input fast return would measure nothing for them", root, empty)
	}
	sort.Strings(all)

	max := lintCorpusMaxDefault
	if v := os.Getenv("RUNECHO_LINT_CORPUS_MAX"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			t.Fatalf("RUNECHO_LINT_CORPUS_MAX=%q is not a positive integer", v)
		}
		max = n
	}
	if len(all) > max {
		t.Logf("corpus %s: %d .py files found, capping at %d — %d NOT measured (raise RUNECHO_LINT_CORPUS_MAX to cover them)",
			root, len(all), max, len(all)-max)
		all = all[:max]
	}
	if len(all) == 0 {
		t.Fatalf("corpus %s contains no .py files — the harness would pass vacuously", root)
	}
	return all
}

// lintDiffEnv stands up the one git repo + enrolled store every corpus file is
// driven through, and neutralises every gated check EXCEPT lint. Without that
// neutralisation an ambient RUNECHO_GUARD_* in the developer's shell (this
// repo's own dogfood window sets RUNECHO_GUARD_LINT=1 globally) would leak into
// the measurement and the guard-only partition would silently include checks the
// harness never meant to compare against.
//
// Returns the .py path inside the repo that every payload targets. One fixed
// path, so the store and index state the additive check resolves against is
// constant across the corpus and only the payload varies.
func lintDiffEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{
		"RUNECHO_GUARD_SKIP", "RUNECHO_GUARD_STRICT", "RUNECHO_GUARD_DANGLING",
		"RUNECHO_GUARD_DROPPED_IMPORT", "RUNECHO_GUARD_DUPLICATE",
		"RUNECHO_GUARD_CALLSHAPE", "RUNECHO_GUARD_RECVMETHOD", "RUNECHO_GUARD_VARTYPE",
		"RUNECHO_GUARD_FILESCOPE", "RUNECHO_GUARD_DEPS_GO",
		"RUNECHO_GUARD_CONTRACT", "RUNECHO_GUARD_LEARN",
	} {
		t.Setenv(k, "")
	}
	// RUNECHO_GUARD_QUALIFIED is the ONE default-on gate: qualifiedEnabled() is
	// `!= "0"`, so clearing it above would have left the check running. Audited
	// against every RUNECHO_GUARD_* read in the package — it is currently the
	// only inverted one, and a future default-on check added to the list above
	// would silently stay on the same way.
	t.Setenv("RUNECHO_GUARD_QUALIFIED", "0")
	root := t.TempDir()
	gitInit(t, root)
	// enrolledStore sets RUNECHO_HOME; the symbol set is deliberately unrelated
	// to any corpus file so nothing resolves out of the index by accident.
	top := enrolledStore(t, root, []string{"IndexedUnrelatedSymbol"})
	return filepath.Join(top, "subject.py")
}

var lintAskLineRe = regexp.MustCompile(`^  line (\d+): (F\d+) (.*)$`)

// parseLintSection pulls the lint findings back out of the rendered ask. The
// decision log would be cheaper to read, but it pools every check's symbols into
// one Symbols array — it cannot say which check reported what, and it carries no
// line or rule, so a finding attributed to the wrong line would pass. The ask
// text is the only place the per-check attribution survives.
func parseLintSection(ask string) []lintFinding {
	lines := strings.Split(ask, "\n")
	var out []lintFinding
	in := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "[runecho-guard] ") && strings.Contains(ln, "ruff finding(s)") {
			in = true
			continue
		}
		if !in {
			continue
		}
		m := lintAskLineRe.FindStringSubmatch(ln)
		if m == nil {
			break // section ended: the next header, or the trailer
		}
		n, _ := strconv.Atoi(m[1])
		out = append(out, lintFinding{Line: n, Rule: m[2], Message: m[3], Symbol: lintSymbolFromMessage(m[3])})
	}
	return out
}

// pyIdentRe matches a name safe to emit into synthetic Python source. Anything
// the oracle names that is not a plain identifier (a dotted attribute, or
// punctuation from a message this parser did not understand) is dropped rather
// than written out — a stale-fold file that does not parse would silence the
// additive check for the wrong reason and the posture would prove nothing.
var pyIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// staleFoldSource builds the pre-edit file for postureStaleFold: a module-level
// def for every name the oracle reports as F821 in the payload. `def` rather
// than an assignment because a def is unambiguously a definition to every
// language's extractor, and the point is to make addInFileDefs resolve the name.
//
// F811 findings are deliberately NOT seeded. A redefinition is about the payload
// alone; the additive check never had anything to say about it, so seeding one
// would change nothing and only make the posture harder to read.
func staleFoldSource(oracle []oracleFinding) string {
	var sb strings.Builder
	sb.WriteString("# synthetic pre-edit state: these definitions exist on disk and the\n")
	sb.WriteString("# proposed Write no longer contains them.\n")
	seen := map[string]bool{}
	for _, f := range oracle {
		if f.Rule != "F821" || seen[f.Symbol] || !pyIdentRe.MatchString(f.Symbol) {
			continue
		}
		seen[f.Symbol] = true
		fmt.Fprintf(&sb, "def %s(*a, **k):\n    return None\n\n", f.Symbol)
	}
	return sb.String()
}

// lintObservation is one corpus file, one posture, run both ways.
type lintObservation struct {
	file     string
	posture  lintPosture
	oracle   []oracleFinding
	reported []lintFinding       // lint section of the ask, lint ON
	other    map[string]struct{} // symbols the OTHER checks flagged, lint OFF
	withLint time.Duration
	noLint   time.Duration
}

// observe drives the hook twice over the same payload: once with the lint check
// enabled, once without. The difference between the two runs is the whole
// measurement — the lint-off run's decision-log symbols are exactly the set
// suppressAlreadyReported suppresses against, derived by running the code rather
// than by re-implementing it.
//
// offFirst alternates which side runs first. Whichever runs first pays this
// payload's cold-cache costs (the file read, ruff's own page-in, git), and with
// a fixed ON-then-OFF order every one of those is charged to the lint check and
// biases the published marginal cost upward. An instrumented run failed to
// detect the effect (cold-position OFF p50 11.4 ms vs warm-position 11.3 ms), so
// this is cheap insurance rather than a correction of a measured error —
// alternating costs nothing and removes the argument.
func observe(t *testing.T, target, file string, posture lintPosture, oracle []oracleFinding, offFirst bool) lintObservation {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read corpus file %s: %v", file, err)
	}
	switch posture {
	case postureCreate:
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			t.Fatalf("clear target for create posture: %v", err)
		}
	case postureOverwriteSame:
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatalf("seed target for overwrite-same posture: %v", err)
		}
	case postureStaleFold:
		if err := os.WriteFile(target, []byte(staleFoldSource(oracle)), 0o644); err != nil {
			t.Fatalf("seed target for stale-fold posture: %v", err)
		}
	}
	body := payload(t, "Write", target, "", string(content), nil)

	runWith := func(lintOn bool) (string, time.Duration) {
		if lintOn {
			t.Setenv("RUNECHO_GUARD_LINT", "1")
		} else {
			t.Setenv("RUNECHO_GUARD_LINT", "")
		}
		start := time.Now()
		_, _, res := runHook(t, body)
		return res.Hook.PermissionReason, time.Since(start)
	}

	// The decision log is truncated immediately BEFORE the lint-off run and read
	// immediately AFTER it, so the record read is unambiguously that run's under
	// either ordering. The log is append-only and every observation targets the
	// SAME path, so a run that logs nothing (any early return that skips
	// logDecision) would otherwise leave the PREVIOUS file's symbols as the last
	// line, and its `file` field would match — the stale read would be
	// undetectable and every downstream partition would be attributed to the
	// wrong corpus file.
	var ask string
	var onDur, offDur time.Duration
	other := map[string]struct{}{}
	readOther := func() {
		if rec := readLastDecisionLog(t); rec != nil {
			if syms, ok := rec["symbols"].([]any); ok {
				for _, s := range syms {
					if str, ok := s.(string); ok {
						other[str] = struct{}{}
					}
				}
			}
		}
	}
	if offFirst {
		resetDecisionLog(t)
		_, offDur = runWith(false)
		readOther()
		ask, onDur = runWith(true)
	} else {
		ask, onDur = runWith(true)
		resetDecisionLog(t)
		_, offDur = runWith(false)
		readOther()
	}

	return lintObservation{
		file:     file,
		posture:  posture,
		oracle:   oracle,
		reported: parseLintSection(ask),
		other:    other,
		withLint: onDur,
		noLint:   offDur,
	}
}

// resetDecisionLog removes decisions.jsonl from the current RUNECHO_HOME.
func resetDecisionLog(t *testing.T) {
	t.Helper()
	home := os.Getenv("RUNECHO_HOME")
	if home == "" {
		t.Fatal("RUNECHO_HOME is unset — lintDiffEnv did not run, and the decision log being read belongs to the developer's real store")
	}
	if err := os.Remove(filepath.Join(home, "decisions.jsonl")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("reset decision log: %v", err)
	}
}

// corpusRun is one full sweep, shared by every subtest. Collected once: each
// observation is two runHookMode invocations, so re-sweeping per assertion
// tripled the cost of exactly the large-corpus runs the harness exists for.
type corpusRun struct {
	obs       []lintObservation
	isDefault bool
}

// observeCorpus runs every corpus file through every posture.
func observeCorpus(t *testing.T) corpusRun {
	t.Helper()
	requireRuff(t)
	root, isDefault := lintCorpusRoot(t)
	files := lintCorpusFiles(t, root)
	target := lintDiffEnv(t)

	// Adjudicate each file ONCE. The oracle reads the corpus file, which no
	// posture mutates, so re-running ruff per posture would only add three
	// chances for a flake to look like a divergence. A file the oracle cannot
	// adjudicate is dropped from the sweep entirely rather than observed against
	// a nil oracle, which would read as "ruff found nothing here".
	oracles := make(map[string][]oracleFinding, len(files))
	adjudicated := make([]string, 0, len(files))
	var dropped []string
	for _, f := range files {
		found, ok := ruffOracle(t, f)
		if !ok {
			dropped = append(dropped, relOrSelf(f))
			continue
		}
		oracles[f] = found
		adjudicated = append(adjudicated, f)
	}
	if len(dropped) > 0 {
		t.Logf("corpus %s: %d of %d file(s) could not be adjudicated and are NOT measured: %v",
			root, len(dropped), len(files), dropped)
	}
	if len(adjudicated) == 0 {
		t.Fatalf("corpus %s: not one of %d .py file(s) could be adjudicated by ruff — the harness would pass vacuously", root, len(files))
	}
	files = adjudicated

	obs := make([]lintObservation, 0, len(files)*len(lintPostures))
	for _, posture := range lintPostures {
		for i, f := range files {
			// Alternate which of the two runs goes first; see observe. Keyed on
			// the file index so the assignment is deterministic and a re-run
			// measures the same thing.
			obs = append(obs, observe(t, target, f, posture, oracles[f], i%2 == 1))
		}
	}
	t.Logf("corpus %s (default=%v): %d file(s) x %d posture(s) = %d observations", root, isDefault, len(files), len(lintPostures), len(obs))
	return corpusRun{obs: obs, isDefault: isDefault}
}

// TestLintDifferential is the whole harness: one corpus sweep, three readings of
// it. Subtests rather than three top-level tests so the sweep is paid for once;
// run one in isolation with -run 'TestLintDifferential/latency'.
func TestLintDifferential(t *testing.T) {
	run := observeCorpus(t)
	t.Run("posture_fidelity", func(t *testing.T) { checkPostureFidelity(t, run) })
	t.Run("overlap_partition", func(t *testing.T) { checkOverlapPartition(t, run) })
	t.Run("latency", func(t *testing.T) { checkLatency(t, run.obs) })
}

// checkPostureFidelity is the assertion half: whatever the hook reports must be
// something the oracle also found, and whatever the oracle found must either be
// reported or be provably suppressed as a duplicate of another check's finding.
// A finding in neither place is a payload-plumbing bug — the hook handed ruff
// something other than what the agent proposed.
func checkPostureFidelity(t *testing.T, run corpusRun) {
	obs := run.obs
	totalOracle, totalReported := 0, 0
	for _, o := range obs {
		totalOracle += len(o.oracle)
		totalReported += len(o.reported)

		// MULTISETS, not sets, on both sides. Set-keyed counting let the hook
		// render the same finding twice and still pass in both directions: the
		// duplicate matched an oracle key that had already been matched, and
		// nothing bounded totalReported against totalOracle. Comparing
		// occurrence counts per key is strictly stronger than that global bound
		// and localises the failure to a file and a line.
		oracleCount := map[string]int{}
		for _, f := range o.oracle {
			oracleCount[f.key()]++
		}
		reportedCount := map[string]int{}
		reportedByKey := map[string]lintFinding{}
		for _, r := range o.reported {
			k := oracleFinding{Line: r.Line, Rule: r.Rule, Symbol: r.Symbol}.key()
			reportedCount[k]++
			reportedByKey[k] = r
		}

		// L subset of R, matched on line AND rule AND symbol, not just count:
		// a finding attributed to the wrong line is a defect a count comparison
		// cannot see. Iterated in sorted key order so a corpus that fails
		// produces the same failure list every run.
		for _, k := range sortedCountKeys(reportedCount) {
			r, got, want := reportedByKey[k], reportedCount[k], oracleCount[k]
			switch {
			case want == 0:
				t.Errorf("%s [%s]: hook reported %s at line %d for %q, which the oracle did not find (oracle: %v)",
					relOrSelf(o.file), o.posture, r.Rule, r.Line, r.Symbol, o.oracle)
			case got > want:
				t.Errorf("%s [%s]: hook rendered %s at line %d for %q %d time(s) but the oracle found it %d — the same finding is being double-counted",
					relOrSelf(o.file), o.posture, r.Rule, r.Line, r.Symbol, got, want)
			}
		}

		// Suppression is ASSERTED here, not merely tolerated.
		//
		// The reverse direction below treats a symbol in o.other as an excuse to
		// SKIP an unreported oracle finding, which is not an assertion about
		// suppression at all: if suppressAlreadyReported did nothing, the
		// finding would simply arrive reported and match, and every other
		// assertion in this file would stay green. Mutation-checked — replacing
		// the suppressAlreadyReported call in main.go with a no-op leaves this
		// harness fully green while a real hallucination is printed under two
		// headers and double-counted in decisionRecord.Symbols. That is the
		// exact "presence is not verification" class this harness was written
		// against, so the invariant is stated directly: a REPORTED lint finding
		// whose symbol another check also flagged for this same edit IS the
		// double-count suppressAlreadyReported exists to prevent.
		//
		// Guarded on Symbol != "" because lint.go documents itself as never
		// suppressing an unparsed message — an unparsed message cannot be proven
		// a duplicate — so asserting there would demand behaviour the check
		// deliberately does not have.
		for _, r := range o.reported {
			if r.Symbol == "" {
				continue
			}
			if _, dup := o.other[r.Symbol]; dup {
				t.Errorf("%s [%s]: hook reported %s %q at line %d, but another check already flagged %q for this same edit — suppressAlreadyReported should have dropped it (other checks: %v)",
					relOrSelf(o.file), o.posture, r.Rule, r.Symbol, r.Line, r.Symbol, sortedKeys(o.other))
			}
		}

		// R \ A subset of L: an oracle finding may go unreported ONLY because
		// suppressAlreadyReported dropped it against another check's symbol.
		// Keyed on line+rule+symbol, matching the forward direction. A bare
		// symbol set would let a hook finding for `helper` at line 3 stand in
		// for an oracle finding for `helper` at line 40, so a regression that
		// reports the right name at the wrong line — for one of several
		// occurrences — would pass. The suppression set stays symbol-keyed
		// because suppressAlreadyReported compares on Symbol.
		for _, f := range o.oracle {
			if reportedCount[f.key()] > 0 {
				continue
			}
			if _, dup := o.other[f.Symbol]; dup {
				continue // legitimately suppressed as a duplicate
			}
			t.Errorf("%s [%s]: oracle found %s %q at line %d but the hook neither reported nor suppressed it (reported: %v, other checks: %v)",
				relOrSelf(o.file), o.posture, f.Rule, f.Symbol, f.Line, o.reported, sortedKeys(o.other))
		}
	}

	assertPostureControl(t, obs)

	// Vacuity has two very different causes and they must not share a verdict.
	//
	// An oracle that found nothing on the COMMITTED DEFAULT corpus is broken:
	// that corpus is built to contain four findings, so silence there means the
	// harness, the corpus or ruff changed under it.
	//
	// An oracle that found nothing on a corpus someone pointed at is the normal
	// state of working code — committed Python that trips F821/F811 is a bug
	// someone already fixed. That run still asserts something real (the check
	// reported nothing either, i.e. zero false positives) but it CANNOT speak to
	// whether a true finding survives the plumbing, and saying so out loud is
	// the whole point: "0 false positives over 750 observations" is the exact
	// sentence a completely disabled check also produces.
	if totalOracle == 0 {
		if run.isDefault {
			t.Fatalf("oracle found nothing across %d observations of the committed default corpus — it is built to contain findings, so this is a harness/corpus regression, not clean code", len(obs))
		}
		t.Logf("oracle silent across %d observations: this corpus is F821/F811-clean, so the run validates FALSE-POSITIVE freedom (hook reported %d) and latency ONLY — it does not exercise a true finding end-to-end", len(obs), totalReported)
		return
	}
	if totalReported == 0 {
		t.Fatalf("the hook reported no lint finding across %d observations (oracle found %d) — the check is wired off or its ask is not rendering",
			len(obs), totalOracle)
	}
	// Guarded on t.Failed: t.Errorf does not stop the loop above, so this line
	// printed "posture fidelity OK" underneath its own failures.
	if !t.Failed() {
		t.Logf("posture fidelity OK: oracle %d finding(s), hook reported %d, remainder suppressed as duplicates", totalOracle, totalReported)
	}
}

// assertPostureControl pins the invariant that makes postureStaleFold's
// divergence readable: a Write whose payload equals the file already on disk
// must produce exactly what the same Write to an absent path produces. The
// on-disk fold can only contribute definitions the payload already carries.
//
// Without this, "create and stale-fold differ" is indistinguishable from
// run-to-run instability in the harness itself.
func assertPostureControl(t *testing.T, obs []lintObservation) {
	t.Helper()
	type k struct {
		file    string
		posture lintPosture
	}
	index := map[k]lintObservation{}
	for _, o := range obs {
		index[k{o.file, o.posture}] = o
	}
	compared := 0
	for _, o := range obs {
		if o.posture != postureCreate {
			continue
		}
		same, ok := index[k{o.file, postureOverwriteSame}]
		if !ok {
			continue
		}
		compared++
		if a, b := findingKeys(o.reported), findingKeys(same.reported); !slices.Equal(a, b) {
			t.Errorf("%s: lint findings differ between create (%v) and overwrite-same (%v) — a byte-identical on-disk file must not change the answer",
				relOrSelf(o.file), a, b)
		}
		if a, b := sortedKeys(o.other), sortedKeys(same.other); !slices.Equal(a, b) {
			t.Errorf("%s: other-check symbols differ between create (%v) and overwrite-same (%v) — the fold added something the payload did not already define",
				relOrSelf(o.file), a, b)
		}
	}
	if compared == 0 {
		t.Fatal("posture control compared nothing — create/overwrite-same pairs are missing from the observation set")
	}
}

func findingKeys(fs []lintFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, oracleFinding{Line: f.Line, Rule: f.Rule, Symbol: f.Symbol}.key())
	}
	sort.Strings(out)
	return out
}

// checkOverlapPartition measures what the check adds and what it misses, per
// posture. It asserts only non-vacuity: the partition is a number to read, not a
// threshold to hold, and pinning one now would freeze a measurement taken before
// the dogfood window has any data.
func checkOverlapPartition(t *testing.T, run corpusRun) {
	obs := run.obs

	type bucket struct {
		both, ruffOnly, guardOnlyAbstain, guardOnlyClean int
		cleanSample, abstainFiles                        map[string]struct{}
	}
	byPosture := map[lintPosture]*bucket{}

	for _, o := range obs {
		b := byPosture[o.posture]
		if b == nil {
			b = &bucket{cleanSample: map[string]struct{}{}, abstainFiles: map[string]struct{}{}}
			byPosture[o.posture] = b
		}
		// Star imports are the one construct verified to silence F821 (pyflakes
		// answers F403/F405 there instead). exec/eval/globals() do NOT silence
		// it — checked against ruff 0.16.1 rather than assumed, because
		// classifying a live disagreement as "oracle abstained" would launder a
		// real divergence into a non-finding.
		abstains := oracleAbstains(t, o.file)

		oracleSyms := map[string]struct{}{}
		for _, f := range o.oracle {
			oracleSyms[f.Symbol] = struct{}{}
		}
		for s := range oracleSyms {
			if _, ok := o.other[s]; ok {
				b.both++
			} else {
				b.ruffOnly++
			}
		}
		for s := range o.other {
			if _, ok := oracleSyms[s]; ok {
				continue
			}
			if abstains {
				b.guardOnlyAbstain++
				b.abstainFiles[relOrSelf(o.file)] = struct{}{}
			} else {
				b.guardOnlyClean++
				b.cleanSample[cleanSampleKey(s, o.file)] = struct{}{}
			}
		}
	}

	total := 0
	for _, p := range lintPostures {
		b := byPosture[p]
		if b == nil {
			continue
		}
		total += b.both + b.ruffOnly + b.guardOnlyAbstain + b.guardOnlyClean
		t.Logf("[%s] both=%d ruff-only=%d guard-only/oracle-abstains=%d guard-only/oracle-clean=%d",
			p, b.both, b.ruffOnly, b.guardOnlyAbstain, b.guardOnlyClean)
		// Name the divergent symbols, capped. A bare count cannot be triaged;
		// the names are what turn this table into the next investigation.
		// The abstain bucket is file-scoped because pyflakes' abstention is:
		// verified against ruff 0.16.1, a single `from x import *` silences F821
		// for the WHOLE module, including names that are plainly undefined
		// (`definitely_not_defined_anywhere()` goes unreported). So the scope is
		// right, but the bucket is still the one this harness does not
		// investigate — name the files so it can be.
		//
		// The counts above are OCCURRENCES; the lists below are DISTINCT. They
		// were previously logged adjacently under names that implied one
		// explained the other, so a reader comparing `guard-only/oracle-clean=41`
		// against a 12-name list had no way to know the two were counting
		// different things. Both cardinalities are now stated on the same line.
		if files := sortedKeys(b.abstainFiles); len(files) > 0 {
			t.Logf("[%s]   oracle-abstains: %d occurrence(s) over %d distinct file(s)", p, b.guardOnlyAbstain, len(files))
			if len(files) > 10 {
				t.Logf("[%s]   oracle-abstains files (first 10 of %d): %v", p, len(files), files[:10])
			} else {
				t.Logf("[%s]   oracle-abstains files: %v", p, files)
			}
		}
		if names := sortedKeys(b.cleanSample); len(names) > 0 {
			t.Logf("[%s]   oracle-clean: %d occurrence(s) over %d distinct symbol@file pair(s)", p, b.guardOnlyClean, len(names))
			if len(names) > 20 {
				t.Logf("[%s]   oracle-clean symbol@file (first 20 of %d): %v", p, len(names), names[:20])
			} else {
				t.Logf("[%s]   oracle-clean symbol@file: %v", p, names)
			}
		}
	}
	// Stated once, so the numbers above are not over-read: guard-only/oracle-clean
	// is NOT a guard false-positive count. F821 binds a name on any `import x`
	// whether or not the target module or attribute exists; the guard asks
	// whether the symbol is in the index. Ruff's silence there is a weaker
	// question answered, not a contradiction.
	t.Logf("note: guard-only/oracle-clean counts divergence, not guard error — F821's binding model is weaker than the index question")

	if total == 0 {
		// Same split as posture fidelity: empty on the committed corpus is a
		// regression; empty on someone's clean repository is just a quiet repo.
		if run.isDefault {
			t.Fatalf("the partition is empty across %d observations of the committed default corpus — it is built to populate it", len(obs))
		}
		t.Logf("partition empty across %d observations: neither the oracle nor any check said anything about this corpus", len(obs))
	}
}

// cleanSampleKey identifies one guard-only/oracle-clean divergence, keyed on
// symbol AND file. This bucket is what TECHNICAL.md calls the next
// investigation, and a bare symbol key failed it twice over: it did not say
// which file to open, and it collapsed the same name appearing in two files into
// one entry, so the distinct count silently under-reported the spread.
func cleanSampleKey(symbol, file string) string {
	return symbol + " @ " + relOrSelf(file)
}

// oracleAbstains reports whether the file contains a star import, the construct
// verified to make F821 stop reporting undefined names.
//
// The trailing comment is stripped before matching. Requiring the line to END
// with " import *" read `from os.path import *  # noqa: F403` as NON-abstaining,
// so ruff's module-wide silence on that file landed in guard-only/oracle-clean —
// the bucket TECHNICAL.md calls a real divergence to investigate. The committed
// star_import.py classified correctly only because its star import happens to
// end the line, so no fixture caught it.
//
// Stripping at the first `#` can over-abstain on a line inside a triple-quoted
// string that both starts with `from ` and contains a commented-out star import.
// That direction is the safe one: this classifier only decides which bucket an
// already-counted divergence is logged under, and over-abstaining under-reports
// the bucket nothing asserts against.
func oracleAbstains(t *testing.T, file string) bool {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(ln, '#'); i >= 0 {
			ln = ln[:i]
		}
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ln), ";"))
		if strings.HasPrefix(trimmed, "from ") && strings.HasSuffix(trimmed, " import *") {
			return true
		}
	}
	return false
}

// TestRuffOracle_UnreadableFileIsNotAdjudicated pins the stderr check.
//
// The readable control runs FIRST and asserts the fixture really does produce a
// finding. Without it, `ok == false` on the unreadable pass would also hold for
// a fixture ruff simply had nothing to say about, and the test would pass while
// asserting nothing — which is the failure mode this whole harness exists to
// catch, so it is not one to reproduce in the harness's own tests.
func TestRuffOracle_UnreadableFileIsNotAdjudicated(t *testing.T) {
	requireRuff(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads, so the unreadable case cannot be staged")
	}
	path := filepath.Join(t.TempDir(), "locked.py")
	if err := os.WriteFile(path, []byte("def f():\n    return undefined_thing\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	found, ok := ruffOracle(t, path)
	if !ok || len(found) != 1 || found[0].Rule != "F821" {
		t.Fatalf("readable control: got ok=%v findings=%v, want one F821 — the fixture must be adjudicable for the unreadable case below to mean anything", ok, found)
	}

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	found, ok = ruffOracle(t, path)
	if ok {
		t.Errorf("an unreadable file was adjudicated (findings=%v) — ruff exits 0 with `[]` on stdout and only warns on stderr, so this file enters the corpus as a clean observation the oracle never read, padding the denominator of every false-positive claim", found)
	}
}

// TestRuffOracle_UnexpectedExitDropsOneFileNotTheSweep pins the exit-code
// branch, which real ruff cannot reach (see ruffBin). A stub is the only way to
// stage it, and staging it is worth doing: the alternative to this branch
// dropping one file is a t.Fatalf that ends a 750-observation sweep over a
// single anomaly.
func TestRuffOracle_UnexpectedExitDropsOneFileNotTheSweep(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "ruff-stub")
	// Exit 2 with well-formed empty JSON on stdout: the point is that the EXIT
	// CODE alone must condemn the observation. A stub that also emitted garbage
	// would be killed by the JSON check instead, and the branch under test would
	// stay unexercised.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho '[]'\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write ruff stub: %v", err)
	}
	defer func(old string) { ruffBin = old }(ruffBin)
	ruffBin = stub

	py := filepath.Join(dir, "x.py")
	if err := os.WriteFile(py, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if found, ok := ruffOracle(t, py); ok {
		t.Errorf("an oracle exit this harness does not understand was treated as a valid adjudication (findings=%v) — the file enters the corpus as clean on the strength of an error", found)
	}
}

// TestRuffOracle_BenignStderrStillAdjudicates is the other side of the stderr
// check. "Any stderr output means unadjudicable" would also pass the unreadable
// test above, and would silently shrink the corpus every time ruff emitted a
// deprecation or unused-noqa warning — a quiet loss of coverage that reads as a
// smaller corpus rather than as a fault. Only a read FAILURE condemns the file.
func TestRuffOracle_BenignStderrStillAdjudicates(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "ruff-stub")
	stubSrc := `#!/bin/sh
echo "warning: unused noqa directive (non-enabled: F401)" >&2
echo '[{"code":"F821","message":"Undefined name missing_helper","location":{"row":3}}]'
exit 1
`
	if err := os.WriteFile(stub, []byte(stubSrc), 0o755); err != nil {
		t.Fatalf("write ruff stub: %v", err)
	}
	defer func(old string) { ruffBin = old }(ruffBin)
	ruffBin = stub

	py := filepath.Join(dir, "x.py")
	if err := os.WriteFile(py, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	found, ok := ruffOracle(t, py)
	if !ok {
		t.Fatalf("a file ruff adjudicated fine was dropped because ruff also printed a warning — every such warning would silently shrink the corpus")
	}
	if len(found) != 1 || found[0].Rule != "F821" || found[0].Line != 3 {
		t.Errorf("got %v, want one F821 at line 3", found)
	}
}

// TestWalkLintCorpus_RootErrorSurfaces pins BOTH halves of the root-vs-subtree
// split. A fix that surfaced every walk error, not just the root's, would make
// any real repository with one unreadable directory fail outright — so the
// subtree case is asserted alongside, not assumed.
func TestWalkLintCorpus_RootErrorSurfaces(t *testing.T) {
	t.Run("missing root is an error", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-corpus")
		files, _, err := walkLintCorpus(missing)
		if err == nil {
			t.Fatalf("walking a nonexistent root returned no error (%d file(s)) — a typo'd RUNECHO_LINT_CORPUS then reports `contains no .py files`, blaming the corpus for a fault in the env var", len(files))
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("got %v, want a not-exist error", err)
		}
	})

	t.Run("unreadable subtree is skipped, not fatal", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: chmod 000 does not deny traversal")
		}
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "good.py"), []byte("x = 1\n"), 0o644); err != nil {
			t.Fatalf("write good.py: %v", err)
		}
		blocked := filepath.Join(root, "blocked")
		if err := os.MkdirAll(blocked, 0o755); err != nil {
			t.Fatalf("mkdir blocked: %v", err)
		}
		if err := os.WriteFile(filepath.Join(blocked, "inner.py"), []byte("y = 2\n"), 0o644); err != nil {
			t.Fatalf("write inner.py: %v", err)
		}
		if err := os.Chmod(blocked, 0o000); err != nil {
			t.Fatalf("chmod blocked: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

		files, _, err := walkLintCorpus(root)
		if err != nil {
			t.Fatalf("one unreadable subtree aborted the whole walk: %v — a real repository with a single unreadable directory would never be measurable", err)
		}
		if len(files) != 1 || filepath.Base(files[0]) != "good.py" {
			t.Errorf("got %v, want just good.py", files)
		}
	})
}

// TestCleanSampleKey_CarriesFileAttribution pins the oracle-clean bucket's key.
func TestCleanSampleKey_CarriesFileAttribution(t *testing.T) {
	alpha := cleanSampleKey("helper", filepath.Join("pkg", "alpha.py"))
	beta := cleanSampleKey("helper", filepath.Join("pkg", "beta.py"))
	if alpha == beta {
		t.Errorf("the same symbol in two files collapsed to one key (%q) — the bucket cannot be triaged without knowing which file, and the distinct count under-reports the spread", alpha)
	}
	if !strings.Contains(alpha, "helper") {
		t.Errorf("key %q dropped the symbol", alpha)
	}
	if !strings.Contains(alpha, "alpha.py") {
		t.Errorf("key %q dropped the file", alpha)
	}
}

// TestOracleAbstains_TrailingComment pins the classifier fix. The committed
// star_import.py cannot catch this: its star import ends the line, which is
// exactly why the suffix-only match survived review. Table-driven rather than a
// new corpus fixture because a corpus file would also have to be adjudicated by
// ruff and would change the published observation counts to test a log label.
func TestOracleAbstains_TrailingComment(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"plain star import", "from os.path import *\n", true},
		{"star import with noqa", "from os.path import *  # noqa: F403\n", true},
		{"star import with trailing semicolon", "from os.path import *;\n", true},
		{"indented star import", "    from os.path import *\n", true},
		{"plain import is not a star import", "from os.path import join\n", false},
		{"commented-out star import only", "# from os.path import *\n", false},
		{"no imports at all", "x = 1\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.py")
			if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
				t.Fatalf("write sample: %v", err)
			}
			if got := oracleAbstains(t, path); got != c.want {
				t.Errorf("oracleAbstains(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// checkLatency records p50 and p99 of the gated path with the lint check on and
// off. The delta is the number that matters: an absolute p50 is dominated by
// store resolution and git shell-outs, so quoting it would overstate the check's
// cost and hide a regression inside the noise.
func checkLatency(t *testing.T, obs []lintObservation) {
	on := make([]time.Duration, 0, len(obs))
	off := make([]time.Duration, 0, len(obs))
	for _, o := range obs {
		on = append(on, o.withLint)
		off = append(off, o.noLint)
	}
	if len(on) == 0 {
		t.Fatal("no timing samples — the corpus produced no observations")
	}
	onP50, onP99 := percentile(on, 50), percentile(on, 99)
	offP50, offP99 := percentile(off, 50), percentile(off, 99)
	tail := tailLabel(len(on))
	t.Logf("n=%d  lint ON  p50=%v %s=%v", len(on), onP50, tail, onP99)
	t.Logf("n=%d  lint OFF p50=%v %s=%v", len(off), offP50, tail, offP99)
	t.Logf("marginal cost of the lint check: p50 +%v, %s +%v", onP50-offP50, tail, onP99-offP99)
	if len(on) < tailMinSamples {
		t.Logf("n=%d is below %d, so the tail figure above is a SINGLE observation, not a percentile — do not publish it as p99",
			len(on), tailMinSamples)
	}
}

// tailMinSamples is where a nearest-rank p99 stops being one observation. Below
// it the p99 index rounds to n, so the "p99" IS the maximum — for n=15 it is
// literally the slowest of fifteen runs, and re-running the unchanged harness
// moves it by milliseconds. Naming it honestly is cheaper than publishing a
// number that reads as a distribution and behaves like a coin flip.
const tailMinSamples = 100

// tailLabel names the upper statistic for a sample of size n.
func tailLabel(n int) string {
	if n < tailMinSamples {
		return "max"
	}
	return "p99"
}

// percentile returns the p-th percentile by nearest-rank. Sorts a copy: the
// caller's slice order is the corpus order and other tests read it.
func percentile(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (p*len(s) + 99) / 100 // ceil(p/100 * n)
	if idx < 1 {
		idx = 1
	}
	if idx > len(s) {
		idx = len(s)
	}
	return s[idx-1]
}

// relOrSelf shortens a corpus path against the working directory for readable
// failures, falling back to the absolute path when it is outside the tree.
func relOrSelf(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedCountKeys is sortedKeys for an occurrence map, so the multiset
// comparison in checkPostureFidelity reports its failures in a stable order.
func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
