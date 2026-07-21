#!/usr/bin/env python3
"""Tests for scripts/next-release.py — run by the release workflow's validate job.

This logic decides version numbers and pushes tags. A tag is effectively
permanent once goreleaser has published a release against it, so the cost of a
bug here is not "CI is red", it is "the version history is wrong forever". These
tests are cheap insurance against that.

Plain asserts, no test framework: this has to run on a bare ubuntu-latest runner
before any toolchain is set up.
"""

import importlib.util
import pathlib
import sys
import tempfile

_spec = importlib.util.spec_from_file_location(
    "next_release", pathlib.Path(__file__).with_name("next-release.py")
)
nr = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(nr)

FAILURES = []


def check(label, got, want):
    if got != want:
        FAILURES.append(f"{label}: got {got!r}, want {want!r}")


# --- bump level from the commit subject -------------------------------------
check("guard: -> minor", nr.bump_for("guard: fix a thing"), "minor")
check("parser: -> minor", nr.bump_for("parser: shell heredocs"), "minor")
check("docs: -> no release", nr.bump_for("docs: typo"), None)
check("chore: -> no release", nr.bump_for("chore: bump dep"), None)
# A scope in parentheses must not defeat the prefix match — `docs(bench):` is
# still docs, and this repo writes that form.
check("docs(scope): -> no release", nr.bump_for("docs(bench): caveat"), None)
check("guard(x): -> minor", nr.bump_for("guard(js): fix"), "minor")
check("unknown prefix -> patch", nr.bump_for("plugin: ship it"), "patch")
check("no prefix -> patch", nr.bump_for("just some words"), "patch")
# Leading whitespace must not change the decision.
check("leading space", nr.bump_for("  docs: typo"), None)
# A capitalised prefix is NOT this repo's convention and must not silently read
# as docs — patch is the safe fallback.
check("Docs: -> patch (not a match)", nr.bump_for("Docs: typo"), "patch")

# --- highest tag ------------------------------------------------------------
# The real tag set: v0.1.1-v0.4.0 are non-monotonic with history (#51), so any
# date- or lexical ordering picks wrong. Must be numeric.
real = ["v0.1.1", "v0.3.0", "v0.4.0", "v0.5.0", "v0.6.0", "v0.7.0"]
check("highest of real tags", nr.highest_tag(real), (0, 7, 0))
check("lexical trap v0.10.0 > v0.9.0", nr.highest_tag(["v0.9.0", "v0.10.0"]), (0, 10, 0))
check("no tags", nr.highest_tag([]), (0, 0, 0))
check("no parseable tags", nr.highest_tag(["nightly", "v1.2", "release-1"]), (0, 0, 0))
check("suffixed tags ignored", nr.highest_tag(["v1.0.0", "v2.0.0-rc1"]), (1, 0, 0))
check("whitespace tolerated", nr.highest_tag([" v1.2.3 \n"]), (1, 2, 3))

# --- version arithmetic -----------------------------------------------------
check("minor zeroes patch", nr.next_version((0, 7, 3), "minor"), (0, 8, 0))
check("patch increments", nr.next_version((0, 7, 3), "patch"), (0, 7, 4))
check("minor from zero", nr.next_version((0, 0, 0), "minor"), (0, 1, 0))

# --- changelog rolling ------------------------------------------------------
CL = (
    "# Changelog\n\nintro prose\n\n## [Unreleased]\n\n### Fixed\n- a real fix\n\n"
    "## [0.7.0] — 2026-07-12\n\n### Added\n- old thing\n"
)
out = nr.roll_changelog(CL, "0.8.0", "2026-07-21", "guard: x")
check("fresh Unreleased kept", out.count("## [Unreleased]"), 1)
check("new version heading", "## [0.8.0] — 2026-07-21" in out, True)
check("body moved under new version", "- a real fix" in out.split("## [0.7.0]")[0], True)
check("prior release untouched", "## [0.7.0] — 2026-07-12" in out, True)
check("intro preserved", out.startswith("# Changelog\n\nintro prose"), True)
# Ordering matters: newest release must sit above the previous one.
check(
    "new release above old",
    out.index("## [0.8.0]") < out.index("## [0.7.0]"),
    True,
)

# Empty [Unreleased] falls back to the commit subject rather than publishing an
# empty section or refusing to release.
EMPTY = "# Changelog\n\n## [Unreleased]\n\n## [0.7.0] — 2026-07-12\n\n- old\n"
out2 = nr.roll_changelog(EMPTY, "0.7.1", "2026-07-21", "plugin: ship it")
check("empty section gets subject", "- plugin: ship it" in out2, True)
check("empty section gets a heading", "### Changed" in out2, True)

# [Unreleased] as the final section (no following heading) must still roll.
TAIL = "# Changelog\n\n## [Unreleased]\n\n### Fixed\n- only entry\n"
out3 = nr.roll_changelog(TAIL, "0.1.0", "2026-07-21", "guard: x")
check("trailing section rolls", "## [0.1.0] — 2026-07-21" in out3, True)
check("trailing body preserved", "- only entry" in out3, True)

# A changelog with no [Unreleased] heading must fail loudly, not guess.
try:
    nr.roll_changelog("# Changelog\n\n## [0.7.0] — x\n", "0.8.0", "d", "s")
    FAILURES.append("missing [Unreleased]: expected SystemExit, got none")
except SystemExit:
    pass

# --- end to end through main() ---------------------------------------------
with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
    fh.write(CL)
    tmp = fh.name

sys.argv = [
    "next-release.py",
    "--subject", "guard: a fix",
    "--tags", "\n".join(real),
    "--date", "2026-07-21",
    "--changelog", tmp,
    "--apply",
]
nr.main()
check("e2e rewrote changelog", "## [0.8.0] — 2026-07-21" in open(tmp).read(), True)

if FAILURES:
    print("next-release self-test FAILED:")
    for f in FAILURES:
        print("  -", f)
    sys.exit(1)
print("next-release self-test: all checks passed")
