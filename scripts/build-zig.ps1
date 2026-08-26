# Builds all Zig native libs for tagged Go builds (see build-zig.sh for the POSIX twin).
# zigcore: libravezig.a (tag zigdsp) + zig-out\bin\rave-probe.exe (features.workers.probeExe)
# zigui:   libraveui.a  (tag zigui)
# zigvr:   libravevr.a  (tag zigvr)
# zigenc:  rave-mate-enc.exe (tag encembed)
$ErrorActionPreference = "Stop"
$zig = Get-Command zig -ErrorAction SilentlyContinue
if (-not $zig) { $zig = Get-Command "$env:LOCALAPPDATA\Microsoft\WinGet\Links\zig.exe" -ErrorAction SilentlyContinue }
if (-not $zig) { throw "zig not found - install zig >= 0.16 (winget install zig.zig)" }

function Build-ZigComponent([string]$name) {
  & $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
  if ($LASTEXITCODE -ne 0) { throw "$name build failed ($LASTEXITCODE)" }
}

Set-Location (Join-Path $PSScriptRoot "..\native\zigcore")
Build-ZigComponent "zigcore"
Copy-Item -Force zig-out\lib\ravezig.lib zig-out\lib\libravezig.a
Write-Host "ravezig + rave-probe built ($(& $zig.Source version))"

# raveui webui render lib (native/zigui, tag zigui)
Set-Location (Join-Path $PSScriptRoot "..\native\zigui")
Build-ZigComponent "zigui"
Copy-Item -Force zig-out\lib\raveui.lib zig-out\lib\libraveui.a
# Stage the PSH1 window child for go:embed (tag shellembed), exactly as build-zig.sh does. Without
# this the exe keeps embedding a STALE rave-shell.exe and every zigui shell change silently no-ops.
$shell = "zig-out\bin\rave-shell.exe"
if (Test-Path $shell) {
  $dst = Join-Path $PSScriptRoot "..\internal\webui\embedded"
  New-Item -ItemType Directory -Force -Path $dst | Out-Null
  Copy-Item -Force $shell (Join-Path $dst "rave-shell.exe")
} else {
  Write-Warning "native/zigui/zig-out/bin/rave-shell.exe absent - builds fall back to the in-process Go window"
}
Write-Host "raveui + rave-shell built ($(& $zig.Source version))"

# ravevr VR-overlay raster lib (native/zigvr, tag zigvr)
Set-Location (Join-Path $PSScriptRoot "..\native\zigvr")
Build-ZigComponent "zigvr"
Copy-Item -Force zig-out\lib\ravevr.lib zig-out\lib\libravevr.a
Write-Host "ravevr built ($(& $zig.Source version))"

# Per-adapter MF encoder child + go:embed staging (tag encembed).
Set-Location (Join-Path $PSScriptRoot "..\native\zigenc")
Build-ZigComponent "zigenc"
$encDst = Join-Path $PSScriptRoot "..\internal\mfenc\embedded"
New-Item -ItemType Directory -Force -Path $encDst | Out-Null
Copy-Item -Force zig-out\bin\rave-mate-enc.exe (Join-Path $encDst "rave-mate-enc.exe")
Write-Host "rave-mate-enc built + embed-staged ($(& $zig.Source version))"

# rave-mate-vfx effects child exe (native/zigvfx: frei0r host, ISF next; no cgo)
Set-Location (Join-Path $PSScriptRoot "..\native\zigvfx")
Build-ZigComponent "zigvfx"
Write-Host "rave-mate-vfx built ($(& $zig.Source version))"
