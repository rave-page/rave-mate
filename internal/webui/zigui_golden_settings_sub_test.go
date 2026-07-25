//go:build zigui

package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/bridge"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/gridfix/train"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/updater"
	"rave.page/mate/internal/zigui"
)

// Golden gate for the four settings SUB-VIEW bodies (settings_sub.zig): each renders
// byte-identically to its pure Go renderer, both standalone (its own export) and embedded in the
// settings block walk (#set-content). The states come from the REAL state builders
// (gridfixCardStateOf / gridfixModelStateOf / bridgeCardStateOf / updFlowStateOf).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func gfCardFixtures() map[string]gfCardSt {
	vers := &gridfix.Versions{BeatThis: "0.1.2", Torch: "2.4.0+cu124"}
	full := &config.GridFixFeature{PythonPath: `C:\py\python.exe`, Device: "cuda",
		MinQuality: 0.8, ThresholdMS: 12, LockFixed: true,
		BiasExt: map[string]float64{".mp3": 42.7, "*": -2.9}}
	esc := &config.GridFixFeature{PythonPath: `C:\p&y\"python".exe <1>`, Device: "auto"}
	uni := &config.GridFixFeature{PythonPath: `C:\Пайтон\питон.exe 🎧`, MinQuality: 0.925}
	long := &config.GridFixFeature{PythonPath: strings.Repeat("p/", 500)}
	both := gridfix.EnvStatus{BasePython: "py", BaseVersion: "3.12.10", GPUPresent: true,
		CPU:  gridfix.VariantStatus{Python: "p", Root: "r", EngineOK: true, Versions: vers},
		CUDA: gridfix.VariantStatus{Python: "c", Root: "rc", EngineOK: true, Versions: vers}}
	return map[string]gfCardSt{
		"probing":  gridfixCardStateOf(&config.GridFixFeature{}, gridfix.EnvStatus{}, false),
		"noPython": gridfixCardStateOf(&config.GridFixFeature{}, gridfix.EnvStatus{}, true),
		"notInstalled": gridfixCardStateOf(full,
			gridfix.EnvStatus{BasePython: `C:\py\python.exe`, BaseVersion: "3.12.10"}, true),
		"cudaGated": gridfixCardStateOf(full,
			gridfix.EnvStatus{BasePython: "py", CPU: gridfix.VariantStatus{Python: "p", EngineOK: true, Versions: vers}}, true),
		"cudaHint": gridfixCardStateOf(full,
			gridfix.EnvStatus{BasePython: "py", GPUPresent: true, CPU: gridfix.VariantStatus{Python: "p", EngineOK: true}}, true),
		"broken": gridfixCardStateOf(full, gridfix.EnvStatus{BasePython: "py", GPUPresent: true,
			CPU: gridfix.VariantStatus{Python: "p"}, CUDA: gridfix.VariantStatus{Python: "c"}}, true),
		"bothReady": gridfixCardStateOf(full, both, true),
		"escaping": gridfixCardStateOf(esc, gridfix.EnvStatus{BasePython: "py", BaseVersion: `3.12 <"a&b">`,
			CPU: gridfix.VariantStatus{Python: "p", EngineOK: true,
				Versions: &gridfix.Versions{BeatThis: `0.1 <"x&y">`, Torch: "2.4'z'"}}}, true),
		"unicode": gridfixCardStateOf(uni, gridfix.EnvStatus{BasePython: "py", BaseVersion: "3.12 版",
			CPU: gridfix.VariantStatus{Python: "p", EngineOK: true,
				Versions: &gridfix.Versions{BeatThis: "0.1 β", Torch: "2.4 торч"}}}, true),
		"long": gridfixCardStateOf(long, gridfix.EnvStatus{BasePython: "py",
			BaseVersion: strings.Repeat("3.12.", 200)}, true),
	}
}

func gfModelFixtures(t *testing.T) map[string]gfModelSt {
	t.Helper()
	at := time.Date(2026, 7, 20, 15, 4, 0, 0, time.UTC)
	cps := []train.CheckpointInfo{
		{Path: `C:\m\ft-1.ckpt`, Name: "ft-1", At: at},
		{Path: `C:\m\ft&2 <"x">.ckpt`, Name: `ft&2 <"x">`, At: at.Add(time.Hour)},
		{Path: `C:\m\モデル.ckpt`, Name: "モデル 🎛️", At: at},
	}
	sel := func(active string, list []train.CheckpointInfo) selState {
		u := &UI{}
		u.gfProbe.checkpoints = list
		return u.gridfixModelSel(active)
	}
	done := &train.TrainEvent{Kind: "done", BeforeF: 0.812, AfterF: 0.877, Improved: true}
	worse := &train.TrainEvent{Kind: "done", BeforeF: 0.812, AfterF: 0.712}
	return map[string]gfModelSt{
		"builtin":      gridfixModelStateOf(sel("", nil), false, 0, false, train.TrainEvent{}, nil, ""),
		"checkpoints":  gridfixModelStateOf(sel(`C:\m\ft-1.ckpt`, cps), true, 40, false, train.TrainEvent{}, nil, ""),
		"canTrain":     gridfixModelStateOf(sel("", cps), true, 5, false, train.TrainEvent{}, nil, ""),
		"runPreparing": gridfixModelStateOf(sel("", cps), true, 3, true, train.TrainEvent{}, nil, ""),
		"runStart": gridfixModelStateOf(sel("", cps), true, 9, true,
			train.TrainEvent{Kind: "start", Tracks: 12, Device: "cuda"}, nil, ""),
		"runEpoch": gridfixModelStateOf(sel("", cps), true, 9, true,
			train.TrainEvent{Kind: "epoch", Epoch: 3, Loss: 0.12345, ValFBeat: 0.9876}, nil, ""),
		"verdictOK":  gridfixModelStateOf(sel("", cps), true, 22, false, train.TrainEvent{}, done, ""),
		"verdictBad": gridfixModelStateOf(sel("", cps), true, 2, false, train.TrainEvent{}, worse, ""),
		"error": gridfixModelStateOf(sel(`C:\m\ft&2 <"x">.ckpt`, cps), true, 21, false,
			train.TrainEvent{}, nil, `boom & <"failed">`),
		"unicode": gridfixModelStateOf(sel(`C:\m\モデル.ckpt`, cps), true, 30, false, train.TrainEvent{}, done, ""),
		"long": gridfixModelStateOf(sel("", []train.CheckpointInfo{{Path: strings.Repeat("p/", 400),
			Name: strings.Repeat("ckpt-", 200), At: at}}), true, 1, false, train.TrainEvent{}, nil,
			strings.Repeat("e", 800)),
	}
}

func bridgeFixtures() map[string]bridgeSt {
	exp := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	sessions := []authz.Session{
		{PeerID: "peer-abcdef0123456789", Label: "Chrome on Studio", Transport: authz.TransportBridge, ExpiresAt: exp},
		{PeerID: `peer&2 <"x">`, Label: "   ", Transport: authz.TransportLAN, ExpiresAt: exp},
		{PeerID: "p3", Label: `лаптоп 🎧 <"a&b">`, Transport: authz.TransportDirect, ExpiresAt: exp},
	}
	return map[string]bridgeSt{
		"noGate":     bridgeCardStateOf(bridgeBits{LocalStudio: true}),
		"online":     bridgeCardStateOf(bridgeBits{HasState: true, State: bridge.State{Registered: true, Devices: 2, Links: 3}, HasGate: true}),
		"signedOut":  bridgeCardStateOf(bridgeBits{HasState: true, State: bridge.State{SignedOut: true}, HasGate: true}),
		"relayError": bridgeCardStateOf(bridgeBits{HasState: true, State: bridge.State{Error: `relay 502 & <"down">`}, HasGate: true}),
		"connecting": bridgeCardStateOf(bridgeBits{HasState: true, HasGate: true}),
		"noAuth":     bridgeCardStateOf(bridgeBits{HasGate: true}),
		"enrolled":   bridgeCardStateOf(bridgeBits{HasGate: true, Enrolled: true, Persistent: true}),
		"memoryOnly": bridgeCardStateOf(bridgeBits{HasGate: true, Enrolled: true}),
		"pending": bridgeCardStateOf(bridgeBits{HasGate: true, LocalStudio: true,
			URI: "otpauth://totp/rave-mate:studio?secret=ABC&issuer=rave.page", Secret: "ABCDEF234567"}),
		"pendingEsc": bridgeCardStateOf(bridgeBits{HasGate: true,
			URI: `otpauth://x?a&b="c"'d'<e>`, Secret: `S&C <"1">`}),
		"sessions": bridgeCardStateOf(bridgeBits{HasState: true, State: bridge.State{Registered: true, Devices: 1},
			LocalStudio: true, HasGate: true, Enrolled: true, Persistent: true, Sessions: sessions}),
		"long": bridgeCardStateOf(bridgeBits{HasGate: true, Enrolled: true, Persistent: true,
			Sessions: []authz.Session{{PeerID: strings.Repeat("id", 400), Label: strings.Repeat("dev-", 300),
				Transport: authz.TransportBridge, ExpiresAt: exp}}}),
	}
}

func updFlowFixtures() map[string]updFlowSt {
	rel := func(v, notes string) *selfupdate.Release { return &selfupdate.Release{Version: v, Notes: notes} }
	return map[string]updFlowSt{
		"hidden":        {}, // no manager / dev build: renders "" (Zig NULL, Go "")
		"idleUnchecked": updFlowStateOf(updater.Status{}),
		"upToDate":      updFlowStateOf(updater.Status{Checked: true}),
		"checkFailed":   updFlowStateOf(updater.Status{Checked: true, Err: `dns lookup failed & <"x">`}),
		"available":     updFlowStateOf(updater.Status{State: updater.Available, Rel: rel("1.2.3", "")}),
		"availNotes":    updFlowStateOf(updater.Status{State: updater.Available, Rel: rel("1.2.3", "fixes & things\n<b>bold</b>")}),
		"availErr":      updFlowStateOf(updater.Status{State: updater.Available, Rel: rel("1.2.3", "n"), Err: "download failed"}),
		"downloading":   updFlowStateOf(updater.Status{State: updater.Downloading, Rel: rel("1.2.3", ""), Progress: 0.4237}),
		"dlClamped":     updFlowStateOf(updater.Status{State: updater.Downloading, Rel: rel("1.2.3", ""), Progress: 1.9}),
		"downloaded":    updFlowStateOf(updater.Status{State: updater.Downloaded, Rel: rel("1.2.3", "")}),
		"applyFailed":   updFlowStateOf(updater.Status{State: updater.Downloaded, Rel: rel("1.2.3", ""), Err: "install failed: locked"}),
		"staged":        updFlowStateOf(updater.Status{State: updater.Staged, Rel: rel("1.2.3", "")}),
		"unicode":       updFlowStateOf(updater.Status{State: updater.Available, Rel: rel("1.2.3 ✓", "заметки 🎧\n二行目")}),
		"long":          updFlowStateOf(updater.Status{State: updater.Available, Rel: rel(strings.Repeat("9.", 300), strings.Repeat("note ", 400))}),
	}
}

func TestZigSettingsSubGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range gfCardFixtures() {
		t.Run("gridfix/"+name, func(t *testing.T) {
			zigFrag(t, "gridfix", gfCardHTML(st), stateJSON(st), zigui.RenderSettingsGridfix)
		})
	}
	for name, st := range gfModelFixtures(t) {
		t.Run("gridfixmodel/"+name, func(t *testing.T) {
			zigFrag(t, "gridfixmodel", gfModelHTML(st), stateJSON(st), zigui.RenderSettingsGridfixModel)
		})
	}
	for name, st := range bridgeFixtures() {
		t.Run("bridge/"+name, func(t *testing.T) {
			zigFrag(t, "bridge", bridgeCardHTML(st), stateJSON(st), zigui.RenderSettingsBridge)
		})
	}
	for name, st := range updFlowFixtures() {
		t.Run("updflow/"+name, func(t *testing.T) {
			want := updFlowHTMLOf(st)
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderSettingsUpdFlow(js)
			if !ok {
				// hidden / not-checked-yet states render EMPTY ⇒ NULL; the Go fallback must agree
				if want != "" {
					t.Fatalf("zig render failed but Go rendered %d bytes", len(want))
				}
				return
			}
			assertBytesEqual(t, "updflow", want, zig)
		})
	}
}

// TestZigSettingsSubBlocksGolden runs the sub-view states through the settings BLOCK WALK (the
// path the tab really uses): one #set-content pane holding a card per body.
func TestZigSettingsSubBlocksGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	gf := gfCardFixtures()
	gfm := gfModelFixtures(t)
	brg := bridgeFixtures()
	upd := updFlowFixtures()
	cards := []setCardSt{
		{ID: "gridfix", Title: "Beatgrid fixer", St: setStatusSt{V: "ok", T: "cpu"},
			Blocks: []setBlock{sbGridfix(gf["bothReady"])}},
		{ID: "gridfix2", Title: "Beatgrid fixer (probing)", St: setStatusSt{V: "off", T: ""},
			Blocks: []setBlock{sbGridfix(gf["probing"])}},
		{ID: "gridfixmodel", Title: "Model", St: setStatusSt{V: "live", T: "training"},
			Blocks: []setBlock{sbGridfixModel(gfm["runEpoch"])}},
		{ID: "accountbridge", Title: "Account bridge", St: setStatusSt{V: "go", T: "online"},
			Blocks: []setBlock{sbBridge(brg["sessions"])}},
		{ID: "accountbridge2", Title: "Account bridge (enrolling)", St: setStatusSt{V: "warn", T: ""},
			Blocks: []setBlock{sbBridge(brg["pendingEsc"])}},
		{ID: "updates", Title: "Updates", St: setStatusSt{V: "ok", T: "1.2.3"}, Blocks: []setBlock{
			sbKV("Version", "1.2.3"),
			sbUpdRegion("inst-update", upd["availNotes"]),
			sbNote("Updates are verified before install")}},
		{ID: "updates2", Title: "Updates (hidden flow)", St: setStatusSt{V: "off", T: ""},
			Blocks: []setBlock{sbUpdRegion("inst-update", upd["hidden"])}},
	}
	ct := setContentSt{
		Nav:  []setNavSt{{ID: "libmedia", Title: "Library & media", Agg: "ok", Active: true}},
		Secs: []setSecSt{{ID: "libmedia", Desc: "Player, transcode, beatgrids", Cards: cards}},
	}
	zigFrag(t, "content", setContentHTML(ct), stateJSON(ct), zigui.RenderSettingsContent)
}
