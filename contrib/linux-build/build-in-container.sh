#!/bin/bash
# Builds a stamped rave-mate linux/amd64 binary from the repo mounted at /src into /out.
# Stamp identity is injected by env (RM_SHA/RM_BUILD/RM_DIRTY), set by the host wrapper where
# git works, so the container needs no .git. RM_TAGS (default empty = plain build, pure-Go
# stubs, no zig) and RM_PKG (default ./cmd/rave-mate) are overridable.
# Updater arming (CI nightly/release; same stamps as the Windows exe): RM_VERSION (default
# dev-<sha>[-dirty]), RM_CHANNEL, RM_FEEDURL, RM_EXTRA_LDFLAGS (e.g. the empty UpdatePubKey
# override of an unsigned fork build). Empty RM_FEEDURL = updater off (dev build).
set -euo pipefail

: "${RM_SHA:=unknown}"
: "${RM_BUILD:=0}"
: "${RM_DIRTY:=}"
: "${RM_TAGS:=}"
: "${RM_PKG:=./cmd/rave-mate}"
: "${RM_VERSION:=}"
: "${RM_CHANNEL:=}"
: "${RM_FEEDURL:=}"
: "${RM_EXTRA_LDFLAGS:=}"

export GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod
mkdir -p /out /tmp/gocache /tmp/gomod

has_tag() { printf '%s' " ${RM_TAGS} " | grep -q " $1 "; }

# Build straight from the read-only source mount: `go build` writes only to GOCACHE/GOMODCACHE
# and the -o target, never into the module tree (-mod=readonly, GOWORK=off). Deps resolve from
# the committed go.sum. The zig* tags link native/*/zig-out libs via ${SRCDIR}-relative cgo
# paths, so those builds run from a writable copy of the tree instead (/src stays untouched).
SRC=/src
if has_tag zigdsp || has_tag zigui || has_tag zigvr; then
    echo "== zig tags: building from a writable copy =="
    mkdir -p /tmp/src
    tar -C /src --exclude=./dist --exclude=./.git --exclude='./native/*/zig-out' --exclude='./native/*/.zig-cache' -cf - . | tar -C /tmp/src -xf -
    SRC=/tmp/src
    ( cd "$SRC" && GOOS=linux bash scripts/build-zig.sh )
fi
cd "$SRC"

V="${RM_VERSION:-dev-${RM_SHA}${RM_DIRTY}}"
P=rave.page/mate/internal/version
LDFLAGS="-s -w -X $P.Version=${V} -X $P.Commit=${RM_SHA} -X $P.Build=${RM_BUILD}"
[ -z "${RM_CHANNEL}" ] || LDFLAGS="$LDFLAGS -X $P.Channel=${RM_CHANNEL}"
[ -z "${RM_FEEDURL}" ] || LDFLAGS="$LDFLAGS -X $P.FeedURL=${RM_FEEDURL}"
[ -z "${RM_EXTRA_LDFLAGS}" ] || LDFLAGS="$LDFLAGS ${RM_EXTRA_LDFLAGS}"

echo "== go env =="
go version
echo "GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) CGO_ENABLED=$(go env CGO_ENABLED) tags='${RM_TAGS}'"

# A -tags pipewire build links libravemedia.a (native realtime TX). Build it with the pinned Zig into a
# writable prefix (the source mount is read-only) and point cgo's linker at it via CGO_LDFLAGS.
if has_tag pipewire; then
    echo "== build libravemedia (native/zigmedia) =="
    mkdir -p /tmp/zm /tmp/zc
    ( cd "$SRC/native/zigmedia" && zig build -Dtarget=x86_64-linux-gnu --prefix /tmp/zm --cache-dir /tmp/zc )
    export CGO_LDFLAGS="-L/tmp/zm/lib ${CGO_LDFLAGS:-}"
    ls -l /tmp/zm/lib/
fi

echo "== build =="
if [ -n "${RM_TAGS}" ]; then
    go build -tags "${RM_TAGS}" -ldflags "${LDFLAGS}" -o /out/rave-mate "${RM_PKG}"
else
    go build -ldflags "${LDFLAGS}" -o /out/rave-mate "${RM_PKG}"
fi

cd /out
sha256sum rave-mate > SHA256SUMS
echo "== stamp (go version -m) =="
go version -m /out/rave-mate | grep -E 'version\.(Version|Commit|Build|Channel|FeedURL)=|-tags=' || true
# glibc baseline actually required (highest versioned symbol) + load-time deps: libpipewire,
# libva, EGL/GLES/gbm must NOT appear here - they are dlopen'ed so a host without them degrades.
echo "== glibc baseline (max GLIBC_ symbol) =="
objdump -T /out/rave-mate | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1
echo "== NEEDED =="
objdump -p /out/rave-mate | awk '/NEEDED/ {print $2}'
echo "== result =="
ls -l /out/rave-mate
cat SHA256SUMS
