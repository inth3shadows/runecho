#!/usr/bin/env python3
"""Decide the next release version from a commit subject, and roll CHANGELOG.md.

Used by the auto-release job in .github/workflows/release.yml. Lives in a script
rather than inline YAML so it can be run and tested locally — a workflow that
pushes tags is the wrong place to discover a bug in a regex.

Bump level comes from the commit-subject prefix this repo already writes
consistently:

    docs: / chore:      -> no release at all
    guard: / parser:    -> minor
    anything else       -> patch

The tradeoff is deliberate and documented in
~/.claude/plans/runecho-plugin-and-auto-release.md: `guard:` covers both "fixed a
false positive" and "added a check that changes what gets flagged", which deserve
different bumps and are indistinguishable from the subject line. The result is
semver that is mechanically consistent rather than semantically precise, chosen
so that a merged fix can never sit unreleased because nobody remembered to tag.

Exit codes: 0 always (a skip is a normal outcome, not a failure). Errors that
mean "do not release" print skip= and exit 0; errors that mean "the repo is in a
state I do not understand" exit 1 so CI fails loudly rather than tagging wrong.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import re
import subprocess
import sys

# A release tag this project recognises. Deliberately strict: no pre-release or
# build-metadata suffixes, because the release flow has no channel for them and a
# tag it cannot parse must not silently become "highest".
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")

# Subject prefixes, allowing an optional scope: `docs(bench):` matches `docs`.
PREFIX_RE = re.compile(r"^([a-z]+)(\([^)]*\))?:")

NO_RELEASE_PREFIXES = {"docs", "chore"}
MINOR_PREFIXES = {"guard", "parser"}

UNRELEASED_HEADING = "## [Unreleased]"


def highest_tag(tags: list[str]) -> tuple[int, int, int]:
    """Highest parseable vX.Y.Z among tags, as a comparable tuple.

    Sorting is by parsed numeric tuple, never lexically and never by commit date.
    Tags v0.1.1-v0.4.0 are non-monotonic with history (see CHANGELOG.md and #51),
    so anything date-ordered would pick the wrong one.
    """
    parsed = [
        tuple(int(g) for g in m.groups())
        for m in (TAG_RE.match(t.strip()) for t in tags)
        if m
    ]
    if not parsed:
        # No release has ever been cut. Start the sequence rather than guessing.
        return (0, 0, 0)
    return max(parsed)


def bump_for(subject: str) -> str | None:
    """'minor', 'patch', or None when the subject should not cut a release."""
    m = PREFIX_RE.match(subject.strip())
    if not m:
        # No recognised prefix. Patch is the safe default: releasing something
        # small beats a fix silently never shipping, which is the failure this
        # automation exists to remove.
        return "patch"
    prefix = m.group(1)
    if prefix in NO_RELEASE_PREFIXES:
        return None
    if prefix in MINOR_PREFIXES:
        return "minor"
    return "patch"


def next_version(current: tuple[int, int, int], bump: str) -> tuple[int, int, int]:
    major, minor, patch = current
    if bump == "minor":
        return (major, minor + 1, 0)
    return (major, minor, patch + 1)


def roll_changelog(text: str, version: str, date: str, subject: str) -> str:
    """Rename the [Unreleased] section to the new version and open a fresh one.

    If [Unreleased] has no body, the commit subject is written in as the entry:
    an empty release section is worse than a terse one, and refusing to release
    would reintroduce the silent-non-release this automation removes.
    """
    idx = text.find(UNRELEASED_HEADING)
    if idx == -1:
        raise SystemExit(
            f"next-release: CHANGELOG.md has no '{UNRELEASED_HEADING}' heading; "
            "refusing to guess where the release section goes"
        )

    body_start = idx + len(UNRELEASED_HEADING)
    next_heading = text.find("\n## ", body_start)
    body_end = len(text) if next_heading == -1 else next_heading
    body = text[body_start:body_end]

    if not body.strip():
        body = f"\n\n### Changed\n- {subject}\n"

    return (
        text[:idx]
        + f"{UNRELEASED_HEADING}\n\n"
        + f"## [{version}] — {date}"
        + body
        + text[body_end:]
    )


def git_tags() -> list[str]:
    out = subprocess.run(
        ["git", "tag", "--list", "v*"],
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    return out.splitlines()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--subject", required=True, help="commit subject line")
    ap.add_argument("--changelog", default="CHANGELOG.md")
    ap.add_argument("--date", default=None, help="release date (default: today, UTC)")
    ap.add_argument(
        "--apply",
        action="store_true",
        help="rewrite the changelog in place (default: report only)",
    )
    ap.add_argument(
        "--tags",
        default=None,
        help="newline-separated tags to consider instead of reading git (testing)",
    )
    args = ap.parse_args()

    bump = bump_for(args.subject)
    if bump is None:
        print("skip=no-release-prefix")
        return 0

    tags = args.tags.splitlines() if args.tags is not None else git_tags()
    current = highest_tag(tags)
    nxt = next_version(current, bump)
    version = "{}.{}.{}".format(*nxt)

    # Belt and braces against a non-monotonic tag: CI pushes tags server-side and
    # never runs githooks/pre-push (#51), so the hook that normally enforces this
    # locally is not in the path here.
    if nxt <= current:
        print(f"skip=not-monotonic:{version}<=" + "{}.{}.{}".format(*current))
        return 0
    if f"v{version}" in {t.strip() for t in tags}:
        print(f"skip=tag-exists:v{version}")
        return 0

    date = args.date or _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%d")

    if args.apply:
        with open(args.changelog, encoding="utf-8") as fh:
            text = fh.read()
        rolled = roll_changelog(text, version, date, args.subject.strip())
        with open(args.changelog, "w", encoding="utf-8") as fh:
            fh.write(rolled)

    print(f"version={version}")
    print(f"bump={bump}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
