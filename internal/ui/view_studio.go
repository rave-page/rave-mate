package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/idmark"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/library"
	"rave.page/mate/internal/maintenance"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/playsync"
	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/transcode"
)

// studioView is the unified Local-Studio dashboard, styled after the Electron client:
// a hero header, a pill tab bar (Browse / Favorites / Collection / History), quick-access
// chips, filter+sort pills, a rich file list, and a shared right-hand detail panel. One
// design for every workflow (browse, library, history; transcode/queue are scaffolded).
type studioView struct {
	u *UI

	// mode is inferred from the selected item's kind (audio → "music", else "media") - NOT a
	// user toggle. It gates the transcode builder (audio-only presets, no video/trim section) +
	// detail-panel treatment exactly as the old explicit Music/Media switch did, without hiding
	// any section. See selectFile / selectTrack.
	mode    string
	section string
	body    *fyne.Container // swapped per section

	// chrome (JetBrains/Resolume-style shell built on the kit)
	nav           map[string]*navRailItem // section name → left-rail item
	navBox        *fyne.Container         // rail item container (collapse refresh target)
	navGroups     []*widget.Label         // rail group headers (hidden when the rail collapses)
	railCollapsed bool                    // left rail shows icons-only when true
	inspector     *kitInspector           // right-hand selected-item panel (replaces the detail VBox)
	inspectorOn   bool                    // inspector pane shown (toggle in the top bar)
	center        *fyne.Container         // holds either the body|inspector split or body alone
	contentSplit  fyne.CanvasObject       // body|inspector split (shown when inspectorOn)
	status        *kitStatusStrip         // bottom status bar
	titleLbl      *widget.Label           // top-bar current-section title

	// ── browse ──
	cwd          string
	entries      []fileEntry
	shown        []int
	kindFilter   string // "ALL"|"VIDEO"|"AUDIO"|"IMAGE"|"OTHER"
	sortBy       string // "Name"|"Modified"|"Size"
	nameFilter   string
	browseList   *widget.List
	browseGrid   *kitDensityGrid // thumbnail/card grid (View: Grid)
	browseStack  *fyne.Container // swaps browseList ↔ browseGrid
	browseView   string          // "List" | "Grid"
	browseSearch *kitSearchField
	countLbl     *widget.Label
	crumb        *fyne.Container
	chips        fyne.CanvasObject // rebuilt as a WrapActions row of quick-access + pinned folders
	marks        *library.Bookmarks
	filterRow    *kitSegmented // ALL|VIDEO|AUDIO|IMAGE|OTHER
	sortRow      *kitSegmented // Name|Modified|Size

	// ── shared imported collection ──
	install  musiclib.TraktorInstall
	tracks   []musiclib.Track
	byPath   map[string]musiclib.Track
	loaded   bool
	tagCache map[string]musiclib.Track // loose-file embedded tags, keyed by path

	waveMu      sync.Mutex
	peakCache   map[string]trackPeaks // waveform peak buckets + duration, keyed by path
	waveLoading map[string]bool       // in-flight peak analyses (dedupe)
	waveSink    func(path string)     // attached player panel's instant post-seek repaint

	artExtractRun atomic.Bool // single-flight guard for the background cover-art backfill

	batchSel map[string]fileEntry // multi-selected files for batch ops (checkbox)
	batchBar *fyne.Container      // batch toolbar (shown when batchSel non-empty)
	batchLbl *widget.Label

	// collection pane
	collSearch   *widget.Entry
	collList     *widget.List
	collShown    []int
	collSortBy   string             // Artist|Title|BPM|Key|Genre|Label|Rating|Plays ("" = import order)
	collSortDesc bool               // sort direction
	collSortRow  *widget.RadioGroup // Segmented sort selector
	collGenreSel map[string]bool    // compound filter: selected genre families (empty = all)
	collLabelSel map[string]bool    // compound filter: selected labels (empty = all)

	// harmonic key state
	keyRef       *musiclib.Key   // selected track's parsed key - drives row-pill relation colors
	collKeySel   map[string]bool // Collection key filter (keyLabel-keyed)
	browseKeySel map[string]bool // Browse key filter
	plKeySel     map[string]bool // open-playlist key filter

	// playlists pane
	plRows      []libdb.PlaylistRow
	plList      *widget.List
	plSel       int64             // selected playlist id (0 = none)
	plCur       libdb.PlaylistRow // selected playlist row
	plTracks    []musiclib.Track  // resolved tracks of the open playlist (ordered)
	plShown     []int             // key-filtered view indices into plTracks
	plPaths     []string          // raw ordered paths (manual/imported reorder source)
	plTrackList *widget.List
	plHead      *fyne.Container // right-pane header, rebuilt per selection
	plSortBy    string          // "" = stored/manual order, else lessTrack field
	plSortDesc  bool
	collSel     map[string]bool // Collection multi-select (add-to-playlist)
	collBar     *fyne.Container
	collBarLbl  *widget.Label
	syncBusy    bool // guards a cross-DJ sync run started from the UI

	// playlist cloud sync (rave.page /playlists)
	plSyncPairs  map[int64]playsync.PlaylistPair // local id → last computed pair
	plRemoteOnly []playsync.PlaylistPair         // owned remote playlists with no local mapping
	plSyncBusy   bool                            // an overview/apply is in flight (UI thread flag)
	plSyncInfo   *widget.Label                   // toolbar status line

	// history pane
	sessions     []musiclib.Session
	summaries    []musiclib.SessionSummary
	sessList     *widget.List
	played       []resolvedPlay
	playShown    []int // sorted/filtered view indices into played
	playList     *widget.List
	playSortBy   string // "" = play order, else lessTrack field
	playSortDesc bool
	playKeySel   map[string]bool // played-tracks key filter

	// transcode queue (live progress)
	jobsMu    sync.Mutex
	jobs      []*tcJob
	nextJob   int
	queueList *widget.List

	player    *nativePlayer // in-app native audio playback (beep + oto; one file at a time)
	detachWin fyne.Window   // detached now-playing window (nil = closed); follows the playing track

	// detected working encoders (test-encode result); nil = not yet detected
	workingEnc    map[string]bool
	detectStarted bool
	curFile       *fileEntry        // last-selected file, for rebuilding the detail on detect
	seedPreset    *transcode.Preset // transient: seeds the transcode builder when a chip is clicked

	// probed source info per file (drives bitrate hints + up-encode warnings); nil entry = probing
	srcCache   map[string]*transcode.SourceInfo
	srcLoading map[string]bool

	loudCache   map[string]transcode.Measurement // EBU R128 source measurements, keyed by path
	loudLoading map[string]bool
}

// tcJob is one queued/running transcode (guarded by studioView.jobsMu).
type tcJob struct {
	name        string
	presetLabel string
	status      string // queued|running|done|error|canceled
	percent     float64
	msg         string
	cancel      context.CancelFunc
}

type fileEntry struct {
	name  string
	path  string
	isDir bool
	size  int64
	mod   time.Time
	kind  string // dir|video|audio|image|other
}

type resolvedPlay struct {
	track  musiclib.Track
	onDisk bool
}

func (u *UI) buildStudio() fyne.CanvasObject {
	home, _ := os.UserHomeDir()
	mf, _ := config.DataPath("bookmarks.json")
	sv := &studioView{
		u: u, mode: "media", section: "Browse",
		cwd: home, kindFilter: "ALL", sortBy: "Modified", collSortBy: "Artist",
		byPath: map[string]musiclib.Track{}, tagCache: map[string]musiclib.Track{},
		peakCache: map[string]trackPeaks{}, waveLoading: map[string]bool{},
		batchSel:   map[string]fileEntry{},
		collSel:    map[string]bool{},
		collKeySel: map[string]bool{}, browseKeySel: map[string]bool{}, plKeySel: map[string]bool{},
		collGenreSel: map[string]bool{}, collLabelSel: map[string]bool{}, playKeySel: map[string]bool{},
		srcCache:   map[string]*transcode.SourceInfo{},
		srcLoading: map[string]bool{},
		loudCache:  map[string]transcode.Measurement{}, loudLoading: map[string]bool{},
		marks: library.LoadBookmarks(mf), body: container.NewStack(),
		player:      &nativePlayer{proxy: u.svc.Player},
		inspector:   newKitInspector("SELECTED"),
		inspectorOn: true,
		nav:         map[string]*navRailItem{},
	}
	sv.clearDetail()
	u.closers = append(u.closers, sv.stopPlayer) // kill any ffplay on app exit
	u.stopPlayback = sv.stopPlayer               // also stop on window close-to-tray

	sv.status = newKitStatusStrip()
	sv.status.SetCenter("Ready")
	topBar := sv.buildTopBar()
	rail := sv.buildNavRail()

	// center = content | inspector; adaptiveSplit reflows to a stacked column on a narrow window,
	// and the inspector collapses to body-only via the top-bar toggle. Every pane scrolls
	// internally - no whole-page scroll.
	sv.contentSplit = container.New(newAdaptiveSplit(0.70), sv.body, sv.inspector.Object())
	sv.center = container.NewStack(sv.contentSplit)

	sv.showSection("Browse")
	sv.ensureDetect()
	// Hydrate the collection from the persisted library so it's there on launch (no re-import).
	debuglog.Go(sv.u.svc.Log, "studio-loaddb", sv.loadFromDB)
	// Border: top bar / left rail / bottom status strip / center content. The rail spans the
	// height between the bar and the strip (JetBrains tool-window rail).
	localContent := container.NewBorder(
		container.NewVBox(topBar, widget.NewSeparator()),
		sv.status.Object(), rail, nil, sv.center)

	// "Controlling: [This computer | peer]" - local shows the studio view; a peer shows the
	// same shell over remotectl. Local view is built once and kept warm. The switcher row is
	// LIVE: it (re)appears/updates as peers connect/disconnect (the old build-once switcher
	// vanished when no peer was connected at app launch - i.e. always - and went stale after
	// reconnects, leaving the remote view erroring on every call).
	center := container.NewStack(localContent)
	swBox := container.NewVBox()
	target := ""   // selected node id ("" = local)
	shownFor := "" // node id the center currently renders (avoid needless remote rebuilds)
	onSelect := func(nodeID string) {
		target = nodeID
		if nodeID == shownFor {
			return
		}
		shownFor = nodeID
		if client := u.remoteClient(nodeID); client != nil {
			center.Objects = []fyne.CanvasObject{u.buildRemoteLibrary(client)}
		} else {
			center.Objects = []fyne.CanvasObject{localContent}
		}
		center.Refresh()
	}
	refreshSwitcher := func() {
		peers := u.controllablePeers()
		if target != "" {
			gone := true
			for _, p := range peers {
				if p.NodeID == target {
					gone = false
					break
				}
			}
			if gone {
				onSelect("")
				u.Notify("rave-mate", "Paired instance disconnected - showing this computer's library.")
			}
		}
		if switcher, ok := u.targetSwitcher(target, onSelect); ok {
			swBox.Objects = []fyne.CanvasObject{switcher, widget.NewSeparator()}
		} else {
			swBox.Objects = nil
		}
		swBox.Refresh()
	}
	refreshSwitcher()
	u.libraryPeersRefresh = func() { fyne.Do(refreshSwitcher) }
	u.closers = append(u.closers, func() { u.libraryPeersRefresh = nil })
	return container.NewBorder(swBox, nil, nil, nil, center)
}

// ── top bar (global tools) ───────────────────────────────────────────────────

// buildTopBar is the dense global toolbar: rail toggle · open-folder / pin · current-section
// title · inspector toggle. Section-local filters/sorts live in each section's own strip, so
// this bar stays constant across sections (JetBrains main-toolbar feel).
func (sv *studioView) buildTopBar() fyne.CanvasObject {
	railTgl := newKitIconButton(theme.MenuIcon(), "Collapse / expand the section rail", sv.toggleRail)
	browse := newKitIconButton(theme.FolderOpenIcon(), "Open a folder in Browse", func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFolderOpen(win, func(u fyne.ListableURI, _ error) {
			if u != nil {
				sv.showSection("Browse")
				sv.navigate(u.Path())
			}
		})
	})
	pin := newKitIconButton(theme.GridIcon(), "Pin the current folder to Favorites", func() {
		sv.marks.Toggle(sv.cwd, filepath.Base(sv.cwd))
		sv.rebuildChips()
	})
	sv.titleLbl = boldLabel("Browse")
	inspTgl := newKitIconButton(theme.InfoIcon(), "Show / hide the inspector panel", sv.toggleInspector)
	left := container.NewHBox(railTgl, kitToolSep(), browse, pin, kitToolSep(),
		smallCaps("LIBRARY"), sv.titleLbl)
	bar := container.NewBorder(nil, nil, left, inspTgl)
	bg := canvas.NewRectangle(colSurface)
	return container.NewStack(bg, container.NewPadded(bar))
}

// ── left section rail ─────────────────────────────────────────────────────────

// navRailItem is one clickable section entry in the left rail: an icon (with tooltip) plus a
// text label. The label + group headers hide when the rail collapses, leaving an icons-only
// rail (tooltips still name each section).
type navRailItem struct {
	name  string
	icon  *kitIconButton
	label *widget.Button
	row   *fyne.Container
}

// buildNavRail builds the collapsible, grouped section rail - the dense vertical replacement
// for the old wrapping horizontal pill bar. Sections are grouped by workflow: FILES (browse /
// listen), LIBRARY (manage / prepare), JOBS (transcode / presets).
func (sv *studioView) buildNavRail() fyne.CanvasObject {
	mk := func(name, hint string, icon fyne.Resource) *fyne.Container {
		it := &navRailItem{name: name}
		it.icon = newKitIconButton(icon, name+" - "+hint, func() { sv.showSection(name) })
		it.label = widget.NewButton(name, func() { sv.showSection(name) })
		it.label.Importance = widget.LowImportance
		it.label.Alignment = widget.ButtonAlignLeading
		it.row = container.NewBorder(nil, nil, it.icon, nil, it.label)
		sv.nav[name] = it
		return it.row
	}
	group := func(title string, items ...*fyne.Container) *fyne.Container {
		hdr := smallCaps(title)
		sv.navGroups = append(sv.navGroups, hdr)
		objs := make([]fyne.CanvasObject, 0, len(items)+1)
		objs = append(objs, hdr)
		for _, it := range items {
			objs = append(objs, it)
		}
		return container.NewVBox(objs...)
	}
	sv.navBox = container.NewVBox(
		group("FILES",
			mk("Browse", "file browser + players", theme.FolderOpenIcon()),
			mk("Favorites", "pinned folders", theme.GridIcon())),
		widget.NewSeparator(),
		group("LIBRARY",
			mk("Collection", "imported DJ tracks", theme.StorageIcon()),
			mk("Playlists", "manual · smart · imported", theme.MenuIcon()),
			mk("History", "your played sets", theme.HistoryIcon()),
			mk("ID Marks", "hide unreleased tracks on every output", theme.VisibilityOffIcon())),
		widget.NewSeparator(),
		group("JOBS",
			mk("Queue", "transcode / batch progress", theme.ListIcon()),
			mk("Presets", "encode presets", theme.SettingsIcon())),
	)
	// VScroll keeps the rail usable if the window is very short; it still reports the rail's
	// content width to the enclosing Border so the labels aren't clipped when expanded.
	return container.NewVScroll(sv.navBox)
}

// toggleRail collapses the rail to icons-only (labels + group headers hidden) and back.
func (sv *studioView) toggleRail() {
	sv.railCollapsed = !sv.railCollapsed
	for _, it := range sv.nav {
		if sv.railCollapsed {
			it.label.Hide()
		} else {
			it.label.Show()
		}
	}
	for _, h := range sv.navGroups {
		if sv.railCollapsed {
			h.Hide()
		} else {
			h.Show()
		}
	}
	if sv.navBox != nil {
		sv.navBox.Refresh()
	}
}

// toggleInspector swaps the center between the content|inspector split and content-only.
func (sv *studioView) toggleInspector() {
	sv.inspectorOn = !sv.inspectorOn
	if sv.center == nil {
		return
	}
	if sv.inspectorOn {
		sv.center.Objects = []fyne.CanvasObject{sv.contentSplit}
	} else {
		sv.center.Objects = []fyne.CanvasObject{sv.body}
	}
	sv.center.Refresh()
}

// showInspector ensures the inspector pane is visible (called when the user selects an item).
func (sv *studioView) showInspector() {
	if !sv.inspectorOn {
		sv.toggleInspector()
	}
}

func (sv *studioView) showSection(name string) {
	sv.section = name
	if sv.titleLbl != nil {
		sv.titleLbl.SetText(name)
	}
	for n, it := range sv.nav {
		active := n == name
		it.icon.SetActive(active)
		it.label.Importance = lowOrHigh(active)
		it.label.Refresh()
	}
	var o fyne.CanvasObject
	switch name {
	case "Favorites":
		o = sv.favoritesSection()
	case "Collection":
		o = sv.collectionSection()
	case "Playlists":
		o = sv.playlistsSection()
	case "History":
		o = sv.historySection()
	case "ID Marks":
		o = sv.idMarksSection()
	case "Queue":
		o = sv.queueSection()
	case "Presets":
		o = sv.presetsSection()
	default:
		o = sv.browseSection()
	}
	sv.body.Objects = []fyne.CanvasObject{o}
	sv.body.Refresh()
	if sv.status != nil {
		if name == "Browse" {
			sv.status.SetLeft(fmt.Sprintf("%d items · %s", len(sv.shown), sv.cwd))
		} else {
			sv.status.SetLeft(name)
		}
	}
}

// ── Browse section ───────────────────────────────────────────────────────────

func (sv *studioView) browseSection() fyne.CanvasObject {
	if sv.browseList != nil {
		return sv.browseListContainer()
	}
	if sv.browseView == "" {
		sv.browseView = "List"
	}
	sv.chips = WrapActions() // populated by rebuildChips once the cwd resolves
	// breadcrumb (populated by rebuildCrumb). HBox, NOT WrapActions: the crumb sits inside
	// kitToolStrip's RowWrapLayout, and a wrap layout's MinSize is its single widest child -
	// nesting one collapses the crumb to one segment per row.
	sv.crumb = container.NewHBox()
	sv.browseSearch = newKitSearchField("Filter by name…", func(q string) {
		sv.nameFilter = q
		sv.applyBrowseFilter()
		sv.refreshBrowse()
	})
	sv.countLbl = mutedInline("0") // must not wrap, else "63" stacks to "6"/"3"

	filterOpts := []string{"ALL", "VIDEO", "AUDIO", "IMAGE", "OTHER"}
	sv.filterRow = newKitSegmented(filterOpts, sv.kindFilter, func(k string) {
		sv.kindFilter = k
		sv.applyBrowseFilter()
		sv.refreshBrowse()
	})
	sortOpts := []string{"Name", "Modified", "Size"}
	sv.sortRow = newKitSegmented(sortOpts, sv.sortBy, func(s string) {
		sv.sortBy = s
		sv.sortEntries()
		sv.applyBrowseFilter()
		sv.refreshBrowse()
	})

	sv.browseList = widget.NewList(
		func() int { return len(sv.shown) },
		func() fyne.CanvasObject { return newFileRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(sv.shown) {
				e := sv.entries[sv.shown[id]]
				sv.fillFileRow(o, e)
				setRowMenu(o, func(pos fyne.Position, obj fyne.CanvasObject) { sv.showRowMenu(e, pos, obj) })
			}
		},
	)
	sv.browseList.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(sv.shown) {
			return
		}
		e := sv.entries[sv.shown[id]]
		if e.isDir {
			sv.browseList.UnselectAll()
			sv.navigate(e.path)
		} else {
			sv.selectFile(e)
		}
	}

	// Grid view: dense thumbnail/card browser. Card body = open dir / select file; hover
	// overlay = Open / Reveal. Batch multi-select stays a List feature (checkboxes).
	sv.browseGrid = newKitDensityGrid(150, 132)
	sv.browseGrid.SetActions(
		kitGridAction{ID: "open", Icon: theme.MediaPlayIcon(), Tip: "Open"},
		kitGridAction{ID: "reveal", Icon: theme.FolderOpenIcon(), Tip: "Reveal in file manager"},
	)
	sv.browseGrid.OnActivate = func(id string) {
		if e, ok := sv.browseEntry(id); ok {
			if e.isDir {
				sv.navigate(e.path)
			} else {
				sv.selectFile(e)
			}
		}
	}
	sv.browseGrid.OnAction = func(id, action string) {
		switch action {
		case "open":
			openFile(id)
		case "reveal":
			revealFile(id)
		}
	}
	sv.browseGrid.OnSecondary = func(id string, ev *fyne.PointEvent) {
		if e, ok := sv.browseEntry(id); ok {
			sv.showRowMenu(e, ev.AbsolutePosition, sv.browseGrid)
		}
	}
	sv.browseStack = container.NewStack(sv.browseList)

	sv.batchBar = sv.buildBatchBar()
	sv.navigate(sv.cwd)
	sv.showBrowseView()
	return sv.browseListContainer()
}

// showRowMenu opens the shared file context menu for a local row.
func (sv *studioView) showRowMenu(e fileEntry, pos fyne.Position, anchor fyne.CanvasObject) {
	showFileMenu(sv.u, sv.rowMenuCtx(e, func() { sv.navigate(sv.cwd) }), pos, anchor)
}

// rowMenuCtx builds the local-row menu context (shared by Browse + Favorites).
func (sv *studioView) rowMenuCtx(e fileEntry, refresh func()) fileMenuCtx {
	c := fileMenuCtx{
		ops:     localOps{},
		entry:   e,
		refresh: refresh,
		xfer:    sv.u.svc.FileXfer,
	}
	if st := sv.u.svc.IDMarks; st != nil {
		c.marked = st.IsMarked(e.path)
		c.onMark = func(mark bool) {
			if mark {
				what := "track"
				if e.isDir {
					what = "folder (recursive)"
				}
				st.Set(e.path, idmark.Mark{})
				sv.u.Notify("rave-mate", "Marked as ID - every output now hides this "+what+"'s identity. Flags: Library → ID Marks.")
			} else {
				st.Remove(e.path)
				sv.u.Notify("rave-mate", "ID mark removed for "+e.name+".")
			}
		}
	}
	return c
}

// browseEntry finds a browse entry by path (grid activation passes the path as the id).
func (sv *studioView) browseEntry(path string) (fileEntry, bool) {
	for _, e := range sv.entries {
		if e.path == path {
			return e, true
		}
	}
	return fileEntry{}, false
}

// browseGridItems projects the filtered browse view (sv.shown) to grid cards; image files get a
// thumbnail from their own path.
func (sv *studioView) browseGridItems() []kitGridItem {
	items := make([]kitGridItem, 0, len(sv.shown))
	for _, idx := range sv.shown {
		e := sv.entries[idx]
		sub := "Folder"
		if !e.isDir {
			sub = humanBytes(e.size) + " · " + strings.ToUpper(e.kind)
		}
		it := kitGridItem{ID: e.path, Title: e.name, Subtitle: sub, Icon: kindIcon(e.kind, e.isDir)}
		if e.kind == "image" {
			it.ThumbPath = e.path
		}
		items = append(items, it)
	}
	return items
}

// showBrowseView swaps the browse body between the list and the thumbnail grid.
func (sv *studioView) showBrowseView() {
	if sv.browseStack == nil {
		return
	}
	if sv.browseView == "Grid" {
		sv.browseGrid.SetItems(sv.browseGridItems())
		sv.browseStack.Objects = []fyne.CanvasObject{sv.browseGrid}
	} else {
		sv.browseStack.Objects = []fyne.CanvasObject{sv.browseList}
	}
	sv.browseStack.Refresh()
}

// refreshBrowse repaints the active browse view (list or grid) + updates the status strip.
func (sv *studioView) refreshBrowse() {
	if sv.browseList != nil {
		sv.browseList.Refresh()
	}
	if sv.browseGrid != nil && sv.browseView == "Grid" {
		sv.browseGrid.SetItems(sv.browseGridItems())
	}
	if sv.status != nil {
		sv.status.SetLeft(fmt.Sprintf("%d items · %s", len(sv.shown), sv.cwd))
	}
}

func (sv *studioView) browseListContainer() fyne.CanvasObject {
	keyChip := sv.keyFilterChip(sv.tracks, sv.browseKeySel, func() {
		sv.applyBrowseFilter()
		sv.refreshBrowse()
	})
	up := newKitIconButton(theme.NavigateBackIcon(), "Up to the parent folder", func() {
		if p := filepath.Dir(sv.cwd); p != sv.cwd {
			sv.navigate(p)
		}
	})
	viewSeg := newKitSegmented([]string{"List", "Grid"}, sv.browseView, func(v string) {
		sv.browseView = v
		sv.showBrowseView()
	})
	// dense filter/sort strip - search, KIND, SORT, VIEW, key filter, count
	filterStrip := kitToolStrip(
		container.NewGridWrap(fyne.NewSize(200, kitSegH), sv.browseSearch.Object()),
		kitToolSep(), smallCaps("KIND"), sv.filterRow,
		kitToolSep(), smallCaps("SORT"), sv.sortRow,
		kitToolSep(), smallCaps("VIEW"), viewSeg,
		kitToolSep(), keyChip, sv.countLbl,
	)
	head := container.NewVBox(
		kitToolStrip(up, kitToolSep(), sv.crumb), // breadcrumb strip
		sv.chips,                                 // quick-access + pinned folders
		filterStrip,
		widget.NewSeparator(),
	)
	return container.NewBorder(head, sv.batchBar, nil, nil, sv.browseStack)
}

func (sv *studioView) navigate(dir string) {
	dir = filepath.Clean(dir)
	ents, err := listDir(dir)
	if err != nil {
		return
	}
	sv.cwd = dir
	sv.entries = ents
	sv.sortEntries()
	sv.applyBrowseFilter()
	sv.rebuildCrumb()
	sv.rebuildChips()
	sv.refreshFilterPills()
	sv.refreshBrowse()
}

func (sv *studioView) sortEntries() {
	sort.SliceStable(sv.entries, func(i, j int) bool {
		a, b := sv.entries[i], sv.entries[j]
		if a.isDir != b.isDir {
			return a.isDir
		}
		switch sv.sortBy {
		case "Size":
			return a.size > b.size
		case "Name":
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		default: // Modified
			return a.mod.After(b.mod)
		}
	})
}

func (sv *studioView) applyBrowseFilter() {
	q := strings.ToLower(strings.TrimSpace(sv.nameFilter))
	sv.shown = sv.shown[:0]
	keyed := anyKeySelected(sv.browseKeySel)
	for i, e := range sv.entries {
		if !e.isDir && sv.kindFilter != "ALL" && !strings.EqualFold(e.kind, sv.kindFilter) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		if keyed && !e.isDir { // key filter resolves via the imported collection
			t, ok := sv.byPath[e.path]
			if !ok || !keyMatches(t.Key, sv.browseKeySel) {
				continue
			}
		}
		sv.shown = append(sv.shown, i)
	}
	if sv.countLbl != nil {
		sv.countLbl.SetText(fmt.Sprintf("%d", len(sv.shown)))
	}
}

func (sv *studioView) rebuildCrumb() {
	if sv.crumb == nil {
		return
	}
	sv.crumb.Objects = sv.crumb.Objects[:0]
	acc := ""
	for i, seg := range splitPathSegs(sv.cwd) {
		acc = seg.path
		target := acc
		b := newKitButton(seg.label, func() { sv.navigate(target) })
		sv.crumb.Add(b)
		if i < len(splitPathSegs(sv.cwd))-1 {
			sv.crumb.Add(mutedLabel("›"))
		}
	}
	sv.crumb.Refresh()
}

func (sv *studioView) rebuildChips() {
	if sv.chips == nil {
		return
	}
	home, _ := os.UserHomeDir()
	quick := []struct{ label, sub string }{
		{"HOME", ""}, {"DESKTOP", "Desktop"}, {"DOWNLOADS", "Downloads"},
		{"MUSIC", "Music"}, {"VIDEOS", "Videos"}, {"PICTURES", "Pictures"},
	}
	items := make([]fyne.CanvasObject, 0, len(quick)+len(sv.marks.List()))
	for _, q := range quick {
		p := home
		if q.sub != "" {
			p = filepath.Join(home, q.sub)
		}
		target := p
		b := newKitButton(q.label, func() { sv.navigate(target) })
		b.SetVariant(outlineOrBrand(sv.cwd == target))
		items = append(items, b)
	}
	for _, bm := range sv.marks.List() {
		target := bm.Path
		b := newKitButtonWithIcon(bm.Label, theme.GridIcon(), func() { sv.navigate(target) })
		b.SetVariant(outlineOrBrand(sv.cwd == target))
		items = append(items, b)
	}
	sv.chips = WrapActions(items...)
}

// refreshFilterPills is now a no-op for filter/sort - the RadioGroup (Segmented)
// highlights its selected option automatically. Kept as a method so existing call
// sites that rebuild browse state still compile (chip/quick-access highlights also
// re-render via rebuildChips, called from navigate()).
func (sv *studioView) refreshFilterPills() {}

// ── detail panel ─────────────────────────────────────────────────────────────

func (sv *studioView) clearDetail() {
	sv.inspector.SetHeader("Nothing selected", "Select a file or track to see details + actions.")
	sv.inspector.SetSections()
}

// modeForKind mirrors the old Music/Media switch, now inferred from the item type: audio →
// "music" (audio-only presets, tag/cue tools, no video/trim), else "media". No feature hides -
// every section stays reachable; only the transcode builder + detail panels adapt.
func modeForKind(kind string) string {
	if kind == "audio" {
		return "music"
	}
	return "media"
}

func (sv *studioView) selectFile(e fileEntry) {
	sv.showInspector()
	sv.mode = modeForKind(e.kind)
	sv.curFile = &e
	sv.inspector.SetHeader(e.name,
		fmt.Sprintf("%s · %s · %s", humanBytes(e.size), strings.ToUpper(e.kind), e.mod.Format("2006-01-02 15:04")))
	// Cross-reference an imported DJ-collection track (BPM/key/cues/beatgrid) by path.
	t := musiclib.Track{Path: e.path, Title: e.name}
	if ct, ok := sv.byPath[e.path]; ok {
		t = ct
	} else if ct, ok := sv.tagCache[e.path]; ok {
		t = ct
	}
	sv.setKeyRef(t.Key)
	secs := []kitSection{{Key: "actions", Title: "ACTIONS", DefaultOpen: true, Content: sv.actionBar(e.path, true, e.kind)}}
	if isMediaPath(e.path) {
		secs = append(secs, kitSection{Key: "player", Title: "PLAYER", DefaultOpen: true, Content: sv.playerPanel(e.path)})
	}
	if e.kind == "video" || e.kind == "audio" {
		secs = append(secs, kitSection{Key: "encode", Title: "ENCODING / TRANSCODE",
			Help:    "Convert to another format/codec. Output goes to a new rave-mate-transcoded folder - the original is never touched.",
			Content: sv.transcodePanel(e)})
	}
	if e.kind == "video" {
		secs = append(secs, kitSection{Key: "quick", Title: "QUICK ACTIONS", Content: sv.quickActions(e)})
	}
	if e.kind == "audio" {
		secs = append(secs, kitSection{Key: "playlists", Title: "PLAYLISTS", Content: sv.playlistPanel(t)})
		if hp := sv.harmonicPanel(t); hp != nil {
			secs = append(secs, kitSection{Key: "harmonic", Title: "HARMONIC KEYS",
				Help:    "Camelot wheel: keys that mix well with this track are colored. Tap a key to filter the Collection to it.",
				Content: hp})
		}
	}
	secs = append(secs, kitSection{Key: "details", Title: "DETAILS", Content: sv.metaPanel(t)})
	sv.inspector.SetSections(secs...)
	// Enrich loose audio files with embedded tags (async). Guard on BOTH the collection and the
	// tag cache - loadTags rebuilds the detail on completion, so without the tagCache check this
	// re-fires every render (infinite tags+waveform loop).
	if e.kind == "audio" {
		_, inColl := sv.byPath[e.path]
		_, cached := sv.tagCache[e.path]
		if !inColl && !cached {
			sv.loadTags(e.path)
		}
	}
}

// ensureDetect runs reliable (test-encode) encoder detection once per session; on
// completion it caches the working set + rebuilds the current detail so HW presets unlock.
func (sv *studioView) ensureDetect() {
	if sv.detectStarted || sv.u.svc.Workers == nil {
		return
	}
	sv.detectStarted = true
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "encoder-detect", false)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		raw, err := sv.u.svc.Workers.RunBackground(ctx, "transcode", "transcode.detect", nil)
		if err != nil {
			return
		}
		var r struct {
			Encoders []struct {
				Name    string `json:"name"`
				Working bool   `json:"working"`
			} `json:"encoders"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return
		}
		m := make(map[string]bool, len(r.Encoders))
		for _, e := range r.Encoders {
			if e.Working {
				m[e.Name] = true
			}
		}
		fyne.Do(func() {
			sv.workingEnc = m
			if sv.curFile != nil {
				sv.selectFile(*sv.curFile)
			}
		})
	}()
}

// ensureSource probes a file's stream/format info once (out-of-process), caches the parsed
// SourceInfo, and re-renders the detail so the transcode panel can show source-aware bitrate
// hints + up-encode warnings. No-op when already cached or in-flight.
func (sv *studioView) ensureSource(path string) {
	if sv.u.svc.Workers == nil || path == "" {
		return
	}
	if _, ok := sv.srcCache[path]; ok || sv.srcLoading[path] {
		return
	}
	sv.srcLoading[path] = true
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-source", false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, err := sv.u.svc.Workers.RunBackground(ctx, "probe", "probe.streams", map[string]any{"path": path})
		var si transcode.SourceInfo
		if err == nil {
			si, _ = transcode.ParseProbe(raw)
		}
		fyne.Do(func() {
			sv.srcLoading[path] = false
			info := si
			sv.srcCache[path] = &info
			if sv.curFile != nil && sv.curFile.path == path {
				sv.selectFile(*sv.curFile)
			}
		})
	}()
}

// detectSilence probes leading/trailing silence (any length) and offers to set Trim start
// to skip the leading silence.
func (sv *studioView) detectSilence(e fileEntry, trimS *widget.Entry) {
	if sv.u.svc.Workers == nil {
		return
	}
	sv.u.Notify("rave-mate", "Detecting silence in "+e.name+" …")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "silence-detect", false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		raw, err := sv.u.svc.Workers.Run(ctx, "transcode", "transcode.silence",
			map[string]any{"path": e.path, "thresholdDb": -50, "minSilence": 2})
		if err != nil {
			sv.u.Notify("rave-mate", "Silence: "+err.Error())
			return
		}
		var r struct {
			Leading  float64 `json:"leadingSeconds"`
			Trailing float64 `json:"trailingSeconds"`
		}
		_ = json.Unmarshal(raw, &r)
		fyne.Do(func() {
			win := currentWindow()
			if win == nil {
				return
			}
			msg := fmt.Sprintf("Leading silence: %.1fs · Trailing: %.1fs", r.Leading, r.Trailing)
			if r.Leading <= 0 {
				dialog.ShowInformation("Silence", msg, win)
				return
			}
			dialog.ShowConfirm("Silence detected", msg+"\n\nSet Trim start to skip the leading silence?",
				func(ok bool) {
					if ok {
						trimS.SetText(strconv.FormatFloat(r.Leading, 'f', 2, 64))
					}
				}, win)
		})
	}()
}

// startTranscode resolves the preset's HW encoder (UI owns detection) and runs the job on
// the shared hub, so the web Local Studio client can attach to / cancel a desktop-started
// transcode (and vice versa). Progress + terminal state arrive via the hub callbacks.
func (sv *studioView) startTranscode(e fileEntry, preset transcode.Preset, ts, te string) {
	hub := sv.u.svc.Hub
	if hub == nil {
		sv.u.Notify("rave-mate", "Worker unavailable.")
		return
	}
	preset = transcode.NormalizePreset(preset)
	if sv.workingEnc != nil {
		if enc, ok := transcode.ResolveEncoder(preset.VideoCodec, preset.Accel, sv.workingEnc); ok {
			preset.EncoderOverride = enc
		}
	}
	id := preset.ID
	if id == "" {
		id = "custom"
	}
	base := strings.TrimSuffix(filepath.Base(e.path), filepath.Ext(e.path))
	out := filepath.Join(filepath.Dir(e.path), "rave-mate-transcoded", base+"-"+id+preset.Ext())
	tsF, _ := strconv.ParseFloat(strings.TrimSpace(ts), 64)
	teF, _ := strconv.ParseFloat(strings.TrimSpace(te), 64)

	sv.jobsMu.Lock()
	sv.nextJob++
	jobID := fmt.Sprintf("ui-%d", sv.nextJob)
	j := &tcJob{name: e.name, presetLabel: preset.Label, status: "queued", cancel: func() { hub.Cancel(jobID) }}
	sv.jobs = append([]*tcJob{j}, sv.jobs...) // newest first
	sv.jobsMu.Unlock()
	sv.refreshQueue()
	sv.showSection("Queue")

	params := map[string]any{"input": e.path, "output": out, "preset": preset, "trimStart": tsF, "trimEnd": teF}
	hub.Start(jobID, params,
		func(event string, data json.RawMessage) {
			if event != "progress" {
				return
			}
			var p struct {
				Percent float64 `json:"percent"`
			}
			if json.Unmarshal(data, &p) == nil {
				sv.updateJob(j, func() {
					if j.status == "queued" {
						j.status = "running"
					}
					j.percent = p.Percent
				})
			}
		},
		func(r jobs.EndResult) {
			switch {
			case r.Canceled:
				sv.updateJob(j, func() { j.status = "canceled" })
			case !r.OK:
				sv.updateJob(j, func() { j.status = "error"; j.msg = r.Error })
				sv.u.Notify("rave-mate", "Transcode failed: "+r.Error)
			default:
				sv.updateJob(j, func() { j.status = "done"; j.percent = 100 })
				sv.u.Notify("rave-mate", "Transcoded → "+out)
			}
		},
	)
}

func (sv *studioView) updateJob(j *tcJob, mutate func()) {
	sv.jobsMu.Lock()
	mutate()
	sv.jobsMu.Unlock()
	sv.refreshQueue()
}

func (sv *studioView) refreshQueue() {
	if sv.queueList != nil {
		fyne.Do(func() { sv.queueList.Refresh() })
	}
}

// queueSection lists transcode jobs with live progress bars.
func (sv *studioView) queueSection() fyne.CanvasObject {
	if sv.queueList == nil {
		sv.queueList = widget.NewList(
			func() int { sv.jobsMu.Lock(); defer sv.jobsMu.Unlock(); return len(sv.jobs) },
			func() fyne.CanvasObject {
				n := widget.NewLabel("")
				n.Truncation = fyne.TextTruncateEllipsis
				cancelBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), nil)
				cancelBtn.Importance = widget.LowImportance
				// top = name+cancel, center = progress bar.
				return container.NewBorder(container.NewBorder(nil, nil, nil, cancelBtn, n), nil, nil, nil, widget.NewProgressBar())
			},
			func(id widget.ListItemID, o fyne.CanvasObject) {
				sv.jobsMu.Lock()
				if id < 0 || id >= len(sv.jobs) {
					sv.jobsMu.Unlock()
					return
				}
				j := sv.jobs[id]
				running := j.status == "running" || j.status == "queued"
				label := fmt.Sprintf("%s · %s · %s", j.name, j.presetLabel, j.status)
				if j.status == "error" && j.msg != "" {
					label += " - " + j.msg
				}
				pct := j.percent
				sv.jobsMu.Unlock()

				// Border containers order Objects as [border objs…, center objs…] - find
				// by type, not index (Objects[0] of the top row is the cancel Button).
				outer := o.(*fyne.Container)
				var nameLbl *widget.Label
				var cancelBtn *widget.Button
				var bar *widget.ProgressBar
				for _, ob := range outer.Objects {
					switch t := ob.(type) {
					case *widget.ProgressBar:
						bar = t
					case *fyne.Container:
						for _, in := range t.Objects {
							switch w := in.(type) {
							case *widget.Label:
								nameLbl = w
							case *widget.Button:
								cancelBtn = w
							}
						}
					}
				}
				if nameLbl == nil || cancelBtn == nil || bar == nil {
					return
				}
				nameLbl.SetText(label)
				bar.SetValue(pct / 100)
				if running {
					cancelBtn.Enable()
					cancelBtn.OnTapped = func() {
						sv.jobsMu.Lock()
						if j.cancel != nil {
							j.cancel()
						}
						sv.jobsMu.Unlock()
					}
				} else {
					cancelBtn.Disable()
					cancelBtn.OnTapped = nil
				}
			},
		)
	}
	head := container.NewVBox(
		mutedLabel("Transcode jobs. Output → a new ‘rave-mate-transcoded’ folder beside each source - originals untouched."),
		widget.NewSeparator(),
	)
	return container.NewBorder(head, nil, nil, nil, sv.queueList)
}

func (sv *studioView) selectTrack(t musiclib.Track, onDisk bool) {
	sv.showInspector()
	sv.mode = "music" // collection/played entries are DJ tracks → music treatment
	sv.setKeyRef(t.Key)
	sub := strOrDash(t.Artist)
	if t.BPM > 0 {
		sub = joinDot(sub, fmt.Sprintf("%.0f BPM", t.BPM))
	}
	if k, ok := musiclib.ParseKey(t.Key); ok {
		sub = joinDot(sub, keyLabel(k))
	} else {
		sub = joinDot(sub, t.Key)
	}
	sv.inspector.SetHeader(strOrDash(t.Title), sub)
	secs := []kitSection{{Key: "actions", Title: "ACTIONS", DefaultOpen: true, Content: sv.actionBar(t.Path, onDisk, "")}}
	if onDisk && isMediaPath(t.Path) {
		secs = append(secs, kitSection{Key: "player", Title: "PLAYER", DefaultOpen: true, Content: sv.playerPanel(t.Path)})
	}
	secs = append(secs,
		kitSection{Key: "tags", Title: "TAGS",
			Help:    "Write the DJ analysis (BPM/key/genre/comment) into the file's tags (MP3/FLAC). Revertible.",
			Content: sv.tagActionBar(t, onDisk)},
		kitSection{Key: "playlists", Title: "PLAYLISTS", Content: sv.playlistPanel(t)})
	if hp := sv.harmonicPanel(t); hp != nil {
		secs = append(secs, kitSection{Key: "harmonic", Title: "HARMONIC KEYS",
			Help:    "Camelot wheel: keys that mix well with this track are colored. Tap a key to filter the Collection to it.",
			Content: hp})
	}
	secs = append(secs, kitSection{Key: "details", Title: "DETAILS", Content: sv.metaPanel(t)})
	sv.inspector.SetSections(secs...)
}

func (sv *studioView) actionBar(path string, onDisk bool, _ string) fyne.CanvasObject {
	open := newKitButtonWithIcon("Open", theme.MediaPlayIcon(), func() { openFile(path) })
	reveal := newKitButtonWithIcon("Reveal", theme.FolderOpenIcon(), func() { revealFile(path) })
	probe := newKitButtonWithIcon("Metadata", theme.InfoIcon(), func() { sv.probeMeta(path) })
	cp := newKitButtonWithIcon("Copy path", theme.ContentCopyIcon(), func() { fyne.CurrentApp().Clipboard().SetContent(path) })
	if !onDisk {
		open.Disable()
		reveal.Disable()
		probe.Disable()
	}
	state := mutedLabel("● file present")
	if !onDisk {
		state = mutedLabel("○ file missing on disk")
	}
	return container.NewVBox(state, container.NewGridWithColumns(2, open, reveal), container.NewGridWithColumns(2, probe, cp))
}

func (sv *studioView) metaPanel(t musiclib.Track) fyne.CanvasObject {
	row := func(k, v string) fyne.CanvasObject {
		if v == "" {
			v = "-"
		}
		value := widget.NewLabel(v)
		value.Wrapping = fyne.TextWrapBreak
		return container.NewVBox(mutedInline(k), value)
	}
	box := container.NewVBox(row("Path", t.Path))
	if t.Artist != "" {
		box.Add(row("Artist", t.Artist))
	}
	if t.Album != "" {
		box.Add(row("Album", t.Album))
	}
	if t.Genre != "" {
		box.Add(row("Genre", t.Genre))
	}
	if t.Label != "" {
		box.Add(row("Label", t.Label))
	}
	if t.BPM > 0 {
		box.Add(row("BPM", fmt.Sprintf("%.0f", t.BPM)))
	}
	if t.Key != "" {
		box.Add(row("Key", t.Key))
	}
	if t.DurationSec > 0 {
		box.Add(row("Duration", fmtDur(t.DurationSec)))
	}
	if len(t.Cues) > 0 {
		box.Add(row("Cues", fmt.Sprintf("%d", len(t.Cues))))
	}
	if len(t.Beatgrid) > 0 {
		box.Add(row("Beatgrid", fmt.Sprintf("%d marker(s)", len(t.Beatgrid))))
	}
	return box
}

func (sv *studioView) probeMeta(path string) {
	if sv.u.svc.Workers == nil {
		sv.u.Notify("rave-mate", "Worker unavailable.")
		return
	}
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-probe", false)
		raw, err := sv.u.svc.Workers.Run(context.Background(), "probe", "probe.streams", map[string]any{"path": path})
		if err != nil {
			sv.u.Notify("rave-mate", "Probe: "+err.Error())
			return
		}
		sv.u.Notify("rave-mate", "Probe ok ("+fmt.Sprintf("%d", len(raw))+" bytes) - ffprobe present")
	}()
}

// ── Favorites section ────────────────────────────────────────────────────────

func (sv *studioView) favoritesSection() fyne.CanvasObject {
	marks := sv.marks.List()
	list := widget.NewList(
		func() int { return len(marks) },
		func() fyne.CanvasObject { return newFileRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(marks) {
				e := fileEntry{name: marks[id].Label, path: marks[id].Path, isDir: true, kind: "dir"}
				sv.fillFileRow(o, e)
				setRowMenu(o, func(pos fyne.Position, obj fyne.CanvasObject) {
					showFileMenu(sv.u, sv.rowMenuCtx(e, func() { sv.showSection("Favorites") }), pos, obj)
				})
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(marks) {
			list.UnselectAll()
			sv.showSection("Browse")
			sv.navigate(marks[id].Path)
		}
	}
	head := container.NewVBox(mutedLabel("Pinned folders. Click to open in Browse."), widget.NewSeparator())
	return container.NewBorder(head, nil, nil, nil, list)
}

// ── Collection section ───────────────────────────────────────────────────────

func (sv *studioView) collectionSection() fyne.CanvasObject {
	importBtn := newKitButtonWithIcon("Import / Refresh", theme.DownloadIcon(), sv.doImportMenu)
	importBtn.SetVariant(kitBtnBrand)
	backup := newKitButtonWithIcon("Backup", theme.MediaReplayIcon(), sv.doBackup)
	scan := newKitButtonWithIcon("Scan missing", theme.SearchIcon(), sv.doScanMissing)
	cleanup := newKitButtonWithIcon("Clean up", theme.DeleteIcon(), sv.doCleanupMissing)
	reloc := newKitButtonWithIcon("Relocate…", theme.ContentRedoIcon(), sv.doRelocate)
	exp := newKitButtonWithIcon("Export / Convert…", theme.DocumentSaveIcon(), sv.doExportMenu)
	sync := newKitButtonWithIcon("Sync…", theme.ViewRefreshIcon(), sv.doSyncMenu)

	if sv.collSearch == nil {
		sv.collSearch = newEntry()
		sv.collSearch.SetPlaceHolder("Search title / artist…")
		sv.collSearch.OnChanged = func(string) { sv.refreshColl() }
		sv.collList = widget.NewList(
			func() int { return len(sv.collShown) },
			func() fyne.CanvasObject { return newTrackColsRow() },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				if id < 0 || id >= len(sv.collShown) {
					return
				}
				t := sv.tracks[sv.collShown[id]]
				fillTrackCols(o, t, pathOnDisk(t.Path), sv.keyRef)
				// re-enable the row checkbox: multi-select tracks → add to a playlist
				check, _, _, _, _, _, _, _, _, _, _ := trackColsParts(o)
				check.OnChanged = nil
				check.SetChecked(sv.collSel[t.Path])
				check.Show()
				check.OnChanged = func(on bool) { sv.toggleCollSel(t.Path, on) }
			},
		)
		sv.collBar = sv.buildCollBar()
		sv.collList.OnSelected = func(id widget.ListItemID) {
			if id >= 0 && id < len(sv.collShown) {
				sv.collList.UnselectAll()
				t := sv.tracks[sv.collShown[id]]
				sv.selectTrack(t, pathOnDisk(t.Path))
			}
		}
		sv.applyCollFilter()
	}
	keyChip := sv.keyFilterChip(sv.tracks, sv.collKeySel, func() {
		sv.refreshColl()
	})
	genreChip := sv.multiSelectChip("Genre", distinctGenreFamilies(sv.tracks), sv.collGenreSel, sv.refreshColl)
	labelChip := sv.multiSelectChip("Label", distinctLabels(sv.tracks), sv.collLabelSel, sv.refreshColl)
	sortOpts := []string{"Artist", "Title", "BPM", "Key", "Genre", "Label", "Rating", "Plays"}
	sv.collSortRow = Segmented(sortOpts, sv.collSortBy, func(s string) {
		sv.collSortBy = s
		sv.refreshColl()
	})
	var dirBtn *kitButton
	dirBtn = newKitButton(sortDirLabel(sv.collSortDesc), func() {
		sv.collSortDesc = !sv.collSortDesc
		dirBtn.SetText(sortDirLabel(sv.collSortDesc))
		sv.refreshColl()
	})
	clearAll := newKitButtonWithIcon("Clear", theme.CancelIcon(), func() {
		sv.collSearch.SetText("")
		clear(sv.collKeySel)
		clear(sv.collGenreSel)
		clear(sv.collLabelSel)
		sv.showSection("Collection") // rebuild head so chip counts reset
	})
	head := container.NewVBox(
		WrapActions(importBtn, backup, scan, cleanup, reloc, exp, sync),
		mutedLabel("Import from Traktor / Rekordbox / VirtualDJ, then convert between them (cues + beatgrid preserved). Read-only - exports write new files. Tick rows to add tracks to a playlist."),
		container.NewBorder(nil, nil, nil, WrapActions(genreChip, labelChip, keyChip, clearAll), sv.collSearch),
		container.NewBorder(nil, nil, smallCaps("SORT"), dirBtn, sv.collSortRow),
		newTrackColsHeader(),
		widget.NewSeparator(),
	)
	sv.applyCollFilter() // key filter may have changed since the last render (wheel tap)
	return container.NewBorder(head, sv.collBar, nil, nil, sv.collList)
}

func (sv *studioView) applyCollFilter() {
	q := strings.ToLower(strings.TrimSpace(sv.collSearch.Text))
	sv.collShown = sv.collShown[:0]
	for i, t := range sv.tracks {
		if q != "" && !strings.Contains(strings.ToLower(t.Title), q) && !strings.Contains(strings.ToLower(t.Artist), q) {
			continue
		}
		if !keyMatches(t.Key, sv.collKeySel) {
			continue
		}
		if !valueInFilter(musiclib.GenreFamily(t.Genre), sv.collGenreSel) {
			continue
		}
		if !valueInFilter(strings.TrimSpace(t.Label), sv.collLabelSel) {
			continue
		}
		sv.collShown = append(sv.collShown, i)
	}
	sv.sortCollShown()
}

// refreshColl re-applies the collection filter+sort and repaints from the top. A reorder must
// re-bind every visible row, and Fyne's List keeps its scroll offset across Refresh - so without
// ScrollToTop a fresh sort can leave stale (mid-scroll) rows on screen.
func (sv *studioView) refreshColl() {
	sv.applyCollFilter()
	if sv.collList != nil {
		// ScrollToTop only on a laid-out list. Segmented() fires onChanged during construction
		// (before the section is sized), and ScrollToTop on an unrendered list deref's a nil
		// scroller in the native renderer → hard crash. A fresh list already lays out at the top.
		if sv.collList.Size().Height > 0 {
			sv.collList.ScrollToTop()
		}
		sv.collList.Refresh()
	}
}

// sortCollShown orders the filtered collection view by the selected field + direction. Stable, so
// equal keys keep import order. Empty collSortBy preserves raw import order.
func (sv *studioView) sortCollShown() {
	sortIndicesBy(sv.collShown, func(i int) musiclib.Track { return sv.tracks[i] }, sv.collSortBy, sv.collSortDesc)
}

// sortIndicesBy stable-sorts shown (indices into a backing slice reached via get) by lessTrack
// field+direction. Empty by leaves the natural order untouched. Shared by Collection, Playlist
// and History track tables.
func sortIndicesBy(shown []int, get func(int) musiclib.Track, by string, desc bool) {
	if by == "" {
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

// trackSortBar builds a "[SORT <segmented> | ↑Asc/↓Desc]" row for a track table. natural is the
// label for unsorted order (e.g. "Manual" / "Play order") - picking it clears the sort field.
// setBy stores the chosen field (""=natural) and toggleDesc flips + returns the new direction;
// both should re-apply the view and repaint.
func (sv *studioView) trackSortBar(opts []string, natural, current string, desc bool, setBy func(string), toggleDesc func() bool) fyne.CanvasObject {
	sel := current
	if sel == "" {
		sel = natural
	}
	seg := Segmented(opts, sel, func(s string) {
		if s == natural {
			setBy("")
		} else {
			setBy(s)
		}
	})
	var dirBtn *kitButton
	dirBtn = newKitButton(sortDirLabel(desc), func() { dirBtn.SetText(sortDirLabel(toggleDesc())) })
	return container.NewBorder(nil, nil, smallCaps("SORT"), dirBtn, seg)
}

// lessTrack is the ascending track comparator for field by; ties fall back to artist/title so the
// order is deterministic.
func lessTrack(a, b musiclib.Track, by string) bool {
	switch by {
	case "Title":
		if c := ciCmp(a.Title, b.Title); c != 0 {
			return c < 0
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "BPM":
		if a.BPM != b.BPM {
			return a.BPM < b.BPM
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "Key":
		ka, oka := musiclib.ParseKey(a.Key)
		kb, okb := musiclib.ParseKey(b.Key)
		if oka != okb {
			return oka // parseable keys sort before unparseable/empty
		}
		if oka && okb {
			if ka.Num != kb.Num {
				return ka.Num < kb.Num
			}
			if ka.Minor != kb.Minor {
				return ka.Minor // A-ring (minor) before B-ring (major)
			}
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "Genre":
		if c := ciCmp(musiclib.GenreFamily(a.Genre), musiclib.GenreFamily(b.Genre)); c != 0 {
			return c < 0 // cluster related genres (families) together
		}
		if c := ciCmp(a.Genre, b.Genre); c != 0 {
			return c < 0
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "Label":
		if c := ciCmp(a.Label, b.Label); c != 0 {
			return c < 0
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "Rating":
		if a.Rating != b.Rating {
			return a.Rating < b.Rating
		}
		return ciCmp(a.Artist, b.Artist) < 0
	case "Plays":
		if a.PlayCount != b.PlayCount {
			return a.PlayCount < b.PlayCount
		}
		return ciCmp(a.Artist, b.Artist) < 0
	default: // Artist
		if c := ciCmp(a.Artist, b.Artist); c != 0 {
			return c < 0
		}
		return ciCmp(a.Title, b.Title) < 0
	}
}

// ciCmp compares two strings case-insensitively (-1/0/1).
func ciCmp(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }

// sortDirLabel renders the sort-direction toggle: ascending vs descending.
func sortDirLabel(desc bool) string {
	if desc {
		return "↓ Desc"
	}
	return "↑ Asc"
}

// doImportMenu lets the user pick which DJ software to import from.
func (sv *studioView) doImportMenu() {
	win := currentWindow()
	if win == nil {
		return
	}
	traktor := widget.NewButtonWithIcon("Traktor (auto-detect)", theme.MediaMusicIcon(), func() { sv.importTraktor() })
	rekordbox := widget.NewButtonWithIcon("Rekordbox (auto-detect)", theme.MediaMusicIcon(), func() { sv.importRekordboxAuto() })
	rekordboxUSB := widget.NewButtonWithIcon("Rekordbox USB (export.pdb)", theme.StorageIcon(), func() { sv.importRekordboxPDB() })
	rekordboxDB := widget.NewButtonWithIcon("Rekordbox library + history (master.db)", theme.HistoryIcon(), func() { sv.importRekordboxMasterDB() })
	rekordboxFile := widget.NewButtonWithIcon("Rekordbox XML…", theme.FolderOpenIcon(), func() { sv.importFromFile(musiclib.FormatRekordbox, "Rekordbox") })
	virtualdj := widget.NewButtonWithIcon("VirtualDJ database.xml…", theme.FolderOpenIcon(), func() { sv.importFromFile(musiclib.FormatVirtualDJ, "VirtualDJ") })
	body := container.NewVBox(
		mutedLabel("Import a DJ library into rave-mate (read-only). Traktor + Rekordbox auto-detect their exported library; USB reads a device export.pdb; master.db reads the live library + play history (newer Rekordbox needs a one-time key - see notice)."),
		traktor, rekordbox, rekordboxUSB, rekordboxDB, rekordboxFile, virtualdj,
	)
	d := dialog.NewCustom("Import library", "Close", body, win)
	d.Resize(fyne.NewSize(480, 360))
	d.Show()
}

// applyImported stores a freshly parsed track set into the collection panes + persists it to
// the relational library DB (so the next launch loads instantly and a refresh syncs only the
// delta). Called from an import goroutine, so the DB write runs off the UI thread.
func (sv *studioView) applyImported(src musiclib.Source, tracks []musiclib.Track, install musiclib.TraktorInstall) {
	sv.persistTracks(src, tracks)
	byPath := make(map[string]musiclib.Track, len(tracks))
	for _, t := range tracks {
		byPath[t.Path] = t
	}
	fyne.Do(func() {
		sv.install, sv.tracks, sv.byPath, sv.loaded = install, tracks, byPath, true
		if sv.collList != nil {
			sv.applyCollFilter()
			sv.collList.Refresh()
		}
		label := src.App
		if src.Version != "" {
			label += " " + src.Version
		}
		sv.u.Notify("rave-mate", fmt.Sprintf("Imported %d tracks (%s).", len(tracks), label))
	})
}

func (sv *studioView) importTraktor() {
	sv.u.Notify("rave-mate", "Importing Traktor library…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-import", false)
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].Collection == "" {
			sv.u.Notify("rave-mate", "No Traktor collection found.")
			return
		}
		in := installs[0]
		f, err := os.Open(in.Collection)
		if err != nil {
			sv.u.Notify("rave-mate", "Open: "+err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		var tracks []musiclib.Track
		if _, err := musiclib.ParseCollection(f, func(t musiclib.Track) { tracks = append(tracks, t) }); err != nil {
			sv.u.Notify("rave-mate", "Parse: "+err.Error())
			return
		}
		src := musiclib.Source{App: "traktor", Version: in.Version, Path: in.Collection}
		sv.applyImported(src, tracks, in)
		// second pass for the (tiny) PLAYLISTS section - the collection stream is consumed
		if pf, err := os.Open(in.Collection); err == nil {
			pls, perr := musiclib.ParseNMLPlaylists(pf)
			_ = pf.Close()
			if perr == nil {
				sv.persistPlaylists(src, pls)
			}
		}
	}()
}

// importRekordboxAuto discovers the newest exported Rekordbox XML in the default Pioneer
// dir and imports its tracks + playlists. (Live play history comes from master.db/PDB, not
// the XML.) Falls back to a notice when nothing is found - the user can pick a file instead.
func (sv *studioView) importRekordboxAuto() {
	sv.u.Notify("rave-mate", "Importing Rekordbox library…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-import-rb", false)
		installs, err := musiclib.DiscoverRekordbox()
		if err != nil || len(installs) == 0 {
			sv.u.Notify("rave-mate", "No exported Rekordbox XML found. In Rekordbox: File → Export Collection in xml format, or use “Rekordbox XML…”.")
			return
		}
		in := installs[0]
		f, err := os.Open(in.XML)
		if err != nil {
			sv.u.Notify("rave-mate", "Open: "+err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		lib, err := musiclib.Import(musiclib.FormatRekordbox, f)
		if err != nil {
			sv.u.Notify("rave-mate", "Parse: "+err.Error())
			return
		}
		lib.Source.Path = in.XML
		sv.applyImported(lib.Source, lib.Tracks, musiclib.TraktorInstall{})
		if len(lib.Playlists) > 0 {
			sv.persistPlaylists(lib.Source, lib.Playlists)
		}
	}()
}

// importRekordboxPDB scans mounted USB devices for PIONEER/rekordbox/export.pdb and imports
// its tracks + playlists + history sets (device-relative paths absolutized to the drive root).
func (sv *studioView) importRekordboxPDB() {
	sv.u.Notify("rave-mate", "Scanning USB devices for a Rekordbox export…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-import-pdb", false)
		roots, pdbs := musiclib.DiscoverRekordboxPDB()
		if len(pdbs) == 0 {
			sv.u.Notify("rave-mate", "No Rekordbox USB export found (PIONEER/rekordbox/export.pdb on a connected drive).")
			return
		}
		data, err := os.ReadFile(pdbs[0])
		if err != nil {
			sv.u.Notify("rave-mate", "Read: "+err.Error())
			return
		}
		lib, err := musiclib.ParseRekordboxPDB(data, roots[0])
		if err != nil {
			sv.u.Notify("rave-mate", "PDB parse: "+err.Error())
			return
		}
		lib.Source.Path = pdbs[0]
		sv.applyImported(lib.Source, lib.Tracks, musiclib.TraktorInstall{})
		if len(lib.Playlists) > 0 {
			sv.persistPlaylists(lib.Source, lib.Playlists)
		}
		if len(lib.Sessions) > 0 {
			sv.persistSessions("rekordbox", pdbs[0], lib.Sessions)
			sums := make([]musiclib.SessionSummary, len(lib.Sessions))
			for i, s := range lib.Sessions {
				sums[i] = musiclib.Summarize(s)
			}
			fyne.Do(func() {
				sv.sessions, sv.summaries = lib.Sessions, sums
				if sv.sessList != nil {
					sv.sessList.Refresh()
				}
			})
		}
	}()
}

// importRekordboxMasterDB decrypts the live Rekordbox master.db (SQLCipher) and imports its
// tracks + playlists + play-history sessions (with per-track timestamps). Newer Rekordbox uses
// a per-install key; on a key mismatch it tells the user how to supply RAVE_REKORDBOX_KEY.
func (sv *studioView) importRekordboxMasterDB() {
	sv.u.Notify("rave-mate", "Reading Rekordbox master.db…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-import-rbdb", false)
		dbs := rekordboxdb.DiscoverRekordboxMasterDB()
		if len(dbs) == 0 {
			sv.u.Notify("rave-mate", "No Rekordbox master.db found (Pioneer/rekordbox/master.db).")
			return
		}
		lib, err := rekordboxdb.Open(dbs[0], "")
		if err != nil {
			sv.u.svc.Log.Warn("library", "rekordbox master.db", map[string]any{"error": err.Error(), "path": dbs[0]})
			sv.u.Notify("rave-mate", "master.db: "+err.Error())
			return
		}
		lib.Source.Path = dbs[0]
		sv.applyImported(lib.Source, lib.Tracks, musiclib.TraktorInstall{})
		if len(lib.Playlists) > 0 {
			sv.persistPlaylists(lib.Source, lib.Playlists)
		}
		if len(lib.Sessions) > 0 {
			sv.persistSessions("rekordbox", dbs[0], lib.Sessions)
			sums := make([]musiclib.SessionSummary, len(lib.Sessions))
			for i, s := range lib.Sessions {
				sums[i] = musiclib.Summarize(s)
			}
			fyne.Do(func() {
				sv.sessions, sv.summaries = lib.Sessions, sums
				if sv.sessList != nil {
					sv.sessList.Refresh()
				}
			})
		}
	}()
}

// importFromFile prompts for a library file and imports it via the format dispatcher.
func (sv *studioView) importFromFile(format musiclib.Format, label string) {
	win := currentWindow()
	if win == nil {
		return
	}
	showFileOpen(win, func(rc fyne.URIReadCloser, _ error) {
		if rc == nil {
			return
		}
		path := rc.URI().Path()
		go func() {
			defer debuglog.Recover(sv.u.svc.Log, "studio-import-file", false)
			defer func() { _ = rc.Close() }()
			lib, err := musiclib.Import(format, rc)
			if err != nil {
				sv.u.Notify("rave-mate", label+" import: "+err.Error())
				return
			}
			lib.Source.Path = path
			sv.applyImported(lib.Source, lib.Tracks, musiclib.TraktorInstall{})
			if len(lib.Playlists) > 0 {
				sv.persistPlaylists(lib.Source, lib.Playlists)
			}
		}()
	})
}

// ── History section ──────────────────────────────────────────────────────────

func (sv *studioView) historySection() fyne.CanvasObject {
	loadBtn := widget.NewButtonWithIcon("Load history", theme.HistoryIcon(), sv.doLoadHistory)
	loadBtn.Importance = widget.HighImportance
	if sv.sessList == nil {
		sv.sessList = widget.NewList(
			func() int { return len(sv.summaries) },
			func() fyne.CanvasObject { l := widget.NewLabel(""); l.Truncation = fyne.TextTruncateEllipsis; return l },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				if id >= 0 && id < len(sv.summaries) {
					s := sv.summaries[id]
					o.(*widget.Label).SetText(fmt.Sprintf("%s · %d tracks · %s", s.StartedAt.Format("2006-01-02 15:04"), s.TrackCount, fmtDur(s.TotalDurationSec)))
				}
			},
		)
		sv.sessList.OnSelected = func(id widget.ListItemID) {
			if id >= 0 && id < len(sv.sessions) {
				sv.openSession(sv.sessions[id])
			}
		}
		sv.playList = widget.NewList(
			func() int { return len(sv.playShown) },
			func() fyne.CanvasObject { return newFileRow() },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				if id >= 0 && id < len(sv.playShown) {
					p := sv.played[sv.playShown[id]]
					fillTrackRow(o, p.track, p.onDisk, sv.keyRef)
				}
			},
		)
		sv.playList.OnSelected = func(id widget.ListItemID) {
			if id >= 0 && id < len(sv.playShown) {
				sv.playList.UnselectAll()
				p := sv.played[sv.playShown[id]]
				sv.selectTrack(p.track, p.onDisk)
			}
		}
	}
	left := container.NewBorder(container.NewVBox(mutedLabel("Your DJ sets, newest first."), loadBtn, widget.NewSeparator()), nil, nil, nil, sv.sessList)
	keyChip := sv.keyFilterChip(sv.playedTracks(), sv.playKeySel, sv.refreshPlayed)
	sortBar := sv.trackSortBar(playSortOpts, "Play order", sv.playSortBy, sv.playSortDesc,
		func(by string) { sv.playSortBy = by; sv.refreshPlayed() },
		func() bool { sv.playSortDesc = !sv.playSortDesc; sv.refreshPlayed(); return sv.playSortDesc })
	rightHead := container.NewVBox(
		container.NewBorder(nil, nil, boldLabel("Played tracks"), keyChip, nil),
		sortBar, widget.NewSeparator(),
	)
	right := container.NewBorder(rightHead, nil, nil, nil, sv.playList)
	return container.New(newAdaptiveSplit(0.42), left, right)
}

func (sv *studioView) doLoadHistory() {
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-history", false)
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].HistoryDir == "" {
			sv.u.Notify("rave-mate", "No Traktor history found.")
			return
		}
		sessions, err := musiclib.LoadSessions(installs[0].HistoryDir)
		if err != nil {
			sv.u.Notify("rave-mate", "History: "+err.Error())
			return
		}
		sv.persistSessions("traktor", installs[0].Collection, sessions)
		sums := make([]musiclib.SessionSummary, len(sessions))
		for i, s := range sessions {
			sums[i] = musiclib.Summarize(s)
		}
		fyne.Do(func() { sv.sessions, sv.summaries = sessions, sums; sv.sessList.Refresh() })
	}()
}

func (sv *studioView) openSession(s musiclib.Session) {
	rows := make([]resolvedPlay, 0, len(s.Played))
	for _, p := range s.Played {
		t, ok := sv.byPath[p.Path]
		if !ok {
			t = musiclib.Track{Path: p.Path, Title: baseName(p.Path)}
		}
		rows = append(rows, resolvedPlay{track: t, onDisk: pathOnDisk(p.Path)})
	}
	sv.played = rows
	sv.refreshPlayed()
}

// playSortOpts: "Play order" = chronological (as played); the rest map to lessTrack fields.
var playSortOpts = []string{"Play order", "Artist", "Title", "BPM", "Key", "Genre", "Rating", "Plays"}

// playedTracks projects the played rows to their tracks (for the key-filter chip's key census).
func (sv *studioView) playedTracks() []musiclib.Track {
	out := make([]musiclib.Track, len(sv.played))
	for i, p := range sv.played {
		out[i] = p.track
	}
	return out
}

// applyPlayView rebuilds the played-tracks view indices (key filter + sort).
func (sv *studioView) applyPlayView() {
	sv.playShown = sv.playShown[:0]
	for i, p := range sv.played {
		if keyMatches(p.track.Key, sv.playKeySel) {
			sv.playShown = append(sv.playShown, i)
		}
	}
	sortIndicesBy(sv.playShown, func(i int) musiclib.Track { return sv.played[i].track }, sv.playSortBy, sv.playSortDesc)
}

// refreshPlayed re-applies the played-tracks key filter + sort and repaints the list.
func (sv *studioView) refreshPlayed() {
	sv.applyPlayView()
	if sv.playList != nil {
		sv.playList.Refresh()
	}
}

// ── management actions (backup/scan/relocate/export) ─────────────────────────

func (sv *studioView) doBackup() {
	if sv.install.Collection == "" {
		sv.u.Notify("rave-mate", "Import a library first.")
		return
	}
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-backup", false)
		root, _ := config.DataPath("library-backups")
		bk, err := musiclib.BackupCollection(sv.install, root)
		if err != nil {
			sv.u.Notify("rave-mate", "Backup failed: "+err.Error())
			return
		}
		sv.u.Notify("rave-mate", "Backed up to "+bk.Path)
	}()
}

func (sv *studioView) doScanMissing() {
	if len(sv.tracks) == 0 {
		sv.u.Notify("rave-mate", "Import a library first.")
		return
	}
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-scan", false)
		_, missing := musiclib.ScanMissing(sv.tracks)
		sv.u.Notify("rave-mate", fmt.Sprintf("%d of %d files missing on disk.", len(missing), len(sv.tracks)))
	}()
}

// doCleanupMissing removes collection tracks (and their playlist references) whose files are gone
// from disk. User-triggered, never automatic; takes a full backup (source collection + library DB
// + settings) before deleting anything, and confirms with the exact counts first.
func (sv *studioView) doCleanupMissing() {
	if len(sv.tracks) == 0 {
		sv.u.Notify("rave-mate", "Import a library first.")
		return
	}
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-cleanup", false)
		_, missingAll := musiclib.ScanMissing(sv.tracks)
		// Only remove tracks that HAVE a local path which is now gone. A track with no path is a
		// streaming/remote or privacy-purged entry, not a moved-file - never delete those here.
		missing := make([]musiclib.Track, 0, len(missingAll))
		pathless := 0
		for _, t := range missingAll {
			if strings.TrimSpace(t.Path) == "" {
				pathless++
				continue
			}
			missing = append(missing, t)
		}
		fyne.Do(func() {
			win := currentWindow()
			if win == nil {
				return
			}
			if len(missing) == 0 {
				dialog.ShowInformation("Clean up collection", "No missing files - nothing to remove.", win)
				return
			}
			note := ""
			if pathless > 0 {
				note = fmt.Sprintf("\n\n(%d entries with no local path - streaming/remote - are left untouched.)", pathless)
			}
			srcNote := ""
			if sv.resolveTraktorInstall().Collection != "" {
				srcNote = "\n\nThe source Traktor collection.nml is pruned too (so a re-import won't re-add them) - close Traktor first, or it will overwrite the change on exit."
			}
			msg := fmt.Sprintf("%d of %d tracks point at local files that no longer exist on disk.\n\n"+
				"Removing them also strips those tracks from any playlists and deletes playlists left empty.\n\n"+
				"A full backup (collection + library database + settings) is taken first. Continue?%s%s",
				len(missing), len(sv.tracks), note, srcNote)
			dialog.ShowConfirm("Clean up collection", msg, func(ok bool) {
				if ok {
					sv.runCleanup()
				}
			}, win)
		})
	}()
}

// resolveTraktorInstall returns the install imported THIS session, or - when the library was loaded
// from the DB on launch (sv.install zero) - auto-detects the newest Traktor collection the SAME way
// importTraktor does, so cleanup prunes the exact collection.nml a re-import would read.
func (sv *studioView) resolveTraktorInstall() musiclib.TraktorInstall {
	if sv.install.Collection != "" {
		return sv.install
	}
	if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
		return installs[0]
	}
	return musiclib.TraktorInstall{}
}

// runCleanup backs up, deletes the missing tracks + their playlist refs from the DB AND prunes them
// from the source collection.nml (so a re-import won't re-add them), then reloads the panes.
func (sv *studioView) runCleanup() {
	sv.u.Notify("rave-mate", "Backing up + cleaning up…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-cleanup-run", false)
		root, _ := config.DataPath("library-backups")
		// settings backup (best-effort; the core handles collection.nml + DB).
		if cfgPath, err := config.DataPath("config.json"); err == nil {
			if data, rerr := os.ReadFile(cfgPath); rerr == nil {
				_ = os.MkdirAll(root, 0o755)
				_ = os.WriteFile(filepath.Join(root, "config-"+time.Now().Format("20060102-150405")+".json"), data, 0o600)
			}
		}
		col := sv.resolveTraktorInstall().Collection
		rep, err := maintenance.CleanupMissing(sv.u.svc.Lib, col, root)
		if err != nil {
			sv.u.Notify("rave-mate", "Cleanup failed (aborted before any change): "+err.Error())
			return
		}
		nmlNote := ""
		switch {
		case col == "":
			nmlNote = " (no source collection.nml found - DB only; a re-import may re-add them)"
		case rep.NMLError != "":
			nmlNote = " (source collection NOT pruned: " + rep.NMLError + ")"
			sv.u.svc.Log.Warn("studio-cleanup", "prune collection.nml failed", map[string]any{"err": rep.NMLError})
		default:
			nmlNote = fmt.Sprintf(" (source collection.nml pruned: %d tracks, %d playlist refs)", rep.NMLTracksRemoved, rep.NMLPlaylistRefsRemvd)
		}
		sv.loadFromDB() // reloads sv.tracks/byPath from the DB + refreshes the collection list
		fyne.Do(func() {
			sv.refreshPlaylists()
			sv.u.Notify("rave-mate", fmt.Sprintf("Cleaned up: %d tracks, %d playlist entries, %d empty playlists removed (backup → %s).%s",
				rep.TracksDeleted, rep.PlaylistEntriesDel, rep.EmptyPlaylistsDel, root, nmlNote))
		})
	}()
}

// doExportMenu lets the user convert the imported library to another DJ format.
func (sv *studioView) doExportMenu() {
	if len(sv.tracks) == 0 {
		sv.u.Notify("rave-mate", "Import a library first.")
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	mk := func(label string, format musiclib.Format, ext string) *widget.Button {
		return widget.NewButtonWithIcon(label, theme.DocumentSaveIcon(), func() { sv.doExport(format, ext) })
	}
	body := container.NewVBox(
		mutedLabel("Convert the imported library to another DJ software. Writes a NEW file (cues + beatgrid included); your source is never modified."),
		mk("Rekordbox XML", musiclib.FormatRekordbox, "rekordbox.xml"),
		mk("Traktor NML", musiclib.FormatTraktor, "collection.exported.nml"),
		mk("VirtualDJ database.xml", musiclib.FormatVirtualDJ, "virtualdj.database.xml"),
		mk("M3U playlist", musiclib.FormatM3U, "library.m3u8"),
		mk("CSV metadata backup", musiclib.FormatCSV, "library.csv"),
	)
	d := dialog.NewCustom("Export / Convert library", "Close", body, win)
	d.Resize(fyne.NewSize(460, 300))
	d.Show()
}

func (sv *studioView) doExport(format musiclib.Format, outName string) {
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-export", false)
		out, _ := config.DataPath("library-export-" + outName)
		f, err := os.Create(out)
		if err != nil {
			sv.u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		lib := musiclib.Library{Source: musiclib.Source{App: sv.install.Version}, Tracks: sv.tracks}
		if err := musiclib.Export(format, lib, f); err != nil {
			sv.u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		sv.u.Notify("rave-mate", "Exported to "+out)
	}()
}

func (sv *studioView) doRelocate() {
	if len(sv.tracks) == 0 {
		sv.u.Notify("rave-mate", "Import a library first.")
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	_, missing := musiclib.ScanMissing(sv.tracks)
	if len(missing) == 0 {
		dialog.ShowInformation("Relocate", "No missing files.", win)
		return
	}
	rootE := newEntry()
	rootE.SetPlaceHolder(`Folder to search, e.g. D:\Music`)
	result := widget.NewLabel(fmt.Sprintf("%d files missing. Enter a folder to search.", len(missing)))
	result.Wrapping = fyne.TextWrapWord
	var cands []musiclib.Candidate
	find := widget.NewButton("Find candidates", func() {
		root := strings.TrimSpace(rootE.Text)
		if root == "" {
			return
		}
		result.SetText("Indexing…")
		go func() {
			defer debuglog.Recover(sv.u.svc.Log, "relocate-index", false)
			idx, err := musiclib.BuildIndex([]string{root})
			if err != nil {
				fyne.Do(func() { result.SetText("Index error: " + err.Error()) })
				return
			}
			c := musiclib.Relocate(missing, idx)
			fyne.Do(func() {
				cands = c
				result.SetText(fmt.Sprintf("Found %d. Apply backs up then writes a NEW collection - original untouched.", len(c)))
			})
		}()
	})
	apply := widget.NewButton("Apply → backup + write fixed collection", func() {
		if len(cands) == 0 {
			result.SetText("Find candidates first.")
			return
		}
		result.SetText("Backing up + writing…")
		go func() {
			defer debuglog.Recover(sv.u.svc.Log, "relocate-apply", false)
			bkRoot, _ := config.DataPath("library-backups")
			if _, err := musiclib.BackupCollection(sv.install, bkRoot); err != nil {
				fyne.Do(func() { result.SetText("Backup failed (aborted): " + err.Error()) })
				return
			}
			out, _ := config.DataPath("collection.fixed.nml")
			src, err := os.Open(sv.install.Collection)
			if err != nil {
				fyne.Do(func() { result.SetText("Open: " + err.Error()) })
				return
			}
			dst, err := os.Create(out)
			if err != nil {
				_ = src.Close()
				fyne.Do(func() { result.SetText("Create: " + err.Error()) })
				return
			}
			fixed, err := musiclib.WriteFixedCollection(src, musiclib.FixPlan{Fixes: cands}, dst)
			_ = src.Close()
			_ = dst.Close()
			fyne.Do(func() {
				if err != nil {
					result.SetText(fmt.Sprintf("Wrote %d then errored: %s", fixed, err.Error()))
					return
				}
				result.SetText(fmt.Sprintf("Fixed %d → %s (collection.nml unchanged).", fixed, out))
			})
		}()
	})
	apply.Importance = widget.HighImportance
	content := container.NewVBox(mutedLabel("Locate moved files; write a CORRECTED collection to a new file. Your collection.nml is NEVER modified; a backup is taken first."), folderPickerRow(rootE), find, result, apply)
	d := dialog.NewCustom("Relocate missing files", "Close", content, win)
	d.Resize(fyne.NewSize(560, 320))
	d.Show()
}

// ── file row + listing ───────────────────────────────────────────────────────

func newFileRow() fyne.CanvasObject {
	check := widget.NewCheck("", nil)
	icon := widget.NewIcon(theme.FileIcon())
	name := widget.NewLabel("")
	name.Truncation = fyne.TextTruncateEllipsis
	// Right-side metadata must NOT wrap: it sits on the right of a Border, which sizes it to
	// its MinSize width - a wrapping label collapses to one char and renders text vertically.
	sub := widget.NewLabel("")
	sub.Importance = widget.LowImportance
	sub.Wrapping = fyne.TextWrapOff
	// No truncation: in the Border right slot an ellipsizing label's MinSize collapses
	// and renders as "…"; the metadata strings are short, the name label truncates instead.
	kp := newPill("", colSecondary, colMuted, nil) // key pill (harmonic-colored vs selected track)
	kp.Hide()
	// NewBorder = [name(center), left=HBox(check,icon), right=HBox(Center(pill),sub)].
	right := container.NewHBox(container.NewCenter(kp), sub)
	// ctxRow catches ONLY secondary taps (context menu); primary taps still select via the List.
	return newCtxRow(container.NewBorder(nil, nil, container.NewHBox(check, icon), right, name))
}

// fileRowParts unpacks the newFileRow object graph for the fill helpers.
func fileRowParts(o fyne.CanvasObject) (name *widget.Label, check *widget.Check, icon *widget.Icon, kp *pill, sub *widget.Label) {
	if r, ok := o.(*ctxRow); ok {
		o = r.content
	}
	c := o.(*fyne.Container)
	name = c.Objects[0].(*widget.Label)
	left := c.Objects[1].(*fyne.Container)
	check = left.Objects[0].(*widget.Check)
	icon = left.Objects[1].(*widget.Icon)
	right := c.Objects[2].(*fyne.Container)
	kp = right.Objects[0].(*fyne.Container).Objects[0].(*pill)
	sub = right.Objects[1].(*widget.Label)
	return
}

// setKeyPill styles a row's key pill: harmonic-relation color vs the reference key
// (selected track), neutral when dissonant/unparsable, hidden when the track has no key.
func setKeyPill(kp *pill, keyText string, ref *musiclib.Key) {
	keyText = strings.TrimSpace(keyText)
	if keyText == "" {
		kp.Hide()
		return
	}
	if k, ok := musiclib.ParseKey(keyText); ok {
		bg, fg := keyPillColors(k, ref)
		kp.setPill(keyLabel(k), bg, fg)
	} else {
		kp.setPill(keyText, colSecondary, colMuted)
	}
	kp.Show()
}

// fillFileRow updates a row + wires its batch-select checkbox (files only, not dirs).
// Audio files cross-referenced in the collection get the key pill.
func (sv *studioView) fillFileRow(o fyne.CanvasObject, e fileEntry) {
	name, check, icon, kp, sub := fileRowParts(o)
	name.SetText(e.name)
	icon.SetResource(kindIcon(e.kind, e.isDir))
	kp.Hide()
	if e.isDir {
		sub.SetText("")
		check.OnChanged = nil
		check.SetChecked(false)
		check.Hide()
	} else {
		sub.SetText(humanBytes(e.size) + " · " + e.mod.Format("02/01/2006"))
		check.Show()
		check.OnChanged = nil // avoid firing during programmatic SetChecked
		_, sel := sv.batchSel[e.path]
		check.SetChecked(sel)
		check.OnChanged = func(on bool) { sv.toggleBatch(e, on) }
		if t, ok := sv.byPath[e.path]; ok {
			setKeyPill(kp, t.Key, sv.keyRef)
		}
	}
}

func fillTrackRow(o fyne.CanvasObject, t musiclib.Track, onDisk bool, ref *musiclib.Key) {
	name, check, icon, kp, sub := fileRowParts(o)
	check.OnChanged = nil // collection tracks aren't batch-selectable
	check.SetChecked(false)
	check.Hide()
	name.SetText(fmt.Sprintf("%s - %s", strOrDash(t.Artist), strOrDash(t.Title)))
	// Dense, Winamp-style meta column: pack the high-signal fields the DJ scans by.
	var parts []string
	if t.BPM > 0 {
		parts = append(parts, fmt.Sprintf("%.0f BPM", t.BPM))
	}
	if t.DurationSec > 0 {
		parts = append(parts, fmtTrackDur(t.DurationSec))
	}
	if t.Genre != "" {
		parts = append(parts, t.Genre)
	}
	if t.BitrateBps > 0 {
		parts = append(parts, fmt.Sprintf("%dk", t.BitrateBps/1000))
	}
	sub.SetText(strings.Join(parts, " · "))
	setKeyPill(kp, t.Key, ref)
	if onDisk {
		icon.SetResource(theme.MediaMusicIcon())
	} else {
		icon.SetResource(theme.WarningIcon())
	}
}

// fmtTrackDur renders seconds as m:ss for compact track-duration columns (vs fmtDur which is
// coarse "Xm"/"Xh YYm" for long spans).
func fmtTrackDur(sec float64) string {
	if sec <= 0 {
		return ""
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func kindIcon(kind string, isDir bool) fyne.Resource {
	if isDir {
		return theme.FolderIcon()
	}
	switch kind {
	case "video":
		return theme.MediaVideoIcon()
	case "audio":
		return theme.MediaMusicIcon()
	case "image":
		return theme.FileImageIcon()
	default:
		return theme.FileIcon()
	}
}

var (
	videoExt = set(".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".wmv", ".flv", ".mj2")
	audioExt = set(".mp3", ".wav", ".aac", ".m4a", ".flac", ".opus", ".ogg", ".aiff")
	imageExt = set(".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp")
)

func listDir(dir string) ([]fileEntry, error) {
	ds, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(ds))
	for _, d := range ds {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, d.Name())
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		isDir := fi.IsDir()
		out = append(out, fileEntry{
			name: d.Name(), path: full, isDir: isDir, size: fi.Size(), mod: fi.ModTime(),
			kind: classify(isDir, strings.ToLower(filepath.Ext(d.Name()))),
		})
	}
	return out, nil
}

func classify(isDir bool, ext string) string {
	switch {
	case isDir:
		return "dir"
	case videoExt[ext]:
		return "video"
	case audioExt[ext]:
		return "audio"
	case imageExt[ext]:
		return "image"
	default:
		return "other"
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func lowOrHigh(active bool) widget.Importance {
	if active {
		return widget.HighImportance
	}
	return widget.LowImportance
}

// outlineOrBrand is lowOrHigh for kitButton variants (active chip → brand fill).
func outlineOrBrand(active bool) kitButtonVariant {
	if active {
		return kitBtnBrand
	}
	return kitBtnOutline
}

type segpart struct{ label, path string }

func splitPathSegs(p string) []segpart {
	p = filepath.Clean(p)
	parts := strings.Split(p, string(filepath.Separator))
	var segs []segpart
	acc := ""
	for _, part := range parts {
		if part == "" {
			if acc == "" {
				acc = string(filepath.Separator)
				segs = append(segs, segpart{string(filepath.Separator), acc})
			}
			continue
		}
		if acc == "" || acc == string(filepath.Separator) {
			acc += part
		} else {
			acc += string(filepath.Separator) + part
		}
		segs = append(segs, segpart{part, acc})
	}
	if len(segs) == 0 {
		segs = append(segs, segpart{p, p})
	}
	return segs
}

func humanBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func boldLabel(s string) *widget.Label {
	return widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func smallCaps(s string) *widget.Label {
	l := widget.NewLabel(strings.ToUpper(s))
	l.Importance = widget.LowImportance
	l.TextStyle = fyne.TextStyle{Bold: true}
	return l
}

func pathOnDisk(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func currentWindow() fyne.Window {
	wins := fyne.CurrentApp().Driver().AllWindows()
	if len(wins) == 0 {
		return nil
	}
	return wins[0]
}

func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinDot(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return b
	default:
		return a + " · " + b
	}
}

func fmtDur(secs float64) string {
	m := int(secs) / 60
	if m >= 60 {
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}
