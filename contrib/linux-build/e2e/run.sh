#!/bin/bash
# Linux unattended self-update e2e, run INSIDE the contrib/linux-build box as a NON-root user:
# old build N (FeedURL -> local fake feed, throwaway Ed25519 key, signature enforced) runs
# `--service` from a user-owned install behind a symlink, then updates itself to N+1 through the
# real path: poll -> verify sig+sha256 -> probe -> idle wait -> swap (.prev) -> clean shutdown ->
# re-exec (same PID) -> health confirm.
#   docker run --rm --user 1000:1000 -v <repo>:/src:ro --entrypoint /bin/bash \
#     rave-mate-linux-build:trixie /src/contrib/linux-build/e2e/run.sh
# Takes ~3-4 min (30 s first-check settle + 2 min continuous-idle gate + 30 s health window).
set -euo pipefail
export HOME=/tmp/e2e/home XDG_CONFIG_HOME=/tmp/e2e/home/.config GOCACHE=/tmp/e2e/gocache GOMODCACHE=/tmp/e2e/gomod
W=/tmp/e2e FEED=/tmp/e2e/feed INST=/tmp/e2e/home/.local/share/rave-mate BIN=/tmp/e2e/home/bin
mkdir -p "$FEED" "$INST" "$BIN" "$W/out"
cd /src
N=${E2E_BUILD:-1000}
N1=$((N + 1))
applog() { find "$HOME" -name rave-mate.log 2>/dev/null | head -1; }
fail() { echo "FAIL: $*"; echo "--- app log tail ---"; tail -40 "$W/app.log" "$(applog)" 2>/dev/null || true; exit 1; }

echo "== feed key (throwaway) =="
go build -o "$W/feedtool" ./contrib/linux-build/e2e/feed
PUB=$("$W/feedtool" -keygen "$W/key")
echo "test UpdatePubKey $PUB"

V=rave.page/mate/internal/version
build() { # <build> <out>: stamped exactly like the nightly (Channel + FeedURL + Build), test key
	go build -ldflags "-s -w -X $V.Version=e2e-$1 -X $V.Commit=e2e -X $V.Build=$1 -X $V.Channel=nightly \
		-X $V.FeedURL=http://127.0.0.1:8099/feed/ -X $V.UpdatePubKey=$PUB" -o "$2" ./cmd/rave-mate
}
echo "== build old ($N) + new ($N1) =="
build "$N" "$W/out/old"
build "$N1" "$W/out/new"
cp "$W/out/new" "$FEED/rave-mate-linux-$N1"
SHA=$(sha256sum "$FEED/rave-mate-linux-$N1" | cut -d' ' -f1)
OLDSHA=$(sha256sum "$W/out/old" | cut -d' ' -f1)
U=http://127.0.0.1:8099/feed/rave-mate-linux-$N1
printf '{"version":"e2e-%s","build":%s,"commit":"e2e","url":"%s","sha256":"%s","linux_url":"%s","linux_sha256":"%s","released":"now","notes":"e2e"}' \
	"$N1" "$N1" "$U" "$SHA" "$U" "$SHA" >"$FEED/latest.json"
"$W/feedtool" -sign "$W/key" -dir "$FEED"
cat "$FEED/latest.json"
echo

echo "== install old build: user-owned dir, symlinked like /usr/local/bin/rave-mate =="
install -m 0755 "$W/out/old" "$INST/rave-mate"
ln -sf "$INST/rave-mate" "$BIN/rave-mate"
"$BIN/rave-mate" version

"$W/feedtool" -serve 127.0.0.1:8099 -dir "$FEED" >"$W/feed.log" 2>&1 &
FPID=$!
echo "== start: $BIN/rave-mate --service =="
"$BIN/rave-mate" --service >"$W/app.log" 2>&1 &
PID=$!
trap 'kill $PID $FPID 2>/dev/null || true' EXIT
echo "pid $PID"

deadline=$((SECONDS + 420))
last=""
swapped=0
while [ $SECONDS -lt $deadline ]; do
	sleep 5
	kill -0 "$PID" 2>/dev/null || fail "process $PID died"
	st=$("$BIN/rave-mate" ctl status 2>/dev/null || true)
	u=$(printf '%s' "$st" | grep -o 'build=.*' || true)
	if [ -n "$u" ] && [ "$u" != "$last" ]; then
		echo "[t+${SECONDS}s] ctl status: $u"
		last=$u
	fi
	case "$st" in *"build=$N1 "*) swapped=1 && break ;; esac
done
[ "$swapped" = 1 ] || fail "no switch to build $N1 within 7 min"

echo "== verify =="
[ "$(readlink "/proc/$PID/exe")" = "$INST/rave-mate" ] || fail "/proc/$PID/exe = $(readlink "/proc/$PID/exe")"
echo "same PID $PID now runs: $(readlink "/proc/$PID/exe")  argv: $(tr '\0' ' ' <"/proc/$PID/cmdline")"
"$BIN/rave-mate" version
[ "$(sha256sum "$INST/rave-mate" | cut -d' ' -f1)" = "$SHA" ] || fail "installed sha != manifest sha"
[ "$(sha256sum "$INST/rave-mate.prev" | cut -d' ' -f1)" = "$OLDSHA" ] || fail ".prev is not the old build"
echo "exe sha == manifest linux_sha256; rave-mate.prev == old build"
ls -la "$INST"
# exactly one ctl listener (127.0.0.1:47620 = 0xBA04, state 0A LISTEN): the old one did not leak
nl=$(awk '$2 ~ /:BA04$/ && $4 == "0A"' /proc/net/tcp | wc -l)
[ "$nl" = 1 ] || fail "ctl listeners on :47620 = $nl"
echo "ctl listeners on :47620: $nl"
# no orphaned feature children of the old image (they'd be reparented away from PID)
orph=0
for s in /proc/[0-9]*/stat; do
	read -r p c _ pp _ <"$s" 2>/dev/null || continue
	case "$c" in "(rmate-"*) [ "$pp" = "$PID" ] || { echo "orphan: pid $p $c ppid $pp"; orph=1; } ;; esac
done
[ "$orph" = 0 ] || fail "orphaned feature children"
echo "children of $PID: $(for s in /proc/[0-9]*/stat; do read -r p c _ pp _ <"$s" 2>/dev/null && [ "$pp" = "$PID" ] && printf '%s ' "$c"; done)"

echo "== health confirm (35 s) =="
sleep 35
kill -0 "$PID" 2>/dev/null || fail "new build died after the swap"
cat "$INST/.rave-mate.update.json"
echo
grep -q '"pending"' "$INST/.rave-mate.update.json" && fail "attempt still pending after the health window"
echo "== update log lines =="
grep -hE '\[update\]|==== rave-mate start|update guard|re-exec' "$W/app.log" "$(applog)" | tail -25 || true
echo "== feed requests =="
cat "$W/feed.log"
echo "E2E PASS: build $N -> $N1, same PID $PID, .prev kept, attempt confirmed"
