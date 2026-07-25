# Builds the ravezig native core for zigdsp-tagged Go builds (see build-zig.sh).
# Also installs zig-out\bin\rave-probe.exe (P4 probe worker, opt-in via features.workers.probeExe).
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\native\zigcore")
$zig = Get-Command zig -ErrorAction SilentlyContinue
if (-not $zig) { $zig = Get-Command "$env:LOCALAPPDATA\Microsoft\WinGet\Links\zig.exe" -ErrorAction SilentlyContinue }
if (-not $zig) { throw "zig not found - install zig >= 0.16 (winget install zig.zig)" }
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Copy-Item -Force zig-out\lib\ravezig.lib zig-out\lib\libravezig.a
Write-Host "ravezig + rave-probe built ($(& $zig.Source version))"

# raveui webui render lib (native/zigui, tag zigui) - appended; zigcore lines above stay untouched.
Set-Location (Join-Path $PSScriptRoot "..
ative\zigui")
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Copy-Item -Force zig-out\libaveui.lib zig-out\lib\libraveui.a
Write-Host "raveui built ($(& $zig.Source version))"
