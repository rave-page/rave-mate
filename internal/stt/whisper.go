package stt

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

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/sysexec"
)

// whisper.cpp is fetched on first use (like mpv): the CPU x64 build (whisper-cli.exe + its DLLs)
// + a user-chosen GGML model. Pinned to a release tag + SHA-256 so a CDN can't serve a
// tampered/stale binary.
const (
	whisperTag    = "v1.9.1"
	whisperZipURL = "https://github.com/ggml-org/whisper.cpp/releases/download/" + whisperTag + "/whisper-bin-x64.zip"
	whisperExe    = "whisper-cli.exe"
	modelBaseURL  = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"
)

// Model is a selectable GGML English model. SHA-256 re-pin source:
// huggingface.co/api/models/ggerganov/whisper.cpp?blobs=true → siblings[].lfs.sha256.
type Model struct {
	File    string // ggml file name (also the on-disk name)
	Display string // UI label
	SHA256  string
	SizeMB  int
}

// Models are offered in the UI (fast → accurate). DefaultModel is the performant, usually-good one.
var Models = []Model{
	{"ggml-tiny.en.bin", "Tiny - fastest (~78 MB)", "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f", 78},
	{"ggml-base.en.bin", "Base - recommended (~148 MB)", "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002", 148},
	{"ggml-small.en.bin", "Small - most accurate (~488 MB)", "c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d", 488},
}

// DefaultModel is the default selection: base.en - performant and usually pretty good.
const DefaultModel = "ggml-base.en.bin"

// ResolvedModel returns the Model for file (or the default when file is empty/unknown).
func ResolvedModel(file string) Model {
	for _, m := range Models {
		if m.File == file {
			return m
		}
	}
	for _, m := range Models {
		if m.File == DefaultModel {
			return m
		}
	}
	return Models[1]
}

// Dir is the app-managed whisper.cpp directory (binary folder + models live here).
func Dir() (string, error) { return config.DataPath("whisper") }

func exePath() (string, error) {
	d, err := Dir()
	return filepath.Join(d, whisperExe), err
}
func modelPath(file string) (string, error) {
	d, err := Dir()
	return filepath.Join(d, file), err
}

// BinInstalled reports whether the whisper-cli binary is present.
func BinInstalled() bool {
	exe, err := exePath()
	return err == nil && fileExists(exe)
}

// Installed reports whether the binary AND the given model (default if empty) are present.
func Installed(modelFile string) bool {
	if !BinInstalled() {
		return false
	}
	mp, err := modelPath(ResolvedModel(modelFile).File)
	return err == nil && fileExists(mp)
}

// CanInstall is true only on Windows (the pinned binary is the Windows x64 build).
func CanInstall() bool { return runtime.GOOS == "windows" }

// Install downloads the whisper.cpp binary folder (if missing) and the chosen model (verifying its
// SHA-256) into Dir(). onProgress (optional) reports the current download's bytes. Idempotent.
func Install(ctx context.Context, modelFile string, onProgress func(done, total int64)) error {
	if !CanInstall() {
		return fmt.Errorf("whisper auto-install is Windows-only; place %s + a model in %q manually", whisperExe, mustDir())
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe, _ := exePath()
	if !fileExists(exe) {
		zipTmp := filepath.Join(dir, "whisper-bin.zip.tmp")
		if err := download(ctx, whisperZipURL, "", zipTmp, onProgress); err != nil {
			return fmt.Errorf("download whisper binary: %w", err)
		}
		if err := unzipFlat(zipTmp, dir); err != nil {
			_ = os.Remove(zipTmp)
			return fmt.Errorf("extract whisper binary: %w", err)
		}
		_ = os.Remove(zipTmp)
		if !fileExists(exe) {
			return fmt.Errorf("extract whisper binary: %s not found in archive", whisperExe)
		}
	}
	m := ResolvedModel(modelFile)
	mp, _ := modelPath(m.File)
	if !fileExists(mp) {
		if err := download(ctx, modelBaseURL+m.File, m.SHA256, mp, onProgress); err != nil {
			return fmt.Errorf("download model %s: %w", m.File, err)
		}
	}
	return nil
}

// Transcribe runs whisper-cli on a 16kHz mono WAV with the chosen model; returns plain text.
func Transcribe(ctx context.Context, wavPath, modelFile string) (string, error) {
	exe, err := exePath()
	if err != nil {
		return "", err
	}
	mp, _ := modelPath(ResolvedModel(modelFile).File)
	if !fileExists(exe) || !fileExists(mp) {
		return "", fmt.Errorf("whisper not installed")
	}
	threads := min(runtime.NumCPU(), 8)
	// -nt (no timestamps) → clean transcript on stdout; -l en; file input.
	cmd := exec.CommandContext(ctx, exe, "-m", mp, "-f", wavPath, "-l", "en", "-nt", "-t", fmt.Sprintf("%d", threads))
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("whisper-cli: %w", err)
	}
	return cleanTranscript(string(out)), nil
}

// cleanTranscript trims whisper's output (blank lines / leading spaces) to a single line.
func cleanTranscript(s string) string {
	var keep []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			keep = append(keep, ln)
		}
	}
	return strings.TrimSpace(strings.Join(keep, " "))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func mustDir() string { d, _ := Dir(); return d }

// download streams url → dst, verifying SHA-256 when wantHex is non-empty. onProgress optional.
func download(ctx context.Context, url, wantHex, dst string, onProgress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, cb: onProgress}
	if _, err := io.Copy(io.MultiWriter(f, h), pr); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if wantHex != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantHex) {
			_ = os.Remove(tmp)
			return fmt.Errorf("checksum mismatch: got %s want %s", got, wantHex)
		}
	}
	return os.Rename(tmp, dst)
}

// unzipFlat extracts every file in the zip into dir by basename (the whisper build is a flat set of
// an exe + DLLs; flattening also tolerates a wrapping top-level folder). Basename use prevents
// zip-slip path traversal.
func unzipFlat(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		if name == "" || name == "." {
			continue
		}
		if err := writeZipEntry(f, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil { //nolint:gosec // trusted pinned archive
		_ = out.Close()
		return err
	}
	return out.Close()
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
