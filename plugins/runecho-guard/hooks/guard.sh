#!/usr/bin/env bash
# PreToolUse shim for the runecho guard, invoked by hooks/hooks.json.
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
# The matcher (Edit|Write|MultiEdit) and the --hook-mode invocation are one
# contract shared by three places, and they must agree:
#   - plugins/runecho-guard/hooks/hooks.json   (this plugin)
#   - install.sh --print-hook-config           (the manual fallback)
#   - cmd/runecho-guard/main.go                (what the binary actually reads)

set -uo pipefail

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

# exec so the guard owns stdin/stdout directly — the hook protocol is JSON in,
# JSON out, and an extra shell frame between them buys nothing.
exec "$guard" --hook-mode
