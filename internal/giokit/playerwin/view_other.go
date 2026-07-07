//go:build !windows

package playerwin

import "gioui.org/app"

// viewHWND: no embeddable native handle off Windows - the player uses the mpv popout.
func viewHWND(app.ViewEvent) uintptr { return 0 }
