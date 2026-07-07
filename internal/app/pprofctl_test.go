package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClampPprofSeconds(t *testing.T) {
	cases := []struct{ in, max, want int }{
		{0, pprofMaxSeconds, pprofDefaultSeconds},
		{-3, pprofMaxSeconds, pprofDefaultSeconds},
		{1, pprofMaxSeconds, 1},
		{60, pprofMaxSeconds, 60},
		{120, pprofMaxSeconds, 60},
		{60, pprofRemoteMaxSeconds, 45},
		{10, pprofRemoteMaxSeconds, 10},
	}
	for _, c := range cases {
		if got := clampPprofSeconds(c.in, c.max); got != c.want {
			t.Errorf("clamp(%d,%d)=%d want %d", c.in, c.max, got, c.want)
		}
	}
}

func TestParseRemotePprof(t *testing.T) {
	cases := []struct {
		in      string
		path    string
		seconds int
		nodeID  string
		wantErr bool
	}{
		{"", "", 0, "", true},
		{"C:/out.pprof", "C:/out.pprof", 0, "", false},
		{"C:/out.pprof 30", "C:/out.pprof", 30, "", false},
		{"C:/out.pprof abc123", "C:/out.pprof", 0, "abc123", false},
		{"C:/out.pprof 30 abc123", "C:/out.pprof", 30, "abc123", false},
		{"C:/out.pprof abc123 30", "C:/out.pprof", 30, "abc123", false}, // order-independent
	}
	for _, c := range cases {
		path, secs, nodeID, err := parseRemotePprof(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parse(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if path != c.path || secs != c.seconds || nodeID != c.nodeID {
			t.Errorf("parse(%q)=(%q,%d,%q) want (%q,%d,%q)", c.in, path, secs, nodeID, c.path, c.seconds, c.nodeID)
		}
	}
}

// Heap capture must yield non-empty pprof bytes (gzip magic).
func TestCapturePprofHeap(t *testing.T) {
	b, err := capturePprofHeap()
	if err != nil {
		t.Fatalf("heap: %v", err)
	}
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		t.Fatalf("not a gzipped pprof (len=%d)", len(b))
	}
}

// CPU capture (tiny window) must yield non-empty pprof bytes and stop cleanly.
func TestCapturePprofCPU(t *testing.T) {
	b, err := capturePprofCPU(context.Background(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("cpu: %v", err)
	}
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		t.Fatalf("not a gzipped pprof (len=%d)", len(b))
	}
}

func TestGoroutineDump(t *testing.T) {
	d := goroutineDump()
	if !strings.Contains(d, "goroutine profile:") {
		t.Fatalf("dump=%q", d)
	}
}
