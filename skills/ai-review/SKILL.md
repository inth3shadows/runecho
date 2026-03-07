---
name: ai-review
description: Review current session — handoff quality, costs, drift
---

Run the following command to produce a session review for the current workspace:

```
ai-session review $CWD
```

`$CWD` is the current workspace directory (the project root where Claude Code is running).

Display the full formatted output of that command. The review covers:
- Handoff quality (is the session contract and task list current?)
- Accumulated token costs and model routing decisions
- Any drift faults recorded in `.ai/progress.jsonl`
- Recommended next steps from the session contract

If `ai-session` is not found in PATH, tell the user to run `bash install.sh` from the RunEcho repo root (`~/personal_projects/.ai/`) to install the binaries.
