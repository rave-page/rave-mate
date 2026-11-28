package app

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
)

func TestCutCapFlags(t *testing.T) {
	for _, tc := range []struct {
		in         string
		rest       string
		kbps, fps  capArg
		wantErrSub string
	}{
		{in: "VRC Sender", rest: "VRC Sender"},
		{in: "VRC --kbps 8000", rest: "VRC", kbps: capArg{set: true, v: 8000}},
		{in: "VRC --fps=30 --kbps none", rest: "VRC", kbps: capArg{set: true}, fps: capArg{set: true, v: 30}},
		{in: "my cam --kbps=0", rest: "my cam", kbps: capArg{set: true}},
		{in: "VRC --fps", wantErrSub: "needs a value"},
		{in: "VRC --kbps fast", wantErrSub: "positive number or none"},
		{in: "VRC --kbps -5", wantErrSub: "positive number or none"},
	} {
		rest, k, f, err := cutCapFlags(tc.in)
		if tc.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("%q: err %v, want %q", tc.in, err, tc.wantErrSub)
			}
			continue
		}
		if err != nil || rest != tc.rest || k != tc.kbps || f != tc.fps {
			t.Errorf("%q: rest=%q k=%+v f=%+v err=%v", tc.in, rest, k, f, err)
		}
	}
}

func TestResolveRemembered(t *testing.T) {
	list := []config.RememberedReceive{
		{Peer: "aaaa1111", SourceID: "spout:VRCSender1", Name: "VRCSender1"},
		{Peer: "aaaa1111", SourceID: "webcam:cam", Name: "Logitech C920"},
		{Peer: "bbbb2222", SourceID: "spout:VRCSender1", Name: "VRCSender1"},
	}
	r, err := resolveRemembered(list, "aaaa", "c920")
	if err != nil || r.SourceID != "webcam:cam" {
		t.Fatalf("substring: %+v %v", r, err)
	}
	r, err = resolveRemembered(list, "bbbb2222", "vrcsender1")
	if err != nil || r.Peer != "bbbb2222" {
		t.Fatalf("name: %+v %v", r, err)
	}
	if _, err := resolveRemembered(list, "cccc", "x"); err == nil {
		t.Fatal("unknown peer resolved")
	}
	if _, err := resolveRemembered(list, "aaaa", "nope"); err == nil {
		t.Fatal("unknown source resolved")
	}
}

// RouteCap end to end over the remembering wrapper: persists to config, pushes live to the plane,
// keeps the unspecified axis, refuses an unreceived source and out-of-range values.
func TestRouteCapCtl(t *testing.T) {
	f := &capRecv{}
	rr := newRR(&f.fakeRecv, config.RememberedReceive{Peer: "aaaa1111", SourceID: "spout:VRC", Name: "VRC"})
	rr.ReceiveControl = f // the capping wrapper, not its embedded fake
	c := &appControl{cfg: rr.cfg, mediaRoutes: rr}
	if out := c.RouteCap("aaaa", "VRC --kbps 8000 --fps 30"); !strings.Contains(out, "maxKbps=8000 maxFps=30") {
		t.Fatalf("set: %s", out)
	}
	if k, fp := rr.cfg.Features.MediaLink.SourceCaps("aaaa1111", "spout:VRC"); k != 8000 || fp != 30 {
		t.Fatalf("not persisted: %d/%d", k, fp)
	}
	if f.k != 8000 || f.f != 30 {
		t.Fatalf("not pushed live: %d/%d", f.k, f.f)
	}
	if out := c.RouteCap("aaaa", "VRC --fps none"); !strings.Contains(out, "maxKbps=8000 maxFps=none") {
		t.Fatalf("fps clear must keep kbps: %s", out)
	}
	if out := c.RouteCap("aaaa", "VRC"); out != "caps aaaa1111 spout:VRC: maxKbps=8000 maxFps=none" {
		t.Fatalf("show: %q", out)
	}
	if out := c.RouteCap("aaaa", "VRC --kbps 50"); !strings.HasPrefix(out, "error: caps out of range") {
		t.Fatalf("range: %s", out)
	}
	if out := c.RouteCap("zzzz", "VRC --kbps 8000"); !strings.HasPrefix(out, "error:") {
		t.Fatalf("unknown peer: %s", out)
	}
}

// capRecv is a fakeRecv that also takes live per-source caps.
type capRecv struct {
	fakeRecv
	k, f int
}

func (c *capRecv) SetSourceCaps(_, _ string, k, f int) error { c.k, c.f = k, f; return nil }
