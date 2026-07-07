package webui

// Remote Publish (webview): the recording/publish cockpit driven over remotectl against a paired
// instance. Live status (REC/CAPTURE/OBS badges, now-playing, embedded player, capture file ops)
// stays on the controlled computer - it needs that box's live session + local files - so the remote
// view degrades to a recorded-sets browser: list sets, page a set's tracklist, read capture
// metadata, and Export / Match-history / Delete over the link. The LOCAL Publish path is
// byte-behaviour-unchanged; renderPublish early-returns here only when a peer is targeted. remotectl
// calls block on the network, so the body renders synchronously from a per-UI cache (pubRemoteSt)
// and background fetches patch the cache in (render never blocks).

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

// ── body ────────────────────────────────────────────────────────────────────────

func (u *UI) pubRemoteBody(target string) string {
	u.pubRemoteEnsure(target)
	s := u.pubR()
	s.mu.Lock()
	sets, total, loading, errMsg, selID := s.sets, s.total, s.loading, s.err, s.selID
	s.mu.Unlock()

	hero := `<div class="rp-card pub-hero"><div class=card-label>` + html.EscapeString(i18n.T("publish.title")) + `</div>` +
		`<p class=page-sub>` + html.EscapeString(i18n.T("publish.remote.hint")) + `</p></div>`
	return hero + masterDetail(u.pubRemoteListHTML(sets, total, loading, errMsg, selID), u.pubRemoteDetailHTML(selID))
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

func (u *UI) pubRemoteListHTML(sets []remotectl.RecMeta, total int, loading bool, errMsg, selID string) string {
	switch {
	case loading && len(sets) == 0:
		return emptyState(i18n.T("remote.loading"))
	case errMsg != "":
		return emptyState(i18n.T("publish.remote.error", i18n.A{"msg": errMsg}))
	case len(sets) == 0:
		return emptyState(i18n.T("publish.remote.noSets"))
	}
	var b strings.Builder
	b.WriteString(`<div class=card-label>` + html.EscapeString(i18n.T("publish.setsCount", i18n.A{"count": fmt.Sprint(total)})) + `</div>`)
	if total > len(sets) {
		b.WriteString(`<div class=lib-remote-note>` + html.EscapeString(i18n.T("publish.remote.showingNewest", i18n.A{"n": fmt.Sprint(len(sets)), "total": fmt.Sprint(total)})) + `</div>`)
	}
	for i := range sets {
		r := sets[i]
		title := orSetName(r.Name)
		if r.EndedAt.IsZero() {
			title = "⏺ " + title
		}
		cls := "irow pub-setrow"
		if r.ID == selID {
			cls += " selected"
		}
		b.WriteString(`<div class="` + cls + `" data-act="pub-select:` + html.EscapeString(r.ID) + `"><div class=irow-main>` +
			`<div class=irow-title>` + html.EscapeString(title) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(pubRemoteSetMeta(r)) + `</div></div></div>`)
	}
	return b.String()
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

func (u *UI) pubRemoteDetailHTML(selID string) string {
	if selID == "" {
		return card(i18n.T("publish.selectedSet"), "", hint("info", i18n.T("publish.selectHint")))
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
		return card(i18n.T("publish.selectedSet"), "", hint("info", i18n.T("publish.selectHint")))
	}
	r := *sel

	head := `<div class=pub-detail-h><div class=pub-detail-name>` + html.EscapeString(orSetName(r.Name)) + `</div>` +
		`<div class=np-artist>` + html.EscapeString(pubRemoteSetMeta(r)) + `</div>` +
		u.pubRemoteActionsHTML(r) + `</div>`
	active := u.pubSubtab()
	tabs := subTabs("pub-tab:", active,
		[2]string{"captures", i18n.T("publish.capturesCount", i18n.A{"count": fmt.Sprint(len(caps))})},
		[2]string{"tracklist", i18n.T("publish.tracklistCount", i18n.A{"count": fmt.Sprint(tlTotal)})},
	)
	var body string
	if active == "tracklist" {
		body = u.pubRemoteTracklistHTML(tl, tlTotal, tlLoading, tlErr, tlStart)
	} else {
		body = u.pubRemoteCapturesHTML(caps, capsErr)
	}
	return card(i18n.T("publish.selectedSet"), "", head+tabs+`<div class=pub-subbody>`+body+`</div>`)
}

func (u *UI) pubRemoteActionsHTML(r remotectl.RecMeta) string {
	btns := []string{btn(i18n.T("publish.export"), "outline", "pub-export:"+r.ID, "")}
	if !r.EndedAt.IsZero() { // match/delete only on a finished set (recording control stays local)
		btns = append(btns,
			btn(i18n.T("publish.matchHistory"), "secondary", "pub-match:"+r.ID, ""),
			btn(i18n.T("publish.delete"), "destructive", "pub-del:"+r.ID, ""))
	}
	return btnRow(btns...)
}

func (u *UI) pubRemoteTracklistHTML(tl []recorder.Track, total int, loading bool, errMsg string, start time.Time) string {
	switch {
	case loading && len(tl) == 0:
		return emptyState(i18n.T("remote.loading"))
	case errMsg != "":
		return hint("info", i18n.T("publish.remote.error", i18n.A{"msg": errMsg}))
	case len(tl) == 0:
		return hint("info", i18n.T("publish.noTracks"))
	}
	var b strings.Builder
	if total > len(tl) {
		b.WriteString(`<div class=lib-remote-note>` + html.EscapeString(i18n.T("publish.remote.tlShowing", i18n.A{"n": fmt.Sprint(len(tl)), "total": fmt.Sprint(total)})) + `</div>`)
	}
	b.WriteString(`<div class=pub-tracklist>`)
	for i := range tl {
		t := tl[i]
		off := t.StartedAt.Sub(start)
		if off < 0 {
			off = 0
		}
		b.WriteString(`<div class=pub-track><span class=pub-track-n>` + fmt.Sprint(i+1) + `.</span>` +
			`<span class=pub-track-o>[` + pubClock(off.Seconds()) + `]</span>` +
			`<span class=pub-track-l>` + html.EscapeString(orTrackLine(pubTrackLine(t))) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// pubRemoteCapturesHTML lists the peer's capture rows read-only (files live on that box; open/trim
// them there). No player / file ops - those need local playback + fs access.
func (u *UI) pubRemoteCapturesHTML(caps []libdb.SetRecording, capsErr string) string {
	if capsErr != "" {
		return hint("info", i18n.T("publish.remote.error", i18n.A{"msg": capsErr}))
	}
	if len(caps) == 0 {
		return hint("info", i18n.T("publish.noCaptures"))
	}
	var b strings.Builder
	b.WriteString(`<div class=np-artist>` + html.EscapeString(i18n.T("publish.remote.capturesNote")) + `</div>`)
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
		b.WriteString(`<div class=pub-cap><div class=pub-cap-cap>` + html.EscapeString(strings.Join(parts, " · ")) + `</div></div>`)
	}
	return b.String()
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
