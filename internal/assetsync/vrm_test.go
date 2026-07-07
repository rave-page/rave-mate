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

	"rave.page/mate/internal/remotectl"
)

// fakeVRMClient serves in-memory avatar files, returning at most serverChunk bytes per getChunk so a
// file larger than serverChunk forces the reconcile loop to reassemble multiple chunks.
type fakeVRMClient struct {
	files       map[string][]byte
	serverChunk int // 0 = unlimited (whole file in one chunk)
	listErr     error
	getCalls    int
}

func (f *fakeVRMClient) VRMList(context.Context) (remotectl.VRMListResult, error) {
	if f.listErr != nil {
		return remotectl.VRMListResult{}, f.listErr
	}
	var items []remotectl.VRMMeta
	for name, b := range f.files {
		sum := sha256.Sum256(b)
		items = append(items, remotectl.VRMMeta{Name: name, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
	}
	return remotectl.VRMListResult{Items: items}, nil
}

func (f *fakeVRMClient) VRMGetChunk(_ context.Context, name string, offset int64, n int) (remotectl.VRMGetChunkResult, error) {
	f.getCalls++
	b, ok := f.files[name]
	if !ok {
		return remotectl.VRMGetChunkResult{}, errors.New("not found")
	}
	if offset >= int64(len(b)) {
		return remotectl.VRMGetChunkResult{DataBase64: "", EOF: true}, nil
	}
	end := offset + int64(n)
	if f.serverChunk > 0 && offset+int64(f.serverChunk) < end {
		end = offset + int64(f.serverChunk)
	}
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	return remotectl.VRMGetChunkResult{
		DataBase64: base64.StdEncoding.EncodeToString(b[offset:end]),
		EOF:        end >= int64(len(b)),
	}, nil
}

func hashOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// A large avatar pulled across many server-clamped chunks reassembles byte-exact + verifies its hash.
func TestReconcileVRMChunkedReassembly(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, 5000)
	for i := range big {
		big[i] = byte(i * 7)
	}
	cl := &fakeVRMClient{files: map[string][]byte{"avatar.vrm": big}, serverChunk: 512} // ~10 chunks
	res, err := ReconcileVRM(context.Background(), cl, dir)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.Pulled) != 1 || res.Pulled[0] != "avatar.vrm" {
		t.Fatalf("pulled = %v, want [avatar.vrm]", res.Pulled)
	}
	got, err := os.ReadFile(filepath.Join(dir, "avatar.vrm"))
	if err != nil {
		t.Fatalf("read pulled: %v", err)
	}
	if hashOf(got) != hashOf(big) {
		t.Fatalf("reassembled file differs from source (len got=%d want=%d)", len(got), len(big))
	}
	if cl.getCalls < 9 {
		t.Fatalf("expected multiple chunked gets, got %d", cl.getCalls)
	}
}

// A local copy whose sha256 already matches is skipped (no re-pull).
func TestReconcileVRMSkipsUpToDate(t *testing.T) {
	dir := t.TempDir()
	data := []byte("glb-bytes-here")
	if err := os.WriteFile(filepath.Join(dir, "a.glb"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cl := &fakeVRMClient{files: map[string][]byte{"a.glb": data}}
	res, err := ReconcileVRM(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || len(res.Pulled) != 0 {
		t.Fatalf("skipped=%d pulled=%v, want 1/none", res.Skipped, res.Pulled)
	}
	if cl.getCalls != 0 {
		t.Fatalf("up-to-date file was fetched (%d gets)", cl.getCalls)
	}
}

// A changed local copy (different bytes → different sha) is re-pulled and overwritten.
func TestReconcileVRMReplacesChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.vrm"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := []byte("NEW avatar bytes")
	cl := &fakeVRMClient{files: map[string][]byte{"a.vrm": remote}}
	res, err := ReconcileVRM(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pulled) != 1 {
		t.Fatalf("pulled=%v, want [a.vrm]", res.Pulled)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.vrm"))
	if string(got) != "NEW avatar bytes" {
		t.Fatalf("changed file not replaced: %q", got)
	}
}

// A non-avatar or traversal name in the peer's list is rejected, not written.
func TestReconcileVRMRejectsUnsafeOrNonAvatar(t *testing.T) {
	dir := t.TempDir()
	cl := &fakeVRMClient{files: map[string][]byte{
		"notes.txt":     []byte("x"), // wrong extension
		"../escape.vrm": []byte("y"), // traversal
		"real.vrm":      []byte("z"), // ok
	}}
	res, err := ReconcileVRM(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pulled) != 1 || res.Pulled[0] != "real.vrm" {
		t.Fatalf("pulled=%v, want [real.vrm]", res.Pulled)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("errors=%v, want 2 rejected", res.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err == nil {
		t.Fatal("non-avatar file was written")
	}
}

// A byte that fails its advertised hash is not written (corruption guard).
func TestReconcileVRMHashMismatchNotWritten(t *testing.T) {
	dir := t.TempDir()
	cl := &badHashVRMClient{name: "a.vrm", data: []byte("payload")}
	res, err := ReconcileVRM(context.Background(), cl, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pulled) != 0 || len(res.Errors) != 1 {
		t.Fatalf("pulled=%v errors=%v, want none/1", res.Pulled, res.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.vrm")); err == nil {
		t.Fatal("mismatched file was written")
	}
}

// badHashVRMClient advertises a wrong sha256 in its list so the content check must reject the pull.
type badHashVRMClient struct {
	name string
	data []byte
}

func (b *badHashVRMClient) VRMList(context.Context) (remotectl.VRMListResult, error) {
	return remotectl.VRMListResult{Items: []remotectl.VRMMeta{
		{Name: b.name, Size: int64(len(b.data)), SHA256: "deadbeef"},
	}}, nil
}

func (b *badHashVRMClient) VRMGetChunk(_ context.Context, _ string, offset int64, _ int) (remotectl.VRMGetChunkResult, error) {
	if offset >= int64(len(b.data)) {
		return remotectl.VRMGetChunkResult{EOF: true}, nil
	}
	return remotectl.VRMGetChunkResult{DataBase64: base64.StdEncoding.EncodeToString(b.data[offset:]), EOF: true}, nil
}
