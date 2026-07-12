package webui

// Passive library refresh: every UI (window + headless mirrors) subscribes to
// libdb.TopicTrackChanged so cue-data edits by another UI or a peer's writeCueData RPC
// show up without a manual reload. The handler only patches THIS UI's libSt + repaints
// visible library fragments - it NEVER calls setTab and never moves the selection.

import (
	"encoding/json"
	"fmt"
	"time"

	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// libWatchOrigin is this UI's publisher token: its own events are skipped (state is
// already patched at the edit site - reprocessing would toast the editor at itself).
func (u *UI) libWatchOrigin() string { return fmt.Sprintf("ui:%p", u) }

// libNotifyTrackChanged publishes path's cue-data change on the mesh bus (local fan-out
// + peer broadcast). Call AFTER the DB write landed so subscribers re-read fresh rows.
func (u *UI) libNotifyTrackChanged(path string) {
	if u.svc.EventBus == nil || path == "" {
		return
	}
	data, _ := json.Marshal(libdb.TrackChangedEvent{Path: path, Origin: u.libWatchOrigin()})
	u.svc.EventBus.Publish(libdb.TopicTrackChanged, data)
}

// libWatchStart subscribes this UI to trackchanged events. Called from onReady (window)
// and newHeadlessUI (mirrors patch too - cheap); Stop() runs the stored unsubscribe.
func (u *UI) libWatchStart() {
	if u.svc.EventBus == nil {
		return
	}
	unsub := u.svc.EventBus.Subscribe(libdb.TopicTrackChanged, func(ev eventbus.Event) {
		var e libdb.TrackChangedEvent
		if json.Unmarshal(ev.Data, &e) != nil || e.Path == "" || e.Origin == u.libWatchOrigin() {
			return
		}
		u.bg(func() { u.libWatchApply(e.Path) }) // fn runs on the publisher's goroutine - hand off
	})
	u.mu.Lock()
	u.libWatchStop = unsub
	u.mu.Unlock()
}

// libWatchApply re-reads path from libdb and patches this UI's collection state; repaints
// only when the library tab is showing. No tab switch, no selection change.
func (u *UI) libWatchApply(path string) {
	if u.svc.Lib == nil {
		return
	}
	tr, ok, err := u.svc.Lib.TrackByPath(path)
	if err != nil || !ok {
		return
	}
	drops, _ := u.svc.Lib.Drops(path)
	s := u.lib()
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock() // not hydrated - the eventual load reads fresh rows anyway
		return
	}
	if _, has := s.byPath[path]; !has {
		s.mu.Unlock()
		return
	}
	s.byPath[path] = tr
	for i := range s.tracks {
		if s.tracks[i].Path == path {
			s.tracks[i] = tr
		}
	}
	if s.dropsIdx == nil {
		s.dropsIdx = map[string][]float64{}
	}
	if len(drops) == 0 {
		delete(s.dropsIdx, path)
	} else {
		s.dropsIdx[path] = drops
	}
	if s.sel != nil && s.sel.path == path {
		s.sel.track = tr // refresh the copy; which row is selected stays untouched
	}
	s.mu.Unlock()
	u.ceWatchReload(path, tr, drops)
	if u.activeTab() == "library" {
		u.libPatchBody()
		u.libPatchDetail()
	}
}

// ceWatchReload re-arms the OPEN cue editor when its track changed underneath it (peer
// edit): swap track/grid/drops, keep cursor + ms-keyed selection, drop stale write-back
// state, toast.
func (u *UI) ceWatchReload(path string, tr musiclib.Track, drops []float64) {
	c := u.ce()
	c.mu.Lock()
	if !c.active || c.path != path {
		c.mu.Unlock()
		return
	}
	c.track = tr
	c.drops = append([]float64(nil), drops...)
	c.wbApplied, c.wbErr = nil, ""           // cue data moved - earlier software writes are stale
	c.undo, c.undoShiftAt = nil, time.Time{} // snapshot predates the remote edit - stale
	if g, err := cuepattern.NewGrid(tr.Beatgrid, tr.DurationSec*1000); err == nil {
		c.grid = g
		c.cursorMs = g.SnapMs(c.cursorMs)
	}
	c.syncSel()
	c.mu.Unlock()
	u.toast(i18n.T("library.ce.remoteReloaded"))
	u.cePatchWave()
	u.cePatchRail()
}
