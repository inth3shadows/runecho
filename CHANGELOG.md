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
