#!/usr/bin/env bash
# Builds the ravezig native core (native/zigcore) for zigdsp-tagged Go builds.
# Requires zig >= 0.16 on PATH (winget zig.zig / https://ziglang.org/download).
# Output: native/zigcore/zig-out/lib/libravezig.a (cgo links via -L -lravezig).
set -euo pipefail
cd "$(dirname "$0")/../native/zigcore"

ZIG="${ZIG:-zig}"
if ! command -v "$ZIG" >/dev/null 2>&1; then
  # winget install location fallback (dev machines)
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
  linux) target="x86_64-linux-gnu" ;;
  darwin) target="aarch64-macos" ;;
esac

"$ZIG" build -Drelease ${target:+-Dtarget=$target}
# zig names gnu-target static libs <name>.lib on Windows; cgo -l wants libravezig.a
cd zig-out/lib
[ -f ravezig.lib ] && cp -f ravezig.lib libravezig.a
ls -la
echo "ravezig built ($ver, ${target:-native})"

# --- zigvr: VR-overlay raster lib (native/zigvr → libravevr.a, Go tag `zigvr`) ---
cd ../../../zigvr
"$ZIG" build -Drelease ${target:+-Dtarget=$target}
cd zig-out/lib
[ -f ravevr.lib ] && cp -f ravevr.lib libravevr.a
ls -la
echo "ravevr built ($ver, ${target:-native})"
