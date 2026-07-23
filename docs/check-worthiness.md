# Does this check earn its place?

A rubric for deciding whether RunEcho should add a check for a new defect class.

Every so often someone asks "can RunEcho also catch X?" — invented dependencies
(#175), design tokens (#204), and there will be more (i18n keys, feature-flag
names, env-var names, ORM field names). Left to intuition, each one gets
re-argued from scratch, and the argument usually sounds good, because the ones
that reach an issue are the plausible ones. #175 shipped on a plausible argument,
moved the corpus by zero, and was reverted.

This is the gate those requests pass through **before** any code. It exists so
the decision is made against evidence and a fixed bar, not against how convincing
the pitch was. The order matters: a request that fails an early gate is declined
without spending effort on the later, more expensive ones.

---

## Gate 1 — Decidability (fail = decline as a *search* feature, not a guard check)

Is "X is wrong" **decidable** from the code, or is it a judgement?

- `procesData()` is undefined → **provably** wrong. Decidable.
- `padding: 13px` → not wrong, just a number. **Not** decidable.

The guard's entire value is that "RunEcho flagged it" means "this is provably
absent." A check whose verdict can't be right or wrong destroys that meaning for
every other check. If X is not decidable, it can only ever be a search feature —
and then it competes with `ripgrep`, which is faster and already installed. Decline.

> Test: can you state the ground truth as "the referenced NAME is declared
> nowhere," with no appeal to intent, taste, or a value's correctness?

## Gate 2 — The four conjuncts (fail = it's a linter's job, not RunEcho's)

RunEcho's whole position is the *conjunction* (`docs/competitive-landscape.md`):

1. **Pre-write** — before the edit lands, inside the agent loop.
2. **Blocking** — can deny, not merely advise.
3. **Existence-aware** — decides on whether the symbol exists.
4. **Deterministic** — parsing, not a second model.

A candidate check must plausibly satisfy **all four in RunEcho specifically**. If
the value it adds over the existing ecosystem tool is only conjuncts 1–2
(*timing*), be honest that the whole pitch is "block pre-write what a linter
already detects post-write" — and weigh that against Gate 4's cost.

## Gate 3 — Prior art (fail = recommend the incumbent, don't rebuild it)

Does a mature tool already detect X? Search before building.

If yes, RunEcho is not adding *detection* — at most it adds *timing* (Gate 2).
Reimplementing a mature detector's logic in Go, with its years of accumulated
edge cases, to gain only pre-write timing, is the "breadth" axis the competitive
map says this project loses ground on. The default answer when prior art exists
is **point the user at it and close**, unless Gate 4 clears convincingly.

Known incumbents by segment (extend as found):

| Segment | Incumbent |
|---|---|
| CSS custom properties (`var(--x)` undeclared) | stylelint `no-unknown-custom-properties` (+ `referenceFiles`) |
| Tailwind utility classes | `eslint-plugin-tailwindcss` → `no-custom-classname` |
| Design tokens in JS/TS theme objects | `@lapidist/design-lint` |
| i18n keys (`t('a.b')` with no catalog entry) | `i18next-cli --ci` / i18nGuard / i18n-cleaner — **all require per-project config**, so none is a config-free sweep |
| Package existence (slopsquatting) | out of scope — package registry, not in-repo (see competitive map) |

**Not every class has a config-free incumbent.** The i18n row above is the
example: the detectors all need to be told where the locales live and which
framework is in use. When that is true, `scripts/triage-check/` may front a
minimal self-extraction detector for the *measurement* (that is what its
`i18n-keys` detector does), but the check RunEcho would *ship* should still front
a configured incumbent — self-extraction has a higher FP floor than a tool that
knows the project's i18n layout.

## Gate 4 — Evidence, measured (the bar that #175 and #204 failed)

Do not build on argument. **Run the incumbent** (or a probe) over real enrolled
repos and hand-classify every finding. The harness for this is
[`scripts/triage-check/`](../scripts/triage-check/) — prototype; see its README.

**Fixed decision rule, set before looking at results:**

> **≥3 hand-confirmed genuine defects across ≥2 repos, AND a false-positive rate
> low enough that an ask-posture gate would not block correct writes.**
>
> Anything less → **decline, record the numbers, ship nothing.**

A run that finds nothing is a **success**: it costs hours and buys the answer
#175 spent a whole feature learning.

### The corpus trap — why an empty `bench/` result is not evidence

`bench/captured/corpus.json` is mined from transcripts where **the toolchain
errored** (compiler diagnostics, `NameError`, `tsc TS2304`). That is what makes
its labels trustworthy — and it is a blind spot: a defect class that **fails
silently** can never appear there, no matter how often it occurs. A hallucinated
CSS property, Tailwind class, i18n key or feature flag throws nothing.

So for any silently-failing X, "searched the corpus, found none" is **guaranteed
before you run it** and means nothing. Gate 4 uses a live incumbent over real
repos precisely because the corpus cannot answer for this class. (Recorded in
`bench/README.md`.)

## Gate 5 — Demand (soft; a tie-breaker, not a veto)

Is this a real pain for **more than one person**? One thread is one user. Demand
does not override a clean Gate-4 pass (a real, low-FP defect class is worth
catching even at N=1), but at the margin — Gate 4 borderline — thin demand is a
reason to decline rather than reach.

---

## Worked precedents

| Request | G1 decidable | G2 four conjuncts | G3 prior art | G4 evidence | Outcome |
|---|---|---|---|---|---|
| **#204 tokens (value lookup)** | ✗ `13px` has no truth value | — | — | — | Declined at G1 |
| **#204 tokens (existence)** | ✓ | timing only | stylelint/eslint/design-lint cover it | probe: 0 genuine / 14 findings, 100% FP | Declined at G4 |
| **#175 dep index** | ✓ | ✓ | — | 0 corpus movement | Reverted (evidence gathered too late) |

The pattern in the failures is the same both times: the *argument* was sound and
the *evidence*, once gathered, was not. Gate 4 moves the evidence to the front.
