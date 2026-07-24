---
name: Propose a new guard check
about: Ask RunEcho to catch a new class of defect (a new kind of "does this exist")
title: "check: <defect class in a few words>"
labels: question
---

<!--
Before filling this in, read docs/check-worthiness.md. It is the gate this
request passes through, and it is ordered: a request that fails an early gate is
declined without spending effort on the later ones. Answering these honestly here
saves the round-trip. "I don't know yet" is a fine answer for Gate 4 — that's
what scripts/triage-check/ is for.
-->

## What defect class?

<!-- The concrete mistake an agent makes. Show a real or realistic example. -->

## Gate 1 — Is it decidable?

Can you state the ground truth as **"the referenced NAME is declared nowhere,"**
with no appeal to intent, taste, or whether a value is *correct*?

- [ ] Yes — it's existence, like `procesData()` being undefined.
- [ ] No — it's a value/judgement, like `padding: 13px`. (If so, this is a search
      feature competing with ripgrep, not a guard check — say why RunEcho anyway.)

## Gate 2 — The four conjuncts

RunEcho's position is the *conjunction*. Check each one this candidate plausibly
satisfies **in RunEcho specifically**:

- [ ] **Pre-write** — it fires before the edit lands, inside the agent loop.
- [ ] **Blocking** — it can deny, not merely advise.
- [ ] **Existence-aware** — it decides on whether the symbol exists.
- [ ] **Deterministic** — parsing, not a second model.

<!-- All four, or it is a linter's job. If the only value over an existing
ecosystem tool is conjuncts 1-2 (timing), say so plainly: the pitch is then
"block pre-write what a linter already catches post-write", which Gate 4 has to
justify on its own. -->

## Gate 3 — Prior art

Does a mature linter already detect this? Name it, or say you searched and found
none.

<!-- If one exists, RunEcho adds at most pre-write *timing*, not detection.
     Say why that timing is worth reimplementing the detector. -->

## Gate 4 — Evidence

Have you run the incumbent (or a probe) over real repos and counted genuine
defects vs false positives?

- [ ] Yes — numbers below.
- [ ] Not yet — I understand this gates the decision, and an empty `bench/` corpus
      result does **not** count if the defect fails silently (see the corpus trap
      in the rubric).

<!-- Bar: >=3 hand-confirmed genuine defects across >=2 repos AND a low enough
     FP rate that an ask-posture gate wouldn't block correct writes. -->

## Gate 5 — Demand

Is this a pain for more than one person? Who else?
