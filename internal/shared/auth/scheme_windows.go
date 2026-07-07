//go:build windows

package auth

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// RegisterScheme claims the custom URL schemes under HKCU\Software\Classes so the OS routes
// ravepage:// and rave:// deeplinks to this executable. Per-user (no admin), reversible,
// and idempotent. On a machine with the Electron client installed this takes over the
// schemes - these native apps are the intended replacement for that bridge.
func (m *Manager) RegisterScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for _, scheme := range registerSchemes {
		if err := registerOne(scheme, exe); err != nil {
			return fmt.Errorf("register %s: %w", scheme, err)
		}
	}
	m.log.Info(source, "registered URL schemes", map[string]any{"schemes": registerSchemes, "exe": exe})
	return nil
}

func registerOne(scheme, exe string) error {
	root := `Software\Classes\` + scheme
	k, _, err := registry.CreateKey(registry.CURRENT_USER, root, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()
	if err := k.SetStringValue("", "URL:"+scheme+" Protocol"); err != nil {
		return err
	}
	if err := k.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, root+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = cmd.Close() }()
	return cmd.SetStringValue("", `"`+exe+`" "%1"`)
}
