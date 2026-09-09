// This file (#313) is the corpus + oracle layer for the Python leg of the
// compiler-oracle differential in resolve_differential_test.go: it does not
// itself decide whether guard.Run has a false positive or a false negative —
// it decides which Python files are in scope (pyCorpusRoot / pyCorpusFiles),
// stages them into a throwaway git repo so the pre-commit posture can run
// against a real guard.ParseStagedDiff (stagePyCorpus), and adjudicates any
// given set of .py files with ruff's F821/F403 checks (ruffF821). The site
// enumeration, mutation, and posture-running machinery live in a sibling file
// in this same package; that file is the one that turns these verdicts into a
// pass/fail claim about the guard.
//
// Go has a compiler behind every mutation; Python does not. ruff is the
// stand-in judge here, and it is not infallible, so its opinion is folded
// into FOUR states rather than treated as a binary pass/fail:
//
//   - clean:  ruff reports nothing on this file — proof, usable for false
//     positives.
//   - silent: `from x import *` makes ruff abstain from F821 for the whole
//     file (it reports F403 instead). A guard flag on such a file is neither
//     confirmed nor refuted by ruff and must never be counted as a false
//     positive.
//   - noisy: ruff reports F821 on the file AS COMMITTED, before any mutation
//     — a ruff false alarm (dynamic `globals()` injection, `__getattr__`,
//     etc. are real patterns in the CPython stdlib). Such a file is not
//     provably clean, so it is excluded from false-positive counting, but it
//     remains eligible for the false-negative arm, which proves a specific
//     mutation differentially (exact fresh name, exact line, absent from the
//     unmutated verdict) rather than trusting ruff's raw opinion.
//   - unadjudicable: invalid syntax, an unreadable file, or a ruff process
//     that failed outright. Dropped from both arms; counted so a run never
//     silently reads as fuller coverage than it had.
//
// Getting this precedence wrong (see ruffF821) silently corrupts every
// downstream false-positive and false-negative count, because a single
// misclassified file changes which population a flag is judged against.
package guard_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// oracleState is ruff's verdict on one Python file. See the file header for
// what each state means and why there are four of them, not two.
type oracleState string

const (
	oracleClean         oracleState = "clean"
	oracleSilent        oracleState = "silent"        // F403 star-import: ruff abstains
	oracleNoisy         oracleState = "noisy"         // F821 on UNMUTATED code: ruff false alarm
	oracleUnadjudicable oracleState = "unadjudicable" // invalid-syntax / unreadable / batch failure
)

// oracleFinding is one ruff F821 row, reduced to what the differential needs:
// the line it fired on and the undefined name it named.
type oracleFinding struct {
	Line int
	Name string
}

// oracleVerdict is ruff's full opinion on one file: its state (see oracleState)
// and every F821 finding ruff reported on it, independent of what that state
// ends up being — a file that is e.g. "silent" because of an unrelated star
// import can still carry F821 rows if ruff emitted any before abstaining.
type oracleVerdict struct {
	State oracleState
	F821  []oracleFinding
}

// reF821Name pulls the undefined identifier out of ruff's F821 message, which
// is (as of ruff 0.16.1) exactly "Undefined name `X`". A message-format change
// makes this stop matching; ruffF821 logs loudly rather than silently emitting
// an empty Name, because a silently-empty Name turns every phase-2 mutation
// downstream into "discarded" — a quiet zero, not a visible failure.
//
// Python identifiers are Unicode (PEP 3131), not ASCII: a name class of
// [A-Za-z_][A-Za-z0-9_]* misses `café` and every other non-ASCII identifier
// ruff reports verbatim, which used to make the "message format may have
// changed" warning cry wolf on ordinary Unicode source while silently
// recording an empty Name (Fix 7 of the #313 review). \p{L}/\p{N} match any
// Unicode letter/number, matching Python's own identifier grammar far more
// closely than an ASCII class.
var reF821Name = regexp.MustCompile("Undefined name `([\\p{L}_][\\p{L}\\p{N}_]*)`")

// ruffBin resolves the ruff executable, or t.Skip if absent. An oracle that
// cannot be found is no evidence, not a defect — matches the posture of every
// other external-oracle harness in this repo (see requireRuff in
// cmd/runecho-guard/lint_differential_test.go). CI installs ruff explicitly
// (.github/workflows/ci.yml), so the skip does not silently drop coverage
// there.
//
// DELIBERATE, already-decided (do not "fix" this by allowlisting): CI runs
// .github/scripts/check-skips.sh against .github/expected-skips.txt, which
// fails the build on any test skip that isn't in that allowlist — and that
// file's own header forbids allowlisting environment-conditional skips like
// this one. That is intentional and this comment is the record of the
// decision: CI installs ruff explicitly and ubuntu-latest always carries
// python3, so a skip in THIS function (or in pyCorpusRoot's python3 lookup)
// happening in CI is genuine environment degradation, not an expected
// condition — a red build there is the gate working as designed, not a
// false positive to silence. The skip-never-fail posture is for a
// developer's local machine (no ruff installed, no CPython stdlib at the
// expected sysconfig path, etc.), not for CI.
func ruffBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ruff")
	if err != nil {
		t.Skip("ruff not on PATH — the Python oracle has nothing to adjudicate against")
	}
	return bin
}

// ruffJSONRow is one row of `ruff check --output-format json` output. Only the
// fields the classifier needs are decoded.
type ruffJSONRow struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Filename string `json:"filename"`
	Location struct {
		Row int `json:"row"`
	} `json:"location"`
}

// ruffF821 runs one ruff invocation over paths and returns a verdict per file,
// keyed by ABSOLUTE path (matching ruff's own JSON "filename", which is
// absolute for absolute path arguments). Every input path appears in the
// result; a path ruff did not report on and did not fail on is clean.
//
// Callers MUST pass absolute paths. ruffF821 resolves every input through
// filepath.Abs and filepath.EvalSymlinks before invoking ruff and before
// building any map key, specifically so the caller's key space and ruff's
// own "filename" key space are the same string space — a relative input and
// ruff's absolute report of it are otherwise silently different keys: the
// caller's key stays pre-seeded "clean" forever while the real verdict lands
// under a phantom key nobody reads. After grouping ruff's rows by filename,
// ruffF821 cross-checks that every row's filename is one of the resolved
// input paths; any row whose filename is NOT in that set is a contract
// violation (ruff reporting on a file this call never asked about, or a key
// space mismatch this resolution step failed to close) and is t.Fatalf'd
// with both the offending key and the full resolved input set printed,
// rather than silently discarded or silently mis-scored.
//
// --ignore-noqa matters here specifically because this oracle judges REAL,
// foreign-authored source (the CPython stdlib by default): a `# noqa: F821`
// already present in that source would otherwise make a genuinely undefined
// name read as "oracle silent" instead of the F821 it actually is.
//
// Exit 0 (clean) and exit 1 (findings, including invalid-syntax rows) are both
// normal. Any other exit is a harness-level fault, not a property of any one
// file in the batch: if more than one path was given, this recurses one path
// at a time so a single bad file cannot take the whole sweep down; with one
// path, that path is marked unadjudicable directly.
//
// ruff's OWN unreadable-file signal is not its exit code — a chmod-000 or
// missing file still exits 0 with `[]` on stdout, and warns
// "Failed to lint <path>: <reason>" on stderr instead (learned from
// cmd/runecho-guard/lint_differential_test.go's ruffOracle; that code is
// package main and rule-set-specific, so the learning is reused here, not the
// code). Measured against ruff 0.16.1, that stderr path is printed relative to
// the process's current working directory when the target is inside the cwd's
// subtree, and absolute otherwise — both forms are matched here so the
// unreadable verdict always lands on the right map key regardless of where
// `go test` happens to run from.
func ruffF821(t *testing.T, ruff string, paths ...string) map[string]oracleVerdict {
	t.Helper()
	verdicts := make(map[string]oracleVerdict, len(paths))
	if len(paths) == 0 {
		return verdicts
	}

	// Resolve every input to the same key space ruff's own JSON "filename"
	// reports in, BEFORE anything below pre-seeds a map key or invokes ruff.
	// See the doc comment above: a caller that passed a relative path (or a
	// path through a symlink ruff itself resolves) would otherwise silently
	// mis-key the whole batch.
	resolvedPaths := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("ruffF821: resolve %q to an absolute path: %v — callers must pass absolute paths", p, err)
		}
		if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			abs = real
		}
		resolvedPaths[i] = abs
	}
	paths = resolvedPaths

	args := append([]string{"check", "--no-cache", "--isolated", "--ignore-noqa",
		"--select", "F821,F403", "--output-format", "json"}, paths...)
	cmd := exec.Command(ruff, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	batchFailed := false
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			batchFailed = true
		}
	}

	if batchFailed {
		t.Logf("oracle ruff batch of %d file(s) exited abnormally: %v (stderr: %s)",
			len(paths), runErr, strings.TrimSpace(stderr.String()))
		if len(paths) == 1 {
			verdicts[paths[0]] = oracleVerdict{State: oracleUnadjudicable}
			return verdicts
		}
		for _, p := range paths {
			for k, v := range ruffF821(t, ruff, p) {
				verdicts[k] = v
			}
		}
		return verdicts
	}

	// Default: every path that survives to here without a more specific
	// verdict is clean. Overwritten below by whatever ruff actually reported.
	for _, p := range paths {
		verdicts[p] = oracleVerdict{State: oracleClean}
	}

	cwd, _ := os.Getwd()
	stderrText := stderr.String()
	unreadable := make(map[string]bool, len(paths))
	for _, p := range paths {
		forms := []string{p}
		if cwd != "" {
			if rel, relErr := filepath.Rel(cwd, p); relErr == nil && !strings.HasPrefix(rel, "..") {
				forms = append(forms, rel)
			}
		}
		for _, form := range forms {
			if strings.Contains(stderrText, "Failed to lint "+form+":") {
				unreadable[p] = true
				break
			}
		}
	}
	for p := range unreadable {
		verdicts[p] = oracleVerdict{State: oracleUnadjudicable}
	}

	var raw []ruffJSONRow
	if jsonErr := json.Unmarshal(stdout.Bytes(), &raw); jsonErr != nil {
		t.Logf("oracle ruff output is not JSON: %v (%d bytes of stdout) — every non-unreadable path in this batch is unadjudicable",
			jsonErr, stdout.Len())
		for _, p := range paths {
			if unreadable[p] {
				continue
			}
			verdicts[p] = oracleVerdict{State: oracleUnadjudicable}
		}
		return verdicts
	}

	grouped := make(map[string][]ruffJSONRow)
	for _, r := range raw {
		grouped[r.Filename] = append(grouped[r.Filename], r)
	}

	// Cross-check: every filename ruff reported under must be one of the
	// resolved input paths. If it is not, the two key spaces have diverged
	// (a mis-resolved input, or ruff reporting on something this call never
	// asked about) and trusting grouped[p] below would silently score the
	// wrong file — or leave the caller's pre-seeded "clean" default standing
	// for a file ruff actually found something on. Fail loudly instead.
	inputSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		inputSet[p] = true
	}
	for filename := range grouped {
		if !inputSet[filename] {
			t.Fatalf("ruffF821: ruff reported findings under filename %q, which is not "+
				"among the %d resolved input path(s): %v — this is the key-space mismatch "+
				"that silently corrupts every downstream false-positive/false-negative count "+
				"(see the file header and Fix 1 of the #313 review); callers must pass "+
				"absolute paths", filename, len(paths), paths)
		}
	}

	for p, rows := range grouped {
		// unreadable[p] (stderr's signal) and classifyRuffRows' own
		// unreadable handling agree by construction: passing it through
		// rather than short-circuiting here means classifyRuffRows is the
		// SINGLE place that decides "unreadable wins over every row
		// combination", exercised directly by
		// TestClassifyRuffRowsPrecedence rather than only implicitly here.
		verdict, warnings := classifyRuffRows(rows, unreadable[p])
		for _, w := range warnings {
			t.Logf("oracle ruff %s: %s", p, w)
		}
		verdicts[p] = verdict
	}

	return verdicts
}

// classifyRuffRows applies ruffF821's four-state precedence to one file's
// ruff rows, given whether ruff's stderr already flagged the file as
// unreadable. It is pure — no I/O, no *testing.T — specifically so
// TestClassifyRuffRowsPrecedence can pin the four-state ORDER against
// synthetic rows that real ruff output cannot be coaxed into producing
// together: real ruff 0.16.1 could not be made to emit two of these
// categories for one file in four attempts, so before this extraction the
// precedence was unobservable from any fixture built on real ruff output —
// a fully inverted switch still passed the old end-to-end classifier test
// (see the file header and Fix 2 of the #313 review).
//
// Precedence, highest to lowest: unreadable > invalid-syntax > F403 (silent)
// > F821 (noisy) > clean. A row whose code matches none of those three does
// NOT silently fall through to clean (Fix 3 of the #313 review): if a file
// has rows but none of them matched a known code, the verdict is
// unadjudicable rather than clean, because ruff has renamed this field
// before (syntax errors were E999 pre-0.9, invalid-syntax since) and a
// silent clean here would be exactly the quiet zero this harness exists to
// prevent. The second return value carries any non-fatal diagnostics (an
// F821 message that didn't match the expected format, or a code this
// function does not recognise) for the caller to log with *testing.T —
// kept out of this function so it stays callable from a plain Go table
// test with no test handle at all.
func classifyRuffRows(rows []ruffJSONRow, unreadable bool) (oracleVerdict, []string) {
	if unreadable {
		return oracleVerdict{State: oracleUnadjudicable}, nil
	}
	if len(rows) == 0 {
		return oracleVerdict{State: oracleClean}, nil
	}

	hasInvalid, hasSilent, hasNoisy := false, false, false
	var findings []oracleFinding
	var warnings []string
	for _, r := range rows {
		switch r.Code {
		case "invalid-syntax":
			hasInvalid = true
		case "F403":
			hasSilent = true
		case "F821":
			hasNoisy = true
			name := ""
			if m := reF821Name.FindStringSubmatch(r.Message); len(m) == 2 {
				name = m[1]
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"WARNING: ruff F821 message did not match the expected "+
						"'Undefined name `X`' format (got %q on line %d) — ruff's "+
						"message format may have changed; recording an empty Name "+
						"rather than guessing, so this shows up as a warning instead "+
						"of a silent zero downstream", r.Message, r.Location.Row))
			}
			findings = append(findings, oracleFinding{Line: r.Location.Row, Name: name})
		default:
			warnings = append(warnings, fmt.Sprintf(
				"WARNING: ruff row carries an unrecognized code %q (message %q, line %d) "+
					"— classifyRuffRows only understands invalid-syntax/F403/F821; ruff has "+
					"renamed this field before (syntax errors were E999 pre-0.9), so this "+
					"file is unadjudicable rather than silently clean", r.Code, r.Message, r.Location.Row))
		}
	}

	switch {
	case hasInvalid:
		return oracleVerdict{State: oracleUnadjudicable, F821: findings}, warnings
	case hasSilent:
		return oracleVerdict{State: oracleSilent, F821: findings}, warnings
	case hasNoisy:
		return oracleVerdict{State: oracleNoisy, F821: findings}, warnings
	}

	// Rows were present but none matched a known code (Fix 3): do not fall
	// through to clean.
	return oracleVerdict{State: oracleUnadjudicable, F821: findings}, warnings
}

// pyCorpusSkipDirs are the trees a real Python install or repository carries
// that are not the code under test — matches the skip list used by the
// sibling oracle harnesses (cmd/runecho-guard/lint_differential_test.go's
// lintCorpusSkipDirs, internal/guard's own pyCorpusFiles in
// callshapecheck_differential_test.go).
var pyCorpusSkipDirs = map[string]bool{
	".venv": true, "venv": true, "env": true, "node_modules": true,
	".git": true, "__pycache__": true, "build": true, "dist": true,
	".tox": true, ".mypy_cache": true, ".ruff_cache": true,
	"site-packages": true, "vendor": true,
}

// pyCorpusFilesDefaultCap bounds the population before mutation-sampling,
// applied AFTER sorting so a capped run is reproducible across machines.
// Override with RUNECHO_ORACLE_PY_FILES.
const pyCorpusFilesDefaultCap = 400

// pyCorpusRoot resolves the corpus root. isDefault is true for the CPython
// stdlib. t.Skip (never fail) when neither the env override nor a stdlib path
// resolves — an oracle corpus that cannot be found is no evidence.
func pyCorpusRoot(t *testing.T) (root string, isDefault bool) {
	t.Helper()

	if r := os.Getenv("RUNECHO_ORACLE_PY_CORPUS"); r != "" {
		abs, err := filepath.Abs(r)
		if err != nil {
			t.Skipf("resolve RUNECHO_ORACLE_PY_CORPUS=%q: %v", r, err)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil {
			t.Skipf("RUNECHO_ORACLE_PY_CORPUS=%q: %v", abs, statErr)
		}
		if !info.IsDir() {
			t.Skipf("RUNECHO_ORACLE_PY_CORPUS=%q is not a directory", abs)
		}
		return abs, false
	}

	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — cannot resolve the CPython stdlib default corpus")
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(python3, "-c", "import sysconfig;print(sysconfig.get_paths()['stdlib'])")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("python3 sysconfig.get_paths() failed: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	stdlib := strings.TrimSpace(stdout.String())
	if stdlib == "" {
		t.Skip("python3 sysconfig reported an empty stdlib path")
	}
	info, statErr := os.Stat(stdlib)
	if statErr != nil {
		t.Skipf("resolved stdlib path %q: %v", stdlib, statErr)
	}
	if !info.IsDir() {
		t.Skipf("resolved stdlib path %q is not a directory", stdlib)
	}
	return stdlib, true
}

// pyCorpusFiles returns corpus-relative .py paths, sorted, zero-byte files
// dropped. isDefault=true (the CPython stdlib) walks depth 1 only — the
// stdlib installs test/, idlelib/, lib2to3/, turtledemo/ (and on some layouts
// site-packages/) underneath the same root, none of which is the standard
// library itself. isDefault=false does a recursive walk skipping
// pyCorpusSkipDirs. The result is capped at RUNECHO_ORACLE_PY_FILES (default
// pyCorpusFilesDefaultCap) after sorting, so a capped run is reproducible; a
// truncation is logged so it never silently reads as full coverage.
//
// The cap selects an evenly-spaced STRIDE over the sorted population, not a
// sorted PREFIX (Fix 6 of the #313 review): capping {a.py, sub/b.py, zz/f.py}
// to 2 with a prefix silently drops zz/ (and every path that sorts after it)
// in its entirety, so an over-cap run reports a per-KLOC rate over whatever
// happens to sort first — typically one or two top-level packages — while
// the log line only ever said "capping N to <limit>". A stride keeps that
// same reproducibility (same sorted input, same deterministic indices) while
// spreading the sample across the whole population instead of one prefix of
// it.
//
// A root that was already stat'd successfully by pyCorpusRoot can still fail
// mid-enumeration (ReadDir/WalkDir) — a live tree losing a directory, a
// permission change, etc. That is a property of the corpus at read time, not
// a defect in this harness, so it is logged and the corpus proceeds with
// whatever was enumerated before the failure, matching the skip-or-log
// posture of every other per-file error path in this function (Fix 5 of the
// #313 review) rather than failing the whole run.
func pyCorpusFiles(t *testing.T, root string, isDefault bool) []string {
	t.Helper()

	var rels []string
	zeroByte := 0

	consider := func(path string, size int64) {
		if !strings.HasSuffix(path, ".py") {
			return
		}
		if size == 0 {
			zeroByte++
			return
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return
		}
		rels = append(rels, rel)
	}

	if isDefault {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Logf("corpus: read corpus root %s: %v — treating as an empty corpus rather than "+
				"failing the build (skip-never-fail posture; see Fix 5 of the #313 review)", root, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			consider(filepath.Join(root, e.Name()), info.Size())
		}
	} else {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				return nil // unreadable subtree: skip, don't abort the corpus
			}
			if d.IsDir() {
				if path != root && pyCorpusSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return nil
			}
			consider(path, info.Size())
			return nil
		})
		if err != nil {
			t.Logf("corpus: walk corpus root %s: %v — using whatever files were enumerated before "+
				"the walk failed rather than failing the build (skip-never-fail posture; see Fix 5 "+
				"of the #313 review)", root, err)
		}
	}

	sort.Strings(rels)

	if zeroByte > 0 {
		t.Logf("corpus: dropped %d zero-byte .py file(s)", zeroByte)
	}

	limit := pyCorpusFilesDefaultCap
	if v := os.Getenv("RUNECHO_ORACLE_PY_FILES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("RUNECHO_ORACLE_PY_FILES=%q must be a positive integer", v)
		}
		limit = n
	}
	if len(rels) > limit {
		// Evenly-spaced stride over the sorted population, NOT a sorted
		// prefix (Fix 6 of the #313 review) — see the doc comment above for
		// why a prefix silently zeroes out whatever sorts last.
		stride := float64(len(rels)) / float64(limit)
		sampled := make([]string, 0, limit)
		for i := 0; i < limit; i++ {
			idx := int(float64(i) * stride)
			if idx >= len(rels) {
				idx = len(rels) - 1
			}
			sampled = append(sampled, rels[idx])
		}
		t.Logf("corpus: capping %d files to %d (RUNECHO_ORACLE_PY_FILES) via an evenly-spaced "+
			"stride over the sorted population, not a prefix; truncated %d",
			len(rels), limit, len(rels)-limit)
		rels = sampled
	}

	return rels
}

// stagePyCorpus copies rels (corpus-relative paths, as returned by
// pyCorpusFiles) into a fresh t.TempDir(), preserving relative paths, then
// git-inits it and makes a baseline commit. IR generation and ruff both run
// against this staged copy — never the original corpus root — so the known
// set, the oracle verdicts, and (for the pre-commit posture) the diff parser
// all agree on exactly the same file population.
//
// A source file that has gone unreadable or vanished since pyCorpusFiles
// enumerated it (TOCTOU on a live tree, or a genuine permission problem) is
// SKIPPED and counted rather than failing the whole run (Fix 5 of the #313
// review): an unreadable source file at staging time is a property of the
// corpus, not a defect in this harness, and t.Fatalf-ing here also made
// ruffF821's own unreadable->unadjudicable branch unreachable from the
// corpus path — nothing ever exercised it end to end. The third return
// value, stagedRels, is the set that ACTUALLY made it onto disk; callers
// (and the oracle verdicts they request) MUST adjudicate against stagedRels,
// not the rels this function was given, or the known set and the oracle's
// opinion silently diverge by exactly the skipped files.
//
// hasGit is false when git is unavailable, any git step fails, OR the
// population cross-check below fails; the staged tree is still returned and
// usable for every posture that does not need a real repository. A global
// core.excludesFile matching e.g. `test_*.py` makes `git add -A` silently
// stage fewer files than are on disk with no error and no log line, and a
// global commit.gpgsign=true with no gpg binary present fails the commit
// outright (Fix 4 of the #313 review, both demonstrated against real git
// config) — so the git invocations pin core.excludesFile and commit.gpgsign
// to neutral values via -c, and the commit is verified against `git
// ls-files` afterward rather than trusted. Staging itself never t.Fatals on
// a git problem or an unreadable source file — only on a destination-side
// copy problem (mkdir/write failure), since those are true machine-level
// faults, not a property of the corpus.
func stagePyCorpus(t *testing.T, root string, rels []string) (staged string, hasGit bool, stagedRels []string) {
	t.Helper()
	staged = t.TempDir()

	dropped := 0
	for _, rel := range rels {
		src := filepath.Join(root, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			dropped++
			continue
		}
		dst := filepath.Join(staged, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("stage corpus: mkdir %s: %v", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatalf("stage corpus: write %s: %v", dst, err)
		}
		stagedRels = append(stagedRels, rel)
	}
	if dropped > 0 {
		t.Logf("stage corpus: staged-drop %d file(s) unreadable or vanished between enumeration "+
			"and staging (TOCTOU/permissions) — adjudicating only the %d file(s) actually staged",
			dropped, len(stagedRels))
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Logf("stage corpus: git not on PATH — the pre-commit posture will be skipped")
		return staged, false, stagedRels
	}

	runGit := func(args ...string) bool {
		cmd := exec.Command("git", args...)
		cmd.Dir = staged
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Logf("stage corpus: git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
			return false
		}
		return true
	}

	// core.excludesFile=/dev/null and commit.gpgsign=false neutralize two
	// global git configs that would otherwise silently corrupt the staged
	// population (see the doc comment above and Fix 4 of the #313 review).
	// Passed as -c overrides, scoped to this throwaway staging repo only.
	gitConfig := []string{
		"-c", "core.excludesFile=/dev/null",
		"-c", "commit.gpgsign=false",
		"-c", "user.name=runecho",
		"-c", "user.email=runecho@test",
	}

	if !runGit("init", "-q") {
		return staged, false, stagedRels
	}
	if !runGit(append(append([]string{}, gitConfig...), "add", "-A")...) {
		return staged, false, stagedRels
	}
	if !runGit(append(append([]string{}, gitConfig...), "commit", "-q", "-m", "baseline")...) {
		return staged, false, stagedRels
	}

	// Population cross-check: the doc comment above promises the known set,
	// the oracle verdicts, and the diff parser all agree on exactly the same
	// file population. `git add -A` / `git commit` can both succeed while
	// silently diverging from stagedRels (a global excludesFile neither
	// command above ever surfaces as an error), so verify what actually got
	// committed rather than trusting a clean exit code.
	lsCmd := exec.Command("git", "ls-files")
	lsCmd.Dir = staged
	var lsOut, lsErrBuf bytes.Buffer
	lsCmd.Stdout = &lsOut
	lsCmd.Stderr = &lsErrBuf
	if err := lsCmd.Run(); err != nil {
		t.Logf("stage corpus: git ls-files: %v (%s)", err, strings.TrimSpace(lsErrBuf.String()))
		return staged, false, stagedRels
	}
	committed := 0
	for _, line := range strings.Split(strings.TrimRight(lsOut.String(), "\n"), "\n") {
		if line != "" {
			committed++
		}
	}
	if committed != len(stagedRels) {
		t.Logf("stage corpus: committed population (%d) != staged population (%d) — a global git "+
			"config (core.excludesFile, gpgsign, or similar) diverged the commit from what was "+
			"written to disk; falling back to hasGit=false rather than proceeding with a "+
			"mismatched population", committed, len(stagedRels))
		return staged, false, stagedRels
	}

	return staged, true, stagedRels
}

// ---------------------------------------------------------------------------
// classifier unit test — hermetic, no corpus, no python3 dependency
// ---------------------------------------------------------------------------

// TestPyRuffOracleClassifier pins ruffF821's four-state precedence (see the
// file header and ruffF821's own doc comment) against four small inline
// fixtures, independent of the CPython stdlib corpus.
func TestPyRuffOracleClassifier(t *testing.T) {
	ruff := ruffBin(t)
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	clean := write("clean.py", "def f():\n    return 1\n")
	silent := write("silent.py", "from os import *\n\ndef g():\n    return path\n")
	noisy := write("noisy.py", "def h():\n    return totally_undefined_thing\n")
	invalid := write("invalid.py", "def broken(:\n    pass\n")

	verdicts := ruffF821(t, ruff, clean, silent, noisy, invalid)

	if len(verdicts) != 4 {
		t.Fatalf("expected a verdict for all 4 input paths, got %d: %+v", len(verdicts), verdicts)
	}

	if got := verdicts[clean]; got.State != oracleClean {
		t.Errorf("clean.py: state = %q, want %q (%+v)", got.State, oracleClean, got)
	}

	if got := verdicts[silent]; got.State != oracleSilent {
		t.Errorf("silent.py: state = %q, want %q (%+v)", got.State, oracleSilent, got)
	}

	noisyVerdict := verdicts[noisy]
	if noisyVerdict.State != oracleNoisy {
		t.Errorf("noisy.py: state = %q, want %q (%+v)", noisyVerdict.State, oracleNoisy, noisyVerdict)
	}
	if len(noisyVerdict.F821) != 1 {
		t.Fatalf("noisy.py: expected exactly 1 F821 finding, got %d: %+v", len(noisyVerdict.F821), noisyVerdict.F821)
	}
	if got := noisyVerdict.F821[0].Name; got != "totally_undefined_thing" {
		t.Errorf("noisy.py: finding Name = %q, want %q", got, "totally_undefined_thing")
	}
	if got := noisyVerdict.F821[0].Line; got != 2 {
		t.Errorf("noisy.py: finding Line = %d, want %d", got, 2)
	}

	if got := verdicts[invalid]; got.State != oracleUnadjudicable {
		t.Errorf("invalid.py: state = %q, want %q (%+v)", got.State, oracleUnadjudicable, got)
	}
}

// TestClassifyRuffRowsPrecedence pins classifyRuffRows' four-state precedence
// (unreadable > invalid-syntax > F403/silent > F821/noisy > clean) against
// synthetic rows, hermetically — no ruff binary, no I/O. See classifyRuffRows'
// doc comment: real ruff 0.16.1 could not be coaxed into emitting two of
// these categories for one file in four attempts, so before classifyRuffRows
// was extracted as a pure function, this order was UNOBSERVABLE from any
// fixture built on real ruff output — a fully inverted switch still passed
// the old end-to-end classifier test. Every case below is chosen specifically
// to fail if the precedence order is wrong; see the mutation proof in the
// #313 review report (invert the switch in classifyRuffRows, confirm these
// fail, restore it, confirm they pass again).
func TestClassifyRuffRowsPrecedence(t *testing.T) {
	f403 := ruffJSONRow{Code: "F403", Message: "'from x import *' used; unable to detect undefined names"}

	f821 := ruffJSONRow{Code: "F821", Message: "Undefined name `totally_undefined_thing`"}
	f821.Location.Row = 7

	invalid := ruffJSONRow{Code: "invalid-syntax", Message: "SyntaxError: invalid syntax"}

	unicodeF821 := ruffJSONRow{Code: "F821", Message: "Undefined name `café`"}
	unicodeF821.Location.Row = 3

	unknown := ruffJSONRow{Code: "F999-not-a-real-code", Message: "ruff has never emitted this code"}

	cases := []struct {
		name string
		rows []ruffJSONRow
		want oracleState
	}{
		{"F403+F821 together -> silent (F403 outranks F821)", []ruffJSONRow{f403, f821}, oracleSilent},
		{"invalid-syntax+F403 -> unadjudicable (invalid-syntax outranks F403)", []ruffJSONRow{invalid, f403}, oracleUnadjudicable},
		{"invalid-syntax+F821 -> unadjudicable (invalid-syntax outranks F821)", []ruffJSONRow{invalid, f821}, oracleUnadjudicable},
		{"invalid-syntax+F403+F821 all together -> unadjudicable", []ruffJSONRow{invalid, f403, f821}, oracleUnadjudicable},
		{"F821 alone -> noisy", []ruffJSONRow{f821}, oracleNoisy},
		{"no rows -> clean", nil, oracleClean},
		{"unicode identifier F821 alone -> noisy", []ruffJSONRow{unicodeF821}, oracleNoisy},
		{"unrecognized code only -> unadjudicable, not clean (Fix 3)", []ruffJSONRow{unknown}, oracleUnadjudicable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := classifyRuffRows(c.rows, false)
			if got.State != c.want {
				t.Errorf("classifyRuffRows(%+v, unreadable=false) state = %q, want %q", c.rows, got.State, c.want)
			}
		})

		// unreadable=true must win over EVERY row combination above, not
		// just the ones that would otherwise be unadjudicable — this is the
		// top of the precedence order, not a tiebreak.
		t.Run(c.name+" / unreadable=true wins over everything", func(t *testing.T) {
			got, _ := classifyRuffRows(c.rows, true)
			if got.State != oracleUnadjudicable {
				t.Errorf("classifyRuffRows(%+v, unreadable=true) state = %q, want %q (unreadable must win)",
					c.rows, got.State, oracleUnadjudicable)
			}
		})
	}

	t.Run("F821 alone: finding Line/Name are correct", func(t *testing.T) {
		got, _ := classifyRuffRows([]ruffJSONRow{f821}, false)
		if len(got.F821) != 1 {
			t.Fatalf("expected exactly 1 finding, got %d: %+v", len(got.F821), got.F821)
		}
		if got.F821[0].Name != "totally_undefined_thing" {
			t.Errorf("Name = %q, want %q", got.F821[0].Name, "totally_undefined_thing")
		}
		if got.F821[0].Line != 7 {
			t.Errorf("Line = %d, want %d", got.F821[0].Line, 7)
		}
	})

	t.Run("unicode identifier: finding Name is not dropped (Fix 7)", func(t *testing.T) {
		got, _ := classifyRuffRows([]ruffJSONRow{unicodeF821}, false)
		if len(got.F821) != 1 {
			t.Fatalf("expected exactly 1 finding, got %d: %+v", len(got.F821), got.F821)
		}
		if got.F821[0].Name != "café" {
			t.Errorf("Name = %q, want %q — reF821Name must match Unicode identifiers, not just ASCII",
				got.F821[0].Name, "café")
		}
		if len(got.F821) == 1 && got.F821[0].Name == "" {
			t.Errorf("Name is empty — reF821Name failed to match and classifyRuffRows silently recorded a zero value")
		}
	})

	t.Run("unrecognized code: warns rather than silently discarding (Fix 3)", func(t *testing.T) {
		_, warnings := classifyRuffRows([]ruffJSONRow{unknown}, false)
		if len(warnings) == 0 {
			t.Fatalf("expected at least one warning for unrecognized code %q, got none", unknown.Code)
		}
	})
}
