package mediatools

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildZip builds an in-memory zip from name->content (mirrors a vendor archive layout so
// the unpack path is exercised without the network).
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func readerFrom(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	return zr
}

// TestExtractBins pulls the requested exes out of a nested, versioned archive layout
// (matched by basename) and ignores the extras.
func TestExtractBins(t *testing.T) {
	base := "ffmpeg-7.1-essentials_build"
	zb := buildZip(t, map[string]string{
		base + "/":                          "",
		base + "/bin/" + exeName("ffmpeg"):  "FFMPEG-BODY",
		base + "/bin/" + exeName("ffprobe"): "FFPROBE-BODY",
		base + "/bin/" + exeName("ffplay"):  "FFPLAY-BODY", // extra, not requested
		base + "/README.txt":                "hi",
	})

	dir := t.TempDir()
	if err := extractBins(readerFrom(t, zb), dir, []string{"ffmpeg", "ffprobe"}); err != nil {
		t.Fatalf("extractBins: %v", err)
	}

	for name, want := range map[string]string{
		exeName("ffmpeg"):  "FFMPEG-BODY",
		exeName("ffprobe"): "FFPROBE-BODY",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// The un-requested extra must not be extracted.
	if _, err := os.Stat(filepath.Join(dir, exeName("ffplay"))); !os.IsNotExist(err) {
		t.Errorf("ffplay should not have been extracted (err=%v)", err)
	}
}

// TestExtractBinsMissing errors when a requested binary isn't in the archive.
func TestExtractBinsMissing(t *testing.T) {
	zb := buildZip(t, map[string]string{
		"pkg/" + exeName("fpcalc"): "FP", // ffprobe absent
	})
	err := extractBins(readerFrom(t, zb), t.TempDir(), []string{"fpcalc", "ffprobe"})
	if err == nil {
		t.Fatal("expected an error for the missing binary, got nil")
	}
}

func TestIsHex64(t *testing.T) {
	good := "36b478e16aa69f757f376645db0d436073a42c0097b6bb2677109e7835b59bbc"
	if !isHex64(good) {
		t.Errorf("isHex64(%q) = false, want true", good)
	}
	for _, bad := range []string{"", "abc", good + "00", "ZZ" + good[2:]} {
		if isHex64(bad) {
			t.Errorf("isHex64(%q) = true, want false", bad)
		}
	}
}
