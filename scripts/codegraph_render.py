#!/usr/bin/env python3
"""Render a scoped call graph from a CodeGraph index (.codegraph/codegraph.db) as SVG.

CodeGraph itself ships no visual renderer — this reads its SQLite index directly
and shells out to Graphviz `dot`. Scope is required: the full graph is thousands
of nodes and renders as an unreadable hairball, so you must name a package/dir.

Usage:
    scripts/codegraph_render.py <path-substring> [output.svg]
    scripts/codegraph_render.py internal/snapshot
    scripts/codegraph_render.py internal/snapshot docs/snapshot-callgraph.svg

Requires: `dot` (Graphviz) on PATH, and an up-to-date .codegraph/codegraph.db
(run `codegraph index` or `codegraph sync` first if it's missing or stale).
"""
import argparse
import html
import re
import shutil
import sqlite3
import subprocess
import sys
from pathlib import Path

TEST_FILE_RE = re.compile(r"(_test\.go|\.test\.[jt]sx?|test_.*\.py|_test\.py)$")
MAX_EXTERNAL = 60  # neighbor cap so the boundary cloud stays readable


def find_db(start: Path) -> Path:
    for d in [start, *start.parents]:
        candidate = d / ".codegraph" / "codegraph.db"
        if candidate.exists():
            return candidate
    sys.exit("no .codegraph/codegraph.db found above %s — run `codegraph index` first" % start)


def short_label(qualified_name: str, name: str) -> str:
    label = (qualified_name or name).split("/")[-1]
    return label if len(label) <= 40 else label[:37] + "..."


def node_id(raw_id: str) -> str:
    return '"n%s"' % (abs(hash(raw_id)) % (10**12))


def build_dot(db_path: Path, scope: str) -> tuple[str, dict]:
    con = sqlite3.connect(str(db_path))
    con.row_factory = sqlite3.Row

    rows = con.execute(
        "SELECT id, name, qualified_name, file_path, kind, start_line "
        "FROM nodes WHERE kind IN ('function','method')"
    ).fetchall()
    nodes = {r["id"]: r for r in rows}

    def in_scope(fp):
        return fp and scope in fp and not TEST_FILE_RE.search(fp)

    core = {i for i, r in nodes.items() if in_scope(r["file_path"])}
    if not core:
        sys.exit("no non-test function/method nodes matched %r — check the path substring" % scope)

    calls = con.execute("SELECT source, target FROM edges WHERE kind='calls'").fetchall()

    intra, in_edges, out_edges, ext = set(), set(), set(), set()
    for e in calls:
        s, t = e["source"], e["target"]
        if s in core and t in core:
            intra.add((s, t))
        elif t in core and s in nodes and not TEST_FILE_RE.search(nodes[s]["file_path"] or ""):
            in_edges.add((s, t)); ext.add(s)
        elif s in core and t in nodes and not TEST_FILE_RE.search(nodes[t]["file_path"] or ""):
            out_edges.add((s, t)); ext.add(t)

    truncated = 0
    if len(ext) > MAX_EXTERNAL:
        truncated = len(ext) - MAX_EXTERNAL
        keep = set(list(ext)[:MAX_EXTERNAL])
        in_edges = {(s, t) for s, t in in_edges if s in keep}
        out_edges = {(s, t) for s, t in out_edges if t in keep}
        ext = keep

    by_file = {}
    for i in core:
        fname = nodes[i]["file_path"].rsplit("/", 1)[-1]
        by_file.setdefault(fname, []).append(i)

    palette = ["#eef4ff", "#eafbea", "#fff4e6", "#fdeaf3", "#eefaff", "#f3eeff", "#fbf7e8"]
    out = [
        "digraph callgraph {",
        "  rankdir=LR;",
        '  graph [fontname="Helvetica", bgcolor="white", nodesep=0.25, ranksep=1.1, splines=true];',
        '  node  [fontname="Helvetica", fontsize=10, shape=box, style="rounded,filled", '
        'fillcolor="#eef4ff", color="#4472c4"];',
        '  edge  [fontname="Helvetica", fontsize=8, color="#8896a8", arrowsize=0.7];',
    ]

    for fi, (fname, ids) in enumerate(sorted(by_file.items())):
        fill = palette[fi % len(palette)]
        out.append('  subgraph "cluster_%d" {' % fi)
        out.append(
            '    label="%s"; style="rounded"; color="#c9d3e0"; fontsize=11; fontcolor="#334";'
            % html.escape(fname)
        )
        for i in ids:
            r = nodes[i]
            out.append(
                '    %s [label="%s", fillcolor="%s", tooltip="%s:%s"];'
                % (node_id(i), html.escape(short_label(r["qualified_name"], r["name"])), fill,
                   html.escape(fname), r["start_line"])
            )
        out.append("  }")

    if ext:
        out.append('  subgraph "cluster_ext" {')
        out.append('    label="outside %s"; style="dashed"; color="#bbb"; fontcolor="#888";' % html.escape(scope))
        for i in ext:
            r = nodes[i]
            out.append(
                '    %s [label="%s", style="dashed,filled", fillcolor="#f5f5f5", '
                'color="#bbb", fontcolor="#666", tooltip="%s"];'
                % (node_id(i), html.escape(short_label(r["qualified_name"], r["name"])),
                   html.escape(r["file_path"] or ""))
            )
        out.append("  }")

    for s, t in sorted(intra):
        out.append("  %s -> %s;" % (node_id(s), node_id(t)))
    for s, t in sorted(in_edges):
        out.append('  %s -> %s [style=dashed, color="#c0a0a0"];' % (node_id(s), node_id(t)))
    for s, t in sorted(out_edges):
        out.append('  %s -> %s [style=dashed, color="#a0b0c0"];' % (node_id(s), node_id(t)))

    out.append("}")

    stats = {
        "core": len(core), "intra": len(intra),
        "in_edges": len(in_edges), "out_edges": len(out_edges),
        "ext": len(ext), "truncated": truncated,
    }
    return "\n".join(out), stats


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("scope", help="path substring to scope the graph to, e.g. internal/snapshot")
    ap.add_argument("output", nargs="?", default=None, help="output .svg path (default: <scope-leaf>-callgraph.svg)")
    args = ap.parse_args()

    if shutil.which("dot") is None:
        sys.exit("Graphviz `dot` not found on PATH — install it first (e.g. `brew install graphviz`)")

    db_path = find_db(Path.cwd())
    dot_src, stats = build_dot(db_path, args.scope)

    out_path = Path(args.output) if args.output else Path(args.scope.rstrip("/").rsplit("/", 1)[-1] + "-callgraph.svg")
    out_path.parent.mkdir(parents=True, exist_ok=True)

    proc = subprocess.run(["dot", "-Tsvg", "-o", str(out_path)], input=dot_src, text=True, capture_output=True)
    if proc.returncode != 0:
        sys.exit("dot failed:\n" + proc.stderr)

    print("%s  (%d functions/methods, %d intra-package calls, %d inbound / %d outbound external calls)"
          % (out_path, stats["core"], stats["intra"], stats["in_edges"], stats["out_edges"]))
    if stats["truncated"]:
        print("NOTE: external boundary truncated — %d more callers/callees not shown (cap=%d)"
              % (stats["truncated"], MAX_EXTERNAL))


if __name__ == "__main__":
    main()
