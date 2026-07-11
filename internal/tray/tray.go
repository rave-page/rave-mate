// Package tray is rave-mate's native system-tray helper menu for the webview renderer (the Fyne
// renderer keeps its own desktop.App tray). A tray icon whose right-click menu surfaces Open /
// Check for updates / Quit. Windows uses Shell_NotifyIcon (pure stdlib syscall, no new dep);
// other OSes are a no-op stub. Ported from rave-app/internal/tray. See tray_windows.go /
// tray_other.go.
package tray

// Options configures the tray. Labels are supplied by the caller (already localized). All
// callbacks are optional and run off the tray's message loop; a nil callback omits its menu item.
type Options struct {
	Tooltip        string
	OpenLabel      string
	CheckLabel     string
	QuitLabel      string
	OnShow         func()
	OnCheckUpdates func()
	OnQuit         func()
	// UpdateLabel is consulted each time the menu opens: a non-empty return adds a
	// state-dependent update item ("Download update X", "Install update", "Restart to finish
	// update") wired to OnUpdate. "" (or nil func) omits the item - no dead chrome when
	// up to date.
	UpdateLabel func() string
	OnUpdate    func()
}
