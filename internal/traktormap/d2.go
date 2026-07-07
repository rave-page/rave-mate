package traktormap

import (
	"context"
	"fmt"
	"os"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/traktortsi"
)

const d2Device = "Traktor.Kontrol D2"

// D2 toggles the native Traktor Kontrol D2 controller device in Controller Manager. The
// presence of a D2 device is what makes Traktor load the D2 QML surface - where the api-client
// ApiModule runs and POSTs deck state to :8080 - so this is the on/off switch for the QML feed
// at the controller level (no file patching). Capture=true: we snapshot the user's OWN D2 DEVI
// when they disable it and restore that exact config when they re-enable, rather than ship a
// generic D2 mapping.
var D2 = Mapping{
	Key:     "d2",
	Display: "Traktor Kontrol D2 (loads the QML feed surface)",
	Device:  d2Device,
	Capture: true,
	Fetch:   fetchCapturedD2,
}

// capturedDEVIPath is where a capture-mapping's snapshotted DEVI lives between disable/enable.
func capturedDEVIPath(key string) (string, error) {
	return config.DataPath("captured-" + key + ".devi")
}

func saveCapturedDEVI(key string, raw []byte) error {
	p, err := capturedDEVIPath(key)
	if err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o644)
}

// fetchCapturedD2 returns the previously-captured D2 DEVI (Activate restores it).
func fetchCapturedD2(_ context.Context) ([]byte, error) {
	p, err := capturedDEVIPath("d2")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no saved Kontrol D2 device yet - it's captured automatically the first time you disable it (or add a Traktor Kontrol D2 in Traktor once): %w", err)
	}
	return b, nil
}

// CaptureIfPresent snapshots a capture-mapping's live DEVI if the device exists and isn't
// cached yet - so a user who already has the device (and never disabled it) can still re-enable
// after a disable. Best-effort; safe to call on UI refresh.
func (m *Manager) CaptureIfPresent(mp Mapping) {
	if !mp.Capture {
		return
	}
	if p, err := capturedDEVIPath(mp.Key); err == nil {
		if _, err := os.Stat(p); err == nil {
			return // already cached
		}
	}
	inst, ok, err := m.resolve()
	if err != nil || !ok {
		return
	}
	blob, err := traktortsi.ReadControllerBlob(inst.Settings)
	if err != nil {
		return
	}
	if raw, ok, _ := traktortsi.DeviceRaw(blob, mp.Device); ok {
		if err := saveCapturedDEVI(mp.Key, raw); err == nil {
			m.log.Info(source, "captured device for restore", map[string]any{"mapping": mp.Key, "bytes": len(raw)})
		}
	}
}
