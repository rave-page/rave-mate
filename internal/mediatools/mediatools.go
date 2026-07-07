// Package mediatools manages the external command-line media binaries the app shells out
// to - ffmpeg/ffprobe (transcoding + analysis) and Chromaprint's fpcalc (fingerprinting).
//
// Like the loopMIDI "install a missing dependency" hint in Settings, the user shouldn't
// have to hunt these down: Install downloads the official Windows build over HTTPS, verifies
// its SHA-256, and unpacks just the executable(s) into an app-managed bin dir
// (<configDir>/bin). Resolve then prefers that managed copy over anything on PATH, so the
// out-of-process workers pick it up automatically with no PATH surgery.
//
// Auto-install is Windows-only (the pinned URLs/layout are the Windows builds); other
// platforms fall back to a manual download link and PATH discovery.
package mediatools

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/sysexec"
)

// Tool identifies one external media binary (or a bundle of them shipped in one archive).
type Tool struct {
	Key     string   // stable id: "ffmpeg", "fpcalc"
	Display string   // human label for the UI
	Bins    []string // logical executable names to extract + resolve (no extension), e.g. ["ffmpeg","ffprobe"]
	Primary string   // the Bins entry used for presence checks, e.g. "ffmpeg"

	// URL is the Windows download archive (a .zip). SHA256 pins its digest; when empty,
	// SHA256URL is fetched (same-origin sidecar returning a bare hex digest) at install
	// time - used for vendors that rebuild a fixed URL on a schedule (gyan's ffmpeg).
	URL       string
	SHA256    string
	SHA256URL string

	// HomePage is the manual-download page surfaced as a fallback hyperlink.
	HomePage string
}

// FFmpeg bundles ffmpeg + ffprobe (used by transcode, waveform/probe analysis, fingerprint
// segmenting). gyan.dev rebuilds the fixed "release-essentials" URL periodically, so its
// digest is verified against the same-origin .sha256 sidecar rather than a stale hard pin.
var FFmpeg = Tool{
	Key:       "ffmpeg",
	Display:   "FFmpeg (transcoding + analysis)",
	Bins:      []string{"ffmpeg", "ffprobe"},
	Primary:   "ffmpeg",
	URL:       "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
	SHA256URL: "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip.sha256",
	HomePage:  "https://www.gyan.dev/ffmpeg/builds/",
}

// Fpcalc is Chromaprint's fingerprinter. The v1.5.1 GitHub release asset is immutable, so
// its SHA-256 is hard-pinned.
var Fpcalc = Tool{
	Key:      "fpcalc",
	Display:  "Chromaprint / fpcalc (fingerprinting)",
	Bins:     []string{"fpcalc"},
	Primary:  "fpcalc",
	URL:      "https://github.com/acoustid/chromaprint/releases/download/v1.5.1/chromaprint-fpcalc-1.5.1-windows-x86_64.zip",
	SHA256:   "36b478e16aa69f757f376645db0d436073a42c0097b6bb2677109e7835b59bbc",
	HomePage: "https://acoustid.org/chromaprint",
}

// MPV is the mpv player (smooth, hardware-accelerated video - drives the in-app player via IPC).
// The zhongfly Windows build ships a monolithic mpv.exe in a .7z; its GitHub release asset is
// immutable, so the SHA-256 is hard-pinned. Extracted with the OS bsdtar (libarchive handles 7z on
// Windows 10 1903+/11); older Windows falls back to the manual download.
var MPV = Tool{
	Key:      "mpv",
	Display:  "mpv (smooth video playback)",
	Bins:     []string{"mpv"},
	Primary:  "mpv",
	URL:      "https://github.com/zhongfly/mpv-winbuild/releases/download/2026-06-20-2d5dfb343a/mpv-x86_64-20260620-git-2d5dfb343a.7z",
	SHA256:   "9a7d5189a3cf079415375322ae3c2bb4909421ca9d1e1f1fa727aea60c7b42c2",
	HomePage: "https://mpv.io/installation/",
}

// All is the ordered set of managed tools.
var All = []Tool{FFmpeg, Fpcalc, MPV}

// exeName returns the platform executable filename for a logical base name.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// BinDir is the app-managed directory that holds downloaded media binaries
// (<configDir>/bin). Not created here - Install makes it.
func BinDir() (string, error) { return config.DataPath("bin") }

// Resolve returns the absolute path to a logical binary, preferring the app-managed bin dir
// (populated by Install) over a copy on PATH. ok=false when neither has it. The
// out-of-process workers call this so a downloaded ffmpeg "just works" without PATH edits.
func Resolve(base string) (path string, ok bool) {
	if dir, err := BinDir(); err == nil {
		p := filepath.Join(dir, exeName(base))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	if p, err := exec.LookPath(base); err == nil {
		return p, true
	}
	return "", false
}

// Status is a tool's current availability, for the Settings UI.
type Status struct {
	Installed bool
	Managed   bool   // resolved from the app-managed bin dir (vs found on PATH)
	Path      string // absolute path when Installed
}

// Status reports whether the tool's Primary binary is available and from where.
func (t Tool) Status() Status {
	if dir, err := BinDir(); err == nil {
		p := filepath.Join(dir, exeName(t.Primary))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return Status{Installed: true, Managed: true, Path: p}
		}
	}
	if p, err := exec.LookPath(t.Primary); err == nil {
		return Status{Installed: true, Path: p}
	}
	return Status{}
}

// CanInstall reports whether auto-install is supported on this OS (Windows-only - the
// pinned URLs are the Windows archives).
func CanInstall() bool { return runtime.GOOS == "windows" }

// Install downloads the tool's archive over HTTPS, verifies its SHA-256, and unpacks the
// requested executables into the managed bin dir. onProgress (optional) receives
// bytes-downloaded / total (total 0 when the server sends no Content-Length).
func (t Tool) Install(ctx context.Context, onProgress func(done, total int64)) error {
	if !CanInstall() {
		return fmt.Errorf("auto-install is Windows-only; install %s manually from %s", t.Display, t.HomePage)
	}
	dir, err := BinDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	want := strings.ToLower(strings.TrimSpace(t.SHA256))
	if want == "" {
		if t.SHA256URL == "" {
			return fmt.Errorf("%s: no checksum pinned", t.Key)
		}
		if want, err = fetchSHA256(ctx, t.SHA256URL); err != nil {
			return fmt.Errorf("fetch checksum: %w", err)
		}
	}
	if !isHex64(want) {
		return fmt.Errorf("%s: checksum is not a 64-char hex digest", t.Key)
	}

	ext := ".zip"
	if strings.HasSuffix(strings.ToLower(t.URL), ".7z") {
		ext = ".7z"
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("ravemate-%s-%d%s", t.Key, time.Now().UnixNano(), ext))
	defer func() { _ = os.Remove(tmp) }()
	if err := download(ctx, t.URL, want, tmp, onProgress); err != nil {
		return err
	}

	if ext == ".7z" {
		return extract7z(ctx, tmp, dir, t.Bins)
	}
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return extractBins(&zr.Reader, dir, t.Bins)
}

// extract7z unpacks a .7z with the OS bsdtar (System32\tar.exe - libarchive handles 7z on Windows
// 10 1903+/11), then copies the requested executables (matched by basename) into dir. mpv ships
// 7z-only; Go has no stdlib 7z, and bundling a 7z decoder would add a heavy dep tree.
func extract7z(ctx context.Context, archive, dir string, bins []string) error {
	tarExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "tar.exe")
	if _, err := os.Stat(tarExe); err != nil {
		tarExe = "tar" // last resort; GNU tar can't do 7z and will error clearly below
	}
	tmp, err := os.MkdirTemp("", "ravemate-7z-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, tarExe, "-xf", archive, "-C", tmp)
	sysexec.Hide(cmd) // no console flash
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract 7z (needs Windows 10 1903+/11 tar): %w: %s", err, strings.TrimSpace(string(out)))
	}
	return copyBinsFromTree(tmp, dir, bins)
}

// copyBinsFromTree walks root for each requested logical binary (matched by basename) and copies it
// into dir. Every requested binary must be found.
func copyBinsFromTree(root, dir string, bins []string) error {
	want := make(map[string]string, len(bins)) // exe filename -> src path ("" until found)
	for _, b := range bins {
		want[strings.ToLower(exeName(b))] = ""
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.ToLower(filepath.Base(p))
		if cur, ok := want[name]; ok && cur == "" {
			want[name] = p
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name, src := range want {
		if src == "" {
			return fmt.Errorf("archive missing expected binary: %s", name)
		}
		if err := copyFile(src, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies src to dst with the executable bit set (capped at 256 MiB).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, 256<<20)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// download streams url to dst, verifying the SHA-256 matches wantHex (lowercase) before
// returning. A long timeout covers the ~80 MB ffmpeg essentials build.
func download(ctx context.Context, url, wantHex, dst string, onProgress func(done, total int64)) error {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// A descriptive UA: some CDNs (NI, occasionally gyan) 403 Go's default agent.
	req.Header.Set("User-Agent", "rave-mate/mediatools")
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
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHex {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, wantHex)
	}
	return nil
}

// extractBins copies each requested logical binary out of the archive into dir, matching
// zip entries by basename (the official archives nest exes under a versioned subfolder).
// Pure (no network) so the unpack path is unit-tested. Every requested binary must be found.
func extractBins(zr *zip.Reader, dir string, bins []string) error {
	want := make(map[string]bool, len(bins)) // exe filename -> still needed
	for _, b := range bins {
		want[strings.ToLower(exeName(b))] = true
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(filepath.Base(f.Name))
		if !want[name] {
			continue
		}
		if err := writeEntry(f, filepath.Join(dir, filepath.Base(f.Name))); err != nil {
			return err
		}
		delete(want, name)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for n := range want {
			missing = append(missing, n)
		}
		return fmt.Errorf("archive missing expected binaries: %s", strings.Join(missing, ", "))
	}
	return nil
}

// writeEntry extracts one zip entry to dst (executable bit set, capped at 256 MiB).
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
	if _, err := io.Copy(out, io.LimitReader(rc, 256<<20)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// fetchSHA256 GETs a checksum sidecar and returns the first 64-char hex token, lowercased.
// Sidecars are either a bare digest or "<digest>  <filename>".
func fetchSHA256(ctx context.Context, url string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "rave-mate/mediatools")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum %s returned %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if i := strings.IndexAny(tok, " \t\r\n"); i >= 0 {
		tok = tok[:i]
	}
	tok = strings.ToLower(tok)
	if !isHex64(tok) {
		return "", fmt.Errorf("sidecar did not contain a 64-char hex digest")
	}
	return tok, nil
}

// isHex64 reports whether s is exactly 64 hex chars (a SHA-256 digest).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// progressReader reports cumulative read progress through cb.
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
