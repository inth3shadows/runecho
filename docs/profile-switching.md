# Claude Code Profile Switching (Work + Personal)

How to run Claude Code against two different authentication backends — a corporate LiteLLM proxy and Claude Pro OAuth — on the same machine, simultaneously in different terminals, with no manual login/logout steps.

---

## The Problem

Claude Code supports two auth methods:

- **OAuth** (`claude /login` → claude.ai) — for Claude Pro/Max/Team subscriptions
- **API key** (`ANTHROPIC_API_KEY` env var) — for direct Anthropic API or custom proxies (LiteLLM, Bedrock, etc.)

If you need both (personal Pro subscription + corporate LiteLLM), Claude Code warns when it sees both simultaneously:

```
⚠ Auth conflict: Both a token (claude.ai) and an API key (ANTHROPIC_API_KEY) are set.
```

The naive fix — running `claude /logout` before switching — resets `hasCompletedOnboarding: false` in `~/.claude.json`, causing the login selector to appear on every subsequent launch even with `ANTHROPIC_API_KEY` set.

---

## Solution: Isolated Config Directories

Claude Code supports `CLAUDE_CONFIG_DIR` — an env var that redirects where it reads configuration and credentials. By pointing work sessions at `~/.claude-work/` and leaving personal at the default `~/.claude/`, the two are fully isolated:

- **Work** sessions: `CLAUDE_CONFIG_DIR=~/.claude-work` + `ANTHROPIC_API_KEY` set → no `credentials.json` in scope, no conflict
- **Personal** sessions: no `CLAUDE_CONFIG_DIR` (default `~/.claude/`) → `credentials.json` in scope, no API key set

Both can run simultaneously in separate terminals with zero conflict.

---

## PowerShell Switcher

```powershell
function claude-profile {
    param([string]$name)
    $profileDir = "$HOME\.claude\profiles"
    $target = "$profileDir\$name.json"
    if (-not (Test-Path $target)) {
        Write-Host "Unknown profile: $name  (available: work, personal)" -ForegroundColor Red
        return
    }

    switch ($name) {
        "work" {
            $env:ANTHROPIC_BASE_URL = "https://your-litellm-proxy.example.com/"
            $env:CLAUDE_CONFIG_DIR  = "$HOME\.claude-work"
            $keyFile = "$HOME\.claude\anthropic-work.key"
            if (-not (Test-Path $keyFile)) {
                Write-Host "ERROR: Work key not found at $keyFile" -ForegroundColor Red
                Write-Host "Run once to store key:" -ForegroundColor Yellow
                Write-Host '  "sk-ant-..." | ConvertTo-SecureString -AsPlainText -Force | ConvertFrom-SecureString | Set-Content "$HOME\.claude\anthropic-work.key"' -ForegroundColor DarkGray
                return
            }
            $env:ANTHROPIC_API_KEY = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
                [Runtime.InteropServices.Marshal]::SecureStringToBSTR(
                    (Get-Content $keyFile | ConvertTo-SecureString)
                )
            )
        }
        "personal" {
            Remove-Item Env:ANTHROPIC_BASE_URL -ErrorAction SilentlyContinue
            Remove-Item Env:ANTHROPIC_API_KEY  -ErrorAction SilentlyContinue
            Remove-Item Env:OPENAI_API_KEY     -ErrorAction SilentlyContinue
            Remove-Item Env:CLAUDE_CONFIG_DIR  -ErrorAction SilentlyContinue
        }
    }
    Write-Host "Claude profile: $name" -ForegroundColor Cyan
}

Set-Alias -Name cp-claude -Value claude-profile
```

---

## First-Time Setup

### 1. Store your work API key (encrypted, run once)

```powershell
"sk-ant-api03-YOUR-WORK-KEY" | ConvertTo-SecureString -AsPlainText -Force | ConvertFrom-SecureString | Set-Content "$HOME\.claude\anthropic-work.key"
```

Encrypted with Windows DPAPI — only readable by your user account on this machine.

### 2. Create the work config directory

```powershell
mkdir "$HOME\.claude-work"
Copy-Item "$HOME\.claude\settings.json" "$HOME\.claude-work\settings.json"
```

The work config dir needs a `settings.json` (for hooks, model settings, etc.) but no `credentials.json`.

### 3. Create profile stub files

```powershell
# ~/.claude/profiles/work.json and ~/.claude/profiles/personal.json
{ "model": "sonnet" }
```

These tell the switcher which profile names are valid.

### 4. Bootstrap personal credentials (once)

```powershell
claude-profile personal
claude  # run /login inside Claude Code
```

After `/login`, `~/.claude/credentials.json` exists and persists. The switcher doesn't touch it.

### 5. Verify work mode

```powershell
claude-profile work
claude  # /status → Settings tab should show API key auth, LiteLLM base URL
```

---

## Day-to-Day Usage

```powershell
# Work terminal
claude-profile work
claude

# Personal terminal (can be open at the same time)
claude-profile personal
claude
```

No logout needed. No login screen. Simultaneous terminals work cleanly.

---

## How It Works

```
claude-profile work
│
├── Set ANTHROPIC_BASE_URL  → LiteLLM proxy URL
├── Set ANTHROPIC_API_KEY   → decrypted from ~/.claude/anthropic-work.key
└── Set CLAUDE_CONFIG_DIR   → ~/.claude-work  (no credentials.json here)

claude-profile personal
│
├── Remove ANTHROPIC_BASE_URL
├── Remove ANTHROPIC_API_KEY
└── Remove CLAUDE_CONFIG_DIR  (falls back to ~/.claude, which has credentials.json)
```

The config directories never share a `credentials.json`, so there is no auth conflict regardless of how many terminals are open or in what order you switch.

---

## Verifying Status

Inside Claude Code, run `/status` → `Settings` tab:

**Work:**
```
Auth token: none
API key: ANTHROPIC_API_KEY
Anthropic base URL: https://your-litellm-proxy.example.com/
```

**Personal:**
```
Login method: Claude Pro Account
```

---

## Edge Cases

| Scenario | Behavior |
|---|---|
| OAuth token expires | `claude-profile personal` → run `/login` once inside Claude Code |
| Work key rotated | Re-run the `ConvertTo-SecureString` store command with the new key |
| Opened Claude before switching profiles | Restart Claude after switching — env vars are read at launch |
| `~/.claude-work/settings.json` missing | Claude Code uses defaults; re-copy from `~/.claude/settings.json` |

---

## Security Notes

- **Work API key** is stored encrypted via Windows DPAPI. Not readable by other user accounts.
- **`credentials.json`** is stored in plaintext by Claude Code itself — upstream behavior.
- Neither the key nor the token is ever written to any file tracked by git.
- `CLAUDE_CONFIG_DIR` is an officially supported Claude Code environment variable.
