package webui

// Self-update UX over the internal/updater state machine (idle → available → downloading →
// downloaded/verified → staged/needs-restart). One Manager drives every surface: the nav-rail
// bottom block (#nav-update), the tray menu's state-dependent item, the Settings→System card
// region (#inst-update), and the once-per-version first-detection notification (tray balloon +
// in-app toast; persisted in config.UpdateNotifiedFor).

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/coord"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/sysnotify"
	"rave.page/mate/internal/updater"
	"rave.page/mate/internal/version"
)

func init() {
	// Settings card "Check for updates" + the ONE state-dependent action (action-bound choice:
	// the button label states the auto-detected next step; shared by nav rail + settings + tray).
	onExact("settings-update-check", func(u *UI, _ actMsg) { u.updateCheck() })
	onExact("upd-download", func(u *UI, _ actMsg) {
		if u.updMgr != nil {
			u.updMgr.StartDownload()
		}
	})
	onExact("upd-install", func(u *UI, _ actMsg) {
		if u.updMgr != nil {
			u.updMgr.Install()
		}
	})
	onExact("upd-restart", func(u *UI, _ actMsg) { u.updateRestart() })
}

// initUpdater builds + starts the update manager (webview renderer's updater surface; replaces
// the old 6h notify loop with the 5-min poll). No-op on a dev build (empty FeedURL).
func (u *UI) initUpdater() {
	u.updMgr = updater.New(updater.Config{
		Feed: updater.WrapFeed(selfupdate.New(version.FeedURL, version.BuildNum(), version.UpdatePubKey)),
		Log:  u.log,
		LastNotified: func() string {
			if u.svc.Cfg != nil {
				return u.svc.Cfg.UpdateNotifiedFor
			}
			return ""
		},
		SetNotified: func(v string) {
			if u.svc.Cfg == nil {
				return
			}
			u.svc.Cfg.UpdateNotifiedFor = v
			cfgSaveMu.Lock()
			_ = u.svc.Cfg.Save()
			cfgSaveMu.Unlock()
		},
		Notify:   u.notifyUpdate,
		OnChange: u.onUpdateChange,
	})
	go u.updMgr.Run(u.stop)
}

// notifyUpdate fires the first-detection notification: tray balloon (NIF_INFO; click raises the
// window) + in-app toast. The manager guarantees once per version, persisted across restarts.
func (u *UI) notifyUpdate(rel *selfupdate.Release) {
	title := i18n.T("tray.updateNotifyTitle")
	body := i18n.T("tray.updateNotifyBody", i18n.A{"version": rel.Version})
	u.toast(title + ": " + body)
	_ = sysnotify.SendAction(title, body, func() { u.Show() })
}

// onUpdateChange re-renders every updater surface on a state/progress change (runs on manager
// goroutines; eval marshals to the UI thread).
func (u *UI) onUpdateChange(updater.Status) {
	u.eval("window.__patch('nav-update'," + jsQuote(u.navUpdateHTML()) + ")")
	if u.activeTab() == "settings" {
		u.patchUpd(u.updateFlowHTML())
	}
}

// ── nav rail block (#nav-update, bottom of the tab rail) ──

// navUpdateHTML renders the rail's update block: version + channel + short note + ONE
// state-dependent action. Empty when no update is known (no dead chrome).
func (u *UI) navUpdateHTML() string {
	if u.updMgr == nil {
		return ""
	}
	st := u.updMgr.Status()
	if st.State == updater.Idle || st.Rel == nil {
		return ""
	}
	head := i18n.T("nav.update.headAvailable")
	switch st.State {
	case updater.Downloaded:
		head = i18n.T("nav.update.headVerified")
	case updater.Staged:
		head = i18n.T("nav.update.headStaged")
	}
	var b strings.Builder
	b.WriteString(`<div class=nav-upd data-label="nav-update-block">`)
	b.WriteString(`<div class=nav-upd-head>` + navUpdIcon + `<span>` + html.EscapeString(head) + `</span>` + tipTopic("app-updates") + `</div>`)
	b.WriteString(`<div class=nav-upd-meta>` + html.EscapeString(i18n.T("nav.update.meta",
		i18n.A{"version": st.Rel.Version, "channel": version.ResolvedChannel()})) + `</div>`)
	if n := shortNote(st.Rel.Notes); n != "" {
		b.WriteString(`<div class=nav-upd-note>` + html.EscapeString(n) + `</div>`)
	}
	if st.Err != "" {
		b.WriteString(hint("bad", i18n.T("nav.update.failed")+st.Err))
	}
	switch st.State {
	case updater.Available:
		label := i18n.T("nav.update.download", i18n.A{"version": st.Rel.Version})
		if st.Err != "" {
			label = i18n.T("nav.update.retry")
		}
		b.WriteString(btn(label, "primary", "upd-download", ""))
	case updater.Downloading:
		b.WriteString(progressBar(st.Progress, i18n.T("nav.update.downloading",
			i18n.A{"pct": strconv.Itoa(int(st.Progress * 100))})))
	case updater.Downloaded:
		b.WriteString(`<div class=nav-upd-note>` + html.EscapeString(i18n.T("nav.update.verifiedNote")) + `</div>`)
		b.WriteString(btn(i18n.T("nav.update.install"), "primary", "upd-install", ""))
	case updater.Staged:
		b.WriteString(`<div class=nav-upd-note>` + html.EscapeString(i18n.T("nav.update.stagedNote")) + `</div>`)
		b.WriteString(btn(i18n.T("nav.update.restart"), "primary", "upd-restart", ""))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// navUpdIcon - lucide-style download glyph for the block head.
const navUpdIcon = `<svg class=nav-upd-ic viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>`

// shortNote returns the first line of the release notes, capped for the rail block.
func shortNote(notes string) string {
	n := strings.TrimSpace(notes)
	if i := strings.IndexByte(n, '\n'); i >= 0 {
		n = strings.TrimSpace(n[:i])
	}
	if len(n) > 110 {
		n = n[:110] + "…"
	}
	return n
}

// ── tray menu (state-dependent item; consulted on every menu open) ──

// trayUpdateLabel returns the tray item label for the current state ("" = up to date → no item).
func (u *UI) trayUpdateLabel() string {
	if u.updMgr == nil {
		return ""
	}
	st := u.updMgr.Status()
	if st.Rel == nil {
		return ""
	}
	switch st.State {
	case updater.Available:
		return i18n.T("tray.update.download", i18n.A{"version": st.Rel.Version})
	case updater.Downloading:
		return i18n.T("tray.update.downloading", i18n.A{"pct": strconv.Itoa(int(st.Progress * 100))})
	case updater.Downloaded:
		return i18n.T("tray.update.install")
	case updater.Staged:
		return i18n.T("tray.update.restart")
	}
	return ""
}

// trayUpdateAction advances the same state machine the nav-rail button drives.
func (u *UI) trayUpdateAction() {
	if u.updMgr == nil {
		return
	}
	switch u.updMgr.Status().State {
	case updater.Available:
		u.updMgr.StartDownload()
	case updater.Downloading:
		u.Show() // watch progress in the rail
	case updater.Downloaded:
		u.updMgr.Install()
	case updater.Staged:
		u.updateRestart()
	}
}

// ── Settings→System card region (#inst-update) ──

// patchUpd patches the #inst-update region (check/apply progress + result).
func (u *UI) patchUpd(inner string) { u.eval("window.__patch('inst-update'," + jsQuote(inner) + ")") }

// updateFlowHTML renders #inst-update from the manager status (same machine as the rail).
func (u *UI) updateFlowHTML() string {
	if u.updMgr == nil || !u.updMgr.Enabled() {
		return ""
	}
	st := u.updMgr.Status()
	switch st.State {
	case updater.Idle:
		switch {
		case st.Err != "":
			return hint("bad", i18n.T("settings.body.updates.checkFailed")+st.Err)
		case st.Checked:
			return hint("ok", i18n.T("settings.body.updates.upToDate"))
		default:
			return "" // not checked yet - no verdict to show
		}
	case updater.Available:
		body := hint("warn", i18n.T("settings.body.updates.available", i18n.A{"version": st.Rel.Version}))
		if st.Rel.Notes != "" {
			body += `<div class=set-note>` + html.EscapeString(st.Rel.Notes) + `</div>`
		}
		if st.Err != "" {
			body += hint("bad", i18n.T("nav.update.failed")+st.Err)
		}
		return body + btnRow(btn(i18n.T("nav.update.download", i18n.A{"version": st.Rel.Version}), "primary", "upd-download", ""))
	case updater.Downloading:
		return progressBar(st.Progress, i18n.T("settings.label.downloading"))
	case updater.Downloaded:
		body := hint("ok", i18n.T("nav.update.verifiedNote"))
		if st.Err != "" {
			body += hint("bad", i18n.T("settings.body.updates.applyFailed")+st.Err)
		}
		return body + btnRow(btn(i18n.T("nav.update.install"), "primary", "upd-install", ""))
	case updater.Staged:
		return hint("ok", i18n.T("settings.body.updates.installedRestart")) +
			btnRow(btn(i18n.T("settings.body.updates.restart"), "primary", "upd-restart", ""))
	}
	return ""
}

// updateCheck runs an immediate poll and renders the verdict into #inst-update.
func (u *UI) updateCheck() {
	if u.updMgr == nil || !u.updMgr.Enabled() {
		u.patchUpd(hint("info", i18n.T("settings.body.updates.devNoFeed")))
		return
	}
	u.patchUpd(hint("info", i18n.T("settings.body.updates.checking")))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		u.updMgr.Check(ctx)
		u.patchUpd(u.updateFlowHTML()) // explicit: Check without a state change emits nothing
	})
}

// updateRestart tells a co-located rave-app to update too, then relaunches the swapped exe.
func (u *UI) updateRestart() {
	coord.NotifyRaveApp() // user-initiated → keep a co-located rave-app in lockstep
	if err := selfupdate.Relaunch(); err != nil {
		u.logErr("relaunch", err)
		u.toast(i18n.T("settings.body.updates.restartFailed") + err.Error())
		return
	}
	u.Stop()
}
