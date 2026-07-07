# Fetches the Spout2 SDK (Windows GPU video sharing) for the `spout` build tag.
# Downloads the official release zip, SHA-256-verifies it, extracts the MT (static-CRT, no
# VC++ redist) SpoutLibrary header + DLL into third_party/spout, and generates a MinGW import
# lib (libSpoutLibrary.a) from the DLL via gendef + dlltool.
#
# Run once before:  go build -tags spout ./...   (and before packaging with Spout).
# Re-run is idempotent. Bump $version/$sha256 to upgrade (honour the 7-day soak, SUPPLY_CHAIN.md).
$ErrorActionPreference = "Stop"

$version = "2.007.017"
$asset   = "Spout-SDK-binaries_2-007-017_1.zip"
$url     = "https://github.com/leadedge/Spout2/releases/download/$version/$asset"
$sha256  = "695F20E3505FA0DA51B2EB959AF359F5D9E2C914BB9676E9118D19F6A5424BF4"

$root = Split-Path -Parent $PSScriptRoot          # rave-mate/
$vd   = Join-Path $root "third_party/spout"
$tmp  = Join-Path $env:TEMP "rave-spout-sdk"

New-Item -ItemType Directory -Force "$vd/include","$vd/lib","$vd/bin" | Out-Null
$zip = Join-Path $env:TEMP "spout-$version.zip"

Write-Host "Downloading Spout2 $version ..."
Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing -TimeoutSec 180

$got = (Get-FileHash $zip -Algorithm SHA256).Hash
if ($got -ne $sha256) { throw "SHA-256 mismatch for $asset`n  want $sha256`n  got  $got" }
Write-Host "SHA-256 OK"

if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
Expand-Archive $zip -DestinationPath $tmp
$libs = Join-Path $tmp "Spout-SDK-binaries/Libs_$($version.Replace('.','-'))"

Copy-Item "$libs/include/SpoutLibrary/SpoutLibrary.h" "$vd/include/" -Force
Copy-Item "$libs/MT/bin/SpoutLibrary.dll"             "$vd/bin/"     -Force

Push-Location "$vd/lib"
try {
  & gendef ../bin/SpoutLibrary.dll | Out-Null
  & dlltool -d SpoutLibrary.def -l libSpoutLibrary.a -D SpoutLibrary.dll
  Remove-Item SpoutLibrary.def -ErrorAction SilentlyContinue
} finally { Pop-Location }

Write-Host "Spout SDK ready in third_party/spout (header + DLL + libSpoutLibrary.a)."
Write-Host "Build:  CGO_ENABLED=1 go build -tags spout ./cmd/rave-mate"
Write-Host "Runtime: SpoutLibrary.dll must sit next to rave-mate.exe (packaging copies it)."
