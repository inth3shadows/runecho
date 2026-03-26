# RunEcho Technical Audit — Claude Code Native Feature Overlap

**Date:** 2026-03-26
**Auditor:** Claude (Opus 4.6)
**Scope:** All 10 binaries, 11 hooks, 5 skills, MCP server

---

## SECTION A: Feature-by-Feature Comparison

### 1. ai-task (Task Ledger)
**What it does:** Persistent task tracking in `.ai/tasks.json` with dependencies (`blocked-by`), scope globs, verify commands, contract generation, drift-check, and replan. Cross-session — tasks survive restarts.

**Claude Code native:** TaskCreate/TaskUpdate/TaskList/TaskGet tools. Native tasks support status, dependencies (blocks/blockedBy), descriptions, and activeForm spinners. Tasks are session-scoped (die when session ends).

**Key differences:**
- RunEcho tasks persist to disk across sessions. Native tasks are ephemeral.
- RunEcho tasks have `scope` (glob patterns) and `verify` (shell command) fields — native tasks do not.
- RunEcho has `contract`, `drift-check`, `replan`, `sync` subcommands — no native equivalent.
- RunEcho's `verify` runs a shell command and records pass/fail to `results.jsonl` — native tasks have no verification concept.

**Verdict: PARTIAL.** Native tasks handle in-session tracking well but lack persistence, scope constraints, verification, and contract generation.

---

### 2. ai-governor + session-governor.sh (Session Governor)
**What it does:** Tracks turn count and cumulative session cost. Injects warnings at thresholds (15/25/35 turns, $1/$3/$8 cost). At hard limit, blocks opus routing and forces handoff. Emits TURN_FATIGUE and COST_FATIGUE faults.

**Claude Code native:** No equivalent. Claude Code has no session cost tracking, no turn-based warnings, no cost-based model downgrade, no forced handoff mechanism. The model has no awareness of how much a session has cost.

**Verdict: UNIQUE.** Nothing in Claude Code tracks or limits session cost/turns.

---

### 3. ai-governor Model Router + Classifier (classifier.go, router.go)
**What it does:** Classifies each prompt via Haiku API call (2s timeout, cached) or regex fallback. Routes to haiku/sonnet/opus/pipeline. Injects routing directive into context. Cost: ~$0.001/classification.

**Claude Code native:** No model routing. Claude Code uses whatever model the subscription provides (Opus/Sonnet depending on plan). Users cannot route individual prompts to different models. There is no concept of "use haiku for this read-only task."

**Key reality check:** This feature was designed when Claude Code let you choose models per-turn or per-subagent. As of early 2026, Claude Code's model selection is subscription-tier-based, not per-turn. The model-enforcer.sh hook itself acknowledges that Agent tool calls have their model parameter "schema-stripped" — meaning Claude Code actively prevents this kind of control. The router injects *text* telling Claude to "use haiku" but Claude Code does not actually change the underlying model.

**Verdict: REDUNDANT.** The routing directives are injected as text, but Claude Code does not honor model selection requests from hooks. The classifier burns API credits classifying prompts into routes that cannot be enforced. The model-enforcer catches Task tool calls but admits Agent calls are audit-only. This entire subsystem is a no-op in practice.

---

### 4. model-enforcer.sh (PreToolUse on Task)
**What it does:** Denies Task subagent spawns that use the wrong model per the router's decision.

**Claude Code native:** Claude Code now manages model selection internally. The hook's own comments admit Agent tool calls have model "schema-stripped" and cannot be enforced.

**Verdict: REDUNDANT.** See router analysis above.

---

### 5. ai-ir (Codebase IR Indexer)
**What it does:** AST-parses JS/TS/Go/Python source files. Produces `.ai/ir.json` — a flat fact table of files + exported symbols (functions, types, classes, constants). Supports snapshots (SQLite), diff between snapshots, churn analysis, and `validate-claims` (anti-hallucination check against IR).

**Claude Code native:** No equivalent. Claude Code has no codebase index. It reads files on demand using Read/Glob/Grep tools. It has no structural awareness of what symbols exist without reading source files. The git status injection is file-level only, not symbol-level.

**Key question:** How much does the IR actually help? The IR is injected on turn 1 (4000 token budget). For a large codebase, this is a compressed file list + symbol dump. Claude Code's Glob/Grep tools are fast and accurate. The IR's value is avoiding the 2-3 tool calls Claude would otherwise make to orient itself.

**Verdict: UNIQUE.** Genuinely not replaceable by native features. The snapshot/diff/churn/validate-claims features have no equivalent. Whether the turn-1 injection is *worth the complexity* is a separate question from whether it's *unique*.

---

### 6. ir-injector.sh + ai-context (Context Compiler)
**What it does:** On session turn 1, compiles and injects: CONTRACT.yaml, IR summary, git diff, last handoff, task state, churn hotspots, verify failures, and code review notes. Budget-aware (4000 tokens default). Includes GUPP (next-session intent from handoff + next task).

**Claude Code native:** CLAUDE.md files provide per-repo context. MEMORY.md provides persistent memory. Git status is injected into system prompt. No equivalent to: contract injection, IR injection, handoff injection, churn injection, verify failure injection, or GUPP.

**Key differences:**
- Claude Code's context is static (CLAUDE.md) or manually maintained (MEMORY.md). RunEcho's context is dynamic and session-aware.
- The handoff provider gives Claude the *previous session's summary* — native memory is unstructured notes, not a session narrative.
- The contract provider gives Claude scope boundaries before it starts working.

**Verdict: PARTIAL.** CLAUDE.md and MEMORY.md cover some of this, but the dynamic, session-aware compilation (especially handoff + contract + verify failure injection) has no native equivalent.

---

### 7. ai-session (Session Synthesizer)
**What it does:** Reads Claude Code's JSONL session log. Extracts ground-truth facts (files edited, commands run, token counts, cost, duration). Calls Haiku to summarize the session narrative. Produces `.ai/handoff.md` with YAML front-matter.

**Claude Code native:** Claude Code has session continuation (resume previous conversation). Auto-memory saves structured memories across sessions. But there is no session summary artifact, no ground-truth extraction from JSONL logs, no cross-session handoff document.

**Verdict: UNIQUE.** Session continuation partially replaces this (you can just resume), but the structured handoff with factual extraction has no equivalent. The question is whether session continuation makes handoffs unnecessary.

---

### 8. ai-session-end + session-end.sh (Session-End Orchestration)
**What it does:** 7-stage pipeline: scope-drift detection, IR snapshot, pipeline envelope, task verify, IR diff summary, handoff generation (via ai-session), fallback checkpoint. Appends to progress.jsonl.

**Claude Code native:** SessionEnd hook exists (RunEcho uses it). But Claude Code has no built-in session-end logic — no scope-drift check, no IR snapshot, no verification run, no handoff generation.

**Verdict: UNIQUE.** The hook point exists natively, but all the logic is RunEcho's.

---

### 9. ai-document (Documentation Generator)
**What it does:** Auto-generates/updates README.md, TECHNICAL.md, USAGE.md via Haiku. Change-gated by IR diff — skips if no structural changes.

**Claude Code native:** No auto-documentation feature. Claude can write docs when asked, but there's no automated "generate docs on session end" mechanism.

**Verdict: UNIQUE but low value.** Claude can generate better docs interactively when asked. Auto-generated docs via Haiku tend to be generic. The change-gating is clever but the output quality is limited by using the cheapest model.

---

### 10. ai-pipeline (Pipeline Definitions)
**What it does:** Declarative multi-stage pipelines (haiku explore -> opus reason -> sonnet implement). Renders injection text and writes execution envelopes.

**Claude Code native:** No equivalent pipeline system. However, this entire subsystem depends on model routing working (see item 3). Since Claude Code doesn't honor per-turn model selection, pipelines are decorative.

**Verdict: REDUNDANT.** Depends on model routing which doesn't work.

---

### 11. ai-provenance (Provenance Export)
**What it does:** Joins progress.jsonl, faults.jsonl, results.jsonl, tasks.json to produce a full execution record per task. Shows session timeline, cost, IR hashes, drift flags, fault signals, verify outcomes.

**Claude Code native:** No equivalent. Claude Code has no execution history, no cost tracking per task, no fault signal history.

**Verdict: UNIQUE.** Genuinely unique observability. Value depends on whether anyone actually reads these reports.

---

### 12. ai-mcp-server (MCP Server)
**What it does:** Exposes RunEcho capabilities as MCP tools: task management, session info, provenance queries, context compilation.

**Claude Code native:** MCP support exists natively (RunEcho uses it as the transport). But the *tools exposed* are RunEcho-specific. If RunEcho's underlying features are redundant, so is the MCP server.

**Verdict: PARTIAL.** The server is just a transport layer for other RunEcho features. Its value tracks the features it exposes.

---

### 13. destructive-bash-guard.sh (PreToolUse on Bash)
**What it does:** Hard-denies catastrophic commands (rm -rf /, mkfs, fork bombs, force-push to main). Soft-denies dangerous-but-recoverable (rm -rf path, git reset --hard, DROP TABLE, pipe-to-shell). Audit logs every decision.

**Claude Code native:** Claude Code has built-in safety. It asks for confirmation on destructive operations. However, Claude Code's safety is *model-based* (it decides whether to ask), not *hook-based* (enforced regardless of model context). The CLAUDE.md instructions in this repo already tell Claude to be careful with destructive operations.

**Key difference:** Hooks are memory-independent. Even if the model's context is compacted and it "forgets" the safety rules, the hook still fires. This is defense-in-depth.

**Verdict: UNIQUE.** The hook-level enforcement (cannot be forgotten, argued around, or compacted away) is genuinely different from model-based safety. This is the strongest argument for keeping any part of RunEcho.

---

### 14. scope-guard.sh (PreToolUse on Edit/Write)
**What it does:** Always blocks writes to .claude/settings, .env, *.key, *.pem, hook scripts. Optional scope-lock via .ai/scope-lock.json restricts writes to declared paths only.

**Claude Code native:** Claude Code asks for confirmation on sensitive writes but has no hard-deny mechanism for specific file patterns. No scope-lock equivalent.

**Verdict: UNIQUE.** Same defense-in-depth argument as the bash guard. Hook-enforced write restrictions cannot be forgotten.

---

### 15. constraint-reinjector.sh + pre-compact-snapshot.sh
**What it does:** Before compaction, snapshots governor state. After compaction, re-injects active constraints (turn count, cost, route, scope lock status, IR context) so the model doesn't lose awareness of its constraints.

**Claude Code native:** No equivalent. Claude Code's compaction is a black box — whatever was in context is summarized and compressed. There's no mechanism to guarantee specific constraints survive compaction.

**Verdict: UNIQUE.** Solves a real problem (constraint loss during compaction). However, most of what it re-injects is routing-related, which doesn't work (see item 3). The safety guard reminders and IR re-injection are the valuable parts.

---

### 16. stop-checkpoint.sh (Stop Hook)
**What it does:** After every Claude response: writes checkpoint.json, re-indexes IR, runs IR verify (structural diff since session start), runs validate-claims (anti-hallucination check against IR).

**Claude Code native:** No equivalent. Claude Code has no per-turn checkpointing, no structural drift detection, no anti-hallucination validation.

**Verdict: UNIQUE.** The anti-hallucination check (validate-claims) is genuinely novel — it catches when Claude references functions/types that don't exist in the codebase.

---

### 17. contract-sync.sh
**What it does:** On every user prompt, checks for .ai/CONTRACT.yaml and auto-creates a matching task in tasks.json if not present.

**Claude Code native:** No contract system. CLAUDE.md can contain project instructions but not per-task scope/verify contracts.

**Verdict: UNIQUE.** Part of the contract system which has no native equivalent.

---

### 18. Skills (ai-classify, ai-cost, ai-drift, ai-review, ai-scope)
**What it does:** Slash commands that expose RunEcho information (classification, cost, drift status, review, scope) as conversational tools.

**Claude Code native:** Skills/slash commands exist natively as a mechanism. These specific skills expose RunEcho data.

**Verdict: PARTIAL.** The mechanism is native; the data they expose tracks the underlying features.

---

## SECTION B: What's Still Valuable (Claude Code Cannot Replace)

1. **destructive-bash-guard.sh** — Hook-level safety enforcement that survives compaction and model confusion. This is the single strongest feature.

2. **scope-guard.sh** — Hook-level write restrictions on sensitive files. Same defense-in-depth argument.

3. **ai-ir (core indexing)** — No native codebase structure index. The turn-1 injection saves orientation time. Snapshot/diff is unique observability.

4. **stop-checkpoint.sh validate-claims** — Anti-hallucination: catches references to nonexistent symbols. No native equivalent.

5. **Session governor (turn/cost tracking only)** — Cost awareness and turn-based warnings. Claude has no idea how much a session costs.

6. **ai-session + handoff system** — Ground-truth session summaries from JSONL logs. Session continuation partially replaces this, but the structured artifact is unique.

7. **Contract system (CONTRACT.yaml + contract-sync + scope drift)** — Per-task scope boundaries with verification. Nothing native comes close.

8. **constraint-reinjector.sh (post-compact)** — Re-anchoring constraints after compaction. The IR and safety parts are valuable; the routing parts are dead.

9. **ai-provenance** — Task-level execution history. Unique observability, unclear if actively used.

---

## SECTION C: What Can Be Deleted

1. **Model Router / Classifier** (classifier.go, router.go, RegexRoute) — Claude Code does not honor model selection from hooks. The Haiku API classifier burns credits for routing decisions that cannot be enforced. The regex fallback is equally ineffective.

2. **model-enforcer.sh** — Enforces routing decisions that don't work. The hook itself admits Agent calls are audit-only.

3. **ai-pipeline** — Entire pipeline system depends on model routing. Declarative pipeline definitions are decorative.

4. **Pipeline-related governor code** — RoutePipeline, pipeline envelope writing in session-end, pipeline rendering in getRouteText.

5. **Persona Registry** (.ai/agents/*.yaml) — Model assignments per agent role, consumed by model-enforcer. Dead since routing doesn't work.

6. **ai-document** — Low value. Auto-generated docs via cheapest model produce generic output. Claude can write better docs on demand. The change-gating is clever but the ROI is poor.

7. **Skills** (ai-classify, ai-cost) — Expose routing and cost data. ai-classify is useless without routing. ai-cost could be folded into the governor's output.

---

## SECTION D: Honest Overall Verdict

**RunEcho is roughly 50% dead weight and 50% genuinely unique.**

The dead weight is all routing-related: the classifier, router, model enforcer, pipelines, and persona registry form a coherent subsystem that was designed for a version of Claude Code that allowed per-turn model selection. That version no longer exists. This subsystem should be deleted entirely — it adds latency (Haiku API calls on every prompt), burns API credits, and creates complexity that obscures the parts that actually work.

The genuinely valuable parts are:

1. **Safety hooks** (bash guard + scope guard) — The strongest argument for RunEcho's existence. Hook-level enforcement is categorically different from model-based safety. These are simple, reliable, and solve a real problem.

2. **Codebase IR** — Unique structural awareness. The anti-hallucination check alone justifies it.

3. **Session governance** (turn/cost tracking, not routing) — Cost awareness is valuable. The forced-handoff mechanism prevents runaway sessions.

4. **Contract + task persistence** — Scope boundaries with verification across sessions. Native tasks are ephemeral and lack scope/verify.

5. **Session handoff** — Structured cross-session context. Session continuation partially replaces this.

**Recommendation:** Strip out the routing/pipeline/enforcer/classifier subsystem. What remains is a lean session discipline + safety + IR toolkit that Claude Code genuinely cannot replicate. The codebase would shrink by ~30-40% and the per-prompt latency would drop significantly (no classifier API call, no regex routing, no model enforcement check).

If the motivation to maintain the project is low, the minimum viable extraction is: copy `destructive-bash-guard.sh` and `scope-guard.sh` into `~/.claude/hooks/` as standalone scripts. These two files alone provide the highest value-to-maintenance-cost ratio. Everything else is nice-to-have.

**Is there enough unique value to maintain the project?** Yes, but only if the dead weight is removed. The current codebase is overengineered for features that no longer function. A focused RunEcho with IR + safety hooks + session discipline + contracts is a legitimate tool. A RunEcho that pretends to route models is a liability.
