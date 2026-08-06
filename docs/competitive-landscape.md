# Competitive landscape — keeping the moat claim defensible

**Last verified 2026-08-06.** Next re-verification due: 2026-10-22 (quarterly).

> The due date tracks the **22nd** of Jan/Apr/Jul/Oct because that is when the
> scheduled job actually fires (local cron, `~/.local/bin/runecho-moat-reverify`).
> Set it to the 22nd, not to "three months from today" — a due date that disagrees
> with the schedule is its own small lie. An **off-cycle** pass (like this one)
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

## Survey — 2026-08-06

| Tool | What it actually does | Fires | Falsifies the claim? |
|---|---|---|---|
| **anti-halu** (`emilyoprog`, Python, 0★, no license, single commit 2026-07-10) | **The tool that falsified the old claim.** A `PreToolUse` hook (`Write\|Edit\|MultiEdit\|apply_patch`) that parses the *pending* Python content with `ast`, resolves each name bound by an import, then **imports the module and checks `hasattr`** — `math.square_root` is denied before the write lands. Second check: every non-stdlib top-level import is looked up on PyPI (import-root → distribution metadata, so `cv2`→`opencv-python`). Fails open on any verifier exception, 800 ms budget per distribution. Conservative by construction: skips `import *`, dynamic `importlib`/`getattr`, relative imports, and any name that is reassigned, shadowed by a parameter, or monkeypatched. | **Pre-write (PreToolUse), denies** — `pre_write_check.py` exits 2 with the reason on stderr | **YES for the old claim — the first tool on record to satisfy all four conjuncts.** 1: PreToolUse. 2: exit 2 is a deny, not a warning. 3: it decides on symbol existence. 4: `ast` + `importlib`, no model anywhere. **No for the narrowed claim:** it resolves only names reaching the file through an import, so it cannot see a symbol the repo itself is supposed to define — relative imports are explicitly skipped, and a bare call to an undefined local function is invisible to it. Python-only. **Not a momentum story** (one commit, dormant since), but substance is the test here, not stars — see the packaging note below, which is the part worth stealing. |
| **Gortex** (`zzet/gortex`, 1090★, Apache-2.0, 257 languages) | **The largest actor in this space by three orders of magnitude of reach, and the shortest path to falsification on record.** A code-intelligence engine (CLI + MCP + API, local) that already owns a full symbol graph with reference resolution. Critically, `internal/mcp/parse_gate.go` **already holds candidate bytes pre-write and parses them with tree-sitter before the atomic swap** — the hard part of the architecture is built and shipped. | Pre-write, on its own MCP write tools | **No — today, and only on a technicality.** Its gate's predicate is `parseErrorCount`: it asks whether the pending bytes *parse*, never whether the symbols they reference *resolve* — so conjunct 3 fails. But it already has the graph to answer that, and swapping the predicate is a contained change. Two further concessions that will not last: it guards only its own MCP write tools (its PreToolUse hook predicate is file-path-only, so edits made with the agent's native tools bypass it), and it is bypassable via `allow_parse_errors=true`. **Reach makes this the one to watch** — 1090★ and pushed the day of this survey. |
| **Roam** | Has the resolver, but fires on a **Stop hook, after the bytes have landed**. Python-only and env-gated. | Post-write (Stop) | **No.** Fails conjuncts 1 and 2 — second-shortest path to falsification after Gortex, since the analysis half already exists. |
| **Snag / Plumbline / agent-guard / Neurcode** | Four separate small entrants in the same niche, run through the four conjuncts in the 2026-08-04 pass by reading their actual hook wiring rather than their marketing. None satisfies all four. | Various | **No.** Recorded for completeness and to stop re-adjudication. Note the momentum signal rather than the capability one: Snag, Plumbline and agent-guard each **published every release on a single day and never shipped again**. Abandoned, but they existed — which is the point of the entry. |
| **GateGuard** (`gateguard-ai`, zunoworks) | "deny + force investigation + demand evidence" — blocks the first write attempt and makes the agent produce facts (importers, data schemas, quoted instruction) before allowing a retry. Now at v0.5.0 (PyPI, released 2026-04-24; up from v0.4.1) — adds condensed fact-force denials for long sessions on top of the earlier opt-in bughunt gate. Behavior re-confirmed this pass directly from the PyPI project page: it still demands evidence-gathering (importers, schemas, instruction quotes via grep/read), never symbol existence. | Pre-write (PreToolUse) | **No.** Nearest neighbour on *timing*, but it never evaluates symbol existence — it evaluates whether the agent looked. Conjunct 3 fails, unchanged by the version bump. |
| **Anti-Hallucination-MCP** (`Akunimal`) | The nearest neighbour on *substance* — and the first tool found that checks the same thing RunEcho checks. Deterministic, model-free symbol existence: AST extraction via `@ast-grep/napi` for JS/TS/TSX, regex fallback for ~15 more languages, symbols persisted to `.wisdom/symbols.json`, confidence-scored with fuzzy typo matching. Ships a Claude Code hook that its own setup script installs. Also bundles command-output compression (dedup/grouping) — so one repo overlaps both RunEcho *and* terse. | **Post-write** — the installed hook is `PostToolUse` on `Write`/`Edit`; it "warns after every edit", writing to stderr *after* the write has landed | **No — but it is the closest miss on record.** Satisfies conjuncts 3 (symbol existence) and 4 (deterministic, no LLM) outright. Fails 1 and 2: the edit is already on disk when it fires, so it advises rather than denies. Two further concessions RunEcho does not make: a persisted registry that *can* go stale (mitigated by an opt-in `watch_project` watcher, not by re-parsing), and an `ANTIHALL_AUTOFIX=1` mode that rewrites the identifier in place — a repair posture, not a gate. **Its falsification path is one line of JSON** (see watch list). |
| **Cursor hooks** | Hook surface enumerated in the docs: `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt` can allow/deny; `afterFileEdit` / `afterTabFileEdit` are observational only. Re-checked this pass against <https://cursor.com/docs/hooks.md> directly: still no `beforeFileEdit` or equivalent pre-edit-for-native-edits event. | `beforeReadFile` pre-read; native edits **post-write only** | **No — and structurally can't today.** There is no `beforeFileEdit` event, so an edit made with Cursor's *native* edit tool cannot be gated before it lands. (Also relevant to issue #174: a Cursor port could only cover edits routed through shell or an MCP filesystem server, via `beforeShellExecution` / `beforeMCPExecution` — that is partial coverage of the thing agents actually do, not a port.) |
| **pyright-lsp** (official Anthropic Claude Code plugin) | Runs Pyright and surfaces LSP diagnostics into the conversation as `<new-diagnostics>`. New first-party entrant since the last positioning pass. | After the edit | **No.** Advisory, post-write, Python-only, needs a language server — every one of these is something the moat sentence already concedes to LSPs. Worth watching: it makes "just use an LSP" cheaper inside Claude Code itself. |
| **Serena** (oraios) | LSP-backed semantic MCP: find-references, rename, symbol-level edits, 40+ languages. | On request, during the loop | **No.** Deeper than RunEcho on every axis except timing and setup; it is a tool the agent *chooses* to call, not a gate that fires whether or not the agent cooperates. |
| **Aider repo map** | Tree-sitter + PageRank ranking of important symbols, packed into the context budget. | Context supply, pre-generation | **No.** Supplies context; validates nothing. Fails conjuncts 2 and 3 by design — the repo map's job is to make the model *better informed*, never to overrule it. |
| **Cline / Continue / Cody** | No pre-write symbol-validation gate found this round. | — | **No.** Note the evidence here is weaker than for Cursor: Cursor publishes an exhaustive hook list, so its negative is documented; for these the negative is only "not found". |
| **Shipmoor** | Described as a local deterministic verification layer producing a binding merge verdict. | Merge time | **No.** Wrong end of the pipeline entirely. *Secondary source only — not primary-verified.* |

**Verdict 2026-08-06: the old claim is FALSIFIED. The narrowed claim holds.**
Last pass predicted the square would be closed by someone merging GateGuard's
timing with Anti-Hallucination-MCP's substance. That is exactly what happened,
except it was neither of them and it had already shipped: `anti-halu` was pushed
2026-07-10, thirteen days *before* the pass that declared the claim safe.

So the position moved twice at once:

- **The general pre-write symbol gate is no longer unclaimed.** One tool holds
  all four conjuncts. It is dormant and Python-only, but the square is occupied
  and the positioning must stop saying otherwise.
- **And the field is far larger than any single pass had seen.** This pass also
  folds in six rivals verified elsewhere and never recorded here — including
  **Gortex** (1090★), which already holds candidate bytes pre-write and parses
  them before the atomic swap, and fails only on *which predicate* it applies.
  See "three surveys, three disjoint sets" below; the completeness of any pass
  is now itself in question.
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

### Why the last pass missed it — and what fixed it

Last pass found Anti-Hallucination-MCP only via a raw GitHub repo-search, and
correctly wrote down that the procedure needed one. That bullet was added to the
procedure but the sweep was never actually run as a distinct step, so the *same*
failure repeated one pass later: `anti-halu` has no stars, no license, no
release, no blog post, no changelog — nothing a docs-and-web survey can see. A
single query found it immediately:

```
gh api -X GET search/repositories -f q='pre-write hallucination gate' -f sort=updated
```

This pass therefore does two things: records the falsification, and promotes the
raw sweep from a note inside step 1 into its own numbered, multi-query step
(closes issue #203). **A survey pass that does not run the sweep has not run.**

### The harder finding: three surveys, three disjoint sets

Fixing the sweep is necessary and not sufficient. There have now been **three
competent surveys of this niche inside three weeks, and they share essentially
zero entries**:

(Pass labels below are written without full dates on purpose — they are fixed
provenance and must not move with the survey stamps.)

| Pass | Method | Found |
|---|---|---|
| July 2026 | product docs, changelogs, web, one repo query | GateGuard, Anti-Hallucination-MCP, Cursor hooks, pyright-lsp, Serena, Aider, Cline/Continue/Cody, Shipmoor |
| Early August 2026 | separate strategy pass, primary-source verified | Gortex, Roam, Snag, Plumbline, agent-guard, Neurcode, KrowForge (+4) — **eleven products, none overlapping the row above** |
| This pass | six-query raw repo sweep | `anti-halu` — **overlapping neither**, and the one that falsified the claim |

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

> **Provenance for the 2026-08-04 cohort** (no full date in this note on purpose
> — fixed provenance, not a survey stamp): Gortex, Roam, Snag, Plumbline,
> agent-guard and Neurcode were verified in a separate strategy pass against
> primary sources, by reading their actual hook wiring rather than their
> marketing, and were run through the four conjuncts there. They were never
> written into this file, which is why a survey dated two days later could omit a
> 1090★ rival. Gortex was re-verified directly for this pass (stars, license,
> and the presence of `internal/mcp/parse_gate.go`); the other five are carried
> across on that pass's verification, not re-checked here.

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
  resolve".** The highest-impact path by a wide margin, because it is the only
  candidate with real reach (1090★ and climbing during this survey). It already
  holds candidate bytes pre-write, already parses them before the atomic swap,
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
