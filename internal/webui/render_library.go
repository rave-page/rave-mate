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
	"rave.page/mate/internal/zigui"
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

	// detail-rail data (works-together partners + playlist membership): two DB queries once ran
	// per detail render (= per keystroke); resolved off-thread + cached here, re-resolved when the
	// Compat/Playlist epochs move. detReady=false only until the first resolve lands.
	detCompat    []libdb.CompatRow
	detPls       []libdb.PlaylistRow
	detCompatVer int64
	detPlVer     int64
	detReady     bool
	detBusy      bool
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

	// ── render caches (render must stay pure+fast: memoized, epoch/generation-invalidated) ──
	// collView: filtered+sorted index list, keyed by loadGen + the three libdb epochs + a hash of
	// the sort/filter controls (was recomputed every render, incl. selection-only re-renders).
	collViewIdx []int
	collViewSig uint64
	collViewOK  bool
	// on-disk existence for the rendered rows: os.Stat per row froze render; swept off-thread and
	// read from the map (unknown path = neutral/present until the sweep lands). Dropped on reload.
	onDiskCk   map[string]bool
	onDiskBusy bool
	onDiskAt   time.Time
	onDiskGen  int
	// ListPlaylists rows (a per-row COUNT subquery; was issued 2-3× per render), cached by PlaylistVersion().
	plRows    []libdb.PlaylistRow
	plRowsVer int64
	plRowsGen int // s.loadGen at cache fill: a reload (e.g. Cleanup drops playlists) invalidates without touching plVer
	plRowsOK  bool
	// smart-playlist match counts (a full ~23k scan + compat DB read per smart list), computed
	// off-thread, keyed by lib+playlist+compat epochs + a hash of every smart rule set.
	smartCounts     map[int64]int
	smartCountsSig  uint64
	smartCountsOK   bool
	smartCountsBusy bool
	// facet dropdown options (distinctCounts over all tracks: genre+label), keyed by loadGen.
	facetGen     int
	facetGenre   [][2]string
	facetLabel   [][2]string
	facetGenreOK bool
	facetLabelOK bool

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

// renderLibrary: Zig-rendered tab (native/zigui/src/library*.zig). Go resolves everything into
// libState (render_library_state.go); the pure renderers below stay the untagged fallback + the
// golden reference (zigui_golden_library_test.go).
func (u *UI) renderLibrary() string {
	st := u.libraryState()
	if zigui.Available() {
		if h, ok := zigui.RenderLibrary(stateJSON(st)); ok {
			return h
		}
	}
	return libraryHTML(st)
}

// libraryHTML is the pure tab renderer.
func libraryHTML(st libState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.NavTitle))
	b.WriteString(st.Switcher)
	if !st.Embedded {
		// remote mirror / remote cue edit: the embedded peer view carries its OWN section
		// tabs - a local duplicate row would be dead weight and shadow ctl clicks
		pairs := make([][2]string, 0, len(st.Tabs))
		for _, t := range st.Tabs {
			pairs = append(pairs, [2]string{t.Val, t.Label})
		}
		b.WriteString(subTabs("lib-section:", st.Section, pairs...))
	}
	b.WriteString(`<div id=lib-body>` + libBodyHTML(st.Body) + `</div>`)
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

func (u *UI) libPatchBody() {
	u.eval("window.__patch('lib-body'," + jsQuote(u.libBody()) + ")")
	u.mpResync() // heal any player patch the build raced (state changed mid-build)
}

func (u *UI) libPatchDetail() {
	s := u.lib()
	s.mu.Lock()
	h := u.libDetailRender(s)
	s.mu.Unlock()
	u.eval("window.__patch('lib-detail'," + jsQuote(h) + ")")
	u.mpResync() // heal any player patch the build raced (state changed mid-build)
}

// libBody builds the active section (#lib-body patch target). When a peer is targeted it routes
// to the live mirror (library_mirror.go) - the peer's own rendered Library tab, remote-driven.
func (u *UI) libBody() string {
	st := u.libBodyState()
	if zigui.Available() {
		if h, ok := zigui.RenderLibraryBody(stateJSON(st)); ok {
			return h
		}
	}
	return libBodyHTML(st)
}

// libBodyHTML is the pure #lib-body renderer: one section per kind.
func libBodyHTML(st libBodySt) string {
	switch st.Kind {
	case libBodyRaw:
		return st.Raw
	case libBodyMsg:
		return emptyState(st.Msg)
	case libBodyFav:
		return libFavHTML(st.Fav)
	case libBodyColl:
		pane := triPane(st.NavRail, libCollHTML(st.Coll), libDetailWrapHTML(st.Detail), "lib-nav-w", "lib-det-w")
		if st.CEFull {
			// cue-edit mode: the waveform (grid + markers) spans the full tab width
			// above the list; the rail keeps only the editor controls.
			return `<div class=ce-fullwave>` + st.CEWave + `</div>` + pane
		}
		return pane
	case libBodyPls:
		return masterDetail(libPlsHTML(st.Pls), libDetailWrapHTML(st.Detail))
	case libBodyHist:
		return masterDetailWide(libHistHTML(st.Hist), libDetailWrapHTML(st.Detail))
	case libBodyIDMarks:
		return libIDMHTML(st.IDM)
	case libBodyQueue:
		return `<div id=lib-queue-body>` + libQueueBodyHTML(st.Queue) + `</div>`
	case libBodyPresets:
		return libPresetsHTMLOf(st.Presets)
	default:
		return triPane(st.NavRail, libBrowseHTMLOf(st.Browse), libDetailWrapHTML(st.Detail), "lib-nav-w", "lib-det-w")
	}
}

// libDetailWrap renders the inspector inside its patch target. Caller holds s.mu.
func (u *UI) libDetailWrap(s *libSt) string {
	return `<div id=lib-detail>` + u.libDetailRender(s) + `</div>`
}

// libDetailRender resolves + renders the inspector (#lib-detail inner). Caller holds s.mu.
func (u *UI) libDetailRender(s *libSt) string {
	st := u.libDetailState(s)
	if zigui.Available() {
		if h, ok := zigui.RenderLibraryDetail(stateJSON(st)); ok {
			return h
		}
	}
	return libDetailHTMLOf(st)
}

func libDetailWrapHTML(st libDetailSt) string {
	return `<div id=lib-detail>` + libDetailHTMLOf(st) + `</div>`
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

// collOnDiskFresh is how long the collection on-disk existence cache serves before a bg re-sweep.
const collOnDiskFresh = 5 * time.Second

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

// libBrowseHTMLOf is the pure Browse-pane renderer.
func libBrowseHTMLOf(st libBrowseSt) string {
	if st.Msg != "" {
		return emptyState(st.Msg)
	}
	var b strings.Builder
	// breadcrumb
	b.WriteString(`<div class=lib-crumb>`)
	for i, seg := range st.Crumbs {
		b.WriteString(btn(seg.Label, "ghost", "lib-nav:"+seg.Path, ""))
		if i < len(st.Crumbs)-1 {
			b.WriteString(`<span class=sep>›</span>`)
		}
	}
	b.WriteString(`</div>`)
	// quick-access + pinned live in the left nav rail (libNavRailHTML)
	// toolbar
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(st.Up, "outline", "lib-nav:"+st.UpPath, ""))
	b.WriteString(btn(st.Goto, "ghost", "pick-dir:lib-nav-to", ""))
	b.WriteString(fieldRaw("lib-search", st.Filter, st.FilterPH))
	// kind + sort: one dropdown each (was 8 chips + 2 labels across two wrapped rows);
	// .lib-ctl glues each label to its control across flex-wrap
	b.WriteString(`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(st.KindLbl) + `</span>`)
	b.WriteString(selHTML(st.Kind) + `</span>`)
	b.WriteString(`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(st.SortLbl) + `</span>`)
	b.WriteString(selHTML(st.Sort) + `</span>`)
	// view: segmented mode switch (mutually exclusive)
	b.WriteString(`<span class=seg>` + fchip(st.ListLbl, "", "lib-view:list", !st.Grid) +
		fchip(st.GridLbl, "", "lib-view:grid", st.Grid) + `</span>`)
	b.WriteString(st.KeyChip.html())
	// folder ops: one ⋯ menu (was four ghost buttons)
	b.WriteString(actionMenuOf(st.Folder))
	if st.SelAll {
		chkAllB := ""
		if st.SelAllOn {
			chkAllB = " checked"
		}
		b.WriteString(`<input type=checkbox class=trk-selall data-act=lib-batch-all title="` + html.EscapeString(st.SelAllTitle) + `"` + chkAllB + `>`)
	}
	b.WriteString(`<span class=lib-tlabel>` + html.EscapeString(st.Count) + `</span>`)
	b.WriteString(`</div>`)
	// folder bound to a playlist -> its actions live right here
	if st.HasBound {
		b.WriteString(`<p class=page-sub>🎵 ` + html.EscapeString(st.BoundNote) + `</p>`)
		b.WriteString(libPlActHTML(st.BoundActs))
	}

	if st.Grid {
		b.WriteString(`<div class=lib-grid>`)
		for _, it := range st.Entries {
			act, ctxAct := "lib-nav:"+it.Path, "lib-dirctx:"+it.Path
			if !it.IsDir {
				act, ctxAct = "lib-open:"+it.Path, "lib-ctx:"+it.Path
			}
			b.WriteString(`<div class=gcard data-act="` + html.EscapeString(act) + `" data-ctx="` + html.EscapeString(ctxAct) + `"><div class=gcard-ic>` + it.Glyph +
				`</div><div class=gcard-t>` + html.EscapeString(it.Name) + `</div><div class=gcard-s>` + html.EscapeString(it.GridSub) + `</div></div>`)
		}
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`<div class=trk-table>`)
		for _, it := range st.Entries {
			if it.IsDir {
				b.WriteString(`<div class=trk-row data-act="lib-nav:` + html.EscapeString(it.Path) + `" data-ctx="lib-dirctx:` + html.EscapeString(it.Path) + `"><span class=trk-ic>📁</span>` +
					`<span class=trk-main><span class=trk-title>` + html.EscapeString(it.Name) + `</span></span></div>`)
				continue
			}
			chk := ""
			if it.Checked {
				chk = " checked"
			}
			selCls := ""
			if it.Sel {
				selCls = " sel"
			}
			b.WriteString(`<div class="trk-row` + selCls + `" data-ctx="lib-ctx:` + html.EscapeString(it.Path) + `">` +
				`<input type=checkbox data-act="lib-batch:` + html.EscapeString(it.Path) + `"` + chk + `>` +
				`<span class=trk-ic data-act="lib-open:` + html.EscapeString(it.Path) + `">` + it.Glyph + `</span>` +
				`<span class=trk-main data-act="lib-open:` + html.EscapeString(it.Path) + `"><span class=trk-title>` + html.EscapeString(it.Name) +
				`</span><span class=trk-sub>` + it.Sub + `</span></span>` +
				`<span class=trk-key>` + libKeyPillHTML(it.Key) + `</span>` +
				btn("⋯", "ghost", "lib-ctx:"+it.Path, "") + `</div>`)
		}
		b.WriteString(`</div>`)
	}
	if st.More != "" {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(st.More) + `</p>`)
	}
	b.WriteString(libBatchHTML(st.Batch))
	return b.String()
}

// libBatchHTML is the pure multi-select bar renderer (browse + collection).
func libBatchHTML(st libBatchSt) string {
	if !st.On {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=batchbar><span class=cnt>` + html.EscapeString(st.Count) + `</span>`)
	for _, bt := range st.Btns {
		b.WriteString(bt.html())
	}
	b.WriteString(`</div>`)
	return b.String()
}

// actionMenuOf renders a resolved actionMenu (actionmenu.go markup) from render state.
func actionMenuOf(s selState) string { return `<span class=amenu>` + selHTML(s) + `</span>` }

// ── Favorites ───────────────────────────────────────────────────────────────

// libFavHTML is the pure pinned-folders renderer.
func libFavHTML(st libFavSt) string {
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Desc) + `</p>`)
	if len(st.Rows) == 0 {
		return b.String() + emptyState(st.Empty)
	}
	b.WriteString(`<div class="rp-card">`)
	for _, m := range st.Rows {
		b.WriteString(itemRow(m.Label, m.Path, btn(st.OpenLbl, "outline", "lib-nav:"+m.Path, ""), btn(st.UnpinLbl, "ghost", "lib-unpin:"+m.Path, "")))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Collection ──────────────────────────────────────────────────────────────

// libCollHTML is the pure Collection-pane renderer.
func libCollHTML(st libCollSt) string {
	if st.Msg != "" {
		return emptyState(st.Msg)
	}
	var b strings.Builder
	// actions: the two everyday operations + the fixer up front; everything
	// occasional lives behind Maintenance so the list gets the vertical space
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(st.ImportLbl, "primary", "lib-import", ""))
	b.WriteString(btn(st.DJSyncLbl, "primary", "lib-djsync", ""))
	if st.GridFix {
		b.WriteString(btn(st.GridFixLbl, "outline", "gf-open", ""))
	}
	b.WriteString(`<span class=lib-more>` + btn(st.MoreLbl, "ghost", "lib-more", "") + libMoreMenuHTMLOf(st) + `</span>`)
	// filters flow in the SAME wrap row: search + facet dropdowns; active facets
	// render as removable chips (one toolbar = one less stacked row above the list)
	b.WriteString(fieldRaw("lib-coll-search", st.Search, st.SearchPH))
	b.WriteString(selHTML(st.Genre))
	b.WriteString(selHTML(st.Label))
	if st.HasPlFacet {
		b.WriteString(selHTML(st.PlFacet))
	}
	b.WriteString(st.KeyChip.html())
	b.WriteString(fchip(st.NoDropsLbl, "", "lib-nodrops", st.NoDrops))
	if st.Clear {
		b.WriteString(btn(st.ClearLbl, "ghost", "lib-clearfilters", ""))
	}
	b.WriteString(st.Prep) // P-key target (library_prep.go)
	b.WriteString(`</div>`)
	for _, c := range st.Chips {
		b.WriteString(c.html())
	}
	// exactly one playlist facet active -> the collection IS that playlist's view:
	// surface its full action row inline (no Playlists-tab round-trip)
	if st.HasInline {
		b.WriteString(libPlActHTML(st.Inline))
	}
	// batch results replace the list while a fixer's results view is on
	if st.HasResults {
		return b.String() + st.Results
	}

	b.WriteString(libCollHeadHTML(st.Head))
	b.WriteString(`<div class=trk-table>`)
	for _, r := range st.Rows {
		chk := ""
		if r.Checked {
			chk = " checked"
		}
		ic := `<span class=trk-ic>🎵</span>`
		if r.Warn {
			ic = `<span class="trk-ic warn">⚠</span>`
		}
		ver := ""
		if r.Verified {
			ver = `<span class=trk-verified title="` + html.EscapeString(st.VerifiedTitle) + `">✓</span>`
		}
		b.WriteString(`<div class="trk-row` + r.SelCls + `" data-ctx="lib-ctx:` + html.EscapeString(r.Path) + `">` +
			`<input type=checkbox data-act="lib-collsel:` + html.EscapeString(r.Path) + `"` + chk + `>` + ic +
			`<span class=trk-main data-act="lib-track:` + html.EscapeString(r.Path) + `"><span class=trk-title>` +
			html.EscapeString(r.Title) + `</span><span class=trk-sub>` +
			html.EscapeString(r.Sub) + `</span></span>` + ver +
			`<span class=trk-cell-ce id=` + r.CellID + `>` + libCueCellHTMLOf(r.Cue) + `</span>` +
			`<span class=trk-bpm>` + r.BPM + `</span><span class=trk-dur>` + r.Dur + `</span>` +
			`<span class=trk-key>` + libKeyPillHTML(r.Key) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if st.IsEmpty {
		b.WriteString(emptyState(st.Empty))
	} else if st.More != "" {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(st.More) + `</p>`)
	}
	// selection bar: playlist add + verified-grid marking; in cue-edit mode the checked
	// rows are the mass-apply set for the assigned patterns
	b.WriteString(libBatchHTML(st.Batch))
	return b.String()
}

// libCollHeadHTML renders the collection table header (sortable columns carry the arrow).
func libCollHeadHTML(st libCollHeadSt) string {
	hdr := func(h libCollHdrSt) string {
		return `<span class="` + h.Cls + ` trk-sortable" data-act="lib-coll-hsort:` + h.Key + `">` + html.EscapeString(h.Label) + h.Arrow + `</span>`
	}
	chkAll := ""
	if st.SelAllOn {
		chkAll = " checked"
	}
	return `<div class=trk-h>` +
		`<input type=checkbox class=trk-selall data-act=lib-collsel-all title="` + html.EscapeString(st.SelAllTitle) + `"` + chkAll + `>` +
		hdr(st.Main) +
		`<span class=trk-cell-ce>` + html.EscapeString(st.CueLbl) + `</span>` +
		hdr(st.BPM) +
		`<span class=trk-dur>` + html.EscapeString(st.TimeLbl) + `</span>` +
		hdr(st.Key) + `</div>`
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
	var cell libCueCellSt
	if ok {
		cell = libCueCellState(s, tr)
	}
	s.mu.Unlock()
	if ok {
		u.eval("window.__patch(" + jsQuote(ceCellID(path)) + "," + jsQuote(libCueCellRender(cell)) + ")")
	}
}

// libCueCellRender renders one cue-census cell via Zig when available.
func libCueCellRender(st libCueCellSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderLibraryCueCell(stateJSON(st)); ok {
			return h
		}
	}
	return libCueCellHTMLOf(st)
}

// libCueCellHTMLOf: compact drops/cues census - ◆n amber = drop markers, ⚑n = cues;
// dim glyphs mark absence so prepared vs unprepared scans at a glance.
func libCueCellHTMLOf(st libCueCellSt) string {
	var b strings.Builder
	if st.Drops > 0 {
		b.WriteString(`<span class=trk-drops title="` + html.EscapeString(st.DropsTitle) + `">◆` + fmt.Sprint(st.Drops) + `</span>`)
	} else {
		b.WriteString(`<span class="trk-drops none" title="` + html.EscapeString(st.NoDropsTitle) + `">◇</span>`)
	}
	if st.Cues > 0 {
		b.WriteString(`<span class=trk-cuen title="` + html.EscapeString(st.CuesTitle) + `">⚑` + fmt.Sprint(st.Cues) + `</span>`)
	} else {
		b.WriteString(`<span class="trk-cuen none" title="` + html.EscapeString(st.NoCuesTitle) + `">⚑</span>`)
	}
	return b.String()
}

// libMoreMenuHTMLOf is the Maintenance popover (occasional collection operations).
func libMoreMenuHTMLOf(st libCollSt) string {
	if !st.MoreOpen {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-popmenu>`)
	for _, it := range st.MoreItems {
		b.WriteString(`<button class=lib-popitem data-act="lib-morego:` + it.Val + `">` + html.EscapeString(it.Label) + `</button>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// libFacetOpts returns the cached distinct-value counts for a collection facet (genre|label),
// rebuilt only when the loaded-track generation changes (distinctCounts is a full O(N) scan over
// ~23k tracks; it ran twice per render inside the smartSelect opts closure). Caller holds s.mu.
func (u *UI) libFacetOpts(s *libSt, kind string, keyOf func(musiclib.Track) string) [][2]string {
	if s.facetGen != s.loadGen {
		s.facetGen = s.loadGen
		s.facetGenre, s.facetLabel = nil, nil
		s.facetGenreOK, s.facetLabelOK = false, false
	}
	if kind == "label" {
		if !s.facetLabelOK {
			s.facetLabel, s.facetLabelOK = distinctCounts(s.tracks, keyOf, 200), true
		}
		return s.facetLabel
	}
	if !s.facetGenreOK {
		s.facetGenre, s.facetGenreOK = distinctCounts(s.tracks, keyOf, 200), true
	}
	return s.facetGenre
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
	if len(want) > 0 { // facet narrowed the view: checked rows outside it would silently
		for p := range s.collSel { // ride into batch actions - keep the selection visible-only
			if !set[p] {
				delete(s.collSel, p)
			}
		}
	}
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

// libEpochs reads the three libdb cache-invalidation epochs (0 when the DB is absent).
func (u *UI) libEpochs() (libVer, plVer, compatVer int64) {
	if u.svc.Lib != nil {
		return u.svc.Lib.LibraryVersion(), u.svc.Lib.PlaylistVersion(), u.svc.Lib.CompatVersion()
	}
	return 0, 0, 0
}

// libCollView returns collView memoized: it re-filtered+sorted ~23k tracks every render (incl.
// selection-only re-renders). Keyed by loadGen + the three epochs + a hash of the sort/filter
// controls; recomputed only when an input moves. Caller holds s.mu.
func (u *UI) libCollView(s *libSt) []int {
	libVer, plVer, compatVer := u.libEpochs()
	sig := s.collViewSignature(libVer, plVer, compatVer)
	if s.collViewOK && s.collViewSig == sig {
		return s.collViewIdx
	}
	idx := s.collView()
	s.collViewSig, s.collViewIdx, s.collViewOK = sig, idx, true
	return idx
}

// collViewSignature hashes every input collView reads. tracks content is proxied by loadGen+libVer
// (in-place edits land via a DB write that bumps LibraryVersion); collPlSet by collPl ids + the
// playlist/compat epochs (it's a pure function of those); dropsIdx (collNoDrops presence test) by
// len (it only holds tracks WITH drops). Caller holds s.mu.
func (s *libSt) collViewSignature(libVer, plVer, compatVer int64) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "g%d|l%d|p%d|c%d|q%q|o%s|d%t|nd%t|di%d",
		s.loadGen, libVer, plVer, compatVer, s.collSearch, s.collSort, s.collDesc, s.collNoDrops, len(s.dropsIdx))
	for _, set := range []struct {
		tag string
		m   map[string]bool
	}{{"k", s.keySel}, {"ge", s.collGenre}, {"la", s.collLabel}} {
		keys := make([]string, 0, len(set.m))
		for k := range set.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "|%s%s", set.tag, k)
		}
	}
	ids := make([]int64, 0, len(s.collPl))
	for id := range s.collPl {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		fmt.Fprintf(h, "|pl%d", id)
	}
	return h.Sum64()
}

// libEnsureOnDisk warms the on-disk existence cache for the rendered rows, sweeping os.Stat
// OFF-THREAD (per-row stat in render froze the UI on a 300-row set / a spun-down drive). Render
// reads s.onDiskOr; unknown paths read as present until the sweep lands. Refreshed on a TTL and
// dropped on library reload. Caller holds s.mu.
func (u *UI) libEnsureOnDisk(s *libSt, paths []string) {
	if s.onDiskGen != s.loadGen { // library reloaded: prior results may be stale (files moved)
		s.onDiskGen = s.loadGen
		s.onDiskCk = nil
		s.onDiskAt = time.Time{}
	}
	if s.onDiskBusy {
		return
	}
	stale := time.Since(s.onDiskAt) > collOnDiskFresh
	unknown := false
	for _, p := range paths {
		if _, ok := s.onDiskCk[p]; !ok {
			unknown = true
			break
		}
	}
	if !stale && !unknown {
		return
	}
	s.onDiskBusy = true
	want := append([]string(nil), paths...)
	u.bg(func() {
		res := make(map[string]bool, len(want))
		for _, p := range want {
			res[p] = pathOnDisk(p)
		}
		s.mu.Lock()
		s.onDiskBusy = false
		s.onDiskAt = time.Now()
		if s.onDiskCk == nil {
			s.onDiskCk = make(map[string]bool, len(res))
		}
		changed := false
		for p, ex := range res {
			if old, ok := s.onDiskCk[p]; !ok || old != ex {
				changed = true
			}
			s.onDiskCk[p] = ex
		}
		s.mu.Unlock()
		if changed && !u.stopped() {
			u.libPatchBody()
		}
	})
}

// onDiskOr reports path existence from the swept cache; an un-swept path reads as present
// (neutral) until libEnsureOnDisk fills it in. Caller holds s.mu.
func (s *libSt) onDiskOr(path string) bool {
	if ex, ok := s.onDiskCk[path]; ok {
		return ex
	}
	return true
}

// libPlaylists returns the playlist rows for this render pass, cached by PlaylistVersion()
// (ListPlaylists issues a per-row COUNT subquery and was called 2-3× per render). Caller holds s.mu.
func (u *UI) libPlaylists(s *libSt) []libdb.PlaylistRow {
	if u.svc.Lib == nil {
		return nil
	}
	ver := u.svc.Lib.PlaylistVersion()
	if s.plRowsOK && s.plRowsVer == ver && s.plRowsGen == s.loadGen {
		return s.plRows
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	s.plRows, s.plRowsVer, s.plRowsGen, s.plRowsOK = rows, ver, s.loadGen, true
	return rows
}

// libSmartCounts returns cached smart-playlist match counts (id→count), computed OFF-THREAD:
// each count is len(filterSmartDB(...)) = a full ~23k scan + a compat DB read, and it ran per
// smart list per render. Keyed by all three epochs it depends on (library + playlist + compat)
// plus a hash of every smart rule set; render shows a placeholder until the first compute lands.
// Caller holds s.mu.
func (u *UI) libSmartCounts(s *libSt, rows []libdb.PlaylistRow) map[int64]int {
	if u.svc.Lib == nil {
		return nil
	}
	libVer, plVer, compatVer := u.libEpochs()
	sig := libSmartCountSig(s.loadGen, libVer, plVer, compatVer, rows)
	if s.smartCountsOK && s.smartCountsSig == sig {
		return s.smartCounts
	}
	if s.smartCountsBusy {
		return s.smartCounts // stale/nil until the in-flight compute lands
	}
	smart := make([]libdb.PlaylistRow, 0)
	for _, p := range rows {
		if p.Kind == libdb.PlaylistSmart {
			smart = append(smart, p)
		}
	}
	if len(smart) == 0 { // nothing to eval: mark this sig ready (empty)
		s.smartCounts, s.smartCountsSig, s.smartCountsOK = map[int64]int{}, sig, true
		return s.smartCounts
	}
	s.smartCountsBusy = true
	trk := s.tracks // immutable after load (replaced wholesale on reload) - safe to read off-thread
	u.bg(func() {
		counts := make(map[int64]int, len(smart))
		for _, p := range smart {
			if r, ok := libParseRules(p.Rules); ok {
				counts[p.ID] = len(u.filterSmartDB(trk, r))
			}
		}
		s.mu.Lock()
		s.smartCounts, s.smartCountsSig, s.smartCountsOK, s.smartCountsBusy = counts, sig, true, false
		s.mu.Unlock()
		if !u.stopped() {
			u.libPatchBody()
		}
	})
	return s.smartCounts
}

// libSmartCountSig hashes loadGen + the three epochs + every smart rule set, so a library/playlist/
// compat mutation, a reload (Cleanup drops tracks without a libVer bump), OR a rules edit invalidates
// the counts. loadGen matches collViewSignature: every memo that scans s.tracks must key on it.
func libSmartCountSig(loadGen int, libVer, plVer, compatVer int64, rows []libdb.PlaylistRow) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "g%d|l%d|p%d|c%d", loadGen, libVer, plVer, compatVer)
	for _, p := range rows {
		if p.Kind == libdb.PlaylistSmart {
			fmt.Fprintf(h, "|%d=%s", p.ID, p.Rules)
		}
	}
	return h.Sum64()
}

// libDetailData returns the selected track's works-together partners + playlist memberships,
// resolved OFF-THREAD and cached on the selection: both were per-detail-render DB queries and the
// detail rebuilds on every collection body render (= every keystroke). Re-resolves when the
// Compat/Playlist epochs move; ready=false only until the first resolve lands. Caller holds s.mu.
func (u *UI) libDetailData(s *libSt, path string) (compat []libdb.CompatRow, pls []libdb.PlaylistRow, ready bool) {
	sel := s.sel
	if sel == nil || sel.path != path || u.svc.Lib == nil {
		return nil, nil, true // no library / no selection: sections render their normal empty state
	}
	cv, pv := u.svc.Lib.CompatVersion(), u.svc.Lib.PlaylistVersion()
	if sel.detReady && sel.detCompatVer == cv && sel.detPlVer == pv {
		return sel.detCompat, sel.detPls, true
	}
	if !sel.detBusy {
		sel.detBusy = true
		u.bg(func() {
			c, _ := u.svc.Lib.CompatFor(path)
			p, _ := u.svc.Lib.PlaylistsForTrack(path)
			s.mu.Lock()
			if s.sel != nil && s.sel.path == path {
				s.sel.detCompat, s.sel.detPls = c, p
				s.sel.detCompatVer, s.sel.detPlVer = cv, pv
				s.sel.detReady, s.sel.detBusy = true, false
			}
			s.mu.Unlock()
			if !u.stopped() {
				u.libPatchDetail()
			}
		})
	}
	return sel.detCompat, sel.detPls, sel.detReady // stale-until-ready (empty on first resolve)
}

// ── Playlists ───────────────────────────────────────────────────────────────

// libPlsHTML is the pure Playlists-section renderer.
func libPlsHTML(st libPlsSt) string {
	if st.Msg != "" {
		return emptyState(st.Msg)
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	b.WriteString(btn(st.NewLbl, "primary", "lib-pl-new", ""))
	b.WriteString(btn(st.NewSmartLbl, "outline", "lib-pl-newsmart", ""))
	if st.HasCloud {
		// cloud ops are occasional - one ⋯ menu instead of three toolbar buttons
		b.WriteString(actionMenuOf(st.Cloud))
	}
	b.WriteString(`</div>`)
	if len(st.Rows) == 0 {
		b.WriteString(emptyState(st.Empty))
	}
	// dense rows: the row itself opens (no per-row Open button)
	b.WriteString(`<div class=trk-table>`)
	for _, p := range st.Rows {
		selCls := ""
		if p.Sel {
			selCls = " sel"
		}
		b.WriteString(`<div class="trk-row` + selCls + `" data-act="lib-pl:` + p.ID + `">` +
			`<span class=trk-ic>` + p.Icon + `</span>` +
			`<span class=trk-main><span class=trk-title>` + html.EscapeString(p.Name) + `</span>` +
			`<span class=trk-sub>` + html.EscapeString(p.Sub) + `</span></span></div>`)
	}
	b.WriteString(`</div>`)

	// open playlist tracks
	if st.HasOpen {
		b.WriteString(libPlOpenHTML(st.Open))
	}
	return b.String()
}

// libPlActHTML: one playlist's action row - shared by the Playlists open view, the
// Collection inline panel (single playlist facet), and Browse (a playlist-bound folder).
// Density: everyday actions stay as buttons; occasional ones demote into a ⋯ actionMenu
// (full labels + Sub hints keep them discoverable).
func libPlActHTML(st libPlActSt) string {
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	for _, bt := range st.Btns {
		b.WriteString(bt.html())
	}
	b.WriteString(actionMenuOf(st.Menu))
	b.WriteString(`</div>`)
	return b.String()
}

// libFolderPlaylist returns the playlist bound to dir (imported folder / manual mark). Caller holds s.mu.
func (u *UI) libFolderPlaylist(s *libSt, dir string) *libdb.PlaylistRow {
	if u.svc.Lib == nil {
		return nil
	}
	d := filepath.Clean(dir)
	rows := u.libPlaylists(s)
	for i := range rows {
		if rows[i].Folder != "" && filepath.Clean(rows[i].Folder) == d {
			return &rows[i]
		}
	}
	return nil
}

// libPlOpenHTML renders the opened playlist's tracks.
func libPlOpenHTML(st libPlOpenSt) string {
	var b strings.Builder
	b.WriteString(section(st.Title, ""))
	if st.SmartNote != "" {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(st.SmartNote) + `</p>`)
	}
	b.WriteString(libPlActHTML(st.Acts))
	b.WriteString(`<div class=trk-table>`)
	for _, it := range st.Items {
		var actions string
		if it.Manual {
			actions = btn("↑", "ghost", "lib-pl-up:"+it.Idx, "") + btn("↓", "ghost", "lib-pl-down:"+it.Idx, "") +
				btn("✕", "ghost", "lib-pl-rm:"+it.Path, "")
		}
		sel := ""
		if it.Path != "" {
			sel = `data-act="lib-track:` + html.EscapeString(it.Path) + `"`
		}
		b.WriteString(`<div class=trk-row><span class=trk-pos>` + it.Pos + `</span>` +
			`<span class=trk-main ` + sel + `><span class=trk-title>` + html.EscapeString(it.Title) + `</span></span>` +
			`<span class=trk-key>` + libKeyPillHTML(it.Key) + `</span>` + actions + `</div>`)
	}
	b.WriteString(`</div>`)
	if len(st.Items) == 0 {
		b.WriteString(emptyState(st.Empty))
	}
	return b.String()
}

// ── History ─────────────────────────────────────────────────────────────────

// libHistHTML is the pure History-section renderer.
func libHistHTML(st libHistSt) string {
	var b strings.Builder
	// source picker: every DJ software with a play-history model (Traktor NML history
	// dir, Rekordbox master.db djmdHistory). VirtualDJ keeps no session history.
	b.WriteString(`<div class=lib-toolbar>` + btn(st.LoadLbl, "primary", "lib-hist-load", "") +
		selHTML(st.Src) + `</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Desc) + `</p>`)
	if st.IsEmpty {
		b.WriteString(emptyState(st.Empty))
	} else {
		// dense rows: the row itself opens the session (no per-row Open button)
		b.WriteString(`<div class=trk-table>`)
		for _, sm := range st.Sessions {
			selCls := ""
			if sm.Sel {
				selCls = " sel"
			}
			b.WriteString(`<div class="trk-row` + selCls + `" data-act="lib-session:` + sm.Idx + `">` +
				`<span class=trk-ic>🗓</span>` +
				`<span class=trk-main><span class=trk-title>` + html.EscapeString(sm.Date) + `</span>` +
				`<span class=trk-sub>` + html.EscapeString(sm.Sub) + `</span></span></div>`)
		}
		b.WriteString(`</div>`)
	}
	if st.HasPlayed {
		// sort: one dropdown + direction chip (was a 9-chip wall)
		b.WriteString(`<div class=lib-toolbar><span class=lib-tlabel>` + html.EscapeString(st.PlayedLbl) + `</span>` +
			`<span class=lib-ctl><span class=lib-tlabel>` + html.EscapeString(st.SortLbl) + `</span>` +
			selHTML(st.Sort) + `</span>`)
		b.WriteString(fchip(st.DirLbl, "", "lib-play-dir", false))
		b.WriteString(`</div>`)
		b.WriteString(`<div class=trk-table>`)
		for _, p := range st.Played {
			ic := `<span class=trk-ic>🎵</span>`
			if p.Warn {
				ic = `<span class="trk-ic warn">⚠</span>`
			}
			b.WriteString(`<div class=trk-row>` + ic + `<span class=trk-main data-act="lib-track:` + html.EscapeString(p.Path) +
				`"><span class=trk-title>` + html.EscapeString(p.Title) +
				`</span><span class=trk-sub>` + html.EscapeString(p.Meta) + `</span></span><span class=trk-key>` +
				libKeyPillHTML(p.Key) + `</span></div>`)
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

// libIDMHTML is the pure ID-Marks-section renderer.
func libIDMHTML(st libIDMSt) string {
	if st.Msg != "" {
		return emptyState(st.Msg)
	}
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>` + btn(st.MarkFileLbl, "primary", "pick-file:lib-id-addpath", "") +
		btn(st.MarkFolderLbl, "outline", "pick-dir:lib-id-addpath", "") + btn(st.TypePathLbl, "ghost", "lib-id-manual", "") + `</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Desc) + `</p>`)
	if len(st.Rows) == 0 {
		return b.String() + emptyState(st.Empty)
	}
	b.WriteString(`<div class="rp-card">`)
	for _, e := range st.Rows {
		b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(e.Path) + `</span>` +
			toggleRowDL(st.ArtistLbl, st.ArtistDL, e.ArtistAct, e.Artist) +
			toggleRowDL(st.LabelLbl, st.LabelDL, e.LabelAct, e.Label) +
			btn(st.RemoveLbl, "ghost", e.DelAct, "") + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Queue ───────────────────────────────────────────────────────────────────

// libQueueHTML renders the #lib-queue-body inner fragment (job progress patch target).
func (u *UI) libQueueHTML() string {
	st := u.libQueueState()
	if zigui.Available() {
		if h, ok := zigui.RenderLibraryQueue(stateJSON(st)); ok {
			return h
		}
	}
	return libQueueBodyHTML(st)
}

// libQueueBodyHTML is the pure queue renderer.
func libQueueBodyHTML(st libQueueSt) string {
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(st.Desc) + `</p>`)
	if len(st.Jobs) == 0 {
		return b.String() + emptyState(st.Empty)
	}
	for _, j := range st.Jobs {
		var trail string
		if j.Cancel {
			trail = btn(j.CancelLbl, "ghost", j.CancelAct, "")
		} else {
			trail = badge(j.Status, j.StatusVar)
		}
		b.WriteString(`<div class=qjob><div class=qjob-h><span class=qjob-t>` + html.EscapeString(j.Label) + `</span>` + trail + `</div>`)
		b.WriteString(progressBarOf(j.Width, j.Caption))
		if j.Msg != "" {
			b.WriteString(`<p class=page-sub>` + html.EscapeString(j.Msg) + `</p>`)
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

// libPresetsHTMLOf is the pure preset-catalog renderer.
func libPresetsHTMLOf(st libPresetsSt) string {
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>` + btn(st.NewLbl, "primary", "lib-pset-new", "") + `</div>`)
	b.WriteString(section(st.YoursTitle, ""))
	if len(st.Custom) == 0 {
		b.WriteString(emptyState(st.EmptyCustom))
	} else {
		b.WriteString(`<div class=pcards>`)
		for _, p := range st.Custom {
			b.WriteString(card(p.Label, badge(st.CustomBadge, "info"), `<p class=page-sub>`+html.EscapeString(p.Desc)+`</p>`+
				btnRow(btn(st.EditLbl, "outline", "lib-pset-edit:"+p.ID, ""), btn(st.DupLbl, "ghost", "lib-pset-dup:"+p.ID, ""),
					btn(st.DelLbl, "destructive", "lib-pset-del:"+p.ID, ""))))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(section(st.BuiltinsTitle, ""))
	b.WriteString(`<div class=pcards>`)
	for _, p := range st.Builtins {
		b.WriteString(card(p.Label, badge(st.BuiltinBadge, "secondary"), `<p class=page-sub>`+html.EscapeString(p.Desc)+`</p>`+
			btn(st.DupEditLbl, "outline", "lib-pset-dup:"+p.ID, "")))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── Inspector (detail pane) ─────────────────────────────────────────────────

// libDetailHTMLOf is the pure inspector renderer.
func libDetailHTMLOf(st libDetailSt) string {
	switch st.Kind {
	case libDetailRaw:
		// cue-edit rail, or the beatgrid-fixer flow / cockpit owning the collection rail
		return st.Raw
	case libDetailMsg:
		return emptyState(st.Msg)
	}
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + html.EscapeString(st.Eyebrow) + `</div><div class=insp-title>` +
		html.EscapeString(st.Title) + `</div><div class=insp-sub>` + html.EscapeString(st.Sub) + `</div></div>`)

	// ACTIONS
	act := `<div class=btn-row>`
	for _, bt := range st.ActBtns {
		act += bt.html()
	}
	act += `</div>`
	if st.Missing != "" {
		act = `<p class=page-sub>` + html.EscapeString(st.Missing) + `</p>` + act
	}
	b.WriteString(inspSec(st.ActionsTitle, act))

	// PLAYER + waveform (audio on disk) - the unified media player/editor (player.go).
	if st.HasPlayer {
		b.WriteString(inspSec(st.PlayerTitle, st.Player))
	}
	// ENCODE builder (audio + video). In collection/playlist context (incl. files
	// living in a playlist-marked folder) the per-file encoder folds away - whole
	// dirs/playlists re-encode via the batch flow; recordings + video keep it up front.
	if st.HasEnc {
		if st.EncDemoted {
			b.WriteString(inspSec(st.EncTitle,
				`<p class=page-sub>`+html.EscapeString(st.DemotedNote)+`</p>`+
					btnRow(btn(st.ShowLbl, "ghost", "lib-enc-open", ""))))
		} else {
			b.WriteString(inspSec(st.EncTitle, libEncHTML(st.Enc)))
		}
	}
	// HARMONIC key-wheel (audio with a key)
	if st.HasHarm {
		b.WriteString(inspSec(st.HarmTitle, libHarmHTML(st.Harm)))
	}
	// TAGS (collection audio): library→file sync buttons + the manual tag editor
	if st.HasTags {
		b.WriteString(inspSec(st.TagsTitle, `<p class=page-sub>`+html.EscapeString(st.TagsDesc)+`</p>`+
			btnRow(btn(st.WriteLbl, "primary", st.WriteAct, ""), btn(st.RevertLbl, "ghost", st.RevertAct, ""))+
			st.TagEditor))
	}
	// PLAYLISTS membership
	if st.HasPls {
		b.WriteString(inspSec(st.PlsTitle, libTrackPlsHTML(st.Pls)))
	}
	// WORKS WELL TOGETHER (compat marks + discovery)
	if st.HasCompat {
		b.WriteString(inspSec(st.CompatTitle, st.Compat))
	}
	// DETAILS
	b.WriteString(inspSec(st.DetailsTitle, libMetaHTML(st.Meta)))
	return b.String()
}

// libHarmHTML renders the Camelot wheel block (the SVG itself is built Go-side).
func libHarmHTML(st libHarmSt) string {
	return `<p class=page-sub>` + html.EscapeString(st.Desc) + `</p>` +
		st.Wheel + kwLegendOf(st.SameLbl, st.RelLbl) +
		btnRow(btn(st.ShowLbl, "outline", st.ShowAct, ""), btn(st.ClearLbl, "ghost", "lib-key-clear", ""))
}

// libTrackPlsHTML renders the selected track's playlist chips from the off-thread-resolved
// cache (see libDetailData); a not-yet-ready resolve shows the loading line.
func libTrackPlsHTML(st libTrackPlsSt) string {
	if st.Unavailable {
		return `<p class=page-sub>-</p>`
	}
	var chips string
	for _, c := range st.Chips {
		chips += c.html()
	}
	if chips == "" {
		chips = `<span class=page-sub>` + html.EscapeString(st.EmptyText) + `</span>`
	}
	return chips + `<div class=btn-row>` + btn(st.AddLbl, "outline", st.AddAct, "") + `</div>`
}

// libMetaHTML renders the metadata kv rows.
func libMetaHTML(rows []uiKV) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.html())
	}
	return b.String()
}

// ── Encoding-preset builder + dynamic media hints (Electron Local-Studio merge) ──

// libEncHTML is the pure encode-builder renderer.
func libEncHTML(st libEncSt) string {
	var b strings.Builder
	// preset picker (rich rows: description sub-line + container badge)
	b.WriteString(selHTML(st.Preset))
	if st.Desc != "" {
		b.WriteString(`<div class=pb-hint>` + html.EscapeString(st.Desc) + `</div>`)
	}
	// dynamic media hints (source-aware)
	if hints := libHintsHTML(st.Hints); hints != "" {
		b.WriteString(`<div class=mediahints>` + hints + `</div>`)
	}

	b.WriteString(`<div class=pbuilder>`)
	b.WriteString(selHTMLRaw(st.Container.Sel, st.Container.Label))
	if !st.AudioOnly {
		v := st.Video
		b.WriteString(`<div class=pb-grp>`)
		// container-compatible codecs only - the builder can't describe an unencodable combo
		b.WriteString(selHTMLRaw(v.VCodec.Sel, v.VCodec.Label))
		b.WriteString(selHTML(v.Accel))
		// quality profiles
		b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(v.QualityLbl) + `</div><div class=seg>`)
		for _, c := range v.Profiles {
			b.WriteString(c.html())
		}
		b.WriteString(`</div><div class=pb-hint>` + html.EscapeString(v.ProfileHint) + `</div></div>`)
		b.WriteString(selHTMLRaw(v.RateMode.Sel, v.RateMode.Label))
		b.WriteString(v.RateField.html())
		b.WriteString(selHTML(v.Res))
		b.WriteString(v.FPS.html())
		b.WriteString(`</div>`)
	}
	// audio section
	b.WriteString(`<div class=pb-grp>`)
	b.WriteString(selHTMLRaw(st.AudioCodec.Sel, st.AudioCodec.Label))
	b.WriteString(st.AudioBitrate.html())
	b.WriteString(selHTML(st.Channels))
	b.WriteString(selHTML(st.SampleRate))
	b.WriteString(`</div>`)
	// loudness - the shared block (components.go); the draft IS the preset, so no override framing
	b.WriteString(st.Loudness)
	// trim + start
	b.WriteString(st.TrimStart.html())
	b.WriteString(st.TrimEnd.html())
	b.WriteString(`</div>`)
	b.WriteString(`<div class=pb-hint>` + html.EscapeString(st.OutputNote) + `</div>`)
	b.WriteString(`<div class=btn-row>` + btn(st.StartLbl, "primary", "lib-transcode", "") +
		btn(st.SaveLbl, "outline", "lib-pset-save", "") + btn(st.SaveAsLbl, "ghost", "lib-pset-saveas", "") + `</div>`)
	return b.String()
}

// libHintsHTML renders the source-aware compareQuality chips (calm, factual - "adds no quality").
func libHintsHTML(hs []libHintSt) string {
	var b strings.Builder
	for _, h := range hs {
		b.WriteString(hint(h.Tone, h.Text))
	}
	return b.String()
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

// kwLegendOf is the wheel colour legend (relation labels resolved by the caller).
func kwLegendOf(same, relative string) string {
	return `<div class=kw-legend>` +
		`<span><i style="background:#08F79B"></i>` + html.EscapeString(same) + `</span><span><i style="background:#7C3AED"></i>` + html.EscapeString(relative) + `</span>` +
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

// pbFieldEx is the encode-builder's labelled input: optional hint under it, optional placeholder
// (a greyed default the user accepts by leaving the field blank - override surfaces need it).
// pbField is the shorthand; extend HERE rather than growing a near-copy.
// data-label matches field/fieldEx/labeledInput: without it ctl read/set cannot reach the input at
// all, which silently took the shared loudness block's LUFS + true-peak targets off the mandated
// verification path on every surface that renders it.
func pbFieldEx(label, act, value, typ, placeholder, hintTx string) string {
	return pbFieldExDL(label, strings.ToLower(label), act, value, typ, placeholder, hintTx)
}

// pbFieldExDL is pbFieldEx with a caller-resolved data-label (the Zig render path keeps
// Unicode strings.ToLower in Go) - ONE markup source for both renderers.
func pbFieldExDL(label, dataLabel, act, value, typ, placeholder, hintTx string) string {
	h := ""
	if hintTx != "" {
		h = `<div class=pb-hint>` + html.EscapeString(hintTx) + `</div>`
	}
	if typ == "" {
		typ = "text"
	}
	ph := ""
	if placeholder != "" {
		ph = ` placeholder="` + html.EscapeString(placeholder) + `"`
	}
	return `<div class=pb-field data-label=` + attrQ(dataLabel) + `><div class=pb-label>` + html.EscapeString(label) + `</div>` +
		`<input class=field-input type="` + typ + `" value="` + html.EscapeString(value) + `" data-act="` + html.EscapeString(act) + `"` + ph + `>` + h + `</div>`
}

func pbField(label, act, value, typ, hintTx string) string {
	return pbFieldEx(label, act, value, typ, "", hintTx)
}

// pbSelect: encode-builder property select - smartSelect over the same act contract
// (val on pick, like the old <select> change). id derived from the act (colon-free).
func pbSelect(label, act string, opts [][2]string, current string) string {
	return selHTML(resolvePbSelect(label, act, opts, current))
}

// pbSelectTip = pbSelect with a shared-glossary tooltip (tooltip.go topic) beside the label.
func pbSelectTip(label, act string, opts [][2]string, current, topic string) string {
	t := resolvePbSelectTip(label, act, opts, current, topic)
	return selHTMLRaw(t.Sel, t.Label)
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
	return libKeyPillHTML(libKeyPillState(keyText, ref))
}

// libKeyPillHTML is the pure key-pill renderer (state from libKeyPillState).
func libKeyPillHTML(p libKeyPillSt) string {
	if !p.Ok && p.Text == "" {
		return ""
	}
	if !p.Ok {
		return `<span class=keypill>` + html.EscapeString(p.Text) + `</span>`
	}
	return `<span class="keypill` + p.Cls + `">` + html.EscapeString(p.Text) + `</span>`
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
