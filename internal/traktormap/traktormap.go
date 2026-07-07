// Package traktormap activates/deactivates Traktor controller mappings for the user by
// editing Settings.tsi safely: it refuses while Traktor is running, backs the file up first,
// then injects/removes the mapping's device via the traktortsi engine. Mapping CONTENT comes
// from a per-mapping Source (the Denon map is fetched on demand from Native Instruments'
// official URL + cached - we don't redistribute it; the RavePage map will be authored).
package traktormap

import (
	"context"
	"fmt"
	"sync"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/traktorcfg"
	"rave.page/mate/internal/traktortsi"
)

const source = "traktormap"

// AutoVersion is the sentinel for "use the newest install" (the default).
const AutoVersion = ""

// Mapping describes an installable controller mapping.
type Mapping struct {
	Key     string                                    // stable id ("denon")
	Display string                                    // UI label
	Device  string                                    // DEVI name as stored in Settings.tsi
	Fetch   func(ctx context.Context) ([]byte, error) // returns the full DEVI frame bytes
	Capture bool                                      // snapshot the live DEVI on Deactivate so Activate can restore the user's own config (vs shipping one) - used for the native Traktor Kontrol D2
}

// Manager mediates mapping install/remove against a Traktor install.
type Manager struct {
	log *logbus.Bus

	mu      sync.RWMutex
	version string // pinned Traktor version; AutoVersion ("") = newest install
}

func New(log *logbus.Bus) *Manager { return &Manager{log: log} }

// Available returns the built-in mappings (Denon now; RavePage once authored).
func (m *Manager) Available() []Mapping { return []Mapping{Denon, D2, RavePage} }

// SetVersion pins the target Traktor version (AutoVersion = newest). Thread-safe.
func (m *Manager) SetVersion(v string) {
	m.mu.Lock()
	m.version = v
	m.mu.Unlock()
}

// Version returns the pinned target (AutoVersion if auto). Thread-safe.
func (m *Manager) Version() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// Installs lists every discovered Traktor install (newest first) for the version picker.
func (m *Manager) Installs() ([]traktorcfg.Install, error) { return traktorcfg.Discover() }

// resolve returns the target install: the pinned version, or the newest when auto.
func (m *Manager) resolve() (traktorcfg.Install, bool, error) {
	if v := m.Version(); v != AutoVersion {
		inst, ok, err := traktorcfg.ByVersion(v)
		if err != nil {
			return inst, false, err
		}
		if !ok {
			return inst, false, fmt.Errorf("Traktor %s has no Settings.tsi (or isn't installed)", v)
		}
		return inst, true, nil
	}
	return traktorcfg.Newest()
}

// Status reports which mappings are currently installed in the newest Traktor install. Also
// returns the install (ok=false if no Traktor / Settings.tsi found).
func (m *Manager) Status() (inst traktorcfg.Install, installed map[string]bool, ok bool, err error) {
	inst, ok, err = m.resolve()
	if err != nil || !ok {
		return inst, nil, ok, err
	}
	blob, err := traktortsi.ReadControllerBlob(inst.Settings)
	if err != nil {
		return inst, nil, true, err
	}
	installed = map[string]bool{}
	for _, mp := range m.Available() {
		has, _ := traktortsi.HasDevice(blob, mp.Device)
		installed[mp.Key] = has
	}
	return inst, installed, true, nil
}

// DeviceNames lists every Controller Manager device currently in the target Settings.tsi
// (for diagnostics - e.g. is a "Traktor.Kontrol D2" device present so the QML surface loads).
func (m *Manager) DeviceNames() ([]string, error) {
	inst, ok, err := m.resolve()
	if err != nil || !ok {
		return nil, err
	}
	blob, err := traktortsi.ReadControllerBlob(inst.Settings)
	if err != nil {
		return nil, err
	}
	return traktortsi.DeviceNames(blob)
}

// Activate installs a mapping (fetching its content) into Settings.tsi. Guard: Traktor must
// not be running (it rewrites the file on exit). The file is backed up first; returns the
// backup path.
func (m *Manager) Activate(ctx context.Context, mp Mapping) (string, error) {
	inst, err := m.guard()
	if err != nil {
		return "", err
	}
	devi, err := mp.Fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch %s mapping: %w", mp.Key, err)
	}
	blob, err := traktortsi.ReadControllerBlob(inst.Settings)
	if err != nil {
		return "", err
	}
	updated, err := traktortsi.AddDevice(blob, devi)
	if err != nil {
		return "", err
	}
	bak, err := traktorcfg.Backup(inst.Settings)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := traktortsi.WriteControllerBlob(inst.Settings, updated); err != nil {
		return bak, err
	}
	m.log.Info(source, "mapping activated", map[string]any{"mapping": mp.Key, "device": mp.Device, "backup": bak})
	return bak, nil
}

// Deactivate removes a mapping from Settings.tsi (backup first). No-op if absent.
func (m *Manager) Deactivate(mp Mapping) (string, error) {
	inst, err := m.guard()
	if err != nil {
		return "", err
	}
	blob, err := traktortsi.ReadControllerBlob(inst.Settings)
	if err != nil {
		return "", err
	}
	if has, _ := traktortsi.HasDevice(blob, mp.Device); !has {
		return "", nil
	}
	// For a capture mapping (D2), snapshot the live DEVI before removing so Activate can
	// restore the user's own device config instead of a shipped one.
	if mp.Capture {
		if raw, ok, _ := traktortsi.DeviceRaw(blob, mp.Device); ok {
			if err := saveCapturedDEVI(mp.Key, raw); err != nil {
				m.log.Warn(source, "capture device failed (re-enable may not work)", map[string]any{"mapping": mp.Key, "error": err.Error()})
			} else {
				m.log.Info(source, "captured device for restore", map[string]any{"mapping": mp.Key, "bytes": len(raw)})
			}
		}
	}
	updated, err := traktortsi.RemoveDevice(blob, mp.Device)
	if err != nil {
		return "", err
	}
	bak, err := traktorcfg.Backup(inst.Settings)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := traktortsi.WriteControllerBlob(inst.Settings, updated); err != nil {
		return bak, err
	}
	m.log.Info(source, "mapping deactivated", map[string]any{"mapping": mp.Key, "device": mp.Device, "backup": bak})
	return bak, nil
}

// guard returns the target install, refusing if Traktor is running or none is found.
func (m *Manager) guard() (traktorcfg.Install, error) {
	if running, _ := traktorcfg.IsRunning(); running {
		return traktorcfg.Install{}, fmt.Errorf("Traktor is running - quit it first (it overwrites Settings.tsi on exit)")
	}
	inst, ok, err := m.resolve()
	if err != nil {
		return traktorcfg.Install{}, err
	}
	if !ok {
		return traktorcfg.Install{}, fmt.Errorf("no Traktor install with a Settings.tsi found")
	}
	return inst, nil
}
