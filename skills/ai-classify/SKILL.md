---
name: ai-classify
description: Classify current prompt and show model routing decision
---

Explain the current model routing decision for this session by doing the following:

1. Check `.ai/.governor-state/` in the current workspace for the latest route file (`<session_id>.route`). It contains the classifier output: the detected task type and the recommended model tier (haiku / sonnet / opus).
2. If a classifier log exists at `.ai/classifier-log.jsonl`, read the most recent entry and show the prompt category, confidence score, and routing decision.
3. Explain why this routing tier was chosen based on the BPB v3 routing rules:
   - **Haiku**: exploration, search, summarization, reading files, formatting, documentation, boilerplate, simple Q&A
   - **Sonnet**: code writing, bug fixes, test writing, standard refactoring, implementation
   - **Opus**: architecture decisions, complex multi-file design, security review, trade-off analysis, root cause debugging

If no routing state is found, describe what routing decision would apply to the most recent user prompt based on the rules above.

Report the current model in use and whether it matches the recommended tier.
