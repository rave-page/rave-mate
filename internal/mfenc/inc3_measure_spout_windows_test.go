//go:build windows && cgo && spout

package mfenc

// inc3_measure_spout_windows_test.go - the MEASUREMENTS increment 3 is gated on.
//
// ZIGMEDIA_DESIGN.md §11 is explicit: increment 3 is "measurement-driven (do NOT pre-build)".
// Two of its three items only exist if a number says so:
//
//	M1  can a sender's frame counter be read metadata-only (no GL, no readback)?   → item 2 feasible?
//	M2  what does a SECOND/THIRD session on the SAME sender cost?                  → item 1 needed?
//	M3  what does a duplicate frame on a static sender actually cost in bitrate?   → item 2 worth it?
//
// Run:
//
//	go test -tags spout ./internal/mfenc -run TestInc3Measure -v
//
// These are measurements, not gates: they assert only that the instrument works, and they PRINT the
// numbers that the increment-3 build/skip decisions cite.

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

const m3Sender = "rave-mate inc3 measure"

// m3Name derives a PER-ARM sender name. One name has one owner and Spout keeps the registry entry
// alive briefly after a publisher dies, so reusing one name made the second arm connect to (or wait
// out) the dead sender instead of the fresh one.
func m3Name(mode string) string { return m3Sender + " " + mode }

// Geometry is env-overridable (RAVE_M3_GEOM=4k) so the fan-out measurement can run at the geometry
// the whole epic exists for: contention scales with how long each Blt holds the sender's mutex.
var m3W, m3H = m3Geom()

func m3Geom() (int, int) {
	if os.Getenv("RAVE_M3_GEOM") == "4k" {
		return 3840, 2160
	}
	return 1280, 720
}

// TestInc3MeasurePublisher is the child role: publish either a STATIC or a MOVING pattern.
func TestInc3MeasurePublisher(t *testing.T) {
	mode := os.Getenv("RAVE_M3_ROLE")
	if mode != "static" && mode != "moving" {
		t.Skip("child role only (set by the measurement tests)")
	}
	fs, err := videoshare.NewFrameSender(logbus.New(64), m3Name(mode))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[m3-send] no sender:", err)
		return
	}
	defer fs.Close()
	img := image.NewNRGBA(image.Rect(0, 0, m3W, m3H))
	fill := func(v byte) {
		for i := range img.Pix {
			img.Pix[i] = v
		}
	}
	// bar draws a vertical white band at x: real SPATIAL change per frame. A flat full-frame value
	// shift is a DC offset the encoder predicts away, which made the "moving" control read
	// identically to the static arm.
	bar := func(x int) {
		fill(0x20)
		for y := 0; y < m3H; y++ {
			row := img.Pix[y*img.Stride:]
			for dx := 0; dx < 96; dx++ {
				px := (x + dx) % m3W
				p := row[px*4:]
				p[0], p[1], p[2], p[3] = 255, 255, 255, 255
			}
		}
	}
	fill(0x40)
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(120 * time.Second) // backstop; the parent kills us
	n := 0
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if mode == "moving" {
				n += 37 // coprime-ish step so the bar never lands on the same column twice in a row
				bar(n % m3W)
			}
			_ = fs.Send(img)
		}
	}
}

// startPublisher spawns the publisher role and waits for a usable shared texture.
func startPublisher(t *testing.T, mode string) (*exec.Cmd, uint64) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestInc3MeasurePublisher", "-test.v")
	cmd.Env = append(os.Environ(), "RAVE_M3_ROLE="+mode)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for {
		h, _, w, hh, ok := videoshare.SenderShare(m3Name(mode))
		if ok && w == m3W && hh == m3H {
			return cmd, h
		}
		if time.Now().After(deadline) {
			t.Skip("publisher never registered a shared texture (no GPU / no SpoutLibrary.dll)")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestInc3MeasureFrameCounter (M1): is the frame counter readable METADATA-ONLY, i.e. with no GL
// context, no receiver binding and no pixel transfer? That is the whole feasibility question for
// the design's "frame-new gating via ... a metadata-only receiver".
func TestInc3MeasureFrameCounter(t *testing.T) {
	pub, _ := startPublisher(t, "moving")
	_ = pub

	first, fps, ok := videoshare.SenderFrame(m3Name("moving"))
	if !ok {
		t.Skip("no frame-counter backend")
	}
	t.Logf("M1 first read: frame=%d fps=%.1f", first, fps)
	if first < 0 {
		// Either the sender disabled counting, or these late vtable slots are SKEWED against the
		// shipped DLL (the shim already documents exactly that for GetSenderWidth/Height). Keep
		// sampling: a real counter moves monotonically, skew produces unrelated junk per call.
		t.Logf("M1 first read is negative - sampling to tell a disabled counter from vtable skew")
		var vals []int64
		for i := 0; i < 8; i++ {
			f, fp, _ := videoshare.SenderFrame(m3Name("moving"))
			vals = append(vals, f)
			t.Logf("M1 sample %d: frame=%d fps=%.1f", i, f, fp)
			time.Sleep(50 * time.Millisecond)
		}
		mono := true
		for i := 1; i < len(vals); i++ {
			if vals[i] < vals[i-1] {
				mono = false
			}
		}
		t.Logf("M1 VERDICT: counter unusable through a metadata-only receiver (monotonic=%v). "+
			"Item 2's stated mechanism is NOT available on this SDK pairing.", mono)
		return
	}
	// Advance check: a moving publisher at 60 fps must move the counter within a few hundred ms.
	var last int64 = first
	moved, samples := 0, 0
	t0 := time.Now()
	for time.Since(t0) < 1500*time.Millisecond {
		time.Sleep(16 * time.Millisecond)
		f, _, _ := videoshare.SenderFrame(m3Name("moving"))
		samples++
		if f != last {
			moved++
			last = f
		}
	}
	_, fps2, _ := videoshare.SenderFrame(m3Name("moving"))
	t.Logf("M1 over 1.5 s: %d/%d samples showed a new frame number, frame %d → %d, sender fps=%.1f",
		moved, samples, first, last, fps2)
	// Cost: how long does one metadata read take? This is what a per-tick poller would pay.
	const n = 2000
	c0 := time.Now()
	for i := 0; i < n; i++ {
		_, _, _ = videoshare.SenderFrame(m3Name("moving"))
	}
	per := time.Since(c0) / n
	t.Logf("M1 read cost: %v per read (%d reads) - a 60 Hz poller would spend %.3f%% of one core",
		per, n, float64(per)/float64(time.Second/60)*100)
	if moved == 0 {
		t.Logf("M1 VERDICT: the counter does NOT advance through a metadata-only receiver on this rig - item 2 is NOT implementable this way")
		return
	}
	t.Logf("M1 VERDICT: metadata-only frame counting WORKS (%d transitions/1.5 s) - item 2 is implementable", moved)
}

// TestInc3MeasureFanout (M2): what does the Nth zero-copy session on ONE sender cost? Design §8.3
// leaves this an open measurement and forbids pre-building the shared-copy optimisation for it.
func TestInc3MeasureFanout(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	startPublisher(t, "moving")

	open := func(n int) []*ProcSession {
		var out []*ProcSession
		for i := 0; i < n; i++ {
			s, err := OpenProcSessionOpts(ProcOpts{
				LUID: 0, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H, FPS: 60, Kbps: 8000, Gop: 60,
				Spout: &SpoutSource{Name: m3Name("moving"), Resolve: func() (uint64, uint32, int, int, bool) {
					return videoshare.SenderShare(m3Name("moving"))
				}},
			})
			if err != nil {
				for _, p := range out {
					p.Close()
				}
				t.Skipf("could not open zero-copy session %d: %v", i+1, err)
			}
			go func(p *ProcSession) {
				for range p.Output() { // drain, or the ring backs up and skews the numbers
				}
			}(s)
			out = append(out, s)
		}
		return out
	}

	sample := func(label string, ss []*ProcSession) {
		for _, s := range ss {
			_ = s.Stats() // anchor the interval-derived rates
		}
		time.Sleep(2 * time.Second)
		var sumBusy, sumFPS float64
		var skips, mtx, errs uint64
		for _, s := range ss {
			st := s.Stats()
			sumBusy += st.EncBusyMs
			sumFPS += st.CapFPS
			skips += st.CapSkips
			mtx += st.MtxTimeouts
			errs += st.SrcErrors
		}
		n := float64(len(ss))
		t.Logf("M2 %s: sessions=%d meanEncBusy=%.2fms meanCapFPS=%.1f totalSkips=%d totalMtxTimeouts=%d srcErrors=%d childCPU=%.1f%%",
			label, len(ss), sumBusy/n, sumFPS/n, skips, mtx, errs, ss[0].Stats().ChildCPUPct)
	}

	one := open(1)
	sample("1 session", one)
	four := open(3) // 3 more = 4 total on the same sender
	all := append(append([]*ProcSession{}, one...), four...)
	sample("4 sessions on ONE sender", all)
	for _, s := range all {
		s.Close()
	}
	t.Log("M2 VERDICT: compare meanEncBusy + totalMtxTimeouts between the two lines. Item 1 (one " +
		"shared CopyResource per adapter+sender) is only justified if the per-session cost or the " +
		"mutex contention grows materially with N.")
}

// TestInc3MeasureDuplicateCost (M3): what does a duplicate frame on a STATIC sender actually cost?
// §6 predicts "a few hundred bytes/frame" as skipped-macroblock P-frames; R12 says count it before
// deciding. This is that count.
func TestInc3MeasureDuplicateCost(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)

	measure := func(t *testing.T, mode string) (bytesPerFrame float64, frames int, total int) {
		startPublisher(t, mode) // cleanup is registered on the SUBTEST: the arms never overlap
		s, err := OpenProcSessionOpts(ProcOpts{
			LUID: 0, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H, FPS: 60, Kbps: 8000, Gop: 60,
			Spout: &SpoutSource{Name: m3Name(mode), Resolve: func() (uint64, uint32, int, int, bool) {
				return videoshare.SenderShare(m3Name(mode))
			}},
		})
		if err != nil {
			t.Skipf("could not open a zero-copy session: %v", err)
		}
		done := make(chan struct{})
		var n, bytes int
		go func() {
			for au := range s.Output() {
				n++
				bytes += len(au.Data)
			}
			close(done)
		}()
		time.Sleep(3 * time.Second)
		s.Close()
		<-done
		if n == 0 {
			return 0, 0, 0
		}
		return float64(bytes) / float64(n), n, bytes
	}

	// Each arm needs its OWN publisher and a sender name has one owner at a time, so each arm runs
	// as a subtest whose publisher is torn down before the next starts. (Registering the cleanup on
	// the parent left both publishers alive and made both arms measure the same static sender.)
	t.Run("static", func(t *testing.T) {
		bpf, n, total := measure(t, "static")
		t.Logf("M3 STATIC sender: %d AUs, %d bytes total, %.0f bytes/frame → %.2f Mbps at 60 fps",
			n, total, bpf, bpf*60*8/1e6)
	})
	t.Run("moving", func(t *testing.T) {
		bpf, n, total := measure(t, "moving")
		t.Logf("M3 MOVING sender: %d AUs, %d bytes total, %.0f bytes/frame → %.2f Mbps at 60 fps",
			n, total, bpf, bpf*60*8/1e6)
	})
	t.Log("M3 VERDICT: item 2 (frame-new gating) only pays if the STATIC bytes/frame is a material " +
		"share of the route budget. A few hundred bytes/frame is ~0.1 Mbps, i.e. noise against a " +
		"20 Mbps route, and duplicates keep the peer's jitter buffer fed (§6).")
}

// TestInc3MeasureCrossAdapter (M4): does a zero-copy open on a DIFFERENT adapter than the sender's
// really refuse? That is risk R7's premise and the whole reason increment 3 lists adapter-affinity
// resolution. If it refuses, the affinity re-place has something to fix; if it succeeds, R7 does not
// bite on this rig and the item is unnecessary here.
func TestInc3MeasureCrossAdapter(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	ads := encoderscan.Adapters()
	for i, a := range ads {
		t.Logf("M4 adapter %d: LUID=%s %q", i, a.LUID, a.Name)
	}
	if len(ads) < 2 {
		t.Skip("M4 needs two adapters")
	}
	startPublisher(t, "moving")

	try := func(luid int64) error {
		s, err := OpenProcSessionOpts(ProcOpts{
			LUID: luid, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H, FPS: 60, Kbps: 8000, Gop: 60,
			Spout: &SpoutSource{Name: m3Name("moving"), Resolve: func() (uint64, uint32, int, int, bool) {
				return videoshare.SenderShare(m3Name("moving"))
			}},
		})
		if err != nil {
			return err
		}
		go func() {
			for range s.Output() {
			}
		}()
		_ = s.Stats()
		time.Sleep(700 * time.Millisecond)
		st := s.Stats()
		s.Close()
		if st.CapFrames == 0 {
			return fmt.Errorf("opened but captured nothing (capFlags=%#x srcErrors=%d)", st.CapFlags, st.SrcErrors)
		}
		t.Logf("M4   → zero-copy live: capFrames=%d capFlags=%#x encBusy=%.2fms", st.CapFrames, st.CapFlags, st.EncBusyMs)
		return nil
	}

	for i, a := range ads {
		luid, ok := encoderscan.LUIDInt64(a.LUID)
		if !ok {
			t.Logf("M4 adapter %d: unparseable LUID %q", i, a.LUID)
			continue
		}
		if err := try(luid); err != nil {
			t.Logf("M4 adapter %d (%s): REFUSED - %v", i, a.LUID, err)
		} else {
			t.Logf("M4 adapter %d (%s): ACCEPTED", i, a.LUID)
		}
	}
	t.Log("M4 VERDICT: an adapter that REFUSES while another ACCEPTS is exactly R7 - the affinity " +
		"re-place turns that refusal into a working zero-copy route instead of a readback downgrade.")
}

// TestInc3AffinityLiveReplace is the LIVE gate for item 3: open a zero-copy session pinned to the
// adapter M4 proved REFUSES this sender, with the other adapter offered as a candidate, and assert
// the session comes up zero-copy on the adapter that owns the texture instead of downgrading.
//
// The control arm is the same open WITHOUT candidates: it must still refuse, which is what proves
// the re-place - not some incidental retry - is what made the difference.
func TestInc3AffinityLiveReplace(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	ads := encoderscan.Adapters()
	if len(ads) < 2 {
		t.Skip("needs two adapters (R7 cannot be reproduced on one)")
	}
	var luids []int64
	for _, a := range ads {
		if l, ok := encoderscan.LUIDInt64(a.LUID); ok {
			luids = append(luids, l)
		}
	}
	if len(luids) < 2 {
		t.Skip("could not parse two adapter LUIDs")
	}
	startPublisher(t, "moving")
	spout := func() *SpoutSource {
		return &SpoutSource{Name: m3Name("moving"), Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(m3Name("moving"))
		}}
	}

	// Which adapter refuses this sender? (M4 says one does; find it rather than assuming an order.)
	resetAffinity()
	refusing, accepting := int64(0), int64(0)
	for _, l := range luids {
		s, err := OpenProcSessionOpts(ProcOpts{LUID: l, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H,
			FPS: 60, Kbps: 8000, Gop: 60, Spout: spout()})
		if err != nil {
			refusing = l
			continue
		}
		accepting = l
		s.Close()
	}
	if refusing == 0 || accepting == 0 {
		t.Skipf("no cross-adapter refusal on this rig right now (refusing=%#x accepting=%#x)", refusing, accepting)
	}
	t.Logf("R7 live: adapter %#x refuses this sender, %#x accepts it", uint64(refusing), uint64(accepting))

	// CONTROL: no candidates → the refusal stands (this is the pre-inc-3 behaviour).
	resetAffinity()
	if _, err := OpenProcSessionOpts(ProcOpts{LUID: refusing, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H,
		FPS: 60, Kbps: 8000, Gop: 60, Spout: spout()}); err == nil {
		t.Fatal("control arm opened without candidates - the refusal is not reproducible, so the test proves nothing")
	} else {
		t.Logf("control (no candidates): %v", err)
	}

	// TREATMENT: offer both adapters → the session must be re-placed and come up zero-copy.
	resetAffinity()
	s, err := OpenProcSessionOpts(ProcOpts{LUID: refusing, InW: m3W, InH: m3H, OutW: m3W, OutH: m3H,
		FPS: 60, Kbps: 8000, Gop: 60, Spout: spout(), ZeroCopyAdapters: luids})
	if err != nil {
		t.Fatalf("affinity re-place failed: %v", err)
	}
	go func() {
		for range s.Output() {
		}
	}()
	if !s.AdapterMoved() {
		s.Close()
		t.Fatal("session did not record a move")
	}
	if s.AdapterLUID() != accepting {
		s.Close()
		t.Fatalf("re-placed onto %#x, want the accepting adapter %#x", uint64(s.AdapterLUID()), uint64(accepting))
	}
	_ = s.Stats()
	time.Sleep(900 * time.Millisecond)
	st := s.Stats()
	s.Close()
	t.Logf("re-placed session: adapter %#x capFrames=%d capFPS=%.1f capFlags=%#x encBusy=%.2fms adapterMoved=%v moves=%d",
		uint64(accepting), st.CapFrames, st.CapFPS, st.CapFlags, st.EncBusyMs, st.AdapterMoved, AdapterMoves())
	if st.CapFlags&1 == 0 || st.CapFrames == 0 {
		t.Fatalf("re-placed session is not capturing (capFlags=%#x capFrames=%d)", st.CapFlags, st.CapFrames)
	}
	if !st.AdapterMoved {
		t.Fatal("ProcStats.AdapterMoved is false on a re-placed session - the panel would not show it")
	}
	if got, ok := AdapterAffinity(m3Name("moving")); !ok || got != accepting {
		t.Fatalf("affinity cache = (%#x,%v), want the accepting adapter", uint64(got), ok)
	}
	resetAffinity()
}
