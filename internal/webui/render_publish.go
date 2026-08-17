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
	"rave.page/mate/internal/zigui"
)

// Publish is the recording/publishing cockpit at parity with the Fyne Publish tab
// (view_recorder.go): REC/CAPTURE/OBS hero badges, now-playing meta + confirm countdown,
// a Sets ↔ Captures/Tracklist master-detail, and Export / Match-history / Delete flows.
// Playback + trim/edit run in the unified media player (player.go) embedded in the
// captures pane - audio, video (mpv) and aligned audio+video pairs alike. Live tick
// patches pub-hero.
//
// Zig-rendered (native/zigui/src/publish.zig): Go resolves recordings + captures +
// tracklist links + i18n into pubSt, Zig renders HTML byte-identical to the pure Go
// renderers below (fallback + golden reference, zigui_golden_publish_test.go).
// Everything owned by another subsystem rides through the state as trusted RAW markup:
// the unified player/editor (player.go mpHTML) and the peer target switcher
// (render_library_remote.go targetSwitcherHTML). Numbers never cross as floats to be
// formatted - the progress bars carry both the float (Go path) and Go's "%.1f%%" string
// (Zig path), pinned by TestPubBarNumberPairsAgree.

// ── render state (JSON → Zig) ───────────────────────────────────────────────────

// pubBadgeSt is one hero badge (REC/CAPTURE/OBS). DL = Go strings.ToLower(Key).
type pubBadgeSt struct {
	Key     string `json:"key"`
	DL      string `json:"dl"`
	Variant string `json:"variant"`
	Line    string `json:"line"`
}

// pubBarSt is a resolved progressBar. Frac feeds the Go primitive; Pct is Go's
// "%.1f%%" of the clamped fraction (Zig never formats a float).
type pubBarSt struct {
	Show bool    `json:"show"`
	Frac float64 `json:"-"`
	Pct  string  `json:"pct"`
	Cap  string  `json:"cap"`
}

// newPubBar clamps + pre-formats exactly like components.go progressBar.
func newPubBar(frac float64, caption string) pubBarSt {
	f := frac
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return pubBarSt{Show: true, Frac: frac, Pct: fmt.Sprintf("%.1f%%", f*100), Cap: caption}
}

// pubNpSt is the now-playing strip.
type pubNpSt struct {
	Label string   `json:"label"`
	Title string   `json:"title"`
	Meta  string   `json:"meta"`
	State string   `json:"state"`
	Bar   pubBarSt `json:"bar"`
}

// pubPlayerSt is the hero transport readout (shared player position).
type pubPlayerSt struct {
	Show  bool     `json:"show"`
	Label string   `json:"label"`
	Pos   string   `json:"pos"`
	Bar   pubBarSt `json:"bar"`
}

// pubHeroSt is the #pub-hero card (tick-patched). Show=false ⇒ no recorder ⇒ empty.
type pubHeroSt struct {
	Show   bool        `json:"show"`
	Rec    pubBadgeSt  `json:"rec"`
	Cap    pubBadgeSt  `json:"cap"`
	Obs    pubBadgeSt  `json:"obs"`
	Finish string      `json:"finish"` // "" = no set live → no Finish-set button
	NP     pubNpSt     `json:"np"`
	Player pubPlayerSt `json:"player"`
}

// pubSetRowSt is one row of the sets list.
type pubSetRowSt struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Sub    string `json:"sub"`
	Sel    bool   `json:"sel"`
	Rename string `json:"rename"`
}

// pubListSt is the sets list (Rows empty ⇒ emptyState(Empty)).
type pubListSt struct {
	Empty string        `json:"empty"`
	Count string        `json:"count"`
	Rows  []pubSetRowSt `json:"rows,omitempty"`
}

// pubCapSt is one capture row: caption + optional loose-capture buttons + the ⋯ menu.
type pubCapSt struct {
	Caption string   `json:"caption"`
	Btns    []uiBtn  `json:"btns,omitempty"`
	Menu    selState `json:"menu"`
}

// pubLooseSt is the unlinked-captures block (Caps empty ⇒ nothing rendered).
type pubLooseSt struct {
	Count string     `json:"count"`
	Desc  string     `json:"desc"`
	Caps  []pubCapSt `json:"caps,omitempty"`
}

// pubCapturesSt is the Captures subtab. Player = RAW player.go mpHTML output.
type pubCapturesSt struct {
	Player string     `json:"player"`
	Empty  string     `json:"empty"`
	Caps   []pubCapSt `json:"caps,omitempty"`
}

// pubTrackSt is one tracklist row. Lead ∈ resolving|none|chk; Ctx = the data-ctx value
// ("" = no context menu); OffAct/OffDL only used on editable (finished-set) rows.
type pubTrackSt struct {
	Num     int    `json:"num"`
	Label   string `json:"label"`
	Off     string `json:"off"`
	Lead    string `json:"lead"`
	LeadTip string `json:"leadTip"`
	Checked bool   `json:"checked"`
	Path    string `json:"path"`
	Ctx     string `json:"ctx"`
	OffAct  string `json:"offAct"`
	OffDL   string `json:"offDl"`
}

// pubBatchSt is the works-together batch bar (Count "" ⇒ hidden).
type pubBatchSt struct {
	Count string  `json:"count"`
	Btns  []uiBtn `json:"btns,omitempty"`
}

// pubTracklistSt is the Tracklist subtab.
type pubTracklistSt struct {
	Empty     string       `json:"empty"`     // no rows → hint
	Resolving string       `json:"resolving"` // "" = library links resolved
	Editable  bool         `json:"editable"`
	OffTip    string       `json:"offTip"`
	Rows      []pubTrackSt `json:"rows,omitempty"`
	ShowFix   bool         `json:"showFix"`
	Fix       uiBtn        `json:"fix"`
	Help      string       `json:"help"`
	Unres     string       `json:"unres"` // "" = nothing unresolved
	Batch     pubBatchSt   `json:"batch"`
}

// pubDetailSt is the right pane. Sel=false ⇒ the select-a-set hint (+ a pinned loose
// capture in the player) instead of the set detail.
type pubDetailSt struct {
	CardTitle string     `json:"cardTitle"`
	Sel       bool       `json:"sel"`
	Hint      string     `json:"hint"`
	Player    string     `json:"player"` // RAW: pinned loose-capture player
	Loose     pubLooseSt `json:"loose"`

	Name      string         `json:"name"`
	Meta      string         `json:"meta"`
	Actions   []uiBtn        `json:"actions,omitempty"`
	Active    string         `json:"active"`
	CapsLbl   string         `json:"capsLbl"`
	TracksLbl string         `json:"tracksLbl"`
	Captures  pubCapturesSt  `json:"captures"`
	Tracklist pubTracklistSt `json:"tracklist"`
}

// pubBodySt is #publish-body (hero + master/detail).
type pubBodySt struct {
	Hero   pubHeroSt   `json:"hero"`
	List   pubListSt   `json:"list"`
	Detail pubDetailSt `json:"detail"`
}

// pubSt is the resolved render state for the local Publish view.
type pubSt struct {
	Title       string    `json:"title"`
	Sub         string    `json:"sub"`
	Switcher    string    `json:"switcher"` // RAW: targetSwitcherHTML
	Available   bool      `json:"available"`
	Unavailable string    `json:"unavailable"`
	Body        pubBodySt `json:"body"`
}

// ── state builders ──────────────────────────────────────────────────────────────

// publishState resolves the whole local Publish view. Impure: recorder/libdb caches,
// the smart-select registration inside targetSwitcherHTML, mpEnsureSet, and the
// off-thread tracklist link resolve all fire here, in the render order the Go
// renderer used.
func (u *UI) publishState() pubSt {
	st := pubSt{
		Title:       i18n.T("publish.title"),
		Sub:         i18n.T("publish.subtitle"),
		Switcher:    u.targetSwitcherHTML("pubtarget", "pub-target:"),
		Available:   u.svc.Recorder != nil,
		Unavailable: i18n.T("publish.recorderUnavailable"),
	}
	if !st.Available {
		return st
	}
	st.Body = u.pubBodyState()
	return st
}

func (u *UI) pubBodyState() pubBodySt {
	recs := u.pubRecordings()
	caps, loose := u.pubCaptures()
	sel := u.pubSelected(recs)
	return pubBodySt{
		Hero:   u.pubHeroState(),
		List:   u.pubListState(recs, sel, caps),
		Detail: u.pubDetailState(sel, caps, loose),
	}
}

// ── hero: live badges + now-playing (tick-patched) ──────────────────────────────

func (u *UI) pubHeroState() pubHeroSt {
	if u.svc.Recorder == nil {
		return pubHeroSt{}
	}
	st := pubHeroSt{
		Show: true,
		Rec:  u.pubRecBadge(), Cap: u.pubCapBadge(), Obs: u.pubObsBadge(),
		NP: u.pubNpState(), Player: u.pubPlayerState(),
	}
	// Finish-set action (only while a set is live).
	if a := u.svc.Recorder.Active(); a != nil {
		st.Finish = i18n.T("publish.finishSet")
	}
	return st
}

func pubBadge(key, variant, line string) pubBadgeSt {
	return pubBadgeSt{Key: key, DL: strings.ToLower(key), Variant: variant, Line: line}
}

func (u *UI) pubRecBadge() pubBadgeSt {
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

func (u *UI) pubCapBadge() pubBadgeSt {
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

func (u *UI) pubObsBadge() pubBadgeSt {
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

// pubNpState mirrors the Fyne now-playing strip: pending candidate (confirm countdown)
// or the current confirmed track, else "Nothing audible".
func (u *UI) pubNpState() pubNpSt {
	st := pubNpSt{Label: i18n.T("publish.nowPlaying"), Title: i18n.T("publish.nothingAudible")}

	if p, ok := u.svc.Recorder.Pending(); ok {
		st.Title = orTrackLine(pubTrackLine(p.Track))
		st.Meta = pubTrackMeta(p.Track)
		left := time.Until(p.ConfirmAt).Truncate(time.Second)
		if left < 0 {
			left = 0
		}
		st.State = i18n.T("publish.confirming", i18n.A{"left": left.String()})
		if window := p.ConfirmAt.Sub(p.FirstSeen); window > 0 {
			st.Bar = newPubBar(1-float64(left)/float64(window), i18n.T("publish.confirmingIn", i18n.A{"left": left.String()}))
		}
	} else if a := u.svc.Recorder.Active(); a != nil && len(a.Tracks) > 0 {
		if t := a.Tracks[len(a.Tracks)-1]; t.EndedAt.IsZero() {
			st.Title = orTrackLine(pubTrackLine(t))
			st.Meta = pubTrackMeta(t)
			st.State = i18n.T("publish.trackInList", i18n.A{"n": fmt.Sprint(len(a.Tracks)), "dur": time.Since(t.StartedAt).Truncate(time.Second).String()})
		}
	}
	return st
}

// pubPlayerState resolves the shared audio player's live position (tick-patched via
// pub-hero) so the transport readout stays fresh without rebuilding the detail pane's
// seek control.
func (u *UI) pubPlayerState() pubPlayerSt {
	pl := u.player() // gated read: a remote mirror must not leak this machine's playback
	if pl == nil {
		return pubPlayerSt{}
	}
	st := pl.State()
	if !st.Playing || st.Path == "" {
		return pubPlayerSt{}
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
	return pubPlayerSt{Show: true, Label: label, Pos: pos, Bar: newPubBar(frac, pos)}
}

// ── sets list ───────────────────────────────────────────────────────────────────

func (u *UI) pubListState(recs []recorder.Recording, sel *recorder.Recording, caps map[string][]libdb.SetRecording) pubListSt {
	st := pubListSt{Empty: i18n.T("publish.noRecordings"), Rows: []pubSetRowSt{}}
	if len(recs) == 0 {
		return st
	}
	st.Count = i18n.T("publish.setsCount", i18n.A{"count": fmt.Sprint(len(recs))})
	for i := range recs {
		r := recs[i]
		title := orSetName(r.Name)
		if r.EndedAt.IsZero() {
			title = "⏺ " + title
		}
		// Rename sits in the row's action slot: the runtime dispatches the closest [data-act], so
		// the button wins over the row's own pub-select. Ungated - Recorder.Rename handles the
		// live set too (unlike delete, which waits for the set to end).
		st.Rows = append(st.Rows, pubSetRowSt{
			ID: r.ID, Title: title, Sub: pubSetMeta(r, caps[r.ID]),
			Sel: sel != nil && sel.ID == r.ID, Rename: i18n.T("publish.renameDots"),
		})
	}
	return st
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

func (u *UI) pubDetailState(sel *recorder.Recording, caps map[string][]libdb.SetRecording, loose []libdb.SetRecording) pubDetailSt {
	st := pubDetailSt{CardTitle: i18n.T("publish.selectedSet"), Hint: i18n.T("publish.selectHint")}
	if sel == nil {
		if t := u.mpSnap("publish"); t.pinned { // loose capture opened in the player
			st.Player = u.mpHTML("publish")
		}
		st.Loose = u.pubLooseState(loose)
		return st
	}
	r := *sel
	st.Sel = true
	st.Name, st.Meta = orSetName(r.Name), pubSetMeta(r, caps[r.ID])
	st.Actions = u.pubActionsState(r, len(caps[r.ID]) > 0)

	sets := caps[r.ID]
	st.Active = u.pubSubtab()
	// Track COUNT needs no library resolution - use it for the subtab label so switching to
	// Captures never pays the (async, DB-backed) tracklist link resolution.
	st.CapsLbl = i18n.T("publish.capturesCount", i18n.A{"count": fmt.Sprint(len(sets))})
	st.TracksLbl = i18n.T("publish.tracklistCount", i18n.A{"count": fmt.Sprint(len(r.Tracks))})

	if st.Active == "tracklist" {
		rows, ready := u.pubTrackRows(r) // library-path resolution is off-thread + cached (see pubTrackPaths)
		st.Tracklist = u.pubTracklistState(r, len(sets) > 0, rows, !ready)
	} else {
		st.Captures = u.pubCapturesState(r, sets)
		st.Loose = u.pubLooseState(loose)
	}
	return st
}

// pubActionsState builds the set's action row. hasCaps = a capture file is linked, which is
// what makes the set publishable (no audio, nothing to upload).
func (u *UI) pubActionsState(r recorder.Recording, hasCaps bool) []uiBtn {
	btns := []uiBtn{{Label: i18n.T("publish.export"), Variant: "outline", Act: "pub-export:" + r.ID}}
	if !r.EndedAt.IsZero() && hasCaps && len(r.Tracks) > 0 {
		btns = append(btns, uiBtn{Label: i18n.T("publish.upload.action"), Variant: "primary",
			Act: "pub-publish:" + r.ID})
	}
	if !r.EndedAt.IsZero() {
		btns = append(btns,
			uiBtn{Label: i18n.T("publish.matchHistory"), Variant: "secondary", Act: "pub-match:" + r.ID},
			uiBtn{Label: i18n.T("publish.matchHistoryFull"), Variant: "ghost", Act: "pub-match-full:" + r.ID},
			uiBtn{Label: i18n.T("publish.delete"), Variant: "destructive", Act: "pub-del:" + r.ID})
	}
	return btns
}

// pubTracklistState resolves the tracklist. resolving = library-path links are still being
// resolved off-thread (names/times show immediately; the works-together checkboxes fill in when
// the async resolve lands and re-renders). Finished sets get editable start offsets + the
// capture-aligned "Fix start times" flow (hasCaps).
func (u *UI) pubTracklistState(r recorder.Recording, hasCaps bool, rows []pubRow, resolving bool) pubTracklistSt {
	st := pubTracklistSt{Empty: i18n.T("publish.noTracks"), Rows: []pubTrackSt{}}
	if len(rows) == 0 {
		return st
	}
	st.Editable = !r.EndedAt.IsZero()
	st.Help = i18n.T("publish.compat.help")
	sel := u.pubTSel()
	if resolving {
		st.Resolving = i18n.T("publish.linkingLibrary")
	}
	if st.Editable {
		st.OffTip = i18n.T("publish.offsetEditTip")
	}
	unresolved := 0
	for i, row := range rows {
		t := pubTrackSt{Num: i + 1, Label: row.label, Off: pubClock(row.offset.Seconds()), Path: row.path}
		switch {
		case resolving:
			// links not resolved yet - neutral placeholder, not a "no match" mark
			t.Lead, t.LeadTip = "resolving", i18n.T("publish.linkingLibrary")
		case row.path == "":
			unresolved++
			t.Lead, t.LeadTip = "none", i18n.T("publish.compat.unresolved")
		default:
			t.Lead, t.Checked = "chk", sel[row.path]
			t.Ctx = "pub-tctx:" + row.path
		}
		if st.Editable {
			t.OffAct = "pub-toff:" + r.ID + "\x1f" + fmt.Sprint(i)
			t.OffDL = "offset-" + fmt.Sprint(i+1) // ctl read/set target (space-free: ctl set splits on first space)
			// finished sets: every row gets a context menu (compat when resolved + remove)
			t.Ctx = "pub-tctx2:" + r.ID + "\x1f" + fmt.Sprint(i) + "\x1f" + row.path
		}
		st.Rows = append(st.Rows, t)
	}
	if st.Editable && hasCaps {
		st.ShowFix = true
		st.Fix = uiBtn{Label: i18n.T("publish.fix.button"), Variant: "outline", Act: "pub-fixtimes:" + r.ID}
	}
	if unresolved > 0 {
		st.Unres = i18n.T("publish.compat.unresolvedCount", i18n.A{"count": fmt.Sprint(unresolved)})
	}
	if len(sel) > 0 {
		st.Batch.Count = i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(sel))})
		if len(sel) >= 2 {
			st.Batch.Btns = append(st.Batch.Btns, uiBtn{Label: i18n.T("library.compat.markBtn"), Variant: "primary", Act: "lib-compat-mark:pub"})
		}
		if len(sel) == 1 {
			for p := range sel {
				st.Batch.Btns = append(st.Batch.Btns, uiBtn{Label: i18n.T("library.compat.findBtn"), Variant: "outline", Act: "lib-compat-find:" + p})
			}
		}
		st.Batch.Btns = append(st.Batch.Btns, uiBtn{Label: i18n.T("library.clear"), Variant: "ghost", Act: "pub-tsel-clear"})
	}
	return st
}

func (u *UI) pubCapturesState(r recorder.Recording, sets []libdb.SetRecording) pubCapturesSt {
	u.mpEnsureSet(r, sets)
	st := pubCapturesSt{
		Player: u.mpHTML("publish"), // unified player/editor (also shows a pinned loose capture)
		Empty:  i18n.T("publish.noCaptures"),
		Caps:   []pubCapSt{},
	}
	for _, s := range sets {
		st.Caps = append(st.Caps, u.pubCapState(s, false))
	}
	return st
}

func (u *UI) pubLooseState(loose []libdb.SetRecording) pubLooseSt {
	st := pubLooseSt{Caps: []pubCapSt{}}
	if len(loose) == 0 {
		return st
	}
	st.Count = i18n.T("publish.unlinkedCount", i18n.A{"count": fmt.Sprint(len(loose))})
	st.Desc = i18n.T("publish.looseDesc")
	for _, s := range loose {
		st.Caps = append(st.Caps, u.pubCapState(s, true))
	}
	return st
}

// pubCapState resolves one capture row: caption + file ops. Playback + trim happen in
// the unified player above; loose captures load into it via "Open in player" / "Trim / edit…".
func (u *UI) pubCapState(s libdb.SetRecording, loose bool) pubCapSt {
	kindLbl := i18n.T("publish.broadcastAudio")
	if s.Kind == libdb.SetKindOBS {
		kindLbl = i18n.T("publish.obsRecordingKind")
	}
	capParts := []string{kindLbl, strings.ToUpper(s.Format)}
	if s.Bytes > 0 {
		capParts = append(capParts, humanBytes(uint64(s.Bytes)))
	}
	capParts = append(capParts, filepath.Base(s.Path))

	st := pubCapSt{Caption: strings.Join(capParts, " · "), Btns: []uiBtn{}}
	if loose {
		st.Btns = append(st.Btns,
			uiBtn{Label: i18n.T("publish.openInPlayer"), Variant: "go", Act: "mp-loadcap:" + s.ID},
			uiBtn{Label: i18n.T("publish.trimEditDots"), Variant: "secondary", Act: "mp-loadcap:" + s.ID + "\x1fedit"})
	}
	// file ops are occasional - one ⋯ menu instead of three buttons per capture row
	st.Menu = resolveActionMenu("capmenu-"+strings.Map(menuIDSafe, s.ID), "⋯ "+i18n.T("player.more"), []ssOpt{
		{Val: "pub-open:" + s.ID, Label: i18n.T("player.openExternally")},
		{Val: "pub-reveal:" + s.ID, Label: i18n.T("publish.showInFolder")},
		{Val: "pub-capdel:" + s.ID, Label: i18n.T("common.remove")},
	})
	return st
}

// ── bridges (Zig when linked, Go otherwise) ─────────────────────────────────────

func (u *UI) renderPublish() string {
	// Remote: a peer is targeted → the recorded-sets browser over remotectl (local path untouched).
	if tgt := u.libRemoteTarget(); tgt != "" {
		return u.renderPublishRemote(tgt)
	}
	st := u.publishState()
	if zigui.Available() {
		if h, ok := zigWire("RenderPublishV2", wirePub(st), zigui.RenderPublishV2,
			zigui.RenderPublish, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return publishHTML(st)
}

// publishHeroHTML is the #pub-hero fragment (live tick patch).
func (u *UI) publishHeroHTML() string {
	st := u.pubHeroState()
	if zigui.Available() {
		if h, ok := zigWire("RenderPublishHeroV2", wirePubHero(st), zigui.RenderPublishHeroV2,
			zigui.RenderPublishHero, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return pubHeroHTML(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ─────────────────

func publishHTML(st pubSt) string {
	if !st.Available {
		return panel(st.Title, "") + st.Switcher + emptyState(st.Unavailable)
	}
	return panel(st.Title, st.Sub) + st.Switcher +
		`<div id=publish-body>` + publishBodyHTML(st.Body) + `</div>`
}

func publishBodyHTML(st pubBodySt) string {
	return `<div id=pub-hero>` + pubHeroHTML(st.Hero) + `</div>` +
		masterDetail(pubListHTML(st.List), pubDetailHTML(st.Detail))
}

func pubHeroHTML(st pubHeroSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card pub-hero">`)
	b.WriteString(`<div class=pub-badges>` + pubBadgeHTML(st.Rec) + pubBadgeHTML(st.Cap) + pubBadgeHTML(st.Obs) + `</div>`)
	if st.Finish != "" {
		b.WriteString(btnRow(btn(st.Finish, "destructive", "rec-finish", "")))
	}
	b.WriteString(pubNpHTML(st.NP))
	b.WriteString(pubPlayerHTML(st.Player))
	b.WriteString(`</div>`)
	return b.String()
}

func pubBadgeHTML(st pubBadgeSt) string {
	return `<div class=pub-badge>` + dot(st.Variant) +
		`<div class=pub-badge-tx><div class=pub-badge-k data-label=` + attrQ(st.DL) + `>` + html.EscapeString(st.Key) + `</div>` +
		`<div class=pub-badge-v data-value=` + attrQ(st.Line) + `>` + html.EscapeString(st.Line) + `</div></div></div>`
}

func pubNpHTML(st pubNpSt) string {
	var b strings.Builder
	b.WriteString(`<div class=pub-np>`)
	b.WriteString(`<div class=card-label>` + html.EscapeString(st.Label) + `</div>`)
	b.WriteString(`<div class=pub-np-t data-label="now playing" data-value="` + html.EscapeString(st.Title) + `">` + html.EscapeString(st.Title) + `</div>`)
	if st.Meta != "" {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(st.Meta) + `</div>`)
	}
	if st.State != "" {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(st.State) + `</div>`)
	}
	if st.Bar.Show {
		b.WriteString(`<div style="margin-top:8px">` + progressBar(st.Bar.Frac, st.Bar.Cap) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func pubPlayerHTML(st pubPlayerSt) string {
	if !st.Show {
		return ""
	}
	return `<div class=pub-player><div class=pub-player-l>` + html.EscapeString(st.Label) +
		` <span class=np-artist>` + html.EscapeString(st.Pos) + `</span></div>` +
		progressBar(st.Bar.Frac, st.Bar.Cap) + `</div>`
}

func pubListHTML(st pubListSt) string {
	if len(st.Rows) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=card-label>` + html.EscapeString(st.Count) + `</div>`)
	for _, r := range st.Rows {
		cls := "irow pub-setrow"
		if r.Sel {
			cls += " selected"
		}
		b.WriteString(`<div class="` + cls + `" data-act="pub-select:` + html.EscapeString(r.ID) + `">` +
			`<div class=irow-main><div class=irow-title>` + html.EscapeString(r.Title) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(r.Sub) + `</div></div>` +
			`<div class=irow-actions>` + btn(r.Rename, "ghost", "pub-rename:"+r.ID, "") + `</div></div>`)
	}
	return b.String()
}

func pubDetailHTML(st pubDetailSt) string {
	if !st.Sel {
		return card(st.CardTitle, "", hint("info", st.Hint)+st.Player+pubLooseHTML(st.Loose))
	}
	head := `<div class=pub-detail-h><div class=pub-detail-name>` + html.EscapeString(st.Name) + `</div>` +
		`<div class=np-artist>` + html.EscapeString(st.Meta) + `</div>` +
		uiBtnRow(st.Actions) + `</div>`
	tabs := subTabs("pub-tab:", st.Active,
		[2]string{"captures", st.CapsLbl},
		[2]string{"tracklist", st.TracksLbl},
	)
	var body string
	if st.Active == "tracklist" {
		body = pubTracklistHTML(st.Tracklist)
	} else {
		body = pubCapturesHTML(st.Captures) + pubLooseHTML(st.Loose)
	}
	return card(st.CardTitle, "", head+tabs+`<div class=pub-subbody>`+body+`</div>`)
}

func pubTracklistHTML(st pubTracklistSt) string {
	if len(st.Rows) == 0 {
		return hint("info", st.Empty)
	}
	var b strings.Builder
	if st.Resolving != "" {
		b.WriteString(hint("info", st.Resolving))
	}
	b.WriteString(`<div class=pub-tracklist>`)
	for _, row := range st.Rows {
		lead := ""
		switch row.Lead {
		case "resolving", "none":
			glyph := "…"
			if row.Lead == "none" {
				glyph = "·"
			}
			lead = `<span class="pub-track-chk none" title=` + attrQ(row.LeadTip) + `>` + glyph + `</span>`
		default:
			chk := ""
			if row.Checked {
				chk = " checked"
			}
			lead = `<span class=pub-track-chk><input type=checkbox data-act="pub-tsel:` + html.EscapeString(row.Path) + `"` + chk + `></span>`
		}
		ctx := ""
		if row.Ctx != "" {
			ctx = ` data-ctx=` + attrQ(row.Ctx)
		}
		oCell := `<span class=pub-track-o>[` + row.Off + `]</span>`
		if st.Editable {
			oCell = `<input class=pub-track-oin type=text value=` + attrQ(row.Off) + ` data-value=` + attrQ(row.Off) +
				` data-act=` + attrQ(row.OffAct) +
				` data-label=` + attrQ(row.OffDL) +
				` title=` + attrQ(st.OffTip) + `>`
		}
		b.WriteString(`<div class=pub-track` + ctx + `>` + lead +
			`<span class=pub-track-n>` + fmt.Sprint(row.Num) + `.</span>` +
			oCell +
			`<span class=pub-track-l>` + html.EscapeString(row.Label) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if st.ShowFix {
		b.WriteString(btnRow(st.Fix.html()))
	}
	b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Help) + `</p>`)
	if st.Unres != "" {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Unres) + `</p>`)
	}
	if st.Batch.Count != "" {
		var btns strings.Builder
		for _, x := range st.Batch.Btns {
			btns.WriteString(x.html())
		}
		b.WriteString(`<div class=batchbar><span class=cnt>` + html.EscapeString(st.Batch.Count) + `</span>` +
			btns.String() + `</div>`)
	}
	return b.String()
}

func pubCapturesHTML(st pubCapturesSt) string {
	var b strings.Builder
	b.WriteString(st.Player)
	if len(st.Caps) == 0 {
		b.WriteString(hint("info", st.Empty))
		return b.String()
	}
	for _, s := range st.Caps {
		b.WriteString(pubCapHTML(s))
	}
	return b.String()
}

func pubLooseHTML(st pubLooseSt) string {
	if len(st.Caps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=pub-loose><div class=card-label>` + html.EscapeString(st.Count) + `</div>` +
		`<div class=np-artist>` + html.EscapeString(st.Desc) + `</div>`)
	for _, s := range st.Caps {
		b.WriteString(pubCapHTML(s))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func pubCapHTML(st pubCapSt) string {
	btns := make([]string, 0, len(st.Btns)+1)
	for _, x := range st.Btns {
		btns = append(btns, x.html())
	}
	btns = append(btns, actionMenuHTML(st.Menu))
	return `<div class=pub-cap><div class=pub-cap-cap>` + html.EscapeString(st.Caption) + `</div>` +
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
// ListSetRecordings(300) is a serialized SQLite read; doing it inline in pubBodyState (every full
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
// its full Tracks slice) + sorts - done inline in pubBodyState on every full Publish render. With
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
