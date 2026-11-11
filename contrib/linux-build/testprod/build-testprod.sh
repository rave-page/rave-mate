#!/bin/bash
# Builds the libfunnel dmabuf test producer (DEV TOOL, not shipped) in the Linux build box.
# libfunnel sources are mounted read-only at /libfunnel (the submodule vendored in
# spout2pw/subprojects/libfunnel); our producer is at /src. Direct gcc (libfunnel has no
# generated headers), so no meson/ninja needed. Output -> /out.
set -euo pipefail

LF=/libfunnel
SRC=/src/contrib/linux-build/testprod
OUT=/out
mkdir -p "$OUT"

PW_GBM_DRM="$(pkg-config --cflags --libs libpipewire-0.3 gbm libdrm)"
EGL="$(pkg-config --cflags --libs egl)"

echo "== libfunnel core =="
gcc -O2 -fPIC -shared -o "$OUT/libfunnel.so" "$LF/src/funnel.c" \
    -I"$LF/include" $PW_GBM_DRM

echo "== libfunnel egl =="
gcc -O2 -fPIC -shared -o "$OUT/libfunnel-egl.so" "$LF/src/egl.c" \
    -I"$LF/include" -L"$OUT" -lfunnel $PW_GBM_DRM $EGL

echo "== funnel-producer =="
gcc -O2 -o "$OUT/funnel-producer" "$SRC/funnel-producer.c" \
    -I"$LF/include" -L"$OUT" -lfunnel -lfunnel-egl \
    $PW_GBM_DRM $EGL -lGL -lGLESv2

echo "== result =="
ls -l "$OUT"/funnel-producer "$OUT"/libfunnel*.so
