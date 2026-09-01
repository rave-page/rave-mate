package peers

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/store"
)

func TestStoreRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	p := Peer{NodeID: "n1", IdentityPub: pub, Nickname: "Laptop", Trusted: true, PairedAt: time.Unix(1, 0).UTC()}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}

	got, ok := s.Get("n1")
	if !ok || got.Nickname != "Laptop" || !got.IdentityPub.Equal(pub) {
		t.Fatalf("round-trip mismatch: %+v ok=%v", got, ok)
	}
	if key, ok := s.TrustedKey("n1"); !ok || !key.Equal(pub) {
		t.Error("TrustedKey did not return the stored trusted key")
	}

	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("want 1 peer, got %d", len(list))
	}

	if err := s.Forget("n1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("n1"); ok {
		t.Error("peer not forgotten")
	}
}

func TestEncOffRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := s.Save(Peer{NodeID: "n1", IdentityPub: pub, Trusted: true}); err != nil {
		t.Fatal(err)
	}
	// Default: everything encrypted.
	if s.EncOff("n1", PlaneControl) || s.EncOff("n1", PlaneFiles) || s.EncOff("n1", PlaneMedia) {
		t.Fatal("fresh peer should have no opt-out")
	}
	// Opt out of two planes; persist + reload.
	if err := s.SetEncOff("n1", PlaneControl, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEncOff("n1", PlaneMedia, true); err != nil {
		t.Fatal(err)
	}
	if !s.EncOff("n1", PlaneControl) || !s.EncOff("n1", PlaneMedia) || s.EncOff("n1", PlaneFiles) {
		t.Fatalf("opt-out not persisted: %+v", func() map[string]bool { p, _ := s.Get("n1"); return p.EncOff }())
	}
	// Other fields survive the read-modify-write.
	if p, _ := s.Get("n1"); !p.Trusted || !p.IdentityPub.Equal(pub) {
		t.Fatal("SetEncOff clobbered other peer fields")
	}
	// Clearing removes the plane; emptying nils the map.
	if err := s.SetEncOff("n1", PlaneControl, false); err != nil {
		t.Fatal(err)
	}
	if s.EncOff("n1", PlaneControl) {
		t.Fatal("opt-out not cleared")
	}
	if err := s.SetEncOff("n1", PlaneMedia, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get("n1"); p.EncOff != nil {
		t.Fatalf("emptied EncOff should be nil, got %v", p.EncOff)
	}
	// Unknown peer: no-op, still reports default.
	if err := s.SetEncOff("ghost", PlaneFiles, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("ghost"); ok {
		t.Fatal("SetEncOff created a bare entry for an unknown peer")
	}
	if s.EncOff("ghost", PlaneFiles) {
		t.Fatal("unknown peer should report encrypted")
	}
}

func TestNilStoreSafe(t *testing.T) {
	var s *Store
	if _, err := s.List(); err != nil {
		t.Error(err)
	}
	if err := s.Save(Peer{NodeID: "x"}); err != nil {
		t.Error(err)
	}
	if _, ok := New(nil).TrustedKey("x"); ok {
		t.Error("nil store should not return a trusted key")
	}
}
