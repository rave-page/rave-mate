package webui

// Remote Publish (webview): the recording/publish cockpit driven over remotectl against a paired
// instance. Live status (REC/CAPTURE/OBS badges, now-playing, embedded player, capture file ops)
// stays on the controlled computer - it needs that box's live session + local files - so the remote
// view degrades to a recorded-sets browser: list sets, page a set's tracklist, read capture
// metadata, and Export / Match-history / Delete over the link. The LOCAL Publish path is
// byte-behaviour-unchanged; renderPublish early-returns here only when a peer is targeted. remotectl
// calls block on the network, so the body renders synchronously from a per-UI cache (pubRemoteSt)
// and background fetches patch the cache in (render never blocks).
//
// Zig-rendered (native/zigui/src/publish.zig, export rz_ui_render_publish_remote): the cache read
// + i18n resolve into pubRemSt, Zig renders HTML byte-identical to the pure Go renderers below
// (golden reference, zigui_golden_publish_test.go).

import (
	"context"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/zigui"
)

const (
	remotePubSetsPage  = 200 // newest-N set summaries in one frame (recorded sets are few; stays under cap)
	remotePubTrackPage = 500 // one set's tracks in one frame (server page max)
)

// ── per-UI remote publish cache ─────────────────────────────────────────────────

type pubRemoteSt struct {
	mu     sync.Mutex
	target string // nodeID this cache is for

	// sets list
	sets    []remotectl.RecMeta
	total   int
	loading bool
	err     string
	selID   string

	// selected set tracklist
	tl        []recorder.Track
	tlStart   time.Time
	tlTotal   int
	tlLoading bool
	tlErr     string

	// peer captures (all rows; filtered per set at render)
	caps        []libdb.SetRecording
	capsLoaded  bool
	capsLoading bool
	capsErr     string
}

// resetFor clears the cache for a new target (fields cleared in place; embedded mutex never copied).
func (s *pubRemoteSt) resetFor(target string) {
	s.target = target
	s.sets, s.total, s.loading, s.err, s.selID = nil, 0, false, "", ""
	s.tl, s.tlStart, s.tlTotal, s.tlLoading, s.tlErr = nil, time.Time{}, 0, false, ""
	s.caps, s.capsLoaded, s.capsLoading, s.capsErr = nil, false, false, ""
}

var (
	pubRemoteMu  sync.Mutex
	pubRemoteSts = map[*UI]*pubRemoteSt{}
)

func (u *UI) pubR() *pubRemoteSt {
	pubRemoteMu.Lock()
	defer pubRemoteMu.Unlock()
	s := pubRemoteSts[u]
	if s == nil {
		s = &pubRemoteSt{}
		pubRemoteSts[u] = s
	}
	return s
}

// ── render state (JSON → Zig) ───────────────────────────────────────────────────

// pubRemRowSt is one row of the peer's sets list.
type pubRemRowSt struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Sel   bool   `json:"sel"`
}

// pubRemListSt is the sets list. Empty ≠ "" ⇒ ONLY the empty state renders
// (loading / link error / no sets).
type pubRemListSt struct {
	Empty string        `json:"empty"`
	Count string        `json:"count"`
	Note  string        `json:"note"` // "" = the full list arrived
	Rows  []pubRemRowSt `json:"rows,omitempty"`
}

// pubRemTrackSt is one paged tracklist row (read-only: no links, no offsets edit).
type pubRemTrackSt struct {
	Num   int    `json:"num"`
	Off   string `json:"off"`
	Label string `json:"label"`
}

// pubRemTlSt is the Tracklist subtab. Empty ⇒ emptyState (loading); Hint ⇒ hint chip
// (link error / no tracks); either wins over Rows.
type pubRemTlSt struct {
	Empty string          `json:"empty"`
	Hint  string          `json:"hint"`
	Note  string          `json:"note"`
	Rows  []pubRemTrackSt `json:"rows,omitempty"`
}

// pubRemCapsSt is the Captures subtab (captions only - files live on the peer).
type pubRemCapsSt struct {
	Hint string   `json:"hint"` // link error / no captures
	Note string   `json:"note"`
	Caps []string `json:"caps,omitempty"`
}

// pubRemDetailSt is the right pane.
type pubRemDetailSt struct {
	CardTitle string `json:"cardTitle"`
	Sel       bool   `json:"sel"`
	Hint      string `json:"hint"` // no selection

	Name      string       `json:"name"`
	Meta      string       `json:"meta"`
	Actions   []uiBtn      `json:"actions,omitempty"`
	Active    string       `json:"active"`
	CapsLbl   string       `json:"capsLbl"`
	TracksLbl string       `json:"tracksLbl"`
	Tl        pubRemTlSt   `json:"tl"`
	Caps      pubRemCapsSt `json:"caps"`
}

// pubRemSt is the resolved render state for the remote Publish view.
type pubRemSt struct {
	Title    string         `json:"title"`
	Sub      string         `json:"sub"`
	Switcher string         `json:"switcher"` // RAW: targetSwitcherHTML
	Hint     string         `json:"hint"`
	List     pubRemListSt   `json:"list"`
	Detail   pubRemDetailSt `json:"detail"`
}

// ── state builders ──────────────────────────────────────────────────────────────

// pubRemoteState resolves the remote view: switcher + cache snapshot + i18n.
func (u *UI) pubRemoteState(target string) pubRemSt {
	st := pubRemSt{
		Title:    i18n.T("publish.title"),
		Sub:      i18n.T("publish.subtitle"),
		Switcher: u.targetSwitcherHTML("pubtarget", "pub-target:"),
		Hint:     i18n.T("publish.remote.hint"),
	}
	u.pubRemoteEnsure(target)
	s := u.pubR()
	s.mu.Lock()
	sets, total, loading, errMsg, selID := s.sets, s.total, s.loading, s.err, s.selID
	s.mu.Unlock()
	st.List = pubRemoteListState(sets, total, loading, errMsg, selID)
	st.Detail = u.pubRemoteDetailState(selID)
	return st
}

// pubRemoteEnsure resets on target change and lazily kicks the sets + captures fetches (idempotent).
func (u *UI) pubRemoteEnsure(target string) {
	s := u.pubR()
	s.mu.Lock()
	if s.target != target {
		s.resetFor(target)
	}
	kickList := s.sets == nil && !s.loading && s.err == ""
	kickCaps := !s.capsLoaded && !s.capsLoading && s.capsErr == ""
	s.mu.Unlock()
	if kickList {
		u.pubRemoteListFetch()
	}
	if kickCaps {
		u.pubRemoteCapturesFetch()
	}
}

// ── sets list ─────────────────────────────────────────────────────────────────────

func pubRemoteListState(sets []remotectl.RecMeta, total int, loading bool, errMsg, selID string) pubRemListSt {
	st := pubRemListSt{Rows: []pubRemRowSt{}}
	switch {
	case loading && len(sets) == 0:
		st.Empty = i18n.T("remote.loading")
		return st
	case errMsg != "":
		st.Empty = i18n.T("publish.remote.error", i18n.A{"msg": errMsg})
		return st
	case len(sets) == 0:
		st.Empty = i18n.T("publish.remote.noSets")
		return st
	}
	st.Count = i18n.T("publish.setsCount", i18n.A{"count": fmt.Sprint(total)})
	if total > len(sets) {
		st.Note = i18n.T("publish.remote.showingNewest", i18n.A{"n": fmt.Sprint(len(sets)), "total": fmt.Sprint(total)})
	}
	for i := range sets {
		r := sets[i]
		title := orSetName(r.Name)
		if r.EndedAt.IsZero() {
			title = "⏺ " + title
		}
		st.Rows = append(st.Rows, pubRemRowSt{ID: r.ID, Title: title, Sub: pubRemoteSetMeta(r), Sel: r.ID == selID})
	}
	return st
}

func pubRemoteSetMeta(r remotectl.RecMeta) string {
	parts := []string{r.StartedAt.Local().Format("2006-01-02 15:04"), i18n.Tn("track", r.TrackCount)}
	if r.EndedAt.IsZero() {
		parts = append(parts, i18n.T("publish.live"))
	} else {
		parts = append(parts, r.EndedAt.Sub(r.StartedAt).Truncate(time.Minute).String())
	}
	if !r.ReconciledAt.IsZero() {
		parts = append(parts, i18n.T("publish.matched"))
	}
	return strings.Join(parts, " · ")
}

// ── detail (right pane) ────────────────────────────────────────────────────────────

func (u *UI) pubRemoteDetailState(selID string) pubRemDetailSt {
	st := pubRemDetailSt{CardTitle: i18n.T("publish.selectedSet"), Hint: i18n.T("publish.selectHint")}
	if selID == "" {
		return st
	}
	s := u.pubR()
	s.mu.Lock()
	var sel *remotectl.RecMeta
	for i := range s.sets {
		if s.sets[i].ID == selID {
			r := s.sets[i]
			sel = &r
			break
		}
	}
	tl, tlTotal, tlLoading, tlErr, tlStart := s.tl, s.tlTotal, s.tlLoading, s.tlErr, s.tlStart
	caps := pubRemoteCapsForSet(s.caps, selID)
	capsErr := s.capsErr
	s.mu.Unlock()
	if sel == nil {
		return st
	}
	r := *sel

	st.Sel = true
	st.Name, st.Meta = orSetName(r.Name), pubRemoteSetMeta(r)
	st.Actions = pubRemoteActionsState(r)
	st.Active = u.pubSubtab()
	st.CapsLbl = i18n.T("publish.capturesCount", i18n.A{"count": fmt.Sprint(len(caps))})
	st.TracksLbl = i18n.T("publish.tracklistCount", i18n.A{"count": fmt.Sprint(tlTotal)})
	if st.Active == "tracklist" {
		st.Tl = pubRemoteTracklistState(tl, tlTotal, tlLoading, tlErr, tlStart)
	} else {
		st.Caps = pubRemoteCapturesState(caps, capsErr)
	}
	return st
}

func pubRemoteActionsState(r remotectl.RecMeta) []uiBtn {
	btns := []uiBtn{{Label: i18n.T("publish.export"), Variant: "outline", Act: "pub-export:" + r.ID}}
	if !r.EndedAt.IsZero() { // match/delete only on a finished set (recording control stays local)
		btns = append(btns,
			uiBtn{Label: i18n.T("publish.matchHistory"), Variant: "secondary", Act: "pub-match:" + r.ID},
			uiBtn{Label: i18n.T("publish.delete"), Variant: "destructive", Act: "pub-del:" + r.ID})
	}
	return btns
}

func pubRemoteTracklistState(tl []recorder.Track, total int, loading bool, errMsg string, start time.Time) pubRemTlSt {
	st := pubRemTlSt{Rows: []pubRemTrackSt{}}
	switch {
	case loading && len(tl) == 0:
		st.Empty = i18n.T("remote.loading")
		return st
	case errMsg != "":
		st.Hint = i18n.T("publish.remote.error", i18n.A{"msg": errMsg})
		return st
	case len(tl) == 0:
		st.Hint = i18n.T("publish.noTracks")
		return st
	}
	if total > len(tl) {
		st.Note = i18n.T("publish.remote.tlShowing", i18n.A{"n": fmt.Sprint(len(tl)), "total": fmt.Sprint(total)})
	}
	for i := range tl {
		t := tl[i]
		off := t.StartedAt.Sub(start)
		if off < 0 {
			off = 0
		}
		st.Rows = append(st.Rows, pubRemTrackSt{Num: i + 1, Off: pubClock(off.Seconds()), Label: orTrackLine(pubTrackLine(t))})
	}
	return st
}

// pubRemoteCapturesState resolves the peer's capture rows read-only (files live on that box; open/trim
// them there). No player / file ops - those need local playback + fs access.
func pubRemoteCapturesState(caps []libdb.SetRecording, capsErr string) pubRemCapsSt {
	st := pubRemCapsSt{Caps: []string{}}
	if capsErr != "" {
		st.Hint = i18n.T("publish.remote.error", i18n.A{"msg": capsErr})
		return st
	}
	if len(caps) == 0 {
		st.Hint = i18n.T("publish.noCaptures")
		return st
	}
	st.Note = i18n.T("publish.remote.capturesNote")
	for _, s := range caps {
		kindLbl := i18n.T("publish.broadcastAudio")
		if s.Kind == libdb.SetKindOBS {
			kindLbl = i18n.T("publish.obsRecordingKind")
		}
		parts := []string{kindLbl, strings.ToUpper(s.Format)}
		if s.Bytes > 0 {
			parts = append(parts, humanBytes(uint64(s.Bytes)))
		}
		parts = append(parts, filepath.Base(s.Path))
		st.Caps = append(st.Caps, strings.Join(parts, " · "))
	}
	return st
}

// pubRemoteCapsForSet filters the peer's capture rows to the ones linked to set id.
func pubRemoteCapsForSet(all []libdb.SetRecording, id string) []libdb.SetRecording {
	var out []libdb.SetRecording
	for _, s := range all {
		if s.RecordingID == id {
			out = append(out, s)
		}
	}
	return out
}

// ── bridge ──────────────────────────────────────────────────────────────────────

func (u *UI) renderPublishRemote(target string) string {
	st := u.pubRemoteState(target)
	if zigui.Available() {
		if h, ok := zigWire("RenderPublishRemoteV2", wirePublishRemote(st), zigui.RenderPublishRemoteV2,
			zigui.RenderPublishRemote, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return pubRemoteHTML(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ─────────────────

func pubRemoteHTML(st pubRemSt) string {
	hero := `<div class="rp-card pub-hero"><div class=card-label>` + html.EscapeString(st.Title) + `</div>` +
		`<p class=page-sub>` + html.EscapeString(st.Hint) + `</p></div>`
	return panel(st.Title, st.Sub) + st.Switcher + `<div id=publish-body>` +
		hero + masterDetail(pubRemoteListHTML(st.List), pubRemoteDetailHTML(st.Detail)) + `</div>`
}

func pubRemoteListHTML(st pubRemListSt) string {
	if st.Empty != "" {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=card-label>` + html.EscapeString(st.Count) + `</div>`)
	if st.Note != "" {
		b.WriteString(`<div class=lib-remote-note>` + html.EscapeString(st.Note) + `</div>`)
	}
	for _, r := range st.Rows {
		cls := "irow pub-setrow"
		if r.Sel {
			cls += " selected"
		}
		b.WriteString(`<div class="` + cls + `" data-act="pub-select:` + html.EscapeString(r.ID) + `"><div class=irow-main>` +
			`<div class=irow-title>` + html.EscapeString(r.Title) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(r.Sub) + `</div></div></div>`)
	}
	return b.String()
}

func pubRemoteDetailHTML(st pubRemDetailSt) string {
	if !st.Sel {
		return card(st.CardTitle, "", hint("info", st.Hint))
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
		body = pubRemoteTracklistHTML(st.Tl)
	} else {
		body = pubRemoteCapturesHTML(st.Caps)
	}
	return card(st.CardTitle, "", head+tabs+`<div class=pub-subbody>`+body+`</div>`)
}

func pubRemoteTracklistHTML(st pubRemTlSt) string {
	if st.Empty != "" {
		return emptyState(st.Empty)
	}
	if st.Hint != "" {
		return hint("info", st.Hint)
	}
	var b strings.Builder
	if st.Note != "" {
		b.WriteString(`<div class=lib-remote-note>` + html.EscapeString(st.Note) + `</div>`)
	}
	b.WriteString(`<div class=pub-tracklist>`)
	for _, t := range st.Rows {
		b.WriteString(`<div class=pub-track><span class=pub-track-n>` + fmt.Sprint(t.Num) + `.</span>` +
			`<span class=pub-track-o>[` + t.Off + `]</span>` +
			`<span class=pub-track-l>` + html.EscapeString(t.Label) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func pubRemoteCapturesHTML(st pubRemCapsSt) string {
	if st.Hint != "" {
		return hint("info", st.Hint)
	}
	var b strings.Builder
	b.WriteString(`<div class=np-artist>` + html.EscapeString(st.Note) + `</div>`)
	for _, c := range st.Caps {
		b.WriteString(`<div class=pub-cap><div class=pub-cap-cap>` + html.EscapeString(c) + `</div></div>`)
	}
	return b.String()
}

// ── fetches (off-thread, cache-then-patch) ─────────────────────────────────────────

func (u *UI) pubRemoteListFetch() {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.pubR()
	s.mu.Lock()
	s.loading, s.err = true, ""
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
		defer cancel()
		res, err := client.RecList(ctx, 0, remotePubSetsPage)
		s.mu.Lock()
		s.loading = false
		if err != nil {
			s.err = err.Error()
		} else {
			s.err = ""
			s.sets, s.total = res.Sets, res.Total
		}
		s.mu.Unlock()
		u.patchMain()
	})
}

func (u *UI) pubRemoteCapturesFetch() {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.pubR()
	s.mu.Lock()
	s.capsLoading = true
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
		defer cancel()
		res, err := client.RecCaptures(ctx, 300)
		s.mu.Lock()
		s.capsLoading, s.capsLoaded = false, true
		if err != nil {
			s.capsErr = err.Error()
		} else {
			s.capsErr = ""
			s.caps = res.Captures
		}
		s.mu.Unlock()
		u.patchMain()
	})
}

func (u *UI) pubRemoteTracklistFetch(id string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.pubR()
	s.mu.Lock()
	s.tlLoading, s.tlErr = true, ""
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
		defer cancel()
		res, err := client.RecTracklist(ctx, id, 0, remotePubTrackPage)
		s.mu.Lock()
		s.tlLoading = false
		if err != nil {
			s.tlErr = err.Error()
		} else {
			s.tlErr = ""
			s.tl, s.tlTotal, s.tlStart = res.Tracks, res.Total, res.StartedAt
		}
		s.mu.Unlock()
		u.patchMain()
	})
}
