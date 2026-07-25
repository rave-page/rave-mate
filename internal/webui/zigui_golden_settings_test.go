//go:build zigui

package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrdll"
	"rave.page/mate/internal/zigui"
)

// Settings golden gate: the Zig renderer must be BYTE-IDENTICAL to the Go renderers for
// representative states - full view + the #set-content pane + #stset-<id> status fragments.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig
//
// The fixtures drive the REAL state builders (settingsState/settingsContentState/cardBlocks) off
// a synthetic UI, so every card in every section is exercised: 40+ cards, all block kinds
// (note/hint/empty/field/toggle/gated toggle/select/selectTip/amenu/fpair/btn-row/path-row/
// item-row/kv/install/form/region/raw).

// setFixtureUI builds a settings UI over a config; populated = every feature configured.
func setFixtureUI(populated bool) *UI {
	cfg := &config.Config{}
	cfg.APIBaseURL = "https://development.api.rave.page"
	u := &UI{svc: ui.Services{Cfg: cfg}, active: "settings"}
	u.probes.ready = true // gates + install cards only render from real probe data
	u.probes.at = time.Now()
	if !populated {
		return u
	}
	f := &cfg.Features
	f.Traktor.Enabled, f.Traktor.Port, f.Traktor.LogPayloads = true, 8080, true
	f.Traktor.MappingVersion = "auto"
	f.MIDI.Enabled, f.MIDI.CustomPort, f.MIDI.DenonPort, f.MIDI.MeshForward = true, "LoopBe", "Denon", true
	f.NML.Enabled, f.NML.CollectionPath = true, `C:\Users\dj\collection.nml`
	f.ProDJLink.Enabled = true
	f.Serato.Enabled, f.Serato.SeratoDir, f.Serato.NowPlaying = true, `C:\Music\_Serato_`, true
	f.Serato.Remote, f.Serato.RemoteDebug, f.Serato.LivePlaylist = true, true, true
	f.Serato.LivePlaylistURL, f.Serato.LivePlaylistInterval = "https://serato.com/playlists/x/live", 20
	f.VirtualDJ.Enabled, f.VirtualDJ.DatabaseDir, f.VirtualDJ.NetCtl = true, `C:\VDJ`, true
	f.VirtualDJ.NetCtlURL, f.VirtualDJ.NetCtlAuth = "http://127.0.0.1:8083", "secret"
	f.VirtualDJ.OS2L, f.VirtualDJ.Tracklist = true, true
	f.Rekordbox.Enabled, f.Rekordbox.DBPoll, f.Rekordbox.MemoryRead = true, true, true
	f.Recorder.Enabled, f.Recorder.ConfirmSeconds = true, 45
	f.SetCapture.Enabled, f.SetCapture.Port, f.SetCapture.Mount = true, 8000, "/stream"
	f.SetCapture.Username, f.SetCapture.Password, f.SetCapture.SetsDir = "source", "hackme", `C:\Sets`
	f.SetCapture.ReconnectGraceSeconds, f.SetCapture.SingleFile, f.SetCapture.MetadataOnly = 30, true, true
	f.AudioRecord.Enabled, f.AudioRecord.Device, f.AudioRecord.Format = true, "Line In", "flac"
	f.AudioRecord.Bitrate, f.AudioRecord.SampleRate, f.AudioRecord.Dir = 320, 48000, `C:\Rec`
	f.AudioRecord.FollowOBS, f.AudioRecord.WriteTags = true, true
	f.OBS.Enabled, f.OBS.Host, f.OBS.Port, f.OBS.Password = true, "127.0.0.1", 4455, "pw"
	f.OBS.Sync.Enabled, f.OBS.Sync.Fps, f.OBS.Sync.DeadBandFrames = true, 30, 2
	f.OBS.Sync.RestartThresholdMs = 1500
	f.Fingerprint.Enabled = true
	f.StreamBridge.Enabled, f.StudioChannel.Enabled = true, true
	f.Peers.Enabled, f.Peers.Nickname, f.Peers.RemoteCacheMaxMB = true, "studio-rig", 512
	f.AccountBridge.Enabled, f.AccountBridge.LocalStudio = true, true
	f.Webcam.Enabled, f.Webcam.AutoStart = true, true
	f.MediaLink.ShareVideo, f.MediaLink.PreferCodec = true, "hevc"
	f.MediaLink.BitrateKbps, f.MediaLink.MaxFPS, f.MediaLink.MaxHeight, f.MediaLink.SWOnly = 20000, 60, 1080, true
	f.Timecode.Enabled, f.Timecode.Rate, f.Timecode.StartAt = true, "25", "01:00:00:00"
	f.Timecode.LTC.On, f.Timecode.LTC.Device, f.Timecode.LTC.GainDb = true, "Speakers", -6
	f.Timecode.MTC.On, f.Timecode.MTC.Device = true, "loopMIDI"
	f.Timecode.ArtNet.On, f.Timecode.ArtNet.Addr = true, "255.255.255.255:6454"
	f.AbletonLink.Enabled, f.AbletonLink.Quantum, f.AbletonLink.TempoOwner = true, 16, "auto"
	f.AbletonLink.StartStopSync, f.AbletonLink.Resolume.Enabled = true, true
	f.AbletonLink.Resolume.Host, f.AbletonLink.Resolume.OSCPort = "127.0.0.1", 7000
	f.AbletonLink.Resolume.RESTPort, f.AbletonLink.Resolume.PhraseClipLayer = 8080, 1
	f.AbletonLink.Resolume.PhraseClipClip = 2
	f.Library.Enabled, f.Player.Embed = true, true
	f.MediaEditor.Enabled = true
	f.Transcode.Enabled, f.Transcode.FfmpegPath, f.Transcode.MaxConcurrent = true, `C:\ff\ffmpeg.exe`, 4
	f.GridFix.Enabled, f.GridFix.PythonPath, f.GridFix.Device = true, `C:\py\python.exe`, "cuda"
	f.GridFix.MinQuality, f.GridFix.ThresholdMS, f.GridFix.LockFixed = 0.8, 12, true
	f.Twitch.Enabled = true
	f.STT.Enabled, f.STT.InputDevice, f.STT.OutputDevice = true, "Mic", "out.txt"
	f.STT.Model, f.STT.SilenceMs, f.STT.AutoSubmit = "ggml-base.en.bin", 900, true
	f.VRChat.Enabled, f.VRChat.RememberSession, f.VRChat.Uplink = true, true, true
	f.VRCTools.Enabled, f.VRCTools.OrganizePhotos, f.VRCTools.OrganizeByEvent = true, true, true
	f.VRCTools.PhotoMove, f.VRCTools.OrganizeCamPaths, f.VRCTools.CamPathMove = true, true, true
	f.VRCTools.AutoBackupCamPaths, f.VRCTools.AutoRestoreCamPaths = true, true
	f.VRCTools.OSCAddr, f.VRCTools.DefaultCamPreset = "127.0.0.1:9000", "club"
	f.WorldSync.Enabled, f.WorldSync.PublishMode, f.WorldSync.HostedWorldID = true, "hosted", "wrld_abc"
	f.VROverlay.Enabled, f.VROverlay.EditHand, f.VROverlay.SummonButton = true, "left", "ax"
	f.VROverlay.SummonTapHides, f.VROverlay.AutoStart, f.VROverlay.VRViewCapture = true, true, true
	f.VROverlay.OSCAddr, f.VROverlay.VMCAddr, f.VROverlay.VMCLive = "127.0.0.1:9000", "127.0.0.1:39539", true
	f.DMX.Enabled, f.DMX.ListenAddr, f.DMX.Universes = true, "0.0.0.0:6454", []int{1, 2, 3}
	f.DMX.Grid.Enabled, f.DMX.Grid.Mode, f.DMX.Grid.SpoutName, f.DMX.Grid.FPSCap = true, "rgb9", "rave-dmx", 30
	f.DMX.ReEmit, f.DMX.EmitTarget, f.DMX.SACN, f.DMX.SACNUniverses = true, "10.0.0.5:6454", true, []int{1}
	f.LightCue.Enabled, f.LightCue.Hz = true, 30
	f.DMXMIDI.Enabled, f.DMXMIDI.Device, f.DMXMIDI.Universes = true, "loopMIDI", []int{1}
	f.DMXMIDI.MaxPerSecond = 200
	f.RTSPServe.Enabled, f.RTSPServe.Source, f.RTSPServe.InputFormat = true, "desktop", "gdigrab"
	f.RTSPServe.Passthrough, f.RTSPServe.ListenAddr, f.RTSPServe.Path = true, ":8554", "/live"
	f.RTSPServe.FPS, f.RTSPServe.BitrateKbps = 30, 6000
	f.Stream.Enabled, f.Stream.Transport, f.Stream.URL, f.Stream.StreamKey = true, "rtmp", "rtmp://x/live", "key"
	f.Stream.Mode, f.Stream.ColorMode, f.Stream.Universes = "extended", "rgb9", []int{1, 2}
	f.Stream.FPS, f.Stream.BitrateKbps, f.Stream.Encoder = 30, 6000, "nvenc"
	f.Mocap.Enabled, f.Mocap.Source, f.Mocap.Device, f.Mocap.Monitor = true, "spout", "cam", 1
	f.Mocap.FPS, f.Mocap.BoneSlots = 30, 12
	f.Mocap.StageMin, f.Mocap.StageSize = []float64{-5, 0, -5}, []float64{10, 4, 10}
	f.Crew.Enabled, f.Crew.EventID, f.Crew.Role, f.Crew.Label = true, "evt_1", "master", "booth"
	f.Unity.Enabled, f.Unity.Projects = true, []string{`C:\Unity\World`, `D:\Unity\Broken`}
	f.AppGroups.Enabled, f.Notifications.Enabled = true, true
	cfg.DisableCrashGuardian = true

	u.probes.tools = map[string]mediatools.Status{
		"ffmpeg": {Installed: true, Managed: true, Path: `C:\tools\ffmpeg.exe`},
		"fpcalc": {Installed: true, Path: `C:\tools\fpcalc.exe`},
	}
	u.probes.vr = vrdll.Status{Installed: true, Path: `C:\tools\openvr_api.dll`}
	u.probes.devs = map[string][]string{
		"midi":     {"LoopBe", "Denon"},
		"waveout":  {"Speakers", "Line Out"},
		"midiout":  {"loopMIDI"},
		"sttmic":   {"Mic"},
		"audiorec": {"Line In"},
	}
	u.probes.unity = map[string]unityproj.Project{
		`C:\Unity\World`: {Valid: true, Name: "World", HasPlugin: true},
	}
	return u
}

// ssOpen forces one registered smart select open with a filter (the ss-panel/filter/rows path).
func ssOpen(id, filter string) {
	ssMu.Lock()
	if ssSts[id] == nil {
		ssSts[id] = &ssSt{}
	}
	ssSts[id].open, ssSts[id].filter = true, filter
	ssMu.Unlock()
}

// setFixture is one settings state fixture: the UI + which sub-tab / query it renders.
type setFixture struct {
	u   *UI
	sec string
	q   string
}

func setFixtures() map[string]setFixture {
	unavailable := &UI{svc: ui.Services{}, active: "settings"}

	// zero config: every feature off, no probes landed → gates + "not found" install cards
	empty := setFixtureUI(false)

	populated := setFixtureUI(true)

	// features ON but their dependencies missing: gates suppress (an on feature stays
	// turn-off-able), install cards show the not-found state
	gated := setFixtureUI(true)
	gated.probes.tools = map[string]mediatools.Status{}
	gated.probes.vr = vrdll.Status{}
	gated.probes.devs = map[string][]string{}

	esc := setFixtureUI(true)
	ef := &esc.svc.Cfg.Features
	esc.svc.Cfg.APIBaseURL = `https://x/?a&b="c"'<>`
	ef.Peers.Nickname = `no&de "1" <rig>`
	ef.NML.CollectionPath = `C:\a&b\"c".nml`
	ef.Serato.SeratoDir = `C:\_Ser&ato_ <"x">`
	ef.Serato.LivePlaylistURL = `https://serato.com/p?a&b='c'`
	ef.VirtualDJ.NetCtlURL = `http://x/?q="&"`
	ef.OBS.Host = `ob&s "host"`
	ef.Timecode.StartAt = `01:00:00:00 <"a&b">`
	ef.DMX.EmitTarget = `10.0.0.5:6454 &"<>'`
	ef.RTSPServe.Path = `/li&ve"<>`
	ef.Stream.URL = `rtmp://x/li&ve?a="b"`
	ef.Mocap.Device = `ca&m "1"`
	ef.Crew.Label = `bo&oth "A" <1>`
	ef.Unity.Projects = []string{`C:\U&nity\"World"`}
	ef.VRCTools.OSCAddr = `127.0.0.1:9000 &'"`
	esc.probes.unity = map[string]unityproj.Project{
		`C:\U&nity\"World"`: {Valid: true, Name: `W&orld <"1">`, HasPlugin: false},
	}

	long := setFixtureUI(true)
	lf := &long.svc.Cfg.Features
	longS := strings.Repeat("very-long-", 120)
	lf.Peers.Nickname = longS
	lf.NML.CollectionPath = `C:\` + strings.Repeat("d/", 500)
	lf.Serato.LivePlaylistURL = "https://serato.com/" + strings.Repeat("p/", 400)
	lf.Unity.Projects = []string{strings.Repeat("u/", 400)}
	lf.Stream.StreamKey = strings.Repeat("k", 900)

	uni := setFixtureUI(true)
	uf := &uni.svc.Cfg.Features
	uni.svc.Cfg.APIBaseURL = "https://апи.рейв.страница/док"
	uf.Peers.Nickname = "スタジオ 🎧 больш"
	uf.Crew.Label = "ブース Б 🎛️"
	uf.Serato.SeratoDir = `C:\Музыка\_Serato_`
	uf.Unity.Projects = []string{`C:\Unity\世界`}
	uni.probes.unity = map[string]unityproj.Project{`C:\Unity\世界`: {Valid: true, Name: "世界 🌍", HasPlugin: true}}
	uni.probes.devs = map[string][]string{"midi": {"ループ 1", "Денон"}, "waveout": {"スピーカー"}}

	// an OPEN smart select (ss-panel + filter + filtered rows) on the media-link codec picker
	open := setFixtureUI(true)

	return map[string]setFixture{
		"unavailable":  {u: unavailable, sec: "account"},
		"account":      {u: empty, sec: "account"},
		"djsources":    {u: populated, sec: "djsources"},
		"recording":    {u: populated, sec: "recording"},
		"streaming":    {u: populated, sec: "streaming"},
		"libmedia":     {u: populated, sec: "libmedia"},
		"integrations": {u: populated, sec: "integrations"},
		"system":       {u: populated, sec: "system"},
		"emptyAll":     {u: empty, sec: "integrations"},
		"gated":        {u: gated, sec: "libmedia"},
		"escaping":     {u: esc, sec: "djsources"},
		"long":         {u: long, sec: "libmedia"},
		"unicode":      {u: uni, sec: "integrations"},
		"selectOpen":   {u: open, sec: "streaming"},
		"search":       {u: populated, sec: "account", q: "port"},
		"searchEsc":    {u: esc, sec: "account", q: `a&b<"c">`},
		"searchNone":   {u: populated, sec: "account", q: "zzz-no-such-setting"},
	}
}

func TestZigSettingsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, fx := range setFixtures() {
		t.Run(name, func(t *testing.T) {
			fx.u.setMu.Lock()
			fx.u.setSec, fx.u.setQuery = fx.sec, fx.q
			fx.u.setMu.Unlock()
			if name == "selectOpen" {
				ssOpen("set-ml-codec", "h") // registered by an earlier render of the same UI
				defer func() { ssOpen("set-ml-codec", ""); ssMu.Lock(); ssSts["set-ml-codec"].open = false; ssMu.Unlock() }()
			}

			st := fx.u.settingsState()
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderSettings(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", settingsHTML(st), zig)

			if !st.Available {
				return // the content pane only exists on the available view
			}
			zigFrag(t, "content", setContentHTML(st.Content), stateJSON(st.Content), zigui.RenderSettingsContent)
		})
	}
}

// TestZigSettingsStatusGolden pins the #stset-<id> tick fragment (dot + escaped state line).
func TestZigSettingsStatusGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for _, s := range []stv{
		stOff(""),
		stOk("https://development.api.rave.page"),
		stOk(`a&b <"c"> 'd'`),
		stWarn("not listening"),
		stLive("recording 3 tracks"),
		stOk("信号 ✓ больш"),
		stWarn(strings.Repeat("e", 500)),
	} {
		st := setStatusSt{V: s.v, T: s.t}
		zigFrag(t, "status", setStatusHTML(st), stateJSON(st), zigui.RenderSettingsStatus)
	}
}
