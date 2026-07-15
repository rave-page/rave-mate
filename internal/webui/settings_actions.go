package webui

import (
	"context"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/elevate"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/rekordboxmap"
	"rave.page/mate/internal/service"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/timecode"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrdll"
)

// Settings-tab action handlers + the ~1 Hz status tick. All names are namespaced `settings-*`
// (scalar/bool field writes go through the shared set:/toggle: path handled in ui.go + applySet).

func init() {
	// live status refresh - only cards actually in the DOM (active sub-tab / search matches),
	// one coalesced eval, unchanged fragments skipped (tickPatch)
	onLiveTick("settings", func(u *UI) {
		if u.svc.Cfg == nil {
			return
		}
		u.maybeRefreshProbes() // keep the cached fs/PATH/device probes warm off the render path
		stats := u.settingsStatus()
		visible, searching := u.settingsVisible()
		var js strings.Builder
		for id, s := range stats {
			if !visible[id] {
				continue
			}
			u.tickPatch(&js, "stset-"+id, renderStatus(s))
		}
		if !searching { // sub-tab pills (with aggregate dots) only exist outside search mode
			for _, sec := range settingsSections() {
				agg := "off"
				for _, id := range sec.cards {
					if st, ok := stats[id]; ok && stRank(st.v) > stRank(agg) {
						agg = st.v
					}
				}
				u.tickPatch(&js, "stnav-"+sec.id, `<span class="dot dot--`+agg+`"></span>`)
			}
		}
		u.flushTick(&js)
	})

	onExact("settings-refresh", func(u *UI, _ actMsg) {
		u.invalidateProbes()
		u.maybeRefreshProbes() // async; re-patches when device lists change
		u.patchMain()
	})

	// settings sub-tab pill
	onExact("settings-sec", func(u *UI, m actMsg) {
		u.navRecord()
		u.setMu.Lock()
		u.setSec = m.Val
		u.setMu.Unlock()
		u.patchSettingsContent()
	})

	// global settings search (per-keystroke input events, debounced Go-side)
	onExact("settings-search", func(u *UI, m actMsg) { u.settingsSearchInput(m.Val) })

	// Ableton Link: hard-realign the phrase (map beat 0 to now).
	onExact("ablelink-resync", func(u *UI, _ actMsg) {
		if u.svc.AbleLink == nil {
			return
		}
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			u.logErr("ablelink resync", u.svc.AbleLink.Resync(ctx))
		})
	})

	// interface language: persist features.ui.language, switch active locale, re-render shell.
	onPrefix("ui-setlang:", func(u *UI, m actMsg) { u.setLanguage(m.arg("ui-setlang:")) })

	// folder openers
	onPrefix("settings-open:", func(u *UI, m actMsg) {
		what := m.arg("settings-open:")
		var p string
		switch what {
		case "sets":
			p = u.svc.Cfg.Features.SetCapture.ResolvedSetsDir()
		case "recordings":
			p = u.svc.Cfg.Features.AudioRecord.ResolvedDir()
		case "remotecache":
			if c := u.rceCacheStore(); c != nil {
				p = c.Root()
				_ = os.MkdirAll(p, 0o755) // created lazily on first pull - ensure it opens
			}
		}
		if p != "" {
			_ = openURL(p)
		}
	})

	// ── remote cue-edit cache (LAN peers card) ──
	onExact("settings-rcecache-clear", func(u *UI, _ actMsg) {
		body := `<p class=page-sub>` + html.EscapeString(i18n.T("settings.body.peers.cacheClearConfirm")) + `</p>` +
			btnRow(btn(i18n.T("settings.body.peers.cacheClear"), "destructive", "settings-rcecache-purge", ""),
				btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
		u.openModal(modal(i18n.T("settings.body.peers.cacheClearTitle"), body, ""))
	})
	onExact("settings-rcecache-purge", func(u *UI, _ actMsg) {
		u.closeModal()
		if u.rceActive() { // editor holds a cached copy open - deleting under it breaks audition
			u.toast(i18n.T("settings.body.peers.cacheBusy"))
			return
		}
		u.bg(func() {
			c := u.rceCacheStore()
			if c == nil {
				u.toast(i18n.T("library.rce.cacheFail"))
				return
			}
			if err := c.Purge(); err != nil {
				u.toast(i18n.T("settings.body.peers.cacheClearFailed") + err.Error())
				return
			}
			u.toast(i18n.T("settings.body.peers.cacheCleared"))
			u.patchMain() // usage line refresh
		})
	})

	// media-tool installs (progress patched into #inst-<key>)
	onPrefix("settings-install:", func(u *UI, m actMsg) {
		key := m.arg("settings-install:")
		tool, ok := map[string]mediatools.Tool{"ffmpeg": mediatools.FFmpeg, "fpcalc": mediatools.Fpcalc, "mpv": mediatools.MPV}[key]
		if !ok {
			return
		}
		u.runInstall(key, tool.Display, func(ctx context.Context, cb func(int64, int64)) error { return tool.Install(ctx, cb) })
	})
	onExact("settings-install-vr", func(u *UI, _ actMsg) {
		u.runInstall("vr", i18n.T("settings.label.vrRuntime"), func(ctx context.Context, cb func(int64, int64)) error {
			return vrdll.Install(ctx, version.FeedURL, cb)
		})
	})
	onExact("settings-stt-install", func(u *UI, _ actMsg) {
		model := u.svc.Cfg.Features.STT.Model
		u.runInstall("stt", "Whisper", func(ctx context.Context, cb func(int64, int64)) error {
			return stt.Install(ctx, model, cb)
		})
	})

	// ── Traktor QML feed (elevated) ──
	onExact("settings-qml-apply", func(u *UI, _ actMsg) { u.qmlElevate("apply") })
	onExact("settings-qml-revert", func(u *UI, _ actMsg) { u.qmlElevate("revert") })

	// ── Traktor mappings ──
	onPrefix("settings-tmap-on:", func(u *UI, m actMsg) { u.traktorMap(m.arg("settings-tmap-on:"), true) })
	onPrefix("settings-tmap-off:", func(u *UI, m actMsg) { u.traktorMap(m.arg("settings-tmap-off:"), false) })

	// ── Rekordbox key ──
	onExact("settings-rbkey-save", func(u *UI, m actMsg) {
		key := strings.TrimSpace(parseForm(m.Form)["key"])
		if key == "" {
			return
		}
		u.bg(func() {
			if err := rekordboxdb.SaveKey(key); err != nil {
				u.toast(i18n.T("settings.toast.saveKeyFailed") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.rekordboxKeySaved"))
		})
	})
	onExact("settings-rbkey-test", func(u *UI, _ actMsg) {
		u.bg(func() {
			dbs := rekordboxdb.DiscoverRekordboxMasterDB()
			if len(dbs) == 0 {
				u.toast(i18n.T("settings.toast.noMasterDb"))
				return
			}
			lib, err := rekordboxdb.Open(dbs[0], "")
			if err != nil {
				u.toast(i18n.T("settings.toast.masterDbErr") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.masterDbOk", i18n.A{"tracks": strconv.Itoa(len(lib.Tracks)), "sessions": strconv.Itoa(len(lib.Sessions))}))
		})
	})
	onExact("settings-rbmidi-export", func(u *UI, _ actMsg) {
		u.bg(func() {
			p, err := config.DataPath("RavePage-rekordbox.csv")
			if err != nil {
				p = "RavePage-rekordbox.csv"
			}
			if err := rekordboxmap.Export(p); err != nil {
				u.toast(i18n.T("settings.toast.exportFailed") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.rbmidiSaved", i18n.A{"path": p, "device": rekordboxmap.DefaultDevice}))
		})
	})
	onExact("settings-rbmidi-folder", func(u *UI, _ actMsg) {
		if p, err := config.DataPath(""); err == nil {
			_ = openURL(p)
		}
	})

	// ── OBS ──
	onExact("settings-obs-validate", func(u *UI, _ actMsg) {
		f := u.svc.Cfg.Features.OBS
		u.toast(i18n.T("settings.toast.obsConnecting"))
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			c, err := obs.Connect(ctx, f.ResolvedHost(), f.ResolvedPort(), f.Password)
			if err != nil {
				u.toast(i18n.T("settings.toast.obsConnectFailed") + err.Error())
				return
			}
			defer func() { _ = c.Close() }()
			diffs, err := c.ValidateStreamSettings(obs.DefaultStreamRequirements())
			switch {
			case err != nil:
				u.toast(i18n.T("settings.toast.obsValidateFailed") + err.Error())
			case len(diffs) == 0:
				u.toast(i18n.T("settings.toast.obsStreamGood"))
			default:
				u.toast(i18n.T("settings.toast.obsCheck") + strings.Join(diffs, "; "))
			}
		})
	})
	onExact("settings-obsrem", func(u *UI, _ actMsg) { u.obsRemotesModal() })
	onExact("settings-obsrem-add", func(u *UI, _ actMsg) {
		f := &u.svc.Cfg.Features.OBS
		f.Remotes = append(f.Remotes, config.OBSRemote{Port: 4455, Enabled: true})
		u.saveCfg()
		u.obsRemotesModal()
	})
	onPrefix("settings-obsrem-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.OBS
		if i := atoiSafe(m.arg("settings-obsrem-del:")); i >= 0 && i < len(f.Remotes) {
			f.Remotes = append(f.Remotes[:i], f.Remotes[i+1:]...)
			u.saveCfg()
		}
		u.obsRemotesModal()
	})
	onPrefix("settings-obsrem-edit:", func(u *UI, m actMsg) { u.obsRemoteEditModal(atoiSafe(m.arg("settings-obsrem-edit:"))) })
	onPrefix("settings-obsrem-save:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.OBS
		i := atoiSafe(m.arg("settings-obsrem-save:"))
		if i < 0 || i >= len(f.Remotes) {
			return
		}
		fm := parseForm(m.Form)
		r := &f.Remotes[i]
		r.Name, r.Host, r.Password = fm["name"], fm["host"], fm["pass"]
		r.Enabled = fm["enabled"] == "on" || fm["enabled"] == "true"
		if n, err := strconv.Atoi(strings.TrimSpace(fm["port"])); err == nil && n > 0 && n < 65536 {
			r.Port = n
		}
		u.saveCfg()
		u.obsRemotesModal()
	})

	// ── OBS media-sync sources ──
	onExact("settings-obssync-src", func(u *UI, _ actMsg) { u.obsSyncModal() })
	onExact("settings-obssync-add", func(u *UI, _ actMsg) {
		f := &u.svc.Cfg.Features.OBS.Sync
		f.Sources = append(f.Sources, config.OBSSyncSource{Enabled: true})
		u.saveCfg()
		u.obsSyncModal()
	})
	onPrefix("settings-obssync-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.OBS.Sync
		if i := atoiSafe(m.arg("settings-obssync-del:")); i >= 0 && i < len(f.Sources) {
			f.Sources = append(f.Sources[:i], f.Sources[i+1:]...)
			u.saveCfg()
		}
		u.obsSyncModal()
	})
	onPrefix("settings-obssync-save:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.OBS.Sync
		i := atoiSafe(m.arg("settings-obssync-save:"))
		if i < 0 || i >= len(f.Sources) {
			return
		}
		fm := parseForm(m.Form)
		s := &f.Sources[i]
		s.InputName, s.Endpoint, s.InputKind = fm["input"], fm["endpoint"], fm["kind"]
		s.Enabled = fm["enabled"] == "on" || fm["enabled"] == "true"
		if n, err := strconv.Atoi(strings.TrimSpace(fm["offset"])); err == nil {
			s.StaticOffsetMs = n
		}
		u.saveCfg()
		u.obsSyncModal()
	})
	onPrefix("settings-obssync-edit:", func(u *UI, m actMsg) { u.obsSyncEditModal(atoiSafe(m.arg("settings-obssync-edit:"))) })

	// ── Timecode extra sinks ──
	onPrefix("settings-tcextra:", func(u *UI, m actMsg) { u.tcExtraModal(m.arg("settings-tcextra:")) })
	onPrefix("settings-tcx-add:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Timecode
		switch m.arg("settings-tcx-add:") {
		case "ltc":
			f.LTCExtra = append(f.LTCExtra, config.TCLTCSink{On: true})
		case "mtc":
			f.MTCExtra = append(f.MTCExtra, config.TCMTCSink{On: true})
		case "art":
			f.ArtNetExtra = append(f.ArtNetExtra, config.TCArtNetSink{On: true})
		}
		u.saveCfg()
		u.tcExtraModal(m.arg("settings-tcx-add:"))
	})
	onPrefix("settings-tcx-del:", func(u *UI, m actMsg) {
		kind, idx := splitKindIdx(m.arg("settings-tcx-del:"))
		f := &u.svc.Cfg.Features.Timecode
		switch kind {
		case "ltc":
			if idx >= 0 && idx < len(f.LTCExtra) {
				f.LTCExtra = append(f.LTCExtra[:idx], f.LTCExtra[idx+1:]...)
			}
		case "mtc":
			if idx >= 0 && idx < len(f.MTCExtra) {
				f.MTCExtra = append(f.MTCExtra[:idx], f.MTCExtra[idx+1:]...)
			}
		case "art":
			if idx >= 0 && idx < len(f.ArtNetExtra) {
				f.ArtNetExtra = append(f.ArtNetExtra[:idx], f.ArtNetExtra[idx+1:]...)
			}
		}
		u.saveCfg()
		u.tcExtraModal(kind)
	})
	onPrefix("settings-tcx-set:", func(u *UI, m actMsg) { u.tcExtraSet(m.arg("settings-tcx-set:"), m.Val) })

	// ── Twitch ──
	onExact("settings-twitch-signin", func(u *UI, _ actMsg) {
		if u.svc.Twitch == nil {
			return
		}
		u.toast(i18n.T("settings.toast.twitchStarting"))
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			da, err := u.svc.Twitch.Auth().StartDevice(ctx)
			if err != nil {
				u.toast(i18n.T("settings.toast.twitchErr") + err.Error())
				return
			}
			_ = openURL(da.VerificationURI)
			u.toast(i18n.T("settings.toast.enterCodeAt", i18n.A{"code": da.UserCode, "url": da.VerificationURI}))
			if err := u.svc.Twitch.Auth().PollDevice(ctx, da); err != nil {
				u.toast(i18n.T("settings.toast.twitchSignInFailed") + err.Error())
				return
			}
			u.svc.Twitch.Kick()
			u.toast(i18n.T("settings.toast.twitchSignedIn"))
			u.patchMain()
		})
	})
	onExact("settings-twitch-signout", func(u *UI, _ actMsg) {
		if u.svc.Twitch != nil {
			u.svc.Twitch.Auth().Logout()
			u.toast(i18n.T("settings.toast.twitchSignedOut"))
			u.patchMain()
		}
	})
	onExact("settings-twpreset", func(u *UI, _ actMsg) { u.twPresetModal() })
	onExact("settings-twpreset-add", func(u *UI, _ actMsg) {
		f := &u.svc.Cfg.Features.Twitch
		f.Presets = append(f.Presets, config.TitlePreset{Name: "New preset", Template: "{genre} set @ {club}"})
		u.saveCfg()
		u.twPresetModal()
	})
	onPrefix("settings-twpreset-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Twitch
		if i := atoiSafe(m.arg("settings-twpreset-del:")); i >= 0 && i < len(f.Presets) {
			f.Presets = append(f.Presets[:i], f.Presets[i+1:]...)
			u.saveCfg()
		}
		u.twPresetModal()
	})
	onPrefix("settings-twpreset-edit:", func(u *UI, m actMsg) { u.twPresetEditModal(atoiSafe(m.arg("settings-twpreset-edit:"))) })
	onPrefix("settings-twpreset-save:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Twitch
		i := atoiSafe(m.arg("settings-twpreset-save:"))
		if i < 0 || i >= len(f.Presets) {
			return
		}
		fm := parseForm(m.Form)
		f.Presets[i].Name, f.Presets[i].Template, f.Presets[i].GameName = fm["name"], fm["template"], fm["game"]
		u.saveCfg()
		u.twPresetModal()
	})
	onPrefix("settings-twpreset-apply:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Twitch
		i := atoiSafe(m.arg("settings-twpreset-apply:"))
		if u.svc.Twitch == nil || i < 0 || i >= len(f.Presets) {
			return
		}
		p := f.Presets[i]
		u.toast(i18n.T("settings.toast.applyingPreset"))
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			if err := u.svc.Twitch.ApplyTitlePreset(ctx, p); err != nil {
				u.toast(i18n.T("settings.toast.titleFailed") + err.Error())
			} else {
				u.toast(i18n.T("settings.toast.streamTitleSet"))
			}
		})
	})

	// ── GitHub (World Sync link) ──
	onExact("settings-gh-device", func(u *UI, _ actMsg) {
		gh := u.svc.GitHub
		if gh == nil {
			return
		}
		u.toast(i18n.T("settings.toast.ghStarting"))
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			da, err := gh.StartDevice(ctx)
			if err != nil {
				u.toast(i18n.T("settings.toast.ghErr") + err.Error())
				return
			}
			_ = openURL(da.VerificationURI)
			u.toast(i18n.T("settings.toast.enterCodeAt", i18n.A{"code": da.UserCode, "url": da.VerificationURI}))
			if err := gh.PollDevice(ctx, da); err != nil {
				u.toast(i18n.T("settings.toast.ghLinkFailed") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.ghLinked"))
			u.patchMain()
		})
	})
	onExact("settings-gh-pat", func(u *UI, _ actMsg) {
		u.openModal(modal(i18n.T("settings.modal.pasteGhToken"),
			`<form class=set-dlgform data-act=settings-gh-patsave><input class=field-input type=password name=pat placeholder="`+html.EscapeString(i18n.T("settings.modal.ghPatPlaceholder"))+`" autocomplete=off>`+
				`<div class=set-note>`+i18n.T("settings.modal.ghPatNote")+`</div>`+
				`<button class="rp-btn rp-btn--primary" type=submit>`+i18n.T("settings.label.link")+`</button></form>`,
			btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
	})
	onExact("settings-gh-patsave", func(u *UI, m actMsg) {
		gh := u.svc.GitHub
		pat := strings.TrimSpace(parseForm(m.Form)["pat"])
		if gh == nil || pat == "" {
			return
		}
		u.closeModal()
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			if err := gh.SetPAT(ctx, pat); err != nil {
				u.toast(i18n.T("settings.toast.tokenRejected") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.ghLinkedAs", i18n.A{"login": gh.Login()}))
			u.patchMain()
		})
	})
	onExact("settings-gh-unlink", func(u *UI, _ actMsg) {
		if u.svc.GitHub != nil {
			u.svc.GitHub.Logout()
			u.patchMain()
		}
	})

	// ── VRChat ──
	onExact("settings-vrc-login", func(u *UI, m actMsg) {
		if u.svc.Vrchat == nil {
			return
		}
		fm := parseForm(m.Form)
		user, pass := strings.TrimSpace(fm["user"]), fm["pass"]
		if user == "" || pass == "" {
			return
		}
		u.toast(i18n.T("settings.toast.vrcSigningIn"))
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = u.svc.Vrchat.Login(ctx, user, pass)
			u.patchMain()
		})
	})
	onExact("settings-vrc-2fa", func(u *UI, m actMsg) {
		if u.svc.Vrchat == nil {
			return
		}
		code := strings.TrimSpace(parseForm(m.Form)["code"])
		if code == "" {
			return
		}
		method := ""
		if ms := u.svc.Vrchat.State().Methods; len(ms) > 0 && !hasStrWeb(ms, "totp") {
			method = ms[0]
		}
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = u.svc.Vrchat.Verify2FA(ctx, method, code)
			u.patchMain()
		})
	})
	onExact("settings-vrc-unlink", func(u *UI, _ actMsg) {
		if u.svc.Vrchat == nil {
			return
		}
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			u.svc.Vrchat.Unlink(ctx)
			u.patchMain()
		})
	})

	// ── VRChat tools ──
	onExact("settings-vct-organize", func(u *UI, _ actMsg) {
		if u.svc.VRCTools == nil {
			return
		}
		u.bg(func() {
			p, c := u.svc.VRCTools.OrganizeNow()
			vrcInvalidateScans() // fs moved - drop the shared vrchat-pane scan cache
			u.toast(i18n.T("settings.toast.organized", i18n.A{"photos": strconv.Itoa(p), "paths": strconv.Itoa(c)}))
		})
	})
	onExact("settings-vct-applypreset", func(u *UI, _ actMsg) {
		s := u.svc.VRCTools
		name := u.svc.Cfg.Features.VRCTools.DefaultCamPreset
		if s == nil || name == "" {
			return
		}
		u.bg(func() {
			if err := s.ApplyCamPreset(name); err != nil {
				u.toast(i18n.T("settings.toast.applyPresetErr") + err.Error())
			} else {
				u.toast(i18n.T("settings.toast.camPresetApplied"))
			}
		})
	})
	onExact("settings-vct-djpaths", func(u *UI, _ actMsg) {
		if u.svc.VRCTools == nil {
			return
		}
		u.bg(func() {
			n, dst, err := u.svc.VRCTools.InstallBuiltinPaths()
			if err != nil {
				u.toast(i18n.T("settings.toast.installDjPathsErr") + err.Error())
				return
			}
			u.toast(i18n.Tn("settings.toast.installedDjPaths", n, i18n.A{"dst": dst}))
		})
	})

	// ── VR overlays ──
	onExact("settings-vr-bindings", func(u *UI, _ actMsg) {
		if u.svc.VROverlay != nil {
			_ = u.svc.VROverlay.OpenBindingUI()
			u.toast(i18n.T("settings.toast.openingBindings"))
		}
	})
	onExact("settings-vrov", func(u *UI, _ actMsg) { u.vrOverlaysModal() })
	onExact("settings-vrov-add", func(u *UI, _ actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		f.Overlays = append(f.Overlays, config.VROverlay{ID: fmt.Sprintf("ov%d", time.Now().Unix()), Type: "chat", Enabled: true, Y: 1.4, Z: -1.0, WidthM: 0.5, Opacity: 0.9, MaxMessages: 8})
		u.saveCfg()
		u.vrOverlaysModal()
	})
	onPrefix("settings-vrov-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("settings-vrov-del:")); i >= 0 && i < len(f.Overlays) {
			f.Overlays = append(f.Overlays[:i], f.Overlays[i+1:]...)
			u.saveCfg()
		}
		u.vrOverlaysModal()
	})
	onPrefix("settings-vrov-edit:", func(u *UI, m actMsg) { u.vrOverlayEditModal(atoiSafe(m.arg("settings-vrov-edit:"))) })
	onPrefix("settings-vrov-save:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		i := atoiSafe(m.arg("settings-vrov-save:"))
		if i < 0 || i >= len(f.Overlays) {
			return
		}
		fm := parseForm(m.Form)
		o := &f.Overlays[i]
		o.Type = or(fm["type"], "chat")
		o.SnapTo = fm["snap"]
		o.Enabled = fm["enabled"] == "on" || fm["enabled"] == "true"
		o.AlwaysShow = fm["lock"] == "on" || fm["lock"] == "true"
		o.X = parseFWeb(fm["x"], o.X)
		o.Y = parseFWeb(fm["y"], o.Y)
		o.Z = parseFWeb(fm["z"], o.Z)
		o.Yaw = parseFWeb(fm["yaw"], o.Yaw)
		o.WidthM = parseFWeb(fm["width"], o.WidthM)
		o.Opacity = parseFWeb(fm["opacity"], o.Opacity)
		o.MaxMessages = int(parseFWeb(fm["maxmsg"], float64(o.MaxMessages)))
		u.saveCfg()
		u.vrOverlaysModal()
	})

	// ── Unity ──
	// VCC discovery = fs scans; run off actWorker, apply results back on it via redispatch
	// (config writes stay serialized).
	onExact("settings-unity-vcc", func(u *UI, _ actMsg) {
		u.toast(i18n.T("remote.loading"))
		u.bg(func() {
			var found []string
			for _, p := range unityproj.DiscoverVCCProjects() {
				if unityproj.IsUnityProject(p) {
					found = append(found, p)
				}
			}
			u.redispatch("settings-unity-vccadd", strings.Join(found, "\n"))
		})
	})
	// settings-unity-vccadd applies a finished VCC scan (Val = newline-joined project dirs)
	onExact("settings-unity-vccadd", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Unity
		added := 0
		for _, p := range strings.Split(m.Val, "\n") {
			if p != "" && !hasStrWeb(f.Projects, p) {
				f.Projects = append(f.Projects, p)
				added++
			}
		}
		u.saveCfg()
		u.patchMain()
		u.toast(i18n.Tn("settings.toast.addedFromVcc", added))
	})
	// pick-dir:settings-unity-addpath (Browse) re-dispatches here with the chosen folder
	onExact("settings-unity-addpath", func(u *UI, m actMsg) { u.unityAddProject(m.Val) })
	onExact("settings-unity-add", func(u *UI, _ actMsg) {
		u.openModal(modal(i18n.T("settings.modal.addUnityProject"),
			`<form class=set-dlgform data-act=settings-unity-addsave><input class=field-input name=path placeholder="`+html.EscapeString(i18n.T("settings.modal.unityPathPlaceholder"))+`" autocomplete=off>`+
				`<div class=set-note>`+i18n.T("settings.modal.unityAddNote")+`</div>`+
				`<button class="rp-btn rp-btn--primary" type=submit>`+i18n.T("settings.label.add")+`</button></form>`,
			btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
	})
	onExact("settings-unity-addsave", func(u *UI, m actMsg) {
		u.closeModal()
		u.unityAddProject(parseForm(m.Form)["path"])
	})
	onPrefix("settings-unity-remove:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Unity
		if i := atoiSafe(m.arg("settings-unity-remove:")); i >= 0 && i < len(f.Projects) {
			f.Projects = append(f.Projects[:i], f.Projects[i+1:]...)
			u.saveCfg()
			u.patchMain()
		}
	})
	onPrefix("settings-unity-install:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.Unity
		i := atoiSafe(m.arg("settings-unity-install:"))
		if i < 0 || i >= len(f.Projects) {
			return
		}
		dir := f.Projects[i]
		u.bg(func() {
			if err := unityproj.InstallPlugin(dir); err != nil {
				u.toast(i18n.T("settings.toast.installPluginErr") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.pluginInstalled"))
			u.patchMain()
		})
	})

	// ── Service ──
	onExact("settings-svc-install", func(u *UI, _ actMsg) { u.svcRun(i18n.T("settings.label.install"), service.InstallInteractive) })
	onExact("settings-svc-uninstall", func(u *UI, _ actMsg) { u.svcRun(i18n.T("settings.label.uninstall"), service.UninstallInteractive) })

	// Updates: see update_actions.go (settings-update-check + upd-download/install/restart).
}

// setLanguage persists the chosen UI locale, switches i18n, and re-renders the shell (nav + main)
// so every localized string updates live. Empty code = OS-locale fallback.
// settingsSearchInput stores the query and debounces the content re-render (input events arrive
// per keystroke). Only #set-content is patched so the search box keeps focus while typing.
func (u *UI) settingsSearchInput(q string) {
	u.setMu.Lock()
	u.setQuery = q
	if u.setDebounce != nil {
		u.setDebounce.Stop()
	}
	u.setDebounce = time.AfterFunc(120*time.Millisecond, u.patchSettingsContent)
	u.setMu.Unlock()
}

// patchSettingsContent re-renders the pane below the search box (pills + cards / results).
func (u *UI) patchSettingsContent() {
	if u.activeTab() != "settings" {
		return
	}
	u.fragMu.Lock()
	u.frags = nil // stset-/stnav- nodes replaced - drop the tick dedup cache
	u.fragMu.Unlock()
	u.eval("window.__patch('set-content'," + jsQuote(u.renderSettingsContent()) + ")")
}

func (u *UI) setLanguage(code string) {
	active := i18n.SetLocale(code)
	if u.svc.Cfg != nil {
		u.svc.Cfg.Features.UI.Language = code
		u.saveCfg()
	}
	if u.log != nil {
		u.log.Info("webui", "language set", map[string]any{"requested": code, "active": active})
	}
	u.patchMain()
	u.eval("window.__patch('nav-list'," + jsQuote(u.navListHTML()) + ")")
}

// unityAddProject validates + appends a Unity project folder (shared by Browse + paste paths).
func (u *UI) unityAddProject(path string) {
	p := strings.TrimSpace(path)
	if p == "" {
		return
	}
	if !unityproj.IsUnityProject(p) {
		u.toast(i18n.T("settings.toast.notUnityProject"))
		return
	}
	f := &u.svc.Cfg.Features.Unity
	if !hasStrWeb(f.Projects, p) {
		f.Projects = append(f.Projects, p)
		u.saveCfg()
		u.patchMain()
	}
}

// ── install runner ──

func (u *UI) runInstall(id, label string, fn func(context.Context, func(int64, int64)) error) {
	u.eval("window.__patch('inst-" + id + "'," + jsQuote(progressBar(0, i18n.T("settings.label.downloading"))) + ")")
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		last := -1
		err := fn(ctx, func(done, total int64) {
			if total <= 0 {
				return
			}
			pct := int(float64(done) / float64(total) * 100)
			if pct == last {
				return
			}
			last = pct
			u.eval("window.__patch('inst-" + id + "'," + jsQuote(progressBar(float64(pct)/100, "")) + ")")
		})
		if err != nil {
			u.eval("window.__patch('inst-" + id + "'," + jsQuote(hint("bad", i18n.T("settings.label.installFailed")+err.Error())) + ")")
			u.toast(i18n.T("settings.toast.installFailed", i18n.A{"tool": label}))
			return
		}
		u.eval("window.__patch('inst-" + id + "'," + jsQuote(hint("ok", i18n.T("settings.label.installed"))) + ")")
		u.toast(i18n.T("settings.toast.installedTool", i18n.A{"tool": label}))
		u.refreshProbes() // a tool/DLL just landed - refresh the cache so patchMain shows it (off UI thread)
		u.patchMain()
	})
}

// ── traktor helpers ──

func (u *UI) qmlElevate(action string) {
	u.toast(i18n.T("settings.toast.qmlWorking"))
	u.bg(func() {
		code, err := elevate.RunSelfElevated([]string{"traktor-qml", action})
		switch {
		case err == elevate.ErrDeclined:
			u.toast(i18n.T("settings.toast.elevationDeclined"))
		case err != nil:
			u.toast(i18n.T("settings.toast.qmlFailed", i18n.A{"action": action}) + err.Error())
		case code != 0:
			u.toast(i18n.T("settings.toast.qmlExited", i18n.A{"code": strconv.Itoa(code)}))
		default:
			u.toast(i18n.T("settings.toast.qmlOk", i18n.A{"action": action}))
		}
	})
}

func (u *UI) traktorMap(key string, on bool) {
	tm := u.svc.TraktorMap
	if tm == nil {
		return
	}
	for _, mp := range tm.Available() {
		if mp.Key != key {
			continue
		}
		mp := mp
		u.toast(i18n.T("settings.toast.traktorWorking"))
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var err error
			if on {
				_, err = tm.Activate(ctx, mp)
			} else {
				_, err = tm.Deactivate(mp)
			}
			if err != nil {
				u.toast(err.Error())
				return
			}
			if on {
				u.toast(i18n.T("settings.toast.mapActivated", i18n.A{"name": mp.Display}))
			} else {
				u.toast(i18n.T("settings.toast.mapRemoved", i18n.A{"name": mp.Display}))
			}
			u.patchMain()
		})
		return
	}
}

func (u *UI) svcRun(label string, fn func() error) {
	u.bg(func() {
		if err := fn(); err != nil {
			u.toast(i18n.T("settings.toast.svcFailed", i18n.A{"action": label}) + err.Error())
		} else {
			u.toast(i18n.T("settings.toast.svcSucceeded", i18n.A{"action": label}))
		}
	})
}

// ── modals: OBS remotes ──

func (u *UI) obsRemotesModal() {
	f := &u.svc.Cfg.Features.OBS
	var rows strings.Builder
	if len(f.Remotes) == 0 {
		rows.WriteString(emptyState(i18n.T("settings.empty.obsRemotes")))
	}
	for i, r := range f.Remotes {
		state := "on"
		if !r.Enabled {
			state = "off"
		}
		rows.WriteString(listRow(fmt.Sprintf("%s - %s:%d", r.ResolvedName(), r.Host, r.ResolvedPort()), state,
			btn(i18n.T("settings.label.edit"), "outline", "settings-obsrem-edit:"+strconv.Itoa(i), ""),
			btn(i18n.T("common.delete"), "ghost", "settings-obsrem-del:"+strconv.Itoa(i), "")))
	}
	u.openModal(modal(i18n.T("settings.modal.obsRemotes"), rows.String(),
		btn(i18n.T("settings.label.addRemoteObs"), "primary", "settings-obsrem-add", "")+btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) obsRemoteEditModal(i int) {
	f := &u.svc.Cfg.Features.OBS
	if i < 0 || i >= len(f.Remotes) {
		return
	}
	r := f.Remotes[i]
	checked := ""
	if r.Enabled {
		checked = " checked"
	}
	body := `<form class=set-dlgform data-act=settings-obsrem-save:` + strconv.Itoa(i) + `>` +
		formInput(i18n.T("settings.label.name"), "name", r.Name, "text") +
		formInput(i18n.T("settings.label.hostIp"), "host", r.Host, "text") +
		formInput(i18n.T("settings.body.obs.port"), "port", strconv.Itoa(r.ResolvedPort()), "number") +
		formInput(i18n.T("settings.body.obs.password"), "pass", r.Password, "password") +
		`<label class=row><span class=row-label>` + i18n.T("common.enabledCap") + `</span><span class=switch><input type=checkbox name=enabled` + checked + `><span class=switch-track></span></span></label>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`
	u.openModal(modal(i18n.T("settings.modal.obsRemote"), body, btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
}

// ── modals: OBS media-sync sources ──

func (u *UI) obsSyncModal() {
	f := &u.svc.Cfg.Features.OBS.Sync
	var rows strings.Builder
	if len(f.Sources) == 0 {
		rows.WriteString(emptyState(i18n.T("settings.empty.obsSync")))
	}
	for i, s := range f.Sources {
		ep := s.Endpoint
		if ep == "" {
			ep = i18n.T("settings.label.local")
		}
		state := "on"
		if !s.Enabled {
			state = "off"
		}
		rows.WriteString(listRow(fmt.Sprintf("%s @ %s (%+dms)", or(s.InputName, "?"), ep, s.StaticOffsetMs), state,
			btn(i18n.T("settings.label.edit"), "outline", "settings-obssync-edit:"+strconv.Itoa(i), ""),
			btn(i18n.T("common.delete"), "ghost", "settings-obssync-del:"+strconv.Itoa(i), "")))
	}
	u.openModal(modal(i18n.T("settings.modal.obsSync"), rows.String(),
		btn(i18n.T("settings.label.addMediaSource"), "primary", "settings-obssync-add", "")+btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) obsSyncEditModal(i int) {
	f := &u.svc.Cfg.Features.OBS.Sync
	if i < 0 || i >= len(f.Sources) {
		return
	}
	s := f.Sources[i]
	epOpts := [][2]string{{"", i18n.T("settings.label.localThisPc")}}
	for _, r := range u.svc.Cfg.Features.OBS.Remotes {
		epOpts = append(epOpts, [2]string{r.ResolvedName(), r.ResolvedName()})
	}
	checked := ""
	if s.Enabled {
		checked = " checked"
	}
	body := `<form class=set-dlgform data-act=settings-obssync-save:` + strconv.Itoa(i) + `>` +
		formInput(i18n.T("settings.label.inputName"), "input", s.InputName, "text") +
		formSelect(i18n.T("settings.label.obs"), "endpoint", epOpts, s.Endpoint) +
		formSelect(i18n.T("settings.label.sourceKind"), "kind", [][2]string{{"", i18n.T("settings.label.autoDetect")}, {"ffmpeg_source", "Media Source"}, {"vlc_source", "VLC Video Source"}}, s.InputKind) +
		formInput(i18n.T("settings.label.syncOffsetMs"), "offset", strconv.Itoa(s.StaticOffsetMs), "number") +
		`<label class=row><span class=row-label>` + i18n.T("common.enabledCap") + `</span><span class=switch><input type=checkbox name=enabled` + checked + `><span class=switch-track></span></span></label>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`
	u.openModal(modal(i18n.T("settings.modal.obsSyncOne"), body, btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
}

// ── modals: timecode extra sinks ──

// tcExtraModal opens the extra-sinks editor. Device enumeration (winmm syscalls) never runs on
// actWorker: a warm settingsProbes cache (≤probeTTL stale) serves names directly; on a cold cache
// the modal opens with a loading body and a bg goroutine enumerates + patches it.
func (u *UI) tcExtraModal(kind string) {
	u.probes.mu.Lock()
	ready := u.probes.ready
	u.probes.mu.Unlock()
	if ready || (kind != "ltc" && kind != "mtc") { // art/unknown need no device list
		u.openModal(u.tcExtraModalHTML(kind, u.devNamesCached("waveout"), u.devNamesCached("midiout")))
		return
	}
	u.openModal(modal(tcExtraTitle(kind), `<div id=tcx-wait>`+emptyState(i18n.T("remote.loading"))+`</div>`,
		btn(i18n.T("common.close"), "outline", "modal-close", "")))
	u.maybeRefreshProbes() // warm the shared cache too
	u.bg(func() {
		var waveOut, midiOut []string
		if kind == "ltc" {
			waveOut = mustNames(timecode.WaveOutDevices)
		} else {
			midiOut = mustNames(timecode.MidiOutDevices)
		}
		// patch only while the loading body is still up (user may have closed / replaced the modal)
		u.eval("if(document.getElementById('tcx-wait'))window.__patch('__modal'," + jsQuote(u.tcExtraModalHTML(kind, waveOut, midiOut)) + ")")
	})
}

func tcExtraTitle(kind string) string {
	switch kind {
	case "ltc":
		return i18n.T("settings.modal.tcExtraLtc")
	case "mtc":
		return i18n.T("settings.modal.tcExtraMtc")
	case "art":
		return i18n.T("settings.modal.tcExtraArt")
	}
	return i18n.T("settings.modal.tcExtra")
}

// tcExtraModalHTML renders the full modal from pre-enumerated device names (no syscalls).
func (u *UI) tcExtraModalHTML(kind string, waveOut, midiOut []string) string {
	f := &u.svc.Cfg.Features.Timecode
	var rows strings.Builder
	switch kind {
	case "ltc":
		for i, s := range f.LTCExtra {
			rows.WriteString(`<div class=set-listrow><div class=set-listmain>` +
				tcxToggle("ltc", i, s.On) +
				selectBox(i18n.T("settings.body.audiorec.device"), "settings-tcx-set:ltc:dev:"+strconv.Itoa(i), devOpts(waveOut, i18n.T("settings.body.common.systemDefault"), s.Device), s.Device) +
				field(i18n.T("settings.label.levelDbfs"), "settings-tcx-set:ltc:gain:"+strconv.Itoa(i), trimNum(s.ResolvedGainDb()), "number") +
				`</div><div class=irow-actions>` + btn(i18n.T("common.delete"), "ghost", "settings-tcx-del:ltc:"+strconv.Itoa(i), "") + `</div></div>`)
		}
	case "mtc":
		for i, s := range f.MTCExtra {
			rows.WriteString(`<div class=set-listrow><div class=set-listmain>` +
				tcxToggle("mtc", i, s.On) +
				selectBox(i18n.T("settings.label.midiPort"), "settings-tcx-set:mtc:dev:"+strconv.Itoa(i), devOpts(midiOut, i18n.T("settings.body.timecode.firstPort"), s.Device), s.Device) +
				`</div><div class=irow-actions>` + btn(i18n.T("common.delete"), "ghost", "settings-tcx-del:mtc:"+strconv.Itoa(i), "") + `</div></div>`)
		}
	case "art":
		for i, s := range f.ArtNetExtra {
			rows.WriteString(`<div class=set-listrow><div class=set-listmain>` +
				tcxToggle("art", i, s.On) +
				field(i18n.T("settings.label.targetHostPort"), "settings-tcx-set:art:addr:"+strconv.Itoa(i), s.Addr, "text") +
				`</div><div class=irow-actions>` + btn(i18n.T("common.delete"), "ghost", "settings-tcx-del:art:"+strconv.Itoa(i), "") + `</div></div>`)
		}
	}
	return modal(tcExtraTitle(kind), rows.String(),
		btn(i18n.T("settings.label.addOutput"), "primary", "settings-tcx-add:"+kind, "")+btn(i18n.T("common.close"), "outline", "modal-close", ""))
}

// tcxToggle renders an on/off switch for an extra sink (dispatches settings-tcx-set:<kind>:on:<idx>).
func tcxToggle(kind string, i int, on bool) string {
	return toggleRow(i18n.T("common.enabledCap"), "settings-tcx-set:"+kind+":on:"+strconv.Itoa(i), on)
}

// tcExtraSet applies an extra-sink field change (arg = "<kind>:<field>:<idx>").
func (u *UI) tcExtraSet(arg, val string) {
	parts := strings.Split(arg, ":")
	if len(parts) != 3 {
		return
	}
	kind, fieldName, idx := parts[0], parts[1], atoiSafe(parts[2])
	f := &u.svc.Cfg.Features.Timecode
	b := val == "true"
	switch kind {
	case "ltc":
		if idx < 0 || idx >= len(f.LTCExtra) {
			return
		}
		switch fieldName {
		case "on":
			f.LTCExtra[idx].On = b
		case "dev":
			f.LTCExtra[idx].Device = val
		case "gain":
			if n, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && n <= 0 && n >= -40 {
				f.LTCExtra[idx].GainDb = n
			}
		}
	case "mtc":
		if idx < 0 || idx >= len(f.MTCExtra) {
			return
		}
		switch fieldName {
		case "on":
			f.MTCExtra[idx].On = b
		case "dev":
			f.MTCExtra[idx].Device = val
		}
	case "art":
		if idx < 0 || idx >= len(f.ArtNetExtra) {
			return
		}
		switch fieldName {
		case "on":
			f.ArtNetExtra[idx].On = b
		case "addr":
			f.ArtNetExtra[idx].Addr = strings.TrimSpace(val)
		}
	}
	u.saveCfg()
}

// ── modals: twitch presets ──

func (u *UI) twPresetModal() {
	f := &u.svc.Cfg.Features.Twitch
	var rows strings.Builder
	if len(f.Presets) == 0 {
		rows.WriteString(emptyState(i18n.T("settings.empty.twPresets")))
	}
	for i, p := range f.Presets {
		rows.WriteString(listRow(p.Name, p.Template,
			btn(i18n.T("settings.label.apply"), "go", "settings-twpreset-apply:"+strconv.Itoa(i), ""),
			btn(i18n.T("settings.label.edit"), "outline", "settings-twpreset-edit:"+strconv.Itoa(i), ""),
			btn(i18n.T("common.delete"), "ghost", "settings-twpreset-del:"+strconv.Itoa(i), "")))
	}
	u.openModal(modal(i18n.T("settings.modal.twPresets"), rows.String(),
		btn(i18n.T("settings.label.addPreset"), "primary", "settings-twpreset-add", "")+btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) twPresetEditModal(i int) {
	f := &u.svc.Cfg.Features.Twitch
	if i < 0 || i >= len(f.Presets) {
		return
	}
	p := f.Presets[i]
	body := `<form class=set-dlgform data-act=settings-twpreset-save:` + strconv.Itoa(i) + `>` +
		formInput(i18n.T("settings.label.name"), "name", p.Name, "text") +
		formInput(i18n.T("settings.label.template"), "template", p.Template, "text") +
		formInput(i18n.T("settings.label.categoryOptional"), "game", p.GameName, "text") +
		`<div class=set-note>Use {placeholders} for values you fill in on apply (e.g. {genre}, {club}, {event}).</div>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`
	u.openModal(modal(i18n.T("settings.modal.editPreset"), body, btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
}

// ── modals: VR overlays ──

func (u *UI) vrOverlaysModal() {
	f := &u.svc.Cfg.Features.VROverlay
	var rows strings.Builder
	if len(f.Overlays) == 0 {
		rows.WriteString(emptyState(i18n.T("settings.empty.vrOverlays")))
	}
	for i, o := range f.Overlays {
		state := "on"
		if !o.Enabled {
			state = "off"
		}
		rows.WriteString(listRow(fmt.Sprintf("%s [%s] - %.2fm", o.ID, o.Type, o.ResolvedWidthM()), state,
			btn(i18n.T("settings.label.edit"), "outline", "settings-vrov-edit:"+strconv.Itoa(i), ""),
			btn(i18n.T("common.delete"), "ghost", "settings-vrov-del:"+strconv.Itoa(i), "")))
	}
	u.openModal(modal(i18n.T("settings.card.vroverlay.title"), rows.String(),
		btn(i18n.T("settings.label.addOverlay"), "primary", "settings-vrov-add", "")+btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) vrOverlayEditModal(i int) {
	f := &u.svc.Cfg.Features.VROverlay
	if i < 0 || i >= len(f.Overlays) {
		return
	}
	o := f.Overlays[i]
	body := `<form class=set-dlgform data-act=settings-vrov-save:` + strconv.Itoa(i) + `>` +
		formSelect(i18n.T("settings.label.type"), "type", [][2]string{{"chat", "chat"}, {"alerts", "alerts"}, {"obs", "obs"}, {"viewers", "viewers"}, {"viewerlist", "viewerlist"}, {"perf", "perf"}, {"network", "network"}, {"timing", "timing"}}, or(o.Type, "chat")) +
		formSelect(i18n.T("settings.label.anchor"), "snap", [][2]string{{"", i18n.T("settings.label.anchorWorld")}, {"left", i18n.T("settings.label.anchorLeft")}, {"right", i18n.T("settings.label.anchorRight")}, {"head", i18n.T("settings.label.anchorHead")}}, o.SnapTo) +
		formCheck(i18n.T("common.enabledCap"), "enabled", o.Enabled) +
		formCheck(i18n.T("settings.label.alwaysVisible"), "lock", o.AlwaysShow) +
		formInput(i18n.T("settings.label.xM"), "x", trimNum(o.X), "number") +
		formInput(i18n.T("settings.label.yM"), "y", trimNum(o.Y), "number") +
		formInput(i18n.T("settings.label.zM"), "z", trimNum(o.Z), "number") +
		formInput(i18n.T("settings.label.yawDeg"), "yaw", trimNum(o.Yaw), "number") +
		formInput(i18n.T("settings.label.widthM"), "width", trimNum(o.ResolvedWidthM()), "number") +
		formInput(i18n.T("settings.label.opacity01"), "opacity", trimNum(o.ResolvedOpacity()), "number") +
		formInput(i18n.T("settings.label.maxMessages"), "maxmsg", strconv.Itoa(o.ResolvedMaxMessages()), "number") +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`
	u.openModal(modal(i18n.T("settings.modal.editOverlay"), body, btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
}

// ── small form/dialog helpers ──

// formInput renders a labelled input inside a dialog form (name = form field key).
func formInput(label, name, value, typ string) string {
	if typ == "" {
		typ = "text"
	}
	return `<label class=field><span class=field-label>` + html.EscapeString(label) + `</span>` +
		`<input class=field-input type=` + typ + ` name=` + name + ` value="` + html.EscapeString(value) + `" autocomplete=off></label>`
}

func formSelect(label, name string, opts [][2]string, cur string) string {
	var o strings.Builder
	for _, op := range opts {
		sel := ""
		if op[0] == cur {
			sel = " selected"
		}
		o.WriteString(`<option value="` + html.EscapeString(op[0]) + `"` + sel + `>` + html.EscapeString(op[1]) + `</option>`)
	}
	return `<label class=field><span class=field-label>` + html.EscapeString(label) + `</span>` +
		`<select class="field-input select-input" name=` + name + `>` + o.String() + `</select></label>`
}

func formCheck(label, name string, on bool) string {
	c := ""
	if on {
		c = " checked"
	}
	return `<label class=row><span class=row-label>` + html.EscapeString(label) + `</span>` +
		`<span class=switch><input type=checkbox name=` + name + c + `><span class=switch-track></span></span></label>`
}

// listRow renders a dialog list entry: title + sub on the left, action buttons right.
func listRow(title, sub string, actions ...string) string {
	s := ""
	if sub != "" {
		s = `<div class=set-listsub>` + html.EscapeString(sub) + `</div>`
	}
	return `<div class=set-listrow><div class=set-listmain>` + html.EscapeString(title) + s + `</div>` +
		`<div class=irow-actions>` + strings.Join(actions, "") + `</div></div>`
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

func splitKindIdx(s string) (string, int) {
	kind, idxs, ok := strings.Cut(s, ":")
	if !ok {
		return s, -1
	}
	return kind, atoiSafe(idxs)
}

func hasStrWeb(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func parseFWeb(s string, def float64) float64 {
	if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return n
	}
	return def
}
