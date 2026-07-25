//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Library golden gate: the Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states (full tab + the #lib-body / #lib-detail / #lib-queue-body /
// #ce-cell-<hash> patch fragments).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func libSel1() selState {
	return selState{ID: "libkind", CurLabel: "All", Rows: []selRow{
		{Val: "ALL", Label: "All", Cur: true}, {Val: "AUDIO", Label: "Audio"},
	}}
}

func libSelOpen() selState {
	return selState{ID: "libsort", Label: "Sort", CurLabel: "Modified", Open: true, Filter: "mo", Rows: []selRow{
		{Val: "Modified", Label: "Modified", Cur: true},
	}}
}

func libMenuSel(id, label string) selState {
	return selState{ID: id, CurLabel: label, Rows: []selRow{
		{Val: "", Label: label, Cur: true},
		{Val: "lib-pl-rename:7", Label: "Rename", Sub: ""},
		{Val: "lib-pl-del:7", Label: "Delete"},
	}}
}

func libPlActs() libPlActSt {
	return libPlActSt{
		Btns: []uiBtn{
			{Label: "View in Collection", Variant: "primary", Act: "lib-plgoto:7"},
			{Label: "Open in cue editor", Variant: "outline", Act: "ce-open-pl:7"},
		},
		Menu: libMenuSel("plmenu-7", "⋯ More"),
	}
}

func libDetailFixture() libDetailSt {
	return libDetailSt{
		Kind: libDetailSel, Eyebrow: "Selected", Title: "Kollektiv - Rausch", Sub: "8.2 MB · AUDIO · 2026-07-01 12:00",
		ActionsTitle: "Actions",
		ActBtns: []uiBtn{
			{Label: "Open", Variant: "outline", Act: "lib-openext:C:\\m\\a.flac"},
			{Label: "Reveal", Variant: "outline", Act: "lib-reveal:C:\\m\\a.flac"},
			{Label: "Copy path", Variant: "ghost", Act: "copy"},
		},
		HasPlayer: true, PlayerTitle: "Player", Player: `<div id=mp-library class=mp><canvas></canvas></div>`,
		HasEnc: true, EncTitle: "Encoding", Enc: libEncFixture(),
		HasHarm: true, HarmTitle: "Harmonic", Harm: libHarmSt{
			Desc: "Pick a compatible key", Wheel: `<svg class=kwheel viewBox="0 0 260 260"><path d="M1 2"/></svg>`,
			SameLbl: "Same", RelLbl: "Relative", ShowLbl: "Show matches", ShowAct: "lib-key-harmonic:8A",
			ClearLbl: "Clear",
		},
		HasTags: true, TagsTitle: "Tags", TagsDesc: "Write library metadata into the file",
		WriteLbl: "Write tags", WriteAct: "lib-tags-write:C:\\m\\a.flac",
		RevertLbl: "Revert", RevertAct: "lib-tags-revert:C:\\m\\a.flac",
		TagEditor: `<div class=tf-ed><input class=field-input></div>`,
		HasPls:    true, PlsTitle: "Playlists",
		Pls: libTrackPlsSt{
			Chips:  []libChipSt{{Label: "Warmup", Act: "lib-plgoto:7"}},
			AddLbl: "Add to playlist", AddAct: "lib-track-addto:C:\\m\\a.flac",
			EmptyText: "Not in a playlist",
		},
		HasCompat: true, CompatTitle: "Works well together", Compat: `<div class=cmp><span>2 marks</span></div>`,
		DetailsTitle: "Details",
		Meta:         []uiKV{newKV("Path", "C:\\m\\a.flac"), newKV("BPM", "128")},
	}
}

func libEncFixture() libEncSt {
	return libEncSt{
		Preset: selState{ID: "lib-preset", Label: "Preset", CurLabel: "FLAC master", Rows: []selRow{
			{Val: "flac", Label: "FLAC master", Sub: "Lossless", Badge: "FLAC", Cur: true},
		}},
		Desc:      "Lossless archive copy",
		Hints:     []libHintSt{{Tone: "info", Text: "Source: FLAC 2ch 44100Hz 900k"}},
		AudioOnly: true,
		Container: libSelTip{
			Sel:   selState{ID: "lib-pf-container", CurLabel: "flac", Rows: []selRow{{Val: "flac", Label: "FLAC", Cur: true}}},
			Label: `<span class=ss-label>Container<span class=tip data-topic=enc-container>?</span></span>`,
		},
		AudioCodec: libSelTip{
			Sel:   selState{ID: "lib-pf-acodec", CurLabel: "flac", Rows: []selRow{{Val: "flac", Label: "FLAC", Cur: true}}},
			Label: `<span class=ss-label>Audio codec</span>`,
		},
		AudioBitrate: newPBField("Audio bitrate", "lib-pf:abitratek", "0", "number", "Opus caps at 510k"),
		Channels:     selState{ID: "lib-pf-channels", Label: "Channels", CurLabel: "Source", Rows: []selRow{{Val: "0", Label: "Source", Cur: true}}},
		SampleRate:   selState{ID: "lib-pf-samplerate", Label: "Sample rate", CurLabel: "Source", Rows: []selRow{{Val: "0", Label: "Source", Cur: true}}},
		Loudness:     `<div class="pb-grp"><label class=row data-label="normalize"></label></div>`,
		TrimStart:    newPBField("Trim start", "lib-trim-s", "", "number", ""),
		TrimEnd:      newPBField("Trim end", "lib-trim-e", "", "number", ""),
		OutputNote:   "Output lands beside the source",
		StartLbl:     "Start", SaveLbl: "Save preset", SaveAsLbl: "Save as new",
	}
}

// libEncVideoFixture exercises the video half (audioOnly=false).
func libEncVideoFixture() libEncSt {
	st := libEncFixture()
	st.AudioOnly = false
	st.Video = libEncVideoSt{
		VCodec: libSelTip{
			Sel:   selState{ID: "lib-pf-vcodec", CurLabel: "H.264", Rows: []selRow{{Val: "h264", Label: "H.264", Cur: true}}},
			Label: `<span class=ss-label>Video codec<span class=tip>?</span></span>`,
		},
		Accel:      selState{ID: "lib-pf-accel", Label: "Acceleration", CurLabel: "none", Rows: []selRow{{Val: "none", Label: "none", Cur: true}}},
		QualityLbl: "Quality profile",
		Profiles: []libChipSt{
			{Label: "streaming", Val: "streaming", Act: "lib-pf:profile"},
			{Label: "master", Val: "master", Act: "lib-pf:profile"},
		},
		ProfileHint: "custom settings",
		RateMode: libSelTip{
			Sel:   selState{ID: "lib-pf-ratemode", CurLabel: "CRF", Rows: []selRow{{Val: "crf", Label: "CRF", Cur: true}}},
			Label: `<span class=ss-label>Rate mode</span>`,
		},
		RateField: newPBField("CRF", "lib-pf:crf", "20", "number", "lower = better"),
		Res:       selState{ID: "lib-pf-res", Label: "Resolution", CurLabel: "1080p", Rows: []selRow{{Val: "1080", Label: "1080p", Cur: true}}},
		FPS:       newPBField("FPS", "lib-pf:fps", "30", "number", ""),
	}
	return st
}

func libTabs() []libTabSt {
	return []libTabSt{
		{"browse", "Browse"}, {"favorites", "Favorites"}, {"collection", "Collection"},
		{"playlists", "Playlists"}, {"history", "History"}, {"idmarks", "ID marks"},
		{"queue", "Queue"}, {"presets", "Presets"},
	}
}

// libFixtures: empty, unavailable (DB missing), populated tri-pane, escaping edge, long
// values, unicode, plus one fixture per remaining section (playlists/history/idmarks/queue/
// presets/favorites/browse-grid/embedded-mirror/cue-edit).
func libFixtures() map[string]libState {
	base := func(body libBodySt) libState {
		return libState{Title: "Library", NavTitle: "Local files + collection", Section: "browse",
			Tabs: libTabs(), Body: body}
	}

	emptyBrowse := libBodySt{Kind: libBodyBrowse, NavRail: `<div class=libnav></div>`,
		Browse: libBrowseSt{
			Up: "Up", UpPath: "C:\\", Goto: "Go to…", FilterPH: "Filter by name",
			KindLbl: "Kind", Kind: libSel1(), SortLbl: "Sort", Sort: libSelOpen(),
			ListLbl: "List", GridLbl: "Grid", KeyChip: newChip("Key", "", "lib-key-clear", false),
			Folder: libMenuSel("libfoldermenu", "📁 Folder"), SelAllTitle: "Select all",
			Count: "0 items", Crumbs: []libSegSt{{Label: "C:", Path: "C:"}},
		},
		Detail: libDetailSt{Kind: libDetailMsg, Msg: "Nothing selected"}}

	// populated tri-pane: collection rows + full inspector
	coll := libCollSt{
		ImportLbl: "Import", DJSyncLbl: "DJ sync", GridFix: true, GridFixLbl: "Fix beatgrids",
		MoreLbl: "Maintenance", MoreOpen: true,
		MoreItems: []libTabSt{{"lib-scan", "Scan folder"}, {"lib-cleanup", "Clean up"}},
		Search:    "rausch", SearchPH: "Search title or artist",
		Genre:      selState{ID: "libfacet-genre", CurLabel: "Genre (1)", Rows: []selRow{{Val: "Techno", Label: "✓ Techno", Badge: "212"}}},
		Label:      selState{ID: "libfacet-label", CurLabel: "Label", Rows: []selRow{{Val: "Ostgut", Label: "Ostgut", Badge: "9"}}},
		HasPlFacet: true,
		PlFacet:    selState{ID: "libfacet-pl", CurLabel: "Playlist (1)", Rows: []selRow{{Val: "7", Label: "✓ Warmup", Badge: "18"}}},
		KeyChip:    newChip("Key (2)", "", "lib-key-clear", true),
		NoDropsLbl: "No drops", NoDrops: true, Clear: true, ClearLbl: "Clear",
		Prep:      `<div class=prep-sel></div>`,
		Chips:     []libChipSt{newChip("Techno ×", "", "lib-genre:Techno", true), newChip("Warmup ×", "", "lib-plfilter:7", true)},
		HasInline: true, Inline: libPlActs(),
		Head: libCollHeadSt{SelAllTitle: "Select all", SelAllOn: false,
			Main:    libCollHdrSt{Cls: "trk-hmain", Key: "Artist", Label: "3 tracks", Arrow: " ▲"},
			CueLbl:  "Cues",
			BPM:     libCollHdrSt{Cls: "trk-bpm", Key: "BPM", Label: "BPM"},
			TimeLbl: "Time",
			Key:     libCollHdrSt{Cls: "trk-keyh", Key: "Key", Label: "Key"},
		},
		Rows: []libCollRowSt{
			{Path: "C:\\m\\a.flac", Checked: true, SelCls: " sel ce-marked", Title: "Kollektiv - Rausch",
				Sub: "Techno · Ostgut · 1411k · ★★★★", Verified: true, CellID: "ce-cell-1a2b3c4d",
				Cue: libCueCellSt{Drops: 2, DropsTitle: "2 drops", NoDropsTitle: "no drops", Cues: 4, CuesTitle: "4 cues", NoCuesTitle: "no cues"},
				BPM: "128", Dur: "6:12", Key: libKeyPillSt{Text: "8A", Cls: " k-same", Ok: true}},
			{Path: "C:\\m\\b.mp3", Warn: true, Title: "b.mp3", Sub: "House",
				CellID: "ce-cell-deadbeef",
				Cue:    libCueCellSt{NoDropsTitle: "no drops", NoCuesTitle: "no cues"},
				Key:    libKeyPillSt{Text: "weird-key"}},
			{Path: "C:\\m\\c.wav", Title: "c.wav", CellID: "ce-cell-00000001",
				Cue: libCueCellSt{Cues: 1, CuesTitle: "1 cue", NoDropsTitle: "no drops"},
				Dur: "3:00"},
		},
		VerifiedTitle: "Verified grid", Empty: "No tracks", More: "Showing first 300 of 1200",
		Batch: libBatchSt{On: true, Count: "1 selected", Btns: []uiBtn{
			{Label: "Add to playlist", Variant: "outline", Act: "lib-addto"},
			{Label: "Clear", Variant: "ghost", Act: "lib-collsel-clear"},
		}},
	}
	populated := base(libBodySt{Kind: libBodyColl, NavRail: `<div class=libnav><div class=libnav-hd>Collection</div></div>`,
		Coll: coll, Detail: libDetailFixture()})
	populated.Section = "collection"
	populated.Switcher = `<div class=lib-target><div class=ss-field></div></div>`

	// cue-edit mode: full-width waveform above the panes
	cueEdit := base(libBodySt{Kind: libBodyColl, NavRail: `<div class=libnav></div>`, CEFull: true,
		CEWave: `<div id=ce-wave><canvas></canvas></div>`, Coll: coll,
		Detail: libDetailSt{Kind: libDetailRaw, Raw: `<div id=ce-rail>editor</div>`}})
	cueEdit.Section = "collection"

	// collection with a fixer results view replacing the list
	results := base(libBodySt{Kind: libBodyColl, NavRail: "", Coll: func() libCollSt {
		r := coll
		r.HasResults, r.Results = true, `<div class=gf-res><span>42 fixed</span></div>`
		return r
	}(), Detail: libDetailSt{Kind: libDetailRaw, Raw: `<div id=gf-rail></div>`}})
	results.Section = "collection"

	unavailable := base(libBodySt{Kind: libBodyColl, Coll: libCollSt{Msg: "Library database unavailable"},
		Detail: libDetailSt{Kind: libDetailMsg, Msg: "Nothing selected"}})
	unavailable.Section = "collection"

	loading := base(libBodySt{Kind: libBodyMsg, Msg: "Loading collection…"})

	embedded := libState{Title: "Library", NavTitle: "sub", Embedded: true,
		Switcher: `<div class=lib-target></div>`,
		Body:     libBodySt{Kind: libBodyRaw, Raw: `<div class=mirror>peer view</div>`}}

	grid := base(libBodySt{Kind: libBodyBrowse, NavRail: `<div class=libnav></div>`,
		Browse: libBrowseSt{
			Up: "Up", UpPath: "C:\\m", Goto: "Go to…", Filter: "ra", FilterPH: "Filter by name",
			KindLbl: "Kind", Kind: libSel1(), SortLbl: "Sort", Sort: libSel1(),
			ListLbl: "List", GridLbl: "Grid", Grid: true,
			KeyChip: newChip("Key", "", "lib-key-clear", false),
			Folder:  libMenuSel("libfoldermenu", "📁 Folder"),
			SelAll:  true, SelAllOn: true, SelAllTitle: "Select all", Count: "2 items",
			Crumbs:   []libSegSt{{Label: "C:", Path: "C:"}, {Label: "m", Path: "C:\\m"}},
			HasBound: true, BoundNote: "Bound to playlist Warmup", BoundActs: libPlActs(),
			Entries: []libFeSt{
				{Name: "sets", Path: "C:\\m\\sets", IsDir: true, Glyph: "📁", GridSub: "Folder"},
				{Name: "a.flac", Path: "C:\\m\\a.flac", Glyph: "🎵", GridSub: "8.2 MB · AUDIO",
					Sub: "8.2 MB · 2026-07-01", Checked: true, Sel: true, Key: libKeyPillSt{Text: "8A", Ok: true}},
			},
			More: "Showing first 300 of 900",
			Batch: libBatchSt{On: true, Count: "2 selected", Btns: []uiBtn{
				{Label: "Waveforms", Variant: "outline", Act: "lib-batch-run:peaks"},
				{Label: "Mark compatible", Variant: "outline", Act: "lib-compat-mark:browse"},
			}},
		},
		Detail: libDetailFixture()})

	listBrowse := grid
	lb := listBrowse.Body
	b2 := lb.Browse
	b2.Grid = false
	lb.Browse = b2
	listBrowse.Body = lb

	fav := base(libBodySt{Kind: libBodyFav, Fav: libFavSt{
		Desc: "Pinned folders", Empty: "Nothing pinned", OpenLbl: "Open", UnpinLbl: "Unpin",
		Rows: []libFavRowSt{{Label: "sets", Path: "C:\\m\\sets"}, {Label: "promo", Path: "D:\\promo"}},
	}})
	fav.Section = "favorites"

	pls := base(libBodySt{Kind: libBodyPls, Pls: libPlsSt{
		NewLbl: "New playlist", NewSmartLbl: "New smart playlist", HasCloud: true,
		Cloud: libMenuSel("plcloudmenu", "☁ Cloud"), Empty: "No playlists",
		Rows: []libPlRowSt{
			{ID: "7", Icon: "🎵", Name: "Warmup", Sub: "manual · 18 tracks", Sel: true},
			{ID: "8", Icon: "⚡", Name: "Peak", Sub: "42 match · BPM 128-134"},
			{ID: "9", Icon: "⤓", Name: "Imported", Sub: "imported · 3 tracks"},
		},
		HasOpen: true, Open: libPlOpenSt{
			Title: "Warmup", SmartNote: "", Acts: libPlActs(), Empty: "Empty playlist",
			Items: []libPlItemSt{
				{Pos: "1", Idx: "0", Path: "C:\\m\\a.flac", Title: "Kollektiv - Rausch", Manual: true,
					Key: libKeyPillSt{Text: "8A", Cls: " k-up", Ok: true}},
				{Pos: "2", Idx: "1", Path: "", Title: "--- divider ---", Manual: true},
			},
		},
	}, Detail: libDetailSt{Kind: libDetailMsg, Msg: "Nothing selected"}})
	pls.Section = "playlists"

	hist := base(libBodySt{Kind: libBodyHist, Hist: libHistSt{
		LoadLbl: "Load history", Src: libSel1(), Desc: "Sessions from your DJ software",
		Empty: "No sessions", Sessions: []libSessSt{
			{Idx: "0", Date: "2026-07-01 22:10", Sub: "Traktor · 34 tracks · 2h 05m", Sel: true},
			{Idx: "1", Date: "2026-06-28 23:00", Sub: "Rekordbox · 12 tracks · 48m"},
		},
		HasPlayed: true, PlayedLbl: "Played", SortLbl: "Sort", Sort: libSel1(), DirLbl: "Ascending",
		Played: []libPlayedSt{
			{Path: "C:\\m\\a.flac", Title: "Kollektiv - Rausch", Meta: "128 BPM · 6:12 · Techno"},
			{Path: "C:\\m\\gone.mp3", Warn: true, Title: "- - -", Meta: "", Key: libKeyPillSt{Text: "3B", Ok: true}},
		},
	}, Detail: libDetailSt{Kind: libDetailMsg, Msg: "Nothing selected"}})
	hist.Section = "history"

	idm := base(libBodySt{Kind: libBodyIDMarks, IDM: libIDMSt{
		MarkFileLbl: "Mark file", MarkFolderLbl: "Mark folder", TypePathLbl: "Type a path",
		Desc: "Hide track IDs on stream", Empty: "Nothing marked",
		ArtistLbl: "Show artist", ArtistDL: "show artist", LabelLbl: "Show label", LabelDL: "show label",
		RemoveLbl: "Remove",
		Rows: []libIDMRowSt{
			{Path: "C:\\m\\id.flac", Artist: true, ArtistAct: "lib-id-artist:C:\\m\\id.flac",
				LabelAct: "lib-id-label:C:\\m\\id.flac", DelAct: "lib-id-del:C:\\m\\id.flac"},
		},
	}})
	idm.Section = "idmarks"

	queue := base(libBodySt{Kind: libBodyQueue, Queue: libQueueFixture()})
	queue.Section = "queue"

	presets := base(libBodySt{Kind: libBodyPresets, Presets: libPresetsSt{
		NewLbl: "New preset", YoursTitle: "Your presets", EmptyCustom: "No custom presets",
		BuiltinsTitle: "Built-ins", CustomBadge: "custom", BuiltinBadge: "built-in",
		EditLbl: "Edit", DupLbl: "Duplicate", DelLbl: "Delete", DupEditLbl: "Duplicate & edit",
		Custom:   []libPresetSt{{ID: "mine", Label: "My AAC", Desc: "256k AAC for the club USB"}},
		Builtins: []libPresetSt{{ID: "flac", Label: "FLAC master", Desc: "Lossless archive copy"}},
	}})
	presets.Section = "presets"

	// escaping edge: quotes/ampersands/apostrophes in every dynamic slot
	esc := populated
	esc.Title = `Lib&rary <"local">`
	esc.NavTitle = `a&b<c>"d"'e'`
	escColl := coll
	escColl.Search = `ra"usch & <mix>'`
	escColl.SearchPH = `Search &<"title">'`
	escColl.Chips = []libChipSt{newChip(`Te&chno "×"`, "", `lib-genre:Te&chno`, true)}
	escColl.Head.Main = libCollHdrSt{Cls: "trk-hmain", Key: "Artist", Label: `3 &tracks <"x">`, Arrow: " ▼"}
	escColl.VerifiedTitle = `Verified &"grid"'<>`
	escColl.Rows = []libCollRowSt{
		{Path: `C:\m\a&"b'<>.flac`, Checked: true, SelCls: " sel", Title: `A&B <"quoted'>`,
			Sub: `Te&chno · O"stgut`, Verified: true, CellID: "ce-cell-ffffffff",
			Cue: libCueCellSt{Drops: 1, DropsTitle: `1 &drop "x"`, Cues: 0, NoCuesTitle: `no &cues'`},
			BPM: "128", Dur: "6:12", Key: libKeyPillSt{Text: `we&ird"<>'`}},
	}
	escColl.Batch = libBatchSt{On: true, Count: `1 &selected "x"`, Btns: []uiBtn{
		{Label: `Add &<to>"list"'`, Variant: "outline", Act: `lib-addto:a&"b'`},
	}}
	escDetail := libDetailFixture()
	escDetail.Title = `Kollektiv & "Rausch" <mix>'`
	escDetail.Sub = `8.2 MB · A&UDIO "x"`
	escDetail.Eyebrow = `Sel&ected'<">`
	escDetail.Missing = `File is &missing "on disk"'`
	escDetail.ActBtns = []uiBtn{{Label: `O&pen "ext"'`, Variant: "outline", Act: `lib-openext:C:\m\a&"b'.flac`}}
	escDetail.Meta = []uiKV{newKV(`P&ath"`, `C:\m\a&"b'<>.flac`)}
	escDetail.Enc = libEncVideoFixture()
	escDetail.Enc.Desc = `Loss&less "archive"'`
	escDetail.Enc.Hints = []libHintSt{{Tone: "warn", Text: `Upscale 1280×720 → 1920×1080 &"x"'`}}
	escDetail.Enc.TrimStart = newPBField(`Tr&im "start"'`, "lib-trim-s", `1&"2'`, "number", `hi&nt "x"'`)
	escDetail.Harm.SameLbl = `Sa&me "key"'`
	escDetail.Pls.Chips = []libChipSt{{Label: `W&armup "×"'`, Act: `lib-plgoto:7`}}
	esc.Body.Coll = escColl
	esc.Body.Detail = escDetail

	// long values
	long := populated
	longS := strings.Repeat("very-long-track-title-", 60)
	longColl := coll
	longColl.Rows = []libCollRowSt{
		{Path: strings.Repeat("C:\\deep\\", 50) + "x.flac", Title: longS, Sub: longS,
			CellID: "ce-cell-11111111", Cue: libCueCellSt{Drops: 999, DropsTitle: longS, Cues: 999, CuesTitle: longS},
			BPM: "128", Dur: "612:12", Key: libKeyPillSt{Text: "12B", Cls: " k-down", Ok: true}},
	}
	longColl.More = strings.Repeat("Showing ", 100)
	long.Body.Coll = longColl
	longDetail := libDetailFixture()
	longDetail.Title = longS
	longDetail.Player = "<div>" + strings.Repeat("w", 5000) + "</div>"
	longDetail.Enc = libEncVideoFixture()
	long.Body.Detail = longDetail

	// unicode
	uni := populated
	uni.Title = "ライブラリ 🎧"
	uni.NavTitle = "größer Библиотека"
	uniColl := coll
	uniColl.Rows = []libCollRowSt{
		{Path: "C:\\Музыка\\сеты\\трек.flac", Title: "Кириллица + 中文 🎛️", Sub: "Техно · Лейбл · ★★★",
			CellID: "ce-cell-22222222", Cue: libCueCellSt{Drops: 3, DropsTitle: "3 дропа", Cues: 2, CuesTitle: "2 кью"},
			BPM: "132", Dur: "7:07", Key: libKeyPillSt{Text: "5A", Cls: " k-rel", Ok: true}},
	}
	uniColl.Chips = []libChipSt{newChip("Техно ×", "", "lib-genre:Техно", true)}
	uni.Body.Coll = uniColl
	uniDetail := libDetailFixture()
	uniDetail.Title = "Кириллица + 中文 🎛️"
	uniDetail.Meta = []uiKV{newKV("Путь", "C:\\Музыка\\трек.flac"), newKV("キー", "5A")}
	uni.Body.Detail = uniDetail

	return map[string]libState{
		"empty":       base(emptyBrowse),
		"unavailable": unavailable,
		"loading":     loading,
		"populated":   populated,
		"cueEdit":     cueEdit,
		"results":     results,
		"embedded":    embedded,
		"browseGrid":  grid,
		"browseList":  listBrowse,
		"favorites":   fav,
		"playlists":   pls,
		"history":     hist,
		"idmarks":     idm,
		"queue":       queue,
		"presets":     presets,
		"escaping":    esc,
		"long":        long,
		"unicode":     uni,
	}
}

func libQueueFixture() libQueueSt {
	return libQueueSt{
		Desc: "Transcode jobs", Empty: "Queue is empty",
		Jobs: []libJobSt{
			{Label: "a.flac · FLAC master", Cancel: true, CancelLbl: "Cancel", CancelAct: "lib-job-cancel:0",
				Status: "running", StatusVar: "info", Width: pbarPctOf(0.42), Caption: "running · 42%"},
			{Label: `b&"c".mp3 · My AAC`, Status: "error", StatusVar: "error",
				Width: pbarPctOf(1.5), Caption: "error · 100%", Msg: `ffmpeg exited 1: bad &<codec>"x"'`},
			{Label: "c.wav · queued", Cancel: true, CancelLbl: "Cancel", CancelAct: "lib-job-cancel:2",
				Status: "queued", StatusVar: "info", Width: pbarPctOf(-0.2), Caption: "queued · 0%"},
		},
	}
}

func TestZigLibraryGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderLibrary(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", libraryHTML(st), zig)

			bjs := stateJSON(st.Body)
			if bjs == nil {
				t.Fatal("body marshal failed")
			}
			zigBody, ok := zigui.RenderLibraryBody(bjs)
			if !ok {
				t.Fatal("zig body render failed")
			}
			assertBytesEqual(t, "body", libBodyHTML(st.Body), zigBody)

			djs := stateJSON(st.Body.Detail)
			if djs == nil {
				t.Fatal("detail marshal failed")
			}
			zigDet, ok := zigui.RenderLibraryDetail(djs)
			if !ok {
				t.Fatal("zig detail render failed")
			}
			assertBytesEqual(t, "detail", libDetailHTMLOf(st.Body.Detail), zigDet)
		})
	}
}

// TestZigLibraryQueueGolden covers the #lib-queue-body fragment on its own (its patch path
// renders the inner HTML without the section wrapper).
func TestZigLibraryQueueGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range map[string]libQueueSt{
		"empty":     {Desc: "Transcode jobs", Empty: "Queue is empty"},
		"populated": libQueueFixture(),
		"unicode":   {Desc: "Задания", Empty: "пусто", Jobs: []libJobSt{{Label: "трек.flac · мастер", Status: "done", StatusVar: "success", Width: pbarPctOf(1), Caption: "done · 100%"}}},
	} {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			zig, ok := zigui.RenderLibraryQueue(js)
			if !ok {
				t.Fatal("zig queue render failed")
			}
			assertBytesEqual(t, "queue", libQueueBodyHTML(st), zig)
		})
	}
}

// TestZigLibraryCueCellGolden covers the per-row #ce-cell-<hash> drop-toggle patch target.
func TestZigLibraryCueCellGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range map[string]libCueCellSt{
		"none":     {NoDropsTitle: "no drops", NoCuesTitle: "no cues"},
		"both":     {Drops: 2, DropsTitle: "2 drops", Cues: 4, CuesTitle: "4 cues"},
		"escaping": {Drops: 1, DropsTitle: `1 &drop "x"'<>`, NoCuesTitle: `no &cues"'<>`},
		"unicode":  {Drops: 3, DropsTitle: "3 дропа 🎛️", Cues: 1, CuesTitle: "1 кью"},
	} {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			zig, ok := zigui.RenderLibraryCueCell(js)
			if !ok {
				t.Fatal("zig cue-cell render failed")
			}
			assertBytesEqual(t, "cuecell", libCueCellHTMLOf(st), zig)
		})
	}
}
