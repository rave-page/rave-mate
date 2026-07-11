package webui

// Settings auto-apply: a config edit must never require the user to toggle a feature
// off/on. Live-read settings already apply on save (Session.Reconcile + per-use config
// closures); settings a module only reads at (re)start trigger a debounced automatic
// module restart here. Modules mid-capture/recording defer the restart until idle so
// an edit never cuts a live set.

import (
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/overlayserver"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/videoshare"
)

const (
	settingRestartDebounce = 1500 * time.Millisecond // coalesce rapid edits (typing a port)
	settingRestartRetry    = 5 * time.Second         // busy-module poll until idle
)

// settingRestarts holds the pending debounced module restarts (one timer per module).
type settingRestarts struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

// settingModule maps a set:<id> field to the module whose (re)start consumes it
// ("" = applies live). Keep in sync with the module Start closures / featurehost init
// closures in internal/app/app.go - they re-read config per (re)spawn.
func settingModule(id string) string {
	switch id {
	case "traktor-port":
		return "traktor"
	case "obs-host", "obs-port", "obs-pass":
		return "obs"
	case "ablelink-quantum", "ablelink-sss": // child init; tempo owner + Resolume fields read live
		return "abletonlink"
	case "peer-nick": // advertised by discovery at peers start
		return "peers"
	}
	for _, p := range [...]struct{ pre, mod string }{
		{"sc-", "setcapture"},
		{"dmxmidi-", "dmxmidi"},
		{"dmx-", "dmx"},
		{"rtsp-", "rtspserve"},
	} {
		if strings.HasPrefix(id, p.pre) {
			return p.mod
		}
	}
	return ""
}

// settingSource maps a set:<id> field to the session source that must restart to apply
// it ("" = none). These sources are registered via aggregator.AddSourceFn, so a restart
// rebuilds them from live config.
func settingSource(id string) string {
	switch id {
	case "nml-path":
		return session.SourceNML
	case "serato-dir", "serato-np":
		return session.SourceSerato
	case "serato-remotedebug":
		return session.SourceSeratoRemote
	case "serato-liveurl", "serato-liveinterval":
		return session.SourceSeratoLive
	case "vdj-dir", "vdj-netctl", "vdj-netctlurl", "vdj-netctlauth", "vdj-os2l", "vdj-tracklist":
		return session.SourceVirtualDJ
	case "rb-dbpoll", "rb-memread":
		return session.SourceRekordbox
	}
	return ""
}

// settingSink maps a set:<id> field to the session sink whose (re)start consumes it
// ("" = none). Sinks restart via Session.RestartSink (busy-safe: a watched live overlay
// defers like busy modules).
func settingSink(id string) string {
	switch id {
	case "overlay-port":
		return overlayserver.SinkID
	}
	return ""
}

// sinkTitleKey maps a sink ID → i18n key of its Overlays-card title for restart toasts.
var sinkTitleKey = map[string]string{
	overlayserver.SinkID: "overlays.web.title",
	videoshare.SinkID:    "overlays.vs.title",
}

// sinkDisplayName resolves the localized output name for restart toasts.
func sinkDisplayName(sid string) string {
	if k, ok := sinkTitleKey[sid]; ok {
		return i18n.T(k)
	}
	return sid
}

// sinkBusy reports a sink in a state an automatic restart would visibly damage (a live
// overlay someone is watching). The scheduler retries until idle instead of cutting it.
func (u *UI) sinkBusy(sid string) bool {
	switch sid {
	case overlayserver.SinkID:
		return u.svc.OverlayWeb != nil && u.svc.OverlayWeb.Busy()
	case videoshare.SinkID:
		return videoshare.Backend() != "none" && u.liveOnAir()
	}
	return false
}

// liveOnAir reports ≥1 deck currently on-air (a restart would cut visible live output).
func (u *UI) liveOnAir() bool {
	if u.svc.Session == nil {
		return false
	}
	ov := u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	for _, d := range ov.Decks {
		if d.OnAir {
			return true
		}
	}
	return false
}

// scheduleSinkRestart debounces a restart of a session sink (overlay web server /
// video share) so its new config applies without a manual feature off/on.
func (u *UI) scheduleSinkRestart(sid string) {
	if u.svc.Session == nil {
		return
	}
	key := "snk:" + sid
	u.restarts.mu.Lock()
	if u.restarts.timers == nil {
		u.restarts.timers = map[string]*time.Timer{}
	}
	if t := u.restarts.timers[key]; t != nil {
		t.Stop()
	}
	u.restarts.timers[key] = time.AfterFunc(settingRestartDebounce, func() { u.sinkRestart(sid, true) })
	u.restarts.mu.Unlock()
}

// sinkRestart executes a due sink restart; a busy sink re-arms the timer (toast once).
func (u *UI) sinkRestart(sid string, notifyDefer bool) {
	key := "snk:" + sid
	if u.svc.Session == nil {
		u.clearRestart(key)
		return
	}
	if u.sinkBusy(sid) {
		if notifyDefer {
			u.toast(i18n.T("overlays.toast.restartDeferred", i18n.A{"name": sinkDisplayName(sid)}))
		}
		u.restarts.mu.Lock()
		u.restarts.timers[key] = time.AfterFunc(settingRestartRetry, func() { u.sinkRestart(sid, false) })
		u.restarts.mu.Unlock()
		return
	}
	u.clearRestart(key)
	if !u.svc.Session.RestartSink(sid) {
		return // not running - next start reads fresh config anyway
	}
	u.toast(i18n.T("settings.toast.moduleRestarted", i18n.A{"name": sinkDisplayName(sid)}))
	// The auto-managed OBS browser source embeds the overlay URL - re-point it at the new
	// port by restarting the OBS bridge (its init closure re-reads config; busy-deferred
	// while recording; no-op when the module isn't running).
	if sid == overlayserver.SinkID && u.svc.Cfg != nil && u.svc.Cfg.Features.OverlayWeb.OBSSource.Enabled {
		u.scheduleModuleRestart("obs")
	}
	if t := u.activeTab(); t == "overlays" || t == "settings" {
		u.patchMain() // URLs/status reflect the fresh state immediately
	}
}

// sourceToggleKey maps a source ID → settings.toggle.<key> for the restart toast.
var sourceToggleKey = map[string]string{
	session.SourceNML:          "nml",
	session.SourceSerato:       "serato",
	session.SourceSeratoRemote: "serato",
	session.SourceSeratoLive:   "serato",
	session.SourceVirtualDJ:    "virtualdj",
	session.SourceRekordbox:    "rekordbox",
}

// scheduleSourceRestart debounces a rebuild-restart of a session source (nml/serato/
// virtualdj/rekordbox) so its new config applies without a manual feature off/on.
func (u *UI) scheduleSourceRestart(sid string) {
	if u.svc.Session == nil {
		return
	}
	key := "src:" + sid
	u.restarts.mu.Lock()
	if u.restarts.timers == nil {
		u.restarts.timers = map[string]*time.Timer{}
	}
	if t := u.restarts.timers[key]; t != nil {
		t.Stop()
	}
	u.restarts.timers[key] = time.AfterFunc(settingRestartDebounce, func() { u.sourceRestart(sid) })
	u.restarts.mu.Unlock()
}

func (u *UI) sourceRestart(sid string) {
	u.clearRestart("src:" + sid)
	if u.svc.Session == nil || !u.svc.Session.RestartSource(sid) {
		return // not running - next start reads fresh config anyway
	}
	name := sid
	if k, ok := sourceToggleKey[sid]; ok {
		name = i18n.T("settings.toggle." + k)
	}
	u.toast(i18n.T("settings.toast.moduleRestarted", i18n.A{"name": name}))
	if u.activeTab() == "settings" {
		u.patchMain()
	}
}

// moduleBusy reports a module in a state an automatic restart would damage (live
// capture / recording). The scheduler retries until idle instead of cutting it.
func (u *UI) moduleBusy(mod string) bool {
	switch mod {
	case "setcapture":
		if s := u.svc.SetCapture; s != nil {
			c := s.Snapshot()
			return c.Connected || c.Reconnecting
		}
	case "obs":
		if o := u.svc.OBS; o != nil {
			return o.Status().Recording
		}
	}
	return false
}

// moduleToggleKey maps module name → settings.toggle.<key> for the restart toasts
// (reuses the feature-toggle labels; no duplicate strings).
var moduleToggleKey = map[string]string{
	"traktor": "traktor", "obs": "obs", "abletonlink": "ablelink", "peers": "peers",
	"setcapture": "setcapture", "dmx": "dmx", "dmxmidi": "dmxmidi", "rtspserve": "rtsp",
}

// moduleDisplayName resolves the localized feature name for restart toasts.
func moduleDisplayName(mod string) string {
	if k, ok := moduleToggleKey[mod]; ok {
		return i18n.T("settings.toggle." + k)
	}
	return mod
}

// scheduleModuleRestart debounces an automatic restart of mod so its new config is
// picked up without a manual feature off/on. No-op when the module isn't running
// (a stopped feature reads fresh config on its next start anyway).
func (u *UI) scheduleModuleRestart(mod string) {
	if u.svc.Modules == nil {
		return
	}
	u.restarts.mu.Lock()
	if u.restarts.timers == nil {
		u.restarts.timers = map[string]*time.Timer{}
	}
	if t := u.restarts.timers[mod]; t != nil {
		t.Stop()
	}
	u.restarts.timers[mod] = time.AfterFunc(settingRestartDebounce, func() { u.moduleRestart(mod, true) })
	u.restarts.mu.Unlock()
}

// moduleRestart executes a due restart; a busy module re-arms the timer (toast once).
func (u *UI) moduleRestart(mod string, notifyDefer bool) {
	m := u.svc.Modules
	if m == nil || !m.IsRunning(mod) {
		u.clearRestart(mod)
		return
	}
	if u.moduleBusy(mod) {
		if notifyDefer {
			u.toast(i18n.T("settings.toast.restartDeferred", i18n.A{"name": moduleDisplayName(mod)}))
		}
		u.restarts.mu.Lock()
		u.restarts.timers[mod] = time.AfterFunc(settingRestartRetry, func() { u.moduleRestart(mod, false) })
		u.restarts.mu.Unlock()
		return
	}
	u.clearRestart(mod)
	m.Restart(mod)
	u.toast(i18n.T("settings.toast.moduleRestarted", i18n.A{"name": moduleDisplayName(mod)}))
	if u.activeTab() == "settings" {
		u.patchMain() // status dots reflect the fresh module state immediately
	}
}

func (u *UI) clearRestart(mod string) {
	u.restarts.mu.Lock()
	if t := u.restarts.timers[mod]; t != nil {
		t.Stop()
	}
	delete(u.restarts.timers, mod)
	u.restarts.mu.Unlock()
}
