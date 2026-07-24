# Changelog

All notable changes to RunEcho are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The build version is the single source of truth in `internal/version`, stamped at
install time from `git describe --tags` (see `install.sh`).

> **Note on pre-0.5.0 tags.** The tags `v0.1.1`–`v0.4.0` are non-monotonic with
> commit history (e.g. `v0.1.1` is a newer commit than `v0.4.0`) and predate
> changelog tracking — see [#51](https://github.com/inth3shadows/runecho/issues/51).
> `v0.5.0` is the first changelog-tracked, monotonic release. The older tags are
> left in place as history; `git describe` resolves to the nearest newer tag, so
> they no longer affect the reported version.

## [Unreleased]

## [0.17.2] — 2026-07-24

### Changed
- fix: release binaries shipped without the Rust and Ruby grammars (#229)

## [0.17.1] — 2026-07-24

### Changed
- **`structure`'s default response no longer carries per-symbol content hashes.**
  `detail=symbols` (the default) returns each symbol's `name`/`kind`/`line` and
  the per-file `hash` and `refs` as before, but omits `symbols[].hash`. On this
  repo that is 76,278 → 36,724 tokens, a 52% cut; ~60% of the old payload was
  1,864 unique SHA-256 strings. **A client reading `symbols[i].hash` from a
  default call must now pass `detail=hashes`**, a new tier returning the previous
  shape exactly. No files, symbols, kinds, lines or refs changed (#224).

## [0.17.0] — 2026-07-24

### Changed
- guard: complementary build constraints are not a duplicate (#225)

## [0.16.1] — 2026-07-23

### Changed
- bench: measure context-token cost of every surface, and correct the README (#201) (#223)

## [0.16.0] — 2026-07-23

### Changed
- guard: multi-candidate did-you-mean, and the allocation bug behind it (#200) (#222)

## [0.15.2] — 2026-07-23

### Changed
- triage-check: real i18n Gate-4 run (DECLINE) + the detector fixes it forced (#221)

## [0.15.1] — 2026-07-23

### Changed
- triage-check: i18n-keys detector — the plugin contract holds (#220)

## [0.15.0] — 2026-07-23

### Changed
- guard: cap the contract read, and make per-check FP rates derivable (#218)

## [0.14.0] — 2026-07-23

### Changed
- guard: close a reachable DoS, a fail-closed panic path, and a prompt-injection surface (#212)

## [0.13.0] — 2026-07-23

### Added
- guard: every `decisions.jsonl` record now carries `gv`, the guard binary version
  that wrote it. `fpreport` groups by it, warns loudly when a window spans more
  than one build, and takes `--gv <version>` to scope a report to a single one
  (#207).

### Security

- **Reachable DoS in `golang.org/x/text` (GO-2026-5970).** `ir.normalizePath`
  NFC-normalizes the relative path of every indexed file, so a crafted file
  name could spin forever inside `x/text`. The walk only checks for
  cancellation *between* files, so the hang bypassed `DefaultGenerateTimeout`
  entirely — and the same function is on the auto-refresh path the PreToolUse
  guard uses. Bumped `x/text` to v0.39.0 and `x/sys` to v0.44.0
  (GO-2026-5024, Windows, unreachable). `govulncheck ./...` is now clean.
- **A panic in `runecho-guard --hook-mode` blocked the edit instead of
  deferring it.** A Go panic exits status 2, which Claude Code reads as "block
  this tool call" — the inverse of the guard's fail-open contract. Hook modes
  now run under `deferOnPanic`, which buffers the response, discards it on
  panic, warns on stderr, and exits 0.
- **Repo-controlled file paths reached the agent unsanitized.** The
  dangling-refs and duplicate-symbol warnings joined repo-relative paths into
  `permissionDecisionReason`. Symbol names are identifier-constrained; POSIX
  file names are not, so a file named with embedded newlines and prose could
  plant text in the string the agent reads at a permission decision point. All
  repo-derived paths now pass through `sanitizeReasonPath`.
- **Periodic reindex log moved out of `/tmp`.** `--periodic` wrote to the
  fixed path `/tmp/runecho-reindex.log`, a symlink target on a shared host.
  It now writes to `$RUNECHO_HOME/logs/reindex.log` (0700). The dependency
  export cache is likewise 0700/0600 rather than 0755/0644.
- **Supply chain.** All GitHub Actions pinned to commit SHAs (the release jobs
  hold `contents: write`), Dependabot added for `github-actions` and `gomod`,
  and release archives now carry Sigstore build provenance — verify with
  `gh attestation verify <archive> -R inth3shadows/runecho`.

### Changed

- `fpreport --max-rate` no longer evaluates a mixed-version window. It skips with
  a stderr note naming `--gv`, the same way it already skips below the minimum ask
  count — gating on an average across two different programs is worse than not
  gating. Measured motivation: the same log reported a 70% approval rate over 30
  days and 19% over the trailing 2, because the installed binary had been six
  releases stale.
- **Minimum Go for source builds is now 1.25** (was 1.24). Forced, not chosen:
  both `x/text@v0.39.0` and `x/sys@v0.44.0` declare `go 1.25.0`, and no lower
  `x/text` carries the GO-2026-5970 fix.
- `SECURITY.md` documented store permissions as `0755`/`0644`; the code has
  used `0700`/`0600` for some time. Corrected, and the guard's panic posture
  and path-sanitization are now documented there.
- `install.sh` rejects a `RUNECHO_BIN_DIR` containing shell metacharacters,
  matching the quoting rule `cmd/runecho-ir/install.go` already applied.

## [0.12.3] — 2026-07-23

### Changed
- contract: the edit-scope guard dimension (#12 D2) (#206)

## [0.12.2] — 2026-07-23

### Changed
- install: ship the Rust and Ruby grammars (they were inert in the real binary) (#199)

## [0.12.1] — 2026-07-23

### Changed
- contract: edit-scope contracts — format, V9 storage, CLI (#12 D1)

## [0.12.0] — 2026-07-23

### Changed
- parser: fix two logic bugs in the Rust and Ruby parsers, add fuzz + invariants (#197)

## [0.11.1] — 2026-07-23

### Changed
- snapshot: single deletion path + orphan invariants (#196)

## [0.11.0] — 2026-07-23

### Changed
- parser: add Ruby (.rb) support (#195)

## [0.10.0] — 2026-07-23

### Changed
- parser: add Rust (.rs) support (#194)

## [0.9.3] — 2026-07-22

### Changed
- bench: let the captured corpus measure the qualified-call checks (#171 Part A) (#186)

## [0.9.2] — 2026-07-21

### Fixed
- Three guard false negatives found by a retroactive adversarial review of the
  v0.7.1–v0.9.0 releases, each a missed hallucination and each now regression-tested:
  - **Lambda default-value arguments leaked into the known set** (v0.9.0). A lambda
    whose default is a multi-argument call (`lambda cfg=build(a, b): …`) bound the
    call's arguments (`b`) as if they were parameters, so a later hallucinated
    `b()` was masked. `pyLambdaParams` now splits the parameter list with the same
    bracket-aware splitter the `def` path uses.
  - **A non-unique `old_string` could seed the wrong string state** (v0.7.1). Under
    a `replace_all` edit (which the hook does not parse) a duplicated full-line
    block straddling a docstring boundary could seed "open string" state and mask
    a hallucinated call in the replacement. `blockStartLine` now returns no match
    when the block appears in more than one place — fail-open toward flagging.
  Also fixed a related false positive:
  - **The duplicate-symbol check reported cross-language collisions** (v0.8.0).
    Editing a Go file that adds `main` warned about a sibling Python `def main` in
    the same directory, contradicting the check's own premise that Go and Python
    are separate namespaces. Candidate definitions are now filtered to the same
    language, not just the same directory.

## [0.9.1] — 2026-07-21

### Added
- `runecho-ir fpreport` — the guard's observed false-positive rate, read from
  `decisions.jsonl`. It joins each ask to its outcome symbol-exactly (not by the
  loose file-and-time-window guess) and reports the fraction the agent approved
  anyway, broken down by check and language, plus the most-approved symbols and
  loudest repos. `--json` for machines, `--max-rate F` for CI/cron gating (exit 2 = tripped
  or bad flag, 1 = no log/skip, 0 = pass; gated to ≥20 asks, and the gate result
  is also in the JSON `gate` object). The approval rate is an upper
  bound on the true FP rate — some approvals are genuine fixes, not dismissals —
  and is meaningful only while the guard actually prompts (a hook that discards
  the guard's output makes every "approval" an artifact). Complements
  `guard-stats`, which reports ask volume rather than correctness.

## [0.9.0] — 2026-07-21

### Fixed
- The guard no longer false-positives on a Python parameter used as a callable.
  A `Callable`-typed parameter (`def pump(transform: Callable[[str], Any]): out =
  transform(line)`) or a lambda argument (`lambda name, fetch: fetch()`) is bound
  by its signature, so calling it is not a hallucination — but neither the
  definition extractor (which sees only `def`/`class`) nor the assignment-target
  fold recognized it. `PyParamNames` now folds parameter names from every `def`,
  `async def`, and `lambda` signature (multi-line signatures included) into the
  known set. It binds the parameter NAME only, never its type annotation, so a
  genuine hallucinated call to a type (`def f(cb: Handler): Handler()` where
  `Handler` is undefined) is still caught. This was the last surviving Python
  false-positive class in six weeks of live decision logs; replaying that corpus,
  the reproducible violation false positives drop from 18 to 0, with the synthetic
  benchmark's 100%% recall / 0%% false-positive rate unchanged.

### Changed
- Positioning aligned across every surface, and scoped honestly. The README lede,
  `docs/runecho-vs-field.html`, and the GitHub description had drifted into three
  different pitches — "code-truth oracle", "prevention, not detection", and a
  claim to stop agents writing bad symbols outright. All three now lead with the
  same thing: RunEcho is one cheap layer, not the whole answer, and it catches
  **4 of 9** real hallucinations in its own N=15 corpus, with the 5 qualified-position
  misses named rather than buried. A guard that overclaims is a guard people turn
  off, and a guard that is off protects nothing — so the scope is stated in the
  lede instead of the appendix.

## [0.8.0] — 2026-07-21

### Fixed
- The duplicate-symbol guard (E5) no longer fires on Python or JS/TS. Its
  same-directory rule encodes Go's package==directory model, where two files in
  one directory sharing a top-level name genuinely collide. Python and JS/TS
  files are independent module namespaces, so `scripts/a.py` and `scripts/b.py`
  can both define `main()` with nothing shared between them — which is how a
  directory of independent entry-point scripts is supposed to look. Every
  Python and JS duplicate ask in six weeks of live decision logs was this false
  positive: `main` most of all (20 of 35), plus per-script helpers (`pad`,
  `parseArgs`, `escapeHtml`) and re-declared TS types. Go is unchanged and still
  warns. The cost is no longer flagging a genuine Python/JS reimplementation —
  a style concern with no compile or runtime consequence, in a guard whose job
  is catching hallucinated symbols.

## [0.7.1] — 2026-07-21

### Added
- RunEcho ships as a **Claude Code plugin**. `/plugin marketplace add
  inth3shadows/runecho` then `/plugin install runecho-guard@runecho` wires the
  `PreToolUse` guard with no hand-merged JSON, and uninstalls cleanly — the
  supported alternative to pasting `install.sh --print-hook-config` output into
  `~/.claude/settings.json`. The repo serves as its own marketplace. The plugin
  carries wiring only, never the binary: `hooks/guard.sh` resolves
  `runecho-guard` from `PATH`, `$RUNECHO_BIN_DIR`, then `~/.local/bin`, and
  defers silently when none match, so enabling the plugin without installing the
  binary costs nothing instead of erroring on every edit. `--print-hook-config`
  remains as the fallback where plugins are unavailable.
- **Releases are cut automatically on merge to `master`.** A merged fix no longer
  waits for someone to remember to tag it. The bump level comes from the commit
  subject's prefix — `guard:`/`parser:` bump minor, `docs:`/`chore:` cut no
  release at all, anything else bumps patch — after which `CHANGELOG.md`'s
  `[Unreleased]` section is renamed to the new version, committed, tagged, and
  published by goreleaser in one atomic push. The decision logic lives in
  `scripts/next-release.py` with its own test suite (`scripts/next-release-test.py`,
  run by the release workflow) rather than inline in YAML, because a workflow that
  pushes tags is the wrong place to find out a regex was wrong. The tradeoff is
  deliberate: `guard:` covers both a false-positive fix and a new check, so the
  resulting semver is mechanically consistent rather than semantically precise.

### Fixed
- The edit-time guard no longer scans docstring prose or SQL string contents as
  code. A Claude Code hook edit whose new text began inside a pre-existing
  docstring was validated without the string-masking state that sits in the
  untouched lines above it, so prose words followed by a parenthetical
  (`candidates (#47)`) and SQL keywords (`VALUES (`) read as calls to undefined
  symbols. Replaying six weeks of live decisions, this was the single largest
  false-positive class — 37 of 40 reproducible cases, ~92%. (Shipped in the
  v0.7.1 binary; this note was added retroactively.)
- Guard no longer false-positives on bare calls to locally-bound callables. It
  now folds local binding targets into the additive known set — JS/TS
  destructures and `useState` setters (#156), and Python assignment targets
  (`handler = HANDLERS[key]; handler(payload)`). Extraction is deliberately
  precise (assignment/declarator targets only, never a parameter's type
  annotation or a keyword argument) so a genuine hallucination of the same name
  is still caught. Promotes the two tracked corpus false positives
  (`js-fp-dynamic-callable`, `py-fp-local-callable`) to true negatives.

## [0.7.0] — 2026-07-12

44 commits since 0.6.0: parser fidelity, guard hardening, and CLI/IR features.

### Added
- `runecho-ir guard-stats` subcommand for guard decision-log summaries (#100).
- `runecho-ir churn --json` output, matching the existing `diff --json` shape (#95).
- Glob-pattern support in `.runechoguardignore` (e.g. `track*`) (#94).
- Offset-based pagination for the MCP `locate` tool on large repos (#97).
- JS/TS parser: AST-based import/export extraction replacing the regex pass, plus
  a regex fallback when the grammar gives up (#98, #109).
- Generic-instantiated call detection for the guard: `Foo<T>(x)` in TS/JS and
  `Foo[int](x)` in Go (#124, #126).
- `codegraph_render.py` + `make graph` — scoped call-graph SVG rendering tool (#78).

### Changed
- `repo reindex` is now incremental — unchanged files are skipped instead of
  re-parsed on every run (#99).
- Struct/class bodies are now hashed over their full span, so field/member
  changes surface in `diff` (closes #53) (#103).
- Guard reads `.runechoguardignore` from the actual git worktree root instead of
  the bare-repo enrolled container, fixing false positives in claudew/codexw-style
  worktree layouts (#119).
- `runecho-ir diff --since=""` and `map --since=""` are now distinguished from an
  omitted flag rather than silently treated the same (#110, #114).
- `locate` named lookups now search all symbol kinds, so a zero-match result is
  definitive rather than kind-scoped (#111).

### Fixed
- Six defects from a combined codegraph/runecho bug hunt, plus follow-on fixes for
  bare calls to imported names in the guard's pre-commit path (#79, #81, #82).
- Three silent false-negatives in the hallucination-detection path, and a batch of
  further guard fidelity fixes (multi-line defs, JS declarators, nested Go
  generics, `$`-prefixed bare-arrow false positives, Go interface method
  signatures misread as calls) (#108, #117, #123, #129, #130).
- E5 duplicate-symbol check: package-scoping and test-file exclusion (#115, #116).
- `normalizePath` idempotence bug (#104); `.mjs`/`.cjs` now recognized as JS files
  (#101); dropped bare `export *` re-exports now captured (#96).
- Concurrency-safe schema migration and `ResolveRepo` bare-root recovery
  (#121, #122).

### Security
- Red-team remediation sweep: parser DoS hardening, store permission fixes, guard
  input-handling hardening, and a follow-up hardening pass (#71, #127, #128).

## [0.6.0] — 2026-07-01

### Added
- Guard: non-call reference extraction — SCREAMING_SNAKE constant references and
  type annotations, widening the guard beyond bare calls (closes #56) (#58).
- Guard: dropped-import check flagging a removed import whose name is still
  referenced in-file (#59), with false-positive suppression for locally-rebound
  names (#60).
- Guard: E5 duplicate-symbol check, detecting same-named symbols redeclared in a
  file (#66), with package-scoping and fast-path fixes (#68).
- `SECURITY.md` — threat model and vulnerability-reporting process (#67).
- Pre-push tag-monotonicity hook, installable via `install.sh --hook-pre-push`
  (#63).
- Synthetic hallucination-reduction benchmark harness for the guard (#55).
- goreleaser-based release workflow — tagged pushes now publish prebuilt binaries
  for macOS/Linux/Windows plus checksums (#69).

### Fixed
- Multi-worktree `common_dir` resolution disambiguated, fixing cross-worktree
  repo lookup (closes #61) (#62).
- `internal/claims` symbol-ref extraction gained `const` for JS/TS parity with the
  guard's own extraction (#64).
- CI gofmt gate, `.gs` (Google Apps Script) doc coverage, and pre-push hook
  automation corrected (#65).

## [0.5.0] — 2026-06-28

First changelog-tracked release; establishes monotonic versioning and a build
version stamp. Notable recent changes folded into this baseline:

### Added
- Single-source build version (`internal/version`), stamped from `git describe`;
  `runecho-guard --version` reports it.
- Go parser extracts exported interface method signatures into Functions,
  qualified by type (`Reader.Read`), located and hashed.
- JS/TS parser captures `export type { … }` re-exports and
  `export type`/`interface`/`enum` declarations in Exports.
- Python: when a module declares no `__all__`, exports fall back to the
  best-practice no-underscore convention — top-level public functions/classes
  plus module-level `UPPER_CASE` constants. An explicit `__all__` (even empty)
  stays authoritative.
- Fuzz harnesses for the guard diff parser and the claims symbol extractor,
  matching the existing parser fuzzers.

### Changed
- IR generation is time-bounded by a context deadline (default 30s, applied when
  the caller sets none; the MCP oracle passes a per-request deadline), so a
  pathological repo or stalled filesystem can no longer hang the indexer. A walk
  that exceeds the deadline returns an error instead of blocking indefinitely.
  The CLI honors `RUNECHO_GENERATE_TIMEOUT` (a Go duration, or `off`/`none`/`0`
  to disable) to raise or remove the ceiling for a large/slow-filesystem repo.

### Fixed
- Documentation referenced a non-existent `runecho-ir index` subcommand and a
  stale guard-corpus count; both corrected.
