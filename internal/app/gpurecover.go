package app

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gpuwatch"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/selfupdate"
)

// GPU-fault recovery policy. A confirmed UI hang triggers a clean relaunch (the in-daemon GL
// context is unrecoverable in-proc), guarded against a restart storm: at most gpuMaxRestarts
// relaunches within gpuRestartWindow, else auto-recovery pauses and the user is told to fix the
// driver. A logged TDR alone does NOT restart (many are transparently recovered) - it's surfaced
// and notified to any in-daemon GPU consumers so they can reinit in place.
const (
	gpuRestartWindow = 5 * time.Minute
	gpuMaxRestarts   = 3
	gpuHistoryFile   = "gpu-restart-history.json"
	gpuHardKillAfter = 8 * time.Second // backstop: force exit if graceful shutdown wedges on the hung UI
)

// gpuRecovery turns detected GPU faults into recovery actions.
type gpuRecovery struct {
	log    *logbus.Bus
	notify func(title, body string) // desktop toast (nil-safe)
	quit   func()                   // graceful shutdown (ctx cancel)

	// Injectable for tests; zero values = production defaults (selfupdate.Relaunch + delayed
	// os.Exit, history under config.DataPath).
	relaunch         func() error
	scheduleHardExit func()
	historyPath      string

	mu         sync.Mutex
	onReset    []func(detail string) // in-daemon consumers (VR overlays) to reinit on a TDR
	restarting bool
}

func (g *gpuRecovery) doRelaunch() error {
	if g.relaunch != nil {
		return g.relaunch()
	}
	return selfupdate.Relaunch()
}

// hardExit schedules a forced process exit as a backstop: if the wedged UI thread stalls the
// graceful shutdown, the new instance (already booting, waiting out our single-instance lock)
// must still be able to bind - the lock releases on exit.
func (g *gpuRecovery) hardExit() {
	if g.scheduleHardExit != nil {
		g.scheduleHardExit()
		return
	}
	go func() {
		time.Sleep(gpuHardKillAfter)
		g.log.Warn("gpuwatch", "graceful shutdown timed out - forcing exit", nil)
		os.Exit(0)
	}()
}

// OnGPUReset registers a callback fired when the OS logs a display-driver reset - used by
// in-daemon GPU consumers (VR overlays) to drop + rebuild their surfaces in place.
func (g *gpuRecovery) OnGPUReset(fn func(detail string)) {
	g.mu.Lock()
	g.onReset = append(g.onReset, fn)
	g.mu.Unlock()
}

// onFault is the gpuwatch.OnFault sink.
func (g *gpuRecovery) onFault(f gpuwatch.Fault) {
	switch f.Kind {
	case gpuwatch.FaultTDR:
		g.log.Warn("gpuwatch", "display-driver reset detected (TDR)", map[string]any{"detail": f.Detail})
		g.toast("GPU driver reset", "rave-mate detected a driver timeout ("+f.Detail+") - watching for a hang")
		g.mu.Lock()
		cbs := append([]func(string){}, g.onReset...)
		g.mu.Unlock()
		for _, fn := range cbs {
			fn(f.Detail)
		}
	case gpuwatch.FaultHungWindow:
		g.log.Error("gpuwatch", "UI wedged by GPU fault - recovering", map[string]any{"detail": f.Detail})
		g.recover(f)
	}
}

// recover relaunches a fresh instance and tears this one down, unless the restart budget is spent.
func (g *gpuRecovery) recover(f gpuwatch.Fault) {
	g.mu.Lock()
	if g.restarting {
		g.mu.Unlock()
		return
	}
	g.restarting = true
	g.mu.Unlock()

	path := g.histPath()
	hist := pruneHistory(loadRestartHistory(path), gpuRestartWindow)
	if len(hist) >= gpuMaxRestarts {
		g.log.Error("gpuwatch", "auto-recovery paused: too many GPU restarts", map[string]any{"restarts": len(hist), "window": gpuRestartWindow.String()})
		g.toast("GPU unrecoverable", "rave-mate keeps failing after driver crashes - update your GPU driver or reboot. Auto-restart paused.")
		g.mu.Lock()
		g.restarting = false
		g.mu.Unlock()
		return
	}
	saveRestartHistory(path, append(hist, time.Now()))

	g.log.Error("gpuwatch", "relaunching rave-mate", map[string]any{"kind": string(f.Kind), "detail": f.Detail})
	g.toast("Recovering", "GPU fault detected - restarting rave-mate…")
	if err := g.doRelaunch(); err != nil {
		g.log.Error("gpuwatch", "relaunch failed", map[string]any{"error": err.Error()})
		g.toast("Restart failed", "Couldn't relaunch rave-mate - please restart it manually.")
		g.mu.Lock()
		g.restarting = false
		g.mu.Unlock()
		return
	}
	g.hardExit()
	if g.quit != nil {
		g.quit()
	}
}

func (g *gpuRecovery) toast(title, body string) {
	if g.notify != nil {
		g.notify(title, body)
	}
}

// ── restart-history persistence (survives the relaunch, so the budget spans instances) ──

// histPath resolves the restart-history file (test override, else config.DataPath).
func (g *gpuRecovery) histPath() string {
	if g.historyPath != "" {
		return g.historyPath
	}
	p, _ := config.DataPath(gpuHistoryFile)
	return p
}

func loadRestartHistory(p string) []time.Time {
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var unix []int64
	if json.Unmarshal(b, &unix) != nil {
		return nil
	}
	out := make([]time.Time, 0, len(unix))
	for _, u := range unix {
		out = append(out, time.Unix(u, 0))
	}
	return out
}

func saveRestartHistory(p string, h []time.Time) {
	if p == "" {
		return
	}
	unix := make([]int64, 0, len(h))
	for _, t := range h {
		unix = append(unix, t.Unix())
	}
	if b, err := json.Marshal(unix); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

// pruneHistory drops entries older than window.
func pruneHistory(h []time.Time, window time.Duration) []time.Time {
	cutoff := time.Now().Add(-window)
	out := h[:0]
	for _, t := range h {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
