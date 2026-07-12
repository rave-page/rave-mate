package webui

// rce-mode state-machine tests (#89): mirror-act interception + the persistence-hook
// branching - in a remote session every editor mutation must stay in ceSt (svc.Lib is nil
// here, so any local persist path would nil-deref) and never arm the tag/DB write-behind.

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/ui"
)

func TestRceInterceptOpen(t *testing.T) {
	for _, tc := range []struct {
		payload string
		path    string
		ok      bool
	}{
		{`{"act":"ce-open:C:\\music\\a.mp3"}`, `C:\music\a.mp3`, true},
		{`{"act":"ce-open:/home/dj/a.flac","val":"x"}`, "/home/dj/a.flac", true},
		{`{"act":"ce-open:"}`, "", false},         // empty path
		{`{"act":"ce-open-pl:12"}`, "", false},    // set flow forwards (P3)
		{`{"act":"ce-open-dir"}`, "", false},      // set flow forwards (P3)
		{`{"act":"mp-surf:down:0.5"}`, "", false}, // unrelated act
		{`not json`, "", false},
	} {
		path, ok := rceInterceptOpen(tc.payload)
		if ok != tc.ok || path != tc.path {
			t.Errorf("rceInterceptOpen(%q) = (%q,%v), want (%q,%v)", tc.payload, path, ok, tc.path, tc.ok)
		}
	}
}

// rceTestUI: headless UI (no window, no Lib) with an rce cue-edit session seeded.
func rceTestUI(t *testing.T) (*UI, *ceSt) {
	t.Helper()
	cap := &capture{}
	u := newHeadlessUI(ui.Services{Cfg: &config.Config{}}, cap.html, cap.eval)
	t.Cleanup(func() { u.Stop(); releaseUIState(u) })
	tr := musiclib.Track{
		Path: `P:\peer\track.mp3`, Title: "Peer Track", DurationSec: 120,
		Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 120}},
		Cues: []musiclib.CuePoint{
			{Kind: musiclib.CueHot, Hotcue: 1, StartMs: 1000},
			{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: 2000},
		},
	}
	grid, err := cuepattern.NewGrid(tr.Beatgrid, tr.DurationSec*1000)
	if err != nil {
		t.Fatalf("grid: %v", err)
	}
	drops := []float64{4000}
	c := u.ce()
	c.mu.Lock()
	c.active, c.path, c.track, c.grid = true, `C:\cache\track.mp3`, tr, grid
	c.drops = append([]float64(nil), drops...)
	c.fileTag = false
	c.rce = &ceRemote{peer: "node1", peerName: "Studio PC", remotePath: tr.Path,
		baseSHA: remotectl.CueStateSHA(tr.Cues, tr.Beatgrid, drops)}
	c.syncSel()
	c.mu.Unlock()
	return u, c
}

func (c *ceSt) snap(t *testing.T) (dirty bool, cues, drops int, tagT, dbT bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rceDirtyLocked(), len(c.track.Cues), len(c.drops), c.tagTimer != nil, c.dbTimer != nil
}

func TestRceMutationsStayLocal(t *testing.T) {
	u, c := rceTestUI(t)

	if dirty, _, _, _, _ := c.snap(t); dirty {
		t.Fatal("fresh session must be clean")
	}

	// drop add: ceSt-only, dirty, no write-behind timers
	u.ceDropAt(8000, false)
	dirty, _, drops, tagT, dbT := c.snap(t)
	if !dirty || drops != 2 || tagT || dbT {
		t.Fatalf("ceDropAt: dirty=%v drops=%d tagTimer=%v dbTimer=%v", dirty, drops, tagT, dbT)
	}

	// cue add (routes through ceSetCues): synchronous ceSt mutation
	u.ceAddCueAt(6000)
	if _, cues, _, _, _ := c.snap(t); cues != 3 {
		t.Fatalf("ceAddCueAt: cues=%d want 3", cues)
	}

	// grid nudge: no DB-persist debounce, markers move in ceSt
	u.ceGridShift(10)
	c.mu.Lock()
	pos := c.track.Beatgrid[0].PositionMs
	c.mu.Unlock()
	if _, _, _, tagT, dbT := c.snap(t); tagT || dbT || pos != 10 {
		t.Fatalf("ceGridShift: pos=%v tagTimer=%v dbTimer=%v", pos, tagT, dbT)
	}

	// hotcue demotion: local
	u.ceConvertAll()
	c.mu.Lock()
	hot := 0
	for _, q := range c.track.Cues {
		if q.Kind == musiclib.CueHot {
			hot++
		}
	}
	c.mu.Unlock()
	if hot != 0 {
		t.Fatalf("ceConvertAll: %d hotcues left", hot)
	}

	// undo swaps back without persist timers
	u.ceUndo()
	if _, _, _, tagT, dbT := c.snap(t); tagT || dbT {
		t.Fatalf("ceUndo armed write-behind: tag=%v db=%v", tagT, dbT)
	}

	// nil svc.Lib: any accidentally-taken local persist path panics in a bg goroutine -
	// give those a beat to surface before the test ends
	time.Sleep(50 * time.Millisecond)
	_ = u
}

func TestRceDirtyTracksBaseline(t *testing.T) {
	u, c := rceTestUI(t)
	u.ceDropAt(8000, false)
	if dirty, _, _, _, _ := c.snap(t); !dirty {
		t.Fatal("mutation must dirty the session")
	}
	u.ceDropAt(8000, true) // remove the same drop → state equals baseline again
	if dirty, _, _, _, _ := c.snap(t); dirty {
		t.Fatal("state equal to baseline must read clean (SHA-derived dirty)")
	}
}

func TestRceCloseGuardsDirty(t *testing.T) {
	u, c := rceTestUI(t)
	u.ceDropAt(8000, false) // dirty
	u.ceClose()             // must NOT end the session - confirm modal instead
	c.mu.Lock()
	active, hasRce := c.active, c.rce != nil
	c.mu.Unlock()
	if !active || !hasRce {
		t.Fatal("dirty close must keep the session (confirm-discard pending)")
	}
	u.ceDropAt(8000, true) // back to clean
	u.ceClose()            // clean close ends the session
	c.mu.Lock()
	active, hasRce = c.active, c.rce != nil
	c.mu.Unlock()
	if active || hasRce {
		t.Fatal("clean close must end the remote session")
	}
}

func TestRceNavAndMassApplyGated(t *testing.T) {
	u, c := rceTestUI(t)
	u.ceKey("down") // rce: track nav is a local-collection concept - must be inert
	u.ceKey("up")
	u.ceApplySelected(false)
	c.mu.Lock()
	active, path := c.active, c.path
	c.mu.Unlock()
	if !active || path != `C:\cache\track.mp3` {
		t.Fatalf("nav/mass-apply must be inert in rce mode: active=%v path=%q", active, path)
	}
	time.Sleep(50 * time.Millisecond) // surface stray bg persists (nil svc.Lib would panic)
}
