#!/usr/bin/env bash
# Hook shim for the runecho guard, invoked by hooks/hooks.json for BOTH events:
#
#   PreToolUse   guard.sh --hook-mode      decides (ask / defer) before the write
#   PostToolUse  guard.sh --outcome-mode   records the outcome + refreshes the IR
#
# One shim serves both on purpose. The binary-resolution block below is the part
# that rots (three fallback locations, an env override), and a second copy of it
# in an outcome.sh is exactly the drift this file's own contract note warns
# about — see the PostToolUse history in that note.
#
# Why this wrapper exists instead of calling `runecho-guard --hook-mode` directly:
# installing the plugin does NOT install the binary. The plugin only wires the
# hook; the binary comes from `bash install.sh` or a release tarball. On a machine
# where that step has not happened, a direct invocation returns 127 and surfaces a
# hook error on EVERY Edit/Write/MultiEdit — strictly worse than not installing the
# plugin at all. So a missing binary exits 0 silently here.
#
# Exiting 0 with no output is the guard's own "defer" response: it hands the edit
# back to Claude Code's normal permission flow, unmodified. That is the same
# fail-open posture the guard applies internally to every degraded state (no
# snapshot, unreadable store, stale IR) — a guard that cannot run must never
# obstruct an edit. Set RUNECHO_GUARD_STRICT=1 to make the guard itself surface
# degraded states it CAN detect; it cannot detect its own absence, which is what
# this file covers.
#
# The matcher (Edit|Write|MultiEdit) and the two mode flags are one contract
# shared by four places, and they must agree:
#   - plugins/runecho-guard/hooks/hooks.json   (this plugin)
#   - install.sh --print-hook-config           (the manual fallback)
#   - .claude/settings.json                    (this repo dogfooding itself)
#   - cmd/runecho-guard/main.go                (what the binary actually reads)
#
# That list said "three places" and omitted PostToolUse entirely, and nothing
# enforced it: --outcome-mode shipped as a working flag that NO config ever
# invoked. Every external install therefore produced asks and zero outcomes, so
# `fpreport` had no join key (its approval rate is computed from outcome
# records), RUNECHO_GUARD_LEARN could never reach its approval threshold, and
# the E6 auto-fresh reindex never ran — leaving the stale-IR false-positive
# class live for everyone but the author, whose personal settings.json wired it
# by hand. A prose list is not a contract: cmd/runecho-guard/hookwiring_test.go
# now parses all three configs and fails if any event goes unwired.

set -uo pipefail

# Mode is the first argument, defaulting to --hook-mode so an older hooks.json
# that calls this shim bare still gets the PreToolUse behavior it expects.
# Validated against a closed set rather than passed through: a typo would
# otherwise reach the binary as an unknown flag, and flag-parse failure on a
# PostToolUse hook is a per-edit error banner for a purely observational step.
mode="${1:---hook-mode}"
case "$mode" in
  --hook-mode | --outcome-mode) ;;
  *)
    echo "guard.sh: unknown mode '$mode' (want --hook-mode or --outcome-mode)" >&2
    exit 0
    ;;
esac

# Resolve the binary: PATH first (the normal case), then the two locations
# install.sh writes to. RUNECHO_BIN_DIR is install.sh's own override, so a user
# who redirected the install is found without extra configuration.
guard=""
if command -v runecho-guard >/dev/null 2>&1; then
  guard="$(command -v runecho-guard)"
elif [ -n "${RUNECHO_BIN_DIR:-}" ] && [ -x "${RUNECHO_BIN_DIR}/runecho-guard" ]; then
  guard="${RUNECHO_BIN_DIR}/runecho-guard"
elif [ -x "${HOME}/.local/bin/runecho-guard" ]; then
  guard="${HOME}/.local/bin/runecho-guard"
fi

# Not installed → defer silently. Deliberately not a warning: the hook fires on
# every edit, so a message here would be a per-edit nag about a state the user may
# have chosen (plugin enabled, binary intentionally absent).
if [ -z "$guard" ]; then
  exit 0
fi

# --outcome-mode is observational, and the plugin and the binary update through
# DIFFERENT channels: the plugin is git-sourced and moves on `/plugin update`,
# the binary comes from install.sh or a tarball and is known to go stale (a
# dogfood session once found it six releases behind). --outcome-mode only exists
# from v0.10.0, so a freshly-updated plugin driving an older binary hits
# flag.ContinueOnError, which prints usage and exits 2 — and because this shim
# execs, that 2 becomes the hook's exit code on EVERY Edit/Write/MultiEdit. The
# mode check above cannot catch it: it validates the argument this script was
# given, not whether the resolved binary understands it.
#
# So run it rather than exec it, and always exit 0.
#
# Do NOT blanket-discard stderr here. An earlier revision did, on the claim that
# "runOutcomeMode writes nothing to stderr" — that claim is false. The outcome
# path reaches registry/snapshot/generator and the parser grammar loaders, which
# warn on a corrupt enrolled_at, a DB fault, or a grammar that failed to load
# (internal/snapshot/registry.go's warning exists specifically so a DB fault is
# "debuggable rather than silent"). Blanket-discarding re-silenced exactly those,
# for every plugin user, on every edit.
#
# So filter instead of discard: drop only the stale-binary flag-parse dump, and
# let every other diagnostic through. Stderr is captured rather than streamed,
# which is fine for an observational hook.
if [ "$mode" = "--outcome-mode" ]; then
  # Redirection order matters: inside the substitution fd1 is already the capture
  # pipe, 2>&1 points stderr at it, then >/dev/null moves stdout away. Captures
  # stderr only. Discarding stdout is safe — runOutcomeMode writes none.
  err="$("$guard" --outcome-mode 2>&1 >/dev/null)" || true
  # Filter per LINE, not on the whole blob: a `case` over the full string would
  # let one flag-parse line suppress a genuine warning emitted alongside it.
  # Not reachable today (every warner is lazy and post-flag-parse, so a binary
  # that fails flag parsing never reaches one) — but the airtight form costs
  # nothing and does not depend on that staying true.
  if [ -n "$err" ]; then
    printf '%s\n' "$err" | grep -v 'flag provided but not defined' >&2 || true
  fi
  exit 0
fi

# exec so the guard owns stdin/stdout directly — the hook protocol is JSON in,
# JSON out, and an extra shell frame between them buys nothing.
exec "$guard" "$mode"
