//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Motion golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states (full view + #mo-body fragment).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// moFixtures: unavailable (no VRChat tools), empty, populated (studio, model+cloud on),
// escaping edge, long values, unicode.
func moFixtures() map[string]moState {
	head := func(sec string) moState {
		return moState{Title: "Motion", Sub: "Camera paths + motion studio", Section: sec,
			TabCam: "Camera paths", TabStudio: "Studio"}
	}
	cam := func() moCamSt {
		return moCamSt{
			Rows: []moCamRow{}, Empty: "No camera paths yet",
			ReloadLbl: "Reload list", OrganizeLbl: "Organize now", DJLbl: "Install DJ paths",
			PreviewLbl: "Preview", Tip: `<label class=tt data-label="tt-camera-paths">tip</label>`,
			View:    `<div id="cpv-mo" class=cpv-view data-actpos="cpv-orbit:mo"><svg class=cpv-svg></svg></div>`,
			Hint:    "Drag to orbit, wheel to zoom",
			Info:    "Select a path",
			PlayBtn: `<span id="cpv-mo-play"></span>`,
			LoadLbl: "Load into VRChat", CopyLbl: "Copy file path",
		}
	}
	studio := func() moStudioSt {
		return moStudioSt{
			Recs: []moRecRow{}, Empty: "No recordings",
			RefreshLbl: "Refresh", ExportLbl: "Export animation", RenderLbl: "Render video", PCViewLbl: "View point cloud",
			RenderProg: "",
			Avatar: moAvatarSt{Label: "Avatar", ImportLbl: "Import avatar", SyncLbl: "Sync now",
				Info: "Current: none",
				Sel: selState{ID: "mo-avatar", Label: "Active avatar", CurLabel: "(select…)",
					Rows: []selRow{{Val: "", Label: "(select…)"}}}},
			PreviewLbl: "Preview", Tip: `<label class=tt data-label="tt-motion-studio">tip</label>`,
			View: `<svg class=mo-svg viewBox="0 0 640 400"></svg>`,
			Hint: "Scrub the take", Time: "0.0 / 0.0 s",
			Scrub:   moSlide("Scrub", "mo-scrub", 0, 1000, 1, 0, ""),
			PlayLbl: "▶ Play", StopLbl: "⏹ Stop",
			Loop:  moTog("Loop", "mo-loop", false),
			OSC:   moTog("OSC trackers", "mo-osc", false),
			VMC:   moTog("Stream VMC", "mo-vmc", false),
			Model: moTog("Show avatar model", "mo-model", false),
			// model-only rows stay resolved even while hidden (the renderers gate on ModelOn)
			PhysNote: "No PhysBones detected",
			Phys:     moTog("Avatar physics", "mo-phys", true),
			Rest:     moTog("Rest pose", "mo-rest", false),
			Marks:    moTog("Overlay tracker points", "mo-marks", false),
			PC:       moTog("Point cloud", "mo-pc", false),
			// mirrors moStudioState: Rows is never nil (nil → JSON null → Zig parse fails)
			PCDensity:   selState{Rows: []selRow{}},
			PCColor:     moTog("Sample colours", "mo-pc-color", true),
			PCNote:      "Density affects export only",
			PCExportLbl: "Export .rmpc",
			VMCHelp:     "VMC streams to 127.0.0.1:39539",
		}
	}

	unavailable := head("campaths")
	unavailable.Cam = &moCamSt{Unavailable: "VRChat tools unavailable", Rows: []moCamRow{}}

	empty := head("campaths")
	c := cam()
	empty.Cam = &c

	populated := head("campaths")
	pc := cam()
	pc.Rows = []moCamRow{
		{Group: "DJ", ShowGroup: true, Act: "mo-cp-sel:0", Name: "Orbit slow", Meta: "24 points · 12.5s · 2026-07-01 21:30"},
		{Act: "mo-cp-sel:1", Sel: true, Name: "Crowd sweep", Meta: "8 points · 4.0s · 2026-07-02 01:05"},
		{Group: "Venue", ShowGroup: true, Act: "mo-cp-sel:2", Name: "Truss run", Meta: "60 points · 30.0s · 2026-07-03 18:00"},
	}
	pc.Info = "Crowd sweep — player-relative · 8 points · 4.0s"
	populated.Cam = &pc

	// studio with the model + point cloud on (every conditional row rendered)
	studioOn := head("studio")
	so := studio()
	so.Recs = []moRecRow{
		{Name: "take-01", Act: "mo-rec-sel:take-01", Sel: true},
		{Name: "take-02", Act: "mo-rec-sel:take-02"},
	}
	so.RenderProg = `<div class=pbar><div class=pbar-fill style="width:42.0%"></div><span class=pbar-cap>frame 12/30</span></div>`
	so.Time = "12.4 / 30.0 s"
	so.Scrub = moSlide("Scrub", "mo-scrub", 0, 1000, 1, 413.33333333333337, "")
	so.PlayLbl = "⏸ Pause"
	so.Loop, so.Model, so.PC = moTog("Loop", "mo-loop", true), moTog("Show avatar model", "mo-model", true), moTog("Point cloud", "mo-pc", true)
	so.ModelOn, so.HasDyn, so.PCOn = true, true, true
	so.PCDensity = selState{ID: "mo-pc-density", Label: "Export density", CurLabel: "Medium", Open: true, Filter: "m",
		Rows: []selRow{{Val: "med", Label: "Medium", Sub: "~250k points", Cur: true}, {Val: "ultra", Label: "Ultra", Sub: "~2M points"}}}
	so.Avatar.Sel = selState{ID: "mo-avatar", Label: "Active avatar", CurLabel: "dj.vrm",
		Rows: []selRow{{Val: "C:\\avatars\\dj.vrm", Label: "dj.vrm", Sub: "18.2 MB", Cur: true}}}
	studioOn.Studio = &so

	// model on, no physics chains → the info line replaces the physics switch
	escaping := head("studio")
	es := studio()
	escaping.Title = `Mo&tion <"studio">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.TabCam = `C&am <"paths">`
	escaping.TabStudio = `St'udio&`
	es.Recs = []moRecRow{{Name: `ta&ke <"01">'`, Act: `mo-rec-sel:ta&ke <"01">'`, Sel: true}}
	es.Empty = `n&one<>`
	es.RefreshLbl = `Re&fresh"`
	es.PreviewLbl = `P&review<>`
	es.Hint = `h&int "x" 'y'`
	es.Time = `1&.0 / <2>.0 s`
	es.Scrub = moSlide(`S&crub "x"`, "mo-scrub", 0, 1000, 1, 999.5, "")
	es.PlayLbl = `▶ P&lay"`
	es.StopLbl = `⏹ St<op>`
	es.Loop = moTog(`Lo&op "x"`, "mo-loop", true)
	es.Model = moTog(`M&odel<>`, "mo-model", true)
	es.ModelOn, es.HasDyn = true, false
	es.PhysNote = "No PhysBones &amp; no sidecar" // RAW on both paths (Go emits it unescaped)
	es.Rest = moTog(`Re&st`, "mo-rest", true)
	es.Marks = moTog(`Ma&rks<>`, "mo-marks", true)
	es.PC = moTog(`P&C<>`, "mo-pc", false)
	es.VMCHelp = `VMC → 127.0.0.1:39539 & "more"`
	es.Avatar = moAvatarSt{Label: `A&vatar<>`, ImportLbl: `Im&port"`, SyncLbl: `Sy<nc>`,
		Info: `Current: we"ird&av'atar<>.vrm`,
		Sel: selState{ID: "mo-avatar", Label: `Ac&tive "avatar"`, CurLabel: `we"ird&av'.vrm`, Open: true, Filter: `f&"x'<`,
			Rows: []selRow{{Val: `C:\a&"v'.vrm`, Label: `we"ird&av'.vrm`, Sub: `1&"2'<> MB`, Badge: `b&"a'<>`, Cur: true}}}}
	escaping.Studio = &es

	long := head("campaths")
	lc := cam()
	longS := strings.Repeat("very-long-path-name-", 100)
	lc.Rows = []moCamRow{
		{Group: strings.Repeat("folder/", 80), ShowGroup: true, Act: "mo-cp-sel:0", Sel: true, Name: longS,
			Meta: strings.Repeat("meta ", 200)},
	}
	lc.Info = longS
	lc.View = `<svg>` + strings.Repeat(`<circle cx="1" cy="2" r="3"/>`, 200) + `</svg>`
	long.Cam = &lc

	unicode := head("studio")
	us := studio()
	unicode.Title = "モーション 🎧"
	unicode.Sub = "größer Движение"
	unicode.TabCam = "カメラ"
	unicode.TabStudio = "Студия"
	us.Recs = []moRecRow{{Name: "тейк-01 中文 🎛️", Act: "mo-rec-sel:тейк-01 中文 🎛️", Sel: true}}
	us.Loop = moTog("Зациклить", "mo-loop", true)
	us.OSC = moTog("OSC-трекеры", "mo-osc", true)
	us.Model = moTog("アバターを表示", "mo-model", true)
	us.ModelOn, us.HasDyn = true, true
	us.Phys = moTog("Физика アバター", "mo-phys", true)
	us.Rest = moTog("休憩ポーズ", "mo-rest", false)
	us.Marks = moTog("Точки трекеров", "mo-marks", true)
	us.PC = moTog("点群", "mo-pc", false)
	us.Time = "12,4 / 30,0 с"
	us.VMCHelp = "VMC → 127.0.0.1:39539 送信"
	us.Avatar.Sel = selState{ID: "mo-avatar", Label: "アクティブ", CurLabel: "аватар.vrm",
		Rows: []selRow{{Val: "аватар.vrm", Label: "аватар.vrm", Sub: "18,2 МБ", Cur: true}}}
	unicode.Studio = &us

	return map[string]moState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"studio":      studioOn,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigMotionGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range moFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderMotion(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", motionHTML(st), zig)

			zigBody, ok := zigui.RenderMotionBody(js)
			if !ok {
				t.Fatal("zig body render failed")
			}
			assertBytesEqual(t, "body", motionBodyHTML(st), zigBody)
		})
	}
}
