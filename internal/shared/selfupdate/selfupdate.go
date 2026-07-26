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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
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
	pubKeyB64 string       // base64 raw 32-byte Ed25519 key; "" disables signature enforcement
	http      *http.Client // small feed fetches (manifest + .sig): total 30s cap is fine
	dl        *http.Client // binary downloads: NO total cap - phase timeouts + stall watchdog
	dlStall   time.Duration
	dlRetries int
	dlBackoff time.Duration
}

// Download tuning. A total-time cap (http.Client.Timeout / ctx deadline) killed slow-but-flowing
// downloads ("context deadline exceeded ... while reading body"); instead, connect/TLS/header
// phases get bounded timeouts and the body read is bounded only by a stall watchdog: abort when
// ZERO bytes arrive for dlStall, reset on every read. Transient failures (stall, reset, 5xx)
// retry with backoff, resuming via Range/If-Range from the persisted partial.
const (
	dlStallDefault   = 60 * time.Second
	dlRetriesDefault = 5
	dlBackoffDefault = 2 * time.Second // doubles per retry: 2s 4s 8s 16s 32s
)

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
	// Refuse redirects that leave the feed origin - a 30x must not be a way around the
	// same-origin check on the download URL. (GitHub feeds get the CDN carve-out, see
	// RedirectPolicy.)
	redirect := RedirectPolicy(feedURL)
	return &Updater{
		feedURL:   feedURL,
		feedHost:  feedHost,
		current:   current,
		pubKeyB64: pubKeyB64,
		http: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: redirect,
		},
		// Download client: no Client.Timeout (it spans the whole body read and killed slow
		// connections). Connect/TLS/headers are individually bounded; the body read is guarded
		// by the stall watchdog in fetchResumable.
		dl: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
			CheckRedirect: redirect,
		},
		dlStall:   dlStallDefault,
		dlRetries: dlRetriesDefault,
		dlBackoff: dlBackoffDefault,
	}
}

// githubFeedHost reports whether host (the feed's) is github.com - the ONLY feed origin
// granted the release-asset CDN carve-out below. Any other feed stays strictly same-origin.
func githubFeedHost(host string) bool {
	return strings.EqualFold(host, "github.com")
}

// githubCDNURL reports whether u is an https URL on GitHub's release-asset CDN
// (*.githubusercontent.com - the exact host has changed over the years, so match the dot-suffix,
// never a single pinned host; https only). Consulted only when the FEED host is github.com;
// sha256 (+ optional Ed25519 manifest signature) still gates every byte regardless of host.
func githubCDNURL(u *url.URL) bool {
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Hostname()), ".githubusercontent.com")
}

// RedirectPolicy returns a CheckRedirect that pins redirects to the feed origin, plus GitHub's
// release-asset CDN when (and only when) the feed host is github.com - GitHub asset downloads
// 302 from github.com to *.githubusercontent.com. Exported so other feed-root fetchers
// (internal/vrdll) enforce the exact same policy.
func RedirectPolicy(feedURL string) func(req *http.Request, via []*http.Request) error {
	feedHost := ""
	if pu, err := url.Parse(strings.TrimRight(feedURL, "/")); err == nil {
		feedHost = pu.Host
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return &policyError{"too many redirects"}
		}
		if feedHost == "" || strings.EqualFold(req.URL.Host, feedHost) {
			return nil
		}
		if githubFeedHost(feedHost) && githubCDNURL(req.URL) {
			return nil
		}
		return &policyError{"refusing cross-origin redirect to " + req.URL.Host}
	}
}

// policyError is a non-retryable client-side refusal (redirect policy) - retrying can never
// change the verdict, so the download retry loop treats it as permanent.
type policyError struct{ msg string }

func (e *policyError) Error() string { return e.msg }

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
// One carve-out: a github.com feed also accepts https *.githubusercontent.com URLs (GitHub's
// release-asset CDN - assets 302 there anyway); no path rule for CDN hosts, sha256 still gates.
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
	if strings.EqualFold(p.Scheme, f.Scheme) && strings.EqualFold(p.Host, f.Host) &&
		strings.HasPrefix(p.Path, strings.TrimSuffix(f.Path, "/")+"/") {
		return nil
	}
	if githubFeedHost(f.Host) && githubCDNURL(p) {
		return nil
	}
	return fmt.Errorf("manifest url not on feed origin (%s)", u.feedURL)
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

// Staged is a downloaded, checksum-verified update parked next to the live exe (exe+".new" +
// sidecar "<name>.new" files). Install swaps it in; Discard removes it. Produced by Download.
type Staged struct {
	Rel    *Release
	exe    string   // running executable path
	tmp    string   // exe+".new" verified payload
	assets []string // staged sidecar .new paths
}

// Download fetches rel's binary (+ sidecar assets), verifies every SHA-256, and stages the
// files next to the running exe WITHOUT touching it - the verified/ready-to-install phase.
// onProgress (optional) gets bytes-downloaded / total (total 0 if no Content-Length).
func (u *Updater) Download(ctx context.Context, rel *Release, onProgress func(done, total int64)) (*Staged, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("auto-update is Windows-only on this build; download the new binary manually")
	}
	if !isHex64(rel.SHA256) {
		return nil, fmt.Errorf("refusing update: manifest sha256 missing/invalid")
	}
	if err := u.validateURL(rel.URL); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)

	tmp := exe + ".new"
	if err := u.download(ctx, rel, tmp, onProgress); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}

	// Pre-fetch sidecar assets (runtime-loaded DLLs) to <name>.new next to the exe BEFORE the swap,
	// so the swap window touches local files only. Best-effort: a failed/locked asset must never
	// abort the exe update (the exe is the critical payload; a DLL just gates one feature).
	return &Staged{Rel: rel, exe: exe, tmp: tmp, assets: u.fetchAssets(ctx, exe, rel.Assets)}, nil
}

// Install atomically swaps the staged binary over the running executable (rename current →
// .old, move new into place), then places sidecar assets. On Windows a running .exe can be
// renamed (not deleted/overwritten). Does NOT relaunch; call Relaunch after, or prompt the
// user. The .old is cleaned next startup (CleanupOld).
func (s *Staged) Install() error {
	old := s.exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(s.exe, old); err != nil {
		s.Discard()
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(s.tmp, s.exe); err != nil {
		_ = os.Rename(old, s.exe) // roll back
		s.Discard()
		return fmt.Errorf("install new binary: %w", err)
	}
	// Move staged assets into place AFTER the exe swap (so a crash mid-update never leaves newer
	// DLLs paired with an older exe). Best-effort per asset.
	for _, a := range s.assets {
		placeAsset(a)
	}
	return nil
}

// Discard removes the staged files (download superseded or aborted).
func (s *Staged) Discard() {
	_ = os.Remove(s.tmp)
	for _, a := range s.assets {
		_ = os.Remove(a)
	}
}

// Apply is Download + Install in one step (kept for one-shot callers: ctl SELF-UPDATE,
// remote peer update).
func (u *Updater) Apply(ctx context.Context, rel *Release, onProgress func(done, total int64)) error {
	st, err := u.Download(ctx, rel, onProgress)
	if err != nil {
		return err
	}
	return st.Install()
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

// downloadURL streams rawURL to dst (resumable, stall-guarded), verifying the FINAL assembled
// file matches sha (lowercase hex64). A resumed assembly that fails verification (corrupted
// partial, validator miss) is deleted and re-fetched from zero ONCE before giving up.
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
	var hi int64 // progress high-water mark: a restart-from-zero must not walk the bar backwards
	resumed, err := u.fetchResumable(ctx, dlURL, dst, onProgress, &hi)
	if err != nil {
		return err
	}
	got, err := fileSHA(dst)
	if err != nil {
		return err
	}
	if got == want {
		return nil
	}
	if !resumed { // clean single-response download that still mismatches = bad payload
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	_ = os.Remove(dst)
	if _, err := u.fetchResumable(ctx, dlURL, dst, onProgress, &hi); err != nil {
		return err
	}
	if got, err = fileSHA(dst); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("checksum mismatch after clean re-download: got %s want %s", got, want)
	}
	return nil
}

// dlState carries one assembly's position across retry attempts.
type dlState struct {
	offset    int64  // bytes of dst already downloaded + written
	total     int64  // full payload size (0 = unknown)
	validator string // ETag/Last-Modified from the first response; gates If-Range resume
	resumed   bool   // any dst byte came from a Range (206) response
}

// errStalled marks a watchdog abort: the transfer produced zero bytes for dlStall.
var errStalled = errors.New("transfer stalled")

// fetchResumable downloads dlURL to dst with bounded retries. There is deliberately NO
// total-time cap: a slow but flowing transfer runs to completion; only a full stall (zero
// bytes for dlStall) aborts an attempt. Retries resume from the persisted partial via
// Range/If-Range; a 200 on resume (Range unsupported or validator changed) restarts from
// zero cleanly. Returns whether any byte of dst came from a resume.
func (u *Updater) fetchResumable(ctx context.Context, dlURL, dst string, cb func(done, total int64), hi *int64) (bool, error) {
	_ = os.Remove(dst)
	st := &dlState{}
	var lastErr error
	for attempt := 0; attempt <= u.dlRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(u.dlBackoff << (attempt - 1)):
			case <-ctx.Done():
				return st.resumed, ctx.Err()
			}
		}
		done, transient, err := u.attemptOnce(ctx, dlURL, dst, st, cb, hi)
		if done {
			return st.resumed, nil
		}
		if !transient {
			return st.resumed, err
		}
		lastErr = err
	}
	at := "unknown progress"
	if st.total > 0 {
		at = fmt.Sprintf("%d%%", st.offset*100/st.total)
	}
	return st.resumed, fmt.Errorf("download gave up after %d retries at %s: %w", u.dlRetries, at, lastErr)
}

// attemptOnce performs one HTTP attempt: request (Range+If-Range when resuming), stream the
// body to dst under the stall watchdog, advance st.offset by what landed. transient reports
// whether the failure is retryable (stall, network hiccup, 5xx/408/429).
func (u *Updater) attemptOnce(ctx context.Context, dlURL, dst string, st *dlState, cb func(done, total int64), hi *int64) (done, transient bool, err error) {
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	wd := time.AfterFunc(u.dlStall, func() { stalled.Store(true); cancel() })
	defer wd.Stop()
	classify := func(e error) (bool, bool, error) {
		var pe *policyError
		switch {
		case stalled.Load():
			return false, true, fmt.Errorf("%w (no data for %s)", errStalled, u.dlStall)
		case ctx.Err() != nil:
			return false, false, ctx.Err() // caller cancelled/deadlined - not ours to retry
		case errors.As(e, &pe):
			return false, false, e // redirect-policy refusal - retrying can't change it
		default:
			return false, true, e // network hiccup (reset, unexpected EOF, ...)
		}
	}

	req, err := http.NewRequestWithContext(actx, http.MethodGet, dlURL, nil)
	if err != nil {
		return false, false, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if st.offset > 0 && st.validator != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", st.offset))
		req.Header.Set("If-Range", st.validator)
	}
	resp, err := u.dl.Do(req)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusPartialContent:
		start, tot, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != st.offset {
			return false, true, fmt.Errorf("resume: server answered range %q for offset %d", resp.Header.Get("Content-Range"), st.offset)
		}
		if tot > 0 {
			st.total = tot
		}
		st.resumed = true
	case resp.StatusCode == http.StatusOK:
		// Full body: fresh download, Range unsupported, or If-Range validator changed. Restart
		// the assembly from zero - nothing of the old partial survives.
		st.offset, st.resumed = 0, false
		if resp.ContentLength > 0 {
			st.total = resp.ContentLength
		}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests:
		return false, true, fmt.Errorf("download returned %s", resp.Status)
	default:
		return false, false, fmt.Errorf("download returned %s", resp.Status)
	}
	if st.validator == "" {
		if st.validator = resp.Header.Get("ETag"); st.validator == "" {
			st.validator = resp.Header.Get("Last-Modified")
		}
	}

	flags := os.O_CREATE | os.O_WRONLY
	if st.offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(dst, flags, 0o755)
	if err != nil {
		return false, false, err
	}
	pr := &watchdogReader{r: resp.Body, wd: wd, stall: u.dlStall, base: st.offset, total: st.total, hi: hi, cb: cb}
	n, cerr := io.Copy(f, pr)
	st.offset += n
	if clErr := f.Close(); cerr == nil {
		cerr = clErr
	}
	if cerr != nil {
		return classify(cerr)
	}
	if st.total > 0 && st.offset < st.total {
		return false, true, fmt.Errorf("short body: got %d of %d bytes", st.offset, st.total)
	}
	return true, false, nil
}

// parseContentRange parses "bytes <start>-<end>/<total|*>" → (start, total, ok); total 0 for "*".
func parseContentRange(h string) (start, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(h), "bytes ")
	if !found {
		return 0, 0, false
	}
	rng, tot, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	startStr, _, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, false
	}
	s, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false
	}
	if tot != "*" {
		t, err := strconv.ParseInt(tot, 10, 64)
		if err != nil || t < 0 {
			return 0, 0, false
		}
		total = t
	}
	return s, total, true
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

// watchdogReader resets the stall watchdog on every chunk and reports monotonic progress:
// base (resume offset) + bytes read, clamped to the shared high-water mark so a
// restart-from-zero never walks the progress bar backwards.
type watchdogReader struct {
	r     io.Reader
	wd    *time.Timer
	stall time.Duration
	base  int64
	done  int64
	total int64
	hi    *int64
	cb    func(done, total int64)
}

func (w *watchdogReader) Read(b []byte) (int, error) {
	n, err := w.r.Read(b)
	if n > 0 {
		w.wd.Reset(w.stall)
		w.done += int64(n)
		if w.cb != nil {
			if d := w.base + w.done; d > *w.hi {
				*w.hi = d
			}
			w.cb(*w.hi, w.total)
		}
	}
	return n, err
}
