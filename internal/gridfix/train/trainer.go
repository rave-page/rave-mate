// Package train is the gridfix model fine-tuning backend: DJ-verified grids ->
// manifest -> trainer.py child -> fine-tuned Beat This checkpoint the engine can
// load via its Checkpoint field.
package train

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"rave.page/mate/internal/sysexec"
)

//go:embed trainer.py
var trainerScript []byte

// TrainEvent is one decoded JSONL progress event from trainer.py.
type TrainEvent struct {
	Kind       string // "start" | "epoch" | "done" | "error"
	Tracks     int    // start
	Device     string // start
	Epoch      int    // epoch
	Loss       float64
	ValFBeat   float64
	ValFDown   float64
	BeforeF    float64 // done: val F-beat before fine-tune
	AfterF     float64 // done: val F-beat after
	Checkpoint string  // done: written .ckpt path
	Msg        string  // error
	Improved   bool    // done
}

// wireEvent mirrors trainer.py's stdout JSON.
type wireEvent struct {
	Ev         string  `json:"ev"`
	Tracks     int     `json:"tracks"`
	Device     string  `json:"device"`
	N          int     `json:"n"`
	Loss       float64 `json:"loss"`
	ValFBeat   float64 `json:"valFBeat"`
	ValFDown   float64 `json:"valFDown"`
	Checkpoint string  `json:"checkpoint"`
	Msg        string  `json:"msg"`
	Report     *struct {
		BeforeFBeat float64 `json:"beforeFBeat"`
		AfterFBeat  float64 `json:"afterFBeat"`
		Improved    bool    `json:"improved"`
	} `json:"report"`
}

// parseTrainEvent decodes one stdout line; ok=false for non-event output.
func parseTrainEvent(line []byte) (TrainEvent, bool) {
	var w wireEvent
	if err := json.Unmarshal(line, &w); err != nil || w.Ev == "" {
		return TrainEvent{}, false
	}
	ev := TrainEvent{
		Kind: w.Ev, Tracks: w.Tracks, Device: w.Device, Epoch: w.N,
		Loss: w.Loss, ValFBeat: w.ValFBeat, ValFDown: w.ValFDown,
		Checkpoint: w.Checkpoint, Msg: w.Msg,
	}
	if w.Report != nil {
		ev.BeforeF = w.Report.BeforeFBeat
		ev.AfterF = w.Report.AfterFBeat
		ev.Improved = w.Report.Improved
	}
	return ev, true
}

// Trainer runs one fine-tune as a supervised python child (one-shot, blocking).
type Trainer struct {
	Device string            // "auto" | "cpu" | "cuda" ("" = auto)
	FFmpeg string            // decode-fallback ffmpeg path ("" = PATH)
	OnLog  func(line string) // optional stderr feed (progress + tracebacks)
}

// Start spawns trainer.py on manifestPath and streams its events to onEvent
// until the child exits. dataDir hosts the script + model caches (same layout
// as the analyze engine so the base checkpoint cache is shared). Ctx cancel
// kills the child tree.
func (t *Trainer) Start(ctx context.Context, python, dataDir, manifestPath string, onEvent func(TrainEvent)) error {
	if python == "" {
		return fmt.Errorf("gridfix train: no python configured")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	script := filepath.Join(dataDir, "trainer.py")
	// refreshed each start so upgrades ship the current script
	if err := os.WriteFile(script, trainerScript, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(python, script, manifestPath)
	cmd.Dir = dataDir
	env := append(os.Environ(),
		"PYTHONIOENCODING=utf-8",
		"PYTHONUNBUFFERED=1",
		// share the engine's model cache: 'final0' resolves without re-download
		"HF_HOME="+filepath.Join(dataDir, "cache"),
		"TORCH_HOME="+filepath.Join(dataDir, "cache", "torch"),
		"XDG_CACHE_HOME="+filepath.Join(dataDir, "cache"),
	)
	if t.Device != "" {
		env = append(env, "GRIDFIX_DEVICE="+t.Device)
	}
	if t.FFmpeg != "" {
		env = append(env, "GRIDFIX_FFMPEG="+t.FFmpeg)
	}
	cmd.Env = env
	sysexec.Hide(cmd)
	sysexec.BelowNormalPriority(cmd) // good neighbour: never impair OBS/decks
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gridfix train start: %w", err)
	}
	sysexec.AssignToJob(cmd.Process, true) // kill-on-close job object
	done := make(chan struct{})
	go func() { // ctx cancel -> kill child tree; done guards post-exit kills
		select {
		case <-ctx.Done():
			sysexec.KillTree(cmd.Process)
		case <-done:
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			if t.OnLog != nil {
				t.OnLog(sc.Text())
			}
		}
	}()
	var errMsg string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		ev, ok := parseTrainEvent(sc.Bytes())
		if !ok {
			continue // stray non-event stdout line
		}
		if ev.Kind == "error" {
			errMsg = ev.Msg
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	waitErr := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errMsg != "" {
		return fmt.Errorf("gridfix train: %s", errMsg)
	}
	if waitErr != nil {
		return fmt.Errorf("gridfix train: %w", waitErr)
	}
	return nil
}
