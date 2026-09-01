package peerlink

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// lanPipe is an in-memory transport implementing the LAN contract (Upgrader + lanTransport)
// exactly as wsConn: plaintext until Upgrade, sealed after. It records every raw frame it puts on
// the wire so a test can assert ciphertext.
type lanPipe struct {
	in  chan []byte
	out chan []byte
	lanCrypto
	rmu  sync.Mutex
	sent [][]byte
}

func newLanPipe() (*lanPipe, *lanPipe) {
	a2b := make(chan []byte, 16)
	b2a := make(chan []byte, 16)
	return &lanPipe{in: b2a, out: a2b}, &lanPipe{in: a2b, out: b2a}
}

func (p *lanPipe) Send(ctx context.Context, b []byte) error {
	out := b
	if p.active() {
		out = p.seal(b)
	}
	p.rmu.Lock()
	p.sent = append(p.sent, append([]byte(nil), out...))
	p.rmu.Unlock()
	select {
	case p.out <- out:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *lanPipe) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-p.in:
		if p.active() {
			return p.open(b)
		}
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *lanPipe) Close()                                      {}
func (p *lanPipe) Upgrade(master []byte, initiator bool) error { return p.upgrade(master, initiator) }
func (p *lanPipe) lanPlane()                                   {}
func (p *lanPipe) wire() [][]byte                              { p.rmu.Lock(); defer p.rmu.Unlock(); return p.sent }

func constPref(v string) func(string) string { return func(string) string { return v } }

// driveLAN runs the AKE over the two lanPipes with the given prefs and upgrades both transports
// (as secureTunnel does). Returns both Results.
func driveLAN(t *testing.T, a, b *lanPipe, prefA, prefB string) (*Result, *Result) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	idA, idB := newIdentity(t), newIdentity(t)
	ca, cb := make(chan hsOut, 1), make(chan hsOut, 1)
	go func() { r, e := doHandshake(ctx, a, roleInitiator, idA, nil, "", constPref(prefA)); ca <- hsOut{r, e} }()
	go func() { r, e := doHandshake(ctx, b, roleResponder, idB, nil, "", constPref(prefB)); cb <- hsOut{r, e} }()
	oa, ob := <-ca, <-cb
	if oa.err != nil || ob.err != nil {
		t.Fatalf("handshake errored: init=%v resp=%v", oa.err, ob.err)
	}
	if err := upgradeTransport(a, oa.res); err != nil {
		t.Fatalf("upgrade a: %v", err)
	}
	if err := upgradeTransport(b, ob.res); err != nil {
		t.Fatalf("upgrade b: %v", err)
	}
	return oa.res, ob.res
}

const plainMarker = "PLAINTEXT-MARKER-9f3a"

// linkRoundTrip sends one data frame per channel a→b and returns whether all arrived, plus a's
// recorded wire frames.
func linkRoundTrip(t *testing.T, a, b *lanPipe, resA, resB *Result) [][]byte {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := newLink(a, resA)
	recv := newLink(b, resB)
	got := make(chan string, 8)
	recv.onFrame = func(ty string, mp map[string]any) {
		if ty == frameData {
			ch, _ := mp["ch"].(string)
			data, _ := mp["data"].(string)
			got <- ch + "|" + data
		}
	}
	go recv.readLoop(ctx)

	channels := []string{ChanSession, ChanMIDI, ChanControl, ChanBus, ChanRemoteUI}
	for _, ch := range channels {
		payload := []byte(`{"ch":"` + ch + `","` + plainMarker + `":1}`)
		if err := sender.SendData(ctx, ch, payload); err != nil {
			t.Fatalf("send %s: %v", ch, err)
		}
	}
	seen := map[string]bool{}
	timeout := time.After(3 * time.Second)
	for len(seen) < len(channels) {
		select {
		case g := <-got:
			ch, _, _ := bytes.Cut([]byte(g), []byte("|"))
			seen[string(ch)] = true
		case <-timeout:
			t.Fatalf("only %d/%d channels round-tripped: %v", len(seen), len(channels), seen)
		}
	}
	return a.wire()
}

// TestLANEncryptedRoundTrip: both ends default (on) → every channel round-trips AND no wire frame
// carries the plaintext marker (ciphertext on the wire).
func TestLANEncryptedRoundTrip(t *testing.T) {
	a, b := newLanPipe()
	resA, resB := driveLAN(t, a, b, encOn, encOn)
	if resA.LinkEncState() != LinkEncrypted || resB.LinkEncState() != LinkEncrypted {
		t.Fatalf("expected encrypted, got %v / %v", resA.LinkEncState(), resB.LinkEncState())
	}
	wire := linkRoundTrip(t, a, b, resA, resB)
	for i, f := range wire {
		if bytes.Contains(f, []byte(plainMarker)) {
			t.Fatalf("wire frame %d leaked the plaintext marker (not encrypted)", i)
		}
	}
}

// TestLANBothOptOutPlaintext: both ends opted out → no upgrade, "you opted out", and the marker IS
// visible on the wire (proves the plaintext branch is what runs).
func TestLANBothOptOutPlaintext(t *testing.T) {
	a, b := newLanPipe()
	resA, resB := driveLAN(t, a, b, encOff, encOff)
	if resA.LinkEncrypted || resB.LinkEncrypted {
		t.Fatal("both opted out but a transport upgraded")
	}
	if resA.LinkEncState() != LinkAuthYouOff {
		t.Fatalf("status = %v, want you-opted-out", resA.LinkEncState())
	}
	wire := linkRoundTrip(t, a, b, resA, resB)
	found := false
	for _, f := range wire {
		if bytes.Contains(f, []byte(plainMarker)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("both opted out but no plaintext frame on the wire")
	}
}

// TestLANOneSideOptOutEncrypted: a lone opt-out never downgrades - still encrypted.
func TestLANOneSideOptOutEncrypted(t *testing.T) {
	a, b := newLanPipe()
	resA, resB := driveLAN(t, a, b, encOff, encOn)
	if !resA.LinkEncrypted || !resB.LinkEncrypted {
		t.Fatalf("a lone opt-out downgraded the link: a=%v b=%v", resA.LinkEncrypted, resB.LinkEncrypted)
	}
	wire := linkRoundTrip(t, a, b, resA, resB)
	for i, f := range wire {
		if bytes.Contains(f, []byte(plainMarker)) {
			t.Fatalf("wire frame %d leaked plaintext despite one-sided opt-out", i)
		}
	}
}

// TestUpgradeTransportPeerOutdated: an old peer (no encPref) → the LAN transport is NOT upgraded
// and the status is "peer outdated". This is the new side's compat behavior.
func TestUpgradeTransportPeerOutdated(t *testing.T) {
	a, _ := newLanPipe()
	res := &Result{Role: roleInitiator, LocalEncPref: encOn, PeerEncPref: ""}
	if err := upgradeTransport(a, res); err != nil {
		t.Fatal(err)
	}
	if res.LinkEncrypted || a.active() {
		t.Fatal("upgraded against an outdated peer")
	}
	if res.LinkEncState() != LinkAuthOld {
		t.Fatalf("status = %v, want peer-outdated", res.LinkEncState())
	}
}

// bridgeLikeConn is an Upgrader that is NOT a lanTransport (models the account bridge): it must
// upgrade unconditionally, ignoring the encPref opt-out.
type bridgeLikeConn struct{ upgraded bool }

func (b *bridgeLikeConn) Send(context.Context, []byte) error   { return nil }
func (b *bridgeLikeConn) Recv(context.Context) ([]byte, error) { return nil, nil }
func (b *bridgeLikeConn) Close()                               {}
func (b *bridgeLikeConn) Upgrade(master []byte, initiator bool) error {
	b.upgraded = true
	return nil
}

func TestUpgradeTransportBridgeAlwaysEncrypts(t *testing.T) {
	c := &bridgeLikeConn{}
	res := &Result{Role: roleInitiator, SessionKey: bytes.Repeat([]byte{1}, 32),
		Transcript: []byte("t"), LocalEncPref: encOff, PeerEncPref: encOff} // both "off"
	if err := upgradeTransport(c, res); err != nil {
		t.Fatal(err)
	}
	if !c.upgraded || !res.LinkEncrypted {
		t.Fatal("bridge transport must upgrade even when both ends opted out of LAN encryption")
	}
}

// TestLinkEncNegotiationMatrix pins the decision + status for every pref combination.
func TestLinkEncNegotiationMatrix(t *testing.T) {
	cases := []struct {
		local, peer string
		enc         bool
		status      LinkEnc
	}{
		{encOn, encOn, true, LinkEncrypted},
		{encOn, encOff, true, LinkEncrypted},
		{encOff, encOn, true, LinkEncrypted},
		{encOff, encOff, false, LinkAuthYouOff},
		{encOn, "", false, LinkAuthOld},
		{encOff, "", false, LinkAuthOld},
	}
	for _, c := range cases {
		res := &Result{LocalEncPref: c.local, PeerEncPref: c.peer}
		if got := res.linkEncrypt(); got != c.enc {
			t.Errorf("linkEncrypt(local=%q peer=%q)=%v want %v", c.local, c.peer, got, c.enc)
		}
		res.LinkEncrypted = c.enc // model the upgradeTransport outcome
		if got := res.LinkEncState(); got != c.status {
			t.Errorf("LinkEncState(local=%q peer=%q)=%v want %v", c.local, c.peer, got, c.status)
		}
	}
}

// TestHelloEncPrefCompat: a new hello's encPref is ignored by the OLD hello struct (unknown-field
// tolerant), and an empty pref is omitted on the wire (so a genuine old peer decodes as absent).
func TestHelloEncPrefCompat(t *testing.T) {
	// Old struct = the hello without EncPref.
	type oldHello struct {
		T      string `json:"t"`
		PV     int    `json:"pv"`
		NodeID string `json:"nodeId"`
	}
	newRaw, err := json.Marshal(helloFrame{T: frameHello, PV: protocolVersion, NodeID: "n", EncPref: encOn})
	if err != nil {
		t.Fatal(err)
	}
	var oh oldHello
	if err := json.Unmarshal(newRaw, &oh); err != nil {
		t.Fatalf("old struct rejected a new hello: %v", err)
	}
	if oh.NodeID != "n" {
		t.Fatal("old struct dropped a known field")
	}
	// Empty pref → omitted (old peers see no field → PeerEncPref == "").
	emptyRaw, _ := json.Marshal(helloFrame{T: frameHello, PV: protocolVersion})
	if bytes.Contains(emptyRaw, []byte("encPref")) {
		t.Fatalf("empty encPref was not omitted: %s", emptyRaw)
	}
}

// TestTamperedEncPrefBreaksHandshake: flipping encPref in a hello in transit changes that side's
// transcript for the peer, so the signature no longer verifies - a wire attacker cannot strip the
// preference to force a downgrade.
func TestTamperedEncPrefBreaksHandshake(t *testing.T) {
	a, b := newPipe()
	a.tamper = func(idx int, raw []byte) []byte {
		if idx == 0 { // the initiator's hello (frame 0)
			return bytes.Replace(raw, []byte(`"encPref":"on"`), []byte(`"encPref":"xn"`), 1)
		}
		return raw
	}
	idA, idB := newIdentity(t), newIdentity(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ca, cb := make(chan hsOut, 1), make(chan hsOut, 1)
	go func() { r, e := doHandshake(ctx, a, roleInitiator, idA, nil, "", constPref(encOn)); ca <- hsOut{r, e} }()
	go func() { r, e := doHandshake(ctx, b, roleResponder, idB, nil, "", constPref(encOn)); cb <- hsOut{r, e} }()
	oa, ob := <-ca, <-cb
	if oa.err == nil && ob.err == nil {
		t.Fatal("tampered encPref was accepted; the transcript signature did not catch the downgrade")
	}
}
