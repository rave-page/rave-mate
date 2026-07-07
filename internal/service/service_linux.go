//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux: a systemd *user* unit (~/.config/systemd/user) - no root needed, runs in the
// user session. UNTESTED on the Windows dev host; the unit + systemctl calls are standard.

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", Name+".service"), nil
}

func Install() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	p, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target

[Service]
ExecStart=%s --service
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, Description, exe)
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", Name+".service").Run(); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	return nil
}

func Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", Name+".service").Run()
	p, err := unitPath()
	if err != nil {
		return err
	}
	_ = os.Remove(p)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

func Status() (string, error) {
	out, _ := exec.Command("systemctl", "--user", "is-active", Name+".service").Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "not installed", nil
	}
	return s, nil
}
