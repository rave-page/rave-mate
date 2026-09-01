package featurehost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysexec"
)

// Restart backoff schedule + the uptime after which the attempt counter resets.
// Package vars so tests can shorten them.
var (
	backoffSchedule = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	stableAfter     = 60 * time.Second
	initTimeout     = 10 * time.Second
	stopGrace       = 3 * time.Second
)

// Options configures one feature host.
type Options struct {
	Name string
	Log  *logbus.Bus
	// Init returns the feature's init params; re-evaluated on every (re)spawn so config
	// edits take effect on restart.
	Init func() any
	// OnEvent routes child events by name (e.g. "obs", "state", "capture"). Each handler runs
	// INLINE on the host's single reader goroutine, so a handler that blocks stalls the whole event
	// pump - including this child's heartbeat frames (EventHeartbeat), which monitorHeartbeat then
	// reads as a hang and force-restarts an otherwise healthy child (2026-09-01: a page act ran a DB
	// query inline here behind a long VACUUM and got the webview child killed). Handlers MUST be
	// fast and non-blocking: hand slow work to a queue/goroutine and return. "log" is built-in.
	OnEvent map[string]func(data json.RawMessage)
	// OnDown fires when the child leaves ready state (crash or stop) - proxies use it to
	// reset mirrored state / broadcast a "down" status. Optional.
	OnDown func()
	// OnReady fires after each successful init handshake (every (re)spawn) - proxies use it to
	// re-push full desired state so a restarted child reconstructs everything. Optional.
	OnReady func()
	// HeartbeatTimeout, if > 0, force-restarts the child when it stops sending rt.Beat() pings for
	// this long while ready - catches a hung (not crashed) feature, e.g. a wedged cgo GPU call.
	// Only for features that actually call rt.Beat() from their work loop; leave 0 otherwise.
	HeartbeatTimeout time.Duration
	// MemLimitMB, if > 0, assigns the child to a Windows job with this per-process committed-memory
	// cap (kill-on-close). A runaway heap fails its next allocation → child dies → Host restarts it.
	// For resource-bearing children (media plane); leave 0 for the plain kill-on-close job.
	MemLimitMB int
	// CPUClass is the child's job CPU-scheduling class (sysexec.JobClass). Zero value =
	// JobRealtime (uncapped, prior behaviour); the media plane child runs as JobMedia.
	CPUClass sysexec.JobClass
	// Command overrides how the child is spawned (default `<exe> feature <name>`). TEST SEAM for
	// callers outside this package: a test re-execs its own binary with an env marker instead of
	// shipping an exe. Nil in production.
	Command func() *exec.Cmd
	// LowPriority spawns the child in BELOW_NORMAL_PRIORITY_CLASS so a background feature (e.g.
	// Icecast set-capture receiving+writing a live broadcast) always yields to the user's
	// foreground app and any active encoder. Leave false for latency-sensitive children.
	LowPriority bool
	// MaxAttempts, if > 0, caps consecutive restart attempts without a stable run - once spent the
	// supervisor gives up (loud log + toast; Stats keeps the error) instead of respawning forever
	// (a child faulting every ~70s otherwise cycles at 1s for the whole session). Recover by
	// toggling the feature off/on (Stop+Start) or restarting the app. 0 = unlimited.
	MaxAttempts int
	// StableAfter overrides the uptime that resets the attempt counter (0 = package default 60s).
	// Lengthen for children whose known failure mode (GPU faults) recurs slower than the default,
	// so periodic faults burn the MaxAttempts budget instead of resetting it.
	StableAfter time.Duration
}

// Host supervises one feature child process: spawn → init handshake → event pump, with
// crash detection, capped-backoff restart, and graceful stop. A child crash (panic, cgo
// fault, OOM-kill) is logged + alerted - never propagated to the daemon.
type Host struct {
	opt     Options
	exePath string
	command func() *exec.Cmd // test hook; nil = `<exe> feature <name>`

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	cur      *childProc
	ready    bool
	restarts int
	lastErr  string
	notify   func(title, body string)
	seq      int
	pending  map[string]chan frame
	lastBeat time.Time // last heartbeat from the child (HeartbeatTimeout monitor)

	framesTotal uint64            // stdout frames read (lifetime; perf probe)
	frames      map[string]uint64 // per event name ("resp" for responses)
}

type childProc struct {
	cmd *exec.Cmd
	w   *wire
}

// New builds a host (does not spawn).
func New(opt Options) (*Host, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	h := &Host{opt: opt, exePath: exe, command: opt.Command,
		pending: map[string]chan frame{}, frames: map[string]uint64{}}
	hostsMu.Lock()
	hosts = append(hosts, h)
	hostsMu.Unlock()
	return h, nil
}

// Package registry of every built Host so `ctl perf` can list the daemon's feature
// children (pid/ready/restarts) without app.go threading each proxy through.
var (
	hostsMu sync.Mutex
	hosts   []*Host
)

// ChildInfo is one supervised feature child's identity + lifecycle counters.
type ChildInfo struct {
	Name     string
	PID      int // 0 while not running
	Ready    bool
	Restarts int
	LastErr  string
}

// Children snapshots every host's child state (perf diagnosis).
func Children() []ChildInfo {
	hostsMu.Lock()
	hs := append([]*Host(nil), hosts...)
	hostsMu.Unlock()
	out := make([]ChildInfo, 0, len(hs))
	for _, h := range hs {
		h.mu.Lock()
		ci := ChildInfo{Name: h.opt.Name, Ready: h.ready, Restarts: h.restarts, LastErr: h.lastErr}
		if h.cur != nil && h.cur.cmd.Process != nil {
			ci.PID = h.cur.cmd.Process.Pid
		}
		h.mu.Unlock()
		out = append(out, ci)
	}
	return out
}

// countFrame tallies one child stdout frame (event name; "resp" for responses).
func (h *Host) countFrame(event string) {
	if event == "" {
		event = "resp"
	}
	h.mu.Lock()
	h.framesTotal++
	h.frames[event]++
	h.mu.Unlock()
}

// frameStats state: previous totals so FrameStats reports the rate between calls.
var (
	frameStatMu   sync.Mutex
	frameStatPrev = map[string]uint64{}
	frameStatAt   time.Time
)

// FrameStats is a perfmon probe: per-child stdout frame totals, per-event breakdown, and
// frames/sec since the previous probe call - makes a flooding child obvious in `ctl perf`.
func FrameStats() string {
	hostsMu.Lock()
	hs := append([]*Host(nil), hosts...)
	hostsMu.Unlock()

	frameStatMu.Lock()
	defer frameStatMu.Unlock()
	now := time.Now()
	dt := now.Sub(frameStatAt).Seconds()
	first := frameStatAt.IsZero()
	frameStatAt = now

	var b strings.Builder
	for _, h := range hs {
		h.mu.Lock()
		total := h.framesTotal
		events := make(map[string]uint64, len(h.frames))
		for k, v := range h.frames {
			events[k] = v
		}
		h.mu.Unlock()

		line := fmt.Sprintf("%-8s total=%d", h.opt.Name, total)
		if !first && dt > 0 {
			line += fmt.Sprintf(" (%.1f/s since last report)", float64(total-frameStatPrev[h.opt.Name])/dt)
		}
		frameStatPrev[h.opt.Name] = total
		keys := make([]string, 0, len(events))
		for k := range events {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return events[keys[i]] > events[keys[j]] })
		for i, k := range keys {
			if i == 0 {
				line += " |"
			}
			line += fmt.Sprintf(" %s=%d", k, events[k])
		}
		b.WriteString(line + "\n")
	}
	if b.Len() == 0 {
		return "(no feature children)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// SetNotifier wires a user-facing crash alert (desktop toast). Nil just logs.
func (h *Host) SetNotifier(fn func(title, body string)) {
	h.mu.Lock()
	h.notify = fn
	h.mu.Unlock()
}

func (h *Host) src() string { return "feature:" + h.opt.Name }

// Start begins supervising (non-blocking). ctx bounds the feature's lifetime - cancel
// (or Stop) reaps the child. Idempotent while running.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.cancel != nil {
		h.mu.Unlock()
		return nil
	}
	cctx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.done = make(chan struct{})
	done := h.done
	h.mu.Unlock()
	debuglog.Go(h.opt.Log, h.src(), func() {
		defer close(done)
		h.supervise(cctx)
	})
	return nil
}

// Stop ends supervision and blocks until the child is reaped (graceful stop + grace,
// then kill). Idempotent.
func (h *Host) Stop() {
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	h.cancel = nil
	h.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Running reports whether the child is up and past its init handshake.
func (h *Host) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready
}

// Kill force-ends the current child session (no graceful stop): the reader sees stdout close and
// supervise restarts with backoff. For a child that is alive but no longer serving - e.g. one that
// stopped reading its stdin (webui procShell's wedge watchdog). No-op when nothing is running.
func (h *Host) Kill() {
	h.mu.Lock()
	cur := h.cur
	h.mu.Unlock()
	if cur == nil || cur.cmd.Process == nil {
		return
	}
	sysexec.KillTree(cur.cmd.Process)
}

// Stats returns the lifetime restart count and the last crash error ("" if none).
func (h *Host) Stats() (restarts int, lastErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.restarts, h.lastErr
}

// Send writes one fire-and-forget event to the child (e.g. merged session updates) -
// no response, no pending state. Errors if the feature is down.
func (h *Host) Send(event string, data any) error {
	h.mu.Lock()
	cur := h.cur
	h.mu.Unlock()
	if cur == nil {
		return errors.New("feature not running")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return cur.w.send(&frame{Event: event, Data: raw})
}

// Call sends one control request to the child and waits for its response (params is
// JSON-marshaled). Errors immediately if the feature is down.
func (h *Host) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return h.call(ctx, method, raw)
}

func (h *Host) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	h.mu.Lock()
	cur := h.cur
	if cur == nil {
		h.mu.Unlock()
		return nil, errors.New("feature not running")
	}
	h.seq++
	id := strconv.Itoa(h.seq)
	ch := make(chan frame, 1)
	h.pending[id] = ch
	h.mu.Unlock()

	if err := cur.w.send(&frame{ID: id, Method: method, Params: params}); err != nil {
		h.dropPending(id)
		return nil, err
	}
	select {
	case fr := <-ch:
		if !fr.OK {
			return nil, errors.New(fr.Error)
		}
		return fr.Result, nil
	case <-ctx.Done():
		h.dropPending(id)
		return nil, ctx.Err()
	}
}

func (h *Host) dropPending(id string) {
	h.mu.Lock()
	delete(h.pending, id)
	h.mu.Unlock()
}

// failPending answers every in-flight Call with an error (child died).
func (h *Host) failPending() {
	h.mu.Lock()
	pend := h.pending
	h.pending = map[string]chan frame{}
	h.mu.Unlock()
	for _, ch := range pend {
		ch <- frame{Error: "feature exited"}
	}
}

// ── supervision ──────────────────────────────────────────────────────────────

// stableWindow is the uptime that resets the attempt counter (per-host override, else default).
func (h *Host) stableWindow() time.Duration {
	if h.opt.StableAfter > 0 {
		return h.opt.StableAfter
	}
	return stableAfter
}

func (h *Host) supervise(ctx context.Context) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		startAt := time.Now()
		err := h.runOnce(ctx)
		if ctx.Err() != nil {
			return // clean stop
		}
		if time.Since(startAt) >= h.stableWindow() {
			attempt = 0 // was healthy long enough - fresh backoff
		}
		delay := backoffSchedule[min(attempt, len(backoffSchedule)-1)]
		msg := "feature crashed"
		if err != nil {
			msg += ": " + err.Error()
		}
		h.mu.Lock()
		h.restarts++
		h.lastErr = msg
		notify := h.notify
		h.mu.Unlock()
		attempt++
		if h.opt.MaxAttempts > 0 && attempt >= h.opt.MaxAttempts {
			// Restart budget spent without a stable run: give up instead of cycling forever.
			gaveUp := msg + " - gave up after " + strconv.Itoa(attempt) + " attempts"
			h.mu.Lock()
			h.lastErr = gaveUp
			h.mu.Unlock()
			h.opt.Log.Error(h.src(), gaveUp+" (toggle the feature off/on or restart rave-mate to retry)", map[string]any{"attempts": attempt})
			if notify != nil {
				notify("Feature stopped", h.opt.Name+" kept crashing - auto-restart paused. Toggle the feature or restart rave-mate to retry.")
			}
			return
		}
		// First crash of a streak + every 5th at ERROR; the rest at Debug so a
		// crash-loop doesn't flood the ring (restart count stays in Status()).
		if attempt == 1 || (attempt-1)%5 == 0 {
			h.opt.Log.Error(h.src(), msg+" - restarting", map[string]any{"delay": delay.String(), "attempt": attempt})
		} else {
			h.opt.Log.Debug(h.src(), msg+" - restarting", map[string]any{"delay": delay.String(), "attempt": attempt})
		}
		if notify != nil && (attempt == 1 || (attempt-1)%5 == 0) {
			notify("Feature crashed", h.opt.Name+" restarting in "+delay.String()+" - other features unaffected")
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

// runOnce runs one child session: spawn → init → pump until exit or ctx cancel.
// Returns the crash error (nil only on graceful ctx-driven stop).
func (h *Host) runOnce(ctx context.Context) error {
	cmd := h.newCmd()
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
		return err
	}
	if h.opt.MemLimitMB > 0 {
		sysexec.AssignToJobMemClass(cmd.Process, h.opt.MemLimitMB, h.opt.CPUClass) // mem-capped kill-on-close job + CPU class
	} else {
		sysexec.AssignToJobClass(cmd.Process, h.opt.CPUClass) // Windows: kill-on-close backstop (+ class CPU cap)
	}

	// Child stderr (panic stacks, raw diagnostics) → daemon log, line by line - AND a
	// bounded tail + latched fatal header. A goroutine dump is hundreds of lines: streamed
	// alone it scrolls the fatal header ("panic: ...", "fatal error: ...") out of the ring
	// before anyone reads it; the consolidated crash entry below keeps the header.
	tail := &stderrTail{}
	debuglog.Go(h.opt.Log, h.src(), func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			if ln := sc.Text(); ln != "" {
				h.opt.Log.Warn(h.src(), ln, nil)
				tail.add(ln)
			}
		}
	})

	w := newWire(stdin)
	h.mu.Lock()
	h.cur = &childProc{cmd: cmd, w: w}
	h.mu.Unlock()

	// Reader: demux responses + events until the child's stdout closes.
	readerDone := make(chan error, 1)
	debuglog.Go(h.opt.Log, h.src(), func() {
		dec := json.NewDecoder(bufio.NewReader(stdout))
		for {
			var fr frame
			if err := dec.Decode(&fr); err != nil {
				readerDone <- err
				return
			}
			h.countFrame(fr.Event)
			switch {
			case fr.Event == EventHeartbeat:
				h.mu.Lock()
				h.lastBeat = time.Now()
				h.mu.Unlock()
			case fr.Event == EventLog:
				var le logEvent
				if json.Unmarshal(fr.Data, &le) == nil {
					if le.Fields == nil {
						le.Fields = map[string]any{}
					}
					le.Fields["proc"] = h.opt.Name
					h.opt.Log.Log(logbus.Level(le.Level), le.Source, le.Msg, le.Fields)
				}
			case fr.Event != "":
				if fn := h.opt.OnEvent[fr.Event]; fn != nil {
					h.dispatchEvent(fr.Event, fn, fr.Data)
				}
			default:
				h.mu.Lock()
				ch := h.pending[fr.ID]
				delete(h.pending, fr.ID)
				h.mu.Unlock()
				if ch != nil {
					ch <- fr
				}
			}
		}
	})

	reap := func() {
		h.mu.Lock()
		h.cur = nil
		h.mu.Unlock()
		h.setDown()
		sysexec.KillTree(cmd.Process)
		_, _ = cmd.Process.Wait()
		h.failPending()
	}

	// Init handshake = ready signal. A bind clash etc. comes back as the init error.
	initRaw, err := json.Marshal(h.opt.Init())
	if err != nil {
		reap()
		return err
	}
	ictx, icancel := context.WithTimeout(ctx, initTimeout)
	_, err = h.call(ictx, methodInit, initRaw)
	icancel()
	if err != nil {
		reap()
		return fmt.Errorf("init: %w", err)
	}
	h.mu.Lock()
	h.ready = true
	h.lastBeat = time.Now()
	h.mu.Unlock()
	h.opt.Log.Info(h.src(), "feature running", map[string]any{"pid": cmd.Process.Pid})
	if h.opt.OnReady != nil { // guarded: a proxy re-push bug must not kill this session
		func() {
			defer debuglog.Recover(h.opt.Log, h.src()+":ready", false)
			h.opt.OnReady()
		}()
	}

	// Liveness monitor: a ready child that stops beating for HeartbeatTimeout is hung (not
	// crashed) - kill it so this session ends and supervise restarts it. Bound to the session.
	monDone := make(chan struct{})
	defer close(monDone)
	if h.opt.HeartbeatTimeout > 0 {
		debuglog.Go(h.opt.Log, h.src(), func() { h.monitorHeartbeat(cmd, monDone) })
	}

	select {
	case rerr := <-readerDone: // child died (or closed stdout)
		ps, _ := cmd.Process.Wait()
		h.mu.Lock()
		h.cur = nil
		h.mu.Unlock()
		h.setDown()
		h.failPending()
		if ps != nil && ps.ExitCode() == 0 {
			return errors.New("feature exited")
		}
		// One consolidated crash entry: latched fatal header + bounded stderr tail (the
		// per-line stream above is ring-evicted by long goroutine dumps).
		if hdr, tl := tail.snapshot(); hdr != "" || tl != "" {
			f := map[string]any{"header": hdr, "stderr_tail": tl}
			// fatal_head carries the FAULTING goroutine; stderr_tail carries whichever goroutine the
			// dump happened to end on. Without the head there is nothing to attribute a fault to.
			if hd := tail.fatalHead(); hd != "" {
				f["fatal_head"] = hd
			}
			h.opt.Log.Error(h.src(), "feature crash forensics", f)
		}
		if ps != nil {
			return fmt.Errorf("feature exited with code %d", ps.ExitCode())
		}
		return fmt.Errorf("feature stream closed: %v", rerr)
	case <-ctx.Done(): // graceful stop
		h.setDown()
		sctx, scancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, _ = h.call(sctx, methodStop, nil)
		scancel()
		select {
		case <-readerDone:
		case <-time.After(stopGrace):
		}
		reap()
		return nil
	}
}

// monitorHeartbeat kills the child if it stops beating for HeartbeatTimeout while ready. Killing
// closes its stdout → the reader ends this session → supervise restarts with backoff. Stops when
// the session ends (monDone closed) or the child is already gone.
func (h *Host) monitorHeartbeat(cmd *exec.Cmd, monDone <-chan struct{}) {
	interval := max(h.opt.HeartbeatTimeout/3, 500*time.Millisecond)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-monDone:
			return
		case <-tick.C:
		}
		h.mu.Lock()
		ready, last := h.ready, h.lastBeat
		h.mu.Unlock()
		if !ready {
			return
		}
		if since := time.Since(last); since > h.opt.HeartbeatTimeout {
			h.opt.Log.Error(h.src(), "feature hung (no heartbeat) - killing to restart", map[string]any{"silent": since.Truncate(time.Second).String()})
			sysexec.KillTree(cmd.Process)
			return
		}
	}
}

// setDown clears ready and fires OnDown on a true→false transition (guarded).
func (h *Host) setDown() {
	h.mu.Lock()
	wasReady := h.ready
	h.ready = false
	h.mu.Unlock()
	if wasReady && h.opt.OnDown != nil {
		defer debuglog.Recover(h.opt.Log, h.src()+":down", false)
		h.opt.OnDown()
	}
}

// dispatchEvent guards a single event handler so a daemon-side handler bug can't kill
// the reader (and with it the whole child session).
func (h *Host) dispatchEvent(name string, fn func(json.RawMessage), data json.RawMessage) {
	defer debuglog.Recover(h.opt.Log, h.src()+":"+name, false)
	fn(data)
}

func (h *Host) newCmd() *exec.Cmd {
	if h.command != nil {
		return h.command()
	}
	cmd := exec.Command(h.exePath, "feature", h.opt.Name)
	sysexec.Hide(cmd)                         // no console window (Windows GUI subsystem)
	sysexec.Named(cmd, "feature-"+h.opt.Name) // distinct image name in task managers / ps
	if h.opt.LowPriority {
		sysexec.BelowNormalPriority(cmd) // background feature - always yield to foreground/encoder
	}
	return cmd
}
