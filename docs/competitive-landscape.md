# Competitive landscape — keeping the moat claim defensible

**Last verified 2026-07-23.** Next re-verification due: 2026-10-22 (quarterly).

> The due date tracks the **22nd** of Jan/Apr/Jul/Oct because that is when the
> scheduled job actually fires (local cron, `~/.local/bin/runecho-moat-reverify`).
> Set it to the 22nd, not to "three months from today" — a due date that disagrees
> with the schedule is its own small lie.

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
| **Anti-Hallucination-MCP** (`Akunimal`) | The nearest neighbour on *substance* — and the first tool found that checks the same thing RunEcho checks. Deterministic, model-free symbol existence: AST extraction via `@ast-grep/napi` for JS/TS/TSX, regex fallback for ~15 more languages, symbols persisted to `.wisdom/symbols.json`, confidence-scored with fuzzy typo matching. Ships a Claude Code hook that its own setup script installs. Also bundles command-output compression (dedup/grouping) — so one repo overlaps both RunEcho *and* terse. | **Post-write** — the installed hook is `PostToolUse` on `Write`/`Edit`; it "warns after every edit", writing to stderr *after* the write has landed | **No — but it is the closest miss on record.** Satisfies conjuncts 3 (symbol existence) and 4 (deterministic, no LLM) outright. Fails 1 and 2: the edit is already on disk when it fires, so it advises rather than denies. Two further concessions RunEcho does not make: a persisted registry that *can* go stale (mitigated by an opt-in `watch_project` watcher, not by re-parsing), and an `ANTIHALL_AUTOFIX=1` mode that rewrites the identifier in place — a repair posture, not a gate. **Its falsification path is one line of JSON** (see watch list). |
| **Cursor hooks** | Hook surface enumerated in the docs: `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt` can allow/deny; `afterFileEdit` / `afterTabFileEdit` are observational only. Re-checked this pass against <https://cursor.com/docs/hooks.md> directly: still no `beforeFileEdit` or equivalent pre-edit-for-native-edits event. | `beforeReadFile` pre-read; native edits **post-write only** | **No — and structurally can't today.** There is no `beforeFileEdit` event, so an edit made with Cursor's *native* edit tool cannot be gated before it lands. (Also relevant to issue #174: a Cursor port could only cover edits routed through shell or an MCP filesystem server, via `beforeShellExecution` / `beforeMCPExecution` — that is partial coverage of the thing agents actually do, not a port.) |
| **pyright-lsp** (official Anthropic Claude Code plugin) | Runs Pyright and surfaces LSP diagnostics into the conversation as `<new-diagnostics>`. New first-party entrant since the last positioning pass. | After the edit | **No.** Advisory, post-write, Python-only, needs a language server — every one of these is something the moat sentence already concedes to LSPs. Worth watching: it makes "just use an LSP" cheaper inside Claude Code itself. |
| **Serena** (oraios) | LSP-backed semantic MCP: find-references, rename, symbol-level edits, 40+ languages. | On request, during the loop | **No.** Deeper than RunEcho on every axis except timing and setup; it is a tool the agent *chooses* to call, not a gate that fires whether or not the agent cooperates. |
| **Aider repo map** | Tree-sitter + PageRank ranking of important symbols, packed into the context budget. | Context supply, pre-generation | **No.** Supplies context; validates nothing. Fails conjuncts 2 and 3 by design — the repo map's job is to make the model *better informed*, never to overrule it. |
| **Cline / Continue / Cody** | No pre-write symbol-validation gate found this round. | — | **No.** Note the evidence here is weaker than for Cursor: Cursor publishes an exhaustive hook list, so its negative is documented; for these the negative is only "not found". |
| **Shipmoor** | Described as a local deterministic verification layer producing a binding merge verdict. | Merge time | **No.** Wrong end of the pipeline entirely. *Secondary source only — not primary-verified.* |

**Verdict 2026-07-23: the claim holds — but by a narrower margin than the last
pass, and the shape of the field has changed.** No surveyed tool satisfies all
four conjuncts. Cursor's hook surface still cannot host a pre-write edit gate for
native edits; GateGuard's version bump (0.4.1 → 0.5.0) changed session
ergonomics, not what it checks.

What *did* change is that the empty square now has two different tools sitting on
two different edges of it:

- **GateGuard is the nearest neighbour on timing** — pre-write and blocking
  (conjuncts 1 and 2), but it never asks whether a symbol exists.
- **Anti-Hallucination-MCP is the nearest neighbour on substance** —
  deterministic symbol-existence checking (conjuncts 3 and 4), but only after the
  write has landed.

Each holds exactly the half of the claim the other is missing. That is a more
fragile position than "nobody is nearby", and it should be stated that way rather
than smoothed over: the moat is now the *conjunction*, not the idea. Anyone who
merges those two halves has a rival, and neither half is hard to build.

Note also that the survey found Anti-Hallucination-MCP only via a GitHub
code-search for `hallucination symbol claude-code`, which returns exactly two
repositories: it and RunEcho. Prior passes searched product docs, changelogs and
the web, which is why a repo that has existed since 2026-05-31 and was pushed to
three days before this survey was missed. **Add a raw GitHub repo-search to the
procedure** — the previous method could not see a competitor with no marketing
surface, which is the exact profile of the most likely rival.

## Watch list — the credible paths to falsification

- **Anti-Hallucination-MCP moving its hook from `PostToolUse` to `PreToolUse`.**
  This is now the cheapest path to falsification on the board, and it is not
  close: the analysis is already deterministic and already symbol-existence-based,
  so the remaining work is changing the event name in the settings block its
  setup script writes, and returning a deny decision instead of a stderr warning.
  Everything the four-conjunct test asks for would then be satisfied. Weigh that
  against the two things it would still concede — a persisted registry that can
  lag the working tree, and JS/TS-only AST fidelity with regex elsewhere — but
  do not mistake those for a moat. **Check this repo's hook wiring every pass,
  not just its changelog**; it is a 0-star project with no release notes habit,
  so the change would ship silently.
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

**Considered and rejected** — record these so a later pass does not re-adjudicate
them from scratch:

- **Alterion Draco** (surfaced in the July 2026 pass — deliberately written
  without a full date so it does not inflate the five-stamp grep) — enterprise agent-governance and
  runtime threat detection (prompt injection, data exfiltration, OWASP Agentic
  Top 10). Governs what an agent is *allowed* to do; has no notion of whether a
  symbol exists. Fails conjunct 3 outright and is not even adjacent.

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
   - **Raw GitHub repo search, not just product docs and the web** — the
     pass that added this bullet missed Anti-Hallucination-MCP for two months
     because it has no marketing surface to search. (No date here on purpose —
     this is a permanent procedural note, so it must not move with the survey
     stamps.) A competitor with 0 stars and no blog post is still a competitor,
     and is in fact the likeliest profile:

     ```
     gh api "search/repositories?q=hallucination+symbol+claude-code&sort=updated" \
       --jq '.items[] | "\(.stargazers_count)★ \(.full_name) pushed=\(.pushed_at[0:10]) — \(.description)"'
     ```

     As of this pass that query returns exactly two repositories: RunEcho and
     Anti-Hallucination-MCP. A third appearing is the signal to re-run the full
     four-conjunct test.
   - For every candidate that checks symbol existence, **read the hook wiring
     itself** (`PreToolUse` vs `PostToolUse`, deny vs stderr) rather than the
     README's adjectives. "Guard", "blocks", and "prevents" are used loosely in
     this space; the event name is not.
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
