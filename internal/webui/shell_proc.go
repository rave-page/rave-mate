package webui

// procShell - the third `shell` implementation (phase B5): the WebView2 window moves into a
// supervised child (`rave-mate feature webview`) and the daemon drives it over PSH1
// (shell_proc_proto.go). NO renderer changes: the daemon builds exactly the same document, patches
// and act payloads as before - only where the bytes go changes. Selection is flag-gated
// (RAVE_MATE_SHELL=proc; default stays cgo, see shell_select.go).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/logbus"
)

const (
	// procOrdQueueCap bounds the ordered lane's pipe hand-off. The real buffer is the daemon's
	// coalescing eval queue (maxEvalQueue), and dispatchEvals holds until each batch is acked, so
	// steady state is ≤1 frame here; this cap only bounds a child that stopped consuming. Policy:
	// drop-OLDEST + fragment-cache wipe (procShell.onDrop), so a dropped patch re-emits next tick
	// instead of sticking stale - the same rule enqueueEval applies on its own overflow.
	procOrdQueueCap = 8
	// procDirQueueCap bounds the direct lane. Overflow FAILS the caller (ctl gets ok=false) rather
	// than dropping silently: a ctl round-trip must never return a stale answer.
	procDirQueueCap = 32
	// procWriteGrace: a pipe write blocked this long means the child stopped reading stdin - i.e.
	// wedged. Kill it; featurehost restarts and the daemon reattaches. Proven by execution
	// (TestProcShellWedgedChildTerminates).
	procWriteGrace = 2 * time.Second
	// procQuitGrace: how long terminate() waits for the child to unwind on its own before the host
	// stop/kill path runs. Mirrors the in-proc forceExitGrace budget.
	procQuitGrace = 1500 * time.Millisecond
	// procBeatInterval / procBeatTimeout: the child pings from the window's UI THREAD, so a wedged
	// webview stops beating and the Host force-restarts the child. Generous timeout - a slow render
	// or a long modal must not cost the user their window.
	procBeatInterval = 2 * time.Second
	procBeatTimeout  = 20 * time.Second
	// procBeatID is the reserved __rave_evalResult id the child's beat rides on (no extra binding).
	procBeatID = "__beat"
)

// shellLog is the bus procShell (and its featurehost Host) logs to. Set once in New before the
// shell is constructed - same pattern as webviewAllowGPU.
var shellLog *logbus.Bus

type procFrame struct {
	ev   string
	data any
}

type procShell struct {
	title    string
	w, h     int
	onAction func(string)
	onReady  func()

	// onReattach fires after every child RESTART (not the first ready): the daemon re-sends the
	// document and re-renders everything (patchMain), which is what makes a crashed window
	// reconstructible - the virtualShell contract already guarantees the UI is derivable from state.
	onReattach func()
	// onDrop fires on ordered-lane overflow: wipe the tick dedup caches so nothing sticks stale.
	onDrop func()

	log  *logbus.Bus
	host *featurehost.Host

	ord chan procFrame
	dir chan procFrame
	// sendFn writes one frame to the child; nil = the feature host. Seam for the lane-policy gates,
	// which must observe the WRITER's choices without a child process in the way.
	sendFn func(ev string, data any) error

	seq     atomic.Uint64
	dropped atomic.Uint64
	hwndV   atomic.Uint64
	sizeMv  atomic.Bool
	writeAt atomic.Int64 // UnixNano of the in-flight pipe write; 0 = idle (wedge watchdog)
	gens    atomic.Uint64
	streamV atomic.Bool

	mu       sync.Mutex
	lastHTML string
	hidden   bool

	cancel   context.CancelFunc
	doneOnce sync.Once
	done     chan struct{} // closed when run() may return (window gone / terminate finished)
	stopW    chan struct{} // closes the writer + watchdog
}

// newProcShell builds the shell + its feature host (nothing spawns until run).
func newProcShell(title string, w, h int, onAction func(string), onReady func()) (shell, bool) {
	log := shellLog
	if log == nil {
		log = logbus.New(64) // never nil: featurehost logs unconditionally
	}
	s := &procShell{
		title: title, w: w, h: h, onAction: onAction, onReady: onReady, log: log,
		ord:  make(chan procFrame, procOrdQueueCap),
		dir:  make(chan procFrame, procDirQueueCap),
		done: make(chan struct{}), stopW: make(chan struct{}),
	}
	h2, err := featurehost.New(featurehost.Options{
		Name:             procFeatureName,
		Log:              log,
		Init:             s.initParams,
		OnEvent:          s.events(),
		OnReady:          s.onChildReady,
		OnDown:           s.onChildDown,
		HeartbeatTimeout: procBeatTimeout,
		Command:          procChildCmd,
	})
	if err != nil {
		return nil, false
	}
	s.host = h2
	return s, true
}

// initParams is re-evaluated on every (re)spawn: a restarted child comes up with the CURRENT
// document. The child reads no config and opens no database - everything it needs is here.
func (s *procShell) initParams() any {
	s.mu.Lock()
	html, hidden := s.lastHTML, s.hidden
	s.mu.Unlock()
	dataDir := ""
	if d, err := shellDataDir(); err == nil {
		dataDir = d
	}
	// One media session per child session: the URLs the reattach render mints are scoped to THIS
	// child, and the previous session stays valid so a <video> mid-play survives the restart
	// (mediahttp.go mediaSessMax). Origin is "" until the media listener has been needed once.
	sess := mpMediaFS.newSession()
	origin, _ := mpMediaFS.originAndSession()
	return procInit{
		Title: s.title, W: s.w, H: s.h, StartHidden: hidden,
		AllowGPU: webviewAllowGPU, DataDir: dataDir,
		RuntimeJS:   runtimeJS,
		InitialHTML: html,
		MediaOrigin: origin, MediaSession: sess,
		Streaming: governor.Snapshot().Streaming,
		Virtual:   procVirtualChild,
	}
}

// procVirtualChild makes the spawned child run the loopback page model instead of WebView2 (tests
// only; see shell_proc_loopback.go).
var procVirtualChild bool

// procChildCmd overrides how the window child is spawned. Nil in production (`<exe> feature
// webview`); the B5 tests re-exec the test binary with an env marker instead of shipping an exe.
var procChildCmd func() *exec.Cmd

func (s *procShell) events() map[string]func(json.RawMessage) {
	return map[string]func(json.RawMessage){
		procEvReady:   s.evReady,
		procEvEvalRes: s.evEvalRes,
		procEvAction:  s.evAction,
		procEvWin:     s.evWin,
		procEvGone:    s.evGone,
	}
}

func (s *procShell) evReady(data json.RawMessage) {
	var m procReady
	if json.Unmarshal(data, &m) == nil {
		s.hwndV.Store(m.HWND)
	}
}

// evEvalRes routes a page result to its blocked caller - the SAME evalWaiters map the in-proc shell
// uses, so ordered-lane acks (dispatchEvals) and ctl round-trips (evalValue) are unchanged.
func (s *procShell) evEvalRes(data json.RawMessage) {
	var m procEvalRes
	if json.Unmarshal(data, &m) == nil {
		deliverEval(m.ID, m.Result)
	}
}

func (s *procShell) evAction(data json.RawMessage) {
	var m procAct
	if json.Unmarshal(data, &m) == nil && s.onAction != nil {
		s.onAction(m.Payload)
	}
}

// evWin re-homes the window signals the in-proc subclass fed the governor directly. Same inputs,
// same governor, one hop later - plus the eval gate's size-move latch (the daemon HOLDS during a
// drag; the child never buffers - see the protocol doc).
func (s *procShell) evWin(data json.RawMessage) {
	var m procWin
	if json.Unmarshal(data, &m) != nil {
		return
	}
	if m.Hidden {
		if onWindowHidden != nil {
			onWindowHidden()
		}
		return
	}
	s.sizeMv.Store(m.SizeMove)
	governor.SetFocused(m.Focused)
	governor.SetMinimized(m.Minimized)
	governor.SetSizeMove(m.SizeMove)
}

func (s *procShell) evGone(json.RawMessage) { s.finish() }

// onChildReady runs after every successful init handshake. First one = the window exists (fire the
// UI's onReady); every later one = a RESTART, so the page is rebuilt from state.
func (s *procShell) onChildReady() {
	s.pushStream(governor.Snapshot().Streaming, true)
	if s.gens.Add(1) == 1 {
		if s.onReady != nil {
			go s.onReady()
		}
		return
	}
	s.log.Warn("webui", "webview child restarted - rebuilding the page from state", map[string]any{
		"generation": s.gens.Load(),
	})
	if s.onReattach != nil {
		go s.onReattach()
	}
}

func (s *procShell) onChildDown() { s.hwndV.Store(0) }

// ── shell seam ──

// run spawns + supervises the child and blocks until the window is gone (or terminate finished),
// mirroring the in-proc message loop. A child CRASH never returns here: the Host restarts it and
// the page is rebuilt (onReattach).
func (s *procShell) run(initialHTML string, startHidden bool) {
	s.mu.Lock()
	s.lastHTML, s.hidden = initialHTML, startHidden
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.writer()
	go s.wedgeWatch()
	governor.OnChange(s.onGovernor)
	if err := s.host.Start(ctx); err != nil {
		s.log.Error("webui", "webview child could not start", map[string]any{"error": err.Error()})
		s.finish()
	}
	<-s.done
	cancel()
	close(s.stopW)
	s.host.Stop()
}

func (s *procShell) setHTML(html string) {
	s.mu.Lock()
	s.lastHTML = html
	s.mu.Unlock()
	s.sendOrdered(procEvDoc, func(seq uint64) any { return procDoc{Seq: seq, HTML: html} })
}

func (s *procShell) eval(js string) {
	s.sendOrdered(procEvEval, func(seq uint64) any { return procEval{Seq: seq, JS: js} })
}

// evalDirect is the ctl lane (control.go evalValue): its own frame, drained ahead of the ordered
// lane, so a flooded batch stream can never deadlock a ctl round-trip.
func (s *procShell) evalDirect(js string) { s.sendDirect(procEvXEval, procXEval{JS: js}) }

func (s *procShell) resize(w, h int) { s.sendDirect(procEvResize, procResize{W: w, H: h}) }
func (s *procShell) show()           { s.sendDirect(procEvShow, struct{}{}) }

func (s *procShell) post(payload string) bool {
	return s.sendDirect(procEvAct, procAct{Payload: payload})
}

func (s *procShell) hwnd() uintptr { return uintptr(s.hwndV.Load()) }

// terminate re-homes the in-proc force-exit watchdog OUTWARD: ask the child to close, then kill it.
// "Webview wedged" now means "child killed", never "daemon hangs" - run() returns either way, so
// the daemon's normal shutdown (module stop, bbolt close, stream end) always executes.
func (s *procShell) terminate() {
	s.sendDirect(procEvQuit, procQuit{GraceMS: int(procQuitGrace / time.Millisecond)})
	go func() {
		select {
		case <-s.done:
		case <-time.After(procQuitGrace):
			s.log.Warn("webui", "webview child did not close in grace - stopping it", nil)
			s.finish() // unblock run(): its deferred host.Stop() reaps (graceful stop, then KillTree)
		}
	}()
}

// inSizeMove reports the child's window is mid drag/resize (the eval gate's predicate).
func (s *procShell) inSizeMove() bool { return s.sizeMv.Load() }

// Stats exposes the child's supervision counters (restarts + last error) for ctl/perf reporting.
func (s *procShell) Stats() (generations uint64, ordDropped uint64, restarts int, lastErr string) {
	r, e := s.host.Stats()
	return s.gens.Load(), s.dropped.Load(), r, e
}

func (s *procShell) finish() { s.doneOnce.Do(func() { close(s.done) }) }

// ── lanes ──

// sendOrdered enqueues one FIFO frame. Cap policy: drop-oldest + onDrop (cache wipe).
func (s *procShell) sendOrdered(ev string, mk func(seq uint64) any) {
	f := procFrame{ev: ev, data: mk(s.seq.Add(1))}
	for {
		select {
		case s.ord <- f:
			return
		default:
		}
		select {
		case <-s.ord: // drop-oldest
			s.dropped.Add(1)
			if s.onDrop != nil {
				s.onDrop()
			}
		default:
		}
	}
}

// sendDirect enqueues one direct-lane frame; false = queue full (caller must fail, not stall).
func (s *procShell) sendDirect(ev string, data any) bool {
	select {
	case s.dir <- procFrame{ev: ev, data: data}:
		return true
	default:
		return false
	}
}

// writer is the SINGLE writer of the child's stdin: it drains the direct lane first, then the
// ordered lane, so both share one pipe without the ordered stream ever heading the direct one.
// It stamps each write so the wedge watchdog can tell "child stopped reading" from "child is slow".
func (s *procShell) writer() {
	for {
		select {
		case <-s.stopW:
			return
		case f := <-s.dir:
			s.write(f)
			continue
		default:
		}
		select {
		case <-s.stopW:
			return
		case f := <-s.dir:
			s.write(f)
		case f := <-s.ord:
			s.write(f)
		}
	}
}

func (s *procShell) write(f procFrame) {
	s.writeAt.Store(time.Now().UnixNano())
	defer s.writeAt.Store(0)
	send := s.sendFn
	if send == nil {
		send = s.host.Send
	}
	if err := send(f.ev, f.data); err != nil && !errors.Is(err, os.ErrClosed) {
		s.log.Debug("webui", "webview child send failed", map[string]any{"event": f.ev, "error": err.Error()})
	}
}

// wedgeWatch kills a child that stopped reading its stdin (a blocked pipe write). Killing it closes
// the pipe, so the write returns, the Host's session ends, and supervision restarts + reattaches.
func (s *procShell) wedgeWatch() {
	t := time.NewTicker(procWriteGrace / 4)
	defer t.Stop()
	for {
		select {
		case <-s.stopW:
			return
		case <-t.C:
		}
		at := s.writeAt.Load()
		if at == 0 || time.Since(time.Unix(0, at)) < procWriteGrace {
			continue
		}
		s.log.Error("webui", "webview child stopped reading stdin - killing to restart", map[string]any{
			"blockedFor": time.Since(time.Unix(0, at)).Truncate(time.Millisecond).String(),
		})
		s.host.Kill()
	}
}

// onGovernor forwards the one governor signal the child cannot observe itself (focus/minimize/
// size-move it owns; a live stream is the daemon's knowledge) so the child's own governor reaches
// the SAME below-normal decision the single in-proc process used to make.
func (s *procShell) onGovernor(sig governor.Signals) {
	select {
	case <-s.done: // retired shell: never touch a dead session's lanes
		return
	default:
	}
	s.pushStream(sig.Streaming, false)
}

func (s *procShell) pushStream(on bool, force bool) {
	if !force && s.streamV.Load() == on {
		return
	}
	s.streamV.Store(on)
	s.sendDirect(procEvStream, procStream{On: on})
}
