package webui

// Beatgrid-fixer settings card: probe + one-click install of the managed Python
// beat_this engine (internal/gridfix.EnvManager). The env probe spawns Python, so it
// gets its own long-TTL cache instead of the 10s settingsProbes cycle.

import (
	"context"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/i18n"
)

const gridfixProbeTTL = 5 * time.Minute

type gridfixProbe struct {
	mu    sync.Mutex
	st    gridfix.EnvStatus
	at    time.Time
	ready bool
	busy  bool
}

func (u *UI) gridfixEnvMgr() *gridfix.EnvManager {
	dir, err := config.DataPath("gridfix")
	if err != nil {
		dir = "gridfix"
	}
	return &gridfix.EnvManager{DataDir: dir, PythonPath: u.svc.Cfg.Features.GridFix.PythonPath}
}

// gridfixStatusCached returns the last env probe and kicks a background refresh when
// stale. Probing spawns Python (~seconds), hence the long TTL + explicit invalidation.
func (u *UI) gridfixStatusCached() (gridfix.EnvStatus, bool) {
	u.gfProbe.mu.Lock()
	st, ready := u.gfProbe.st, u.gfProbe.ready
	stale := !ready || time.Since(u.gfProbe.at) > gridfixProbeTTL
	kick := stale && !u.gfProbe.busy
	if kick {
		u.gfProbe.busy = true
	}
	u.gfProbe.mu.Unlock()
	if kick {
		u.bg(u.refreshGridfixProbe)
	}
	return st, ready
}

func (u *UI) refreshGridfixProbe() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := u.gridfixEnvMgr().Status(ctx)
	u.gfProbe.mu.Lock()
	changed := !u.gfProbe.ready || st != u.gfProbe.st
	u.gfProbe.st = st
	u.gfProbe.at = time.Now()
	u.gfProbe.ready = true
	u.gfProbe.busy = false
	u.gfProbe.mu.Unlock()
	if changed && u.activeTab() == "settings" {
		u.patchMain()
	}
}

func (u *UI) invalidateGridfixProbe() {
	u.gfProbe.mu.Lock()
	u.gfProbe.at = time.Time{}
	u.gfProbe.mu.Unlock()
}

// gridfixCardBody renders the engine state + install/uninstall controls + knobs.
func (u *UI) gridfixCardBody() string {
	f := &u.svc.Cfg.Features.GridFix
	st, ready := u.gridfixStatusCached()
	var b strings.Builder
	esc := html.EscapeString

	var envLine string
	switch {
	case !ready:
		envLine = hint("", i18n.T("settings.body.gridfix.probing"))
	case st.EngineOK:
		v := ""
		if st.Versions != nil {
			v = " (beat-this " + st.Versions.BeatThis + ", torch " + st.Versions.Torch + ")"
		}
		envLine = hint("ok", i18n.T("settings.body.gridfix.engineReady")+esc(v))
	case st.EnvPython != "":
		envLine = hint("bad", i18n.T("settings.body.gridfix.engineBroken"))
	case st.BasePython == "":
		envLine = hint("bad", i18n.T("settings.body.gridfix.noPython"))
	default:
		envLine = hint("", i18n.T("settings.body.gridfix.notInstalled", i18n.A{"version": st.BaseVersion}))
	}
	b.WriteString(envLine)

	// install / uninstall row + streamed progress target
	var buttons string
	if ready && st.BasePython != "" && !st.EngineOK {
		buttons += btn(i18n.T("settings.body.gridfix.install"), "primary", "gridfix-install", "")
	}
	if ready && st.EnvPython != "" {
		buttons += btn(i18n.T("settings.body.gridfix.uninstall"), "", "gridfix-uninstall", "")
	}
	buttons += btn(i18n.T("settings.body.gridfix.recheck"), "", "gridfix-recheck", "")
	b.WriteString(btnRow(buttons))
	b.WriteString(`<div id=inst-gridfix></div>`)

	b.WriteString(pathField(i18n.T("settings.body.gridfix.pythonPath"), "set:gridfix-python", f.PythonPath, "file"))
	b.WriteString(toggleRow(i18n.T("settings.body.gridfix.cuda"), "set:gridfix-cuda", f.CUDA))
	b.WriteString(field(i18n.T("settings.body.gridfix.minQuality"), "set:gridfix-minq",
		strconv.FormatFloat(f.ResolvedMinQuality(), 'f', -1, 64), "number"))
	b.WriteString(field(i18n.T("settings.body.gridfix.thresholdMs"), "set:gridfix-thresh",
		strconv.FormatFloat(f.ResolvedThresholdMS(), 'f', -1, 64), "number"))
	b.WriteString(toggleRow(i18n.T("settings.body.gridfix.lockFixed"), "set:gridfix-lock", f.LockFixed))
	b.WriteString(`<div class=set-note>` + esc(i18n.T("settings.body.gridfix.note")) + `</div>`)
	return b.String()
}

func init() {
	// one-click engine install: venv + pinned beat-this + torch, pip lines streamed
	// into the card (#inst-gridfix)
	onExact("gridfix-install", func(u *UI, _ actMsg) {
		f := u.svc.Cfg.Features.GridFix
		patch := func(inner string) { u.eval("window.__patch('inst-gridfix'," + jsQuote(inner) + ")") }
		patch(progressBar(0, i18n.T("settings.body.gridfix.installing")))
		u.bg(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()
			var lastPatch time.Time
			err := u.gridfixEnvMgr().Install(ctx, f.CUDA, func(line string) {
				if time.Since(lastPatch) < 300*time.Millisecond {
					return // pip is chatty - throttle DOM patches
				}
				lastPatch = time.Now()
				if len(line) > 120 {
					line = line[:120] + "…"
				}
				patch(progressBar(0, line))
			})
			if err != nil {
				patch(hint("bad", i18n.T("settings.label.installFailed")+err.Error()))
				u.toast(i18n.T("settings.toast.installFailed", i18n.A{"tool": "Beat This!"}))
				return
			}
			patch(hint("ok", i18n.T("settings.label.installed")))
			u.toast(i18n.T("settings.toast.installedTool", i18n.A{"tool": "Beat This!"}))
			u.invalidateGridfixProbe()
			u.refreshGridfixProbe()
		})
	})

	onExact("gridfix-uninstall", func(u *UI, _ actMsg) {
		u.bg(func() {
			if err := u.gridfixEnvMgr().Uninstall(); err != nil {
				u.toast(i18n.T("settings.toast.gridfixUninstallFailed") + err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.gridfixUninstalled"))
			u.invalidateGridfixProbe()
			u.refreshGridfixProbe()
		})
	})

	onExact("gridfix-recheck", func(u *UI, _ actMsg) {
		u.invalidateGridfixProbe()
		u.bg(u.refreshGridfixProbe)
	})
}
