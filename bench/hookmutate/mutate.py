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

Mutations are applied to the working tree and restored immediately, including
on error or interrupt. It refuses to start if the tree is already dirty, so a
crash can never be confused with your own edits.
"""

import argparse
import glob
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
CORPUS = os.path.join(REPO, "cmd", "runecho-guard", "testdata", "hookcorpus")
TEST_ARGS = ["go", "test", "./cmd/runecho-guard/", "-run", "TestHookCorpus", "-json"]


def git(*args):
    return subprocess.run(["git", *args], cwd=REPO, capture_output=True, text=True)


def fixture_names():
    """Every fixture in the corpus, as the subtest names TestHookCorpus emits."""
    names = []
    for f in sorted(glob.glob(os.path.join(CORPUS, "*.json"))):
        for case in json.load(open(f)):
            names.append(case["name"])
    return names


def run_corpus():
    """Return the set of failing fixture names, or None if the build broke."""
    p = subprocess.run(TEST_ARGS, cwd=REPO, capture_output=True, text=True)
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--strict", action="store_true",
                    help="exit 1 if a fixture caught nothing or a survivor is unclaimed")
    ap.add_argument("--only", default="", help="substring filter on mutation id")
    args = ap.parse_args()

    if git("status", "--porcelain").stdout.strip():
        sys.exit("refusing to run: working tree is dirty — mutations are applied in place")

    catalog = json.load(open(os.path.join(HERE, "mutations.json")))
    muts = [m for m in catalog["mutations"] if args.only in m["id"]]

    baseline = run_corpus()
    if baseline is None:
        sys.exit("baseline build failed — fix the tree before scoring")
    if baseline:
        sys.exit(f"baseline is already failing: {sorted(baseline)}")

    killed_by, broken = {}, []
    for m in muts:
        path = os.path.join(REPO, m["file"])
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
        sys.exit(f"INTERNAL: tree left dirty after restore:\n{dirty}")

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
