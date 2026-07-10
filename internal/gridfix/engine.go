package gridfix

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/sysexec"
)

//go:embed runner.py
var runnerScript []byte

// Detection is one track's neural beat analysis.
type Detection struct {
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	Device    string    `json:"device,omitempty"`
}

// Versions reported by the engine's ping.
type Versions struct {
	Python    string `json:"python"`
	BeatThis  string `json:"beat-this"`
	Torch     string `json:"torch"`
	NumPy     string `json:"numpy"`
	SoundFile string `json:"soundfile"`
}

// Engine supervises the persistent Python beat-detection child (runner.py): model
// loads once, requests stream over newline-JSON stdio. Not safe for concurrent
// Analyze calls by design (one model, serial GPU/CPU inference) - a mutex serializes.
type Engine struct {
	Python  string // venv python executable
	DataDir string // dir holding runner.py + model cache (HF_HOME etc. kept inside)
	Device  string // "auto" | "cpu" | "cuda"
	FFmpeg  string // ffmpeg path for decode fallback ("" = PATH)
	OnLog   func(line string)

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Scanner
	nextID int
}

type runnerReply struct {
	ID        int       `json:"id"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error"`
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	Device    string    `json:"device"`
	Versions  *Versions `json:"versions"`
}

// scriptPath writes the embedded runner.py under DataDir (refreshed each start so
// upgrades ship the current script) and returns its path.
func (e *Engine) scriptPath() (string, error) {
	if err := os.MkdirAll(e.DataDir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(e.DataDir, "runner.py")
	if err := os.WriteFile(p, runnerScript, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// Start launches the runner child (idempotent). The model itself loads lazily on
// the first Analyze (or a ping with load_model).
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked()
}

// startLocked deliberately doesn't bind the child to a request ctx - the engine
// outlives individual calls; lifetime is Stop() + the kill-on-close job object.
func (e *Engine) startLocked() error {
	if e.cmd != nil {
		return nil
	}
	if e.Python == "" {
		return fmt.Errorf("gridfix engine: no python configured")
	}
	script, err := e.scriptPath()
	if err != nil {
		return err
	}
	cmd := exec.Command(e.Python, script)
	cmd.Dir = e.DataDir
	env := append(os.Environ(),
		"PYTHONIOENCODING=utf-8",
		"PYTHONUNBUFFERED=1",
		// keep model/checkpoint caches inside our data dir, not the user profile
		"HF_HOME="+filepath.Join(e.DataDir, "cache"),
		"TORCH_HOME="+filepath.Join(e.DataDir, "cache", "torch"),
		"XDG_CACHE_HOME="+filepath.Join(e.DataDir, "cache"),
	)
	if e.Device != "" {
		env = append(env, "GRIDFIX_DEVICE="+e.Device)
	}
	if e.FFmpeg != "" {
		env = append(env, "GRIDFIX_FFMPEG="+e.FFmpeg)
	}
	cmd.Env = env
	sysexec.Hide(cmd)
	sysexec.BelowNormalPriority(cmd) // good neighbour: never impair OBS/decks
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gridfix engine start: %w", err)
	}
	sysexec.AssignToJob(cmd.Process, true) // kill-on-close job object
	go func() {                            // surface python tracebacks/progress to the log
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			if e.OnLog != nil {
				e.OnLog(sc.Text())
			}
		}
	}()
	e.cmd = cmd
	e.stdin = stdin
	e.out = bufio.NewScanner(stdout)
	// beats arrays for a 2h DJ set are large; give the scanner room (cap 32MB)
	e.out.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	return nil
}

// Stop terminates the child (idempotent).
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
}

func (e *Engine) stopLocked() {
	if e.cmd == nil {
		return
	}
	_ = e.stdin.Close() // EOF = clean exit
	done := make(chan struct{})
	cmd := e.cmd
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		sysexec.KillTree(cmd.Process)
	}
	e.cmd, e.stdin, e.out = nil, nil, nil
}

// roundTrip sends one request and reads its reply (child restarted on wire failure).
func (e *Engine) roundTrip(ctx context.Context, req map[string]any) (*runnerReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.startLocked(); err != nil {
		return nil, err
	}
	e.nextID++
	id := e.nextID
	req["id"] = id
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := e.stdin.Write(append(raw, '\n')); err != nil {
		e.stopLocked()
		return nil, fmt.Errorf("gridfix engine write: %w", err)
	}
	type scanResult struct {
		reply *runnerReply
		err   error
	}
	ch := make(chan scanResult, 1)
	out := e.out
	go func() {
		for out.Scan() {
			var r runnerReply
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				continue // stray non-JSON stdout line
			}
			if r.ID == id {
				ch <- scanResult{reply: &r}
				return
			}
		}
		err := out.Err()
		if err == nil {
			err = fmt.Errorf("gridfix engine exited")
		}
		ch <- scanResult{err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			e.stopLocked()
			return nil, res.err
		}
		if res.reply.Error != "" {
			return nil, fmt.Errorf("gridfix engine: %s", res.reply.Error)
		}
		return res.reply, nil
	case <-ctx.Done():
		e.stopLocked() // reader goroutine unblocks via pipe close
		return nil, ctx.Err()
	}
}

// Ping verifies the engine imports cleanly; loadModel also warms the checkpoint
// (first call downloads it into DataDir/cache).
func (e *Engine) Ping(ctx context.Context, loadModel bool) (*Versions, string, error) {
	r, err := e.roundTrip(ctx, map[string]any{"op": "ping", "load_model": loadModel})
	if err != nil {
		return nil, "", err
	}
	return r.Versions, r.Device, nil
}

// Analyze runs neural beat detection on one audio file.
func (e *Engine) Analyze(ctx context.Context, path string) (*Detection, error) {
	r, err := e.roundTrip(ctx, map[string]any{"op": "analyze", "path": path})
	if err != nil {
		return nil, err
	}
	return &Detection{Beats: r.Beats, Downbeats: r.Downbeats, Device: r.Device}, nil
}
