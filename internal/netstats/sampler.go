package netstats

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// Totals are cumulative byte counters + the latest per-peer RTT, fetched once per tick.
type Totals struct {
	PeerIn, PeerOut uint64               // peerlink wire bytes, all peers summed
	APIIn, APIOut   uint64               // API HTTP body bytes
	RTT             map[string]RTTSample // by peer node id; key present = peer connected
}

// RTTSample is one peer's latest round-trip measurement. Ms = NaN until the first pong.
type RTTSample struct {
	Label string
	Ms    float64
}

// RTTSeries is one peer's RTT history for the TIMING graph.
type RTTSeries struct {
	NodeID, Label string
	Ms            []float64 // oldest→newest; NaN = no sample yet
	LatestMs      float64   // valid when Has
	Has           bool
}

// Snapshot is a UI-ready copy of every series. Rates are bytes/sec, oldest→newest.
type Snapshot struct {
	PeerIn, PeerOut, APIIn, APIOut                 []float64
	SessPeerIn, SessPeerOut, SessAPIIn, SessAPIOut uint64 // session totals (counter-reset proof)
	RTT                                            []RTTSeries
	Span                                           int
}

type rttState struct {
	label string
	ring  *Ring
}

// Sampler folds periodic Totals readings into per-series rate rings.
type Sampler struct {
	span int

	mu     sync.Mutex
	prev   Totals
	prevAt time.Time
	have   bool

	peerIn, peerOut, apiIn, apiOut                 *Ring
	sessPeerIn, sessPeerOut, sessAPIIn, sessAPIOut uint64
	rtt                                            map[string]*rttState
}

// NewSampler returns a sampler keeping span samples per series.
func NewSampler(span int) *Sampler {
	return &Sampler{
		span:   span,
		peerIn: NewRing(span), peerOut: NewRing(span), apiIn: NewRing(span), apiOut: NewRing(span),
		rtt: map[string]*rttState{},
	}
}

// Tick folds one totals reading taken at now into the rings. First call sets the baseline
// only. Deterministic in (now, t) - no wall clock - so tests drive it directly.
func (s *Sampler) Tick(now time.Time, t Totals) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have {
		dt := now.Sub(s.prevAt).Seconds()
		if dt > 0 {
			s.peerIn.Push(rate(t.PeerIn, s.prev.PeerIn, dt, &s.sessPeerIn))
			s.peerOut.Push(rate(t.PeerOut, s.prev.PeerOut, dt, &s.sessPeerOut))
			s.apiIn.Push(rate(t.APIIn, s.prev.APIIn, dt, &s.sessAPIIn))
			s.apiOut.Push(rate(t.APIOut, s.prev.APIOut, dt, &s.sessAPIOut))
		}
	}
	s.prev, s.prevAt, s.have = t, now, true

	// RTT rings track the current peer set; departed peers are pruned.
	for id := range s.rtt {
		if _, ok := t.RTT[id]; !ok {
			delete(s.rtt, id)
		}
	}
	for id, sm := range t.RTT {
		st := s.rtt[id]
		if st == nil {
			st = &rttState{ring: NewRing(s.span)}
			s.rtt[id] = st
		}
		st.label = sm.Label
		st.ring.Push(sm.Ms)
	}
}

// rate converts a counter delta into B/s and accrues the session total. A counter that
// shrank (connection replaced → fresh counters) contributes its new value as the delta.
func rate(cur, prev uint64, dt float64, sess *uint64) float64 {
	if cur < prev {
		prev = 0
	}
	d := cur - prev
	*sess += d
	return float64(d) / dt
}

// Snapshot copies every series for rendering.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		PeerIn: s.peerIn.Values(), PeerOut: s.peerOut.Values(),
		APIIn: s.apiIn.Values(), APIOut: s.apiOut.Values(),
		SessPeerIn: s.sessPeerIn, SessPeerOut: s.sessPeerOut,
		SessAPIIn: s.sessAPIIn, SessAPIOut: s.sessAPIOut,
		Span: s.span,
	}
	for id, st := range s.rtt {
		rs := RTTSeries{NodeID: id, Label: st.label, Ms: st.ring.Values()}
		if v, ok := st.ring.Latest(); ok && !math.IsNaN(v) {
			rs.LatestMs, rs.Has = v, true
		}
		out.RTT = append(out.RTT, rs)
	}
	sort.Slice(out.RTT, func(i, j int) bool {
		if out.RTT[i].Label != out.RTT[j].Label {
			return out.RTT[i].Label < out.RTT[j].Label
		}
		return out.RTT[i].NodeID < out.RTT[j].NodeID
	})
	return out
}

// Run ticks the sampler with fetch() at interval until ctx ends. Call in its own goroutine.
func (s *Sampler) Run(ctx context.Context, interval time.Duration, fetch func() Totals) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			s.Tick(now, fetch())
		}
	}
}
