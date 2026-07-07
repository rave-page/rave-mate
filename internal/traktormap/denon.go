package traktormap

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/traktortsi"
)

// denonURL is Native Instruments' official DN-HC4500 Traktor mapping (a zip with a .tsi). We
// fetch + cache it rather than redistribute it.
const denonURL = "https://www.native-instruments.com/fileadmin/downloads/Traktor_Pro_4_Controller_Mappings/Denon/Denon_-_DN-HC4500.zip"

const denonDevice = "Denon.DN-HC4500"

// Denon is the built-in DN-HC4500 mapping (sends A/B title/artist to the controller LCD via
// SysEx, which session/sources/midisrc decodes).
var Denon = Mapping{
	Key:     "denon",
	Display: "Denon DN-HC4500 (A/B titles via MIDI)",
	Device:  denonDevice,
	Fetch:   fetchDenonDEVI,
}

// fetchDenonDEVI returns the Denon DEVI frame, from the local cache if present, else by
// downloading NI's official zip, extracting the .tsi, and caching the extracted DEVI.
func fetchDenonDEVI(ctx context.Context) ([]byte, error) {
	cache, _ := config.DataPath("denon-dn-hc4500.devi")
	if cache != "" {
		if b, err := os.ReadFile(cache); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	devi, err := downloadDenonDEVI(ctx)
	if err != nil {
		return nil, err
	}
	if cache != "" {
		_ = os.WriteFile(cache, devi, 0o644) // best-effort cache
	}
	return devi, nil
}

func downloadDenonDEVI(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, denonURL, nil)
	if err != nil {
		return nil, err
	}
	// NI's CDN allowlists a curl-style User-Agent and 403s others (Go's default, browser
	// strings, empty) for this public download - match what it accepts.
	req.Header.Set("User-Agent", "curl/8.7.1")
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	zipBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap (file is ~600 KB)
	if err != nil {
		return nil, err
	}
	return extractDEVIFromZip(zipBytes, denonDevice)
}

// extractDEVIFromZip finds the .tsi entry in a controller-mapping zip and returns the named
// device's DEVI frame. Pure (no network) so the parse/extract path is unit-tested.
func extractDEVIFromZip(zipBytes []byte, device string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if !strings.EqualFold(filepath.Ext(f.Name), ".tsi") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		xml, err := io.ReadAll(io.LimitReader(rc, 16<<20))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		blob, err := traktortsi.ControllerBlobFromXML(xml)
		if err != nil {
			return nil, err
		}
		devi, ok, err := traktortsi.DeviceRaw(blob, device)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("device %q not in mapping", device)
		}
		return devi, nil
	}
	return nil, fmt.Errorf("no .tsi inside the mapping zip")
}
