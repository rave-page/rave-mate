package webui

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/library"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/transcode"
)

// renderLibrary is the full Library tab at parity with the Fyne studio: eight sub-sections
// (Browse · Favorites · Collection · Playlists · History · ID Marks · Queue · Presets) with a
// shared right-hand inspector (player + Go-built waveform SVG · encoding-preset builder with
// source-aware media hints · Camelot key-wheel SVG · tags · details). Merges the deprecated
// Electron Local-Studio design language (segmented chips, label+hint fields, compareQuality
// hint chips). All u.svc.* handles are nil-guarded.

// ── per-UI state (UI struct is off-limits, so hold Library state per instance here) ──

type libSel struct {
	path, kind string
	track      musiclib.Track
	inColl     bool
	size       int64
	mod        time.Time
	src        *transcode.SourceInfo // ffprobe result (nil = none/loading)
	srcLoading bool
}

type libJob struct {
	id, name, preset, status, msg string
	pct                           float64
	cancel                        func()
}

type libPlay struct {
	track  musiclib.Track
	onDisk bool
}

type libSt struct {
	mu sync.Mutex // guards everything except jobs

	kindFilter, sortBy, nameFilter, view string
	collSearch, collSort                 string
	collDesc                             bool
	collGenre, collLabel, keySel         map[string]bool
	collSel                              map[string]bool // add-to-playlist multi-select
	batch                                map[string]bool // browse batch multi-select

	sel       *libSel
	draft     transcode.Preset
	draftInit bool
	trimS     string
	trimE     string

	plSel   int64
	plCur   libdb.PlaylistRow
	plItems []libdb.PlaylistItemRow
	addto   []string // pending paths for the add-to-playlist modal

	played    []libPlay
	playSort  string
	playDesc  bool
	sessions  []musiclib.Session
	summaries []musiclib.SessionSummary
	histApps  []string // source app per session (aligned with sessions)
	histSrc   string   // "" = all, "traktor", or a master.db path
	selSess   int

	tracks []musiclib.Track
	byPath map[string]musiclib.Track
	loaded bool
	marks  *library.Bookmarks

	// smart-playlist rules editor draft
	srID     int64 // 0 = create
	srName   string
	srRules  musiclib.SmartRules // Genres field derived from srGenres on save/count
	srGenres map[string]bool

	// relocate-missing flow
	relocRoot  string
	relocMiss  []musiclib.Track
	relocCands []musiclib.Candidate
	relocSkip  map[int]bool // excluded candidate indexes
	relocMsg   string
	relocBusy  bool

	// path-picking modal drafts
	impPath, impFormat string
	movePath, moveDir  string

	jobsMu sync.Mutex
	jobs   []*libJob
}

var (
	libStMu sync.Mutex
	libSts  = map[*UI]*libSt{}
)

// lib returns this UI's Library state (created on first use).
func (u *UI) lib() *libSt {
	libStMu.Lock()
	defer libStMu.Unlock()
	s := libSts[u]
	if s == nil {
		s = &libSt{
			kindFilter: "ALL", sortBy: "Modified", view: "list", collSort: "Artist",
			collGenre: map[string]bool{}, collLabel: map[string]bool{}, keySel: map[string]bool{},
			collSel: map[string]bool{}, batch: map[string]bool{},
			byPath: map[string]musiclib.Track{},
		}
		libSts[u] = s
	}
	return s
}

// ── entry + section switch (lib-section:/lib-nav: dispatched from ui.go, keep these) ──

func (u *UI) renderLibrary() string {
	sec := u.libSectionOr()
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.library"), i18n.T("navtitle.library")))
	b.WriteString(u.targetSwitcherHTML("libtarget", "lib-target:"))
	b.WriteString(subTabs("lib-section:", sec,
		[2]string{"browse", i18n.T("library.section.browse")}, [2]string{"favorites", i18n.T("library.section.favorites")},
		[2]string{"collection", i18n.T("library.section.collection")}, [2]string{"playlists", i18n.T("library.section.playlists")},
		[2]string{"history", i18n.T("library.section.history")}, [2]string{"idmarks", i18n.T("library.section.idmarks")},
		[2]string{"queue", i18n.T("library.section.queue")}, [2]string{"presets", i18n.T("library.section.presets")}))
	b.WriteString(`<div id=lib-body>` + u.libBody() + `</div>`)
	return b.String()
}

func (u *UI) libSectionOr() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.libSection == "" {
		return "browse"
	}
	return u.libSection
}

func (u *UI) libDirOr() string {
	u.mu.Lock()
	d := u.libDir
	u.mu.Unlock()
	if d == "" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Music")
		}
		return "."
	}
	return d
}

// libSetSection flips the active sub-section (called from ui.go dispatch).
func (u *UI) libSetSection(s string) {
	u.mu.Lock()
	u.libSection = s
	u.mu.Unlock()
	u.patchMain()
}

// libNav changes the Browse cwd (called from ui.go dispatch).
func (u *UI) libNav(path string) {
	u.mu.Lock()
	u.libDir = path
	u.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libPatchBody() { u.eval("window.__patch('lib-body'," + jsQuote(u.libBody()) + ")") }

func (u *UI) libPatchDetail() {
	s := u.lib()
	s.mu.Lock()
	h := u.libDetailHTML(s)
	s.mu.Unlock()
	u.eval("window.__patch('lib-detail'," + jsQuote(h) + ")")
}

// libBody builds the active section (locks state; sub-builders are lock-free). When a peer is
// targeted it routes to the remote renderer; the local path below is byte-behaviour-unchanged.
func (u *UI) libBody() string {
	if tgt := u.libRemoteTarget(); tgt != "" {
		return u.libRemoteBody(tgt)
	}
	sec := u.libSectionOr()
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch sec {
	case "favorites":
		return u.libFavoritesHTML(s)
	case "collection":
		u.libEnsureTracks(s)
		return masterDetailWide(u.libCollectionHTML(s), u.libDetailWrap(s))
	case "playlists":
		u.libEnsureTracks(s)
		return masterDetail(u.libPlaylistsHTML(s), u.libDetailWrap(s))
	case "history":
		u.libEnsureTracks(s)
		return masterDetailWide(u.libHistoryHTML(s), u.libDetailWrap(s))
	case "idmarks":
		return u.libIDMarksHTML(s)
	case "queue":
		return `<div id=lib-queue-body>` + u.libQueueHTML() + `</div>`
	case "presets":
		return u.libPresetsHTML(s)
	default:
		u.libEnsureTracks(s)
		return masterDetailWide(u.libBrowseHTML(s), u.libDetailWrap(s))
	}
}

func (u *UI) libDetailWrap(s *libSt) string {
	return `<div id=lib-detail>` + u.libDetailHTML(s) + `</div>`
}

// libEnsureTracks lazily hydrates the collection from the persisted DB (once).
func (u *UI) libEnsureTracks(s *libSt) {
	if s.loaded || u.svc.Lib == nil {
		return
	}
	s.loaded = true
	tr, err := u.svc.Lib.LoadAllTracks()
	if err != nil {
		return
	}
	s.tracks = tr
	s.byPath = make(map[string]musiclib.Track, len(tr))
	for _, t := range tr {
		if t.Path != "" {
			s.byPath[t.Path] = t
		}
	}
}

func (u *UI) libMarks(s *libSt) *library.Bookmarks {
	if s.marks == nil {
		mf, _ := config.DataPath("bookmarks.json")
		s.marks = library.LoadBookmarks(mf)
	}
	return s.marks
}

// ── Browse ────────────────────────────────────────────────────────────────────

func (u *UI) libBrowseHTML(s *libSt) string {
	dir := u.libDirOr()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return emptyState(i18n.T("library.browse.cannotRead", i18n.A{"path": dir}))
	}
	type fe struct {
		name, path, kind string
		isDir            bool
		size             int64
		mod              time.Time
	}
	var fs []fe
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		fi, serr := e.Info()
		if serr != nil {
			continue
		}
		fs = append(fs, fe{name, full, libKind(name, fi.IsDir()), fi.IsDir(), fi.Size(), fi.ModTime()})
	}
	// filter
	q := strings.ToLower(strings.TrimSpace(s.nameFilter))
	keyed := len(s.keySel) > 0
	out := fs[:0]
	for _, e := range fs {
		if !e.isDir && s.kindFilter != "ALL" && !strings.EqualFold(e.kind, s.kindFilter) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		if keyed && !e.isDir {
			if t, ok := s.byPath[e.path]; !ok || !keyMatches(t.Key, s.keySel) {
				continue
			}
		}
		out = append(out, e)
	}
	fs = out
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.isDir != b.isDir {
			return a.isDir
		}
		switch s.sortBy {
		case "Size":
			return a.size > b.size
		case "Name":
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		default:
			return a.mod.After(b.mod)
		}
	})

	var b strings.Builder
	// breadcrumb
	b.WriteString(`<div class=lib-crumb>`)
	for i, seg := range splitSegs(dir) {
		b.WriteString(btn(seg.label, "ghost", "lib-nav:"+seg.path, ""))
		if i < len(splitSegs(dir))-1 {
			b.WriteString(`<span class=sep>›</span>`)
		}
	}
	b.WriteString(`</div>`)
	// quick-access + pinned chips
	b.WriteString(`<div class=lib-chips>`)
	home, _ := os.UserHomeDir()
	for _, q := range [][2]string{{"home", ""}, {"desktop", "Desktop"}, {"downloads", "Downloads"}, {"music", "Music"}, {"videos", "Videos"}, {"pictures", "Pictures"}} {
		p := home
		if q[1] != "" {
			p = filepath.Join(home, q[1])
		}
		b.WriteString(fchip(i18n.T("library.browse."+q[0]), "", "lib-nav:"+p, p == dir))
	}
	for _, bm := range u.libMarks(s).List() {
		b.WriteString(fchip("★ "+bm.Label, "", "lib-nav:"+bm.Path, bm.Path == dir))
	}
	b.WriteString(`</div>`)
	// toolbar
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(i18n.T("library.browse.up"), "outline", "lib-nav:"+filepath.Dir(dir), ""))
	b.WriteString(btn(i18n.T("library.browse.goto"), "ghost", "pick-dir:lib-nav-to", ""))
	b.WriteString(fieldRaw("lib-search", s.nameFilter, i18n.T("library.browse.filterName")))
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.kind")) + `</span>`)
	for _, k := range []string{"ALL", "VIDEO", "AUDIO", "IMAGE", "OTHER"} {
		b.WriteString(fchip(i18n.T("library.kind."+strings.ToLower(k)), "", "lib-kind:"+k, s.kindFilter == k))
	}
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.sort")) + `</span>`)
	for _, so := range []string{"Name", "Modified", "Size"} {
		b.WriteString(fchip(i18n.T("library.sort."+strings.ToLower(so)), "", "lib-sort:"+so, s.sortBy == so))
	}
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.view")) + `</span>`)
	b.WriteString(fchip(i18n.T("library.browse.list"), "", "lib-view:list", s.view != "grid"))
	b.WriteString(fchip(i18n.T("library.browse.grid"), "", "lib-view:grid", s.view == "grid"))
	b.WriteString(u.libKeyChip(s))
	pinLabel := i18n.T("library.browse.pin")
	for _, bm := range u.libMarks(s).List() {
		if bm.Path == dir {
			pinLabel = i18n.T("library.browse.unpin")
			break
		}
	}
	b.WriteString(btn(pinLabel, "ghost", "lib-pin", ""))
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.Tn("library.item", len(fs))) + `</span>`)
	b.WriteString(`</div>`)

	ref := s.selRef()
	if s.view == "grid" {
		b.WriteString(`<div class=lib-grid>`)
		for _, e := range clampFE(len(fs)) {
			it := fs[e]
			sub := i18n.T("library.browse.folder")
			if !it.isDir {
				sub = humanBytes(uint64(it.size)) + " · " + strings.ToUpper(it.kind)
			}
			act := "lib-nav:" + it.path
			if !it.isDir {
				act = "lib-open:" + it.path
			}
			b.WriteString(`<div class=gcard data-act="` + html.EscapeString(act) + `"><div class=gcard-ic>` + libGlyph(it.kind, it.isDir) +
				`</div><div class=gcard-t>` + html.EscapeString(it.name) + `</div><div class=gcard-s>` + html.EscapeString(sub) + `</div></div>`)
		}
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`<div class=trk-table>`)
		for _, e := range clampFE(len(fs)) {
			it := fs[e]
			if it.isDir {
				b.WriteString(`<div class=trk-row data-act="lib-nav:` + html.EscapeString(it.path) + `"><span class=trk-ic>📁</span>` +
					`<span class=trk-main><span class=trk-title>` + html.EscapeString(it.name) + `</span></span></div>`)
				continue
			}
			var kp string
			if t, ok := s.byPath[it.path]; ok {
				kp = keyPillHTML(t.Key, ref)
			}
			chk := ""
			if s.batch[it.path] {
				chk = " checked"
			}
			selCls := ""
			if s.sel != nil && s.sel.path == it.path {
				selCls = " sel"
			}
			b.WriteString(`<div class="trk-row` + selCls + `">` +
				`<input type=checkbox data-act="lib-batch:` + html.EscapeString(it.path) + `"` + chk + `>` +
				`<span class=trk-ic data-act="lib-open:` + html.EscapeString(it.path) + `">` + libGlyph(it.kind, false) + `</span>` +
				`<span class=trk-main data-act="lib-open:` + html.EscapeString(it.path) + `"><span class=trk-title>` + html.EscapeString(it.name) +
				`</span><span class=trk-sub>` + humanBytes(uint64(it.size)) + " · " + it.mod.Format("2006-01-02") + `</span></span>` +
				`<span class=trk-key>` + kp + `</span>` +
				btn("⋯", "ghost", "lib-ctx:"+it.path, "") + `</div>`)
		}
		b.WriteString(`</div>`)
	}
	if len(fs) > libMaxRows {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.showingFirst", i18n.A{"shown": fmt.Sprint(libMaxRows), "total": fmt.Sprint(len(fs))})) + `</p>`)
	}
	b.WriteString(u.libBatchBar(s))
	return b.String()
}

func (u *UI) libBatchBar(s *libSt) string {
	if len(s.batch) == 0 {
		return ""
	}
	return `<div class=batchbar><span class=cnt>` + html.EscapeString(i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.batch))})) + `</span>` +
		btn(i18n.T("library.batch.waveforms"), "outline", "lib-batch-run:peaks", "") + btn(i18n.T("library.batch.tags"), "outline", "lib-batch-run:tags", "") +
		btn(i18n.T("library.batch.fingerprint"), "outline", "lib-batch-run:fingerprint", "") + btn(i18n.T("library.batch.transcode"), "primary", "lib-batch-run:transcode", "") +
		btn(i18n.T("library.clear"), "ghost", "lib-batch-clear", "") + `</div>`
}

// ── Favorites ───────────────────────────────────────────────────────────────

func (u *UI) libFavoritesHTML(s *libSt) string {
	marks := u.libMarks(s).List()
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.favorites.desc")) + `</p>`)
	if len(marks) == 0 {
		return b.String() + emptyState(i18n.T("library.favorites.empty"))
	}
	b.WriteString(`<div class="rp-card">`)
	for _, m := range marks {
		b.WriteString(itemRow(m.Label, m.Path, btn(i18n.T("library.open"), "outline", "lib-nav:"+m.Path, ""), btn(i18n.T("library.favorites.unpin"), "ghost", "lib-unpin:"+m.Path, "")))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Collection ──────────────────────────────────────────────────────────────

func (u *UI) libCollectionHTML(s *libSt) string {
	if u.svc.Lib == nil {
		return emptyState(i18n.T("library.dbUnavailable"))
	}
	var b strings.Builder
	// management toolbar
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(i18n.T("library.coll.import"), "primary", "lib-import", ""))
	b.WriteString(btn(i18n.T("library.coll.backup"), "outline", "lib-backup", ""))
	b.WriteString(btn(i18n.T("library.coll.scan"), "outline", "lib-scan", ""))
	b.WriteString(btn(i18n.T("library.coll.cleanup"), "outline", "lib-cleanup", ""))
	b.WriteString(btn(i18n.T("library.coll.relocate"), "outline", "lib-relocate", ""))
	b.WriteString(btn(i18n.T("library.coll.export"), "outline", "lib-export", ""))
	b.WriteString(btn(i18n.T("library.coll.sync"), "outline", "lib-sync", ""))
	b.WriteString(`</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.coll.desc")) + `</p>`)

	// filters
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(fieldRaw("lib-coll-search", s.collSearch, i18n.T("library.coll.search")))
	b.WriteString(u.libKeyChip(s))
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.sort")) + `</span>`)
	for _, so := range []string{"Artist", "Title", "BPM", "Key", "Genre", "Label", "Rating", "Plays"} {
		b.WriteString(fchip(i18n.T("library.collsort."+strings.ToLower(so)), "", "lib-coll-sort:"+so, s.collSort == so))
	}
	b.WriteString(fchip(sortDir(s.collDesc), "", "lib-coll-dir", false))
	if len(s.collGenre)+len(s.collLabel)+len(s.keySel) > 0 || s.collSearch != "" {
		b.WriteString(btn(i18n.T("library.clear"), "ghost", "lib-clearfilters", ""))
	}
	b.WriteString(`</div>`)
	// genre / label chips (top families)
	b.WriteString(`<div class=lib-toolbar><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.genre")) + `</span>`)
	for _, gc := range distinctCounts(s.tracks, func(t musiclib.Track) string { return musiclib.GenreFamily(t.Genre) }, 8) {
		b.WriteString(fchipN(gc[0], gc[1], "lib-genre:"+gc[0], s.collGenre[gc[0]]))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class=lib-toolbar><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.label")) + `</span>`)
	for _, lc := range distinctCounts(s.tracks, func(t musiclib.Track) string { return strings.TrimSpace(t.Label) }, 8) {
		b.WriteString(fchipN(lc[0], lc[1], "lib-label:"+lc[0], s.collLabel[lc[0]]))
	}
	b.WriteString(`</div>`)

	// filtered + sorted view
	shown := s.collView()
	total := len(shown)
	b.WriteString(`<div class=trk-h><span style="flex:1">` + html.EscapeString(i18n.T("library.coll.trackHeader", i18n.A{"count": fmt.Sprint(total)})) + `</span>` +
		`<span class=trk-bpm>` + html.EscapeString(i18n.T("library.col.bpm")) + `</span><span class=trk-dur>` + html.EscapeString(i18n.T("library.col.time")) + `</span><span class=trk-keyh>` + html.EscapeString(i18n.T("library.col.key")) + `</span></div>`)
	b.WriteString(`<div class=trk-table>`)
	ref := s.selRef()
	for i, ti := range shown {
		if i >= libMaxRows {
			break
		}
		t := s.tracks[ti]
		onDisk := pathOnDisk(t.Path)
		chk := ""
		if s.collSel[t.Path] {
			chk = " checked"
		}
		selCls := ""
		if s.sel != nil && s.sel.path == t.Path {
			selCls = " sel"
		}
		ic := `<span class=trk-ic>🎵</span>`
		if !onDisk {
			ic = `<span class="trk-ic warn">⚠</span>`
		}
		bpm, dur := "", ""
		if t.BPM > 0 {
			bpm = fmt.Sprintf("%.0f", t.BPM)
		}
		if t.DurationSec > 0 {
			dur = mmss(t.DurationSec)
		}
		b.WriteString(`<div class="trk-row` + selCls + `">` +
			`<input type=checkbox data-act="lib-collsel:` + html.EscapeString(t.Path) + `"` + chk + `>` + ic +
			`<span class=trk-main data-act="lib-track:` + html.EscapeString(t.Path) + `"><span class=trk-title>` +
			html.EscapeString(trackTitle(t)) + `</span><span class=trk-sub>` +
			html.EscapeString(trackMetaSub(t)) + `</span></span>` +
			`<span class=trk-bpm>` + bpm + `</span><span class=trk-dur>` + dur + `</span>` +
			`<span class=trk-key>` + keyPillHTML(t.Key, ref) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if total == 0 {
		b.WriteString(emptyState(i18n.T("library.coll.empty")))
	} else if total > libMaxRows {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.showingFirst", i18n.A{"shown": fmt.Sprint(libMaxRows), "total": fmt.Sprint(total)})) + `</p>`)
	}
	// add-to-playlist bar
	if len(s.collSel) > 0 {
		b.WriteString(`<div class=batchbar><span class=cnt>` + html.EscapeString(i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.collSel))})) + `</span>` +
			btn(i18n.T("library.addToPlaylist"), "primary", "lib-addto", "") + btn(i18n.T("library.clear"), "ghost", "lib-collsel-clear", "") + `</div>`)
	}
	return b.String()
}

// collView returns filtered+sorted indices into s.tracks.
func (s *libSt) collView() []int {
	q := strings.ToLower(strings.TrimSpace(s.collSearch))
	var out []int
	for i, t := range s.tracks {
		if q != "" && !strings.Contains(strings.ToLower(t.Title), q) && !strings.Contains(strings.ToLower(t.Artist), q) {
			continue
		}
		if !keyMatches(t.Key, s.keySel) {
			continue
		}
		if !inFilter(musiclib.GenreFamily(t.Genre), s.collGenre) {
			continue
		}
		if !inFilter(strings.TrimSpace(t.Label), s.collLabel) {
			continue
		}
		out = append(out, i)
	}
	sortIdx(out, func(i int) musiclib.Track { return s.tracks[i] }, s.collSort, s.collDesc)
	return out
}

// ── Playlists ───────────────────────────────────────────────────────────────

func (u *UI) libPlaylistsHTML(s *libSt) string {
	if u.svc.Lib == nil {
		return emptyState(i18n.T("library.dbUnavailable"))
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(i18n.T("library.pl.new"), "primary", "lib-pl-new", ""))
	b.WriteString(btn(i18n.T("library.pl.newSmart"), "outline", "lib-pl-newsmart", ""))
	if u.svc.Syncer != nil {
		b.WriteString(btn(i18n.T("library.pl.cloudStatus"), "ghost", "lib-pl-cloud", ""))
		b.WriteString(btn(i18n.T("library.pl.syncAll"), "ghost", "lib-pl-syncall", ""))
		b.WriteString(btn(i18n.T("library.pl.remote"), "ghost", "lib-pl-remote", ""))
	}
	b.WriteString(`</div>`)
	if len(rows) == 0 {
		b.WriteString(emptyState(i18n.T("library.pl.empty")))
	}
	b.WriteString(`<div class="rp-card">`)
	for _, p := range rows {
		ic := "🎵"
		sub := fmt.Sprint(p.Kind) + " · " + i18n.Tn("track", p.TrackCount)
		switch p.Kind {
		case libdb.PlaylistSmart:
			ic = "⚡"
			if r, ok := libParseRules(p.Rules); ok {
				sub = i18n.T("library.pl.smartSub", i18n.A{"count": fmt.Sprint(len(musiclib.FilterSmart(s.tracks, r))), "desc": r.Describe()})
			}
		case libdb.PlaylistImported:
			ic = "⤓"
		}
		selCls := ""
		if p.ID == s.plSel {
			selCls = "primary"
		} else {
			selCls = "outline"
		}
		b.WriteString(itemRow(ic+" "+p.Name, sub, btn(i18n.T("library.open"), selCls, fmt.Sprintf("lib-pl:%d", p.ID), "")))
	}
	b.WriteString(`</div>`)

	// open playlist tracks
	if s.plSel != 0 {
		b.WriteString(u.libPlaylistOpenHTML(s))
	}
	return b.String()
}

func (u *UI) libPlaylistOpenHTML(s *libSt) string {
	p := s.plCur
	manual := p.Kind == libdb.PlaylistManual
	var b strings.Builder
	b.WriteString(section(p.Name, ""))
	if p.Kind == libdb.PlaylistSmart {
		if r, ok := libParseRules(p.Rules); ok {
			b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.pl.smartMatchLive", i18n.A{"desc": r.Describe(), "count": fmt.Sprint(len(s.plItems))})) + `</p>`)
		}
	}
	b.WriteString(`<div class=lib-toolbar>`)
	if p.Kind != libdb.PlaylistImported {
		b.WriteString(btn(i18n.T("library.pl.rename"), "outline", fmt.Sprintf("lib-pl-rename:%d", p.ID), ""))
	}
	if p.Kind == libdb.PlaylistSmart {
		b.WriteString(btn(i18n.T("library.pl.editRules"), "outline", fmt.Sprintf("lib-sr-edit:%d", p.ID), ""))
	}
	b.WriteString(btn(i18n.T("library.pl.exportM3U"), "outline", fmt.Sprintf("lib-pl-export:%d", p.ID), ""))
	b.WriteString(btn(i18n.T("library.pl.exportM3UAs"), "ghost", fmt.Sprintf("pick-save:m3u8:lib-pl-exportas:%d", p.ID), ""))
	if !manual {
		b.WriteString(btn(i18n.T("library.pl.dupManual"), "outline", fmt.Sprintf("lib-pl-dup:%d", p.ID), ""))
	}
	b.WriteString(btn(i18n.T("common.delete"), "destructive", fmt.Sprintf("lib-pl-del:%d", p.ID), ""))
	if u.svc.Syncer != nil {
		b.WriteString(btn(i18n.T("library.pl.push"), "ghost", fmt.Sprintf("lib-pl-push:%d", p.ID), ""))
		b.WriteString(btn(i18n.T("library.pl.pull"), "ghost", fmt.Sprintf("lib-pl-pull:%d", p.ID), ""))
		b.WriteString(btn(i18n.T("library.pl.unlink"), "ghost", fmt.Sprintf("lib-pl-unlink:%d", p.ID), ""))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class=trk-table>`)
	ref := s.selRef()
	for i, it := range s.plItems {
		title := it.Title + " - " + it.Artist
		if it.Path != "" {
			if t, ok := s.byPath[it.Path]; ok {
				title = strOrDash(t.Artist) + " - " + strOrDash(t.Title)
			} else if title == " - " {
				title = filepath.Base(it.Path)
			}
		}
		var kp string
		if t, ok := s.byPath[it.Path]; ok {
			kp = keyPillHTML(t.Key, ref)
		}
		var actions string
		if manual {
			actions = btn("↑", "ghost", fmt.Sprintf("lib-pl-up:%d", i), "") + btn("↓", "ghost", fmt.Sprintf("lib-pl-down:%d", i), "") +
				btn("✕", "ghost", "lib-pl-rm:"+it.Path, "")
		}
		sel := ""
		if it.Path != "" {
			sel = `data-act="lib-track:` + html.EscapeString(it.Path) + `"`
		}
		b.WriteString(`<div class=trk-row><span class=trk-pos>` + fmt.Sprintf("%d", i+1) + `</span>` +
			`<span class=trk-main ` + sel + `><span class=trk-title>` + html.EscapeString(title) + `</span></span>` +
			`<span class=trk-key>` + kp + `</span>` + actions + `</div>`)
	}
	b.WriteString(`</div>`)
	if len(s.plItems) == 0 {
		b.WriteString(emptyState(i18n.T("library.pl.emptyTracks")))
	}
	return b.String()
}

// ── History ─────────────────────────────────────────────────────────────────

func (u *UI) libHistoryHTML(s *libSt) string {
	var b strings.Builder
	// source picker: every DJ software with a play-history model (Traktor NML history
	// dir, Rekordbox master.db djmdHistory). VirtualDJ keeps no session history.
	b.WriteString(`<div class=lib-toolbar>` + btn(i18n.T("library.hist.load"), "primary", "lib-hist-load", "") +
		smartSelect("lib-hist-src", "", "lib-hist-srcpick", s.histSrc, libHistSources) + `</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.hist.desc")) + `</p>`)
	if len(s.summaries) == 0 {
		b.WriteString(emptyState(i18n.T("library.hist.empty")))
	} else {
		b.WriteString(`<div class="rp-card">`)
		for i, sm := range s.summaries {
			v := "outline"
			if i == s.selSess {
				v = "primary"
			}
			sub := i18n.Tn("track", sm.TrackCount) + " · " + fmtDurCoarse(sm.TotalDurationSec)
			if i < len(s.histApps) && s.histApps[i] != "" {
				sub = s.histApps[i] + " · " + sub
			}
			b.WriteString(itemRow(sm.StartedAt.Format("2006-01-02 15:04"), sub,
				btn(i18n.T("library.open"), v, fmt.Sprintf("lib-session:%d", i), "")))
		}
		b.WriteString(`</div>`)
	}
	if len(s.played) > 0 {
		b.WriteString(`<div class=lib-toolbar><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.played")) + `</span>`)
		for _, so := range []string{"Play order", "Artist", "Title", "BPM", "Key", "Genre", "Rating", "Plays"} {
			cur := s.playSort
			if cur == "" {
				cur = "Play order"
			}
			b.WriteString(fchip(i18n.T("library.playsort."+strings.ToLower(strings.ReplaceAll(so, " ", ""))), "", "lib-play-sort:"+so, cur == so))
		}
		b.WriteString(fchip(sortDir(s.playDesc), "", "lib-play-dir", false))
		b.WriteString(`</div>`)
		b.WriteString(`<div class=trk-table>`)
		ref := s.selRef()
		for _, pi := range s.playView() {
			p := s.played[pi]
			ic := `<span class=trk-ic>🎵</span>`
			if !p.onDisk {
				ic = `<span class="trk-ic warn">⚠</span>`
			}
			b.WriteString(`<div class=trk-row>` + ic + `<span class=trk-main data-act="lib-track:` + html.EscapeString(p.track.Path) +
				`"><span class=trk-title>` + html.EscapeString(strOrDash(p.track.Artist)+" - "+strOrDash(p.track.Title)) +
				`</span><span class=trk-sub>` + html.EscapeString(trackMeta(p.track)) + `</span></span><span class=trk-key>` +
				keyPillHTML(p.track.Key, ref) + `</span></div>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

func (s *libSt) playView() []int {
	var out []int
	for i, p := range s.played {
		if keyMatches(p.track.Key, s.keySel) {
			out = append(out, i)
		}
	}
	sortIdx(out, func(i int) musiclib.Track { return s.played[i].track }, s.playSort, s.playDesc)
	return out
}

// ── ID Marks ────────────────────────────────────────────────────────────────

func (u *UI) libIDMarksHTML(s *libSt) string {
	st := u.svc.IDMarks
	if st == nil {
		return emptyState(i18n.T("library.idmarks.unavailable"))
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>` + btn(i18n.T("library.idmarks.markFile"), "primary", "pick-file:lib-id-addpath", "") +
		btn(i18n.T("library.idmarks.markFolder"), "outline", "pick-dir:lib-id-addpath", "") + btn(i18n.T("library.idmarks.typePath"), "ghost", "lib-id-manual", "") + `</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.idmarks.desc")) + `</p>`)
	entries := st.List()
	if len(entries) == 0 {
		return b.String() + emptyState(i18n.T("library.idmarks.empty"))
	}
	b.WriteString(`<div class="rp-card">`)
	for _, e := range entries {
		b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(e.Path) + `</span>` +
			toggleRow(i18n.T("library.idmarks.showArtist"), "lib-id-artist:"+e.Path, e.ShowArtist) +
			toggleRow(i18n.T("library.idmarks.showLabel"), "lib-id-label:"+e.Path, e.ShowLabel) +
			btn(i18n.T("library.remove"), "ghost", "lib-id-del:"+e.Path, "") + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Queue ───────────────────────────────────────────────────────────────────

func (u *UI) libQueueHTML() string {
	s := u.lib()
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.queue.desc")) + `</p>`)
	if len(s.jobs) == 0 {
		return b.String() + emptyState(i18n.T("library.queue.empty"))
	}
	for i, j := range s.jobs {
		var trail string
		if j.status == "running" || j.status == "queued" {
			trail = btn(i18n.T("common.cancel"), "ghost", fmt.Sprintf("lib-job-cancel:%d", i), "")
		} else {
			trail = badge(j.status, jobBadge(j.status))
		}
		label := j.name + " · " + j.preset
		b.WriteString(`<div class=qjob><div class=qjob-h><span class=qjob-t>` + html.EscapeString(label) + `</span>` + trail + `</div>`)
		b.WriteString(progressBar(j.pct/100, fmt.Sprintf("%s · %.0f%%", j.status, j.pct)))
		if j.msg != "" {
			b.WriteString(`<p class=page-sub>` + html.EscapeString(j.msg) + `</p>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

func jobBadge(st string) string {
	switch st {
	case "done":
		return "success"
	case "error":
		return "error"
	case "canceled":
		return "secondary"
	}
	return "info"
}

// ── Presets catalog ─────────────────────────────────────────────────────────

func (u *UI) libPresetsHTML(s *libSt) string {
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>` + btn(i18n.T("library.preset.new"), "primary", "lib-pset-new", "") + `</div>`)
	b.WriteString(section(i18n.T("library.preset.yours"), ""))
	if len(custom) == 0 {
		b.WriteString(emptyState(i18n.T("library.preset.emptyCustom")))
	} else {
		b.WriteString(`<div class=pcards>`)
		for _, p := range custom {
			b.WriteString(card(p.Label, badge(i18n.T("library.preset.custom"), "info"), `<p class=page-sub>`+html.EscapeString(p.Desc)+`</p>`+
				btnRow(btn(i18n.T("library.edit"), "outline", "lib-pset-edit:"+p.ID, ""), btn(i18n.T("library.duplicate"), "ghost", "lib-pset-dup:"+p.ID, ""),
					btn(i18n.T("common.delete"), "destructive", "lib-pset-del:"+p.ID, ""))))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(section(i18n.T("library.preset.builtins"), ""))
	b.WriteString(`<div class=pcards>`)
	for _, p := range transcode.Builtins {
		b.WriteString(card(p.Label, badge(i18n.T("library.preset.builtin"), "secondary"), `<p class=page-sub>`+html.EscapeString(p.Desc)+`</p>`+
			btn(i18n.T("library.preset.dupEdit"), "outline", "lib-pset-dup:"+p.ID, "")))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Inspector (detail pane) ─────────────────────────────────────────────────

func (u *UI) libDetailHTML(s *libSt) string {
	sel := s.sel
	if sel == nil {
		return emptyState(i18n.T("library.insp.empty"))
	}
	var b strings.Builder
	title := sel.track.Title
	if title == "" {
		title = filepath.Base(sel.path)
	}
	sub := ""
	if sel.size > 0 || !sel.mod.IsZero() {
		sub = humanBytes(uint64(sel.size)) + " · " + strings.ToUpper(sel.kind)
		if !sel.mod.IsZero() {
			sub += " · " + sel.mod.Format("2006-01-02 15:04")
		}
	} else {
		sub = strOrDash(sel.track.Artist)
	}
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + html.EscapeString(i18n.T("library.insp.selected")) + `</div><div class=insp-title>` +
		html.EscapeString(title) + `</div><div class=insp-sub>` + html.EscapeString(sub) + `</div></div>`)

	onDisk := pathOnDisk(sel.path)
	// ACTIONS
	act := `<div class=btn-row>` + btn(i18n.T("library.open"), "outline", "lib-openext:"+sel.path, "") + btn(i18n.T("library.reveal"), "outline", "lib-reveal:"+sel.path, "") +
		btn(i18n.T("library.metadata"), "ghost", "lib-probe:"+sel.path, "") + btn(i18n.T("library.copyPath"), "ghost", "copy", "") + `</div>`
	if !onDisk {
		act = `<p class=page-sub>` + html.EscapeString(i18n.T("library.insp.missing")) + `</p>` + act
	}
	b.WriteString(inspSec(i18n.T("library.insp.actions"), act))

	// PLAYER + waveform (audio on disk) - the unified media player/editor (player.go)
	if onDisk && sel.kind == "audio" {
		u.mpEnsureFile("library", sel.path, sel.track)
		b.WriteString(inspSec(i18n.T("library.insp.player"), u.mpHTML("library")))
	}
	// ENCODE builder (audio + video)
	if sel.kind == "audio" || sel.kind == "video" {
		b.WriteString(inspSec(i18n.T("library.insp.encoding"), u.libEncodeHTML(s, sel)))
	}
	// HARMONIC key-wheel (audio with a key)
	if sel.kind == "audio" {
		if _, ok := musiclib.ParseKey(sel.track.Key); ok {
			b.WriteString(inspSec(i18n.T("library.insp.harmonic"), u.libHarmonicHTML(s, sel)))
		}
	}
	// TAGS (collection audio)
	if sel.inColl && sel.kind == "audio" {
		b.WriteString(inspSec(i18n.T("library.insp.tags"), `<p class=page-sub>`+html.EscapeString(i18n.T("library.insp.tagsDesc"))+`</p>`+
			btnRow(btn(i18n.T("library.insp.writeTags"), "primary", "lib-tags-write:"+sel.path, ""), btn(i18n.T("library.revert"), "ghost", "lib-tags-revert:"+sel.path, ""))))
	}
	// PLAYLISTS membership
	if sel.kind == "audio" {
		b.WriteString(inspSec(i18n.T("library.insp.playlists"), u.libTrackPlaylistsHTML(sel.path)))
	}
	// DETAILS
	b.WriteString(inspSec(i18n.T("library.insp.details"), u.libDetailsMeta(sel.track)))
	return b.String()
}

func (u *UI) libHarmonicHTML(s *libSt, sel *libSel) string {
	k, _ := musiclib.ParseKey(sel.track.Key)
	census := libKeyCensus(s.tracks)
	return `<p class=page-sub>` + html.EscapeString(i18n.T("library.harmonic.desc")) + `</p>` +
		keywheelSVG(&k, s.keySel, census) + kwLegend() +
		btnRow(btn(i18n.T("library.harmonic.show"), "outline", "lib-key-harmonic:"+k.Camelot(), ""), btn(i18n.T("library.harmonic.clear"), "ghost", "lib-key-clear", ""))
}

func (u *UI) libTrackPlaylistsHTML(path string) string {
	if u.svc.Lib == nil {
		return `<p class=page-sub>-</p>`
	}
	pls, _ := u.svc.Lib.PlaylistsForTrack(path)
	var chips string
	for _, p := range pls {
		chips += badge(p.Name, "secondary")
	}
	if chips == "" {
		chips = `<span class=page-sub>` + html.EscapeString(i18n.T("library.insp.notInPlaylist")) + `</span>`
	}
	return chips + `<div class=btn-row>` + btn(i18n.T("library.addToPlaylist"), "outline", "lib-track-addto:"+path, "") + `</div>`
}

func (u *UI) libDetailsMeta(t musiclib.Track) string {
	var b strings.Builder
	row := func(k, v string) {
		if v != "" {
			b.WriteString(kv(k, v))
		}
	}
	row(i18n.T("library.meta.path"), t.Path)
	row(i18n.T("library.meta.artist"), t.Artist)
	row(i18n.T("library.meta.album"), t.Album)
	row(i18n.T("library.meta.genre"), t.Genre)
	row(i18n.T("library.meta.label"), t.Label)
	if t.BPM > 0 {
		row(i18n.T("library.meta.bpm"), fmt.Sprintf("%.0f", t.BPM))
	}
	row(i18n.T("library.meta.key"), t.Key)
	if t.DurationSec > 0 {
		row(i18n.T("library.meta.duration"), mmss(t.DurationSec))
	}
	if t.BitrateBps > 0 {
		row(i18n.T("library.meta.bitrate"), i18n.T("library.meta.kbps", i18n.A{"count": fmt.Sprint(t.BitrateBps / 1000)}))
	}
	if t.Rating > 0 {
		row(i18n.T("library.meta.rating"), strings.Repeat("★", t.Rating))
	}
	if t.PlayCount > 0 {
		row(i18n.T("library.meta.plays"), fmt.Sprintf("%d", t.PlayCount))
	}
	if len(t.Cues) > 0 {
		row(i18n.T("library.meta.cues"), fmt.Sprintf("%d", len(t.Cues)))
	}
	if len(t.Beatgrid) > 0 {
		row(i18n.T("library.meta.beatgrid"), i18n.T("library.meta.markers", i18n.A{"count": fmt.Sprint(len(t.Beatgrid))}))
	}
	return b.String()
}

// ── Encoding-preset builder + dynamic media hints (Electron Local-Studio merge) ──

func (u *UI) libEncodeHTML(s *libSt, sel *libSel) string {
	if !s.draftInit {
		s.libSeedDraft(sel)
	}
	d := s.draft
	audioOnly := sel.kind == "audio"
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	var b strings.Builder
	// preset picker (rich rows: description sub-line + container badge)
	b.WriteString(smartSelect("lib-preset", i18n.T("library.enc.preset"), "lib-preset:", d.ID, func() []ssOpt {
		var out []ssOpt
		for _, p := range transcode.AllPresets(custom) {
			if audioOnly && !p.IsAudioOnly() {
				continue
			}
			out = append(out, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(p.Container)})
		}
		return out
	}))
	if d.Desc != "" {
		b.WriteString(`<div class=pb-hint>` + html.EscapeString(d.Desc) + `</div>`)
	}

	// dynamic media hints (source-aware)
	if hints := u.libMediaHints(sel, d); hints != "" {
		b.WriteString(`<div class=mediahints>` + hints + `</div>`)
	}

	b.WriteString(`<div class=pbuilder>`)
	b.WriteString(pbSelect(i18n.T("library.enc.container"), "lib-pf:container", containerOpts, d.Container))
	if !audioOnly {
		b.WriteString(`<div class=pb-grp>`)
		b.WriteString(pbSelect(i18n.T("library.enc.videoCodec"), "lib-pf:vcodec", videoCodecOpts, d.VideoCodec))
		b.WriteString(pbSelect(i18n.T("library.enc.accel"), "lib-pf:accel", accelOpts(), d.Accel))
		// quality profiles
		b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(i18n.T("library.enc.qualityProfile")) + `</div><div class=seg>`)
		for _, pr := range transcode.Profiles {
			b.WriteString(fchip(pr, pr, "lib-pf:profile", false))
		}
		b.WriteString(`</div><div class=pb-hint>` + html.EscapeString(profileHint(profileOfDraft(d))) + `</div></div>`)
		b.WriteString(pbSelect(i18n.T("library.enc.rateMode"), "lib-pf:ratemode", [][2]string{{"crf", i18n.T("library.enc.rateCRF")}, {"bitrate", i18n.T("library.enc.rateBitrate")}}, d.RateMode))
		if d.RateMode == "bitrate" {
			b.WriteString(pbField(i18n.T("library.enc.bitrateK"), "lib-pf:bitratek", strconv.Itoa(d.BitrateK), "number", i18n.T("library.enc.bitrateKHint")))
		} else {
			b.WriteString(pbField(i18n.T("library.enc.crf"), "lib-pf:crf", strconv.Itoa(d.CRF), "number", crfHint(d.VideoCodec)))
		}
		b.WriteString(pbSelect(i18n.T("library.enc.resolution"), "lib-pf:res", resOpts, resLabel(d.Width, d.Height)))
		b.WriteString(pbField(i18n.T("library.enc.fps"), "lib-pf:fps", trimNum(d.FPS), "number", ""))
		b.WriteString(`</div>`)
	}
	// audio section
	b.WriteString(`<div class=pb-grp>`)
	b.WriteString(pbSelect(i18n.T("library.enc.audioCodec"), "lib-pf:acodec", audioCodecOpts, d.AudioCodec))
	b.WriteString(pbField(i18n.T("library.enc.audioBitrate"), "lib-pf:abitratek", strconv.Itoa(d.AudioBitrateK), "number", audioCapHint(d.AudioCodec)))
	b.WriteString(pbSelect(i18n.T("library.enc.channels"), "lib-pf:channels", [][2]string{{"0", i18n.T("library.enc.source")}, {"1", i18n.T("library.enc.mono")}, {"2", i18n.T("library.enc.stereo")}}, strconv.Itoa(d.Channels)))
	b.WriteString(pbSelect(i18n.T("library.enc.sampleRate"), "lib-pf:samplerate", [][2]string{{"0", i18n.T("library.enc.source")}, {"44100", "44.1 kHz"}, {"48000", "48 kHz"}, {"96000", "96 kHz"}}, strconv.Itoa(d.SampleRate)))
	b.WriteString(`</div>`)
	// loudness
	b.WriteString(`<div class=pb-grp>`)
	b.WriteString(toggleRow(i18n.T("library.enc.normalize"), "lib-pf:loudon", d.LoudnessOn))
	if d.LoudnessOn {
		b.WriteString(pbField(i18n.T("library.enc.lufsTarget"), "lib-pf:loudi", trimNum(d.LoudnessI), "number", i18n.T("library.enc.lufsHint")))
		b.WriteString(pbField(i18n.T("library.enc.truePeak"), "lib-pf:loudtp", trimNum(d.LoudnessTP), "number", ""))
		b.WriteString(toggleRow(i18n.T("library.enc.raiseQuiet"), "lib-pf:loudraise", d.LoudnessRaiseOnly))
	}
	b.WriteString(`</div>`)
	// trim + start
	b.WriteString(pbField(i18n.T("library.enc.trimStart"), "lib-trim-s", s.trimS, "number", ""))
	b.WriteString(pbField(i18n.T("library.enc.trimEnd"), "lib-trim-e", s.trimE, "number", ""))
	b.WriteString(`</div>`)
	b.WriteString(`<div class=btn-row>` + btn(i18n.T("library.enc.start"), "primary", "lib-transcode", "") +
		btn(i18n.T("library.enc.savePreset"), "outline", "lib-pset-save", "") + btn(i18n.T("library.enc.saveAsNew"), "ghost", "lib-pset-saveas", "") + `</div>`)
	return b.String()
}

// libMediaHints renders the source-aware compareQuality chips (calm, factual - "adds no quality").
func (u *UI) libMediaHints(sel *libSel, d transcode.Preset) string {
	if sel.srcLoading {
		return hint("info", i18n.T("library.hints.probing"))
	}
	src := sel.src
	if src == nil {
		return ""
	}
	var out []string
	// source summary
	var parts []string
	if src.HasVideo {
		parts = append(parts, fmt.Sprintf("%s %d×%d %.0ffps %dk", strings.ToUpper(src.VideoCodec), src.Width, src.Height, src.FPS, src.VideoKbps))
	}
	if src.HasAudio {
		parts = append(parts, fmt.Sprintf("%s %dch %dHz %dk", strings.ToUpper(src.AudioCodec), src.Channels, src.SampleRate, src.AudioKbps))
	}
	if len(parts) > 0 {
		out = append(out, hint("info", i18n.T("library.hints.source", i18n.A{"detail": strings.Join(parts, " · ")})))
	}
	// video comparisons
	if src.HasVideo && d.VideoCodec != "copy" && d.VideoCodec != "none" && d.VideoCodec != "" {
		if d.Width > 0 && src.Width > 0 && d.Width > src.Width {
			out = append(out, hint("warn", i18n.T("library.hints.upscale", i18n.A{"sw": fmt.Sprint(src.Width), "sh": fmt.Sprint(src.Height), "dw": fmt.Sprint(d.Width), "dh": fmt.Sprint(d.Height)})))
		}
		if d.RateMode == "bitrate" && src.VideoKbps > 0 && d.BitrateK > int(float64(src.VideoKbps)*1.05) {
			out = append(out, hint("warn", i18n.T("library.hints.vbitrate", i18n.A{"target": fmt.Sprint(d.BitrateK), "source": fmt.Sprint(src.VideoKbps)})))
		}
		if strings.EqualFold(src.VideoCodec, d.VideoCodec) {
			out = append(out, hint("info", i18n.T("library.hints.alreadyCodec", i18n.A{"codec": strings.ToUpper(d.VideoCodec)})))
		}
	}
	// audio comparisons
	if src.HasAudio && d.AudioCodec != "copy" && d.AudioCodec != "none" && d.AudioCodec != "" {
		if d.SampleRate > 0 && src.SampleRate > 0 && d.SampleRate > src.SampleRate {
			out = append(out, hint("warn", i18n.T("library.hints.upsample", i18n.A{"source": fmt.Sprint(src.SampleRate), "target": fmt.Sprint(d.SampleRate)})))
		}
		if d.AudioBitrateK > 0 && src.AudioKbps > 0 && d.AudioBitrateK > int(float64(src.AudioKbps)*1.05) {
			out = append(out, hint("warn", i18n.T("library.hints.abitrate", i18n.A{"target": fmt.Sprint(d.AudioBitrateK), "source": fmt.Sprint(src.AudioKbps)})))
		}
	}
	return strings.Join(out, "")
}

// libSeedDraft seeds the preset builder from the first matching built-in (called locked).
func (s *libSt) libSeedDraft(sel *libSel) {
	s.draftInit = true
	for _, p := range transcode.Builtins {
		if sel.kind == "audio" && !p.IsAudioOnly() {
			continue
		}
		s.draft = p
		return
	}
	if len(transcode.Builtins) > 0 {
		s.draft = transcode.Builtins[0]
	}
}

// ── Camelot key-wheel SVG (own fn) ──

func keywheelSVG(ref *musiclib.Key, sel map[string]bool, present map[string]int) string {
	const sz = 260.0
	cx, cy := sz/2, sz/2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class=kwheel viewBox="0 0 %.0f %.0f">`, sz, sz)
	sector := func(k musiclib.Key, r0, r1 float64) {
		cam := k.Camelot()
		n := k.Num
		a0 := float64(n-1)*(math.Pi/6) - math.Pi/2 - math.Pi/12 + 0.02
		a1 := float64(n-1)*(math.Pi/6) - math.Pi/2 + math.Pi/12 - 0.02
		x0o, y0o := cx+r1*math.Cos(a0), cy+r1*math.Sin(a0)
		x1o, y1o := cx+r1*math.Cos(a1), cy+r1*math.Sin(a1)
		x0i, y0i := cx+r0*math.Cos(a0), cy+r0*math.Sin(a0)
		x1i, y1i := cx+r0*math.Cos(a1), cy+r0*math.Sin(a1)
		fill, op := kwColor(ref, k), kwOpacity(cam, sel, present)
		path := fmt.Sprintf("M%.2f %.2f A%.2f %.2f 0 0 1 %.2f %.2f L%.2f %.2f A%.2f %.2f 0 0 0 %.2f %.2f Z",
			x0o, y0o, r1, r1, x1o, y1o, x1i, y1i, r0, r0, x0i, y0i)
		fmt.Fprintf(&b, `<path class=kw-seg d="%s" fill="%s" fill-opacity="%.2f" stroke="%s" stroke-width="%s" data-act="lib-key:%s"/>`,
			path, fill, op, fill, kwStroke(cam, sel), cam)
		lr := (r0 + r1) / 2
		am := (a0 + a1) / 2
		lx, ly := cx+lr*math.Cos(am), cy+lr*math.Sin(am)
		tc := "#fafafa"
		if op < 0.25 {
			tc = "rgba(255,255,255,0.5)"
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" text-anchor=middle dominant-baseline=central>%s</text>`, lx, ly, tc, cam)
	}
	for n := 1; n <= 12; n++ {
		sector(musiclib.Key{Num: n, Minor: false}, sz*0.30, sz*0.485) // outer = B (major)
		sector(musiclib.Key{Num: n, Minor: true}, sz*0.14, sz*0.295)  // inner = A (minor)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func kwColor(ref *musiclib.Key, k musiclib.Key) string {
	if ref == nil {
		return "#F70864"
	}
	switch musiclib.KeyRelation(*ref, k) {
	case musiclib.RelSame:
		return "#08F79B"
	case musiclib.RelRelative:
		return "#7C3AED"
	case musiclib.RelUp:
		return "#FF3E8A"
	case musiclib.RelDown:
		return "#FFB547"
	default:
		return "rgba(255,255,255,0.6)"
	}
}

func kwOpacity(cam string, sel map[string]bool, present map[string]int) float64 {
	if sel[cam] {
		return 0.9
	}
	if present[cam] > 0 {
		return 0.35
	}
	return 0.12
}

func kwStroke(cam string, sel map[string]bool) string {
	if sel[cam] {
		return "1.5"
	}
	return "0"
}

func kwLegend() string {
	return `<div class=kw-legend>` +
		`<span><i style="background:#08F79B"></i>` + html.EscapeString(i18n.T("library.kw.same")) + `</span><span><i style="background:#7C3AED"></i>` + html.EscapeString(i18n.T("library.kw.relative")) + `</span>` +
		`<span><i style="background:#FF3E8A"></i>+1</span><span><i style="background:#FFB547"></i>−1</span></div>`
}

// ── smart-playlist rules editor (Fyne smartRulesDialog parity) ──

func libParseRules(s string) (musiclib.SmartRules, bool) {
	var r musiclib.SmartRules
	if s == "" {
		return r, true
	}
	return r, json.Unmarshal([]byte(s), &r) == nil
}

// srCurrent assembles the draft rules (Genres from the chip map, sorted). Caller holds s.mu.
func (s *libSt) srCurrent() musiclib.SmartRules {
	out := s.srRules
	out.Genres = nil
	for g, on := range s.srGenres {
		if on {
			out.Genres = append(out.Genres, g)
		}
	}
	sort.Strings(out.Genres)
	return out
}

// libSRCountText is the live match-count line. Caller holds s.mu (tracks hydrated).
func libSRCountText(s *libSt) string {
	cur := s.srCurrent()
	return i18n.T("library.sr.countText", i18n.A{"count": fmt.Sprint(len(musiclib.FilterSmart(s.tracks, cur))), "total": fmt.Sprint(len(s.tracks)), "desc": cur.Describe()})
}

// libGenres returns distinct collection genres (display form), name-sorted, capped.
func libGenres(tr []musiclib.Track) []string {
	seen := map[string]string{} // lower → display
	for _, t := range tr {
		g := strings.TrimSpace(t.Genre)
		if g == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(g)]; !ok {
			seen[strings.ToLower(g)] = g
		}
	}
	var out []string
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// libSmartModalHTML builds the rules editor modal. Caller holds s.mu.
func (u *UI) libSmartModalHTML(s *libSt) string {
	r := s.srRules
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.sr.desc")) + `</p>`)
	b.WriteString(`<div class=mform>`)
	b.WriteString(pbField(i18n.T("library.sr.name"), "lib-sr-name", s.srName, "text", ""))
	// genre chips from the collection
	b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(i18n.T("library.sr.genres")) + `</div><div class=seg>`)
	for _, g := range libGenres(s.tracks) {
		b.WriteString(fchip(g, "", "lib-sr-genre:"+g, s.srGenres[g]))
	}
	b.WriteString(`</div></div>`)
	// feel presets seed the BPM band - energy proxy without audio analysis
	feelOpts := [][2]string{{"", i18n.T("library.sr.feelPlaceholder")}}
	for _, f := range musiclib.FeelPresets() {
		feelOpts = append(feelOpts, [2]string{f.Label, f.Label})
	}
	b.WriteString(pbSelect(i18n.T("library.sr.feel"), "lib-sr-feel", feelOpts, ""))
	b.WriteString(`<div class=sr-band>` + pbField(i18n.T("library.sr.bpmMin"), "lib-sr-bpmmin", libTrimF0(r.BPMMin), "number", "") +
		pbField(i18n.T("library.sr.bpmMax"), "lib-sr-bpmmax", libTrimF0(r.BPMMax), "number", "") + `</div>`)
	b.WriteString(pbField(i18n.T("library.sr.keyContains"), "lib-sr-key", r.KeyContains, "text", i18n.T("library.sr.keyHint")))
	rateOpts := [][2]string{{"0", i18n.T("library.sr.rateAny")}, {"1", "≥ 1★"}, {"2", "≥ 2★"}, {"3", "≥ 3★"}, {"4", "≥ 4★"}, {"5", "5★"}}
	b.WriteString(`<div class=sr-band>` + pbSelect(i18n.T("library.sr.rating"), "lib-sr-rating", rateOpts, strconv.Itoa(r.RatingMin)) +
		pbField(i18n.T("library.sr.plays"), "lib-sr-plays", libTrimI0(r.PlayCountMin), "number", "") + `</div>`)
	b.WriteString(pbField(i18n.T("library.sr.search"), "lib-sr-search", r.Search, "text", i18n.T("library.sr.searchHint")))
	b.WriteString(`<div id=lib-sr-count class=sr-count>` + html.EscapeString(libSRCountText(s)) + `</div>`)
	confirm := i18n.T("library.sr.create")
	if s.srID != 0 {
		confirm = i18n.T("common.save")
	}
	b.WriteString(btnRow(btn(confirm, "primary", "lib-sr-save", ""), btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
	b.WriteString(`</div>`)
	title := i18n.T("library.sr.titleNew")
	if s.srID != 0 {
		title = i18n.T("library.sr.titleEdit")
	}
	return modal(title, b.String(), "")
}

func libTrimI0(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func libTrimF0(f float64) string {
	if f == 0 {
		return ""
	}
	return trimNum(f)
}

// ── relocate-missing modal (Fyne doRelocate parity: index root → candidates → backup + write NEW collection) ──

// libRelocModalHTML builds the relocate flow modal. Caller holds s.mu.
func (u *UI) libRelocModalHTML(s *libSt) string {
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.reloc.desc")) + `</p>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.reloc.missing", i18n.A{"count": fmt.Sprint(len(s.relocMiss))})) + `</p>`)
	b.WriteString(`<div class=lib-toolbar>` + fieldRaw("lib-reloc-root", s.relocRoot, i18n.T("library.reloc.rootPlaceholder")) +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:lib-reloc-root", "") + `</div>`)
	find := i18n.T("library.reloc.find")
	if s.relocBusy {
		find = i18n.T("library.reloc.working")
	}
	b.WriteString(btnRow(btn(find, "outline", "lib-reloc-find", "")))
	if s.relocMsg != "" {
		b.WriteString(hint("info", s.relocMsg))
	}
	if n := len(s.relocCands); n > 0 {
		b.WriteString(`<div class=reloc-list>`)
		for i, c := range s.relocCands {
			if i >= 200 {
				b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.reloc.showing", i18n.A{"total": fmt.Sprint(n)})) + `</p>`)
				break
			}
			chk := " checked"
			if s.relocSkip[i] {
				chk = ""
			}
			b.WriteString(`<div class=reloc-row><input type=checkbox data-act="lib-reloc-skip:` + strconv.Itoa(i) + `"` + chk + `>` +
				`<span class=reloc-paths><span class=reloc-old>` + html.EscapeString(c.Track.Path) + `</span>` +
				`<span class=reloc-new>→ ` + html.EscapeString(c.NewPath) + `</span></span>` +
				badge(relocConf(c.Confidence), relocConfBadge(c.Confidence)) + `</div>`)
		}
		b.WriteString(`</div>`)
		b.WriteString(btnRow(btn(i18n.T("library.reloc.apply"), "primary", "lib-reloc-apply", "")))
	}
	return modal(i18n.T("library.reloc.title"), b.String(), "")
}

func relocConf(c float64) string {
	switch {
	case c >= 1:
		return i18n.T("library.reloc.confUnique")
	case c >= 0.9:
		return i18n.T("library.reloc.confSize")
	default:
		return i18n.T("library.reloc.confAmbiguous")
	}
}

func relocConfBadge(c float64) string {
	switch {
	case c >= 1:
		return "success"
	case c >= 0.9:
		return "info"
	default:
		return "warning"
	}
}

// ── small render helpers ──

const (
	libMaxRows = 300
)

var (
	containerOpts  = strPairs("mp4", "webm", "mkv", "m4a", "mp3", "aac", "ogg", "wav", "aiff", "flac", "opus")
	videoCodecOpts = [][2]string{{"h264", "H.264"}, {"h265", "H.265 / HEVC"}, {"vp9", "VP9"}, {"av1", "AV1"}, {"copy", "Stream copy"}, {"none", "None"}}
	audioCodecOpts = [][2]string{{"aac", "AAC"}, {"opus", "Opus"}, {"mp3", "MP3"}, {"vorbis", "Vorbis"}, {"flac", "FLAC"}, {"copy", "Stream copy"}, {"none", "None"}}
	resOpts        = [][2]string{{"source", "Source"}, {"720", "720p"}, {"1080", "1080p"}, {"1440", "1440p"}, {"2160", "4K"}}
)

func accelOpts() [][2]string {
	var o [][2]string
	for _, a := range transcode.HWAccels() {
		o = append(o, [2]string{a, a})
	}
	return o
}

func inspSec(title, body string) string {
	return `<div class=insp-sec><div class=insp-sec-h>` + html.EscapeString(title) + `</div>` + body + `</div>`
}

// fchip renders a segmented/filter chip. val (optional) becomes data-val for the handler.
func fchip(label, val, act string, active bool) string {
	cls := "fchip"
	if active {
		cls += " active"
	}
	v := ""
	if val != "" {
		v = ` data-val="` + html.EscapeString(val) + `"`
	}
	return `<button class="` + cls + `" data-act="` + html.EscapeString(act) + `"` + v + `>` + html.EscapeString(label) + `</button>`
}

func fchipN(label, n, act string, active bool) string {
	cls := "fchip"
	if active {
		cls += " active"
	}
	return `<button class="` + cls + `" data-act="` + html.EscapeString(act) + `">` + html.EscapeString(label) +
		`<span class=n>` + html.EscapeString(n) + `</span></button>`
}

// fieldRaw is a bare input that dispatches act with its value on change.
func fieldRaw(act, value, placeholder string) string {
	return `<input class=field-input type=text value="` + html.EscapeString(value) + `" placeholder="` +
		html.EscapeString(placeholder) + `" data-act="` + html.EscapeString(act) + `" style="min-width:160px">`
}

func pbField(label, act, value, typ, hintTx string) string {
	h := ""
	if hintTx != "" {
		h = `<div class=pb-hint>` + html.EscapeString(hintTx) + `</div>`
	}
	if typ == "" {
		typ = "text"
	}
	return `<div class=pb-field><div class=pb-label>` + html.EscapeString(label) + `</div>` +
		`<input class=field-input type="` + typ + `" value="` + html.EscapeString(value) + `" data-act="` + html.EscapeString(act) + `">` + h + `</div>`
}

// pbSelect: encode-builder property select - smartSelect over the same act contract
// (val on pick, like the old <select> change). id derived from the act (colon-free).
func pbSelect(label, act string, opts [][2]string, current string) string {
	id := strings.ReplaceAll(act, ":", "-")
	return smartSelect(id, label, act, current, func() []ssOpt {
		out := make([]ssOpt, 0, len(opts))
		for _, op := range opts {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	})
}

func (u *UI) libKeyChip(s *libSt) string {
	n := len(s.keySel)
	label := i18n.T("library.meta.key")
	if n > 0 {
		label = i18n.T("library.keyChipN", i18n.A{"count": fmt.Sprint(n)})
	}
	// tapping opens the collection harmonic filter via the inspector wheel; here it clears.
	act := "lib-key-clear"
	return fchip(label, "", act, n > 0)
}

func (s *libSt) selRef() *musiclib.Key {
	if s.sel == nil {
		return nil
	}
	if k, ok := musiclib.ParseKey(s.sel.track.Key); ok {
		return &k
	}
	return nil
}

func keyMatches(keyText string, sel map[string]bool) bool {
	if len(sel) == 0 {
		return true
	}
	if k, ok := musiclib.ParseKey(keyText); ok {
		return sel[k.Camelot()]
	}
	return false
}

func keyPillHTML(keyText string, ref *musiclib.Key) string {
	keyText = strings.TrimSpace(keyText)
	if keyText == "" {
		return ""
	}
	k, ok := musiclib.ParseKey(keyText)
	if !ok {
		return `<span class=keypill>` + html.EscapeString(keyText) + `</span>`
	}
	cls := ""
	if ref != nil {
		switch musiclib.KeyRelation(*ref, k) {
		case musiclib.RelSame:
			cls = " k-same"
		case musiclib.RelRelative:
			cls = " k-rel"
		case musiclib.RelUp:
			cls = " k-up"
		case musiclib.RelDown:
			cls = " k-down"
		}
	}
	return `<span class="keypill` + cls + `">` + html.EscapeString(k.Camelot()) + `</span>`
}

func libKeyCensus(tr []musiclib.Track) map[string]int {
	m := map[string]int{}
	for _, t := range tr {
		if k, ok := musiclib.ParseKey(t.Key); ok {
			m[k.Camelot()]++
		}
	}
	return m
}

func inFilter(v string, sel map[string]bool) bool {
	if len(sel) == 0 {
		return true
	}
	return sel[v]
}

func distinctCounts(tr []musiclib.Track, get func(musiclib.Track) string, top int) [][2]string {
	counts := map[string]int{}
	for _, t := range tr {
		if v := get(t); v != "" {
			counts[v]++
		}
	}
	type kc struct {
		k string
		n int
	}
	var xs []kc
	for k, n := range counts {
		xs = append(xs, kc{k, n})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].n != xs[j].n {
			return xs[i].n > xs[j].n
		}
		return xs[i].k < xs[j].k
	})
	var out [][2]string
	for i, x := range xs {
		if i >= top {
			break
		}
		out = append(out, [2]string{x.k, strconv.Itoa(x.n)})
	}
	return out
}

// trackTitle: "Artist - Title", skipping a missing artist (no "- -" noise).
func trackTitle(t musiclib.Track) string {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = filepath.Base(t.Path)
	}
	if a := strings.TrimSpace(t.Artist); a != "" {
		return a + " - " + title
	}
	return title
}

// trackMetaSub is the row sub-line when BPM/time render as their own columns.
func trackMetaSub(t musiclib.Track) string {
	var p []string
	if t.Genre != "" {
		p = append(p, t.Genre)
	}
	if t.Label != "" {
		p = append(p, t.Label)
	}
	if t.BitrateBps > 0 {
		p = append(p, fmt.Sprintf("%dk", t.BitrateBps/1000))
	}
	if t.Rating > 0 {
		p = append(p, strings.Repeat("★", t.Rating))
	}
	return strings.Join(p, " · ")
}

func trackMeta(t musiclib.Track) string {
	var p []string
	if t.BPM > 0 {
		p = append(p, fmt.Sprintf("%.0f BPM", t.BPM))
	}
	if t.DurationSec > 0 {
		p = append(p, mmss(t.DurationSec))
	}
	if t.Genre != "" {
		p = append(p, t.Genre)
	}
	if t.BitrateBps > 0 {
		p = append(p, fmt.Sprintf("%dk", t.BitrateBps/1000))
	}
	if t.Rating > 0 {
		p = append(p, strings.Repeat("★", t.Rating))
	}
	return strings.Join(p, " · ")
}

func sortIdx(shown []int, get func(int) musiclib.Track, by string, desc bool) {
	if by == "" || by == "Play order" {
		return
	}
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := get(shown[i]), get(shown[j])
		if desc {
			return lessTrack(b, a, by)
		}
		return lessTrack(a, b, by)
	})
}

func lessTrack(a, b musiclib.Track, by string) bool {
	ci := func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) }
	switch by {
	case "Title":
		if c := ci(a.Title, b.Title); c != 0 {
			return c < 0
		}
	case "BPM":
		if a.BPM != b.BPM {
			return a.BPM < b.BPM
		}
	case "Key":
		ka, oka := musiclib.ParseKey(a.Key)
		kb, okb := musiclib.ParseKey(b.Key)
		if oka != okb {
			return oka
		}
		if oka && okb && ka.Num != kb.Num {
			return ka.Num < kb.Num
		}
	case "Genre":
		if c := ci(musiclib.GenreFamily(a.Genre), musiclib.GenreFamily(b.Genre)); c != 0 {
			return c < 0
		}
	case "Label":
		if c := ci(a.Label, b.Label); c != 0 {
			return c < 0
		}
	case "Rating":
		if a.Rating != b.Rating {
			return a.Rating < b.Rating
		}
	case "Plays":
		if a.PlayCount != b.PlayCount {
			return a.PlayCount < b.PlayCount
		}
	}
	if c := ci(a.Artist, b.Artist); c != 0 {
		return c < 0
	}
	return ci(a.Title, b.Title) < 0
}

func sortDir(desc bool) string {
	if desc {
		return i18n.T("library.sortDesc")
	}
	return i18n.T("library.sortAsc")
}

type seg struct{ label, path string }

func splitSegs(p string) []seg {
	p = filepath.Clean(p)
	parts := strings.Split(p, string(filepath.Separator))
	var out []seg
	acc := ""
	for _, part := range parts {
		if part == "" {
			if acc == "" {
				acc = string(filepath.Separator)
				out = append(out, seg{acc, acc})
			}
			continue
		}
		if acc == "" || acc == string(filepath.Separator) {
			acc += part
		} else {
			acc += string(filepath.Separator) + part
		}
		out = append(out, seg{part, acc})
	}
	if len(out) == 0 {
		out = append(out, seg{p, p})
	}
	return out
}

func libKind(name string, isDir bool) string {
	if isDir {
		return "dir"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".flac", ".wav", ".aiff", ".aif", ".m4a", ".ogg", ".opus", ".aac", ".wma":
		return "audio"
	case ".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v", ".wmv", ".flv":
		return "video"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return "image"
	default:
		return "other"
	}
}

func libGlyph(kind string, isDir bool) string {
	if isDir {
		return "📁"
	}
	switch kind {
	case "audio":
		return "🎵"
	case "video":
		return "🎬"
	case "image":
		return "🖼"
	default:
		return "📄"
	}
}

func pathOnDisk(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtDurCoarse(secs float64) string {
	m := int(secs) / 60
	if m >= 60 {
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

func clampFE(n int) []int {
	if n > libMaxRows {
		n = libMaxRows
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func strPairs(vals ...string) [][2]string {
	out := make([][2]string, len(vals))
	for i, v := range vals {
		out[i] = [2]string{v, strings.ToUpper(v)}
	}
	return out
}

// draft field helpers
func crfHint(codec string) string {
	switch codec {
	case "av1":
		return i18n.T("library.crf.av1")
	case "vp9":
		return i18n.T("library.crf.vp9")
	default:
		return i18n.T("library.crf.default")
	}
}

func audioCapHint(codec string) string {
	if codec == "opus" {
		return i18n.T("library.audioCap.opus")
	}
	return i18n.T("library.audioCap.default")
}

func resLabel(w, h int) string {
	switch {
	case w == 0 && h == 0:
		return "source"
	case h == 720 || w == 1280:
		return "720"
	case h == 1080 || w == 1920:
		return "1080"
	case h == 1440 || w == 2560:
		return "1440"
	case h == 2160 || w == 3840:
		return "2160"
	}
	return "source"
}

func profileOfDraft(_ transcode.Preset) string { return "custom" }

func profileHint(p string) string {
	switch p {
	case "streaming":
		return i18n.T("library.profile.streaming")
	case "youtube-hq":
		return i18n.T("library.profile.youtubehq")
	case "master":
		return i18n.T("library.profile.master")
	case "mobile":
		return i18n.T("library.profile.mobile")
	case "match-source":
		return i18n.T("library.profile.matchsource")
	default:
		return i18n.T("library.profile.default")
	}
}
