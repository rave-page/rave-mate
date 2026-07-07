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
