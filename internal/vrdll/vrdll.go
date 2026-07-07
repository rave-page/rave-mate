// Package vrdll manages the runtime-loaded openvr_api.dll the `vr` overlay backend LoadLibrary's.
// Mirrors internal/spoutdll: a one-click download (SHA-256 verified) that drops the DLL beside the
// exe so a self-updated install can enable VR without a full reinstall. Needed because the in-app
// updater historically swapped only the exe - a vr-capable exe with no DLL beside it reports
// "waiting for vr build". (Newer updater also pulls sidecar assets; this button heals older installs.)
package vrdll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/shared/selfupdate"
)

const (
	// Display is the human label for the Settings card.
	Display = "VR runtime library (openvr_api.dll)"
	// HomePage is the manual-download fallback (OpenVR releases - the lib/win64 DLL).
	HomePage = "https://github.com/ValveSoftware/openvr/tree/master/bin/win64"
	// DLLName is the runtime library the vr backend LoadLibrary's (default search incl. exe dir).
	DLLName = "openvr_api.dll"

	// dllSHA pins the vendored openvr_api.dll (internal/vroverlay/sdk/openvr_api.dll - the exact
	// bytes CI publishes to the feed). Update this when bumping the vendored DLL (honour the 7-day
	// soak + SUPPLY_CHAIN.md).
	dllSHA = "bab8ac6ef64e68a9ca53315b0014d131088584b2efdfa6db511d67ec03cfcb4a"
)

// Dir is the app-managed fallback dir (<configDir>/bin) - shared with spoutdll/mediatools.
func Dir() (string, error) { return config.DataPath("bin") }

// Status is the DLL's current availability for the Settings UI.
type Status struct {
	Installed bool
	Path      string // absolute path when Installed
}

// Probe reports whether openvr_api.dll is present beside the exe or in the managed bin dir.
func Probe() Status {
	for _, p := range candidatePaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return Status{Installed: true, Path: p}
		}
	}
	return Status{}
}

// candidatePaths mirrors the loader's search order: beside the exe first (default LoadLibrary dir +
// where the installer/updater drop it), then the managed bin dir.
func candidatePaths() []string {
	var ps []string
	if exe, err := os.Executable(); err == nil {
		if exe, e := filepath.EvalSymlinks(exe); e == nil {
			ps = append(ps, filepath.Join(filepath.Dir(exe), DLLName))
		}
	}
	if d, err := Dir(); err == nil {
		ps = append(ps, filepath.Join(d, DLLName))
	}
	return ps
}

// CanInstall reports whether auto-install is supported (Windows-only - openvr_api.dll is the win64 lib).
func CanInstall() bool { return runtime.GOOS == "windows" }

// Install downloads openvr_api.dll from this build's update feed, verifies its SHA-256, and writes
// it beside the exe (where LoadLibrary finds it). Requires a restart for the vr backend to pick it
// up. feedURL is version.FeedURL (e.g. https://development.rave.page/app/mate/). onProgress optional.
func Install(ctx context.Context, feedURL string, onProgress func(done, total int64)) error {
	if !CanInstall() {
		return fmt.Errorf("openvr_api.dll is Windows-only; download it manually from %s", HomePage)
	}
	feedURL = strings.TrimRight(strings.TrimSpace(feedURL), "/")
	if feedURL == "" {
		return fmt.Errorf("no update feed on this build (dev build); download %s manually from %s", DLLName, HomePage)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if e, err := filepath.EvalSymlinks(exe); err == nil {
		exe = e
	}
	dst := filepath.Join(filepath.Dir(exe), DLLName)
	// Cache-bust (the feed serves a stable filename a CDN may cache) by keying on the content hash.
	url := feedURL + "/" + DLLName + "?v=" + dllSHA
	// Same redirect policy as the self-updater: feed origin only, plus GitHub's release-asset
	// CDN when the feed is github.com (asset downloads 302 there). sha256 pin gates the bytes.
	client := &http.Client{CheckRedirect: selfupdate.RedirectPolicy(feedURL)}
	tmp := dst + ".dl"
	if err := download(ctx, client, url, dllSHA, tmp, onProgress); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Replace atomically; if the live DLL is locked (VR active) move it aside.
	if _, err := os.Stat(dst); err == nil {
		if os.Remove(dst) != nil {
			_ = os.Rename(dst, dst+".old")
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s beside the app (%s): %w - try reinstalling from the installer", DLLName, dst, err)
	}
	return nil
}

// download streams url to dst via client (redirect-pinned), verifying its SHA-256 equals
// wantHex (lowercase) before returning.
func download(ctx context.Context, client *http.Client, url, wantHex, dst string, onProgress func(done, total int64)) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rave-mate/vrdll")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s returned %s", url, resp.Status)
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
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantHex) {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, wantHex)
	}
	return nil
}

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
