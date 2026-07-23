# Security

## Reporting a vulnerability

Use GitHub's private reporting: **Security tab → Report a vulnerability** (or
https://github.com/inth3shadows/runecho/security/advisories/new). Do not open
a public issue for a suspected vulnerability until it's triaged.

## Threat model

RunEcho is a local, single-operator dev tool: no server, no accounts, no
multi-tenant deployment. The relevant threats are what happens when it parses
untrusted repo content and when it runs unattended on every edit/commit — not
network attack surface, because it has none.

### No network surface

RunEcho makes no outbound network calls and requires no API keys — it is
model-free by design (see README). There is nothing to exfiltrate data to and
no remote service to compromise.

### What's stored, and where

The central store (`~/.runecho/history.db`, override via `RUNECHO_HOME`) holds,
per enrolled repo: absolute file paths, SHA-256 content hashes, and symbol
names/kinds. It does **not** store raw source code or file contents. The store
directory is created `0700` and its contents `0600` — `history.db` and its
WAL/SHM sidecars (`internal/snapshot/db.go`), the guard's decision log
(`cmd/runecho-guard/declog.go`), the learned-allow store, the dependency-export
cache, and the periodic-reindex log. Protection is standard Unix file
permissions on your home directory, not encryption: other local users are kept
out by mode bits, and anything running **as you** has full access.

Opening the store also re-tightens it (`tightenStorePerms`, best-effort), so a
directory created before these modes existed — or one you made yourself with a
loose umask — is corrected rather than left as-is. The repo-local `.ai/ir.json`
working copy is likewise `0700`/`0600`.

### Execution model — no sandboxing, no privilege separation

`runecho-guard` runs automatically as a git `pre-commit` hook and/or a Claude
Code `PreToolUse` hook, with the invoking user's OS permissions, on every
commit or edit. This is inherent to what it is — a local hook, not a sandboxed
service. Do not enroll or point RunEcho at a repo you don't already trust
enough to run its hooks against.

### The guard is a hallucination-catcher, not a security control

`runecho-guard` is fail-open by design: missing store, unenrolled repo, no
snapshot, DB error, or a hung git subprocess all degrade to silence rather than
blocking work, and a clean check never auto-approves — it always defers to the
normal permission flow. This means an unresolved-symbol warning is a
correctness signal, not an access-control decision. Do not rely on the guard
to prevent a determined attacker from landing malicious code — it isn't built
to, and it will step aside rather than block a workflow it can't cleanly
evaluate. Similarly, `.runechoguardignore` is a plain, repo-local text file:
anyone with write access to the repo can add a line to suppress a warning.

### Parsing adversarial or malformed source

The JS/TS and Python parsers run on a pure-Go tree-sitter runtime that can
panic on adversarial or malformed input. Both grammar loading and per-file
parsing are wrapped in `recover()` (`internal/parser/js.go`,
`internal/parser/python.go`) so a crafted file degrades that file to "no AST
symbols" instead of crashing the indexer or the long-lived MCP server. RunEcho
never executes the code it parses — all extraction is static (AST/regex), no
`eval`, no shelling out to the target language's runtime.

`runecho-guard`'s stdio hook modes are wrapped the same way (`deferOnPanic`,
`cmd/runecho-guard/main.go`). That wrapper is load-bearing rather than tidy: a
Go panic exits status 2, which Claude Code reads as *block this tool call* — so
without it an unexpected panic would flip the guard from fail-open to
fail-closed and obstruct every subsequent edit. A recovered panic emits nothing
(the defer response), warns on stderr, and exits 0.

### Repo-controlled text reaching the agent

The guard's `permissionDecisionReason` is read by the agent at a permission
decision point, so anything interpolated into it is a potential prompt-injection
vector. Symbol names are constrained to identifiers by the extractor's regexes.
File **paths** are not — a POSIX file name may contain newlines and arbitrary
prose — so every repo-derived path is passed through `sanitizeReasonPath`
(control characters neutralized, length capped) before it reaches that string or
the pre-commit stderr report.

### Out of scope

RunEcho is not a SAST tool, secret scanner, malware scanner, or supply-chain
auditor, and makes no attempt to be one. It answers "does this symbol exist"
and "what structurally changed" — nothing about the safety or intent of the
code it indexes.
