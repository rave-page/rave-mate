package app

import (
	"context"
	"time"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerfed"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/twitch"
)

// runTwitchFederationWatcher arms/disarms Twitch federation on the proxy: with no LOCAL
// session, the first paired instance advertising the twitch capability (and confirming a live
// session over twitch.state) serves chat/moderation/title as if signed in locally. A local
// sign-in always wins; the serving peer dropping the capability or going away disarms. Shared
// peerfed loop (30s cadence + immediate first pass). The OAuth token never crosses the link.
func runTwitchFederationWatcher(ctx context.Context, log *logbus.Bus, proxy *featurehost.TwitchProxy,
	bus *eventbus.Bus, peers *peerlink.Manager, endpoint func() *remotectl.Endpoint) {
	if proxy == nil {
		return
	}
	peerfed.Watcher[remotectl.TwitchStateResult]{
		Interval: 30 * time.Second,
		LocalActive: func() bool {
			return proxy.LocalSignedIn()
		},
		// Trigger on the CapTwitch advertisement (cheap) so only peers that actually hold a
		// Twitch session are probed; twitch.state then confirms + carries the typed identity.
		Peers: func() []peerfed.Peer {
			if endpoint() == nil || peers == nil || bus == nil {
				return nil
			}
			owners := bus.Owners(twitch.CapTwitch)
			if len(owners) == 0 {
				return nil
			}
			has := make(map[string]bool, len(owners))
			for _, o := range owners {
				has[o] = true
			}
			var out []peerfed.Peer
			for _, p := range connectedPeers(peers) {
				if has[p.NodeID] {
					out = append(out, p)
				}
			}
			return out
		},
		Probe: func(pctx context.Context, nodeID string) (remotectl.TwitchStateResult, bool, error) {
			e := endpoint()
			if e == nil {
				return remotectl.TwitchStateResult{}, false, nil
			}
			st, err := remotectl.NewClient(e, nodeID).TwitchState(pctx)
			return st, st.SignedIn, err
		},
		Arm: func(nodeID, name string, st remotectl.TwitchStateResult) {
			proxy.SetFederated(remotectl.NewClient(endpoint(), nodeID), name,
				twitch.User{ID: st.UserID, Login: st.Login, DisplayName: st.DisplayName})
			log.Info("twitch", "federation armed - session served by peer", map[string]any{"via": name})
		},
		Disarm: func() { proxy.ClearFederated() },
	}.Run(ctx)
}
