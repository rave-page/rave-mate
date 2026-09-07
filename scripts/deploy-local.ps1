# deploy-local.ps1 - guarded copy of a stamped dev build into the local install dir.
# Guards added after 2026-09-02: a stale-branch, unstamped exe ran unnoticed for 5 days.
# Refuses a tree missing origin/development, an unstamped exe, or a live set (Resolume/OBS).
# Never restarts the app - a set may be running. Windows PowerShell 5.1 compatible.
param(
    [string]$Exe = "dist/rave-mate.exe",
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\rave-mate",
    [switch]$Force
)

git fetch origin --quiet
git merge-base --is-ancestor origin/development HEAD
if ($LASTEXITCODE -ne 0 -and -not $Force) {
    Write-Error "HEAD does not contain origin/development - deploying would downgrade the install (2026-09-02 incident). Merge or rebase first, or pass -Force."
    exit 2
}

if (-not (Test-Path $Exe)) {
    Write-Error "build not found: $Exe"
    exit 1
}

$m = go version -m $Exe | Select-String -Pattern "version\.Version=(\S+)"
if ($null -eq $m) {
    Write-Error "unstamped build - use make build or the CLAUDE.md build command"
    exit 3
}
$stamp = $m.Matches[0].Groups[1].Value

$live = Get-Process -Name Arena,obs64 -ErrorAction SilentlyContinue
if ($live -and -not $Force) {
    Write-Error "Resolume/OBS running - a set may be live; pass -Force to deploy anyway"
    exit 4
}

$target = Join-Path $InstallDir "rave-mate.exe"
try {
    if (Test-Path $target) {
        # Rename, not copy: Windows lets a RUNNING exe be renamed but not overwritten, and the live
        # process keeps executing from the renamed file until it restarts.
        Move-Item $target (Join-Path $InstallDir ("rave-mate.exe.bak-" + (Get-Date -Format "yyyyMMdd-HHmmss"))) -ErrorAction Stop
    }
    Copy-Item $Exe $target -ErrorAction Stop
} catch {
    Write-Error "deploy FAILED - install dir unchanged or backup left in place: $_"
    exit 5
}
Write-Host "Deployed $stamp -> $target"
Write-Host "Restart: & '$target' ctl quit ; then start '$target'. This script never restarts the app - a set may be running."

if (Get-Process -Name rave-mate -ErrorAction SilentlyContinue) {
    Write-Warning "rave-mate is running the OLD exe until restarted"
}
