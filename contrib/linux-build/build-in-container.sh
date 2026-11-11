#!/bin/bash
# Builds a stamped rave-mate linux/amd64 binary from the repo mounted at /src into /out.
# Stamp identity is injected by env (RM_SHA/RM_BUILD/RM_DIRTY), set by the host wrapper where
# git works, so the container needs no .git. RM_TAGS (default empty = plain build, pure-Go
# stubs, no zig) and RM_PKG (default ./cmd/rave-mate) are overridable.
set -euo pipefail

: "${RM_SHA:=unknown}"
: "${RM_BUILD:=0}"
: "${RM_DIRTY:=}"
: "${RM_TAGS:=}"
: "${RM_PKG:=./cmd/rave-mate}"

export GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod
mkdir -p /out /tmp/gocache /tmp/gomod

# Build straight from the read-only source mount: `go build` writes only to GOCACHE/GOMODCACHE
# and the -o target, never into the module tree (-mod=readonly, GOWORK=off). Deps resolve from
# the committed go.sum.
cd /src

V="dev-${RM_SHA}${RM_DIRTY}"
LDFLAGS="-s -w -X rave.page/mate/internal/version.Version=${V} -X rave.page/mate/internal/version.Commit=${RM_SHA} -X rave.page/mate/internal/version.Build=${RM_BUILD}"

echo "== go env =="
go version
echo "GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) CGO_ENABLED=$(go env CGO_ENABLED) tags='${RM_TAGS}'"

echo "== build =="
if [ -n "${RM_TAGS}" ]; then
    go build -tags "${RM_TAGS}" -ldflags "${LDFLAGS}" -o /out/rave-mate "${RM_PKG}"
else
    go build -ldflags "${LDFLAGS}" -o /out/rave-mate "${RM_PKG}"
fi

cd /out
sha256sum rave-mate > SHA256SUMS
echo "== stamp (go version -m) =="
go version -m /out/rave-mate | grep -E 'version\.(Version|Commit|Build)=' || true
echo "== result =="
ls -l /out/rave-mate
cat SHA256SUMS
