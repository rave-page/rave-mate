package webui

// Beatgrid-fixer settings card: probe + one-click install of the managed Python
// beat_this engine (internal/gridfix.EnvManager). CPU and CUDA engines are independent
// installs (any order, both may coexist); an engine-preference select (auto/CPU/CUDA)
// picks which one runs. The env probe spawns Python, so it gets its own long-TTL cache
// instead of the 10s settingsProbes cycle.

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

// gridfixEngine resolves interpreter + inference device per the engine preference
// (auto = CUDA when installed+working, else CPU). Prefers the cached probe; before the
// first probe lands it falls back to fs existence (device "auto" lets torch decide).
func (u *UI) gridfixEngine() (py, device string) {
	pref := u.svc.Cfg.Features.GridFix.ResolvedDevice()
	if st, ready := u.gridfixStatusCached(); ready {
		return st.SelectEngine(pref)
	}
	mgr := u.gridfixEnvMgr()
	if p := mgr.EnvPython(true); p != "" && pref != "cpu" {
		return p, "cuda"
	}
	if p := mgr.EnvPython(false); p != "" {
		return p, "auto" // legacy env may carry CUDA torch - let torch decide
	}
	if p := mgr.EnvPython(true); p != "" {
		return p, "cpu"
	}
	return "", ""
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

// gridfixVariantHTML renders one engine variant's status line + install/remove buttons
// + its progress target. key ∈ {"cpu","cuda"}.
func (u *UI) gridfixVariantHTML(key string, v gridfix.VariantStatus, gpuPresent bool) string {
	esc := html.EscapeString
	name := i18n.T("settings.body.gridfix." + key + "Name")
	var line string
	switch {
	case v.EngineOK:
		ver := ""
		if v.Versions != nil {
			ver = " (beat-this " + v.Versions.BeatThis + ", torch " + v.Versions.Torch + ")"
		}
		line = hint("ok", i18n.T("settings.body.gridfix.variantReady", i18n.A{"name": name})+esc(ver))
	case v.Python != "":
		line = hint("bad", i18n.T("settings.body.gridfix.variantBroken", i18n.A{"name": name}))
	default:
		line = hint("", i18n.T("settings.body.gridfix.variantMissing", i18n.A{"name": name}))
	}
	var buttons string
	installLabel := i18n.T("settings.body.gridfix.installCpu")
	if key == "cuda" {
		installLabel = i18n.T("settings.body.gridfix.installCuda")
	}
	if !v.EngineOK {
		if key == "cuda" && !gpuPresent {
			// gated, never hidden: name what's missing instead of failing later
			buttons += btnGated(installLabel, i18n.T("settings.body.gridfix.noGpu"))
		} else {
			buttons += btn(installLabel, "primary", "gridfix-install:"+key, "")
		}
	}
	if v.Python != "" {
		buttons += btn(i18n.T("settings.body.gridfix.remove", i18n.A{"name": name}), "", "gridfix-uninstall:"+key, "")
	}
	out := line
	if buttons != "" {
		out += btnRow(buttons)
	}
	if key == "cuda" && !v.EngineOK && gpuPresent {
		out += `<div class=set-note>` + esc(i18n.T("settings.body.gridfix.cudaHint")) + `</div>`
	}
	return out + `<div id=inst-gridfix-` + key + `></div>`
}

// gridfixCardBody renders the engine state + install/uninstall controls + knobs.
func (u *UI) gridfixCardBody() string {
	f := &u.svc.Cfg.Features.GridFix
	st, ready := u.gridfixStatusCached()
	var b strings.Builder
	esc := html.EscapeString

	switch {
	case !ready:
		b.WriteString(hint("", i18n.T("settings.body.gridfix.probing")))
	case st.BasePython == "":
		b.WriteString(hint("bad", i18n.T("settings.body.gridfix.noPython")))
	case st.CPU.Python == "" && st.CUDA.Python == "":
		b.WriteString(`<div class=set-note>` + esc(i18n.T("settings.body.gridfix.notInstalled", i18n.A{"version": st.BaseVersion})) + `</div>`)
	}

	if ready && st.BasePython != "" {
		b.WriteString(u.gridfixVariantHTML("cpu", st.CPU, st.GPUPresent))
		b.WriteString(u.gridfixVariantHTML("cuda", st.CUDA, st.GPUPresent))
	}
	b.WriteString(btnRow(btn(i18n.T("settings.body.gridfix.recheck"), "", "gridfix-recheck", "")))

	// engine preference - honored at run time; auto = CUDA if installed+working else CPU
	b.WriteString(selectBox(i18n.T("settings.body.gridfix.enginePref"), "set:gridfix-device",
		[][2]string{
			{"auto", i18n.T("settings.body.gridfix.engineAuto")},
			{"cpu", i18n.T("settings.body.gridfix.engineCpu")},
			{"cuda", i18n.T("settings.body.gridfix.engineCuda")},
		}, f.ResolvedDevice()))

	b.WriteString(pathField(i18n.T("settings.body.gridfix.pythonPath"), "set:gridfix-python", f.PythonPath, "file"))
	b.WriteString(field(i18n.T("settings.body.gridfix.minQuality"), "set:gridfix-minq",
		strconv.FormatFloat(f.ResolvedMinQuality(), 'f', -1, 64), "number"))
	b.WriteString(field(i18n.T("settings.body.gridfix.thresholdMs"), "set:gridfix-thresh",
		strconv.FormatFloat(f.ResolvedThresholdMS(), 'f', -1, 64), "number"))
	b.WriteString(toggleRow(i18n.T("settings.body.gridfix.lockFixed"), "set:gridfix-lock", f.LockFixed))
	if len(f.BiasExt) > 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("settings.body.gridfix.calibrated", i18n.A{"vals": gfBiasSummary(f.BiasExt)})) + `</div>`)
	}
	b.WriteString(`<div class=set-note>` + esc(i18n.T("settings.body.gridfix.calNote")) + `</div>`)
	b.WriteString(`<div class=set-note>` + esc(i18n.T("settings.body.gridfix.note")) + `</div>`)
	return b.String()
}

// gridfixInstall runs one variant's engine install, pip lines streamed into the card
// (#inst-gridfix-<key>).
func (u *UI) gridfixInstall(key string) {
	cuda := key == "cuda"
	patch := func(inner string) { u.eval("window.__patch('inst-gridfix-" + key + "'," + jsQuote(inner) + ")") }
	patch(progressBar(0, i18n.T("settings.body.gridfix.installing")))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		var lastPatch time.Time
		err := u.gridfixEnvMgr().Install(ctx, cuda, func(line string) {
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
}

func init() {
	onPrefix("gridfix-install:", func(u *UI, m actMsg) {
		if key := m.arg("gridfix-install:"); key == "cpu" || key == "cuda" {
			u.gridfixInstall(key)
		}
	})

	onPrefix("gridfix-uninstall:", func(u *UI, m actMsg) {
		key := m.arg("gridfix-uninstall:")
		st, ready := u.gridfixStatusCached()
		if !ready {
			return
		}
		root := st.CPU.Root
		if key == "cuda" {
			root = st.CUDA.Root // legacy single-env CUDA installs report their root here too
		}
		u.bg(func() {
			if err := u.gridfixEnvMgr().Uninstall(root); err != nil {
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
