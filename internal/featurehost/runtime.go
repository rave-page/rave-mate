package featurehost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

// Feature is one subprocess-hosted feature (child side). Init configures it from the
// daemon's init params. Start blocks serving until ctx is done; returning a non-nil
// error (or panicking) makes the child exit non-zero so the host restarts it. Handle
// serves feature-specific control requests (each on its own guarded goroutine).
type Feature interface {
	Init(params json.RawMessage, rt *Runtime) error
	Start(ctx context.Context) error
	Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)
}

// EventHandler is implemented by features that consume parent→child events (fire-and-
// forget frames, e.g. high-rate merged updates - no per-frame response round trip).
type EventHandler interface {
	HandleEvent(event string, data json.RawMessage)
}

// heartbeatCoalesce caps how often Beat() actually hits the wire (a hung loop is detected in
// seconds, so sub-second beat traffic buys nothing).
const heartbeatCoalesce = 500 * time.Millisecond

// Runtime is the child's handle back to the daemon: typed events + a local logbus whose
// entries are forwarded as "log" events (and land in the daemon's bus with their source).
type Runtime struct {
	w   *wire
	Log *logbus.Bus

	hbMu   sync.Mutex
	hbLast time.Time
}

// Emit sends one unsolicited event to the daemon (e.g. "obs", "state", "capture").
func (rt *Runtime) Emit(event string, data any) { rt.w.event(event, data) }

// Beat pings the host to prove the feature's main loop is still making progress. Call it from the
// top of that loop; it coalesces to heartbeatCoalesce so per-tick calls are free. If beats stop
// (loop wedged) the host force-restarts the child - only meaningful when the Host set a
// HeartbeatTimeout.
func (rt *Runtime) Beat() {
	rt.hbMu.Lock()
	if !rt.hbLast.IsZero() && time.Since(rt.hbLast) < heartbeatCoalesce {
		rt.hbMu.Unlock()
		return
	}
	rt.hbLast = time.Now()
	rt.hbMu.Unlock()
	rt.w.event(EventHeartbeat, nil)
}

// newChildMonitor returns a logbus whose entries forward to the daemon as `event` frames
// (for per-interface monitor buses like traktorMon/midiMon). Lives for the child process.
func newChildMonitor(rt *Runtime, event string) *logbus.Bus {
	bus := logbus.New(64)
	ch, _ := bus.Subscribe()
	go func() {
		for e := range ch {
			rt.Emit(event, entryToLogEvent(e))
		}
	}()
	return bus
}

func errUnknownMethod(m string) error { return fmt.Errorf("unknown method %s", m) }

// registry maps feature name → constructor. Each `rave-mate feature <name>` child hosts
// exactly one feature instance.
var registry = map[string]func() Feature{
	"crash": func() Feature { return &crashFeature{} },
}

// Register adds a feature constructor (called from feat_*.go init or app wiring).
func Register(name string, ctor func() Feature) { registry[name] = ctor }

// KnownFeature reports whether name has a registered constructor.
func KnownFeature(name string) bool {
	_, ok := registry[name]
	return ok
}

// RunFeature is the child entrypoint: host the named feature over stdio until stdin
// closes or a stop request arrives. Returns the process exit code. stdout is the pure
// JSON channel; diagnostics (incl. panic stacks) go to stderr, which the host scans
// into the daemon log.
func RunFeature(name string) int {
	ctor, ok := registry[name]
	if !ok {
		fmt.Fprintln(os.Stderr, "feature: unknown "+name)
		return 2
	}
	return serveFeature(ctor(), name, os.Stdin, os.Stdout)
}

// serveFeature runs the duplex loop over arbitrary streams (testable in-mem).
// Exit codes: 0 = clean stop (stop request / stdin EOF), 1 = feature failure (Start
// error or panic anywhere in the feature).
func serveFeature(f Feature, name string, in io.Reader, out io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "feature %s: panic: %v\n%s\n", name, r, debug.Stack())
			code = 1
		}
	}()

	w := newWire(out)
	bus := logbus.New(256)
	logCh, unsubLog := bus.Subscribe()
	defer unsubLog()
	go func() {
		for e := range logCh {
			w.event(EventLog, entryToLogEvent(e))
		}
	}()

	rt := &Runtime{w: w, Log: bus}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// fatal carries the first terminal verdict (clean stop vs failure).
	fatal := make(chan int, 1)
	die := func(c int) {
		select {
		case fatal <- c:
		default:
		}
	}
	var started sync.WaitGroup

	// guard runs fn; a panic is reported to stderr (host scans it into the log) and
	// kills the child non-zero - process isolation IS the containment here.
	guard := func(src string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "feature %s: %s panic: %v\n%s\n", name, src, r, debug.Stack())
				die(1)
			}
		}()
		fn()
	}

	inited := false
	// Reader: decode + dispatch frames until stdin closes (daemon gone → clean exit).
	go guard("reader", func() {
		dec := json.NewDecoder(bufio.NewReader(in))
		for {
			var fr frame
			if err := dec.Decode(&fr); err != nil {
				die(0) // EOF or daemon-side close: wind down quietly
				return
			}
			switch {
			case fr.Event != "": // parent→child event (no response)
				if eh, ok := f.(EventHandler); ok && inited {
					eh.HandleEvent(fr.Event, fr.Data) // inline: ordering matters; panic = die(1) via guard
				}
			case fr.Method == methodInit:
				if inited {
					w.respond(fr.ID, nil, fmt.Errorf("already initialized"))
					continue
				}
				if err := f.Init(fr.Params, rt); err != nil {
					w.respond(fr.ID, nil, err)
					die(1)
					return
				}
				inited = true
				started.Add(1)
				go guard("start", func() {
					defer started.Done()
					if err := f.Start(ctx); err != nil && ctx.Err() == nil {
						bus.Error("feature:"+name, "start failed", map[string]any{"error": err.Error()})
						die(1)
						return
					}
					if ctx.Err() == nil {
						die(0) // Start returned without error before stop - treat as done
					}
				})
				w.respond(fr.ID, nil, nil) // init OK = ready
			case fr.Method == methodStop:
				w.respond(fr.ID, nil, nil)
				die(0)
				return
			case fr.Method != "":
				id, method, params := fr.ID, fr.Method, fr.Params
				if !inited {
					w.respond(id, nil, fmt.Errorf("not initialized"))
					continue
				}
				// Handler panic: answer the caller, then fail the child so the host
				// restarts the feature in a known-good state.
				go func() {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "feature %s: handle %s panic: %v\n%s\n", name, method, r, debug.Stack())
							w.respond(id, nil, fmt.Errorf("panic: %v", r))
							die(1)
						}
					}()
					res, err := f.Handle(ctx, method, params)
					w.respond(id, res, err)
				}()
			}
		}
	})

	code = <-fatal
	cancel()
	// Bounded wait for Start to unwind (close listeners, flush files).
	done := make(chan struct{})
	go func() { started.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return code
}
