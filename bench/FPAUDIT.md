# False-positive rate, measured against git history — not approvals

RunEcho's guard fires an `ask` when it thinks a symbol reference doesn't
resolve. The obvious way to measure "how often is it wrong" is the rate at
which a user approves the edit anyway. That number turned out to have **no
variance to measure**: joined to transcript ground truth over 30 days across
every dogfooded repo, **308 of 308** ask-gated edits were approved and **zero**
were denied. `fpreport`'s approval rate — previously quoted as a
false-positive proxy — describes a join's failure modes, not guard precision.
A constant cannot rank anything. Full derivation in
[PR #303](https://github.com/inth3shadows/runecho/pull/303).

`fpaudit` replaces it with something that has real variance: it judges each
flagged symbol against **git history** instead of a human decision.

Reproduce: `runecho-ir fpaudit --days 30` against your own `~/.runecho`
dogfood log. Measured live on 2026-08-07 across this project's own dogfood
corpus (every repo the maintainer worked in with the guard enabled, not a
constructed benchmark):

## Results

**405 ask events, 532 flagged symbols, 406 rated** (86 n/a, 40 unanswerable).

| verdict | count | share | meaning |
|---|---:|---:|---|
| **fp** | 62 | 15.3% | already defined at ask time — the guard's resolver missed it |
| **premature** | 137 | 33.7% | not defined then, defined now — the guard was **correct**, just early |
| **stands** | 207 | 51.0% | never came to resolve — the guard caught something real |

## What this says

**The `premature` bucket is what no approval-based metric could ever see.**
Founding case: `hashToSeed` in a dogfooded repo's `track-b-db.ts`, read for
weeks as the cleanest false positive in the log. A symbol-existence search
finds that declaration today — but it first entered the tree **ten hours
after the ask** that flagged it. The agent was writing the caller before the
callee. Judged against the file as it is *now*, a correct catch looks like a
resolver bug; judged against the file as it was *at ask time* — what `fpaudit`
actually does — it wasn't one. **A high `premature` share is not a resolver
bug.** It means a check fires at the wrong moment in the edit sequence, and
the fix is to move the check later, not to widen the known-symbol set.

**Two dated questions, no human in the loop.** Was the symbol defined
*anywhere in the repo* at ask time, and is it defined *now* — both answered
read-only against git history (`rev-list`, `rev-parse`, `grep`; nothing is
checked out, nothing written). Approval carries no signal into this number at
all.

## The rate is shrinking, and it traces to specific fixes

The 30-day number pools a month of guard versions — the corpus's own
`fpreport` already warns against trusting a pooled window. Narrowing it shows
`fp` isn't flat; it's falling, and recent data traces the drop to a named
commit rather than noise:

| window | rated | fp | fp share |
|---|---:|---:|---:|
| 30 days (2026-07-08 → 08-07) | 406 | 62 | 15.3% |
| 14 days (2026-07-24 → 08-07) | 84 | 9 | 10.7% |
| 7 days (2026-07-31 → 08-07) | 27 | 2 | 7.4% |
| 3 days (2026-08-04 → 08-07) | 24 | 0 | **0.0%** |

Both `fp` verdicts in the 7-day window are Python, and both predate
[`9ee28ea`](https://github.com/inth3shadows/runecho/commit/9ee28ea) (2026-08-04
17:09, PR #288, "fix 7 extract.go false positives/negatives"), which fixed
exactly this class of bug — including two Python-specific extraction defects
(`isFStringPrefix` misreading `bf`/`fb`/`ff` prefixes; `appendConstRefs`
skipping a dict key or mis-tagging a tuple-assignment target as a reference)
each independently capable of producing a genuine `fp` on real code, plus a
same-day follow-up ([`a8aded1`](https://github.com/inth3shadows/runecho/commit/a8aded1),
"fix multi-line dict-key false negative in appendConstRefs/defNames"). Every
`fp` verdict in the 3-day window that followed is zero, across all three
languages.

**Read this as plausible, not proven.** Two `fp` events removed by one
extraction-bug batch is consistent with the timing, not a controlled
before/after trial — the 7-day and 3-day denominators are small (27 and 24
rated symbols) and the next batch of asks could easily reintroduce a nonzero
rate. The honest claim is: recent `fp` share is lower than the 30-day
headline, and there is a specific, dated, described fix at the boundary where
it dropped — not "the guard's false-positive rate is now zero."

## What this deliberately does NOT say

**Do not read `stands: 51.0%` as "the guard was right 51% of the time."**
`stands` also absorbs symbols that were never real references to begin with —
a SQL keyword, comment prose the extractor mis-parsed as a call. Those are
real guard false positives of a *different* kind (extraction, not
resolution), and separating them from genuine catches needs the edit's added
lines, which the decision log does not record. `stands` is a ceiling on
correctness, not a measured precision figure.

**86 symbols (`n/a`) are outside what this oracle can judge at all**, and
`n/a` is not one thing. Since #393 the audit splits it, because the kinds call
for opposite responses:

- `not-a-resolution-claim` — the question does not apply. `duplicate-symbol`
  and `dangling` flag a name that *is* defined (the check is about whether
  that's a problem); `call-shape` flags a callee that resolves by construction
  — its own ask says "the symbol resolves but the call does not match it";
  ruff `F811` is the duplicate shape. Correct and permanent.
- `no-oracle-question` — a real resolution claim *this* oracle cannot ask.
  `qualified` and `deps-go` assert package membership, and resolving a
  qualifier to a package is not something `git grep` does. Honest missing
  coverage.
- `mixed-reason` — the ask names both kinds and the record does not say which
  flagged this symbol. Reported on its own rather than folded into either, so
  the missing-coverage figure stays trustworthy.
- `legacy-record` — written before the guard recorded `claim_symbols`.
  Shrinks on its own as the window moves forward.

`file-scope` and `contract+violations` used to land here and no longer do.
Both were unrateable for a reason that never applied to them: the audit read
`learn_symbols`, which is gated on what an **approval licenses** — deliberately
narrow, because it feeds the guard's own known-set. What the audit needs is what
the **check asserted**, which is a different question and is now recorded
separately as `claim_symbols`.

**Each check is asked the question it actually made.** `violations` asserts
"resolves nowhere in the repo" and is answered tree-wide. `file-scope` and ruff
`F821` both assert "not reachable in **this file**" — pyflakes resolves against
the file's own scopes, not the repo's — and are answered against the edited file
alone. Asking the repo-wide question of either would report every correct catch
as a false positive: the name they flag is usually declared elsewhere, which is
the premise of the finding, not evidence against it. That mistake has a
precedent in this very file — the audit's first run scored `duplicate-symbol` at
27 fp / 0 stands before `n/a` existed — and it very nearly shipped again here,
caught in review.

The numbers above predate `claim_symbols`, which only stamps records written by
a guard new enough to carry it — so the per-check table fills in going forward,
not retroactively.

**This is one maintainer's dogfood corpus, not a controlled benchmark.** Same
scope limit as [TOKEN-COST.md](TOKEN-COST.md)'s "one repo" caveat, generalized:
these numbers describe how the guard performed across the specific repos and
editing patterns it actually saw, not a claim about hallucination rates in
general. The oracle itself is regex-over-source (`git grep`), chosen to be
*independent* of the guard's own extractors rather than assumed better than
them — `Audit` takes an `Oracle` interface, so a parser-grade replacement
doesn't require redesigning the measurement.

## Related measurement

[TOKEN-COST.md](TOKEN-COST.md) measures a different axis entirely: not
whether the guard is *right*, but what it *costs* in context tokens. Same
posture on both — publish the number that required correcting an existing
claim, not only the ones that flatter it.
