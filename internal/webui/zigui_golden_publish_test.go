//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Publish golden gate: the Zig renderers must be BYTE-IDENTICAL to the Go renderers for
// representative states - local view (full + the #pub-hero tick fragment) and the remote
// peer view. Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// pubMenu mirrors resolveActionMenu's output shape without touching the live ss registry:
// the label rides as the leading empty-Val row and becomes CurLabel. Rows is never nil (a
// nil slice marshals to JSON null, which fails the Zig slice parse).
func pubMenu(id, label string, items ...selRow) selState {
	rows := append([]selRow{{Val: "", Label: label}}, items...)
	return selState{ID: id, CurLabel: label, Rows: rows}
}

func pubCapFx(caption string, loose bool, menuID, more string) pubCapSt {
	st := pubCapSt{Caption: caption, Btns: []uiBtn{}, Menu: pubMenu(menuID, more,
		selRow{Val: "pub-open:c1", Label: "Open externally"},
		selRow{Val: "pub-reveal:c1", Label: "Show in folder"},
		selRow{Val: "pub-capdel:c1", Label: "Remove"},
	)}
	if loose {
		st.Btns = append(st.Btns,
			uiBtn{Label: "Open in player", Variant: "go", Act: "mp-loadcap:c1"},
			uiBtn{Label: "Trim / edit…", Variant: "secondary", Act: "mp-loadcap:c1\x1fedit"})
	}
	return st
}

func pubTrackFx(n int, off, label, lead, tip, path, ctx string, checked bool) pubTrackSt {
	return pubTrackSt{Num: n, Off: off, Label: label, Lead: lead, LeadTip: tip,
		Path: path, Ctx: ctx, Checked: checked}
}

// pubFixtures: unavailable, empty, captures/tracklist populated (incl. the editable
// finished-set flow), escaping edge, long values, unicode.
func pubFixtures() map[string]pubSt {
	hero := func() pubHeroSt {
		return pubHeroSt{
			Show: true,
			Rec:  pubBadgeSt{Key: "REC", DL: "rec", Variant: "muted", Line: "Armed"},
			Cap:  pubBadgeSt{Key: "CAPTURE", DL: "capture", Variant: "info", Line: "Listening on :8000"},
			Obs:  pubBadgeSt{Key: "OBS", DL: "obs", Variant: "warning", Line: "Not connected"},
			NP:   pubNpSt{Label: "Now playing", Title: "Nothing audible"},
		}
	}
	base := func() pubSt {
		return pubSt{
			Title: "Publish", Sub: "Recorded sets, captures and tracklists",
			Switcher:    `<div class=lib-target><div class=ss-field><span class=ss-label>Controlling</span></div></div>`,
			Available:   true,
			Unavailable: "Recorder unavailable",
			Body: pubBodySt{
				Hero: hero(),
				List: pubListSt{Empty: "No recordings yet", Rows: []pubSetRowSt{}},
				Detail: pubDetailSt{CardTitle: "Selected set", Hint: "Pick a set on the left",
					Loose: pubLooseSt{Caps: []pubCapSt{}}},
			},
		}
	}

	unavailable := base()
	unavailable.Available = false
	unavailable.Body = pubBodySt{}

	empty := base()

	// a pinned loose capture in the player, no set selected
	loose := base()
	loose.Body.Detail.Player = `<div class=mp><button class="rp-btn rp-btn--go" data-act="mp-play">Play</button></div>`
	loose.Body.Detail.Loose = pubLooseSt{Count: "2 unlinked captures",
		Desc: "Captures with no matching set", Caps: []pubCapSt{
			pubCapFx("Broadcast audio · OGG · 84.2 MB · set.ogg", true, "capmenu-c1", "⋯ More"),
			pubCapFx("OBS recording · MKV · 1.9 GB · set.mkv", true, "capmenu-c2", "⋯ More"),
		}}

	// live set: hero busy, captures subtab, one linked capture
	captures := base()
	captures.Body.Hero.Rec = pubBadgeSt{Key: "REC", DL: "rec", Variant: "error", Line: "Live set · 7 tracks · 41m12s"}
	captures.Body.Hero.Cap = pubBadgeSt{Key: "CAPTURE", DL: "capture", Variant: "success", Line: "Capturing OGG · 84.2 MB"}
	captures.Body.Hero.Obs = pubBadgeSt{Key: "OBS", DL: "obs", Variant: "error", Line: "Recording 12m03s"}
	captures.Body.Hero.Finish = "Finish set"
	captures.Body.Hero.NP = pubNpSt{Label: "Now playing", Title: "Artist - Title",
		Meta: "Deck A · 128.0 BPM · 4A", State: "confirming in 12s",
		Bar: newPubBar(0.62, "confirming in 12s")}
	captures.Body.Hero.Player = pubPlayerSt{Show: true, Label: "▶ set.ogg", Pos: "1:04 / 41:12",
		Bar: newPubBar(64.0/2472.0, "1:04 / 41:12")}
	captures.Body.List = pubListSt{Empty: "No recordings yet", Count: "3 sets", Rows: []pubSetRowSt{
		{ID: "r1", Title: "⏺ Live set", Sub: "2026-07-25 21:40 · 7 tracks · live · audio", Sel: true, Rename: "Rename…"},
		{ID: "r2", Title: "Warmup", Sub: "2026-07-24 19:00 · 21 tracks · 2h0m0s · audio · video · matched", Rename: "Rename…"},
		{ID: "r3", Title: "Closing", Sub: "2026-07-23 03:00 · 9 tracks · 1h10m0s", Rename: "Rename…"},
	}}
	captures.Body.Detail = pubDetailSt{
		CardTitle: "Selected set", Sel: true, Hint: "Pick a set on the left",
		Name: "Live set", Meta: "2026-07-25 21:40 · 7 tracks · live · audio",
		Actions: []uiBtn{{Label: "Export…", Variant: "outline", Act: "pub-export:r1"}},
		Active:  "captures",
		CapsLbl: "Captures (1)", TracksLbl: "Tracklist (7)",
		Captures: pubCapturesSt{
			Player: `<div class=mp id=mp-publish><div class=mp-wave></div></div>`,
			Empty:  "No captures linked to this set",
			Caps:   []pubCapSt{pubCapFx("Broadcast audio · OGG · 84.2 MB · set.ogg", false, "capmenu-c1", "⋯ More")},
		},
		Loose: pubLooseSt{Caps: []pubCapSt{}},
	}

	// finished set: editable offsets, mixed link states, batch bar + fix-times
	tracklist := captures
	tracklist.Body.Detail.Active = "tracklist"
	tracklist.Body.Detail.Actions = []uiBtn{
		{Label: "Export…", Variant: "outline", Act: "pub-export:r2"},
		{Label: "Match history", Variant: "secondary", Act: "pub-match:r2"},
		{Label: "Delete", Variant: "destructive", Act: "pub-del:r2"},
	}
	tracklist.Body.Detail.Captures = pubCapturesSt{Caps: []pubCapSt{}}
	tracklist.Body.Detail.Tracklist = pubTracklistSt{
		Empty: "No tracks recorded", Editable: true, OffTip: "Edit the start offset (m:ss)",
		Help: "Marked tracks are stored as works-together pairs",
		Rows: []pubTrackSt{
			{Num: 1, Off: "0:00", Label: "A - One", Lead: "chk", Checked: true, Path: `C:\m\a.flac`,
				Ctx: "pub-tctx2:r2\x1f0\x1fC:\\m\\a.flac", OffAct: "pub-toff:r2\x1f0", OffDL: "offset-1"},
			{Num: 2, Off: "4:31", Label: "B - Two", Lead: "chk", Path: `C:\m\b.flac`,
				Ctx: "pub-tctx2:r2\x1f1\x1fC:\\m\\b.flac", OffAct: "pub-toff:r2\x1f1", OffDL: "offset-2"},
			{Num: 3, Off: "1:02:07", Label: "C - Three", Lead: "none", LeadTip: "No library match",
				Ctx: "pub-tctx2:r2\x1f2\x1f", OffAct: "pub-toff:r2\x1f2", OffDL: "offset-3"},
		},
		ShowFix: true, Fix: uiBtn{Label: "Fix start times…", Variant: "outline", Act: "pub-fixtimes:r2"},
		Unres: "1 track has no library match",
		Batch: pubBatchSt{Count: "2 selected", Btns: []uiBtn{
			{Label: "Mark works together", Variant: "primary", Act: "lib-compat-mark:pub"},
			{Label: "Clear", Variant: "ghost", Act: "pub-tsel-clear"},
		}},
	}

	// live set tracklist: not editable, links still resolving, single selection
	resolving := captures
	resolving.Body.Detail.Active = "tracklist"
	resolving.Body.Detail.Captures = pubCapturesSt{Caps: []pubCapSt{}}
	resolving.Body.Detail.Tracklist = pubTracklistSt{
		Empty: "No tracks recorded", Resolving: "Linking to the library…",
		Help: "Marked tracks are stored as works-together pairs",
		Rows: []pubTrackSt{
			pubTrackFx(1, "0:00", "A - One", "resolving", "Linking to the library…", "", "", false),
			pubTrackFx(2, "4:31", "B - Two", "resolving", "Linking to the library…", "", "", false),
		},
		Batch: pubBatchSt{Count: "1 selected", Btns: []uiBtn{
			{Label: "Find compatible", Variant: "outline", Act: `lib-compat-find:C:\m\a.flac`},
			{Label: "Clear", Variant: "ghost", Act: "pub-tsel-clear"},
		}},
	}

	// no tracks at all (hint path) + no captures (hint path)
	bare := base()
	bare.Body.Detail = pubDetailSt{CardTitle: "Selected set", Sel: true, Hint: "Pick a set on the left",
		Name: "Empty set", Meta: "2026-07-20 12:00 · no tracks",
		Actions: []uiBtn{}, Active: "tracklist", CapsLbl: "Captures (0)", TracksLbl: "Tracklist (0)",
		Tracklist: pubTracklistSt{Empty: "No tracks recorded", Rows: []pubTrackSt{}},
		Loose:     pubLooseSt{Caps: []pubCapSt{}}}

	noCaps := bare
	noCaps.Body.Detail.Active = "captures"
	noCaps.Body.Detail.Captures = pubCapturesSt{Empty: "No captures linked to this set", Caps: []pubCapSt{}}

	escaping := captures
	escaping.Title = `P&ublish <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Switcher = `<div class="raw & kept"><b>'unescaped'</b></div>`
	escaping.Unavailable = `no &"recorder"<>'`
	escaping.Body.Hero.Rec = pubBadgeSt{Key: `R&C"<>'`, DL: `r&c"<>'`, Variant: "error", Line: `Live "set" & <7>'`}
	escaping.Body.Hero.NP = pubNpSt{Label: `N&ow"<>'`, Title: `A&B - <"T">'`, Meta: `Deck "A" & 128'`,
		State: `confirming <12s>&"'`, Bar: newPubBar(0.5, `in <12s> & "now"'`)}
	escaping.Body.Hero.Player = pubPlayerSt{Show: true, Label: `▶ s&t"<>'.ogg`, Pos: `1:04 / 41:12 &"'`,
		Bar: newPubBar(0.1, `1:04 / 41:12 &"'`)}
	escaping.Body.Hero.Finish = `F&inish"<>'`
	escaping.Body.List = pubListSt{Empty: "e", Count: `3 &"sets"<>'`, Rows: []pubSetRowSt{
		{ID: `r&1"<>'`, Title: `⏺ L&ive"<>'`, Sub: `2026 &"·"<>'`, Sel: true, Rename: `R&ename"<>'…`},
	}}
	escaping.Body.Detail.Name = `L&ive"<>' set`
	escaping.Body.Detail.Meta = `m&eta"<>'`
	escaping.Body.Detail.Actions = []uiBtn{{Label: `E&xport"<>'…`, Variant: "outline", Act: `pub-export:r&1"`}}
	escaping.Body.Detail.CapsLbl = `C&aptures"<>' (1)`
	escaping.Body.Detail.TracksLbl = `T&racklist"<>' (7)`
	escaping.Body.Detail.Captures = pubCapturesSt{
		Player: `<div class="raw & player"><b>'kept'</b></div>`,
		Empty:  `no &caps"<>'`,
		Caps: []pubCapSt{{Caption: `Broadcast &"audio"<>' · set.ogg`, Btns: []uiBtn{},
			Menu: pubMenu(`capmenu-c&1"`, `⋯ M&ore"<>'`, selRow{Val: `pub-open:c&1"`, Label: `O&pen"<>'`})}},
	}
	escaping.Body.Detail.Loose = pubLooseSt{Count: `2 &"unlinked"<>'`, Desc: `d&esc"<>'`,
		Caps: []pubCapSt{{Caption: `l&oose"<>' · x.ogg`, Btns: []uiBtn{},
			Menu: pubMenu("capmenu-c2", "⋯ More")}}}

	escTracks := escaping
	escTracks.Body.Detail.Active = "tracklist"
	escTracks.Body.Detail.Tracklist = pubTracklistSt{
		Empty: "e", Editable: true, OffTip: `E&dit"<>' the offset`,
		Help: `h&elp"<>'`, Unres: `1 &"unresolved"<>'`,
		Rows: []pubTrackSt{
			{Num: 1, Off: "0:00", Label: `A&B - <"One">'`, Lead: "chk", Checked: true,
				Path: `C:\m\a&b"<>'.flac`, Ctx: "pub-tctx2:r&1\x1f0\x1fC:\\m\\a&b\"<>'.flac",
				OffAct: "pub-toff:r&1\x1f0", OffDL: "offset-1"},
			{Num: 2, Off: "4:31", Label: `c&d"<>'`, Lead: "none", LeadTip: `n&o"<>' match`,
				Ctx: "pub-tctx2:r&1\x1f1\x1f", OffAct: "pub-toff:r&1\x1f1", OffDL: "offset-2"},
		},
		ShowFix: true, Fix: uiBtn{Label: `F&ix"<>'…`, Variant: "outline", Act: `pub-fixtimes:r&1"`},
		Batch: pubBatchSt{Count: `2 &"selected"<>'`, Btns: []uiBtn{
			{Label: `M&ark"<>'`, Variant: "primary", Act: "lib-compat-mark:pub"},
			{Label: `C&lear"<>'`, Variant: "ghost", Act: "pub-tsel-clear"},
		}},
	}
	// non-editable branch keeps the raw data-ctx="pub-tctx:<path>" form
	escTracks2 := escTracks
	escTracks2.Body.Detail.Tracklist.Editable = false
	escTracks2.Body.Detail.Tracklist.Rows = []pubTrackSt{
		{Num: 1, Off: "0:00", Label: `A&B"<>'`, Lead: "chk", Checked: true,
			Path: `C:\m\a&b"<>'.flac`, Ctx: `pub-tctx:C:\m\a&b"<>'.flac`},
	}

	long := captures
	longS := strings.Repeat("very-long-", 120)
	long.Sub = longS
	long.Switcher = "<div>" + strings.Repeat("s", 2000) + "</div>"
	long.Body.Hero.Rec = pubBadgeSt{Key: "REC", DL: "rec", Variant: "error", Line: longS}
	long.Body.Hero.NP = pubNpSt{Label: "Now playing", Title: longS, Meta: longS, State: longS,
		Bar: newPubBar(0.999, longS)}
	long.Body.List.Rows = []pubSetRowSt{{ID: strings.Repeat("id-", 200), Title: longS, Sub: longS,
		Sel: true, Rename: longS}}
	long.Body.Detail.Captures.Player = "<div>" + strings.Repeat("p", 3000) + "</div>"
	long.Body.Detail.Captures.Caps = []pubCapSt{{Caption: longS, Btns: []uiBtn{},
		Menu: pubMenu("capmenu-"+strings.Repeat("x", 200), longS)}}

	unicode := captures
	unicode.Title = "公開 🎧"
	unicode.Sub = "größer Опубліковано"
	unicode.Body.Hero.Rec = pubBadgeSt{Key: "REC", DL: "rec", Variant: "error", Line: "Живий сет · 7 треків"}
	unicode.Body.Hero.NP = pubNpSt{Label: "再生中", Title: "アーティスト - タイトル",
		Meta: "デッキ A · 128.0 BPM", State: "確認中 12秒", Bar: newPubBar(0.42, "確認中 12秒")}
	unicode.Body.List.Rows = []pubSetRowSt{
		{ID: "r☂1", Title: "⏺ Живий сет", Sub: "2026-07-25 · 7 треків · 中文", Sel: true, Rename: "Перейменувати…"},
	}
	unicode.Body.Detail.Name = "Живий сет 🎛️"
	unicode.Body.Detail.Meta = "2026-07-25 · 7 треків"
	unicode.Body.Detail.CapsLbl = "キャプチャ (1)"
	unicode.Body.Detail.TracksLbl = "トラックリスト (7)"
	unicode.Body.Detail.Captures.Caps = []pubCapSt{{Caption: "Трансляція · OGG · 84.2 МБ · сет.ogg",
		Btns: []uiBtn{}, Menu: pubMenu("capmenu-c1", "⋯ もっと")}}

	return map[string]pubSt{
		"unavailable":     unavailable,
		"empty":           empty,
		"loose":           loose,
		"captures":        captures,
		"tracklist":       tracklist,
		"resolving":       resolving,
		"noTracks":        bare,
		"noCaptures":      noCaps,
		"escaping":        escaping,
		"escapingTracks":  escTracks,
		"escapingTracks2": escTracks2,
		"long":            long,
		"unicode":         unicode,
	}
}

func TestZigPublishGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range pubFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderPublish(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", publishHTML(st), zig)

			if st.Body.Hero.Show { // an empty hero renders "" ⇒ NULL ⇒ the Go fallback
				zigFrag(t, "hero", pubHeroHTML(st.Body.Hero), stateJSON(st.Body.Hero), zigui.RenderPublishHero)
			}
		})
	}
}

// A hero with no recorder renders legitimately EMPTY: renderJSON returns NULL ⇒
// ok=false ⇒ the Go fallback renders the same "".
func TestZigPublishHeroEmptyFallsBack(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	st := pubHeroSt{}
	if _, ok := zigui.RenderPublishHero(stateJSON(st)); ok {
		t.Error("empty hero: want ok=false (NULL) so the Go fallback runs")
	}
	if got := pubHeroHTML(st); got != "" {
		t.Errorf("Go empty hero = %q, want \"\"", got)
	}
}

// pubRemFixtures: loading, link error, no sets, populated captures/tracklist, paged
// (showing-newest note), escaping edge, unicode.
func pubRemFixtures() map[string]pubRemSt {
	base := func() pubRemSt {
		return pubRemSt{
			Title: "Publish", Sub: "Recorded sets, captures and tracklists",
			Switcher: `<div class=lib-target><div class=ss-field><span class=ss-label>Controlling</span></div></div>`,
			Hint:     "Live status stays on the controlled computer",
			List:     pubRemListSt{Rows: []pubRemRowSt{}},
			Detail:   pubRemDetailSt{CardTitle: "Selected set", Hint: "Pick a set on the left"},
		}
	}
	loading := base()
	loading.List.Empty = "Loading…"

	errored := base()
	errored.List.Empty = "Link error: peer unreachable"

	noSets := base()
	noSets.List.Empty = "No recorded sets on that computer"

	rows := []pubRemRowSt{
		{ID: "r1", Title: "⏺ Live set", Sub: "2026-07-25 21:40 · 7 tracks · live", Sel: true},
		{ID: "r2", Title: "Warmup", Sub: "2026-07-24 19:00 · 21 tracks · 2h0m0s · matched"},
	}
	caps := base()
	caps.List = pubRemListSt{Count: "2 sets", Rows: rows}
	caps.Detail = pubRemDetailSt{CardTitle: "Selected set", Sel: true, Hint: "Pick a set on the left",
		Name: "Live set", Meta: "2026-07-25 21:40 · 7 tracks · live",
		Actions: []uiBtn{{Label: "Export…", Variant: "outline", Act: "pub-export:r1"}},
		Active:  "captures", CapsLbl: "Captures (2)", TracksLbl: "Tracklist (7)",
		Caps: pubRemCapsSt{Note: "Files live on that computer", Caps: []string{
			"Broadcast audio · OGG · 84.2 MB · set.ogg",
			"OBS recording · MKV · 1.9 GB · set.mkv",
		}},
	}

	paged := caps
	paged.List = pubRemListSt{Count: "412 sets", Note: "Showing the newest 200 of 412", Rows: rows}

	tl := caps
	tl.Detail.Active = "tracklist"
	tl.Detail.Actions = []uiBtn{
		{Label: "Export…", Variant: "outline", Act: "pub-export:r2"},
		{Label: "Match history", Variant: "secondary", Act: "pub-match:r2"},
		{Label: "Delete", Variant: "destructive", Act: "pub-del:r2"},
	}
	tl.Detail.Caps = pubRemCapsSt{Caps: []string{}}
	tl.Detail.Tl = pubRemTlSt{Note: "Showing the first 500 of 812", Rows: []pubRemTrackSt{
		{Num: 1, Off: "0:00", Label: "A - One"},
		{Num: 2, Off: "4:31", Label: "B - Two"},
		{Num: 3, Off: "1:02:07", Label: "C - Three"},
	}}

	tlLoading := tl
	tlLoading.Detail.Tl = pubRemTlSt{Empty: "Loading…", Rows: []pubRemTrackSt{}}

	tlErr := tl
	tlErr.Detail.Tl = pubRemTlSt{Hint: "Link error: timeout", Rows: []pubRemTrackSt{}}

	capsErr := caps
	capsErr.Detail.Caps = pubRemCapsSt{Hint: "Link error: timeout", Caps: []string{}}

	escaping := tl
	escaping.Title = `P&ublish <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Switcher = `<div class="raw & kept">'x'</div>`
	escaping.Hint = `h&int"<>'`
	escaping.List = pubRemListSt{Count: `2 &"sets"<>'`, Note: `n&ote"<>'`, Rows: []pubRemRowSt{
		{ID: `r&1"<>'`, Title: `⏺ L&ive"<>'`, Sub: `s&ub"<>'`, Sel: true},
	}}
	escaping.Detail.Name = `L&ive"<>'`
	escaping.Detail.Meta = `m&eta"<>'`
	escaping.Detail.CapsLbl = `C&aps"<>' (2)`
	escaping.Detail.TracksLbl = `T&racks"<>' (7)`
	escaping.Detail.Actions = []uiBtn{{Label: `E&xport"<>'…`, Variant: "outline", Act: `pub-export:r&1"`}}
	escaping.Detail.Tl = pubRemTlSt{Note: `n&ote"<>'`, Rows: []pubRemTrackSt{
		{Num: 1, Off: "0:00", Label: `A&B - <"One">'`},
	}}

	escCaps := escaping
	escCaps.Detail.Active = "captures"
	escCaps.Detail.Caps = pubRemCapsSt{Note: `c&note"<>'`, Caps: []string{`B&roadcast"<>' · s&t.ogg`}}

	unicode := tl
	unicode.Title = "公開 🎧"
	unicode.Sub = "größer Опубліковано"
	unicode.Hint = "Статус залишається на керованому комп'ютері"
	unicode.List = pubRemListSt{Count: "2 セット", Rows: []pubRemRowSt{
		{ID: "r☂1", Title: "⏺ Живий сет", Sub: "2026-07-25 · 7 треків", Sel: true},
	}}
	unicode.Detail.Name = "Живий сет 🎛️"
	unicode.Detail.Tl = pubRemTlSt{Rows: []pubRemTrackSt{{Num: 1, Off: "0:00", Label: "アーティスト - タイトル"}}}

	return map[string]pubRemSt{
		"loading":   loading,
		"error":     errored,
		"noSets":    noSets,
		"captures":  caps,
		"paged":     paged,
		"tracklist": tl,
		"tlLoading": tlLoading,
		"tlError":   tlErr,
		"capsError": capsErr,
		"escaping":  escaping,
		"escCaps":   escCaps,
		"unicode":   unicode,
	}
}

func TestZigPublishRemoteGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range pubRemFixtures() {
		t.Run(name, func(t *testing.T) {
			zigFrag(t, "remote", pubRemoteHTML(st), stateJSON(st), zigui.RenderPublishRemote)
		})
	}
}
