package webui

// Remote Library (webview): the Library tab driven over remotectl against a paired instance,
// mirroring the Fyne buildRemoteLibrary (view_remote_library.go). Only Browse + Collection run
// remotely; the other sections render an explicit degraded panel with the reason. The LOCAL
// path (no peer targeted) is byte-behaviour-unchanged - libBody early-returns here only when a
// peer is targeted. remotectl calls block on the network, so libBody renders synchronously from
// a per-UI cache (libRemoteSt) and background fetches patch the cache in (never blocks render).

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/transcode"
)

const remoteLibPageSize = 100

// ── target switcher + peer enumeration ─────────────────────────────────────────

type remotePeerW struct{ NodeID, Name string }

// controllablePeers returns connected peers (the switcher options). Empty when the peer link is
// off or nothing is connected, so the switcher hides itself and the tab stays local.
func (u *UI) controllablePeers() []remotePeerW {
	if u.svc.Peers == nil || u.svc.RemoteCtl == nil {
		return nil
	}
	var out []remotePeerW
	for _, c := range u.svc.Peers.Connections() {
		if c.Status == peerlink.StatusConnected {
			out = append(out, remotePeerW{NodeID: c.NodeID, Name: peerName(c.Nickname, c.NodeID)})
		}
	}
	return out
}

// libRemoteTarget returns the current control target ("" = this computer). Falls back to local
// if the targeted peer is no longer connected.
func (u *UI) libRemoteTarget() string {
	u.mu.Lock()
	t := u.remoteTarget
	u.mu.Unlock()
	if t == "" {
		return ""
	}
	for _, p := range u.controllablePeers() {
		if p.NodeID == t {
			return t
		}
	}
	return ""
}

// remoteClient binds a typed peer-control client to a node id (nil if unavailable).
func (u *UI) remoteClient(nodeID string) *remotectl.Client {
	if u.svc.RemoteCtl == nil || nodeID == "" {
		return nil
	}
	return remotectl.NewClient(u.svc.RemoteCtl, nodeID)
}

// targetSwitcherHTML renders the "Controlling [This computer ▾]" row. "" when no peer is
// connected (caller omits it). id = smartSelect id, act = dispatch prefix (trailing colon).
func (u *UI) targetSwitcherHTML(id, act string) string {
	peers := u.controllablePeers()
	if len(peers) == 0 {
		return ""
	}
	cur := u.libRemoteTarget()
	opts := func() []ssOpt {
		out := []ssOpt{{Val: "", Label: i18n.T("remote.thisComputer")}}
		for _, p := range peers {
			out = append(out, ssOpt{Val: p.NodeID, Label: "▸ " + p.Name})
		}
		return out
	}
	return `<div class=lib-target>` + smartSelect(id, i18n.T("remote.controlling"), act, cur, opts) + `</div>`
}

// ── per-UI remote cache ─────────────────────────────────────────────────────────

type libRemoteSt struct {
	mu     sync.Mutex
	target string // nodeID this cache is for

	// browse
	brDir      string
	brListing  *localmedia.Listing
	brDefaults *localmedia.DefaultPaths
	brLoading  bool
	brErr      string
	brSel      *localmedia.Entry

	// collection
	colInfo    *remotectl.LibInfo
	colInfoErr string
	colTracks  []musiclib.Track
	colTotal   int
	colOffset  int
	colQuery   string
	colLoading bool
	colErr     string
	colSel     *musiclib.Track

	transPreset string // selected transcode preset label
}

// resetFor clears the cache for a new target. Caller holds s.mu (fields cleared in place so the
// embedded mutex is never copied).
func (s *libRemoteSt) resetFor(target string) {
	s.target = target
	s.brDir, s.brListing, s.brDefaults, s.brLoading, s.brErr, s.brSel = "", nil, nil, false, "", nil
	s.colInfo, s.colInfoErr = nil, ""
	s.colTracks, s.colTotal, s.colOffset, s.colQuery, s.colLoading, s.colErr, s.colSel = nil, 0, 0, "", false, "", nil
	s.transPreset = ""
}

var (
	libRemoteMu  sync.Mutex
	libRemoteSts = map[*UI]*libRemoteSt{}
)

func (u *UI) libR() *libRemoteSt {
	libRemoteMu.Lock()
	defer libRemoteMu.Unlock()
	s := libRemoteSts[u]
	if s == nil {
		s = &libRemoteSt{}
		libRemoteSts[u] = s
	}
	return s
}

// ── body dispatch ───────────────────────────────────────────────────────────────

func (u *UI) libRemoteBody(target string) string {
	u.libRemoteEnsure(target)
	sec := u.libSectionOr()
	switch sec {
	case "collection":
		return masterDetailWide(u.libRemoteCollHTML(), `<div id=lib-detail>`+u.libRemoteDetailHTML()+`</div>`)
	case "favorites", "playlists", "history", "idmarks", "queue", "presets":
		return remoteUnavailableW(sec)
	default: // browse
		return masterDetailWide(u.libRemoteBrowseHTML(), `<div id=lib-detail>`+u.libRemoteDetailHTML()+`</div>`)
	}
}

// libRemoteEnsure resets the cache on target change and lazily kicks the active section's fetch.
// Idempotent: guarded by loading/loaded/error flags so a re-render (or a patch-driven re-entry)
// never re-fires an in-flight or completed load.
func (u *UI) libRemoteEnsure(target string) {
	sec := u.libSectionOr()
	s := u.libR()
	s.mu.Lock()
	if s.target != target {
		s.resetFor(target)
	}
	var kickBrowse, kickColl bool
	switch sec {
	case "collection":
		kickColl = s.colInfo == nil && !s.colLoading && s.colErr == "" && s.colInfoErr == ""
	case "favorites", "playlists", "history", "idmarks", "queue", "presets":
		// nothing to fetch
	default:
		kickBrowse = s.brListing == nil && !s.brLoading && s.brErr == ""
	}
	s.mu.Unlock()
	if kickBrowse {
		u.libRemoteBrowseFetch("")
	}
	if kickColl {
		u.libRemoteCollFetch()
	}
}

// ── Browse (remote fs) ──────────────────────────────────────────────────────────

func (u *UI) libRemoteBrowseHTML() string {
	s := u.libR()
	s.mu.Lock()
	listing, def := s.brListing, s.brDefaults
	loading, errMsg := s.brLoading, s.brErr
	selPath := ""
	if s.brSel != nil {
		selPath = s.brSel.Path
	}
	s.mu.Unlock()

	var b strings.Builder
	// toolbar: up + refresh + current path
	var tools []string
	if listing != nil && listing.Parent != nil {
		tools = append(tools, btn("↑ "+i18n.T("library.remote.up"), "ghost", "lib-r-nav:"+*listing.Parent, ""))
	}
	tools = append(tools, btn(i18n.T("library.remote.refresh"), "ghost", "lib-r-refresh", ""))
	b.WriteString(btnRow(tools...))
	if listing != nil {
		b.WriteString(`<div class=lib-remote-path>` + html.EscapeString(listing.Path) + `</div>`)
	}
	// quick-access chips from the peer defaults
	if def != nil {
		var chips []string
		for _, q := range []struct{ label, path string }{
			{i18n.T("library.browse.home"), def.Home}, {i18n.T("library.browse.desktop"), def.Desktop},
			{i18n.T("library.browse.downloads"), def.Downloads}, {i18n.T("library.browse.music"), def.Music},
			{i18n.T("library.browse.videos"), def.Videos}, {i18n.T("library.browse.pictures"), def.Pictures},
		} {
			if q.path == "" {
				continue
			}
			chips = append(chips, fchip(q.label, "", "lib-r-nav:"+q.path, listing != nil && listing.Path == q.path))
		}
		if len(chips) > 0 {
			b.WriteString(`<div class=lib-chips>` + strings.Join(chips, "") + `</div>`)
		}
	}
	switch {
	case loading && listing == nil:
		b.WriteString(emptyState(i18n.T("remote.loading")))
		return b.String()
	case errMsg != "":
		b.WriteString(emptyState(i18n.T("library.remote.error", i18n.A{"msg": errMsg})))
		return b.String()
	case listing == nil:
		b.WriteString(emptyState(i18n.T("remote.loading")))
		return b.String()
	}
	if listing.Error != "" {
		b.WriteString(emptyState(listing.Error))
		return b.String()
	}
	if len(listing.Entries) == 0 {
		b.WriteString(emptyState(i18n.T("library.remote.empty")))
		return b.String()
	}
	b.WriteString(`<div class=lib-remote-list>`)
	for _, e := range listing.Entries {
		act := "lib-r-file:" + e.Path
		if e.IsDirectory {
			act = "lib-r-nav:" + e.Path
		}
		cls := "irow"
		if !e.IsDirectory && e.Path == selPath {
			cls += " selected"
		}
		icon := "🗎"
		if e.IsDirectory {
			icon = "📁"
		}
		sub := ""
		if !e.IsDirectory {
			sub = `<div class=irow-sub>` + html.EscapeString(remoteFileMeta(e)) + `</div>`
		}
		b.WriteString(`<div class="` + cls + `" data-act="` + html.EscapeString(act) + `"><div class=irow-main>` +
			`<div class=irow-title>` + icon + ` ` + html.EscapeString(e.Name) + `</div>` + sub + `</div></div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class=lib-remote-count>` + html.EscapeString(i18n.T("library.remote.items", i18n.A{"n": fmt.Sprint(len(listing.Entries))})) + `</div>`)
	return b.String()
}

// remoteFileMeta formats a file row's sub-line (size + modified date).
func remoteFileMeta(e localmedia.Entry) string {
	size := humanBytes(uint64(e.SizeBytes))
	if t, err := time.Parse(time.RFC3339, e.ModifiedAt); err == nil {
		return size + " · " + t.Format("2006-01-02 15:04")
	}
	return size
}

// ── Collection (remote library) ─────────────────────────────────────────────────

func (u *UI) libRemoteCollHTML() string {
	s := u.libR()
	s.mu.Lock()
	info, infoErr := s.colInfo, s.colInfoErr
	tracks, total, offset := s.colTracks, s.colTotal, s.colOffset
	query := s.colQuery
	loading, errMsg := s.colLoading, s.colErr
	selPath := ""
	if s.colSel != nil {
		selPath = s.colSel.Path
	}
	s.mu.Unlock()

	var b strings.Builder
	// summary line
	summary := i18n.T("library.remote.col.loading")
	switch {
	case infoErr != "":
		summary = i18n.T("library.remote.error", i18n.A{"msg": infoErr})
	case info != nil && !info.HasSource:
		summary = i18n.T("library.remote.col.noSource")
	case info != nil:
		summary = i18n.T("library.remote.col.summary", i18n.A{"app": orDash(info.App), "version": info.Version, "total": fmt.Sprint(info.Total)})
	}
	b.WriteString(`<div class=lib-remote-summary>` + html.EscapeString(summary) + `</div>`)
	// search
	b.WriteString(`<form class=lib-remote-search data-act=lib-r-col-search><input class=field-input name=q placeholder="` +
		html.EscapeString(i18n.T("library.remote.col.search")) + `" value="` + html.EscapeString(query) + `" autocomplete=off></form>`)
	// paging
	if total > 0 {
		from := offset + 1
		to := offset + len(tracks)
		var pager []string
		if offset > 0 {
			pager = append(pager, btn("‹", "ghost", "lib-r-col-prev", ""))
		}
		pager = append(pager, btn("›", "ghost", "lib-r-col-next", ""))
		b.WriteString(`<div class=lib-remote-pager><span class=lib-remote-page>` +
			html.EscapeString(i18n.T("library.remote.col.page", i18n.A{"from": fmt.Sprint(from), "to": fmt.Sprint(to), "total": fmt.Sprint(total)})) +
			`</span>` + btnRow(pager...) + `</div>`)
	}
	b.WriteString(`<div class=lib-remote-note>` + html.EscapeString(i18n.T("library.remote.col.serverOrder")) + `</div>`)
	switch {
	case loading && len(tracks) == 0:
		b.WriteString(emptyState(i18n.T("remote.loading")))
		return b.String()
	case errMsg != "":
		b.WriteString(emptyState(i18n.T("library.remote.error", i18n.A{"msg": errMsg})))
		return b.String()
	case len(tracks) == 0:
		b.WriteString(emptyState(i18n.T("library.remote.col.noSource")))
		return b.String()
	}
	b.WriteString(`<div class=lib-remote-list>`)
	for i := range tracks {
		t := tracks[i]
		cls := "irow"
		if t.Path == selPath {
			cls += " selected"
		}
		title := fmt.Sprintf("%s - %s", orDash(t.Artist), orDash(t.Title))
		var meta []string
		if t.BPM > 0 {
			meta = append(meta, fmt.Sprintf("%.0f BPM", t.BPM))
		}
		if t.Key != "" {
			meta = append(meta, t.Key)
		}
		if t.Genre != "" {
			meta = append(meta, t.Genre)
		}
		b.WriteString(`<div class="` + cls + `" data-act="lib-r-track:` + html.EscapeString(t.Path) + `"><div class=irow-main>` +
			`<div class=irow-title>🎵 ` + html.EscapeString(title) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(strings.Join(meta, " · ")) + `</div></div></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── inspector (right pane) ──────────────────────────────────────────────────────

func (u *UI) libRemoteDetailHTML() string {
	sec := u.libSectionOr()
	s := u.libR()
	s.mu.Lock()
	brSel, colSel := s.brSel, s.colSel
	preset := s.transPreset
	s.mu.Unlock()
	if sec == "collection" {
		if colSel == nil {
			return `<div class=rp-card>` + emptyState(i18n.T("library.remote.selectHint")) + `</div>`
		}
		return u.libRemoteTrackDetail(*colSel)
	}
	if brSel == nil {
		return `<div class=rp-card>` + emptyState(i18n.T("library.remote.selectHint")) + `</div>`
	}
	return u.libRemoteFileDetail(*brSel, preset)
}

func (u *UI) libRemoteFileDetail(e localmedia.Entry, preset string) string {
	var b strings.Builder
	b.WriteString(`<div class=rp-card><div class=card-label>` + html.EscapeString(e.Name) + `</div>`)
	// actions
	b.WriteString(section(i18n.T("library.remote.browse.actions"),
		`<p class=page-sub>`+html.EscapeString(i18n.T("library.remote.browse.localOnly"))+`</p>`+
			btnRow(btn(i18n.T("library.remote.browse.copyPath"), "outline", "lib-r-copy:"+e.Path, ""))))
	// transcode (audio/video only)
	if e.Kind == "audio" || e.Kind == "video" {
		var custom []transcode.Preset
		if u.svc.Cfg != nil {
			custom = u.svc.Cfg.Features.Transcode.Presets
		}
		presets := transcode.AllPresets(custom)
		cur := preset
		if cur == "" && len(presets) > 0 {
			cur = presets[0].Label
		}
		sel := smartSelect("librtrans", i18n.T("library.remote.browse.preset"), "lib-r-preset:", cur, func() []ssOpt {
			out := make([]ssOpt, 0, len(presets))
			for _, p := range presets {
				out = append(out, ssOpt{Val: p.Label, Label: p.Label})
			}
			return out
		})
		b.WriteString(section(i18n.T("library.remote.browse.transcode"),
			`<p class=page-sub>`+html.EscapeString(i18n.T("library.remote.browse.transcodeHelp"))+`</p>`+sel+
				btnRow(btn(i18n.T("library.remote.browse.transcode"), "primary", "lib-r-trans:"+e.Path, ""))))
	}
	// details
	b.WriteString(section(i18n.T("library.remote.browse.details"),
		kv(i18n.T("library.remote.field.path"), e.Path)+
			kv(i18n.T("library.remote.field.size"), humanBytes(uint64(e.SizeBytes)))+
			kv(i18n.T("library.remote.field.kind"), orDash(e.Kind))+
			kv(i18n.T("library.remote.field.modified"), orDash(e.ModifiedAt))))
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) libRemoteTrackDetail(t musiclib.Track) string {
	var b strings.Builder
	title := orDash(t.Title)
	b.WriteString(`<div class=rp-card><div class=card-label>` + html.EscapeString(title) + `</div>`)
	b.WriteString(section(i18n.T("library.remote.col.tagsTitle"),
		`<p class=page-sub>`+html.EscapeString(i18n.T("library.remote.col.tagsHelp"))+`</p>`+
			btnRow(btn(i18n.T("library.remote.col.write"), "primary", "lib-r-tagwrite:"+t.Path, ""),
				btn(i18n.T("library.remote.col.revert"), "outline", "lib-r-tagrevert:"+t.Path, ""))))
	bpm := "-"
	if t.BPM > 0 {
		bpm = fmt.Sprintf("%.0f", t.BPM)
	}
	b.WriteString(section(i18n.T("library.remote.col.details"),
		kv(i18n.T("library.remote.field.path"), t.Path)+
			kv(i18n.T("library.remote.field.album"), orDash(t.Album))+
			kv(i18n.T("library.remote.field.genre"), orDash(t.Genre))+
			kv(i18n.T("library.remote.field.bpm"), bpm)+
			kv(i18n.T("library.remote.field.key"), orDash(t.Key))+
			kv(i18n.T("library.remote.field.label"), orDash(t.Label))))
	b.WriteString(`</div>`)
	return b.String()
}

// remoteUnavailableW renders an explicit degraded section (why it can't run remotely).
func remoteUnavailableW(section string) string {
	title := i18n.T("library.section." + section)
	return `<div class=rp-card><div class=card-label>` +
		html.EscapeString(i18n.T("library.remote.degrade.title", i18n.A{"section": title})) + `</div>` +
		`<p class=page-sub>` + html.EscapeString(i18n.T("library.remote.degrade."+section)) + `</p></div>`
}
