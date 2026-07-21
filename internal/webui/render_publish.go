package webui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/store"
)

// renderPublish is the recording/publishing cockpit at parity with the Fyne Publish tab
// (view_recorder.go): REC/CAPTURE/OBS hero badges, now-playing meta + confirm countdown,
// a Sets ↔ Captures/Tracklist master-detail, and Export / Match-history / Delete flows.
// Playback + trim/edit run in the unified media player (player.go) embedded in the
// captures pane - audio, video (mpv) and aligned audio+video pairs alike. Live tick
// patches pub-hero.
func (u *UI) renderPublish() string {
	sw := u.targetSwitcherHTML("pubtarget", "pub-target:")
	// Remote: a peer is targeted → the recorded-sets browser over remotectl (local path untouched).
	if tgt := u.libRemoteTarget(); tgt != "" {
		return panel(i18n.T("publish.title"), i18n.T("publish.subtitle")) + sw +
			`<div id=publish-body>` + u.pubRemoteBody(tgt) + `</div>`
	}
	if u.svc.Recorder == nil {
		return panel(i18n.T("publish.title"), "") + sw + emptyState(i18n.T("publish.recorderUnavailable"))
	}
	return panel(i18n.T("publish.title"), i18n.T("publish.subtitle")) + sw +
		`<div id=publish-body>` + u.publishBody() + `</div>`
}

func (u *UI) publishBody() string {
	recs := u.pubRecordings()
	caps, loose := u.pubCaptures()
	sel := u.pubSelected(recs)
	return `<div id=pub-hero>` + u.pubHeroHTML() + `</div>` +
		masterDetail(u.pubListHTML(recs, sel, caps), u.pubDetailHTML(sel, caps, loose))
}

// ── hero: live badges + now-playing (tick-patched) ──────────────────────────────

func (u *UI) pubHeroHTML() string {
	if u.svc.Recorder == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card pub-hero">`)
	b.WriteString(`<div class=pub-badges>` + u.pubRecBadge() + u.pubCapBadge() + u.pubObsBadge() + `</div>`)

	// Finish-set action (only while a set is live).
	if a := u.svc.Recorder.Active(); a != nil {
		b.WriteString(btnRow(btn(i18n.T("publish.finishSet"), "destructive", "rec-finish", "")))
	}

	b.WriteString(u.pubNowPlayingHTML())
	b.WriteString(u.pubPlayerHTML())
	b.WriteString(`</div>`)
	return b.String()
}

func pubBadge(key, variant, line string) string {
	return `<div class=pub-badge>` + dot(variant) +
		`<div class=pub-badge-tx><div class=pub-badge-k data-label=` + attrQ(strings.ToLower(key)) + `>` + html.EscapeString(key) + `</div>` +
		`<div class=pub-badge-v data-value=` + attrQ(line) + `>` + html.EscapeString(line) + `</div></div></div>`
}

func (u *UI) pubRecBadge() string {
	cfg := u.svc.Cfg
	switch a := u.svc.Recorder.Active(); {
	case cfg != nil && !cfg.Features.Recorder.Enabled:
		return pubBadge("REC", "muted", i18n.T("publish.recOff"))
	case a != nil:
		dur := time.Since(a.StartedAt).Truncate(time.Second)
		return pubBadge("REC", "error", i18n.T("publish.recActive", i18n.A{"name": orSetName(a.Name), "count": fmt.Sprint(len(a.Tracks)), "dur": dur.String()}))
	default:
		return pubBadge("REC", "muted", i18n.T("publish.recArmed"))
	}
}

func (u *UI) pubCapBadge() string {
	cfg := u.svc.Cfg
	if u.svc.SetCapture == nil || (cfg != nil && !cfg.Features.SetCapture.Enabled) {
		return pubBadge("CAPTURE", "muted", i18n.T("common.off"))
	}
	st := u.svc.SetCapture.Snapshot()
	switch {
	case st.Connected:
		return pubBadge("CAPTURE", "success", i18n.T("publish.capturing", i18n.A{"format": strings.ToUpper(st.Format), "bytes": humanBytes(uint64(st.Bytes))}))
	case st.Reconnecting:
		return pubBadge("CAPTURE", "warning", i18n.T("publish.reconnecting"))
	case st.Listening:
		return pubBadge("CAPTURE", "info", i18n.T("publish.listening", i18n.A{"addr": st.Addr}))
	default:
		return pubBadge("CAPTURE", "warning", i18n.T("publish.notListening"))
	}
}

func (u *UI) pubObsBadge() string {
	cfg := u.svc.Cfg
	if u.svc.OBS == nil || (cfg != nil && !cfg.Features.OBS.Enabled) {
		return pubBadge("OBS", "muted", i18n.T("common.off"))
	}
	st := u.svc.OBS.Status()
	switch {
	case st.Recording:
		return pubBadge("OBS", "error", i18n.T("publish.obsRecording", i18n.A{"dur": time.Since(st.RecStartedAt).Truncate(time.Second).String()}))
	case st.Connected:
		return pubBadge("OBS", "success", i18n.T("publish.connected"))
	default:
		return pubBadge("OBS", "warning", i18n.T("publish.notConnected"))
	}
}

// pubNowPlayingHTML mirrors the Fyne now-playing strip: pending candidate (confirm countdown)
// or the current confirmed track, else "Nothing audible".
func (u *UI) pubNowPlayingHTML() string {
	title, meta, state, bar := i18n.T("publish.nothingAudible"), "", "", ""

	if p, ok := u.svc.Recorder.Pending(); ok {
		title = orTrackLine(pubTrackLine(p.Track))
		meta = pubTrackMeta(p.Track)
		left := time.Until(p.ConfirmAt).Truncate(time.Second)
		if left < 0 {
			left = 0
		}
		state = i18n.T("publish.confirming", i18n.A{"left": left.String()})
		if window := p.ConfirmAt.Sub(p.FirstSeen); window > 0 {
			bar = progressBar(1-float64(left)/float64(window), i18n.T("publish.confirmingIn", i18n.A{"left": left.String()}))
		}
	} else if a := u.svc.Recorder.Active(); a != nil && len(a.Tracks) > 0 {
		if t := a.Tracks[len(a.Tracks)-1]; t.EndedAt.IsZero() {
			title = orTrackLine(pubTrackLine(t))
			meta = pubTrackMeta(t)
			state = i18n.T("publish.trackInList", i18n.A{"n": fmt.Sprint(len(a.Tracks)), "dur": time.Since(t.StartedAt).Truncate(time.Second).String()})
		}
	}

	var b strings.Builder
	b.WriteString(`<div class=pub-np>`)
	b.WriteString(`<div class=card-label>` + html.EscapeString(i18n.T("publish.nowPlaying")) + `</div>`)
	b.WriteString(`<div class=pub-np-t data-label="now playing" data-value="` + html.EscapeString(title) + `">` + html.EscapeString(title) + `</div>`)
	if meta != "" {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(meta) + `</div>`)
	}
	if state != "" {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(state) + `</div>`)
	}
	if bar != "" {
		b.WriteString(`<div style="margin-top:8px">` + bar + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pubPlayerHTML shows the shared audio player's live position (tick-patched via pub-hero) so
// the transport readout stays fresh without rebuilding the detail pane's seek control.
func (u *UI) pubPlayerHTML() string {
	pl := u.player() // gated read: a remote mirror must not leak this machine's playback
	if pl == nil {
		return ""
	}
	st := pl.State()
	if !st.Playing || st.Path == "" {
		return ""
	}
	label := "▶ "
	if st.Paused {
		label = "⏸ "
	}
	label += filepath.Base(st.Path)
	pos := pubClock(st.Cur) + " / " + pubClock(st.Total)
	frac := 0.0
	if st.Total > 0 {
		frac = st.Cur / st.Total
	}
	return `<div class=pub-player><div class=pub-player-l>` + html.EscapeString(label) +
		` <span class=np-artist>` + html.EscapeString(pos) + `</span></div>` + progressBar(frac, pos) + `</div>`
}

// ── sets list ───────────────────────────────────────────────────────────────────

func (u *UI) pubListHTML(recs []recorder.Recording, sel *recorder.Recording, caps map[string][]libdb.SetRecording) string {
	if len(recs) == 0 {
		return emptyState(i18n.T("publish.noRecordings"))
	}
	var b strings.Builder
	b.WriteString(`<div class=card-label>` + html.EscapeString(i18n.T("publish.setsCount", i18n.A{"count": fmt.Sprint(len(recs))})) + `</div>`)
	for i := range recs {
		r := recs[i]
		title := orSetName(r.Name)
		if r.EndedAt.IsZero() {
			title = "⏺ " + title
		}
		cls := "irow pub-setrow"
		if sel != nil && sel.ID == r.ID {
			cls += " selected"
		}
		// Rename sits in the row's action slot: the runtime dispatches the closest [data-act], so
		// the button wins over the row's own pub-select. Ungated - Recorder.Rename handles the
		// live set too (unlike delete, which waits for the set to end).
		b.WriteString(`<div class="` + cls + `" data-act="pub-select:` + html.EscapeString(r.ID) + `">` +
			`<div class=irow-main><div class=irow-title>` + html.EscapeString(title) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(pubSetMeta(r, caps[r.ID])) + `</div></div>` +
			`<div class=irow-actions>` + btn(i18n.T("publish.renameDots"), "ghost", "pub-rename:"+r.ID, "") + `</div></div>`)
	}
	return b.String()
}

func pubSetMeta(r recorder.Recording, caps []libdb.SetRecording) string {
	parts := []string{r.StartedAt.Local().Format("2006-01-02 15:04"), i18n.Tn("track", len(r.Tracks))}
	if r.EndedAt.IsZero() {
		parts = append(parts, i18n.T("publish.live"))
	} else {
		parts = append(parts, r.EndedAt.Sub(r.StartedAt).Truncate(time.Minute).String())
	}
	for _, s := range caps {
		if s.Kind == libdb.SetKindOBS {
			parts = append(parts, i18n.T("publish.video"))
		} else {
			parts = append(parts, i18n.T("publish.audio"))
		}
	}
	if !r.ReconciledAt.IsZero() {
		parts = append(parts, i18n.T("publish.matched"))
	}
	return strings.Join(parts, " · ")
}

// ── detail: header + actions + Captures/Tracklist subtabs ───────────────────────

func (u *UI) pubDetailHTML(sel *recorder.Recording, caps map[string][]libdb.SetRecording, loose []libdb.SetRecording) string {
	if sel == nil {
		body := hint("info", i18n.T("publish.selectHint"))
		if t := u.mpSnap("publish"); t.pinned { // loose capture opened in the player
			body += u.mpHTML("publish")
		}
		return card(i18n.T("publish.selectedSet"), "", body+u.pubLooseHTML(loose))
	}
	r := *sel
	head := `<div class=pub-detail-h><div class=pub-detail-name>` + html.EscapeString(orSetName(r.Name)) + `</div>` +
		`<div class=np-artist>` + html.EscapeString(pubSetMeta(r, caps[r.ID])) + `</div>` +
		u.pubActionsHTML(r) + `</div>`

	sets := caps[r.ID]
	active := u.pubSubtab()
	// Track COUNT needs no library resolution - use it for the subtab label so switching to
	// Captures never pays the (async, DB-backed) tracklist link resolution.
	tabs := subTabs("pub-tab:", active,
		[2]string{"captures", i18n.T("publish.capturesCount", i18n.A{"count": fmt.Sprint(len(sets))})},
		[2]string{"tracklist", i18n.T("publish.tracklistCount", i18n.A{"count": fmt.Sprint(len(r.Tracks))})},
	)

	var body string
	if active == "tracklist" {
		rows, ready := u.pubTrackRows(r) // library-path resolution is off-thread + cached (see pubTrackPaths)
		body = u.pubTracklistHTML(r, len(sets) > 0, rows, !ready)
	} else {
		body = u.pubCapturesHTML(r, sets) + u.pubLooseHTML(loose)
	}
	return card(i18n.T("publish.selectedSet"), "", head+tabs+`<div class=pub-subbody>`+body+`</div>`)
}

func (u *UI) pubActionsHTML(r recorder.Recording) string {
	btns := []string{btn(i18n.T("publish.export"), "outline", "pub-export:"+r.ID, "")}
	if !r.EndedAt.IsZero() {
		btns = append(btns,
			btn(i18n.T("publish.matchHistory"), "secondary", "pub-match:"+r.ID, ""),
			btn(i18n.T("publish.delete"), "destructive", "pub-del:"+r.ID, ""))
	}
	return btnRow(btns...)
}

// pubTracklistHTML renders the tracklist. resolving = library-path links are still being
// resolved off-thread (names/times show immediately; the works-together checkboxes fill in when
// the async resolve lands and re-renders). Finished sets get editable start offsets + the
// capture-aligned "Fix start times" flow (hasCaps).
func (u *UI) pubTracklistHTML(r recorder.Recording, hasCaps bool, rows []pubRow, resolving bool) string {
	if len(rows) == 0 {
		return hint("info", i18n.T("publish.noTracks"))
	}
	editable := !r.EndedAt.IsZero()
	sel := u.pubTSel()
	var b strings.Builder
	if resolving {
		b.WriteString(hint("info", i18n.T("publish.linkingLibrary")))
	}
	b.WriteString(`<div class=pub-tracklist>`)
	unresolved := 0
	for i, row := range rows {
		lead, ctx := "", ""
		switch {
		case resolving:
			// links not resolved yet - neutral placeholder, not a "no match" mark
			lead = `<span class="pub-track-chk none" title=` + attrQ(i18n.T("publish.linkingLibrary")) + `>…</span>`
		case row.path == "":
			unresolved++
			lead = `<span class="pub-track-chk none" title=` + attrQ(i18n.T("publish.compat.unresolved")) + `>·</span>`
		default:
			chk := ""
			if sel[row.path] {
				chk = " checked"
			}
			lead = `<span class=pub-track-chk><input type=checkbox data-act="pub-tsel:` + html.EscapeString(row.path) + `"` + chk + `></span>`
			ctx = ` data-ctx="pub-tctx:` + html.EscapeString(row.path) + `"`
		}
		off := pubClock(row.offset.Seconds())
		oCell := `<span class=pub-track-o>[` + off + `]</span>`
		if editable {
			oCell = `<input class=pub-track-oin type=text value=` + attrQ(off) + ` data-value=` + attrQ(off) +
				` data-act=` + attrQ("pub-toff:"+r.ID+"\x1f"+fmt.Sprint(i)) +
				` data-label=` + attrQ("offset-"+fmt.Sprint(i+1)) + // ctl read/set target (space-free: ctl set splits on first space)
				` title=` + attrQ(i18n.T("publish.offsetEditTip")) + `>`
		}
		b.WriteString(`<div class=pub-track` + ctx + `>` + lead +
			`<span class=pub-track-n>` + fmt.Sprint(i+1) + `.</span>` +
			oCell +
			`<span class=pub-track-l>` + html.EscapeString(row.label) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if editable && hasCaps {
		b.WriteString(btnRow(btn(i18n.T("publish.fix.button"), "outline", "pub-fixtimes:"+r.ID, "")))
	}
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("publish.compat.help")) + `</p>`)
	if unresolved > 0 {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("publish.compat.unresolvedCount", i18n.A{"count": fmt.Sprint(unresolved)})) + `</p>`)
	}
	if len(sel) > 0 {
		btns := []string{}
		if len(sel) >= 2 {
			btns = append(btns, btn(i18n.T("library.compat.markBtn"), "primary", "lib-compat-mark:pub", ""))
		}
		if len(sel) == 1 {
			for p := range sel {
				btns = append(btns, btn(i18n.T("library.compat.findBtn"), "outline", "lib-compat-find:"+p, ""))
			}
		}
		btns = append(btns, btn(i18n.T("library.clear"), "ghost", "pub-tsel-clear", ""))
		b.WriteString(`<div class=batchbar><span class=cnt>` + html.EscapeString(i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(sel))})) + `</span>` +
			strings.Join(btns, "") + `</div>`)
	}
	return b.String()
}

func (u *UI) pubCapturesHTML(r recorder.Recording, sets []libdb.SetRecording) string {
	u.mpEnsureSet(r, sets)
	var b strings.Builder
	if p := u.mpHTML("publish"); p != "" { // unified player/editor (also shows a pinned loose capture)
		b.WriteString(p)
	}
	if len(sets) == 0 {
		b.WriteString(hint("info", i18n.T("publish.noCaptures")))
		return b.String()
	}
	for _, s := range sets {
		b.WriteString(u.pubCaptureBlock(s, false))
	}
	return b.String()
}

func (u *UI) pubLooseHTML(loose []libdb.SetRecording) string {
	if len(loose) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=pub-loose><div class=card-label>` + html.EscapeString(i18n.T("publish.unlinkedCount", i18n.A{"count": fmt.Sprint(len(loose))})) + `</div>` +
		`<div class=np-artist>` + html.EscapeString(i18n.T("publish.looseDesc")) + `</div>`)
	for _, s := range loose {
		b.WriteString(u.pubCaptureBlock(s, true))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pubCaptureBlock renders one capture row: caption + file ops. Playback + trim happen in
// the unified player above; loose captures load into it via "Open in player" / "Trim / edit…".
func (u *UI) pubCaptureBlock(s libdb.SetRecording, loose bool) string {
	kindLbl := i18n.T("publish.broadcastAudio")
	if s.Kind == libdb.SetKindOBS {
		kindLbl = i18n.T("publish.obsRecordingKind")
	}
	capParts := []string{kindLbl, strings.ToUpper(s.Format)}
	if s.Bytes > 0 {
		capParts = append(capParts, humanBytes(uint64(s.Bytes)))
	}
	capParts = append(capParts, filepath.Base(s.Path))

	var btns []string
	if loose {
		btns = append(btns,
			btn(i18n.T("publish.openInPlayer"), "go", "mp-loadcap:"+s.ID, ""),
			btn(i18n.T("publish.trimEditDots"), "secondary", "mp-loadcap:"+s.ID+"\x1fedit", ""))
	}
	// file ops are occasional - one ⋯ menu instead of three buttons per capture row
	btns = append(btns, actionMenu("capmenu-"+strings.Map(menuIDSafe, s.ID), "⋯ "+i18n.T("player.more"), []ssOpt{
		{Val: "pub-open:" + s.ID, Label: i18n.T("player.openExternally")},
		{Val: "pub-reveal:" + s.ID, Label: i18n.T("publish.showInFolder")},
		{Val: "pub-capdel:" + s.ID, Label: i18n.T("common.remove")},
	}))

	return `<div class=pub-cap><div class=pub-cap-cap>` + html.EscapeString(strings.Join(capParts, " · ")) + `</div>` +
		btnRow(btns...) + `</div>`
}

// ── data helpers ────────────────────────────────────────────────────────────────

// pubRecordings returns recordings newest-first with the live set pinned first (live copy).
// The persisted list comes from the epoch-keyed cache (pubRecList); Active() stays live so the
// in-flight set updates every frame.
func (u *UI) pubRecordings() []recorder.Recording {
	recs, _ := u.pubRecList()
	if a := u.svc.Recorder.Active(); a != nil {
		out := make([]recorder.Recording, 0, len(recs)+1)
		out = append(out, *a)
		for _, r := range recs {
			if r.ID != a.ID {
				out = append(out, r)
			}
		}
		return out
	}
	return recs
}

// pubCaptures buckets linked captures by recording id + collects finished, unlinked captures.
// Reads the epoch-keyed cache (pubCapList) - never libdb directly (a cold cache buckets nil →
// empty maps, then the bg load re-renders).
func (u *UI) pubCaptures() (map[string][]libdb.SetRecording, []libdb.SetRecording) {
	caps := map[string][]libdb.SetRecording{}
	var loose []libdb.SetRecording
	all, _ := u.pubCapList()
	for _, s := range all {
		if s.RecordingID != "" {
			caps[s.RecordingID] = append(caps[s.RecordingID], s)
		} else if !s.EndedAt.IsZero() {
			loose = append(loose, s)
		}
	}
	return caps, loose
}

// ── captured-sets list cache: ListSetRecordings resolved off-thread, epoch-keyed ────
//
// ListSetRecordings(300) is a serialized SQLite read; doing it inline in publishBody (every full
// Publish render) AND in every capture file-op click (pubCapByID) froze the act lane. Cache the raw
// rows per-UI, keyed by libdb SetRecVersion() (bumps on any set_recordings insert/relink/delete,
// incl. featurehost-created icecast/obs captures). Render + file-ops read the cache; a bg refresh
// reloads when the epoch advanced (or on first read) and re-renders. Never blocks on libdb.
// Mutation-safety: rows are value copies (libdb.SetRecording is all values); the cached slice is
// replaced wholesale under the mutex, never appended in place, so a reader's slice header stays valid.
type pubCapCache struct {
	mu      sync.Mutex
	epoch   int64
	all     []libdb.SetRecording
	loaded  bool // ≥1 load completed
	loading bool // a load is in flight
}

var (
	pubCapMu     sync.Mutex
	pubCapCaches = map[*UI]*pubCapCache{}
)

func (u *UI) pubCapC() *pubCapCache {
	pubCapMu.Lock()
	defer pubCapMu.Unlock()
	c := pubCapCaches[u]
	if c == nil {
		c = &pubCapCache{}
		pubCapCaches[u] = c
	}
	return c
}

// pubCapList returns the cached set-recording rows, kicking an off-thread reload when the libdb
// set-recordings epoch advanced (or on first read). loaded=false only until the FIRST load lands
// (render shows empty meanwhile); afterwards it serves the last rows while a refresh runs.
func (u *UI) pubCapList() ([]libdb.SetRecording, bool) {
	if u.svc.Lib == nil {
		return nil, true
	}
	epoch := u.svc.Lib.SetRecVersion()
	c := u.pubCapC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if (!c.loaded || c.epoch != epoch) && !c.loading {
		c.loading = true
		u.bg(u.pubCapReload)
	}
	return c.all, c.loaded
}

// pubCapReload reloads the captured-sets list off-thread + re-renders the Publish tab. Reads the
// epoch immediately before the query so the stored epoch matches the loaded rows (a mutation racing
// the load just triggers one redundant reload on the next read - converges).
func (u *UI) pubCapReload() {
	epoch := u.svc.Lib.SetRecVersion()
	all, err := u.svc.Lib.ListSetRecordings(300)
	c := u.pubCapC()
	c.mu.Lock()
	if err != nil {
		c.loading = false
		c.mu.Unlock()
		return
	}
	c.all, c.epoch, c.loaded, c.loading = all, epoch, true, false
	c.mu.Unlock()
	if !u.stopped() && u.activeTab() == "publish" && u.libRemoteTarget() == "" {
		u.patchMain()
	}
}

// ── recordings list cache: Recorder.List() resolved off-thread, epoch-keyed ────
//
// Recorder.List() ForEach-scans the recordings bucket + json.Unmarshals EVERY recording (including
// its full Tracks slice) + sorts - done inline in publishBody on every full Publish render. With
// many long sets that deserializes thousands of Track structs per render on the act lane. Cache the
// rows per-UI keyed by Recorder.RecordingsVersion() (bumps on any put/delete); render reads the
// cache, a bg refresh reloads when the epoch advanced. Mutation-safety: the cached slice is owned by
// this cache (from its own List() call) and read-only by pubRecordings; sweepStale mutates its OWN
// List() results, never these.
type pubRecCache struct {
	mu      sync.Mutex
	epoch   uint64
	all     []recorder.Recording
	loaded  bool // ≥1 load completed
	loading bool // a load is in flight
}

var (
	pubRecMu     sync.Mutex
	pubRecCaches = map[*UI]*pubRecCache{}
)

func (u *UI) pubRecC() *pubRecCache {
	pubRecMu.Lock()
	defer pubRecMu.Unlock()
	c := pubRecCaches[u]
	if c == nil {
		c = &pubRecCache{}
		pubRecCaches[u] = c
	}
	return c
}

// pubRecList returns the cached persisted recordings, kicking an off-thread reload when the recorder
// epoch advanced (or on first read). loaded=false only until the first load lands.
func (u *UI) pubRecList() ([]recorder.Recording, bool) {
	if u.svc.Recorder == nil {
		return nil, true
	}
	epoch := u.svc.Recorder.RecordingsVersion()
	c := u.pubRecC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if (!c.loaded || c.epoch != epoch) && !c.loading {
		c.loading = true
		u.bg(u.pubRecReload)
	}
	return c.all, c.loaded
}

// pubRecReload reloads the persisted recordings off-thread + re-renders the Publish tab. Reads the
// epoch before the scan so the stored epoch matches the loaded rows (a racing write just triggers
// one redundant reload on the next read - converges).
func (u *UI) pubRecReload() {
	epoch := u.svc.Recorder.RecordingsVersion()
	all := u.svc.Recorder.List()
	c := u.pubRecC()
	c.mu.Lock()
	c.all, c.epoch, c.loaded, c.loading = all, epoch, true, false
	c.mu.Unlock()
	if !u.stopped() && u.activeTab() == "publish" && u.libRemoteTarget() == "" {
		u.patchMain()
	}
}

// pubSelected resolves the selected recording (falls back to newest).
func (u *UI) pubSelected(recs []recorder.Recording) *recorder.Recording {
	if len(recs) == 0 {
		return nil
	}
	id := u.pubSelID()
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i]
		}
	}
	return &recs[0]
}

// pubRow is one tracklist entry (offset into the set + label + resolved library path;
// "" = no library identity, row can't carry works-together marks).
type pubRow struct {
	offset time.Duration
	label  string
	path   string
}

// pubTrackRows builds the tracklist from the live session tracks (offset = StartedAt − set start).
// ready=false while each row's library path resolves off-thread (see pubTrackPaths); the label +
// offset come straight off the track (no I/O) and render immediately.
func (u *UI) pubTrackRows(r recorder.Recording) (rows []pubRow, ready bool) {
	paths, ok := u.pubTrackPaths(r)
	rows = make([]pubRow, len(r.Tracks))
	for i, t := range r.Tracks {
		p := ""
		if ok && i < len(paths) {
			p = paths[i]
		}
		rows[i] = pubRow{offset: t.StartedAt.Sub(r.StartedAt), label: orTrackLine(pubTrackLine(t)), path: p}
	}
	return rows, ok
}

// ── tracklist library-link resolution: computed ONCE, persisted, data-change-aware ──
//
// Resolving a track to a library path is a per-track libdb query (pubResolvePath →
// TrackPathByMeta, a full scan); doing it in render for a 30+ track set froze the whole UI.
// Instead we resolve the whole set ONCE off-thread and persist the result in the store, keyed by
// recording ID. It is invalidated by BOTH data sources it depends on:
//   - library side: the store mtime slot carries libdb LibraryVersion() (bumps on any library
//     mutation) → a collection/import/edit re-resolves.
//   - recording side: a content signature (artist+title+path of every track) stored in the blob →
//     Match-history rewriting the paths, or a live set growing, re-resolves.
//
// A tiny in-proc map fronts the store (also dedups in-flight resolves) so repeat renders never
// touch bbolt. Render NEVER blocks on the DB - it reads the cache or shows "linking…".
type pubLinkKey struct {
	u     *UI
	recID string
}
type pubLinkEntry struct {
	epoch   int64
	sig     uint64
	nTracks int
	paths   []string // index-aligned with r.Tracks; nil while pending
	pending bool
}
type pubLinkBlob struct {
	Sig   uint64   `json:"sig"`
	Paths []string `json:"paths"`
}

var (
	pubLinkMu  sync.Mutex
	pubLinkMem = map[pubLinkKey]*pubLinkEntry{}
)

// pubTracksSig hashes the resolution inputs (artist/title/path per track) so any recording-side
// change invalidates the cached links without an explicit hook. Pure CPU, no I/O.
func pubTracksSig(tracks []recorder.Track) uint64 {
	h := fnv.New64a()
	for _, t := range tracks {
		_, _ = h.Write([]byte(t.Artist + "\x00" + t.Title + "\x00" + t.Path + "\n"))
	}
	return h.Sum64()
}

// pubTrackPaths returns the recording's resolved per-track library paths, or (nil,false) while a
// one-shot async resolve runs. NEVER blocks the caller on the library DB.
func (u *UI) pubTrackPaths(r recorder.Recording) ([]string, bool) {
	epoch := int64(0)
	if u.svc.Lib != nil {
		epoch = u.svc.Lib.LibraryVersion()
	}
	n := len(r.Tracks)
	sig := pubTracksSig(r.Tracks)
	k := pubLinkKey{u, r.ID}

	pubLinkMu.Lock()
	if e := pubLinkMem[k]; e != nil && e.epoch == epoch && e.sig == sig && e.nTracks == n {
		if e.paths != nil {
			p := e.paths
			pubLinkMu.Unlock()
			return p, true
		}
		if e.pending {
			pubLinkMu.Unlock()
			return nil, false // resolve already in flight for this (epoch,sig)
		}
	}
	if u.svc.Store != nil {
		if raw, hit := u.svc.Store.GetAnalysis(store.KindSetTrackLinks, r.ID, epoch); hit {
			var blob pubLinkBlob
			if json.Unmarshal(raw, &blob) == nil && blob.Sig == sig && len(blob.Paths) == n {
				pubLinkMem[k] = &pubLinkEntry{epoch: epoch, sig: sig, nTracks: n, paths: blob.Paths}
				pubLinkMu.Unlock()
				return blob.Paths, true
			}
		}
	}
	pubLinkMem[k] = &pubLinkEntry{epoch: epoch, sig: sig, nTracks: n, pending: true}
	pubLinkMu.Unlock()

	tracks := append([]recorder.Track(nil), r.Tracks...)
	recID := r.ID
	u.bg(func() {
		paths := make([]string, len(tracks))
		for i := range tracks {
			paths[i] = u.pubResolvePath(tracks[i])
		}
		if u.svc.Store != nil {
			if raw, err := json.Marshal(pubLinkBlob{Sig: sig, Paths: paths}); err == nil {
				u.svc.Store.PutAnalysis(store.KindSetTrackLinks, recID, epoch, raw)
			}
		}
		pubLinkMu.Lock()
		pubLinkMem[k] = &pubLinkEntry{epoch: epoch, sig: sig, nTracks: len(tracks), paths: paths}
		pubLinkMu.Unlock()
		if !u.stopped() && u.activeTab() == "publish" && u.pubSubtab() == "tracklist" && u.pubSelID() == recID {
			u.patchMain()
		}
	})
	return nil, false
}

// pubResolvePath maps a recorder track to its library path (existing link first, then
// exact-meta fallback; "" = unresolved).
func (u *UI) pubResolvePath(t recorder.Track) string {
	if t.Path != "" {
		return t.Path
	}
	if u.svc.Lib != nil {
		if p, ok := u.svc.Lib.TrackPathByMeta(t.Artist, t.Title); ok {
			return p
		}
	}
	return ""
}

// ── small formatters ────────────────────────────────────────────────────────────

func pubTrackLine(t recorder.Track) string {
	if t.Artist != "" {
		return t.Artist + " - " + t.Title
	}
	return t.Title
}

func pubTrackMeta(t recorder.Track) string {
	var p []string
	if t.Deck != "" {
		p = append(p, i18n.T("publish.deck", i18n.A{"name": t.Deck}))
	}
	if t.BPM > 0 {
		p = append(p, fmt.Sprintf("%.1f BPM", t.BPM))
	}
	if t.Key != "" {
		p = append(p, t.Key)
	}
	if t.TitleSource != "" {
		p = append(p, i18n.T("publish.via", i18n.A{"name": t.TitleSource}))
	}
	return strings.Join(p, " · ")
}

func orSetName(name string) string {
	if strings.TrimSpace(name) == "" {
		return i18n.T("publish.liveSet")
	}
	return name
}

func orTrackLine(s string) string {
	if strings.TrimSpace(s) == "" {
		return i18n.T("publish.untitledTrack")
	}
	return s
}

// menuIDSafe maps capture IDs onto smartSelect's colon-free id token space.
func menuIDSafe(r rune) rune {
	if r == ':' || r == ' ' {
		return '_'
	}
	return r
}

// pubClock formats seconds as m:ss (or h:mm:ss past an hour).
func pubClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec)
	h, m, s := t/3600, (t/60)%60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func pubIsAudio(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac", ".wav", ".mp3", ".aac", ".ogg", ".m4a", ".opus", ".oga", ".aiff", ".aif":
		return true
	}
	return false
}
