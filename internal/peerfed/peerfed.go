// Package peerfed is the shared arm/disarm loop for "via peer" session federation:
// when THIS instance has no local session for an external platform (VRChat, Twitch,
// GitHub) but a paired instance does, the first healthy peer serves that platform's
// features as if signed in locally. One loop, one set of semantics, reused per platform
// (a third hand-rolled copy is a defect - workspace UI/discovery rule).
//
// Semantics (identical for every platform):
//   - immediate first pass, then every Interval;
//   - a LOCAL session always wins: while LocalActive() is true the loop stays disarmed;
//   - the FIRST peer whose Probe reports ok gets armed;
//   - the armed peer is re-probed each pass and kept while still healthy;
//   - the serving peer vanishing (dropped from Peers) or going unhealthy disarms.
//
// Tokens/cookies never cross the link: Probe/Arm operate over the platform's remotectl
// proxy, which the serving side gates. peerfed only decides WHEN to arm.
package peerfed

import (
	"context"
	"time"
)

// Peer is one candidate serving instance: its stable node id + a display name.
type Peer struct {
	NodeID string
	Name   string
}

// Watcher runs the arm/disarm loop for one platform. I is the per-platform probe result
// (e.g. remotectl.VrcStatus) handed to Arm. All callbacks run on the watcher goroutine.
type Watcher[I any] struct {
	// Interval between passes after the immediate first one.
	Interval time.Duration
	// ProbeTimeout bounds a single Probe call (default 5s).
	ProbeTimeout time.Duration
	// LocalActive reports whether a LOCAL session is live (federation stays disarmed).
	LocalActive func() bool
	// Peers lists the current candidate serving instances (already filtered to connected).
	Peers func() []Peer
	// Probe asks one peer whether it can serve. ok=false (or err) means "not this peer".
	Probe func(ctx context.Context, nodeID string) (info I, ok bool, err error)
	// Arm binds federation to the named serving peer with its probed identity.
	Arm func(nodeID, name string, info I)
	// Disarm drops federation (local login won, or the serving peer is gone/unhealthy).
	Disarm func()
}

// Run drives the loop until ctx is done. Blocks; start it on its own goroutine.
func (w Watcher[I]) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	armed := w.step(ctx, "") // immediate first pass
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			armed = w.step(ctx, armed)
		}
	}
}

// step runs one pass. armed = the node id serving federation before the pass; the return
// value is the node id serving it after. Pure w.r.t. the loop state, so a test drives the
// state machine by calling it directly (a fake clock = manual stepping).
func (w Watcher[I]) step(ctx context.Context, armed string) string {
	// Local session always wins - disarm any federation and stop.
	if w.LocalActive() {
		if armed != "" {
			w.Disarm()
		}
		return ""
	}
	for _, p := range w.Peers() {
		info, ok, err := w.probe(ctx, p.NodeID)
		if err != nil || !ok {
			if armed == p.NodeID { // the serving peer went unhealthy
				w.Disarm()
				armed = ""
			}
			continue
		}
		if armed == p.NodeID {
			return armed // serving peer still healthy
		}
		w.Arm(p.NodeID, p.Name, info)
		return p.NodeID
	}
	// No healthy peer this pass - the serving peer vanished from the list.
	if armed != "" {
		w.Disarm()
	}
	return ""
}

func (w Watcher[I]) probe(ctx context.Context, nodeID string) (I, bool, error) {
	to := w.ProbeTimeout
	if to <= 0 {
		to = 5 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	return w.Probe(pctx, nodeID)
}
