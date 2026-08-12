#!/usr/bin/env bash
# Builds the frei0r plugin collection (https://github.com/dyne/frei0r, GPL-2.0+) for
# Windows from the pinned source tag - the official win64 release binaries link DEBUG
# CRT DLLs (VCRUNTIME140D/ucrtbased, non-redistributable), so we build our own with
# MinGW + static libgcc/libstdc++: self-contained DLLs needing only KERNEL32 + UCRT.
#
# Output: third_party/frei0r/bin/*.dll (~155 plugins) + COPYING + AUTHORS.md +
# SOURCE.txt (GPL corresponding-source pointer). Optional deps (OpenCV/Cairo/gavl/
# facedetect) are disabled - those plugins are excluded.
#
# Runs native on Windows (git-bash/MSYS, plain gcc) and cross on Linux CI
# (x86_64-w64-mingw32-*, override with MINGW_PREFIX). Needs cmake; uses Ninja when
# present, else Unix Makefiles. Idempotent: skips when the staged tag matches.
# Bump $version/$sha256 together (honour the 7-day soak - SUPPLY_CHAIN.md).
set -euo pipefail

version="3.2.3"
url="https://github.com/dyne/frei0r/archive/refs/tags/v${version}.tar.gz"
sha256="898f80e5fdae6108a2d9b2317649af576a4b5e636c73429ee11b64397a596e12"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
vd="$root/third_party/frei0r"

if [ -f "$vd/SOURCE.txt" ] && grep -q "v${version}" "$vd/SOURCE.txt" && ls "$vd/bin"/*.dll >/dev/null 2>&1; then
  echo "frei0r ${version} already staged in third_party/frei0r - skipping (delete to rebuild)."
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

tarball="$tmp/frei0r-${version}.tar.gz"
echo "Downloading frei0r ${version} source ..."
curl -fsSL "$url" -o "$tarball"

if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tarball" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tarball" | awk '{print $1}')"
fi
got="$(printf '%s' "$got" | tr 'A-Z' 'a-z')"
if [ "$got" != "$sha256" ]; then
  echo "SHA-256 mismatch for frei0r v${version} source" >&2
  echo "  want $sha256" >&2
  echo "  got  $got" >&2
  exit 1
fi
echo "SHA-256 OK"

tar xzf "$tarball" -C "$tmp"
src="$tmp/frei0r-${version}"

# Toolchain: native gcc on Windows shells, mingw cross triple elsewhere.
cross_args=()
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) ;;
  *)
    prefix="${MINGW_PREFIX:-x86_64-w64-mingw32}"
    command -v "${prefix}-gcc" >/dev/null 2>&1 || { echo "build-frei0r: ${prefix}-gcc not found" >&2; exit 1; }
    cross_args=(
      -DCMAKE_SYSTEM_NAME=Windows
      -DCMAKE_C_COMPILER="${prefix}-gcc"
      -DCMAKE_CXX_COMPILER="${prefix}-g++"
      -DCMAKE_RC_COMPILER="${prefix}-windres"
      -DCMAKE_FIND_ROOT_PATH_MODE_PROGRAM=NEVER
    )
    ;;
esac

gen="Unix Makefiles"
command -v ninja >/dev/null 2>&1 && gen="Ninja"

FREI0R_VERSION="$version" cmake -S "$src" -B "$tmp/build" -G "$gen" \
  -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF \
  -DWITHOUT_OPENCV=ON -DWITHOUT_CAIRO=ON -DWITHOUT_GAVL=ON -DWITHOUT_FACERECOGNITION=ON \
  "-DCMAKE_SHARED_LINKER_FLAGS=-static-libgcc -static-libstdc++ -static" \
  "-DCMAKE_MODULE_LINKER_FLAGS=-static-libgcc -static-libstdc++ -static"
cmake --build "$tmp/build" --parallel

mkdir -p "$vd/bin"
rm -f "$vd/bin"/*.dll
find "$tmp/build/src" -name '*.dll' -exec cp -f {} "$vd/bin/" \;
n="$(ls "$vd/bin"/*.dll | wc -l)"
[ "$n" -ge 100 ] || { echo "build-frei0r: only $n DLLs built - expected ~155" >&2; exit 1; }

cp -f "$src/COPYING" "$vd/bin/"
cp -f "$src/AUTHORS.md" "$vd/bin/"
cat > "$vd/SOURCE.txt" <<EOF
frei0r v${version} (GPL-2.0-or-later), built from source with MinGW (static libgcc/libstdc++).
Corresponding source: ${url}  (SHA-256 ${sha256})
Upstream: https://github.com/dyne/frei0r
EOF
cp -f "$vd/SOURCE.txt" "$vd/bin/SOURCE.txt"

echo "frei0r ${version}: $n plugin DLLs staged in third_party/frei0r/bin."
echo "Packaging ships that folder as <install>/frei0r; rave-mate scans it at runtime."
