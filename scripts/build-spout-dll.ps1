# Builds the PATCHED SpoutLibrary.dll from source (Windows, needs Visual Studio 2022 + CMake).
#
# WHY a patched DLL: the official 2.007.017 binary crashes ITS HOST PROCESS when GL/DX interop
# creation fails - spoutGL::LinkGLDXtextures formats ~103 chars into char[128] then strcat_s's
# ~34 more; the static CRT __fastfail's (int 29h, uncatchable). Killed the rave-mate daemon
# three times before the shim's interop pre-flight landed; the patch (buffer 128 -> 256,
# patches/spoutgl-linkgldx-bufferoverflow.patch) removes the failure mode at the source.
# Building from the SOURCE tag also makes the DLL's vtable match our vendored SpoutLibrary.h -
# the official binary was built from a different interface revision (the "vtable window"
# documented in internal/videoshare/spout_shim.cpp).
#
# Output: third_party/spout/bin/SpoutLibrary.dll (+ .pdb beside it, + .patched marker with the
# DLL's SHA-256). The committed DLL in git is this script's output; re-run to reproduce/upgrade.
$ErrorActionPreference = "Stop"

$version = "2.007.017"
$srcUrl  = "https://github.com/leadedge/Spout2/archive/refs/tags/$version.zip"
$srcSha  = "1CC0C958EE14AF614744AC40C054C2FD7DF17F8C5B94EB8ECFEACB37F5B06460"

$root  = Split-Path -Parent $PSScriptRoot          # rave-mate/
$vd    = Join-Path $root "third_party/spout"
$patch = Join-Path $vd "patches/spoutgl-linkgldx-bufferoverflow.patch"
$work  = Join-Path $env:TEMP "spout2-dll-build"    # short path: MSBuild FileTracker dies on deep ones (FTK1011)

$zip = Join-Path $env:TEMP "spout2-src-$version.zip"
Write-Host "Downloading Spout2 $version source ..."
Invoke-WebRequest -Uri $srcUrl -OutFile $zip -UseBasicParsing -TimeoutSec 180
$got = (Get-FileHash $zip -Algorithm SHA256).Hash
if ($got -ne $srcSha) { throw "SHA-256 mismatch for source zip`n  want $srcSha`n  got  $got" }
Write-Host "SHA-256 OK"

if (Test-Path $work) { Remove-Item -Recurse -Force $work }
Expand-Archive $zip -DestinationPath $work
$src = Join-Path $work "Spout2-$version"

Write-Host "Applying $((Split-Path -Leaf $patch)) ..."
& git -C $src apply --ignore-whitespace $patch
if ($LASTEXITCODE -ne 0) { throw "patch failed to apply" }

Write-Host "Configuring (MSVC, /MT, PDB) ..."
& cmake -S $src -B "$src/build" -A x64 -DSPOUT_BUILD_CMT=ON -DSPOUT_BUILD_LIBRARY=ON -DSPOUT_BUILD_SPOUTDX=OFF `
  "-DCMAKE_CXX_FLAGS_RELEASE=/MT /O2 /Ob2 /DNDEBUG /Zi" `
  "-DCMAKE_SHARED_LINKER_FLAGS_RELEASE=/DEBUG /OPT:REF /OPT:ICF"
if ($LASTEXITCODE -ne 0) { throw "cmake configure failed" }
& cmake --build "$src/build" --config Release --target SpoutLibrary
if ($LASTEXITCODE -ne 0) { throw "build failed" }

New-Item -ItemType Directory -Force "$vd/bin" | Out-Null
Copy-Item "$src/build/bin/Release/SpoutLibrary.dll" "$vd/bin/" -Force
Copy-Item "$src/build/bin/Release/SpoutLibrary.pdb" "$vd/bin/" -Force
(Get-FileHash "$vd/bin/SpoutLibrary.dll" -Algorithm SHA256).Hash.ToLower() |
  Set-Content -NoNewline "$vd/bin/SpoutLibrary.dll.patched"

Write-Host "Patched SpoutLibrary.dll + .pdb installed into third_party/spout/bin."
Write-Host "Commit the DLL + .patched marker; fetch-spout skips the DLL while the marker exists."
