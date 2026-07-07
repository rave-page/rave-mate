//go:build !windows

package ui

// installMouseNav is a no-op off Windows - the X1/X2 mouse-button hook is Windows-only. Alt+←/→
// (installNavShortcuts) provides back/forward everywhere.
func (u *UI) installMouseNav() {}
