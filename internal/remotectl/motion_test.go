package remotectl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestMotionSyncRPC round-trips list+get over the loopback endpoint and asserts the advertised
// sha256 matches the served bytes.
func TestMotionSyncRPC(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"name":"take1","hz":30,"frames":[]}`)
	if err := os.WriteFile(filepath.Join(dir, "take1.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	server, client := loopback()
	RegisterMotionSync(server, dir)
	rc := NewClient(client, "server")

	list, err := rc.MotionList(ctx(t))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "take1" {
		t.Fatalf("list=%+v (want only take1)", list.Items)
	}
	sum := sha256.Sum256(body)
	if list.Items[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256=%s", list.Items[0].SHA256)
	}
	if list.Items[0].Size != int64(len(body)) {
		t.Fatalf("size=%d want %d", list.Items[0].Size, len(body))
	}

	got, err := rc.MotionGet(ctx(t), "take1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(got.JSONBase64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(data) != string(body) {
		t.Fatalf("body=%q", data)
	}
}

// TestMotionGetPathTraversal asserts the server handler rejects names that escape the dir.
func TestMotionGetPathTraversal(t *testing.T) {
	dir := t.TempDir()
	// a secret one level up that traversal would target
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.json"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, client := loopback()
	RegisterMotionSync(server, dir)
	rc := NewClient(client, "server")

	for _, bad := range []string{"../secret", "..\\secret", "sub/x", "a/../b", "..", ""} {
		if _, err := rc.MotionGet(ctx(t), bad); err == nil {
			t.Fatalf("MotionGet(%q) must be rejected", bad)
		}
	}
}

// TestMotionListMissingDir asserts a missing dir yields an empty (non-error) list.
func TestMotionListMissingDir(t *testing.T) {
	server, client := loopback()
	RegisterMotionSync(server, filepath.Join(t.TempDir(), "does-not-exist"))
	rc := NewClient(client, "server")
	list, err := rc.MotionList(ctx(t))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("items=%+v want empty", list.Items)
	}
}
