# Builds all Zig native libs for tagged Go builds (see build-zig.sh for the POSIX twin).
# zigcore: libravezig.a (tag zigdsp) + zig-out\bin\rave-probe.exe (features.workers.probeExe)
# zigui:   libraveui.a  (tag zigui)
# zigvr:   libravevr.a  (tag zigvr)
$ErrorActionPreference = "Stop"
$zig = Get-Command zig -ErrorAction SilentlyContinue
if (-not $zig) { $zig = Get-Command "$env:LOCALAPPDATA\Microsoft\WinGet\Links\zig.exe" -ErrorAction SilentlyContinue }
if (-not $zig) { throw "zig not found - install zig >= 0.16 (winget install zig.zig)" }

Set-Location (Join-Path $PSScriptRoot "..\native\zigcore")
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Copy-Item -Force zig-out\lib\ravezig.lib zig-out\lib\libravezig.a
Write-Host "ravezig + rave-probe built ($(& $zig.Source version))"

# raveui webui render lib (native/zigui, tag zigui)
Set-Location (Join-Path $PSScriptRoot "..\native\zigui")
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Copy-Item -Force zig-out\lib\raveui.lib zig-out\lib\libraveui.a
Write-Host "raveui built ($(& $zig.Source version))"

# ravevr VR-overlay raster lib (native/zigvr, tag zigvr)
Set-Location (Join-Path $PSScriptRoot "..\native\zigvr")
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Copy-Item -Force zig-out\lib\ravevr.lib zig-out\lib\libravevr.a
Write-Host "ravevr built ($(& $zig.Source version))"

# rave-mate-vfx effects child exe (native/zigvfx: frei0r host, ISF next; no cgo)
Set-Location (Join-Path $PSScriptRoot "..\native\zigvfx")
& $zig.Source build -Drelease -Dtarget=x86_64-windows-gnu
Write-Host "rave-mate-vfx built ($(& $zig.Source version))"
