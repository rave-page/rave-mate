// Package dmx is the DMX-plane router: it wires sources into the shared universe store and fans
// the store out to sinks, per config.
//
//	sources: Art-Net listener (UDP :6454 ingest)
//	sinks:   VRSL video grid (Spout / PNG fallback), Art-Net re-emit to another target
//
// Peer plane (future, deliberately out of scope here): the store API is source/sink-agnostic -
// a peer source receiving universes over the peer bus calls store.Set exactly like the listener,
// and a peer sink polls store.Generation/Get like the grid loop. Adding either is a new goroutine
// in Run + a config gate; no store or router redesign needed.
package dmx

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/vrslgrid"
)

const source = "dmx"

// Router owns the DMX plane for one Run lifetime. cfgFn is re-read on each module (re)start, so
// settings edits apply on toggle off/on (the standard module pattern).
type Router struct {
	log     *logbus.Bus
	store   *artnet.Store
	cfgFn   func() config.DMXFeature
	pngPath string

	mu      sync.Mutex
	running bool
	gridErr string
	backend string
	frames  uint64
	lastPub time.Time
}

// New builds the router. pngPath is the grid's PNG-fallback output file.
func New(log *logbus.Bus, cfgFn func() config.DMXFeature, pngPath string) *Router {
	return &Router{log: log, store: artnet.NewStore(), cfgFn: cfgFn, pngPath: pngPath}
}

// Store exposes the universe store (read surface for future sinks; Set for future peer sources).
func (r *Router) Store() *artnet.Store { return r.store }

// Start launches the plane bound to ctx (module Start contract: non-blocking). The Art-Net
// listener failing to bind fails the module; grid/emit sinks degrade with a logged warning.
func (r *Router) Start(ctx context.Context) error {
	cfg := r.cfgFn()

	// Listener is the plane's reason to exist - probe the bind synchronously so a port clash
	// surfaces as a module-start error, then serve on a goroutine.
	lis := artnet.NewListener(r.log, r.store, "rave-mate", "rave-mate VRSL DMX bridge")
	errCh := make(chan error, 1)
	debuglog.Go(r.log, source, func() { errCh <- lis.Run(ctx, cfg.ResolvedListenAddr()) })
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("art-net listen %s: %w", cfg.ResolvedListenAddr(), err)
		}
	case <-time.After(300 * time.Millisecond):
		// bound and serving
	}

	if cfg.Grid.Enabled {
		debuglog.Go(r.log, source, func() { r.runGrid(ctx, cfg) })
	}
	if cfg.ReEmit {
		em, err := artnet.NewEmitter(r.log, cfg.ResolvedEmitTarget())
		if err != nil {
			r.log.Warn(source, "art-net re-emit unavailable", map[string]any{"target": cfg.ResolvedEmitTarget(), "error": err.Error()})
		} else {
			debuglog.Go(r.log, source, func() { _ = em.Run(ctx) })
			debuglog.Go(r.log, source, func() { r.runReEmit(ctx, em) })
		}
	}

	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	debuglog.Go(r.log, source, func() {
		<-ctx.Done()
		r.mu.Lock()
		r.running = false
		r.backend = ""
		r.mu.Unlock()
	})
	return nil
}

// runGrid renders the universe store into the VRSL grid at ≤fpsCap, only when a universe changed
// (dirty flag via store generation), with a ≥1fps keep-alive so receivers never see a frozen feed
// as "gone".
func (r *Router) runGrid(ctx context.Context, cfg config.DMXFeature) {
	pub := vrslgrid.NewPublisher(r.log, cfg.Grid.ResolvedSpoutName(), r.pngPath)
	defer pub.Close()
	r.mu.Lock()
	r.backend = pub.Name()
	r.mu.Unlock()
	r.log.Info(source, "vrsl grid sink up", map[string]any{"output": pub.Name(), "mode": string(vrslgrid.ParseMode(cfg.Grid.Mode))})

	mode := vrslgrid.ParseMode(cfg.Grid.Mode)
	unis := cfg.ResolvedUniverses()
	tick := time.NewTicker(time.Second / time.Duration(cfg.Grid.ResolvedFPSCap()))
	defer tick.Stop()
	var lastGen uint64
	var lastSend time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			gen := r.store.Generation()
			if !shouldRender(gen, lastGen, now, lastSend) {
				continue
			}
			lastGen = gen
			lastSend = now
			img := vrslgrid.Render(r.store, unis, mode)
			if err := pub.Publish(img); err != nil {
				r.setGridErr(err.Error())
				continue
			}
			r.setGridErr("")
			r.mu.Lock()
			r.frames++
			r.lastPub = now
			r.mu.Unlock()
		}
	}
}

// shouldRender is the grid dirty-flag: render when universe data changed (generation moved) or
// the ≥1fps keep-alive is due.
func shouldRender(gen, lastGen uint64, now, lastSend time.Time) bool {
	return gen != lastGen || now.Sub(lastSend) >= time.Second
}

// runReEmit forwards ingested universes to the emitter at ~44Hz on store change (the emitter does
// its own per-universe change-detection + keep-alive).
func (r *Router) runReEmit(ctx context.Context, em *artnet.Emitter) {
	tick := time.NewTicker(23 * time.Millisecond)
	defer tick.Stop()
	var lastGen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			gen := r.store.Generation()
			if gen == lastGen {
				continue
			}
			lastGen = gen
			for _, st := range r.store.Stats(time.Now(), time.Hour) {
				if d, ok := r.store.Get(st.Universe); ok {
					em.SendDMX(st.Universe, d[:])
				}
			}
		}
	}
}

func (r *Router) setGridErr(s string) {
	r.mu.Lock()
	if r.gridErr != s && s != "" {
		r.log.Warn(source, "grid publish failed", map[string]any{"error": s})
	}
	r.gridErr = s
	r.mu.Unlock()
}

// Status is the live plane snapshot for the settings card + ctl dmx-status.
type Status struct {
	Running     bool
	GridBackend string // output label ("Spout sender: …" / "PNG file: …"); "" = grid sink off
	GridFrames  uint64
	LastPublish time.Time
	GridErr     string
	Universes   []artnet.UniverseStat
}

// Status returns the live snapshot.
func (r *Router) Status() Status {
	r.mu.Lock()
	st := Status{Running: r.running, GridBackend: r.backend, GridFrames: r.frames, LastPublish: r.lastPub, GridErr: r.gridErr}
	r.mu.Unlock()
	st.Universes = r.store.Stats(time.Now(), 3*time.Second)
	return st
}

// StatusText renders the snapshot as the multi-line ctl reply.
func (r *Router) StatusText() string {
	st := r.Status()
	if !st.Running {
		return "dmx plane off (enable it in Settings → DMX / VRSL)"
	}
	out := "dmx plane running\n"
	if st.GridBackend != "" {
		out += fmt.Sprintf("grid: %s · %d frame(s)", st.GridBackend, st.GridFrames)
		if st.GridErr != "" {
			out += " · ERROR " + st.GridErr
		}
		out += "\n"
	} else {
		out += "grid: off\n"
	}
	if len(st.Universes) == 0 {
		return out + "no DMX received yet - point an Art-Net source at this machine's IP, port 6454"
	}
	for _, u := range st.Universes {
		out += fmt.Sprintf("universe %d: %.1f pkt/s · %d total · from %s · last %s ago\n",
			u.Universe, u.PPS, u.Packets, u.SourceIP, time.Since(u.LastSeen).Truncate(time.Millisecond*100))
	}
	return out
}
