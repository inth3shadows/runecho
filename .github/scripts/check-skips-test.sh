#!/usr/bin/env bash
#
# Self-test for check-skips.sh.
#
# The gate it exercises decides whether every pull request goes red, and it was
# shipped (#343) and then fixed (#345) with its case suite living only in a
# scratch directory — enforcing on every PR with nothing in the repo proving it
# still works. This is that suite, committed.
#
# Fixtures are SYNTHETIC rather than captured runs: a real `go test -race -cover -v`
# log of this repo is ~3,800 lines, which is not something to carry in git to
# assert three things about. The shapes below are copied verbatim from real logs
# observed in CI and locally — `=== RUN   TestName`, `--- SKIP: TestName (0.00s)`,
# and `FAIL <pkg> [build failed]` — so the risk of a synthetic corpus quietly
# encoding the same assumptions as the code is bounded to those three line
# formats. If go's test output format ever changes, this suite keeps passing
# while the gate stops working; that is the one failure mode it cannot see.
#
# Usage: check-skips-test.sh    (exit 0 = all cases pass)

set -uo pipefail # deliberately NOT -e: several cases expect a non-zero exit

HERE=$(cd "$(dirname "$0")" && pwd)
GATE=$HERE/check-skips.sh

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fails=0
cases=0

# A log with `=== RUN` lines, so the gate treats it as "tests actually ran", plus
# whichever skips the caller names.
mklog() { # dest, [skipped test names...]
  local dest=$1
  shift
  {
    echo "=== RUN   TestAlpha"
    echo "--- PASS: TestAlpha (0.01s)"
    for t in "$@"; do
      echo "=== RUN   $t"
      case "$t" in
      */*) printf '    --- SKIP: %s (0.00s)\n' "$t" ;; # subtests are indented
      *) printf -- '--- SKIP: %s (0.00s)\n' "$t" ;;
      esac
    done
    echo "PASS"
    echo "ok  	github.com/inth3shadows/runecho/internal/guard	1.204s"
  } >"$dest"
}

# What `tee` captures when a package fails to compile: no `=== RUN` at all, and a
# `FAIL` line that a naive "did tests run" check would match.
mkbuildfail() {
  cat >"$1" <<'EOF'
# brokenmod [brokenmod.test]
./x.go:3: undefined: undefined_symbol
FAIL	brokenmod [build failed]
FAIL
EOF
}

check() { # label, want-exit, log, allowlist, [substring that must appear on stdout]
  local label=$1 want=$2 log=$3 allow=$4 needle=${5:-}
  cases=$((cases + 1))
  local out got
  # A real file, not the unset fallback: stdout and the summary file are
  # different destinations and #344 proved the difference matters.
  out=$(GITHUB_STEP_SUMMARY="$work/summary.md" bash "$GATE" "$log" "$allow" 2>&1)
  got=$?
  if [ "$got" != "$want" ]; then
    printf 'FAIL  %-42s exit=%s want=%s\n' "$label" "$got" "$want"
    fails=$((fails + 1))
    return
  fi
  if [ -n "$needle" ] && ! printf '%s' "$out" | grep -qF -- "$needle"; then
    printf 'FAIL  %-42s exit ok but stdout lacks %s\n' "$label" "$needle"
    printf '%s\n' "$out" | sed 's/^/        | /'
    fails=$((fails + 1))
    return
  fi
  printf 'ok    %-42s exit=%s\n' "$label" "$got"
}

allow=$work/allow.txt
cat >"$allow" <<'EOF'
# comment lines and blanks are ignored

TestFileScopeFPSmoke: opt-in smoke harness
TestSpikeGoplsOracleVarType: opt-in spike
EOF

mklog "$work/matching.log" TestFileScopeFPSmoke TestSpikeGoplsOracleVarType
check "skip set matches the allowlist" 0 "$work/matching.log" "$allow" "2 expected skip(s)"

mklog "$work/unexpected.log" TestFileScopeFPSmoke TestSpikeGoplsOracleVarType TestSomethingNewSkipped
check "unexpected skip fails" 1 "$work/unexpected.log" "$allow" "TestSomethingNewSkipped"

mklog "$work/stale.log" TestFileScopeFPSmoke
check "stale entry fails" 1 "$work/stale.log" "$allow" "TestSpikeGoplsOracleVarType"

# Exact matching is documented behaviour: a parent entry must NOT cover a
# subtest, because a different thing skipping is a different fact.
mklog "$work/subtest.log" TestFileScopeFPSmoke TestSpikeGoplsOracleVarType "TestFileScopeFPSmoke/inner"
check "parent entry does not cover a subtest" 1 "$work/subtest.log" "$allow" "TestFileScopeFPSmoke/inner"

# These two assert the MALFORMED banner, not merely a non-zero exit and the
# offending name. Both were originally written that looser way and a mutation
# that stopped requiring a reason survived: an unexplained entry is then treated
# as valid, never skips, and fails as STALE instead — same exit code, same name
# in the output, entirely different reason. The banner is what separates them.
noreason=$work/noreason.txt
printf 'TestFileScopeFPSmoke: fine\nTestSomething\n' >"$noreason"
check "entry with no reason fails" 1 "$work/matching.log" "$noreason" "allowlist lines with no"

emptyreason=$work/emptyreason.txt
printf 'TestFileScopeFPSmoke: fine\nTestSomething:   \n' >"$emptyreason"
check "entry with an empty reason fails" 1 "$work/matching.log" "$emptyreason" "allowlist lines with no"

empty=$work/empty.txt
printf '# nothing is expected to skip\n' >"$empty"
mklog "$work/noskips.log"
check "zero skips against an empty allowlist" 0 "$work/noskips.log" "$empty" "None"

# A compile error still produces a log, and it holds zero SKIP lines. Reporting
# "none skipped" there would be a false coverage claim; FAILING there would stack
# a bogus stale-entry error on top of the real build error.
mkbuildfail "$work/buildfail.log"
check "build failure is not adjudicated" 0 "$work/buildfail.log" "$allow" "No test executed"

check "missing test log is not adjudicated" 0 "$work/nope.log" "$allow" "did not get far enough"
check "missing allowlist fails" 1 "$work/matching.log" "$work/nope.txt" "is missing"

# The regression #345 fixed, and the shape every earlier case was blind to: with
# GITHUB_STEP_SUMMARY set to a FILE, the reason must still reach stdout, because
# stdout is what the job log shows. Every check above already asserts on stdout
# with the summary pointed at a file, so this case only has to prove the summary
# file is still written too — the other half of the same fix.
: >"$work/summary.md"
GITHUB_STEP_SUMMARY="$work/summary.md" bash "$GATE" "$work/unexpected.log" "$allow" >/dev/null 2>&1
cases=$((cases + 1))
if [ -s "$work/summary.md" ] && grep -qF "TestSomethingNewSkipped" "$work/summary.md"; then
  printf 'ok    %-42s\n' "summary file is written as well as stdout"
else
  printf 'FAIL  %-42s summary file empty or missing the finding\n' "summary file is written as well as stdout"
  fails=$((fails + 1))
fi

echo "-----------------------------------------------"
if [ "$fails" -eq 0 ]; then
  echo "check-skips.sh: $cases/$cases cases pass"
else
  echo "check-skips.sh: $fails of $cases cases FAILED"
fi
exit $((fails > 0))
