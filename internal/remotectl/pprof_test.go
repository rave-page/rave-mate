package remotectl

import (
	"bytes"
	"context"
	"testing"
)

// fakeProfiler records the requested seconds and returns fixed profile bytes.
type fakeProfiler struct {
	gotSeconds int
}

func (f *fakeProfiler) CPUProfile(_ context.Context, seconds int) ([]byte, error) {
	f.gotSeconds = seconds
	return []byte{0x1f, 0x8b, 1, 2, 3}, nil // gzip-magic-shaped stand-in
}
func (f *fakeProfiler) HeapProfile() ([]byte, error) { return []byte("heap-profile"), nil }
func (f *fakeProfiler) Goroutines() string           { return "goroutine profile: total 7\n7 @ 0x1" }

// TestPprofCPURPC round-trips app.pprofcpu: bytes survive base64, seconds reach the profiler.
func TestPprofCPURPC(t *testing.T) {
	server, client := loopback()
	fp := &fakeProfiler{}
	RegisterPprof(server, fp)

	b, err := NewClient(client, "server").PprofCPU(ctx(t), 25)
	if err != nil {
		t.Fatalf("pprof cpu: %v", err)
	}
	if !bytes.Equal(b, []byte{0x1f, 0x8b, 1, 2, 3}) {
		t.Fatalf("bytes=%v", b)
	}
	if fp.gotSeconds != 25 {
		t.Fatalf("seconds=%d", fp.gotSeconds)
	}
}

// TestPprofHeapRPC round-trips app.pprofheap.
func TestPprofHeapRPC(t *testing.T) {
	server, client := loopback()
	RegisterPprof(server, &fakeProfiler{})

	b, err := NewClient(client, "server").PprofHeap(ctx(t))
	if err != nil {
		t.Fatalf("pprof heap: %v", err)
	}
	if string(b) != "heap-profile" {
		t.Fatalf("bytes=%q", b)
	}
}

// TestGoroutinesRPC round-trips app.goroutines (multi-line text).
func TestGoroutinesRPC(t *testing.T) {
	server, client := loopback()
	RegisterPprof(server, &fakeProfiler{})

	text, err := NewClient(client, "server").Goroutines(ctx(t))
	if err != nil {
		t.Fatalf("goroutines: %v", err)
	}
	if text != "goroutine profile: total 7\n7 @ 0x1" {
		t.Fatalf("text=%q", text)
	}
}

// Nil endpoint/profiler registration must be a no-op (mirrors the other Register* guards).
func TestRegisterPprofNil(t *testing.T) {
	RegisterPprof(nil, &fakeProfiler{})
	server, client := loopback()
	RegisterPprof(server, nil)
	rc := NewClient(client, "server")
	if _, err := rc.PprofCPU(ctx(t), 1); err == nil {
		t.Fatal("unregistered app.pprofcpu must error")
	}
	if _, err := rc.PprofHeap(ctx(t)); err == nil {
		t.Fatal("unregistered app.pprofheap must error")
	}
	if _, err := rc.Goroutines(ctx(t)); err == nil {
		t.Fatal("unregistered app.goroutines must error")
	}
}
