package webui

// Remote-UI hub: mux over the peerlink ChanRemoteUI channel. Host side (this file) serves
// headless Library sessions to paired controllers - one session per peer, each a full headless
// UI over the SAME Services the window UI uses, so gridfix/cue-edit/transcode/tag writes all
// execute here, on this machine's files. The controller (mirror) side lives in
// library_mirror.go and plugs in via the hub's mirror sink.

import (
	"encoding/json"
	"sync"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/peerlink"
)

const ruiLogTag = "remoteui"

type ruiSession struct {
	peer, sid string
	hu        *UI
	vs        *virtualShell
}

type ruiHub struct {
	u      *UI                                       // owning window UI (Services + log)
	sendTo func(nodeID string, payload []byte) error // peerlink SendTo (seam for tests)

	mu    sync.Mutex
	host  map[string]*ruiSession // peer nodeID → serving session (≤1 per paired peer)
	reasm map[string]*ruiReasm   // peer nodeID → inbound doc/eval reassembly (1 in flight, ≤ruiReasmMax)

	// onMirror receives complete controller-side messages (doc/eval/closed/fetchres);
	// set by the mirror (library_mirror.go). Nil = drop.
	onMirror func(peer string, m ruiMsg)
}

func newRuiHub(u *UI) *ruiHub {
	h := &ruiHub{u: u, host: map[string]*ruiSession{}, reasm: map[string]*ruiReasm{}}
	if u.svc.Peers != nil {
		h.sendTo = func(nodeID string, payload []byte) error {
			return u.svc.Peers.SendTo(nodeID, peerlink.ChanRemoteUI, payload)
		}
	}
	return h
}

// ruiInit wires the hub into the peer plane; called once from New (window UI only).
func (u *UI) ruiInit() {
	if u.svc.PeerBridge == nil || u.svc.Peers == nil {
		return
	}
	u.rui = newRuiHub(u)
	u.rui.setMirrorSink(u.onMirrorMsg)
	u.svc.PeerBridge.SetRemoteUISink(u.rui.onInbound)
	u.svc.Peers.AddListener(nil, u.rui.onPeerState)
}

func (h *ruiHub) send(peer string, m ruiMsg) error {
	if h.sendTo == nil {
		return nil
	}
	raw, err := ruiEncode(m)
	if err != nil {
		return err
	}
	return h.sendTo(peer, raw)
}

// sendChunked streams a doc/eval payload (chunked over ruiChunkMax).
func (h *ruiHub) sendChunked(peer string, m ruiMsg) error {
	return ruiSendChunked(func(part ruiMsg) error { return h.send(peer, part) }, m, ruiChunkMax, randToken())
}

// onInbound is the ChanRemoteUI sink (called on the link read loop - keep it non-blocking;
// session builds/teardowns hop to their own goroutine).
func (h *ruiHub) onInbound(peer string, payload []byte) {
	var m ruiMsg
	if json.Unmarshal(payload, &m) != nil || m.T == "" {
		return
	}
	switch m.T {
	case ruiKindOpen:
		debuglog.Go(h.u.log, ruiLogTag, func() { h.handleOpen(peer, m) })
	case ruiKindAct:
		h.handleAct(peer, m)
	case ruiKindClose:
		debuglog.Go(h.u.log, ruiLogTag, func() { h.closeHost(peer, m.SID, "", false) })
	case ruiKindFetch:
		debuglog.Go(h.u.log, ruiLogTag, func() { h.handleFetch(peer, m) })
	case ruiKindDoc, ruiKindEval:
		h.mu.Lock()
		r := h.reasm[peer]
		if r == nil {
			r = &ruiReasm{}
			h.reasm[peer] = r
		}
		full, done := r.feed(m)
		sink := h.onMirror
		h.mu.Unlock()
		if done && sink != nil {
			sink(peer, full)
		}
	case ruiKindClosed, ruiKindFetchRes:
		h.mu.Lock()
		sink := h.onMirror
		h.mu.Unlock()
		if sink != nil {
			sink(peer, m)
		}
	}
}

// handleOpen builds (or replaces) the headless session for peer and streams the initial doc.
func (h *ruiHub) handleOpen(peer string, m ruiMsg) {
	if m.SID == "" {
		return
	}
	h.closeHost(peer, "", "", false) // replace any prior session silently
	s := &ruiSession{peer: peer, sid: m.SID}
	emit := func(kind string) func(string) {
		return func(payload string) {
			out := ruiMsg{T: kind, SID: s.sid, Data: h.rewriteMediaOut(payload)}
			if err := h.sendChunked(peer, out); err != nil && h.u.log != nil {
				h.u.log.Warn(ruiLogTag, "stream send failed", map[string]any{"peer": peer, "error": err.Error()})
			}
		}
	}
	s.hu = newHeadlessUI(h.u.svc, emit(ruiKindDoc), emit(ruiKindEval))
	s.vs = s.hu.shell.(*virtualShell)
	h.mu.Lock()
	h.host[peer] = s
	h.mu.Unlock()
	emit(ruiKindDoc)(s.hu.headlessDocHTML())
	if h.u.log != nil {
		h.u.log.Info(ruiLogTag, "remote library session opened", map[string]any{"peer": peer})
	}
}

// handleAct replays controller input into the session (bounded queue; drops are the
// controller's own flood).
func (h *ruiHub) handleAct(peer string, m ruiMsg) {
	h.mu.Lock()
	s := h.host[peer]
	h.mu.Unlock()
	if s == nil || s.sid != m.SID || m.Data == "" {
		return
	}
	if !s.vs.post(m.Data) && h.u.log != nil {
		h.u.log.Warn(ruiLogTag, "input dropped (queue full)", map[string]any{"peer": peer})
	}
}

// closeHost tears down peer's session. sid "" = any. notify sends a closed frame (reason).
func (h *ruiHub) closeHost(peer, sid, reason string, notify bool) {
	h.mu.Lock()
	s := h.host[peer]
	if s == nil || (sid != "" && s.sid != sid) {
		h.mu.Unlock()
		return
	}
	delete(h.host, peer)
	h.mu.Unlock()
	s.hu.Stop()
	releaseUIState(s.hu)
	if notify {
		_ = h.send(peer, ruiMsg{T: ruiKindClosed, SID: s.sid, Data: reason})
	}
	if h.u.log != nil {
		h.u.log.Info(ruiLogTag, "remote library session closed", map[string]any{"peer": peer, "reason": reason})
	}
}

// onPeerState GCs sessions (and mirror state) for peers that are no longer connected.
func (h *ruiHub) onPeerState() {
	connected := map[string]bool{}
	if h.u.svc.Peers != nil {
		for _, c := range h.u.svc.Peers.Connections() {
			if c.Status == peerlink.StatusConnected {
				connected[c.NodeID] = true
			}
		}
	}
	h.mu.Lock()
	var gone []string
	for peer := range h.host {
		if !connected[peer] {
			gone = append(gone, peer)
		}
	}
	for peer := range h.reasm {
		if !connected[peer] {
			delete(h.reasm, peer)
		}
	}
	h.mu.Unlock()
	for _, peer := range gone {
		h.closeHost(peer, "", "peer disconnected", false)
	}
	h.u.mirrorPeerState(connected)
}

// setMirrorSink registers the controller-side message sink (library_mirror.go).
func (h *ruiHub) setMirrorSink(fn func(peer string, m ruiMsg)) {
	h.mu.Lock()
	h.onMirror = fn
	h.mu.Unlock()
}

// rewriteMediaOut rewrites this host's loopback media URLs to the wire placeholder so the
// controller can point them at its proxy (media proxy phase); identity until then.
func (h *ruiHub) rewriteMediaOut(payload string) string { return payload }

// handleFetch answers a controller's media byte-range request (media proxy phase).
func (h *ruiHub) handleFetch(peer string, m ruiMsg) { _ = peer }
