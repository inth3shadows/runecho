# Competitive landscape — keeping the moat claim defensible

**Last verified 2026-10-22.** Next re-verification due: 2027-01-22 (quarterly).

> The due date tracks the **22nd** of Jan/Apr/Jul/Oct because that is when the
> scheduled job actually fires (local cron, `~/.local/bin/runecho-moat-reverify`).
> Set it to the 22nd, not to "three months from today" — a due date that disagrees
> with the schedule is its own small lie. An **off-cycle** pass
> moves the *verified* stamps but must leave the *due* date on the cron's next
> firing — an ad-hoc survey does not reschedule the job.

This file exists to stop one sentence in [`runecho-vs-field.html`](./runecho-vs-field.html)
from silently going stale:

> No rival deterministically blocks a pre-write reference to a symbol that should
> be defined inside your own codebase and isn't.

That is a claim about the *field*, not about RunEcho, so it decays without anyone
touching this repo. If it ever stops being true, the honest move is to update the
positioning — not to leave the sentence standing.

### The claim this replaced — falsified, do not restore it

Until the August 2026 pass that sentence read (deliberately written without a
full date — this is fixed provenance and must *not* move with the survey stamps):

> ~~No rival deterministically blocks an undefined symbol pre-write.~~

**That broader claim was falsified on 2026-07-10** by `emilyoprog/anti-halu`
(row below), which satisfies all four conjuncts for *imported third-party*
symbols. The falsification is recorded here permanently, and the narrowing above
is a concession, not a clarification — the field caught up on one slice of the
square and RunEcho no longer owns it.

Two honesty rules follow from that, and they are the whole point of this section:

1. **The four conjuncts below are unchanged.** The *claim* narrowed; the *test*
   did not soften. A future pass that widens the claim back out, or that adds a
   fifth conjunct to make falsification harder, is re-arguing positioning from
   vibes — which is the failure this file exists to prevent.
2. **The narrowed claim carries a narrower conjunct 3** (in-repo, not any
   symbol). That genuinely does make falsification harder, so it is stated
   openly rather than smuggled in: anyone reading the moat claim should know it
   is now scoped to symbols the user's own repository is supposed to define.

## The claim, stated precisely

A rival falsifies it only by satisfying **all four** conjuncts at once:

1. **Pre-write** — intervenes *before* the edit lands on disk, inside the agent loop.
2. **Blocking** — can actually deny the write, not merely advise, warn, or inject
   context the model is free to ignore.
3. **Symbol-existence aware** — decides on whether the referenced symbol *exists*,
   not on whether the agent performed some ritual first. Since the August 2026
   pass (no full date on purpose — fixed provenance, not a survey stamp) the
   claim scopes this to **in-repo** symbols: identifiers the codebase under edit
   is itself supposed to define. A tool that resolves only *imported third-party*
   names satisfies the conjunct in general but not the narrowed claim — and the
   rest of this file must say which of the two it means, every time.
4. **Deterministic / model-free** — the verdict comes from parsing, not from a
   second model's judgement.

Anything that misses one of the four is *adjacent*, not a rival, and belongs in
the watch list rather than in the positioning.

## Survey — 2026-10-22

| Tool | What it actually does | Fires | Falsifies the claim? |
|---|---|---|---|
| **anti-halu** (`emilyoprog`, Python, 0★, no license, single commit 2026-07-10) | **The tool that falsified the old claim.** A `PreToolUse` hook (`Write\|Edit\|MultiEdit\|apply_patch`) that parses the *pending* Python content with `ast`, resolves each name bound by an import, then **imports the module and checks `hasattr`** — `math.square_root` is denied before the write lands. Second check: every non-stdlib top-level import is looked up on PyPI (import-root → distribution metadata, so `cv2`→`opencv-python`). Fails open on any verifier exception, 800 ms budget per distribution. Conservative by construction: skips `import *`, dynamic `importlib`/`getattr`, relative imports, and any name that is reassigned, shadowed by a parameter, or monkeypatched. **Reconfirmed this pass via `gh api repos/emilyoprog/anti-halu` and its tree: still 0★, still the single 2026-07-10 commit, `verifiers/` still holds only `symbol_verifier.py` and `package_verifier.py` — no project-scanning module added.** | **Pre-write (PreToolUse), denies** — `pre_write_check.py` exits 2 with the reason on stderr | **YES for the old claim — the first tool on record to satisfy all four conjuncts.** 1: PreToolUse. 2: exit 2 is a deny, not a warning. 3: it decides on symbol existence. 4: `ast` + `importlib`, no model anywhere. **No for the narrowed claim:** it resolves only names reaching the file through an import, so it cannot see a symbol the repo itself is supposed to define — relative imports are explicitly skipped, and a bare call to an undefined local function is invisible to it. Python-only. **Not a momentum story** (one commit, dormant over three months now), but substance is the test here, not stars — see the packaging note below, which is the part worth stealing. |
| **Gortex** (`zzet/gortex`, 1913★ as of this pass — up from 1090★, Apache-2.0, 257 languages) | **The largest standalone code-intelligence tool in this space (Cursor and Cline, below, are harnesses, not gates), and the shortest path to falsification on record.** A code-intelligence engine (CLI + MCP + API, local) that already owns a full symbol graph with reference resolution. Critically, `internal/mcp/parse_gate.go` **already holds candidate bytes pre-write and parses them with tree-sitter before the atomic swap** — the hard part of the architecture is built and shipped. **Re-fetched from current `main` (pushed the day before this pass) this pass: unchanged.** | Pre-write, on its own MCP write tools | **No — today, and only on a technicality.** Its gate's predicate is still `parseErrorCount`, confirmed by direct source read this pass: it asks whether the pending bytes *parse*, never whether the symbols they reference *resolve* — so conjunct 3 fails. But it already has the graph to answer that, and swapping the predicate is a contained change. Two further concessions that will not last: it guards only its own MCP write tools (its PreToolUse hook predicate is file-path-only, so edits made with the agent's native tools bypass it), and it is bypassable via `allow_parse_errors=true`. **Reach makes this the one to watch** — nearly doubled (1090★ → 1913★) since last pass. |
| **GateGuard** (`gateguard-ai`, zunoworks) | "deny + force investigation + demand evidence" — blocks the first write attempt and makes the agent produce facts (importers, data schemas, quoted instruction) before allowing a retry. Now at **v0.6.2** (PyPI; the 0.6 line has been out since late July 2026, so the August pass's v0.5.0 was already stale): 0.6 adds a `PostToolUse` hook on `Read`/`Grep`/`Glob`/`Bash` that records observed investigation into an evidence ledger, so the gate opens on what the agent actually did rather than on what it claims. Re-confirmed this pass against the current PyPI release (`pypi.org/pypi/gateguard-ai/json`): it still demands evidence-gathering (importers, schemas, instruction quotes via grep/read), never symbol existence. | Pre-write (PreToolUse) | **No.** Nearest neighbour on *timing*, but it never evaluates symbol existence — it evaluates whether the agent looked. Conjunct 3 fails, unchanged by the version bump. |
| ⚠️ **Anti-Hallucination-MCP** (`Akunimal`) | The nearest neighbour on *substance* — and the first tool found that checks the same thing RunEcho checks. Deterministic, model-free symbol existence: AST extraction via `@ast-grep/napi` for JS/TS/TSX, regex fallback for ~15 more languages, symbols persisted to `.wisdom/symbols.json`, confidence-scored with fuzzy typo matching. Ships a Claude Code hook that its own setup script installs. Also bundles command-output compression (dedup/grouping) — so one repo overlaps both RunEcho *and* terse. **Could not be re-located this pass:** a GitHub repo-name search, a search of Akunimal's own repos, and a code search for its `ANTIHALL_AUTOFIX` signature all returned nothing this pass, and `gh api repos/Akunimal/Anti-Hallucination-MCP` is a 404. No URL was ever recorded for this row — the resolvable-identifier rule below postdates it — so this verdict is **carried forward unverified, not reconfirmed.** It is kept one more pass only because it is the historical half of the three-way split below; **if the next pass cannot re-locate it either, remove it** under the same rule that removed the early-August names. | **Post-write** (as last verified) — the installed hook is `PostToolUse` on `Write`/`Edit`; it "warns after every edit", writing to stderr *after* the write has landed | **No — but it was the closest miss on record.** Satisfies conjuncts 3 (symbol existence) and 4 (deterministic, no LLM) outright. Fails 1 and 2: the edit is already on disk when it fires, so it advises rather than denies. Two further concessions RunEcho does not make: a persisted registry that *can* go stale (mitigated by an opt-in `watch_project` watcher, not by re-parsing), and an `ANTIHALL_AUTOFIX=1` mode that rewrites the identifier in place — a repair posture, not a gate. **Its falsification path is one line of JSON** (see watch list) — unverifiable this pass whether that line has been written. |
| **Cursor hooks** | **Changed since last pass.** The hook surface now documents a generic `preToolUse` hook — "called before any tool execution... fires for all tool types (Shell, Read, Write, MCP, Task, etc.)" — which can return `"permission": "deny"` to block the call. Its matcher values are `Shell`, `Read`, `Write`, `Grep`, `Delete`, `Task` and `MCP:<tool>`; `Write` is the value the docs also give for agent file edits (`afterFileEdit` is "matched against the value `Write`"), so agent edits are covered. **Tab edits are not**: they have only the observational `afterTabFileEdit`. Two limits on conjunct 2: `failClosed` defaults to `false` (a hook that crashes or times out lets the write through), and `"ask"` is "accepted by the schema but not enforced for `preToolUse` today" — only an explicit deny blocks. **And Cursor loads Claude Code hooks**: with *Include Third-Party Plugins, Skills, and Other Configs* (on by default) it reads `.claude/settings.json`, maps `PreToolUse` → `preToolUse` and both `Write` and `Edit` → `Write`, and treats exit 2 as a deny "for compatibility". Source: <https://cursor.com/docs/hooks.md> and <https://cursor.com/docs/reference/third-party-hooks.md>, read this pass. Documented, not exercised: no Claude Code hook was run under Cursor. | `preToolUse` fires before agent writes (not Tab edits) and can deny | **No — but the "structurally can't" framing from the last two passes is retracted as false.** Cursor's agent edit tool *can* be gated pre-write today. What's missing is substance, not architecture: no tool is known to run an in-repo symbol-existence check inside `preToolUse`. Because Claude Code `PreToolUse` gates are imported by default, **any Claude Code gate is, per the docs, also a Cursor gate** — so the first in-repo Claude Code gate by anyone would contest the claim on both harnesses at once. (Issue #174, now closed, was scoped on the old premise that a Cursor port could only cover `beforeShellExecution` / `beforeMCPExecution`; the follow-up is #543.) |
| **Cline SDK hooks** | **New row this pass.** Cline's plugin docs describe a `beforeTool` hook — "before a tool runs, with the `tool`, `toolCall`, and `input`" — that can return `{ skip: true, reason }` to block the call before it executes. Plugins run "in the compiled CLI, the hub, and the desktop app", so this is not SDK-only. Source: <https://docs.cline.bot/sdk/plugins.md>, read this pass. The page's examples are a database query and an API call; **it does not name Cline's file-edit tools**, so that edits route through `beforeTool` is inferred from "before a tool runs", not documented. This tool was previously folded into the "Cline / Continue / Cody" catch-all row below without being checked on its own, which is how its hook docs were missed until this pass. | `beforeTool` fires before any tool call (edits inferred, not documented) | **No — same shape as Cursor/Gortex.** If edits do pass through `beforeTool`, the hook can block a write before it lands (conjuncts 1 and 2). No plugin was found that uses it to check symbol existence — but no plugin search was run for Cline specifically (see procedure step 1), so this negative is "not found", not "does not exist". |
| **pyright-lsp** (official Anthropic Claude Code plugin) | Runs Pyright and surfaces LSP diagnostics into the conversation as `<new-diagnostics>`. New first-party entrant since the last positioning pass. | After the edit | **No.** Advisory, post-write, Python-only, needs a language server — every one of these is something the moat sentence already concedes to LSPs. Worth watching: it makes "just use an LSP" cheaper inside Claude Code itself. |
| **Serena** (oraios) | LSP-backed semantic MCP: find-references, rename, symbol-level edits, 40+ languages. | On request, during the loop | **No.** Deeper than RunEcho on every axis except timing and setup; it is a tool the agent *chooses* to call, not a gate that fires whether or not the agent cooperates. |
| **Aider repo map** | Tree-sitter + PageRank ranking of important symbols, packed into the context budget. | Context supply, pre-generation | **No.** Supplies context; validates nothing. Fails conjuncts 2 and 3 by design — the repo map's job is to make the model *better informed*, never to overrule it. |
| **Continue / Cody** | No pre-write symbol-validation gate found this round. Continue has pivoted toward a CLI-first platform with `.continue/checks/` running as GitHub status checks on PRs — merge-time, not pre-write. | — | **No.** Note the evidence here is weaker than for Cursor or Cline above: those tools publish an exhaustive hook/event list, so a negative there is documented; for Continue and Cody the negative is only "not found". |
| **Shipmoor** | Described as a local deterministic verification layer producing a binding merge verdict. | Merge time | **No.** Wrong end of the pipeline entirely. *Secondary source only — not primary-verified.* |

**Verdict 2026-10-22: no new falsification. The narrowed claim still holds.**
`anti-halu` and Gortex are both unchanged this pass (reconfirmed directly against
current source, see table). Anti-Hallucination-MCP could not be re-located and is
carried forward unverified. The one real move this pass is architectural, not a
new rival: **Cursor and Cline have each shipped a generic pre-write blocking hook**
(`preToolUse` and `beforeTool` respectively) that closes the specific gap this
file previously cited — "Cursor's hook surface can't host such a gate for its
native edit tool" is now false for agent edits (Tab edits remain ungated) and
has been corrected in both files. No rival is
known to run a symbol-existence predicate inside either hook, so the four-conjunct
claim does not fall; it is the same shape of gap as Gortex (architecture built,
predicate missing).

**The claim is more exposed than that sentence alone suggests.** Cursor imports
Claude Code `PreToolUse` hooks by default, so the first in-repo symbol gate anyone
ships for Claude Code would, per Cursor's docs, contest the claim on both harnesses
at once. The falsification no longer needs a port — only a predicate.

The prior (August) pass's finding, unchanged since and preserved here because it
is still why the narrowed claim reads the way it does: last pass predicted the
square would be closed by someone merging GateGuard's timing with
Anti-Hallucination-MCP's substance. That is exactly what happened, except it was
neither of them and it had already shipped: `anti-halu` was pushed 2026-07-10,
thirteen days *before* the pass that declared the claim safe. So the position
moved twice at once, that pass:

- **The general pre-write symbol gate is no longer unclaimed.** One tool holds
  all four conjuncts. It is dormant and Python-only, but the square is occupied
  and the positioning must stop saying otherwise.
- **And the field is larger than any single pass had seen.** That pass also added
  **Gortex** (1090★ then), found in a separate strategy pass and never recorded here,
  which already holds candidate bytes pre-write and parses them before the
  atomic swap, and fails only on *which predicate* it applies. See "three
  surveys, three disjoint sets" below; the completeness of any pass is now
  itself in question.
- **The in-repo pre-write symbol gate is still unclaimed.** `anti-halu` resolves
  only what arrives through an import; it never asks whether the codebase under
  edit defines the thing being called. Nothing surveyed does. That is the
  narrowed claim, and it is the honest remainder.

The three-way split of the field is now worth stating plainly, because each holds
a different piece and the pieces are individually cheap:

- **GateGuard** — pre-write and blocking (1, 2), never asks whether a symbol exists.
- **Anti-Hallucination-MCP** — deterministic symbol existence (3, 4), only after
  the write has landed.
- **anti-halu** — all four, but only for symbols that came from an import.

Read that as a warning, not a scoreboard. Three separate 0-star projects
independently built three different faces of this gate inside one quarter. The
technique is not scarce; what is still scarce is doing it for the user's own
codebase, across languages, without a persisted index that can lag.

### Why the August pass missed it — and what fixed it

The July pass found Anti-Hallucination-MCP only via a raw GitHub repo-search, and
correctly wrote down that the procedure needed one. That bullet was added to the
procedure but the sweep was never actually run as a distinct step, so the *same*
failure repeated one pass later: `anti-halu` has no stars, no license, no
release, no blog post, no changelog — nothing a docs-and-web survey can see. A
single query found it immediately:

```
gh api -X GET search/repositories -f q='pre-write hallucination gate' -f sort=updated
```

The August pass therefore did two things: recorded the falsification, and promotes the
raw sweep from a note inside step 1 into its own numbered, multi-query step
(closed issue #203). **A survey pass that does not run the sweep has not run.**

### The harder finding: three surveys, three disjoint sets

Fixing the sweep is necessary and not sufficient. There have now been **three
competent surveys of this niche inside three weeks, and they share essentially
zero entries**:

(Pass labels below are written without full dates on purpose — they are fixed
provenance and must not move with the survey stamps.)

| Pass | Method | Found |
|---|---|---|
| July 2026 | product docs, changelogs, web, one repo query | GateGuard, Anti-Hallucination-MCP, Cursor hooks, pyright-lsp, Serena, Aider, Cline/Continue/Cody, Shipmoor |
| Early August 2026 | separate strategy pass | **Gortex** — plus ten further products reported as verified but recorded only by bare product name, none of which resolves today (see note below). **None overlapped the row above.** |
| Later August 2026 | six-query raw repo sweep | `anti-halu` — **overlapping neither**, and the one that falsified the claim |

The intuitive reading is "the search protocol is unstable, so search harder."
That is probably too comfortable. Three different methods, run by different
processes within three weeks, producing three near-disjoint sets is better
explained by the niche itself: **it is being built independently by many small
actors with no marketing surface, faster than any quarterly enumeration can
track.** Under that reading a completeness claim about the field is not merely
hard to maintain — it is not maintainable, and the honest response is to keep
the claim narrow, keep it dated, and treat every pass as a lower bound on the
field rather than a census.

State that limitation wherever the claim is printed. A claim that cannot be
verified should not be printed as though it had been, even on the passes where
it happens to be true.

> **Why that middle row names only one product** (no full date in this note on
> purpose — fixed provenance, not a survey stamp). The early-August pass
> reported eleven verified competitors, but recorded them **by bare product name
> only**. Just one, Gortex, could be re-verified; it was checked directly for
> this pass (stars, licence, and the presence of `internal/mcp/parse_gate.go`)
> and has a full row above.
>
> The rest were **removed rather than carried**. Their names do not resolve to
> anything in this niche — on PyPI the reported names belong to a file
> downloader, a nested-dict helper, a DEM-comparison tool and a CrewAI
> monitoring library, and one is absent entirely; on GitHub one name alone
> matches two unrelated plausible candidates. An entry that cannot be resolved
> to a specific project cannot be re-checked, cannot be falsified, and would sit
> in the table implying a verification this file cannot point at. That is worse
> than an honest gap, so it is not kept.
>
> **The lesson is procedural: a survey row must carry a resolvable identifier —
> repo slug, PyPI package, or URL — recorded at the moment the verdict is
> written.** A product name alone is not a citation. It was sufficient for the
> pass that wrote it and worthless two days later, which cost this file ten
> entries.

### Distribution, not just capability

`anti-halu` ships as a **plugin marketplace root for both Claude Code and Codex**
(`.claude-plugin/plugin.json`, `.codex-plugin/plugin.json`,
`.agents/plugins/marketplace.json`), so installing it is `claude plugin install`
— no hand-editing of a settings file. RunEcho still asks users to wire a hook
themselves. That is not a conjunct and it does not affect the claim, but it is
the one thing in this survey a dormant 0-star repo does better, and it should be
recorded as such rather than quietly ignored because the competitor is small.

## Watch list — the credible paths to falsification

Now that the general claim has fallen, these are paths to falsifying the
**narrowed** one: a pre-write, blocking, deterministic gate on symbols the
repository under edit is supposed to define.

- **Gortex swapping its `parse_gate` predicate from "does it parse" to "does it
  resolve".** The highest-impact path among standalone tools, because it has
  the most reach of any of them (1913★ and still climbing — up from 1090★ last
  pass). It already holds candidate bytes pre-write, already parses them before the atomic swap,
  and already owns a symbol graph with reference resolution — the three
  expensive pieces. What stands between it and all four conjuncts is the
  predicate itself, plus widening the gate beyond its own MCP write tools.
  **Check `internal/mcp/parse_gate.go` every pass**, specifically what
  `parseErrorCount` is compared against; a resolve-based predicate landing there
  falsifies the narrowed claim outright and does so in front of a real audience.
- **`anti-halu` extending from imported names to in-repo definitions.** The
  cheapest path on the board, and the current holder of the general claim. Its
  `symbol_verifier.py` already walks the pending file's AST and already has the
  binding/shadowing machinery; what it lacks is any notion of the *rest of the
  repo*. Adding "collect defs from the project tree, flag bare calls that match
  nothing" is a contained change to a file that already exists. Against that,
  weigh what its design would have to give up: it resolves by **actually
  importing the module**, which is why it is exact for third-party APIs and why
  it cannot cheaply generalise to a repo that may not import cleanly, or to any
  language but Python. **Check `verifiers/` for a project-scanning module every
  pass.** Dormant since its single commit — but a dormant repo can wake up in one
  push, and this one needs one file.
- **Anti-Hallucination-MCP moving its hook from `PostToolUse` to `PreToolUse`**
  (⚠️ unverifiable while the repo cannot be re-located — see its row). If it
  resurfaces, this is a one-line path: the analysis is already deterministic and already symbol-existence-based,
  so the remaining work is changing the event name in the settings block its
  setup script writes, and returning a deny decision instead of a stderr warning.
  Everything the four-conjunct test asks for would then be satisfied. Weigh that
  against the two things it would still concede — a persisted registry that can
  lag the working tree, and JS/TS-only AST fidelity with regex elsewhere — but
  do not mistake those for a moat. **Re-search for the repo every pass; if found,
  read its hook wiring, not just its changelog** — it is a 0-star project with no
  release notes habit, so the change would ship silently.
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
- **RESOLVED this pass, watch what's built on it: Cursor's `preToolUse` and
  Cline's `beforeTool`.** The bullet that used to read "Cursor shipping a
  `beforeFileEdit` event" is overtaken — Cursor now documents a generic
  `preToolUse` hook (matcher covers `Write`, can return `permission: "deny"`)
  and Cline's SDK docs a `beforeTool` hook (`skip: true` blocks the call), both
  read directly from each tool's current docs this pass. The empty square is
  now buildable in Cursor and Cline, and on Cursor it needs not even a port:
  Cursor imports Claude Code `PreToolUse` hooks by default, so a Claude Code
  in-repo gate is a Cursor gate. **Check whether any plugin or Claude Code hook
  wires a deterministic in-repo symbol-existence check into either surface every
  pass** (procedure step 1) — that is now the highest-leverage path to
  falsification overall, ahead of Gortex, because it requires no new
  infrastructure, only a predicate.
- **A first-party LSP-backed pre-write gate.** `pyright-lsp` today only injects
  diagnostics after the fact; wiring the same language server into a blocking
  PreToolUse decision would satisfy all four conjuncts for one language at a time,
  at the cost of the setup and staleness RunEcho trades away.
- **tribunal-kit** (`Harmitx7/tribunal-kit`, 5★, MIT, npm `tribunal-kit`,
  new in the October 2026 pass, found via the raw repo sweep). Its README
  describes "31 domain-specific reviewers" analyzing generated code "before
  writing to disk," gated by a Rust core doing AST/graph extraction, and calls
  the swarm deterministic. **Read from its hook wiring this pass, not its
  README:** both `.claude/hooks.json` and `hooks/hooks.json` register only
  `SessionStart`. Its blocking exists only on its own MCP write path
  (`tribunal_request_write` / `tribunal_commit_write`) and a git pre-push hook,
  so edits through the agent's native tools are never gated — conjuncts 1 and 2
  fail for native edits, the same concession as Gortex. Its AST check is
  imports against package manifests (phantom packages), not in-repo symbol
  existence, so the narrowed conjunct 3 fails too. (An earlier draft of this
  entry dismissed it on conjunct 4 from a CHANGELOG line about LLMs not
  self-authorizing findings; that line actually says an *engine* grants the
  verdict, so it was the wrong basis and is withdrawn.) Watch for its native
  hooks gaining a `PreToolUse` entry.
- **nio-xs** (`nio-labs/nio-xs`, new in the October 2026 pass) — an
  OpenAI-compatible gateway/companion whose `--code guard` directive, per the
  README at commit `3c613965`, "verifies generated code against Rust,
  TypeScript, Python, and Go AST symbols" and "prompts to reroute" on a miss.
  The one new candidate this pass with a plausible conjunct-3 claim. As
  described it is an on-demand directive the agent must choose to invoke (fails
  conjunct 1 as a *gate*), and "prompts to reroute" reads as advisory (conjunct
  2 unconfirmed). **Scored from the README only, and that sentence has since
  been removed from README HEAD** — so the next pass must read its source, per
  procedure step 1, before any verdict is final.

## Deciding what to add

The four conjuncts above also gate *inbound* feature requests, not just rival
surveys: "can RunEcho also catch X?" is answered by the same test, plus a
decidability check, a prior-art check, and a measured evidence bar. That rubric
lives in [check-worthiness.md](check-worthiness.md) — it is what stops the next
"add a check for X" from being re-argued from intuition (the failure mode behind
#175 and #204).

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
- **vouch** (`sunnyadn/vouch`, surfaced in the October 2026 pass — written
  without a full date for the same reason as above) — a Stop-hook reviewer that
  checks an agent's *claims* ("there's no library for X", "all tests pass")
  against its recorded trace of tool calls. Explicitly passive and end-of-turn
  by its own README, not pre-write. Fails conjunct 1 outright, and conjunct 3 —
  it grades truthfulness of statements, not symbol existence. Different lane
  entirely; author's own numbers describe it as an unreleased research prototype
  with an LLM reviewer that currently false-positives too often to run.
- **DCGP** (`addicted2crypto/DCGP`, surfaced in the October 2026 pass) — a
  context/entropy governance layer that predicts conversational drift and prunes
  stale context; ships a separate static-analysis add-on (`@dcgp/vibe-audit`)
  for AI-code-smell patterns (stub markers, hardcoded credentials, etc.), which
  is lint-time, not pre-write. Neither half checks whether a referenced symbol
  exists. Fails conjunct 3; wrong lane.

## Re-verification procedure

Quarterly, or immediately if a major harness announces new hook events.

1. Re-run these searches and read primary docs, not summaries:
   - `Claude Code PreToolUse hook block undefined symbol hallucination guard`
   - Cursor: read <https://cursor.com/docs/hooks.md> (every `before*` and
     `preToolUse` event, its matcher values, `failClosed` and `ask` handling) and
     <https://cursor.com/docs/reference/third-party-hooks.md> (whether Claude Code
     `PreToolUse` hooks are still imported, and by default)
   - Cline: read <https://docs.cline.bot/sdk/plugins.md> for `beforeTool`, and
     whether the file-edit tools are now named as routing through it
   - **Plugins on those hooks**, which the six-query sweep below does not reach:
     `gh api -X GET search/code -f q='preToolUse symbol'`, the same for
     `beforeTool`, and the Cursor and Cline plugin listings — a deterministic
     in-repo symbol check on either surface is now the top falsification path
   - `MCP server validate symbol exists before write`
   - `deterministic AST hallucination detection LLM code` — track whether the
     arXiv line of work has shipped a hook
   - Changelogs: GateGuard, Serena, Aider, Cline, Continue.
   - For every candidate that checks symbol existence, **read the hook wiring
     itself** (`PreToolUse` vs `PostToolUse`, deny vs stderr) rather than the
     README's adjectives. "Guard", "blocks", and "prevents" are used loosely in
     this space; the event name is not.
2. **Run the raw GitHub repo sweep. This step is not optional and is not
   satisfied by step 1.** It has its own number because it was a sub-bullet of
   step 1 for one cycle and was skipped, which is how `anti-halu` was missed for
   four weeks after being written down as the exact profile to look for. (No date
   in this step on purpose — it is a permanent procedural note and must not move
   with the survey stamps.) The competitors in this space have 0 stars, no
   license, no release and no blog post; the web cannot see them.

   Run **every** query, not just the one that worked last time — each phrasing
   returns a nearly disjoint set, and single-query sweeps are how the misses
   happen:

   ```
   for q in 'hallucination symbol claude-code' \
            'pre-write hallucination gate' \
            'PreToolUse block edit' \
            'anti-hallucination coding agent' \
            'ast verify agent generated code' \
            'symbol existence verification LLM'; do
     echo "=== $q ==="
     gh api -X GET search/repositories -f q="$q" -f sort=updated -f per_page=10 \
       --jq '.items[] | "\(.stargazers_count)★ \(.full_name) pushed=\(.pushed_at[0:10]) — \(.description)"'
   done
   ```

   Any repo whose description mentions gating, blocking or verifying generated
   code goes to step 3 — **including dormant ones**. `anti-halu` had a single
   commit and zero stars and still falsified the claim; momentum is not the test.
3. Score each candidate against the four conjuncts above. Three out of four is a
   watch-list entry, not a falsification. For conjunct 3, record explicitly
   whether it resolves **in-repo** symbols or only imported third-party ones —
   that distinction is now what separates a watch-list entry from a rival.

   **Every row must carry a resolvable identifier** — repo slug, PyPI package
   name, or URL — written down at the same time as the verdict. A bare product
   name is not a citation. A row whose project cannot be re-located is marked
   ⚠️ and carried at most one more pass, then removed — the early-August pass
   recorded ten verdicts against names that could not later be resolved to any
   specific project, and those verifications were unrecoverable.
4. Update the table **and every dated string in both files** — there are more of
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

   Everything else in these two files that looks like a date is **fixed
   provenance** — when a rival shipped, when a claim fell — and must stay put
   while the five stamps move. Provenance that could collide with a survey date
   is written as "the August 2026 pass" rather than as a full ISO date, for
   exactly this reason. If you add a real stamp, register it in this list; if you
   add provenance, keep it out of the grep.
5. If something does satisfy all four: rewrite the positioning honestly in the
   same pass. A stale moat claim costs more credibility than a conceded one. If
   it satisfies all four for a *narrower* slice than the claim covers, narrow the
   claim and **record the falsification permanently** (see the struck-through
   claim near the top) — never quietly re-scope the sentence so it reads as
   though it always meant the narrower thing.
