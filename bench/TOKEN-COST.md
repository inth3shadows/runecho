# Context-token cost, measured

The whole code-intelligence category is sold on *saving context*, and an
[independent benchmark](https://dev.to/samchon/i-made-ts-compiler-graph-mcp-10x-fewer-tokens-in-claude-code-1aea)
found several popular tools **increase** it (+22% to +93% vs no MCP at all).
RunEcho's README claimed "~0 tokens of your context window." This measures that
claim rather than asserting it — and the result required correcting the README.

Reproduce: `./bench/tokencost/measure.py <enrolled-repo-name>`
Measured 2026-07-23 on `runecho` itself (177 files), guard/oracle **v0.16.0**,
tokenizer `cl100k_base`.

## Results

| Surface | bytes | tokens |
|---|---:|---:|
| **guard — clean edit** | **0** | **0** |
| guard — ask (only when it blocks) | 398 | 100 |
| mcp — `tools/list` (**always-on**) | 3,696 | 919 |
| mcp — `diff` | 118 | 37 |
| mcp — `health` | 117 | 36 |
| mcp — `hash` | 137 | 61 |
| mcp — `locate` (one symbol) | 216 | 69 |
| mcp — `status` | 360 | 135 |
| mcp — `structure` `detail=tree` | 22,541 | 9,534 |
| mcp — `structure` scoped by `paths` | 35,899 | 13,740 |
| mcp — `structure` (**default**) | 199,683 | **75,205** |

For scale, on the same repo: `codegraph query` = 237 tokens, `codegraph explore`
= 4,865 tokens.

## What this says

**1. The guard's zero is real, and it is structural — not a tuning result.**
A clean check writes *nothing* to stdout. It is a `PreToolUse` hook, so the agent
never decides to call it and never pays to find out it exists. It costs 100
tokens on the edits it actually stops, which is the entire bill. Nothing else
measured here is free at rest.

**2. The MCP server is not free, and the README used to imply it was.**
`tools/list` is 919 tokens injected at session start whether or not a single tool
is ever called — the standing tax every MCP server charges. The README listed
"~0 tokens" among RunEcho's general properties; that was only ever true of the
guard. Corrected in the same change as this file.

**3. `structure` at its default is the most expensive thing here, by far.**
75,205 tokens is ~37% of a 200k window for one call, and 15× `codegraph explore`.
That is precisely the failure mode the linked benchmark diagnoses in other tools:
hand back everything and the thing you pay for never drops. **RunEcho is not
exempt from it — only its guard is.** The tool description already tells agents to
scope with `paths` and `detail`, and this quantifies why it matters:

| instead of | use | tokens | saving |
|---|---|---:|---|
| `structure` (default) | — | 75,205 | — |
| | `detail=tree` | 9,534 | 87% |
| | `paths=["internal/guard/**"]` | 13,740 | 82% |
| "where is symbol X?" | `locate` | 69 | **99.9%** |

`locate` answering "where is X" in 69 tokens against `codegraph explore`'s 4,865
is a genuine 70× advantage — but it is an advantage of *`locate`*, not of RunEcho
generally, and saying otherwise would be the same overclaim as #2.

**Open question this raises:** `structure`'s default of `detail=symbols` is the
worst-value setting measured. Whether the default should be `tree` is a real
question this benchmark can now inform — tracked separately rather than changed
here, because it is a behaviour change to a shipped tool.

## Method, and what it deliberately does not do

It counts tokens each surface **emits into context per invocation**. It does
**not** run an agent across a task set and diff whole-session usage.

That is a deliberate scope limit, not an omission. A per-invocation count is
deterministic and reproducible; a whole-session count depends mostly on which
tools the agent chose to call — a property of the agent and the task, not of
RunEcho. Publishing a session-level number would attribute the agent's choices to
this tool. The per-invocation numbers are what determine that result anyway, and
they are the honest thing to publish.

Other limits, stated rather than buried:

- **One repo.** 177 files. `structure` scales with repo size, so its number is
  specific to this codebase; the guard's zero is not (it is structural).
- **stdout only.** The guard writes diagnostics to stderr, which never reaches the
  model, so stderr is not counted. That is the correct boundary, but it means the
  guard's cost is zero *to the model*, not zero in absolute terms.
- **One tokenizer.** `cl100k_base`. Anthropic models tokenize differently; the
  ratios hold, the absolute counts shift.
- **`codegraph` figures are per-call CLI output**, which its docs state is the
  same payload as its MCP tool. Its always-on `tools/list` cost is not measured
  here, so the comparison understates its fixed overhead, not ours.
