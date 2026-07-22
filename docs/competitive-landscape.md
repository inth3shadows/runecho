# Competitive landscape — keeping the moat claim defensible

**Last verified 2026-07-22.** Next re-verification due: 2026-10-22 (quarterly).

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

## Survey — 2026-07-22

| Tool | What it actually does | Fires | Falsifies the claim? |
|---|---|---|---|
| **GateGuard** (`gateguard-ai`, zunoworks) | "deny + force investigation + demand evidence" — blocks the first write attempt and makes the agent produce facts (importers, data schemas, quoted instruction) before allowing a retry. v0.4.1 added an opt-in bughunt gate requiring test/build verification after N edits. | Pre-write (PreToolUse) | **No.** Nearest neighbour on *timing*, but it never evaluates symbol existence — it evaluates whether the agent looked. Conjunct 3 fails. |
| **Cursor hooks** (1.7+) | Hook surface enumerated in the docs: `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt` can allow/deny; `afterFileEdit` / `afterTabFileEdit` are observational only. | `beforeReadFile` pre-read; edits **post-write only** | **No — and structurally can't today.** There is no `beforeFileEdit` event, so no Cursor hook of any kind can gate an edit before it lands. (Also relevant to issue #174: the guard cannot be ported to Cursor until such an event exists.) |
| **pyright-lsp** (official Anthropic Claude Code plugin) | Runs Pyright and surfaces LSP diagnostics into the conversation as `<new-diagnostics>`. New first-party entrant since the last positioning pass. | After the edit | **No.** Advisory, post-write, Python-only, needs a language server — the three things the moat sentence concedes to LSPs anyway. Worth watching: it makes "just use an LSP" cheaper inside Claude Code itself. |
| **Serena** (oraios) | LSP-backed semantic MCP: find-references, rename, symbol-level edits, 40+ languages. | On request, during the loop | **No.** Deeper than RunEcho on every axis except timing and setup; it is a tool the agent *chooses* to call, not a gate that fires whether or not the agent cooperates. |
| **Aider repo map** | Tree-sitter + PageRank ranking of important symbols, packed into the context budget. No MCP support as of v0.86.x. | Context supply, pre-generation | **No.** Supplies context; validates nothing. |
| **Cline / Continue / Cody** | No pre-write symbol-validation gate found this round. | — | **No.** Note the evidence here is weaker than for Cursor: Cursor publishes an exhaustive hook list, so its negative is documented; for these the negative is only "not found". |
| **Shipmoor** | Described as a local deterministic verification layer producing a binding merge verdict. | Merge time | **No.** Wrong end of the pipeline entirely. *Secondary source only — not primary-verified.* |

**Verdict 2026-07-22: the claim holds.** No surveyed tool satisfies all four
conjuncts. Cursor's hook surface strengthened it — one major rival harness cannot
host a pre-write edit gate at all.

## Watch list — the credible paths to falsification

- **Khati, Rodriguez-Cardenas, Pantzer & Poshyvanyk (William & Mary, 2026)** —
  *Detecting and Correcting Hallucinations in LLM-Generated Code via Deterministic
  AST Analysis* ([arXiv 2601.19106](https://arxiv.org/html/2601.19106)). The
  closest thing to RunEcho's technique in print: entirely static, non-executing,
  AST + a versioned knowledge base built by library introspection; flags unknown
  APIs and bare critical calls; reports 100% precision / 87.6% recall / 77%
  auto-correction. **It is explicitly a post-generation post-processing step, and
  names IDE integration as future work** — so it satisfies conjuncts 3 and 4 but
  not 1 and 2. This is the most likely origin of a real rival: someone moves that
  analysis into a pre-write hook. It also independently validates the approach,
  which is worth citing rather than fearing.
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
3. Update the table, the **Last verified** date at the top of this file, **and**
   the matching date stamp in `runecho-vs-field.html`. All three move together or
   the stamp is lying.
4. If something does satisfy all four: rewrite the positioning honestly in the
   same pass. A stale moat claim costs more credibility than a conceded one.
