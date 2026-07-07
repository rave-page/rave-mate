package studio

import (
	"context"
	"encoding/json"
	"time"
)

// PeerGateway surfaces the LAN peer link to the web client - the Local Studio "contexts"
// switcher that manages rave-mate instances on other machines - and routes a unary Local-Studio
// call to a selected paired peer. nil ⇒ peers disabled: peers.* aren't advertised and a request
// carrying a target errors. Implemented in the app package over peerlink + remotectl.
type PeerGateway interface {
	// Peers returns the merged context-switcher view (discovered + remembered + connected).
	Peers() []PeerInfo
	// Connect dials a discovered peer (starts pairing if unknown). No-op if not discoverable.
	Connect(nodeID string)
	// ConfirmSAS records the local verdict on a pending pairing code.
	ConfirmSAS(nodeID string, accept bool)
	// Forget unpairs + disconnects a peer.
	Forget(nodeID string)
	// CallRemote forwards a unary Local-Studio method to a paired peer (remotectl). Errors if
	// the peer isn't connected or doesn't implement the method.
	CallRemote(ctx context.Context, nodeID, method string, params any) (json.RawMessage, error)
	// Subscribe registers per-session hooks for peer-list changes + SAS prompts; returns an
	// unsubscribe. The caller re-reads Peers() on each onChange.
	Subscribe(onChange func(), onSAS func(PeerSAS)) (unsub func())
}

// PeerInfo is the web context-switcher's view of one peer.
type PeerInfo struct {
	NodeID   string `json:"nodeId"`
	Nickname string `json:"nickname"`
	Status   string `json:"status"` // connected | awaiting-sas | connecting | offline
	SAS      string `json:"sas,omitempty"`
	Trusted  bool   `json:"trusted"`
	Online   bool   `json:"online"`
}

// PeerSAS is a pending pairing prompt streamed to the web client (peers.subscribe).
type PeerSAS struct {
	NodeID   string `json:"nodeId"`
	Nickname string `json:"nickname"`
	SAS      string `json:"sas"`
}

// peerMethods are the peer-management RPCs, advertised in capabilities only when a gateway is
// wired. peers.subscribe is the only streaming one (snapshots + SAS prompts).
var peerMethods = []string{
	"peers.list",
	"peers.connect",
	"peers.confirmSAS",
	"peers.forget",
	"peers.subscribe",
}

func isPeerMethod(m string) bool {
	switch m {
	case "peers.list", "peers.connect", "peers.confirmSAS", "peers.forget", "peers.subscribe":
		return true
	}
	return false
}

// handlePeerReq dispatches a peers.* method locally (never remoted). gw is non-nil here.
func (s *session) handlePeerReq(req dataReq) {
	gw := s.srv.peers
	if gw == nil {
		s.sendErr(req.ID, errUnknownMethod, "peers unavailable")
		return
	}
	p := asMap(req.Params)
	switch req.Method {
	case "peers.list":
		s.send(map[string]any{"t": "res", "id": req.ID, "ok": true, "result": gw.Peers()})
	case "peers.connect":
		gw.Connect(asString(p["nodeId"]))
		s.okRes(req.ID)
	case "peers.confirmSAS":
		gw.ConfirmSAS(asString(p["nodeId"]), asBool(p["accept"]))
		s.okRes(req.ID)
	case "peers.forget":
		gw.Forget(asString(p["nodeId"]))
		s.okRes(req.ID)
	case "peers.subscribe":
		s.subscribePeers(req.ID)
	}
}

// subscribePeers opens a long-lived stream: push the current snapshot, then push a fresh
// snapshot on every peer-list change and an SAS prompt whenever a pairing needs confirmation.
func (s *session) subscribePeers(reqID string) {
	gw := s.srv.peers
	if gw == nil {
		s.sendErr(reqID, errUnknownMethod, "peers unavailable")
		return
	}
	s.notifyStream(reqID, "peers", gw.Peers()) // initial snapshot
	unsub := gw.Subscribe(
		func() { s.notifyStream(reqID, "peers", gw.Peers()) },
		func(sas PeerSAS) { s.notifyStream(reqID, "sas", sas) },
	)
	s.subsMu.Lock()
	s.subs[reqID] = &subRec{unsub: unsub}
	s.subsMu.Unlock()
}

// remoteCall forwards a unary studio method to the request's target peer and relays the result.
func (s *session) remoteCall(req dataReq) {
	gw := s.srv.peers
	if gw == nil {
		s.sendErr(req.ID, errUnknownMethod, "remote contexts unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := gw.CallRemote(ctx, req.Target, req.Method, req.Params)
	if err != nil {
		s.sendErr(req.ID, errInternal, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": req.ID, "ok": true, "result": json.RawMessage(res)})
}

// okRes sends a generic {ok:true} response.
func (s *session) okRes(id string) {
	s.send(map[string]any{"t": "res", "id": id, "ok": true, "result": map[string]any{"ok": true}})
}
