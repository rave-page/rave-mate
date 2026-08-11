#!/usr/bin/env bash
# POSIX twin of fetch-spout.ps1 - fetches the Spout2 SDK (Windows GPU video share) for the
# `spout` build tag, for CI/Linux cross-builds (the .ps1 only runs on Windows). Downloads the
# official release zip, SHA-256-verifies it, extracts the MT (static-CRT, no VC++ redist)
# SpoutLibrary header + DLL into third_party/spout, then builds a MinGW import lib
# (libSpoutLibrary.a) from the DLL via gendef + dlltool.
#
# Run once before:  CGO_ENABLED=1 go build -tags spout ./cmd/rave-mate  (and before packaging).
# Idempotent. Bump $version/$sha256 to upgrade (honour the 7-day soak - SUPPLY_CHAIN.md).
#
# Tool resolution (Linux cross vs native Windows mingw):
#   dlltool/gendef are looked up as ${MINGW_PREFIX}-<tool> first (e.g. x86_64-w64-mingw32-dlltool
#   on a Debian cross image), then unprefixed. Override the prefix with MINGW_PREFIX=…; default
#   probes the common cross triple.
set -euo pipefail

version="2.007.017"
asset="Spout-SDK-binaries_2-007-017_1.zip"
url="https://github.com/leadedge/Spout2/releases/download/${version}/${asset}"
sha256="695f20e3505fa0da51b2eb959af359f5d9e2c914bb9676e9118d19f6a5424bf4"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # rave-mate/
vd="$root/third_party/spout"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$vd/include" "$vd/lib" "$vd/bin"
zip="$tmp/spout-${version}.zip"

echo "Downloading Spout2 ${version} ..."
curl -fsSL "$url" -o "$zip"

# SHA-256 verify (sha256sum on Linux; shasum -a 256 fallback on macOS).
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$zip" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$zip" | awk '{print $1}')"
fi
got="$(printf '%s' "$got" | tr 'A-Z' 'a-z')"
if [ "$got" != "$sha256" ]; then
  echo "SHA-256 mismatch for $asset" >&2
  echo "  want $sha256" >&2
  echo "  got  $got" >&2
  exit 1
fi
echo "SHA-256 OK"

unzip -q -o "$zip" -d "$tmp/extract"
libs="$tmp/extract/Spout-SDK-binaries/Libs_${version//./-}"

cp -f "$libs/include/SpoutLibrary/SpoutLibrary.h" "$vd/include/"
# A committed PATCHED DLL (fatal LinkGLDXtextures error path fixed - see patches/ +
# scripts/build-spout-dll.ps1) outranks the official binary. The .patched marker carries its
# SHA-256; never clobber it with the release DLL.
if [ -f "$vd/bin/SpoutLibrary.dll.patched" ]; then
  echo "Patched SpoutLibrary.dll present (marker found) - keeping it, official DLL skipped."
else
  cp -f "$libs/MT/bin/SpoutLibrary.dll" "$vd/bin/"
fi

# Resolve gendef + dlltool: prefer the mingw-cross-prefixed variants, fall back to unprefixed.
prefix="${MINGW_PREFIX:-x86_64-w64-mingw32}"
resolve() { # resolve <toolname> → echoes the runnable command
  if command -v "${prefix}-$1" >/dev/null 2>&1; then echo "${prefix}-$1"
  elif command -v "$1" >/dev/null 2>&1; then echo "$1"
  else echo "fetch-spout: required tool '$1' (or ${prefix}-$1) not found" >&2; exit 1; fi
}
GENDEF="$(resolve gendef)"
DLLTOOL="$(resolve dlltool)"

( cd "$vd/lib"
  "$GENDEF" ../bin/SpoutLibrary.dll >/dev/null
  "$DLLTOOL" -d SpoutLibrary.def -l libSpoutLibrary.a -D SpoutLibrary.dll
  rm -f SpoutLibrary.def )

echo "Spout SDK ready in third_party/spout (header + DLL + libSpoutLibrary.a)."
echo "Build:   CGO_ENABLED=1 go build -tags spout ./cmd/rave-mate"
echo "Runtime: SpoutLibrary.dll must sit next to rave-mate.exe (packaging copies it)."
