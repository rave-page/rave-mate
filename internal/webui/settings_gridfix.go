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
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/gridfix/train"
	"rave.page/mate/internal/i18n"
)

const gridfixProbeTTL = 5 * time.Minute

type gridfixProbe struct {
	mu          sync.Mutex
	st          gridfix.EnvStatus
	checkpoints []train.CheckpointInfo // fine-tune checkpoints (ReadDir) - folded in so the model-card picker never scans on render
	at          time.Time
	ready       bool
	busy        bool
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
	cps := train.ListCheckpoints(u.gfModelsDir()) // fold the checkpoint scan (ReadDir) into the probe - off the render goroutine
	u.gfProbe.mu.Lock()
	changed := !u.gfProbe.ready || st != u.gfProbe.st
	u.gfProbe.st = st
	u.gfProbe.checkpoints = cps
	u.gfProbe.at = time.Now()
	u.gfProbe.ready = true
	u.gfProbe.busy = false
	u.gfProbe.mu.Unlock()
	// Re-render the surface that shows engine state when the async probe lands: settings (install
	// card) AND library (the Collection cockpit health rail) - else the rail is stuck on its
	// pre-probe "checking…" state until an unrelated re-render.
	if changed {
		switch u.activeTab() {
		case "settings", "library":
			u.patchMain()
		}
	}
}

func (u *UI) invalidateGridfixProbe() {
	u.gfProbe.mu.Lock()
	u.gfProbe.at = time.Time{}
	u.gfProbe.mu.Unlock()
}

// gfCheckpointsCached returns the cached fine-tune checkpoints (nil until the first probe lands).
// Read on the render goroutine - the ReadDir scan runs in refreshGridfixProbe (u.bg).
func (u *UI) gfCheckpointsCached() []train.CheckpointInfo {
	u.gfProbe.mu.Lock()
	defer u.gfProbe.mu.Unlock()
	return u.gfProbe.checkpoints
}

// gridfixVariantState resolves one engine variant's status line + install/remove actions
// + its progress target. key ∈ {"cpu","cuda"}. Pure renderer: gfVarHTML.
func gridfixVariantState(key string, v gridfix.VariantStatus, gpuPresent bool) gfVarSt {
	esc := html.EscapeString
	name := i18n.T("settings.body.gridfix." + key + "Name")
	out := gfVarSt{Key: key, Btns: []gfBtn{}}
	switch {
	case v.EngineOK:
		ver := ""
		if v.Versions != nil {
			ver = " (beat-this " + v.Versions.BeatThis + ", torch " + v.Versions.Torch + ")"
		}
		// esc(ver) here + hint()'s own escaping = the original's double escape; kept for parity
		out.Tone = "ok"
		out.Line = i18n.T("settings.body.gridfix.variantReady", i18n.A{"name": name}) + esc(ver)
	case v.Python != "":
		out.Tone, out.Line = "bad", i18n.T("settings.body.gridfix.variantBroken", i18n.A{"name": name})
	default:
		out.Line = i18n.T("settings.body.gridfix.variantMissing", i18n.A{"name": name})
	}
	installLabel := i18n.T("settings.body.gridfix.installCpu")
	if key == "cuda" {
		installLabel = i18n.T("settings.body.gridfix.installCuda")
	}
	if !v.EngineOK {
		if key == "cuda" && !gpuPresent {
			// gated, never hidden: name what's missing instead of failing later
			out.Btns = append(out.Btns, gfBtn{Label: installLabel, Gate: i18n.T("settings.body.gridfix.noGpu")})
		} else {
			out.Btns = append(out.Btns, gfBtn{Label: installLabel, Variant: "primary", Act: "gridfix-install:" + key})
		}
	}
	if v.Python != "" {
		out.Btns = append(out.Btns, gfBtn{Label: i18n.T("settings.body.gridfix.remove", i18n.A{"name": name}),
			Act: "gridfix-uninstall:" + key})
	}
	if key == "cuda" && !v.EngineOK && gpuPresent {
		out.HasNote, out.Note = true, i18n.T("settings.body.gridfix.cudaHint")
	}
	return out
}

// gridfixCardState resolves the engine state + install/uninstall controls + knobs (impure:
// config + the cached env probe, whose refresh this call may kick).
func (u *UI) gridfixCardState() gfCardSt {
	st, ready := u.gridfixStatusCached()
	return gridfixCardStateOf(&u.svc.Cfg.Features.GridFix, st, ready)
}

// gridfixCardStateOf maps config + probe result to render state (i18n + number formatting only).
func gridfixCardStateOf(f *config.GridFixFeature, st gridfix.EnvStatus, ready bool) gfCardSt {
	s := gfCardSt{Vars: []gfVarSt{}}
	switch {
	case !ready:
		s.LeadKind, s.Lead = "hint", i18n.T("settings.body.gridfix.probing")
	case st.BasePython == "":
		s.LeadKind, s.LeadTone, s.Lead = "hint", "bad", i18n.T("settings.body.gridfix.noPython")
	case st.CPU.Python == "" && st.CUDA.Python == "":
		s.LeadKind, s.Lead = "note", i18n.T("settings.body.gridfix.notInstalled", i18n.A{"version": st.BaseVersion})
	}
	if ready && st.BasePython != "" {
		s.Vars = append(s.Vars,
			gridfixVariantState("cpu", st.CPU, st.GPUPresent),
			gridfixVariantState("cuda", st.CUDA, st.GPUPresent))
	}
	s.Recheck = nbtn(i18n.T("settings.body.gridfix.recheck"), "", "gridfix-recheck", "")
	// engine preference - honored at run time; auto = CUDA if installed+working else CPU
	s.Engine = resolveSelectBox(i18n.T("settings.body.gridfix.enginePref"), "set:gridfix-device",
		[][2]string{
			{"auto", i18n.T("settings.body.gridfix.engineAuto")},
			{"cpu", i18n.T("settings.body.gridfix.engineCpu")},
			{"cuda", i18n.T("settings.body.gridfix.engineCuda")},
		}, f.ResolvedDevice())
	// path row (Go pathField): text input + the pick-file:<act> Browse button
	s.Python = newField(i18n.T("settings.body.gridfix.pythonPath"), "set:gridfix-python", f.PythonPath, "text")
	s.Browse = nbtn("Browse…", "ghost", "pick-file:set:gridfix-python", "")
	s.MinQ = newField(i18n.T("settings.body.gridfix.minQuality"), "set:gridfix-minq",
		strconv.FormatFloat(f.ResolvedMinQuality(), 'f', -1, 64), "number")
	s.Thresh = newField(i18n.T("settings.body.gridfix.thresholdMs"), "set:gridfix-thresh",
		strconv.FormatFloat(f.ResolvedThresholdMS(), 'f', -1, 64), "number")
	s.Lock = newToggle(i18n.T("settings.body.gridfix.lockFixed"), "set:gridfix-lock", f.LockFixed)
	if len(f.BiasExt) > 0 {
		s.HasCal = true
		s.Cal = i18n.T("settings.body.gridfix.calibrated", i18n.A{"vals": gfBiasSummary(f.BiasExt)})
	}
	s.CalNote = i18n.T("settings.body.gridfix.calNote")
	s.Note = i18n.T("settings.body.gridfix.note")
	return s
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
