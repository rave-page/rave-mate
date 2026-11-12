#!/usr/bin/env bash
# Builds native/zigmedia (realtime media transport, C ABI include/ravemedia.h). Requires zig >= 0.16.
# Output: native/zigmedia/zig-out/lib/libravemedia.a (gnu targets) or ravemedia.lib (msvc/windows).
# The Linux VA shim links this + includes ravemedia.h; the Windows encode child the same.
set -euo pipefail
cd "$(dirname "$0")/../native/zigmedia"

ZIG="${ZIG:-zig}"
if ! command -v "$ZIG" >/dev/null 2>&1; then
  ZIG="$(ls -d "$LOCALAPPDATA"/Microsoft/WinGet/Links/zig 2>/dev/null || true)"
  [ -n "$ZIG" ] || { echo "zig not found — install zig >= 0.16" >&2; exit 1; }
fi
ver="$("$ZIG" version)"
case "$ver" in
  0.1[6-9].*|[1-9].*) ;;
  *) echo "zig $ver too old — need >= 0.16" >&2; exit 1 ;;
esac

target=""
case "${GOOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}" in
  windows|msys*|mingw*) target="x86_64-windows-gnu" ;;
  linux)   target="x86_64-linux-gnu" ;;
  darwin)  target="aarch64-macos" ;;
esac
targ=()
[ -n "$target" ] && targ=(-Dtarget="$target")

"$ZIG" build test "${targ[@]}"   # wire-core goldens vs the Go implementation
"$ZIG" build "${targ[@]}"
echo "built: $(ls native 2>/dev/null; pwd)/zig-out/lib/"
ls -la zig-out/lib/
