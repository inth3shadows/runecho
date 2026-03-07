---
name: ai-drift
description: Run drift-aware task advisory check
---

Run the drift-aware task advisory check for the current workspace:

```
ai-task drift-check $CWD
```

`$CWD` is the current workspace directory.

Display the full output and explain any drift findings in plain language:
- What the original task scope declared
- What has changed (file modifications, new dependencies, schema changes) that constitutes drift
- The severity of each drift fault (WARN vs ERROR)
- Recommended remediation steps

If drift faults are found, also suggest running `ai-task replan $CWD` to update the task contract to reflect the current state.

If no drift is detected, confirm that the workspace is clean relative to the active task scope.

If `ai-task` is not found in PATH, tell the user to run `bash install.sh` from the RunEcho repo root (`~/personal_projects/.ai/`) to install the binaries.
