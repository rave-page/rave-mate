#!/usr/bin/env bash
# Builds the ravezig native core (native/zigcore) for zigdsp-tagged Go builds.
# Requires zig >= 0.16 on PATH (winget zig.zig / https://ziglang.org/download).
# Outputs: native/zigcore/zig-out/lib/libravezig.a (cgo links via -L -lravezig)
#          native/zigcore/zig-out/bin/rave-probe[.exe] (P4 probe worker exe,
#          opt-in via config features.workers.probeExe).
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
ls -la . ../bin
echo "ravezig + rave-probe built ($ver, ${target:-native})"

# ── raveui webui render lib (native/zigui, tag zigui) — appended block; zigcore lines
# above stay untouched. cwd here is native/zigcore/zig-out/lib.
cd ../../../zigui
"$ZIG" build -Drelease ${target:+-Dtarget=$target}
cd zig-out/lib
[ -f raveui.lib ] && cp -f raveui.lib libraveui.a
ls -la
echo "raveui built ($ver, ${target:-native})"
