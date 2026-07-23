# triage-check (prototype)

The reproducible form of Gate 4 in [`docs/check-worthiness.md`](../../docs/check-worthiness.md):
*does a candidate guard check earn its place?* Run a deterministic detector for
the defect class over real repos, split source findings from generated-output
noise, hand-classify the source findings, and apply the fixed bar.

This is what the #204 design-token investigation did by hand. The point of the
tool is that the **next** such question — i18n keys, feature flags, env-var
names — is answered the same way, against the same bar, instead of re-argued.

## Status: prototype

Spiked from the #204 scratchpad probe. It works end-to-end for the one detector
that ships (`css-custom-properties`) and reproduces #204's result exactly. It is
a **maintainer tool** — not built, not shipped in the release binaries, no Go
dependency.

Known rough edges (deliberately not polished yet — see the spike's pivot note):
- Hand-classification is a JSON edit (`genuine: null → true/false`) or the
  `--genuine N` shortcut; no interactive classifier.
- One detector. A second one (e.g. an i18n-key checker) is the real test of
  whether the plugin contract holds without per-check special-casing.

## Use

```bash
# 1. Run the detector over some repos.
scripts/triage-check/triage.py run \
  --detector css-custom-properties \
  --repos ~/proj/app-a,~/proj/app-b \
  --out /tmp/tokens.json

# 2. Hand-classify: for each source finding in /tmp/tokens.json, set
#    "genuine": true (a real undeclared-token bug) or false (a false positive —
#    e.g. declared at runtime, or in a scope the detector didn't see).

# 3. Verdict.
scripts/triage-check/triage.py verdict /tmp/tokens.json
#    …or skip the JSON edit and pass a hand tally:
scripts/triage-check/triage.py verdict /tmp/tokens.json --genuine 0
```

## The bar (from the rubric, kept as data in triage.py)

> ≥3 hand-confirmed genuine defects across ≥2 repos, **and** a false-positive
> rate low enough that an ask-posture gate would not block correct writes.

A run that finds nothing is a **success** — it costs hours and buys the answer
`#175` spent a whole feature learning. The tool computes the mechanical part of
the verdict; the FP-tolerance judgement (an ask-gate tolerates more noise than a
blocking one) stays human, on purpose.

## Adding a detector

Drop an executable at `detectors/<name>`. Contract:

- `argv[1]` is a repo path.
- Print a JSON list of `{"file", "line", "symbol"}` to stdout.
- Exit `0` clean, `1` with findings, anything else = the detector itself failed
  (the harness distinguishes these — a crash must never read as "clean").

Front an existing linter rather than writing detection from scratch: Gate 3 of
the rubric says if a mature tool already detects the class, RunEcho is deciding
whether to add *timing*, not *detection*, so the incumbent is exactly what the
evidence run should measure.

## The corpus trap this exists to route around

`bench/captured/corpus.json` is mined from transcripts where the **toolchain
errored**, so a silently-failing defect class (CSS, i18n, feature flags) can
never appear in it — "searched the corpus, found none" is guaranteed empty and
means nothing for those. triage-check uses a live detector over real repos
precisely because the corpus cannot answer for this class. See
`bench/README.md`.
