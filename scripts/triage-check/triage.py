#!/usr/bin/env python3
"""triage-check — measure whether a candidate guard check earns its place.

PROTOTYPE. Codifies Gate 4 of docs/check-worthiness.md: run a deterministic
detector for a candidate defect class over real repos, split its findings into
hand-written source vs generated output, and apply the pre-registered decision
rule. This is the reproducible form of the hand-run investigation in #204.

It deliberately does NOT decide for you. It produces the finding table and the
mechanical part of the verdict (finding counts, source/generated split, the FP
rate you enter after classifying). The genuine-defect classification is a human
step by design — that judgement is the whole point of the gate, and automating it
would reintroduce the "sounds plausible" failure this exists to prevent.

Usage:
    triage.py run   --detector <name> --repos <dir>[,<dir>...] [--out report.json]
    triage.py verdict <report.json> [--genuine N]

`run` invokes a detector plugin (see detectors/) over each repo and records raw
findings. `verdict` applies the fixed bar once you have hand-classified how many
findings are genuine defects.

Not shipped in the release binaries — a maintainer tool. No dependency on the Go
build; detectors shell out to whatever real linter they front.
"""

import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))

# Path fragments that mark generated / vendored output. A finding here is not a
# defect in code anyone wrote — it is noise from a build artifact — and counting
# it as either a hit or a false positive would corrupt the rate. Split, don't drop:
# the generated count is reported so a detector that ONLY fires on build output
# (a real signal it is mis-scoped) is visible rather than silently zero.
GENERATED_MARKERS = (
    "/node_modules/", "/dist/", "/build/", "/vendor/", "/.venv/",
    "/_astro/", "/coverage/", "/immutable/", "/.next/", "/out/", "/target/",
)

# The Gate 4 bar, from docs/check-worthiness.md. Kept here as data so the verdict
# is computed against the SAME numbers the rubric documents, not a re-typed guess.
MIN_GENUINE_DEFECTS = 3
MIN_REPOS_WITH_DEFECTS = 2
# The FP-rate ceiling is intentionally not a single number: an ask-posture gate
# tolerates more noise than a blocking one. The verdict reports the rate and the
# bar's intent; the human sets the acceptable ceiling per the check's posture.


def _is_generated(path: str) -> bool:
    return any(m in path for m in GENERATED_MARKERS)


def run_detector(detector: str, repo: str) -> list[dict]:
    """Invoke a detector plugin and return its normalized findings.

    A detector is an executable at detectors/<name> that takes one repo path as
    argv[1] and prints JSON to stdout: a list of {"file","line","symbol"}. Keeping
    the detector out-of-process is what lets a Go check, a stylelint run, and an
    eslint run all be triaged by the same harness without this file importing any
    of them.
    """
    plugin = os.path.join(HERE, "detectors", detector)
    if not os.access(plugin, os.X_OK):
        sys.exit(f"triage: no executable detector at {plugin} "
                 f"(available: {', '.join(_list_detectors()) or 'none'})")
    proc = subprocess.run([plugin, repo], capture_output=True, text=True)
    if proc.returncode not in (0, 1):
        # A linter returns 1 when it finds lint (expected here) and 0 when clean;
        # anything else is the detector itself failing, not a finding.
        sys.stderr.write(proc.stderr)
        sys.exit(f"triage: detector {detector} failed on {repo} "
                 f"(exit {proc.returncode})")
    try:
        return json.loads(proc.stdout or "[]")
    except json.JSONDecodeError:
        sys.exit(f"triage: detector {detector} did not emit JSON for {repo}:\n"
                 f"{proc.stdout[:400]}")


def _list_detectors() -> list[str]:
    d = os.path.join(HERE, "detectors")
    if not os.path.isdir(d):
        return []
    return sorted(f for f in os.listdir(d)
                  if os.access(os.path.join(d, f), os.X_OK))


def cmd_run(args) -> int:
    repos = [r for r in args.repos.split(",") if r]
    report = {"detector": args.detector, "repos": []}
    missing = 0
    print(f"{'repo':<24}{'src':>6}{'generated':>11}")
    for repo in repos:
        repo = os.path.abspath(os.path.expanduser(repo))
        if not os.path.isdir(repo):
            print(f"{os.path.basename(repo):<24}  MISSING", file=sys.stderr)
            missing += 1
            continue
        findings = run_detector(args.detector, repo)
        src = [f for f in findings if not _is_generated(f.get("file", ""))]
        gen = [f for f in findings if _is_generated(f.get("file", ""))]
        report["repos"].append({
            "repo": repo,
            "name": os.path.basename(repo.rstrip("/")),
            # Only source findings need hand-classification; each carries a
            # genuine=null the human fills in. Generated findings are recorded
            # but not classified — they never count toward the bar.
            "source_findings": [dict(f, genuine=None) for f in src],
            "generated_count": len(gen),
        })
        print(f"{os.path.basename(repo.rstrip('/')):<24}{len(src):>6}{len(gen):>11}")

    # If nothing was actually scanned, refuse to write a report and exit nonzero.
    # A zero-repo report would later read as "clean, nothing found" — the exact
    # false negative the harness exists to prevent, here caused by a bad --repos
    # path rather than a real result.
    if not report["repos"]:
        print(f"triage: no repos scanned ({missing} missing) — nothing to report",
              file=sys.stderr)
        return 1

    out = args.out or "triage-report.json"
    with open(out, "w") as fh:
        json.dump(report, fh, indent=2)
    total_src = sum(len(r["source_findings"]) for r in report["repos"])
    print(f"\n{total_src} source finding(s) across {len(report['repos'])} repo(s) "
          f"→ {out}")
    if missing:
        print(f"({missing} repo path(s) were missing and skipped)")
    print("Next: hand-classify each source finding's \"genuine\" field "
          "(true/false), then: triage.py verdict " + out)
    return 0


def cmd_verdict(args) -> int:
    with open(args.report) as fh:
        report = json.load(fh)

    classified, unclassified, genuine = 0, 0, 0
    repos_with_defect = set()
    for r in report["repos"]:
        for f in r["source_findings"]:
            g = f.get("genuine")
            if g is None:
                unclassified += 1
                continue
            classified += 1
            if g:
                genuine += 1
                repos_with_defect.add(r["name"])

    # --genuine lets a run whose report wasn't edited in-place still get a verdict
    # from hand-tallied counts, so the tool is usable before the JSON round-trip
    # is wired up. It overrides only the genuine count, not the FP arithmetic.
    if args.genuine is not None:
        genuine = args.genuine

    total_src = sum(len(r["source_findings"]) for r in report["repos"])
    fp = classified - genuine if args.genuine is None else total_src - genuine
    fp_rate = (fp / total_src) if total_src else 0.0

    print(f"detector:            {report['detector']}")
    print(f"source findings:     {total_src}")
    if unclassified and args.genuine is None:
        print(f"UNCLASSIFIED:        {unclassified} "
              f"(set each finding's \"genuine\" field, or pass --genuine N)")
    print(f"genuine defects:     {genuine}")
    print(f"repos with a defect: {len(repos_with_defect) or '?'}")
    # A partially-classified report has no meaningful FP rate yet — every
    # not-yet-judged finding is neither hit nor miss. Printing 0% there would
    # read as "no false positives," the opposite of "not measured."
    if unclassified and args.genuine is None:
        print("false-positive rate: not measured "
              f"({unclassified} finding(s) still unclassified)")
    else:
        print(f"false-positive rate: {fp_rate:.0%} ({fp}/{total_src})")

    meets_count = genuine >= MIN_GENUINE_DEFECTS
    # repos_with_defect is only known when the JSON was classified in-place;
    # --genuine N gives a count without a per-repo breakdown, so spread is unknown.
    meets_spread = (len(repos_with_defect) >= MIN_REPOS_WITH_DEFECTS
                    if args.genuine is None else None)

    print()
    print(f"bar: ≥{MIN_GENUINE_DEFECTS} genuine defects across "
          f"≥{MIN_REPOS_WITH_DEFECTS} repos, AND an FP rate an ask-gate tolerates")
    if not meets_count:
        print(f"VERDICT: DECLINE — {genuine} genuine defect(s), "
              f"below the {MIN_GENUINE_DEFECTS} bar. Record the numbers, ship "
              f"nothing (see docs/check-worthiness.md Gate 4).")
        return 0
    if meets_spread is False:
        print(f"VERDICT: DECLINE — defects in {len(repos_with_defect)} repo(s), "
              f"below the {MIN_REPOS_WITH_DEFECTS}-repo spread bar.")
        return 0
    spread_note = ("" if meets_spread
                   else " (spread unverified — pass a classified report, not "
                        "--genuine, to confirm ≥2 repos)")
    print(f"VERDICT: PROCEED to a scoped spike{spread_note} — IF the "
          f"{fp_rate:.0%} FP rate is one an ask-posture gate tolerates for this "
          f"check. That last judgement is yours; the bar cannot set it (an "
          f"ask-gate tolerates more noise than a blocking one).")
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    pr = sub.add_parser("run", help="run a detector over repos, record findings")
    pr.add_argument("--detector", required=True,
                    help=f"detector plugin name ({', '.join(_list_detectors()) or 'none installed'})")
    pr.add_argument("--repos", required=True, help="comma-separated repo paths")
    pr.add_argument("--out", help="report path (default triage-report.json)")
    pr.set_defaults(fn=cmd_run)

    pv = sub.add_parser("verdict", help="apply the Gate 4 bar to a report")
    pv.add_argument("report")
    pv.add_argument("--genuine", type=int,
                    help="hand-tallied genuine-defect count (skips reading the "
                         "per-finding genuine fields)")
    pv.set_defaults(fn=cmd_verdict)

    args = p.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
