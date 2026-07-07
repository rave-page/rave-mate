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

	"rave.page/mate/internal/shared/selfupdate"
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

	client := &http.Client{CheckRedirect: selfupdate.RedirectPolicy(srv.URL)}
	dst := filepath.Join(t.TempDir(), DLLName)
	if err := download(context.Background(), client, srv.URL+"/"+DLLName, sha, dst, nil); err != nil {
		t.Fatalf("download (matching sha): %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("written bytes = %q err=%v", got, err)
	}
	// Wrong expected hash → error, no acceptance.
	if err := download(context.Background(), client, srv.URL+"/"+DLLName, strings.Repeat("0", 64), dst, nil); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

// The DLL fetch enforces the feed redirect policy: a feed 302ing off-origin is refused.
func TestDownloadRefusesCrossOriginRedirect(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("evil"))
	}))
	t.Cleanup(evil.Close)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/"+DLLName, http.StatusFound)
	}))
	t.Cleanup(feed.Close)

	client := &http.Client{CheckRedirect: selfupdate.RedirectPolicy(feed.URL)}
	dst := filepath.Join(t.TempDir(), DLLName)
	err := download(context.Background(), client, feed.URL+"/"+DLLName, strings.Repeat("a", 64), dst, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing cross-origin redirect") {
		t.Fatalf("want cross-origin redirect refusal, got %v", err)
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
