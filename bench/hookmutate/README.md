# hookmutate — does each hook fixture actually catch anything?

`cmd/runecho-guard/testdata/hookcorpus/*.json` is the only instrument that
reaches eight of the guard's nine checks (#227). It is a **wiring** instrument,
not a catch-rate — never quote it as one.

Its own failure mode is silent: a fixture can pass forever while pinning nothing,
and the corpus then reports coverage it does not have. That is the exact defect
#227 was filed about, so the corpus needs an instrument of its own.

```
python3 bench/hookmutate/mutate.py            # report
python3 bench/hookmutate/mutate.py --strict   # exit 1 on a dead fixture or an unclaimed survivor
python3 bench/hookmutate/mutate.py --only dropped-import
```

It breaks one behaviour of one check at a time (catalog: `mutations.json`),
replays the corpus, and reports which fixtures noticed. Two outputs matter:

- **a fixture that caught nothing** — delete it, or replace it with a case that
  pins a behaviour the set is missing
- **a mutation nothing caught** — either a real hole, or an equivalent mutant;
  mark the latter `known_gap` in the catalog with the reason, so it is recorded
  rather than implied away

A full run is **71 test runs (69 mutations, a baseline, and a canary), ~9
minutes measured**. Budget for that rather than reaching for a short `timeout`
— see the signal note below.

## The rule

**A mutation must be no coarser than the claim it supports.**

Deleting a whole lookup that several inputs feed will happily "confirm" a fixture
that pins only one of them. This was got wrong twice while building the corpus,
and both times it certified a vacuous fixture as verified — most recently a
dropped-import fixture that claimed to pin `preBound` while replaying as a
`Write`, for which `preBound` is never computed at all. The coarse mutation
deleted the shared `bound` set; the fixture was actually pinning `ExtractDefs`.

If a fixture claims to pin X, mutate X alone. `dropped-import/prebound-fold` and
`dropped-import/newtext-defs-fold` are in the catalog as separate entries for
exactly this reason.

## Where mutations are applied

**In a disposable sandbox — `git archive HEAD` into a temp dir — never in your
working tree.** That is a correctness requirement, not tidiness (#367).

Applying a mutant in place and restoring it in a `finally` looks safe, and the
end-of-run `git status` check even passes, because the tree is clean at every
quiescent moment. It cannot see the failure that actually happens: **anything
else reading the repo mid-run sees the mutant and diagnoses it as a code
defect.** Two agents in one worktree, one mutating and one reviewing, has now
cost hours in two separate repos — and it presents as a bug in the code under
test, which is what makes it expensive.

Three consequences of the sandbox:

- The tree must still be **clean to start**. The sandbox is `HEAD`, so
  uncommitted edits would be silently unscored — a wrong answer in the other
  direction. Commit first.
- A run killed by a signal strands a mutant in a temp dir, not in your repo.
  Handlers for `SIGTERM`/`SIGINT`/`SIGHUP`/`SIGQUIT` remove the sandbox and re-raise with
  the default disposition, so the exit status still reports the signal. **They
  only help when the signal reaches this process**: `timeout … uv run python
  mutate.py` absorbs it in the wrapper and leaks the temp dir.
- Before scoring, a **liveness canary** appends a deliberate syntax error to the
  first catalog file and requires the build to break. If a guaranteed break does
  *not* register, the run is scoring a tree it is not mutating, and it aborts
  rather than reporting survivors — leaving the sandbox in place, since every
  cause is diagnosed by looking at it. See "Lifting this" for what causes that,
  and for the precise (narrower than it looks) claim one canary supports.

## Adding a mutation

`find` must match exactly once, and `replace` must leave valid Go — deleting a
block often orphans a variable, so prefer neutering a condition (`&& false`,
`|| true`, `_ = x`) over removing lines. Both failure modes are reported rather
than skipped, so a catalog that drifts from the code says so.

`find == replace` and duplicate `id`s are rejected at load. A no-op entry applies
cleanly, changes nothing, and is reported `SURVIVED` — indistinguishable from a
real hole, and invisible to the occurs-once drift check.

**Where a line admits several mutants, carry each as its own entry.** They do not
score alike: on one line of kb-mcp, `state == "Z"` → `return False` survived
while `state != "Z"` was killed. Reporting one representative understates what
the tests actually pin.

## Lifting this into another repo

The Go-specific parts are `run_corpus()` and the "keep it valid Go" advice in the
`broken` path. Everything above the rule line generalises. Two traps that Go
happens to be immune to, and which the canary is designed to catch:

- **Stale bytecode (Python).** CPython invalidates `.pyc` on `(mtime, size)`, so
  restoring a mutant with a same-size edit (`0.05` → `0.06`, `== "Z"` → `!= "Z"`)
  can reuse stale bytecode and silently score the *previous* revision. Only ever
  a false `SURVIVED`; a `KILLED` verdict is never affected. Purge between
  transitions: `find . -name __pycache__ -type d -exec rm -rf {} +` then `touch`
  the mutated file. The canary catches this too — a syntax error that leaves the
  build green means the source was never re-parsed.
- **The tests import a different tree.** A `cp -a` copy carries its venv, and an
  editable install or a stale `.pth` resolves the package back to the original.
  Observed: deleting a load-bearing `os.killpg` scored `SURVIVED` because the
  tests were passing against the unmutated original. `git archive` avoids this by
  construction (no venv is copied); `uv run python -c "import pkg;
  print(pkg.__file__)"` asserts it directly.

### What one canary proves, and what it does not

The canary breaks **one file**, so the claim it supports is exactly this: *the
compilation unit holding the first catalog entry is reached by the tests*. It
says nothing about the rest of the catalog. A repo whose catalog spans packages
that resolve **independently** — a second checkout on the path, a vendored copy,
an editable install covering only part of the tree — can pass the canary while a
second package still resolves to the original tree, and every mutation in that
package then reports a false `SURVIVED`.

Here that gap is closed by a check rather than by care: after the canary,
`assert_build_graph()` runs `go list -test -deps -f '{{.Dir}}' ./cmd/runecho-guard/`
inside the sandbox and requires every catalog file's directory to appear in the
result, aborting with the name of any package that does not. `-test` matters:
that is the graph the canary actually proved, and it is a strict superset of the
plain build's — without it, a package reached only from a `_test.go` file is
wrongly reported as one the tests never build. Both measured: 0.3s warm, against
~0.9s for a second `go test` canary, which is a build break and so never links or
runs. The saving is small; naming the offending package is the point.

The check is per **directory**, so a file excluded from its own package by a
`//go:build` constraint still slips through — no catalog entry carries one today.
**A consumer with no equivalent
build-graph query needs one canary per independently-resolved package in its
catalog** — per package, not per run.

## Scope

Hook-level checks only. The additive check has its own corpus
(`internal/guard/testdata/corpus/`) driven through `guard.Run`, and the
`internal/guard` unit suites cover the pure functions — including the two
restrictions this corpus provably cannot see (see the `known_gap` entries).
