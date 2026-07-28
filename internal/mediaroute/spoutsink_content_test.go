package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// countingSender lives in dropstats_test.go.

func rawFrame(w, h int, v byte) *medialink.Frame {
	pix := make([]byte, w*h*4)
	for i := range pix {
		pix[i] = v
	}
	return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: pix}
}

// The #58 regression gate: a sink republishing IDENTICAL frames must report the stall through
// PipeStats, because every other number it reports stays healthy. A 4K route shipped one
// bit-identical frame for 48 minutes with published frames climbing, fps 58.5 and dropped 0 - the
// panel had nothing that could contradict it.
func TestSpoutSinkReportsFrozenPictureWhilePublishedClimbs(t *testing.T) {
	framedebug.SetDir(t.TempDir())
	const w, h = 32, 18
	// Unique sender name: recorders are process-global, so a shared name would inherit another
	// test's stall clock and make this pass (or fail) for the wrong reason.
	snd := &countingSender{}
	s := &spoutSink{log: logbus.New(16), fs: snd, name: "frozen-gate-" + t.Name(), w: w, h: h}

	frozen := rawFrame(w, h, 7)
	for range 30 {
		if err := s.Write(frozen); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if err := s.Write(frozen); err != nil {
		t.Fatal(err)
	}

	st := s.PipeStats()
	if st.PubFrames != 31 {
		t.Fatalf("PubFrames=%d, want 31", st.PubFrames)
	}
	if snd.sent != 31 {
		t.Fatalf("sender got %d frames, want 31 - the tap must not swallow frames", snd.sent)
	}
	if st.PubChanges != 0 {
		t.Fatalf("PubChanges=%d on identical frames, want 0", st.PubChanges)
	}
	if st.PubStalledMs < 25 {
		t.Fatalf("PubStalledMs=%d after ~30ms of identical frames, want >= 25: a frozen picture "+
			"must AGE while PubFrames climbs", st.PubStalledMs)
	}

	// A real content change must clear it, else every static moment reads as a fault.
	if err := s.Write(rawFrame(w, h, 200)); err != nil {
		t.Fatal(err)
	}
	st = s.PipeStats()
	if st.PubChanges != 1 {
		t.Fatalf("PubChanges=%d after a changed frame, want 1", st.PubChanges)
	}
	if st.PubStalledMs > 25 {
		t.Fatalf("PubStalledMs=%d right after a change, want ~0", st.PubStalledMs)
	}
}

// InnerContent must lift the verdict up the wrapper chain: the decode wrapper reports the route's
// stats, and a stall that dies at the innermost sink is invisible - exactly how AUBytes was
// collected and rendered nowhere while a black route reported healthy.
func TestInnerContentLiftsTheStallAndReportsUnknownAsNegative(t *testing.T) {
	framedebug.SetDir(t.TempDir())
	const w, h = 16, 16
	s := &spoutSink{log: logbus.New(16), fs: &countingSender{},
		name: "lift-gate-" + t.Name(), w: w, h: h}
	for range 5 {
		_ = s.Write(rawFrame(w, h, 3))
	}
	stalled, changes, hash := medialink.InnerContent(s)
	if stalled < 0 || hash == 0 {
		t.Fatalf("InnerContent gave stalled=%d hash=%d, want a real reading", stalled, hash)
	}
	if changes != 0 {
		t.Fatalf("changes=%d, want 0", changes)
	}
	// A sink that reports nothing must read -1, never 0: "fresh" and "never published" must differ.
	if got, _, _ := medialink.InnerContent(struct{}{}); got != -1 {
		t.Fatalf("InnerContent(non-reporter) = %d, want -1", got)
	}
}
