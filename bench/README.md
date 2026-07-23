# Hallucination-Reduction Benchmark (synthetic)

Puts a number behind RunEcho's headline claim — *"catches invented symbols
before they land"* — by scoring the guard as a binary classifier.

## Run it

```sh
go test ./bench/ -run TestScorecard -v   # prints the scorecard
go test ./bench/                         # tripwire only (fails on regression)
```

## What it measures

The guard is a classifier over symbol references:

| reference is… | guard should… | a mistake here is… |
|---|---|---|
| **hallucinated** (symbol absent) | flag it | a false **negative** — a hallucination slips through |
| **real** (symbol exists) | pass it | a false **positive** — the adoption-killer (a guard that cries wolf gets disabled) |

Scorecard reports **catch-rate (recall)**, **false-positive rate**, precision,
and F1 — overall, **per language**, and **per hallucination type** (typo,
plausible-affix, wrong-case, invented, renamed-away).

The per-language / per-type cut is the point: it tells you *which* parser work
actually moves the number, instead of polishing fidelity on faith.

## How the corpus is built

Declared symbol pools per language, perturbed by a **seeded RNG** (same seed →
same cases). Go pool symbols are Exported, because RunEcho's IR indexes only
exported Go symbols and the guard skips unexported refs by design — an
unexported corpus would measure a non-feature.

The known-symbol set is supplied directly, **not** derived from the IR, so this
isolates the guard's ref-extraction + validation classifier. IR extraction
accuracy is a separate claim and is held constant here.

## What a green run does and does NOT mean

- **Does:** on in-scope, call-position references, the guard is exact
  (recall 100%, false-positive 0%) — and stays that way. Because corpus and
  guard are both deterministic, the test gates on that baseline as a
  **regression tripwire**: a parser/guard change that drops recall or raises
  false-positives fails the build with the exact delta.
- **Does NOT:** prove RunEcho catches *real-world* hallucinations. Synthetic
  perturbation saturates at 100% — it can't probe the hard cases (qualified
  `obj.Method` calls, unexported Go, non-call positions). The true quality
  number against an **observed LLM error distribution** comes from the
  captured-LLM corpus (Phase 2), not this scaffold.

## Related measurement

[TOKEN-COST.md](TOKEN-COST.md) measures a different axis: not whether the guard
CATCHES a hallucination, but what each surface COSTS in context tokens. Same
posture — it corrected a README overclaim and reports RunEcho's most expensive
call rather than only its cheapest.

## Caveats (stated, not buried)

- Synthetic perturbations approximate, but are not, a real model error
  distribution.
- Only call-position references are in scope (the guard's own scope).
- Known-set is declared; end-to-end IR-sourced scoring is a later mode.
- **The captured corpus can only contain defects the toolchain reports.** Every
  label in `captured/corpus.json` is grounded in a compiler or runtime error —
  Go diagnostics, Python `NameError`/`AttributeError`, `tsc TS2304`/`TS2339`.
  That is what makes the labels trustworthy, and it is also a blind spot: a
  defect class that **fails silently** can never appear here, no matter how
  often it occurs. A hallucinated CSS custom property, Tailwind class, i18n
  key, feature-flag name or env-var name throws nothing — the page just renders
  wrong.

  So "we searched the corpus and found none" is **not** evidence of absence for
  those classes; the query was guaranteed to come back empty before it was run.
  Answering "does RunEcho need to check X?" for a silent-failure X needs a
  different instrument (run an existing linter over real repos and hand-classify
  its findings), not this corpus. Recorded because #204 set exactly that query as
  its decision gate and the null would have read as a finding.
