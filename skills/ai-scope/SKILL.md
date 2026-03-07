---
name: ai-scope
description: Show active task scope and check for drift
---

Show the active task scope for the current workspace by running:

```
ai-task list $CWD
```

`$CWD` is the current workspace directory.

Display the output and highlight:
- The currently active task (marked active or with the most recent start timestamp)
- Its declared scope, success criteria, and non-goals
- Any tasks that are blocked or have unresolved faults

If multiple tasks are listed, clearly indicate which one is active and which are pending or complete.

If `ai-task` is not found in PATH, tell the user to run `bash install.sh` from the RunEcho repo root (`~/personal_projects/.ai/`) to install the binaries.
