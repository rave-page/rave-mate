package testcard

import (
	"image"
	"sync"
	"testing"
	"time"
)

// feed renders seq under session and observes it at stage, with now = t0 + lagMs.
func feed(stage string, session uint16, seq uint32, t0 time.Time, lagMs int64) {
	img := image.NewNRGBA(image.Rect(0, 0, 640, 360))
	Render(img, Payload{Session: session, Seq: seq, T0ms: uint32(t0.UnixMilli()), FPS: 30}, t0)
	ObserveAt(stage, img, t0.Add(time.Duration(lagMs)*time.Millisecond))
}

// The verdicts this harness exists to produce: dups = freeze, jumps = skipped frames, both with
// exact counts - no downstream rate counter can produce either.
func TestVerifierCountsGapsDupsAndFreezes(t *testing.T) {
	VerifyReset()
	base := time.UnixMilli(1753000000000)
	// seq: 1 2 3 3 3 4 8  -> dups 2 (run of 2), gap 3 (4->8), unique advances 4
	seqs := []uint32{1, 2, 3, 3, 3, 4, 8}
	for i, s := range seqs {
		feed("t-gaps", 0x111, s, base.Add(time.Duration(i)*33*time.Millisecond), 40)
	}
	v := VerifySnapshot()["t-gaps"]
	if v.Decoded != 7 || v.Frames != 7 {
		t.Fatalf("decoded=%d frames=%d, want 7/7", v.Decoded, v.Frames)
	}
	if v.Dups != 2 || v.MaxDupRun != 2 {
		t.Fatalf("dups=%d maxRun=%d, want 2/2", v.Dups, v.MaxDupRun)
	}
	if v.Gaps != 3 || v.MaxGap != 3 {
		t.Fatalf("gaps=%d maxGap=%d, want 3/3", v.Gaps, v.MaxGap)
	}
	if v.Unique != 4 {
		t.Fatalf("unique=%d, want 4", v.Unique)
	}
	if v.Reorders != 0 {
		t.Fatalf("reorders=%d, want 0", v.Reorders)
	}
}

func TestVerifierMeasuresDrift(t *testing.T) {
	VerifyReset()
	base := time.UnixMilli(1753000000000)
	// Lag grows 40 -> 40 -> 100 -> 220ms: drift must read 180 (220 - min 40).
	for i, lag := range []int64{40, 40, 100, 220} {
		feed("t-drift", 0x222, uint32(i), base.Add(time.Duration(i)*33*time.Millisecond), lag)
	}
	v := VerifySnapshot()["t-drift"]
	if v.MinDeltaMs != 40 || v.LastDeltaMs != 220 {
		t.Fatalf("min=%d last=%d, want 40/220", v.MinDeltaMs, v.LastDeltaMs)
	}
	if v.DriftMs() != 180 {
		t.Fatalf("drift=%d, want 180", v.DriftMs())
	}
}

// A generator restart (new session) must re-baseline, not report a giant gap/reorder.
func TestVerifierHandlesSessionRestart(t *testing.T) {
	VerifyReset()
	base := time.UnixMilli(1753000000000)
	feed("t-restart", 0x333, 500, base, 40)
	feed("t-restart", 0x333, 501, base.Add(33*time.Millisecond), 40)
	feed("t-restart", 0x444, 0, base.Add(66*time.Millisecond), 40) // restarted
	feed("t-restart", 0x444, 1, base.Add(99*time.Millisecond), 40)
	v := VerifySnapshot()["t-restart"]
	if v.Restarts != 1 {
		t.Fatalf("restarts=%d, want 1", v.Restarts)
	}
	if v.Gaps != 0 || v.Reorders != 0 {
		t.Fatalf("gaps=%d reorders=%d across a restart, want 0/0", v.Gaps, v.Reorders)
	}
	if v.Session != 0x444 {
		t.Fatalf("session=%#x, want 0x444", v.Session)
	}
}

// Non-card frames on a stage that HAS seen a card keep Frames climbing with Decoded flat - the
// "card disappeared mid-run" signature.
func TestVerifierTracksCardDisappearing(t *testing.T) {
	VerifyReset()
	base := time.UnixMilli(1753000000000)
	feed("t-gone", 0x555, 1, base, 40)
	blank := image.NewNRGBA(image.Rect(0, 0, 640, 360))
	for range 5 {
		ObserveAt("t-gone", blank, base.Add(time.Second))
	}
	v := VerifySnapshot()["t-gone"]
	if v.Frames != 6 || v.Decoded != 1 {
		t.Fatalf("frames=%d decoded=%d, want 6/1", v.Frames, v.Decoded)
	}
	// And a stage that never saw a card must not appear at all.
	ObserveAt("t-never", blank, base)
	if _, ok := VerifySnapshot()["t-never"]; ok {
		t.Fatal("stage with no card ever should not be registered")
	}
}

func TestObserveIsConcurrencySafe(t *testing.T) {
	VerifyReset()
	base := time.UnixMilli(1753000000000)
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for i := range 50 {
				feed("t-conc", 0x666, uint32(g*50+i), base.Add(time.Duration(i)*time.Millisecond), 10)
			}
		})
	}
	wg.Wait()
	if v := VerifySnapshot()["t-conc"]; v.Decoded != 200 {
		t.Fatalf("decoded=%d, want 200", v.Decoded)
	}
}

// Generator ground truth: frames flow at roughly the target rate through the sink seam, stats add
// up, and Stop joins cleanly.
type memSink struct {
	mu     sync.Mutex
	frames []Payload
}

func (m *memSink) Send(img *image.NRGBA) error {
	p, derr := Decode(img)
	if derr != DecodeOK {
		panic("generator rendered an undecodable frame: " + derr.String())
	}
	m.mu.Lock()
	m.frames = append(m.frames, p)
	m.mu.Unlock()
	return nil
}
func (m *memSink) Close() {}

func TestGeneratorProducesSequentialDecodableFrames(t *testing.T) {
	sink := &memSink{}
	g, err := NewGen(sink, 640, 360, 60)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.frames)
		sink.mu.Unlock()
		if n >= 10 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.Stop()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.frames) < 10 {
		t.Fatalf("only %d frames in 3s at 60fps", len(sink.frames))
	}
	for i, p := range sink.frames {
		if p.Seq != uint32(i) {
			t.Fatalf("frame %d carries seq %d - generator must never skip a SEQ (it skips ticks)", i, p.Seq)
		}
		if p.Session != sink.frames[0].Session {
			t.Fatal("session changed mid-run")
		}
	}
	st := g.Stats()
	if st.Frames != uint64(len(sink.frames)) {
		t.Fatalf("stats.Frames=%d, sink saw %d", st.Frames, len(sink.frames))
	}
}

func TestGeneratorRefusesOutOfRangeSpecs(t *testing.T) {
	for _, s := range [][3]int{{100, 100, 30}, {1280, 720, 0}, {1280, 720, 500}, {8000, 720, 30}} {
		if _, err := NewGen(&memSink{}, s[0], s[1], s[2]); err == nil {
			t.Fatalf("NewGen(%v) accepted", s)
		}
	}
}
