package spoutdll

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// extractDLL must pick the MT (static-CRT) SpoutLibrary.dll, not the MD variant - the MD build
// would add a Visual C++ redistributable dependency and reintroduce the missing-DLL crash class.
func TestExtractPicksMT(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"Spout-SDK-binaries/Libs_2-007-017/MD/bin/SpoutLibrary.dll": "MD-variant",
		"Spout-SDK-binaries/Libs_2-007-017/MT/bin/SpoutLibrary.dll": "MT-variant",
		"Spout-SDK-binaries/readme.txt":                             "noise",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), DLLName)
	if err := extractDLL(zr, dst); err != nil {
		t.Fatalf("extractDLL: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "MT-variant" {
		t.Fatalf("extracted %q, want the MT variant", got)
	}
}

// A zip with no SpoutLibrary.dll is an error (not a silent empty file).
func TestExtractMissing(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Spout-SDK-binaries/readme.txt")
	_, _ = w.Write([]byte("no dll here"))
	_ = zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err := extractDLL(zr, filepath.Join(t.TempDir(), DLLName)); err == nil {
		t.Fatal("expected error for archive without the DLL")
	}
}
