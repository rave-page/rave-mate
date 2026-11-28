package webui

import (
	"context"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/testcard"
	"rave.page/mate/internal/ui"
)

// capRoutes is a ReceiveControl + SourceCapper fake: one active receive, records cap pushes.
type capRoutes struct {
	mu   sync.Mutex
	set  [][2]int
	recv []mediaroute.Receive
}

func (c *capRoutes) Start(context.Context)                         {}
func (c *capRoutes) RemoteVideoSources() []mediaroute.RemoteSource { return nil }
func (c *capRoutes) Receives() []mediaroute.Receive                { return c.recv }
func (c *capRoutes) StartReceive(string, string) (string, error)   { return "", nil }
func (c *capRoutes) StopReceive(string)                            {}
func (c *capRoutes) FrameShot(string, int, int, int, [4]int) (mediaroute.FrameShot, error) {
	return mediaroute.FrameShot{}, nil
}
func (c *capRoutes) Testcard(string, int, int, int) (testcard.Report, error) {
	return testcard.Report{}, nil
}
func (c *capRoutes) SetSourceCaps(_, _ string, k, f int) error {
	c.mu.Lock()
	c.set = append(c.set, [2]int{k, f})
	c.mu.Unlock()
	return nil
}

// An active receive renders both cap selects at the stored values (a non-preset ctl value is kept as
// an option), and picking one axis pushes it with the other axis unchanged.
func TestPeersRecvCapsStateAndAction(t *testing.T) {
	cfg := &config.Config{}
	cfg.Features.MediaLink.RememberedReceives = []config.RememberedReceive{
		{Peer: "node1", SourceID: "spout:cam", Name: "cam", MaxKbps: 12500, MaxFPS: 30}}
	rt := &capRoutes{recv: []mediaroute.Receive{{Session: "s1", Peer: "node1", SourceID: "spout:cam", Name: "cam"}}}
	u := &UI{svc: ui.Services{Cfg: cfg, MediaRoutes: rt}}
	st := u.peerRecvState(func(s string) string { return s })
	if len(st.Rows) != 1 || !st.Rows[0].Caps || st.Note == "" {
		t.Fatalf("active receive must carry caps + note: %+v", st)
	}
	r := st.Rows[0]
	if r.Kbps.CurLabel == "" || r.FPS.CurLabel != "30" {
		t.Fatalf("current labels: kbps %q fps %q", r.Kbps.CurLabel, r.FPS.CurLabel)
	}
	opts := capKbpsOptions(12500)
	if opts[0][0] != "0" || opts[3][0] != "12000" || opts[4][0] != "12500" || opts[5][0] != "20000" {
		t.Fatalf("non-preset 12500 not kept in order: %v", opts)
	}
	u.mediaSetCap("node1/spout:cam", "8000", true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		rt.mu.Lock()
		n := len(rt.set)
		rt.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cap pick never reached the media plane")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.set[0] != [2]int{8000, 30} {
		t.Fatalf("pushed %v, want 8000 kbps with fps 30 kept", rt.set[0])
	}
}
