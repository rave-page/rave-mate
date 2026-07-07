// Package vrcmidi bridges the DMX universe store into MIDI CC messages on a local MIDI
// output port, for VRChat's --midi ingestion (worlds with a VRC Midi Listener, VRSL/MDMX
// local preview - VRC-MIDIDMX-style).
//
// Safety invariant: VRChat's MIDI→Udon path crashes the client above ~128 MIDI events per
// frame, so the bridge NEVER streams raw DMX. It change-detects at MIDI resolution (a
// channel is dirty only when its 7-bit value moved), coalesces (a channel dirty N times
// between flushes sends once, latest value) and paces sends through a hard token-bucket
// cap (config, clamped ≤1000/s - under 128/frame down to ~8 fps).
//
// Address mapping (VRC-MIDIDMX convention): bridged universes are concatenated in config
// order into one 0-based channel space; index g → MIDI channel g/128 (0..15), CC g%128,
// value = DMX>>1. 16×128 = 2048 addresses = 4 universes max.
package vrcmidi

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
)

const (
	source       = "vrcmidi"
	tickInterval = 20 * time.Millisecond
)

// Out is the MIDI output the bridge sends on (satisfied by *midi.Output; fake in tests).
type Out interface {
	Send(status, data1, data2 byte)
	Close()
}

// Bridge is the DMX→MIDI bridge module. cfg is re-read on each (re)start.
type Bridge struct {
	log     *logbus.Bus
	store   artnet.Reader
	cfgFn   func() config.DMXMIDIFeature
	openOut func(device string) (Out, string, error) // injectable for tests

	mu      sync.Mutex
	running bool
	port    string
	rate    int
	sent    uint64
	backlog int
}

// New builds the bridge over the shared universe store.
func New(log *logbus.Bus, store artnet.Reader, cfgFn func() config.DMXMIDIFeature) *Bridge {
	return &Bridge{log: log, store: store, cfgFn: cfgFn, openOut: openMidiOut}
}

// openMidiOut is the production Out factory (winmm on Windows; ErrUnsupported elsewhere).
func openMidiOut(device string) (Out, string, error) {
	o, err := midi.OpenOutput(device)
	if err != nil {
		return nil, "", err
	}
	return o, o.Name, nil
}

// Start opens the MIDI port and launches the pacing loop (module Start contract:
// non-blocking, goroutines bound to ctx). Fails when no output port matches.
func (b *Bridge) Start(ctx context.Context) error {
	cfg := b.cfgFn()
	out, port, err := b.openOut(cfg.Device)
	if err != nil {
		return err
	}
	st := newState(cfg, b.store)
	b.mu.Lock()
	b.running, b.port, b.rate, b.sent, b.backlog = true, port, st.rate, 0, 0
	b.mu.Unlock()
	b.log.Info(source, "dmx→midi bridge up", map[string]any{
		"port": port, "universes": cfg.ResolvedUniverses(), "maxPerSecond": st.rate})
	debuglog.Go(b.log, source, func() {
		defer out.Close()
		tick := time.NewTicker(tickInterval)
		defer tick.Stop()
		last := time.Now()
		for {
			select {
			case <-ctx.Done():
				b.mu.Lock()
				b.running, b.port = false, ""
				b.mu.Unlock()
				return
			case now := <-tick.C:
				n := st.step(out, now.Sub(last))
				last = now
				b.mu.Lock()
				b.sent += uint64(n)
				b.backlog = st.dirtyCount
				b.mu.Unlock()
			}
		}
	})
	return nil
}

// Status is the live bridge snapshot for the settings card.
type Status struct {
	Running bool
	Port    string
	Rate    int    // effective messages/s cap
	Sent    uint64 // CC messages sent this run
	Backlog int    // dirty channels waiting for budget
}

// Status returns the live snapshot.
func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Status{Running: b.running, Port: b.port, Rate: b.rate, Sent: b.sent, Backlog: b.backlog}
}

// state is the pacing/diff engine (no locking - owned by the loop goroutine; tests drive
// step/scan/flush directly).
type state struct {
	store artnet.Reader
	unis  []uint16
	rate  int

	shadow [][512]byte // last 8-bit values acknowledged per bridged universe (zero-init:
	// first scan syncs every non-zero channel; zeros match the world's assumed default)
	dirty   [][512]bool
	snap    [][512]byte // scan snapshot; flush sends from here (coalesced latest value)
	lastGen uint64
	scanned bool // first scan done (lastGen 0 is a valid generation)

	dirtyCount int
	cursor     int     // round-robin resume point in the global channel space
	acc        float64 // fractional token-bucket accumulator
}

// newState builds the engine from config.
func newState(cfg config.DMXMIDIFeature, store artnet.Reader) *state {
	resolved := cfg.ResolvedUniverses()
	unis := make([]uint16, len(resolved))
	for i, u := range resolved {
		unis[i] = uint16(u)
	}
	return &state{
		store: store, unis: unis, rate: cfg.ResolvedRate(),
		shadow: make([][512]byte, len(unis)),
		dirty:  make([][512]bool, len(unis)), snap: make([][512]byte, len(unis)),
	}
}

// step scans for changes (when the store generation moved) and flushes up to the elapsed
// token budget. Returns messages sent.
func (s *state) step(out Out, elapsed time.Duration) int {
	s.scan()
	s.acc += float64(s.rate) * elapsed.Seconds()
	if lim := float64(s.rate); s.acc > lim {
		s.acc = lim // idle time never banks a burst above 1s worth
	}
	budget := int(s.acc)
	sent := s.flush(out, budget)
	s.acc -= float64(sent)
	return sent
}

// scan diffs the store against the shadow at MIDI (7-bit) resolution, marking dirty
// channels. Cheap when nothing changed (generation check).
func (s *state) scan() {
	gen := s.store.Generation()
	if s.scanned && gen == s.lastGen {
		return
	}
	s.lastGen = gen
	s.scanned = true
	for i, u := range s.unis {
		data, ok := s.store.Get(u)
		if !ok {
			continue
		}
		s.snap[i] = data
		for c := 0; c < 512; c++ {
			if s.dirty[i][c] {
				continue // still pending - snap already holds the latest value (coalesce)
			}
			if data[c]>>1 != s.shadow[i][c]>>1 {
				s.dirty[i][c] = true
				s.dirtyCount++
			}
		}
	}
}

// flush sends up to budget dirty channels round-robin from the cursor, latest snapshot
// value each. Returns messages sent.
func (s *state) flush(out Out, budget int) int {
	if budget <= 0 || s.dirtyCount == 0 {
		return 0
	}
	total := len(s.unis) * 512
	sent := 0
	for i := 0; i < total && sent < budget && s.dirtyCount > 0; i++ {
		g := (s.cursor + i) % total
		ui, c := g/512, g%512
		if !s.dirty[ui][c] {
			continue
		}
		v := s.snap[ui][c]
		out.Send(0xB0|byte(g/128%16), byte(g%128), v>>1)
		s.shadow[ui][c] = v
		s.dirty[ui][c] = false
		s.dirtyCount--
		sent++
		if sent == budget {
			s.cursor = (g + 1) % total
			return sent
		}
	}
	s.cursor = 0
	return sent
}
