# Captured-LLM Benchmark — First Findings (2026-06-30)

> **Update (#56 landed):** the "achievable in-design" gap below is now closed.
> Extending the guard to extract bare **const references** and **type
> annotations** moved the captured catch-rate from **1/9 → 4/9** with false
> positives unchanged at **0/6**, and the synthetic tripwire held at 100%/0%.
> The remaining 5 misses are all qualified positions (out of scope by design).
> The body below is the original first-measurement writeup; numbers in the
> tables are pre-#56 and annotated where they moved.


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
removed its import. Cross-checking references against the file's own import set
is a cheap, structural, in-design signal worth exploring.

## Honest caveats (read before quoting the 1/9)

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
