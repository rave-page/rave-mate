package webui

import (
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/peers"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/ui"
)

func encTestManager(t *testing.T) (*peerlink.Manager, *peers.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id, err := identity.LoadOrCreate(nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := peers.New(st)
	return peerlink.New(id, ps, nil), ps
}

// TestPeerEncCardReflectsStore: the sub-card toggles + warning + status mirror the stored opt-out.
func TestPeerEncCardReflectsStore(t *testing.T) {
	mgr, ps := encTestManager(t)
	if err := ps.Save(peers.Peer{NodeID: "n1", Trusted: true}); err != nil {
		t.Fatal(err)
	}
	u := &UI{svc: ui.Services{Peers: mgr}}

	// Default: three toggles, all ON, no warning.
	card := u.peerEncCard("n1", nil)
	if card == nil || len(card.Toggles) != 3 {
		t.Fatalf("want 3 toggles, got %+v", card)
	}
	for _, tg := range card.Toggles {
		if !tg.On {
			t.Fatalf("default toggle %q should be ON (encrypted)", tg.Act)
		}
	}
	if card.Warn != "" {
		t.Fatalf("no opt-out should mean no warning, got %q", card.Warn)
	}
	if !strings.Contains(card.Toggles[0].Act, "peer-enc:n1\x1f"+peers.PlaneControl) {
		t.Fatalf("control toggle act = %q", card.Toggles[0].Act)
	}

	// Opt control + media out; files stays encrypted.
	if err := ps.SetEncOff("n1", peers.PlaneControl, true); err != nil {
		t.Fatal(err)
	}
	if err := ps.SetEncOff("n1", peers.PlaneMedia, true); err != nil {
		t.Fatal(err)
	}
	card = u.peerEncCard("n1", nil)
	if card.Toggles[0].On || card.Toggles[2].On || !card.Toggles[1].On {
		t.Fatalf("toggles: control=%v files=%v media=%v", card.Toggles[0].On, card.Toggles[1].On, card.Toggles[2].On)
	}
	if card.Warn == "" {
		t.Fatal("opting out must surface an exposure warning")
	}

	// A connected peer shows the live control-plane wire state.
	card = u.peerEncCard("n1", &peerlink.ConnInfo{EncStatus: peerlink.LinkEncrypted})
	if card.Status == "" || card.StatusVar != "success" {
		t.Fatalf("live status = %q/%q, want a success line", card.Status, card.StatusVar)
	}
}

// TestPeerEncActionWritesStore: the peer-enc toggle action persists the opt-out through the store
// (OFF opts out, ON clears), driven through the real dispatch pipeline.
func TestPeerEncActionWritesStore(t *testing.T) {
	mgr, ps := encTestManager(t)
	if err := ps.Save(peers.Peer{NodeID: "n1", Trusted: true}); err != nil {
		t.Fatal(err)
	}
	cap := &capture{}
	u := newHeadlessUI(ui.Services{Cfg: &config.Config{}, Peers: mgr}, cap.html, cap.eval)
	t.Cleanup(func() { u.Stop(); releaseUIState(u) })

	act := "peer-enc:n1\x1f" + peers.PlaneControl
	if !u.dispatch(actMsg{Act: act, Val: "false"}) { // switch off = opt out
		t.Fatal("no handler matched peer-enc:")
	}
	if !ps.EncOff("n1", peers.PlaneControl) {
		t.Fatal("toggle off did not write the control opt-out")
	}
	if !u.dispatch(actMsg{Act: act, Val: "true"}) { // switch on = encrypt
		t.Fatal("no handler matched on the second dispatch")
	}
	if ps.EncOff("n1", peers.PlaneControl) {
		t.Fatal("toggle on did not clear the control opt-out")
	}
}

// TestEncStatusLine pins the control-plane wire-state → (line, tone) mapping.
func TestEncStatusLine(t *testing.T) {
	for _, c := range []struct {
		in   peerlink.LinkEnc
		tone string
	}{
		{peerlink.LinkEncrypted, "success"},
		{peerlink.LinkAuthYouOff, "warning"},
		{peerlink.LinkAuthPeerOff, "warning"},
		{peerlink.LinkAuthOld, "warning"},
	} {
		line, tone := encStatusLine(c.in)
		if line == "" || tone != c.tone {
			t.Errorf("encStatusLine(%q) = %q/%q, want a line + tone %q", c.in, line, tone, c.tone)
		}
	}
	if line, tone := encStatusLine(peerlink.LinkEnc("")); line != "" || tone != "" {
		t.Errorf("unknown state should be blank, got %q/%q", line, tone)
	}
}

// TestPeerEncHTMLMarkup: the sub-card renders its status, every toggle action, and the warning.
func TestPeerEncHTMLMarkup(t *testing.T) {
	got := peerEncHTML(peerEncSt{
		Status: "Encrypted", StatusVar: "success",
		Toggles: []uiToggle{
			newToggle("Control", "peer-enc:n1\x1fcontrol", true),
			newToggle("Files", "peer-enc:n1\x1ffiles", false),
		},
		Warn: "readable on your LAN",
	})
	for _, want := range []string{
		`peer-enc-status success`, `Encrypted`,
		`data-act="peer-enc:n1` + "\x1f" + `control"`,
		`data-act="peer-enc:n1` + "\x1f" + `files"`,
		`readable on your LAN`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("peerEncHTML missing %q in %q", want, got)
		}
	}
	// No status + no warn = just the toggles wrapped.
	bare := peerEncHTML(peerEncSt{Toggles: []uiToggle{newToggle("Media", "peer-enc:n1\x1fmedia", true)}})
	if strings.Contains(bare, "peer-enc-status") {
		t.Fatalf("empty status should render no status line: %q", bare)
	}
}

// TestFmtRouteStatPlaintextMarker: an opted-out media route is flagged; an encrypted one is not.
func TestFmtRouteStatPlaintextMarker(t *testing.T) {
	resolve := func(s string) string { return s }
	plain, _ := fmtRouteStat(medialink.RouteStat{Direction: "send", Peer: "n1", Encrypted: false}, resolve)
	if !strings.Contains(plain, "plaintext") {
		t.Fatalf("plaintext route not flagged: %q", plain)
	}
	enc, _ := fmtRouteStat(medialink.RouteStat{Direction: "send", Peer: "n1", Encrypted: true}, resolve)
	if strings.Contains(enc, "plaintext") {
		t.Fatalf("encrypted route wrongly flagged: %q", enc)
	}
}
