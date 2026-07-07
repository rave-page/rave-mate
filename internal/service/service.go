// Package service installs/uninstalls rave-mate as an OS-managed background service that
// runs `rave-mate --service`. Cross-platform: Windows Service (SCM), Linux systemd user
// unit, macOS launchd LaunchAgent. The unix paths are user-scoped (no root) and the
// Windows path uses the SCM (needs an elevated prompt). Per-OS impls in service_*.go.
package service

import "os"

const (
	// Name is the service/unit identifier.
	Name = "rave-mate"
	// DisplayName is the human-facing service name (Windows SCM).
	DisplayName = "rave.page companion (rave-mate)"
	// Description is the service description.
	Description = "Local worker + bridge for rave.page (Traktor, Local Studio, transcode/probe workers)."
	// Label is the launchd label (macOS) - matches the app bundle id.
	Label = "page.rave.mate"
)

func exePath() (string, error) { return os.Executable() }

// InstallInteractive installs the service, self-elevating via UAC on Windows when the caller
// isn't already admin (the SCM needs it). On Linux/macOS the unit is user-scoped, so it calls
// Install directly. Use this from the CLI + the UI button so neither has to know about UAC.
func InstallInteractive() error { return interactive("install", Install) }

// UninstallInteractive is InstallInteractive's counterpart for removal.
func UninstallInteractive() error { return interactive("uninstall", Uninstall) }
