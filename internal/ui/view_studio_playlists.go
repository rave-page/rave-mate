package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// Playlists section: manage manual playlists (create/rename/reorder/remove/export),
// rule-based smart playlists (genre/BPM/key/rating/plays, evaluated live against the
// collection), and read-only playlists imported from the DJ software. Membership +
// add-to-playlist live in the track detail panel; the Collection list multi-selects
// into a playlist in bulk.

// plSortOpts: "Default" = stored/manual order; the rest map to lessTrack fields. Selecting a sort
// (or a key filter) hides the manual ↑↓✕ reorder controls - view order then ≠ stored order.
var plSortOpts = []string{"Default", "Artist", "Title", "BPM", "Key", "Genre", "Rating", "Plays"}

// ── section ──────────────────────────────────────────────────────────────────

func (sv *studioView) playlistsSection() fyne.CanvasObject {
	if sv.u.svc.Lib == nil {
		return container.NewVBox(mutedLabel("Library DB unavailable - playlists need persistence."))
	}
	if sv.plList == nil {
		sv.plList = widget.NewList(
			func() int { return len(sv.plRows) },
			func() fyne.CanvasObject { return newFileRow() },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				if id < 0 || id >= len(sv.plRows) {
					return
				}
				sv.fillPlaylistRow(o, sv.plRows[id])
			},
		)
		sv.plList.OnSelected = func(id widget.ListItemID) {
			if id >= 0 && id < len(sv.plRows) {
				sv.openPlaylist(sv.plRows[id])
			}
		}
		sv.plTrackList = widget.NewList(
			func() int { return len(sv.plShown) },
			func() fyne.CanvasObject { return newPlaylistTrackRow() },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				if id >= 0 && id < len(sv.plShown) {
					sv.fillPlaylistTrackRow(o, sv.plShown[id])
				}
			},
		)
		sv.plTrackList.OnSelected = func(id widget.ListItemID) {
			if id >= 0 && id < len(sv.plShown) {
				sv.plTrackList.UnselectAll()
				t := sv.plTracks[sv.plShown[id]]
				sv.selectTrack(t, pathOnDisk(t.Path))
			}
		}
		sv.plHead = container.NewVBox(mutedLabel("Select a playlist."))
	}
	sv.refreshPlaylists()
	if sv.plSyncPairs == nil && sv.plSignedIn() { // first open: hydrate cloud statuses once
		sv.refreshPlaylistSync()
	}

	newBtn := widget.NewButtonWithIcon("New playlist", theme.ContentAddIcon(), func() { sv.newPlaylistDialog() })
	newBtn.Importance = widget.HighImportance
	smartBtn := widget.NewButtonWithIcon("New smart playlist", theme.SearchIcon(), func() { sv.newSmartPlaylistDialog() })
	left := container.NewBorder(container.NewVBox(
		WrapActions(newBtn, smartBtn),
		mutedLabel("★ manual · ⚡ smart (rules, live) · imported from your DJ software (read-only)."),
		sv.playlistSyncBar(),
		widget.NewSeparator()), nil, nil, nil, sv.plList)
	right := container.NewBorder(sv.plHead, nil, nil, nil, sv.plTrackList)
	return container.New(newAdaptiveSplit(0.40), shrinkWidth(260, left), shrinkWidth(300, right))
}

func (sv *studioView) fillPlaylistRow(o fyne.CanvasObject, r libdb.PlaylistRow) {
	name, check, icon, kp, sub := fileRowParts(o)
	check.OnChanged = nil
	check.SetChecked(false)
	check.Hide()
	kp.Hide() // playlists have no key of their own
	label := r.Name
	if r.Folder != "" {
		label = r.Folder + " / " + r.Name
	}
	name.SetText(label)
	chip := ""
	if p, ok := sv.plSyncPairs[r.ID]; ok && r.Kind != libdb.PlaylistSmart {
		if s := plSyncStatusLabel(p.Status); s != "" {
			chip = " · " + s
		}
	}
	switch r.Kind {
	case libdb.PlaylistSmart:
		icon.SetResource(theme.SearchIcon())
		sub.SetText(fmt.Sprintf("⚡ %d", sv.smartCount(r)))
	case libdb.PlaylistImported:
		icon.SetResource(theme.DownloadIcon())
		sub.SetText(fmt.Sprintf("%d%s", r.TrackCount, chip))
	default:
		icon.SetResource(theme.MediaMusicIcon())
		sub.SetText(fmt.Sprintf("★ %d%s", r.TrackCount, chip))
	}
}

// refreshPlaylists reloads the playlist list from the DB (UI thread).
func (sv *studioView) refreshPlaylists() {
	db := sv.u.svc.Lib
	if db == nil {
		return
	}
	rows, err := db.ListPlaylists()
	if err != nil {
		sv.u.svc.Log.Warn("library", "list playlists", map[string]any{"error": err.Error()})
		return
	}
	sv.plRows = rows
	if sv.plList != nil {
		sv.plList.Refresh()
	}
	// keep the open playlist in sync (rename / count changes)
	if sv.plSel != 0 {
		for _, r := range rows {
			if r.ID == sv.plSel {
				sv.openPlaylist(r)
				return
			}
		}
		sv.closePlaylist()
	}
}

// smartCount evaluates a smart playlist's live match count against the loaded collection.
func (sv *studioView) smartCount(r libdb.PlaylistRow) int {
	rules, ok := parseRules(r.Rules)
	if !ok {
		return 0
	}
	n := 0
	for _, t := range sv.tracks {
		if rules.Match(t) {
			n++
		}
	}
	return n
}

func parseRules(s string) (musiclib.SmartRules, bool) {
	var r musiclib.SmartRules
	if s == "" {
		return r, true
	}
	return r, json.Unmarshal([]byte(s), &r) == nil
}

// ── open / render one playlist ───────────────────────────────────────────────

// openPlaylist loads a playlist's tracks (smart = live rule evaluation; others from the DB,
// resolved against the collection) and renders the right pane.
func (sv *studioView) openPlaylist(r libdb.PlaylistRow) {
	db := sv.u.svc.Lib
	sv.plSel = r.ID
	sv.plCur = r
	if r.Kind == libdb.PlaylistSmart {
		rules, _ := parseRules(r.Rules)
		sv.plTracks = musiclib.FilterSmart(sv.tracks, rules)
		sv.plPaths = nil
	} else {
		items, err := db.PlaylistItems(r.ID)
		if err != nil {
			sv.u.svc.Log.Warn("library", "playlist tracks", map[string]any{"error": err.Error()})
			return
		}
		sv.plPaths = make([]string, len(items))
		sv.plTracks = make([]musiclib.Track, len(items))
		for i, it := range items {
			sv.plPaths[i] = it.Path
			switch {
			case it.Unresolved(): // pulled remote item without a local file
				sv.plTracks[i] = musiclib.Track{Path: it.Path, Title: it.Title, Artist: it.Artist}
			default:
				if t, ok := sv.byPath[it.Path]; ok {
					sv.plTracks[i] = t
				} else {
					sv.plTracks[i] = musiclib.Track{Path: it.Path, Title: baseName(it.Path)}
				}
			}
		}
	}
	sv.applyPlKeyFilter()
	sv.renderPlaylistHead(r)
	if sv.plTrackList != nil {
		sv.plTrackList.Refresh()
	}
}

// applyPlKeyFilter rebuilds the open playlist's visible row indices (key filter + sort).
func (sv *studioView) applyPlKeyFilter() {
	sv.plShown = sv.plShown[:0]
	for i, t := range sv.plTracks {
		if keyMatches(t.Key, sv.plKeySel) {
			sv.plShown = append(sv.plShown, i)
		}
	}
	sortIndicesBy(sv.plShown, func(i int) musiclib.Track { return sv.plTracks[i] }, sv.plSortBy, sv.plSortDesc)
}

// refreshPlTracks re-applies the open playlist's key filter + sort and repaints the track list.
func (sv *studioView) refreshPlTracks() {
	sv.applyPlKeyFilter()
	if sv.plTrackList != nil {
		sv.plTrackList.Refresh()
	}
}

func (sv *studioView) closePlaylist() {
	sv.plSel, sv.plCur = 0, libdb.PlaylistRow{}
	sv.plTracks, sv.plPaths, sv.plShown = nil, nil, nil
	if sv.plHead != nil {
		sv.plHead.Objects = []fyne.CanvasObject{mutedLabel("Select a playlist.")}
		sv.plHead.Refresh()
	}
	if sv.plTrackList != nil {
		sv.plTrackList.Refresh()
	}
}

func (sv *studioView) renderPlaylistHead(r libdb.PlaylistRow) {
	if sv.plHead == nil {
		return
	}
	sub := ""
	switch r.Kind {
	case libdb.PlaylistSmart:
		rules, _ := parseRules(r.Rules)
		sub = "⚡ Smart · " + rules.Describe() + fmt.Sprintf(" · %d match", len(sv.plTracks))
	case libdb.PlaylistImported:
		sub = fmt.Sprintf("Imported from your DJ software · %d tracks · refreshed on re-import (read-only)", len(sv.plTracks))
	default:
		sub = fmt.Sprintf("★ Manual · %d tracks · drag-free reorder via ↑ ↓ on each row", len(sv.plTracks))
	}
	subLbl := mutedLabel(sub)
	subLbl.Wrapping = fyne.TextWrapWord

	low := func(label string, icon fyne.Resource, fn func()) *widget.Button {
		b := widget.NewButtonWithIcon(label, icon, fn)
		b.Importance = widget.LowImportance
		return b
	}
	actions := []fyne.CanvasObject{}
	if r.Kind != libdb.PlaylistImported {
		actions = append(actions, low("Rename", theme.DocumentCreateIcon(), func() { sv.renamePlaylistDialog(r) }))
	}
	if r.Kind == libdb.PlaylistSmart {
		actions = append(actions, low("Edit rules", theme.SearchIcon(), func() { sv.smartRulesDialog(r) }))
	}
	if r.Kind != libdb.PlaylistManual {
		actions = append(actions, low("Duplicate as manual", theme.ContentCopyIcon(), func() { sv.duplicatePlaylist(r) }))
	}
	actions = append(actions,
		low("Export M3U", theme.DocumentSaveIcon(), func() { sv.exportPlaylistM3U(r) }),
		low("Delete", theme.DeleteIcon(), func() { sv.deletePlaylistDialog(r) }),
	)
	actions = append(actions, sv.keyFilterChip(sv.plTracks, sv.plKeySel, sv.refreshPlTracks))
	sortBar := sv.trackSortBar(plSortOpts, "Default", sv.plSortBy, sv.plSortDesc,
		func(by string) { sv.plSortBy = by; sv.refreshPlTracks() },
		func() bool { sv.plSortDesc = !sv.plSortDesc; sv.refreshPlTracks(); return sv.plSortDesc })
	sv.plHead.Objects = []fyne.CanvasObject{
		boldLabel(r.Name), subLbl, WrapActions(actions...), sortBar,
		sv.playlistSyncPanel(r), widget.NewSeparator(),
	}
	sv.plHead.Refresh()
}

// ── playlist track rows (↑ ↓ ✕ for manual) ──────────────────────────────────

// newPlaylistTrackRow: [name(center), left=HBox(pos,icon), right=HBox(Center(pill),up,down,remove)].
func newPlaylistTrackRow() fyne.CanvasObject {
	pos := mutedInline("0")
	icon := widget.NewIcon(theme.MediaMusicIcon())
	name := widget.NewLabel("")
	name.Truncation = fyne.TextTruncateEllipsis
	mk := func(ic fyne.Resource) *widget.Button {
		b := widget.NewButtonWithIcon("", ic, nil)
		b.Importance = widget.LowImportance
		return b
	}
	kp := newPill("", colSecondary, colMuted, nil)
	kp.Hide()
	right := container.NewHBox(container.NewCenter(kp), mk(theme.MoveUpIcon()), mk(theme.MoveDownIcon()), mk(theme.CancelIcon()))
	return container.NewBorder(nil, nil, container.NewHBox(pos, icon), right, name)
}

func (sv *studioView) fillPlaylistTrackRow(o fyne.CanvasObject, id int) {
	t := sv.plTracks[id]
	c := o.(*fyne.Container)
	name := c.Objects[0].(*widget.Label)
	left := c.Objects[1].(*fyne.Container)
	pos := left.Objects[0].(*widget.Label)
	icon := left.Objects[1].(*widget.Icon)
	right := c.Objects[2].(*fyne.Container)
	kp := right.Objects[0].(*fyne.Container).Objects[0].(*pill)
	up := right.Objects[1].(*widget.Button)
	down := right.Objects[2].(*widget.Button)
	rem := right.Objects[3].(*widget.Button)

	pos.SetText(fmt.Sprintf("%2d", id+1))
	label := fmt.Sprintf("%s - %s", strOrDash(t.Artist), strOrDash(t.Title))
	if t.BPM > 0 {
		label += "  ·  " + fmt.Sprintf("%.0f BPM", t.BPM)
	}
	name.SetText(label)
	setKeyPill(kp, t.Key, sv.keyRef)
	if pathOnDisk(t.Path) {
		icon.SetResource(theme.MediaMusicIcon())
	} else {
		icon.SetResource(theme.WarningIcon())
	}

	// key filter or sort active → view order ≠ stored order; reorder/remove disabled
	editable := sv.plCur.Kind == libdb.PlaylistManual && !anyKeySelected(sv.plKeySel) && sv.plSortBy == ""
	for _, b := range []*widget.Button{up, down, rem} {
		if editable {
			b.Show()
		} else {
			b.Hide()
		}
	}
	if !editable {
		up.OnTapped, down.OnTapped, rem.OnTapped = nil, nil, nil
		return
	}
	up.OnTapped = func() { sv.movePlaylistTrack(id, -1) }
	down.OnTapped = func() { sv.movePlaylistTrack(id, +1) }
	rem.OnTapped = func() { sv.removePlaylistTrack(id) }
	if id == 0 {
		up.Disable()
	} else {
		up.Enable()
	}
	if id == len(sv.plTracks)-1 {
		down.Disable()
	} else {
		down.Enable()
	}
}

func (sv *studioView) movePlaylistTrack(id, delta int) {
	j := id + delta
	if sv.plSel == 0 || id < 0 || id >= len(sv.plPaths) || j < 0 || j >= len(sv.plPaths) {
		return
	}
	sv.plPaths[id], sv.plPaths[j] = sv.plPaths[j], sv.plPaths[id]
	if err := sv.u.svc.Lib.ReplacePlaylistTracks(sv.plSel, sv.plPaths); err != nil {
		sv.u.Notify("rave-mate", "Reorder failed: "+err.Error())
	}
	sv.openPlaylist(sv.plCur)
}

func (sv *studioView) removePlaylistTrack(id int) {
	if sv.plSel == 0 || id < 0 || id >= len(sv.plPaths) {
		return
	}
	if err := sv.u.svc.Lib.RemoveFromPlaylist(sv.plSel, sv.plPaths[id]); err != nil {
		sv.u.Notify("rave-mate", "Remove failed: "+err.Error())
	}
	sv.refreshPlaylists()
}

// ── create / rename / delete / duplicate / export ────────────────────────────

func (sv *studioView) newPlaylistDialog() {
	win := currentWindow()
	if win == nil {
		return
	}
	ent := newEntry()
	ent.SetPlaceHolder("Playlist name")
	dialog.ShowForm("New playlist", "Create", "Cancel",
		[]*widget.FormItem{widget.NewFormItem("Name", ent)},
		func(ok bool) {
			if !ok {
				return
			}
			sv.createPlaylist(strings.TrimSpace(ent.Text), libdb.PlaylistManual, "")
		}, win)
}

// createPlaylist inserts + reselects; returns the new id (0 on failure).
func (sv *studioView) createPlaylist(name, kind, rules string) int64 {
	if name == "" {
		return 0
	}
	id, err := sv.u.svc.Lib.CreatePlaylist(name, kind, rules)
	if err != nil {
		sv.u.Notify("rave-mate", "Create failed: "+err.Error())
		return 0
	}
	sv.plSel = id
	sv.refreshPlaylists()
	return id
}

func (sv *studioView) renamePlaylistDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	ent := newEntry()
	ent.SetText(r.Name)
	dialog.ShowForm("Rename playlist", "Rename", "Cancel",
		[]*widget.FormItem{widget.NewFormItem("Name", ent)},
		func(ok bool) {
			if !ok || strings.TrimSpace(ent.Text) == "" {
				return
			}
			if err := sv.u.svc.Lib.RenamePlaylist(r.ID, strings.TrimSpace(ent.Text)); err != nil {
				sv.u.Notify("rave-mate", "Rename failed: "+err.Error())
				return
			}
			sv.refreshPlaylists()
		}, win)
}

func (sv *studioView) deletePlaylistDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	note := "Tracks themselves are never touched."
	if r.Kind == libdb.PlaylistImported {
		note += " Re-importing your library brings it back."
	}
	dialog.ShowConfirm("Delete playlist", fmt.Sprintf("Delete “%s”? %s", r.Name, note), func(ok bool) {
		if !ok {
			return
		}
		if err := sv.u.svc.Lib.DeletePlaylist(r.ID); err != nil {
			sv.u.Notify("rave-mate", "Delete failed: "+err.Error())
			return
		}
		if sv.plSel == r.ID {
			sv.closePlaylist()
		}
		sv.refreshPlaylists()
	}, win)
}

// duplicatePlaylist materializes a smart/imported playlist as an editable manual copy.
func (sv *studioView) duplicatePlaylist(r libdb.PlaylistRow) {
	id := sv.createPlaylist(r.Name+" (copy)", libdb.PlaylistManual, "")
	if id == 0 {
		return
	}
	paths := make([]string, 0, len(sv.plTracks))
	for _, t := range sv.plTracks {
		paths = append(paths, t.Path)
	}
	if err := sv.u.svc.Lib.ReplacePlaylistTracks(id, paths); err != nil {
		sv.u.Notify("rave-mate", "Duplicate failed: "+err.Error())
		return
	}
	sv.refreshPlaylists()
	sv.u.Notify("rave-mate", fmt.Sprintf("Duplicated “%s” → manual playlist (%d tracks).", r.Name, len(paths)))
}

func (sv *studioView) exportPlaylistM3U(r libdb.PlaylistRow) {
	tracks := append([]musiclib.Track(nil), sv.plTracks...)
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "playlist-export", false)
		out, _ := config.DataPath("playlist-" + safeFileName(r.Name) + ".m3u8")
		f, err := os.Create(out)
		if err != nil {
			sv.u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		if err := musiclib.ExportM3U(tracks, f); err != nil {
			sv.u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		sv.u.Notify("rave-mate", "Exported "+out)
	}()
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, s)
}

// ── smart playlist editor ────────────────────────────────────────────────────

func (sv *studioView) newSmartPlaylistDialog() {
	sv.smartRulesDialog(libdb.PlaylistRow{Kind: libdb.PlaylistSmart})
}

// smartRulesDialog edits (or creates, when r.ID==0) a smart playlist: genre chips from the
// collection, BPM band (with feel presets), key/rating/plays/search - with a live match count.
func (sv *studioView) smartRulesDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	rules, _ := parseRules(r.Rules)

	nameEnt := newEntry()
	nameEnt.SetPlaceHolder("Smart playlist name")
	nameEnt.SetText(r.Name)

	// genre chips from the collection's distinct genres
	selGenres := map[string]bool{}
	for _, g := range rules.Genres {
		selGenres[g] = true
	}
	count := mutedLabel("")
	bpmMin, bpmMax := newEntry(), newEntry()
	bpmMin.SetPlaceHolder("min")
	bpmMax.SetPlaceHolder("max")
	keyEnt := newEntry()
	keyEnt.SetPlaceHolder("e.g. Am or 8A")
	keyEnt.SetText(rules.KeyContains)
	searchEnt := newEntry()
	searchEnt.SetPlaceHolder("title / artist / album / label / comment")
	searchEnt.SetText(rules.Search)
	playsEnt := newEntry()
	playsEnt.SetPlaceHolder("0")
	if rules.BPMMin > 0 {
		bpmMin.SetText(strconv.FormatFloat(rules.BPMMin, 'f', -1, 64))
	}
	if rules.BPMMax > 0 {
		bpmMax.SetText(strconv.FormatFloat(rules.BPMMax, 'f', -1, 64))
	}
	if rules.PlayCountMin > 0 {
		playsEnt.SetText(strconv.Itoa(rules.PlayCountMin))
	}
	ratingOpts := []string{"Any", "≥ 1★", "≥ 2★", "≥ 3★", "≥ 4★", "5★"}
	ratingSel := widget.NewSelect(ratingOpts, nil)
	ratingSel.SetSelectedIndex(rules.RatingMin)

	current := func() musiclib.SmartRules {
		out := musiclib.SmartRules{KeyContains: strings.TrimSpace(keyEnt.Text), Search: strings.TrimSpace(searchEnt.Text)}
		for g, on := range selGenres {
			if on {
				out.Genres = append(out.Genres, g)
			}
		}
		sort.Strings(out.Genres)
		out.BPMMin, _ = strconv.ParseFloat(strings.TrimSpace(bpmMin.Text), 64)
		out.BPMMax, _ = strconv.ParseFloat(strings.TrimSpace(bpmMax.Text), 64)
		out.RatingMin = ratingSel.SelectedIndex()
		n, _ := strconv.Atoi(strings.TrimSpace(playsEnt.Text))
		out.PlayCountMin = n
		return out
	}
	recount := func() {
		cur := current()
		count.SetText(fmt.Sprintf("%d of %d tracks match - %s", len(musiclib.FilterSmart(sv.tracks, cur)), len(sv.tracks), cur.Describe()))
	}
	recount()
	for _, e := range []*widget.Entry{bpmMin, bpmMax, keyEnt, searchEnt, playsEnt} {
		e.OnChanged = func(string) { recount() }
	}
	ratingSel.OnChanged = func(string) { recount() }
	genreChip := ChipMultiSelect("Genres", sv.collectionGenres(), selGenres, func(map[string]bool) { recount() })

	// feel presets seed the BPM band - energy proxy without audio analysis
	feelOpts := []string{"Feel preset…"}
	feels := musiclib.FeelPresets()
	for _, f := range feels {
		feelOpts = append(feelOpts, f.Label)
	}
	feelSel := widget.NewSelect(feelOpts, func(s string) {
		for _, f := range feels {
			if f.Label != s {
				continue
			}
			if f.BPMMin > 0 {
				bpmMin.SetText(strconv.FormatFloat(f.BPMMin, 'f', -1, 64))
			} else {
				bpmMin.SetText("")
			}
			if f.BPMMax > 0 && f.BPMMax < 200 {
				bpmMax.SetText(strconv.FormatFloat(f.BPMMax, 'f', -1, 64))
			} else {
				bpmMax.SetText("")
			}
			recount()
		}
	})
	feelSel.SetSelectedIndex(0)

	body := container.NewVBox(
		mutedLabel("Rules combine with AND; genres are OR. Matches update live as your collection changes."),
		container.NewBorder(nil, nil, smallCaps("NAME"), nil, nameEnt),
		WrapActions(genreChip, feelSel),
		container.NewBorder(nil, nil, smallCaps("BPM"), nil, container.NewGridWithColumns(2, bpmMin, bpmMax)),
		container.NewBorder(nil, nil, smallCaps("KEY"), nil, keyEnt),
		container.NewBorder(nil, nil, smallCaps("RATING"), container.NewBorder(nil, nil, smallCaps("PLAYS ≥"), nil, playsEnt), ratingSel),
		container.NewBorder(nil, nil, smallCaps("SEARCH"), nil, searchEnt),
		widget.NewSeparator(), count,
	)
	title := "New smart playlist"
	confirm := "Create"
	if r.ID != 0 {
		title, confirm = "Edit smart playlist", "Save"
	}
	d := dialog.NewCustomConfirm(title, confirm, "Cancel", body, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(nameEnt.Text)
		if name == "" {
			sv.u.Notify("rave-mate", "Smart playlist needs a name.")
			return
		}
		raw, err := json.Marshal(current())
		if err != nil {
			return
		}
		if r.ID == 0 {
			sv.createPlaylist(name, libdb.PlaylistSmart, string(raw))
			return
		}
		serr := sv.u.svc.Lib.RenamePlaylist(r.ID, name)
		if serr == nil {
			serr = sv.u.svc.Lib.SetSmartRules(r.ID, string(raw))
		}
		if serr != nil {
			sv.u.Notify("rave-mate", "Save failed: "+serr.Error())
			return
		}
		sv.refreshPlaylists()
	}, win)
	d.Resize(fyne.NewSize(520, 460))
	d.Show()
}

// collectionGenres returns the distinct, name-sorted genres in the loaded collection.
func (sv *studioView) collectionGenres() []string {
	seen := map[string]string{} // lower → display
	for _, t := range sv.tracks {
		g := strings.TrimSpace(t.Genre)
		if g == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(g)]; !ok {
			seen[strings.ToLower(g)] = g
		}
	}
	out := make([]string, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// ── Collection multi-select → playlist ───────────────────────────────────────

// buildCollBar is the Collection bottom bar shown while rows are ticked: "N selected ·
// Add to playlist… · Clear".
func (sv *studioView) buildCollBar() *fyne.Container {
	sv.collBarLbl = mutedInline("")
	add := widget.NewButtonWithIcon("Add to playlist…", theme.ContentAddIcon(), func() {
		sv.addToPlaylistDialog(sv.collSelPaths())
	})
	add.Importance = widget.HighImportance
	clear := widget.NewButtonWithIcon("Clear", theme.CancelIcon(), sv.clearCollSel)
	clear.Importance = widget.LowImportance
	bar := container.NewBorder(nil, nil, sv.collBarLbl, nil, WrapActions(add, clear))
	bar.Hide()
	return bar
}

func (sv *studioView) toggleCollSel(path string, on bool) {
	if on {
		sv.collSel[path] = true
	} else {
		delete(sv.collSel, path)
	}
	sv.refreshCollBar()
}

func (sv *studioView) refreshCollBar() {
	if sv.collBar == nil {
		return
	}
	if n := len(sv.collSel); n == 0 {
		sv.collBar.Hide()
	} else {
		sv.collBarLbl.SetText(fmt.Sprintf("%d selected", n))
		sv.collBar.Show()
	}
	sv.collBar.Refresh()
}

func (sv *studioView) clearCollSel() {
	sv.collSel = map[string]bool{}
	sv.refreshCollBar()
	if sv.collList != nil {
		sv.collList.Refresh()
	}
}

// collSelPaths snapshots the selection in collection (artist/title) order.
func (sv *studioView) collSelPaths() []string {
	out := make([]string, 0, len(sv.collSel))
	for _, t := range sv.tracks {
		if sv.collSel[t.Path] {
			out = append(out, t.Path)
		}
	}
	return out
}

// ── add-to-playlist + membership (track detail) ──────────────────────────────

// playlistPanel renders the detail-panel PLAYLISTS block for a track: membership chips
// (manual/imported from the DB + smart computed live) and the add/remove dialog.
func (sv *studioView) playlistPanel(t musiclib.Track) fyne.CanvasObject {
	db := sv.u.svc.Lib
	if db == nil || t.Path == "" {
		return container.NewVBox()
	}
	member, err := db.PlaylistsForTrack(t.Path)
	if err != nil {
		return container.NewVBox()
	}
	chips := make([]fyne.CanvasObject, 0, len(member)+2)
	jump := func(r libdb.PlaylistRow) *widget.Button {
		b := widget.NewButton(r.Name, func() {
			sv.showSection("Playlists")
			sv.openPlaylist(r)
		})
		b.Importance = widget.LowImportance
		return b
	}
	for _, r := range member {
		chips = append(chips, jump(r))
	}
	// smart membership: evaluated live, marked ⚡
	if rows, err := db.ListPlaylists(); err == nil {
		for _, r := range rows {
			if r.Kind != libdb.PlaylistSmart {
				continue
			}
			if rules, ok := parseRules(r.Rules); ok && rules.Match(t) {
				b := jump(r)
				b.SetText("⚡ " + r.Name)
				chips = append(chips, b)
			}
		}
	}
	add := widget.NewButtonWithIcon("Add to playlist…", theme.ContentAddIcon(), func() { sv.addToPlaylistDialog([]string{t.Path}) })
	add.Importance = widget.HighImportance
	chips = append(chips, add)
	box := container.NewVBox()
	if len(member) == 0 {
		box.Add(mutedLabel("Not in any playlist yet."))
	}
	box.Add(WrapActions(chips...))
	return box
}

// addToPlaylistDialog toggles membership of paths across manual playlists (Check per list;
// single-track = pre-checked where member) and creates+adds via the inline name entry.
func (sv *studioView) addToPlaylistDialog(paths []string) {
	db := sv.u.svc.Lib
	win := currentWindow()
	if db == nil || win == nil || len(paths) == 0 {
		return
	}
	rows, err := db.ListPlaylists()
	if err != nil {
		sv.u.Notify("rave-mate", "Playlists: "+err.Error())
		return
	}
	memberOf := map[int64]bool{}
	if len(paths) == 1 {
		if pls, err := db.PlaylistsForTrack(paths[0]); err == nil {
			for _, r := range pls {
				memberOf[r.ID] = true
			}
		}
	}
	single := len(paths) == 1
	box := container.NewVBox()
	hasManual := false
	for _, r := range rows {
		if r.Kind != libdb.PlaylistManual {
			continue
		}
		hasManual = true
		r := r
		chk := widget.NewCheck(fmt.Sprintf("%s (%d)", r.Name, r.TrackCount), nil)
		chk.SetChecked(memberOf[r.ID])
		chk.OnChanged = func(on bool) {
			var err error
			if on {
				var n int
				n, err = db.AddToPlaylist(r.ID, paths...)
				if err == nil && !single {
					sv.u.Notify("rave-mate", fmt.Sprintf("Added %d to “%s”.", n, r.Name))
				}
			} else if single {
				err = db.RemoveFromPlaylist(r.ID, paths[0])
			}
			if err != nil {
				sv.u.Notify("rave-mate", "Playlist update failed: "+err.Error())
			}
			sv.refreshPlaylists()
		}
		box.Add(chk)
	}
	if !hasManual {
		box.Add(mutedLabel("No manual playlists yet - create one below."))
	}
	newEnt := newEntry()
	newEnt.SetPlaceHolder("New playlist name")
	create := widget.NewButtonWithIcon("Create + add", theme.ContentAddIcon(), nil)
	create.Importance = widget.HighImportance
	box.Add(widget.NewSeparator())
	box.Add(container.NewBorder(nil, nil, nil, create, newEnt))

	label := "1 track"
	if len(paths) > 1 {
		label = fmt.Sprintf("%d tracks", len(paths))
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(380, 300))
	d := dialog.NewCustom("Add "+label+" to playlist", "Done", scroll, win)
	create.OnTapped = func() {
		name := strings.TrimSpace(newEnt.Text)
		if name == "" {
			return
		}
		id := sv.createPlaylist(name, libdb.PlaylistManual, "")
		if id == 0 {
			return
		}
		if n, err := db.AddToPlaylist(id, paths...); err == nil {
			sv.u.Notify("rave-mate", fmt.Sprintf("Added %d to “%s”.", n, name))
		}
		sv.refreshPlaylists()
		d.Hide()
		// re-open so the new playlist shows checked (single) / available (multi)
		sv.addToPlaylistDialog(paths)
	}
	d.Show()
}
