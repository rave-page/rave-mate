package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	ghlink "rave.page/mate/internal/github"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/ui"
)

// fedClient is a no-op remotectl client (SetFederated only needs a non-nil handle for the
// state/render path; these tests never call through it).
func fedClient() *remotectl.Client {
	return remotectl.NewClient(remotectl.New(nil, func(string, []byte) error { return nil }), "peerA")
}

// TestTwitchTabFederatedRender: with a federated Twitch session (no local), the tab's status
// region renders the via-peer hint and the controls stay active - the fallback for the two-
// instance e2e (SAS pairing needs interactive 6-digit confirmation, not driveable via ctl).
func TestTwitchTabFederatedRender(t *testing.T) {
	tw, err := featurehost.NewTwitchProxy(logbus.New(16), nil, nil, func() string { return "" })
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	tw.SetFederated(fedClient(), "desk", twitch.User{ID: "u_p", Login: "raverdj", DisplayName: "RaverDJ"})
	u := &UI{svc: ui.Services{Cfg: &config.Config{}, Twitch: tw}}

	st := u.twitchState()
	if !st.HasStatus || st.StatusVariant != "success" {
		t.Fatalf("federated tab must show a status region: %+v", st)
	}
	if !strings.Contains(st.StatusLabel, "RaverDJ") {
		t.Fatalf("status label must name the streamer: %q", st.StatusLabel)
	}
	if !strings.Contains(st.StatusLine, "desk") {
		t.Fatalf("status line must name the serving peer: %q", st.StatusLine)
	}
	html := twitchHTML(st)
	if !strings.Contains(html, "strow") || !strings.Contains(html, "desk") || !strings.Contains(html, "RaverDJ") {
		t.Fatalf("tab HTML missing via-peer status region: %s", html)
	}
	// controls stay active: the send form still renders.
	if !strings.Contains(html, "twitch-send") {
		t.Fatalf("federated tab must keep the send control: %s", html)
	}
}

// TestTwitchSettingsFederatedRender: Settings shows the via-peer hint AND keeps the local
// device-code sign-in (a local sign-in overrides federation).
func TestTwitchSettingsFederatedRender(t *testing.T) {
	tw, err := featurehost.NewTwitchProxy(logbus.New(16), nil, nil, func() string { return "" })
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	tw.SetFederated(fedClient(), "desk", twitch.User{Login: "raverdj", DisplayName: "RaverDJ"})
	u := &UI{svc: ui.Services{Cfg: &config.Config{}, Twitch: tw}}

	blocks := u.twitchBlocks()
	if !blockNoteContains(blocks, "desk") || !blockNoteContains(blocks, "RaverDJ") {
		t.Fatalf("settings must show the via-peer hint: %+v", blocks)
	}
	if !blockHasBtnAct(blocks, "settings-twitch-signin") {
		t.Fatalf("settings must keep the local sign-in control: %+v", blocks)
	}
}

// TestWorldSyncFederatedRender: World Sync shows the via-peer hint AND keeps the local link
// controls (device code + paste token).
func TestWorldSyncFederatedRender(t *testing.T) {
	gh := ghlink.NewAuth(func() string { return "" }, logbus.New(16))
	gh.SetFederated("peerdj", "desk")
	u := &UI{svc: ui.Services{Cfg: &config.Config{}, GitHub: gh}}

	blocks := u.worldSyncBlocks()
	if !blockNoteContains(blocks, "desk") || !blockNoteContains(blocks, "peerdj") {
		t.Fatalf("worldsync must show the via-peer hint: %+v", blocks)
	}
	if !blockHasBtnAct(blocks, "settings-gh-device") {
		t.Fatalf("worldsync must keep the local link controls: %+v", blocks)
	}
	// federated (no local token) must NOT offer unlink - there is no local link to unlink.
	if blockHasBtnAct(blocks, "settings-gh-unlink") {
		t.Fatalf("federated-only worldsync must not offer unlink: %+v", blocks)
	}
}

func blockNoteContains(blocks []setBlock, s string) bool {
	for _, b := range blocks {
		if (b.K == "note" || b.K == "noteRaw") && strings.Contains(b.Text+b.HTML, s) {
			return true
		}
	}
	return false
}

func blockHasBtnAct(blocks []setBlock, act string) bool {
	for _, b := range blocks {
		for _, k := range b.Kids {
			if k.Btn != nil && k.Btn.Act == act {
				return true
			}
		}
	}
	return false
}
