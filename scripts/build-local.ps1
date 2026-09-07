# build-local.ps1 - THE full-feature, updater-ARMED dev build for the set PC.
#
# WHY (2026-09-02 incident): the rig is developed on the same PC that runs live sets. A plain dev
# build stamps FeedURL="" (and Build 0), which DISABLES the in-app self-updater - app.go builds
# selfupdate.New(version.FeedURL, ...) and an empty FeedURL leaves it inert. Once such an exe sat in
# the install dir the client never self-updated again and was frozen on a stale build for 5 days.
#
# The fix - arm the dev build so it ALWAYS returns to nightly on its own:
#   * FeedURL = the nightly feed          -> updater is ON.
#   * Channel = nightly                   -> polls the nightly manifest.
#   * Build   = the CURRENT nightly build -> during this dev session no published nightly is > ours,
#                                            so the dev build STICKS and is testable; after you push
#                                            development the next nightly (Build = current+N) is
#                                            strictly greater, so the 5-min self-updater auto-replaces
#                                            this dev build with the full nightly. No human memory.
#   * UpdatePubKey stays the SOURCE DEFAULT (do NOT override / empty it) so the signed nightly
#                                            manifest verifies against the real release pubkey.
# Full-feature tags (spout vr abletonlink zigdsp zigui zigvr encembed shellembed) + the static MinGW
# runtime match the nightly recipe exactly - never the crippled "spout vr"-only build.
#
# Windows PowerShell 5.1 compatible (no &&, no ternary, no ??/?.). Does a full cgo+zig build.
param(
    [switch]$NoZig,
    [string]$Out = "dist/rave-mate.exe"
)

$feed = "https://github.com/rave-page/rave-mate/releases/download/nightly/"

# 1. Zig native libs (POSIX twin scripts/build-zig.sh, same as CI; git-bash bash is on PATH).
#    -NoZig reuses whatever libs are already built.
if (-not $NoZig) {
    $zig = Get-Command zig -ErrorAction SilentlyContinue
    if ($null -eq $zig) {
        $zigPath = Join-Path $env:LOCALAPPDATA "Microsoft\WinGet\Links\zig.exe"
        if (Test-Path $zigPath) {
            $env:PATH = (Split-Path $zigPath) + ";" + $env:PATH
        } else {
            Write-Error "zig not found (Get-Command zig / $zigPath) - install zig >= 0.16 or pass -NoZig"
            exit 1
        }
    }
    bash scripts/build-zig.sh
    if ($LASTEXITCODE -ne 0) {
        Write-Error "build-zig.sh failed ($LASTEXITCODE)"
        exit 1
    }
}

# 2. Build identity.
$sha = (git rev-parse --short HEAD).Trim()
$dirty = ""
git diff --quiet
if ($LASTEXITCODE -ne 0) { $dirty = "-dirty" }

# 3. Current nightly build number. This build sticks this session (feed Build == ours, not >);
#    the next pushed nightly (Build > ours) supersedes it via the self-updater.
$curBuild = 0
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $manifest = Invoke-RestMethod -UseBasicParsing ($feed + "latest.json")
    $curBuild = [int]$manifest.build
} catch {
    Write-Warning "could not read nightly latest.json - dev build will NOT auto-return to nightly until the feed is reachable (Build stamped 0; any nightly then supersedes it)"
    $curBuild = 0
}

# 4. LDFLAGS (single string) - keep the DEFAULT UpdatePubKey (do not override). Matches the nightly
#    recipe plus the arming stamps.
$ldflags = "-s -w -H windowsgui -linkmode external -extldflags '-static -static-libgcc -static-libstdc++' -X rave.page/mate/internal/version.Version=dev-$sha$dirty -X rave.page/mate/internal/version.Commit=$sha -X rave.page/mate/internal/version.Build=$curBuild -X rave.page/mate/internal/version.Channel=nightly -X rave.page/mate/internal/version.FeedURL=$feed"

# 5. Build: native windows, cgo on, all feature tags.
$env:CGO_ENABLED = "1"
$tags = "spout vr abletonlink zigdsp zigui zigvr encembed shellembed"
Write-Host "go build -tags `"$tags`" -ldflags `"$ldflags`" -o $Out ./cmd/rave-mate"
go build -tags "$tags" -ldflags "$ldflags" -o $Out ./cmd/rave-mate
if ($LASTEXITCODE -ne 0) {
    Write-Error "go build failed ($LASTEXITCODE)"
    exit 1
}

# 6. Read the stamp back out of the exe (identifies the running build; mind the superproject-sha trap).
Write-Host "Built $Out - stamped:"
go version -m $Out | Select-String -Pattern "version\.(Version|Build|Channel|FeedURL)="
Write-Host "This build STICKS this session (feed Build $curBuild is not > $curBuild) and auto-updates to the next nightly (Build > $curBuild) via the 5-min self-updater after you push development."
