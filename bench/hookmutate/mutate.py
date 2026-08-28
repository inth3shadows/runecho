#!/usr/bin/env python3
"""Mutation-score the hook-level guard fixtures.

The hook corpus (cmd/runecho-guard/testdata/hookcorpus/*.json) is the only
instrument that reaches eight of the guard's nine checks. A fixture in it is worth
keeping only if some defect can make it fail; one that no mutation kills is a
claim of coverage the corpus does not have, which is the exact failure #227 was
filed about.

This script breaks one behaviour of one check at a time (the catalog lives in
mutations.json, next to this file), replays the corpus, and records which
fixtures noticed. It reports two things the fixtures themselves cannot say:

  * fixtures that caught nothing  -> delete them, or replace them with a case
    that pins a behaviour the set is missing
  * mutations that nothing caught -> a real behaviour with no fixture, or an
    equivalent mutant worth recording as such

THE RULE THAT MATTERS: a mutation must be no coarser than the claim it is used
to support. Deleting a whole lookup that several inputs feed will happily
"confirm" a fixture that pins only one of them. That error was made twice while
building this corpus, and both times it certified a vacuous fixture as verified.
If a fixture claims to pin X, mutate X alone.

Usage:
    python3 bench/hookmutate/mutate.py            # report
    python3 bench/hookmutate/mutate.py --strict   # exit 1 on a dead fixture
                                                  # or an unclaimed survivor

Mutations are applied inside a disposable sandbox (`git archive HEAD` into a
temp dir), never to your working tree — see sandbox() for why that is a
correctness requirement and not just tidiness. The tree must still be clean to
start, because the sandbox is HEAD: uncommitted edits would go unscored.
"""

import argparse
import atexit
import glob
import io
import json
import os
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
CORPUS_REL = os.path.join("cmd", "runecho-guard", "testdata", "hookcorpus")
TEST_ARGS = ["go", "test", "./cmd/runecho-guard/", "-run", "TestHookCorpus", "-json"]

# Appended to a real source file to prove a break in the sandbox reaches the
# tests. Deliberately not valid Go, and deliberately not valid anything else
# either, so the same canary works if this harness is lifted to another language.
CANARY = "\n@@@ hookmutate liveness canary -- not valid source in any language @@@\n"

SANDBOX = None


def git(*args, cwd=REPO):
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)


def _cleanup():
    global SANDBOX
    if SANDBOX:
        shutil.rmtree(SANDBOX, ignore_errors=True)
        SANDBOX = None


def _on_signal(signum, _frame):
    """Restore the default disposition and re-raise, so exit status still reports the signal.

    try/finally does not cover SIGTERM, which is what a `timeout` wrapper or a CI
    step budget sends. Since the mutant lives in the sandbox, a missed signal
    costs a leaked temp dir rather than a mutated file in someone's repo — but
    only when the signal reaches *this* process. `timeout ... uv run python
    mutate.py` does not: the wrapper absorbs it. See the README on run cost.
    """
    _cleanup()
    signal.signal(signum, signal.SIG_DFL)
    os.kill(os.getpid(), signum)


def sandbox():
    """Materialise HEAD into a temp dir, and score there.

    Mutating the working tree is not safe even with a faithful restore: anything
    else reading the repo during the run -- a second agent, a review pass, an
    editor -- sees the mutant and diagnoses it as a code defect. The end-of-run
    `git status` check cannot see that, because the tree is clean at every
    quiescent moment. This has now happened in two separate repos.
    """
    d = tempfile.mkdtemp(prefix="hookmutate-")
    p = subprocess.run(["git", "archive", "--format=tar", "HEAD"], cwd=REPO, capture_output=True)
    if p.returncode != 0:
        shutil.rmtree(d, ignore_errors=True)
        sys.exit("git archive HEAD failed:\n" + p.stderr.decode(errors="replace"))
    # `filter` landed in 3.12; these are our own tracked files either way.
    kwargs = {"filter": "data"} if sys.version_info >= (3, 12) else {}
    with tarfile.open(fileobj=io.BytesIO(p.stdout)) as tf:
        tf.extractall(d, **kwargs)
    return d


def fixture_names():
    """Every fixture in the corpus, as the subtest names TestHookCorpus emits."""
    names = []
    for f in sorted(glob.glob(os.path.join(SANDBOX, CORPUS_REL, "*.json"))):
        for case in json.load(open(f)):
            names.append(case["name"])
    return names


def run_corpus():
    """Return the set of failing fixture names, or None if the build broke."""
    p = subprocess.run(TEST_ARGS, cwd=SANDBOX, capture_output=True, text=True)
    failed, saw_test = set(), False
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        name = ev.get("Test", "")
        if name:
            saw_test = True
        # Only the leaf subtest (TestHookCorpus/<fixture>) names a fixture.
        if ev.get("Action") == "fail" and name.count("/") == 1:
            failed.add(name.split("/", 1)[1])
    if not saw_test and p.returncode != 0:
        return None  # compile error: the mutation did not produce valid Go
    return failed


def load_catalog():
    """Read mutations.json, rejecting entries that would score as a false survivor."""
    catalog = json.load(open(os.path.join(HERE, "mutations.json")))
    muts = catalog["mutations"]
    seen, bad = set(), []
    for m in muts:
        # A no-op entry applies cleanly, changes nothing, and is reported
        # SURVIVED -- indistinguishable from a real hole. The `find` occurs-once
        # drift check below cannot see it, because it does occur once.
        if m["find"] == m["replace"]:
            bad.append(f"{m['id']}: find == replace -- applies cleanly, changes nothing, reads as SURVIVED")
        if m["id"] in seen:
            bad.append(f"{m['id']}: duplicate id -- the later entry overwrites the earlier one in the report")
        seen.add(m["id"])
    if bad:
        sys.exit("catalog is invalid:\n  " + "\n  ".join(bad))
    return muts


def canary(rel_path):
    """Prove a break in the sandbox actually reaches the tests before scoring anything.

    Generalises the "is the package imported from the tree under test?" check.
    A deliberate syntax error MUST break the build. If it does not, the run is
    scoring something other than what it is mutating -- a stale build cache, the
    wrong working directory, or, in a Python consumer, stale .pyc (a syntax error
    is never seen when the source is not re-parsed) or an editable install
    resolving the package back to the original tree.
    """
    path = os.path.join(SANDBOX, rel_path)
    src = open(path).read()
    try:
        open(path, "w").write(src + CANARY)
        registered = run_corpus() is None
    finally:
        open(path, "w").write(src)
    if not registered:
        sys.exit(
            f"liveness canary did not fire: a deliberate syntax error in {rel_path}\n"
            "left the build green, so this run would score a tree it is not mutating.\n"
            "Causes: a stale build cache, the wrong working directory, or -- in a\n"
            "Python consumer -- stale .pyc or an editable install resolving the\n"
            "package back to the original tree."
        )


def main():
    global SANDBOX
    ap = argparse.ArgumentParser()
    ap.add_argument("--strict", action="store_true",
                    help="exit 1 if a fixture caught nothing or a survivor is unclaimed")
    ap.add_argument("--only", default="", help="substring filter on mutation id")
    args = ap.parse_args()

    # Validate the catalog before the dirty check: authoring a mutation is
    # exactly when you have a dirty tree, and "your tree is dirty" is a useless
    # answer to "this entry is a no-op".
    catalog = load_catalog()
    muts = [m for m in catalog if args.only in m["id"]]
    if not muts:
        sys.exit(f"no mutation id matches --only {args.only!r}")

    # The sandbox is HEAD, so uncommitted edits would be silently unscored --
    # a wrong answer in the other direction. Commit first.
    if git("status", "--porcelain").stdout.strip():
        sys.exit("refusing to run: working tree is dirty — the sandbox is HEAD, so your edits would go unscored")

    atexit.register(_cleanup)
    for signame in ("SIGTERM", "SIGINT", "SIGHUP"):
        if hasattr(signal, signame):
            signal.signal(getattr(signal, signame), _on_signal)
    SANDBOX = sandbox()

    baseline = run_corpus()
    if baseline is None:
        sys.exit("baseline build failed — fix the tree before scoring")
    if baseline:
        sys.exit(f"baseline is already failing: {sorted(baseline)}")
    canary(muts[0]["file"])

    killed_by, broken = {}, []
    for m in muts:
        path = os.path.join(SANDBOX, m["file"])
        src = open(path).read()
        if src.count(m["find"]) != 1:
            broken.append((m["id"], f"pattern matches {src.count(m['find'])} times — catalog drifted from the code"))
            continue
        try:
            open(path, "w").write(src.replace(m["find"], m["replace"], 1))
            failed = run_corpus()
        finally:
            open(path, "w").write(src)
        if failed is None:
            broken.append((m["id"], "mutation does not compile — rewrite it to keep the file valid Go"))
            continue
        killed_by[m["id"]] = sorted(failed)

    dirty = git("status", "--porcelain").stdout.strip()
    if dirty:
        sys.exit(f"INTERNAL: working tree dirty after a sandboxed run — a path escaped the sandbox:\n{dirty}")

    by_id = {m["id"]: m for m in muts}
    caught, survivors = {}, []
    print(f"{len(muts)} mutations, {len(fixture_names())} fixtures\n")
    for mid, killers in killed_by.items():
        print(f"  {mid:34s} {'|'.join(killers) if killers else 'SURVIVED'}")
        for k in killers:
            caught.setdefault(k, []).append(mid)
        if not killers:
            survivors.append(mid)

    dead = [n for n in fixture_names() if n not in caught]
    if dead:
        print("\nFixtures that caught nothing (delete, or replace with a case that pins a gap):")
        for n in dead:
            print(f"  {n}")
    unclaimed = [s for s in survivors if not by_id[s].get("known_gap")]
    if survivors:
        print("\nMutations nothing caught:")
        for s in survivors:
            note = by_id[s].get("known_gap")
            print(f"  {s}{'  [known gap: ' + note + ']' if note else '  <- UNCLAIMED'}")
    if broken:
        print("\nCatalog entries that could not be applied:")
        for mid, why in broken:
            print(f"  {mid}: {why}")

    if args.strict and (dead or unclaimed or broken):
        sys.exit(1)


if __name__ == "__main__":
    main()
