package webui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	collSel                              map[string]bool      // add-to-playlist multi-select
	collAnchor, batchAnchor              string               // last plain-clicked row (Shift-range anchor)
	collNoDrops                          bool                 // facet: only tracks WITHOUT drop markers
	collPl                               map[int64]bool       // facet: playlist membership (OR union)
	collPlSet                            map[string]bool      // union of the selected playlists' track paths
	collPlNames                          map[int64]string     // active playlist-facet id -> name (chips)
	dropsIdx                             map[string][]float64 // path -> drop markers (cue-prepare enrichment)
	batch                                map[string]bool      // browse batch multi-select

	sel       *libSel
	draft     transcode.Preset
	draftInit bool
	trimS     string
	trimE     string
	encOpen   bool              // per-file encoder expanded despite collection/playlist demotion
	tagEdit   bool              // per-track tag editor open (library_tagfix.go)
	tagDraft  map[string]string // tag editor draft values

	plSel   int64
	plCur   libdb.PlaylistRow
	plItems []libdb.PlaylistItemRow
	addto   []string // pending paths for the add-to-playlist modal

	compatPaths []string // pending paths for the works-together kind modal
	compatFind  string   // discovery modal subject path

	// sorted-copy set builder draft
	plsortID  int64
	plsortBy  string
	plsortDiv string

	played    []libPlay
	playSort  string
	playDesc  bool
	sessions  []musiclib.Session
	summaries []musiclib.SessionSummary
	histApps  []string // source app per session (aligned with sessions)
	histSrc   string   // "" = all, "traktor", or a master.db path
	selSess   int

	tracks   []musiclib.Track
	byPath   map[string]musiclib.Track
	loaded   bool
	loading  bool          // background hydrate in flight (render a loading placeholder)
	loadDone chan struct{} // closed when the in-flight hydrate finishes
	loadGen  int           // bumped by libReload: an in-flight hydrate from a prior gen is discarded
	marks    *library.Bookmarks

	// Browse listing cache: os.ReadDir + per-entry stat run in the BACKGROUND (a network
	// share / spun-down drive would otherwise wedge the single action goroutine); renders
	// come from the cache and a stale cache refreshes async + re-patches.
	browseDir  string  // dir the cache belongs to
	browseFes  []libFe // cached entries (unfiltered; treat as immutable - copy before filter/sort)
	browseErr  bool    // last read failed
	browseBusy bool    // a background read is in flight
	browseAt   time.Time

	moreOpen      bool // Maintenance popover (collection toolbar) open
	autoRefreshed bool // once-per-run auto folder-playlist sweep kicked

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

	// cross-DJ sync (libsync) editor draft
	djDraft   config.SyncJob
	djIdx     int  // -1 = new job, else index into cfg.Features.LibrarySync.Jobs
	djEditing bool // editor modal open (vs. the job list)
	djRunning bool // a run is in flight (guards double-run)

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
			collPl:  map[int64]bool{},
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
	if u.libRemoteTarget() != "" {
		// remote mirror: the embedded peer view carries its OWN section tabs - a local
		// duplicate row would be dead weight and shadow ctl clicks aimed at the mirror
		b.WriteString(`<div id=lib-body>` + u.libBody() + `</div>`)
		return b.String()
	}
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
	u.navRecord()
	u.mu.Lock()
	u.libSection = s
	u.mu.Unlock()
	u.patchMain()
}

// libNav changes the Browse cwd (called from ui.go dispatch).
func (u *UI) libNav(path string) {
	u.navRecord()
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
// targeted it routes to the live mirror (library_mirror.go) - the peer's own rendered Library
// tab, remote-driven; the local path below is byte-behaviour-unchanged.
func (u *UI) libBody() string {
	if tgt := u.libRemoteTarget(); tgt != "" {
		return u.libMirrorBody(tgt)
	}
	sec := u.libSectionOr()
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.autoRefreshed && u.svc.Lib != nil {
		s.autoRefreshed = true
		u.bg(u.libAutoRefreshFolders)
	}
	switch sec {
	case "favorites":
		return u.libFavoritesHTML(s)
	case "collection":
		if !u.libEnsureTracks(s) {
			return emptyState(i18n.T("library.remote.col.loading"))
		}
		if u.ceActiveFor("library") {
			// cue-edit mode: the waveform (grid + markers) spans the full tab width
			// above the list; the rail keeps only the editor controls.
			return `<div class=ce-fullwave>` + u.ceWaveHTML() + `</div>` +
				triPane(u.libNavRailHTML(s, "collection"), u.libCollectionHTML(s), u.libDetailWrap(s), "lib-nav-w", "lib-det-w")
		}
		return triPane(u.libNavRailHTML(s, "collection"), u.libCollectionHTML(s), u.libDetailWrap(s), "lib-nav-w", "lib-det-w")
	case "playlists":
		if !u.libEnsureTracks(s) {
			return emptyState(i18n.T("library.remote.col.loading"))
		}
		return masterDetail(u.libPlaylistsHTML(s), u.libDetailWrap(s))
	case "history":
		if !u.libEnsureTracks(s) {
			return emptyState(i18n.T("library.remote.col.loading"))
		}
		return masterDetailWide(u.libHistoryHTML(s), u.libDetailWrap(s))
	case "idmarks":
		return u.libIDMarksHTML()
	case "queue":
		return `<div id=lib-queue-body>` + u.libQueueHTML() + `</div>`
	case "presets":
		return u.libPresetsHTML()
	default:
		// Browse renders the dir listing regardless; the collection (metadata enrichment)
		// hydrates in the background and re-patches when ready.
		u.libEnsureTracks(s)
		return triPane(u.libNavRailHTML(s, "browse"), u.libBrowseHTML(s), u.libDetailWrap(s), "lib-nav-w", "lib-det-w")
	}
}

func (u *UI) libDetailWrap(s *libSt) string {
	return `<div id=lib-detail>` + u.libDetailHTML(s) + `</div>`
}

// libEnsureTracks lazily hydrates the collection from the persisted DB, in the
// BACKGROUND: a big library would otherwise block the single action goroutine on first
// open (frozen tab, dropped clicks). Returns whether the collection is ready; while the
// load is in flight the caller renders a loading placeholder and the completion re-patches
// the body. Caller holds s.mu.
func (u *UI) libEnsureTracks(s *libSt) bool {
	if s.loaded || u.svc.Lib == nil {
		return true
	}
	if s.loading {
		return false
	}
	s.loading = true
	s.loadDone = make(chan struct{})
	done, gen := s.loadDone, s.loadGen
	u.bg(func() {
		defer close(done)
		tr, err := u.svc.Lib.LoadAllTracks() // excludes divider rows (collection stays clean)
		drops, _ := u.svc.Lib.AllDrops()
		var byPath map[string]musiclib.Track
		if err == nil { // index built off the action goroutine too
			byPath = make(map[string]musiclib.Track, len(tr))
			for _, t := range tr {
				if t.Path != "" {
					byPath[t.Path] = t
				}
			}
			// dividers hydrate into byPath ONLY: playlist rows show their title,
			// the collection list/facets never see them
			if divs, derr := u.svc.Lib.DividerTracks(); derr == nil {
				for _, t := range divs {
					if t.Path != "" {
						byPath[t.Path] = t
					}
				}
			}
		}
		s.mu.Lock()
		s.loading = false
		if s.loadGen == gen { // a libReload mid-load discards this read (next render re-hydrates)
			s.loaded = true
			if err == nil {
				s.tracks, s.byPath = tr, byPath
				s.dropsIdx = drops
			}
		}
		s.mu.Unlock()
		u.ceReloadTrack() // an open cue editor re-reads its track + grid (gridfix reimport)
		u.libPatchBody()
	})
	return false
}

func (u *UI) libMarks(s *libSt) *library.Bookmarks {
	if s.marks == nil {
		mf, _ := config.DataPath("bookmarks.json")
		s.marks = library.LoadBookmarks(mf)
	}
	return s.marks
}

// ── Browse ────────────────────────────────────────────────────────────────────

// libFe is one cached browse entry.
type libFe struct {
	name, path, kind string
	isDir            bool
	size             int64
	mod              time.Time
}

// browseFresh is how long a cached listing serves without a background re-read.
const browseFresh = 2 * time.Second

// libBrowseEntries returns the (possibly stale) cached listing for dir, kicking a
// background read when the cache is missing or stale. ok=false → nothing cached yet for
// this dir (render a loading placeholder; the read completion re-patches). Caller holds s.mu.
func (u *UI) libBrowseEntries(s *libSt, dir string) (fes []libFe, errRead, ok bool) {
	cached := s.browseDir == dir
	if (!cached || time.Since(s.browseAt) > browseFresh) && !s.browseBusy {
		s.browseBusy = true
		u.bg(func() {
			entries, err := os.ReadDir(dir)
			var out []libFe
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") {
					continue
				}
				fi, serr := e.Info()
				if serr != nil {
					continue
				}
				out = append(out, libFe{name, filepath.Join(dir, name), libKind(name, fi.IsDir()), fi.IsDir(), fi.Size(), fi.ModTime()})
			}
			s.mu.Lock()
			changed := s.browseDir != dir || s.browseErr != (err != nil) || !slices.Equal(s.browseFes, out)
			s.browseBusy = false
			s.browseDir, s.browseAt = dir, time.Now()
			s.browseErr = err != nil
			s.browseFes = out
			s.mu.Unlock()
			if changed { // replace the loading placeholder / refresh a listing that changed on disk
				u.libPatchBody()
			}
		})
	}
	if !cached {
		return nil, false, false
	}
	return s.browseFes, s.browseErr, true
}

// libBrowseViewOf filters+sorts the cached dir entries per the current controls -
// the single source of row ORDER for rendering and Shift-range selection.
func libBrowseViewOf(s *libSt, fes []libFe) []libFe {
	fs := append([]libFe(nil), fes...) // cache is immutable - copy before filter/sort
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
	return fs
}

func (u *UI) libBrowseHTML(s *libSt) string {
	dir := u.libDirOr()
	cachedFes, errRead, ok := u.libBrowseEntries(s, dir)
	if !ok {
		return emptyState(i18n.T("library.remote.col.loading"))
	}
	if errRead {
		return emptyState(i18n.T("library.browse.cannotRead", i18n.A{"path": dir}))
	}
	fs := libBrowseViewOf(s, cachedFes)

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
	// quick-access + pinned live in the left nav rail (libNavRailHTML)
	// toolbar
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(i18n.T("library.browse.up"), "outline", "lib-nav:"+filepath.Dir(dir), ""))
	b.WriteString(btn(i18n.T("library.browse.goto"), "ghost", "pick-dir:lib-nav-to", ""))
	b.WriteString(fieldRaw("lib-search", s.nameFilter, i18n.T("library.browse.filterName")))
	// kind + sort: one dropdown each (was 8 chips + 2 labels across two wrapped rows);
	// .lib-ctl glues each label to its control across flex-wrap
	b.WriteString(`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.kind")) + `</span>`)
	b.WriteString(smartSelect("libkind", "", "lib-kind:", s.kindFilter, func() []ssOpt {
		opts := make([]ssOpt, 0, 5)
		for _, k := range []string{"ALL", "VIDEO", "AUDIO", "IMAGE", "OTHER"} {
			opts = append(opts, ssOpt{Val: k, Label: i18n.T("library.kind." + strings.ToLower(k))})
		}
		return opts
	}) + `</span>`)
	b.WriteString(`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.sort")) + `</span>`)
	b.WriteString(smartSelect("libsort", "", "lib-sort:", s.sortBy, func() []ssOpt {
		opts := make([]ssOpt, 0, 3)
		for _, so := range []string{"Name", "Modified", "Size"} {
			opts = append(opts, ssOpt{Val: so, Label: i18n.T("library.sort." + strings.ToLower(so))})
		}
		return opts
	}) + `</span>`)
	// view: segmented mode switch (mutually exclusive)
	b.WriteString(`<span class=seg>` + fchip(i18n.T("library.browse.list"), "", "lib-view:list", s.view != "grid") +
		fchip(i18n.T("library.browse.grid"), "", "lib-view:grid", s.view == "grid") + `</span>`)
	b.WriteString(u.libKeyChip(s))
	// folder ops: one ⋯ menu (was four ghost buttons)
	pinLabel := i18n.T("library.browse.pin")
	for _, bm := range u.libMarks(s).List() {
		if bm.Path == dir {
			pinLabel = i18n.T("library.browse.unpin")
			break
		}
	}
	b.WriteString(actionMenu("libfoldermenu", "📁 "+i18n.T("library.browse.folderMenu"), []ssOpt{
		{Val: "ce-open-dir", Label: i18n.T("library.ce.openDir")},
		{Val: "lib-reenc-dir", Label: i18n.T("library.re.dirBtn")},
		{Val: "lib-markpl", Label: i18n.T("library.re.markBtn")},
		{Val: "lib-pin", Label: pinLabel},
	}))
	nf, allB := 0, true
	for _, e := range fs {
		if e.isDir {
			continue
		}
		nf++
		if !s.batch[e.path] {
			allB = false
		}
	}
	chkAllB := ""
	if nf > 0 && allB {
		chkAllB = " checked"
	}
	if nf > 0 {
		b.WriteString(`<input type=checkbox class=trk-selall data-act=lib-batch-all title="` + html.EscapeString(i18n.T("library.selectAll")) + `"` + chkAllB + `>`)
	}
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(i18n.Tn("library.item", len(fs))) + `</span>`)
	b.WriteString(`</div>`)
	// folder bound to a playlist -> its actions live right here
	if pl := u.libFolderPlaylist(dir); pl != nil {
		b.WriteString(`<p class=page-sub>🎵 ` + html.EscapeString(i18n.T("library.pl.folderBound", i18n.A{"name": pl.Name})) + `</p>`)
		b.WriteString(u.libPlaylistActionsHTML(*pl, false))
	}

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
			ctxAct := "lib-dirctx:" + it.path
			if !it.isDir {
				act = "lib-open:" + it.path
				ctxAct = "lib-ctx:" + it.path
			}
			b.WriteString(`<div class=gcard data-act="` + html.EscapeString(act) + `" data-ctx="` + html.EscapeString(ctxAct) + `"><div class=gcard-ic>` + libGlyph(it.kind, it.isDir) +
				`</div><div class=gcard-t>` + html.EscapeString(it.name) + `</div><div class=gcard-s>` + html.EscapeString(sub) + `</div></div>`)
		}
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`<div class=trk-table>`)
		for _, e := range clampFE(len(fs)) {
			it := fs[e]
			if it.isDir {
				b.WriteString(`<div class=trk-row data-act="lib-nav:` + html.EscapeString(it.path) + `" data-ctx="lib-dirctx:` + html.EscapeString(it.path) + `"><span class=trk-ic>📁</span>` +
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
			b.WriteString(`<div class="trk-row` + selCls + `" data-ctx="lib-ctx:` + html.EscapeString(it.path) + `">` +
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
	compatBtn := ""
	if len(s.batch) >= 2 {
		compatBtn = btn(i18n.T("library.compat.markBtn"), "outline", "lib-compat-mark:browse", "")
	}
	return `<div class=batchbar><span class=cnt>` + html.EscapeString(i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.batch))})) + `</span>` +
		btn(i18n.T("library.batch.waveforms"), "outline", "lib-batch-run:peaks", "") + btn(i18n.T("library.batch.tags"), "outline", "lib-batch-run:tags", "") +
		btn(i18n.T("library.batch.fingerprint"), "outline", "lib-batch-run:fingerprint", "") + btn(i18n.T("library.batch.transcode"), "primary", "lib-batch-run:transcode", "") +
		compatBtn + btn(i18n.T("library.clear"), "ghost", "lib-batch-clear", "") + `</div>`
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
	// actions: the two everyday operations + the fixer up front; everything
	// occasional lives behind Maintenance so the list gets the vertical space
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(i18n.T("library.coll.import"), "primary", "lib-import", ""))
	b.WriteString(btn(i18n.T("library.coll.djsync"), "primary", "lib-djsync", ""))
	if u.svc.Cfg.Features.GridFix.Enabled {
		b.WriteString(btn(i18n.T("library.gf.start"), "outline", "gf-open", ""))
	}
	b.WriteString(`<span class=lib-more>` + btn(i18n.T("library.coll.more"), "ghost", "lib-more", "") + u.libMoreMenuHTML(s) + `</span>`)
	// filters flow in the SAME wrap row: search + facet dropdowns; active facets
	// render as removable chips (one toolbar = one less stacked row above the list)
	b.WriteString(fieldRaw("lib-coll-search", s.collSearch, i18n.T("library.coll.search")))
	b.WriteString(u.libFacetSelect(s, "genre", i18n.T("library.label.genre"), s.collGenre,
		func(t musiclib.Track) string { return musiclib.GenreFamily(t.Genre) }))
	b.WriteString(u.libFacetSelect(s, "label", i18n.T("library.label.label"), s.collLabel,
		func(t musiclib.Track) string { return strings.TrimSpace(t.Label) }))
	b.WriteString(u.libPlaylistFacet(s))
	b.WriteString(u.libKeyChip(s))
	b.WriteString(fchip(i18n.T("library.ce.noDropsChip"), "", "lib-nodrops", s.collNoDrops))
	if len(s.collGenre)+len(s.collLabel)+len(s.keySel)+len(s.collPl) > 0 || s.collSearch != "" || s.collNoDrops {
		b.WriteString(btn(i18n.T("library.clear"), "ghost", "lib-clearfilters", ""))
	}
	b.WriteString(`</div>`)
	for g := range s.collGenre {
		b.WriteString(fchip(g+" ×", "", "lib-genre:"+g, true))
	}
	for l := range s.collLabel {
		b.WriteString(fchip(l+" ×", "", "lib-label:"+l, true))
	}
	for _, id := range sortedPlIDs(s.collPlNames) {
		b.WriteString(fchip(s.collPlNames[id]+" ×", "", fmt.Sprintf("lib-plfilter:%d", id), true))
	}
	// exactly one playlist facet active -> the collection IS that playlist's view:
	// surface its full action row inline (no Playlists-tab round-trip)
	if len(s.collPl) == 1 && u.svc.Lib != nil {
		var id int64
		for k := range s.collPl {
			id = k
		}
		if rows, _ := u.svc.Lib.ListPlaylists(); rows != nil {
			for _, p := range rows {
				if p.ID == id {
					b.WriteString(u.libPlaylistActionsHTML(p, true))
					break
				}
			}
		}
	}

	// batch results replace the list while a fixer's results view is on
	u.tf.mu.Lock()
	tfView := u.tf.resView
	u.tf.mu.Unlock()
	if tfView {
		return b.String() + u.tfResultsHTML()
	}
	u.gf.mu.Lock()
	resView := u.gf.resView && u.gf.stage == "done"
	u.gf.mu.Unlock()
	if resView {
		return b.String() + u.gfResultsHTML(&u.gf)
	}

	// filtered + sorted view; column headers sort (click again flips direction)
	shown := s.collView()
	total := len(shown)
	hdr := func(key, label, cls string) string {
		arrow := ""
		if s.collSort == key {
			arrow = " ▲"
			if s.collDesc {
				arrow = " ▼"
			}
		}
		return `<span class="` + cls + ` trk-sortable" data-act="lib-coll-hsort:` + key + `">` + html.EscapeString(label) + arrow + `</span>`
	}
	allSel := total > 0
	for _, ti := range shown {
		if !s.collSel[s.tracks[ti].Path] {
			allSel = false
			break
		}
	}
	chkAll := ""
	if allSel {
		chkAll = " checked"
	}
	b.WriteString(`<div class=trk-h>` +
		`<input type=checkbox class=trk-selall data-act=lib-collsel-all title="` + html.EscapeString(i18n.T("library.selectAll")) + `"` + chkAll + `>` +
		hdr("Artist", i18n.T("library.coll.trackHeader", i18n.A{"count": fmt.Sprint(total)}), "trk-hmain") +
		`<span class=trk-cell-ce>` + html.EscapeString(i18n.T("library.col.cues")) + `</span>` +
		hdr("BPM", i18n.T("library.col.bpm"), "trk-bpm") +
		`<span class=trk-dur>` + html.EscapeString(i18n.T("library.col.time")) + `</span>` +
		hdr("Key", i18n.T("library.col.key"), "trk-keyh") + `</div>`)
	b.WriteString(`<div class=trk-table>`)
	ref := s.selRef()
	vs := u.gfVerified()
	ceOn := u.ceActiveFor("library")
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
		if ceOn && s.collSel[t.Path] {
			selCls += " ce-marked" // in the mass-apply set (batch bar below)
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
		ver := ""
		if vs != nil && vs.Has(t.Path) {
			ver = `<span class=trk-verified title="` + html.EscapeString(i18n.T("library.gf.verifiedBadge")) + `">✓</span>`
		}
		b.WriteString(`<div class="trk-row` + selCls + `" data-ctx="lib-ctx:` + html.EscapeString(t.Path) + `">` +
			`<input type=checkbox data-act="lib-collsel:` + html.EscapeString(t.Path) + `"` + chk + `>` + ic +
			`<span class=trk-main data-act="lib-track:` + html.EscapeString(t.Path) + `"><span class=trk-title>` +
			html.EscapeString(trackTitle(t)) + `</span><span class=trk-sub>` +
			html.EscapeString(trackMetaSub(t)) + `</span></span>` + ver +
			`<span class=trk-cell-ce id=` + ceCellID(t.Path) + `>` + libCueCellHTML(s, t) + `</span>` +
			`<span class=trk-bpm>` + bpm + `</span><span class=trk-dur>` + dur + `</span>` +
			`<span class=trk-key>` + keyPillHTML(t.Key, ref) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if total == 0 {
		b.WriteString(emptyState(i18n.T("library.coll.empty")))
	} else if total > libMaxRows {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.showingFirst", i18n.A{"shown": fmt.Sprint(libMaxRows), "total": fmt.Sprint(total)})) + `</p>`)
	}
	// selection bar: playlist add + verified-grid marking; in cue-edit mode the checked
	// rows are the mass-apply set for the assigned patterns
	if len(s.collSel) > 0 {
		ceBtns, addVar := "", "primary"
		if ceOn {
			ceBtns = btn(i18n.T("library.ce.applySelHot"), "primary", "ce-apply-sel:hot", "") +
				btn(i18n.T("library.ce.applySelMem"), "outline", "ce-apply-sel:mem", "")
			addVar = "outline"
		}
		compatBtn := ""
		if len(s.collSel) >= 2 {
			compatBtn = btn(i18n.T("library.compat.markBtn"), "outline", "lib-compat-mark:coll", "")
		}
		b.WriteString(`<div class=batchbar><span class=cnt>` + html.EscapeString(i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.collSel))})) + `</span>` +
			ceBtns + btn(i18n.T("library.addToPlaylist"), addVar, "lib-addto", "") + compatBtn +
			btn(i18n.T("library.gf.markVerified"), "outline", "gf-verify-sel", "") +
			btn(i18n.T("library.clear"), "ghost", "lib-collsel-clear", "") + `</div>`)
	}
	return b.String()
}

// ceCellID is the stable DOM id of a row's cue-census cell (targeted drop-toggle patch).
func ceCellID(path string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return fmt.Sprintf("ce-cell-%08x", h.Sum32())
}

// libPatchCueCell patches one row's ◆/⚑ census cell in place - a drop toggle must not
// rebuild the whole collection body. Falls back to the full body when the "no drops"
// facet is active (row membership itself changes).
func (u *UI) libPatchCueCell(path string) {
	s := u.lib()
	s.mu.Lock()
	if s.collNoDrops {
		s.mu.Unlock()
		u.libPatchBody()
		return
	}
	tr, ok := s.byPath[path]
	cell := ""
	if ok {
		cell = libCueCellHTML(s, tr)
	}
	s.mu.Unlock()
	if ok {
		u.eval("window.__patch(" + jsQuote(ceCellID(path)) + "," + jsQuote(cell) + ")")
	}
}

// libCueCellHTML: compact drops/cues census - ◆n amber = drop markers, ⚑n = cues;
// dim glyphs mark absence so prepared vs unprepared scans at a glance.
func libCueCellHTML(s *libSt, t musiclib.Track) string {
	var b strings.Builder
	if nd := len(s.dropsIdx[t.Path]); nd > 0 {
		b.WriteString(`<span class=trk-drops title="` + html.EscapeString(i18n.Tn("library.ce.drops", nd)) + `">◆` + fmt.Sprint(nd) + `</span>`)
	} else {
		b.WriteString(`<span class="trk-drops none" title="` + html.EscapeString(i18n.T("library.ce.noDropsBadge")) + `">◇</span>`)
	}
	if nc := ceCueCount(t.Cues); nc > 0 {
		b.WriteString(`<span class=trk-cuen title="` + html.EscapeString(i18n.Tn("library.ce.patternCues", nc)) + `">⚑` + fmt.Sprint(nc) + `</span>`)
	} else {
		b.WriteString(`<span class="trk-cuen none" title="` + html.EscapeString(i18n.T("library.ce.noCuesBadge")) + `">⚑</span>`)
	}
	return b.String()
}

// libMoreMenuHTML is the Maintenance popover (occasional collection operations).
func (u *UI) libMoreMenuHTML(s *libSt) string {
	if !s.moreOpen {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-popmenu>`)
	items := [][2]string{
		{"lib-pl-new", i18n.T("library.pl.new")},
		{"lib-pl-newsmart", i18n.T("library.pl.newSmart")},
		{"lib-pl-refresh-all", i18n.T("library.pl.refreshAll")},
		{"lib-backups", i18n.T("library.coll.backup")},
		{"lib-scan", i18n.T("library.coll.scan")},
		{"lib-cleanup", i18n.T("library.coll.cleanup")},
		{"lib-relocate", i18n.T("library.coll.relocate")},
		{"lib-export", i18n.T("library.coll.export")},
		{"lib-tagfix", i18n.T("library.tf.menu")},
	}
	if u.svc.Syncer != nil {
		items = append(items, [2]string{"lib-sync", i18n.T("library.coll.sync")})
	}
	for _, it := range items {
		b.WriteString(`<button class=lib-popitem data-act="lib-morego:` + it[0] + `">` + html.EscapeString(it[1]) + `</button>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// libFacetSelect renders a filterable multi-facet dropdown (SmartSelect) whose
// label summarizes the active picks - replaces the old always-open chip clouds.
func (u *UI) libFacetSelect(s *libSt, kind, label string, active map[string]bool, keyOf func(musiclib.Track) string) string {
	lbl := label
	if n := len(active); n > 0 {
		lbl = fmt.Sprintf("%s (%d)", label, n)
	}
	tracks := s.tracks
	return smartSelect("libfacet-"+kind, "", "lib-"+kind+":", lbl, func() []ssOpt {
		opts := make([]ssOpt, 0, 32)
		for _, gc := range distinctCounts(tracks, keyOf, 200) {
			o := ssOpt{Val: gc[0], Label: gc[0], Badge: gc[1]}
			if active[gc[0]] {
				o.Label = "✓ " + o.Label
			}
			opts = append(opts, o)
		}
		return opts
	})
}

// libPlaylistFacet: filterable playlist dropdown - filters the collection to tracks
// that are members of any picked playlist (smart playlists eval live).
func (u *UI) libPlaylistFacet(s *libSt) string {
	if u.svc.Lib == nil {
		return ""
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	if len(rows) == 0 {
		return ""
	}
	lbl := i18n.T("library.label.playlist")
	if n := len(s.collPl); n > 0 {
		lbl = fmt.Sprintf("%s (%d)", lbl, n)
	}
	tracks := s.tracks
	active := s.collPl
	return smartSelect("libfacet-pl", "", "lib-plfilter:", lbl, func() []ssOpt {
		opts := make([]ssOpt, 0, len(rows))
		for _, p := range rows {
			n := p.TrackCount
			if p.Kind == libdb.PlaylistSmart {
				if r, ok := libParseRules(p.Rules); ok {
					n = len(u.filterSmartDB(tracks, r))
				}
			}
			o := ssOpt{Val: fmt.Sprint(p.ID), Label: p.Name, Badge: fmt.Sprint(n)}
			if active[p.ID] {
				o.Label = "✓ " + o.Label
			}
			opts = append(opts, o)
		}
		return opts
	})
}

// libRebuildPlFilter recomputes the playlist-facet membership union: stored paths for
// manual/imported playlists, live rule eval for smart ones.
func (u *UI) libRebuildPlFilter() {
	s := u.lib()
	s.mu.Lock()
	want := make(map[int64]bool, len(s.collPl))
	for id := range s.collPl {
		want[id] = true
	}
	tracks := s.tracks
	s.mu.Unlock()
	set, names := map[string]bool{}, map[int64]string{}
	if u.svc.Lib != nil && len(want) > 0 {
		rows, _ := u.svc.Lib.ListPlaylists()
		for _, p := range rows {
			if !want[p.ID] {
				continue
			}
			names[p.ID] = p.Name
			if p.Kind == libdb.PlaylistSmart {
				if r, ok := libParseRules(p.Rules); ok {
					for _, t := range u.filterSmartDB(tracks, r) {
						set[t.Path] = true
					}
				}
				continue
			}
			paths, _ := u.svc.Lib.PlaylistTracks(p.ID)
			for _, pth := range paths {
				set[pth] = true
			}
		}
	}
	s.mu.Lock()
	s.collPlSet, s.collPlNames = set, names
	s.mu.Unlock()
}

func sortedPlIDs(m map[int64]string) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
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
		if s.collNoDrops && len(s.dropsIdx[t.Path]) > 0 {
			continue
		}
		if len(s.collPl) > 0 && !s.collPlSet[t.Path] {
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
		// cloud ops are occasional - one ⋯ menu instead of three toolbar buttons
		b.WriteString(actionMenu("plcloudmenu", "☁ "+i18n.T("library.pl.menu.cloud"), []ssOpt{
			{Val: "lib-pl-cloud", Label: i18n.T("library.pl.cloudStatus")},
			{Val: "lib-pl-syncall", Label: i18n.T("library.pl.syncAll")},
			{Val: "lib-pl-remote", Label: i18n.T("library.pl.remote")},
		}))
	}
	b.WriteString(`</div>`)
	if len(rows) == 0 {
		b.WriteString(emptyState(i18n.T("library.pl.empty")))
	}
	// dense rows: the row itself opens (no per-row Open button)
	b.WriteString(`<div class=trk-table>`)
	for _, p := range rows {
		ic := "🎵"
		sub := fmt.Sprint(p.Kind) + " · " + i18n.Tn("track", p.TrackCount)
		switch p.Kind {
		case libdb.PlaylistSmart:
			ic = "⚡"
			if r, ok := libParseRules(p.Rules); ok {
				sub = i18n.T("library.pl.smartSub", i18n.A{"count": fmt.Sprint(len(u.filterSmartDB(s.tracks, r))), "desc": r.Describe()})
			}
		case libdb.PlaylistImported:
			ic = "⤓"
		}
		selCls := ""
		if p.ID == s.plSel {
			selCls = " sel"
		}
		b.WriteString(`<div class="trk-row` + selCls + `" data-act="lib-pl:` + fmt.Sprint(p.ID) + `">` +
			`<span class=trk-ic>` + ic + `</span>` +
			`<span class=trk-main><span class=trk-title>` + html.EscapeString(p.Name) + `</span>` +
			`<span class=trk-sub>` + html.EscapeString(sub) + `</span></span></div>`)
	}
	b.WriteString(`</div>`)

	// open playlist tracks
	if s.plSel != 0 {
		b.WriteString(u.libPlaylistOpenHTML(s))
	}
	return b.String()
}

// libPlaylistActionsHTML: one playlist's action row - shared by the Playlists
// open view, the Collection inline panel (single playlist facet), and Browse (a
// playlist-bound folder). inColl hides the "View in Collection" jump.
// Density: everyday actions stay as buttons; occasional ones demote into a ⋯
// actionMenu (full labels + Sub hints keep them discoverable).
func (u *UI) libPlaylistActionsHTML(p libdb.PlaylistRow, inColl bool) string {
	manual := p.Kind == libdb.PlaylistManual
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	if !inColl {
		b.WriteString(btn(i18n.T("library.pl.viewInColl"), "primary", fmt.Sprintf("lib-plgoto:%d", p.ID), ""))
	}
	b.WriteString(btn(i18n.T("library.ce.openPl"), "outline", fmt.Sprintf("ce-open-pl:%d", p.ID), ""))
	if p.Kind == libdb.PlaylistSmart {
		b.WriteString(btn(i18n.T("library.pl.editRules"), "outline", fmt.Sprintf("lib-sr-edit:%d", p.ID), ""))
	}
	b.WriteString(btn(i18n.T("library.pl.exportM3U"), "outline", fmt.Sprintf("lib-pl-export:%d", p.ID), ""))

	var items []ssOpt
	add := func(label, act, sub string) { items = append(items, ssOpt{Val: act, Label: label, Sub: sub}) }
	if p.Kind != libdb.PlaylistImported {
		add(i18n.T("library.pl.rename"), fmt.Sprintf("lib-pl-rename:%d", p.ID), "")
	}
	add(i18n.T("library.pl.exportM3UAs"), fmt.Sprintf("pick-save:m3u8:lib-pl-exportas:%d", p.ID), "")
	add(i18n.T("library.re.plBtn"), fmt.Sprintf("lib-reenc-pl:%d", p.ID), i18n.T("library.pl.menu.reencSub"))
	if p.Kind != libdb.PlaylistSmart {
		// refresh works for any file-backed playlist: stored folder binding, or the
		// members' dominant dir (Traktor folder imports store tree names, not paths)
		add(i18n.T("library.pl.refreshFolder"), fmt.Sprintf("lib-pl-refresh:%d", p.ID), "")
		arLbl := i18n.T("library.pl.autoOff")
		if p.AutoRefresh {
			arLbl = i18n.T("library.pl.autoOn")
		}
		add(arLbl, fmt.Sprintf("lib-pl-autorefresh:%d", p.ID), i18n.T("library.pl.menu.autoSub"))
	}
	if !manual {
		add(i18n.T("library.pl.dupManual"), fmt.Sprintf("lib-pl-dup:%d", p.ID), "")
	}
	add(i18n.T("library.plsort.btn"), fmt.Sprintf("lib-plsort:%d", p.ID), "")
	if u.svc.Syncer != nil {
		add(i18n.T("library.pl.push"), fmt.Sprintf("lib-pl-push:%d", p.ID), "")
		add(i18n.T("library.pl.pull"), fmt.Sprintf("lib-pl-pull:%d", p.ID), "")
		add(i18n.T("library.pl.unlink"), fmt.Sprintf("lib-pl-unlink:%d", p.ID), "")
	}
	add(i18n.T("common.delete"), fmt.Sprintf("lib-pl-del:%d", p.ID), "")
	b.WriteString(actionMenu(fmt.Sprintf("plmenu-%d", p.ID), "⋯ "+i18n.T("player.more"), items))
	b.WriteString(`</div>`)
	return b.String()
}

// libFolderPlaylist returns the playlist bound to dir (imported folder / manual mark).
func (u *UI) libFolderPlaylist(dir string) *libdb.PlaylistRow {
	if u.svc.Lib == nil {
		return nil
	}
	d := filepath.Clean(dir)
	rows, _ := u.svc.Lib.ListPlaylists()
	for i := range rows {
		if rows[i].Folder != "" && filepath.Clean(rows[i].Folder) == d {
			return &rows[i]
		}
	}
	return nil
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
	b.WriteString(u.libPlaylistActionsHTML(p, false))
	b.WriteString(`<div class=trk-table>`)
	ref := s.selRef()
	for i, it := range s.plItems {
		title := it.Title + " - " + it.Artist
		if it.Path != "" {
			if t, ok := s.byPath[it.Path]; ok {
				title = trackTitle(t) // skips a missing artist (divider rows, loose files)
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
		// dense rows: the row itself opens the session (no per-row Open button)
		b.WriteString(`<div class=trk-table>`)
		for i, sm := range s.summaries {
			selCls := ""
			if i == s.selSess {
				selCls = " sel"
			}
			sub := i18n.Tn("track", sm.TrackCount) + " · " + fmtDurCoarse(sm.TotalDurationSec)
			if i < len(s.histApps) && s.histApps[i] != "" {
				sub = s.histApps[i] + " · " + sub
			}
			b.WriteString(`<div class="trk-row` + selCls + `" data-act="lib-session:` + fmt.Sprint(i) + `">` +
				`<span class=trk-ic>🗓</span>` +
				`<span class=trk-main><span class=trk-title>` + html.EscapeString(sm.StartedAt.Format("2006-01-02 15:04")) + `</span>` +
				`<span class=trk-sub>` + html.EscapeString(sub) + `</span></span></div>`)
		}
		b.WriteString(`</div>`)
	}
	if len(s.played) > 0 {
		// sort: one dropdown + direction chip (was a 9-chip wall)
		cur := s.playSort
		if cur == "" {
			cur = "Play order"
		}
		sortOpts := func() []ssOpt {
			opts := make([]ssOpt, 0, 8)
			for _, so := range []string{"Play order", "Artist", "Title", "BPM", "Key", "Genre", "Rating", "Plays"} {
				opts = append(opts, ssOpt{Val: so, Label: i18n.T("library.playsort." + strings.ToLower(strings.ReplaceAll(so, " ", "")))})
			}
			return opts
		}
		b.WriteString(`<div class=lib-toolbar><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.played")) + `</span>` +
			`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(i18n.T("library.label.sort")) + `</span>` +
			smartSelect("libplaysort", "", "lib-play-sort:", cur, sortOpts) + `</span>`)
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

func (u *UI) libIDMarksHTML() string {
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

func (u *UI) libPresetsHTML() string {
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
	if u.ceActiveFor("library") {
		return u.ceDetailHTML(s)
	}
	sel := s.sel
	if sel == nil {
		// Collection rail without a selection = the beatgrid cockpit / health card
		if u.libSectionOr() == "collection" {
			return u.gfRailHTML(s)
		}
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
	verifyBtn := ""
	if u.libSectionOr() == "collection" && u.svc.Cfg.Features.GridFix.Enabled {
		if vs := u.gfVerified(); vs != nil {
			lbl, variant := i18n.T("library.gf.markVerified"), "outline"
			if vs.Has(sel.path) {
				lbl, variant = "✓ "+i18n.T("library.gf.verifiedBadge"), "primary"
			}
			verifyBtn = btn(lbl, variant, "gf-verify:"+sel.path, "")
		}
	}
	ceBtn := ""
	if sel.inColl && sel.kind == "audio" && len(sel.track.Beatgrid) > 0 {
		ceBtn = btn(i18n.T("library.ce.open"), "outline", "ce-open:"+sel.path, "")
	}
	act := `<div class=btn-row>` + btn(i18n.T("library.open"), "outline", "lib-openext:"+sel.path, "") + btn(i18n.T("library.reveal"), "outline", "lib-reveal:"+sel.path, "") +
		verifyBtn + ceBtn +
		btn(i18n.T("library.metadata"), "ghost", "lib-probe:"+sel.path, "") + btn(i18n.T("library.copyPath"), "ghost", "copy", "") + `</div>`
	if !onDisk {
		act = `<p class=page-sub>` + html.EscapeString(i18n.T("library.insp.missing")) + `</p>` + act
	}
	b.WriteString(inspSec(i18n.T("library.insp.actions"), act))

	// PLAYER + waveform (audio on disk) - the unified media player/editor (player.go)
	if onDisk && sel.kind == "audio" {
		u.mpEnsureFile("library", sel.path, sel.track)
		u.mpSetDrops("library", sel.path, s.dropsIdx[sel.path])
		b.WriteString(inspSec(i18n.T("library.insp.player"), u.mpHTML("library")))
	}
	// ENCODE builder (audio + video). In collection/playlist context (incl. files
	// living in a playlist-marked folder) the per-file encoder folds away - whole
	// dirs/playlists re-encode via the batch flow; recordings + video keep it up front.
	if sel.kind == "audio" || sel.kind == "video" {
		if sel.kind == "audio" && !s.encOpen && u.libEncDemoted(sel.path) {
			b.WriteString(inspSec(i18n.T("library.insp.encoding"),
				`<p class=page-sub>`+html.EscapeString(i18n.T("library.enc.demotedNote"))+`</p>`+
					btnRow(btn(i18n.T("library.enc.show"), "ghost", "lib-enc-open", ""))))
		} else {
			b.WriteString(inspSec(i18n.T("library.insp.encoding"), u.libEncodeHTML(s, sel)))
		}
	}
	// HARMONIC key-wheel (audio with a key)
	if sel.kind == "audio" {
		if _, ok := musiclib.ParseKey(sel.track.Key); ok {
			b.WriteString(inspSec(i18n.T("library.insp.harmonic"), u.libHarmonicHTML(s, sel)))
		}
	}
	// TAGS (collection audio): library→file sync buttons + the manual tag editor
	if sel.inColl && sel.kind == "audio" {
		b.WriteString(inspSec(i18n.T("library.insp.tags"), `<p class=page-sub>`+html.EscapeString(i18n.T("library.insp.tagsDesc"))+`</p>`+
			btnRow(btn(i18n.T("library.insp.writeTags"), "primary", "lib-tags-write:"+sel.path, ""), btn(i18n.T("library.revert"), "ghost", "lib-tags-revert:"+sel.path, ""))+
			u.tfEditorHTML(s, sel)))
	}
	// PLAYLISTS membership
	if sel.kind == "audio" {
		b.WriteString(inspSec(i18n.T("library.insp.playlists"), u.libTrackPlaylistsHTML(sel.path)))
	}
	// WORKS WELL TOGETHER (compat marks + discovery)
	if sel.inColl && sel.kind == "audio" && u.svc.Lib != nil {
		b.WriteString(inspSec(i18n.T("library.compat.section"), u.libCompatSectionHTML(s, sel.path)))
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
		chips += fchip(p.Name, "", fmt.Sprintf("lib-plgoto:%d", p.ID), false)
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
	b.WriteString(pbSelectTip(i18n.T("library.enc.container"), "lib-pf:container", containerOpts, d.Container, "enc-container"))
	if !audioOnly {
		b.WriteString(`<div class=pb-grp>`)
		b.WriteString(pbSelectTip(i18n.T("library.enc.videoCodec"), "lib-pf:vcodec", videoCodecOpts, d.VideoCodec, "enc-video-codec"))
		b.WriteString(pbSelect(i18n.T("library.enc.accel"), "lib-pf:accel", accelOpts(), d.Accel))
		// quality profiles
		b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(i18n.T("library.enc.qualityProfile")) + `</div><div class=seg>`)
		for _, pr := range transcode.Profiles {
			b.WriteString(fchip(pr, pr, "lib-pf:profile", false))
		}
		b.WriteString(`</div><div class=pb-hint>` + html.EscapeString(profileHint(profileOfDraft(d))) + `</div></div>`)
		b.WriteString(pbSelectTip(i18n.T("library.enc.rateMode"), "lib-pf:ratemode", [][2]string{{"crf", i18n.T("library.enc.rateCRF")}, {"bitrate", i18n.T("library.enc.rateBitrate")}}, d.RateMode, "enc-rate"))
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
	b.WriteString(pbSelectTip(i18n.T("library.enc.audioCodec"), "lib-pf:acodec", audioCodecOpts, d.AudioCodec, "enc-audio-codec"))
	b.WriteString(pbField(i18n.T("library.enc.audioBitrate"), "lib-pf:abitratek", strconv.Itoa(d.AudioBitrateK), "number", audioCapHint(d.AudioCodec)))
	b.WriteString(pbSelect(i18n.T("library.enc.channels"), "lib-pf:channels", [][2]string{{"0", i18n.T("library.enc.source")}, {"1", i18n.T("library.enc.mono")}, {"2", i18n.T("library.enc.stereo")}}, strconv.Itoa(d.Channels)))
	b.WriteString(pbSelect(i18n.T("library.enc.sampleRate"), "lib-pf:samplerate", [][2]string{{"0", i18n.T("library.enc.source")}, {"44100", "44.1 kHz"}, {"48000", "48 kHz"}, {"96000", "96 kHz"}}, strconv.Itoa(d.SampleRate)))
	b.WriteString(`</div>`)
	// loudness
	b.WriteString(`<div class=pb-grp>`)
	b.WriteString(toggleRowTip(i18n.T("library.enc.normalize"), "lib-pf:loudon", d.LoudnessOn, tipTopic("enc-loudness")))
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
	b.WriteString(`<div class=pb-hint>` + html.EscapeString(i18n.T("library.enc.outputNote")) + `</div>`)
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

// srCurrent assembles the draft rules (Genres from the chip map, sorted; compat depth
// normalized). Caller holds s.mu.
func (s *libSt) srCurrent() musiclib.SmartRules {
	out := s.srRules
	out.Genres = nil
	for g, on := range s.srGenres {
		if on {
			out.Genres = append(out.Genres, g)
		}
	}
	sort.Strings(out.Genres)
	switch {
	case out.CompatWith == "":
		out.CompatDepth = 0
	case out.CompatDepth < 1:
		out.CompatDepth = 1
	}
	return out
}

// libSRCountText is the live match-count line. Caller holds s.mu (tracks hydrated).
func (u *UI) libSRCountText(s *libSt) string {
	cur := s.srCurrent()
	return i18n.T("library.sr.countText", i18n.A{"count": fmt.Sprint(len(u.filterSmartDB(s.tracks, cur))), "total": fmt.Sprint(len(s.tracks)), "desc": cur.Describe()})
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
	// works-together anchor: caller-prepped compat set becomes the rule predicate
	b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(i18n.T("library.sr.compat")) + `</div>` +
		libSRCompatPicker(s.tracks, r.CompatWith))
	if r.CompatWith != "" {
		depth2 := r.CompatDepth >= 2
		b.WriteString(`<div class=seg>` + fchip(i18n.T("library.sr.compatDirect"), "", "lib-sr-depth:1", !depth2) +
			fchip(i18n.T("library.sr.compatDepth2"), "", "lib-sr-depth:2", depth2) + `</div>`)
	}
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.sr.compatHint")) + `</p></div>`)
	b.WriteString(`<div id=lib-sr-count class=sr-count>` + html.EscapeString(u.libSRCountText(s)) + `</div>`)
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

// libSRCompatPicker: filterable anchor-track picker for the compat rule. Captures the
// tracks slice (NOT s - the opts closure runs off the render path, no s.mu). Unfiltered
// open shows a capped slice; the filter pre-filters server-side via ssFilter.
func libSRCompatPicker(tracks []musiclib.Track, cur string) string {
	const capRows = 60
	return smartSelect("lib-sr-compat", "", "lib-sr-compat:", cur, func() []ssOpt {
		q := strings.ToLower(strings.TrimSpace(ssFilter("lib-sr-compat")))
		opts := []ssOpt{{Val: "", Label: i18n.T("library.sr.compatNone")}}
		if cur != "" { // anchor always listed so the closed control shows its label
			opts = append(opts, ssOpt{Val: cur, Label: libSRTrackLabel(tracks, cur), Sub: cur})
		}
		for _, t := range tracks {
			if len(opts) >= capRows {
				break
			}
			if t.Path == cur {
				continue
			}
			label := trackTitle(t)
			if q != "" && !strings.Contains(strings.ToLower(label+" "+t.Path), q) {
				continue
			}
			opts = append(opts, ssOpt{Val: t.Path, Label: label, Sub: t.Path})
		}
		return opts
	})
}

// libSRTrackLabel resolves a path's display title from the collection (else file name).
func libSRTrackLabel(tracks []musiclib.Track, path string) string {
	for _, t := range tracks {
		if t.Path == path {
			return trackTitle(t)
		}
	}
	return filepath.Base(path)
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

// pbSelectTip = pbSelect with a shared-glossary tooltip (tooltip.go topic) beside the label.
func pbSelectTip(label, act string, opts [][2]string, current, topic string) string {
	id := strings.ReplaceAll(act, ":", "-")
	lbl := `<span class=ss-label>` + html.EscapeString(label) + tipTopic(topic) + `</span>`
	return smartSelectRaw(id, lbl, act, current, func() []ssOpt {
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
