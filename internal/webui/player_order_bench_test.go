package webui

import (
	"testing"

	"rave.page/mate/internal/featurehost"
)

// Phase B4a numbers. Two rows matter:
//
//	container_*  what a container patch (main / #lib-body / #lib-detail) pays for keeping the
//	             embedded players correct. resync_old is the retired workaround transcribed
//	             verbatim: re-render + re-quote EVERY embedded player, unconditionally, after
//	             every container patch. ordered_quiet is the generation-counter path when nothing
//	             moved (the overwhelming case: a tab switch, a section change, a nav click).
//	             ordered_raced is the case that used to be broken - a mutation landed mid-build,
//	             so exactly one component is re-rendered.
//	tick_*       the ~1 Hz player tick, plus the engine sampling the collapse removed from it.
//
// The container UIs run with shell == nil so the queue insert is out of the numbers (it is
// identical work for both paths); the tick bench keeps a shell because mpPushRealtime needs one,
// and drains periodically so the queue cap cannot start dropping mid-run.
//
// Run: GOWORK=off go test -count=1 -tags zigui ./internal/webui -run '^$' -bench 'PlayerContainer|PlayerTick' -benchmem
// (also runs untagged, which measures the Go renderer instead of the Zig bridge)

// mpResyncLegacy is the RETIRED workaround, transcribed for the bench (and as the reference the
// numbers are quoted against): every embedded player re-rendered + re-patched after every container
// patch. The <video> restore eval is left out - it is identical on both paths.
func (u *UI) mpResyncLegacy() {
	for _, host := range mpHosts {
		if t := u.mpSnap(host); len(t.media) > 0 {
			u.mpPatch(t.host, "root", u.mpInnerHTML(t))
		}
	}
}

// benchPlayerUI loads both hosts with a realistic set: a playing 1 h audio capture with peaks,
// probe + loudness chips and markers, which is what the waveform SVG cost tracks.
func benchPlayerUI(tb testing.TB, withShell bool) *UI {
	tb.Helper()
	u := newMpUI(tb)
	if !withShell {
		u.shell = nil
	}
	u.mpMirrorOv = &scriptMirror{pin: true, seq: []featurehost.State{
		{Path: mpTestPath, Playing: true, Cur: 1234.5, Total: 3600}}}
	peaks := make([]byte, 20000)
	for i := range peaks {
		peaks[i] = byte(i * 7 % 256)
	}
	mom := make([]float64, 3600)
	for i := range mom {
		mom[i] = -18 + float64(i%40)/8
	}
	for _, host := range mpHosts {
		*u.mp(host) = mpSt{host: host, viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
			firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: 1,
			markers: []mpMark{{off: 0, label: "Opener"}, {off: 900, label: "Peak time"}, {off: 2700, label: "Closer"}},
			media: []mpMedia{{path: mpTestPath, kind: "audio", dur: 3600, size: 987654321, peaks: peaks,
				loud: &mpLoud{I: -9.1, TP: -0.3, LRA: 6.4, Step: 1, Mom: mom}}}}
	}
	return u
}

func BenchmarkPlayerContainerPatch(b *testing.B) {
	u := benchPlayerUI(b, false)
	// size the thing being avoided
	inner := u.mpInnerHTML(u.mpSnap("library"))
	b.Logf("component %d B of HTML per host, %d hosts", len(inner), len(mpHosts))

	b.Run("resync_old", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			u.mpResyncLegacy()
		}
	})
	b.Run("ordered_quiet", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			mk := u.mpMarkGens()
			u.mpHeal(mk)
		}
	})
	b.Run("ordered_raced", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			mk := u.mpMarkGens()
			u.mpMut("library", func(v *mpSt) { v.media[0].peaksLoading = !v.media[0].peaksLoading })
			u.mpHeal(mk)
		}
	})
	b.Run("mark_only", func(b *testing.B) { // the whole quiet-path cost, isolated
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if mk := u.mpMarkGens(); len(mk) == 0 {
				b.Fatal("no hosts")
			}
		}
	})
}

func BenchmarkPlayerTick(b *testing.B) {
	u := benchPlayerUI(b, true)
	snap := u.mpSnap("library")

	b.Run("tick", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			mpTick(u, "library")
			if i%64 == 63 {
				u.drainEvals() // keep the queue off its 512 cap; amortized over 64 ticks
			}
		}
		u.drainEvals()
	})
	// What the collapse removed from the tick: the tick sampled the engine 5 times (snapshot,
	// wave playhead, transport clock, transport seek slider, hover readout) and a component render
	// 4 times. A sample is a proxy-mirror mutex read + time.Now + copy.
	b.Run("sample", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if u.mpSampleEng(&snap).total == 0 {
				b.Fatal("nothing sampled")
			}
		}
	})
	b.Run("removed_samples_per_tick", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for j := 0; j < 4; j++ {
				if u.mpSampleEng(&snap).total == 0 {
					b.Fatal("nothing sampled")
				}
			}
		}
	})
	b.Run("render_inner", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if u.mpInnerHTML(snap) == "" {
				b.Fatal("empty render")
			}
		}
	})
}
