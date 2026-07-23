# Competitive landscape — keeping the moat claim defensible

**Last verified 2026-07-23.** Next re-verification due: 2026-10-23 (quarterly).

This file exists to stop one sentence in [`runecho-vs-field.html`](./runecho-vs-field.html)
from silently going stale:

> No rival deterministically blocks an undefined symbol pre-write.

That is a claim about the *field*, not about RunEcho, so it decays without anyone
touching this repo. If it ever stops being true, the honest move is to update the
positioning — not to leave the sentence standing.

## The claim, stated precisely

A rival falsifies it only by satisfying **all four** conjuncts at once:

1. **Pre-write** — intervenes *before* the edit lands on disk, inside the agent loop.
2. **Blocking** — can actually deny the write, not merely advise, warn, or inject
   context the model is free to ignore.
3. **Symbol-existence aware** — decides on whether the referenced symbol *exists*,
   not on whether the agent performed some ritual first.
4. **Deterministic / model-free** — the verdict comes from parsing, not from a
   second model's judgement.

Anything that misses one of the four is *adjacent*, not a rival, and belongs in
the watch list rather than in the positioning.

## Survey — 2026-07-23

| Tool | What it actually does | Fires | Falsifies the claim? |
|---|---|---|---|
| **GateGuard** (`gateguard-ai`, zunoworks) | "deny + force investigation + demand evidence" — blocks the first write attempt and makes the agent produce facts (importers, data schemas, quoted instruction) before allowing a retry. Now at v0.5.0 (PyPI, released 2026-04-24; up from v0.4.1) — adds condensed fact-force denials for long sessions on top of the earlier opt-in bughunt gate. Behavior re-confirmed this pass directly from the PyPI project page: it still demands evidence-gathering (importers, schemas, instruction quotes via grep/read), never symbol existence. | Pre-write (PreToolUse) | **No.** Nearest neighbour on *timing*, but it never evaluates symbol existence — it evaluates whether the agent looked. Conjunct 3 fails, unchanged by the version bump. |
| **Cursor hooks** | Hook surface enumerated in the docs: `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt` can allow/deny; `afterFileEdit` / `afterTabFileEdit` are observational only. Re-checked this pass against <https://cursor.com/docs/hooks.md> directly: still no `beforeFileEdit` or equivalent pre-edit-for-native-edits event. | `beforeReadFile` pre-read; native edits **post-write only** | **No — and structurally can't today.** There is no `beforeFileEdit` event, so an edit made with Cursor's *native* edit tool cannot be gated before it lands. (Also relevant to issue #174: a Cursor port could only cover edits routed through shell or an MCP filesystem server, via `beforeShellExecution` / `beforeMCPExecution` — that is partial coverage of the thing agents actually do, not a port.) |
| **pyright-lsp** (official Anthropic Claude Code plugin) | Runs Pyright and surfaces LSP diagnostics into the conversation as `<new-diagnostics>`. New first-party entrant since the last positioning pass. | After the edit | **No.** Advisory, post-write, Python-only, needs a language server — every one of these is something the moat sentence already concedes to LSPs. Worth watching: it makes "just use an LSP" cheaper inside Claude Code itself. |
| **Serena** (oraios) | LSP-backed semantic MCP: find-references, rename, symbol-level edits, 40+ languages. | On request, during the loop | **No.** Deeper than RunEcho on every axis except timing and setup; it is a tool the agent *chooses* to call, not a gate that fires whether or not the agent cooperates. |
| **Aider repo map** | Tree-sitter + PageRank ranking of important symbols, packed into the context budget. | Context supply, pre-generation | **No.** Supplies context; validates nothing. Fails conjuncts 2 and 3 by design — the repo map's job is to make the model *better informed*, never to overrule it. |
| **Cline / Continue / Cody** | No pre-write symbol-validation gate found this round. | — | **No.** Note the evidence here is weaker than for Cursor: Cursor publishes an exhaustive hook list, so its negative is documented; for these the negative is only "not found". |
| **Shipmoor** | Described as a local deterministic verification layer producing a binding merge verdict. | Merge time | **No.** Wrong end of the pipeline entirely. *Secondary source only — not primary-verified.* |

**Verdict 2026-07-23: the claim holds.** No surveyed tool satisfies all four
conjuncts, and no new entrant since the last pass does either. Cursor's hook
surface still cannot host a pre-write edit gate for native edits; GateGuard's
version bump (0.4.1 → 0.5.0) changed session ergonomics, not what it checks.

## Watch list — the credible paths to falsification

- **Khati, Rodriguez-Cardenas, Pantzer & Poshyvanyk (William & Mary, 2026)** —
  *Detecting and Correcting Hallucinations in LLM-Generated Code via Deterministic
  AST Analysis* ([arXiv 2601.19106](https://arxiv.org/abs/2601.19106), submitted
  27 Jan 2026). The
  closest thing to RunEcho's technique in print: entirely static, non-executing,
  AST + a versioned knowledge base built by library introspection; flags unknown
  APIs and bare critical calls; reports 100% precision / 87.6% recall / 77%
  auto-correction. **It is explicitly a post-generation post-processing step, and
  names IDE integration as future work** — so it satisfies conjuncts 3 and 4 but
  not 1 and 2. This is the most likely origin of a real rival: someone moves that
  analysis into a pre-write hook. It also independently validates the approach,
  which is worth citing rather than fearing. Re-checked this pass against the
  arXiv abstract and HTML body directly: still framed as post-processing, and the
  future-work section still only envisions a VS Code semantic-linter integration
  ("as the LLM generates code, the analyzer would validate each call in real
  time") — no shipped hook, no pre-write blocking. Unchanged since the last pass.
- **Cursor shipping a `beforeFileEdit` event.** The moment that exists, the empty
  square becomes buildable in the largest agent harness.
- **A first-party LSP-backed pre-write gate.** `pyright-lsp` today only injects
  diagnostics after the fact; wiring the same language server into a blocking
  PreToolUse decision would satisfy all four conjuncts for one language at a time,
  at the cost of the setup and staleness RunEcho trades away.

## Adjacent, deliberately out of scope

Hallucinated *dependencies* (slopsquatting — e.g. the `react-codeshift` package
invented by a model and propagated into hundreds of repos in early 2026) are a
different lane: package-registry existence, not in-repo symbol existence. Tools in
that lane are not rivals and should not be listed as if they were.

## Re-verification procedure

Quarterly, or immediately if a major harness announces new hook events.

1. Re-run these searches and read primary docs, not summaries:
   - `Claude Code PreToolUse hook block undefined symbol hallucination guard`
   - `Cursor hooks beforeFileEdit` — check <https://cursor.com/docs/hooks.md> for
     any new pre-edit event
   - `MCP server validate symbol exists before write`
   - `deterministic AST hallucination detection LLM code` — track whether the
     arXiv line of work has shipped a hook
   - Changelogs: GateGuard, Serena, Aider, Cline, Continue.
2. Score each candidate against the four conjuncts above. Three out of four is a
   watch-list entry, not a falsification.
3. Update the table **and every dated string in both files** — there are more of
   them than is obvious, and a survey date left behind in one place is exactly the
   silent staleness this file exists to prevent. Find them mechanically rather
   than from memory:

   ```
   grep -rn '<previous survey date>' docs/competitive-landscape.md docs/runecho-vs-field.html
   ```

   That grep should return exactly five hits — no more, no fewer, and if it
   returns six someone added a date stamp without adding it here. The five are:
   this file's **Last verified**
   line (plus its *next due* date), the `## Survey` heading, the **Verdict** line,
   the `Last verified` stamp in the field doc's moat note, and the
   **re-surveyed** date in the field doc's footer sourcing. They all move together
   or the stamps are lying.
4. If something does satisfy all four: rewrite the positioning honestly in the
   same pass. A stale moat claim costs more credibility than a conceded one.
