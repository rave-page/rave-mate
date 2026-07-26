//go:build windows && cgo

package mfenc

// Increment-1 gates for the zero-copy capture path (zigmedia). The oracle + sizing tests are
// hardware-free; the end-to-end ones spawn the real child and skip without an MF encoder.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRingKBIsGeometryIndependent: the AU ring is derived from BITRATE, so a sender resize
// costs zero SHM realloc (that is the whole point of dropping the frame slot).
func TestRingKBIsGeometryIndependent(t *testing.T) {
	cases := []struct{ kbps, want int }{
		{0, ringKBMin},
		{500, ringKBMin},
		{50_000, ringKBMin},     // 4K60 @ 50 Mbps → 0.5 s = 3.05 MiB → the 4 MiB floor
		{200_000, 200_000 / 16}, // 12.2 MiB, inside the window
		{1_000_000, ringKBMax},  // absurd budget → the 16 MiB ceiling
	}
	for _, c := range cases {
		if got := ringKBFor(c.kbps); got != c.want {
			t.Errorf("ringKBFor(%d) = %d, want %d", c.kbps, got, c.want)
		}
	}
	// Same bitrate at wildly different geometries must give the SAME ring.
	if ringKBFor(50_000) != ringKBFor(50_000) || ringKBFor(20_000) > ringKBMax {
		t.Fatal("ring sizing must not depend on geometry")
	}
}

// TestZeroCopyShmHasNoFrameSlot: a zero-copy session's mapping is header + ring, and the ring
// offset is the header size - no 33 MB frame slot exists to write into.
func TestZeroCopyShmHasNoFrameSlot(t *testing.T) {
	const w, h, kbps = 3840, 2160, 50_000
	ringKB := ringKBFor(kbps)
	zcTotal := shmHdrSize + ringKB*1024
	readbackTotal := shmHdrSize + w*h*4 + max(8<<20, w*h*4)
	if zcTotal >= readbackTotal/4 {
		t.Fatalf("zero-copy shm %d B is not materially smaller than the readback shm %d B", zcTotal, readbackTotal)
	}
	if zcTotal != shmHdrSize+4<<20 {
		t.Fatalf("4K60/50Mbps zero-copy shm = %d B, want header + 4 MiB", zcTotal)
	}
}

// TestSpoutCheckOracle is the R1 gate: the FROZEN case must be detected, not just the clean one.
func TestSpoutCheckOracle(t *testing.T) {
	const stale = int64(3 * time.Second)
	cases := []struct {
		name string
		p    spoutProbe
		want spoutVerdict
	}{
		{"healthy: same handle, capture progressing", spoutProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			capFrames: 120, prevFrames: 60, lastCapNs: 1_000_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
		{"handle changed: sender restarted (OpenSharedResource would still succeed on the dead one)", spoutProbe{
			curHandle: 0xA, newHandle: 0xB, resolved: true,
			capFrames: 120, prevFrames: 60, lastCapNs: 8_900_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutRecycleNow},
		{"FROZEN: handle unchanged, capture clock stopped, no new frames", spoutProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			capFrames: 120, prevFrames: 120, lastCapNs: 1_000_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutRecycleNow},
		{"slow but alive: no new frames yet, clock still fresh", spoutProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			capFrames: 120, prevFrames: 120, lastCapNs: 8_500_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
		{"sender vanished: wait, do not churn reopens", spoutProbe{
			curHandle: 0xA, resolved: false, staleNs: stale,
		}, spoutUnresolvable},
		{"no capture yet (lastCapNs 0): not frozen, just starting", spoutProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true, lastCapNs: 0, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
	}
	for _, c := range cases {
		got, why := spoutCheck(c.p)
		if got != c.want {
			t.Errorf("%s: verdict %d, want %d (%s)", c.name, got, c.want, why)
		}
		if got == spoutRecycleNow && why == "" {
			t.Errorf("%s: a recycle must always name a reason", c.name)
		}
	}
}

// TestSpoutWatchdogRecyclesOnHandleChange wires the oracle to the ACTION: a session whose sender
// restarted (new handle) must have its recycle path invoked. The recycle func is the seam, so the
// wiring is asserted without a live child or a real Spout sender.
func TestSpoutWatchdogRecyclesOnHandleChange(t *testing.T) {
	shm, err := createShm(`Local\rvmfenc-zctest-1`, shmHdrSize+4<<20)
	if err != nil {
		t.Fatalf("createShm: %v", err)
	}
	defer shm.close()
	handle := uint64(0xAAAA)
	fired := make(chan string, 4)
	s := &ProcSession{
		shm: shm, zeroCopy: true, zcName: "OBS",
		done:     make(chan struct{}),
		resolve:  func() (uint64, uint32, int, int, bool) { return handle, 87, 1920, 1080, true },
		recycle:  func(why string) { fired <- why },
		zcHandle: 0xAAAA,
	}
	s.watchDone = make(chan struct{})
	go s.watchSpout()
	defer func() { close(s.done); <-s.watchDone }()

	// Healthy first: the handle matches and nothing is stale yet.
	select {
	case why := <-fired:
		t.Fatalf("recycled a healthy session: %s", why)
	case <-time.After(spoutWatchEvery + 500*time.Millisecond):
	}
	// Sender restarts: same name, NEW texture. The route would otherwise ship a frozen picture.
	handle = 0xBBBB
	select {
	case why := <-fired:
		if !strings.Contains(why, "handle changed") {
			t.Fatalf("recycle reason = %q, want the changed-handle verdict", why)
		}
	case <-time.After(3 * spoutWatchEvery):
		t.Fatal("a changed share handle did not recycle the session (R1: silently frozen picture)")
	}
}

// TestZeroCopySessionRefusesEncode: there is no frame slot, so a host frame must be refused, not
// written past the header into the AU ring.
func TestZeroCopySessionRefusesEncode(t *testing.T) {
	s := &ProcSession{zeroCopy: true}
	if err := s.Encode(make([]byte, 64), 0); err == nil {
		t.Fatal("Encode on a zero-copy session must error")
	}
}

// TestZeroCopyPinning: a sender pinned after repeated failures stays pinned (the caller then
// skips the zero-copy request entirely).
func TestZeroCopyPinning(t *testing.T) {
	const name = "pin-test-sender"
	if ZeroCopyPinnedToReadback(name) {
		t.Fatal("sender pinned before anything failed")
	}
	pinReadback(name)
	if !ZeroCopyPinnedToReadback(name) {
		t.Fatal("pinReadback did not stick")
	}
	if ZeroCopyPinnedToReadback("some-other-sender") {
		t.Fatal("pinning must be per sender")
	}
}

// TestZeroCopyOpenRefusesBogusHandle drives the REAL child: a handle that no sender owns must
// come back as ErrZeroCopyRefused (the caller reopens on the readback path) - never a crash,
// never a hang, and never a session that pretends to work.
func TestZeroCopyOpenRefusesBogusHandle(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	_, err := OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: 1920, InH: 1080, OutW: 1920, OutH: 1080, FPS: 60, Kbps: 8000, Gop: 120,
		Spout: &SpoutSource{Name: "no such sender", Resolve: func() (uint64, uint32, int, int, bool) {
			return 0xDEAD0000, 87, 1920, 1080, true
		}},
	})
	if err == nil {
		t.Fatal("a bogus share handle must not open a zero-copy session")
	}
	if !errors.Is(err, ErrZeroCopyRefused) {
		t.Fatalf("err = %v, want ErrZeroCopyRefused so the caller downgrades to the readback path", err)
	}
	t.Logf("clean zero-copy refusal: %v", err)
}

// TestChildAdvertisesProtoV2: the version gate is the only thing standing between a v1 child and
// a mapping it would size from in_w*in_h*4. The shipped child must therefore say 2.
func TestChildAdvertisesProtoV2(t *testing.T) {
	requireEncExe(t)
	c, err := getChild(0x7e5a) // own child object; an unknown LUID degrades inside the child
	if err != nil {
		t.Skipf("no child: %v", err)
	}
	if v := c.waitProtoVer(5 * time.Second); v < protoVerZeroC {
		t.Fatalf("child hello.ver = %d, want >= %d", v, protoVerZeroC)
	}
}

// TestFlagOffKeepsTheReadbackPath: with no Spout source the open command carries NO src field at
// all, i.e. the child sees byte-identical v1 arguments.
func TestFlagOffKeepsTheReadbackPath(t *testing.T) {
	s := &ProcSession{
		sid: 7, shm: &shmRegion{name: `Local\x`}, inW: 1920, inH: 1080, outW: 1920, outH: 1080,
		fps: 60, kbps: 8000, gop: 120,
	}
	fpsN, fpsD := fpsRational(s.fps)
	cmd := openCmd{Op: "open", SID: s.sid, Shm: s.shm.name, InW: s.inW, InH: s.inH,
		OutW: s.outW, OutH: s.outH, FpsN: fpsN, FpsD: fpsD, Kbps: s.kbps, Gop: s.gop}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"src", "sh", "sfmt", "sname", "cap_n", "cap_d", "ring_kb", "pts0"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("readback open carries the zero-copy field %q: %s", k, b)
		}
	}
}
