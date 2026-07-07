package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/studio"
)

// peerGateway adapts the LAN peer link (discovery + peerlink + remotectl) to studio.PeerGateway
// so the web Local Studio can list/pair/forget rave-mate instances on other machines and route
// unary calls to a selected one. One instance, registered once with peerlink for SAS/state
// events; fans those out to per-session studio subscribers.
type peerGateway struct {
	peerMgr   *peerlink.Manager
	disc      *discovery.Discovery
	remoteCtl *remotectl.Endpoint

	mu         sync.Mutex
	seq        int
	changeSubs map[int]func()
	sasSubs    map[int]func(studio.PeerSAS)
}

func newPeerGateway(pm *peerlink.Manager, d *discovery.Discovery, rc *remotectl.Endpoint) *peerGateway {
	g := &peerGateway{
		peerMgr: pm, disc: d, remoteCtl: rc,
		changeSubs: map[int]func(){}, sasSubs: map[int]func(studio.PeerSAS){},
	}
	pm.AddListener(
		func(req peerlink.SASRequest) {
			g.fanSAS(studio.PeerSAS{NodeID: req.NodeID, Nickname: req.Nickname, SAS: req.SAS})
		},
		g.fanChange,
	)
	return g
}

// Peers merges remembered (offline list) + discovered (LAN) + live connections into the
// web context-switcher view, keyed by node id.
func (g *peerGateway) Peers() []studio.PeerInfo {
	byID := map[string]*studio.PeerInfo{}
	get := func(id string) *studio.PeerInfo {
		if p, ok := byID[id]; ok {
			return p
		}
		p := &studio.PeerInfo{NodeID: id, Status: "offline"}
		byID[id] = p
		return p
	}
	for _, r := range g.peerMgr.Remembered() {
		p := get(r.NodeID)
		p.Nickname, p.Trusted = r.Nickname, r.Trusted
	}
	for _, d := range g.disc.Peers() {
		p := get(d.NodeID)
		if p.Nickname == "" {
			p.Nickname = d.Name
		}
		p.Online = true
	}
	for _, c := range g.peerMgr.Connections() {
		p := get(c.NodeID)
		if c.Nickname != "" {
			p.Nickname = c.Nickname
		}
		p.Status, p.SAS, p.Online = string(c.Status), c.SAS, true
		p.Trusted = p.Trusted || c.Trusted
	}
	out := make([]studio.PeerInfo, 0, len(byID))
	for _, p := range byID {
		out = append(out, *p)
	}
	return out
}

// Connect dials a currently-discovered peer (no-op if it isn't on the LAN right now).
func (g *peerGateway) Connect(nodeID string) {
	for _, d := range g.disc.Peers() {
		if d.NodeID == nodeID {
			g.peerMgr.Connect(d)
			return
		}
	}
}

func (g *peerGateway) ConfirmSAS(nodeID string, accept bool) { g.peerMgr.ConfirmSAS(nodeID, accept) }
func (g *peerGateway) Forget(nodeID string)                  { g.peerMgr.Forget(nodeID) }

// CallRemote forwards a unary method to a paired peer over remotectl. Rejects unpaired peers
// up front (defence in depth - the transport also won't deliver to a non-connected peer).
func (g *peerGateway) CallRemote(ctx context.Context, nodeID, method string, params any) (json.RawMessage, error) {
	if !g.peerMgr.IsTrusted(nodeID) {
		return nil, errors.New("peer not paired")
	}
	return g.remoteCtl.Call(ctx, nodeID, method, params)
}

// Subscribe registers per-session change/SAS hooks; returns an unsubscribe.
func (g *peerGateway) Subscribe(onChange func(), onSAS func(studio.PeerSAS)) func() {
	g.mu.Lock()
	id := g.seq
	g.seq++
	g.changeSubs[id] = onChange
	g.sasSubs[id] = onSAS
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		delete(g.changeSubs, id)
		delete(g.sasSubs, id)
		g.mu.Unlock()
	}
}

func (g *peerGateway) fanChange() {
	g.mu.Lock()
	subs := make([]func(), 0, len(g.changeSubs))
	for _, f := range g.changeSubs {
		subs = append(subs, f)
	}
	g.mu.Unlock()
	for _, f := range subs {
		f()
	}
}

func (g *peerGateway) fanSAS(s studio.PeerSAS) {
	g.mu.Lock()
	subs := make([]func(studio.PeerSAS), 0, len(g.sasSubs))
	for _, f := range g.sasSubs {
		subs = append(subs, f)
	}
	g.mu.Unlock()
	for _, f := range subs {
		f(s)
	}
}
