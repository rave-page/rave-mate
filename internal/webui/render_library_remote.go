package webui

// Remote-control target plumbing shared by the Library + Publish tabs: peer enumeration, the
// "Controlling [This computer ▾]" switcher, and the typed remotectl client binding (still used
// by the Publish remote cockpit + non-UI callers). The Library's remote SURFACE is the live
// mirror (library_mirror.go) - the old degraded RPC-rendered panels are gone.
//
// The switcher is Zig-rendered (native/zigui/src/libremote.zig): Go resolves the smart-select
// into libRemoteSt, Zig renders byte-identical markup (Go stays fallback + golden reference,
// zigui_golden_libremote_test.go).

import (
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/zigui"
)

type remotePeerW struct{ NodeID, Name string }

// libRemoteSt is the resolved switcher state. Show=false = no row at all (headless remote
// session, or nothing connected) - both renderers then emit "".
type libRemoteSt struct {
	Show bool     `json:"show"`
	Sel  selState `json:"sel"`
}

// controllablePeers returns connected peers (the switcher options). Empty when the peer link is
// off or nothing is connected, so the switcher hides itself and the tab stays local.
func (u *UI) controllablePeers() []remotePeerW {
	if u.svc.Peers == nil || u.svc.RemoteCtl == nil {
		return nil
	}
	var out []remotePeerW
	for _, c := range u.svc.Peers.Connections() {
		if c.Status == peerlink.StatusConnected {
			out = append(out, remotePeerW{NodeID: c.NodeID, Name: peerName(c.Nickname, c.NodeID)})
		}
	}
	return out
}

// libRemoteTarget returns the current control target ("" = this computer). Falls back to local
// if the targeted peer is no longer connected.
func (u *UI) libRemoteTarget() string {
	u.mu.Lock()
	t := u.remoteTarget
	u.mu.Unlock()
	if t == "" {
		return ""
	}
	for _, p := range u.controllablePeers() {
		if p.NodeID == t {
			return t
		}
	}
	return ""
}

// remoteClient binds a typed peer-control client to a node id (nil if unavailable).
func (u *UI) remoteClient(nodeID string) *remotectl.Client {
	if u.svc.RemoteCtl == nil || nodeID == "" {
		return nil
	}
	return remotectl.NewClient(u.svc.RemoteCtl, nodeID)
}

// targetSwitcherHTML renders the "Controlling [This computer ▾]" row. "" when no peer is
// connected (caller omits it). id = smartSelect id, act = dispatch prefix (trailing colon).
func (u *UI) targetSwitcherHTML(id, act string) string {
	st := u.targetSwitcherState(id, act)
	if zigui.Available() {
		if h, ok := zigui.RenderLibRemote(stateJSON(st)); ok {
			return h
		}
	}
	return libRemoteHTML(st)
}

// targetSwitcherState registers + resolves the switcher's smart select.
func (u *UI) targetSwitcherState(id, act string) libRemoteSt {
	if u.virtual() {
		return libRemoteSt{Sel: emptySel()} // headless remote session: no chained control from inside a mirror
	}
	peers := u.controllablePeers()
	if len(peers) == 0 {
		return libRemoteSt{Sel: emptySel()}
	}
	cur := u.libRemoteTarget()
	sel := resolveSmartSelect(id, act, cur, func() []ssOpt {
		out := []ssOpt{{Val: "", Label: i18n.T("remote.thisComputer")}}
		for _, p := range peers {
			out = append(out, ssOpt{Val: p.NodeID, Label: "▸ " + p.Name})
		}
		return out
	})
	sel.Label = i18n.T("remote.controlling")
	return libRemoteSt{Show: true, Sel: sel}
}

// libRemoteHTML is the pure renderer (golden reference; byte-identical to Zig).
func libRemoteHTML(st libRemoteSt) string {
	if !st.Show {
		return ""
	}
	return `<div class=lib-target>` + selHTML(st.Sel) + `</div>`
}
