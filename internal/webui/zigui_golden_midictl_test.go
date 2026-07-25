//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// MIDI tab golden gate: Zig must be BYTE-IDENTICAL to the Go renderers for representative
// states (full tab + the #midi-active and #midi-ctlstat-<i> tick fragments).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func midiSel(id, cur string, rows ...selRow) selState {
	if rows == nil {
		rows = []selRow{}
	}
	return selState{ID: id, CurLabel: cur, Rows: rows}
}

// midiCtlFixtures: unavailable (nothing wired), empty, populated, escaping edge, long, unicode.
func midiCtlFixtures() map[string]midiCtlState {
	grid := func(n int, set bool) midiLearnGridState {
		g := midiLearnGridState{
			Hdr: "Learn per channel", HdrTip: `<label class=tt data-label="tt-midi-learn-grid"></label>`,
			Cols: "2", ChHdrs: []string{}, Rows: []midiLearnRow{},
			Learn: "Learn", Relearn: "Re-learn", Clear: "Clear",
		}
		for ch := 1; ch <= n; ch++ {
			g.ChHdrs = append(g.ChHdrs, "ch"+strings.Repeat("I", ch))
		}
		for _, ctl := range []string{"eqHigh", "fader", "play"} {
			row := midiLearnRow{Label: ctl, Cells: []midiLearnCell{}}
			for ch := 1; ch <= n; ch++ {
				cell := midiLearnCell{
					Act: "midi-learn:0:" + ctl + ":1", ClearAct: "midi-unlearn:0:" + ctl + ":1",
					Tid: "midi-learn-0-" + ctl + "-1",
				}
				if set && ch == 1 {
					cell.Set, cell.Readout = true, "CC24"
				}
				row.Cells = append(row.Cells, cell)
			}
			g.Rows = append(g.Rows, row)
		}
		return g
	}
	knob := func(label, cc string) midiKnobState {
		return midiKnobState{
			DL: "ch1 " + strings.ToLower(label), V: "0.5039370078740157", Rot: "1.0629921259842407", Val: "64",
			Act: "midi-send:0:24", Tid: "midi-ch1-eqHigh", Aria: label + " " + cc,
			Label: label, CC: cc, SweepAct: "midi-sweep:0:24",
			SweepTitle: "Sweep", SweepAria: "Sweep " + label, SweepGlyph: "↯",
		}
	}
	fader := func() midiKnobState {
		k := knob("Fader", "CC23·ch1")
		k.V, k.Rot, k.Val, k.Tid = "0", "-135", "0", "midi-ch1-fader"
		return k
	}
	mom := func(id, label, cc string) midiMomState {
		return midiMomState{
			Cls: "midi-btn midi-btn--" + id, Act: "midi-note:0:20", Tid: "midi-ch1-" + id,
			DL: "ch1 " + strings.ToLower(label), Aria: label + " " + cc, Label: label, CC: cc,
		}
	}
	rack := func(n int) midiRackState {
		st := midiRackState{
			Card: "Channel rack", StepLbl: "Channels", N: "2", Dec: "1", Inc: "3",
			Sub: "Move a control to send its CC", Strips: []midiStripState{},
		}
		for ch := 1; ch <= n; ch++ {
			st.Strips = append(st.Strips, midiStripState{
				Head:   "Channel 1 (A)",
				Knobs:  []midiKnobState{knob("Hi", "CC24·ch1"), knob("Mid", "CC25·ch1")},
				Faders: []midiKnobState{fader()},
				Btns:   []midiMomState{mom("cue", "Cue", "Note28·ch1"), mom("play", "Play", "Note20·ch1")},
			})
		}
		return st
	}
	help := func() midiHelpState {
		return midiHelpState{
			Card: "How to map", Badge: "guide",
			Step1: "Pick a control", Step2: "Hit learn in your DJ app", Step3: "Move the control",
			Feedback: "LED feedback rides the same CC", Caveat: "Rekordbox needs a mapping file:",
			Link: "Rekordbox mapping FAQ", SwHdr: "Per-software status",
			Rows: []midiSwRow{
				{Name: "Traktor Pro", Badge: "stable", BadgeVar: "success", Note: "verified"},
				{Name: "Serato", Badge: "unfinished", BadgeVar: "error", Note: "LED echo loops"},
			},
		}
	}
	emptyCtls := func() midiCtlsState {
		return midiCtlsState{Links: []midiLinkState{}, Blocks: []midiCtlBlock{}}
	}
	emptyUM := func() umState {
		return umState{Add: umRow{Trail: []umTrail{}}, Profiles: []umProfileRow{}}
	}
	emptyDrv := func() midiDrvCard {
		return midiDrvCard{Managed: midiDrvManaged{Inputs: []midiDrvInput{}, Trace: midiTraceState{Rows: []midiTraceRow{}}}}
	}
	base := func() midiCtlState {
		return midiCtlState{
			Title: "MIDI", Sub: "Send MIDI so a DJ app can learn it",
			Ctls: emptyCtls(), UIMap: emptyUM(), Driver: emptyDrv(),
			Port: midiPortCard{
				Card: "Output", Sub: "Pick the loopback port",
				Port:   midiSel("midi-port", "Auto", selRow{Val: "", Label: "Auto", Cur: true}),
				Active: midiActiveState{Variant: "off", Label: "Active port", LabelDL: "active port", Line: "not open"},
				Panic:  "Panic",
			},
			Rack: rack(1), Bridge: midiBridgeState{ToDJ: emptySel(), FromDJ: emptySel()}, Help: help(),
			Mon: midiMonState{Lines: midiMonLines{Rows: []midiMonRow{}}},
		}
	}

	// unavailable: no controllers card, no mappings, no monitor, no driver, no bridge.
	unavailable := base()

	// empty: cards shown but nothing configured.
	empty := base()
	empty.Ctls = midiCtlsState{
		Show: true, Card: "Controllers", Badge: "input", Intro: "Connect a controller",
		IntroTip: `<label class=tt data-label="tt-midi-learn-controllers"></label>`,
		LinksLbl: "Need a virtual port?",
		Links: []midiLinkState{
			{Label: "loopMIDI", URL: "https://www.tobias-erichsen.de/software/loopmidi.html"},
			{Label: "LoopBe1", URL: "https://www.nerds.de/en/loopbe1.html"},
		},
		Empty: "No controllers yet", Blocks: []midiCtlBlock{}, Add: "Add controller",
	}
	empty.UIMap = umState{
		Show: true, Title: "Control rave-mate", TitleTip: `<label class=tt data-label="tt-midi-mapping"></label>`,
		Sub: "Map controls to app actions", EnableLbl: "Enabled", EnableDL: "enabled", EnableAct: "um-enable",
		EnableOn: true, EnableTip: `<label class=tt data-label="tt-midi-mapping"></label>`,
		Add: umRow{Title: "Add mapping", Sub: "Pick an action, then touch a control", Trail: []umTrail{
			umSelTrail(midiSel("um-add", "Learn", selRow{Val: "ui.ce.audition", Label: "Audition", Sub: "Cue editor · trigger"})),
		}},
		Profiles: []umProfileRow{{
			Row:   umRow{Title: "Any device", Sub: "0 mappings", Trail: []umTrail{umBtnTrail("On", "secondary", "um-prof:*")}},
			Empty: "No mappings on this profile", Binds: []umRow{},
		}},
		Note: "Mappings are shared with the VR binds",
	}
	empty.ShowMon = true
	empty.Mon = midiMonState{Card: "Monitor", Badge: "live", Sub: "Press a control", Lines: midiMonLines{Empty: "No input yet", Rows: []midiMonRow{}}}
	empty.Driver = midiDrvCard{
		Show: true, Card: "ravemidi driver", Badge: "preview", BadgeVar: "warning",
		Why: "Why a driver?", StVariant: "muted", StLabel: "Driver", StLabelDL: "driver", StLine: "not installed",
		TestSign: "Test-signed build", Steps: "Install steps:", Cmds: driverInstallCmds, SmartScreen: "SmartScreen will warn",
		Managed: midiDrvManaged{Inputs: []midiDrvInput{}, Trace: midiTraceState{Rows: []midiTraceRow{}}},
		Docs:    "Driver docs", DocsURL: "https://github.com/rave-page/rave-mate/tree/development/driver/ravemidi",
	}
	empty.Bridge = midiBridgeState{
		Show: true, Card: "DJ bridge", Badge: "router", Intro: "Route peer control to the DJ app",
		IntroTip:  `<label class=tt data-label="tt-midi-bridge"></label>`,
		EnableLbl: "Enabled", EnableDL: "enabled", EnableAct: "midi-bridge-enable",
		EnableTip: `<label class=tt data-label="tt-midi-bridge"></label>`,
		ToDJ:      midiSel("midi-bridge-todj", "None"), ToDJLbl: `<span class=ss-label>To DJ</span>`,
		FromDJ: midiSel("midi-bridge-fromdj", "None"), FromDJLbl: `<span class=ss-label>From DJ</span>`,
	}

	// populated: two controllers (one driver-managed + clashing THRU), binds, driver installed.
	mkPopulated := func() midiCtlState {
		populated := empty
		populated.Ctls.Blocks = []midiCtlBlock{
			{
				Tid: "midi-ctl-0", Title: "DDJ-400", StatID: "midi-ctlstat-0",
				Port:    midiSel("midi-ctl-port-0", "DDJ-400", selRow{Val: "DDJ-400", Label: "DDJ-400", Cur: true}),
				PortLbl: `<span class=ss-label>Port<label class=tt data-label="tt-midi-in-port"></label></span>`,
				Stat: midiPortStat{
					HasRow: true, Variant: "ok", Label: "Port status", LabelDL: "port status", Line: "reading",
					HasAct: true, Act: "last input 2s ago", ActMsg: "CC 20 ch1 = 127",
				},
				EnableLbl: "Enabled", EnableDL: "enabled", EnableAct: "midi-ctl-enable:0", EnableOn: true,
				Thru:    midiSel("midi-ctl-thru-0", "ravemidi driver", selRow{Val: "\x00drv", Label: "ravemidi driver", Cur: true}),
				ThruLbl: `<span class=ss-label>THRU<label class=tt data-label="tt-midi-thru"></label></span>`,
				DrvThru: midiDrvThru{
					Show: true, UseInDJ: "Use in your DJ app:", Port: "DDJ-400",
					CloneLbl: "Clone device name", CloneDL: "clone device name", CloneAct: "midi-ctl-clone:0", CloneOn: true,
					CloneNote: "Serato matches by name", DrvNote: "Forwarding lives in the driver",
					HasState: true, StVariant: "success", StLabel: "Driver state", StLabelDL: "driver state",
					StLine: "bound · feedback", FilterLbl: "Drop messages",
					FilterTip: `<label class=tt data-label="tt-midi-drv-filter"></label>`,
					Chips: []midiChipState{
						{Label: "Aftertouch", Act: "midi-ctl-filter:0:aftertouch", Active: true},
						{Label: "Bend", Act: "midi-ctl-filter:0:bend"},
					},
				},
				Remove: "Remove", RemoveAct: "midi-ctl-remove:0", Grid: grid(2, true),
			},
			{
				Tid: "midi-ctl-1", Title: "New controller", StatID: "midi-ctlstat-1",
				Port:    midiSel("midi-ctl-port-1", "(select…)"),
				PortLbl: `<span class=ss-label>Port</span>`,
				Stat: midiPortStat{
					HasRow: true, Variant: "warn", Label: "Port status", LabelDL: "port status",
					Line: "in use", Hint: "Close the other app or route via loopMIDI",
				},
				EnableLbl: "Enabled", EnableDL: "enabled", EnableAct: "midi-ctl-enable:1",
				Thru: midiSel("midi-ctl-thru-1", "loopMIDI Port"), ThruLbl: `<span class=ss-label>THRU</span>`,
				DrvThru: midiDrvThru{Chips: []midiChipState{}},
				Warn: midiWarnState{
					Show: true, Label: "THRU clash", LabelDL: "thru clash",
					Line: "rave-mate already reads that port", Hint: "Use a dedicated virtual cable",
				},
				Remove: "Remove", RemoveAct: "midi-ctl-remove:1", Grid: grid(2, false),
			},
		}
		populated.UIMap.Profiles = []umProfileRow{
			{
				Row: umRow{Title: "DDJ-400", Sub: "2 mappings", Trail: []umTrail{
					umBtnTrail("On", "secondary", "um-prof:DDJ-400"),
					umSelTrail(midiSel("um-pcopy-0", "Copy to…", selRow{Val: "*", Label: "Any device", Sub: "copies every mapping"})),
					umBtnTrail("Clear", "ghost", "um-pclear:DDJ-400"),
				}},
				HasBinds: true, Empty: "No mappings on this profile",
				Binds: []umRow{
					{Title: "↳ Audition", Sub: "CC 20 (ch1) · Press", Trail: []umTrail{
						umSelTrail(midiSel("um-mode-0", "Press", selRow{Val: "", Label: "Press", Sub: "on press"})),
						umBtnTrail("✕", "ghost", "um-del:0"),
					}},
					{Title: "↳ Scroll library", Sub: "CC 21 (ch1) · Absolute", Trail: []umTrail{
						umSelTrail(midiSel("um-mode-1", "Absolute")),
						umSelTrail(midiSel("um-step-1", "Step 4", selRow{Val: "4", Label: "4", Sub: "4 rows"})),
						umBtnTrail("Reverse", "secondary", "um-rev:1"),
						umBtnTrail("✕", "ghost", "um-del:1"),
					}},
				},
			},
			{
				Row:   umRow{Title: "Any device", Sub: "Paused · 0 mappings", Trail: []umTrail{umBtnTrail("Off", "warn", "um-prof:*")}},
				Empty: "No mappings on this profile", Binds: []umRow{},
			},
		}
		populated.Mon.Lines.Rows = []midiMonRow{{Ago: "now", Src: "DDJ-400", Msg: "CC 20 ch1 = 127"}}
		populated.Driver = midiDrvCard{
			Show: true, Installed: true, Card: "ravemidi driver", Badge: "active", BadgeVar: "success",
			Why: "Why a driver?", StVariant: "success", StLabel: "Driver", StLabelDL: "driver", StLine: "installed",
			Managed: midiDrvManaged{
				Hdr: "Managed inputs", Sub: "Forwarding is kernel-side", SyncErr: "Sync failed: 0x5",
				NoneManaged: "Nothing managed yet",
				Inputs: []midiDrvInput{
					{
						Variant: "success", Name: "DDJ-400", NameDL: "ddj-400", Line: "bound · feedback",
						HasBtns: true, TraceLbl: "Open trace", TraceAct: "midi-drv-trace:3",
						FbTest: true, FbTestLbl: "LED test", FbTestAct: "midi-fbtest:3",
						FbTip: `<label class=tt data-label="tt-led-feedback"></label>`,
						FbRes: true, FbResVar: "success", FbResLbl: "LED test", FbResDL: "led test", FbResLine: "4 events",
					},
					{
						Variant: "warning", Name: "Z1", NameDL: "z1", Line: "retrying (3)",
						FbHint: "Render pin not bound", HasBtns: true, TraceLbl: "Open trace", TraceAct: "midi-drv-trace:4",
					},
				},
				ShowTrace: true,
				Trace: midiTraceState{
					Hdr: "Wire trace · port 3", Empty: "empty", Refresh: "Refresh", Close: "Close",
					Rows: []midiTraceRow{{DT: "+3ms", Dir: "1", Label: "to app", Hex: "B0 14 7F", Len: "3B", Dec: "CC 20 = 127"}},
				},
				Reapply: "Re-apply", Reload: "Reload",
			},
			Docs: "Driver docs", DocsURL: "https://github.com/rave-page/rave-mate/tree/development/driver/ravemidi",
		}
		populated.Rack = rack(2)
		populated.Bridge.EnableOn = true
		populated.Bridge.ToDJ = midiSel("midi-bridge-todj", "loopMIDI Port", selRow{Val: "loopMIDI Port", Label: "loopMIDI Port", Cur: true})
		return populated
	}
	populated := mkPopulated()

	// escaping: every dynamic field carries & ' < > " (attrs + text + select rows + tooltips raw).
	escaping := mkPopulated()
	escaping.Title = `MI&DI <"tab">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Ctls.Card = `Con&trollers <"in">`
	escaping.Ctls.Intro = `intro &<>"'`
	escaping.Ctls.Links = []midiLinkState{{Label: `loop&MIDI <"x">`, URL: `https://x/?a=1&b="2"`}}
	escaping.Ctls.Add = `Ad&d <"ctl">`
	escaping.Ctls.Blocks = []midiCtlBlock{{
		Tid: `midi-ctl-0"&'`, Title: `DDJ&"400"'<>`, StatID: "midi-ctlstat-0",
		Port: selState{ID: `p&"0"`, CurLabel: `cur&"'<>`, Open: true, Filter: `f&"'<>`,
			Rows: []selRow{{Val: `v&"'<>`, Label: `l&"'<>`, Sub: `s&"'<>`, Badge: `b&"'<>`, Cur: true}}},
		PortLbl:   `<span class=ss-label>P&ort<label class=tt data-label="tt-x"></label></span>`,
		Stat:      midiPortStat{HasRow: true, Variant: "warn", Label: `Po&rt "st"`, LabelDL: `po&rt "st"`, Line: `in &"use"`, Hint: `hi&nt<">`, HasAct: true, Act: `la&st "2s"`, ActMsg: `CC &<20>"'`},
		EnableLbl: `En&abled"`, EnableDL: `en&abled"`, EnableAct: `midi-ctl-enable:0&"`, EnableOn: true,
		Thru:    selState{ID: `t&"0"`, CurLabel: `thru&"'`, Rows: []selRow{}},
		ThruLbl: `<span class=ss-label>TH&RU</span>`,
		DrvThru: midiDrvThru{
			Show: true, UseInDJ: `Use &<"in">`, Port: `DDJ&"400"`,
			CloneLbl: `Clo&ne"`, CloneDL: `clo&ne"`, CloneAct: `midi-ctl-clone:0&`, CloneOn: false,
			CloneNote: `no&te<">`, DrvNote: `drv&"note"`,
			HasState: true, StVariant: "warning", StLabel: `Drv &"st"`, StLabelDL: `drv &"st"`, StLine: `retry &<3>`,
			FilterLbl: `Dro&p "msgs"`, FilterTip: `<label class=tt data-label="tt-f&x"></label>`,
			Chips: []midiChipState{{Label: `After&touch"`, Act: `midi-ctl-filter:0:after&touch`, Active: true}},
		},
		Warn:   midiWarnState{Show: true, Label: `Cla&sh"`, LabelDL: `cla&sh"`, Line: `li&ne<">`, Hint: `hi&nt'`},
		Remove: `Re&move"`, RemoveAct: `midi-ctl-remove:0&`,
		Grid: midiLearnGridState{
			Hdr: `Le&arn "grid"`, HdrTip: `<label class=tt data-label="tt-g&x"></label>`, Cols: "1",
			ChHdrs: []string{`ch&1"`}, Learn: `Le&arn"`, Relearn: `Re&learn"`, Clear: `Cl&ear"`,
			Rows: []midiLearnRow{{Label: `eq&High"`, Cells: []midiLearnCell{
				{Act: `midi-learn:0:eq&High:1`, ClearAct: `midi-unlearn:0:eq&High:1`, Tid: `t&"1"`, Set: true, Readout: `CC&24"`},
				{Act: `midi-learn:0:eq&High:2`, Tid: `t&"2"`},
			}}},
		},
	}}
	escaping.UIMap.Title = `Con&trol <"rave-mate">`
	escaping.UIMap.Sub = `su&b"<>`
	escaping.UIMap.EnableLbl = `En&able"`
	escaping.UIMap.EnableDL = `en&able"`
	escaping.UIMap.Note = `no&te<">`
	escaping.UIMap.Add = umRow{Title: `Ad&d "map"`, Sub: `su&b'<>`, Trail: []umTrail{
		umBtnTrail(`Can&cel"`, "warn", `um-learn:ui.ce&audition`),
	}}
	escaping.UIMap.Profiles = []umProfileRow{{
		Row:      umRow{Title: `DDJ&"400"`, Sub: `2 &"maps"`, Trail: []umTrail{umBtnTrail(`O&n"`, "secondary", `um-prof:DDJ&400`)}},
		HasBinds: true, Empty: `em&pty"`,
		Binds: []umRow{{Title: `↳ Au&dition"`, Sub: `CC 20 &<ch1>`, Trail: []umTrail{umBtnTrail("✕", "ghost", `um-del:0&`)}}},
	}}
	escaping.Mon = midiMonState{Card: `Mo&n"`, Badge: `li&ve"`, Sub: `su&b"`, Lines: midiMonLines{
		Empty: `no&ne"`, Rows: []midiMonRow{{Ago: `1s&`, Src: `DDJ&"400"`, Msg: `CC &<20>"'`}},
	}}
	escaping.Port = midiPortCard{
		Card: `Out&put"`, Sub: `su&b<">`,
		Port:   selState{ID: `midi-port&`, CurLabel: `Au&to"`, Rows: []selRow{}},
		Active: midiActiveState{Variant: "ok", Label: `Ac&tive"`, LabelDL: `ac&tive"`, Line: `port &"1"`},
		Panic:  `Pa&nic"`,
	}
	escaping.Driver.Managed.Hdr = `Man&aged"`
	escaping.Driver.Managed.SyncErr = `sync &"failed"`
	escaping.Driver.Managed.Inputs = []midiDrvInput{{
		Variant: "success", Name: `DDJ&"400"`, NameDL: `ddj&"400"`, Line: `bo&und"`,
		HasBtns: true, TraceLbl: `Tr&ace"`, TraceAct: `midi-drv-trace:3&`,
		FbTest: true, FbTestLbl: `LED &"test"`, FbTestAct: `midi-fbtest:3&`, FbTip: `<label class=tt data-label="tt-l&f"></label>`,
		FbRes: true, FbResVar: "warning", FbResLbl: `LED &"lbl"`, FbResDL: `led &"lbl"`, FbResLine: `0 &<events>`,
	}}
	escaping.Driver.Managed.Trace.Hdr = `Tra&ce "3"`
	escaping.Rack.Card = `Ra&ck"`
	escaping.Rack.StepLbl = `Chan&nels"`
	escaping.Rack.Sub = `su&b"`
	escaping.Rack.Strips = []midiStripState{{
		Head: `Chan&nel 1 <"A">`,
		Knobs: []midiKnobState{{
			DL: `ch1 h&i"`, V: "0.5", Rot: "0", Val: "64", Act: `midi-send:0:24&`, Tid: `midi-ch1-eqHigh&`,
			Aria: `H&i "CC24"`, Label: `H&i"`, CC: `CC24&·ch1"`, SweepAct: `midi-sweep:0:24&`,
			SweepTitle: `Swe&ep"`, SweepAria: `Swe&ep "Hi"`, SweepGlyph: `↯&`,
		}},
		Faders: []midiKnobState{{
			DL: `ch1 f&ader"`, V: "0", Val: "0", Act: `midi-send:0:23&`, Tid: `midi-ch1-fader&`,
			Aria: `F&ader"`, Label: `F&ader"`, CC: `CC23&·ch1`, SweepAct: `midi-sweep:0:23&`,
			SweepTitle: `Swe&ep"`, SweepAria: `Swe&ep "F"`, SweepGlyph: `↯&`,
		}},
		Btns: []midiMomState{{
			Cls: `midi-btn midi-btn--play&`, Act: `midi-note:0:20&`, Tid: `midi-ch1-play&`,
			DL: `ch1 pl&ay"`, Aria: `Pl&ay "Note20"`, Label: `Pl&ay"`, CC: `Note20&·ch1`,
		}},
	}}
	escaping.Bridge.Card = `Brid&ge"`
	escaping.Bridge.Intro = `in&tro"`
	escaping.Bridge.EnableLbl = `En&able"`
	escaping.Bridge.EnableDL = `en&able"`
	escaping.Bridge.ToDJLbl = `<span class=ss-label>To &DJ</span>`
	escaping.Bridge.FromDJLbl = `<span class=ss-label>From &DJ</span>`
	escaping.Help.Card = `He&lp"`
	escaping.Help.Step1 = `st&ep1<">`
	escaping.Help.Caveat = `ca&veat"`
	escaping.Help.Link = `li&nk"`
	escaping.Help.Rows = []midiSwRow{{Name: `Trak&tor "Pro"`, Badge: `sta&ble"`, BadgeVar: "success", Note: `no&te"`}}

	// long: oversized labels/ids everywhere the layout could clip.
	long := mkPopulated()
	big := strings.Repeat("very-long-", 120)
	long.Title = big
	long.Ctls.Card = big
	long.Ctls.Blocks[0].Title = big
	long.Ctls.Blocks[0].Stat.ActMsg = strings.Repeat("m", 900)
	long.Ctls.Blocks[0].DrvThru.Port = strings.Repeat("p", 400)
	long.Ctls.Blocks[0].Grid.Rows[0].Cells[0].Readout = strings.Repeat("C", 200)
	long.UIMap.Profiles[0].Row.Title = big
	long.Rack.Strips[0].Head = big
	long.Help.Rows[0].Note = big

	// unicode: 7-locale reality (ru/ja/uk + emoji) through every escaper.
	unicode := mkPopulated()
	unicode.Title = "МИДИ 🎛️"
	unicode.Sub = "größer ミディ"
	unicode.Ctls.Card = "Контроллеры"
	unicode.Ctls.Blocks[0].Title = "コントローラー"
	unicode.Ctls.Blocks[0].EnableLbl = "Включено"
	unicode.Ctls.Blocks[0].EnableDL = "включено"
	unicode.Ctls.Blocks[0].Grid.Rows[0].Label = "Высокие"
	unicode.UIMap.Title = "Керування rave-mate"
	unicode.UIMap.Profiles[0].Row.Sub = "2 привʼязки"
	unicode.Mon.Lines.Rows = []midiMonRow{{Ago: "сейчас", Src: "コントローラー", Msg: "нота 36 · 中文 🎧"}}
	unicode.Rack.Strips[0].Head = "Канал 1 (A)"
	unicode.Rack.Strips[0].Knobs[0].Label = "Высокие"
	unicode.Rack.Strips[0].Knobs[0].DL = "ch1 высокие"
	unicode.Help.Rows[0].Note = "проверено ✅"

	return map[string]midiCtlState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigMIDICtlGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range midiCtlFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderMIDICtl(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", midiCtlHTML(st), zig)

			ajs := stateJSON(st.Port.Active)
			if ajs == nil {
				t.Fatal("active marshal failed")
			}
			zigActive, ok := zigui.RenderMIDIActive(ajs)
			if !ok {
				t.Fatal("zig active render failed")
			}
			assertBytesEqual(t, "active", midiActiveRowHTML(st.Port.Active), zigActive)

			for i, bl := range st.Ctls.Blocks {
				sjs := stateJSON(bl.Stat)
				if sjs == nil {
					t.Fatalf("stat %d marshal failed", i)
				}
				want := midiPortStatHTML(bl.Stat)
				zigStat, ok := zigui.RenderMIDICtlStat(sjs)
				if !ok {
					// empty fragment ⇒ Zig returns NULL; the Go fallback renders "" too
					if want != "" {
						t.Fatalf("stat %d: zig render failed but Go rendered %d bytes", i, len(want))
					}
					continue
				}
				assertBytesEqual(t, "stat", want, zigStat)
			}
		})
	}
}
