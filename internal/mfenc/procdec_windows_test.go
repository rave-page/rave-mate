//go:build windows && cgo

package mfenc

// Increment-2 gates for the native decode path (zigmedia). The ring + oracle tests are
// hardware-free; the end-to-end ones spawn the real child and skip without an MF pipeline.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// decRingReader mirrors native/zigenc/src/main.zig's ringTake EXACTLY, so this file is the
// protocol test for the inbound ring: if the two ever diverge, the round trip below breaks.
type decRingReader struct{ d *ProcDecSession }

func (r decRingReader) take() ([]byte, int64, bool, bool) {
	d := r.d
	for hops := 0; hops < 2; hops++ {
		rd := *d.shm.u64(offInRead)
		w := *d.shm.u64(offInWrite)
		if rd >= w {
			return nil, 0, false, false
		}
		tail := d.ringSize - (rd % d.ringSize)
		if tail < 16 {
			*d.shm.u64(offInRead) = rd + tail
			continue
		}
		pos := uintptr(rd % d.ringSize)
		ln := *(*uint32)(unsafe.Add(d.shm.base, d.ringOff+pos))
		if ln == wrapMarker {
			*d.shm.u64(offInRead) = rd + tail
			continue
		}
		if ln == 0 || uint64(ln)+16 > tail {
			return nil, 0, false, false
		}
		flags := *(*uint32)(unsafe.Add(d.shm.base, d.ringOff+pos+4))
		pts := *(*int64)(unsafe.Add(d.shm.base, d.ringOff+pos+8))
		au := make([]byte, ln)
		copy(au, unsafe.Slice((*byte)(unsafe.Add(d.shm.base, d.ringOff+pos+16)), ln))
		*d.shm.u64(offInRead) = rd + 16 + ((uint64(ln) + 7) &^ 7)
		return au, pts, flags&1 != 0, true
	}
	return nil, 0, false, false
}

// newTestDecSession builds a session over a real SHM mapping with NO child attached: Decode only
// touches the mapping + the event, so the ring contract is fully testable without hardware.
func newTestDecSession(t *testing.T, ringBytes int) *ProcDecSession {
	t.Helper()
	shm, err := createShm(`Local\rvmfdec-test-`+t.Name(), shmHdrSize+ringBytes)
	if err != nil {
		t.Fatalf("createShm: %v", err)
	}
	t.Cleanup(shm.close)
	return &ProcDecSession{
		child: &procChild{sessions: map[uint32]*ProcSession{}, decs: map[uint32]*ProcDecSession{}},
		sid:   1, shm: shm, inW: 1920, inH: 1080, outW: 1920, outH: 1080, fps: 60,
		ringSize: uint64(ringBytes), ringOff: shmHdrSize, done: make(chan struct{}),
	}
}

// TestDecRingRoundTrip: every appended AU comes back byte-exact with its pts + keyframe flag,
// across a wrap. This is the parent/child record-layout contract.
func TestDecRingRoundTrip(t *testing.T) {
	d := newTestDecSession(t, 64<<10)
	rd := decRingReader{d}
	// Enough AUs to wrap the 64 KiB ring several times, draining as we go.
	for i := 0; i < 400; i++ {
		au := make([]byte, 300+i%700)
		for j := range au {
			au[j] = byte(i)
		}
		pts := int64(1_000_000 * (i + 1))
		key := i%30 == 0
		if err := d.Decode(au, pts, key); err != nil {
			t.Fatalf("Decode %d: %v", i, err)
		}
		got, gotPTS, gotKey, ok := rd.take()
		if !ok {
			t.Fatalf("AU %d: ring empty right after append", i)
		}
		if len(got) != len(au) || got[0] != byte(i) || got[len(got)-1] != byte(i) {
			t.Fatalf("AU %d: payload mismatch (len %d vs %d)", i, len(got), len(au))
		}
		if gotPTS != pts || gotKey != key {
			t.Fatalf("AU %d: pts %d/%d key %v/%v", i, gotPTS, pts, gotKey, key)
		}
	}
	if got := *d.shm.u64(offInDropped); got != 0 {
		t.Fatalf("%d drops on a drained ring", got)
	}
}

// TestDecRingFullDropsNewestAndCounts is the BOUND: the ring never grows and never blocks. A
// consumer that stops reading must cause counted drops, not an unbounded queue.
func TestDecRingFullDropsNewestAndCounts(t *testing.T) {
	const ring = 32 << 10
	d := newTestDecSession(t, ring)
	au := make([]byte, 4096)
	appended := 0
	for i := 0; i < 200; i++ { // nothing is ever consumed
		if err := d.Decode(au, int64(i), false); err != nil {
			t.Fatalf("Decode must never error on a full ring: %v", err)
		}
		appended++
	}
	w := *d.shm.u64(offInWrite)
	if w > uint64(ring) {
		t.Fatalf("write head advanced %d bytes into a %d-byte ring", w, ring)
	}
	drops := *d.shm.u64(offInDropped)
	if drops == 0 {
		t.Fatal("a full ring must DROP and count, not accumulate")
	}
	if int(drops) >= appended {
		t.Fatalf("dropped %d of %d: nothing was ever accepted", drops, appended)
	}
	// Draining must then make room again (the ring is reusable, not wedged).
	rd := decRingReader{d}
	for {
		if _, _, _, ok := rd.take(); !ok {
			break
		}
	}
	before := *d.shm.u64(offInDropped)
	if err := d.Decode(au, 1, true); err != nil {
		t.Fatal(err)
	}
	if *d.shm.u64(offInDropped) != before {
		t.Fatal("a drained ring still dropped: the ring is wedged")
	}
}

// TestDecRingRefusesOversizedAU: an AU bigger than the whole ring is dropped + counted, never
// written past the mapping.
func TestDecRingRefusesOversizedAU(t *testing.T) {
	const ring = 8 << 10
	d := newTestDecSession(t, ring)
	if err := d.Decode(make([]byte, ring+1), 0, true); err != nil {
		t.Fatalf("oversized AU must be dropped cleanly: %v", err)
	}
	if *d.shm.u64(offInWrite) != 0 {
		t.Fatal("oversized AU moved the write head")
	}
	if *d.shm.u64(offInDropped) != 1 {
		t.Fatal("oversized AU was not counted")
	}
}

// TestDecCheckOracle is the frozen-DESTINATION gate. The idle-route case is the trap: a route with
// no traffic publishes nothing and is perfectly healthy.
func TestDecCheckOracle(t *testing.T) {
	const stale = int64(3 * time.Second)
	cases := []struct {
		name string
		p    decProbe
		want spoutVerdict
	}{
		{"healthy: same handle, publishing", decProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			decFrames: 120, prevFrames: 60, appended: 130, prevAppended: 65,
			lastPubNs: 8_900_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
		{"destination re-created: handle changed", decProbe{
			curHandle: 0xA, newHandle: 0xB, resolved: true,
			decFrames: 120, prevFrames: 60, appended: 130, prevAppended: 65,
			lastPubNs: 8_900_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutRecycleNow},
		{"FROZEN: AUs arriving, nothing published, publish clock stopped", decProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			decFrames: 120, prevFrames: 120, appended: 200, prevAppended: 130,
			lastPubNs: 1_000_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutRecycleNow},
		{"IDLE route: no AUs, nothing published - healthy, must not churn reopens", decProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			decFrames: 120, prevFrames: 120, appended: 130, prevAppended: 130,
			lastPubNs: 1_000_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
		{"slow but alive: publish clock still fresh", decProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true,
			decFrames: 120, prevFrames: 120, appended: 200, prevAppended: 130,
			lastPubNs: 8_500_000_000, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
		{"destination unresolvable: wait", decProbe{curHandle: 0xA, resolved: false, staleNs: stale}, spoutUnresolvable},
		{"nothing published yet (lastPubNs 0): starting, not frozen", decProbe{
			curHandle: 0xA, newHandle: 0xA, resolved: true, appended: 10, prevAppended: 0,
			lastPubNs: 0, nowNs: 9_000_000_000, staleNs: stale,
		}, spoutHealthy},
	}
	for _, c := range cases {
		got, why := decCheck(c.p)
		if got != c.want {
			t.Errorf("%s: verdict %d, want %d (%s)", c.name, got, c.want, why)
		}
		if got == spoutRecycleNow && why == "" {
			t.Errorf("%s: a recycle must always name a reason", c.name)
		}
	}
}

// TestDecWatchdogRecyclesOnHandleChange wires the oracle to the ACTION without a live child: a
// destination sender re-created under us must recycle, or the route publishes into a dead texture
// forever while looking healthy.
func TestDecWatchdogRecyclesOnHandleChange(t *testing.T) {
	d := newTestDecSession(t, 4<<20)
	handle := uint64(0xAAAA)
	fired := make(chan string, 4)
	d.dstName = "rave-mate link OBS"
	d.dstHandle = 0xAAAA
	d.resolve = func() (uint64, uint32, int, int, bool) { return handle, 87, 1920, 1080, true }
	d.recycle = func(why string) { fired <- why }
	d.watchDone = make(chan struct{})
	go d.watchDest()
	defer func() { close(d.done); <-d.watchDone }()

	select {
	case why := <-fired:
		t.Fatalf("recycled a healthy session: %s", why)
	case <-time.After(decWatchEvery + 500*time.Millisecond):
	}
	handle = 0xBBBB
	select {
	case why := <-fired:
		if !strings.Contains(why, "handle changed") {
			t.Fatalf("recycle reason = %q, want the changed-handle verdict", why)
		}
	case <-time.After(3 * decWatchEvery):
		t.Fatal("a re-created destination sender did not recycle the session")
	}
}

// TestDecodePinning: a destination pinned after repeated failures stays pinned, per sender.
func TestDecodePinning(t *testing.T) {
	const name = "dec-pin-test"
	if DecodePinnedToFrames(name) {
		t.Fatal("pinned before anything failed")
	}
	pinDecodeFrames(name)
	if !DecodePinnedToFrames(name) {
		t.Fatal("pinDecodeFrames did not stick")
	}
	if DecodePinnedToFrames("other") {
		t.Fatal("pinning must be per sender")
	}
}

// TestDecOpenCmdCarriesTheDecFields / TestEncOpenCmdCarriesNoDecFields: the wire contract in both
// directions. An encode open must be byte-identical to the pre-inc-2 one.
func TestDecOpenCmdCarriesTheDecFields(t *testing.T) {
	cmd := openCmd{Op: "open", SID: 3, Shm: `Local\x`, InW: 1920, InH: 1080, OutW: 1280, OutH: 720,
		FpsN: 60, FpsD: 1, Dir: "dec", Codec: "hevc", DSh: 0xBEEF, DFmt: 87, DName: "sink",
		InRingKB: ringKBMin}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dir", "codec", "dsh", "dfmt", "dname", "in_ring_kb"} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("dec open is missing %q: %s", k, b)
		}
	}
	// The zero-copy CAPTURE fields must be absent (kbps/gop are not omitempty and ride both
	// directions harmlessly - the child ignores them on a dec session).
	for _, k := range []string{"src", "sh", "sfmt", "sname", "ring_kb", "pts0"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("dec open carries the capture field %q: %s", k, b)
		}
	}
}

func TestEncOpenCmdCarriesNoDecFields(t *testing.T) {
	cmd := openCmd{Op: "open", SID: 1, Shm: `Local\x`, InW: 1920, InH: 1080, OutW: 1920, OutH: 1080,
		FpsN: 60, FpsD: 1, Kbps: 8000, Gop: 120}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dir", "codec", "dsh", "dfmt", "dname", "in_ring_kb"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("encode open carries the dec field %q: %s", k, b)
		}
	}
}

// TestDecShmHasNoFrameSlotAndNoOutboundRing: a dec mapping is header + inbound ring ONLY. At 4K
// that is 4 MiB against the frame path's 33 MB per frame down a pipe.
func TestDecShmHasNoFrameSlotAndNoOutboundRing(t *testing.T) {
	ringKB := ringKBFor(decTestKbps)
	total := shmHdrSize + ringKB*1024
	if total != shmHdrSize+4<<20 {
		t.Fatalf("dec shm = %d B, want header + 4 MiB", total)
	}
	if ringKB*1024 >= 3840*2160*4 {
		t.Fatal("the dec mapping must not scale with geometry")
	}
	// The counter block must fit inside the 256 B header with room to spare.
	if offDecMtxTimeo+8 > shmHdrSize {
		t.Fatalf("dec counters end at %d, past the %d B header", offDecMtxTimeo+8, shmHdrSize)
	}
	// And must not collide with the encode-side block (0..111).
	if offInWrite < 112 {
		t.Fatalf("dec block starts at %d, inside the encode counters", offInWrite)
	}
}

const decTestKbps = 50_000

// TestDecOpenRefusesBogusDestination drives the REAL child: a destination handle no sender owns
// must come back as ErrDecodeRefused (the caller falls back to ffmpeg) - never a crash, never a
// hang, never a session that pretends to publish.
func TestDecOpenRefusesBogusDestination(t *testing.T) {
	if !Available() {
		t.Skip("no D3D11 / MF pipeline")
	}
	requireEncExe(t)
	_, err := OpenProcDecSession(ProcDecOpts{
		LUID: 0, InW: 1280, InH: 720, OutW: 1280, OutH: 720, FPS: 60, KbpsHint: 8000,
		Dest: &DecodeDest{Name: "no such sender", Resolve: func() (uint64, uint32, int, int, bool) {
			return 0xDEAD0000, 87, 1280, 720, true
		}},
	})
	if err == nil {
		t.Fatal("a bogus destination handle must not open a decode session")
	}
	if !errors.Is(err, ErrDecodeRefused) {
		t.Fatalf("err = %v, want ErrDecodeRefused so the caller falls back to the ffmpeg decoder", err)
	}
	t.Logf("clean decode refusal: %v", err)
}

// TestDecOpenNeedsADestination: no destination texture at all = a clean refusal, never a nil-deref.
func TestDecOpenNeedsADestination(t *testing.T) {
	if _, err := OpenProcDecSession(ProcDecOpts{InW: 640, InH: 480, OutW: 640, OutH: 480}); !errors.Is(err, ErrDecodeRefused) {
		t.Fatalf("err = %v, want ErrDecodeRefused", err)
	}
}

// TestChildAdvertisesProtoV3: the version gate is the only thing between an older child and a
// mapping it would size from in_w*in_h*4, so the shipped child must say >= 3.
func TestChildAdvertisesProtoV3(t *testing.T) {
	requireEncExe(t)
	c, err := getChild(0x7e5b) // own child object; an unknown LUID degrades inside the child
	if err != nil {
		t.Skipf("no child: %v", err)
	}
	if v := c.waitProtoVer(5 * time.Second); v < protoVerDecode {
		t.Fatalf("child hello.ver = %d, want >= %d (dir:\"dec\" gate)", v, protoVerDecode)
	}
}
