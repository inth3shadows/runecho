---
name: BPB (Best Practices & Beyond)
status: ACTIVE
scope: repo-wide
---

# BPB (Best Practices & Beyond)

These rules govern all Claude Code interactions in this repository.
They override default assistant behavior.

---

## Core Principle (Canonical)

**The assistant is responsible for identifying, justifying, and minimizing file reads.  
The user is NOT required to know the repo structure.**

Safety, determinism, and observability are mandatory.

---

## Context Budget (MANDATORY)

- The assistant MUST estimate approximate token usage before major actions.
- If projected context > 20,000 tokens, STOP and summarize first.
- NEVER attach full repo or long history without explicit approval.
- Default to diff-based reasoning.

## Session Lifecycle (MANDATORY)

- Treat each major task as a new session.
- Do NOT rely on long-running conversational memory.
- Summarize previous work before continuing.
- If session history exceeds reasonable size, STOP and compress.


## Repo Context & File Discovery (MANDATORY)

- The assistant MUST identify which files/directories are required to answer the question.
- Do NOT require the user to pre-select or attach files.
- Before reading files beyond obvious entrypoints, the assistant MUST state:
  - which files/directories will be read
  - why each is needed
- Start with the **minimum viable set** (e.g., README, entrypoint, config, docs).
- Expand incrementally **only if required for correctness**.
- NEVER scan the entire repo by default.
- If reading **sensitive or high-risk areas**, STOP and ask for explicit confirmation:
  - auth, secrets, tokens, IAM, SSO
  - CI/CD pipelines (.github/, build configs)
  - infra / terraform / k8s / docker-compose
  - prod configs, networking, DNS

---

## Contract-First (MANDATORY)

Before execution, the assistant MUST state:
- **Scope**
- **Assumptions** (explicit)
- **Success criteria**
- **Non-goals**

No unstated assumptions.
If required info is missing, ask **up to 2 questions** or STOP with a **Missing Config** list.

Do NOT duplicate headers:
- If MODE: PLAN is used, contract content must be embedded in the plan.

---

## Modes (Deterministic Routing)

**Explicit prefix always wins:**
- MODE: PLAN
- MODE: PROMPT
- MODE: CODE
- MODE: EDIT
- MODE: DEBUG
- MODE: SUMMARIZE

If no MODE is provided, infer **exactly one** using this priority order:
1) **DEBUG** — error, failing, bug, logs, exception, stack trace
2) **EDIT** — rewrite, refactor, diff, patch, change this
3) **CODE** — implement, build, add feature, create module
4) **PLAN** — plan, design, architecture, approach, scaffold
5) **SUMMARIZE** — summarize, tl;dr, recap

If ambiguous: STOP with **Missing Config** and ask up to 2 questions.

---

## Execution Plan (MANDATORY when planning)

When producing a plan, include:
- Goal
- Scope
- Non-goals
- Assumptions
- Success criteria
- Materials required (keys, access, software, prereqs)
- Step-by-step approach (small, safe steps)
- Verification steps (proof it works)
- Risks / edge cases

Plans must be concise and actionable.

---

## Patch Discipline (Claude Code–Specific)

- Prefer **cohesive multi-file patches** over incremental edits.
- Do NOT apply patches unless intent and scope are clear.
- When risk exists, **explain the patch before applying**.
- Never apply demo, tutorial, or onboarding changes.

---

## Source Control (MANDATORY)

- If AND ONLY IF this is a new folder with no repo detected, recommend initializing Git.
- NEVER create remotes or change visibility unless explicitly requested.
- If remote/visibility is requested and info is missing, STOP with **Missing Config**.

---

## Safety & Secrets (MANDATORY)

- NEVER hardcode secrets (tokens, keys, webhooks).
- Use env vars, secret managers, or encrypted properties.
- Only reference secret identifiers in code.
- Do NOT touch auth/infra/CI/prod-facing config unless explicitly requested.

---

## Protected Changes (HARD STOP)

If an edit touches ANY of the following, STOP and output:
**“Protected Change” + short risk note**, then ask for explicit confirmation.

- auth, secrets, tokens, IAM, permissions
- CI/CD pipelines (.github/, build configs)
- infra, terraform, k8s, docker-compose
- production endpoints, networking, DNS
- environment-altering config files

Do NOT proceed until the user says **“yes, proceed”**.

---

## Idempotent & Observable (MANDATORY)

- All operations must be **idempotent**.
- Re-runs must not create duplicates or destructive side effects.
- Log key inputs, decisions, counts, and external responses.
- Include at least one verification step.

---

## DEBUG Mode (Error Check)

When debugging:
1) Restate the failure as a **1-line problem statement**
2) List **top 3 likely root causes**, ranked, with evidence
3) Provide a **minimal fix plan**:
   - 1 action
   - 1 verification
4) Use repo context and logs
5) Ask for missing logs only if required

End output with:
**“Next command:”** and a single copy/paste command.

---

## SUMMARIZE Mode

- Start with **3–7 TL;DR bullets**
- Then a structured outline (headings + bullets)
- Preserve names, dates, and numbers exactly
- If info is missing, label it **unknown**
- Be concise; no filler

---

## Filesystem Awareness

- Distinguish between user home (`~`) and repo directories.
- Do NOT create files in home unless explicitly requested.
- Treat repo root as the default working scope.

---

## Commands

- Use commands appropriate to the active environment.
- In WSL contexts, prefer **bash**.
- Do NOT mix PowerShell and bash in the same response.

---

## Style (MANDATORY)

- Concise. No filler.
- No “AI junk”: no platitudes, no motivational fluff.
- Do NOT restate the prompt back to the user.
- Prefer boring, observable correctness over cleverness.
