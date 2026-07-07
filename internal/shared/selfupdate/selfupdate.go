// Package selfupdate gives a desktop build an in-app updater: poll a JSON manifest on the
// same feed the binary was published to, and, on confirm, download the new executable, verify
// its SHA-256, swap it in (Windows-safe rename dance), and relaunch. Shared by rave-mate and
// rave-app - each passes its own feed URL, current build, and (optional) update public key.
//
// "Newer" is decided by the manifest's monotonic Build number (CI pipeline id), not semver -
// dev builds carry a "-branch.sha" prerelease that semver can't order. An empty feed URL = a
// disabled updater (Enabled() == false), as on a dev build.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// manifestName is the file polled under the feed URL.
const manifestName = "latest.json"

// AwaitRestartEnv, when set to "1" in a relaunched process's environment, tells it to wait for
// the prior instance to release its single-instance lock instead of immediately deferring to it.
// Set by Relaunch; honored by the app's single-instance acquire. Without it a relaunch can race
// the exiting old process for the lock and lose (then forward-show + exit → "app didn't start").
const AwaitRestartEnv = "RAVE_AWAIT_RESTART"

// Release is the decoded update manifest (written by CI - see the deploy job). url/sha256 are
// the updater's target: a VERSIONED raw exe so the fixed feed filename can never be served
// stale by a CDN. installer_url / linux_url are for the web download page; the updater ignores
// them. All download URLs are versioned + same-origin with the feed.
type Release struct {
	Version         string `json:"version"`
	Build           int    `json:"build"`
	Commit          string `json:"commit"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	InstallerURL    string `json:"installer_url,omitempty"`
	InstallerSHA256 string `json:"installer_sha256,omitempty"`
	LinuxURL        string `json:"linux_url,omitempty"`
	LinuxSHA256     string `json:"linux_sha256,omitempty"`
	Released        string `json:"released"`
	Notes           string `json:"notes"`
	// Assets are sidecar files shipped beside the exe (e.g. runtime-loaded DLLs: openvr_api.dll,
	// SpoutLibrary.dll). The in-app updater swaps only the exe, so without these a fresh exe that
	// gained a runtime-loaded feature would launch with that feature dead. The updater places each
	// next to the swapped exe (sha256-verified, same-origin) so the feature keeps working.
	Assets []Asset `json:"assets,omitempty"`
}

// Asset is one sidecar file placed next to the exe on update. Name is a bare basename (no path
// separators - rejected otherwise); URL is same-origin with the feed; SHA256 gates the bytes.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Updater polls a feed + applies updates. Zero value is unusable; build with New.
type Updater struct {
	feedURL   string
	feedHost  string // origin the download URL must stay within
	current   int
	pubKeyB64 string // base64 raw 32-byte Ed25519 key; "" disables signature enforcement
	http      *http.Client
}

// New builds an updater. feedURL is the per-branch feed (e.g. https://x.rave.page/app/app/);
// current is this binary's build number; pubKeyB64 is the base64 Ed25519 manifest-signing
// public key ("" = unsigned, rely on sha256 + same-origin). Always non-nil; Enabled() reports
// whether it can actually check (false when feedURL is empty, e.g. a dev build).
func New(feedURL string, current int, pubKeyB64 string) *Updater {
	feedURL = strings.TrimRight(feedURL, "/")
	feedHost := ""
	if pu, err := url.Parse(feedURL); err == nil {
		feedHost = pu.Host
	}
	return &Updater{
		feedURL:   feedURL,
		feedHost:  feedHost,
		current:   current,
		pubKeyB64: pubKeyB64,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Refuse redirects that leave the feed origin - a 30x must not be a way around the
			// same-origin check on the download URL.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if feedHost != "" && !strings.EqualFold(req.URL.Host, feedHost) {
					return fmt.Errorf("refusing cross-origin redirect to %s", req.URL.Host)
				}
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// Enabled reports whether the updater has a feed to poll (false on a dev build).
func (u *Updater) Enabled() bool { return u != nil && u.feedURL != "" }

// Latest fetches, verifies, and validates the manifest. Order matters: when a public key is
// set, the manifest's detached signature (latest.json.sig) is verified over the RAW bytes
// BEFORE anything in it is trusted - so a feed-write attacker can't forge a release without
// the private key. Then: download URL must be same-origin with the feed; sha256 must be a
// 64-char hex digest (no fail-open).
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	if !u.Enabled() {
		return nil, fmt.Errorf("updater disabled (no feed)")
	}
	body, err := u.fetch(ctx, manifestName, 1<<20)
	if err != nil {
		return nil, err
	}
	if err := u.verifySignature(ctx, body); err != nil {
		return nil, err
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := u.validateURL(rel.URL); err != nil {
		return nil, err
	}
	if !isHex64(rel.SHA256) {
		return nil, fmt.Errorf("manifest sha256 must be a 64-char hex digest")
	}
	rel.SHA256 = strings.ToLower(rel.SHA256)
	return &rel, nil
}

// fetch GETs name relative to the feed (origin-bound client) and returns up to limit bytes.
func (u *Updater) fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.feedURL+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned %s for %s", resp.Status, name)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// verifySignature checks the detached Ed25519 signature over the manifest bytes. No-op when no
// public key is set; when one IS set, a missing or invalid signature is a hard failure.
func (u *Updater) verifySignature(ctx context.Context, manifest []byte) error {
	pub := u.embeddedPubKey()
	if pub == nil {
		return nil // signing not provisioned → rely on sha256 + same-origin
	}
	sigB64, err := u.fetch(ctx, manifestName+".sig", 1<<12)
	if err != nil {
		return fmt.Errorf("fetch manifest signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("manifest signature not base64: %w", err)
	}
	if !ed25519.Verify(pub, manifest, sig) {
		return fmt.Errorf("manifest signature invalid (not signed by the build key)")
	}
	return nil
}

// embeddedPubKey decodes pubKeyB64 (base64 raw 32-byte key) → nil if unset/bad.
func (u *Updater) embeddedPubKey() ed25519.PublicKey {
	if u.pubKeyB64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(u.pubKeyB64))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(raw)
}

// validateURL requires the download URL to share the feed's scheme+host and sit under the
// feed's path. The real feed is https (a dev build has no feed → updater disabled), so this
// enforces https in production while staying testable against an http test server.
func (u *Updater) validateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("manifest has no download url")
	}
	p, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("manifest url invalid: %w", err)
	}
	f, err := url.Parse(u.feedURL)
	if err != nil {
		return err
	}
	if !strings.EqualFold(p.Scheme, f.Scheme) || !strings.EqualFold(p.Host, f.Host) ||
		!strings.HasPrefix(p.Path, strings.TrimSuffix(f.Path, "/")+"/") {
		return fmt.Errorf("manifest url not on feed origin (%s)", u.feedURL)
	}
	return nil
}

// cacheBustURL appends the content hash as a query param so a CDN/proxy keys the binary on its
// content, not just the fixed filename - defeats a stale cached .exe. Same scheme/host/path
// (validateURL already vetted those); only the query changes.
func cacheBustURL(raw, sha string) (string, error) {
	p, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("manifest url invalid: %w", err)
	}
	q := p.Query()
	q.Set("v", strings.ToLower(strings.TrimSpace(sha)))
	p.RawQuery = q.Encode()
	return p.String(), nil
}

// isHex64 reports whether s is exactly 64 hex chars (a SHA-256 digest).
func isHex64(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Available returns the release + true when the feed's build is newer than this binary's. The
// build comparison is platform-agnostic (testable); applying it is Windows-only (Apply gates
// that) since the manifest's checksum is the Windows artifact's.
func (u *Updater) Available(ctx context.Context) (*Release, bool, error) {
	rel, err := u.Latest(ctx)
	if err != nil {
		return nil, false, err
	}
	return rel, rel.Build > u.current, nil
}

// Apply downloads rel's binary, verifies its SHA-256, and atomically swaps it over the running
// executable (rename current → .old, move new into place). It does NOT relaunch; call Relaunch
// after, or prompt the user. onProgress (optional) gets bytes-downloaded / total (total 0 if
// the server sent no Content-Length).
func (u *Updater) Apply(ctx context.Context, rel *Release, onProgress func(done, total int64)) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("auto-update is Windows-only on this build; download the new binary manually")
	}
	if !isHex64(rel.SHA256) {
		return fmt.Errorf("refusing update: manifest sha256 missing/invalid")
	}
	if err := u.validateURL(rel.URL); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)

	tmp := exe + ".new"
	if err := u.download(ctx, rel, tmp, onProgress); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// Pre-fetch sidecar assets (runtime-loaded DLLs) to <name>.new next to the exe BEFORE the swap,
	// so the swap window touches local files only. Best-effort: a failed/locked asset must never
	// abort the exe update (the exe is the critical payload; a DLL just gates one feature).
	staged := u.fetchAssets(ctx, exe, rel.Assets)

	// Swap. On Windows a running .exe can be renamed (not deleted/overwritten), so move the
	// live binary aside, then move the new one into its place. The .old is cleaned next startup.
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(tmp)
		for _, s := range staged {
			_ = os.Remove(s)
		}
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe) // roll back
		_ = os.Remove(tmp)
		for _, s := range staged {
			_ = os.Remove(s)
		}
		return fmt.Errorf("install new binary: %w", err)
	}
	// Move staged assets into place AFTER the exe swap (so a crash mid-update never leaves newer
	// DLLs paired with an older exe). Best-effort per asset.
	for _, s := range staged {
		placeAsset(s)
	}
	return nil
}

// fetchAssets downloads each sidecar asset to "<dir>/<name>.new" (sha-verified), returning the
// staged temp paths. Skips assets already present with the right hash (avoids touching an in-use,
// locked, identical DLL) and any that fail to download. dir is the exe's directory.
func (u *Updater) fetchAssets(ctx context.Context, exe string, assets []Asset) []string {
	dir := filepath.Dir(exe)
	var staged []string
	for _, a := range assets {
		name := filepath.Base(a.Name)
		if name == "" || name == "." || name != a.Name { // reject path traversal / nested paths
			continue
		}
		if !isHex64(a.SHA256) || u.validateURL(a.URL) != nil {
			continue
		}
		dst := filepath.Join(dir, name)
		_ = os.Remove(dst + ".old") // clean a prior locked-rename leftover
		if got, err := fileSHA(dst); err == nil && got == strings.ToLower(a.SHA256) {
			continue // already up to date
		}
		tmp := dst + ".new"
		if err := u.downloadURL(ctx, a.URL, a.SHA256, tmp, nil); err != nil {
			_ = os.Remove(tmp)
			continue
		}
		staged = append(staged, tmp)
	}
	return staged
}

// placeAsset moves a "<dst>.new" staged file over <dst>. If <dst> is locked (in use), it's moved
// aside to <dst>.old (cleaned on the next update) so the rename can still land.
func placeAsset(tmp string) {
	dst := strings.TrimSuffix(tmp, ".new")
	if _, err := os.Stat(dst); err == nil {
		if os.Remove(dst) != nil {
			_ = os.Rename(dst, dst+".old")
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
	}
}

// fileSHA returns the lowercase hex SHA-256 of a file.
func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// download streams rel.URL to dst, verifying SHA-256 and the executable bit.
func (u *Updater) download(ctx context.Context, rel *Release, dst string, onProgress func(done, total int64)) error {
	return u.downloadURL(ctx, rel.URL, rel.SHA256, dst, onProgress)
}

// downloadURL streams rawURL to dst, verifying it matches sha (lowercase hex64).
func (u *Updater) downloadURL(ctx context.Context, rawURL, sha, dst string, onProgress func(done, total int64)) error {
	want := strings.ToLower(strings.TrimSpace(sha))
	if !isHex64(want) {
		return fmt.Errorf("refusing download: sha256 missing/invalid")
	}
	// Cache-bust the binary fetch (the feed may serve a fixed filename a CDN caches
	// independently of latest.json). Key the URL on the expected sha256 + no-cache headers.
	dlURL, err := cacheBustURL(rawURL, want)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, cb: onProgress}
	if _, err := io.Copy(io.MultiWriter(f, h), pr); err != nil {
		_ = f.Close()
		return fmt.Errorf("download: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}

// CleanupOld removes the .old/.new binaries left by a previous Apply. Call once at startup.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		if exe, e := filepath.EvalSymlinks(exe); e == nil {
			_ = os.Remove(exe + ".old")
			_ = os.Remove(exe + ".new")
		}
	}
}

// progressReader reports cumulative read progress.
type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	cb    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.cb != nil {
		p.cb(p.done, p.total)
	}
	return n, err
}
