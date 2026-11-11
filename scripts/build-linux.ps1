# build-linux.ps1 - build a stamped rave-mate linux/amd64 binary in the pinned Docker box.
#
# WHY: the Linux sender (`sus`, Arch) must never build on the box that runs it (owner rule) and
# this is a Windows dev PC, so the Linux binary is produced in a reproducible container
# (contrib/linux-build/Dockerfile: Debian 13 glibc 2.41 <= Arch, pinned base digest + apt
# snapshot + SHA-256-verified Go toolchain). Output: a stamped binary + SHA256SUMS in -Out.
#
# Windows PowerShell 5.1 compatible (no &&, no ternary). Stamp identity is resolved here (git
# works on the host) and injected into the container via env, so the box needs no .git.
param(
    [string]$Out = "dist/linux",
    [string]$Tags = "",          # extra go build tags (default: plain build, pure-Go stubs, no zig)
    [string]$Image = "rave-mate-linux-build:trixie",
    [switch]$NoCache             # force a clean image rebuild
)
# NOT "Stop": docker writes build progress to stderr, and under Stop PS 5.1 turns a native
# command's stderr line into a terminating error. Gate on $LASTEXITCODE instead.
$ErrorActionPreference = "Continue"

# Repo root = parent of this script's dir.
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$ctx  = Join-Path $repo "contrib/linux-build"
$outAbs = Join-Path $repo $Out
New-Item -ItemType Directory -Force -Path $outAbs | Out-Null

# Build identity (host git).
Push-Location $repo
$sha = (git rev-parse --short HEAD).Trim()
git diff --quiet
$dirty = ""
if ($LASTEXITCODE -ne 0) { $dirty = "-dirty" }
# Monotonic-ish build number: commits on HEAD. Stable + strictly increasing per push.
$build = (git rev-list --count HEAD).Trim()
Pop-Location

Write-Host "== docker build $Image =="
$buildArgs = @("build", "-t", $Image, $ctx)
if ($NoCache) { $buildArgs = @("build", "--no-cache", "-t", $Image, $ctx) }
& docker @buildArgs
if ($LASTEXITCODE -ne 0) { Write-Error "docker build failed ($LASTEXITCODE)"; exit 1 }

Write-Host "== docker run (stamp dev-$sha$dirty build $build, tags '$Tags') =="
# Repo mounted read-only at /src; results land in $outAbs mounted at /out.
$runArgs = @(
    "run", "--rm",
    "-v", "${repo}:/src:ro",
    "-v", "${outAbs}:/out",
    "-e", "RM_SHA=$sha",
    "-e", "RM_DIRTY=$dirty",
    "-e", "RM_BUILD=$build",
    "-e", "RM_TAGS=$Tags",
    $Image
)
& docker @runArgs
if ($LASTEXITCODE -ne 0) { Write-Error "container build failed ($LASTEXITCODE)"; exit 1 }

Write-Host "== done: $outAbs\rave-mate =="
Get-Content (Join-Path $outAbs "SHA256SUMS")
