package webui

import (
	"math"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/ui"
)

// Phase B4a gate for the engine-sample collapse. The transport used to be re-sampled per
// consumer, so ONE render could carry three different engine instants. mpSt.eng makes that
// unrepresentable - proven here by rendering against a mirror that MOVES on every read and
// requiring the bytes of a mirror PINNED to the first sample.
//
// Untagged on purpose: the mechanism is pure Go and must hold on the stub build too.

// ── harness ────────────────────────────────────────────────────────────────────

// scriptMirror is an engine mirror that walks a scripted sequence, one step per read, and counts
// reads. The featurehost mirror can only be moved by a live child process, so this is the only way
// to drive a transport that changes between two renders.
type scriptMirror struct {
	mu   sync.Mutex
	seq  []featurehost.State
	n    int
	pin  bool // don't advance: every read returns seq[0]
	step int
}

func (m *scriptMirror) State() featurehost.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := 0
	if !m.pin {
		i = m.step
		if i >= len(m.seq) {
			i = len(m.seq) - 1
		}
		m.step++
	}
	m.n++
	return m.seq[i]
}

func (m *scriptMirror) reads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

// newMpUI builds a UI with a shell (so the eval queue accepts entries) but no flusher, so the
// queue can be inspected entry by entry.
func newMpUI(t *testing.T) *UI {
	t.Helper()
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "library", started: time.Now(),
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	u.shell = newVirtualShell(nil, func(string) {}, func(string) {})
	u.mu.Lock()
	u.libSection = "collection"
	u.mu.Unlock()
	t.Cleanup(func() { u.shell.terminate(); releaseUIState(u) })
	return u
}

const mpTestPath = `C:\sets\order.flac`

// ── 3. one engine sample per render ─────────────────────────────────────────────

// mpPlayingFixture is an audio player the engine is playing, with a momentary-loudness timeline so
// the hover readout depends on the sampled position.
func mpPlayingFixture(u *UI, host string) {
	mom := make([]float64, 64)
	for i := range mom {
		mom[i] = -20 + float64(i)/4
	}
	peaks := make([]byte, 400)
	for i := range peaks {
		peaks[i] = byte(i * 7 % 256)
	}
	*u.mp(host) = mpSt{host: host, viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: -1,
		media: []mpMedia{{path: mpTestPath, kind: "audio", dur: 600, peaks: peaks,
			loud: &mpLoud{I: -9.1, TP: -0.3, LRA: 6.4, Step: 10, Mom: mom}}}}
}

// mpMirrorScript is the transport moving under a render: playing early, playing much later, then
// stopped (fireEnd zeroes the mirror). Sampling per consumer picked one step EACH.
func mpMirrorScript() []featurehost.State {
	return []featurehost.State{
		{Path: mpTestPath, Playing: true, Cur: 10, Total: 600},
		{Path: mpTestPath, Playing: true, Cur: 300, Total: 600},
		{}, // stopped: the transport row would render "not loaded" beside a live playhead
	}
}

func mpRenderInnerWith(t *testing.T, mirror mpMirror, host string) string {
	t.Helper()
	u := newMpUI(t)
	u.mpMirrorOv = mirror
	mpPlayingFixture(u, host)
	return u.mpInnerHTML(u.mpSnap(host))
}

// TestMpEngOneSamplePerRender: a component render against a MOVING mirror must be byte-identical
// to the same render against a mirror pinned to the FIRST sample, and must read the mirror exactly
// once. Before the collapse it read three times (wave playhead, hover readout, transport row) and
// the three fragments of one DOM could disagree.
func TestMpEngOneSamplePerRender(t *testing.T) {
	moving := &scriptMirror{seq: mpMirrorScript()}
	got := mpRenderInnerWith(t, moving, "library")
	if n := moving.reads(); n != 1 {
		t.Fatalf("one render sampled the engine %d times, want 1", n)
	}
	pinned := &scriptMirror{seq: mpMirrorScript(), pin: true}
	want := mpRenderInnerWith(t, pinned, "library")
	if got != want {
		t.Fatalf("render against a moving mirror differs from the first sample's render (%d vs %d bytes)",
			len(got), len(want))
	}
	// non-vacuous: the samples must actually render differently, or nothing above proves anything
	later := &scriptMirror{seq: mpMirrorScript()[1:], pin: true}
	if other := mpRenderInnerWith(t, later, "library"); other == want {
		t.Fatal("fixture cannot tell two engine samples apart - the gate would pass on a torn render")
	}
	stopped := &scriptMirror{seq: []featurehost.State{{}}, pin: true}
	if idle := mpRenderInnerWith(t, stopped, "library"); idle == want {
		t.Fatal("fixture renders a stopped engine like a playing one")
	}
}

// TestMpEngTransportRowIsOneInstant documents the worst of the tears the per-consumer sampling
// allowed: the transport row itself sampled TWICE - once for the clock readout, once for the seek
// slider's thumb - so one fragment could read "00:10" beside a thumb parked at the 5-minute mark.
func TestMpEngTransportRowIsOneInstant(t *testing.T) {
	u := newMpUI(t)
	u.mpMirrorOv = &scriptMirror{seq: mpMirrorScript()}
	mpPlayingFixture(u, "library")
	st := u.mpTpState(u.mpSnap("library"))
	first := mpMirrorScript()[0]
	if want := pubClock(first.Cur) + " / " + pubClock(first.Total); st.TimeTx != want {
		t.Fatalf("clock %q, want %q (the first sample)", st.TimeTx, want)
	}
	if want := math.Round(1000 * first.Cur / first.Total); st.Seek.Val != want {
		t.Fatalf("seek thumb at %v, want %v - the clock and the thumb are two samples of one row",
			st.Seek.Val, want)
	}
}

// TestMpEngOneSamplePerTick: the ~1 Hz tick patches clock, wave, hover and (across a marker) the
// transport from ONE snapshot, so it must sample once too - it used to sample per patch.
func TestMpEngOneSamplePerTick(t *testing.T) {
	u := newMpUI(t)
	moving := &scriptMirror{seq: mpMirrorScript()}
	u.mpMirrorOv = moving
	mpPlayingFixture(u, "library")
	mpTick(u, "library")
	if n := moving.reads(); n != 1 {
		t.Fatalf("one tick sampled the engine %d times, want 1", n)
	}
	if u.drainEvals() == "" {
		t.Fatal("the tick patched nothing - a playing engine must move the clock/playhead")
	}
}

// TestMpEngAsksAboutActiveMedia pins the sampler's contract: it resolves the ACTIVE media, which is
// what every consumer asked about when each one passed its own `m`.
func TestMpEngAsksAboutActiveMedia(t *testing.T) {
	u := newMpUI(t)
	u.mpMirrorOv = &scriptMirror{seq: []featurehost.State{{Path: mpTestPath, Playing: true, Cur: 42, Total: 600}}, pin: true}
	*u.mp("publish") = mpSt{host: "publish", viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: -1,
		media: []mpMedia{
			{path: mpTestPath, kind: "audio", dur: 600},
			{path: `C:\sets\order.mp4`, kind: "video", dur: 590},
		}}
	audio := u.mpSnap("publish")
	if tr := u.mpEng(&audio); !tr.loaded || tr.cur != 42 {
		t.Fatalf("active=0 must sample the audio engine, got %+v", tr)
	}
	vid := u.mpMut("publish", func(v *mpSt) {
		v.active = 1
		v.vid = mpVid{started: true, cur: 7.5, dur: 590}
	})
	tr := u.mpEng(&vid)
	if !tr.loaded || tr.cur != 7.5 || tr.total != 590 {
		t.Fatalf("active=1 must sample the <video> mirror, got %+v", tr)
	}
}

// TestMpEngNotStoredOnInstance: a sample cached on the shared instance would be served to later
// snapshots as if it were current.
func TestMpEngNotStoredOnInstance(t *testing.T) {
	u := newMpUI(t)
	mirror := &scriptMirror{seq: mpMirrorScript()}
	u.mpMirrorOv = mirror
	mpPlayingFixture(u, "library")
	a := u.mpSnap("library")
	b := u.mpSnap("library")
	if u.mp("library").eng != nil {
		t.Fatal("the instance carries an engine sample - it would go stale")
	}
	if u.mpEng(&a).cur == u.mpEng(&b).cur {
		t.Fatal("two snapshots shared one sample; each must take its own")
	}
	if n := mirror.reads(); n != 2 {
		t.Fatalf("two snapshots took %d samples, want 2", n)
	}
}
