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
| `post-merge` | same | `version-check --quiet` (freshness **advisory** — never builds), then background `runecho-ir repo reindex .` |
| `post-checkout` | same, on branch switches only (`$3 == 1`) | `version-check --quiet` (advisory), then background `runecho-ir repo reindex .` |
| `pre-push` | `bash install.sh --hook-pre-push` (`githooks/pre-push`) | rejects a non-monotonic `vX.Y.Z` tag push |

The three reindex hooks are the E6 auto-fresh-IR feature (#20/#21). They keep the
IR index current, and **every guard answer is computed from that index** — so
anything that overwrites `post-merge` or `post-checkout` degrades the guard
silently rather than loudly. The #228 freshness check is folded into these SAME
two hooks — never a separate installer, which is what collided in #226 — but
since #375 the hooks only **advise**: one offline "installed vX is BEHIND vY"
line, and they execute nothing. `git checkout` of a contributor's branch is not
an act of trust, so no hook may run the checked-out tree.

**Where rebuilds happen (#375).** Only in `freshen` (`cmd/runecho-ir/freshen.go`),
reached from its own hourly schedule entry (`runecho-ir freshen
<git-common-dir>` — a `:30` crontab line or the `com.runecho.freshen`
LaunchAgent, written by `runecho-ir install --periodic` run from inside this
checkout) and from an explicit `runecho-ir version-check --reinstall`. It is a
separate entry, not a reindex flag, because a binary predating it would reject
the flag and skip the reindex that every guard answer depends on. It asks origin for its tags (`ls-remote`), and when the installed
binary is behind the newest `vX.Y.Z` it fetches, requires that tag's commit to be
contained in origin's default branch, exports it with `git archive`, and runs
**that** tree's `install.sh` with `RUNECHO_VERSION=<tag>`. Local branches,
worktrees and local `refs/tags` are never consulted, and the checked-out tree is
never executed — which is why #373's HEAD-containment gate (inert on nearly every
branch) is gone. `git archive` works because the version is known from origin and
`install.sh` honours `RUNECHO_VERSION` (the Dockerfile precedent); without it the
build would stamp `dev`. The remote name `origin` is hardcoded (fail-closed,
logged). Every tick writes one `freshen:` line to `~/.runecho/logs/reindex.log`;
a run finding `$RUNECHO_HOME/freshen.lock` held skips rather than queueing, and
`$RUNECHO_HOME/no-auto-install` is the opt-out a scheduled job can see.
`version-check --reinstall --quiet` — the body of hooks installed before #375 —
deliberately stays the offline advisory, so a not-yet-rewritten hook never
fetches or builds.
