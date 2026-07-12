package webui

// Local-first remote cue editing (#89): "Prepare cues" on a mirrored peer track is NOT
// forwarded - the controller pulls the audio bytes into internal/remotecache, binds the
// normal cue editor (library_cueedit.go) to the CACHED copy, so waveform/peaks/audition all
// run locally, and holds an rce context (peer + remote path + StateSHA baseline). Editor
// mutations stay in ceSt (never local libdb/file tags - see the c.rce branches in
// library_cueedit.go); Save ships them back over remotectl library.writeCueData with
// optimistic concurrency (Conflict → overwrite / re-fetch / cancel).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/remotecache"
	"rave.page/mate/internal/remotectl"
)

// ceRemote is the remote-edit context while the cue editor is bound to a cached copy of a
// peer's track. Guarded by ceSt.mu.
type ceRemote struct {
	peer       string // peer nodeID
	peerName   string
	remotePath string // track path on the PEER's filesystem
	baseSHA    string // optimistic-concurrency baseline (TrackDetail.StateSHA)
	peerMoved  bool   // peer's cue state changed since fetch/save - Save will conflict
	savedOnce  bool   // ≥1 successful save (gates the peer DJ-software write-back rail)
	targets    []remotectl.CueTarget
}

// remote reports rce mode. Caller holds c.mu.
func (c *ceSt) remote() bool { return c.rce != nil }

// rceDirtyLocked: local state differs from the last known peer baseline. Caller holds c.mu.
func (c *ceSt) rceDirtyLocked() bool {
	return c.rce != nil && remotectl.CueStateSHA(c.track.Cues, c.track.Beatgrid, c.drops) != c.rce.baseSHA
}

// rceActive reports whether THIS UI is in a remote cue-edit session.
func (u *UI) rceActive() bool {
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active && c.rce != nil
}

func (u *UI) selfNodeID() string {
	if u.svc.Identity != nil {
		return u.svc.Identity.NodeID
	}
	return ""
}

// rceInterceptOpen returns the track path when payload (the mirror's forwarded act JSON) is
// `ce-open:<path>` - the ONE act the controller handles locally. Set flows
// (ce-open-pl:/ce-open-dir) still forward and run on the peer (P3).
func rceInterceptOpen(payload string) (string, bool) {
	var m struct {
		Act string `json:"act"`
	}
	if json.Unmarshal([]byte(payload), &m) != nil {
		return "", false
	}
	if p, ok := strings.CutPrefix(m.Act, "ce-open:"); ok && p != "" {
		return p, true
	}
	return "", false
}

// ── cache (process-wide; the config data dir is shared by every UI) ──

var (
	rceCacheMu sync.Mutex
	rceCache   *remotecache.Cache
)

// rceCacheStore returns the process-wide cache, applying the configured byte cap
// (Settings → LAN peers; 0 = remotecache.DefaultCap) on every access - a cap edit in one
// UI reaches the shared instance without a restart.
func (u *UI) rceCacheStore() *remotecache.Cache {
	var capBytes int64
	if u.svc.Cfg != nil {
		capBytes = u.svc.Cfg.Features.Peers.RemoteCacheBytes()
	}
	rceCacheMu.Lock()
	defer rceCacheMu.Unlock()
	if rceCache == nil {
		dir, err := config.DataPath("remote_cache")
		if err != nil {
			return nil
		}
		rceCache = remotecache.New(dir, capBytes)
		return rceCache
	}
	rceCache.SetCap(capBytes) // memory-only; eviction runs on next Commit / explicit EvictNow
	return rceCache
}

// ── fetch flow ──

// rceChunk is the pull chunk size (the server clamps to the same 8 MiB).
const rceChunk = 8 << 20

func (u *UI) rcePeerName(nodeID string) string {
	for _, p := range u.controllablePeers() {
		if p.NodeID == nodeID {
			return p.Name
		}
	}
	return nodeID
}

// rceArmPull cancels any in-flight pull and arms a fresh cancellable ctx (rce-cancel fires it).
func (u *UI) rceArmPull() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	u.rceMu.Lock()
	if u.rcePull != nil {
		u.rcePull()
	}
	u.rcePull = cancel
	u.rceMu.Unlock()
	return ctx
}

func (u *UI) rceCancelPull() {
	u.rceMu.Lock()
	if u.rcePull != nil {
		u.rcePull()
		u.rcePull = nil
	}
	u.rceMu.Unlock()
}

// rceOpen launches the local-first flow for a peer track: fetch detail + audio (cache-aware,
// progress modal), then bind the local editor. Runs the network off the act worker.
func (u *UI) rceOpen(peer, remotePath string) {
	client := u.remoteClient(peer)
	if client == nil {
		u.toast(i18n.T("library.mirror.noLink"))
		return
	}
	ctx := u.rceArmPull()
	u.openModal(modal(i18n.T("library.rce.fetchTitle"),
		`<div id=rce-prog>`+progressBar(0, i18n.T("library.rce.connecting"))+`</div>`,
		btn(i18n.T("common.cancel"), "outline", "rce-cancel", "")))
	u.bg(func() { u.rceFetch(ctx, client, peer, remotePath) })
}

// rceFetch: trackDetail → cache lookup / chunked pull → ceEnterRemote. Any error closes the
// modal with a toast (cancel closes silently).
func (u *UI) rceFetch(ctx context.Context, client *remotectl.Client, peer, remotePath string) {
	fail := func(err error) {
		u.closeModal()
		if ctx.Err() == nil { // user cancel already closed the modal - stay quiet
			u.toast(i18n.T("library.rce.fetchFailed") + err.Error())
		}
	}
	detail, err := rceCall(ctx, func(c2 context.Context) (remotectl.TrackDetail, error) {
		return client.LibraryTrackDetail(c2, remotePath)
	})
	if err != nil {
		fail(err)
		return
	}
	if _, gerr := cuepattern.NewGrid(detail.Track.Beatgrid, detail.Track.DurationSec*1000); gerr != nil {
		u.closeModal()
		u.toast(i18n.T("library.ce.noGrid"))
		return
	}
	cache := u.rceCacheStore()
	if cache == nil {
		fail(errors.New(i18n.T("library.rce.cacheFail")))
		return
	}
	local, ok := cache.Lookup(peer, remotePath, detail.MTimeUnix)
	if !ok {
		local, err = u.rcePullFile(ctx, client, cache, peer, remotePath, &detail)
		if err != nil {
			fail(err)
			return
		}
	}
	u.ceEnterRemote(peer, u.rcePeerName(peer), remotePath, detail, local)
	u.closeModal()
	// peer DJ-software write-back targets, best-effort off the open path
	u.bg(func() {
		targets, terr := rceCall(context.Background(), client.CueWriteTargets)
		if terr != nil {
			return // rail simply omits the peer write-back section
		}
		c := u.ce()
		c.mu.Lock()
		if r := c.rce; r != nil && r.remotePath == remotePath {
			r.targets = targets
		}
		c.mu.Unlock()
		u.cePatchRail()
	})
}

// rceCall runs one client RPC under the default per-call timeout, honoring the pull ctx.
func rceCall[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	c2, cancel := context.WithTimeout(ctx, remotectl.DefaultCallTimeout)
	defer cancel()
	return fn(c2)
}

// rcePullFile replicates the peer's audio file into the cache via sequential fileChunk reads.
// Bytes are copied VERBATIM - never transcode the pull: the cached copy keeps the peer file's
// exact codec, so CodecLeadSkipMs and every ms-domain cue/grid/drop value line up 1:1 with the
// peer's own playback (#89 lead-skip correctness).
// Completion is offset >= Total, NOT the EOF flag - Go's ReadAt returns a nil error on an
// exact-boundary read, so the last full-size chunk can arrive without EOF. A MTimeUnix change
// mid-pull means the peer file was rewritten: restart once (fresh detail), then give up.
func (u *UI) rcePullFile(ctx context.Context, client *remotectl.Client, cache *remotecache.Cache,
	peer, remotePath string, detail *remotectl.TrackDetail) (string, error) {
	name := baseNameAny(remotePath)
	progress := func(done, total int64) {
		frac := 0.0
		if total > 0 {
			frac = float64(done) / float64(total)
		}
		cap := fmt.Sprintf("%s · %s / %s", name, humanBytes(uint64(done)), humanBytes(uint64(total)))
		u.eval("window.__patch('rce-prog'," + jsQuote(progressBar(frac, cap)) + ")")
	}
	w, err := cache.Writer(peer, remotePath, detail.MTimeUnix)
	if err != nil {
		return "", err
	}
	var off, total int64 = 0, detail.SizeBytes
	restarted := false
	for {
		if ctx.Err() != nil {
			w.Abort()
			return "", ctx.Err()
		}
		r, cerr := rceCall(ctx, func(c2 context.Context) (remotectl.FileChunkResult, error) {
			return client.LibraryFileChunk(c2, remotePath, off, rceChunk)
		})
		if cerr != nil {
			w.Abort()
			return "", cerr
		}
		if r.Total > 0 {
			total = r.Total
		}
		if r.MTimeUnix != detail.MTimeUnix {
			// peer file changed under the pull → the bytes so far are torn; restart once
			w.Abort()
			if restarted {
				return "", errors.New(i18n.T("library.rce.fileChanged"))
			}
			restarted = true
			d2, derr := rceCall(ctx, func(c2 context.Context) (remotectl.TrackDetail, error) {
				return client.LibraryTrackDetail(c2, remotePath)
			})
			if derr != nil {
				return "", derr
			}
			*detail = d2
			if w, err = cache.Writer(peer, remotePath, detail.MTimeUnix); err != nil {
				return "", err
			}
			off, total = 0, detail.SizeBytes
			continue
		}
		data, derr := base64.StdEncoding.DecodeString(r.DataBase64)
		if derr != nil {
			w.Abort()
			return "", derr
		}
		if len(data) == 0 && !r.EOF {
			w.Abort()
			return "", errors.New("empty chunk before EOF")
		}
		if _, werr := w.Write(data); werr != nil {
			w.Abort()
			return "", werr
		}
		off += int64(len(data))
		progress(off, total)
		if off >= total && total > 0 {
			break
		}
		if r.EOF { // file shorter than announced (shrank without an mtime tick) - torn
			w.Abort()
			return "", errors.New(i18n.T("library.rce.fileChanged"))
		}
	}
	return w.Commit()
}

// baseNameAny is filepath.Base tolerant of the peer's separators (its OS may differ).
func baseNameAny(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// ── session lifecycle ──

// ceEnterRemote binds the cue editor to the cached copy of a peer track: same machinery as
// ceEnter (waveform/peaks/audition run locally through mpEnsureFile), plus the rce context.
// Never touches libSection or the local collection selection - the local library is not involved.
func (u *UI) ceEnterRemote(peer, peerName, remotePath string, detail remotectl.TrackDetail, cachedPath string) {
	tr := detail.Track
	grid, err := cuepattern.NewGrid(tr.Beatgrid, tr.DurationSec*1000)
	if err != nil {
		u.toast(i18n.T("library.ce.noGrid"))
		return
	}
	drops := append([]float64(nil), detail.Drops...)
	cursor := grid.SnapMs(0)
	if len(drops) > 0 {
		cursor = drops[0]
	}
	c := u.ce()
	c.mu.Lock()
	c.selMs, c.dselMs = map[int64]bool{}, map[int64]bool{}
	c.undo, c.undoShiftAt = nil, time.Time{}
	c.active, c.path, c.track, c.grid = true, cachedPath, tr, grid
	c.drops, c.cursorMs = drops, cursor
	c.dragA, c.dragB = -1, -1
	if c.assign == nil {
		c.assign = map[int]string{}
	}
	c.report, c.lastErr = nil, ""
	c.wbApplied, c.wbBusy, c.wbErr = nil, false, ""
	c.fileTag = false // NEVER tag-write the cached copy - bytes must stay verbatim
	c.rce = &ceRemote{peer: peer, peerName: peerName, remotePath: remotePath, baseSHA: detail.StateSHA}
	c.syncSel()
	c.mu.Unlock()
	u.mirrorShutdown() // the rce surface replaces the mirror; a fresh session opens on return
	u.mpEnsureFile("library", cachedPath, tr)
	if pl := u.player(); pl != nil {
		if st := pl.State(); st.Playing && st.Path != cachedPath {
			u.mpAudCall("library", "stop", func() { pl.Stop() })
		}
	}
	u.patchMain()
}

// rceEnd leaves the remote session (saved or explicitly discarded): unbind the editor and stop
// cached-copy audio - the mirror that returns has no local transport to reach it.
func (u *UI) rceEnd() {
	c := u.ce()
	c.mu.Lock()
	path := c.path
	c.rce, c.active = nil, false
	c.mu.Unlock()
	if pl := u.player(); pl != nil {
		if st := pl.State(); st.Playing && st.Path == path {
			u.mpStop("library")
		}
	}
	u.patchMain()
}

// rceGuardTarget gates a control-target switch on unsaved remote edits. True = handled here
// (confirm modal shown); false = caller proceeds (any clean rce session is ended first).
func (u *UI) rceGuardTarget(target string) bool {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil || !c.active {
		c.mu.Unlock()
		return false
	}
	dirty := c.rceDirtyLocked()
	name := r.peerName
	c.mu.Unlock()
	if dirty {
		u.rceConfirmDiscard(name, "rce-discard-target:"+target)
		return true
	}
	u.rceEnd()
	return false
}

// rceConfirmDiscard opens the discard-unsaved-edits confirm; confirmAct proceeds.
func (u *UI) rceConfirmDiscard(peerName, confirmAct string) {
	body := `<p class=page-sub>` + esc(i18n.T("library.rce.discardBody", i18n.A{"name": peerName})) + `</p>`
	footer := btnRow(
		btn(i18n.T("library.rce.discard"), "destructive", confirmAct, ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
	u.openModal(modal(i18n.T("library.rce.discardTitle"), body, footer))
}

// rceTrackChanged flags "peer state moved" when the mesh reports a cue-data change on the
// track being remote-edited. Our own save echoes back as Origin "peer:<ourNodeID>" - skip it
// (the save response already rebased). Local edits are never clobbered; Save conflicts anyway.
func (u *UI) rceTrackChanged(e libdb.TrackChangedEvent) {
	if self := u.selfNodeID(); self != "" && e.Origin == "peer:"+self {
		return
	}
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil || !c.active || r.remotePath != e.Path || r.peerMoved {
		c.mu.Unlock()
		return
	}
	r.peerMoved = true
	c.mu.Unlock()
	u.toast(i18n.T("library.rce.peerMovedToast"))
	u.cePatchRail()
}

// ── save to peer ──

// rceSave ships the editor state to the peer (library.writeCueData). force=true overrides a
// stale BaseSHA (conflict dialog's Overwrite).
func (u *UI) rceSave(force bool) {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil || !c.active || c.wbBusy {
		c.mu.Unlock()
		return
	}
	peer, name, remotePath, base := r.peer, r.peerName, r.remotePath, r.baseSHA
	cues := append([]musiclib.CuePoint(nil), c.track.Cues...)
	grid := append([]musiclib.GridMarker(nil), c.track.Beatgrid...)
	drops := append([]float64(nil), c.drops...)
	c.wbBusy, c.wbErr = true, ""
	c.mu.Unlock()
	u.cePatchRail()
	u.bg(func() {
		client := u.remoteClient(peer)
		var res remotectl.WriteCueDataResult
		err := errors.New(i18n.T("library.mirror.noLink"))
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
			res, err = client.WriteCueData(ctx, remotectl.WriteCueDataParams{
				Path: remotePath, Cues: cues, Beatgrid: grid,
				Drops: drops, DropsSet: true, BaseSHA: base, Force: force})
			cancel()
		}
		c.mu.Lock()
		c.wbBusy = false
		cur := c.rce
		if cur == nil || cur.remotePath != remotePath { // session moved on mid-flight
			c.mu.Unlock()
			return
		}
		switch {
		case err != nil: // transport error: keep dirty, surface + Retry via the rail
			c.wbErr = err.Error()
			c.mu.Unlock()
			u.toast(i18n.T("library.rce.saveFailed") + err.Error())
		case res.Conflict: // no write happened; rebase choice is the user's
			cur.peerMoved = true
			c.mu.Unlock()
			u.rceConflictModal(name)
		default:
			cur.baseSHA = res.Detail.StateSHA
			cur.savedOnce, cur.peerMoved = true, false
			c.wbApplied = nil // peer cue data changed - earlier peer-software writes are stale
			c.mu.Unlock()
			u.toast(i18n.T("library.rce.savedToast", i18n.A{"name": name}))
		}
		u.cePatchRail()
	})
}

// rceConflictModal: the peer's state moved under the edit - Overwrite / Re-fetch / Cancel.
func (u *UI) rceConflictModal(peerName string) {
	body := `<p class=page-sub>` + esc(i18n.T("library.rce.conflictBody", i18n.A{"name": peerName})) + `</p>`
	footer := btnRow(
		btn(i18n.T("library.rce.overwrite"), "destructive", "rce-save-force", ""),
		btn(i18n.T("library.rce.refetch"), "outline", "rce-reload-force", ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
	u.openModal(modal(i18n.T("library.rce.conflictTitle"), body, footer))
}

// rceReload re-runs the fetch flow for the current session (discards local edits; cache makes
// an unchanged file instant).
func (u *UI) rceReload() {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil {
		c.mu.Unlock()
		return
	}
	peer, path := r.peer, r.remotePath
	c.mu.Unlock()
	u.rceOpen(peer, path)
}

// ── peer-side DJ-software write-back (after a successful save) ──

func (u *UI) rceWriteTo(key string) {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil || !c.active || c.wbBusy || !r.savedOnce || c.rceDirtyLocked() {
		c.mu.Unlock()
		return
	}
	if _, done := c.wbApplied[key]; done {
		c.mu.Unlock()
		return
	}
	peer, remotePath := r.peer, r.remotePath
	label := key
	for _, t := range r.targets {
		if t.Key == key {
			label = t.Label
		}
	}
	c.wbBusy, c.wbErr = true, ""
	c.mu.Unlock()
	u.cePatchRail()
	u.bg(func() {
		client := u.remoteClient(peer)
		var res remotectl.WriteResult
		err := errors.New(i18n.T("library.mirror.noLink"))
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
			res, err = client.WriteCuesTo(ctx, key, []string{remotePath})
			cancel()
		}
		c.mu.Lock()
		c.wbBusy = false
		if err != nil {
			c.wbErr = err.Error()
		} else {
			if c.wbApplied == nil {
				c.wbApplied = map[string]int{}
			}
			c.wbApplied[key] = res.Written
		}
		c.mu.Unlock()
		if err != nil {
			u.toast(i18n.T("library.ce.writeFailed") + err.Error())
		} else {
			u.toast(i18n.T("library.ce.wroteHint", i18n.A{"app": label, "n": fmt.Sprint(res.Written)}))
		}
		u.cePatchRail()
	})
}

// ── rendering ──

// rceBody is the Library body while remote-editing: full-width local waveform + the editor
// rail (same ids as the local layout, so cePatchWave/cePatchRail keep working).
func (u *UI) rceBody() string {
	s := u.lib()
	s.mu.Lock()
	detail := u.libDetailWrap(s)
	s.mu.Unlock()
	return `<div class=ce-fullwave>` + u.ceWaveHTML() + `</div>` +
		masterDetailWide(`<div id=rce-info>`+u.rceInfoHTML()+`</div>`, detail)
}

// rceInfoHTML is the left pane: whose track is being edited, where audio runs, session status.
func (u *UI) rceInfoHTML() string {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil {
		c.mu.Unlock()
		return ""
	}
	title := trackTitle(c.track)
	name, remotePath := r.peerName, r.remotePath
	dirty, moved := c.rceDirtyLocked(), r.peerMoved
	c.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<div class=rp-card>`)
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.rce.eyebrow", i18n.A{"name": name})) +
		`</div><div class=insp-title>` + esc(title) + `</div><div class=insp-sub>` + esc(remotePath) + `</div></div>`)
	b.WriteString(`<p class=page-sub>` + esc(i18n.T("library.rce.localNote", i18n.A{"name": name})) + `</p>`)
	if moved {
		b.WriteString(hint("warn", i18n.T("library.rce.peerMoved", i18n.A{"name": name})))
	}
	if dirty {
		b.WriteString(hint("warn", i18n.T("library.rce.unsaved")))
	} else {
		b.WriteString(hint("ok", i18n.T("library.rce.clean")))
	}
	b.WriteString(btnRow(btn(i18n.T("library.rce.back"), "outline", "ce-close", "")))
	b.WriteString(`</div>`)
	return b.String()
}

// rceSaveHTML is the save/write-back section of the editor rail in rce mode (replaces the
// local DJ-software router, library_cuewrite.go). Locks ceSt itself - build it before
// ceRailHTML locks c (same contract as ceWriteHTML).
func (u *UI) rceSaveHTML() string {
	c := u.ce()
	c.mu.Lock()
	r := c.rce
	if r == nil || !c.active {
		c.mu.Unlock()
		return ""
	}
	dirty := c.rceDirtyLocked()
	busy, errStr := c.wbBusy, c.wbErr
	applied := make(map[string]int, len(c.wbApplied))
	for k, v := range c.wbApplied {
		applied[k] = v
	}
	name := r.peerName
	moved, savedOnce := r.peerMoved, r.savedOnce
	targets := append([]remotectl.CueTarget(nil), r.targets...)
	c.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.rce.saveHeader", i18n.A{"name": name})) + `</div>`)
	if moved {
		b.WriteString(hint("warn", i18n.T("library.rce.peerMoved", i18n.A{"name": name})))
		b.WriteString(btnRow(btn(i18n.T("library.rce.reload"), "outline", "rce-reload", "")))
	}
	if errStr != "" {
		b.WriteString(hint("bad", i18n.T("library.rce.saveFailed")+errStr))
	}
	switch {
	case busy:
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.rce.saving")) + `</div>`)
	case dirty:
		label := i18n.T("library.rce.save", i18n.A{"name": name})
		if errStr != "" {
			label = i18n.T("library.rce.retry")
		}
		b.WriteString(hint("warn", i18n.T("library.rce.unsaved")))
		b.WriteString(`<div class=btn-col>` + btn(label, "primary", "rce-save", "") + `</div>`)
	case savedOnce:
		b.WriteString(hint("ok", i18n.T("library.rce.saved")))
	default:
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.rce.clean")) + `</div>`)
	}
	// peer-side DJ-software write-back: only meaningful once the peer holds the saved state
	if savedOnce && len(targets) > 0 {
		b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.rce.writeHeader", i18n.A{"name": name})) + `</div>`)
		for _, t := range targets {
			if n, ok := applied[t.Key]; ok {
				b.WriteString(hint("ok", i18n.T("library.ce.wroteHint", i18n.A{"app": t.Label, "n": fmt.Sprint(n)})))
				continue
			}
			label := i18n.T("library.ce.writeTo", i18n.A{"app": t.Label, "n": "1"})
			if dirty || busy {
				b.WriteString(btnRow(btnGated(label, i18n.T("library.rce.writeGate"))))
			} else {
				b.WriteString(btnRow(btn(label, "outline", "rce-write:"+t.Key, "")))
			}
		}
	}
	return b.String()
}

// ── actions ──

func init() {
	onExact("rce-cancel", func(u *UI, _ actMsg) {
		u.rceCancelPull()
		u.closeModal()
	})
	onExact("rce-save", func(u *UI, _ actMsg) { u.rceSave(false) })
	onExact("rce-save-force", func(u *UI, _ actMsg) {
		u.closeModal()
		u.rceSave(true)
	})
	onExact("rce-discard", func(u *UI, _ actMsg) {
		u.closeModal()
		u.rceEnd()
	})
	onPrefix("rce-discard-target:", func(u *UI, m actMsg) {
		u.closeModal()
		u.rceEnd()
		u.libSetTarget(m.arg("rce-discard-target:"))
	})
	onExact("rce-reload", func(u *UI, _ actMsg) { // rail button: confirm, then re-fetch
		c := u.ce()
		c.mu.Lock()
		name := ""
		if c.rce != nil {
			name = c.rce.peerName
		}
		c.mu.Unlock()
		if name == "" {
			return
		}
		body := `<p class=page-sub>` + esc(i18n.T("library.rce.reloadBody", i18n.A{"name": name})) + `</p>`
		footer := btnRow(
			btn(i18n.T("library.rce.reloadConfirm"), "destructive", "rce-reload-force", ""),
			btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
		u.openModal(modal(i18n.T("library.rce.reloadTitle"), body, footer))
	})
	onExact("rce-reload-force", func(u *UI, _ actMsg) {
		u.closeModal()
		u.rceReload()
	})
	onPrefix("rce-write:", func(u *UI, m actMsg) { u.rceWriteTo(m.arg("rce-write:")) })
}
