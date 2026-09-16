//go:build zigui

package webui

import (
	"testing"

	"rave.page/mate/internal/zigui"
)

// Picker-modal golden gate: the Zig renderer (native/zigui/src/pickbrowse.zig) must be BYTE-
// IDENTICAL to the Go golden reference (pick_browser_render.go pkBrowseHTMLOf) over both the JSON
// and the RZW1 wire paths, across every branch (dir/file/save/multi, list/grid, filter, empty,
// escaping, unicode).

func pkChip(label, act string, active bool) pkChipSt { return pkChipSt{label, act, active} }

func pkSortChips(cur string) []pkChipSt {
	return []pkChipSt{pkChip("Name ↑", "pk-sort:name", cur == "name"), pkChip("Modified", "pk-sort:modified", cur == "modified"),
		pkChip("Size", "pk-sort:size", cur == "size"), pkChip("Type", "pk-sort:type", cur == "type")}
}

func pkBaseSt() pkBrowseSt {
	return pkBrowseSt{
		Title: "Choose a file",
		Groups: []pkGroupSt{
			{Header: "QUICK ACCESS", Rows: []pkNavRowSt{
				{"pk-nav:E:\\media", "📌", "media", false, ""},
				{"pk-nav:C:\\Users\\dj\\Downloads", "🕘", "Downloads", true, ""},
			}},
			{Header: "PLACES", Rows: []pkNavRowSt{
				{"pk-nav:C:\\Users\\dj", "⌂", "HOME", false, ""},
				{"pk-nav:C:\\", "💾", "C:\\", false, ""},
			}},
			{Header: "PINNED", Rows: []pkNavRowSt{
				{"pk-nav:E:\\sets", "★", "sets", false, "pk-unpin:E:\\sets"},
			}},
		},
		Crumbs:    []pkCrumbSt{{"C:", "pk-nav:C:\\"}, {"Users", "pk-nav:C:\\Users"}, {"dj", "pk-nav:C:\\Users\\dj"}},
		PathVal:   "C:\\Users\\dj",
		PathPH:    "Type or paste a path, Enter to go",
		SearchPH:  "Search this folder…",
		Sorts:     pkSortChips("name"),
		ViewList:  pkChip("List", "pk-view:list", true),
		ViewGrid:  pkChip("Grid", "pk-view:grid", false),
		Hidden:    pkChip("Hidden", "pk-hidden", false),
		Pin:       pkChip("Pin", "pk-pin", false),
		ColName:   "Name",
		ColMod:    "Modified",
		ColSize:   "Size",
		ColType:   "Type",
		Cancel:    "Cancel",
		SysDialog: "System dialog…",
	}
}

func pkBrowseFixtures() map[string]pkBrowseSt {
	fx := map[string]pkBrowseSt{}

	// file/list with a highlighted row, an ext filter, and a "showing N of M" line
	list := pkBaseSt()
	list.HasFilter = true
	list.FilterOne = pkChip("Video", "pk-filter:match", true)
	list.FilterAll = pkChip("All files", "pk-filter:all", false)
	list.Entries = []pkEntrySt{
		{Act: "pk-nav:C:\\Users\\dj\\sub", Glyph: "📁", Name: "sub", HL: true},
		{Act: "pk-open:C:\\Users\\dj\\a.mp4", Glyph: "🎬", Name: "a.mp4", Modified: "2026-07-03 07:02", Size: "29.2 GB", Type: "MP4", Sel: true},
		{Act: "pk-open:C:\\Users\\dj\\b.mp4", Glyph: "🎬", Name: "b.mp4", Modified: "2026-07-04 03:01", Size: "30.9 GB", Type: "MP4"},
	}
	list.More = "Showing 3 of 42"
	list.Readout = "C:\\Users\\dj\\a.mp4"
	list.Primary = "Choose"
	fx["fileList"] = list

	// grid with an image tile + a highlighted folder tile
	grid := pkBaseSt()
	grid.Grid = true
	grid.ViewList = pkChip("List", "pk-view:list", false)
	grid.ViewGrid = pkChip("Grid", "pk-view:grid", true)
	grid.Entries = []pkEntrySt{
		{Act: "pk-nav:C:\\Users\\dj\\sub", Glyph: "📁", Name: "sub", HL: true},
		{Act: "pk-open:C:\\Users\\dj\\p.png", Glyph: "🖼", Img: "http://127.0.0.1:5599/img/abc", Name: "p.png", Sel: true},
	}
	grid.Readout = ""
	grid.Primary = "Choose"
	fx["grid"] = grid

	// dir kind, empty folder
	empty := pkBaseSt()
	empty.Title = "Choose a folder"
	empty.HasFilter = false
	empty.Empty = "Nothing here"
	empty.Entries = []pkEntrySt{}
	empty.Readout = "C:\\Users\\dj"
	empty.Primary = "Choose folder"
	fx["dirEmpty"] = empty

	// save mode with an overwrite confirm + filename field
	save := pkBaseSt()
	save.Title = "Save as"
	save.SaveMode = true
	save.Readout = "E:\\media\\recordings"
	save.SaveVal = "set-01"
	save.SavePH = "*.mp4"
	save.Badge = "File exists"
	save.Primary = "Overwrite"
	save.Entries = []pkEntrySt{{Act: "pk-open:E:\\media\\set-01.mp4", Glyph: "🎬", Name: "set-01.mp4", Modified: "2026-09-13 13:00", Size: "197.8 MB", Type: "MP4"}}
	fx["save"] = save

	// multi with checkboxes (some checked+sel)
	multi := pkBaseSt()
	multi.Title = "Choose files"
	multi.Entries = []pkEntrySt{
		{Act: "pk-open:C:\\a.flac", SelAct: "pk-sel:C:\\a.flac", Glyph: "🎵", Name: "a.flac", Modified: "2026-01-01 00:00", Size: "40 MB", Type: "FLAC", Checked: true, Sel: true},
		{Act: "pk-open:C:\\b.flac", SelAct: "pk-sel:C:\\b.flac", Glyph: "🎵", Name: "b.flac", Modified: "2026-01-02 00:00", Size: "41 MB", Type: "FLAC", HL: true},
	}
	multi.Readout = "2 selected"
	multi.Primary = "Choose"
	fx["multi"] = multi

	// escaping + unicode across every escapable field
	esc := pkBaseSt()
	esc.Title = `A&B <"x">`
	esc.PathVal = `C:\a&b<"x">`
	esc.SearchVal = `q&<">`
	esc.Groups = []pkGroupSt{{Header: `H&<">`, Rows: []pkNavRowSt{{`pk-nav:C:\a&b`, "★", `l&<">`, true, `pk-unpin:C:\a&b`}}}}
	esc.Crumbs = []pkCrumbSt{{`C&:`, `pk-nav:C:\`}, {`Му&зыка`, `pk-nav:C:\Му&зыка`}}
	esc.Entries = []pkEntrySt{{Act: `pk-open:C:\a&b.mp3`, SelAct: `pk-sel:C:\a&b.mp3`, Glyph: "🎵", Name: `a&<"ю">.mp3`, Modified: "—", Size: "1 B", Type: "MP3", Checked: true}}
	esc.Readout = `C:\a&b<"x">`
	esc.Primary = `Ch&oose`
	esc.Cancel = "Отмена"
	fx["escaping"] = esc

	return fx
}

func TestZigPkBrowseGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	for name, st := range pkBrowseFixtures() {
		t.Run(name, func(t *testing.T) {
			threeWayFrag(t, "pkbrowse", pkBrowseHTMLOf(st), stateJSON(st), wirePkBrowse(st),
				zigui.RenderPkBrowse, zigui.RenderPkBrowseV2)
		})
	}
	assertNoNewFallbacksIn(t, before, "RenderPkBrowse", "RenderPkBrowseV2")
}
