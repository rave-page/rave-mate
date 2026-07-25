//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// v1 (JSON) vs v2 (RZW1 binary) for the pilot views. Each benchmark measures the WHOLE
// dispatch cost - state serialization + the Zig render - because that is what a tick pays.
// Run: GOWORK=off go test -count=1 -tags zigui ./internal/webui -run '^$' -bench WireBench -benchmem

// wireBenchTail is the realistic hot path: livePush patches #log-view ~1 Hz with the 400-line
// filtered tail (logTailN).
func wireBenchTail() logsLines {
	es := make([]logsEntry, 0, logTailN)
	for i := 0; i < logTailN; i++ {
		es = append(es, logsEntry{
			Time: "09:15:01.250", Lvl: "INFO", Cls: "INFO", Src: "session",
			Msg: "merge tick " + strings.Repeat("x", i%40), Fields: "map[bpm:128]",
		})
	}
	return logsLines{Wired: true, NoBus: "no bus", NoEntries: "none", Entries: es}
}

func wireBenchLogsState() logsState {
	st := logsFixtures()["populated"]
	st.Lines = wireBenchTail()
	return st
}

func benchPair(b *testing.B, v1 func() (string, bool), v2 func() (string, bool)) {
	b.Run("v1_json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := v1(); !ok {
				b.Fatal("v1 render failed")
			}
		}
	})
	b.Run("v2_wire", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := v2(); !ok {
				b.Fatal("v2 render failed")
			}
		}
	})
}

func BenchmarkWireBenchAppGroups(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := agFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderAppGroups(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderAppGroupsV2(wireAgState(st)) })
}

func BenchmarkWireBenchAppGroupsBody(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := agFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderAppGroupsBody(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderAppGroupsBodyV2(wireAgState(st)) })
}

func BenchmarkWireBenchLogs(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := wireBenchLogsState()
	benchPair(b,
		func() (string, bool) { return zigui.RenderLogs(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLogsV2(wireLogsState(st)) })
}

func BenchmarkWireBenchLogsLines(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := wireBenchTail()
	benchPair(b,
		func() (string, bool) { return zigui.RenderLogsLines(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLogsLinesV2(wireLogsLines(st)) })
}

// ── B-2 fan-out: one full-view pair + two representative fragment pairs per tab (the
// fragments are the ~1 Hz path; a full tab render is rare). Fixtures are the golden
// `populated` states, so these numbers sit next to the B0 baseline table.

func BenchmarkWireBenchLive(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := liveFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderLive(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLiveV2(wireLiveState(st)) })
}

func BenchmarkWireBenchLiveTransport(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := liveFixtures()["populated"].Transport
	benchPair(b,
		func() (string, bool) { return zigui.RenderLiveFrag("transport", stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLiveFragV2("transport", wireLiveTransport(st)) })
}

func BenchmarkWireBenchLivePerf(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := liveFixtures()["populated"].Perf
	benchPair(b,
		func() (string, bool) { return zigui.RenderLiveFrag("perf", stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLiveFragV2("perf", wireLivePerf(st)) })
}

func BenchmarkWireBenchMotion(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := moFixtures()["studio"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderMotion(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderMotionV2(wireMoState(st)) })
}

func BenchmarkWireBenchMotionBody(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := moFixtures()["studio"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderMotionBody(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderMotionBodyV2(wireMoState(st)) })
}

func BenchmarkWireBenchPublish(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := pubFixtures()["tracklist"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderPublish(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderPublishV2(wirePub(st)) })
}

func BenchmarkWireBenchPublishHero(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := pubFixtures()["tracklist"].Body.Hero
	benchPair(b,
		func() (string, bool) { return zigui.RenderPublishHero(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderPublishHeroV2(wirePubHero(st)) })
}

func BenchmarkWireBenchSettings(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	f := setFixtures()["libmedia"] // the widest card set in the suite
	f.u.setMu.Lock()
	f.u.setSec, f.u.setQuery = f.sec, f.q
	f.u.setMu.Unlock()
	st := f.u.settingsState()
	benchPair(b,
		func() (string, bool) { return zigui.RenderSettings(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderSettingsV2(wireSetState(st)) })
}

func BenchmarkWireBenchSettingsStatus(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := setStatusSt{V: "ok", T: "https://development.api.rave.page"}
	benchPair(b,
		func() (string, bool) { return zigui.RenderSettingsStatus(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderSettingsStatusV2(wireSetStatus(st)) })
}

func BenchmarkWireBenchLibrary(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := libFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderLibrary(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLibraryV2(wireLibState(st)) })
}

func BenchmarkWireBenchLibraryBody(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := libFixtures()["populated"].Body
	benchPair(b,
		func() (string, bool) { return zigui.RenderLibraryBody(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLibraryBodyV2(wireLibBody(st)) })
}

func BenchmarkWireBenchLibraryCueCell(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := libCueCellSt{Drops: 2, DropsTitle: "2 drops", Cues: 4, CuesTitle: "4 cues"}
	benchPair(b,
		func() (string, bool) { return zigui.RenderLibraryCueCell(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderLibraryCueCellV2(wireLibCueCell(st)) })
}

func BenchmarkWireBenchPlayer(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	u := &UI{}
	b.Cleanup(func() { releaseUIState(u) })
	fx := mpFixtures()["singleEdit"]
	*u.mp(fx.host) = fx
	inner := u.mpInnerState(u.mpSnap(fx.host))
	full := mpFullSt{Host: fx.host, Inner: inner}
	benchPair(b,
		func() (string, bool) { return zigui.RenderPlayer(stateJSON(full)) },
		func() (string, bool) { return zigui.RenderPlayerV2(wireMpFull(full)) })
}

func BenchmarkWireBenchPlayerTp(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	u := &UI{}
	b.Cleanup(func() { releaseUIState(u) })
	fx := mpFixtures()["singleEdit"]
	*u.mp(fx.host) = fx
	st := u.mpInnerState(u.mpSnap(fx.host)).Tp
	benchPair(b,
		func() (string, bool) { return zigui.RenderPlayerTp(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderPlayerTpV2(wireMpTp(st)) })
}

func BenchmarkWireBenchPlayerExport(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	u := &UI{}
	b.Cleanup(func() { releaseUIState(u) })
	fx := mpFixtures()["dualExport"]
	*u.mp(fx.host) = fx
	st := u.mpInnerState(u.mpSnap(fx.host)).EditBox.Export
	benchPair(b,
		func() (string, bool) { return zigui.RenderPlayerExport(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderPlayerExportV2(wireMpExport(st)) })
}

// Serialization only - isolates what the wire replaces (reflection + escaping + quoting).
func BenchmarkWireBenchSerializeLogsTail(b *testing.B) {
	st := wireBenchTail()
	b.Run("json_marshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(stateJSON(st)) == 0 {
				b.Fatal("marshal failed")
			}
		}
	})
	b.Run("wire_encode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(wireLogsLines(st)) == 0 {
				b.Fatal("encode failed")
			}
		}
	})
}
