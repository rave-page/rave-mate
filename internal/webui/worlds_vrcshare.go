package webui

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/vrchat"
)

// VRChat-link federation, client side: ONE paired instance holds the VRChat session and
// serves friends/groups/roles to the pair group over remotectl's vrchat.* methods. The
// Worlds pickers read through wsVrcSrc - local session when linked, else the discovered
// peer - so this instance behaves as-if linked. Read-only; the session never crosses
// the link (publish-time group-role expansion federates app-side, internal/app/vrcfederation.go).

// wsVrcSrc is the resolved VRChat data source for one bg operation.
type wsVrcSrc struct {
	peerName string // "" = local session, else the serving peer's name (UI hint)
	local    *vrchat.Client
	ownID    string
	remote   *remotectl.Client
}

func (s *wsVrcSrc) Friends(ctx context.Context, offset, n int, offline bool) ([]vrchat.Friend, error) {
	if s.local != nil {
		return s.local.Friends(ctx, offset, n, offline)
	}
	return s.remote.VrcFriends(ctx, offset, n, offline)
}

func (s *wsVrcSrc) OwnGroups(ctx context.Context) ([]vrchat.Group, error) {
	if s.local != nil {
		return s.local.UserGroups(ctx, s.ownID)
	}
	return s.remote.VrcUserGroups(ctx)
}

func (s *wsVrcSrc) SearchGroups(ctx context.Context, q string, offset, n int) ([]vrchat.Group, error) {
	if s.local != nil {
		return s.local.SearchGroups(ctx, q, offset, n)
	}
	return s.remote.VrcSearchGroups(ctx, q, offset, n)
}

func (s *wsVrcSrc) GroupRoles(ctx context.Context, groupID string) ([]vrchat.GroupRole, error) {
	if s.local != nil {
		return s.local.GroupRoles(ctx, groupID)
	}
	return s.remote.VrcGroupRoles(ctx, groupID)
}

// wsVrcFed memoizes which connected peer serves the VRChat link. Render paths read the
// memo only (never the network); bg paths refresh it. TTL keeps a vanished peer from
// being trusted forever; probing dedupes concurrent kicks.
var wsVrcFed struct {
	mu      sync.Mutex
	nodeID  string
	name    string
	at      time.Time
	probing bool
}

const wsVrcFedTTL = 60 * time.Second

// wsVrcFedCached returns the memoized serving peer (render-safe, no network).
func (u *UI) wsVrcFedCached() (nodeID, name string, ok bool) {
	wsVrcFed.mu.Lock()
	defer wsVrcFed.mu.Unlock()
	if wsVrcFed.nodeID == "" || time.Since(wsVrcFed.at) > wsVrcFedTTL {
		return "", "", false
	}
	// still connected? (Connections() is an in-memory snapshot - cheap)
	if u.svc.Peers != nil {
		for _, c := range u.svc.Peers.Connections() {
			if c.NodeID == wsVrcFed.nodeID && c.Status == peerlink.StatusConnected {
				return wsVrcFed.nodeID, wsVrcFed.name, true
			}
		}
	}
	return "", "", false
}

// wsVrcFedKick probes connected peers for a VRChat link OFF-THREAD and memoizes the
// first hit. Deduped; the Worlds 1 Hz tick re-renders the link hint when it lands.
func (u *UI) wsVrcFedKick() {
	if u.svc.RemoteCtl == nil || u.svc.Peers == nil {
		return
	}
	wsVrcFed.mu.Lock()
	if wsVrcFed.probing || (wsVrcFed.nodeID != "" && time.Since(wsVrcFed.at) <= wsVrcFedTTL) {
		wsVrcFed.mu.Unlock()
		return
	}
	wsVrcFed.probing = true
	wsVrcFed.mu.Unlock()
	u.bg(func() {
		defer func() {
			wsVrcFed.mu.Lock()
			wsVrcFed.probing = false
			wsVrcFed.mu.Unlock()
		}()
		for _, c := range u.svc.Peers.Connections() {
			if c.Status != peerlink.StatusConnected {
				continue
			}
			cli := u.remoteClient(c.NodeID)
			if cli == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			st, err := cli.VrcStatus(ctx)
			cancel()
			if err != nil || !st.Linked {
				continue
			}
			wsVrcFed.mu.Lock()
			wsVrcFed.nodeID, wsVrcFed.name, wsVrcFed.at = c.NodeID, peerName(c.Nickname, c.NodeID), time.Now()
			wsVrcFed.mu.Unlock()
			return
		}
	})
}

// wsVrcSource resolves the VRChat source for a bg operation: local session first, else
// the memoized federation peer (fresh probe when the memo is cold). BLOCKS on a status
// round-trip on cache miss - bg goroutines only, never render/handlers.
func (u *UI) wsVrcSource(ctx context.Context) (*wsVrcSrc, bool) {
	if u.svc.Vrchat != nil && u.svc.Vrchat.State().LoggedIn {
		return &wsVrcSrc{local: u.svc.Vrchat.Client(), ownID: u.svc.Vrchat.CurrentUserID()}, true
	}
	if id, name, ok := u.wsVrcFedCached(); ok {
		if cli := u.remoteClient(id); cli != nil {
			return &wsVrcSrc{peerName: name, remote: cli}, true
		}
	}
	if u.svc.RemoteCtl == nil || u.svc.Peers == nil {
		return nil, false
	}
	for _, c := range u.svc.Peers.Connections() {
		if c.Status != peerlink.StatusConnected {
			continue
		}
		cli := u.remoteClient(c.NodeID)
		if cli == nil {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := cli.VrcStatus(sctx)
		cancel()
		if err != nil || !st.Linked {
			continue
		}
		name := peerName(c.Nickname, c.NodeID)
		wsVrcFed.mu.Lock()
		wsVrcFed.nodeID, wsVrcFed.name, wsVrcFed.at = c.NodeID, name, time.Now()
		wsVrcFed.mu.Unlock()
		return &wsVrcSrc{peerName: name, remote: cli}, true
	}
	return nil, false
}
