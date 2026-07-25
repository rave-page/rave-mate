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
