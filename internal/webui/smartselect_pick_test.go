package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

// A pick must update the control's displayed value IMMEDIATELY. Nothing re-renders
// surfaces like settings after applySet, so the ss-<id> patch ssPick emits is the only
// thing the user sees until the next full tab render - it must carry the NEW value.
// (Long-standing bug: config persisted but the button showed the old value forever.)
func TestSsPickUpdatesDisplayedValueImmediately(t *testing.T) {
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "settings", started: time.Now(),
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	sh := newVirtualShell(nil, func(string) {}, func(string) {})
	u.shell = sh
	t.Cleanup(func() { sh.terminate(); releaseUIState(u) })

	const id = "sspicktest-fmt"
	ssRegister(id, "set:sspicktest-fmt", "flac", func() []ssOpt {
		return []ssOpt{{Val: "flac", Label: "flac"}, {Val: "wav", Label: "wav"}}
	})
	t.Cleanup(func() {
		ssMu.Lock()
		delete(ssSts, id)
		delete(ssOpts, id)
		delete(ssActs, id)
		delete(ssCurs, id)
		ssMu.Unlock()
	})

	u.ssPick(id, "wav")

	got := ssResolve(id)
	if got.CurLabel != "wav" {
		t.Fatalf("displayed value after pick = %q, want %q (stale-cur bug)", got.CurLabel, "wav")
	}
	var curRow *selRow
	for i := range got.Rows {
		if got.Rows[i].Cur {
			curRow = &got.Rows[i]
		}
	}
	if curRow == nil || curRow.Val != "wav" {
		t.Fatalf("current row after pick = %+v, want wav", curRow)
	}
	if got.Open {
		t.Fatalf("dropdown still open after pick")
	}
	// The patched fragment itself must carry the new value.
	if html := ssInner(id); !strings.Contains(html, ">wav<") {
		t.Fatalf("ssInner after pick does not show the new value: %s", html)
	}
}

// A pick whose act is a core (non-registry) act like set: must reach applySet and
// persist. Regression: ssPick used u.dispatch, which routes ONLY registry handlers -
// every plain settings select silently persisted nothing.
func TestSsPickReachesCoreSetHandler(t *testing.T) {
	cfg := &config.Config{}
	u := &UI{svc: ui.Services{Cfg: cfg}, active: "settings", started: time.Now(),
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	sh := newVirtualShell(nil, func(string) {}, func(string) {})
	u.shell = sh
	t.Cleanup(func() { sh.terminate(); releaseUIState(u) })

	const id = "set-ar-format"
	ssRegister(id, "set:ar-format", "flac", func() []ssOpt {
		return []ssOpt{{Val: "flac", Label: "flac"}, {Val: "wav", Label: "wav"}}
	})
	t.Cleanup(func() {
		ssMu.Lock()
		delete(ssSts, id)
		delete(ssOpts, id)
		delete(ssActs, id)
		delete(ssCurs, id)
		ssMu.Unlock()
	})

	u.ssPick(id, "wav")

	if got := cfg.Features.AudioRecord.Format; got != "wav" {
		t.Fatalf("config after pick = %q, want %q (pick never reached applySet)", got, "wav")
	}
}
