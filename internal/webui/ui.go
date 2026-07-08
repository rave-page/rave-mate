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

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/sysnotify"
	"rave.page/mate/internal/tray"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/version"
)

// UI is the webview frontend. It satisfies the same seam app.go uses for the Fyne *ui.UI.
type UI struct {
	svc     ui.Services
	log     *logbus.Bus
	shell   shell
	started time.Time

	mu       sync.Mutex
	active   string
	stop     chan struct{}
	closed   bool
	trayStop func() // system-tray teardown (webview renderer only); nil off Windows / before ready

	updMu   sync.Mutex          // guards updater + updRel
	updater *selfupdate.Updater // self-update poller (disabled on a dev build); lazily built
	updRel  *selfupdate.Release // last "available" release, staged for apply

	probes settingsProbes // cached fs/PATH probes (mediatools + vrdll) - kept off the render goroutine

	twMu         sync.Mutex
	twitchRows   []string // rolling twitch chat/alert feed (cap 250)
	libSection   string   // Library active sub-section: "browse" | "collection"
	libDir       string   // Library browse cwd
	remoteTarget string   // Library/Automations control target: "" = this computer, else a peer nodeID

	logMu         sync.Mutex // guards the logs-tab filter state below
	logBus        string     // active bus: "app"|"midi"|"traktor"|"session" ("" = app)
	logLevel      string     // "all"|"info"|"warn"|"error" ("" = all)
	logSource     string     // distinct e.Source filter; "" = all sources
	logSearch     string     // free-text filter over msg/source/fields
	logAutoscroll bool       // tail-follow toggle (default on)

	fragMu sync.Mutex        // guards frags
	frags  map[string]string // last HTML pushed per fragment id - ticks skip unchanged fragments

	setMu       sync.Mutex      // guards the Settings-tab view state below
	setSec      string          // active settings sub-tab (section id); "" = first
	setQuery    string          // live settings-search text
	setDebounce *time.Timer     // pending search re-render
	setVisible  map[string]bool // card ids currently in the DOM (status tick patches only these)
	setSearch   bool            // content pane is showing search results
}

// New builds the webview UI over the shared Services (identical struct the Fyne UI consumes). The
// window is not created until Run (it must own a locked OS thread).
func New(svc ui.Services) *UI {
	u := &UI{svc: svc, log: svc.Log, active: "live", started: time.Now(), stop: make(chan struct{}),
		logBus: "app", logLevel: "all", logAutoscroll: true}
	if svc.Cfg != nil {
		webviewAllowGPU = svc.Cfg.Features.UI.AllowWebviewGPU()
	}
	if sh, ok := newShell("rave-mate", 1280, 820, u.onAction, u.onReady); ok {
		u.shell = sh
	}
	return u
}

// Available reports whether the webview host is compiled in (cgo Windows). app.go falls back to
// Fyne if not.
func Available() bool { return shellAvailable }

// ProbeWebview reports whether the OS webview runtime (WebView2) is actually usable right now -
// a runtime check on top of the compile-time Available(). False on a machine lacking the WebView2
// runtime, so the seam picks Fyne instead of a blank window. No-op (false) on non-webview builds.
func ProbeWebview() bool { return probeWebview() }

// Run creates the window and blocks on its message loop until close (mirrors ui.UI.Run).
func (u *UI) Run(startHidden bool) {
	if u.shell == nil {
		if u.log != nil {
			u.log.Error("webui", "no webview host (nocgo build) - cannot render", nil)
		}
		return
	}
	u.shell.run(u.shellHTML(), startHidden)
}

// onReady fires once the window + bindings exist; start the live pusher + event feeds + tray.
func (u *UI) onReady() {
	go u.livePush()
	u.subscribeTwitch()
	u.startTray()
}

// startTray installs the native system-tray icon + menu (Fyne owns its own tray on the Fyne
// renderer; this covers the webview renderer). No-op off Windows. Stop() removes the icon.
func (u *UI) startTray() {
	stop, err := tray.Start(tray.Options{
		Tooltip:    i18n.T("tray.tooltip", i18n.A{"version": version.String()}),
		OpenLabel:  i18n.T("tray.open"),
		CheckLabel: i18n.T("tray.checkUpdates"),
		QuitLabel:  i18n.T("tray.quit"),
		OnShow:     func() { u.Show() },
		OnCheckUpdates: func() { // surface the Settings→System sub-tab (Updates card), run the check into it
			u.Show()
			u.setMu.Lock()
			u.setSec, u.setQuery = "system", ""
			u.setMu.Unlock()
			u.setTab("settings")
			u.updateCheck()
		},
		OnQuit: func() { u.Stop() },
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
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	close(u.stop)
	trayStop := u.trayStop
	u.trayStop = nil
	u.mu.Unlock()
	if trayStop != nil {
		trayStop()
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

// RefreshRecordings re-renders the Publish tab if it is showing (recorder list changed).
func (u *UI) RefreshRecordings() {
	if u.activeTab() == "publish" {
		u.patchMain()
	}
}

// ── action dispatch (page → Go) ──

func (u *UI) onAction(payload string) {
	var m struct {
		Act, Val, Form, ID string
	}
	if json.Unmarshal([]byte(payload), &m) != nil {
		return
	}
	if u.log != nil { // every incoming act at debug - the first thing to check when a control is dead
		u.log.Debug("webui", "act", map[string]any{"act": m.Act, "val": m.Val, "form": len(m.Form) > 0})
	}
	switch {
	case m.Act == "tab":
		u.setTab(m.Val)
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
	case m.Act == "stream-golive":
		u.streamGoLive(m.Form)
	case m.Act == "stream-end":
		u.streamEnd()
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
		if u.dispatch(actMsg{Act: m.Act, Val: m.Val, Form: m.Form, ID: m.ID}) {
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
func (u *UI) setTab(id string) {
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
	u.eval("window.__patch('main'," + jsQuote(u.mainHTML()) + ")")
}

// tickPatch appends a __patch call to js unless html matches the last push for id. Ticks batch
// all fragments into ONE eval (each Eval is a cross-process ExecuteScript on the UI thread -
// per-fragment evals made the window stutter).
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
	u.fragMu.Unlock()
	js.WriteString("window.__patch('" + id + "'," + jsQuote(html) + ");")
}

func (u *UI) flushTick(js *strings.Builder) {
	if js.Len() > 0 {
		u.eval(js.String())
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
			if u.shell == nil || inSizeMove() || !governor.UIAnimAllowed() {
				continue
			}
			if fn := liveTicks[u.activeTab()]; fn != nil {
				fn(u)
			}
		}
	}
}

// openModal renders inner (use the modal() component) into the modal root; closeModal clears it.
func (u *UI) openModal(inner string) { u.eval("window.__patch('__modal'," + jsQuote(inner) + ")") }
func (u *UI) closeModal()            { u.eval("window.__patch('__modal','')") }

func (u *UI) toast(msg string) {
	if u.shell != nil {
		u.eval("window.__toast(" + jsQuote(msg) + ")")
	}
}

func (u *UI) eval(js string) {
	if u.shell != nil {
		u.shell.eval(js)
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

var _ = context.Background // reserved for picker ctx use
