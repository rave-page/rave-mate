package vrdll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// download verifies the SHA-256 of the fetched bytes and writes them only on a match.
func TestDownloadVerifiesSHA(t *testing.T) {
	payload := []byte("openvr-dll-bytes")
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), DLLName)
	if err := download(context.Background(), srv.URL+"/"+DLLName, sha, dst, nil); err != nil {
		t.Fatalf("download (matching sha): %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("written bytes = %q err=%v", got, err)
	}
	// Wrong expected hash → error, no acceptance.
	if err := download(context.Background(), srv.URL+"/"+DLLName, strings.Repeat("0", 64), dst, nil); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

// Install refuses cleanly on a dev build (empty feed) instead of producing a bad partial DLL.
func TestInstallNeedsFeed(t *testing.T) {
	if !CanInstall() {
		t.Skip("install is Windows-only")
	}
	if err := Install(context.Background(), "", nil); err == nil {
		t.Fatal("expected error with empty feed URL")
	}
}
