//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Library fixer/section SUBVIEW golden gate: nav rail · beatgrid-fixer rail (+ #gf-live) ·
// fixer results (gridfix table / tagfix problem list) · per-track tag editor · prep picker ·
// "works well together". Each must be BYTE-IDENTICAL between Zig and the Go pure renderers
// in render_library_fixers.go, standalone AND embedded in the whole-tab library goldens.
// Run: bash scripts/build-zig.sh && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// ── nav rail ──

func libNavFixture() libNavSt {
	return libNavSt{Rows: []libNavRowSt{
		navHdRow("Collection"),
		navItRow("lib-plclear", "🎧", "All tracks", "2312", true),
		navHdRow("Playlists"),
		navItRow("lib-plgoto:7", "🎵", "Warmup", "18", false),
		navItRow("lib-plgoto:8", "⚡", "Peak smart", "", true),
		navItRow("lib-plgoto:9", "⤓", "Imported", "3", false),
		navHdRow("…"),
	}}
}

func libNavBrowseFixture() libNavSt {
	return libNavSt{Rows: []libNavRowSt{
		navHdRow("Places"),
		navItRow(`lib-nav:C:\Users\dj`, "⌂", "Home", "", false),
		navItRow(`lib-nav:C:\Users\dj\Music`, "⌂", "Music", "", true),
		navHdRow("Pinned"),
		navItRow(`lib-nav:D:\sets`, "★", "sets", "", false),
		navHdRow("Drives"),
		navItRow(`lib-nav:C:\`, "💾", `C:\`, "", true),
		navItRow(`lib-nav:D:\`, "💾", `D:\`, "", false),
		navHdRow("Folders"),
		navItRow(`lib-nav:C:\Users`, "↰", "..", "", false),
		navItRow(`lib-nav:C:\Users\dj\Music\promo`, "📁", "promo", "", false),
	}}
}

func libNavFixtures() map[string]libNavSt {
	long := strings.Repeat("very-long-playlist-name-", 40)
	return map[string]libNavSt{
		"empty":      {Rows: []libNavRowSt{}},
		"collection": libNavFixture(),
		"browse":     libNavBrowseFixture(),
		"escaping": {Rows: []libNavRowSt{
			navHdRow(`Coll&ection <"x">'`),
			navItRow(`lib-plgoto:7&"a'<>`, "🎵", `W&armup <"peak">'`, `1&2"`, true),
		}},
		"long": {Rows: []libNavRowSt{
			navHdRow(long),
			navItRow("lib-nav:"+strings.Repeat(`C:\deep\`, 60), "📁", long, "999999", false),
		}},
		"unicode": {Rows: []libNavRowSt{
			navHdRow("Коллекция"),
			navItRow("lib-plgoto:1", "🎧", "Все треки 中文 🎛️", "1 234", true),
		}},
	}
}

func TestZigLibFixNavRailGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libNavFixtures() {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixNavRail(stateJSON(st))
			if !ok {
				t.Fatal("zig nav-rail render failed")
			}
			assertBytesEqual(t, "navrail", libNavRailHTMLOf(st), zig)
		})
	}
}

// ── prep-playlist picker ──

func libPrepFixture() selState {
	return selState{ID: "prep-coll", Label: "Prepare", CurLabel: "Warmup", Rows: []selRow{
		{Val: "", Label: "None"},
		{Val: "__new", Label: "New playlist…"},
		{Val: "7", Label: "Warmup", Badge: "18 tracks", Cur: true},
	}}
}

func TestZigLibFixPrepGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	open := libPrepFixture()
	open.Open, open.Filter = true, "wa"
	for name, st := range map[string]selState{
		"zero":     emptySel(),
		"closed":   libPrepFixture(),
		"open":     open,
		"escaping": {ID: "prep-coll", Label: `Pre&pare <"x">'`, CurLabel: `W&armup "×"'`, Rows: []selRow{{Val: `7&"a'`, Label: `W&armup`, Cur: true}}},
		"unicode":  {ID: "prep-coll", Label: "Подготовка", CurLabel: "Разогрев 🎧", Rows: []selRow{{Val: "7", Label: "Разогрев 🎧", Badge: "18 треков", Cur: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixPrep(stateJSON(st))
			if !ok {
				t.Fatal("zig prep render failed")
			}
			assertBytesEqual(t, "prep", selHTML(st), zig)
		})
	}
}

// ── beatgrid-fixer rail ──

func libGFTilesFixture() []libGFTileSt {
	return []libGFTileSt{
		{N: "42", Label: "FIX", Tone: "violet"},
		{N: "180", Label: "OK", Tone: "mint"},
		{N: "7", Label: "MANUAL", Tone: "amber"},
		{N: "1", Label: "ERR", Tone: "red"},
	}
}

func libGFHealthFixture() libGFSt {
	return libGFSt{Kind: libGFHealth, Eyebrow: "Collection health", Title: "Beatgrids",
		Stats: []libGFStatSt{
			{N: "2312", Label: "Tracks"},
			{N: "48", Label: "Verified", Tone: "mint"},
			{N: "310", Label: "No grid", Tone: "amber"},
			{N: "12", Label: "Manual"},
		},
		Btns:      []uiBtn{newBtn("Fix beatgrids", "primary", "gf-open"), newBtn("Calibrate", "outline", "gf-cal")},
		NoteAfter: true,
		Note:      "Calibration bias: .mp3 +42.7 ms · * −2.9 ms"}
}

func libGFDoneFixture() libGFSt {
	return libGFSt{Kind: libGFDone, Eyebrow: "Beatgrid fixer", Title: "Done",
		Tiles:      libGFTilesFixture(),
		CachedNote: "12 tracks served from the detection cache",
		Hints: []libHintSt{
			{Tone: "ok", Text: "Applied to Traktor (42)"},
			{Tone: "bad", Text: "Rekordbox is running — close it and retry"},
		},
		Acts: []uiBtn{
			newBtn("Apply to Rekordbox (42)", "primary", "gf-apply:rekordbox"),
			newBtn("Prepare 7 for manual gridding", "outline", "gf-prep"),
			newBtn("View results", "outline", "gf-results"),
			newBtn("Close", "ghost", "gf-close"),
		},
		Notes:     []string{"Rekordbox re-imports the XML on next start"},
		ApplyNote: "Apply writes a backup first"}
}

func libGFFixtures() map[string]libGFSt {
	long := strings.Repeat("very-long-current-track-name-", 40)
	return map[string]libGFSt{
		"health": libGFHealthFixture(),
		"healthDisabled": {Kind: libGFHealth, Eyebrow: "Collection health", Title: "Beatgrids",
			Stats: []libGFStatSt{{N: "0", Label: "Tracks"}},
			Note:  "Enable the beatgrid fixer in Settings"},
		"healthNoEngine": {Kind: libGFHealth, Eyebrow: "Collection health", Title: "Beatgrids",
			Stats: []libGFStatSt{{N: "12", Label: "Tracks"}},
			Note:  "No analysis engine installed",
			Btns:  []uiBtn{newBtn("Open settings", "outline", "gf-settings")}},
		"confirm": {Kind: libGFConfirm, Eyebrow: "Beatgrid fixer", Title: "What should I analyze?",
			ConfirmNote: "Nothing is written until you press Apply",
			Force:       newToggle("Force re-analyze", "gf-force", true),
			ForceHint:   "Overrides the cache + manual-marker skips; verified grids stay protected",
			Scopes: []uiBtn{
				newBtn("All tracks (2312)", "primary", "gf-run:all"),
				newBtn("Filtered (310)", "outline", "gf-run:filtered"),
				newBtn("Cancel", "ghost", "gf-close"),
			}},
		"running": {Kind: libGFRunning, Eyebrow: "Beatgrid fixer", Title: "Analyzing…",
			Live:    libGFLiveSt{Tiles: libGFTilesFixture(), Pct: progressPct(0.42), Caption: "230 / 548  ~4m12s left", Current: `C:\m\a&b.flac`},
			StopLbl: "Stop"},
		"calibrating": {Kind: libGFRunning, Eyebrow: "Beatgrid fixer", Title: "Calibrating…",
			Live:    libGFLiveSt{Pct: progressPct(0.1), Caption: "6 / 60", Current: "kick.wav"},
			StopLbl: "Stop"},
		"done": libGFDoneFixture(),
		"doneNoTargets": {Kind: libGFDone, Eyebrow: "Beatgrid fixer", Title: "Cancelled",
			Tiles:     libGFTilesFixture(),
			Hints:     []libHintSt{{Tone: "bad", Text: "No DJ library found to write to"}},
			Acts:      []uiBtn{newBtn("View results", "outline", "gf-results"), newBtn("Close", "ghost", "gf-close")},
			ApplyNote: "Apply writes a backup first"},
		"escaping": {Kind: libGFDone, Eyebrow: `Bea&tgrid <"fixer">'`, Title: `D&one "x"'<>`,
			Tiles:      []libGFTileSt{{N: "1", Label: `F&IX <"x">'`, Tone: "violet"}},
			CachedNote: `12 &cached "x"'<>`,
			Hints:      []libHintSt{{Tone: "bad", Text: `write failed: a&"b'<c>`}},
			Acts:       []uiBtn{newBtn(`Apply to &"Traktor"'`, "primary", `gf-apply:tra&ktor"`)},
			Notes:      []string{`note &"x"'<>`},
			ApplyNote:  `Apply &writes "a backup"'`},
		"long": {Kind: libGFRunning, Eyebrow: "E", Title: "Analyzing…",
			Live:    libGFLiveSt{Tiles: libGFTilesFixture(), Pct: progressPct(0.999), Caption: strings.Repeat("cap ", 200), Current: long},
			StopLbl: "Stop"},
		"unicode": {Kind: libGFHealth, Eyebrow: "Состояние", Title: "Битовые сетки 🎛️",
			Stats:     []libGFStatSt{{N: "1 234", Label: "Треков"}, {N: "48", Label: "Проверено", Tone: "mint"}},
			Btns:      []uiBtn{newBtn("Исправить", "primary", "gf-open")},
			NoteAfter: true, Note: "Смещение: .mp3 +42.7 мс"},
	}
}

func TestZigLibFixGFRailGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libGFFixtures() {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixGFRail(stateJSON(st))
			if !ok {
				t.Fatal("zig gf-rail render failed")
			}
			assertBytesEqual(t, "gfrail", libGFRailHTML(st), zig)
		})
	}
}

// TestZigLibFixGFLiveGolden covers the #gf-live fragment on its own (the run goroutine
// patches it directly, ~2 Hz, through gfLiveRender).
func TestZigLibFixGFLiveGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range map[string]libGFLiveSt{
		"zero":      {Pct: progressPct(0), Caption: "0 / 0  "},
		"batch":     {Tiles: libGFTilesFixture(), Pct: progressPct(0.42), Caption: "230 / 548  ~4m12s left", Current: `C:\m\a.flac`},
		"calibrate": {Pct: progressPct(0.5), Caption: "30 / 60", Current: "kick.wav"},
		"clamped":   {Tiles: libGFTilesFixture(), Pct: progressPct(1.7), Caption: "", Current: ""},
		"escaping":  {Tiles: []libGFTileSt{{N: "1", Label: `F&IX"'<>`, Tone: "violet"}}, Pct: progressPct(0.1), Caption: `1 / 10 &"x"'`, Current: `a&"b'<>.flac`},
		"unicode":   {Pct: progressPct(0.25), Caption: "15 / 60", Current: "трек 🎧.flac"},
	} {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixGFLive(stateJSON(st))
			if !ok {
				t.Fatal("zig gf-live render failed")
			}
			assertBytesEqual(t, "gflive", libGFLiveHTML(st), zig)
		})
	}
}

// ── fixer results ──

func libGFResFixture() libGFResSt {
	return libGFResSt{
		Chips: []libChipSt{
			newChip("All", "", "gf-flt:", true),
			newChip("FIX", "", "gf-flt:FIX", false),
			newChip("OK", "", "gf-flt:OK", false),
			newChip("MANUAL", "", "gf-flt:SKIP", false),
			newChip("ERR", "", "gf-flt:ERR", false),
		},
		Rows: []libGFResRowSt{
			{Path: `C:\m\a.flac`, St: "FIX", StLow: "fix", Title: "Kollektiv - Rausch",
				Detail: "downbeat off by 38 ms", Delta: "128.00 → 128.02 BPM"},
			{Path: `C:\m\b.mp3`, St: "OK", StLow: "ok", Title: "b.mp3", Detail: "grid already tight", Delta: ""},
			{Path: `C:\m\c.wav`, St: "SKIP", StLow: "skip", Title: "c.wav", Detail: "multiple markers", Delta: ""},
			{Path: `C:\m\d.aiff`, St: "ERR", StLow: "err", Title: "d.aiff", Detail: "decode failed: unsupported codec", Delta: ""},
			{Path: `C:\m\e.flac`, St: "FIX", StLow: "fix", Title: "e.flac", Detail: "new marker", Delta: "-12 ms"},
		},
		Empty: "No results",
	}
}

func libTFResFixture() libTFResSt {
	return libTFResSt{
		Eyebrow: "Maintenance", Title: "Fix tags", Desc: "Repairs read from the files themselves",
		CloseLbl: "Close", ApplyLbl: "Apply 3 repairs", RescanLbl: "Rescan",
		Hints:   []libHintSt{{Tone: "info", Text: "Writing…"}},
		Skipped: "4 files skipped (changed on disk)",
		Groups: []libTFGrpSt{
			{Title: "ID3v1 only", Badge: "2/3", AllLbl: "All", AllAct: "tf-kind:v1only:on",
				NoneLbl: "None", NoneAct: "tf-kind:v1only:off", Desc: "Upgrade to ID3v2.4",
				Rows: []libTFRowSt{
					{Idx: "0", Checked: true, Path: `C:\m\a.mp3`, Base: "a.mp3", Field: "title", Cur: "—", Proposed: "Rausch"},
					{Idx: "1", Path: `C:\m\b.mp3`, Base: "b.mp3", Field: "artist", Cur: "Unknown", Proposed: "Kollektiv"},
				},
				More: "Showing first 200 of 812"},
			{Title: "Mojibake", Badge: "1/1", AllLbl: "All", AllAct: "tf-kind:mojibake:on",
				NoneLbl: "None", NoneAct: "tf-kind:mojibake:off", Desc: "Repair mis-decoded text",
				Rows: []libTFRowSt{
					{Idx: "2", Checked: true, Path: `C:\m\c.mp3`, Base: "c.mp3", Field: "album", Cur: "GrÃ¶ÃŸer", Proposed: "Größer"},
				}},
		},
	}
}

func libFixResFixtures() map[string]libFixResSt {
	long := strings.Repeat("very-long-detail-", 60)
	escGF := libGFResFixture()
	escGF.Chips = []libChipSt{newChip(`A&ll "x"'`, "", `gf-flt:&"`, true)}
	escGF.Rows = []libGFResRowSt{{Path: `C:\m\a&"b'<>.flac`, St: "FIX", StLow: "fix",
		Title: `A&B <"quoted'>`, Detail: `off by 38 ms &"x"'`, Delta: `+38 ms &"x"'`}}
	longGF := libGFResFixture()
	longGF.Rows = []libGFResRowSt{{Path: strings.Repeat(`C:\deep\`, 50) + "x.flac", St: "ERR", StLow: "err",
		Title: long, Detail: long, Delta: ""}}
	uniGF := libGFResFixture()
	uniGF.Rows = []libGFResRowSt{{Path: `C:\Музыка\трек.flac`, St: "FIX", StLow: "fix",
		Title: "Кириллица + 中文 🎛️", Detail: "смещение 38 мс", Delta: "132.00 → 132.04 BPM"}}

	escTF := libTFResFixture()
	escTF.Title = `Fix &tags <"now">'`
	escTF.Skipped = `4 &skipped "x"'`
	escTF.Hints = []libHintSt{{Tone: "bad", Text: `write failed: a&"b'<c>`}}
	escTF.Groups = []libTFGrpSt{{Title: `ID3v1 &only "x"'`, Badge: `1/2`, AllLbl: `A&ll`,
		AllAct: `tf-kind:v1&"only":on`, NoneLbl: "None", NoneAct: "tf-kind:v1only:off",
		Desc: `Up&grade <"v2.4">'`,
		Rows: []libTFRowSt{{Idx: "7", Checked: true, Path: `C:\m\a&"b'<>.mp3`, Base: `a&"b'<>.mp3`,
			Field: `ti&tle"`, Cur: `—&"'`, Proposed: `Rau&sch <"x">'`}}}}

	return map[string]libFixResSt{
		"gf":         {Kind: libFixResGF, GF: libGFResFixture()},
		"gfEmpty":    {Kind: libFixResGF, GF: libGFResSt{Chips: libGFResFixture().Chips, IsEmpty: true, Empty: "No results"}},
		"gfEscaping": {Kind: libFixResGF, GF: escGF},
		"gfLong":     {Kind: libFixResGF, GF: longGF},
		"gfUnicode":  {Kind: libFixResGF, GF: uniGF},
		"tf":         {Kind: libFixResTF, TF: libTFResFixture()},
		"tfScanning": {Kind: libFixResTF, TF: libTFResSt{Eyebrow: "Maintenance", Title: "Fix tags",
			Desc: "Repairs read from the files themselves", Scanning: true,
			Pct: progressPct(0.31), ScanCap: "744 / 2312", CloseLbl: "Close"}},
		"tfClean": {Kind: libFixResTF, TF: libTFResSt{Eyebrow: "Maintenance", Title: "Fix tags",
			Desc: "d", CloseLbl: "Close", ApplyLbl: "Apply 0 repairs", RescanLbl: "Rescan",
			IsEmpty: true, Empty: "Tags are clean"}},
		"tfEscaping": {Kind: libFixResTF, TF: escTF},
		"tfUnicode": {Kind: libFixResTF, TF: libTFResSt{Eyebrow: "Обслуживание", Title: "Исправить теги 🎛️",
			Desc: "Читает сами файлы", CloseLbl: "Закрыть", ApplyLbl: "Применить 2", RescanLbl: "Пересканировать",
			Groups: []libTFGrpSt{{Title: "Мохибейк", Badge: "1/1", AllLbl: "Все", AllAct: "tf-kind:mojibake:on",
				NoneLbl: "Нет", NoneAct: "tf-kind:mojibake:off", Desc: "Исправить текст",
				Rows: []libTFRowSt{{Idx: "0", Checked: true, Path: `C:\Музыка\трек.mp3`, Base: "трек.mp3",
					Field: "альбом", Cur: "GrÃ¶ÃŸer", Proposed: "Größer"}}}}}},
	}
}

func TestZigLibFixResultsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libFixResFixtures() {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixResults(stateJSON(st))
			if !ok {
				t.Fatal("zig results render failed")
			}
			assertBytesEqual(t, "results", libFixResHTML(st), zig)
		})
	}
}

// ── per-track tag editor ──

func libTagEdFixture() libTagEdSt {
	st := libTagEdSt{Open: true, Desc: "Writes the file, revertible", SaveLbl: "Save", CancelLbl: "Cancel"}
	for _, f := range []struct{ label, field, val string }{
		{"Title", "title", "Rausch"}, {"Artist", "artist", "Kollektiv"},
		{"Album", "album", "Größer"}, {"Genre", "genre", "Techno"},
		{"Label", "label", "Ostgut"}, {"Year", "year", "2026"}, {"Rating", "rating", "4"},
	} {
		st.Fields = append(st.Fields, newPBField(f.label, "tf-edit:"+f.field, f.val, "text", ""))
	}
	return st
}

func TestZigLibFixTagEditGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range map[string]libTagEdSt{
		"closed": {OpenLbl: "Edit tags"},
		"open":   libTagEdFixture(),
		"escaping": {Open: true, Desc: `Writes the &file <"revertible">'`,
			Fields:  []libPBFieldSt{newPBField(`Ti&tle "x"'`, "tf-edit:title", `Rau&sch <"mix">'`, "text", "")},
			SaveLbl: `Sa&ve"`, CancelLbl: `Can&cel'`},
		"unicode": {Open: true, Desc: "Пишет в файл", SaveLbl: "Сохранить", CancelLbl: "Отмена",
			Fields: []libPBFieldSt{newPBField("Название", "tf-edit:title", "Трек 🎧", "text", "")}},
		"long": {Open: true, Desc: strings.Repeat("desc ", 200),
			Fields:  []libPBFieldSt{newPBField("Title", "tf-edit:title", strings.Repeat("t", 4000), "text", "")},
			SaveLbl: "Save", CancelLbl: "Cancel"},
	} {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixTagEdit(stateJSON(st))
			if !ok {
				t.Fatal("zig tag-editor render failed")
			}
			assertBytesEqual(t, "tagedit", libTagEdHTML(st), zig)
		})
	}
}

// ── "works well together" ──

func libCompatFixture() libCompatSecSt {
	return libCompatSecSt{
		OpenLbl: "Open", FindLbl: "Find compatible tracks", FindAct: `lib-compat-find:C:\m\a.flac`,
		Rows: []libCompatRowSt{
			{Title: "Kollektiv - Rausch", Sub: "Blend", Act: `lib-compat-go:C:\m\b.flac`},
			{Title: "Second - Drop", Sub: "Blend · Double drop", Act: `lib-compat-go:C:\m\c.flac`},
			{Title: "Third", Sub: "Energy", Act: `lib-compat-go:C:\m\d.flac`},
		},
	}
}

func TestZigLibFixCompatGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range map[string]libCompatSecSt{
		"empty":   {IsEmpty: true, Empty: "No marks yet", OpenLbl: "Open", FindLbl: "Find", FindAct: "lib-compat-find:p"},
		"loading": {IsEmpty: true, Empty: "Loading…", OpenLbl: "Open", FindLbl: "Find", FindAct: "lib-compat-find:p"},
		"marked":  libCompatFixture(),
		"escaping": {OpenLbl: `O&pen "x"'`, FindLbl: `Fi&nd <"partners">'`, FindAct: `lib-compat-find:C:\m\a&"b'.flac`,
			Rows: []libCompatRowSt{{Title: `A&B <"quoted'>`, Sub: `Ble&nd · Do"uble`, Act: `lib-compat-go:C:\m\b&"c'.flac`}}},
		"unicode": {OpenLbl: "Открыть", FindLbl: "Найти совместимые", FindAct: "lib-compat-find:x",
			Rows: []libCompatRowSt{{Title: "Кириллица 中文 🎛️", Sub: "Смешение · Двойной дроп", Act: "lib-compat-go:y"}}},
		"long": {OpenLbl: "Open", FindLbl: "Find", FindAct: "lib-compat-find:x",
			Rows: []libCompatRowSt{{Title: strings.Repeat("long-title-", 60), Sub: strings.Repeat("kind · ", 60), Act: "lib-compat-go:y"}}},
	} {
		t.Run(name, func(t *testing.T) {
			zig, ok := zigui.RenderLibFixCompat(stateJSON(st))
			if !ok {
				t.Fatal("zig compat render failed")
			}
			assertBytesEqual(t, "compat", libCompatSecHTML(st), zig)
		})
	}
}
