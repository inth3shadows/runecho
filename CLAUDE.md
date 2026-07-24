# RunEcho Workflow

- This repo uses RunEcho as the code-truth source for symbol existence and structural drift questions.
- Use the `runecho` MCP server before making claims about what functions, classes, exports, or imports exist.
- If RunEcho reports stale or missing baseline data, run `runecho-ir repo reindex <name>` before trusting structural answers.
- Treat unresolved-symbol findings from `runecho-guard` as verification stops until they are fixed or intentionally explained.

# Release Tags

- Release tags must be monotonic (`vX.Y.Z`, semver-increasing) — non-monotonic tags previously broke `git describe`/version stamping (issue #51).
- Enforced by a `pre-push` hook that rejects a tag push that isn't semver-greater than the highest existing tag. Tracked source: `githooks/pre-push`. Not auto-installed by git — run `bash install.sh --hook-pre-push` from the repo root on each machine/worktree setup that pushes tags (installs into `$(git rev-parse --git-common-dir)/hooks/pre-push`).

# Git hooks — there are five, from two different installers

Both installers write into `$(git rev-parse --git-common-dir)/hooks`, which is
shared across every worktree of this repo. Knowing which installer owns a hook
matters: a change that overwrites one silently disables the other's feature.

| Hook | Installed by | Does |
|---|---|---|
| `pre-commit` | `runecho-ir install` → `installHooks` (`cmd/runecho-ir/install.go`) | runs `runecho-guard` at commit time |
| `post-commit` | same | background `runecho-ir repo reindex .` |
| `post-merge` | same | background `runecho-ir repo reindex .` |
| `post-checkout` | same, on branch switches only (`$3 == 1`) | background `runecho-ir repo reindex .` |
| `pre-push` | `bash install.sh --hook-pre-push` (`githooks/pre-push`) | rejects a non-monotonic `vX.Y.Z` tag push |

The three reindex hooks are the E6 auto-fresh-IR feature (#20/#21). They keep the
IR index current, and **every guard answer is computed from that index** — so
anything that overwrites `post-merge` or `post-checkout` degrades the guard
silently rather than loudly. A proposal to add an auto-reinstall hook at those
same two names was split out of #226 for exactly this reason; see #228.
