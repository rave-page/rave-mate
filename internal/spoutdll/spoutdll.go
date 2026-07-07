// Package spoutdll manages the optional SpoutLibrary.dll the Windows Spout video-share backend
// loads at runtime. Mirrors internal/mediatools: a one-click download of the pinned Spout2 SDK
// release (SHA-256 verified) that drops the MT (static-CRT, no VC++ redist) SpoutLibrary.dll into
// the app-managed bin dir, with a manual-download link as fallback. The spout backend preloads the
// DLL from there, so a fresh/self-updated install can enable Spout without a reinstall.
package spoutdll

import (
	"archive/zip"
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
)

const (
	// Display is the human label for the Settings card.
	Display = "Spout runtime (SpoutLibrary.dll)"
	// HomePage is the manual-download fallback (the SDK releases page).
	HomePage = "https://github.com/leadedge/Spout2/releases"
	// DLLName is the runtime library the spout backend LoadLibrary's.
	DLLName = "SpoutLibrary.dll"

	// Pinned Spout2 SDK release (same URL + digest as scripts/fetch-spout.{ps1,sh}); honour the
	// 7-day soak + SUPPLY_CHAIN.md when bumping.
	sdkURL = "https://github.com/leadedge/Spout2/releases/download/2.007.017/Spout-SDK-binaries_2-007-017_1.zip"
	sdkSHA = "695f20e3505fa0da51b2eb959af359f5d9e2c914bb9676e9118d19f6a5424bf4"
	// MT = the static-CRT build (no Visual C++ redistributable needed). Match the zip entry by
	// this path suffix so we never grab the MD variant (which would add a VCRUNTIME dependency).
	mtSuffix = "mt/bin/spoutlibrary.dll"
)

// Dir is the app-managed dir the DLL installs into (<configDir>/bin) - shared with mediatools.
func Dir() (string, error) { return config.DataPath("bin") }

// Status is the DLL's current availability for the Settings UI.
type Status struct {
	Installed bool
	Path      string // absolute path when Installed
}

// Probe reports whether SpoutLibrary.dll is present beside the exe or in the managed bin dir.
func Probe() Status {
	for _, p := range candidatePaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return Status{Installed: true, Path: p}
		}
	}
	return Status{}
}

// candidatePaths is the DLL search order the backend mirrors: beside the exe first (the installer
// drops it there + it's the default LoadLibrary dir), then the managed bin dir (preloaded by path).
func candidatePaths() []string {
	var ps []string
	if exe, err := os.Executable(); err == nil {
		ps = append(ps, filepath.Join(filepath.Dir(exe), DLLName))
	}
	if d, err := Dir(); err == nil {
		ps = append(ps, filepath.Join(d, DLLName))
	}
	return ps
}

// CanInstall reports whether auto-install is supported (Windows-only - SpoutLibrary is Win/DX).
func CanInstall() bool { return runtime.GOOS == "windows" }

// Install downloads the pinned Spout2 SDK over HTTPS, verifies its SHA-256, and extracts the MT
// SpoutLibrary.dll into the managed bin dir. onProgress (optional) gets bytes-done/total.
func Install(ctx context.Context, onProgress func(done, total int64)) error {
	if !CanInstall() {
		return fmt.Errorf("SpoutLibrary.dll is Windows-only; download it manually from %s", HomePage)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("ravemate-spout-%d.zip", time.Now().UnixNano()))
	defer func() { _ = os.Remove(tmp) }()
	if err := download(ctx, sdkURL, sdkSHA, tmp, onProgress); err != nil {
		return err
	}
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return extractDLL(&zr.Reader, filepath.Join(dir, DLLName))
}

// extractDLL writes the MT SpoutLibrary.dll from the SDK zip to dst (falls back to any
// SpoutLibrary.dll if the MT path layout ever changes). Pure → unit-testable.
func extractDLL(zr *zip.Reader, dst string) error {
	var mt, fallback *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ln := strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.HasSuffix(ln, mtSuffix) {
			mt = f
			break
		}
		if strings.HasSuffix(ln, "/"+strings.ToLower(DLLName)) || ln == strings.ToLower(DLLName) {
			fallback = f
		}
	}
	f := mt
	if f == nil {
		f = fallback
	}
	if f == nil {
		return fmt.Errorf("archive has no %s", DLLName)
	}
	return writeEntry(f, dst)
}

func writeEntry(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, 64<<20)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// download streams url to dst, verifying its SHA-256 equals wantHex before returning.
func download(ctx context.Context, url, wantHex, dst string, onProgress func(done, total int64)) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rave-mate/spoutdll")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s returned %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
