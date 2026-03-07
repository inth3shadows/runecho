---
name: ai-cost
description: Show session cost breakdown and model routing
---

Show the cost breakdown for the current session by doing the following:

1. Read `.ai/progress.jsonl` in the current workspace (each line is a JSON progress entry). Look for entries with `type: "cost"` or `type: "governor"` fields that record per-turn token usage and model routing decisions.
2. Summarize total tokens consumed (input, output, cache read, cache write) and estimated USD cost.
3. Show the model used per turn (haiku / sonnet / opus) from the routing decisions recorded by `session-governor.sh`.
4. Highlight any turns where the model deviated from the recommended routing tier.

If `.ai/progress.jsonl` does not exist in the current directory, check `.ai/.governor-state/` for the latest route file (filename: `<session_id>.route`).

If no cost data is found, report that and suggest the user ensure `session-governor.sh` is active (visible in `~/.claude/settings.json` under `UserPromptSubmit` hooks).
