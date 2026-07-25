package webui

// Beatgrid model card: active-checkpoint picker + fine-tuning on verified grids.
// Training runs the train.Trainer child (JSONL events); the card shows live epoch
// metrics and the before/after F-measure verdict. A fine-tuned model is only ever
// ACTIVATED by the user picking it - never automatically.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix/train"
	"rave.page/mate/internal/i18n"
)

type gfTrainState struct {
	mu      sync.Mutex
	running bool
	lastEv  train.TrainEvent // latest epoch/start event
	verdict *train.TrainEvent
	err     string
	cancel  context.CancelFunc
}

func (u *UI) gfModelsDir() string {
	dir, err := config.DataPath("gridfix")
	if err != nil {
		return "gridfix"
	}
	return filepath.Join(dir, "models")
}

// gridfixModelState resolves the model/training card (impure: config, the cached env probe +
// checkpoint list, the verified-grid store, the live training state).
func (u *UI) gridfixModelState() gfModelSt {
	verified := 0
	if vs := u.gfVerified(); vs != nil {
		verified = vs.Count()
	}
	st, ready := u.gridfixStatusCached()
	t := &u.gfTrain
	t.mu.Lock()
	running, lastEv, verdict, terr := t.running, t.lastEv, t.verdict, t.err
	t.mu.Unlock()
	// opts() runs on EVERY render (per keystroke in Settings search mode) - read the cached checkpoint
	// list (populated by the gridfix probe, u.bg), never ReadDir here.
	sel := u.gridfixModelSel(u.svc.Cfg.Features.GridFix.ActiveModel)
	return gridfixModelStateOf(sel, ready && (st.CPU.EngineOK || st.CUDA.EngineOK), verified,
		running, lastEv, verdict, terr)
}

// gridfixModelSel registers + resolves the active-model picker (builtin + fine-tuned checkpoints).
// cur is the CURRENT LABEL, not the value - the Go original passed the label to smartSelect and
// the resolver's value match then never fires; kept as-is (the label is what the button shows).
func (u *UI) gridfixModelSel(active string) selState {
	curLabel := i18n.T("settings.body.gridfixmodel.builtin")
	if active != "" {
		curLabel = filepath.Base(active)
	}
	s := resolveSmartSelect("gfmodel", "gfm-model", curLabel, func() []ssOpt {
		opts := []ssOpt{{Val: "", Label: i18n.T("settings.body.gridfixmodel.builtin"), Sub: "final0"}}
		for _, c := range u.gfCheckpointsCached() {
			opts = append(opts, ssOpt{Val: c.Path, Label: c.Name, Sub: c.At.Format("2006-01-02 15:04")})
		}
		return opts
	})
	s.Label = i18n.T("settings.body.gridfixmodel.active")
	return s
}

// gridfixModelStateOf maps the resolved picker + training facts to render state (all numbers
// formatted here - the Zig path never sees a float).
func gridfixModelStateOf(sel selState, engineOK bool, verified int, running bool,
	lastEv train.TrainEvent, verdict *train.TrainEvent, terr string) gfModelSt {
	s := gfModelSt{
		Sel:     sel,
		Dataset: i18n.T("settings.body.gridfixmodel.dataset", i18n.A{"n": fmt.Sprint(verified)}),
		Running: running,
		Note:    i18n.T("settings.body.gridfixmodel.note"),
	}
	if running {
		line := i18n.T("settings.body.gridfixmodel.preparing")
		if lastEv.Kind == "epoch" {
			line = fmt.Sprintf("%s %d — loss %.4f · F %.3f", i18n.T("settings.body.gridfixmodel.epoch"), lastEv.Epoch, lastEv.Loss, lastEv.ValFBeat)
		} else if lastEv.Kind == "start" {
			line = i18n.T("settings.body.gridfixmodel.started", i18n.A{"n": fmt.Sprint(lastEv.Tracks), "device": lastEv.Device})
		}
		s.BarPct, s.BarCap = progressPct(0), line
		s.Cancel = nbtn(i18n.T("library.gf.stop"), "outline", "gfm-cancel", "")
		return s
	}
	if verdict != nil {
		s.HasVerdict, s.VerdictTone = true, "ok"
		s.Verdict = i18n.T("settings.body.gridfixmodel.improved",
			i18n.A{"before": fmt.Sprintf("%.3f", verdict.BeforeF), "after": fmt.Sprintf("%.3f", verdict.AfterF)})
		if !verdict.Improved {
			s.VerdictTone = "bad"
			s.Verdict = i18n.T("settings.body.gridfixmodel.notImproved",
				i18n.A{"before": fmt.Sprintf("%.3f", verdict.BeforeF), "after": fmt.Sprintf("%.3f", verdict.AfterF)})
		}
	}
	s.Err = terr
	if engineOK && verified >= 2 {
		s.CanTrain = true
		s.Train = nbtn(i18n.T("settings.body.gridfixmodel.train"), "primary", "gfm-train", "")
	}
	if verified < 20 {
		s.Few, s.FewHint = true, i18n.T("settings.body.gridfixmodel.fewHint")
	}
	return s
}

func init() {
	onExact("gfm-model", func(u *UI, m actMsg) {
		u.svc.Cfg.Features.GridFix.ActiveModel = m.Val
		u.saveCfg()
		u.patchMain()
	})
	onExact("gfm-train", func(u *UI, _ actMsg) { u.gfmTrain() })
	onExact("gfm-cancel", func(u *UI, _ actMsg) {
		t := &u.gfTrain
		t.mu.Lock()
		c := t.cancel
		t.mu.Unlock()
		if c != nil {
			c()
		}
	})
}

// gfmTrain exports the manifest from verified grids and runs the fine-tune child.
func (u *UI) gfmTrain() {
	vs := u.gfVerified()
	if vs == nil {
		return
	}
	mgr := u.gridfixEnvMgr()
	py, dev := u.gridfixEngine()
	if py == "" {
		u.toast(i18n.T("library.gf.noEngineHint"))
		return
	}
	t := &u.gfTrain
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.running, t.err, t.verdict, t.lastEv, t.cancel = true, "", nil, train.TrainEvent{}, cancel
	t.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		defer cancel()
		fail := func(msg string) {
			t.mu.Lock()
			t.running, t.err, t.cancel = false, msg, nil
			t.mu.Unlock()
			u.toast(msg)
			u.patchMain()
		}
		manifest, skipped, err := train.BuildManifest(vs.All(), u.gfModelsDir(), train.TrainOptions{
			BaseCheckpoint: baseOr(u.svc.Cfg.Features.GridFix.ActiveModel, "final0")})
		if err != nil {
			fail(i18n.T("settings.toast.gfTrainFailed") + err.Error())
			return
		}
		if skipped > 0 {
			u.toast(i18n.T("settings.toast.gfTrainSkipped", i18n.A{"n": fmt.Sprint(skipped)}))
		}
		tr := &train.Trainer{Device: dev,
			OnLog: func(line string) {
				if u.log != nil {
					u.log.Debug("gridfix-train", line, nil)
				}
			}}
		var lastPatch time.Time
		err = tr.Start(ctx, py, mgr.DataDir, manifest, func(ev train.TrainEvent) {
			t.mu.Lock()
			t.lastEv = ev
			if ev.Kind == "done" {
				v := ev
				t.verdict = &v
			}
			t.mu.Unlock()
			if time.Since(lastPatch) > time.Second {
				lastPatch = time.Now()
				line := fmt.Sprintf("%s %d — loss %.4f · F %.3f", i18n.T("settings.body.gridfixmodel.epoch"), ev.Epoch, ev.Loss, ev.ValFBeat)
				u.eval("window.__patch('gfm-live'," + jsQuote(progressBar(0, line)) + ")")
			}
		})
		t.mu.Lock()
		t.running, t.cancel = false, nil
		v := t.verdict
		if err != nil && t.err == "" {
			t.err = err.Error()
		}
		t.mu.Unlock()
		if err != nil {
			u.toast(i18n.T("settings.toast.gfTrainFailed") + err.Error())
		} else if v != nil {
			u.Notify(i18n.T("settings.toast.gfTrainDoneTitle"),
				i18n.T("settings.toast.gfTrainDoneBody",
					i18n.A{"before": fmt.Sprintf("%.3f", v.BeforeF), "after": fmt.Sprintf("%.3f", v.AfterF)}))
		}
		// a fine-tune just wrote a new checkpoint - re-list into the probe cache (still off-thread here)
		// so the picker shows it without waiting for the env-probe TTL.
		cps := train.ListCheckpoints(u.gfModelsDir())
		u.gfProbe.mu.Lock()
		u.gfProbe.checkpoints = cps
		u.gfProbe.mu.Unlock()
		u.patchMain()
	})
}

func baseOr(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
