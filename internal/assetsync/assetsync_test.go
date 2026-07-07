package assetsync

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
)

// fakeClient serves recordings from an in-memory name→bytes map, tracking get calls.
type fakeClient struct {
	files map[string][]byte
	gets  []string
	listE error
}

func (f *fakeClient) MotionList(context.Context) (remotectl.MotionListResult, error) {
	if f.listE != nil {
		return remotectl.MotionListResult{}, f.listE
	}
	items := make([]remotectl.MotionMeta, 0, len(f.files))
	for name, b := range f.files {
		sum := sha256.Sum256(b)
		items = append(items, remotectl.MotionMeta{Name: name, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
	}
	return remotectl.MotionListResult{Items: items}, nil
}

func (f *fakeClient) MotionGet(_ context.Context, name string) (remotectl.MotionGetResult, error) {
	f.gets = append(f.gets, name)
	b, ok := f.files[name]
	if !ok {
		return remotectl.MotionGetResult{}, errors.New("not found")
	}
	return remotectl.MotionGetResult{JSONBase64: base64.StdEncoding.EncodeToString(b)}, nil
}

func write(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilePullsMissingAndChanged(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "same", []byte(`{"a":1}`))    // identical on both → skip
	write(t, dir, "stale", []byte(`{"old":1}`)) // differs → re-pull

	cl := &fakeClient{files: map[string][]byte{
		"same":  []byte(`{"a":1}`),   // matches local
		"stale": []byte(`{"new":1}`), // changed
		"new":   []byte(`{"n":1}`),   // absent locally
	}}

	res, err := ReconcileMotion(context.Background(), cl, dir)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Skipped != 1 {
		t.Fatalf("skipped=%d want 1", res.Skipped)
	}
	if len(res.Pulled) != 2 {
		t.Fatalf("pulled=%v want 2 (stale,new)", res.Pulled)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors=%v", res.Errors)
	}
	// "same" must never be fetched (idempotent skip)
	for _, g := range cl.gets {
		if g == "same" {
			t.Fatal("fetched unchanged recording")
		}
	}
	// disk reflects the peer content
	got, _ := os.ReadFile(filepath.Join(dir, "stale.json"))
	if string(got) != `{"new":1}` {
		t.Fatalf("stale=%q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Fatalf("new.json not written: %v", err)
	}
}

func TestReconcileIdempotent(t *testing.T) {
	dir := t.TempDir()
	cl := &fakeClient{files: map[string][]byte{"x": []byte(`{"x":1}`)}}
	if _, err := ReconcileMotion(context.Background(), cl, dir); err != nil {
		t.Fatal(err)
	}
	res, err := ReconcileMotion(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || len(res.Pulled) != 0 {
		t.Fatalf("second pass pulled=%v skipped=%d (want skip)", res.Pulled, res.Skipped)
	}
}

func TestReconcileRejectsUnsafeName(t *testing.T) {
	dir := t.TempDir()
	cl := &fakeClient{files: map[string][]byte{"../evil": []byte("x")}}
	res, err := ReconcileMotion(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Errors["../evil"]; !ok {
		t.Fatalf("unsafe name not rejected: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.json")); !os.IsNotExist(err) {
		t.Fatal("traversal wrote outside dir")
	}
}

func TestReconcileHashMismatch(t *testing.T) {
	dir := t.TempDir()
	// fake list advertises a hash that won't match the served bytes
	cl := &badHashClient{name: "bad", body: []byte("real"), advertised: "deadbeef"}
	res, err := ReconcileMotion(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors["bad"] != "sha256 mismatch" {
		t.Fatalf("errors=%v want sha256 mismatch", res.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); !os.IsNotExist(err) {
		t.Fatal("mismatched body persisted")
	}
}

func TestReconcileListError(t *testing.T) {
	if _, err := ReconcileMotion(context.Background(), &fakeClient{listE: errors.New("down")}, t.TempDir()); err == nil {
		t.Fatal("list error must propagate")
	}
}

// badHashClient advertises a wrong sha256 in the list to exercise verify-before-write.
type badHashClient struct {
	name       string
	body       []byte
	advertised string
}

func (b *badHashClient) MotionList(context.Context) (remotectl.MotionListResult, error) {
	return remotectl.MotionListResult{Items: []remotectl.MotionMeta{{Name: b.name, Size: int64(len(b.body)), SHA256: b.advertised}}}, nil
}

func (b *badHashClient) MotionGet(context.Context, string) (remotectl.MotionGetResult, error) {
	return remotectl.MotionGetResult{JSONBase64: base64.StdEncoding.EncodeToString(b.body)}, nil
}

// fakeMgr is a PeerManager over a fixed connection list.
type fakeMgr struct{ conns []peerlink.ConnInfo }

func (m fakeMgr) Connections() []peerlink.ConnInfo { return m.conns }

func TestReconcileAllPeersSkipsUnconnected(t *testing.T) {
	dir := t.TempDir()
	mgr := fakeMgr{conns: []peerlink.ConnInfo{
		{NodeID: "up", Status: peerlink.StatusConnected},
		{NodeID: "down", Status: peerlink.StatusConnecting},
	}}
	cl := &fakeClient{files: map[string][]byte{"x": []byte(`{"x":1}`)}}
	built := map[string]bool{}
	out := ReconcileAllPeers(context.Background(), mgr, func(nodeID string) MotionClient {
		built[nodeID] = true
		return cl
	}, dir)
	if len(out) != 1 {
		t.Fatalf("results=%v want only connected peer", out)
	}
	if _, ok := out["up"]; !ok {
		t.Fatal("connected peer missing from results")
	}
	if built["down"] {
		t.Fatal("client built for unconnected peer")
	}
}
