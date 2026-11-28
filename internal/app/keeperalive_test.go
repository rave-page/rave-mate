package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

type fakeAlive struct {
	calls int
	err   error
}

func (f *fakeAlive) KeepAlive() error { f.calls++; return f.err }

// Default config keeps the keeper alive from boot; a missing sidecar is logged once (routes then use
// the in-process sender); an explicit opt-out never touches the keeper.
func TestKeeperKeepAliveDefaultOnAndOptOut(t *testing.T) {
	t.Setenv("RAVE_MATE_SENDER_KEEPER", "")
	run := func(c config.MediaLinkFeature, k *fakeAlive) []logbus.Entry {
		log := logbus.New(16)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // one iteration, then exit
		runKeeperKeepAlive(ctx, log, k, func() config.MediaLinkFeature { return c })
		return log.Snapshot()
	}
	var on config.MediaLinkFeature
	on.SetZeroCopyDecode(true)
	k := &fakeAlive{}
	if es := run(on, k); k.calls != 1 || !hasMsg(es, "sender keeper up") {
		t.Fatalf("default: keep-alive calls=%d logs=%v", k.calls, es)
	}
	k = &fakeAlive{err: errors.New("keeper sidecar missing")}
	if es := run(on, k); k.calls != 1 || !hasMsg(es, "not available") {
		t.Fatalf("missing sidecar must be logged: %v", es)
	}
	off := false
	on.SenderKeeper = &off
	k = &fakeAlive{}
	if run(on, k); k.calls != 0 {
		t.Fatalf("opt-out must never touch the keeper, calls=%d", k.calls)
	}
}

func hasMsg(es []logbus.Entry, sub string) bool {
	for _, e := range es {
		if strings.Contains(e.Msg, sub) {
			return true
		}
	}
	return false
}
