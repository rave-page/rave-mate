# deploy-local.ps1 - guarded deploy of a stamped dev build into the local install dir, plus
# -RestoreNightly to put the set PC back on the pure auto-updating nightly.
#
# Dev-on-set-PC policy (2026-09-02 incident): this rig is developed on the machine that runs live
# sets. Only a FULL-FEATURE, updater-ARMED dev build may be deployed here - one stamped with the
# nightly FeedURL + Channel=nightly + Build=current-nightly (see scripts/build-local.ps1). Such a
# build keeps every feature during the session and auto-returns to nightly (5-min self-updater,
# Build strictly greater) after you push development. A "spout,vr"-only or empty-FeedURL exe FREEZES
# the self-updater (the 2026-09-02 freeze) - this script REFUSES it (exit 6). Use -RestoreNightly to
# force the current nightly right now.
#
# Guards: origin/development ancestor (exit 2), missing exe (exit 1), unstamped (exit 3),
# not-full-feature/not-armed (exit 6), Resolume/OBS live (exit 4), rename-then-copy failure (exit 5),
# -RestoreNightly installer sha mismatch (exit 7). Never restarts the app - a set may be running.
# Windows PowerShell 5.1 compatible.
param(
    [string]$Exe = "dist/rave-mate.exe",
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\rave-mate",
    [switch]$Build,
    [switch]$RestoreNightly,
    [switch]$Force
)

$feed = "https://github.com/rave-page/rave-mate/releases/download/nightly/"

# ---------------------------------------------------------------------------
# -RestoreNightly: download + sha-verify the current nightly installer and run it silently.
# Mutually exclusive with deploying a dev build. Forces the set PC back onto the pure nightly.
# ---------------------------------------------------------------------------
if ($RestoreNightly) {
    if ($Build) { Write-Warning "-Build ignored with -RestoreNightly" }

    $live = Get-Process -Name Arena,obs64 -ErrorAction SilentlyContinue
    if ($live -and -not $Force) {
        Write-Error "Resolume/OBS running - a set may be live; pass -Force to restore anyway"
        exit 4
    }

    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $manifest = Invoke-RestMethod -UseBasicParsing ($feed + "latest.json")
    } catch {
        Write-Error "cannot read nightly latest.json: $_"
        exit 1
    }
    $iurl = $manifest.installer_url
    $isha = $manifest.installer_sha256
    if ([string]::IsNullOrEmpty($iurl) -or [string]::IsNullOrEmpty($isha)) {
        Write-Error "nightly manifest has no installer_url/installer_sha256"
        exit 1
    }

    $setup = Join-Path $env:TEMP ("rave-mate-setup-" + $manifest.build + ".exe")
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $iurl -OutFile $setup
    } catch {
        Write-Error "installer download failed: $_"
        exit 1
    }

    $got = (Get-FileHash -Algorithm SHA256 $setup).Hash
    if ($got.ToLower() -ne ("" + $isha).ToLower()) {
        Write-Error "installer sha256 mismatch - refusing to run (want $isha got $got)"
        exit 7
    }
    Write-Host "installer sha256 OK ($got)"

    Start-Process -Wait -FilePath $setup -ArgumentList '/S'
    Write-Host "Installed nightly build $($manifest.build) ($($manifest.version)) into $InstallDir"
    $target = Join-Path $InstallDir "rave-mate.exe"
    if (Test-Path $target) {
        go version -m $target | Select-String -Pattern "version\.(Version|Build|Channel|FeedURL)="
    }
    Write-Host "Restart MANUALLY (a set may be running): & '$target' ctl quit ; then start '$target'. This script never restarts the app."
    exit 0
}

# ---------------------------------------------------------------------------
# Dev-build deploy path.
# ---------------------------------------------------------------------------
git fetch origin --quiet
git merge-base --is-ancestor origin/development HEAD
if ($LASTEXITCODE -ne 0 -and -not $Force) {
    Write-Error "HEAD does not contain origin/development - deploying would downgrade the install (2026-09-02 incident). Merge or rebase first, or pass -Force."
    exit 2
}

# -Build: build a full-feature, updater-armed exe first (unless -Exe points at a prebuilt path).
if ($Build -and $Exe -eq "dist/rave-mate.exe") {
    & (Join-Path $PSScriptRoot "build-local.ps1") -Out "dist/rave-mate.exe"
    if ($LASTEXITCODE -ne 0) {
        Write-Error "build-local.ps1 failed ($LASTEXITCODE)"
        exit 1
    }
}

if (-not (Test-Path $Exe)) {
    Write-Error "build not found: $Exe (run scripts/build-local.ps1 or pass -Build)"
    exit 1
}

$mlines = go version -m $Exe
$info = ($mlines | Out-String)
$m = $mlines | Select-String -Pattern "version\.Version=(\S+)"
if ($null -eq $m) {
    Write-Error "unstamped build - use scripts/build-local.ps1 (or make build-local)"
    exit 3
}
$stamp = $m.Matches[0].Groups[1].Value

# FULL-FEATURE + ARMED gate (exit 6): refuse exactly the 2026-09-02 mistake - a "spout,vr"-only or
# empty-FeedURL exe that would freeze the self-updater on the set PC.
$tagsLine = ($mlines | Select-String -Pattern "-tags=").Line
if ($null -eq $tagsLine) { $tagsLine = "" }
$fullFeature = ($tagsLine -like "*zigui*") -and ($tagsLine -like "*encembed*")
$armed = ($info -like "*version.FeedURL=$feed*") -and ($info -like "*version.Channel=nightly*")
if (-not ($fullFeature -and $armed)) {
    Write-Error "refusing $stamp - not a full-feature, updater-armed build. Deploying a crippled/unarmed dev build to the set PC freezes the self-updater (2026-09-02). Rebuild with scripts/build-local.ps1 (or deploy-local.ps1 -Build). full-feature(zigui+encembed)=$fullFeature armed(FeedURL+Channel=nightly)=$armed"
    exit 6
}

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
Write-Host "Deployed $stamp -> $target (full-feature, updater-armed; auto-returns to nightly after the next push)"
Write-Host "Restart: & '$target' ctl quit ; then start '$target'. This script never restarts the app - a set may be running."

if (Get-Process -Name rave-mate -ErrorAction SilentlyContinue) {
    Write-Warning "rave-mate is running the OLD exe until restarted"
}
