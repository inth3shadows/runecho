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
| `post-merge` | same | `version-check --reinstall` (freshness), then background `runecho-ir repo reindex .` |
| `post-checkout` | same, on branch switches only (`$3 == 1`) | `version-check --reinstall` (freshness), then background `runecho-ir repo reindex .` |
| `pre-push` | `bash install.sh --hook-pre-push` (`githooks/pre-push`) | rejects a non-monotonic `vX.Y.Z` tag push |

The three reindex hooks are the E6 auto-fresh-IR feature (#20/#21). They keep the
IR index current, and **every guard answer is computed from that index** — so
anything that overwrites `post-merge` or `post-checkout` degrades the guard
silently rather than loudly. The #228 freshness check (auto-reinstall when the
installed binary is behind the newest reachable tag) is folded into these SAME
two hooks — never a separate installer, which is what collided in #226 — so both
features share one hook body: freshness runs first, then the background reindex
picks up the just-rebuilt binary.

The freshness half also requires the checked-out revision to be **contained in
`origin`'s default branch** (#373). Reaching the rebuild runs `install.sh`, which
compiles the whole worktree, so it runs that revision's code — and `git checkout`
of a contributor's branch is not an act of trust. Note the gate is on HEAD, not
on the tag: a fork branch based on master resolves a legitimate release tag by
ancestry, so no amount of tag verification closes it. The cost is that your own
unpushed feature branches no longer auto-refresh a stale binary; `runecho-ir
version-check` (without `--quiet`) still reports it.
