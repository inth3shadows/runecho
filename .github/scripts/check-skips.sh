#!/usr/bin/env bash
#
# Gate the set of skipped tests against .github/expected-skips.txt, and write the
# result to $GITHUB_STEP_SUMMARY.
#
# `go test` without -v prints only a per-package `ok`, which makes a test that
# SKIPPED indistinguishable from one that passed. Several tests here skip on a
# condition the RUNNER controls rather than the code — ruff absent, a chmod that
# does not deny (CAP_DAC_OVERRIDE, or a filesystem where mode bits are advisory),
# a stub that cannot exec on a noexec TMPDIR. Each is a correct skip on a
# developer box and a silent hole in coverage in CI. This turns that difference
# into a build failure instead of a line nobody reads.
#
# A script rather than an inline `run:` block so the logic can be exercised
# locally against staged logs. An inline block can only be tested by pushing,
# and a gate whose own behaviour is unverified is the thing this repo keeps
# finding in its instruments.
#
# Usage: check-skips.sh [test-log] [allowlist]
# Exit 0 when the skip set matches the allowlist, or when no test ran at all.
# Exit 1 on an unexpected skip, a stale entry, or a malformed allowlist line.

set -euo pipefail

LOG=${1:-test.log}
ALLOW=${2:-.github/expected-skips.txt}
SUMMARY=${GITHUB_STEP_SUMMARY:-/dev/stdout}

emit() { printf '%s\n' "$@" >>"$SUMMARY"; }

emit "### Skipped tests" ""

if [ ! -f "$LOG" ]; then
  emit "_No \`$LOG\`: the test step did not get far enough to produce one._"
  exit 0
fi

# "No SKIP lines" does NOT mean "nothing skipped" — it equally describes a run
# where no test ever started. On a compile error anywhere in the tree `tee` still
# creates the log, and it holds build errors and zero SKIP lines. Reporting
# "none skipped" there would be a positively false coverage claim, and FAILING
# there would stack a bogus stale-entry error on top of the real build error.
# `=== RUN` is the discriminator: a healthy run of this suite emits ~1,800 and a
# build failure emits none. `^FAIL` cannot serve — `FAIL <pkg> [build failed]`
# prints on precisely the run being excluded.
if ! grep -q '^=== RUN' "$LOG"; then
  emit "_No test executed — the build failed before any test started, so this run says nothing about what does or does not skip. See the Test step log._"
  exit 0
fi

if [ ! -f "$ALLOW" ]; then
  emit "**FAIL** — allowlist \`$ALLOW\` is missing, so nothing can be adjudicated."
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

: >"$tmp/allowed"
: >"$tmp/malformed"
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%$'\r'} # tolerate CRLF checkouts
  case "$line" in '' | '#'*) continue ;; esac
  name=${line%%:*}
  reason=${line#*:}
  name=$(printf '%s' "$name" | tr -d '[:space:]')
  reason=$(printf '%s' "$reason" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
  # No colon at all leaves name == line; an empty reason is an unexplained
  # silence, which is the thing the file refuses to carry.
  if [ "$name" = "$line" ] || [ -z "$name" ] || [ -z "$reason" ]; then
    printf '%s\n' "$line" >>"$tmp/malformed"
    continue
  fi
  printf '%s\n' "$name" >>"$tmp/allowed"
done <"$ALLOW"
sort -u -o "$tmp/allowed" "$tmp/allowed"

# `|| true` because grep exits 1 on no matches, which under `set -e` with
# pipefail would abort the script on the entirely normal zero-skip run.
grep -oE '^[[:space:]]*--- SKIP: [^[:space:]]+' "$LOG" |
  sed 's/.*--- SKIP: //' | sort -u >"$tmp/actual" || true
[ -f "$tmp/actual" ] || : >"$tmp/actual"

comm -23 "$tmp/actual" "$tmp/allowed" >"$tmp/unexpected"
comm -13 "$tmp/actual" "$tmp/allowed" >"$tmp/stale"

block() { # title, file
  emit "$1" '```'
  cat "$2" >>"$SUMMARY"
  emit '```' ''
}

fail=0
if [ -s "$tmp/malformed" ]; then
  block "**FAIL — allowlist lines with no \`TestName: reason\`:**" "$tmp/malformed"
  fail=1
fi
if [ -s "$tmp/unexpected" ]; then
  block "**FAIL — skipped but not in the allowlist.** The run measured less than it claims; either the environment degraded or the skip is deliberate and belongs in \`$ALLOW\` with a reason:" "$tmp/unexpected"
  fail=1
fi
if [ -s "$tmp/stale" ]; then
  block "**FAIL — listed as expected-to-skip but did not skip.** Either the test now runs (delete the entry) or it no longer exists (delete the entry). Left alone, \`$ALLOW\` is claiming coverage is missing that is not:" "$tmp/stale"
  fail=1
fi

if [ "$fail" -eq 0 ]; then
  n=$(wc -l <"$tmp/actual" | tr -d ' ')
  if [ "$n" -eq 0 ]; then
    emit "_None — every test ran._"
  else
    block "$n expected skip(s), all allowlisted:" "$tmp/actual"
  fi
fi

exit "$fail"
