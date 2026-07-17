// Package webui is the Go-driven HTML/CSS renderer for rave-mate - an experimental replacement for
// the Fyne UI (config: features.ui.renderer="webview"). Go renders every view with the rave.page
// design system, loads it into a native WebView2/WebKit window, and drives the DOM directly through
// the webview binding (patch fragments like JS would). The only JS is a tiny transport+introspection
// runtime (shell.go). Coexists with Fyne behind the flag until parity; the Gio player is untouched.
package webui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysnotify"
	"rave.page/mate/internal/tray"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/updater"
	"rave.page/mate/internal/version"
)

// UI is the webview frontend. It satisfies the same seam app.go uses for the Fyne *ui.UI.
type UI struct {
	svc     ui.Services
	log     *logbus.Bus
	shell   shell
	started time.Time

	mu        sync.Mutex
	active    string
	pinnedTab string // non-"" locks setTab to this tab (headless remote-library sessions)

	modalMu  sync.Mutex // guards the single modal slot's session identity (see modalTok)
	modalTk  modalTok   // the session on screen; the zero token = no modal
	modalSeq uint64     // monotonic session-id source: only ever incremented, never reset or reused

	pickMu   sync.Mutex         // guards the in-flight picked-path requests below (pick_actions.go)
	pickSeq  uint64             // monotonic request-id source
	pickReqs map[uint64]pickReq // request id → the session + target a returning dialog must apply under

	// Pending account-bridge enrolment, shown ONCE while the user scans it. Held in memory
	// only, cleared the moment a code confirms it. NEVER logged, never persisted here.
	bridgeURI    string
	bridgeSecret string
	bridgeCode   string // the code being typed back; kept in Go so a re-render can't drop it
	stop         chan struct{}
	closed       bool
	trayStop     func() // system-tray teardown (webview renderer only); nil off Windows / before ready
	libWatchStop func() // library.trackchanged unsubscribe (library_libwatch.go); nil before start

	updMgr *updater.Manager // self-update state machine (5-min poll; nil until onReady; disabled on dev builds)

	rui *ruiHub // remote Library sessions over the peer link (host + mirror mux); nil without peers

	probes     settingsProbes   // cached fs/PATH probes (mediatools + vrdll) - kept off the render goroutine
	gfProbe    gridfixProbe     // beatgrid-engine env probe (spawns Python; own long TTL) + fine-tune checkpoints
	midiProbe  midiCtlProbe     // MIDI-tab driver ioctl + winmm port enum - kept off render goroutine + tick
	restarts   settingRestarts  // debounced auto module restarts on settings change (settings_apply.go)
	capFS      capExistCache    // publish-tab capture on-disk existence (mpEnsureSet; off-thread stat sweep)
	spoutProbe spoutProbeCache  // Overlays Spout-DLL presence probe (os.Stat) - off the render goroutine
	ovlStyle   ovlStyleCache    // overlay-style.json fader flag (mtime-watched) - off the render goroutine
	wuCache    worldsUnityCache // Worlds-tab Unity project inspects (fs stat sweep) - off the render goroutine

	gf      gfState      // beatgrid-fixer cockpit run state (library_gridfix.go)
	tf      tfState      // tag-fixer scan/apply state (library_tagfix.go)
	re      reencSt      // batch re-encode modal state (library_reencode.go)
	ae      aeSt         // automation create/edit form state (automations_editor.go)
	as      asSt         // automation schedule form state (automations_schedules.go)
	ar      arSt         // automation run-now modal state (automations_runnow.go)
	gfTrain gfTrainState // model fine-tuning state (settings_gridfix_model.go)

	ceMu    sync.Mutex // guards ceState/ceStore/cePref lazy-init (library_cueedit.go)
	ceState *ceSt
	ceStore *cuepattern.Store
	cePref  *cePrefsSt // cue-editor mode + per-software defaults (cueprefs.go)

	rceMu   sync.Mutex         // guards rcePull/rcePre (remote-track fetch, library_remotecue.go)
	rcePull context.CancelFunc // active peer-file pull (nil = none); rce-cancel invokes it

	rcePre context.CancelFunc // active next-set-track prefetch (nil = none); nav/exit cancels

	mpLoadMu     sync.Mutex                    // guards mpLoadCancel (player_actions.go analyses)
	mpLoadCancel map[string]context.CancelFunc // host -> cancel for the in-flight analysis kick

	twMu         sync.Mutex
	twitchRows   []string             // rolling twitch chat/alert feed (cap 250)
	libSection   string               // Library active sub-section: "browse" | "collection"
	libDir       string               // Library browse cwd
	midiTrace    uint32               // ravemidi wire-trace viewer: port id (0 = closed)
	fbtMu        sync.Mutex           // guards the LED-feedback test state (midictl_fbtest.go)
	fbtBusy      bool                 // one in-flight test (re-click guard)
	fbtRes       map[uint32]fbtResult // reserved-port id → last outcome line
	libSearchDeb *time.Timer          // pending debounced library/collection search re-render (guarded by mu)
	remoteTarget string               // Library/Automations control target: "" = this computer, else a peer nodeID

	logMu         sync.Mutex // guards the logs-tab filter state below
	logBus        string     // active bus: "app"|"midi"|"traktor"|"session" ("" = app)
	logLevel      string     // "all"|"info"|"warn"|"error" ("" = all)
	logSource     string     // distinct e.Source filter; "" = all sources
	logSearch     string     // free-text filter over msg/source/fields
	logAutoscroll bool       // tail-follow toggle (default on)

	fragMu   sync.Mutex                       // guards frags + tickPend
	frags    map[string]string                // last HTML pushed per fragment id - ticks skip unchanged fragments
	tickPend map[*strings.Builder][]evalEntry // per-batch (id,patch) pairs tickPatch records for flushTick

	evalMu   sync.Mutex     // guards the eval queue below
	evalQ    []evalEntry    // pending page evals, insertion-ordered; keyed entries update in place
	evalIdx  map[string]int // coalescing key -> absolute seq (index = seq-evalBase)
	evalBase int            // seq of evalQ[0] (advances on overflow drop)
	evalKick chan struct{}  // cap-1 flusher wakeup

	setMu       sync.Mutex      // guards the Settings-tab view state below
	setSec      string          // active settings sub-tab (section id); "" = first
	setQuery    string          // live settings-search text
	setDebounce *time.Timer     // pending search re-render
	setVisible  map[string]bool // card ids currently in the DOM (status tick patches only these)
	setSearch   bool            // content pane is showing search results

	nav navHist // browser-style back/forward stack (mouse X1/X2 + Alt+←/→)
}

// New builds the webview UI over the shared Services (identical struct the Fyne UI consumes). The
// window is not created until Run (it must own a locked OS thread).
func New(svc ui.Services) *UI {
	u := &UI{svc: svc, log: svc.Log, active: "live", started: time.Now(), stop: make(chan struct{}),
		logBus: "app", logLevel: "all", logAutoscroll: true, evalKick: make(chan struct{}, 1)}
	if svc.Cfg != nil {
		webviewAllowGPU = svc.Cfg.Features.UI.AllowWebviewGPU()
	}
	if sh, ok := newShell("rave-mate", 1280, 820, u.onAction, u.onReady); ok {
		u.shell = sh
		go u.evalFlusher()
		u.registerUIBinds() // MIDI-mapped desktop-UI actions (primary window only)
	}
	u.ruiInit() // remote Library sessions over the peer link (no-op without peers)
	return u
}

// Available reports whether the webview host is compiled in (cgo Windows). app.go falls back to
// Fyne if not.
func Available() bool { return shellAvailable }

// ProbeWebview reports whether the OS webview runtime (WebView2) is actually usable right now -
// a runtime check on top of the compile-time Available(). False on a machine lacking the WebView2
// runtime, so the seam picks Fyne instead of a blank window. No-op (false) on non-webview builds.
func ProbeWebview() bool { return probeWebview() }

// onWindowHidden fires from the window subclass when close-to-tray hides the window (Windows).
// Set before the window exists (Run) - read on the UI thread only.
var onWindowHidden func()

// Run creates the window and blocks on its message loop until close (mirrors ui.UI.Run).
func (u *UI) Run(startHidden bool) {
	if u.shell == nil {
		if u.log != nil {
			u.log.Error("webui", "no webview host (nocgo build) - cannot render", nil)
		}
		return
	}
	onWindowHidden = func() {
		if u.log != nil {
			u.log.Info("webui", "window hidden to tray", nil)
		}
	}
	u.shell.run(u.shellHTML(), startHidden)
}

// onReady fires once the window + bindings exist; start the live pusher + event feeds + tray.
func (u *UI) onReady() {
	go u.livePush()
	u.initUpdater()
	u.subscribeTwitch()
	u.libWatchStart()
	u.startTray()
}

// showUpdateSettings raises the window on Settings→System (Updates card) and runs a check into
// it. Shared by the tray "Check for updates" menu item and the update-available balloon click.
func (u *UI) showUpdateSettings() {
	u.Show()
	u.setMu.Lock()
	u.setSec, u.setQuery = "system", ""
	u.setMu.Unlock()
	u.setTab("settings")
	u.updateCheck()
}

// startTray installs the native system-tray icon + menu (Fyne owns its own tray on the Fyne
// renderer; this covers the webview renderer). No-op off Windows. Stop() removes the icon.
func (u *UI) startTray() {
	stop, err := tray.Start(tray.Options{
		Tooltip:        i18n.T("tray.tooltip", i18n.A{"version": version.String()}),
		OpenLabel:      i18n.T("tray.open"),
		CheckLabel:     i18n.T("tray.checkUpdates"),
		QuitLabel:      i18n.T("tray.quit"),
		OnShow:         func() { u.Show() },
		OnCheckUpdates: func() { u.showUpdateSettings() },
		OnQuit:         func() { u.Stop() },
		UpdateLabel:    u.trayUpdateLabel, // state-dependent item; "" (up to date) = omitted
		OnUpdate:       u.trayUpdateAction,
	})
	if err != nil {
		if u.log != nil {
			u.log.Warn("webui", "system tray unavailable", map[string]any{"err": err.Error()})
		}
		return
	}
	u.mu.Lock()
	if u.closed { // raced a Stop() before the tray came up - tear it straight back down
		u.mu.Unlock()
		stop()
		return
	}
	u.trayStop = stop
	u.mu.Unlock()
}

// Stop tears the window down (idempotent).
func (u *UI) Stop() {
	u.mirrorShutdown() // best-effort close of a live remote-library session
	u.rceCancelPull()  // abort an in-flight remote-track pull
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	close(u.stop)
	trayStop, libWatchStop := u.trayStop, u.libWatchStop
	u.trayStop, u.libWatchStop = nil, nil
	u.mu.Unlock()
	if trayStop != nil {
		trayStop()
	}
	if libWatchStop != nil {
		libWatchStop()
	}
	if u.shell != nil {
		u.shell.terminate()
	}
}

func (u *UI) Show() {
	if u.shell != nil {
		u.shell.show()
	}
}

// Notify surfaces a message as an in-page toast + a log line (no Fyne dependency).
func (u *UI) Notify(title, body string) {
	if u.log != nil {
		u.log.Info("notify", title, map[string]any{"body": body})
	}
	msg := title
	if body != "" {
		msg = title + ": " + body
	}
	u.toast(msg)
	// Also fire a native OS notification off-thread (visible when the window is hidden/in tray).
	go func() { _ = sysnotify.Send(title, body) }()
}

// RefreshRecordings re-renders the Publish tab if it is showing (recorder list changed). Async so
// the caller (AutoReconciler goroutine) never renders inline; bursts coalesce in the eval queue.
func (u *UI) RefreshRecordings() {
	go func() {
		if u.activeTab() == "publish" {
			u.patchMain()
		}
	}()
}

// ── action dispatch (page → Go) ──

// onAction decodes one page act. Unexported actMsg fields (tok) are not decodable, so nothing the
// page says can forge a modal session - see onActMsg.
func (u *UI) onAction(payload string) {
	var m actMsg
	if json.Unmarshal([]byte(payload), &m) != nil {
		return
	}
	u.onActMsg(m)
}

// onActMsg routes one act on the act worker. pickApply re-enters here with the same message plus
// the modal session a returning native dialog's path must be applied under (pick_actions.go).
func (u *UI) onActMsg(m actMsg) {
	if u.log != nil { // every incoming act at debug - the first thing to check when a control is dead
		u.log.Debug("webui", "act", map[string]any{"act": m.Act, "val": m.Val, "form": len(m.Form) > 0})
	}
	if u.virtual() && virtualDenied(m.Act) { // headless: never open desktop surfaces on the host
		u.toast(i18n.T("library.mirror.noDesktop"))
		return
	}
	switch {
	case m.Act == "tab":
		u.setTab(m.Val)
	case m.Act == "nav-back":
		u.navBack()
	case m.Act == "nav-fwd":
		u.navFwd()
	case m.Act == "logs-clear":
		u.eval("var lv=document.getElementById('log-view');if(lv)lv.innerHTML='';")
	case m.Act == "auth-login":
		u.authLogin()
	case m.Act == "auth-logout":
		if u.svc.Auth != nil {
			u.svc.Auth.SignOut()
		}
		u.toast("Signed out")
		u.patchMain()
	case m.Act == "open-url":
		if m.Val != "" {
			_ = openURL(m.Val)
		}
	case m.Act == "copy":
		u.eval("navigator.clipboard&&navigator.clipboard.writeText(" + jsQuote(m.Val) + ")")
		u.toast("Copied to clipboard")
	case strings.HasPrefix(m.Act, "toggle:"):
		u.applyToggle(strings.TrimPrefix(m.Act, "toggle:"), m.Val == "true")
	case strings.HasPrefix(m.Act, "set:"):
		u.applySet(strings.TrimPrefix(m.Act, "set:"), m.Val)
	case strings.HasPrefix(m.Act, "ag-launch:"):
		u.launchAppGroup(strings.TrimPrefix(m.Act, "ag-launch:"))
	case strings.HasPrefix(m.Act, "auto-toggle:"):
		u.autoToggle(strings.TrimPrefix(m.Act, "auto-toggle:"), m.Val == "true")
	case strings.HasPrefix(m.Act, "auto-del:"):
		u.autoDelete(strings.TrimPrefix(m.Act, "auto-del:"))
	case strings.HasPrefix(m.Act, "peer-connect:"):
		u.peerConnect(strings.TrimPrefix(m.Act, "peer-connect:"))
	case strings.HasPrefix(m.Act, "peer-forget:"):
		u.peerForget(strings.TrimPrefix(m.Act, "peer-forget:"))
	case strings.HasPrefix(m.Act, "peer-sas:"):
		u.peerSAS(strings.TrimPrefix(m.Act, "peer-sas:"), m.Val == "1")
	case strings.HasPrefix(m.Act, "media-recv:"):
		u.mediaReceive(strings.TrimPrefix(m.Act, "media-recv:"))
	case strings.HasPrefix(m.Act, "media-stop:"):
		u.mediaStop(strings.TrimPrefix(m.Act, "media-stop:"))
	case strings.HasPrefix(m.Act, "xfer-accept:"):
		u.xferAccept(strings.TrimPrefix(m.Act, "xfer-accept:"), m.Val == "1")
	case strings.HasPrefix(m.Act, "xfer-cancel:"):
		u.xferCancel(strings.TrimPrefix(m.Act, "xfer-cancel:"))
	case m.Act == "rec-finish":
		u.recFinish()
	case m.Act == "vrc-status":
		u.vrcStatus(m.Form)
	case m.Act == "vrc-bio":
		u.vrcBio(m.Form)
	case strings.HasPrefix(m.Act, "ws-pub-list:"):
		u.wsPublish("list", strings.TrimPrefix(m.Act, "ws-pub-list:"))
	case m.Act == "ws-pub-posters":
		u.wsPublish("posters", "")
	case m.Act == "ws-pub-events":
		u.wsPublish("events", "")
	case m.Act == "ws-pub-nowplaying":
		u.wsPublish("nowplaying", "")
	case m.Act == "twitch-send":
		u.twitchSend(m.Form)
	case strings.HasPrefix(m.Act, "lib-section:"):
		u.libSetSection(strings.TrimPrefix(m.Act, "lib-section:"))
	case strings.HasPrefix(m.Act, "lib-nav:"):
		u.libNav(strings.TrimPrefix(m.Act, "lib-nav:"))
	case m.Act == "stream-pause":
		u.streamPause(m.Val == "true")
	case m.Act == "arec-toggle":
		u.arecToggle()
	case m.Act == "tc-start":
		u.tcStart()
	case m.Act == "tc-stop":
		u.tcStop()
	case strings.HasPrefix(m.Act, "obs-stream:"):
		u.obsCmd(strings.TrimPrefix(m.Act, "obs-stream:"), "stream")
	case strings.HasPrefix(m.Act, "obs-record:"):
		u.obsCmd(strings.TrimPrefix(m.Act, "obs-record:"), "record")
	default:
		if u.dispatch(m) {
			return
		}
		if u.log != nil {
			u.log.Info("webui", "action", map[string]any{"act": m.Act, "val": m.Val})
		}
		u.toast("Not wired yet: " + m.Act)
	}
}

func (u *UI) activeTab() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.active == "" {
		return "live"
	}
	return u.active
}

// setTab switches the active tab and patches the main + nav fragments (Go-driven DOM update).
// A pinned (headless) UI ignores switches away from its pinned tab - the mirror surface has no
// nav, but nav-back/fwd or stray acts must never walk the session off the Library tab.
func (u *UI) setTab(id string) {
	u.mu.Lock()
	pinned := u.pinnedTab
	u.mu.Unlock()
	if pinned != "" && id != pinned {
		return
	}
	if id != u.activeTab() {
		u.navRecord() // record the pre-switch view for mouse-back
	}
	u.mu.Lock()
	u.active = id
	u.mu.Unlock()
	u.patchMain()
	u.eval("window.__patch('nav-list'," + jsQuote(u.navListHTML()) + ")")
}

func (u *UI) patchMain() {
	u.fragMu.Lock()
	u.frags = nil // DOM replaced - drop the tick dedup cache
	u.fragMu.Unlock()
	u.eval("window.__patch('main'," + jsQuote(u.mainHTML()) + ");document.body.setAttribute('data-keyscope'," + jsQuote(u.keyScope()) + ")")
	u.mpResync() // heal any player patch the build raced (state changed mid-build)
}

// keyScope names the active editing-key surface ("" = none; see shell.go keydown).
func (u *UI) keyScope() string {
	if u.activeTab() != "library" {
		return ""
	}
	if u.ceActiveFor("library") {
		return "cueedit"
	}
	if u.libSectionOr() == "collection" {
		return "library"
	}
	return ""
}

// tickPatch records a __patch call for id unless html matches the last push (dedup). Pairs are
// keyed per batch builder and enqueued by flushTick; the eval flusher batches everything queued
// into ONE Eval (each Eval is a cross-process ExecuteScript on the UI thread - per-fragment evals
// made the window stutter). js still receives the call for len()>0 checks + tests.
func (u *UI) tickPatch(js *strings.Builder, id, html string) {
	u.fragMu.Lock()
	if prev, ok := u.frags[id]; ok && prev == html {
		u.fragMu.Unlock()
		return
	}
	if u.frags == nil {
		u.frags = map[string]string{}
	}
	u.frags[id] = html
	call := "window.__patch('" + id + "'," + jsQuote(html) + ");"
	if u.tickPend == nil {
		u.tickPend = map[*strings.Builder][]evalEntry{}
	}
	u.tickPend[js] = append(u.tickPend[js], evalEntry{key: id, js: call})
	u.fragMu.Unlock()
	js.WriteString(call)
}

func (u *UI) flushTick(js *strings.Builder) {
	u.fragMu.Lock()
	pend := u.tickPend[js]
	delete(u.tickPend, js)
	u.fragMu.Unlock()
	for _, e := range pend {
		u.enqueueEval(e.key, e.js)
	}
}

// SelectTab (ctl) selects a tab by id or label (case-insensitive), returns ok + available labels.
func (u *UI) SelectTab(name string) (bool, []string) {
	labels := u.tabLabels()
	if u.shell == nil {
		return false, labels
	}
	want := trimLower(name)
	for _, t := range u.tabs() {
		if !t.enabled {
			continue
		}
		if trimLower(t.id) == want || trimLower(t.label) == want {
			u.setTab(t.id)
			return true, labels
		}
	}
	return false, labels
}

// livePush patches the Live cockpit's live fragments ~1 Hz while it is showing (Go pushes DOM
// updates, exactly as JS would). Cheap no-op on other tabs.
func (u *UI) livePush() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-t.C:
			// Skip the ~1 Hz DOM refresh whenever the window isn't being looked at (dragging,
			// unfocused, minimized) or a stream is live - repainting rave-mate's own graphs then
			// only competes with the encoder for CPU/GPU. governor.UIAnimAllowed covers all four.
			if u.shell == nil || inSizeMove() {
				continue
			}
			if !governor.UIAnimAllowed() {
				// cue-edit exemption: the audition playhead must keep moving while
				// streaming/unfocused (mpTick no-ops unless actually playing)
				if u.activeTab() == "library" && u.ceActiveFor("library") {
					mpTick(u, "library")
				}
				continue
			}
			if fn := liveTicks[u.activeTab()]; fn != nil {
				fn(u)
			}
		}
	}
}

// ── modal slot ──
//
// One modal on screen at a time, so any patch of it is a patch of whatever the user is looking
// at NOW. Work that finishes off the actWorker must therefore PROVE the modal is still its own
// before touching it: a run outlives its modal by design (arRunCtx carries no deadline), so by
// the time it completes the user may have cancelled and opened an unrelated form - an unguarded
// openModal/closeModal there destroys it, unsaved, with no recovery path. modalTok is the proof,
// and it lives here so every bg-completing modal gets it rather than one call site special-casing.
//
// Identity is the session id (gen), not the owner: a form re-renders its own modal on every field
// change, so its token must survive that, while two SESSIONS of the same owner (run A cancelled,
// run-now re-opened for automation B) must be told apart. owner rides along only for
// openModalIfOwner. Ids come from one monotonic counter that is never reset and never reused, so
// no two sessions can compare equal - not an empty slot against an open one, not two anonymous
// dialogs against each other - and nothing outside this file can mint or re-derive one.

// modalTok identifies one modal session. The zero token owns nothing and matches no live session:
// ids start at 1, so an empty slot can never be mistaken for an occupied one.
type modalTok struct {
	owner string
	gen   uint64
}

// live reports tok names a session at all.
func (t modalTok) live() bool { return t.gen != 0 }

// modalCur snapshots the modal slot's current session. Capture it ON the actWorker (before u.bg)
// to pin what the user was looking at when they clicked.
func (u *UI) modalCur() modalTok {
	u.modalMu.Lock()
	defer u.modalMu.Unlock()
	return u.modalTk
}

// claimLocked takes the slot for owner. A NAMED owner re-rendering its own live modal keeps the
// session (one form, many field-change re-renders - its outstanding save token must survive them);
// EVERY other claim mints a fresh id: a different owner, a re-open after a close, and any anonymous
// open, which never re-renders itself and so must never share an identity with the slot it found.
// Caller holds u.modalMu.
func (u *UI) claimLocked(owner string) modalTok {
	if owner == "" || !u.modalTk.live() || u.modalTk.owner != owner {
		u.modalSeq++
		u.modalTk = modalTok{owner, u.modalSeq}
	}
	return u.modalTk
}

// openModalAs renders inner (use the modal() component) as owner's modal and returns the session
// token to hand to any bg work that will patch it later. owner = a per-feature constant.
func (u *UI) openModalAs(owner, inner string) modalTok {
	u.modalMu.Lock()
	tok := u.claimLocked(owner)
	u.modalMu.Unlock()
	u.evalModal(inner)
	return tok
}

// claimModalWith opens owner's modal only if the slot is still exactly as prev left it - the guard
// for an open that had to load off the actWorker first (a bbolt read). If anything else took the
// modal while we loaded, the user has moved on and this open would clobber it.
//
// build runs UNDER the slot lock, only once prev is confirmed, and returns the HTML to render.
// Everything the open mutates - the form's working copy - belongs inside it: a load that ran
// before the check would seed the form the user actually has on screen with this entity's data,
// and dropping the render afterwards does not undo that (the precedent: tfEditOpen seeded a new
// track with the old track's tags). Check first, write second, atomically.
func (u *UI) claimModalWith(prev modalTok, owner string, build func() string) (modalTok, bool) {
	u.modalMu.Lock()
	if u.modalTk != prev {
		u.modalMu.Unlock()
		return modalTok{}, false
	}
	inner := build()
	tok := u.claimLocked(owner)
	u.modalMu.Unlock()
	u.evalModal(inner)
	return tok, true
}

// updateModalIf applies a mutation to the modal's own state, but only while tok is still the
// session on screen. mutate runs under the slot lock, so the check and the write are one step -
// a reload that won the race would otherwise take the write into ANOTHER entity's form. It returns
// the HTML to re-render with, or "" to leave the DOM alone (a field that can't change the form's
// shape must not re-render under the user's cursor). false = the session is gone: the caller drops
// the write, never forces it.
func (u *UI) updateModalIf(tok modalTok, mutate func() string) bool {
	u.modalMu.Lock()
	if !tok.live() || u.modalTk != tok {
		u.modalMu.Unlock()
		return false
	}
	inner := mutate()
	u.modalMu.Unlock()
	if inner != "" {
		u.evalModal(inner)
	}
	return true
}

// openModalIf re-renders the modal only if tok still owns it; reports whether it did.
func (u *UI) openModalIf(tok modalTok, inner string) bool {
	return u.updateModalIf(tok, func() string { return inner })
}

// openModalIfOwner re-renders the modal only if owner holds the slot, whatever the session - for
// state a LATER session of the same form still renders (a finished run's "Running…" gate). Never
// matches the anonymous owner: an unguarded dialog has no successor session to refresh.
func (u *UI) openModalIfOwner(owner, inner string) bool {
	u.modalMu.Lock()
	if owner == "" || !u.modalTk.live() || u.modalTk.owner != owner {
		u.modalMu.Unlock()
		return false
	}
	u.modalMu.Unlock()
	u.evalModal(inner)
	return true
}

// closeModalIf clears the modal only if tok still owns it; reports whether it did. false is not an
// error - it means the user closed it or moved to another modal, and both must be left alone.
func (u *UI) closeModalIf(tok modalTok) bool {
	u.modalMu.Lock()
	if !tok.live() || u.modalTk != tok {
		u.modalMu.Unlock()
		return false
	}
	u.releaseLocked()
	u.modalMu.Unlock()
	u.evalModal("")
	return true
}

// releaseLocked empties the slot and invalidates every outstanding token. Caller holds u.modalMu.
func (u *UI) releaseLocked() { u.modalTk = modalTok{} }

// openModal renders inner into the modal root anonymously - for modals with no bg completion to
// guard. It still takes the slot (with a fresh id, so two anonymous dialogs are distinct), and an
// owned modal's outstanding tokens go stale, which is correct: that owner's modal is no longer the
// thing on screen.
func (u *UI) openModal(inner string) { u.openModalAs("", inner) }

// closeModal clears the modal unconditionally - a user close (modal-close, scrim, ✕), which is
// always authoritative - and invalidates every outstanding token.
func (u *UI) closeModal() {
	u.modalMu.Lock()
	u.releaseLocked()
	u.modalMu.Unlock()
	u.evalModal("")
}

func (u *UI) evalModal(inner string) { u.eval("window.__patch('__modal'," + jsQuote(inner) + ")") }

func (u *UI) toast(msg string) {
	if u.shell != nil {
		u.eval("window.__toast(" + jsQuote(msg) + ")")
	}
}

// ── eval queue ──
// "eval" = webview ExecuteScript on our own page; every script is Go-generated (or local-operator
// ctl, loopback-only) - never remote/untrusted input (see control.go).
// All Go-driven page evals funnel through one bounded, coalescing queue drained by evalFlusher.
// Direct shell.eval from every producer piled Dispatch closures on the WebView2 UI thread: evals
// processed inside the Windows size-move modal loop made the window trail the cursor, and a hung
// UI thread grew daemon RSS without bound. ctl round-trips (control.go evalValue) stay direct.

// evalEntry is one queued page eval; key!="" coalesces newest-wins per fragment id.
type evalEntry struct{ key, js string }

const (
	maxEvalQueue   = 512                   // queue cap; overflow drops oldest + wipes tick dedup so nothing sticks stale
	evalGatePoll   = 50 * time.Millisecond // exit-size-move detection latency while gated
	evalAckTimeout = 3 * time.Second       // hung-UI-thread guard: ≤1 un-acked batch per this window
)

// eval enqueues js for the page. Fragment patches (leading window.__patch('id'…) coalesce
// newest-wins per id; everything else is FIFO.
func (u *UI) eval(js string) { u.enqueueEval(evalKey(js), js) }

// evalKey extracts the fragment id from a leading window.__patch('id'…/("id"… call - the
// coalescing key. "" (no coalescing) for any other JS.
func evalKey(js string) string {
	const p = "window.__patch("
	if !strings.HasPrefix(js, p) || len(js) < len(p)+2 {
		return ""
	}
	rest := js[len(p):]
	q := rest[0]
	if q != '\'' && q != '"' {
		return ""
	}
	if i := strings.IndexByte(rest[1:], q); i >= 0 {
		return rest[1 : 1+i]
	}
	return ""
}

// enqueueEval queues js for the flusher. A keyed entry replaces (newest-wins, position kept) any
// queued entry with the same key; cap policy = drop-oldest + frags wipe (a dropped patch re-emits
// on the next tick instead of sticking stale).
func (u *UI) enqueueEval(key, js string) {
	if u.shell == nil {
		return
	}
	wipe := false
	u.evalMu.Lock()
	if key != "" {
		if seq, ok := u.evalIdx[key]; ok {
			u.evalQ[seq-u.evalBase].js = js
			u.evalMu.Unlock()
			u.kickEval()
			return
		}
	}
	if len(u.evalQ) >= maxEvalQueue {
		old := u.evalQ[0]
		u.evalQ = u.evalQ[1:]
		u.evalBase++
		if old.key != "" {
			delete(u.evalIdx, old.key)
		}
		wipe = true
	}
	u.evalQ = append(u.evalQ, evalEntry{key: key, js: js})
	if key != "" {
		if u.evalIdx == nil {
			u.evalIdx = map[string]int{}
		}
		u.evalIdx[key] = u.evalBase + len(u.evalQ) - 1
	}
	u.evalMu.Unlock()
	if wipe {
		u.fragMu.Lock()
		u.frags = nil
		u.fragMu.Unlock()
	}
	u.kickEval()
}

func (u *UI) kickEval() {
	select {
	case u.evalKick <- struct{}{}:
	default:
	}
}

// drainEvals empties the queue into one script; each entry is isolated in an IIFE+try so one
// failing eval can't kill the rest (matches the old one-Eval-per-call isolation).
func (u *UI) drainEvals() string {
	u.evalMu.Lock()
	q := u.evalQ
	u.evalQ, u.evalIdx, u.evalBase = nil, nil, 0
	u.evalMu.Unlock()
	if len(q) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range q {
		b.WriteString(";(function(){try{")
		b.WriteString(e.js)
		b.WriteString("}catch(e){}})();")
	}
	return b.String()
}

// evalFlusher is the single dispatcher of page evals: coalesces the queue into one batched Eval,
// holds everything while the user drags/resizes (evals processed inside the size-move modal loop
// make the window trail the cursor) and flushes right after WM_EXITSIZEMOVE. NOT gated on
// governor.UIAnimAllowed: that predicate includes focused/!streaming, which must never stall
// interactive renders (tab clicks while streaming, ctl verification against an unfocused window) -
// livePush gates its tick producers on it at the source instead.
func (u *UI) evalFlusher() {
	for {
		select {
		case <-u.stop:
			return
		case <-u.evalKick:
		}
		for {
			// The size-move gate protects the REAL window's UI thread; a virtual shell has no
			// window and must keep streaming while the local user drags theirs.
			for !u.virtual() && inSizeMove() {
				select {
				case <-u.stop:
					return
				case <-time.After(evalGatePoll):
				}
			}
			js := u.drainEvals()
			if js == "" {
				break
			}
			u.dispatchEvals(js)
		}
	}
}

// dispatchEvals sends one batch to the page and waits for its ack (bounded): ≤1 un-acked Dispatch
// per evalAckTimeout, so a wedged UI thread accumulates coalesced fragments here - never closures.
func (u *UI) dispatchEvals(js string) {
	if vs, ok := u.shell.(*virtualShell); ok {
		vs.eval(js) // synchronous sink - no UI thread, no ack round-trip needed
		return
	}
	id := nextEvalID()
	ch := make(chan string, 1)
	evalWaiters.Store(id, ch)
	defer evalWaiters.Delete(id)
	u.shell.eval(js + "window.__rave_evalResult(" + jsQuote(id) + ",'1');")
	select {
	case <-ch:
	case <-time.After(evalAckTimeout):
	case <-u.stop:
	}
}

// jsQuote returns a JS/JSON string literal for s (safe to splice into eval'd code).
func jsQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
