# triage-check (prototype)

The reproducible form of Gate 4 in [`docs/check-worthiness.md`](../../docs/check-worthiness.md):
*does a candidate guard check earn its place?* Run a deterministic detector for
the defect class over real repos, split source findings from generated-output
noise, hand-classify the source findings, and apply the fixed bar.

This is what the #204 design-token investigation did by hand. The point of the
tool is that the **next** such question — i18n keys, feature flags, env-var
names — is answered the same way, against the same bar, instead of re-argued.

## Status: prototype

Spiked from the #204 scratchpad probe. A **maintainer tool** — not built, not
shipped in the release binaries, no Go dependency.

Two detectors ship:

| Detector | Fronts | Class |
|---|---|---|
| `css-custom-properties` | stylelint `no-unknown-custom-properties` | `var(--x)` with no declaration. Reproduces #204 exactly (2 findings, both FPs). |
| `i18n-keys` | nothing (see below) | `t('a.b')` / `i18nKey="a.b"` with no key in any locale catalog. |

**The contract held.** `i18n-keys` is shaped nothing like the CSS detector — a
JS/TS surface instead of CSS, JSON catalogs instead of CSS declarations,
dotted-key symbols instead of `--custom-props` — and it plugged into `triage.py`
with **zero changes** (verified by hash). That was the open question the spike
named; the plugin boundary (repo path in, `{file,line,symbol}` out, exit codes)
carried a second, differently-shaped check without special-casing.

**One nuance it surfaced, worth Gate 3's attention:** `i18n-keys` fronts no
incumbent. The tools that detect this (`i18next-cli`, i18nGuard, i18n-cleaner)
all need per-project i18n config, so none runs as a config-free sweep. The
detector therefore does minimal extraction itself — acceptable for *measurement*,
but a reminder that "front the incumbent" (rubric Gate 3) is an ideal some
classes can't satisfy, and the shipping check (if the class ever cleared Gate 4)
should still front a configured tool.

**Gate-4 run (2026-07-23):** `i18n-keys` over excalidraw + outline + mattermost
(~5.6k catalog keys). First pass: 26 findings — **all** the detector's own bugs
(apostrophe-truncated keys, `/* */` doc blocks, undecoded escapes, unmodelled
plural suffixes). Fixing each: 26 → 4 → **2**, and the final two were one genuine
missing key (`t("a user")` in outline, absent from its 1833-key catalog). Verdict
**DECLINE** (below the ≥3-genuine / ≥2-repo bar), and — the point — the residual
FP causes were every one of them i18next *framework semantics* the config-free
detector had to reimplement, which is exactly why Gate 3 says to front the
configured incumbent. The self-extraction detector is now correct enough to
measure with, and the fixes it needed are the evidence for that gate.

Known rough edges (deliberately not polished — see the spike's pivot note):
- Hand-classification is a JSON edit (`genuine: null → true/false`) or the
  `--genuine N` shortcut; no interactive classifier.
- `i18n-keys` is config-free extraction, so it is FP-prone by construction
  (namespaces, plural suffixes, framework variants) — fine for a measurement
  detector whose whole job is to surface candidates for hand-classification.

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
