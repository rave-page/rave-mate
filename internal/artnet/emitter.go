package artnet

import (
	"context"
	"net"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

// Emitter builds + sends spec-correct ArtDmx. It caps output to ≤44Hz per universe with
// change-detection (a frame is sent only when its slots differ) and a ~1Hz keep-alive resend of
// the last frame so a node that joined late still gets state. SendDMX is a thread-safe enqueue;
// the Run loop does the rate-limited sending.
//
// Targeting: if a universe has ArtPoll-discovered subscribers (Subscribe), frames unicast to each;
// otherwise they go to the configured default target (broadcast by default). Populating subscribers
// requires an ArtPoll/ArtPollReply exchange initiated by us - left as a future extension; today the
// default target (broadcast) is the shipped path.
type Emitter struct {
	log     *logbus.Bus
	conn    *net.UDPConn
	dflt    *net.UDPAddr
	tickDur time.Duration
	keepDur time.Duration

	mu    sync.Mutex
	state map[uint16]*emitState
	subs  map[uint16]map[string]*net.UDPAddr
}

type emitState struct {
	pending  []byte // latest slots awaiting send (nil once flushed & clean)
	last     [512]byte
	haveLast bool
	seq      byte
	lastSent time.Time
}

// NewEmitter dials target ("255.255.255.255:6454" broadcast if empty). The returned emitter must
// be Run to actually send.
func NewEmitter(log *logbus.Bus, target string) (*Emitter, error) {
	if target == "" {
		target = "255.255.255.255:6454"
	}
	ua, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", nil) // ephemeral local port; per-packet dest via WriteToUDP
	if err != nil {
		return nil, err
	}
	return &Emitter{
		log: log, conn: conn, dflt: ua,
		tickDur: 23 * time.Millisecond, // ~44Hz
		keepDur: time.Second,
		state:   map[uint16]*emitState{},
		subs:    map[uint16]map[string]*net.UDPAddr{},
	}, nil
}

// SendDMX queues data for universe (change-detected + rate-capped by Run). Copies data.
func (e *Emitter) SendDMX(universe uint16, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	e.mu.Lock()
	st := e.state[universe]
	if st == nil {
		st = &emitState{}
		e.state[universe] = st
	}
	st.pending = cp
	e.mu.Unlock()
}

// Subscribe adds a unicast destination for a universe (future ArtPoll-discovered output node).
func (e *Emitter) Subscribe(universe uint16, addr *net.UDPAddr) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.subs[universe] == nil {
		e.subs[universe] = map[string]*net.UDPAddr{}
	}
	e.subs[universe][addr.String()] = addr
}

// Run flushes queued frames at ≤44Hz/universe + keep-alives at ~1Hz until ctx cancels.
func (e *Emitter) Run(ctx context.Context) error {
	t := time.NewTicker(e.tickDur)
	defer t.Stop()
	defer func() { _ = e.conn.Close() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			e.flush(now)
		}
	}
}

// flush sends changed frames (change-detection) + keep-alive resends of idle universes.
func (e *Emitter) flush(now time.Time) {
	type job struct {
		uni  uint16
		data []byte
		seq  byte
	}
	var jobs []job
	e.mu.Lock()
	for uni, st := range e.state {
		send := false
		if st.pending != nil {
			// change-detect vs last sent frame.
			if !st.haveLast || !equalSlots(st.last, st.pending) {
				var l [512]byte
				copy(l[:], st.pending)
				st.last = l
				st.haveLast = true
				send = true
			}
			st.pending = nil
		}
		if !send && st.haveLast && now.Sub(st.lastSent) >= e.keepDur {
			send = true // keep-alive resend of last frame
		}
		if send {
			st.seq++
			if st.seq == 0 {
				st.seq = 1
			}
			st.lastSent = now
			jobs = append(jobs, job{uni: uni, data: st.last[:], seq: st.seq})
		}
	}
	e.mu.Unlock()
	for _, j := range jobs {
		pkt := BuildArtDmx(j.uni, j.seq, 0, j.data)
		for _, dst := range e.targets(j.uni) {
			if _, err := e.conn.WriteToUDP(pkt, dst); err != nil {
				e.log.Debug(source, "art-dmx send failed", map[string]any{"universe": j.uni, "to": dst.String(), "error": err.Error()})
			}
		}
	}
}

// targets returns the unicast subscribers for a universe, or the default target if none.
func (e *Emitter) targets(uni uint16) []*net.UDPAddr {
	e.mu.Lock()
	defer e.mu.Unlock()
	if m := e.subs[uni]; len(m) > 0 {
		out := make([]*net.UDPAddr, 0, len(m))
		for _, a := range m {
			out = append(out, a)
		}
		return out
	}
	return []*net.UDPAddr{e.dflt}
}

func equalSlots(a [512]byte, b []byte) bool {
	if len(b) > 512 {
		b = b[:512]
	}
	for i := 0; i < len(b); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	// bytes past len(b) in a must be zero to match a shorter frame.
	for i := len(b); i < 512; i++ {
		if a[i] != 0 {
			return false
		}
	}
	return true
}
