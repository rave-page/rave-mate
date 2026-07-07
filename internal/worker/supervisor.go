package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysexec"
)

const source = "worker"

// Supervisor spawns + pools worker subprocesses per type. Workers are spawned lazily on
// first job, reused across jobs, idle-reaped after idleTimeout, and restarted on crash.
type Supervisor struct {
	log         *logbus.Bus
	exePath     string
	idleTimeout time.Duration

	mu    sync.Mutex
	pools map[string]*pool
}

// New builds a supervisor. exePath is this executable (re-invoked as `<exe> worker <type>`).
func New(log *logbus.Bus) (*Supervisor, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		log:         log,
		exePath:     exe,
		idleTimeout: 60 * time.Second,
		pools:       map[string]*pool{},
	}, nil
}

// pool holds idle worker processes for one type, capped at maxProcs.
type pool struct {
	sup        *Supervisor
	typ        string
	background bool
	maxProcs   int

	mu   sync.Mutex
	idle []*proc
	all  map[*proc]struct{} // every live proc (idle + busy) - so shutdown kills busy ones too
	live int                // total spawned (idle + busy)
	wait []chan *proc
}

// Configure sets the per-type concurrency cap (e.g. transcode MaxConcurrent). Safe to call
// before/after first use; default cap is 1.
func (s *Supervisor) Configure(typ string, maxProcs int) {
	if maxProcs < 1 {
		maxProcs = 1
	}
	s.mu.Lock()
	p := s.poolLocked(typ, false)
	p.mu.Lock()
	p.maxProcs = maxProcs
	p.mu.Unlock()
	s.mu.Unlock()
}

func poolKey(typ string, background bool) string {
	if background {
		return typ + "\x00background"
	}
	return typ
}

func (s *Supervisor) poolLocked(typ string, background bool) *pool {
	key := poolKey(typ, background)
	p, ok := s.pools[key]
	if !ok {
		p = &pool{sup: s, typ: typ, background: background, maxProcs: 1, all: map[*proc]struct{}{}}
		s.pools[key] = p
		debuglog.Go(s.log, source, p.reaper)
	}
	return p
}

// ProgressFunc receives a worker's progress events (event name + JSON data) during a job.
type ProgressFunc func(event string, data json.RawMessage)

// Run executes one job on a worker of the given type. method/params are the worker RPC;
// result is the worker's JSON result.
func (s *Supervisor) Run(ctx context.Context, typ, method string, params any) (json.RawMessage, error) {
	return s.RunStream(ctx, typ, method, params, nil)
}

// RunBackground executes one low-priority background job.
func (s *Supervisor) RunBackground(ctx context.Context, typ, method string, params any) (json.RawMessage, error) {
	return s.RunStreamBackground(ctx, typ, method, params, nil)
}

// RunStream is Run plus a progress callback invoked for each event a streaming handler
// emits before its terminal result (e.g. transcode "progress").
func (s *Supervisor) RunStream(ctx context.Context, typ, method string, params any, onProgress ProgressFunc) (json.RawMessage, error) {
	return s.runStream(ctx, typ, method, params, onProgress, false)
}

// RunStreamBackground is RunStream on a separate low-priority worker pool.
func (s *Supervisor) RunStreamBackground(ctx context.Context, typ, method string, params any, onProgress ProgressFunc) (json.RawMessage, error) {
	return s.runStream(ctx, typ, method, params, onProgress, true)
}

func (s *Supervisor) runStream(ctx context.Context, typ, method string, params any, onProgress ProgressFunc, background bool) (json.RawMessage, error) {
	if !KnownType(typ) {
		return nil, fmt.Errorf("unknown worker type %q", typ)
	}
	s.mu.Lock()
	p := s.poolLocked(typ, background)
	s.mu.Unlock()

	w, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	label := jobLabel(method, params)
	s.log.Info(source, label, nil) // surface the operation as it starts
	start := time.Now()
	res, err := w.call(ctx, method, params, onProgress)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		// Process is likely broken - discard it; the pool will spawn a fresh one.
		p.discard(w)
		s.log.Warn(source, label+" failed", map[string]any{"ms": ms, "error": err.Error()})
		return nil, err
	}
	p.release(w)
	s.log.Debug(source, label+" done", map[string]any{"ms": ms})
	return res, nil
}

// jobLabel turns a worker method (+ its path/input param) into a human log line so the
// Logs tab shows what file analysis is happening.
func jobLabel(method string, params any) string {
	verb := map[string]string{
		"transcode.detect":    "Detecting encoders",
		"transcode.silence":   "Detecting silence",
		"transcode.run":       "Transcoding",
		"transcode.encoders":  "Listing encoders",
		"probe.duration":      "Probing duration",
		"probe.streams":       "Reading media info",
		"probe.tags":          "Reading tags",
		"probe.artwork":       "Reading artwork",
		"probe.waveform":      "Rendering waveform",
		"probe.peaks":         "Analyzing waveform",
		"probe.envelope":      "Analyzing audio for alignment",
		"fingerprint.compute": "Fingerprinting",
		"render.motionvideo":  "Rendering motion video",
	}[method]
	if verb == "" {
		verb = method
	}
	if p := jobPath(params); p != "" {
		return verb + ": " + filepath.Base(p)
	}
	return verb
}

// jobPath best-effort extracts a "path"/"input" field from job params for the log label.
func jobPath(params any) string {
	raw, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var m struct {
		Path  string `json:"path"`
		Input string `json:"input"`
	}
	_ = json.Unmarshal(raw, &m)
	if m.Path != "" {
		return m.Path
	}
	return m.Input
}

// Stop kills all workers across all pools.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	pools := make([]*pool, 0, len(s.pools))
	for _, p := range s.pools {
		pools = append(pools, p)
	}
	s.mu.Unlock()
	for _, p := range pools {
		p.shutdown()
	}
}

// ── pool acquire/release ─────────────────────────────────────────────────────

func (p *pool) acquire(ctx context.Context) (*proc, error) {
	p.mu.Lock()
	if n := len(p.idle); n > 0 {
		w := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return w, nil
	}
	if p.live < p.maxProcs {
		p.live++
		p.mu.Unlock()
		w, err := p.spawn()
		if err != nil {
			p.mu.Lock()
			p.live--
			p.mu.Unlock()
			return nil, err
		}
		return w, nil
	}
	// At capacity - wait for a release.
	ch := make(chan *proc, 1)
	p.wait = append(p.wait, ch)
	p.mu.Unlock()
	select {
	case w := <-ch:
		return w, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pool) release(w *proc) {
	w.lastUsed = time.Now()
	p.mu.Lock()
	if len(p.wait) > 0 {
		ch := p.wait[0]
		p.wait = p.wait[1:]
		p.mu.Unlock()
		ch <- w
		return
	}
	p.idle = append(p.idle, w)
	p.mu.Unlock()
}

func (p *pool) discard(w *proc) {
	w.kill()
	p.mu.Lock()
	delete(p.all, w)
	p.live--
	// Wake a waiter so it can spawn a replacement.
	if len(p.wait) > 0 {
		ch := p.wait[0]
		p.wait = p.wait[1:]
		p.mu.Unlock()
		if nw, err := p.acquireSpawn(); err == nil {
			ch <- nw
		} else {
			close(ch)
		}
		return
	}
	p.mu.Unlock()
}

// acquireSpawn spawns a fresh proc counting it as live (used to replace a discarded one).
func (p *pool) acquireSpawn() (*proc, error) {
	p.mu.Lock()
	p.live++
	p.mu.Unlock()
	w, err := p.spawn()
	if err != nil {
		p.mu.Lock()
		p.live--
		p.mu.Unlock()
	}
	return w, err
}

func (p *pool) spawn() (*proc, error) {
	cmd := exec.Command(p.sup.exePath, "worker", p.typ)
	sysexec.Hide(cmd)                   // no console window for the spawned worker (Windows GUI subsystem)
	sysexec.Named(cmd, "worker-"+p.typ) // distinct image name in task managers / ps
	if p.background {
		cmd.Env = workerEnv(true)
		sysexec.LowPriority(cmd)
	} else {
		cmd.Env = workerEnv(false)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sysexec.AssignToJob(cmd.Process, p.background) // Windows: kill-on-close; background pool is CPU-capped.
	w := &proc{cmd: cmd, stdin: stdin, dec: json.NewDecoder(bufio.NewReader(stdout)), lastUsed: time.Now()}
	p.mu.Lock()
	p.all[w] = struct{}{}
	p.mu.Unlock()
	p.sup.log.Debug(source, "spawned worker", map[string]any{"type": p.typ, "background": p.background, "pid": cmd.Process.Pid})
	return w, nil
}

// shutdown kills EVERY live worker in the pool - idle and busy (mid-ffmpeg) alike - so no
// child outlives the app on a clean quit. (The Windows job object is the backstop for
// non-clean exits.)
func (p *pool) shutdown() {
	p.mu.Lock()
	all := make([]*proc, 0, len(p.all))
	for w := range p.all {
		all = append(all, w)
	}
	p.all = map[*proc]struct{}{}
	p.idle = nil
	p.live = 0
	p.wait = nil
	p.mu.Unlock()
	for _, w := range all {
		w.kill()
	}
}

// reaper kills workers idle longer than the supervisor's idleTimeout.
func (p *pool) reaper() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		p.mu.Lock()
		kept := p.idle[:0]
		for _, w := range p.idle {
			if now.Sub(w.lastUsed) > p.sup.idleTimeout {
				w.kill()
				delete(p.all, w)
				p.live--
			} else {
				kept = append(kept, w)
			}
		}
		p.idle = kept
		p.mu.Unlock()
	}
}

// ── proc: one worker process ─────────────────────────────────────────────────

type proc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	dec      *json.Decoder
	lastUsed time.Time
	seq      int
	mu       sync.Mutex
}

func (w *proc) call(ctx context.Context, method string, params any, onProgress ProgressFunc) (json.RawMessage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	id := fmt.Sprintf("%d", w.seq)

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reqLine, err := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, err
	}
	type result struct {
		resp Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// done is buffered(1); recover must still feed it so the caller's select can't hang.
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("worker io panic: %v", r)}
			}
		}()
		if _, werr := w.stdin.Write(append(reqLine, '\n')); werr != nil {
			done <- result{err: werr}
			return
		}
		// Read frames: Event (event!="") is non-terminal progress; the first frame
		// without an event field is the terminal Response.
		for {
			var f struct {
				Event  string          `json:"event"`
				Data   json.RawMessage `json:"data"`
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
				Error  string          `json:"error"`
			}
			if derr := w.dec.Decode(&f); derr != nil {
				done <- result{err: derr}
				return
			}
			if f.Event != "" {
				if onProgress != nil {
					onProgress(f.Event, f.Data)
				}
				continue
			}
			done <- result{resp: Response{OK: f.OK, Result: f.Result, Error: f.Error}}
			return
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		if !r.resp.OK {
			return nil, fmt.Errorf("%s", r.resp.Error)
		}
		return r.resp.Result, nil
	}
}

func (w *proc) kill() {
	if w.cmd.Process != nil {
		sysexec.KillTree(w.cmd.Process) // also kills children (e.g. a running ffmpeg)
		_, _ = w.cmd.Process.Wait()
	}
}
