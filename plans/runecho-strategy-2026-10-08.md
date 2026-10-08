# RunEcho strategy — what to keep, cut, and build as harnesses absorb the space

**Date:** 2026-10-08
**Inputs:** the 2026-10-08 module audit (issues #472–#495), fresh web research on
Claude Code, Codex, Cursor, Copilot, Gemini CLI, Kiro and code-graph MCP servers,
`docs/competitive-landscape.md` (2026-08-06 pass), `bench/FPAUDIT.md`,
`bench/FINDINGS.md`, `docs/check-worthiness.md`, and the 2026-03-26 overlap audit.
**Optimizes for:** the maintainer's own leverage first, outside adoption second.
Where the two conflict, personal value wins.

Every external claim below carries a source. Items marked UNVERIFIED could not be
fetched from a primary page in this pass (the research proxy blocked several vendor
doc sites) and must be checked before anything is built on them.

---

## 1. The short answer

**Narrow and harden. Do not expand, and do not sunset yet.**

RunEcho should stop being "a code-intelligence toolkit" and become one thing: **a
deterministic, model-free, pre-write gate on in-repo symbols that works in every
harness with a hook API, and that can prove its own precision.** Everything that a
harness or an LSP plugin now does as well or better, such as symbol navigation,
structure dumps, scope rules and generic health checks, should be frozen or cut.
The engineering time freed goes to three things nobody else is doing:
1. making the gate impossible to turn off silently;
2. proving it works in each harness;
3. measuring whether it changes outcomes at all.

That last item is the uncomfortable one, and it decides everything else (§4).

---

## 2. What the harnesses now do (October 2026)

### Claude Code

| Capability | Shipped | Overlap with RunEcho |
|---|---|---|
| `LSP` tool: definition, references, hover, workspace symbols, call hierarchy | v2.0.74, 2025-12-19 ([changelog](https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md), [tools reference](https://code.claude.com/docs/en/tools-reference#lsp-tool-behavior)) | **Strong** with `runecho-mcp` `locate`/`structure` |
| LSP diagnostics pushed to the model **after every edit** ("type errors and missing imports") | Documented; `diagnostics` defaults to true ([plugins reference](https://code.claude.com/docs/en/plugins-reference#lspservers)) | **Partial** with the guard: same signal, one turn later, advisory, needs a language server |
| Official LSP plugins for Go, TS/JS, Python, Rust, Java, C#, … | [code-intelligence](https://code.claude.com/docs/en/plugins/code-intelligence) | Covers every language the guard checks |
| Code intelligence **disabled in cloud sessions** | Same page | **RunEcho's opening** (§3) |
| Hooks: `permissionDecision` allow/deny/ask/defer, `updatedInput`, `tool_use_id` (v2.0.43), PostToolUse `duration_ms` | [hooks](https://code.claude.com/docs/en/hooks), changelog | Platform RunEcho runs on, not a rival. `tool_use_id` unblocks #467 |
| Plugins and marketplaces bundling hooks + MCP + LSP | v2.0.12, 2025-10-09 | Distribution channel RunEcho under-uses |
| `Edit(path-glob)` permission rules | [permissions](https://code.claude.com/docs/en/permissions) | **Partial to strong** with edit-scope contracts |
| `/doctor`, checkpoints/`/rewind`, native tasks, worktrees, `/code-review`, a "verify" skill convention (v2.1.286), "You should know" side agent (v2.1.287) | Changelog | Weak. All LLM-based or file-level, none symbol-level or deterministic |
| Built-in undefined-symbol blocker or deterministic verifier | **Not found** | — |

### The rest of the field

- **Every major harness now has a pre-tool hook.** Copilot `.github/hooks` `preToolUse` ([docs](https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-hooks)), VS Code Copilot agent mode, which reads `.claude/settings.json` directly ([docs](https://code.visualstudio.com/docs/copilot/customization/hooks)), Gemini CLI `BeforeTool` ([docs](https://geminicli.com/docs/hooks/)), Kiro `PreToolUse` ([docs](https://kiro.dev/docs/cli/v3/hooks)), Codex hooks stable since v0.124, and Cursor `preToolUse`, which can also import Claude Code hooks ([docs](https://cursor.com/docs/reference/third-party-hooks.md)). Claude Code's schema is the de facto standard; there is no formal spec ([convergence analysis](https://codex.danielvaughan.com/2026/06/25/agent-hook-convergence-codex-cli-claude-code-gemini-cli-kiro-opencode-portable-governance/)). Agent Plugins 1.0 (Aug 2026) packages skills and MCP but **not hooks**.
- **Denying a file write is the least reliable hook path.** Codex [#27833](https://github.com/openai/codex/issues/27833) reports deny ignored on `apply_patch`. Cursor's imported Claude hooks get a Cursor-shaped payload and fail open ([forum](https://forum.cursor.com/t/cursor-agent-imported-claude-code-plugin-hooks-get-a-cursor-shaped-payload-so-pretooluse-gates-fail-open/173171)). Claude Code [#13744](https://github.com/anthropics/claude-code/issues/13744) reported exit-2 not blocking Write/Edit. Current status of all three is UNVERIFIED.
- **Every vendor's answer to bad symbols is "write, then show diagnostics":** Cursor's lint loop, OpenCode's LSP, Aider's tree-sitter lint, and the Claude Code LSP plugins. At PR time it is LLM review (Bugbot, Copilot review), which is itself adding deterministic analyzers (CodeQL/ESLint). **Nobody ships a pre-write symbol gate.**
- **Symbol and graph MCP servers are crowded.** Serena (LSP-backed, 30–40 languages, every harness), Sourcegraph MCP, Augment Context Engine, CodeGraphContext, codebase-memory-mcp, and code-graph-mcp (already a Claude Code plugin). Gortex (1090★) already holds candidate bytes pre-write in its own MCP write path (landscape doc). `runecho-mcp` has no advantage here, and the audit found it the least fit-for-purpose surface (#488, #489).

---

## 3. What is still RunEcho's, and what isn't

| RunEcho part | Still unique? | Verdict |
|---|---|---|
| **Pre-write, blocking, deterministic, in-repo symbol gate** | **Yes.** Narrowed moat claim holds; nothing in this pass falsifies it | **Keep. This is the product.** |
| Works with **no language server**, so it runs where LSP isn't (Claude Code cloud sessions, CI agents, Copilot coding agent) | Yes | **Keep, and lean in** |
| Index freshness from **git hooks** (human and tool commits, not just agent edits) | Yes, as part of the gate | Keep; fix the silent-staleness bugs (#479, #480, #481, #482) |
| **Measured precision** (`fpaudit`: fp / premature / stands against git history) | Yes. No harness measures hook precision | **Keep. This is the credibility lever** |
| Symbol snapshots, structural diff, churn | Unique, but who uses them? | Freeze; keep only what the gate needs |
| `runecho-mcp` locate/structure/hash/diff | **No.** The LSP tool, Serena and graph MCPs do this better | **Keep (maintainer uses it), but stop growing it**: fix #488/#489, no new tools |
| Edit-scope contracts | Mostly covered by `Edit(glob)` permission rules | **Freeze**; don't deepen (#427 stays as is) |
| `doctor` | Needed for RunEcho's own install, not a differentiator | Keep, narrowed to "is the gate actually live" (#490) |
| Claims extraction / truth-trail | Niche | Freeze |

---

## 4. The question that decides everything: does the gate change outcomes?

RunEcho's own measurements, which are the best evidence available:

- **0 denials in 308 ask-gated edits** over 30 days of dogfooding (`bench/FPAUDIT.md`). Every ask was approved.
- Of 406 rated flags: **15% fp, 34% premature, 51% stands** (2026-08-07), with fp trending toward 0% after #288.
- Catch rate on real, transcript-observed hallucinations: **1 of 9** captured (`bench/FINDINGS.md`); 4 of 9 in the README's benchmark.

Read together, these say the gate's precision is improving. They do not yet show that it changes what lands. Every ask was approved. A third of flags were the agent writing the caller before the callee. And in sessions where an LSP plugin is installed, the same undefined name is reported one turn later anyway.

So the strategic question is **not** "what features should RunEcho add". It is:

> **In a session that already has LSP diagnostics after edits, does a pre-write
> gate reduce wasted turns, broken commits, or hallucinations that reach a
> commit — and in a session without LSP, by how much?**

If the answer is "no measurable difference with LSP, material difference without," then RunEcho's honest position is **the gate for environments without a language server**: cloud agents, CI agents, polyglot or minimal containers. That is a real and growing niche, and smaller than today's pitch.

If the answer is "no difference anywhere," the honest move is to archive the gate, keep `fpaudit` as a write-up, and stop paying the maintenance cost. That cost is 97k LOC, and the audit found 23 Medium+ defects in it.

### A posture change worth testing first: `deny` with a reason instead of `ask`

`ask` routes every flag to a human, and humans approved 308 of 308. A `deny` whose reason names the missing symbol goes to the **agent**, which then has to define the callee or fix the name. That addresses two problems at once:
- the 34% `premature` share: the agent is pushed to write the callee first, or is told the symbol is new and may proceed;
- the zero-denial problem: humans stop being the bottleneck.

Hook support for `deny` + reason is documented in every harness above. This needs no new checks, only a posture flag and a measurement.

---

## 5. Recommendation and plan

### Phase 0: stop the silent failures (now, ~1–2 weeks)

The audit's most damaging findings are not false positives. They are cases where the gate turns itself off and says nothing. Fix these before measuring anything, or the measurements are wrong:

- #472 (new directory → unchecked), #478 (symlinks), #479 (cross-worktree auto snapshot), #480 (reindex keeps stale symbols after upgrade), #482 (ignored dirs leak in), #490 (doctor says ok when the gate is off).
- **Make the gate usable in cloud sessions, starting with this maintainer's own.** In this audit session the `runecho` MCP server failed to start (`${RUNECHO_MCP_COMMAND}` unresolved) and no `runecho-guard` was on PATH, so the project's own `.claude/settings.json` hooks were inert. Cloud sessions are exactly where Claude Code's LSP is off and RunEcho should matter most. Ship a SessionStart install path (or plugin) and verify it here.

### Phase 1: run the experiment (2–4 weeks of dogfooding)

- Land #467 (stamp `tool_use_id`, now available in Claude Code since v2.0.43) so asks and outcomes join exactly; fix #493/#494 so the reports agree.
- Run four arms on your own work, recorded in `decisions.jsonl`:
  1. guard `ask` (today)
  2. guard `deny`+reason
  3. LSP plugin only
  4. LSP plugin + guard `deny`
- **Measure per arm:** turns to a compiling state after an edit that introduced an unresolved name; unresolved names that reached a commit (git-oracle, as fpaudit already does); human interruptions.
- **Pre-registered decision rule:**
  - If arm 4 is not better than arm 3 on Claude Code local, stop positioning against LSP and reposition as "the gate where LSP isn't".
  - If arm 2 is not better than arm 1, revert the posture.
  - If no arm beats "nothing", archive the gate (§4).

### Phase 2: if the gate earns its place, make the Claude Code install first-class; other harnesses only for adoption

Claude Code is the maintainer's primary harness (decided 2026-10-08), so the order below is fixed and items 2+ are adoption work that waits until item 1 is solid.

1. **Claude Code plugin**: marketplace install that bundles the hooks, the MCP server and a SessionStart install for cloud sessions, replacing hand-edited settings (as `anti-halu` already does). This is personal value, not adoption.
2. *(adoption, later)* VS Code Copilot reads `.claude/settings.json`, so it needs verification only. Then Copilot `.github/hooks`, Gemini `BeforeTool` (`write_file|replace`), Kiro `PreToolUse` (`write`), Codex (gated on #27833), and Cursor `preToolUse` with a native payload rather than the fail-open import path.

- **A deny-conformance suite.** Start with Claude Code's own write tools (Edit, Write, MultiEdit, NotebookEdit) and settle #13744 first. For each harness and version: does `deny` actually stop the write, for each write tool? Nobody publishes this, the research found three harnesses where it is reported broken, and RunEcho's value is zero wherever it fails. It is small, differentiated, and useful to others even if they never adopt the gate.
- **Publish the precision numbers** (fp/premature/stands per language, per version). The field is moving toward deterministic signals precisely because LLM reviewers are noisy. A gate with a published, falsifiable precision record is the adoption argument.

### Phase 3: shrink the surface (in parallel, low effort)

- **Keep `runecho-mcp`, but stop growing it** (the maintainer uses it; decided 2026-10-08). Fix #488/#489: cap and paginate `structure`, report coverage in `locate`, and serve from the enrolled snapshot instead of a live 30s walk. Add no new tools; navigation beyond that belongs to the LSP tool.
- **Freeze contracts, claims and truth-trail.** No new work; close #12 as superseded by `Edit(glob)` permission rules unless a concrete use appears.
- **Parser and extractor work stays under Gate 0.** The false-positive issues filed by the audit (#473–#477, #483–#485) are verified bugs, but their repros are constructed. Per `docs/check-worthiness.md`, each needs a live `decisions.jsonl` observation and a first-party exposure count before it is fixed. Measure exposure first: dot-imports, wrapped signatures and inline `type` imports are likely common, while docstring-seeded edits may not be.
- **Consider using the language server as an oracle where one exists.** When gopls, pyright or tsserver is running, ask it instead of hand-extending regex extractors. Keep the AST path for environments without one. This could retire a large share of `internal/guard/extract.go`'s special cases. It needs a latency test against the 5s hook budget before any commitment.

### What not to do

- Don't build more code-intelligence surface (graphs, search, navigation). The field is crowded and backed by LSP.
- Don't widen the moat claim back out (the landscape doc's honesty rules already forbid this).
- Don't add new check classes (Gates 1–5) until Phase 1 shows the existing check moves an outcome.

---

## 6. Personal value vs adoption, where they conflict

| Decision | Personal-value answer (wins) | Adoption answer |
|---|---|---|
| Ask vs deny posture | **`deny`+reason becomes the default if Phase 1 supports it** (decided) | Deny by default reads better in demos |
| Cross-harness adapters | **Claude Code only** (decided) | All of them, later |
| Keep `runecho-mcp` | **Keep** (you use it); fix, don't grow | A "full toolkit" sells better, but competes with free LSP |
| Cloud-session install | **First**: it is where your own gate is currently off | Also a differentiator |

---

## 7. Decisions (maintainer, 2026-10-08)

1. **Harness:** Claude Code is primary. Phase 2 builds the Claude Code plugin first; other adapters are deferred adoption work.
2. **`runecho-mcp`:** in use. Keep it, fix #488/#489, add no new tools.
3. **Posture:** if Phase 1 shows `deny`+reason beats `ask`, it becomes the default; `ask` stays available as an opt-in.

## 8. Re-verification

This document's field claims decay like `docs/competitive-landscape.md`'s. They should be re-checked on that file's quarterly cycle (next due 2026-10-22). The UNVERIFIED hook-deny reliability items (#13744, #27833, the Cursor import path) are the first things the Phase 2 conformance suite would settle.
