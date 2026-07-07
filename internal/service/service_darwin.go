//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS: a launchd LaunchAgent (~/Library/LaunchAgents) - per-user, runs at login,
// KeepAlive restarts on crash. UNTESTED on the Windows dev host; the plist + launchctl
// calls are standard.

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func Install() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	p, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>--service</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`, Label, exe)
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return err
	}
	if err := exec.Command("launchctl", "load", "-w", p).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	return nil
}

func Uninstall() error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", "-w", p).Run()
	_ = os.Remove(p)
	return nil
}

func Status() (string, error) {
	out, _ := exec.Command("launchctl", "list").Output()
	if strings.Contains(string(out), Label) {
		return "loaded", nil
	}
	return "not installed", nil
}
