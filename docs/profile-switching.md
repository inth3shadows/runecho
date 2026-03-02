# Claude Code Profile Switching (Work + Personal)

How to run Claude Code against two different authentication backends — a corporate LiteLLM proxy and Claude Pro OAuth — on the same machine, switching between them instantly with no manual login/logout steps.

---

## The Problem

Claude Code supports two auth methods:

- **OAuth** (`claude /login` → claude.ai) — for Claude Pro/Max/Team subscriptions
- **API key** (`ANTHROPIC_API_KEY` env var) — for direct Anthropic API or custom proxies (LiteLLM, Bedrock, etc.)

If you need both (personal Pro subscription + corporate LiteLLM), Claude Code detects when both are present simultaneously and refuses to start cleanly:

```
⚠ Auth conflict: Both a token (claude.ai) and an API key (ANTHROPIC_API_KEY) are set.
This may lead to unexpected behavior.
```

The naive fix — manually running `claude /logout` before each work session — resets `hasCompletedOnboarding` to `false` in `~/.claude.json`, causing Claude Code to show the login selector on every subsequent launch, even when your API key is correctly set.

---

## Root Cause

Claude Code stores OAuth credentials in **`~/.claude/credentials.json`**.

- `claude /login` → creates the file
- `claude /logout` → deletes the file and resets `hasCompletedOnboarding: false` in `~/.claude.json`
- No `credentials.json` + `hasCompletedOnboarding: false` → login selector shown, even if `ANTHROPIC_API_KEY` is set

The fix is to **swap** the credentials file atomically as part of profile switching, and **patch** `hasCompletedOnboarding` to `true` for API-key mode.

---

## Solution: PowerShell Profile Switcher

Add this function to your PowerShell profile (`~\Documents\PowerShell\Microsoft.PowerShell_profile.ps1`):

```powershell
function claude-profile {
    param([string]$name)
    $profileDir = "$HOME\.claude\profiles"
    $target = "$profileDir\$name.json"
    if (-not (Test-Path $target)) {
        Write-Host "Unknown profile: $name  (available: work, personal)" -ForegroundColor Red
        return
    }

    $credFile   = "$HOME\.claude\credentials.json"
    $credStash  = "$HOME\.claude\credentials.personal.json"
    $claudeJson = "$HOME\.claude.json"

    switch ($name) {
        "work" {
            $env:ANTHROPIC_BASE_URL = "https://your-litellm-proxy.example.com/"
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
            # Stash personal OAuth token so it doesn't conflict with API key
            if (Test-Path $credFile) {
                Move-Item $credFile $credStash -Force
                Write-Host "  (personal credentials stashed)" -ForegroundColor DarkGray
            }
            # Ensure Claude Code doesn't show login selector when no credentials.json exists
            if (Test-Path $claudeJson) {
                $cfg = Get-Content $claudeJson -Raw | ConvertFrom-Json
                $cfg.hasCompletedOnboarding = $true
                $cfg | ConvertTo-Json -Depth 20 | Set-Content $claudeJson
            }
        }
        "personal" {
            Remove-Item Env:ANTHROPIC_BASE_URL -ErrorAction SilentlyContinue
            Remove-Item Env:ANTHROPIC_API_KEY  -ErrorAction SilentlyContinue
            # Restore personal OAuth token
            if (Test-Path $credStash) {
                Move-Item $credStash $credFile -Force
                Write-Host "  (personal credentials restored)" -ForegroundColor DarkGray
            } else {
                Write-Host "  (no stashed credentials — run: claude /login)" -ForegroundColor Yellow
            }
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

The key is encrypted with DPAPI — it can only be decrypted by your Windows user account on this machine.

### 2. Create profile stub files

These tell the switcher which profile names are valid. They can also hold profile-specific settings.

```powershell
# ~/.claude/profiles/work.json
{
  "model": "claude-sonnet-4-6"
}

# ~/.claude/profiles/personal.json
{
  "model": "sonnet"
}
```

### 3. Bootstrap personal credentials

Switch to personal mode and log in once:

```powershell
claude-profile personal
claude  # then run /login inside Claude Code
```

After `/login`, `~/.claude/credentials.json` exists. Going forward, the switcher stashes and restores it — you never need to `/login` again unless the token expires.

### 4. Verify work mode

```powershell
claude-profile work
claude  # /status should show: Auth token: none, API key: ANTHROPIC_API_KEY, Base URL: your-proxy
```

---

## Day-to-Day Usage

```powershell
# Start a work session
claude-profile work
cd C:\Work\projects\my-project
claude

# Start a personal session
claude-profile personal
cd C:\personal_projects\my-project
claude
```

No logout needed. No login screen. The switch is instant.

---

## How It Works

```
claude-profile work
│
├── Set $env:ANTHROPIC_BASE_URL  → LiteLLM proxy URL
├── Set $env:ANTHROPIC_API_KEY   → decrypted from ~/.claude/anthropic-work.key
├── Move credentials.json        → credentials.personal.json  (stash OAuth token)
└── Patch ~/.claude.json         → hasCompletedOnboarding: true

claude-profile personal
│
├── Remove $env:ANTHROPIC_BASE_URL
├── Remove $env:ANTHROPIC_API_KEY
└── Move credentials.personal.json → credentials.json  (restore OAuth token)
```

**Work mode:** no `credentials.json` on disk, `ANTHROPIC_API_KEY` + `ANTHROPIC_BASE_URL` in env, `hasCompletedOnboarding: true` → Claude uses API key, routes through proxy, no login screen.

**Personal mode:** `credentials.json` present, no env vars → Claude uses OAuth, routes through claude.ai.

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
| OAuth token expires | `claude-profile personal` → `claude` → run `/login` once; switcher will stash new token going forward |
| Work key rotated | Re-run the `ConvertTo-SecureString` store command with new key |
| Opened Claude before switching profiles | Restart Claude after switching — env vars are read at launch |
| `~/.claude.json` doesn't exist | Patch step no-ops silently; login screen may appear once on first work launch |

---

## Security Notes

- **Work API key** is stored encrypted via Windows DPAPI (`ConvertTo-SecureString` / `ConvertFrom-SecureString`). It is not readable by other Windows user accounts.
- **`credentials.json`** is stored in plaintext by Claude Code itself — this is upstream behavior, not introduced by this setup.
- Neither the key nor the token is ever written to `settings.json` or any file tracked by git.

---

## Why Not Use `settings.json` env Block?

The work docs suggest putting `ANTHROPIC_API_KEY` directly in `~/.claude/settings.json`:

```json
{ "env": { "ANTHROPIC_API_KEY": "sk-ant-..." } }
```

This works, but:
1. The key is in plaintext in a config file
2. It applies globally — no way to switch profiles without editing the file
3. Still conflicts with OAuth if you run `/login` for personal use

The PowerShell switcher approach keeps the key encrypted, makes switching instant, and requires zero edits to any config file after initial setup.
