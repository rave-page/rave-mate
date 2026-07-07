//go:build windows

package playerwin

import "gioui.org/app"

// viewHWND extracts the native Win32 window handle from a Gio view event (0 when the
// view is gone or the event is another platform's).
func viewHWND(e app.ViewEvent) uintptr {
	if v, ok := e.(app.Win32ViewEvent); ok && v.Valid() {
		return v.HWND
	}
	return 0
}
