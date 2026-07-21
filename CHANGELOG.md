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
