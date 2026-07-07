#!/usr/bin/env bash
# Fetches the Ableton Link SDK (+ its asio-standalone submodule) for the `abletonlink` build
# tag and compiles the C wrapper into a static archive the cgo package links. Mirror of
# fetch-spout.sh: two pinned, SHA-256-verified source tarballs (no git submodules - CI-friendly),
# then a single C++ compile → third_party/link/lib/libabl_link.a.
#
# Link is header-only C++ EXCEPT the abl_link C wrapper (abl_link.cpp). cgo can't carry a stray
# .cpp (the untagged build would reject it), so we compile abl_link.cpp + the header-only Link
# runtime + asio into libabl_link.a here; the cgo file only needs abl_link.h + the archive.
#
# Run once before:  CGO_ENABLED=1 go build -tags abletonlink ./cmd/rave-mate  (and before packaging).
# Idempotent. Bump the pins below to upgrade (honour the 7-day soak - SUPPLY_CHAIN.md).
#
# Cross vs native toolchain:
#   Windows cross (CI): set CXX=x86_64-w64-mingw32-g++ (or MINGW_PREFIX=x86_64-w64-mingw32) - the
#   archive is built for Windows (LINK_PLATFORM_WINDOWS). Native builds set LINK_TARGET_OS=linux|
#   macos to pick the right platform macro. CXX (or ${MINGW_PREFIX}-g++, else g++) is the compiler;
#   ar is resolved the same prefixed→plain way.
#
# LICENSE: Ableton Link is dual GPLv2+/commercial; rave-mate is NOT GPL. Building for dev is fine,
# but DISTRIBUTING a Link-enabled binary needs Ableton's free Link license grant
# (https://www.ableton.com/en/link/). Request it before shipping.
set -euo pipefail

# ── pins (Link-3.1.5, released 2025-12-01; asio at the commit that tag's submodule references) ──
link_commit="45931b8bd2a7066f1950f2a0beab81a0c51f28fe"   # Ableton/link Link-3.1.5
link_sha256="4fcabe1eb377dd29c1b3884757f9d44fb067be3bfd7e3f6b534f2452b19d7227"
asio_commit="231cb29bab30f82712fcd54faaea42424cc6e710"   # chriskohlhoff/asio (Link submodule pin)
asio_sha256="5def09efbd4be199dd6ddca53a2c99b9eef696f6b430910d896594b04ff59108"

link_url="https://codeload.github.com/Ableton/link/tar.gz/${link_commit}"
asio_url="https://codeload.github.com/chriskohlhoff/asio/tar.gz/${asio_commit}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # rave-mate/
vd="$root/third_party/link"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# sha256 helper (sha256sum on Linux; shasum -a 256 fallback on macOS).
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
verify() { # verify <file> <want-sha> <label>
  local got; got="$(sha256 "$1" | tr 'A-Z' 'a-z')"
  if [ "$got" != "$2" ]; then
    echo "SHA-256 mismatch for $3" >&2; echo "  want $2" >&2; echo "  got  $got" >&2; exit 1
  fi
  echo "SHA-256 OK ($3)"
}

echo "Downloading Ableton Link ${link_commit:0:10} ..."
curl -fsSL "$link_url" -o "$tmp/link.tgz"
verify "$tmp/link.tgz" "$link_sha256" "Ableton/link"
echo "Downloading asio-standalone ${asio_commit:0:10} ..."
curl -fsSL "$asio_url" -o "$tmp/asio.tgz"
verify "$tmp/asio.tgz" "$asio_sha256" "chriskohlhoff/asio"

# Extract Link + asio into the temp dir (NOT third_party/link - the tarball roots carry their own
# .gitignore/README/LICENSE that would clobber our committed ones). asio goes where Link's headers
# expect the submodule (modules/asio-standalone). We compile here, then place only the two artifacts.
mkdir -p "$tmp/link/modules/asio-standalone"
tar xzf "$tmp/link.tgz" -C "$tmp/link" --strip-components=1
tar xzf "$tmp/asio.tgz" -C "$tmp/link/modules/asio-standalone" --strip-components=1

# Resolve the C++ compiler + ar (prefer the mingw-cross-prefixed variants, fall back to plain).
prefix="${MINGW_PREFIX:-x86_64-w64-mingw32}"
resolve() { # resolve <tool> [envoverride] → echoes a runnable command
  if [ -n "${2:-}" ]; then echo "$2"; return; fi
  if command -v "${prefix}-$1" >/dev/null 2>&1; then echo "${prefix}-$1"
  elif command -v "$1" >/dev/null 2>&1; then echo "$1"
  else echo "fetch-link: required tool '$1' (or ${prefix}-$1) not found" >&2; exit 1; fi
}
CXXBIN="$(resolve g++ "${CXX:-}")"
ARBIN="$(resolve ar)"

# Platform macro picks Link's per-OS clock/socket code. Default Windows (the CI cross target);
# native builds override via LINK_TARGET_OS.
case "${LINK_TARGET_OS:-windows}" in
  windows) plat="-DLINK_PLATFORM_WINDOWS=1 -D_WIN32_WINNT=0x0601" ;;
  linux)   plat="-DLINK_PLATFORM_LINUX=1" ;;
  macos|darwin) plat="-DLINK_PLATFORM_MACOSX=1" ;;
  *) echo "fetch-link: unknown LINK_TARGET_OS='${LINK_TARGET_OS}'" >&2; exit 1 ;;
esac

echo "Compiling abl_link.cpp with $CXXBIN (${LINK_TARGET_OS:-windows}) ..."
( cd "$tmp/link"
  # -w: Link's headers use multi-char constants (-Wmultichar) intentionally; silence the noise.
  "$CXXBIN" -std=c++17 -O2 -w $plat -DASIO_STANDALONE \
    -I extensions/abl_link/include \
    -I include \
    -I modules/asio-standalone/asio/include \
    -c extensions/abl_link/src/abl_link.cpp -o "$tmp/abl_link.o" )

# Place ONLY the two artifacts the cgo package needs (header + static archive) into third_party/link,
# under subdirs so our tracked .gitignore/README.md are never touched (mirrors third_party/spout).
rm -rf "$vd/extensions" "$vd/lib"
mkdir -p "$vd/extensions/abl_link/include" "$vd/lib"
cp -f "$tmp/link/extensions/abl_link/include/abl_link.h" "$vd/extensions/abl_link/include/"
"$ARBIN" rcs "$vd/lib/libabl_link.a" "$tmp/abl_link.o"

echo "Ableton Link SDK ready in third_party/link (abl_link.h + libabl_link.a)."
echo "Build:   CGO_ENABLED=1 go build -tags abletonlink ./cmd/rave-mate"
echo "LICENSE: distributing Link-enabled binaries needs Ableton's free Link license grant."
