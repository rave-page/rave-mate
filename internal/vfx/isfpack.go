package vfx

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Vidvox ISF-Files: 200+ MIT-licensed ISF 2.0 generators/filters
// (github.com/Vidvox/ISF-Files). One zipball GET - no API, no rate limits.
const vidvoxZipURL = "https://codeload.github.com/Vidvox/ISF-Files/zip/refs/heads/master"

// PackDirName is the subdir under the ISF plugin dir that holds the fetched pack.
const PackDirName = "vidvox"

const (
	maxPackZip  = 200 << 20 // whole zipball cap (repo carries preview images)
	maxPackFile = 1 << 20   // per-shader cap, matches the child's max_shader_bytes
)

// FetchVidvoxPack downloads the Vidvox ISF pack into <isfDir>/vidvox: every *.fs
// (flattened, zip-slip-safe) plus the LICENSE file for attribution. Existing
// files are overwritten (upstream is canonical). Returns the shader count.
func FetchVidvoxPack(ctx context.Context, isfDir string) (int, error) {
	dst := filepath.Join(isfDir, PackDirName)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vidvoxZipURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("pack download: HTTP %d", resp.StatusCode)
	}

	// zip needs ReaderAt - stage the (capped) body in a temp file
	tmp, err := os.CreateTemp("", "isfpack-*.zip")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	sz, err := io.Copy(tmp, io.LimitReader(resp.Body, maxPackZip+1))
	if err != nil {
		return 0, err
	}
	if sz > maxPackZip {
		return 0, errors.New("pack download exceeds size cap")
	}

	zr, err := zip.NewReader(tmp, sz)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, zf := range zr.File {
		base := filepath.Base(zf.Name) // flatten; discards any ../ traversal
		isFs := strings.EqualFold(filepath.Ext(base), ".fs")
		isLic := strings.EqualFold(base, "LICENSE") || strings.EqualFold(base, "LICENSE.md")
		if zf.FileInfo().IsDir() || (!isFs && !isLic) {
			continue
		}
		if zf.UncompressedSize64 > maxPackFile {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxPackFile+1))
		_ = rc.Close()
		if err != nil || len(data) > maxPackFile {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, base), data, 0o644); err != nil {
			return n, err
		}
		if isFs {
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("pack contained no shaders")
	}
	return n, nil
}
