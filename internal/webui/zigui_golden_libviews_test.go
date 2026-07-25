//go:build zigui

package webui

import (
	"fmt"
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Library alternate-body + modal golden gate: the Zig renderers must be BYTE-IDENTICAL to the
// Go ones for the peer mirror (#lib-body + #rmirror-banner), the remote cue-edit surface
// (#lib-body + #rce-info + the rail's save section) and the two Library modals.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func libMirrorFixtures() map[string]libMirrorSt {
	live := libMirrorSt{Banner: libMirrorBanSt{
		Status: mirrorLive, Title: "Mirroring Studio PC", Tip: `<span class=tip data-act="tip:remote-library">?</span>`,
		HasNote: true, Note: "Audio and file writes happen on the peer",
	}}
	return map[string]libMirrorSt{
		"empty":       {Banner: libMirrorBanSt{}},
		"unavailable": {NoLink: true, NoLinkMsg: "No paired peer is connected"},
		"connecting": {Banner: libMirrorBanSt{Status: mirrorConnecting, Title: "Mirroring Studio PC",
			Tip: `<span class=tip></span>`, HasNote: true, Note: "Connecting…"}},
		"populated": live,
		"errored": {Banner: libMirrorBanSt{Status: mirrorError, Title: "Mirroring Studio PC",
			Tip: `<span class=tip></span>`, IsErr: true, Err: "peer did not answer", Reconnect: "Reconnect"}},
		"closed": {Banner: libMirrorBanSt{Status: mirrorClosed, Title: "Mirroring Studio PC",
			IsErr: true, Err: "Session closed", Reconnect: "Reconnect"}},
		"escaping": {Banner: libMirrorBanSt{Status: mirrorError,
			Title: `Mirroring node & "B" <x>'`, Tip: `<span class=tip title="a&b">?</span>`,
			IsErr: true, Err: `boom & "timeout" <x>'`, Reconnect: `Re&connect "now"'`}},
		"unavailableEsc": {NoLink: true, NoLinkMsg: `No link & "peer" <x>'`},
		"long": {Banner: libMirrorBanSt{Status: mirrorLive, Title: strings.Repeat("Mirroring a very long peer name ", 40),
			HasNote: true, Note: strings.Repeat("note ", 200)}},
		"unicode": {Banner: libMirrorBanSt{Status: mirrorLive, Title: "ミラー Библиотека 🎧",
			HasNote: true, Note: "größer · аудио на пире"}},
	}
}

func rceInfoFixtures() map[string]rceInfoSt {
	base := rceInfoSt{
		Show: true, Eyebrow: "Editing on Studio PC", Title: "Kollektiv - Rausch",
		Path: `P:\peer\track.mp3`, LocalNote: "Audio plays here from a cached copy",
		Hints: []libHintSt{{Tone: "ok", Text: "In sync with Studio PC"}}, Back: "Back to library",
	}
	set := base
	set.HasSet, set.SetLine = true, "Track 2 of 9 in Peer Crate · next ready"
	set.Prev = rceNavSt{Label: "Previous", Act: "rce-set-prev"}
	set.Next = rceNavSt{Label: "Next", Act: "rce-set-next"}

	edges := set
	edges.SetLine = "Track 1 of 9"
	edges.Prev = rceNavSt{Label: "Previous", Gated: true, Why: "Start of set"}
	edges.Next = rceNavSt{Label: "Next", Gated: true, Why: "End of set"}

	dirty := base
	dirty.Hints = []libHintSt{
		{Tone: "warn", Text: "Studio PC changed this track"},
		{Tone: "warn", Text: "Unsaved edits"},
	}

	esc := set
	esc.Eyebrow = `Editing on Stu & "dio"'<x>`
	esc.Title = `T & "itle"'<x>`
	esc.Path = `P:\peer\a & "b"'.mp3`
	esc.SetLine = `Track 2 of 9 in Cr&ate "x"'`
	esc.LocalNote = `Audio & "here"'`
	esc.Hints = []libHintSt{{Tone: "warn", Text: `moved & "on peer"'`}}
	esc.Back = `B&ack "now"'`
	esc.Prev = rceNavSt{Label: `P&rev "1"'`, Gated: true, Why: `St&art "of set"'`}
	esc.Next = rceNavSt{Label: `N&ext "1"'`, Act: "rce-set-next"}

	long := base
	long.Title = strings.Repeat("very-long-track-title-", 60)
	long.Path = strings.Repeat(`P:\deep\`, 50) + "x.flac"

	uni := set
	uni.Eyebrow = "Правка на Studio PC"
	uni.Title = "Кириллица + 中文 🎛️"
	uni.Path = `P:\Музыка\сеты\трек.flac`
	uni.SetLine = "Трек 2 из 9 · 次の準備完了"

	return map[string]rceInfoSt{
		"empty":     {},
		"populated": base,
		"set":       set,
		"setEdges":  edges,
		"dirty":     dirty,
		"escaping":  esc,
		"long":      long,
		"unicode":   uni,
	}
}

func rceSaveFixtures() map[string]rceSaveSt {
	clean := rceSaveSt{Show: true, Header: "Save to Studio PC", Status: "clean", StatusText: "In sync with Studio PC"}
	busy := clean
	busy.Status, busy.StatusText = "busy", "Saving…"
	dirty := clean
	dirty.Status, dirty.StatusText = "dirty", ""
	dirty.UnsavedText, dirty.SaveLbl = "Unsaved edits", "Save to Studio PC"
	retry := dirty
	retry.HasErr, retry.ErrText = true, "Save failed: link closed"
	retry.SaveLbl = "Retry"
	moved := dirty
	moved.Moved, moved.MovedText, moved.ReloadLbl = true, "Studio PC changed this track", "Re-fetch from peer"

	saved := clean
	saved.Status, saved.StatusText = "saved", "Saved on Studio PC"
	saved.HasWrites, saved.WriteHeader = true, "Write into Studio PC's DJ software"
	saved.Writes = []rceWriteSt{
		{Done: true, Text: "Wrote 1 track to Traktor"},
		{Text: "Write to Rekordbox (1)", Act: "rce-write:rekordbox"},
		{Text: "Write to Serato (1)", Gated: true, Why: "Save your edits first"},
	}

	esc := saved
	esc.Header = `Save to Stu & "dio"'<x>`
	esc.WriteHeader = `Write & "into"' <sw>`
	esc.ErrText, esc.HasErr = `failed & "x"'<y>`, true
	esc.Writes = []rceWriteSt{
		{Done: true, Text: `Wrote 1 to Trak&tor "x"'`},
		{Text: `Write to R&B "x"'`, Act: `rce-write:r&b`},
		{Text: `Write to S&erato "x"'`, Gated: true, Why: `Sa&ve "first"'`},
	}

	long := saved
	long.Header = strings.Repeat("Save to a very long peer name ", 40)
	long.Writes = append(long.Writes, rceWriteSt{Text: strings.Repeat("Write to ", 200), Act: "rce-write:x"})

	uni := saved
	uni.Header = "Сохранить на Studio PC"
	uni.StatusText = "保存しました 🎛️"
	uni.WriteHeader = "Записать в ПО"

	return map[string]rceSaveSt{
		"empty":     {},
		"clean":     clean,
		"busy":      busy,
		"dirty":     dirty,
		"retry":     retry,
		"moved":     moved,
		"populated": saved,
		"escaping":  esc,
		"long":      long,
		"unicode":   uni,
	}
}

func rceBodyFixtures() map[string]rceBodySt {
	infos := rceInfoFixtures()
	det := libDetailFixture()
	empty := rceBodySt{Info: rceInfoSt{}}
	pop := rceBodySt{
		Wave:   `<div id=ce-topbar><div class=ce-topbar><span class=ce-tb-title>Rausch</span></div></div><canvas id=ce-wave></canvas>`,
		Info:   infos["set"],
		Detail: det,
	}
	esc := pop
	esc.Info = infos["escaping"]
	escDet := libDetailFixture()
	escDet.Title = `Kollektiv & "Rausch" <mix>'`
	esc.Detail = escDet
	long := pop
	long.Wave = "<div>" + strings.Repeat("w", 5000) + "</div>"
	long.Info = infos["long"]
	uni := pop
	uni.Info = infos["unicode"]
	return map[string]rceBodySt{
		"empty":     empty,
		"populated": pop,
		"escaping":  esc,
		"long":      long,
		"unicode":   uni,
	}
}

func libSRSel(id, label, cur string) selState {
	return selState{ID: id, Label: label, CurLabel: cur, Rows: []selRow{
		{Val: "", Label: "Any"}, {Val: "Peak time", Label: "Peak time", Cur: cur == "Peak time"},
	}}
}

func libSmartModalFixtures() map[string]libSmartModalSt {
	base := libSmartModalSt{
		Title: "New smart playlist", Desc: "Rules combine with AND; genres are OR",
		Name:      newPBField("Name", "lib-sr-name", "", "text", ""),
		GenresLbl: "Genres", Genres: []libChipSt{},
		Feel:      libSRSel("lib-sr-feel", "Feel", ""),
		BPMMin:    newPBField("BPM from", "lib-sr-bpmmin", "", "number", ""),
		BPMMax:    newPBField("BPM to", "lib-sr-bpmmax", "", "number", ""),
		KeyField:  newPBField("Key contains", "lib-sr-key", "", "text", "e.g. 8A"),
		Rating:    libSRSel("lib-sr-rating", "Rating", "0"),
		Plays:     newPBField("Min plays", "lib-sr-plays", "", "number", ""),
		Search:    newPBField("Search", "lib-sr-search", "", "text", "title, artist, path"),
		CompatLbl: "Works well with", Compat: emptySel(),
		CompatHint: "Pick an anchor track to use its compatible set",
		Count:      "0 of 0 tracks match · everything",
		Confirm:    "Create", Cancel: "Cancel",
	}

	pop := base
	pop.Name = newPBField("Name", "lib-sr-name", "Warmup", "text", "")
	pop.Genres = []libChipSt{
		newChip("Techno", "", "lib-sr-genre:Techno", true),
		newChip("House", "", "lib-sr-genre:House", false),
	}
	pop.Feel = libSRSel("lib-sr-feel", "Feel", "Peak time")
	pop.BPMMin = newPBField("BPM from", "lib-sr-bpmmin", "122.5", "number", "")
	pop.BPMMax = newPBField("BPM to", "lib-sr-bpmmax", "138", "number", "")
	pop.Rating = libSRSel("lib-sr-rating", "Rating", "3")
	pop.Count = "12 of 240 tracks match · Techno, 122.5-138 BPM"

	edit := pop
	edit.Title, edit.Confirm = "Edit smart playlist", "Save"
	edit.Compat = selState{ID: "lib-sr-compat", CurLabel: "Kollektiv - Rausch", Rows: []selRow{
		{Val: "", Label: "No anchor"},
		{Val: `C:\m\a.flac`, Label: "Kollektiv - Rausch", Sub: `C:\m\a.flac`, Cur: true},
	}}
	edit.HasDepth = true
	edit.Depth = []libChipSt{
		newChip("Direct pairs", "", "lib-sr-depth:1", false),
		newChip("+ friends of friends", "", "lib-sr-depth:2", true),
	}

	open := edit
	open.Compat = selState{ID: "lib-sr-compat", Open: true, Filter: "трек", CurLabel: "Kollektiv - Rausch",
		Rows: []selRow{{Val: `C:\m\сет\трек.flac`, Label: "Кириллица 🎧", Sub: `C:\m\сет\трек.flac`}}}

	nomatch := edit
	nomatch.Compat = selState{ID: "lib-sr-compat", Open: true, Filter: "zzz", CurLabel: "Kollektiv - Rausch"}
	nomatch.Feel = selState{ID: "lib-sr-feel", Label: "Feel", Open: true, CurLabel: "(select…)", Rows: []selRow{}}

	esc := edit
	esc.Title = `Edit & "smart" <pl>'`
	esc.Desc = `Rules & "AND"'`
	esc.Name = newPBField(`N&ame "x"'`, "lib-sr-name", `My & "rules"'`, "text", "")
	esc.GenresLbl = `G&enres "x"'`
	esc.Genres = []libChipSt{newChip(`Hard & "Techno"'`, "", `lib-sr-genre:Hard & "Techno"'`, true)}
	esc.KeyField = newPBField(`K&ey "x"'`, "lib-sr-key", `8A & "x"'`, "text", `hi&nt "x"'`)
	esc.Search = newPBField("Search", "lib-sr-search", `deep & "cut"'`, "text", "q")
	esc.CompatLbl = `W&orks "with"'`
	esc.CompatHint = `Pick & "an anchor"'`
	esc.Count = `1 of 4 match · genre "Hard & Techno"'`
	esc.Confirm, esc.Cancel = `S&ave "it"'`, `C&ancel "x"'`

	long := pop
	long.Count = strings.Repeat("very long describe clause ", 100)
	for i := 0; i < 40; i++ {
		long.Genres = append(long.Genres, newChip(fmt.Sprintf("Genre-%02d-%s", i, strings.Repeat("x", 30)), "", fmt.Sprintf("lib-sr-genre:g%d", i), i%3 == 0))
	}

	uni := pop
	uni.Title = "スマートプレイリスト"
	uni.GenresLbl = "Жанры"
	uni.Genres = []libChipSt{newChip("Мелодик 🎧", "", "lib-sr-genre:Мелодик", true)}
	uni.Count = "12 из 240 · Техно"

	return map[string]libSmartModalSt{
		"empty":     base,
		"populated": pop,
		"edit":      edit,
		"open":      open,
		"noMatches": nomatch,
		"escaping":  esc,
		"long":      long,
		"unicode":   uni,
	}
}

func libRelocModalFixtures() map[string]libRelocModalSt {
	base := libRelocModalSt{
		Title: "Relocate missing files", Desc: "Index a folder, then apply the matches",
		Missing: "12 missing tracks", Root: "", RootPH: "Search root folder",
		BrowseLbl: "Browse", FindLbl: "Find matches",
	}
	busy := base
	busy.Root, busy.FindLbl = `D:\music`, "Working…"
	busy.HasMsg, busy.Msg = true, "Indexing D:\\music…"

	pop := base
	pop.Root = `D:\music`
	pop.HasMsg, pop.Msg = true, "Found 3 of 12"
	pop.HasRows, pop.ApplyLbl = true, "Apply and write collection"
	pop.Rows = []libRelocRowSt{
		{Act: "lib-reloc-skip:0", Checked: true, Old: `C:\old\a.flac`, New: `D:\new\a.flac`, Conf: "Unique match", ConfVar: "success"},
		{Act: "lib-reloc-skip:1", Checked: false, Old: `C:\old\b.mp3`, New: `D:\new\b.mp3`, Conf: "Size match", ConfVar: "info"},
		{Act: "lib-reloc-skip:2", Checked: true, Old: `C:\old\c.wav`, New: `D:\new\c.wav`, Conf: "Ambiguous", ConfVar: "warning"},
	}

	capped := pop
	capped.Rows = nil
	for i := 0; i < 200; i++ {
		capped.Rows = append(capped.Rows, libRelocRowSt{
			Act: fmt.Sprintf("lib-reloc-skip:%d", i), Checked: i%2 == 0,
			Old: fmt.Sprintf(`C:\old\%03d.flac`, i), New: fmt.Sprintf(`D:\new\%03d.flac`, i),
			Conf: "Unique match", ConfVar: "success",
		})
	}
	capped.HasMore, capped.More = true, "Showing the first 200 of 900 matches"

	esc := pop
	esc.Title = `Reloc & "missing"'<x>`
	esc.Desc = `Index & "a folder"'`
	esc.Missing = `12 & "missing"'`
	esc.Root = `D:\music & "lib"'`
	esc.RootPH = `Se&arch "root"'`
	esc.BrowseLbl, esc.FindLbl, esc.ApplyLbl = `B&rowse "…"'`, `F&ind "x"'`, `A&pply "x"'`
	esc.Msg = `Indexed & "9000" files'`
	esc.Rows = []libRelocRowSt{
		{Act: "lib-reloc-skip:0", Checked: true, Old: `C:\old\b & "c".mp3`, New: `D:\new\b & "c".mp3`,
			Conf: `Uni&que "match"'`, ConfVar: "success"},
	}

	long := pop
	long.Rows = []libRelocRowSt{{Act: "lib-reloc-skip:0", Checked: true,
		Old: strings.Repeat(`C:\deep\`, 60) + "x.flac", New: strings.Repeat(`D:\deep\`, 60) + "x.flac",
		Conf: strings.Repeat("Unique ", 50), ConfVar: "success"}}
	long.Msg = strings.Repeat("Found ", 200)

	uni := pop
	uni.Title = "Найти перемещённые файлы"
	uni.Missing = "12 отсутствуют"
	uni.Rows = []libRelocRowSt{{Act: "lib-reloc-skip:0", Checked: true,
		Old: `C:\Музыка\трек.flac`, New: `D:\Музыка\сеты\трек.flac`, Conf: "Уникально 🎯", ConfVar: "success"}}

	return map[string]libRelocModalSt{
		"empty":     base,
		"busy":      busy,
		"populated": pop,
		"capped":    capped,
		"escaping":  esc,
		"long":      long,
		"unicode":   uni,
	}
}

// zigGolden renders one fixture through Zig and compares against the Go renderer. An empty Go
// render makes renderJSON return NULL (ok=false) - the bridge then falls back to Go, which
// emits the same empty string, so that case is a PASS (same rule as #midi-ctlstat-<i>).
func zigGolden[T any](t *testing.T, what string, st T, goHTML string, zig func([]byte) (string, bool)) {
	t.Helper()
	js := stateJSON(st)
	if js == nil {
		t.Fatal("state marshal failed")
	}
	h, ok := zig(js)
	if !ok {
		if goHTML != "" {
			t.Fatalf("%s: zig render failed (Go rendered %d bytes)", what, len(goHTML))
		}
		return
	}
	assertBytesEqual(t, what, goHTML, h)
}

func TestZigLibMirrorGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libMirrorFixtures() {
		t.Run(name, func(t *testing.T) {
			zigGolden(t, "mirror", st, libMirrorBodyHTML(st), zigui.RenderLibMirror)
			zigGolden(t, "banner", st.Banner, mirrorBannerHTMLOf(st.Banner), zigui.RenderLibMirrorBanner)
		})
	}
}

func TestZigRCEGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range rceBodyFixtures() {
		t.Run("body/"+name, func(t *testing.T) {
			zigGolden(t, "rceBody", st, rceBodyHTML(st), zigui.RenderRCEBody)
		})
	}
	for name, st := range rceInfoFixtures() {
		t.Run("info/"+name, func(t *testing.T) {
			zigGolden(t, "rceInfo", st, rceInfoHTMLOf(st), zigui.RenderRCEInfo)
		})
	}
	for name, st := range rceSaveFixtures() {
		t.Run("save/"+name, func(t *testing.T) {
			zigGolden(t, "rceSave", st, rceSaveHTMLOf(st), zigui.RenderRCESave)
		})
	}
}

func TestZigLibModalsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libSmartModalFixtures() {
		t.Run("smart/"+name, func(t *testing.T) {
			zigGolden(t, "smartModal", st, libSmartModalHTMLOf(st), zigui.RenderLibSmartModal)
		})
	}
	for name, st := range libRelocModalFixtures() {
		t.Run("reloc/"+name, func(t *testing.T) {
			zigGolden(t, "relocModal", st, libRelocModalHTMLOf(st), zigui.RenderLibRelocModal)
		})
	}
}
