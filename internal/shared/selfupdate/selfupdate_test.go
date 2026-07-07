package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64 hex

// serveFeed serves latest.json with a same-origin download URL (the SSRF guard requires it).
func serveFeed(t *testing.T, downloadURL string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+manifestName) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		u := downloadURL
		if u == "" {
			u = srv.URL + "/rave-app.exe"
		}
		fmt.Fprintf(w, `{"version":"development-abc1234","build":4242,"commit":"abc1234",`+
			`"url":%q,"sha256":%q,"released":"2026-06-04T18:30:00Z","notes":"fix things"}`, u, testSHA)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAvailableComparesBuild(t *testing.T) {
	srv := serveFeed(t, "")

	rel, avail, err := New(srv.URL, 4000, "").Available(context.Background())
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if !avail {
		t.Fatal("expected update available (4000 < 4242)")
	}
	if rel.Version != "development-abc1234" || rel.Build != 4242 {
		t.Fatalf("bad manifest: %+v", rel)
	}
	if _, avail, _ := New(srv.URL, 4242, "").Available(context.Background()); avail {
		t.Fatal("equal build should not be available")
	}
	if _, avail, _ := New(srv.URL, 5000, "").Available(context.Background()); avail {
		t.Fatal("newer local build should not be available")
	}
}

// TestRejectsForeignURL: a download URL off the feed origin is refused (SSRF guard).
func TestRejectsForeignURL(t *testing.T) {
	srv := serveFeed(t, "https://evil.example.com/rave-app.exe")
	if _, err := New(srv.URL, 1, "").Latest(context.Background()); err == nil {
		t.Fatal("expected rejection of off-origin download url")
	}
}

// TestRejectsBadSHA: a same-origin URL but a non-hex / wrong-length checksum is refused
// (no fail-open). URL is same-origin so the sha check - not the origin check - is the cause.
func TestRejectsBadSHA(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"build":9,"url":%q,"sha256":"deadbeef"}`, srv.URL+"/rave-app.exe")
	}))
	t.Cleanup(srv.Close)
	if _, err := New(srv.URL, 1, "").Latest(context.Background()); err == nil {
		t.Fatal("expected rejection of short/non-hex sha256")
	}
}

// TestDownloadCacheBusts: download() keys the binary fetch on the content hash (defeating a
// stale CDN-cached .exe) + sends no-cache, and accepts bytes matching the sha.
func TestDownloadCacheBusts(t *testing.T) {
	payload := []byte("rave-app-binary-v2")
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])

	var gotQuery, gotCacheCtl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("v")
		gotCacheCtl = r.Header.Get("Cache-Control")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	rel := &Release{URL: srv.URL + "/rave-app.exe", SHA256: sha}
	dst := t.TempDir() + "/out.bin"
	if err := New(srv.URL, 1, "").download(context.Background(), rel, dst, nil); err != nil {
		t.Fatalf("download (matching sha): %v", err)
	}
	if gotQuery != sha {
		t.Fatalf("expected cache-bust query v=%s, got %q", sha, gotQuery)
	}
	if gotCacheCtl != "no-cache" {
		t.Fatalf("expected Cache-Control: no-cache, got %q", gotCacheCtl)
	}

	rel.SHA256 = strings.Repeat("0", 64)
	if err := New(srv.URL, 1, "").download(context.Background(), rel, dst, nil); err == nil {
		t.Fatal("expected checksum mismatch for non-matching sha")
	}
}

// TestFetchAssetsStagesPlacesAndSkips: sidecar assets are downloaded next to the exe (sha-verified),
// placed by placeAsset, and skipped on a second pass when already up to date.
func TestFetchAssetsStagesAndPlaces(t *testing.T) {
	payload := []byte("openvr-dll-bytes")
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	exe := dir + "/rave-mate.exe"
	u := New(srv.URL, 1, "")
	assets := []Asset{{Name: "openvr_api.dll", URL: srv.URL + "/openvr_api.dll", SHA256: sha}}

	staged := u.fetchAssets(context.Background(), exe, assets)
	if len(staged) != 1 {
		t.Fatalf("expected 1 staged asset, got %d", len(staged))
	}
	placeAsset(staged[0])
	if got, err := fileSHA(dir + "/openvr_api.dll"); err != nil || got != sha {
		t.Fatalf("placed asset sha = %q err=%v, want %s", got, err, sha)
	}
	// Second pass: already up to date → nothing staged.
	if staged := u.fetchAssets(context.Background(), exe, assets); len(staged) != 0 {
		t.Fatalf("expected 0 staged on re-fetch (up to date), got %d", len(staged))
	}
}

// TestFetchAssetsRejectsUnsafe: path-traversal names and off-origin URLs are skipped (never staged).
func TestFetchAssetsRejectsUnsafe(t *testing.T) {
	srv := serveFeed(t, "")
	u := New(srv.URL, 1, "")
	exe := t.TempDir() + "/rave-mate.exe"
	bad := []Asset{
		{Name: "../evil.dll", URL: srv.URL + "/x.dll", SHA256: testSHA},
		{Name: "sub/evil.dll", URL: srv.URL + "/x.dll", SHA256: testSHA},
		{Name: "evil.dll", URL: "https://evil.example.com/x.dll", SHA256: testSHA},
		{Name: "evil.dll", URL: srv.URL + "/x.dll", SHA256: "nothex"},
	}
	if staged := u.fetchAssets(context.Background(), exe, bad); len(staged) != 0 {
		t.Fatalf("expected all unsafe assets rejected, got %d staged", len(staged))
	}
}

func TestDisabledWithoutFeed(t *testing.T) {
	if New("", 0, "").Enabled() {
		t.Fatal("updater should be disabled without a feed")
	}
	if _, err := New("", 0, "").Latest(context.Background()); err == nil {
		t.Fatal("Latest should error when disabled")
	}
}

// TestSignatureEnforced: with a pubkey set, a valid signature is accepted and a bad/missing
// one is rejected - a feed-write attacker can't forge a release.
func TestSignatureEnforced(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	var manifest []byte
	var sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+manifestName+".sig"):
			fmt.Fprint(w, sig)
		case strings.HasSuffix(r.URL.Path, "/"+manifestName):
			_, _ = w.Write(manifest)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	manifest = fmt.Appendf(nil, `{"build":7,"url":%q,"sha256":%q}`, srv.URL+"/rave-app.exe", testSHA)

	sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	if _, err := New(srv.URL, 1, pubB64).Latest(context.Background()); err != nil {
		t.Fatalf("valid signature should verify: %v", err)
	}
	manifest = fmt.Appendf(nil, `{"build":999,"url":%q,"sha256":%q}`, srv.URL+"/rave-app.exe", testSHA)
	if _, err := New(srv.URL, 1, pubB64).Latest(context.Background()); err == nil {
		t.Fatal("tampered manifest must fail signature verification")
	}
}
