package remotectl

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readVRMChunk returns a mid-file chunk with EOF=false, the tail chunk with EOF=true, and an empty
// EOF chunk once the offset reaches end-of-file - the sequence the reconcile loop relies on.
func TestReadVRMChunkEOF(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	writeFile(t, dir, "a.vrm", data)

	mid, err := readVRMChunk(dir, VRMGetChunkParams{Name: "a.vrm", Offset: 0, Len: 40})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := base64.StdEncoding.DecodeString(mid.DataBase64); len(b) != 40 || mid.EOF {
		t.Fatalf("mid chunk: len=%d eof=%v, want 40/false", len(b), mid.EOF)
	}
	tail, err := readVRMChunk(dir, VRMGetChunkParams{Name: "a.vrm", Offset: 80, Len: 40})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := base64.StdEncoding.DecodeString(tail.DataBase64); len(b) != 20 || !tail.EOF {
		t.Fatalf("tail chunk: len=%d eof=%v, want 20/true", len(b), tail.EOF)
	}
	past, err := readVRMChunk(dir, VRMGetChunkParams{Name: "a.vrm", Offset: 100, Len: 40})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := base64.StdEncoding.DecodeString(past.DataBase64); len(b) != 0 || !past.EOF {
		t.Fatalf("past-end chunk: len=%d eof=%v, want 0/true", len(b), past.EOF)
	}
}

func TestReadVRMChunkRejectsTraversalAndNonAvatar(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.vrm", []byte("x"))
	for _, bad := range []string{"../secret", "a.txt", "sub/a.vrm", "..", ""} {
		if _, err := readVRMChunk(dir, VRMGetChunkParams{Name: bad, Offset: 0, Len: 8}); err == nil {
			t.Fatalf("readVRMChunk accepted unsafe/non-avatar name %q", bad)
		}
	}
}

func TestVRMListEnumeratesAvatarsOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.vrm", []byte("aaa"))
	writeFile(t, dir, "b.glb", []byte("bbbb"))
	writeFile(t, dir, "notes.txt", []byte("ignore"))
	res, err := vrmList(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items=%d, want 2 (a.vrm,b.glb)", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Name == "notes.txt" {
			t.Fatal("non-avatar listed")
		}
		if it.SHA256 == "" || it.Size == 0 {
			t.Fatalf("meta incomplete: %+v", it)
		}
	}
}

// A missing dir lists empty (not an error) so sync is a no-op until the first avatar is added.
func TestVRMListMissingDirEmpty(t *testing.T) {
	res, err := vrmList(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("items=%d, want 0", len(res.Items))
	}
}
