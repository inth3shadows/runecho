# RunEcho Workflow

- This repo uses RunEcho as the code-truth source for symbol existence and structural drift questions.
- Use the `runecho` MCP server before making claims about what functions, classes, exports, or imports exist.
- If RunEcho reports stale or missing baseline data, run `runecho-ir repo reindex <name>` before trusting structural answers.
- Treat unresolved-symbol findings from `runecho-guard` as verification stops until they are fixed or intentionally explained.

# Release Tags

- Release tags must be monotonic (`vX.Y.Z`, semver-increasing) — non-monotonic tags previously broke `git describe`/version stamping (issue #51).
- Enforced by a `pre-push` hook that rejects a tag push that isn't semver-greater than the highest existing tag. Tracked source: `githooks/pre-push`. It is not auto-installed by git — copy or symlink it to the real hooks dir (`$(git rev-parse --git-common-dir)/hooks/pre-push`) on each machine/worktree setup that pushes tags.
