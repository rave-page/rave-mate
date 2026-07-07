package ui

import (
	_ "embed"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/appgroups"
	"rave.page/mate/internal/audiorec"
	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/dmx"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/filexfer"
	ghlink "rave.page/mate/internal/github"
	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/idmark"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/module"
	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/playsync"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/rtspserve"
	"rave.page/mate/internal/session/aggregator"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/shared/auth"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/timecode"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcmidi"
	"rave.page/mate/internal/vrcperm"
	"rave.page/mate/internal/vrctools"
	"rave.page/mate/internal/vroverlay"
	"rave.page/mate/internal/vrstats"
	"rave.page/mate/internal/webcam"
	"rave.page/mate/internal/worker"
)

const appID = "page.rave.mate"

//go:embed assets/icon.png
var iconBytes []byte

// uiLog is the bus goUI logs panics to; set once in New before any view builds.
var uiLog *logbus.Bus

// goUI runs fn panic-guarded; a UI goroutine panic logs instead of killing the app.
func goUI(source string, fn func()) { debuglog.Go(uiLog, "ui:"+source, fn) }

// PeerInfo identifies a paired instance for file transfer.
type PeerInfo struct {
	NodeID string
	Name   string
}

// FileXfer is the peer file-transfer surface (Peers-tab transfer list + the Library
// "Send to a paired instance…" context action). Wired over *filexfer.Manager +
// peerlink connections; nil Services.FileXfer hides both surfaces.
type FileXfer interface {
	SendToPeer(nodeID, path string) (id string, err error)
	Transfers() []filexfer.Transfer
	Cancel(id string)
	Accept(id string, ok bool)
	Peers() []PeerInfo // connected paired instances (send targets)
}

// Services are the backend handles the UI binds to.
type Services struct {
	Log          *logbus.Bus
	Cfg          *config.Config
	API          *api.Client
	Auth         *auth.Manager
	Traktor      *featurehost.TraktorProxy // subprocess-hosted Traktor listener (Listening/SetLogging)
	Stream       *featurehost.StreamProxy  // subprocess-hosted live-stream publisher
	Player       *featurehost.PlayerProxy  // subprocess-hosted audio playback engine (shared by all panels)
	Modules      *module.Manager
	Workers      *worker.Supervisor
	Hub          *jobs.Hub                     // shared transcode job fan-out (also used by the studio WS channel)
	Store        *store.Store                  // local persistence (analysis cache, automations, jobs); may be nil
	Lib          *libdb.DB                     // relational DJ-library store (tracks/sessions); may be nil
	OverlayArt   *overlayart.Resolver          // cover-art resolver (extract → DB store + disk cache); may be nil
	Syncer       *playsync.Syncer              // play-layer + playlist backend sync; may be nil
	Automations  automation.Manager            // media-automation engine facade; may be nil
	Session      *aggregator.Aggregator        // DJ-data aggregation hub (sources → merger → sinks); may be nil
	Recorder     *recorder.Recorder            // session tracklist recorder; may be nil
	SetCapture   *featurehost.IcecastProxy     // subprocess-hosted set-capture receiver (status + captures); may be nil
	OBS          *featurehost.ObsProxy         // subprocess-hosted OBS bridge (status + finished recordings); may be nil
	AbleLink     *featurehost.AbletonLinkProxy // subprocess-hosted Ableton Link session (state mirror + resync); may be nil
	AudioRec     *audiorec.Recorder            // native audio-device recorder (FLAC, OBS-synced + manual); may be nil
	TraktorMap   *traktormap.Manager           // activate/deactivate Traktor controller mappings; may be nil
	Identity     *identity.Identity            // stable node identity for the LAN peer link; may be nil
	Peers        *peerlink.Manager             // LAN peer-link connections; may be nil
	Discovery    *discovery.Discovery          // LAN mDNS discovery; may be nil
	PeerBridge   *peerbridge.Bridge            // live DJ-data bridge over the peerlink; may be nil
	NetStats     *netstats.Sampler             // 1 Hz network rate/RTT sampler (dashboard graphs); may be nil
	Perf         *perfmon.Monitor              // always-on 1 Hz perf collector (system-perf card); may be nil
	EventBus     *eventbus.Bus                 // cross-instance pub/sub bus (twitch/vr/obs.mic + capability routing); may be nil
	RemoteCtl    *remotectl.Endpoint           // peer-control RPC (drive a paired instance's automations/library); may be nil
	Vrchat       *vrchat.Manager               // VRChat account state machine (login/2FA/sealed session); may be nil
	GitHub       *ghlink.Auth                  // GitHub link (device flow / PAT, gist scope); may be nil
	WorldSync    *vrcperm.Service              // VRChat world gist feeds (perms/posters/events/now-playing); may be nil
	VrchatPipe   *featurehost.VrchatProxy      // subprocess-hosted VRChat pipeline WS (status mirror); may be nil
	Twitch       *twitch.Manager               // Twitch chat/alerts/title-control/moderation; may be nil
	VROverlay    vroverlay.Surface             // VR overlay control plane (in-proc or subprocess-proxied); may be nil
	OBSControl   *obscontrol.Manager           // cross-instance OBS stream/record control + status; may be nil
	VRStats      *vrstats.Collector            // VR perf/debug telemetry from any instance (monitor); may be nil
	VRCTools     *vrctools.Service             // VRChat screenshot organizer + camera-path manager; may be nil
	STT          *stt.Controller               // Whisper dictation controller (preview/copy/send); may be nil
	AppGroups    *appgroups.Service            // application-group launcher (crash recovery); may be nil
	DMX          *dmx.Router                   // DMX plane (Art-Net ingest + VRSL grid); may be nil
	DMXMIDI      *vrcmidi.Bridge               // DMX→MIDI VRChat bridge (rate-limited CC out); may be nil
	RTSP         *rtspserve.Server             // local RTSP performer chain (ffmpeg → rtspt); may be nil
	Timecode     *timecode.Service             // house SMPTE timecode outputs (LTC/MTC/Art-Net); may be nil
	Media        medialink.MediaControl        // LAN media plane: route stats + clock sync (Peers tab); in-proc or subprocess-proxied; may be nil
	MediaRoutes  mediaroute.ReceiveControl     // P4 video routes: remote-source listing + receive control; in-proc or subprocess-proxied; may be nil
	TCPlane      *medialink.TCPlane            // timecode master election/announce state (stays in-daemon); may be nil
	Webcam       webcam.CamControl             // webcam/UVC source: capture → Spout + PTZ control (Peers tab); in-proc or subprocess-proxied; may be nil
	FileXfer     FileXfer                      // file transfer to/from paired instances (Peers tab); may be nil
	VrchatUplink func(on bool)                 // apply the uplink toggle now (store/delete server vault); may be nil

	ReconcileLibSync func() // re-arm the auto-sync scheduler after a sync-job change; may be nil

	// IDMarks is the ID-mark store (unreleased-track redaction; internal/idmark). The
	// session merger redacts through it; the Library manages it. May be nil (tests).
	IDMarks *idmark.Store

	// SyncVRMAvatars runs a blocking all-peer avatar reconcile; returns (pulled, up-to-date,
	// errored) counts. Call off the UI thread. May be nil.
	SyncVRMAvatars func() (pulled, skipped, errored int)

	// MIDILearn registers a one-shot capture of the next note/CC press (status,data1) for the
	// keybind editor's "Learn MIDI" button; returns a cancel func. May be nil.
	MIDILearn func(onCapture func(status, data1 byte)) (cancel func())

	// Per-interface monitor buses (raw event firehoses for the Logs-area monitor tabs); may be nil.
	MIDIMon    *logbus.Bus // raw MIDI messages
	TraktorMon *logbus.Bus // Traktor HTTP ingest
	SessionMon *logbus.Bus // observations through the merger
}

// UI owns the Fyne app, window, and tray. The access token lives in the auth manager.
type UI struct {
	svc  Services
	app  fyne.App
	win  fyne.Window
	icon fyne.Resource
	tabs *container.AppTabs

	updater *selfupdate.Updater // self-update poller (disabled on a dev build)

	closers []func() // teardown for view subscriptions

	// Browser-style tab history (back/forward via Alt+←/→ + mouse X1/X2 where the OS hook works).
	navHist     []string
	navPos      int
	navApplying bool

	dashCards []string // in-memory dashboard layout when Cfg is nil (tests)

	peersRefresh        func()        // rebuilds the Peers tab list; set while that tab is built
	libraryPeersRefresh func()        // rebuilds the Library "Controlling" switcher; set while that tab is built
	recorderRefresh     func()        // in-place Recordings cockpit refresh; set while that tab is built
	settingsStats       []*cardStatus // live settings indicators; reset each buildSettings, driven by its ticker
	stopPlayback        func()        // stop in-app media playback (set by the studio view)
	stopOnce            sync.Once     // Stop() is reachable from ctx-cancel + tray Quit; run teardown once

	setPlayer *nativePlayer // captured-set playback (Publish tab); lazily created

	embeds []*embedController // active in-window mpv video embeds (suspended while a modal covers them)
}

// setAudioPlayer returns the captured-set audio player (Publish tab) - a thin adapter over
// the shared subprocessed player proxy.
func (u *UI) setAudioPlayer() *nativePlayer {
	if u.setPlayer == nil {
		u.setPlayer = &nativePlayer{proxy: u.svc.Player}
	}
	return u.setPlayer
}

// New builds the Fyne app + window (does not show). startHidden launches to tray only.
func New(svc Services) *UI {
	uiLog = svc.Log
	a := fyneapp.NewWithID(appID)
	a.Settings().SetTheme(newBrandTheme())
	icon := fyne.NewStaticResource("rave-mate.png", iconBytes)
	a.SetIcon(icon)

	u := &UI{svc: svc, app: a, icon: icon, updater: selfupdate.New(version.FeedURL, version.BuildNum(), version.UpdatePubKey)}
	u.win = a.NewWindow("rave-mate")
	u.win.SetIcon(icon)
	// Restore the user's last window size; first run opens at 85% of the primary monitor (wide
	// enough that the settings masonry reflows to 2+ columns); fixed default if the screen can't
	// be read.
	switch sw, sh, ok := screenSizeDIP(); {
	case svc.Cfg.WindowW >= 600 && svc.Cfg.WindowH >= 400:
		w, h := svc.Cfg.WindowW, svc.Cfg.WindowH
		// Clamp to the screen: restoring a canvas larger than the display leaves Fyne
		// mapping mouse coords against the un-clamped canvas while the OS clamps the
		// window - every click lands offset from the visuals.
		if ok {
			if w > sw {
				w = sw
			}
			if h > sh*0.96 { // taskbar headroom
				h = sh * 0.96
			}
		}
		u.win.Resize(fyne.NewSize(w, h))
	case ok && sw > 0 && sh > 0:
		u.win.Resize(fyne.NewSize(sw*0.85, sh*0.85))
	default:
		u.win.Resize(fyne.NewSize(1360, 820))
	}
	u.win.SetMaster()

	u.tabs = container.NewAppTabs(u.buildTabItems()...)
	u.tabs.SetTabLocation(container.TabLocationLeading)
	u.tabs.OnSelected = func(ti *container.TabItem) {
		if ti != nil {
			u.navRecord(ti.Text)
		}
	}
	u.navPos = -1
	if sel := u.tabs.Selected(); sel != nil {
		u.navRecord(sel.Text) // seed history with the initial tab
	}
	u.win.SetContent(u.tabs)
	u.installNavShortcuts() // Alt+←/→
	u.installMouseNav()     // mouse X1/X2 (Windows hook; no-op elsewhere)

	// Close = hide to tray (tray-resident; only tray Quit exits). AGENTS.md §1.
	// Stop media playback on hide so a preview doesn't keep playing in the background.
	u.win.SetCloseIntercept(func() {
		u.saveWindowSize() // remember the size the user pulled it to
		if u.stopPlayback != nil {
			u.stopPlayback()
		}
		u.win.Hide()
		u.svc.Log.Info("app", "window hidden to tray", nil)
	})
	u.closers = append(u.closers, u.saveWindowSize) // also persist on app exit

	u.installTray()
	if u.svc.Peers != nil {
		u.svc.Peers.AddListener(u.onPeerSAS, u.onPeerState)
	}
	// Wire the subprocessed player: tick/end/toast callbacks land on the Fyne thread (fyne.Do),
	// decode failures toast, and a child crash notifies like other feature subprocesses.
	if u.svc.Player != nil {
		u.svc.Player.SetDispatcher(fyne.Do)
		u.svc.Player.SetNotify(u.Notify)
		u.svc.Player.Host().SetNotifier(u.Notify)
	}
	// Wire the STT clipboard setter (keeps internal/stt UI-free): the stt.clipboard keybind +
	// preview "Copy" route the last transcript to the OS clipboard via Fyne.
	if u.svc.STT != nil {
		u.svc.STT.SetClipboard(func(s string) { u.app.Clipboard().SetContent(s) })
	}
	return u
}

// buildTabItems builds the tab set from current feature gates (workflow-shaped IA, see
// UI_WORKFLOW_IA.md). Live/Logs/Settings are always present; the rest appear when their
// feature is enabled (or their service exists). Rebuilt live by rebuildTabs when a
// tab-gating feature is toggled - no restart.
func (u *UI) buildTabItems() []*container.TabItem {
	on := func(get func(config.Features) bool) bool {
		return u.svc.Cfg == nil || get(u.svc.Cfg.Features)
	}
	// Live = the mid-set cockpit: transport strip + modular cards (now-playing, merged
	// decks from every enabled DJ source, streaming cockpit, …) + live-signal status
	// strip. Absorbs the old Dashboard + Session tabs.
	items := []*container.TabItem{
		container.NewTabItemWithIcon("Live", theme.HomeIcon(), u.buildLive()),
		container.NewTabItemWithIcon("Overlays", theme.ColorPaletteIcon(), u.buildOverlays()),
	}
	// Publish = finish a set: recordings + captures + tracklist link/export.
	if u.svc.Recorder != nil {
		items = append(items, container.NewTabItemWithIcon("Publish", theme.UploadIcon(), u.buildRecorder()))
	}
	if on(func(f config.Features) bool { return f.Library.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("Library", theme.StorageIcon(), u.buildStudio()))
	}
	if on(func(f config.Features) bool { return f.MediaEditor.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("Editor", theme.DocumentCreateIcon(), u.buildEditor()))
	}
	if u.svc.Automations != nil {
		items = append(items, container.NewTabItemWithIcon("Automations", theme.MediaPlayIcon(), u.buildAutomations()))
	}
	if u.svc.Peers != nil && on(func(f config.Features) bool { return f.Peers.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("Peers", theme.ComputerIcon(), u.buildPeers()))
	}
	if u.svc.Twitch != nil && on(func(f config.Features) bool { return f.Twitch.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("Twitch", theme.AccountIcon(), u.buildTwitch()))
	}
	if u.svc.Vrchat != nil && on(func(f config.Features) bool { return f.VRChat.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("VRChat", theme.MediaPhotoIcon(), u.buildVRChat()))
	}
	if u.svc.WorldSync != nil && on(func(f config.Features) bool { return f.WorldSync.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("Worlds", theme.VisibilityIcon(), u.buildWorldSync()))
	}
	if u.svc.AppGroups != nil && on(func(f config.Features) bool { return f.AppGroups.Enabled }) {
		items = append(items, container.NewTabItemWithIcon("App Groups", theme.ComputerIcon(), u.buildAppGroups()))
	}
	items = append(items,
		container.NewTabItemWithIcon("Logs", theme.ListIcon(), u.buildLogs()),
		container.NewTabItemWithIcon("Settings", theme.SettingsIcon(), u.buildSettings()),
	)
	return items
}

// rebuildTabs reapplies the feature gates live: it tears down the current tab set's view
// subscriptions (closers) and swaps in a freshly-built set, preserving the selected tab by
// name. Called when a tab-gating feature toggles, so a tab appears/disappears without restart.
func (u *UI) rebuildTabs() {
	if u.tabs == nil {
		return // not constructed yet (a toggle fired during initial build)
	}
	for _, c := range u.closers {
		c()
	}
	u.closers = nil
	sel := ""
	if cur := u.tabs.Selected(); cur != nil {
		sel = cur.Text
	}
	items := u.buildTabItems()
	u.tabs.SetItems(items)
	for _, it := range items {
		if it.Text == sel {
			u.tabs.Select(it)
			break
		}
	}
}

// Run shows the window (unless startHidden) and blocks on the Fyne event loop.
func (u *UI) Run(startHidden bool) {
	if startHidden {
		u.svc.Log.Info("app", "started hidden (tray only)", nil)
	} else {
		u.win.Show()
		u.showPreReleaseWarning()
	}
	selfupdate.CleanupOld()      // remove a prior update's .old binary
	u.checkUpdatesInBackground() // notify if a newer build is on the feed
	u.app.Run()
}

// showPreReleaseWarning warns on explicitly channel-stamped pre-release builds (GitHub
// alpha/beta releases), once per version - NOT on internal CI/dev builds (Channel unset),
// which would nag the daily-driver instances on every launch.
func (u *UI) showPreReleaseWarning() {
	if version.Channel == "" || !version.IsPreRelease() {
		return
	}
	if u.svc.Cfg != nil && u.svc.Cfg.PreReleaseWarnedFor == version.String() {
		return
	}
	msg := "This is a " + version.ResolvedChannel() + " build (" + version.String() + ") - a development release, not production.\n\n" +
		"Expect bugs. Always create backups of your media, library and configs before use. " +
		"Use at your own risk - the authors are not liable for any damage to files or systems (see LICENSE)."
	dialog.ShowInformation("Development release", msg, u.win)
	if u.svc.Cfg != nil {
		u.svc.Cfg.PreReleaseWarnedFor = version.String()
		_ = u.svc.Cfg.Save()
	}
}

// Stop tears down view subscriptions and quits the event loop. Idempotent - reachable from
// both the ctx-cancel goroutine and the tray Quit, and running closers twice double-closed a
// view's ticker channel ("close of closed channel" panic on shutdown).
func (u *UI) Stop() {
	u.stopOnce.Do(func() {
		for _, c := range u.closers {
			c()
		}
		u.closers = nil
		fyne.Do(u.app.Quit) // Stop is reachable from a non-UI goroutine (ctx-cancel); Quit must run on the UI thread
	})
}

// Notify sends a native desktop notification (gated on config).
func (u *UI) Notify(title, body string) {
	if u.svc.Cfg != nil && !u.svc.Cfg.Features.Notifications.Enabled {
		return
	}
	u.app.SendNotification(&fyne.Notification{Title: title, Content: body})
}

func (u *UI) installTray() {
	desk, ok := u.app.(desktop.App)
	if !ok {
		u.svc.Log.Warn("app", "no system tray on this platform", nil)
		return
	}
	show := fyne.NewMenuItem("Open rave-mate", func() {
		u.win.Show()
		u.win.RequestFocus()
	})
	quit := fyne.NewMenuItem("Quit", func() { u.Stop() })
	menu := fyne.NewMenu("rave-mate", show, fyne.NewMenuItemSeparator(), quit)
	desk.SetSystemTrayMenu(menu)
	desk.SetSystemTrayIcon(u.icon)
}

// Show surfaces the window (used by the tray + the single-instance "SHOW" ping). Safe
// to call from any goroutine.
func (u *UI) Show() {
	fyne.Do(func() {
		u.win.Show()
		u.win.RequestFocus()
	})
}

// SelectTab switches to the named tab (case-insensitive) from any goroutine. Returns
// whether it matched + the list of available tab names. Used by the CLI control plane to
// drive the UI (e.g. for reproducing a view's behaviour).
func (u *UI) SelectTab(name string) (bool, []string) {
	names := make([]string, 0, len(u.tabs.Items))
	var target *container.TabItem
	for _, it := range u.tabs.Items {
		names = append(names, it.Text)
		if strings.EqualFold(it.Text, name) {
			target = it
		}
	}
	if target == nil {
		return false, names
	}
	fyne.Do(func() { u.tabs.Select(target) })
	return true, names
}

// getToken returns the current access token from the auth manager (empty if signed out).
func (u *UI) getToken() string {
	if u.svc.Auth == nil {
		return ""
	}
	return u.svc.Auth.Token()
}

// muted is a helper to render dim secondary text.
func mutedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapWord // wrap long descriptions instead of forcing the card/window wider
	return l
}

// formGrid lays out alternating label/field pairs in a 2-column form: the label column is
// sized to the widest label so every field starts at the same x (aligned grouped inputs).
// Pass pairs as label, field, label, field, …
func formGrid(pairs ...fyne.CanvasObject) *fyne.Container {
	return container.New(layout.NewFormLayout(), pairs...)
}

// fieldLabel is a plain right-hand-aligned label for a form field's key column.
func fieldLabel(text string) *widget.Label { return widget.NewLabel(text) }

// mutedInline is a low-importance label that does NOT wrap - for inline field labels placed at
// the left/right of a Border or in an HBox, where wrapping mutedLabel would collapse to one
// char (vertical/stacked text). Use mutedLabel for full-width descriptions instead.
func mutedInline(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapOff
	return l
}
