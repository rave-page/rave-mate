package remotectl

import (
	"context"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/twitch"
)

// fakeTwitch is a minimal TwitchServer for the twitch.* RPC tests.
type fakeTwitch struct {
	connected bool
	self      twitch.User
	live      twitch.ViewerInfo
	searchOut []twitch.Game

	sent      []string
	moderated []twitch.ModerateCmd
	title     string
	game      string
	preset    string
}

func (f *fakeTwitch) LocalConnected() bool        { return f.connected }
func (f *fakeTwitch) LocalSelf() twitch.User      { return f.self }
func (f *fakeTwitch) LiveInfo() twitch.ViewerInfo { return f.live }
func (f *fakeTwitch) SearchCategories(_ context.Context, _ string) ([]twitch.Game, error) {
	return f.searchOut, nil
}
func (f *fakeTwitch) SetTitle(_ context.Context, title, gameID string) error {
	f.title, f.game = title, gameID
	return nil
}
func (f *fakeTwitch) ApplyTitlePreset(_ context.Context, p config.TitlePreset) error {
	f.preset = p.Name
	return nil
}
func (f *fakeTwitch) SendChat(_ context.Context, text, _ string) error {
	f.sent = append(f.sent, text)
	return nil
}
func (f *fakeTwitch) Moderate(_ context.Context, cmd twitch.ModerateCmd) error {
	f.moderated = append(f.moderated, cmd)
	return nil
}

// TestTwitchRPC drives state + every write/read verb through the full client→server path.
func TestTwitchRPC(t *testing.T) {
	server, client := loopback()
	f := &fakeTwitch{
		connected: true,
		self:      twitch.User{ID: "u1", Login: "raverdj", DisplayName: "RaverDJ"},
		live:      twitch.ViewerInfo{Live: true, ViewerCount: 1234, GameName: "Music", Title: "Techno"},
		searchOut: []twitch.Game{{ID: "g1", Name: "Music"}},
	}
	RegisterTwitch(server, f)
	rc := NewClient(client, "server")

	// state: identity + live snapshot, and NO token field exists on the wire type.
	st, err := rc.TwitchState(ctx(t))
	if err != nil || !st.SignedIn || st.Login != "raverdj" || st.UserID != "u1" || st.DisplayName != "RaverDJ" {
		t.Fatalf("state=%+v err=%v", st, err)
	}
	if !st.Live || st.ViewerCount != 1234 || st.GameName != "Music" || st.Title != "Techno" {
		t.Fatalf("state live snapshot drift: %+v", st)
	}

	// searchCategories round-trips.
	games, err := rc.TwitchSearchCategories(ctx(t), "mus")
	if err != nil || len(games) != 1 || games[0].Name != "Music" {
		t.Fatalf("search=%+v err=%v", games, err)
	}

	// writes execute on the serving box.
	if err := rc.TwitchSetTitle(ctx(t), "Live set", "g1"); err != nil || f.title != "Live set" || f.game != "g1" {
		t.Fatalf("setTitle: title=%q game=%q err=%v", f.title, f.game, err)
	}
	if err := rc.TwitchApplyTitlePreset(ctx(t), config.TitlePreset{Name: "Techno"}); err != nil || f.preset != "Techno" {
		t.Fatalf("applyPreset: preset=%q err=%v", f.preset, err)
	}
	if err := rc.TwitchSendChat(ctx(t), "hello chat", ""); err != nil || len(f.sent) != 1 || f.sent[0] != "hello chat" {
		t.Fatalf("sendChat: sent=%v err=%v", f.sent, err)
	}
	if err := rc.TwitchModerate(ctx(t), twitch.ModerateCmd{Action: "ban", UserID: "troll"}); err != nil ||
		len(f.moderated) != 1 || f.moderated[0].Action != "ban" || f.moderated[0].UserID != "troll" {
		t.Fatalf("moderate: %+v err=%v", f.moderated, err)
	}
}

// TestTwitchGateWhenDisconnected: a box with no live session answers state (signedIn=false, no
// stats) but REFUSES every write - a box that is itself borrowing never serves a third peer.
func TestTwitchGateWhenDisconnected(t *testing.T) {
	server, client := loopback()
	f := &fakeTwitch{connected: false}
	RegisterTwitch(server, f)
	rc := NewClient(client, "server")

	st, err := rc.TwitchState(ctx(t))
	if err != nil || st.SignedIn || st.Live {
		t.Fatalf("disconnected state=%+v err=%v", st, err)
	}
	if err := rc.TwitchSendChat(ctx(t), "x", ""); err == nil {
		t.Fatal("sendChat must be refused when the serving box is not connected")
	}
	if err := rc.TwitchSetTitle(ctx(t), "t", ""); err == nil {
		t.Fatal("setTitle must be refused when the serving box is not connected")
	}
	if len(f.sent) != 0 || f.title != "" {
		t.Fatalf("refused writes must not touch the serving box: sent=%v title=%q", f.sent, f.title)
	}
}

// TestTwitchNoAuthSurface is the security gate: federation must expose NO verb that can re-auth,
// refresh, log out, or return the OAuth token. The serving side registers only the state/read/
// write verbs; every auth-shaped method name is unknown.
func TestTwitchNoAuthSurface(t *testing.T) {
	server, client := loopback()
	RegisterTwitch(server, &fakeTwitch{connected: true, self: twitch.User{ID: "u1"}})

	for _, m := range []string{
		"twitch.auth.start", "twitch.auth.poll", "twitch.logout", "twitch.auth.logout",
		"twitch.token", "twitch.auth", "twitch.startDevice", "twitch.pollDevice", "twitch.refresh",
	} {
		if _, err := client.Call(ctx(t), "server", m, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
			t.Fatalf("auth-shaped method %q must be unregistered (got err=%v)", m, err)
		}
	}
}
