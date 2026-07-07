package ui

// Remote Library: the SAME redesigned shell as the local Library (nav rail · toolstrip ·
// list/grid browse · inspector · status strip), driven over remotectl against a paired
// instance. Sections that genuinely can't run remotely render an explicit disabled panel
// with the reason - never a broken/empty view.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/transcode"
)

const remoteLibPageSize = 100

// remoteStudioView mirrors studioView's chrome against a paired instance.
type remoteStudioView struct {
	u      *UI
	client *remotectl.Client
	ops    mediaOps

	section   string
	body      *fyne.Container
	nav       map[string]*navRailItem
	navBox    *fyne.Container
	navGroups []*widget.Label
	railColl  bool
	inspector *kitInspector
	inspOn    bool
	center    *fyne.Container
	split     fyne.CanvasObject
	status    *kitStatusStrip
	titleLbl  *widget.Label

	// ── browse ──
	cwd          string
	parent       *string
	entries      []fileEntry
	shown        []int
	kindFilter   string
	sortBy       string
	nameFilter   string
	browseList   *widget.List
	browseGrid   *kitDensityGrid
	browseStack  *fyne.Container
	browseView   string
	browseSearch *kitSearchField
	countLbl     *widget.Label
	crumb        *fyne.Container
	chips        *fyne.Container
	browseRoot   fyne.CanvasObject
	defaults     localmedia.DefaultPaths

	// ── collection ──
	collRoot fyne.CanvasObject
	query    string
	offset   int
	total    int
	tracks   []musiclib.Track
	sel      int
	info     *widget.Label
	list     *widget.List
	pageLbl  *widget.Label
	prev     *widget.Button
	next     *widget.Button
}

// buildRemoteLibrary renders the Library shell for a connected peer.
func (u *UI) buildRemoteLibrary(client *remotectl.Client) fyne.CanvasObject {
	v := &remoteStudioView{
		u: u, client: client, ops: remoteOps{client: client},
		section: "Browse", kindFilter: "ALL", sortBy: "Name", sel: -1,
		nav: map[string]*navRailItem{}, body: container.NewStack(),
		inspector: newKitInspector("SELECTED"), inspOn: true,
	}
	v.clearDetail()
	v.status = newKitStatusStrip()
	v.status.SetCenter("Controlling a paired instance")

	topBar := v.buildTopBar()
	rail := v.buildNavRail()
	v.split = container.New(newAdaptiveSplit(0.70), v.body, v.inspector.Object())
	v.center = container.NewStack(v.split)
	v.showSection("Browse")
	return container.NewBorder(
		container.NewVBox(topBar, widget.NewSeparator()),
		v.status.Object(), rail, nil, v.center)
}

// ── chrome (mirrors the local shell) ─────────────────────────────────────────

func (v *remoteStudioView) buildTopBar() fyne.CanvasObject {
	railTgl := newKitIconButton(theme.MenuIcon(), "Collapse / expand the section rail", v.toggleRail)
	v.titleLbl = boldLabel("Browse")
	inspTgl := newKitIconButton(theme.InfoIcon(), "Show / hide the inspector panel", v.toggleInspector)
	left := container.NewHBox(railTgl, kitToolSep(), smallCaps("LIBRARY (PAIRED INSTANCE)"), v.titleLbl)
	bar := container.NewBorder(nil, nil, left, inspTgl)
	bg := canvas.NewRectangle(colSurface)
	return container.NewStack(bg, container.NewPadded(bar))
}

func (v *remoteStudioView) buildNavRail() fyne.CanvasObject {
	mk := func(name, hint string, icon fyne.Resource) *fyne.Container {
		it := &navRailItem{name: name}
		it.icon = newKitIconButton(icon, name+" - "+hint, func() { v.showSection(name) })
		it.label = widget.NewButton(name, func() { v.showSection(name) })
		it.label.Importance = widget.LowImportance
		it.label.Alignment = widget.ButtonAlignLeading
		it.row = container.NewBorder(nil, nil, it.icon, nil, it.label)
		v.nav[name] = it
		return it.row
	}
	group := func(title string, items ...*fyne.Container) *fyne.Container {
		hdr := smallCaps(title)
		v.navGroups = append(v.navGroups, hdr)
		objs := make([]fyne.CanvasObject, 0, len(items)+1)
		objs = append(objs, hdr)
		for _, it := range items {
			objs = append(objs, it)
		}
		return container.NewVBox(objs...)
	}
	v.navBox = container.NewVBox(
		group("FILES",
			mk("Browse", "the paired instance's files", theme.FolderOpenIcon()),
			mk("Favorites", "pinned folders", theme.GridIcon())),
		widget.NewSeparator(),
		group("LIBRARY",
			mk("Collection", "the paired instance's imported DJ tracks", theme.StorageIcon()),
			mk("Playlists", "manual · smart · imported", theme.MenuIcon()),
			mk("History", "played sets", theme.HistoryIcon()),
			mk("ID Marks", "unreleased-track redaction", theme.VisibilityOffIcon())),
		widget.NewSeparator(),
		group("JOBS",
			mk("Queue", "transcode / batch progress", theme.ListIcon()),
			mk("Presets", "encode presets", theme.SettingsIcon())),
	)
	return container.NewVScroll(v.navBox)
}

func (v *remoteStudioView) toggleRail() {
	v.railColl = !v.railColl
	for _, it := range v.nav {
		if v.railColl {
			it.label.Hide()
		} else {
			it.label.Show()
		}
	}
	for _, h := range v.navGroups {
		if v.railColl {
			h.Hide()
		} else {
			h.Show()
		}
	}
	v.navBox.Refresh()
}

func (v *remoteStudioView) toggleInspector() {
	v.inspOn = !v.inspOn
	if v.inspOn {
		v.center.Objects = []fyne.CanvasObject{v.split}
	} else {
		v.center.Objects = []fyne.CanvasObject{v.body}
	}
	v.center.Refresh()
}

func (v *remoteStudioView) showInspector() {
	if !v.inspOn {
		v.toggleInspector()
	}
}

func (v *remoteStudioView) clearDetail() {
	v.inspector.SetHeader("Nothing selected", "Select a file or track to see details + actions.")
	v.inspector.SetSections()
}

func (v *remoteStudioView) showSection(name string) {
	v.section = name
	v.titleLbl.SetText(name)
	for n, it := range v.nav {
		active := n == name
		it.icon.SetActive(active)
		it.label.Importance = lowOrHigh(active)
		it.label.Refresh()
	}
	var o fyne.CanvasObject
	switch name {
	case "Collection":
		o = v.collectionSection()
	case "Favorites":
		o = remoteUnavailable("Favorites", "Pin the current folder",
			"Pinned folders are stored per computer. Pinning folders of a paired instance isn't supported yet - browse its drives from HOME / MUSIC in Browse.")
	case "Playlists":
		o = remoteUnavailable("Playlists", "New playlist",
			"Playlists live in the paired instance's own library and can't be edited remotely yet. Manage them on that computer; the Collection tab here still browses + tags its tracks.")
	case "History":
		o = remoteUnavailable("History", "Import history",
			"Played-set history is stored in the paired instance's library and isn't streamed remotely yet.")
	case "ID Marks":
		o = remoteUnavailable("ID Marks", "Mark file…",
			"ID marks redact a computer's OWN outputs (overlays, stream, now-playing, recorder). Each instance keeps its own list - manage the paired instance's marks on that computer (Library → ID Marks).")
	case "Queue":
		o = remoteUnavailable("Queue", "Cancel job",
			"Transcodes you start here run on the paired instance and finish in place (a 'rave-mate-transcoded' folder beside the source). A live remote job queue isn't streamed yet.")
	case "Presets":
		o = remoteUnavailable("Presets", "New preset",
			"Encode presets are managed on this computer (switch Controlling to 'This computer' → Presets). Your presets are applied when you start a transcode on the paired instance.")
	default:
		o = v.browseSection()
	}
	v.body.Objects = []fyne.CanvasObject{o}
	v.body.Refresh()
	if name == "Browse" {
		v.status.SetLeft(fmt.Sprintf("%d items · %s", len(v.shown), v.cwd))
	} else {
		v.status.SetLeft(name)
	}
}

// remoteUnavailable renders an explicit degraded section: the local action, disabled, plus why.
func remoteUnavailable(title, action, reason string) fyne.CanvasObject {
	btn := newKitButton(action, nil)
	btn.Disable()
	return container.NewCenter(container.NewVBox(
		container.NewHBox(boldLabel(title+" - not available for a paired instance"),
			helpIcon(reason)),
		mutedLabel(reason),
		container.NewHBox(btn),
	))
}

// ── Browse section (remote fs, full local feature set) ───────────────────────

func (v *remoteStudioView) browseSection() fyne.CanvasObject {
	if v.browseRoot != nil {
		return v.browseRoot
	}
	if v.browseView == "" {
		v.browseView = "List"
	}
	v.chips = container.NewHBox()
	v.crumb = container.NewHBox()
	v.browseSearch = newKitSearchField("Filter by name…", func(q string) {
		v.nameFilter = q
		v.applyFilter()
		v.refreshBrowse()
	})
	v.countLbl = mutedInline("0")

	filterRow := newKitSegmented([]string{"ALL", "VIDEO", "AUDIO", "IMAGE", "OTHER"}, v.kindFilter, func(k string) {
		v.kindFilter = k
		v.applyFilter()
		v.refreshBrowse()
	})
	sortRow := newKitSegmented([]string{"Name", "Modified", "Size"}, v.sortBy, func(s string) {
		v.sortBy = s
		v.sortEntries()
		v.applyFilter()
		v.refreshBrowse()
	})

	v.browseList = widget.NewList(
		func() int { return len(v.shown) },
		func() fyne.CanvasObject { return newFileRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(v.shown) {
				return
			}
			e := v.entries[v.shown[id]]
			fillRemoteFileRow(o, e)
			setRowMenu(o, func(pos fyne.Position, obj fyne.CanvasObject) {
				v.showRowMenu(e, pos, obj)
			})
		},
	)
	v.browseList.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(v.shown) {
			return
		}
		e := v.entries[v.shown[id]]
		if e.isDir {
			v.browseList.UnselectAll()
			v.navigate(e.path)
		} else {
			v.selectFile(e)
		}
	}

	v.browseGrid = newKitDensityGrid(150, 132)
	v.browseGrid.OnActivate = func(id string) {
		if e, ok := v.entryByPath(id); ok {
			if e.isDir {
				v.navigate(e.path)
			} else {
				v.selectFile(e)
			}
		}
	}
	v.browseGrid.OnSecondary = func(id string, ev *fyne.PointEvent) {
		if e, ok := v.entryByPath(id); ok {
			v.showRowMenu(e, ev.AbsolutePosition, v.browseGrid)
		}
	}
	v.browseStack = container.NewStack(v.browseList)

	up := newKitIconButton(theme.NavigateBackIcon(), "Up to the parent folder", func() {
		if v.parent != nil {
			v.navigate(*v.parent)
		}
	})
	viewSeg := newKitSegmented([]string{"List", "Grid"}, v.browseView, func(s string) {
		v.browseView = s
		v.showBrowseView()
	})
	filterStrip := kitToolStrip(
		container.NewGridWrap(fyne.NewSize(200, kitSegH), v.browseSearch.Object()),
		kitToolSep(), smallCaps("KIND"), filterRow,
		kitToolSep(), smallCaps("SORT"), sortRow,
		kitToolSep(), smallCaps("VIEW"), viewSeg,
		kitToolSep(), v.countLbl,
	)
	head := container.NewVBox(
		kitToolStrip(up, kitToolSep(), v.crumb),
		v.chips,
		filterStrip,
		widget.NewSeparator(),
	)
	v.browseRoot = container.NewBorder(head, nil, nil, nil, v.browseStack)

	// Start at the peer's Music folder (falling back to Home) + build the quick-access chips.
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		def, err := v.ops.Defaults(ctx)
		start := ""
		if err == nil {
			v.defaults = def
			switch {
			case def.Music != "":
				start = def.Music
			case def.Home != "":
				start = def.Home
			}
		}
		fyne.Do(v.rebuildChips)
		v.navigate(start)
	})
	return v.browseRoot
}

func (v *remoteStudioView) entryByPath(path string) (fileEntry, bool) {
	for _, e := range v.entries {
		if e.path == path {
			return e, true
		}
	}
	return fileEntry{}, false
}

// navigate lists a remote directory off-thread and repaints.
func (v *remoteStudioView) navigate(dir string) {
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		listing, err := v.ops.ListDir(ctx, dir)
		fyne.Do(func() {
			if err != nil {
				v.status.SetLeft("Error: " + err.Error())
				v.u.Notify("rave-mate", "Remote browse failed: "+err.Error())
				return
			}
			if listing.Error != "" {
				v.status.SetLeft(listing.Error)
			}
			v.cwd = listing.Path
			v.parent = listing.Parent
			v.entries = entriesToFileEntries(listing.Entries)
			v.sortEntries()
			v.applyFilter()
			v.rebuildCrumb()
			v.rebuildChips()
			v.refreshBrowse()
			if v.browseList != nil {
				v.browseList.UnselectAll()
			}
		})
	})
}

// refresh re-lists the cwd (post file-op).
func (v *remoteStudioView) refresh() { v.navigate(v.cwd) }

func (v *remoteStudioView) sortEntries() {
	sortFileEntries(v.entries, v.sortBy)
}

func (v *remoteStudioView) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(v.nameFilter))
	v.shown = v.shown[:0]
	for i, e := range v.entries {
		if !e.isDir && v.kindFilter != "ALL" && !strings.EqualFold(e.kind, v.kindFilter) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		v.shown = append(v.shown, i)
	}
	if v.countLbl != nil {
		v.countLbl.SetText(fmt.Sprintf("%d", len(v.shown)))
	}
}

func (v *remoteStudioView) refreshBrowse() {
	if v.browseList != nil {
		v.browseList.Refresh()
	}
	if v.browseGrid != nil && v.browseView == "Grid" {
		v.browseGrid.SetItems(v.gridItems())
	}
	v.status.SetLeft(fmt.Sprintf("%d items · %s", len(v.shown), v.cwd))
}

func (v *remoteStudioView) showBrowseView() {
	if v.browseView == "Grid" {
		v.browseGrid.SetItems(v.gridItems())
		v.browseStack.Objects = []fyne.CanvasObject{v.browseGrid}
	} else {
		v.browseStack.Objects = []fyne.CanvasObject{v.browseList}
	}
	v.browseStack.Refresh()
}

// gridItems projects the filtered view to cards. No thumbnails - image bytes live on the
// paired instance and aren't streamed.
func (v *remoteStudioView) gridItems() []kitGridItem {
	items := make([]kitGridItem, 0, len(v.shown))
	for _, idx := range v.shown {
		e := v.entries[idx]
		sub := "Folder"
		if !e.isDir {
			sub = humanBytes(e.size) + " · " + strings.ToUpper(e.kind)
		}
		items = append(items, kitGridItem{ID: e.path, Title: e.name, Subtitle: sub, Icon: kindIcon(e.kind, e.isDir)})
	}
	return items
}

func (v *remoteStudioView) rebuildCrumb() {
	v.crumb.Objects = v.crumb.Objects[:0]
	segs := splitRemotePathSegs(v.cwd)
	for i, seg := range segs {
		target := seg.path
		v.crumb.Add(newKitButton(seg.label, func() { v.navigate(target) }))
		if i < len(segs)-1 {
			v.crumb.Add(mutedLabel("›"))
		}
	}
	v.crumb.Refresh()
}

func (v *remoteStudioView) rebuildChips() {
	quick := []struct{ label, path string }{
		{"HOME", v.defaults.Home}, {"DESKTOP", v.defaults.Desktop}, {"DOWNLOADS", v.defaults.Downloads},
		{"MUSIC", v.defaults.Music}, {"VIDEOS", v.defaults.Videos}, {"PICTURES", v.defaults.Pictures},
	}
	items := make([]fyne.CanvasObject, 0, len(quick))
	for _, q := range quick {
		if q.path == "" {
			continue
		}
		target := q.path
		b := newKitButton(q.label, func() { v.navigate(target) })
		b.SetVariant(outlineOrBrand(v.cwd == target))
		items = append(items, b)
	}
	v.chips.Objects = []fyne.CanvasObject{WrapActions(items...)}
	v.chips.Refresh()
}

// selectFile fills the inspector for a remote file: parity actions (unavailable ones
// disabled with the reason) + remote transcode + details.
func (v *remoteStudioView) selectFile(e fileEntry) {
	v.showInspector()
	v.inspector.SetHeader(e.name,
		fmt.Sprintf("%s · %s · %s", humanBytes(e.size), strings.ToUpper(e.kind), e.mod.Format("2006-01-02 15:04")))
	secs := []kitSection{{Key: "actions", Title: "ACTIONS", DefaultOpen: true, Content: v.actionBar(e)}}
	if e.kind == "video" || e.kind == "audio" {
		secs = append(secs, kitSection{Key: "encode", Title: "ENCODING / TRANSCODE",
			Help:    "Runs on the paired instance's worker pool. Output goes to a 'rave-mate-transcoded' folder beside the source there - the original is never touched.",
			Content: v.transcodePanel(e)})
	}
	secs = append(secs, kitSection{Key: "details", Title: "DETAILS", Content: v.detailsPanel(e)})
	v.inspector.SetSections(secs...)
}

// actionBar mirrors the local ACTIONS block; playback/reveal need the file locally, so they
// degrade explicitly.
func (v *remoteStudioView) actionBar(e fileEntry) fyne.CanvasObject {
	open := newKitButtonWithIcon("Open", theme.MediaPlayIcon(), nil)
	reveal := newKitButtonWithIcon("Reveal", theme.FolderOpenIcon(), nil)
	probe := newKitButtonWithIcon("Metadata", theme.InfoIcon(), nil)
	open.Disable()
	reveal.Disable()
	probe.Disable()
	cp := newKitButtonWithIcon("Copy path", theme.ContentCopyIcon(), func() {
		fyne.CurrentApp().Clipboard().SetContent(e.path)
	})
	return container.NewVBox(
		mutedLabel("Open / Reveal / Metadata run where the file lives - not available for a paired instance. Rename, move, duplicate and delete are in the right-click menu."),
		container.NewGridWithColumns(2, open, reveal),
		container.NewGridWithColumns(2, probe, cp))
}

func (v *remoteStudioView) detailsPanel(e fileEntry) fyne.CanvasObject {
	row := func(k, val string) fyne.CanvasObject {
		value := widget.NewLabel(strOrDash(val))
		value.Wrapping = fyne.TextWrapBreak
		return container.NewVBox(mutedInline(k), value)
	}
	return container.NewVBox(
		row("Path", e.path),
		row("Size", humanBytes(e.size)),
		row("Kind", e.kind),
		row("Modified", e.mod.Format("2006-01-02 15:04")),
	)
}

func (v *remoteStudioView) transcodePanel(e fileEntry) fyne.CanvasObject {
	var custom []transcode.Preset
	if v.u.svc.Cfg != nil {
		custom = v.u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)
	labels := make([]string, 0, len(presets))
	byLabel := map[string]transcode.Preset{}
	for _, p := range presets {
		labels = append(labels, p.Label)
		byLabel[p.Label] = p
	}
	presetSel := widget.NewSelect(labels, nil)
	if len(labels) > 0 {
		presetSel.SetSelected(labels[0])
	}
	start := newKitButtonWithIcon("Transcode on paired instance", theme.MediaPlayIcon(), func() {
		p, ok := byLabel[presetSel.Selected]
		if !ok {
			v.u.Notify("rave-mate", "Pick a preset first.")
			return
		}
		v.startRemoteTranscode(e, p)
	})
	start.SetVariant(kitBtnBrand)
	return container.NewVBox(smallCaps("PRESET"), presetSel, start,
		mutedLabel("Software encoder on the paired instance. Output → 'rave-mate-transcoded' beside the source; the original is untouched."))
}

// startRemoteTranscode fires the blocking media.transcode RPC off-thread and toasts the result.
func (v *remoteStudioView) startRemoteTranscode(e fileEntry, p transcode.Preset) {
	v.u.Notify("rave-mate", "Transcoding "+e.name+" on the paired instance…")
	goUI("remote-library", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := v.client.Transcode(ctx, e.path, p, 0, 0)
		if err != nil {
			v.u.Notify("rave-mate", "Remote transcode failed: "+err.Error())
			return
		}
		v.u.Notify("rave-mate", "Transcoded on the paired instance → "+res.Output)
	})
}

// showRowMenu opens the shared file context menu for a remote row.
func (v *remoteStudioView) showRowMenu(e fileEntry, pos fyne.Position, anchor fyne.CanvasObject) {
	showFileMenu(v.u, fileMenuCtx{
		ops:     v.ops,
		entry:   e,
		refresh: v.refresh,
		pickDir: func(onPick func(dir string)) { v.u.showRemoteDirPicker(v.client, "Move to…", onPick) },
	}, pos, anchor)
}

// ── Collection section (remote library over remotectl) ───────────────────────

func (v *remoteStudioView) collectionSection() fyne.CanvasObject {
	if v.collRoot != nil {
		v.reload()
		return v.collRoot
	}
	search := newEntry()
	search.SetPlaceHolder("Search title / artist / album - Enter")
	search.OnSubmitted = func(s string) { v.query = s; v.offset = 0; v.reload() }

	v.info = mutedLabel("Loading collection…")
	v.pageLbl = mutedInline("")
	v.prev = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		if v.offset > 0 {
			v.offset = max(0, v.offset-remoteLibPageSize)
			v.reload()
		}
	})
	v.next = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		if v.offset+remoteLibPageSize < v.total {
			v.offset += remoteLibPageSize
			v.reload()
		}
	})

	v.list = widget.NewList(
		func() int { return len(v.tracks) },
		func() fyne.CanvasObject { return newFileRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(v.tracks) {
				return
			}
			fillRemoteTrackRow(o, v.tracks[id])
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(v.tracks) {
			return
		}
		v.sel = id
		v.selectTrack(v.tracks[id])
	}

	sortNote := mutedInline("Server order · search to narrow")
	searchRow := container.NewBorder(nil, nil, nil,
		container.NewHBox(sortNote, helpIcon("The paired instance pages its collection in import order; per-column sorting of a remote library isn't supported yet. Use search to narrow."),
			v.prev, v.pageLbl, v.next), search)
	head := container.NewVBox(v.info, searchRow, widget.NewSeparator())
	v.collRoot = container.NewBorder(head, nil, nil, nil, v.list)

	v.loadInfo()
	v.reload()
	return v.collRoot
}

// loadInfo pulls the collection summary (app/version/total) off-thread.
func (v *remoteStudioView) loadInfo() {
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		info, err := v.client.LibraryInfo(ctx)
		fyne.Do(func() {
			switch {
			case err != nil:
				v.info.SetText("Collection: error - " + err.Error())
			case !info.HasSource:
				v.info.SetText("No collection imported on the paired instance.")
			default:
				v.info.SetText(fmt.Sprintf("%s %s · %d tracks", strOrDash(info.App), info.Version, info.Total))
			}
		})
	})
}

// reload fetches the current page (query+offset) off-thread and repaints.
func (v *remoteStudioView) reload() {
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		res, err := v.client.LibraryTracks(ctx, v.offset, remoteLibPageSize, v.query)
		fyne.Do(func() {
			if err != nil {
				v.info.SetText("Collection: error - " + err.Error())
				return
			}
			v.tracks = res.Tracks
			v.total = res.Total
			v.sel = -1
			v.list.UnselectAll()
			v.list.Refresh()
			from := 0
			if v.total > 0 {
				from = v.offset + 1
			}
			v.pageLbl.SetText(fmt.Sprintf("%d–%d / %d", from, min(v.offset+len(v.tracks), v.total), v.total))
			if v.offset > 0 {
				v.prev.Enable()
			} else {
				v.prev.Disable()
			}
			if v.offset+remoteLibPageSize < v.total {
				v.next.Enable()
			} else {
				v.next.Disable()
			}
			v.status.SetLeft(fmt.Sprintf("Collection · %d tracks", v.total))
		})
	})
}

// selectTrack fills the inspector for a remote collection track (tags + details).
func (v *remoteStudioView) selectTrack(t musiclib.Track) {
	v.showInspector()
	sub := strOrDash(t.Artist)
	if t.BPM > 0 {
		sub = joinDot(sub, fmt.Sprintf("%.0f BPM", t.BPM))
	}
	sub = joinDot(sub, t.Key)
	v.inspector.SetHeader(strOrDash(t.Title), sub)

	write := newKitButtonWithIcon("Write tags → file", theme.DocumentSaveIcon(), func() { v.writeTags(t) })
	write.SetVariant(kitBtnBrand)
	revert := newKitButtonWithIcon("Revert", theme.ContentUndoIcon(), func() { v.revertTags(t) })
	tags := container.NewVBox(
		container.NewGridWithColumns(2, write, revert),
		mutedLabel("Writes BPM / key / genre / comment into the file on the paired instance - revertible there."))

	row := func(k, val string) fyne.CanvasObject {
		value := widget.NewLabel(strOrDash(val))
		value.Wrapping = fyne.TextWrapBreak
		return container.NewVBox(mutedInline(k), value)
	}
	details := container.NewVBox(
		row("Path", t.Path), row("Album", t.Album), row("Genre", t.Genre),
		row("BPM", fmtBPM(t.BPM)), row("Key", t.Key), row("Label", t.Label))

	v.inspector.SetSections(
		kitSection{Key: "tags", Title: "TAGS (ON THE PAIRED INSTANCE)", DefaultOpen: true,
			Help:    "Write the DJ analysis (BPM/key/genre/comment) into the file's tags on the paired instance (MP3/FLAC). Revertible there.",
			Content: tags},
		kitSection{Key: "details", Title: "DETAILS", DefaultOpen: true, Content: details},
	)
}

func (v *remoteStudioView) writeTags(t musiclib.Track) {
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		res, err := v.client.WriteTags(ctx, t.Path)
		if err != nil {
			v.u.Notify("rave-mate", "Remote tag write failed: "+err.Error())
			return
		}
		if res.Written == 0 {
			v.u.Notify("rave-mate", "No analysis (BPM/key/genre) to write for this track.")
			return
		}
		v.u.Notify("rave-mate", fmt.Sprintf("Wrote %d tags on the paired instance (revertible).", res.Written))
	})
}

func (v *remoteStudioView) revertTags(t musiclib.Track) {
	goUI("remote-library", func() {
		ctx, cancel := rctx()
		defer cancel()
		if err := v.client.RevertTags(ctx, t.Path); err != nil {
			v.u.Notify("rave-mate", "Remote revert failed: "+err.Error())
			return
		}
		v.u.Notify("rave-mate", "Reverted tag changes on the paired instance.")
	})
}

// ── shared helpers ────────────────────────────────────────────────────────────

// entriesToFileEntries converts localmedia entries to the browse row shape.
func entriesToFileEntries(in []localmedia.Entry) []fileEntry {
	out := make([]fileEntry, 0, len(in))
	for _, e := range in {
		kind := e.Kind
		if e.IsDirectory {
			kind = "dir"
		}
		mod, _ := time.Parse(time.RFC3339, e.ModifiedAt)
		out = append(out, fileEntry{name: e.Name, path: e.Path, isDir: e.IsDirectory, size: e.SizeBytes, mod: mod, kind: kind})
	}
	return out
}

// sortFileEntries orders dirs-first, then by field (shared with the local browse semantics).
func sortFileEntries(entries []fileEntry, by string) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.isDir != b.isDir {
			return a.isDir
		}
		switch by {
		case "Size":
			return a.size > b.size
		case "Modified":
			return a.mod.After(b.mod)
		default: // Name
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		}
	})
}

// fillRemoteFileRow fills a newFileRow for a remote entry (no batch checkbox / key pill).
func fillRemoteFileRow(o fyne.CanvasObject, e fileEntry) {
	name, check, icon, kp, sub := fileRowParts(o)
	name.SetText(e.name)
	icon.SetResource(kindIcon(e.kind, e.isDir))
	kp.Hide()
	check.OnChanged = nil
	check.SetChecked(false)
	check.Hide()
	if e.isDir {
		sub.SetText("")
	} else {
		sub.SetText(humanBytes(e.size) + " · " + e.mod.Format("02/01/2006"))
	}
}

// fillRemoteTrackRow fills a newFileRow for a remote collection track.
func fillRemoteTrackRow(o fyne.CanvasObject, t musiclib.Track) {
	name, check, icon, kp, sub := fileRowParts(o)
	check.OnChanged = nil
	check.SetChecked(false)
	check.Hide()
	icon.SetResource(theme.MediaMusicIcon())
	name.SetText(fmt.Sprintf("%s - %s", strOrDash(t.Artist), strOrDash(t.Title)))
	var parts []string
	if t.BPM > 0 {
		parts = append(parts, fmt.Sprintf("%.0f BPM", t.BPM))
	}
	if t.Genre != "" {
		parts = append(parts, t.Genre)
	}
	sub.SetText(strings.Join(parts, " · "))
	setKeyPill(kp, t.Key, nil)
}

// splitRemotePathSegs is splitPathSegs for a path that may use the peer OS's separators.
func splitRemotePathSegs(p string) []segpart {
	sep := "/"
	if strings.Contains(p, `\`) {
		sep = `\`
	}
	parts := strings.Split(p, sep)
	var segs []segpart
	acc := ""
	for _, part := range parts {
		if part == "" {
			if acc == "" && sep == "/" {
				acc = "/"
				segs = append(segs, segpart{"/", acc})
			}
			continue
		}
		if acc == "" || acc == "/" {
			if acc == "/" {
				acc += part
			} else {
				acc = part
			}
			if sep == `\` && strings.HasSuffix(part, ":") {
				acc += `\` // drive root needs the trailing separator to list
			}
		} else {
			acc = strings.TrimSuffix(acc, sep) + sep + part
		}
		segs = append(segs, segpart{part, acc})
	}
	if len(segs) == 0 {
		segs = append(segs, segpart{p, p})
	}
	return segs
}

func fmtBPM(b float64) string {
	if b <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f", b)
}
