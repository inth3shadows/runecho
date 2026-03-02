# claude-profile — Claude Code profile switcher
#
# Switches between work (LiteLLM proxy + API key) and personal (Claude Pro OAuth)
# without requiring manual /login or /logout steps.
#
# INSTALL: Copy this function into your PowerShell profile:
#   code $PROFILE   (or notepad $PROFILE)
#
# FIRST-TIME SETUP:
#   1. Store your work API key (encrypted via DPAPI, run once):
#      "sk-ant-api03-YOUR-KEY" | ConvertTo-SecureString -AsPlainText -Force | ConvertFrom-SecureString | Set-Content "$HOME\.claude\anthropic-work.key"
#
#   2. Create profile stub files (tells the switcher which names are valid):
#      New-Item -ItemType Directory -Force "$HOME\.claude\profiles"
#      '{ "model": "claude-sonnet-4-6" }' | Set-Content "$HOME\.claude\profiles\work.json"
#      '{ "model": "sonnet" }'            | Set-Content "$HOME\.claude\profiles\personal.json"
#
#   3. Log in for personal once:
#      claude-profile personal
#      claude   # then run /login inside Claude Code
#
# See docs/profile-switching.md for full explanation.

function claude-profile {
    param([string]$name)

    $profileDir = "$HOME\.claude\profiles"
    $target     = "$profileDir\$name.json"
    if (-not (Test-Path $target)) {
        Write-Host "Unknown profile: $name  (available: work, personal)" -ForegroundColor Red
        return
    }

    $credFile   = "$HOME\.claude\credentials.json"
    $credStash  = "$HOME\.claude\credentials.personal.json"
    $claudeJson = "$HOME\.claude.json"

    switch ($name) {
        "work" {
            # Set your corporate LiteLLM proxy URL here
            $env:ANTHROPIC_BASE_URL = "https://your-litellm-proxy.example.com/"

            $keyFile = "$HOME\.claude\anthropic-work.key"
            if (-not (Test-Path $keyFile)) {
                Write-Host "ERROR: Work key not found at $keyFile" -ForegroundColor Red
                Write-Host "Store it once with:" -ForegroundColor Yellow
                Write-Host '  "sk-ant-..." | ConvertTo-SecureString -AsPlainText -Force | ConvertFrom-SecureString | Set-Content "$HOME\.claude\anthropic-work.key"' -ForegroundColor DarkGray
                return
            }
            $env:ANTHROPIC_API_KEY = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
                [Runtime.InteropServices.Marshal]::SecureStringToBSTR(
                    (Get-Content $keyFile | ConvertTo-SecureString)
                )
            )

            # Stash personal OAuth token — prevents auth conflict with API key
            if (Test-Path $credFile) {
                Move-Item $credFile $credStash -Force
                Write-Host "  (personal credentials stashed)" -ForegroundColor DarkGray
            }

            # Patch hasCompletedOnboarding so Claude doesn't show login selector
            # when credentials.json is absent but ANTHROPIC_API_KEY is set
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
