package webui

// Library tab render STATE: the impure half of the Zig migration (see .devnotes/ZIG_UI_GUIDE.md).
// Everything that touches svc/cfg/locks/i18n/os resolves here into plain JSON-able structs; the
// pure renderers in render_library.go (golden reference) and native/zigui/src/library*.zig render
// the same bytes from the same input.
//
// Rules this file obeys:
//   - every slice field carries `,omitempty`: a nil Go slice marshals to JSON null, which the Zig
//     parser rejects (whole-tab silent fallback to Go);
//   - numbers are pre-formatted Go-side (no float ever crosses the ABI);
//   - `dl` fields are the Go-resolved strings.ToLower(label) (Unicode lowering stays in Go);
//   - pre-rendered markup from OTHER renderers (cue-edit wave/rail, remote mirror/cue-edit
//     bodies, player, loudness block, key wheel, tooltips) rides as trusted raw strings. The nav
//     rail, gridfix rail + results, tagfix results + editor, prep picker and compat section were
//     lifted to structured state in wave 3 (render_library_fixers.go).

import (
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/transcode"
)

// ── shared leaf state ──

// libTabSt is one [value,label] pair (subTabs items, popover menu items).
type libTabSt struct {
	Val   string `json:"val"`
	Label string `json:"label"`
}

// libChipSt is an fchip() call as state.
type libChipSt struct {
	Label  string `json:"label"`
	Val    string `json:"val"`
	Act    string `json:"act"`
	Active bool   `json:"active"`
}

func newChip(label, val, act string, active bool) libChipSt {
	return libChipSt{Label: label, Val: val, Act: act, Active: active}
}

func (c libChipSt) html() string { return fchip(c.Label, c.Val, c.Act, c.Active) }

func newBtn(label, variant, act string) uiBtn {
	return uiBtn{Label: label, Variant: variant, Act: act}
}

// libKeyPillSt is a keyPillHTML() call as state. Ok=false + empty Text = render nothing;
// Ok=false + Text = an unparsable key (rendered verbatim in the plain pill).
type libKeyPillSt struct {
	Text string `json:"text,omitempty"`
	Cls  string `json:"cls,omitempty"`
	Ok   bool   `json:"ok,omitempty"`
}

func libKeyPillState(keyText string, ref *musiclib.Key) libKeyPillSt {
	keyText = strings.TrimSpace(keyText)
	if keyText == "" {
		return libKeyPillSt{}
	}
	k, ok := musiclib.ParseKey(keyText)
	if !ok {
		return libKeyPillSt{Text: keyText}
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
	return libKeyPillSt{Text: k.Camelot(), Cls: cls, Ok: true}
}

// libHintSt is a hint() call as state.
type libHintSt struct {
	Tone string `json:"tone"`
	Text string `json:"text"`
}

// libPBFieldSt is a pbFieldEx() call as state (DL = Go strings.ToLower(Label)).
type libPBFieldSt struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Act   string `json:"act"`
	Value string `json:"value"`
	Type  string `json:"inputType"`
	PH    string `json:"ph"`
	Hint  string `json:"hint"`
}

func newPBField(label, act, value, typ, hintTx string) libPBFieldSt {
	return libPBFieldSt{Label: label, DL: strings.ToLower(label), Act: act, Value: value, Type: typ, Hint: hintTx}
}

func (f libPBFieldSt) html() string {
	return pbFieldExDL(f.Label, f.DL, f.Act, f.Value, f.Type, f.PH, f.Hint)
}

// libSelTip pairs a resolved smart select with its pre-rendered ss-label (label + tooltip
// markup from tooltip.go, which stays Go-side).
type libSelTip struct {
	Sel   selState `json:"sel"`
	Label string   `json:"labelHtml"`
}

// libBatchSt is a batchbar (browse + collection selection bars).
type libBatchSt struct {
	On    bool    `json:"on"`
	Count string  `json:"count"`
	Btns  []uiBtn `json:"btns,omitempty"`
}

// libPlActSt is one playlist's action row (Playlists open view, Collection single-facet
// panel, Browse playlist-bound folder): leading buttons + the demoted ⋯ actionMenu.
type libPlActSt struct {
	Btns []uiBtn  `json:"btns,omitempty"`
	Menu selState `json:"menu"`
}

// ── top level ──

// libState is the resolved render state for the whole Library tab.
type libState struct {
	Title    string     `json:"title"`
	NavTitle string     `json:"navTitle"`
	Switcher string     `json:"switcher"` // raw targetSwitcherHTML ("" = no peer connected)
	Embedded bool       `json:"embedded"` // remote mirror / remote cue edit carries its own tabs
	Section  string     `json:"section"`
	Tabs     []libTabSt `json:"tabs,omitempty"`
	Body     libBodySt  `json:"body"`
}

// libBody kinds (the section switch).
const (
	libBodyRaw     = "raw" // pre-rendered body from another renderer (rce / mirror)
	libBodyMsg     = "msg" // whole body is one emptyState
	libBodyBrowse  = "browse"
	libBodyFav     = "favorites"
	libBodyColl    = "collection"
	libBodyPls     = "playlists"
	libBodyHist    = "history"
	libBodyIDMarks = "idmarks"
	libBodyQueue   = "queue"
	libBodyPresets = "presets"
)

// libBodySt is the #lib-body inner state: one kind + that section's sub-state.
type libBodySt struct {
	Kind    string   `json:"kind"`
	Raw     string   `json:"raw,omitempty"`
	Msg     string   `json:"msg,omitempty"`
	NavRail libNavSt `json:"navRail"`          // triPane nav column (library_navrail.go)
	CEFull  bool     `json:"ceFull,omitempty"` // cue-edit: full-width waveform above the panes
	CEWave  string   `json:"ceWave,omitempty"` // its markup (library_cueedit.go)

	Detail  libDetailSt  `json:"detail"`
	Browse  libBrowseSt  `json:"browse"`
	Coll    libCollSt    `json:"coll"`
	Fav     libFavSt     `json:"fav"`
	Pls     libPlsSt     `json:"pls"`
	Hist    libHistSt    `json:"hist"`
	IDM     libIDMSt     `json:"idm"`
	Queue   libQueueSt   `json:"queue"`
	Presets libPresetsSt `json:"presets"`
}

// libraryState resolves the tab header + section tabs + active body.
func (u *UI) libraryState() libState {
	sec := u.libSectionOr()
	st := libState{
		Title:    i18n.T("tab.library"),
		NavTitle: i18n.T("navtitle.library"),
		Switcher: u.targetSwitcherHTML("libtarget", "lib-target:"),
		Section:  sec,
	}
	if u.rceActive() || u.libRemoteTarget() != "" {
		st.Embedded = true
		st.Body = u.libBodyState()
		return st
	}
	st.Tabs = []libTabSt{
		{"browse", i18n.T("library.section.browse")}, {"favorites", i18n.T("library.section.favorites")},
		{"collection", i18n.T("library.section.collection")}, {"playlists", i18n.T("library.section.playlists")},
		{"history", i18n.T("library.section.history")}, {"idmarks", i18n.T("library.section.idmarks")},
		{"queue", i18n.T("library.section.queue")}, {"presets", i18n.T("library.section.presets")},
	}
	st.Body = u.libBodyState()
	return st
}

// libBodyState resolves the active section (locks state; sub-builders are lock-free). Peer
// targeting routes to the live mirror (library_mirror.go), remote cue edit to its own surface -
// both arrive as trusted pre-rendered markup.
func (u *UI) libBodyState() libBodySt {
	if u.rceActive() { // remote cue edit owns the surface (survives a link drop - edits are local)
		return libBodySt{Kind: libBodyRaw, Raw: u.rceBody()}
	}
	if tgt := u.libRemoteTarget(); tgt != "" {
		return libBodySt{Kind: libBodyRaw, Raw: u.libMirrorBody(tgt)}
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
		return libBodySt{Kind: libBodyFav, Fav: u.libFavoritesState(s)}
	case "collection":
		if !u.libEnsureTracks(s) {
			return libBodySt{Kind: libBodyMsg, Msg: i18n.T("library.remote.col.loading")}
		}
		st := libBodySt{Kind: libBodyColl, NavRail: u.libNavRailState(s, "collection"), Coll: u.libCollectionState(s)}
		if u.ceActiveFor("library") {
			// cue-edit mode: the waveform (grid + markers) spans the full tab width
			// above the list; the rail keeps only the editor controls.
			st.CEFull, st.CEWave = true, u.ceWaveHTML()
		}
		st.Detail = u.libDetailState(s)
		return st
	case "playlists":
		if !u.libEnsureTracks(s) {
			return libBodySt{Kind: libBodyMsg, Msg: i18n.T("library.remote.col.loading")}
		}
		return libBodySt{Kind: libBodyPls, Pls: u.libPlaylistsState(s), Detail: u.libDetailState(s)}
	case "history":
		if !u.libEnsureTracks(s) {
			return libBodySt{Kind: libBodyMsg, Msg: i18n.T("library.remote.col.loading")}
		}
		return libBodySt{Kind: libBodyHist, Hist: u.libHistoryState(s), Detail: u.libDetailState(s)}
	case "idmarks":
		return libBodySt{Kind: libBodyIDMarks, IDM: u.libIDMarksState()}
	case "queue":
		return libBodySt{Kind: libBodyQueue, Queue: u.libQueueState()}
	case "presets":
		return libBodySt{Kind: libBodyPresets, Presets: u.libPresetsState()}
	default:
		// Browse renders the dir listing regardless; the collection (metadata enrichment)
		// hydrates in the background and re-patches when ready.
		u.libEnsureTracks(s)
		return libBodySt{Kind: libBodyBrowse, NavRail: u.libNavRailState(s, "browse"),
			Browse: u.libBrowseState(s), Detail: u.libDetailState(s)}
	}
}

// ── Browse ──

// libSegSt is one breadcrumb segment.
type libSegSt struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// libFeSt is one browse entry row (list + grid share it; acts are prefix++Path in the renderer).
type libFeSt struct {
	Name    string       `json:"name"`
	Path    string       `json:"path"`
	IsDir   bool         `json:"isDir"`
	Glyph   string       `json:"glyph"`
	GridSub string       `json:"gridSub"`
	Sub     string       `json:"sub"`
	Key     libKeyPillSt `json:"key"`
	Checked bool         `json:"checked"`
	Sel     bool         `json:"sel"`
}

// libBrowseSt is the Browse pane (the triPane centre column).
type libBrowseSt struct {
	Msg string `json:"msg,omitempty"` // non-empty = the whole pane is one emptyState

	Crumbs      []libSegSt `json:"crumbs,omitempty"`
	Up          string     `json:"up"`
	UpPath      string     `json:"upPath"`
	Goto        string     `json:"gotoLbl"` // `goto` avoided as a Zig field name
	Filter      string     `json:"filter"`
	FilterPH    string     `json:"filterPh"`
	KindLbl     string     `json:"kindLbl"`
	Kind        selState   `json:"kind"`
	SortLbl     string     `json:"sortLbl"`
	Sort        selState   `json:"sort"`
	ListLbl     string     `json:"listLbl"`
	GridLbl     string     `json:"gridLbl"`
	Grid        bool       `json:"grid"`
	KeyChip     libChipSt  `json:"keyChip"`
	Folder      selState   `json:"folder"`
	SelAll      bool       `json:"selAll"`
	SelAllOn    bool       `json:"selAllOn"`
	SelAllTitle string     `json:"selAllTitle"`
	Count       string     `json:"count"`
	BoundNote   string     `json:"boundNote,omitempty"`
	HasBound    bool       `json:"hasBound,omitempty"`
	BoundActs   libPlActSt `json:"boundActs"`
	Entries     []libFeSt  `json:"entries,omitempty"`
	More        string     `json:"more,omitempty"`
	Batch       libBatchSt `json:"batch"`
}

// libBrowseState resolves the Browse pane. Caller holds s.mu.
func (u *UI) libBrowseState(s *libSt) libBrowseSt {
	dir := u.libDirOr()
	cachedFes, errRead, ok := u.libBrowseEntries(s, dir)
	if !ok {
		return libBrowseSt{Msg: i18n.T("library.remote.col.loading")}
	}
	if errRead {
		return libBrowseSt{Msg: i18n.T("library.browse.cannotRead", i18n.A{"path": dir})}
	}
	fs := libBrowseViewOf(s, cachedFes)

	st := libBrowseSt{
		Up: i18n.T("library.browse.up"), UpPath: filepath.Dir(dir),
		Goto:   i18n.T("library.browse.goto"),
		Filter: s.nameFilter, FilterPH: i18n.T("library.browse.filterName"),
		KindLbl: i18n.T("library.label.kind"), SortLbl: i18n.T("library.label.sort"),
		ListLbl: i18n.T("library.browse.list"), GridLbl: i18n.T("library.browse.grid"),
		Grid: s.view == "grid", KeyChip: u.libKeyChipState(s),
		SelAllTitle: i18n.T("library.selectAll"),
	}
	for _, seg := range splitSegs(dir) {
		st.Crumbs = append(st.Crumbs, libSegSt{Label: seg.label, Path: seg.path})
	}
	// kind + sort: one dropdown each (was 8 chips + 2 labels across two wrapped rows)
	st.Kind = resolveSmartSelect("libkind", "lib-kind:", s.kindFilter, func() []ssOpt {
		opts := make([]ssOpt, 0, 5)
		for _, k := range []string{"ALL", "VIDEO", "AUDIO", "IMAGE", "OTHER"} {
			opts = append(opts, ssOpt{Val: k, Label: i18n.T("library.kind." + strings.ToLower(k))})
		}
		return opts
	})
	st.Sort = resolveSmartSelect("libsort", "lib-sort:", s.sortBy, func() []ssOpt {
		opts := make([]ssOpt, 0, 3)
		for _, so := range []string{"Name", "Modified", "Size"} {
			opts = append(opts, ssOpt{Val: so, Label: i18n.T("library.sort." + strings.ToLower(so))})
		}
		return opts
	})
	// folder ops: one ⋯ menu (was four ghost buttons)
	pinLabel := i18n.T("library.browse.pin")
	for _, bm := range u.libMarks(s).List() {
		if bm.Path == dir {
			pinLabel = i18n.T("library.browse.unpin")
			break
		}
	}
	// dir rides in the act so a mirror controller can resolve THIS (remote) folder (#90)
	st.Folder = resolveActionMenu("libfoldermenu", "📁 "+i18n.T("library.browse.folderMenu"), []ssOpt{
		{Val: "ce-open-dir:" + dir, Label: i18n.T("library.ce.openDir")},
		{Val: "lib-reenc-dir", Label: i18n.T("library.re.dirBtn")},
		{Val: "lib-markpl", Label: i18n.T("library.re.markBtn")},
		{Val: "lib-folderimp", Label: i18n.T("library.fi.menuBtn"), Sub: i18n.T("library.fi.menuSub")},
		{Val: "lib-pin", Label: pinLabel},
	})
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
	st.SelAll = nf > 0
	st.SelAllOn = nf > 0 && allB
	st.Count = i18n.Tn("library.item", len(fs))
	// folder bound to a playlist -> its actions live right here
	if pl := u.libFolderPlaylist(s, dir); pl != nil {
		st.HasBound = true
		st.BoundNote = i18n.T("library.pl.folderBound", i18n.A{"name": pl.Name})
		st.BoundActs = u.libPlaylistActionsState(*pl, false)
	}

	ref := s.selRef()
	for _, e := range clampFE(len(fs)) {
		it := fs[e]
		row := libFeSt{Name: it.name, Path: it.path, IsDir: it.isDir, Glyph: libGlyph(it.kind, it.isDir)}
		if it.isDir {
			row.GridSub = i18n.T("library.browse.folder")
		} else {
			row.GridSub = humanBytes(uint64(it.size)) + " · " + strings.ToUpper(it.kind)
			row.Sub = humanBytes(uint64(it.size)) + " · " + it.mod.Format("2006-01-02")
			if t, ok := s.byPath[it.path]; ok {
				row.Key = libKeyPillState(t.Key, ref)
			}
			row.Checked = s.batch[it.path]
			row.Sel = s.sel != nil && s.sel.path == it.path
		}
		st.Entries = append(st.Entries, row)
	}
	if len(fs) > libMaxRows {
		st.More = i18n.T("library.showingFirst", i18n.A{"shown": fmt.Sprint(libMaxRows), "total": fmt.Sprint(len(fs))})
	}
	st.Batch = u.libBatchState(s)
	return st
}

// libBatchState resolves the browse multi-select bar. Caller holds s.mu.
func (u *UI) libBatchState(s *libSt) libBatchSt {
	if len(s.batch) == 0 {
		return libBatchSt{}
	}
	st := libBatchSt{On: true, Count: i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.batch))})}
	st.Btns = append(st.Btns,
		newBtn(i18n.T("library.batch.waveforms"), "outline", "lib-batch-run:peaks"),
		newBtn(i18n.T("library.batch.tags"), "outline", "lib-batch-run:tags"),
		newBtn(i18n.T("library.batch.fingerprint"), "outline", "lib-batch-run:fingerprint"),
		newBtn(i18n.T("library.batch.transcode"), "primary", "lib-batch-run:transcode"))
	if len(s.batch) >= 2 {
		st.Btns = append(st.Btns, newBtn(i18n.T("library.compat.markBtn"), "outline", "lib-compat-mark:browse"))
	}
	st.Btns = append(st.Btns, newBtn(i18n.T("library.clear"), "ghost", "lib-batch-clear"))
	return st
}

// ── Favorites ──

// libFavRowSt is one pinned folder.
type libFavRowSt struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type libFavSt struct {
	Desc     string        `json:"desc"`
	Empty    string        `json:"empty"`
	OpenLbl  string        `json:"openLbl"`
	UnpinLbl string        `json:"unpinLbl"`
	Rows     []libFavRowSt `json:"rows,omitempty"`
}

// libFavoritesState resolves the pinned-folder list. Caller holds s.mu.
func (u *UI) libFavoritesState(s *libSt) libFavSt {
	st := libFavSt{
		Desc: i18n.T("library.favorites.desc"), Empty: i18n.T("library.favorites.empty"),
		OpenLbl: i18n.T("library.open"), UnpinLbl: i18n.T("library.favorites.unpin"),
	}
	for _, m := range u.libMarks(s).List() {
		st.Rows = append(st.Rows, libFavRowSt{Label: m.Label, Path: m.Path})
	}
	return st
}

// ── Collection ──

// libCueCellSt is one row's drops/cues census (also the #ce-cell-<hash> patch target).
type libCueCellSt struct {
	Drops        int    `json:"drops"`
	DropsTitle   string `json:"dropsTitle"`
	NoDropsTitle string `json:"noDropsTitle"`
	Cues         int    `json:"cues"`
	CuesTitle    string `json:"cuesTitle"`
	NoCuesTitle  string `json:"noCuesTitle"`
}

// libCueCellState resolves a row's cue census. Caller holds s.mu.
func libCueCellState(s *libSt, t musiclib.Track) libCueCellSt {
	nd, nc := len(s.dropsIdx[t.Path]), ceCueCount(t.Cues)
	return libCueCellSt{
		Drops: nd, DropsTitle: i18n.Tn("library.ce.drops", nd), NoDropsTitle: i18n.T("library.ce.noDropsBadge"),
		Cues: nc, CuesTitle: i18n.Tn("library.ce.patternCues", nc), NoCuesTitle: i18n.T("library.ce.noCuesBadge"),
	}
}

// libCollHdrSt is one sortable column header.
type libCollHdrSt struct {
	Cls   string `json:"cls"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Arrow string `json:"arrow,omitempty"`
}

// libCollHeadSt is the collection table header row.
type libCollHeadSt struct {
	SelAllTitle string       `json:"selAllTitle"`
	SelAllOn    bool         `json:"selAllOn"`
	Main        libCollHdrSt `json:"main"`
	CueLbl      string       `json:"cueLbl"`
	BPM         libCollHdrSt `json:"bpm"`
	TimeLbl     string       `json:"timeLbl"`
	Key         libCollHdrSt `json:"key"`
}

// libCollRowSt is one collection track row.
type libCollRowSt struct {
	Path     string       `json:"path"`
	Checked  bool         `json:"checked"`
	Warn     bool         `json:"warn"` // not on disk
	SelCls   string       `json:"selCls,omitempty"`
	Title    string       `json:"title"`
	Sub      string       `json:"sub"`
	Verified bool         `json:"verified"`
	CellID   string       `json:"cellId"`
	Cue      libCueCellSt `json:"cue"`
	BPM      string       `json:"bpm"`
	Dur      string       `json:"dur"`
	Key      libKeyPillSt `json:"key"`
}

// libCollSt is the Collection pane.
type libCollSt struct {
	Msg string `json:"msg,omitempty"` // library DB unavailable

	ImportLbl  string     `json:"importLbl"`
	DJSyncLbl  string     `json:"djsyncLbl"`
	GridFix    bool       `json:"gridFix"`
	GridFixLbl string     `json:"gridFixLbl"`
	MoreLbl    string     `json:"moreLbl"`
	MoreOpen   bool       `json:"moreOpen"`
	MoreItems  []libTabSt `json:"moreItems,omitempty"`
	Search     string     `json:"search"`
	SearchPH   string     `json:"searchPh"`
	Genre      selState   `json:"genre"`
	Label      selState   `json:"label"`
	HasPlFacet bool       `json:"hasPlFacet"`
	PlFacet    selState   `json:"plFacet"`
	KeyChip    libChipSt  `json:"keyChip"`
	NoDropsLbl string     `json:"noDropsLbl"`
	NoDrops    bool       `json:"noDrops"`
	Clear      bool       `json:"clear"`
	ClearLbl   string     `json:"clearLbl"`
	Prep       selState   `json:"prep"` // prep-playlist picker (library_prep.go)

	Chips     []libChipSt `json:"chips,omitempty"`
	HasInline bool        `json:"hasInline"`
	Inline    libPlActSt  `json:"inlineActs"` // `inline` avoided as a Zig field name

	HasResults bool        `json:"hasResults"` // a fixer's results view replaces the list
	Results    libFixResSt `json:"results"`    // library_tagfix.go / library_gridfix.go

	Head          libCollHeadSt  `json:"head"`
	Rows          []libCollRowSt `json:"rows,omitempty"`
	VerifiedTitle string         `json:"verifiedTitle"`
	Empty         string         `json:"empty"`
	IsEmpty       bool           `json:"isEmpty"`
	More          string         `json:"more,omitempty"`
	Batch         libBatchSt     `json:"batch"`
}

// libCollectionState resolves the Collection pane. Caller holds s.mu.
func (u *UI) libCollectionState(s *libSt) libCollSt {
	if u.svc.Lib == nil {
		return libCollSt{Msg: i18n.T("library.dbUnavailable")}
	}
	st := libCollSt{
		ImportLbl: i18n.T("library.coll.import"), DJSyncLbl: i18n.T("library.coll.djsync"),
		GridFix: u.svc.Cfg.Features.GridFix.Enabled, GridFixLbl: i18n.T("library.gf.start"),
		MoreLbl: i18n.T("library.coll.more"), MoreOpen: s.moreOpen, MoreItems: u.libMoreMenuState(s),
		Search: s.collSearch, SearchPH: i18n.T("library.coll.search"),
		KeyChip: u.libKeyChipState(s), NoDropsLbl: i18n.T("library.ce.noDropsChip"), NoDrops: s.collNoDrops,
		ClearLbl: i18n.T("library.clear"), Prep: u.prepSelectState("prep-coll"),
		VerifiedTitle: i18n.T("library.gf.verifiedBadge"),
	}
	st.Genre = u.libFacetSelectState(s, "genre", i18n.T("library.label.genre"), s.collGenre,
		func(t musiclib.Track) string { return musiclib.GenreFamily(t.Genre) })
	st.Label = u.libFacetSelectState(s, "label", i18n.T("library.label.label"), s.collLabel,
		func(t musiclib.Track) string { return strings.TrimSpace(t.Label) })
	st.PlFacet, st.HasPlFacet = u.libPlaylistFacetState(s)
	st.Clear = len(s.collGenre)+len(s.collLabel)+len(s.keySel)+len(s.collPl) > 0 || s.collSearch != "" || s.collNoDrops
	// active facets render as removable chips (one toolbar = one less stacked row above the list)
	for g := range s.collGenre {
		st.Chips = append(st.Chips, newChip(g+" ×", "", "lib-genre:"+g, true))
	}
	for l := range s.collLabel {
		st.Chips = append(st.Chips, newChip(l+" ×", "", "lib-label:"+l, true))
	}
	for _, id := range sortedPlIDs(s.collPlNames) {
		st.Chips = append(st.Chips, newChip(s.collPlNames[id]+" ×", "", fmt.Sprintf("lib-plfilter:%d", id), true))
	}
	// exactly one playlist facet active -> the collection IS that playlist's view:
	// surface its full action row inline (no Playlists-tab round-trip)
	if len(s.collPl) == 1 && u.svc.Lib != nil {
		var id int64
		for k := range s.collPl {
			id = k
		}
		if rows := u.libPlaylists(s); rows != nil {
			for _, p := range rows {
				if p.ID == id {
					st.HasInline, st.Inline = true, u.libPlaylistActionsState(p, true)
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
		st.HasResults, st.Results = true, libFixResSt{Kind: libFixResTF, TF: u.tfResultsState()}
		return st
	}
	u.gf.mu.Lock()
	resView := u.gf.resView && u.gf.stage == "done"
	u.gf.mu.Unlock()
	if resView {
		st.HasResults, st.Results = true, libFixResSt{Kind: libFixResGF, GF: u.gfResultsState(&u.gf)}
		return st
	}

	// filtered + sorted view (memoized) + on-disk existence swept off-thread (was os.Stat/row)
	shown := u.libCollView(s)
	total := len(shown)
	shownPaths := make([]string, 0, libMaxRows)
	for i, ti := range shown {
		if i >= libMaxRows {
			break
		}
		shownPaths = append(shownPaths, s.tracks[ti].Path)
	}
	u.libEnsureOnDisk(s, shownPaths)
	hdr := func(key, label string, cls string) libCollHdrSt {
		arrow := ""
		if s.collSort == key {
			arrow = " ▲"
			if s.collDesc {
				arrow = " ▼"
			}
		}
		return libCollHdrSt{Cls: cls, Key: key, Label: label, Arrow: arrow}
	}
	allSel := total > 0
	for _, ti := range shown {
		if !s.collSel[s.tracks[ti].Path] {
			allSel = false
			break
		}
	}
	st.Head = libCollHeadSt{
		SelAllTitle: i18n.T("library.selectAll"), SelAllOn: allSel,
		Main:    hdr("Artist", i18n.T("library.coll.trackHeader", i18n.A{"count": fmt.Sprint(total)}), "trk-hmain"),
		CueLbl:  i18n.T("library.col.cues"),
		BPM:     hdr("BPM", i18n.T("library.col.bpm"), "trk-bpm"),
		TimeLbl: i18n.T("library.col.time"),
		Key:     hdr("Key", i18n.T("library.col.key"), "trk-keyh"),
	}
	ref := s.selRef()
	vs := u.gfVerified()
	ceOn := u.ceActiveFor("library")
	for i, ti := range shown {
		if i >= libMaxRows {
			break
		}
		t := s.tracks[ti]
		row := libCollRowSt{
			Path: t.Path, Checked: s.collSel[t.Path], Warn: !s.onDiskOr(t.Path),
			Title: trackTitle(t), Sub: trackMetaSub(t), Verified: vs != nil && vs.Has(t.Path),
			CellID: ceCellID(t.Path), Cue: libCueCellState(s, t), Key: libKeyPillState(t.Key, ref),
		}
		if s.sel != nil && s.sel.path == t.Path {
			row.SelCls = " sel"
		}
		if ceOn && s.collSel[t.Path] {
			row.SelCls += " ce-marked" // in the mass-apply set (batch bar below)
		}
		if t.BPM > 0 {
			row.BPM = fmt.Sprintf("%.0f", t.BPM)
		}
		if t.DurationSec > 0 {
			row.Dur = mmss(t.DurationSec)
		}
		st.Rows = append(st.Rows, row)
	}
	st.IsEmpty = total == 0
	st.Empty = i18n.T("library.coll.empty")
	if total > libMaxRows {
		st.More = i18n.T("library.showingFirst", i18n.A{"shown": fmt.Sprint(libMaxRows), "total": fmt.Sprint(total)})
	}
	// selection bar: playlist add + verified-grid marking; in cue-edit mode the checked
	// rows are the mass-apply set for the assigned patterns
	if len(s.collSel) > 0 {
		st.Batch.On = true
		st.Batch.Count = i18n.T("library.selectedCount", i18n.A{"count": fmt.Sprint(len(s.collSel))})
		addVar := "primary"
		if ceOn {
			st.Batch.Btns = append(st.Batch.Btns,
				newBtn(i18n.T("library.ce.applySelHot"), "primary", "ce-apply-sel:hot"),
				newBtn(i18n.T("library.ce.applySelMem"), "outline", "ce-apply-sel:mem"))
			addVar = "outline"
		}
		st.Batch.Btns = append(st.Batch.Btns, newBtn(i18n.T("library.addToPlaylist"), addVar, "lib-addto"))
		if len(s.collSel) >= 2 {
			st.Batch.Btns = append(st.Batch.Btns, newBtn(i18n.T("library.compat.markBtn"), "outline", "lib-compat-mark:coll"))
		}
		st.Batch.Btns = append(st.Batch.Btns,
			newBtn(i18n.T("library.gf.markVerified"), "outline", "gf-verify-sel"),
			newBtn(i18n.T("library.clear"), "ghost", "lib-collsel-clear"))
	}
	return st
}

// libMoreMenuState resolves the Maintenance popover items (occasional collection ops).
func (u *UI) libMoreMenuState(s *libSt) []libTabSt {
	if !s.moreOpen {
		return nil
	}
	items := []libTabSt{
		{"lib-pl-new", i18n.T("library.pl.new")},
		{"lib-pl-newsmart", i18n.T("library.pl.newSmart")},
		{"lib-pl-refresh-all", i18n.T("library.pl.refreshAll")},
		{"lib-backups", i18n.T("library.coll.backup")},
		{"lib-scan", i18n.T("library.coll.scan")},
		{"lib-cleanup", i18n.T("library.coll.cleanup")},
		{"lib-relocate", i18n.T("library.coll.relocate")},
		{"lib-export", i18n.T("library.coll.export")},
		{"lib-tagfix", i18n.T("library.tf.menu")},
		{"lib-bpmranges", i18n.T("library.bpr.menu")},
	}
	if u.svc.Syncer != nil {
		items = append(items, libTabSt{"lib-sync", i18n.T("library.coll.sync")})
	}
	return items
}

// libFacetSelectState resolves a filterable multi-facet dropdown whose label summarizes the
// active picks. Caller holds s.mu.
func (u *UI) libFacetSelectState(s *libSt, kind, label string, active map[string]bool, keyOf func(musiclib.Track) string) selState {
	lbl := label
	if n := len(active); n > 0 {
		lbl = fmt.Sprintf("%s (%d)", label, n)
	}
	facet := u.libFacetOpts(s, kind, keyOf) // memoized: was 2 full 23k scans per render
	return resolveSmartSelect("libfacet-"+kind, "lib-"+kind+":", lbl, func() []ssOpt {
		opts := make([]ssOpt, 0, len(facet))
		for _, gc := range facet {
			o := ssOpt{Val: gc[0], Label: gc[0], Badge: gc[1]}
			if active[gc[0]] {
				o.Label = "✓ " + o.Label
			}
			opts = append(opts, o)
		}
		return opts
	})
}

// libPlaylistFacetState resolves the playlist membership facet (ok=false = nothing to show).
// Caller holds s.mu.
func (u *UI) libPlaylistFacetState(s *libSt) (selState, bool) {
	if u.svc.Lib == nil {
		return emptySel(), false
	}
	rows := u.libPlaylists(s)
	if len(rows) == 0 {
		return emptySel(), false
	}
	counts := u.libSmartCounts(s, rows) // smart counts precomputed off-thread (was a full scan per list per render)
	lbl := i18n.T("library.label.playlist")
	if n := len(s.collPl); n > 0 {
		lbl = fmt.Sprintf("%s (%d)", lbl, n)
	}
	active := s.collPl
	return resolveSmartSelect("libfacet-pl", "lib-plfilter:", lbl, func() []ssOpt {
		opts := make([]ssOpt, 0, len(rows))
		for _, p := range rows {
			badge := fmt.Sprint(p.TrackCount)
			if p.Kind == libdb.PlaylistSmart {
				if c, ok := counts[p.ID]; ok {
					badge = fmt.Sprint(c)
				} else {
					badge = "…" // count not computed yet
				}
			}
			o := ssOpt{Val: fmt.Sprint(p.ID), Label: p.Name, Badge: badge}
			if active[p.ID] {
				o.Label = "✓ " + o.Label
			}
			opts = append(opts, o)
		}
		return opts
	}), true
}

// libKeyChipState resolves the harmonic-filter chip. Caller holds s.mu.
func (u *UI) libKeyChipState(s *libSt) libChipSt {
	n := len(s.keySel)
	label := i18n.T("library.meta.key")
	if n > 0 {
		label = i18n.T("library.keyChipN", i18n.A{"count": fmt.Sprint(n)})
	}
	// tapping opens the collection harmonic filter via the inspector wheel; here it clears.
	return newChip(label, "", "lib-key-clear", n > 0)
}

// ── Playlists ──

// libPlRowSt is one playlist list row.
type libPlRowSt struct {
	ID   string `json:"id"`
	Icon string `json:"icon"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
	Sel  bool   `json:"sel"`
}

// libPlItemSt is one row of an opened playlist.
type libPlItemSt struct {
	Pos    string       `json:"pos"`
	Idx    string       `json:"idx"`
	Path   string       `json:"path"`
	Title  string       `json:"title"`
	Key    libKeyPillSt `json:"key"`
	Manual bool         `json:"manual"`
}

// libPlOpenSt is the opened-playlist block below the list.
type libPlOpenSt struct {
	Title     string        `json:"title"`
	SmartNote string        `json:"smartNote,omitempty"`
	Acts      libPlActSt    `json:"acts"`
	Items     []libPlItemSt `json:"items,omitempty"`
	Empty     string        `json:"empty"`
}

type libPlsSt struct {
	Msg string `json:"msg,omitempty"`

	NewLbl      string       `json:"newLbl"`
	NewSmartLbl string       `json:"newSmartLbl"`
	HasCloud    bool         `json:"hasCloud"`
	Cloud       selState     `json:"cloud"`
	Rows        []libPlRowSt `json:"rows,omitempty"`
	Empty       string       `json:"empty"`
	HasOpen     bool         `json:"hasOpen"`
	Open        libPlOpenSt  `json:"open"`
}

// libPlaylistsState resolves the Playlists section. Caller holds s.mu.
func (u *UI) libPlaylistsState(s *libSt) libPlsSt {
	if u.svc.Lib == nil {
		return libPlsSt{Msg: i18n.T("library.dbUnavailable")}
	}
	rows := u.libPlaylists(s)
	counts := u.libSmartCounts(s, rows)
	st := libPlsSt{
		NewLbl: i18n.T("library.pl.new"), NewSmartLbl: i18n.T("library.pl.newSmart"),
		Empty: i18n.T("library.pl.empty"),
	}
	if u.svc.Syncer != nil {
		// cloud ops are occasional - one ⋯ menu instead of three toolbar buttons
		st.HasCloud = true
		st.Cloud = resolveActionMenu("plcloudmenu", "☁ "+i18n.T("library.pl.menu.cloud"), []ssOpt{
			{Val: "lib-pl-cloud", Label: i18n.T("library.pl.cloudStatus")},
			{Val: "lib-pl-syncall", Label: i18n.T("library.pl.syncAll")},
			{Val: "lib-pl-remote", Label: i18n.T("library.pl.remote")},
		})
	}
	for _, p := range rows {
		row := libPlRowSt{ID: fmt.Sprint(p.ID), Icon: "🎵", Sel: p.ID == s.plSel, Name: p.Name,
			Sub: fmt.Sprint(p.Kind) + " · " + i18n.Tn("track", p.TrackCount)}
		switch p.Kind {
		case libdb.PlaylistSmart:
			row.Icon = "⚡"
			if r, ok := libParseRules(p.Rules); ok {
				cnt := "…"
				if c, ok2 := counts[p.ID]; ok2 {
					cnt = fmt.Sprint(c)
				}
				row.Sub = i18n.T("library.pl.smartSub", i18n.A{"count": cnt, "desc": r.Describe()})
			}
		case libdb.PlaylistImported:
			row.Icon = "⤓"
		}
		st.Rows = append(st.Rows, row)
	}
	if s.plSel != 0 {
		st.HasOpen, st.Open = true, u.libPlaylistOpenState(s)
	}
	return st
}

// libPlaylistOpenState resolves the opened playlist's tracks. Caller holds s.mu.
func (u *UI) libPlaylistOpenState(s *libSt) libPlOpenSt {
	p := s.plCur
	manual := p.Kind == libdb.PlaylistManual
	st := libPlOpenSt{Title: p.Name, Acts: u.libPlaylistActionsState(p, false), Empty: i18n.T("library.pl.emptyTracks")}
	if p.Kind == libdb.PlaylistSmart {
		if r, ok := libParseRules(p.Rules); ok {
			st.SmartNote = i18n.T("library.pl.smartMatchLive", i18n.A{"desc": r.Describe(), "count": fmt.Sprint(len(s.plItems))})
		}
	}
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
		row := libPlItemSt{Pos: fmt.Sprintf("%d", i+1), Idx: strconv.Itoa(i), Path: it.Path, Title: title, Manual: manual}
		if t, ok := s.byPath[it.Path]; ok {
			row.Key = libKeyPillState(t.Key, ref)
		}
		st.Items = append(st.Items, row)
	}
	return st
}

// libPlaylistActionsState resolves one playlist's action row: everyday actions stay buttons,
// occasional ones demote into the ⋯ actionMenu (full labels + Sub hints keep them discoverable).
// inColl hides the "View in Collection" jump.
func (u *UI) libPlaylistActionsState(p libdb.PlaylistRow, inColl bool) libPlActSt {
	manual := p.Kind == libdb.PlaylistManual
	var st libPlActSt
	if !inColl {
		st.Btns = append(st.Btns, newBtn(i18n.T("library.pl.viewInColl"), "primary", fmt.Sprintf("lib-plgoto:%d", p.ID)))
	}
	st.Btns = append(st.Btns, newBtn(i18n.T("library.ce.openPl"), "outline", fmt.Sprintf("ce-open-pl:%d", p.ID)))
	if p.Kind == libdb.PlaylistSmart {
		st.Btns = append(st.Btns, newBtn(i18n.T("library.pl.editRules"), "outline", fmt.Sprintf("lib-sr-edit:%d", p.ID)))
	}
	st.Btns = append(st.Btns, newBtn(i18n.T("library.pl.exportM3U"), "outline", fmt.Sprintf("lib-pl-export:%d", p.ID)))

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
	if libPlCanSendTraktor(p) {
		add(i18n.T("library.pl.sendTraktor"), fmt.Sprintf("lib-pl-traktor:%d", p.ID), i18n.T("library.pl.menu.sendTraktorSub"))
	}
	add(i18n.T("library.plsort.btn"), fmt.Sprintf("lib-plsort:%d", p.ID), "")
	if p.Kind != libdb.PlaylistSmart && u.svc.Lib != nil {
		lbl := i18n.T("library.bpr.plMenu")
		if r, ok := u.svc.Lib.PlaylistBPMRange(p.ID); ok {
			lbl += fmt.Sprintf(" (%g–%g)", r.Min, r.Max)
		}
		add(lbl, fmt.Sprintf("lib-pl-bpmrange:%d", p.ID), i18n.T("library.bpr.plMenuSub"))
	}
	if u.svc.Syncer != nil {
		add(i18n.T("library.pl.push"), fmt.Sprintf("lib-pl-push:%d", p.ID), "")
		add(i18n.T("library.pl.pull"), fmt.Sprintf("lib-pl-pull:%d", p.ID), "")
		add(i18n.T("library.pl.unlink"), fmt.Sprintf("lib-pl-unlink:%d", p.ID), "")
	}
	add(i18n.T("common.delete"), fmt.Sprintf("lib-pl-del:%d", p.ID), "")
	st.Menu = resolveActionMenu(fmt.Sprintf("plmenu-%d", p.ID), "⋯ "+i18n.T("player.more"), items)
	return st
}

// ── History ──

// libSessSt is one history session row.
type libSessSt struct {
	Idx  string `json:"idx"`
	Date string `json:"date"`
	Sub  string `json:"sub"`
	Sel  bool   `json:"sel"`
}

// libPlayedSt is one played-track row.
type libPlayedSt struct {
	Path  string       `json:"path"`
	Warn  bool         `json:"warn"`
	Title string       `json:"title"`
	Meta  string       `json:"meta"`
	Key   libKeyPillSt `json:"key"`
}

type libHistSt struct {
	LoadLbl   string        `json:"loadLbl"`
	Src       selState      `json:"src"`
	Desc      string        `json:"desc"`
	Empty     string        `json:"empty"`
	IsEmpty   bool          `json:"isEmpty"`
	Sessions  []libSessSt   `json:"sessions,omitempty"`
	HasPlayed bool          `json:"hasPlayed"`
	PlayedLbl string        `json:"playedLbl"`
	SortLbl   string        `json:"sortLbl"`
	Sort      selState      `json:"sort"`
	DirLbl    string        `json:"dirLbl"`
	Played    []libPlayedSt `json:"played,omitempty"`
}

// libHistoryState resolves the History section. Caller holds s.mu.
func (u *UI) libHistoryState(s *libSt) libHistSt {
	// source picker: every DJ software with a play-history model (Traktor NML history
	// dir, Rekordbox master.db djmdHistory). VirtualDJ keeps no session history.
	st := libHistSt{
		LoadLbl: i18n.T("library.hist.load"),
		Src:     resolveSmartSelect("lib-hist-src", "lib-hist-srcpick", s.histSrc, libHistSources),
		Desc:    i18n.T("library.hist.desc"), Empty: i18n.T("library.hist.empty"),
		IsEmpty: len(s.summaries) == 0,
	}
	for i, sm := range s.summaries {
		sub := i18n.Tn("track", sm.TrackCount) + " · " + fmtDurCoarse(sm.TotalDurationSec)
		if i < len(s.histApps) && s.histApps[i] != "" {
			sub = s.histApps[i] + " · " + sub
		}
		st.Sessions = append(st.Sessions, libSessSt{
			Idx: strconv.Itoa(i), Date: sm.StartedAt.Format("2006-01-02 15:04"), Sub: sub, Sel: i == s.selSess,
		})
	}
	if len(s.played) == 0 {
		return st
	}
	st.HasPlayed = true
	// sort: one dropdown + direction chip (was a 9-chip wall)
	cur := s.playSort
	if cur == "" {
		cur = "Play order"
	}
	st.PlayedLbl, st.SortLbl, st.DirLbl = i18n.T("library.label.played"), i18n.T("library.label.sort"), sortDir(s.playDesc)
	st.Sort = resolveSmartSelect("libplaysort", "lib-play-sort:", cur, func() []ssOpt {
		opts := make([]ssOpt, 0, 8)
		for _, so := range []string{"Play order", "Artist", "Title", "BPM", "Key", "Genre", "Rating", "Plays"} {
			opts = append(opts, ssOpt{Val: so, Label: i18n.T("library.playsort." + strings.ToLower(strings.ReplaceAll(so, " ", "")))})
		}
		return opts
	})
	ref := s.selRef()
	for _, pi := range s.playView() {
		p := s.played[pi]
		st.Played = append(st.Played, libPlayedSt{
			Path: p.track.Path, Warn: !p.onDisk,
			Title: strOrDash(p.track.Artist) + " - " + strOrDash(p.track.Title),
			Meta:  trackMeta(p.track), Key: libKeyPillState(p.track.Key, ref),
		})
	}
	return st
}

// ── ID Marks ──

// libIDMRowSt is one marked path.
type libIDMRowSt struct {
	Path      string `json:"path"`
	Artist    bool   `json:"artist"`
	ArtistAct string `json:"artistAct"`
	Label     bool   `json:"label"`
	LabelAct  string `json:"labelAct"`
	DelAct    string `json:"delAct"`
}

type libIDMSt struct {
	Msg string `json:"msg,omitempty"`

	MarkFileLbl   string        `json:"markFileLbl"`
	MarkFolderLbl string        `json:"markFolderLbl"`
	TypePathLbl   string        `json:"typePathLbl"`
	Desc          string        `json:"desc"`
	Empty         string        `json:"empty"`
	ArtistLbl     string        `json:"artistLbl"`
	ArtistDL      string        `json:"artistDl"`
	LabelLbl      string        `json:"labelLbl"`
	LabelDL       string        `json:"labelDl"`
	RemoveLbl     string        `json:"removeLbl"`
	Rows          []libIDMRowSt `json:"rows,omitempty"`
}

// libIDMarksState resolves the ID Marks section.
func (u *UI) libIDMarksState() libIDMSt {
	st := u.svc.IDMarks
	if st == nil {
		return libIDMSt{Msg: i18n.T("library.idmarks.unavailable")}
	}
	artist, label := i18n.T("library.idmarks.showArtist"), i18n.T("library.idmarks.showLabel")
	out := libIDMSt{
		MarkFileLbl: i18n.T("library.idmarks.markFile"), MarkFolderLbl: i18n.T("library.idmarks.markFolder"),
		TypePathLbl: i18n.T("library.idmarks.typePath"), Desc: i18n.T("library.idmarks.desc"),
		Empty: i18n.T("library.idmarks.empty"), RemoveLbl: i18n.T("library.remove"),
		ArtistLbl: artist, ArtistDL: strings.ToLower(artist),
		LabelLbl: label, LabelDL: strings.ToLower(label),
	}
	for _, e := range st.List() {
		out.Rows = append(out.Rows, libIDMRowSt{
			Path: e.Path, Artist: e.ShowArtist, ArtistAct: "lib-id-artist:" + e.Path,
			Label: e.ShowLabel, LabelAct: "lib-id-label:" + e.Path, DelAct: "lib-id-del:" + e.Path,
		})
	}
	return out
}

// ── Queue ──

// libJobSt is one transcode-queue job card.
type libJobSt struct {
	Label     string `json:"label"`
	Cancel    bool   `json:"cancel"`
	CancelLbl string `json:"cancelLbl"`
	CancelAct string `json:"cancelAct"`
	Status    string `json:"status"`
	StatusVar string `json:"statusVar"`
	Width     string `json:"width"` // progressBar fill width, pre-formatted "%.1f%%"
	Caption   string `json:"caption"`
	Msg       string `json:"msg"`
}

type libQueueSt struct {
	Desc  string     `json:"desc"`
	Empty string     `json:"empty"`
	Jobs  []libJobSt `json:"jobs,omitempty"`
}

// libQueueState resolves the transcode queue (#lib-queue-body patch target).
func (u *UI) libQueueState() libQueueSt {
	s := u.lib()
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	st := libQueueSt{Desc: i18n.T("library.queue.desc"), Empty: i18n.T("library.queue.empty")}
	for i, j := range s.jobs {
		job := libJobSt{
			Label: j.name + " · " + j.preset, Status: j.status, StatusVar: jobBadge(j.status),
			Width: progressPct(j.pct / 100), Caption: fmt.Sprintf("%s · %.0f%%", j.status, j.pct), Msg: j.msg,
		}
		if j.status == "running" || j.status == "queued" {
			job.Cancel, job.CancelLbl, job.CancelAct = true, i18n.T("common.cancel"), fmt.Sprintf("lib-job-cancel:%d", i)
		}
		st.Jobs = append(st.Jobs, job)
	}
	return st
}

// ── Presets catalog ──

// libPresetSt is one preset card.
type libPresetSt struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

type libPresetsSt struct {
	NewLbl        string        `json:"newLbl"`
	YoursTitle    string        `json:"yoursTitle"`
	EmptyCustom   string        `json:"emptyCustom"`
	BuiltinsTitle string        `json:"builtinsTitle"`
	CustomBadge   string        `json:"customBadge"`
	BuiltinBadge  string        `json:"builtinBadge"`
	EditLbl       string        `json:"editLbl"`
	DupLbl        string        `json:"dupLbl"`
	DelLbl        string        `json:"delLbl"`
	DupEditLbl    string        `json:"dupEditLbl"`
	Custom        []libPresetSt `json:"custom,omitempty"`
	Builtins      []libPresetSt `json:"builtins,omitempty"`
}

// libPresetsState resolves the encoding-preset catalog.
func (u *UI) libPresetsState() libPresetsSt {
	st := libPresetsSt{
		NewLbl: i18n.T("library.preset.new"), YoursTitle: i18n.T("library.preset.yours"),
		EmptyCustom: i18n.T("library.preset.emptyCustom"), BuiltinsTitle: i18n.T("library.preset.builtins"),
		CustomBadge: i18n.T("library.preset.custom"), BuiltinBadge: i18n.T("library.preset.builtin"),
		EditLbl: i18n.T("library.edit"), DupLbl: i18n.T("library.duplicate"),
		DelLbl: i18n.T("common.delete"), DupEditLbl: i18n.T("library.preset.dupEdit"),
	}
	if u.svc.Cfg != nil {
		for _, p := range u.svc.Cfg.Features.Transcode.Presets {
			st.Custom = append(st.Custom, libPresetSt{ID: p.ID, Label: p.Label, Desc: p.Desc})
		}
	}
	for _, p := range transcode.Builtins {
		st.Builtins = append(st.Builtins, libPresetSt{ID: p.ID, Label: p.Label, Desc: p.Desc})
	}
	return st
}

// ── Inspector (detail pane) ──

// libDetail kinds.
const (
	libDetailRaw = "raw" // cue-edit rail owns the pane
	libDetailGF  = "gf"  // beatgrid-fixer rail owns the pane (library_gridfix.go)
	libDetailMsg = "msg" // nothing selected
	libDetailSel = "sel"
)

// libTrackPlsSt is the selected track's playlist-membership chips.
type libTrackPlsSt struct {
	Unavailable bool        `json:"unavailable"`
	Chips       []libChipSt `json:"chips,omitempty"`
	EmptyText   string      `json:"emptyText"`
	AddLbl      string      `json:"addLbl"`
	AddAct      string      `json:"addAct"`
}

// libHarmSt is the Camelot key-wheel block. Wheel is the Go-built SVG (all float math +
// %.2f formatting stays Go-side) and rides as trusted markup.
type libHarmSt struct {
	Desc     string `json:"desc"`
	Wheel    string `json:"wheel"`
	SameLbl  string `json:"sameLbl"`
	RelLbl   string `json:"relLbl"`
	ShowLbl  string `json:"showLbl"`
	ShowAct  string `json:"showAct"`
	ClearLbl string `json:"clearLbl"`
}

// libEncVideoSt is the video half of the encode builder (omitted for audio-only sources).
type libEncVideoSt struct {
	VCodec      libSelTip    `json:"vcodec"`
	Accel       selState     `json:"accel"`
	QualityLbl  string       `json:"qualityLbl"`
	Profiles    []libChipSt  `json:"profiles,omitempty"`
	ProfileHint string       `json:"profileHint"`
	RateMode    libSelTip    `json:"rateMode"`
	RateField   libPBFieldSt `json:"rateField"`
	Res         selState     `json:"res"`
	FPS         libPBFieldSt `json:"fps"`
}

// libEncSt is the encoding-preset builder + source-aware media hints.
type libEncSt struct {
	Preset       selState      `json:"preset"`
	Desc         string        `json:"desc"`
	Hints        []libHintSt   `json:"hints,omitempty"`
	AudioOnly    bool          `json:"audioOnly"`
	Container    libSelTip     `json:"container"`
	Video        libEncVideoSt `json:"video"`
	AudioCodec   libSelTip     `json:"audioCodec"`
	AudioBitrate libPBFieldSt  `json:"audioBitrate"`
	Channels     selState      `json:"channels"`
	SampleRate   selState      `json:"sampleRate"`
	Loud         loudSt        `json:"loud"` // the shared loudness block (components.go), structured
	TrimStart    libPBFieldSt  `json:"trimStart"`
	TrimEnd      libPBFieldSt  `json:"trimEnd"`
	OutputNote   string        `json:"outputNote"`
	StartLbl     string        `json:"startLbl"`
	SaveLbl      string        `json:"saveLbl"`
	SaveAsLbl    string        `json:"saveAsLbl"`
}

// libDetailSt is the #lib-detail inner state.
type libDetailSt struct {
	Kind string  `json:"kind"`
	Raw  string  `json:"raw,omitempty"`
	Msg  string  `json:"msg,omitempty"`
	GF   libGFSt `json:"gf"` // beatgrid-fixer rail (Kind == libDetailGF)

	Eyebrow string `json:"eyebrow"`
	Title   string `json:"title"`
	Sub     string `json:"sub"`

	ActionsTitle string  `json:"actionsTitle"`
	Missing      string  `json:"missing,omitempty"`
	ActBtns      []uiBtn `json:"actBtns,omitempty"`

	HasPlayer   bool   `json:"hasPlayer"`
	PlayerTitle string `json:"playerTitle"`
	Player      string `json:"player,omitempty"` // raw (player.go)

	HasEnc      bool     `json:"hasEnc"`
	EncTitle    string   `json:"encTitle"`
	EncDemoted  bool     `json:"encDemoted"`
	DemotedNote string   `json:"demotedNote"`
	ShowLbl     string   `json:"showLbl"`
	Enc         libEncSt `json:"enc"`

	HasHarm   bool      `json:"hasHarm"`
	HarmTitle string    `json:"harmTitle"`
	Harm      libHarmSt `json:"harm"`

	HasTags   bool       `json:"hasTags"`
	TagsTitle string     `json:"tagsTitle"`
	TagsDesc  string     `json:"tagsDesc"`
	WriteLbl  string     `json:"writeLbl"`
	WriteAct  string     `json:"writeAct"`
	RevertLbl string     `json:"revertLbl"`
	RevertAct string     `json:"revertAct"`
	TagEditor libTagEdSt `json:"tagEditor"` // library_tagfix.go

	HasPls   bool          `json:"hasPls"`
	PlsTitle string        `json:"plsTitle"`
	Pls      libTrackPlsSt `json:"pls"`

	HasCompat   bool           `json:"hasCompat"`
	CompatTitle string         `json:"compatTitle"`
	Compat      libCompatSecSt `json:"compat"` // library_compat.go

	DetailsTitle string `json:"detailsTitle"`
	Meta         []uiKV `json:"meta,omitempty"`
}

// libDetailState resolves the inspector. Caller holds s.mu.
func (u *UI) libDetailState(s *libSt) libDetailSt {
	if u.ceActiveFor("library") {
		return libDetailSt{Kind: libDetailRaw, Raw: u.ceDetailHTML(s)}
	}
	// A live beatgrid-fixer flow (confirm/running/done/cal) owns the collection inspector even when a
	// track is selected - else "Fix beatgrids" from the toolbar with a track open set the stage but
	// the confirm had nowhere to render, so the click looked dead.
	if u.libSectionOr() == "collection" && u.gfStageActive() {
		return libDetailSt{Kind: libDetailGF, GF: u.gfRailState(s)}
	}
	sel := s.sel
	if sel == nil {
		// Collection rail without a selection = the beatgrid cockpit / health card
		if u.libSectionOr() == "collection" {
			return libDetailSt{Kind: libDetailGF, GF: u.gfRailState(s)}
		}
		return libDetailSt{Kind: libDetailMsg, Msg: i18n.T("library.insp.empty")}
	}
	st := libDetailSt{Kind: libDetailSel, Eyebrow: i18n.T("library.insp.selected")}
	st.Title = sel.track.Title
	if st.Title == "" {
		st.Title = filepath.Base(sel.path)
	}
	if sel.size > 0 || !sel.mod.IsZero() {
		st.Sub = humanBytes(uint64(sel.size)) + " · " + strings.ToUpper(sel.kind)
		if !sel.mod.IsZero() {
			st.Sub += " · " + sel.mod.Format("2006-01-02 15:04")
		}
	} else {
		st.Sub = strOrDash(sel.track.Artist)
	}

	onDisk := pathOnDisk(sel.path)
	st.ActionsTitle = i18n.T("library.insp.actions")
	if !onDisk {
		st.Missing = i18n.T("library.insp.missing")
	}
	st.ActBtns = append(st.ActBtns,
		newBtn(i18n.T("library.open"), "outline", "lib-openext:"+sel.path),
		newBtn(i18n.T("library.reveal"), "outline", "lib-reveal:"+sel.path))
	if u.libSectionOr() == "collection" && u.svc.Cfg.Features.GridFix.Enabled {
		if vs := u.gfVerified(); vs != nil {
			lbl, variant := i18n.T("library.gf.markVerified"), "outline"
			if vs.Has(sel.path) {
				lbl, variant = "✓ "+i18n.T("library.gf.verifiedBadge"), "primary"
			}
			st.ActBtns = append(st.ActBtns, newBtn(lbl, variant, "gf-verify:"+sel.path))
		}
	}
	if sel.inColl && sel.kind == "audio" && len(sel.track.Beatgrid) > 0 {
		st.ActBtns = append(st.ActBtns, newBtn(i18n.T("library.ce.open"), "outline", "ce-open:"+sel.path))
	}
	st.ActBtns = append(st.ActBtns,
		newBtn(i18n.T("library.metadata"), "ghost", "lib-probe:"+sel.path),
		newBtn(i18n.T("library.copyPath"), "ghost", "copy"))

	// PLAYER + waveform (audio on disk) - the unified media player/editor (player.go).
	// Binding happens in the SELECTION HANDLER (libSelect / ceEnter), never here:
	// a render-side mpEnsureFile rebinds mid-build and re-arms the lost-patch race
	// (analysis applies patched the old DOM, the render overwrote them - stuck
	// "Analyzing waveform…" with healthy state). mpSetDrops is idempotent (no kick).
	if onDisk && sel.kind == "audio" {
		u.mpSetDrops("library", sel.path, s.dropsIdx[sel.path])
		st.HasPlayer, st.PlayerTitle, st.Player = true, i18n.T("library.insp.player"), u.mpHTML("library")
	}
	// ENCODE builder (audio + video). In collection/playlist context (incl. files
	// living in a playlist-marked folder) the per-file encoder folds away - whole
	// dirs/playlists re-encode via the batch flow; recordings + video keep it up front.
	if sel.kind == "audio" || sel.kind == "video" {
		st.HasEnc, st.EncTitle = true, i18n.T("library.insp.encoding")
		if sel.kind == "audio" && !s.encOpen && u.libEncDemoted(sel.path) {
			st.EncDemoted = true
			st.DemotedNote, st.ShowLbl = i18n.T("library.enc.demotedNote"), i18n.T("library.enc.show")
		} else {
			st.Enc = u.libEncodeState(s, sel)
		}
	}
	// HARMONIC key-wheel (audio with a key)
	if sel.kind == "audio" {
		if _, ok := musiclib.ParseKey(sel.track.Key); ok {
			st.HasHarm, st.HarmTitle, st.Harm = true, i18n.T("library.insp.harmonic"), u.libHarmonicState(s, sel)
		}
	}
	// TAGS (collection audio): library→file sync buttons + the manual tag editor
	if sel.inColl && sel.kind == "audio" {
		st.HasTags, st.TagsTitle = true, i18n.T("library.insp.tags")
		st.TagsDesc = i18n.T("library.insp.tagsDesc")
		st.WriteLbl, st.WriteAct = i18n.T("library.insp.writeTags"), "lib-tags-write:"+sel.path
		st.RevertLbl, st.RevertAct = i18n.T("library.revert"), "lib-tags-revert:"+sel.path
		st.TagEditor = u.tfEditorState(s)
	}
	// detail-rail DB reads (works-together partners + playlist membership) resolve off-thread and
	// cache on the selection - they used to run per detail render (= per keystroke).
	var detCompat []libdb.CompatRow
	var detPls []libdb.PlaylistRow
	detReady := true
	if sel.kind == "audio" {
		detCompat, detPls, detReady = u.libDetailData(s, sel.path)
	}
	// PLAYLISTS membership
	if sel.kind == "audio" {
		st.HasPls, st.PlsTitle = true, i18n.T("library.insp.playlists")
		st.Pls = u.libTrackPlaylistsState(sel.path, detPls, detReady)
	}
	// WORKS WELL TOGETHER (compat marks + discovery)
	if sel.inColl && sel.kind == "audio" && u.svc.Lib != nil {
		st.HasCompat, st.CompatTitle = true, i18n.T("library.compat.section")
		st.Compat = u.libCompatSectionState(s, sel.path, detCompat, detReady)
	}
	st.DetailsTitle = i18n.T("library.insp.details")
	st.Meta = libDetailsMetaState(sel.track)
	return st
}

// libHarmonicState resolves the key-wheel block. Caller holds s.mu.
func (u *UI) libHarmonicState(s *libSt, sel *libSel) libHarmSt {
	k, _ := musiclib.ParseKey(sel.track.Key)
	census := libKeyCensus(s.tracks)
	return libHarmSt{
		Desc: i18n.T("library.harmonic.desc"), Wheel: keywheelSVG(&k, s.keySel, census),
		SameLbl: i18n.T("library.kw.same"), RelLbl: i18n.T("library.kw.relative"),
		ShowLbl: i18n.T("library.harmonic.show"), ShowAct: "lib-key-harmonic:" + k.Camelot(),
		ClearLbl: i18n.T("library.harmonic.clear"),
	}
}

// libTrackPlaylistsState resolves the selected track's playlist chips from the off-thread-resolved
// cache (see libDetailData); ready=false shows a loading line until the first resolve lands.
func (u *UI) libTrackPlaylistsState(path string, pls []libdb.PlaylistRow, ready bool) libTrackPlsSt {
	if u.svc.Lib == nil {
		return libTrackPlsSt{Unavailable: true}
	}
	st := libTrackPlsSt{
		EmptyText: i18n.T("library.insp.notInPlaylist"),
		AddLbl:    i18n.T("library.addToPlaylist"), AddAct: "lib-track-addto:" + path,
	}
	if !ready {
		st.EmptyText = i18n.T("library.remote.col.loading")
	}
	for _, p := range pls {
		st.Chips = append(st.Chips, newChip(p.Name, "", fmt.Sprintf("lib-plgoto:%d", p.ID), false))
	}
	return st
}

// libDetailsMetaState resolves the metadata kv rows (blank values omitted, like the original).
func libDetailsMetaState(t musiclib.Track) []uiKV {
	var out []uiKV
	row := func(k, v string) {
		if v != "" {
			out = append(out, newKV(k, v))
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
	return out
}

// libEncodeState resolves the encode builder. Caller holds s.mu.
func (u *UI) libEncodeState(s *libSt, sel *libSel) libEncSt {
	if !s.draftInit {
		s.libSeedDraft(sel)
	}
	d := s.draft
	audioOnly := sel.kind == "audio"
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	st := libEncSt{
		AudioOnly: audioOnly, Desc: d.Desc, Hints: u.libMediaHintsState(sel, d),
		OutputNote: i18n.T("library.enc.outputNote"), StartLbl: i18n.T("library.enc.start"),
		SaveLbl: i18n.T("library.enc.savePreset"), SaveAsLbl: i18n.T("library.enc.saveAsNew"),
	}
	// preset picker (rich rows: description sub-line + container badge)
	st.Preset = resolveSmartSelect("lib-preset", "lib-preset:", d.ID, func() []ssOpt {
		var out []ssOpt
		for _, p := range transcode.AllPresets(custom) {
			if audioOnly && !p.IsAudioOnly() {
				continue
			}
			out = append(out, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(p.Container)})
		}
		return out
	})
	st.Preset.Label = i18n.T("library.enc.preset")
	st.Container = resolvePbSelectTip(i18n.T("library.enc.container"), "lib-pf:container", pbContainerOptsFor(audioOnly), d.Container, "enc-container")
	if !audioOnly {
		v := libEncVideoSt{QualityLbl: i18n.T("library.enc.qualityProfile"), ProfileHint: profileHint(profileOfDraft(d))}
		// container-compatible codecs only - the builder can't describe an unencodable combo
		v.VCodec = resolvePbSelectTip(i18n.T("library.enc.videoCodec"), "lib-pf:vcodec", pbVideoCodecOptsFor(d.Container), d.VideoCodec, "enc-video-codec")
		v.Accel = resolvePbSelect(i18n.T("library.enc.accel"), "lib-pf:accel", accelOpts(), d.Accel)
		for _, pr := range transcode.Profiles {
			v.Profiles = append(v.Profiles, newChip(pr, pr, "lib-pf:profile", false))
		}
		v.RateMode = resolvePbSelectTip(i18n.T("library.enc.rateMode"), "lib-pf:ratemode",
			[][2]string{{"crf", i18n.T("library.enc.rateCRF")}, {"bitrate", i18n.T("library.enc.rateBitrate")}}, d.RateMode, "enc-rate")
		if d.RateMode == "bitrate" {
			v.RateField = newPBField(i18n.T("library.enc.bitrateK"), "lib-pf:bitratek", strconv.Itoa(d.BitrateK), "number", i18n.T("library.enc.bitrateKHint"))
		} else {
			v.RateField = newPBField(i18n.T("library.enc.crf"), "lib-pf:crf", strconv.Itoa(d.CRF), "number", crfHint(d.VideoCodec))
		}
		v.Res = resolvePbSelect(i18n.T("library.enc.resolution"), "lib-pf:res", resOpts, resLabel(d.Width, d.Height))
		v.FPS = newPBField(i18n.T("library.enc.fps"), "lib-pf:fps", trimNum(d.FPS), "number", "")
		st.Video = v
	}
	st.AudioCodec = resolvePbSelectTip(i18n.T("library.enc.audioCodec"), "lib-pf:acodec", pbAudioCodecOptsFor(d.Container), d.AudioCodec, "enc-audio-codec")
	st.AudioBitrate = newPBField(i18n.T("library.enc.audioBitrate"), "lib-pf:abitratek", strconv.Itoa(d.AudioBitrateK), "number", audioCapHint(d.AudioCodec))
	st.Channels = resolvePbSelect(i18n.T("library.enc.channels"), "lib-pf:channels",
		[][2]string{{"0", i18n.T("library.enc.source")}, {"1", i18n.T("library.enc.mono")}, {"2", i18n.T("library.enc.stereo")}}, strconv.Itoa(d.Channels))
	st.SampleRate = resolvePbSelect(i18n.T("library.enc.sampleRate"), "lib-pf:samplerate",
		[][2]string{{"0", i18n.T("library.enc.source")}, {"44100", "44.1 kHz"}, {"48000", "48 kHz"}, {"96000", "96 kHz"}}, strconv.Itoa(d.SampleRate))
	// loudness - the shared block (components.go); the draft IS the preset, so no override framing
	st.Loud = newLoudSt(loudnessOpts{
		act:       func(f string) string { return "lib-pf:" + f },
		toggleLbl: i18n.T("library.enc.normalize"),
		topic:     "enc-loudness",
		vals:      loudnessVals{On: d.LoudnessOn, I: d.LoudnessI, TP: d.LoudnessTP, RaiseOnly: d.LoudnessRaiseOnly},
		preset:    &d,
	})
	st.TrimStart = newPBField(i18n.T("library.enc.trimStart"), "lib-trim-s", s.trimS, "number", "")
	st.TrimEnd = newPBField(i18n.T("library.enc.trimEnd"), "lib-trim-e", s.trimE, "number", "")
	return st
}

// libMediaHintsState resolves the source-aware compareQuality chips (calm, factual - "adds no
// quality"). Every number is formatted here; Zig only frames the chips.
func (u *UI) libMediaHintsState(sel *libSel, d transcode.Preset) []libHintSt {
	if sel.srcLoading {
		return []libHintSt{{Tone: "info", Text: i18n.T("library.hints.probing")}}
	}
	src := sel.src
	if src == nil {
		return nil
	}
	var out []libHintSt
	// source summary
	var parts []string
	if src.HasVideo {
		parts = append(parts, fmt.Sprintf("%s %d×%d %.0ffps %dk", strings.ToUpper(src.VideoCodec), src.Width, src.Height, src.FPS, src.VideoKbps))
	}
	if src.HasAudio {
		parts = append(parts, fmt.Sprintf("%s %dch %dHz %dk", strings.ToUpper(src.AudioCodec), src.Channels, src.SampleRate, src.AudioKbps))
	}
	if len(parts) > 0 {
		out = append(out, libHintSt{Tone: "info", Text: i18n.T("library.hints.source", i18n.A{"detail": strings.Join(parts, " · ")})})
	}
	// video comparisons
	if src.HasVideo && d.VideoCodec != "copy" && d.VideoCodec != "none" && d.VideoCodec != "" {
		if d.Width > 0 && src.Width > 0 && d.Width > src.Width {
			out = append(out, libHintSt{Tone: "warn", Text: i18n.T("library.hints.upscale", i18n.A{"sw": fmt.Sprint(src.Width), "sh": fmt.Sprint(src.Height), "dw": fmt.Sprint(d.Width), "dh": fmt.Sprint(d.Height)})})
		}
		if d.RateMode == "bitrate" && src.VideoKbps > 0 && d.BitrateK > int(float64(src.VideoKbps)*1.05) {
			out = append(out, libHintSt{Tone: "warn", Text: i18n.T("library.hints.vbitrate", i18n.A{"target": fmt.Sprint(d.BitrateK), "source": fmt.Sprint(src.VideoKbps)})})
		}
		if strings.EqualFold(src.VideoCodec, d.VideoCodec) {
			out = append(out, libHintSt{Tone: "info", Text: i18n.T("library.hints.alreadyCodec", i18n.A{"codec": strings.ToUpper(d.VideoCodec)})})
		}
	}
	// audio comparisons
	if src.HasAudio && d.AudioCodec != "copy" && d.AudioCodec != "none" && d.AudioCodec != "" {
		if d.SampleRate > 0 && src.SampleRate > 0 && d.SampleRate > src.SampleRate {
			out = append(out, libHintSt{Tone: "warn", Text: i18n.T("library.hints.upsample", i18n.A{"source": fmt.Sprint(src.SampleRate), "target": fmt.Sprint(d.SampleRate)})})
		}
		if d.AudioBitrateK > 0 && src.AudioKbps > 0 && d.AudioBitrateK > int(float64(src.AudioKbps)*1.05) {
			out = append(out, libHintSt{Tone: "warn", Text: i18n.T("library.hints.abitrate", i18n.A{"target": fmt.Sprint(d.AudioBitrateK), "source": fmt.Sprint(src.AudioKbps)})})
		}
	}
	return out
}

// ── shared select resolvers ──
// (resolveActionMenu lives in actionmenu.go - the publish batch added it first.)

// pbOptsFn adapts [][val,label] pairs to the smart-select opts contract.
func pbOptsFn(options [][2]string) func() []ssOpt {
	return func() []ssOpt {
		out := make([]ssOpt, 0, len(options))
		for _, op := range options {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	}
}

// resolvePbSelect registers + resolves an encode-builder property select. id is derived from
// the act exactly like pbSelect did (colons → dashes).
func resolvePbSelect(label, act string, options [][2]string, current string) selState {
	s := resolveSmartSelect(strings.ReplaceAll(act, ":", "-"), act, current, pbOptsFn(options))
	s.Label = label
	return s
}

// resolvePbSelectTip is resolvePbSelect plus the pre-rendered ss-label carrying the
// shared-glossary tooltip (tooltip.go topic markup stays Go-side).
func resolvePbSelectTip(label, act string, options [][2]string, current, topic string) libSelTip {
	s := resolveSmartSelect(strings.ReplaceAll(act, ":", "-"), act, current, pbOptsFn(options))
	return libSelTip{Sel: s, Label: `<span class=ss-label>` + html.EscapeString(label) + tipTopic(topic) + `</span>`}
}
