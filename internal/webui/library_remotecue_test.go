package webui

// rce-mode state-machine tests (#89 + #90 sets): mirror-act interception + the
// persistence-hook branching - in a remote session every editor mutation must stay in ceSt
// (svc.Lib is nil here, so any local persist path would nil-deref) and never arm the tag/DB
// write-behind - plus set-session nav (dirty guard, bounds), gridless scan filtering and
// the next-track-ready flag.

import (
	"context"
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/ui"
)

func TestRceIntercept(t *testing.T) {
	for _, tc := range []struct {
		payload   string
		kind, arg string
		ok        bool
	}{
		{`{"act":"ce-open:C:\\music\\a.mp3"}`, "track", `C:\music\a.mp3`, true},
		{`{"act":"ce-open:/home/dj/a.flac","val":"x"}`, "track", "/home/dj/a.flac", true},
		{`{"act":"ce-open:"}`, "", "", false}, // empty path
		{`{"act":"ce-open-pl:12"}`, "pl", "12", true},
		{`{"act":"ce-open-pl:"}`, "", "", false},
		{`{"act":"ce-open-dir:/mnt/music"}`, "dir", "/mnt/music", true},
		{`{"act":"menugo:ce-open-dir:D:\\crates"}`, "dir", `D:\crates`, true}, // actionMenu wrap
		{`{"act":"ce-open-dir"}`, "", "", false},                              // old peer render: forwards, runs peer-side
		{`{"act":"mp-surf:down:0.5"}`, "", "", false},                         // unrelated act
		{`not json`, "", "", false},
	} {
		kind, arg, ok := rceIntercept(tc.payload)
		if ok != tc.ok || kind != tc.kind || arg != tc.arg {
			t.Errorf("rceIntercept(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.payload, kind, arg, ok, tc.kind, tc.arg, tc.ok)
		}
	}
}

// rceTestUI: headless UI (no window, no Lib) with an rce cue-edit session seeded.
func rceTestUI(t *testing.T) (*UI, *ceSt, *capture) {
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
	return u, c, cap
}

// rceSeedSet attaches a set context (pos = index of the open track among paths).
func rceSeedSet(c *ceSt, pos int, paths ...string) {
	c.mu.Lock()
	c.rce.set = &rceSet{label: "Peer Crate", paths: paths, pos: pos}
	c.mu.Unlock()
}

func setSnap(c *ceSt) (pos int, nextOK bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rce.set.pos, c.rce.set.nextOK
}

func (c *ceSt) snap(t *testing.T) (dirty bool, cues, drops int, tagT, dbT bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rceDirtyLocked(), len(c.track.Cues), len(c.drops), c.tagTimer != nil, c.dbTimer != nil
}

func TestRceMutationsStayLocal(t *testing.T) {
	u, c, _ := rceTestUI(t)

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
	u, c, _ := rceTestUI(t)
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
	u, c, _ := rceTestUI(t)
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
	u, c, _ := rceTestUI(t)
	u.ceKey("down") // single-track rce (no set): nav must be inert
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

func TestRceSetNavDirtyGuardAndBounds(t *testing.T) {
	u, c, cap := rceTestUI(t)
	rceSeedSet(c, 1, `P:\peer\a.mp3`, `P:\peer\track.mp3`, `P:\peer\c.mp3`)

	// bounds: nav past either end is a silent no-op
	u.rceNavSet(2)
	u.rceNavSet(-2)
	if pos, _ := setSnap(c); pos != 1 {
		t.Fatalf("OOB nav moved pos: %d", pos)
	}

	// dirty: nav must confirm-discard (targeting the next index), not move
	u.ceDropAt(8000, false)
	u.rceNavSet(1)
	if pos, _ := setSnap(c); pos != 1 {
		t.Fatalf("dirty nav moved pos: %d", pos)
	}
	c.mu.Lock()
	active, hasRce := c.active, c.rce != nil
	c.mu.Unlock()
	if !active || !hasRce {
		t.Fatal("dirty nav must keep the session")
	}
	cap.waitEval(t, "rce-set-go:2")   // confirm-discard modal targets the next index
	time.Sleep(50 * time.Millisecond) // surface stray bg persists (nil svc.Lib would panic)
}

func TestRceKeyNavRoutesToSet(t *testing.T) {
	u, c, cap := rceTestUI(t)
	rceSeedSet(c, 0, `P:\peer\track.mp3`, `P:\peer\b.mp3`)
	u.ceDropAt(8000, false) // dirty → key nav must raise the guard, proving the set route
	u.ceKey("down")
	cap.waitEval(t, "rce-set-go:1") // ↓ routed to set nav (dirty guard modal)
	u.ceKey("up")                   // pos 0: silent bounds no-op
	if pos, _ := setSnap(c); pos != 0 {
		t.Fatalf("↑ at set start moved pos: %d", pos)
	}
}

func TestRceMarkNextReady(t *testing.T) {
	u, c, _ := rceTestUI(t)
	rceSeedSet(c, 0, `P:\peer\track.mp3`, `P:\peer\b.mp3`)
	u.rceMarkNextReady(1, `P:\peer\b.mp3`) // wrong pos
	if _, ok := setSnap(c); ok {
		t.Fatal("wrong pos must not flag next-ready")
	}
	u.rceMarkNextReady(0, `P:\peer\zzz.mp3`) // wrong path (set changed under the prefetch)
	if _, ok := setSnap(c); ok {
		t.Fatal("wrong path must not flag next-ready")
	}
	u.rceMarkNextReady(0, `P:\peer\b.mp3`)
	if _, ok := setSnap(c); !ok {
		t.Fatal("matching prefetch must flag next-ready")
	}
}

func TestRceScanSet(t *testing.T) {
	grid := []musiclib.GridMarker{{PositionMs: 0, BPM: 128}}
	mk := func(withGrid bool) remotectl.TrackDetail {
		d := remotectl.TrackDetail{}
		d.Track.DurationSec = 120
		if withGrid {
			d.Track.Beatgrid = grid
		}
		return d
	}
	fetch := func(p string) (remotectl.TrackDetail, error) {
		switch p {
		case "b":
			return remotectl.TrackDetail{}, errors.New("not in collection")
		case "c":
			return mk(false), nil // gridless
		default:
			return mk(true), nil
		}
	}
	eligible, first, skipped, err := rceScanSet(context.Background(), []string{"a", "b", "c", "d"}, fetch, nil)
	if err != nil || skipped != 2 || len(eligible) != 2 || eligible[0] != "a" || eligible[1] != "d" {
		t.Fatalf("scan: eligible=%v skipped=%d err=%v", eligible, skipped, err)
	}
	if len(first.Track.Beatgrid) != 1 {
		t.Fatal("first must carry the first eligible track's detail")
	}

	// canceled ctx aborts
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := rceScanSet(ctx, []string{"a"}, fetch, nil); err == nil {
		t.Fatal("canceled ctx must abort the scan")
	}

	// a dead link (call timeout) aborts instead of burning a timeout per remaining path
	dead := func(string) (remotectl.TrackDetail, error) { return remotectl.TrackDetail{}, context.DeadlineExceeded }
	if _, _, _, err := rceScanSet(context.Background(), []string{"a", "b"}, dead, nil); err == nil {
		t.Fatal("call timeout must abort the scan")
	}
}
