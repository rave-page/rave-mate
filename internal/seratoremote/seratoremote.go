// Package seratoremote implements the Serato Remote real-time OSC-over-TCP protocol as a
// live per-deck now-playing source. Serato DJ Pro is the TCP CLIENT: we advertise a
// Bonjour/mDNS service (_SeratoIOSRemote._tcp, empty TXT) + run a TCP server; Serato
// browses, connects inbound (two parallel streams), and streams its state to us. We drive
// the handshake as the server side and parse /Status/... messages into typed per-deck
// events (deck change, playhead, loop, mixer).
//
// Protocol derived from chrisle/serato-connect (MIT, Copyright (c) 2026 Chris Le) - its
// docs/protocol.md wire spec and src/remote/* TypeScript implementation. The framing +
// OSC test vectors here are ports of that project's tests for parity. Mirrors the credit
// style of internal/serato (adat ids). The MIT notice is reproduced in THIRD_PARTY_NOTICES.
//
// Isolation note: this receiver runs IN-PROCESS (panic-guarded via debuglog.Go), NOT as a
// featurehost child, matching the sibling live network sources (prodjlinksrc UDP,
// virtualdjsrc OS2L). Rationale: the wire carries tiny control messages (a track string or
// 3 floats), not media/audio frames; the read buffer is fixed and the frameReader is hard-
// bounded (drop-connection on overrun); playhead is coalesced to <=~10 Hz per deck before
// it reaches the merger; and it is pure stdlib net (no fault-prone cgo). If a real capture
// shows a sustained flood that in-proc bounding can't absorb, promote to a featurehost
// child - the Receiver is already event/callback-shaped for a proxy seam.
//
// HANDSHAKE STATUS: the Authorize/Pair exchange is UNVERIFIED end-to-end (serato-connect's
// capture stalled after Authorize/Response). The Receiver ships an opt-in debug capture
// mode that logs every inbound frame so a live session against real Serato can finish the
// reverse-engineering. See docs and the session-level source wrapper.
package seratoremote

import (
	"context"
	"os"
	"sync"
	"time"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/osc"
)

// Options configures a Receiver.
type Options struct {
	PeerName        string   // Bonjour instance label; default "rave-mate"
	PeerUUID        string   // Pair peerUUID (,ssi arg 2); default a fixed rave-mate UUID
	Host            string   // TCP bind host; default "0.0.0.0"
	Port            int      // TCP port; 0 = OS-assigned (advertised via SRV)
	SubscribeTopics []string // default DefaultSubscriptionTopics
	MaxFrameBytes   int      // per-packet cap; 0 = defaultMaxFrameBytes
	Debug           bool     // log every inbound frame (handshake capture)
	// PlayheadInterval coalesces per-deck playhead emits. 0 = 100ms (~10 Hz).
	PlayheadInterval time.Duration
}

// Receiver publishes the Bonjour advert, accepts Serato's inbound TCP streams, drives the
// handshake, and surfaces typed per-deck events via Callbacks. Not safe to Start twice.
type Receiver struct {
	opts Options
	cb   Callbacks
	log  *logbus.Bus

	mu     sync.Mutex
	srv    *server
	adv    *discovery.Advertiser
	tracks [NumDecks]*Track
	loops  [NumDecks]Loop
	lastPH [NumDecks]time.Time
	xfader float32
	hasXF  bool
}

// New builds a Receiver. cb may have nil fields.
func New(opts Options, cb Callbacks, log *logbus.Bus) *Receiver {
	if opts.PeerName == "" {
		opts.PeerName = "rave-mate"
	}
	if opts.PeerUUID == "" {
		opts.PeerUUID = defaultPeerUUID
	}
	if opts.Host == "" {
		opts.Host = "0.0.0.0"
	}
	if len(opts.SubscribeTopics) == 0 {
		opts.SubscribeTopics = DefaultSubscriptionTopics
	}
	if opts.PlayheadInterval <= 0 {
		opts.PlayheadInterval = 100 * time.Millisecond
	}
	return &Receiver{opts: opts, cb: cb, log: log}
}

// Start binds the TCP server, publishes the Bonjour advert, and runs until ctx is
// cancelled. It returns after setup; the accept + read loops run in panic-guarded
// goroutines. Blocks on ctx.Done() then tears everything down.
func (r *Receiver) Start(ctx context.Context) error {
	srv, port, err := listen(ctx, r.opts.Host, r.opts.Port, r.opts.SubscribeTopics, r.opts.PeerName, r.opts.PeerUUID, r.opts.MaxFrameBytes, r.opts.Debug, r.cb, r.routeStatus, r.log)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "rave-mate"
	}
	adv := discovery.NewAdvertiser(ServiceType, r.opts.PeerName+" @ "+host, host, port, nil, r.log)
	if aerr := adv.Start(ctx); aerr != nil {
		srv.close()
		return aerr
	}
	r.mu.Lock()
	r.srv, r.adv = srv, adv
	r.mu.Unlock()
	r.log.Info("seratoremote", "listening", map[string]any{"port": port, "instance": adv.InstanceName(), "debug": r.opts.Debug})

	<-ctx.Done()
	r.stop()
	return nil
}

func (r *Receiver) stop() {
	r.mu.Lock()
	srv, adv := r.srv, r.adv
	r.srv, r.adv = nil, nil
	for i := range r.tracks {
		r.tracks[i] = nil
		r.loops[i] = Loop{}
	}
	r.hasXF = false
	r.mu.Unlock()
	if adv != nil {
		adv.Stop()
	}
	if srv != nil {
		srv.close()
	}
}

// routeStatus applies one /Status/* message to the per-deck state and fires typed
// callbacks. Serialized by mu (both Serato streams may deliver concurrently). Deck indices
// on the wire are 0-based 0..3 (UNVERIFIED end-to-end per docs/open-questions.md).
func (r *Receiver) routeStatus(m osc.Message) {
	// Crossfader is mixer-wide: no deck index.
	if m.Address == pathCrossfader {
		v, ok := floatArg(m.Args, 0)
		if !ok {
			return
		}
		r.mu.Lock()
		if r.hasXF && r.xfader == v {
			r.mu.Unlock()
			return
		}
		r.xfader, r.hasXF = v, true
		r.mu.Unlock()
		if r.cb.OnMixer != nil {
			r.cb.OnMixer(MixerEvent{Crossfader: v, HasCrossfader: true})
		}
		return
	}

	deck, ok := intArg(m.Args, 0)
	if !ok || deck < 0 || deck >= NumDecks {
		return
	}

	switch m.Address {
	case pathSongTitle:
		r.patchTrack(deck, func(t *Track) { t.Title, _ = stringArg(m.Args, 1) })
	case pathSongArtist:
		r.patchTrack(deck, func(t *Track) { t.Artist, _ = stringArg(m.Args, 1) })
	case pathSongFilepath:
		r.patchTrack(deck, func(t *Track) { t.FilePath, _ = stringArg(m.Args, 1) })
	case pathSongValid:
		valid := boolArg(m.Args, 1)
		if !valid {
			r.ejectDeck(deck)
			return
		}
		r.patchTrack(deck, func(t *Track) { t.Valid, t.HasValid = true, true })
	case pathPlayhead:
		r.emitPlayhead(deck, m.Args)
	case pathAutoLoopOn:
		r.patchLoop(deck, func(l *Loop) { l.AutoLoopOn = boolArg(m.Args, 1) })
	case pathBeatLength:
		if v, okf := floatArg(m.Args, 1); okf {
			r.patchLoop(deck, func(l *Loop) { l.BeatLength = v })
		}
	case pathLoopRollOn:
		r.patchLoop(deck, func(l *Loop) { l.LoopRollOn = boolArg(m.Args, 1) })
	case pathUpfader:
		if v, okf := floatArg(m.Args, 1); okf && r.cb.OnMixer != nil {
			r.cb.OnMixer(MixerEvent{Deck: deck, Upfader: v, HasUpfader: true})
		}
	}
}

func (r *Receiver) patchTrack(deck int, patch func(*Track)) {
	r.mu.Lock()
	prev := r.tracks[deck]
	next := Track{}
	if prev != nil {
		next = *prev
	}
	patch(&next)
	if prev != nil && *prev == next {
		r.mu.Unlock()
		return
	}
	cp := next
	r.tracks[deck] = &cp
	r.mu.Unlock()
	if r.cb.OnDeckChange != nil {
		r.cb.OnDeckChange(DeckChange{Deck: deck, Track: &cp, Previous: prev})
	}
}

func (r *Receiver) ejectDeck(deck int) {
	r.mu.Lock()
	prev := r.tracks[deck]
	if prev == nil {
		r.mu.Unlock()
		return
	}
	r.tracks[deck] = nil
	r.mu.Unlock()
	if r.cb.OnDeckChange != nil {
		r.cb.OnDeckChange(DeckChange{Deck: deck, Track: nil, Previous: prev})
	}
}

func (r *Receiver) patchLoop(deck int, patch func(*Loop)) {
	r.mu.Lock()
	next := r.loops[deck]
	patch(&next)
	if next == r.loops[deck] {
		r.mu.Unlock()
		return
	}
	r.loops[deck] = next
	r.mu.Unlock()
	if r.cb.OnLoop != nil {
		r.cb.OnLoop(LoopEvent{Deck: deck, Loop: next})
	}
}

// emitPlayhead coalesces per-deck playhead to <= 1/PlayheadInterval so a high-rate stream
// can't flood the merger. The three floats' semantics are UNVERIFIED (open-questions.md).
func (r *Receiver) emitPlayhead(deck int, args []osc.Arg) {
	a, ok0 := floatArg(args, 1)
	b, ok1 := floatArg(args, 2)
	c, ok2 := floatArg(args, 3)
	if !ok0 || !ok1 || !ok2 {
		return
	}
	now := time.Now()
	r.mu.Lock()
	if now.Sub(r.lastPH[deck]) < r.opts.PlayheadInterval {
		r.mu.Unlock()
		return
	}
	r.lastPH[deck] = now
	r.mu.Unlock()
	if r.cb.OnPlayhead != nil {
		r.cb.OnPlayhead(PlayheadEvent{Deck: deck, Playhead: Playhead{
			PositionSeconds: a, LengthSeconds: b, BPM: c, Raw: [3]float32{a, b, c},
		}})
	}
}

// ── arg helpers ──────────────────────────────────────────────────────────────

func intArg(args []osc.Arg, i int) (int, bool) {
	if i >= len(args) {
		return 0, false
	}
	switch args[i].Kind {
	case osc.KindInt:
		return int(args[i].Int), true
	case osc.KindFloat:
		return int(args[i].Float + 0.5), true
	}
	return 0, false
}

func floatArg(args []osc.Arg, i int) (float32, bool) {
	if i >= len(args) {
		return 0, false
	}
	switch args[i].Kind {
	case osc.KindFloat:
		return args[i].Float, true
	case osc.KindInt:
		return float32(args[i].Int), true
	}
	return 0, false
}

func stringArg(args []osc.Arg, i int) (string, bool) {
	if i >= len(args) || args[i].Kind != osc.KindString {
		return "", false
	}
	return args[i].Str, true
}

// boolArg reads a "boolean as float" arg (protocol convention 1.0/0.0); >=0.5 is true.
func boolArg(args []osc.Arg, i int) bool {
	v, ok := floatArg(args, i)
	return ok && v >= 0.5
}
