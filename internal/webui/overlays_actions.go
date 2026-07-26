package webui

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/overlaystyle"
	"rave.page/mate/internal/spoutdll"
	"rave.page/mate/internal/videoshare"
)

// Overlays tab action handlers + live-status tick. All actions are namespaced ovl-* so they never
// collide with other tabs. Config setters persist via saveCfg (which reconciles the session so the
// overlay pipeline picks up changes live). Reused already-wired actions: open-url, copy,
// set:overlay-port - not re-registered here. Per-output enable switches = ovl-en-<kind>.

func init() {
	// Enable switches (ovl-en-<kind>): Fyne sessionToggle parity - set field, saveCfg
	// (persist + Session.Reconcile starts/stops the output live), then patch the card
	// status + summary strip immediately (don't wait for the ~1 Hz tick).
	for _, e := range []struct {
		kind  string
		field func(f *config.Features) *bool
	}{
		{"web", func(f *config.Features) *bool { return &f.OverlayWeb.Enabled }},
		{"wave", func(f *config.Features) *bool { return &f.OverlayWaveform.Enabled }},
		{"png", func(f *config.Features) *bool { return &f.OverlayPNG.Enabled }},
		{"obs", func(f *config.Features) *bool { return &f.OverlayOBS.Enabled }},
		{"vs", func(f *config.Features) *bool { return &f.VideoShare.Enabled }},
		{"np", func(f *config.Features) *bool { return &f.NowPlayingFile.Enabled }},
	} {
		onExact("ovl-en-"+e.kind, func(u *UI, m actMsg) {
			if u.svc.Cfg == nil {
				return
			}
			*e.field(&u.svc.Cfg.Features) = m.Val == "true"
			u.saveCfg()
			u.eval("window.__patch('ovl-st-" + e.kind + "'," + jsQuote(u.ovlStatusHTML(e.kind)) + ")")
			u.eval("window.__patch('ovl-strip'," + jsQuote(u.ovlStripHTML()) + ")")
		})
	}

	// Appearance: fade deck cards by fader - surgically written to overlay-style.json + pushed live.
	onExact("ovl-fader", func(u *UI, m actMsg) {
		path, _ := config.DataPath("overlay-style.json")
		on := m.Val == "true"
		if err := overlaystyle.SetBool(path, "cardFaderReact", on); err != nil {
			u.logErr("overlay fader", err)
			u.toast("Couldn't save: " + err.Error())
			return
		}
		u.invalidateOvlStyle() // file changed - next render/tick re-reads the flag off-thread
		if u.svc.Cfg != nil && u.svc.Cfg.Features.OverlayWeb.Enabled {
			port := u.svc.Cfg.Features.OverlayWeb.ResolvedPort()
			u.bg(func() { _ = overlaystyle.Push(port, path) })
		}
	})

	// Browser overlay: OBS auto-manage source. The OBS bridge reads these at (re)spawn, so
	// apply via a debounced module restart (busy-deferred while recording) - no manual off/on.
	onExact("ovl-obssrc", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.OverlayWeb.OBSSource.Enabled = m.Val == "true"
		u.saveCfg()
		u.scheduleModuleRestart("obs")
	})
	onExact("ovl-obsnest", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.OverlayWeb.OBSSource.NestInProgram = m.Val == "true"
		u.saveCfg()
		u.scheduleModuleRestart("obs")
	})
	onExact("ovl-obsscene", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.OverlayWeb.OBSSource.Scene = strings.TrimSpace(m.Val)
		u.saveCfg()
		u.scheduleModuleRestart("obs")
	})

	// Waveform panel.
	onExact("ovl-wf-zoom", func(u *UI, m actMsg) {
		if v, err := strconv.ParseFloat(m.Val, 64); err == nil && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.ZoomSeconds = v
			u.saveCfg()
		}
	})
	onExact("ovl-wf-playhead", func(u *UI, m actMsg) {
		if v, err := strconv.ParseFloat(m.Val, 64); err == nil && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.PlayheadPct = v
			u.saveCfg()
		}
	})
	onExact("ovl-wf-wavecolor", func(u *UI, m actMsg) {
		if s := strings.TrimSpace(m.Val); ovlValidHex(s) && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.WaveColor = s
			u.saveCfg()
		}
	})
	onExact("ovl-wf-bgcolor", func(u *UI, m actMsg) {
		if s := strings.TrimSpace(m.Val); ovlValidHex(s) && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.BgColor = s
			u.saveCfg()
		}
	})
	onExact("ovl-wf-waveopac", func(u *UI, m actMsg) {
		if v, err := strconv.ParseFloat(m.Val, 64); err == nil && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.WaveOpacity = v
			u.saveCfg()
		}
	})
	onExact("ovl-wf-bgopac", func(u *UI, m actMsg) {
		if v, err := strconv.ParseFloat(m.Val, 64); err == nil && u.svc.Cfg != nil {
			u.svc.Cfg.Features.OverlayWaveform.BgOpacity = v
			u.saveCfg()
		}
	})

	// Output dirs + open-folder.
	onExact("ovl-png-dir", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.OverlayPNG.Dir = strings.TrimSpace(m.Val)
		u.saveCfg()
	})
	onExact("ovl-png-open", func(u *UI, _ actMsg) {
		dir := ""
		if u.svc.Cfg != nil {
			dir = u.svc.Cfg.Features.OverlayPNG.Dir
		}
		if dir == "" {
			dir, _ = config.DataPath("overlay-png")
		}
		u.ovlOpenDir(dir)
	})
	onExact("ovl-np-dir", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.NowPlayingFile.Dir = strings.TrimSpace(m.Val)
		u.saveCfg()
	})
	onExact("ovl-np-open", func(u *UI, _ actMsg) {
		dir := ""
		if u.svc.Cfg != nil {
			dir = u.svc.Cfg.Features.NowPlayingFile.Dir
		}
		if dir == "" {
			dir, _ = config.DataPath("")
		}
		u.ovlOpenDir(dir)
	})

	// Video share. Render scale is read at sink start - debounced restart applies it live
	// (deferred while a deck is on-air so a live share isn't cut mid-set).
	onExact("ovl-vs-scale", func(u *UI, m actMsg) {
		if n, err := strconv.Atoi(m.Val); err == nil && u.svc.Cfg != nil {
			u.svc.Cfg.Features.VideoShare.RenderScale = n
			u.saveCfg()
			u.scheduleSinkRestart(videoshare.SinkID)
		}
	})
	onExact("ovl-spout-install", func(u *UI, _ actMsg) { u.spoutInstall() })

	// ~1 Hz status refresh (cheap: reads config + OBS proxy). Patches each card's status region +
	// the bottom summary strip; leaves the card bodies (user is editing them) untouched.
	onLiveTick("overlays", func(u *UI) {
		if u.svc.Cfg == nil {
			return
		}
		for _, s := range [][2]string{{"ovl-st-web", "web"}, {"ovl-st-wave", "wave"}, {"ovl-st-png", "png"},
			{"ovl-st-obs", "obs"}, {"ovl-st-vs", "vs"}, {"ovl-st-np", "np"}} {
			u.eval("window.__patch('" + s[0] + "'," + jsQuote(u.ovlStatusHTML(s[1])) + ")")
		}
		u.eval("window.__patch('ovl-strip'," + jsQuote(u.ovlStripHTML()) + ")")
		// keep the off-thread probe caches warm (read cache + kick a bg refresh when stale); their
		// refreshers self-patch ovl-appearance / ovl-spout on change (e.g. browser editor / DLL landed).
		u.ovlFaderCached()
		if videoshare.Backend() == "Spout" {
			u.spoutStatusCached()
		}
	})
}

// spoutInstall downloads + installs SpoutLibrary.dll off-thread, streaming progress into the
// #ovl-spout-prog region, then re-renders the whole Spout controls block.
func (u *UI) spoutInstall() {
	u.toast("Downloading Spout runtime…")
	u.eval("window.__patch('ovl-spout-prog'," + jsQuote(progressBar(0, "starting…")) + ")")
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		last := -1
		err := spoutdll.Install(ctx, func(done, total int64) {
			if total <= 0 {
				return
			}
			pct := int(float64(done) / float64(total) * 100)
			if pct == last {
				return
			}
			last = pct
			u.eval("window.__patch('ovl-spout-prog'," + jsQuote(progressBar(float64(pct)/100, "")) + ")")
		})
		if err != nil {
			u.logErr("spout install", err)
			u.toast("Spout runtime install failed: " + err.Error())
		} else {
			u.toast(i18n.T("overlays.spout.installedToast"))
			u.scheduleSinkRestart(videoshare.SinkID) // running share reloads the DLL; off = next start finds it
		}
		u.refreshSpoutProbe() // re-probe (DLL just landed) + re-patch the controls block (already off-thread)
	})
}

// ovlOpenDir opens dir in the OS file browser (best-effort; no native picker in the webview UI).
func (u *UI) ovlOpenDir(dir string) {
	if dir == "" {
		u.toast("No folder set yet")
		return
	}
	if err := openURL(dir); err != nil {
		u.logErr("open folder", err)
	}
}

// ── Spout DLL probe cache (keep spoutdll.Probe's os.Executable+os.Stat sweep off the render goroutine) ──

const spoutProbeTTL = 30 * time.Second

type spoutProbeCache struct {
	mu    sync.Mutex
	st    spoutdll.Status
	at    time.Time
	ready bool
	busy  bool
}

// spoutStatusCached returns the last SpoutLibrary.dll probe, kicking an off-thread refresh when
// stale. Never touches the filesystem - safe on the render goroutine.
func (u *UI) spoutStatusCached() spoutdll.Status {
	u.spoutProbe.mu.Lock()
	st := u.spoutProbe.st
	stale := !u.spoutProbe.ready || time.Since(u.spoutProbe.at) > spoutProbeTTL
	kick := stale && !u.spoutProbe.busy
	if kick {
		u.spoutProbe.busy = true
	}
	u.spoutProbe.mu.Unlock()
	if kick {
		u.bg(u.refreshSpoutProbe)
	}
	return st
}

// refreshSpoutProbe re-probes the DLL off the render goroutine + re-patches the Spout controls
// block when the install state changed (probe landed / DLL installed). #ovl-spout only exists when
// the Spout backend is active; __patch is a no-op on a missing id.
func (u *UI) refreshSpoutProbe() {
	st := spoutdll.Probe()
	u.spoutProbe.mu.Lock()
	changed := !u.spoutProbe.ready || st != u.spoutProbe.st
	u.spoutProbe.st, u.spoutProbe.at, u.spoutProbe.ready, u.spoutProbe.busy = st, time.Now(), true, false
	u.spoutProbe.mu.Unlock()
	if changed && !u.stopped() {
		u.eval("window.__patch('ovl-spout'," + jsQuote(u.spoutControlsHTML()) + ")")
	}
}

// ── overlay-style.json fader-flag cache (no os.ReadFile+Unmarshal on every overlays render) ──

const ovlStyleTTL = 5 * time.Second

type ovlStyleCache struct {
	mu      sync.Mutex
	watcher *overlaystyle.Watcher // owned here; only ever touched inside refreshOvlStyle (busy-guarded → single-flight)
	path    string
	fader   bool
	at      time.Time
	ready   bool
	busy    bool
}

// ovlFaderCached returns the cached cardFaderReact flag, kicking an off-thread refresh when stale.
// Never reads the filesystem - safe on the render goroutine (the Watcher's os.Stat runs in u.bg).
func (u *UI) ovlFaderCached() bool {
	u.ovlStyle.mu.Lock()
	v := u.ovlStyle.fader
	stale := !u.ovlStyle.ready || time.Since(u.ovlStyle.at) > ovlStyleTTL
	kick := stale && !u.ovlStyle.busy
	if kick {
		u.ovlStyle.busy = true
	}
	u.ovlStyle.mu.Unlock()
	if kick {
		u.bg(u.refreshOvlStyle)
	}
	return v
}

// refreshOvlStyle re-reads the fader flag via the mtime-caching Watcher (off the render goroutine)
// and re-patches the appearance card when it changed (e.g. the browser editor flipped it).
func (u *UI) refreshOvlStyle() {
	path, _ := config.DataPath("overlay-style.json")
	u.ovlStyle.mu.Lock()
	if u.ovlStyle.watcher == nil || u.ovlStyle.path != path {
		u.ovlStyle.watcher = overlaystyle.NewWatcher(path)
		u.ovlStyle.path = path
	}
	w := u.ovlStyle.watcher
	u.ovlStyle.mu.Unlock()
	st := w.Get() // os.Stat; re-parses only when the file changed
	fader := st.CardFaderReact != nil && *st.CardFaderReact
	u.ovlStyle.mu.Lock()
	changed := !u.ovlStyle.ready || fader != u.ovlStyle.fader
	u.ovlStyle.fader, u.ovlStyle.at, u.ovlStyle.ready, u.ovlStyle.busy = fader, time.Now(), true, false
	u.ovlStyle.mu.Unlock()
	if changed && !u.stopped() && u.activeTab() == "overlays" {
		u.eval("window.__patch('ovl-appearance'," + jsQuote(u.overlayAppearanceCard(u.ovlBase())) + ")")
	}
}

// invalidateOvlStyle forces the next ovlFaderCached to re-read (after the fader toggle writes).
func (u *UI) invalidateOvlStyle() {
	u.ovlStyle.mu.Lock()
	u.ovlStyle.at = time.Time{}
	u.ovlStyle.mu.Unlock()
}

// ovlValidHex reports whether s is a #rgb or #rrggbb colour.
func ovlValidHex(s string) bool {
	if (len(s) != 4 && len(s) != 7) || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
