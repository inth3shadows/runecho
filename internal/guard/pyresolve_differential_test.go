// This file (#313) is the Python leg of the compiler-oracle differential:
// resolve_differential_test.go proves guard.Run's false-positive/false-negative
// rates against `go build`; this file proves the same two claims for Python,
// against `ruff check --select F821,F403` as the oracle, since Python has no
// compiler to ask.
//
// It mirrors resolve_differential_test.go phase-for-phase and reuses its
// package-level helpers (knownSet, copyModule, mutationSite/mutationSuffix,
// applyMutation, attributed, pct, mutationSuffix, the RUNECHO_ORACLE_MUTATIONS
// parsing convention) rather than redeclaring them. The corpus/oracle layer
// (pyCorpusRoot, pyCorpusFiles, stagePyCorpus, ruffF821, oracleVerdict) lives in
// the sibling pyoracle_test.go; the site enumerator (pySites, pyTopLevelBlocks)
// lives in pysites_test.go. Neither of those two files is modified here — see
// their own headers for what they already guarantee.
//
// WHY FOUR POSTURES, NOT ONE. #313's own motivating incident (see the "harness
// measures the posture you gave it" gotcha in this repo's history) was a
// harness that reported zero false positives while two default-on hook paths
// were silently broken, because it only ever exercised the one posture
// (whole-file, self-folding) that cannot fail by construction. Section 7 of
// the plan builds four postures — write-whole, write-new, edit-hunk, precommit
// — precisely so that trap cannot repeat here: write-whole is kept ONLY as a
// control (asserted equal to write-new, since for Python guard.Run's Pass 1
// folds the identical four extractors FoldInFileDefs does), and the two real
// claims (false positives, false negatives) are carried by edit-hunk and
// precommit, which are the postures a model's actual Write/Edit/commit
// traffic produces.
//
// WHY A FIFTH POSTURE, inner-hunk. An Opus review of the first four postures
// ran three product mutations against them: deleting the PyParamNames fold
// from validate.go's Pass 1 correctly turned the suite red, and deleting the
// PyDeclaredNames fold from foldinfile.go also correctly turned it red — but
// deleting the PyParamNames fold from foldinfile.go left the suite GREEN,
// identical to baseline, across all 155 corpus files. foldinfile.go's own
// comment calls that fold "the last surviving Python false-positive class in
// the live decision log", and the harness could not prove it does anything.
// Cause: edit-hunk's hunk is always a WHOLE top-level block (pyTopLevelBlocks),
// so a parameter's binding site (`def f(cb):`) and its use (`cb()`) always
// land in the SAME hunk — Run's own Pass 1 supplies the binding, and
// FoldInFileDefs' fold is never exercised. The production shape that needs
// the fold — an Edit of a few lines INSIDE a function body, with the
// signature outside the hunk — is the one posture the first four could not
// construct. inner-hunk closes that hole: added is a slice strictly INSIDE a
// block's body (never the def/class header or a decorator); fold is the
// whole file MINUS exactly those lines, so the signature — and every other
// binding in the file — sits ONLY in the fold. See pyInnerHunkForBlock for
// the eligibility checks this requires that edit-hunk does not (an interior
// line, unlike a block boundary, is not guaranteed to start outside an open
// string or at zero bracket depth).
//
// WHY FOUR ORACLE STATES, NOT TWO. ruff is not infallible ground truth the way
// `go build` is: a star import makes it abstain (F403, "silent"), and the
// CPython stdlib itself contains ~140 pre-existing F821s from dynamic
// `globals()`/`__getattr__` patterns ruff cannot see through ("noisy"). Both
// are handled in ruffF821/oracleState (pyoracle_test.go) — this file's only
// job is to respect the resulting classification: only oracleClean adjudicates
// a false positive; oracleNoisy is excluded from false-positive counting but
// stays eligible for the differential false-negative proof (which never
// trusts ruff's raw opinion, only an exact fresh name at an exact line,
// absent from the pre-mutation baseline); oracleSilent is tabulated
// separately as UNADJUDICATED; oracleUnadjudicable is dropped from both arms.
//
// SCOPE. Two checks answer "does this Python identifier resolve" and both are
// exercised regardless of shipped default, per the same "what the code CAN
// see" rule resolve_differential_test.go uses for the Go qualified checks:
// guard.Run (bare references — reCallIdent bare calls, appendConstRefs
// SCREAMING_SNAKE) and guard.FileScopeViolations (gated by
// RUNECHO_GUARD_FILESCOPE in production; run here unconditionally). Only
// guard.Run's bare-call population is a scored population (fail condition);
// FileScope false positives and every other reference shape are report-only
// — see the plan's section 10 for the full argument.
package guard_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// defaultPyMutations bounds phase 2 (one ruff invocation + up to two git
// operations per mutation). Overridable via the shared RUNECHO_ORACLE_MUTATIONS
// convention resolve_differential_test.go already established.
//
// 40 matches resolve_differential_test.go's defaultMutations rather than being
// tuned independently.
//
// It is NOT a runtime knob, and a past reading of these numbers that said it
// was is recorded here so it is not re-derived: at 120 mutations this phase
// cost 196s, at 40 it cost 178s, and at 1 it cost 169s. The budget was never
// the cost — ~169s was FIXED, spent in an O(sites x corpus bytes) freshness
// scan while building the pools (see the one-time suffix proof above, which
// took the phase to ~17s). Each mutation is ~0.23s.
//
// So the budget is now purely a resolution decision. 40 gives 5 per shape
// across 8 shapes; the thin shapes (annotation has 60 eligible sites
// corpus-wide, except-class 80, decorator 168) are owned by no check and are
// measured to document a non-goal, not to gate anything. The shape that gates
// the build, bare-call, measured 15/15 across all four postures with zero
// discards at budget 120.
//
// RUNECHO_ORACLE_MUTATIONS=400 is the deep local run when a shape's number is
// actually in question. The report always prints the effective budget next to
// each shape's eligible-site count, so a thin sample reads as thin.
const defaultPyMutations = 40

// pyHunkCap bounds how many edit-hunk postures phase 1 measures per file — the
// stdlib's larger modules can carry 100+ top-level blocks, and running four
// postures over every one of them would make phase 1's runtime scale with the
// corpus's def count rather than its file count. Evenly spaced (stride), not a
// prefix, for the same reproducibility-without-bias reason pyCorpusFiles' own
// cap uses a stride (see its doc comment).
const pyHunkCap = 8

// pyEffectiveHunkCap and pyEffectiveMutations scale the two sample sizes down
// under `-race`, and are identity otherwise. See pyresolve_race_test.go for the
// measurement that forced this and for why the corpus itself is NOT reduced.
//
// Both are reported by the harness next to the population they sample, so a
// race run is legible as a reduced run.
func pyEffectiveHunkCap() int {
	if pyRaceBuild {
		return 2
	}
	return pyHunkCap
}

func pyEffectiveMutations(budget int) int {
	if pyRaceBuild && budget > 8 {
		return 8
	}
	return budget
}

// pyConstSuffix is the fresh-name suffix for the bare-const shape only. Every
// other shape uses the shared mutationSuffix ("Zq7Runecho"); bare-const uses
// this instead so the mutant NAME stays in reUpperSnakeRef's SCREAMING_SNAKE
// lexical class — appending mutationSuffix's mixed-case tail would push
// "MAX_SIZE" to "MAX_SIZEZq7Runecho", which is no longer SCREAMING_SNAKE, so
// appendConstRefs would never have looked at it and the harness would
// manufacture a fake miss rather than measure a real one (plan section 13 #4).
const pyConstSuffix = "_ZQ7RUNECHO"

// pyCapLineBytes mirrors internal/guard/util.go's unexported maxLineBytes
// (capLine's ceiling, currently 64 KiB). It is duplicated here, not imported,
// because this file is package guard_test (an external test package) and
// cannot reference guard's unexported constant. A mutation site whose column
// falls at or past this offset is invisible to the guard BY DESIGN (the line
// is truncated before any extractor ever sees it) — skipped, never scored as
// a miss (plan section 13 #5). In practice this almost never fires: it exists
// for correctness under RUNECHO_ORACLE_PY_CORPUS pointed at unusual input,
// not because the stdlib has 64 KiB lines.
const pyCapLineBytes = 64 << 10

// ---------------------------------------------------------------------------
// small shared helpers
// ---------------------------------------------------------------------------

// pyResolvedAbsPaths resolves rels (staged-relative) to the same absolute,
// symlink-resolved key space ruffF821 keys its own result map under (see its
// doc comment) — needed here only for a BATCH oracle call, where the caller
// must look results back up by path afterward. A single-path oracle call
// (phase 2's per-mutation checks) instead just takes the lone map value,
// sidestepping this resolution entirely.
func pyResolvedAbsPaths(t *testing.T, staged string, rels []string) []string {
	t.Helper()
	out := make([]string, len(rels))
	for i, rel := range rels {
		abs, err := filepath.Abs(filepath.Join(staged, rel))
		if err != nil {
			t.Fatalf("resolve absolute path for %s: %v", rel, err)
		}
		if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			abs = real
		}
		out[i] = abs
	}
	return out
}

// pyCountZeroByteFiles independently counts zero-byte .py files under root,
// mirroring pyCorpusFiles' own enumeration rule (isDefault: depth-1 only;
// else recursive, skipping pyCorpusSkipDirs). This exists ONLY because
// pyCorpusFiles logs its zero-byte count via t.Logf but does not return it
// (pyoracle_test.go, not modified here), and the report format below wants
// the number. Report-only: it never influences which files are staged or
// adjudicated — pyCorpusFiles alone owns that.
func pyCountZeroByteFiles(root string, isDefault bool) int {
	n := 0
	if isDefault {
		entries, err := os.ReadDir(root)
		if err != nil {
			return 0
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
				continue
			}
			if info, ierr := e.Info(); ierr == nil && info.Size() == 0 {
				n++
			}
		}
		return n
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p != root && pyCorpusSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".py") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() == 0 {
			n++
		}
		return nil
	})
	return n
}

// pyInterpreterVersion and pyRuffVersion are best-effort report fields (plan
// section 4's "the report always prints the resolved path and interpreter
// version" — corpus composition depends on which python3/ruff resolve on the
// machine running the test, see pyCorpusRoot). Never fatal: an unreadable
// version string is a report cosmetic, not a defect.
func pyInterpreterVersion() string {
	out, err := exec.Command("python3", "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func pyRuffVersion(ruff string) string {
	out, err := exec.Command(ruff, "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// pyGitConfigArgs neutralizes the same global git configs stagePyCorpus's own
// runGit closure does (core.excludesFile, commit.gpgsign) plus a committer
// identity, scoped via -c to whichever throwaway staged repo dir is passed —
// never touching the user's real git config. Duplicated from stagePyCorpus's
// unexported closure (pyoracle_test.go, not modified here) because that
// closure is local to stagePyCorpus and not reusable across this file's own
// git operations (the phase-1 precommit driver's extra "pre" commit, and
// phase 2's per-mutation `git add`).
func pyGitConfigArgs() []string {
	return []string{
		"-c", "core.excludesFile=/dev/null",
		"-c", "commit.gpgsign=false",
		"-c", "user.name=runecho",
		"-c", "user.email=runecho@test",
	}
}

// pyRunGit runs one git subcommand in dir with pyGitConfigArgs applied. false
// on any failure (logged, never fatal) — a git operation failing mid-harness
// degrades that one posture's coverage, not the whole run (matches
// stagePyCorpus's own fail-soft posture for git).
func pyRunGit(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	full := append(append([]string{}, pyGitConfigArgs()...), args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Logf("git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// postures (plan section 7)
// ---------------------------------------------------------------------------

// pyPostureSpec is one in-memory edit shape to measure for a Python file. A
// separate type from resolve_differential_test.go's own `posture` (same
// package, same file group) since that name is already taken there — this is
// its Python analogue, not a replacement.
type pyPostureSpec struct {
	name string
	// fold is the text folded into the known set via guard.FoldInFileDefs —
	// "" means no fold at all (write-new's nil pre-edit file).
	fold string
	// added is the text presented as the edit.
	added string
}

// pyPosturesForFile returns write-whole, write-new, one edit-hunk posture per
// top-level block (pyTopLevelBlocks, pysites_test.go), and one inner-hunk
// posture per ELIGIBLE top-level block (pyInnerHunkForBlock below) — the
// fifth posture, see the file header for why it exists. edit-hunk blocks are
// capped at pyHunkCap per file via an evenly-spaced stride — never a prefix,
// for the same reason pyCorpusFiles' own cap isn't a prefix (see its doc
// comment) — and inner-hunk is capped the same way, independently, over its
// own (smaller) eligible population; see pyInnerHunkPosturesForFile.
// blocksTotal/blocksDropped let the caller report the edit-hunk cap honestly;
// innerStats does the same for inner-hunk.
func pyPosturesForFile(text string) (postures []pyPostureSpec, blocksTotal, blocksDropped int, innerStats pyInnerHunkStats) {
	postures = []pyPostureSpec{
		{name: "write-whole", fold: text, added: text},
		{name: "write-new", fold: "", added: text},
	}
	blocks := pyTopLevelBlocks(text)
	blocksTotal = len(blocks)
	kept := blocks
	if len(blocks) > pyEffectiveHunkCap() {
		// Float stride spanning the whole population, mirroring pyCorpusFiles'
		// own cap (pyoracle_test.go) — an integer stride truncates to a prefix
		// whenever len(blocks)/pyHunkCap floors to 1 (any file with 9-15
		// blocks at cap 8), and even where it doesn't, the last picked index
		// stays fixed at 7*floor(n/8), leaving the final ~1/8 of every large
		// file permanently unsampled (Fix 6a of the #313 review).
		stride := float64(len(blocks)) / float64(pyEffectiveHunkCap())
		kept = make([]pyBlock, 0, pyEffectiveHunkCap())
		for i := 0; i < pyEffectiveHunkCap(); i++ {
			idx := int(float64(i) * stride)
			if idx >= len(blocks) {
				idx = len(blocks) - 1
			}
			kept = append(kept, blocks[idx])
		}
	}
	blocksDropped = blocksTotal - len(kept)
	for _, blk := range kept {
		postures = append(postures, pyPostureSpec{name: "edit-hunk", fold: blk.Rest, added: blk.Body})
	}

	// inner-hunk is generated over ALL of this file's blocks (not the
	// edit-hunk-capped `kept` subset above) — its own eligibility filter
	// (pyInnerHunkForBlock) and its own pyHunkCap stride are independent of
	// edit-hunk's, so a file with e.g. 20 blocks where only 3 have an
	// eligible interior still gets all 3 measured, regardless of which 8 of
	// the 20 edit-hunk happened to keep.
	innerPostures, innerStats := pyInnerHunkPosturesForFile(text, blocks)
	postures = append(postures, innerPostures...)

	return postures, blocksTotal, blocksDropped, innerStats
}

// pyInnerHunkStats accumulates the fifth posture's per-file bookkeeping —
// plan section 9 wants inner-hunk's slice count reported next to edit-hunk's
// "blocks=" figure, including WHY a block was not sliced (its three
// ineligibility reasons) so a thin inner-hunk count reads as thin, not as a
// silent zero.
type pyInnerHunkStats struct {
	total        int // top-level blocks considered (same population edit-hunk starts from)
	noInterior   int // skipped: no lines after the header/decorators (header-only or one-line body)
	openString   int // skipped: interior's first line starts inside an open string
	bracketDepth int // skipped: interior's first line starts at nonzero ()/[]/{} depth
	eligible     int // passed all three checks, before the pyHunkCap stride
	dropped      int // eligible but dropped by the pyHunkCap stride
}

// pyInnerHunkForBlock builds the inner-hunk posture (plan section 7's fifth
// posture; see the file header for the finding that motivated it) for one
// top-level block: added is strictly the lines inside blk's body AFTER its
// def/class header line (and after any leading decorators) to the block's
// end; fold is the whole file MINUS exactly those lines, so the header — and
// therefore every name FoldInFileDefs' PyParamNames/PyDeclaredNames folds
// from it (parameters, walrus/loop/`as`-bound locals) — sits ONLY in the
// fold, never in the added hunk. That is what makes those two folds
// load-bearing for this posture, unlike edit-hunk, whose hunk always carries
// its own header.
//
// ok is false — and no posture is built — when:
//   - the block has no interior lines after its header (a one-line body, or
//     header-only def/class), reason "no-interior";
//   - the interior's first line starts inside an open triple-quoted string
//     (or a backslash-continued single/double-quoted string, tracked exactly
//     the way pyTopLevelBlocks itself tracks it via startsOpen/pyLineStates),
//     reason "open-string";
//   - the interior's first line starts at nonzero ()/[]/{}-bracket depth —
//     e.g. a multi-line def signature whose closing paren lands on a later
//     physical line, so "the line after the header" is still mid-signature,
//     not real body — reason "bracket-depth".
//
// Both checks exist because, unlike a top-level block BOUNDARY (which
// pyTopLevelBlocks' own scan guarantees starts at column 0, open-string ""),
// an INTERIOR line carries no such guarantee: it can start anywhere inside
// the function's body. Building a slice there anyway would let the
// consumer's zero-seed assumption (see runPyPostureChecks' doc comment) go
// unverified for the one posture built specifically to be LESS forgiving
// than edit-hunk — which would let this posture manufacture a false
// positive purely from the harness's own seeding gap, not from anything
// guard.Run or FoldInFileDefs actually did wrong (plan section 7,
// requirements 1-2). The same two values also certify the FOLD side's
// concatenation is clean at the seam: "depth/open-string state immediately
// BEFORE the interior's first line" is, by construction, exactly the state
// at the END of the header line — the point where fold's "before" half
// (ending at the header) is joined to its "after" half (starting right
// after the block's own end, which pyTopLevelBlocks' block-boundary
// invariant already guarantees is clean — the same invariant edit-hunk's
// own nil-seeded fold already relies on).
func pyInnerHunkForBlock(lines, startsOpen, scans []string, wholeFileLines []guard.AddedLine, blk pyBlock) (spec pyPostureSpec, startLine, endLine int, reason string, ok bool) {
	headerLine := blk.StartLine
	for headerLine <= blk.EndLine && strings.HasPrefix(scans[headerLine-1], "@") {
		headerLine++
	}
	interiorStart := headerLine + 1
	interiorEnd := blk.EndLine
	if interiorStart > interiorEnd {
		return pyPostureSpec{}, 0, 0, "no-interior", false
	}
	if startsOpen[interiorStart-1] != "" {
		return pyPostureSpec{}, 0, 0, "open-string", false
	}
	// idx=interiorStart-1 processes wholeFileLines[:interiorStart-1] — lines
	// 1..interiorStart-1 — giving the bracket depth in effect at the START of
	// interiorStart, per PyBracketDepthBefore's own doc comment.
	if depth := guard.PyBracketDepthBefore(wholeFileLines, interiorStart-1); depth != 0 {
		return pyPostureSpec{}, 0, 0, "bracket-depth", false
	}

	added := strings.Join(lines[interiorStart-1:interiorEnd], "\n")
	rest := make([]string, 0, len(lines)-(interiorEnd-interiorStart+1))
	rest = append(rest, lines[:interiorStart-1]...)
	rest = append(rest, lines[interiorEnd:]...)
	fold := strings.Join(rest, "\n")
	return pyPostureSpec{name: "inner-hunk", fold: fold, added: added}, interiorStart, interiorEnd, "", true
}

// pyInnerHunkPosturesForFile builds every eligible inner-hunk posture for
// text's top-level blocks, capped at pyHunkCap per file via the same
// evenly-spaced float stride pyPosturesForFile's own edit-hunk cap uses (see
// its doc comment for why a stride, not a prefix, over the ELIGIBLE
// population — capping before filtering would let a run of ineligible
// blocks starve the sample instead of spreading it across what actually
// qualified).
func pyInnerHunkPosturesForFile(text string, blocks []pyBlock) ([]pyPostureSpec, pyInnerHunkStats) {
	stats := pyInnerHunkStats{total: len(blocks)}
	if len(blocks) == 0 {
		return nil, stats
	}
	lines := strings.Split(text, "\n")
	startsOpen, scans := pyLineStates(text)
	wholeFileLines := guard.TextToAddedLines(text)

	var eligible []pyPostureSpec
	for _, blk := range blocks {
		spec, _, _, reason, ok := pyInnerHunkForBlock(lines, startsOpen, scans, wholeFileLines, blk)
		if !ok {
			switch reason {
			case "no-interior":
				stats.noInterior++
			case "open-string":
				stats.openString++
			case "bracket-depth":
				stats.bracketDepth++
			}
			continue
		}
		eligible = append(eligible, spec)
	}
	stats.eligible = len(eligible)

	kept := eligible
	if cap := pyEffectiveHunkCap(); len(eligible) > cap {
		stride := float64(len(eligible)) / float64(cap)
		kept = make([]pyPostureSpec, 0, cap)
		for i := 0; i < cap; i++ {
			idx := int(float64(i) * stride)
			if idx >= len(eligible) {
				idx = len(eligible) - 1
			}
			kept = append(kept, eligible[idx])
		}
	}
	stats.dropped = len(eligible) - len(kept)
	return kept, stats
}

// pyInnerHunkForLine returns the inner-hunk posture (fold/added) for a phase-2
// mutation at line, if the enclosing top-level block's interior slice is
// eligible per pyInnerHunkForBlock. ok is false when line sits at module
// level (no enclosing block — reason "module-level"), inside the enclosing
// block's own header/decorator lines (reason "line-in-header-or-decorator"),
// or the enclosing block's interior slice failed one of pyInnerHunkForBlock's
// eligibility checks (reason is that check's own string). Unlike
// pyEditHunkForLine, there is no module-level fallback: inner-hunk's whole
// point is separating a hunk from its OWN signature, and a module-level bare
// reference has no signature to separate from — see the file header's
// motivating finding. A mutation this returns ok=false for is simply not
// measured for this posture (mirrors how the precommit posture is "not
// measured" when git is unavailable) — it does not discard the mutation
// itself, which the other four postures still adjudicate.
func pyInnerHunkForLine(mutatedText string, line int) (fold, added string, ok bool, reason string) {
	lines := strings.Split(mutatedText, "\n")
	startsOpen, scans := pyLineStates(mutatedText)
	wholeFileLines := guard.TextToAddedLines(mutatedText)
	for _, blk := range pyTopLevelBlocks(mutatedText) {
		if line < blk.StartLine || line > blk.EndLine {
			continue
		}
		spec, startLine, endLine, blkReason, blkOK := pyInnerHunkForBlock(lines, startsOpen, scans, wholeFileLines, blk)
		if !blkOK {
			return "", "", false, blkReason
		}
		if line < startLine || line > endLine {
			return "", "", false, "line-in-header-or-decorator"
		}
		return spec.fold, spec.added, true, ""
	}
	return "", "", false, "module-level"
}

// runPyPostureChecks runs guard.Run and guard.FileScopeViolations for one
// in-memory posture (write-whole, write-new, edit-hunk, or inner-hunk) and
// returns every violation, attributed. Reuses resolve_differential_test.go's
// `attributed` type verbatim — it is check+posture+guard.Violation, language
// agnostic. No AbsPath/SeedByLine is set: write-whole/write-new have nothing
// to seed from (synthetic 1..N lines with no on-disk counterpart); edit-hunk's
// blocks are constructed by pyTopLevelBlocks to always start at column 0
// outside an open string (see its doc comment) — exactly the condition under
// which a nil seed and a real seed agree, per plan section 7; inner-hunk's
// slices are constructed by pyInnerHunkForBlock to satisfy that same
// condition, but CHECKED rather than structurally guaranteed (an interior
// line, unlike a block boundary, is not guaranteed clean by construction —
// see that function's doc comment for the two checks and why an ineligible
// slice is skipped rather than seeded).
func runPyPostureChecks(known, repoKnown map[string]struct{}, rel string, p pyPostureSpec) []attributed {
	var out []attributed
	added := guard.TextToAddedLines(p.added)
	var foldLines []guard.AddedLine
	symbols := make(map[string]struct{}, len(known)+16)
	for s := range known {
		symbols[s] = struct{}{}
	}
	if p.fold != "" {
		foldLines = guard.TextToAddedLines(p.fold)
		guard.FoldInFileDefs(symbols, foldLines, guard.LangPython)
	}
	fd := guard.FileDiff{Path: rel, AddedLines: added}
	for _, v := range guard.Run(symbols, "", []guard.FileDiff{fd}) {
		out = append(out, attributed{check: "Run", posture: p.name, v: v})
	}
	for _, v := range guard.FileScopeViolations(guard.LangPython, foldLines, fd, repoKnown) {
		out = append(out, attributed{check: "FileScope", posture: p.name, v: v})
	}
	return out
}

// pyRunSymbolSet extracts the set of symbols guard.Run flagged, for the
// write-whole == write-new control assertion (plan section 7's "control": for
// Python, Run's Pass 1 folds the identical four extractors FoldInFileDefs
// does, so the two postures must agree exactly — this is a harness/fold
// integrity check, not an oracle-dependent one, and runs on every processed
// file regardless of its oracle state).
func pyRunSymbolSet(atts []attributed) map[string]bool {
	out := map[string]bool{}
	for _, a := range atts {
		if a.check == "Run" {
			out[a.v.Symbol] = true
		}
	}
	return out
}

func pySetsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// pre-commit posture — phase 1 (plan section 7's driver)
// ---------------------------------------------------------------------------

// runPyPrecommitPhase1 measures the precommit posture for phase 1: for each
// staged file with at least one top-level block, it re-bases HEAD to the file
// MINUS its largest block (so the block reads as newly added), restores the
// full file, stages it, and runs guard.Run/guard.FileScopeViolations against
// the real guard.ParseStagedDiff output — the only posture that exercises the
// AbsPath seed chain (openSeedFor, pyBraceDepthSeedFor, ...), per plan section
// 6. Files with no top-level block report "n/a", not zero (filesNA).
//
// This needs an extra "pre" commit (unlike phase 2's per-mutation precommit,
// see runPyPrecommitOutcome) because HEAD already holds the FULL file from
// stagePyCorpus's baseline commit — to make the block read as an addition,
// HEAD must first be moved to a version that doesn't have it.
// filesParsed is the ACTUAL count ParseStagedDiff returned — distinct from
// filesWithBlock (what the driver picked and intended to measure). They agree
// whenever ParseStagedDiff behaves, but a caller that prints filesWithBlock
// would keep reporting the intended count even if the parser silently
// returned far fewer diffs (Fix 7c of the #313 review) — always report the
// actual parsed count, not the intended one.
func runPyPrecommitPhase1(t *testing.T, staged string, stagedRels []string, known, repoKnown map[string]struct{}) (byRel map[string][]attributed, filesWithBlock, filesParsed, filesNA int) {
	t.Helper()
	byRel = map[string][]attributed{}

	type picked struct {
		rel  string
		blk  pyBlock
		orig string
	}
	var picks []picked
	for _, rel := range stagedRels {
		data, err := os.ReadFile(filepath.Join(staged, rel))
		if err != nil {
			continue
		}
		text := string(data)
		blocks := pyTopLevelBlocks(text)
		if len(blocks) == 0 {
			filesNA++
			continue
		}
		best := blocks[0]
		for _, b := range blocks[1:] {
			if (b.EndLine - b.StartLine) > (best.EndLine - best.StartLine) {
				best = b
			}
		}
		picks = append(picks, picked{rel: rel, blk: best, orig: text})
	}
	filesWithBlock = len(picks)
	if len(picks) == 0 {
		return byRel, 0, 0, filesNA
	}

	for _, p := range picks {
		if err := os.WriteFile(filepath.Join(staged, p.rel), []byte(p.blk.Rest), 0o644); err != nil {
			t.Fatalf("precommit phase1: write minus-block %s: %v", p.rel, err)
		}
	}
	if !pyRunGit(t, staged, "commit", "-q", "-am", "pre") {
		for _, p := range picks {
			_ = os.WriteFile(filepath.Join(staged, p.rel), []byte(p.orig), 0o644)
		}
		t.Logf("precommit phase1: git commit failed — precommit posture not measured this run")
		return byRel, filesWithBlock, 0, filesNA
	}
	for _, p := range picks {
		if err := os.WriteFile(filepath.Join(staged, p.rel), []byte(p.orig), 0o644); err != nil {
			t.Fatalf("precommit phase1: restore %s: %v", p.rel, err)
		}
	}
	if !pyRunGit(t, staged, "add", "-A") {
		t.Logf("precommit phase1: git add -A failed — precommit posture not measured this run")
		return byRel, filesWithBlock, 0, filesNA
	}

	diffs, partial, err := guard.ParseStagedDiff(context.Background(), staged)
	if err != nil {
		t.Logf("precommit phase1: ParseStagedDiff failed: %v — precommit posture not measured this run", err)
		pyRunGit(t, staged, "add", "-A")
		return byRel, filesWithBlock, 0, filesNA
	}
	if partial {
		t.Logf("precommit phase1: ParseStagedDiff reported partial coverage")
	}
	filesParsed = len(diffs)

	for _, fd := range diffs {
		fd.AbsPath = filepath.Join(staged, fd.Path)
		data, err := os.ReadFile(fd.AbsPath)
		if err != nil {
			continue
		}
		symbols := make(map[string]struct{}, len(known)+16)
		for s := range known {
			symbols[s] = struct{}{}
		}
		wholeFileLines := guard.TextToAddedLines(string(data))
		guard.FoldInFileDefs(symbols, wholeFileLines, guard.LangPython)

		var out []attributed
		for _, v := range guard.Run(symbols, "", []guard.FileDiff{fd}) {
			out = append(out, attributed{check: "Run", posture: "precommit", v: v})
		}
		for _, v := range guard.FileScopeViolations(guard.LangPython, wholeFileLines, fd, repoKnown) {
			out = append(out, attributed{check: "FileScope", posture: "precommit", v: v})
		}
		byRel[fd.Path] = out
	}

	// Tree already equals baseline (every picked file was restored above); this
	// only clears the index that the "add -A" a few lines up populated.
	pyRunGit(t, staged, "add", "-A")
	return byRel, filesWithBlock, filesParsed, filesNA
}

// ---------------------------------------------------------------------------
// Phase 1 — false positives, proven by ruff on unmutated code
// ---------------------------------------------------------------------------

// pyFlag is one guard flag, attributed to the file/check/posture/line it came
// from — the Python analogue of resolve_differential_test.go's inline `fp`
// struct, shared here across the false-positive, unadjudicated, and noisy
// report tables so they can use one formatter.
type pyFlag struct {
	file, symbol, check, posture string
	line                         int
}

// formatPyFlagBlock renders one CHECK/POSTURE table plus top offenders and
// example sites, per plan section 9's report format. title carries its own
// parenthetical (e.g. "oracle-clean files only") so this stays generic across
// the three tables that use it.
func formatPyFlagBlock(title string, flags []pyFlag) string {
	var b strings.Builder
	if len(flags) == 0 {
		fmt.Fprintf(&b, "%s: 0\n", title)
		return b.String()
	}
	type cpKey struct{ check, posture string }
	counts := map[cpKey]int{}
	files := map[cpKey]map[string]bool{}
	byName := map[string]int{}
	for _, f := range flags {
		k := cpKey{f.check, f.posture}
		counts[k]++
		if files[k] == nil {
			files[k] = map[string]bool{}
		}
		files[k][f.file] = true
		byName[f.symbol]++
	}
	keys := make([]cpKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].check != keys[j].check {
			return keys[i].check < keys[j].check
		}
		return keys[i].posture < keys[j].posture
	})
	// "occurrences", not "distinct flags": edit-hunk calls the checks once
	// PER BLOCK, so the same (file, symbol, check, posture) can appear more
	// than once here (e.g. QUOTE_MINIMAL@8 and QUOTE_MINIMAL@34 on csv.py
	// both counted under FileScope/edit-hunk) — the count below is the raw
	// occurrence total, conservative by construction, just not "distinct"
	// (Fix 7a of the #313 review; the counting itself is unchanged).
	fmt.Fprintf(&b, "%s: %d occurrences\n", title, len(flags))
	fmt.Fprintf(&b, "%-24s %8s %8s\n", "CHECK/POSTURE", "FLAGS", "FILES")
	for _, k := range keys {
		fmt.Fprintf(&b, "%-24s %8d %8d\n", k.check+"/"+k.posture, counts[k], len(files[k]))
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if byName[names[i]] != byName[names[j]] {
			return byName[names[i]] > byName[names[j]]
		}
		return names[i] < names[j]
	})
	shownN := len(names)
	if shownN > 20 {
		shownN = 20
	}
	for _, n := range names[:shownN] {
		fmt.Fprintf(&b, "  %-40s x%d\n", n, byName[n])
	}
	shown := len(flags)
	if shown > 25 {
		shown = 25
	}
	for _, f := range flags[:shown] {
		// edit-hunk's and inner-hunk's lines are both hunk-relative
		// (guard.TextToAddedLines synthesizes 1..N for the slice, per
		// pyPosturesForFile/pyInnerHunkPosturesForFile) — unmarked, either
		// prints beside real file line numbers from every other posture and
		// reads as the same site when it isn't (e.g. "runpy.py:10" from
		// edit-hunk and "runpy.py:114" from write-whole can name the SAME
		// line; Fix 7b of the #313 review). Mark both so nobody chases the
		// wrong line.
		lineLabel := fmt.Sprintf("%d", f.line)
		if f.posture == "edit-hunk" || f.posture == "inner-hunk" {
			lineLabel = fmt.Sprintf("%d(hunk-relative)", f.line)
		}
		fmt.Fprintf(&b, "  [%s/%s] %s:%s  %s\n", f.check, f.posture, f.file, lineLabel, f.symbol)
	}
	if len(flags) > shown {
		fmt.Fprintf(&b, "  ... %d more sites not shown\n", len(flags)-shown)
	}
	return b.String()
}

func TestPyResolveNoFalsePositivesAgainstRuff(t *testing.T) {
	ruff := ruffBin(t)
	root, isDefault := pyCorpusRoot(t)
	rels := pyCorpusFiles(t, root, isDefault)
	if len(rels) == 0 {
		t.Skipf("no .py files under corpus root %s", root)
	}
	staged, hasGit, stagedRels := stagePyCorpus(t, root, rels)
	if len(stagedRels) == 0 {
		t.Skip("no files could be staged")
	}
	if !hasGit {
		t.Logf("git unavailable/staging failed — precommit posture will not be measured this run")
	}

	known := knownSet(t, staged)
	// FileScopeViolations' repoKnown is the UNFOLDED store set (mirrors
	// snapshotSymbols at cmd/runecho-guard/main.go:819) — the same `known`
	// knownSet returns, before any posture folds anything into it.
	repoKnown := known

	absPaths := pyResolvedAbsPaths(t, staged, stagedRels)
	verdicts := ruffF821(t, ruff, absPaths...)
	verdictByRel := make(map[string]oracleVerdict, len(stagedRels))
	for i, rel := range stagedRels {
		verdictByRel[rel] = verdicts[absPaths[i]]
	}

	var cleanN, silentN, noisyN, unadjN int
	var fps, unadjudicatedFlags, noisyFlags []pyFlag
	filesChecked, linesChecked := 0, 0
	blocksTotal, blocksDropped := 0, 0
	var innerStatsTotal pyInnerHunkStats
	controlMatch, controlTotal := 0, 0

	for _, rel := range stagedRels {
		v := verdictByRel[rel]
		if v.State == oracleUnadjudicable {
			unadjN++
			continue
		}
		data, err := os.ReadFile(filepath.Join(staged, rel))
		if err != nil {
			t.Logf("phase1: could not read staged file %s: %v — treating as unadjudicable", rel, err)
			unadjN++
			continue
		}
		text := string(data)
		filesChecked++
		linesChecked += strings.Count(text, "\n")

		postures, bTotal, bDropped, innerStats := pyPosturesForFile(text)
		blocksTotal += bTotal
		blocksDropped += bDropped
		innerStatsTotal.total += innerStats.total
		innerStatsTotal.noInterior += innerStats.noInterior
		innerStatsTotal.openString += innerStats.openString
		innerStatsTotal.bracketDepth += innerStats.bracketDepth
		innerStatsTotal.eligible += innerStats.eligible
		innerStatsTotal.dropped += innerStats.dropped

		byPosture := map[string][]attributed{}
		for _, p := range postures {
			byPosture[p.name] = append(byPosture[p.name], runPyPostureChecks(known, repoKnown, rel, p)...)
		}

		// Control (plan section 7): write-whole and write-new MUST agree
		// exactly for guard.Run, independent of ruff's verdict — the #313 trap
		// itself is a harness that never checks this. A mismatch is a
		// seed/fold bug in the harness or the product, not a property of the
		// corpus, so it is a hard failure regardless of oracle state.
		controlTotal++
		ww := pyRunSymbolSet(byPosture["write-whole"])
		wn := pyRunSymbolSet(byPosture["write-new"])
		if pySetsEqual(ww, wn) {
			controlMatch++
		} else {
			t.Errorf("control violated: write-whole and write-new disagree on %s: write-whole=%v write-new=%v",
				rel, ww, wn)
		}

		var dest *[]pyFlag
		switch v.State {
		case oracleClean:
			cleanN++
			dest = &fps
		case oracleSilent:
			silentN++
			dest = &unadjudicatedFlags
		case oracleNoisy:
			noisyN++
			dest = &noisyFlags
		}
		for postureName, atts := range byPosture {
			for _, a := range atts {
				*dest = append(*dest, pyFlag{file: rel, symbol: a.v.Symbol, line: a.v.Line, check: a.check, posture: postureName})
			}
		}
	}

	// Vacuity (plan section 10 fail condition 4): zero ruff-adjudicable files
	// is no evidence either way. On the default corpus that is a defect (the
	// oracle or corpus discovery broke); on an env-pointed one it's simply an
	// empty run.
	if filesChecked == 0 {
		if isDefault {
			t.Fatalf("vacuity: zero ruff-adjudicable files in the default corpus (%d unadjudicable of %d staged) — "+
				"the oracle or corpus discovery is broken", unadjN, len(stagedRels))
		}
		t.Skip("vacuity: zero ruff-adjudicable files in the env-pointed corpus")
	}

	filesWithBlock, filesParsed, filesNA := 0, 0, 0
	if hasGit {
		precommitByRel, fwb, fp, fna := runPyPrecommitPhase1(t, staged, stagedRels, known, repoKnown)
		filesWithBlock, filesParsed, filesNA = fwb, fp, fna
		for rel, atts := range precommitByRel {
			v := verdictByRel[rel]
			var dest *[]pyFlag
			switch v.State {
			case oracleClean:
				dest = &fps
			case oracleSilent:
				dest = &unadjudicatedFlags
			case oracleNoisy:
				dest = &noisyFlags
			default:
				continue
			}
			for _, a := range atts {
				*dest = append(*dest, pyFlag{file: rel, symbol: a.v.Symbol, line: a.v.Line, check: a.check, posture: a.posture})
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "corpus=%s default=%t python=%s ruff=%s files=%d lines=%d known-symbols=%d\n",
		root, isDefault, pyInterpreterVersion(), pyRuffVersion(ruff), filesChecked, linesChecked, len(known))
	fmt.Fprintf(&b, "oracle: clean=%d silent(F403)=%d noisy(pre-existing F821)=%d unadjudicable=%d zero-byte=%d\n",
		cleanN, silentN, noisyN, unadjN, pyCountZeroByteFiles(root, isDefault))
	precommitNote := ""
	if !hasGit {
		precommitNote = " (git unavailable — not measured)"
	}
	// precommit files=%d prints filesParsed — what guard.ParseStagedDiff
	// actually returned — not filesWithBlock (what the driver picked and
	// intended to measure); they usually agree, but only filesParsed is true
	// when the parser silently returns fewer diffs than intended (Fix 7c of
	// the #313 review).
	//
	// inner-hunk's slices= is its eligible-and-kept count (post-cap, the
	// actual population measured below); skipped breaks down WHY the rest of
	// its `total` top-level blocks never became a slice, so a thin inner-hunk
	// count reads as "most blocks were one-liners" or "most interiors start
	// mid-string/mid-signature", not as a silent zero.
	fmt.Fprintf(&b, "postures: edit-hunk blocks=%d (cap %d/file, %d dropped) "+
		"inner-hunk slices=%d/%d blocks (cap %d/file, %d dropped-by-cap, skipped: no-interior=%d open-string=%d bracket-depth=%d) "+
		"precommit files=%d/%d picked (n/a %d)%s\n",
		blocksTotal, pyHunkCap, blocksDropped,
		innerStatsTotal.eligible-innerStatsTotal.dropped, innerStatsTotal.total, pyHunkCap, innerStatsTotal.dropped,
		innerStatsTotal.noInterior, innerStatsTotal.openString, innerStatsTotal.bracketDepth,
		filesParsed, filesWithBlock, filesNA, precommitNote)
	fmt.Fprintf(&b, "control: write-whole == write-new on %d/%d files\n\n", controlMatch, controlTotal)

	linesPerK := float64(max(linesChecked, 1)) / 1000
	b.WriteString(formatPyFlagBlock(
		fmt.Sprintf("PROVEN FALSE POSITIVES (oracle-clean files only): %d over %d files / %d lines (%.2f per KLOC)",
			len(fps), filesChecked, linesChecked, float64(len(fps))/linesPerK),
		fps))
	b.WriteString("\n")
	b.WriteString(formatPyFlagBlock("UNADJUDICATED (oracle-silent files, NOT counted)", unadjudicatedFlags))
	b.WriteString("\n")
	b.WriteString(formatPyFlagBlock("NOISY (oracle pre-existing-F821 files, NOT counted)", noisyFlags))
	t.Log("\n" + b.String())

	// Only guard.Run false positives fail the build (plan section 10, fail
	// condition 1). FileScope is gated off by default in production, so a
	// finding there is real but report-only — see the plan's argument for why
	// promoting it is a one-line change deferred to a follow-up, not this PR.
	//
	// Known gaps (pyKnownGaps) are subtracted from the FAIL set only. They are
	// still measured, still counted in the PROVEN FALSE POSITIVES total above,
	// and still printed below — a silenced instrument that stops reporting is
	// how a real defect becomes invisible.
	var runFPs, knownFPs []pyFlag
	seenGap := map[string]bool{}
	for _, f := range fps {
		if f.check != "Run" {
			continue
		}
		if _, known := pyKnownGaps[f.symbol]; known {
			seenGap[f.symbol] = true
			knownFPs = append(knownFPs, f)
			continue
		}
		runFPs = append(runFPs, f)
	}

	if len(knownFPs) > 0 {
		var kb strings.Builder
		for sym := range seenGap {
			fmt.Fprintf(&kb, "  %-14s %s — %s\n", sym, pyKnownGaps[sym].issue, pyKnownGaps[sym].reason)
		}
		t.Logf("KNOWN GAPS excluded from the fail set: %d flag(s) across %d symbol(s), each tracked by a filed issue:\n%s%s",
			len(knownFPs), len(seenGap), kb.String(),
			formatPyFlagBlock("Known-gap flags (measured, not failing)", knownFPs))
	}

	// The allowlist fails in BOTH directions on a full, unsampled run of the
	// default corpus, matching .github/expected-skips.txt and bench/hookmutate's
	// known_gap discipline: an entry that no longer fires means the gap was
	// fixed (or the corpus moved) and the list is now lying about what this
	// harness tolerates. mutations.json has already drifted out of sync with
	// the code it named once; this gets the same treatment.
	//
	// Gated on fullDefaultCorpus, matching every other fail condition in this
	// file (vacuity above, the pySites parse-majority check, the phase-2
	// vacuity checks, the bare-call fail below) in SPIRIT — but plain isDefault
	// is not enough here (Fix 2 of the #313 review): isDefault only tracks the
	// corpus ROOT (RUNECHO_ORACLE_PY_CORPUS unset), while pyCorpusFiles applies
	// RUNECHO_ORACLE_PY_FILES's stride-sampling cap independently of that root,
	// so `isDefault` alone stays true for a RUNECHO_ORACLE_PY_FILES=20 run even
	// though only 20 of the stdlib's ~1900 .py files were actually scanned.
	// pyKnownGaps was measured against the FULL CPython stdlib, so neither a
	// smaller sample of it nor a different corpus (RUNECHO_ORACLE_PY_CORPUS)
	// has any reason to contain both a __import__ and a SystemError bare-call
	// false positive, and failing the build over their absence there measures
	// the corpus, not the guard. Still worth knowing about on a non-full run,
	// so log it instead.
	fullDefaultCorpus := isDefault && os.Getenv("RUNECHO_ORACLE_PY_FILES") == ""
	if fullDefaultCorpus {
		for sym, gap := range pyKnownGaps {
			if !seenGap[sym] {
				t.Errorf("STALE known gap %q (%s): it no longer produces a false positive, so the entry is fiction — "+
					"delete it from pyKnownGaps and let the harness enforce the symbol again", sym, gap.issue)
			}
		}
	} else {
		for sym, gap := range pyKnownGaps {
			if !seenGap[sym] {
				t.Logf("known gap %q (%s) did not fire on this run — not failing (only a full, unsampled run of "+
					"the default corpus enforces staleness on pyKnownGaps)", sym, gap.issue)
			}
		}
	}

	if len(runFPs) > 0 {
		t.Errorf("PROVEN FALSE POSITIVES: guard.Run flagged %d site(s) on file(s) ruff proves clean — every one of "+
			"these is a defect with no counter-argument:\n%s", len(runFPs), formatPyFlagBlock("Run false positives", runFPs))
	}
}

// pyKnownGap is one tolerated false-positive class: a defect that IS real, is
// filed, and is deliberately out of scope for the PR that introduced this
// harness. A reason is required for the same purpose .github/expected-skips.txt
// requires one — you cannot silence an instrument here without writing down why.
type pyKnownGap struct {
	issue  string
	reason string
}

// pyKnownGaps subtracts measured-but-filed defects from phase 1's FAIL set.
//
// Keyed by SYMBOL, not by file:line. A file:line entry would still stop
// matching when the corpus moves — that is not what symbol keying prevents,
// and the STALE check above fails loudly either way, it does not fail
// silently. What symbol keying actually buys: (1) it tolerates the whole
// class the issue names, not one accidental occurrence of it, so the entry
// stays meaningful across CPython versions where the surrounding code
// differs; and (2) it survives line-number drift between interpreter
// versions, where a file:line entry would go stale on every version bump
// even though the underlying gap (an incomplete pyBuiltins) hasn't changed.
//
// Both entries here are #387 — an incomplete pyBuiltins that also disagrees with
// filescope.go's own separate list. #388 (backslash-continued `from M import`)
// needs no entry: it surfaces only through FileScope, which is already
// report-only above. If FileScope is ever promoted to a fail condition, #388's
// symbols must be added here or that promotion goes red on a filed defect.
var pyKnownGaps = map[string]pyKnownGap{
	"__import__":  {"#387", "pyBuiltins omits __import__; filescope.go:60 has it, extract.go:111-143 does not"},
	"SystemError": {"#387", "pyBuiltins omits the SystemError builtin exception"},
}

// ---------------------------------------------------------------------------
// Phase 2 — false negatives, proven by a ruff-adjudicated mutation
// ---------------------------------------------------------------------------

// pyMutationSite is one candidate rewrite, before ruff has ruled on it — the
// Python analogue of resolve_differential_test.go's mutationSite (that type
// carries Go-specific fields this doesn't need, so it is not reused directly).
type pyMutationSite struct {
	rel, name, fresh string
	line, col        int
	shape            pyShape
}

// pyFreshName derives the fresh identifier for a site. bare-const uses
// pyConstSuffix (stays SCREAMING_SNAKE); every other shape uses the shared
// mutationSuffix from resolve_differential_test.go.
func pyFreshName(shape pyShape, name string) string {
	if shape == pyShapeBareConst {
		return name + pyConstSuffix
	}
	return name + mutationSuffix
}

// pyShapeOwner names the check that owns a shape's population, per plan
// section 8's table. Only bare-call/bare-const are Run's declared population;
// every other shape (including attr-member, which is never mutated at all —
// ruff is verifiably silent on it) has no owning check today.
func pyShapeOwner(sh pyShape) string {
	switch sh {
	case pyShapeBareCall, pyShapeBareConst:
		return "Run"
	default:
		return "-"
	}
}

// pyMutableShapes is every shape phase 2 mutates. attr-member is deliberately
// excluded (plan section 8: "not mutated at all — ruff is verifiably silent
// on it"), but its raw population is still reported so the blind spot stays
// visible rather than silently absent from the numbers.
var pyMutableShapes = []pyShape{
	pyShapeBareCall, pyShapeBareConst, pyShapeBareName, pyShapeAttrBase,
	pyShapeDecorator, pyShapeClassBase, pyShapeAnnotation, pyShapeExceptClass,
}

// pyOutcome is one posture's verdict on one proven mutation: whether ANY
// check caught the fresh name, and which one (empty if none).
type pyOutcome struct {
	caught bool
	check  string
}

// pyEditHunkForLine returns the edit-hunk posture for a mutation at line: the
// top-level block enclosing it (fold = rest of file, added = block body), or
// — for a module-level site outside any block — the single mutated line
// itself as a one-line hunk (fold = file minus that line), per plan section 8
// ("module-level sites use the single mutated line as the hunk").
func pyEditHunkForLine(mutatedText string, line int) (fold, added string) {
	for _, blk := range pyTopLevelBlocks(mutatedText) {
		if line >= blk.StartLine && line <= blk.EndLine {
			return blk.Rest, blk.Body
		}
	}
	lines := strings.Split(mutatedText, "\n")
	idx := line - 1
	if idx < 0 || idx >= len(lines) {
		return mutatedText, ""
	}
	added = lines[idx]
	rest := make([]string, 0, len(lines)-1)
	rest = append(rest, lines[:idx]...)
	rest = append(rest, lines[idx+1:]...)
	fold = strings.Join(rest, "\n")
	return fold, added
}

// pyCheckMutation runs guard.Run and guard.FileScopeViolations for one
// in-memory posture over a mutated file and reports whether either caught
// fresh, and which.
func pyCheckMutation(known, repoKnown map[string]struct{}, rel, fold, added, fresh, absPath string) (bool, string) {
	addedLines := guard.TextToAddedLines(added)
	var foldLines []guard.AddedLine
	symbols := make(map[string]struct{}, len(known)+16)
	for s := range known {
		symbols[s] = struct{}{}
	}
	if fold != "" {
		foldLines = guard.TextToAddedLines(fold)
		guard.FoldInFileDefs(symbols, foldLines, guard.LangPython)
	}
	fd := guard.FileDiff{Path: rel, AddedLines: addedLines, AbsPath: absPath}
	for _, v := range guard.Run(symbols, "", []guard.FileDiff{fd}) {
		if v.Symbol == fresh {
			return true, "Run"
		}
	}
	for _, v := range guard.FileScopeViolations(guard.LangPython, foldLines, fd, repoKnown) {
		if v.Symbol == fresh {
			return true, "FileScope"
		}
	}
	return false, ""
}

// pyPrecommitOutcomeForMutation is phase 2's precommit driver: unlike phase
// 1's (runPyPrecommitPhase1), HEAD already holds the ORIGINAL file (from
// stagePyCorpus's baseline commit), so staging the already-mutated on-disk
// file directly produces a one-line hunk against real history — no extra
// "pre" commit needed (plan section 7's precommit row / section 7's
// phase-2-specific driver). measured=false means "not run" (no git, or a git
// step failed) — the caller must not count this posture's denominator then.
func pyPrecommitOutcomeForMutation(t *testing.T, staged, rel, fresh string, known, repoKnown map[string]struct{}) (caught bool, check string, measured bool) {
	t.Helper()
	if !pyRunGit(t, staged, "add", "--", rel) {
		return false, "", false
	}
	diffs, _, err := guard.ParseStagedDiff(context.Background(), staged)
	if err != nil {
		t.Logf("phase2 precommit: ParseStagedDiff failed on %s: %v", rel, err)
		pyRunGit(t, staged, "add", "--", rel)
		return false, "", false
	}
	var fd *guard.FileDiff
	for i := range diffs {
		if diffs[i].Path == rel {
			fd = &diffs[i]
			break
		}
	}
	if fd == nil {
		// The mutation produced no staged diff against HEAD — should not
		// happen for a single-character rewrite, but fail-soft rather than
		// panic if it ever does.
		return false, "", false
	}
	fd.AbsPath = filepath.Join(staged, rel)
	data, err := os.ReadFile(fd.AbsPath)
	if err != nil {
		return false, "", false
	}
	symbols := make(map[string]struct{}, len(known)+16)
	for s := range known {
		symbols[s] = struct{}{}
	}
	wholeFileLines := guard.TextToAddedLines(string(data))
	guard.FoldInFileDefs(symbols, wholeFileLines, guard.LangPython)
	for _, v := range guard.Run(symbols, "", []guard.FileDiff{*fd}) {
		if v.Symbol == fresh {
			return true, "Run", true
		}
	}
	for _, v := range guard.FileScopeViolations(guard.LangPython, wholeFileLines, *fd, repoKnown) {
		if v.Symbol == fresh {
			return true, "FileScope", true
		}
	}
	return false, "", true
}

// pyRunAllPostures runs all five postures for one proven mutation, keyed by
// posture name. "precommit" is absent from the map (not just false) when it
// was not measured (no git or a git step failed); "inner-hunk" is absent for
// the same reason whenever pyInnerHunkForLine reports ok=false (module-level
// mutation, mutation inside a header/decorator line, or an ineligible
// enclosing block — see its doc comment) — callers must check for presence,
// not just zero-value, to keep every posture's denominator honest.
func pyRunAllPostures(t *testing.T, staged string, hasGit bool, rel, mutatedText string, line int, fresh string, known, repoKnown map[string]struct{}) map[string]pyOutcome {
	out := map[string]pyOutcome{}

	c, chk := pyCheckMutation(known, repoKnown, rel, mutatedText, mutatedText, fresh, "")
	out["write-whole"] = pyOutcome{caught: c, check: chk}

	c, chk = pyCheckMutation(known, repoKnown, rel, "", mutatedText, fresh, "")
	out["write-new"] = pyOutcome{caught: c, check: chk}

	hFold, hAdded := pyEditHunkForLine(mutatedText, line)
	c, chk = pyCheckMutation(known, repoKnown, rel, hFold, hAdded, fresh, "")
	out["edit-hunk"] = pyOutcome{caught: c, check: chk}

	if iFold, iAdded, ok, _ := pyInnerHunkForLine(mutatedText, line); ok {
		c, chk = pyCheckMutation(known, repoKnown, rel, iFold, iAdded, fresh, "")
		out["inner-hunk"] = pyOutcome{caught: c, check: chk}
	}

	if hasGit {
		if c, chk, measured := pyPrecommitOutcomeForMutation(t, staged, rel, fresh, known, repoKnown); measured {
			out["precommit"] = pyOutcome{caught: c, check: chk}
		}
	}
	return out
}

// pyShapeResult accumulates one shape's phase-2 outcome across the run.
type pyShapeResult struct {
	proven        int
	postureProven map[string]int
	postureCaught map[string]int
	catchBy       map[string]map[string]int // posture -> check -> count
	missCount     int                       // TRUE count of proven-AND-unseen-by-every-posture mutations
	totalMisses   []string                  // capped-at-8 EXAMPLES of missCount, for printing only — never use len() of this as a count (Fix 1 of the #313 review: it was, and silently truncated the headline miss metric for any budget > 64)
	discardedExs  []string
}

func newPyShapeResult() *pyShapeResult {
	return &pyShapeResult{
		postureProven: map[string]int{},
		postureCaught: map[string]int{},
		catchBy:       map[string]map[string]int{},
	}
}

var pyPostureOrder = []string{"write-whole", "write-new", "edit-hunk", "inner-hunk", "precommit"}

func TestPyResolveFalseNegativesAgainstRuff(t *testing.T) {
	if testing.Short() {
		t.Skip("phase 2 pays one ruff invocation and up to two git operations per mutation; skipped in -short")
	}
	ruff := ruffBin(t)
	root, isDefault := pyCorpusRoot(t)
	rels := pyCorpusFiles(t, root, isDefault)
	if len(rels) == 0 {
		t.Skipf("no .py files under corpus root %s", root)
	}
	staged, hasGit, stagedRels := stagePyCorpus(t, root, rels)
	if len(stagedRels) == 0 {
		t.Skip("no files could be staged")
	}
	if !hasGit {
		t.Logf("git unavailable/staging failed — precommit posture will not be measured this run")
	}

	known := knownSet(t, staged)
	repoKnown := known

	absPaths := pyResolvedAbsPaths(t, staged, stagedRels)
	baseline := ruffF821(t, ruff, absPaths...)
	baselineByRel := make(map[string]oracleVerdict, len(stagedRels))
	for i, rel := range stagedRels {
		baselineByRel[rel] = baseline[absPaths[i]]
	}

	// Eligible files: silent (ruff cannot prove anything) and unadjudicable
	// are excluded. Noisy files ARE eligible — the differential proof rule
	// below never trusts ruff's raw pre-existing opinion, only an exact fresh
	// name at an exact line that is ALSO absent from the baseline verdict.
	var eligibleRels []string
	for _, rel := range stagedRels {
		switch baselineByRel[rel].State {
		case oracleSilent, oracleUnadjudicable:
			continue
		}
		eligibleRels = append(eligibleRels, rel)
	}
	if len(eligibleRels) == 0 {
		t.Skip("no ruff-adjudicable files (every staged file is oracle-silent or unadjudicable)")
	}

	sites, stats := pySites(t, staged, eligibleRels)
	if stats.Parsed == 0 {
		t.Fatalf("pySites parsed 0/%d eligible file(s) (skipped=%d) — the site enumerator is broken, "+
			"or the corpus is not real Python at the version pySites' embedded ast.parse expects", len(eligibleRels), stats.Skipped)
	}
	// A corpus where MOST files fail to parse must not read as a clean small
	// run (task instructions, mirroring plan section 13's spirit for
	// pySitesStats). On the default corpus that's a defect worth failing
	// loudly for; on an env-pointed one it's simply a bad corpus, so skip
	// rather than fail the build.
	if total := stats.Skipped + stats.Parsed; total > 0 && stats.Skipped*2 > total {
		msg := fmt.Sprintf("pySites: majority of eligible files failed to parse (skipped=%d parsed=%d)", stats.Skipped, stats.Parsed)
		if isDefault {
			t.Fatalf("%s on the default corpus — refusing to treat this as a valid small run", msg)
		}
		t.Skipf("%s on the env-pointed corpus", msg)
	}

	var wholeCorpus strings.Builder
	for _, rel := range eligibleRels {
		data, err := os.ReadFile(filepath.Join(staged, rel))
		if err != nil {
			continue
		}
		wholeCorpus.Write(data)
		wholeCorpus.WriteByte('\n')
	}
	corpusText := wholeCorpus.String()

	// Freshness is proven ONCE for the whole corpus, not per site.
	//
	// Every fresh name is `<original><suffix>` for one of exactly two fixed
	// suffixes, so if neither suffix occurs anywhere in the corpus then no
	// derived fresh name can either — a strictly stronger guarantee than the
	// per-site substring test it replaces, and one exec of the scan instead of
	// one per site.
	//
	// The per-site form was O(sites x corpus bytes): 110,496 enumerated sites
	// against ~5 MB of text is ~550 GB of scanning, and it was the reason this
	// phase cost 169s of FIXED time at any mutation budget (measured: 20 files
	// 2.9s, 155 files 169s — 7.75x the corpus for 58x the time). Lowering the
	// mutation budget did not touch it, because the cost is in building the
	// pools, not in mutating.
	for _, suffix := range []string{mutationSuffix, pyConstSuffix} {
		if strings.Contains(corpusText, suffix) {
			t.Fatalf("mutation suffix %q already occurs in the corpus — every fresh name derived from it "+
				"would be indistinguishable from pre-existing text, so no mutation in this run could be "+
				"trusted; change the suffix", suffix)
		}
	}

	pools := map[pyShape][]pyMutationSite{}
	attrMemberRaw := 0
	skippedPastCap := 0
	for _, s := range sites {
		if s.Shape == pyShapeAttrMember {
			attrMemberRaw++
			continue
		}
		fresh := pyFreshName(s.Shape, s.Name)
		// Absence from the corpus text is already proven for every fresh name
		// by the one-time suffix check above. What remains is the repo-wide
		// known index (plan section 8): a fresh name already indexed would be
		// a fake miss, since the guard would resolve it for a reason unrelated
		// to the mutation. That is a map lookup, so it stays per-site.
		if _, inIndex := known[fresh]; inIndex {
			continue
		}
		// capLine truncation (internal/guard/util.go:22): a site past this
		// column is invisible to the guard by design. Skip, don't score.
		if s.Col >= pyCapLineBytes {
			skippedPastCap++
			continue
		}
		pools[s.Shape] = append(pools[s.Shape], pyMutationSite{
			rel: s.RelPath, line: s.Line, col: s.Col, name: s.Name, fresh: fresh, shape: s.Shape,
		})
	}
	if skippedPastCap > 0 {
		t.Logf("phase2: %d site(s) skipped — past capLine's %d-byte truncation, invisible to the guard by design", skippedPastCap, pyCapLineBytes)
	}

	budget := pyEffectiveMutations(defaultPyMutations)
	if v := os.Getenv("RUNECHO_ORACLE_MUTATIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("RUNECHO_ORACLE_MUTATIONS must be a positive integer, got %q", v)
		}
		budget = n
	}
	perShape := budget / len(pyMutableShapes)
	if perShape < 1 {
		perShape = 1
	}

	eligibleCounts := map[pyShape]int{}
	var chosen []pyMutationSite
	for _, sh := range pyMutableShapes {
		pool := pools[sh]
		eligibleCounts[sh] = len(pool)
		sort.Slice(pool, func(i, j int) bool {
			if pool[i].rel != pool[j].rel {
				return pool[i].rel < pool[j].rel
			}
			if pool[i].line != pool[j].line {
				return pool[i].line < pool[j].line
			}
			return pool[i].col < pool[j].col
		})
		if len(pool) <= perShape {
			chosen = append(chosen, pool...)
			continue
		}
		// Float stride spanning the whole pool, mirroring pyCorpusFiles' own
		// cap (pyoracle_test.go) — an integer stride truncates the sample to
		// a contiguous prefix of the (rel-sorted) pool whenever it floors,
		// leaving the alphabetical tail (e.g. bare-call's symtable...zipimport
		// range) permanently unreachable, and gets WORSE at a higher budget
		// once stride reaches 1 (Fix 6b of the #313 review).
		stride := float64(len(pool)) / float64(perShape)
		for i := 0; i < perShape; i++ {
			idx := int(float64(i) * stride)
			if idx >= len(pool) {
				idx = len(pool) - 1
			}
			chosen = append(chosen, pool[idx])
		}
	}
	if len(chosen) == 0 {
		if isDefault {
			t.Fatalf("vacuity: zero eligible mutation sites in the default corpus (bare-call alone should number " +
				"in the thousands per the #313 measured census) — the enumerator or the freshness filter is broken")
		}
		t.Skip("no eligible mutation sites in the env-pointed corpus")
	}

	results := map[pyShape]*pyShapeResult{}
	for _, sh := range pyMutableShapes {
		results[sh] = newPyShapeResult()
	}
	discarded := map[pyShape]int{}

	for _, s := range chosen {
		abs := filepath.Join(staged, s.rel)
		original, err := os.ReadFile(abs)
		if err != nil {
			discarded[s.shape]++
			continue
		}
		lines := strings.Split(string(original), "\n")
		if s.line-1 < 0 || s.line-1 >= len(lines) {
			discarded[s.shape]++
			continue
		}
		line := lines[s.line-1]
		if s.col < 0 || s.col+len(s.name) > len(line) || line[s.col:s.col+len(s.name)] != s.name {
			discarded[s.shape]++
			continue
		}
		lines[s.line-1] = line[:s.col] + s.fresh + line[s.col+len(s.name):]
		mutatedText := strings.Join(lines, "\n")
		if err := os.WriteFile(abs, []byte(mutatedText), 0o644); err != nil {
			t.Fatalf("phase2: write mutation for %s:%d: %v", s.rel, s.line, err)
		}

		vmap := ruffF821(t, ruff, abs)
		var postVerdict oracleVerdict
		for _, vv := range vmap {
			postVerdict = vv
		}

		proven := false
		for _, f := range postVerdict.F821 {
			if f.Line == s.line && f.Name == s.fresh {
				proven = true
				break
			}
		}
		// Structural assertion (plan section 5): the PRE-mutation baseline
		// must never already contain this exact (line, fresh) pair — the
		// freshness filter above makes it "structurally impossible", so
		// assert it rather than silently trust that.
		for _, f := range baselineByRel[s.rel].F821 {
			if f.Line == s.line && f.Name == s.fresh {
				t.Errorf("phase2 invariant violated: fresh name %q at %s:%d already present in the PRE-mutation "+
					"baseline verdict — the freshness filter is broken", s.fresh, s.rel, s.line)
			}
		}

		if !proven {
			discarded[s.shape]++
			r := results[s.shape]
			if len(r.discardedExs) < 8 {
				r.discardedExs = append(r.discardedExs, fmt.Sprintf("%s:%d  %s -> %s", s.rel, s.line, s.name, s.fresh))
			}
			if err := os.WriteFile(abs, original, 0o644); err != nil {
				t.Fatalf("phase2: restore %s after discard: %v", s.rel, err)
			}
			continue
		}

		outcomes := pyRunAllPostures(t, staged, hasGit, s.rel, mutatedText, s.line, s.fresh, known, repoKnown)
		if err := os.WriteFile(abs, original, 0o644); err != nil {
			t.Fatalf("phase2: restore %s after adjudication: %v", s.rel, err)
		}

		res := results[s.shape]
		res.proven++
		anyCaught := false
		for _, pn := range pyPostureOrder {
			oc, measured := outcomes[pn]
			if !measured {
				continue
			}
			res.postureProven[pn]++
			if oc.caught {
				res.postureCaught[pn]++
				anyCaught = true
				if res.catchBy[pn] == nil {
					res.catchBy[pn] = map[string]int{}
				}
				res.catchBy[pn][oc.check]++
			}
		}
		if !anyCaught {
			res.missCount++
			if len(res.totalMisses) < 8 {
				res.totalMisses = append(res.totalMisses, fmt.Sprintf("%s:%d  %s -> %s", s.rel, s.line, s.name, s.fresh))
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "corpus=%s default=%t module=n/a(python)\n", root, isDefault)
	fmt.Fprintf(&b, "eligible sites by shape:")
	for _, sh := range pyMutableShapes {
		fmt.Fprintf(&b, " %s=%d", sh, eligibleCounts[sh])
	}
	fmt.Fprintf(&b, " attr-member=%d(not mutated: ruff cannot adjudicate)\n", attrMemberRaw)
	fmt.Fprintf(&b, "mutations attempted=%d (budget %d, %d per shape)\n", len(chosen), budget, perShape)
	fmt.Fprintf(&b, "quota vs eligible (a thin row reads as thin, not a confident zero):")
	for _, sh := range pyMutableShapes {
		fmt.Fprintf(&b, " %s=%d/%d", sh, min(perShape, eligibleCounts[sh]), eligibleCounts[sh])
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%-13s %-6s %8s", "SHAPE", "OWNER", "PROVEN")
	for _, pn := range pyPostureOrder {
		fmt.Fprintf(&b, " %12s", pn)
	}
	fmt.Fprintf(&b, " %10s\n", "DISCARDED")

	totalProven, totalMisses := 0, 0
	var catchByLines []string
	for _, sh := range pyMutableShapes {
		r := results[sh]
		totalProven += r.proven
		totalMisses += r.missCount
		fmt.Fprintf(&b, "%-13s %-6s %8d", sh, pyShapeOwner(sh), r.proven)
		for _, pn := range pyPostureOrder {
			if pn == "precommit" && !hasGit {
				fmt.Fprintf(&b, " %12s", "not measured")
				continue
			}
			proven, measured := r.postureProven[pn]
			if !measured && r.proven == 0 {
				fmt.Fprintf(&b, " %12s", "0/0")
				continue
			}
			fmt.Fprintf(&b, " %12s", fmt.Sprintf("%d/%d", r.postureCaught[pn], proven))
		}
		fmt.Fprintf(&b, " %10d\n", discarded[sh])

		for _, pn := range pyPostureOrder {
			checks := r.catchBy[pn]
			if len(checks) == 0 {
				continue
			}
			var parts []string
			for _, chk := range []string{"Run", "FileScope"} {
				if n := checks[chk]; n > 0 {
					parts = append(parts, fmt.Sprintf("%s=%d", chk, n))
				}
			}
			if len(parts) > 0 {
				catchByLines = append(catchByLines, fmt.Sprintf("%s/%s %s", sh, pn, strings.Join(parts, " ")))
			}
		}
	}
	fmt.Fprintf(&b, "\nTOTAL proven-unresolved=%d, unseen by every measured posture/check=%d (%.0f%%)\n",
		totalProven, totalMisses, pct(totalMisses, totalProven))
	if len(catchByLines) > 0 {
		fmt.Fprintf(&b, "caught-by per shape/posture: %s\n", strings.Join(catchByLines, " ; "))
	}
	fmt.Fprintf(&b, "discarded = ruff did not report the fresh name at that exact line after mutation (string "+
		"content, a dead branch the ast still walks, a site whose text moved between enumeration and mutation) "+
		"— dropped, never scored\n")
	for _, sh := range pyMutableShapes {
		for _, e := range results[sh].discardedExs {
			fmt.Fprintf(&b, "  [%s discarded] %s\n", sh, e)
		}
	}
	for _, sh := range pyMutableShapes {
		r := results[sh]
		for _, e := range r.totalMisses {
			fmt.Fprintf(&b, "  [%s unseen-by-every-posture] %s\n", sh, e)
		}
		if r.missCount > len(r.totalMisses) {
			fmt.Fprintf(&b, "  [%s unseen-by-every-posture] ... showing %d of %d\n", sh, len(r.totalMisses), r.missCount)
		}
	}
	t.Log("\n" + b.String())

	// Only bare-call is scored (plan section 10, fail condition 3): it is the
	// population guard.Run declares it checks, and unlike Go, a Python hunk
	// has no compiler behind it — a miss here is a hallucination that ships.
	if r := results[pyShapeBareCall]; r.proven == 0 {
		t.Log("NOTE: no bare-call mutation survived adjudication — the in-population claim rests on nothing " +
			"in this run; raise RUNECHO_ORACLE_MUTATIONS")
		if isDefault {
			t.Fatalf("vacuity: zero PROVEN bare-call mutations on the default corpus (stdlib has ~5.8K bare-call " +
				"sites per the #313 measured census) — the oracle, enumerator, or freshness filter is broken")
		}
	} else if r.missCount > 0 {
		t.Errorf("PROVEN FALSE NEGATIVES: %d/%d bare-call mutations are reported `undefined name` by ruff and "+
			"flagged by nothing in the guard, in any posture (showing %d example(s)):\n  %s",
			r.missCount, r.proven, len(r.totalMisses), strings.Join(r.totalMisses, "\n  "))
	}
}
