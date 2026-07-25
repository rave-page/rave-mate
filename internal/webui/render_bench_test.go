package webui

// Phase-B baseline, UNTAGGED half: renderer + state-build + marshal costs that need no Zig lib, so
// `GOWORK=off go test -bench` measures them on a stub build too. Fixtures come from the untagged
// sources (render_settings_test.go's UI, render_dialogs_a_test.go's dialog states); the per-tab
// Go-vs-Zig table lives in render_bench_zig_test.go (//go:build zigui) because the tab fixtures are
// defined in the tagged golden files.
//
// Run: GOWORK=off go test -count=1 ./internal/webui -run '^$' -bench . -benchtime 200x
// Numbers from this machine at this commit: .devnotes/PHASEB_BASELINE.md
//
// The benchSink* vars defeat dead-store elimination; a renderer whose result is dropped can be
// optimised into nothing and the number becomes a lie.

import (
	"testing"
)

var (
	benchSinkS string
	benchSinkB []byte
)

func sink(s string)   { benchSinkS = s }
func sinkB(b []byte)  { benchSinkB = b }
func benchSinks() int { return len(benchSinkS) + len(benchSinkB) } // keeps both vars used

// benchSettingsUI is the zero-config settings UI pinned to one sub-tab (the state build walks
// EVERY card - the search path renders them all - so this is the heaviest state builder we have).
func benchSettingsUI(tb testing.TB) *UI {
	tb.Helper()
	u := newSettingsTestUI()
	u.setMu.Lock()
	u.setSec = "libmedia"
	u.setMu.Unlock()
	return u
}

// BenchmarkSettingsStateBuild: the impure half (config/probe snapshots, i18n, smart-select
// registration, block lists). Renderer-independent - phase B does not remove this.
func BenchmarkSettingsStateBuild(b *testing.B) {
	u := benchSettingsUI(b)
	b.ResetTimer()
	for range b.N {
		st := u.settingsState()
		sink(st.Title)
	}
}

// BenchmarkSettingsRenderGo: the pure Go renderer over that state.
func BenchmarkSettingsRenderGo(b *testing.B) {
	u := benchSettingsUI(b)
	st := u.settingsState()
	out := settingsHTML(st)
	b.SetBytes(int64(len(out)))
	b.ResetTimer()
	for range b.N {
		sink(settingsHTML(st))
	}
	// ReportMetric AFTER the loop: ResetTimer clears previously reported extra metrics
	// (SetBytes survives it, ReportMetric does not).
	b.ReportMetric(float64(len(out)), "html_B")
}

// BenchmarkSettingsMarshal: the phase-A marshal tax for the same state.
func BenchmarkSettingsMarshal(b *testing.B) {
	u := benchSettingsUI(b)
	st := u.settingsState()
	js := stateJSON(st)
	if js == nil {
		b.Fatal("state marshal failed")
	}
	b.SetBytes(int64(len(js)))
	b.ResetTimer()
	for range b.N {
		sinkB(stateJSON(st))
	}
	b.ReportMetric(float64(len(js)), "state_B")
}

// dialogBenchCases: the four dialog families whose fixtures are untagged (dialog-sweep A kept
// render_dialogs_a_test.go untagged so the parity harness and the Zig gate share one fixture set).
func dialogBenchCases() []struct {
	name string
	st   any
	goFn func() string
} {
	txt := dlgTxtFx()[1] // open, populated
	fix := dlgFixFx()["populated"]
	pat := dlgPatFx()["live"]
	pre := dlgPresetFx()["video"]
	return []struct {
		name string
		st   any
		goFn func() string
	}{
		{"txtExport", txt, func() string { return pubTxtDlgHTMLOf(txt) }},
		{"fixTimes", fix, func() string { return pubFixDlgHTMLOf(fix) }},
		{"patMgr", pat, func() string { return cePatMgrHTMLOf(pat) }},
		{"presetEditor", pre, func() string { return mpPresetDlgHTMLOf(pre) }},
	}
}

// BenchmarkDialogRenderGo: modal renderers (re-run on EVERY re-open, no fragment patching).
func BenchmarkDialogRenderGo(b *testing.B) {
	for _, c := range dialogBenchCases() {
		b.Run(c.name, func(b *testing.B) {
			out := c.goFn()
			b.SetBytes(int64(len(out)))
			b.ResetTimer()
			for range b.N {
				sink(c.goFn())
			}
			b.ReportMetric(float64(len(out)), "html_B")
		})
	}
}

// BenchmarkDialogMarshal: their state sizes + marshal cost.
func BenchmarkDialogMarshal(b *testing.B) {
	for _, c := range dialogBenchCases() {
		b.Run(c.name, func(b *testing.B) {
			js := stateJSON(c.st)
			if js == nil {
				b.Fatalf("%s: state marshal failed", c.name)
			}
			b.SetBytes(int64(len(js)))
			b.ResetTimer()
			for range b.N {
				sinkB(stateJSON(c.st))
			}
			b.ReportMetric(float64(len(js)), "state_B")
		})
	}
}

// TestBenchSinksUsed keeps the sinks honest under `go vet`/unused analysis and proves the bench
// helpers run at all in a plain `go test` (no -bench) pass.
func TestBenchSinksUsed(t *testing.T) {
	sink("x")
	sinkB([]byte("yy"))
	if benchSinks() != 3 {
		t.Fatalf("sinks = %d, want 3", benchSinks())
	}
}
