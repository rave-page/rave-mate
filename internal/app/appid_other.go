//go:build !windows

package app

import "rave.page/mate/internal/shared/logbus"

// App identity (AppUserModelID) is a Windows taskbar concept; macOS/Linux take window grouping
// from the bundle/.desktop file.
func initAppIdentity(*logbus.Bus) {}
