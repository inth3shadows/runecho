# Captured-LLM Benchmark — First Findings (2026-06-30)

> **Update (#56 landed):** the "achievable in-design" gap below is now closed.
> Extending the guard to extract bare **const references** and **type
> annotations** moved the captured catch-rate from **1/9 → 4/9** with false
> positives unchanged at **0/6**, and the synthetic tripwire held at 100%/0%.
> The remaining 5 misses are all qualified positions (out of scope by design).
> The body below is the original first-measurement writeup; numbers in the
> tables are pre-#56 and annotated where they moved.

> **Update (2026-07-22): why the corpus is N=15, and why that is the honest
> number.** A full mine of 1.2 GB of real agent transcripts for *new* cases came
> back empty across all three of the guard's target positions. The reason is a
> finding in its own right, and it reframes the whole claim: real agent
> "undefined symbol" errors are mostly import/scope mistakes on symbols that
> *exist*, not invented symbols. See **"The corpus is N=15 because that is the
> organic yield"** near the end.


The first measurement of RunEcho's headline claim against **real** model
hallucinations, not synthetic ones. N=15 hand-verified cases (9 hallucinated,
6 real) mined from session transcripts, each backed by an in-session compiler or
runtime error as independent ground truth. 100% observed (no elicited cases were
needed to clear the floor).

## The two-number story

| benchmark | what it measures | result |
|---|---|---|
| **synthetic** (`go test ./bench -run Scorecard`) | guard exactness on in-scope, call-position refs | **100% recall / 0% FP** |
| **captured** (`go test ./bench -run RealCorpus`) | guard catch-rate on real, transcript-observed hallucinations | **caught 1 of 9 ; 0 false positives of 6** |

Both are true. The synthetic 100% says the classifier is exact *within its
scope*. The captured 1/9 says *that scope is a narrow slice of where real
hallucinations occur.* Neither number alone is honest; together they are.

## The coverage map (the actual finding)

The guard only extracts references in **call position** (`X(`), and skips
anything preceded by a dot. Real hallucinations land mostly elsewhere:

| reference position | caught | example | in RunEcho's design? |
|---|---|---|---|
| **bare-call** | **1/1** | `str(ULID())` — invented constructor | ✅ covered |
| const-ref | 0/1 → **1/1** | `TASTING_ROOM_KIND[t]` — dropped import | ✅ closed by #56 |
| type-ref | 0/2 → **2/2** | `ctx: RouteContext<…>` — ambient type absent | ✅ closed by #56 |
| qualified-attr | 0/3 | `series.pearson_corr(…)`, `df.groupby(…)` | ❌ needs type resolution |
| qualified-method | 0/1 | `tree.Root()` — wrong method name | ❌ needs type resolution |
| qualified-prop | 0/1 | `import.meta.env` — missing type aug | ❌ needs type resolution |

The lone catch was the one bare-call constructor. Every miss was a qualified,
type, or constant reference.

## Two classes of miss — and they are NOT the same

1. **Achievable within RunEcho's deterministic-structural design** (≈3 cases):
   bare identifiers in **non-call positions** — a type annotation (`RouteContext`),
   a constant subscript (`TASTING_ROOM_KIND`). These are unqualified names the
   guard could validate against the IR known-set *without any type inference* —
   it simply doesn't extract them today because `ExtractRefs` only looks before
   `(`. This is the real, actionable gap. → **issue filed.**

2. **Out of scope by design** (≈4 cases): qualified references
   (`series.pearson_corr`, `df.groupby`, `tree.Root`). Catching these requires
   knowing the *type* of the receiver — semantic analysis RunEcho deliberately
   avoids (it is structural and model-free). This is a **documented boundary,
   not a bug.** RunEcho should say so in its README rather than imply it catches
   method hallucinations. → **README honesty note.**

A frequent sub-pattern across both classes: the hallucination was a **dropped
import** (`ULID`, `TASTING_ROOM_KIND`) — the agent referenced a real symbol but
removed its import. **Implemented** as `guard.DroppedImportRefs`: when an edit
removes an import binding whose name the new text still uses unqualified (and does
not re-define), the hook warns "dropped import, still used." It is the file-scoped
mirror of the E1 dangling-refs check and complements the additive check, which at
edit time still sees the old import on disk and stays silent. Gated OFF by default
(`RUNECHO_GUARD_DROPPED_IMPORT=1`) for dogfood-first rollout, same as E1. Note: it
is a hook-level check (operates on edit old-vs-new text), so it is not exercised by
this benchmark's `guard.Run` path — its validation is the unit suite.

## Honest caveats (read before quoting the captured catch-rate — 4/9 post-#56; the 1/9 below is the pre-#56 first measurement)

- **Selection bias toward qualified positions.** The corpus was mined by
  searching for *compiler/runtime errors*, and the loudest error signatures
  (`AttributeError`, `undefined (type …)`, `TS2304`) are inherently qualified or
  type errors. Bare-call function hallucinations (which the guard catches) are
  under-sampled here. So 1/9 is **not** "RunEcho catches 11% of all
  hallucinations" — it is "in this error-sourced sample, the misses cluster in
  positions the guard doesn't cover." The *direction* is robust; the ratio is not
  a population estimate. (N=15 → counts, not rates — see the scorecard.)
- **0 false positives, partly by abstention.** The guard flagged none of the 6
  real references — but for qualified/import positions that is because it
  *abstains* on those positions, not because it validated them. No-false-positive
  is real and good; it is not evidence of validation on those positions.
- All cases are guard-absent (the errors reached the compiler/runtime, so nothing
  gated them) — exactly the uncaught-hallucination population we wanted.

## What this replaces

These are real, evidenced backlog items grounded in observed model behavior —
the opposite of audit-manufactured issues. The benchmark now makes "does
widening ref extraction actually catch more real hallucinations?" a measurable
question: add the case, extend the extractor, watch the captured catch-rate move.

---

## The corpus is N=15 because that is the organic yield (2026-07-22)

The obvious next step for this benchmark is "mine more cases." We did — the whole
1.2 GB of local agent transcripts, automated, across all three positions the
guard's checks target. It came back essentially empty, and *why* it came back
empty is the more useful result than any case it might have added.

### Three positions mined, three dry wells

| target position | guard check | new organic cases |
|---|---|---|
| in-scope invented-symbol (bare-call / const-ref / type-ref) | `guard.Run` | **~0** |
| package-qualified (same-repo internal, external dep) | qualified checks (default-off) | **0** |
| dropped-import | `DroppedImportRefs` (default-off) | **0** |

Candidates were hand-verified — the stronger ones (`render`, `has_terse_marker`,
`evaluator`, `logging`, `build_report`, `BASE_RATING`) against the source repo on
disk, the rest against their transcript context — not taken from the error string
alone. What each well actually contained:

- **In-scope invented-symbol.** Every fresh `NameError` / `undefined:` / `TS2304`
  hit resolved, on inspection, to a symbol that *exists*: `render` was a real
  function called without its local import; `has_terse_marker` was a real
  `transforms.has_terse_marker` called without its qualifier; `os` was a missing
  `import os`; `cfg` and `rows` were undefined *local variables* (outside the
  guard's symbol model entirely). The genuinely-invented cases the benchmark
  wants — `ULID`, `TASTING_ROOM_KIND`, `RouteContext` — were already in the 15.

- **Package-qualified.** The entire non-dependency universe was documentation
  placeholders (`pkg.Sym`), a build error *inside* a dependency (a
  `zapslog.HandlerOptions` failure compiling caddy — not agent-written), and
  deliberately-constructed `*_repro_test.go` files (which fail the organic rule).
  Real Go hallucinations land in **value-method** position (`tree.Root`,
  `db.RawExec`) — the type-resolution class this tool abstains on by design.

- **Dropped-import.** The archetype is real — `ULID` and `TASTING_ROOM_KIND`
  above were caused by dropped imports — but those are already in the corpus, and
  no *new* ones surfaced. 60 edits *looked* like an import removed while its name
  is still used. Exactly one of those names (`evaluator`) also produced a runtime
  `NameError`, and on inspection it was a **real module imported lazily inside a
  function**, used on a path where that import had not run — the same
  real-symbol-scope class as `render`/`has_terse_marker`, not a dropped-import
  bug. Every other spot-check (`logging`, `build_report`, `BASE_RATING`) was a
  **refactor that relocated the symbol to another import line** — it still
  resolves and would not fail. These are exactly the false positives
  `DroppedImportRefs`' own `preBound` guard is built to suppress, not bugs.

### The structural reason

Agents run tests and builds and fix errors immediately, so transcripts are dense
with error *moments*. But the errors themselves are overwhelmingly **missing
imports, missing qualifiers, and local-scope slips on symbols that already
exist** — plus **refactor relocations** that read as drops but aren't. Inventing
a call to a symbol that exists *nowhere* in the reachable code — the thing a
deterministic existence-checker catches — is comparatively rare in real work.

So N=15 is not a mining shortfall to be embarrassed about; it is approximately
what 1.2 GB of genuine agent behavior yields in cleanly, independently labelable
invented-symbol cases. Reporting a bigger number would mean either lowering the
labeling bar or fabricating cases — and a benchmark whose corpus shares the
tool's assumptions is worse than a small honest one.

### What this means for the claim

- **The two-number story stands, and is now better grounded.** Synthetic 100%/0%
  says the classifier is exact within scope; captured 4/9 says that scope is a
  narrow slice of where real hallucinations occur — and the mining explains *why*
  it is narrow: the surrounding population is import/scope bugs, not inventions.
- **The default-off checks (qualified, dropped-import) stay off — now on
  evidence, not just caution.** They target positions that this corpus of real
  behavior barely exercises, so promoting them buys little real-world catch while
  risking the zero-false-positive property that is the entire pitch.
- **Growing the corpus further requires elicited cases**, clearly labeled as such
  (a separate stratum under the ≥50%-observed floor), used only to prove a check
  *fires* on realistic input — never quoted as field catch-rate. The organic
  transcript source, for this box, is exhausted.

The mining is reproducible (the scripts live outside the repo); re-running it on a
larger or different transcript corpus is the only thing that would move these
counts, and it should be re-run before anyone concludes the wells are dry
*in general* rather than *on this machine*.
