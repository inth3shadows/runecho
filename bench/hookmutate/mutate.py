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
TEST_PKG = "./cmd/runecho-guard/"
TEST_ARGS = ["go", "test", TEST_PKG, "-run", "TestHookCorpus", "-json"]

# Appended to a real source file to prove a break in the sandbox reaches the
# tests. Deliberately not valid Go, and deliberately not valid anything else
# either, so the same canary works if this harness is lifted to another language.
CANARY = "\n@@@ hookmutate liveness canary -- not valid source in any language @@@\n"

SANDBOX = None

# Set on the canary failure path only. That abort tells the operator to go and
# look at the sandbox -- stale build cache, wrong working directory, editable
# install -- so removing it would delete the evidence the message asks for.
KEEP_SANDBOX = False


def git(*args):
    return subprocess.run(["git", *args], cwd=REPO, capture_output=True, text=True)


def sandbox_path(rel):
    """Resolve a repo-relative path inside the sandbox, or refuse to hand one back.

    `os.path.join(SANDBOX, rel)` silently discards the sandbox prefix when `rel`
    is absolute, and `../` walks out of it. Either one writes the mutant into the
    live working tree for the length of a test run -- and the `finally` restores
    it, so the end-of-run `git status` check sees a clean tree and never fires.
    That is the exact failure the sandbox exists to prevent, reachable by pasting
    an editor's "Copy path" into the catalog, so containment is structural here
    rather than remembered at each write site.
    """
    root = os.path.realpath(SANDBOX)
    p = os.path.realpath(os.path.join(root, rel))
    if os.path.commonpath([root, p]) != root:
        sys.exit(f"INTERNAL: {rel!r} resolves outside the sandbox — refusing to write")
    return p


def _cleanup():
    global SANDBOX
    if SANDBOX and not KEEP_SANDBOX:
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
    global SANDBOX
    # Publish the path before anything else can fail. Archive + extract measured
    # ~186ms, and a signal in that window -- or a raise out of extractall -- would
    # otherwise leave _cleanup() and _on_signal with nothing to remove, stranding
    # several MB in /tmp. atexit is the single owner from here on, which is why
    # the git-archive failure branch below no longer rmtree's by hand.
    SANDBOX = tempfile.mkdtemp(prefix="hookmutate-")
    p = subprocess.run(["git", "archive", "--format=tar", "HEAD"], cwd=REPO, capture_output=True)
    if p.returncode != 0:
        sys.exit("git archive HEAD failed:\n" + p.stderr.decode(errors="replace"))
    # `filter` landed in 3.12; these are our own tracked files either way.
    kwargs = {"filter": "data"} if sys.version_info >= (3, 12) else {}
    with tarfile.open(fileobj=io.BytesIO(p.stdout)) as tf:
        tf.extractall(SANDBOX, **kwargs)


def fixture_names():
    """Every fixture in the corpus, as the subtest names TestHookCorpus emits.

    os.listdir and not glob, deliberately: glob runs the WHOLE path through
    fnmatch, so a `[`, `*` or `?` anywhere in the sandbox root -- which is
    $TMPDIR-derived, and $TMPDIR is not ours to constrain -- returns [] from a
    directory that plainly holds the fixtures. Measured: TMPDIR='br[a]nch' gives
    listdir=['a.json'] glob=[]. That reads as "no fixtures", which empties `dead`
    and switches off the dead-fixture half of --strict -- the half #227 was filed
    about -- leaving a header saying `0 fixtures` as the only trace. The abort
    below is what stops the next variant of this rather than this one.
    """
    d = os.path.join(SANDBOX, CORPUS_REL)
    files = [f for f in os.listdir(d) if f.endswith(".json")] if os.path.isdir(d) else []
    names = []
    for f in sorted(files):
        for case in json.load(open(os.path.join(d, f))):
            names.append(case["name"])
    if not names:
        sys.exit(f"no fixtures found under {d} — the dead-fixture half of --strict would pass vacuously")
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
        # os.path.join(SANDBOX, file) drops the sandbox prefix for an absolute
        # path, and `../` walks out of it -- either one puts the mutant in the
        # live working tree for the length of a test run. sandbox_path() refuses
        # too; this gate is the one that says so at authoring time, before the
        # dirty check, which is when the catalog is actually being written.
        if os.path.isabs(m["file"]) or os.path.normpath(m["file"]).startswith(".."):
            bad.append(f"{m['id']}: file {m['file']!r} is not a repo-relative path — "
                       "os.path.join would write the mutant outside the sandbox, into the working tree")
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
    global KEEP_SANDBOX
    path = sandbox_path(rel_path)
    src = open(path).read()
    try:
        open(path, "w").write(src + CANARY)
        registered = run_corpus() is None
    finally:
        open(path, "w").write(src)
    if not registered:
        # Every cause named below is diagnosed by looking at the sandbox, so the
        # sandbox has to outlive this abort -- otherwise the message sends the
        # operator to a directory atexit just deleted, and in CI there is nothing
        # left to inspect at all.
        KEEP_SANDBOX = True
        sys.exit(
            f"liveness canary did not fire: a deliberate syntax error in {rel_path}\n"
            "left the build green, so this run would score a tree it is not mutating.\n"
            "Causes: a stale build cache, the wrong working directory, or -- in a\n"
            "Python consumer -- stale .pyc or an editable install resolving the\n"
            "package back to the original tree.\n"
            f"Sandbox left in place for inspection: {SANDBOX}\n"
            f"Delete it when done: rm -rf {SANDBOX}"
        )


def assert_build_graph(muts):
    """Require every catalog file's DIRECTORY to sit in the graph the canary proved live.

    Directory, not file: a file excluded from its own package by a `//go:build`
    constraint sits in a live directory and still reports a false SURVIVED. This
    check does not close that; nothing in the catalog carries a build tag today.

    canary() breaks ONE file, so what it proves is narrower than it looks: the
    compilation unit holding the first catalog entry is reached by the tests. It
    says nothing about the rest of the catalog. Where a catalog spans packages
    that resolve independently -- a second checkout on the path, a vendored copy,
    an editable install covering part of the tree -- the canary can pass while a
    second package still resolves to the original tree, and every mutation in
    that package reports a false SURVIVED whatever it does.

    `-test` is load-bearing: the canary proves the TEST binary's graph is live,
    and that is a strict superset of the plain build's. Without it, a package
    reached only from a _test.go file reads as outside the graph and the abort
    below tells the author the tests never build it, which is false. Today that
    is internal/guardstats and internal/hookwiring.

    Asking the toolchain costs 0.3s warm, against ~0.9s for a second `go test`
    canary (a canary is a build break, so it never links or runs) -- both
    measured. The saving is small; naming the offending package is the point.
    """
    p = subprocess.run(["go", "list", "-test", "-deps", "-f", "{{.Dir}}", TEST_PKG],
                       cwd=SANDBOX, capture_output=True, text=True)
    if p.returncode != 0:
        sys.exit(f"go list -test -deps {TEST_PKG} failed, so the catalog could not be checked\n"
                 "against the build graph the canary proved live:\n" + p.stderr)
    live = {os.path.realpath(line.strip()) for line in p.stdout.splitlines() if line.strip()}
    outside = sorted({os.path.dirname(m["file"]) for m in muts
                      if os.path.dirname(sandbox_path(m["file"])) not in live})
    if outside:
        sys.exit(
            f"catalog files live outside the build graph of {TEST_PKG}:\n  "
            + "\n  ".join(outside)
            + f"\nThe liveness canary covered {os.path.dirname(muts[0]['file'])} only. A mutation in a\n"
              "package the tests never build reports SURVIVED whatever it does.\n"
              "Give each such package its own canary, or drop its entries."
        )


def main():
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
    # SIGABRT is raised at the C level and a Python-level handler does not
    # reliably run for it, so it is deliberately not in this set.
    for signame in ("SIGTERM", "SIGINT", "SIGHUP", "SIGQUIT"):
        if hasattr(signal, signame):
            signal.signal(getattr(signal, signame), _on_signal)
    sandbox()

    baseline = run_corpus()
    if baseline is None:
        sys.exit("baseline build failed — fix the tree before scoring")
    if baseline:
        sys.exit(f"baseline is already failing: {sorted(baseline)}")
    canary(muts[0]["file"])
    assert_build_graph(muts)

    killed_by, broken = {}, []
    for m in muts:
        path = sandbox_path(m["file"])
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
