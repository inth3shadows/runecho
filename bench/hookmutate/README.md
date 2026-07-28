# hookmutate — does each hook fixture actually catch anything?

`cmd/runecho-guard/testdata/hookcorpus/*.json` is the only instrument that
reaches five of the guard's six checks (#227). It is a **wiring** instrument, not
a catch-rate — never quote it as one.

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

Mutations are applied in place and restored immediately, including on error. It
refuses to start on a dirty tree, so a crash can never be mistaken for your edits.

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

## Adding a mutation

`find` must match exactly once, and `replace` must leave valid Go — deleting a
block often orphans a variable, so prefer neutering a condition (`&& false`,
`|| true`, `_ = x`) over removing lines. Both failure modes are reported rather
than skipped, so a catalog that drifts from the code says so.

## Scope

Hook-level checks only. The additive check has its own corpus
(`internal/guard/testdata/corpus/`) driven through `guard.Run`, and the
`internal/guard` unit suites cover the pure functions — including the two
restrictions this corpus provably cannot see (see the `known_gap` entries).
