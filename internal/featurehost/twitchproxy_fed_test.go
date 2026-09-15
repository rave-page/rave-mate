package featurehost

import (
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/twitch"
)

// TestTwitchProxyFederation mirrors vrchat/federation_test.go for Twitch: an armed federation
// answers SignedIn/Self/Via ONLY without a local session, a local session always wins (and
// routing falls back to the child), and ClearFederated reverts cleanly.
func TestTwitchProxyFederation(t *testing.T) {
	log := logbus.New(64)
	p, err := NewTwitchProxy(log, nil, nil, func() string { return "" })
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	cli := remotectl.NewClient(remotectl.New(nil, func(string, []byte) error { return nil }), "peerA")

	// no local session + armed federation -> federated identity answers, ops route to the peer.
	p.SetFederated(cli, "desk", twitch.User{ID: "u_p", Login: "peerdj", DisplayName: "PeerDJ"})
	if !p.SignedIn() || !p.Federated() || p.Via() != "desk" {
		t.Fatalf("federated drift: signedIn=%v fed=%v via=%q", p.SignedIn(), p.Federated(), p.Via())
	}
	if self := p.Self(); self.Login != "peerdj" || self.ID != "u_p" {
		t.Fatalf("Self must be the peer identity: %+v", self)
	}
	if p.LocalSignedIn() || p.LocalConnected() {
		t.Fatal("Local* must stay false while only federated")
	}
	if p.fedClient() == nil {
		t.Fatal("fedClient must route while federated with no local session")
	}

	// in-progress local auth (token sealed, EventSub not yet up) already wins over federation -
	// federation is never allowed to mask a local session that is coming up.
	p.mu.Lock()
	p.st = twitchState{SignedIn: true, Connected: false, Self: twitch.User{ID: "u_l", Login: "localdj"}}
	p.mu.Unlock()
	if p.Federated() || p.Via() != "" {
		t.Fatalf("local (connecting) session must not be masked: fed=%v via=%q", p.Federated(), p.Via())
	}
	if p.fedClient() != nil {
		t.Fatal("a local session always wins - fedClient must be nil")
	}

	// local session fully up -> Self is local, LocalConnected true.
	p.mu.Lock()
	p.st = twitchState{SignedIn: true, Connected: true, Self: twitch.User{ID: "u_l", Login: "localdj"}}
	p.mu.Unlock()
	if self := p.Self(); self.Login != "localdj" {
		t.Fatalf("Self must be the local identity when signed in: %+v", self)
	}
	if !p.LocalSignedIn() || !p.LocalConnected() {
		t.Fatal("Local flags must be true when the child is signed in + connected")
	}

	// local logout + explicit disarm -> signed out.
	p.mu.Lock()
	p.st = twitchState{}
	p.mu.Unlock()
	p.ClearFederated()
	if p.SignedIn() || p.Federated() || p.Via() != "" || p.fedClient() != nil {
		t.Fatalf("cleared federation still answering: signedIn=%v fed=%v", p.SignedIn(), p.Federated())
	}
}
