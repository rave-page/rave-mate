//go:build windows

package webui

import (
	"sync/atomic"
	"syscall"

	"rave.page/mate/internal/governor"
)

// Windows runs a modal message loop while the user drags/resizes a window; dispatched evals
// (ExecuteScript) processed inside that loop delay WM_MOVING handling, so the window trails the
// cursor. Subclass the top-level window to observe the modal loop's bounds and let livePush skip
// ticks while it runs. The same subclass also feeds the activity governor the window's focus +
// minimized state so the UI throttles (and the process deprioritizes) when it isn't being looked at.

var (
	comctl32              = syscall.NewLazyDLL("comctl32.dll")
	procSetWindowSubclass = comctl32.NewProc("SetWindowSubclass")
	procDefSubclassProc   = comctl32.NewProc("DefSubclassProc")
)

const (
	wmEnterSizeMove = 0x0231
	wmExitSizeMove  = 0x0232
	wmActivate      = 0x0006
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmShowWindow    = 0x0018
	sizeMinimized   = 1 // SIZE_MINIMIZED (wParam of WM_SIZE)
	sizeRestored    = 0 // SIZE_RESTORED
	sizeMaximized   = 2 // SIZE_MAXIMIZED
	swHide          = 0 // SW_HIDE
)

var uiSizeMove atomic.Bool

// inSizeMove reports the user is currently dragging/resizing the window.
func inSizeMove() bool { return uiSizeMove.Load() }

var sizeMoveProc = syscall.NewCallback(func(hwnd, msg, wp, lp, _, _ uintptr) uintptr {
	switch msg {
	case wmEnterSizeMove:
		uiSizeMove.Store(true)
		governor.SetSizeMove(true)
	case wmExitSizeMove:
		uiSizeMove.Store(false)
		governor.SetSizeMove(false)
	case wmActivate:
		governor.SetFocused((wp & 0xffff) != 0) // WA_INACTIVE==0 -> lost focus
	case wmSize:
		switch wp {
		case sizeMinimized:
			governor.SetMinimized(true)
		case sizeRestored, sizeMaximized:
			governor.SetMinimized(false)
		}
	case wmShowWindow:
		governor.SetMinimized(wp == 0) // hidden-to-tray = not being looked at
	case wmClose:
		// Tray app, not quit-on-close (CLAUDE.md): X/Alt+F4 hides the window; only tray
		// Quit / service stop exits. terminate() uses PostQuitMessage, so quit is unaffected.
		_, _, _ = procShowWindow.Call(hwnd, swHide)
		if onWindowHidden != nil {
			onWindowHidden()
		}
		return 0 // swallow - never reaches DefWindowProc's DestroyWindow
	}
	r, _, _ := procDefSubclassProc.Call(hwnd, msg, wp, lp)
	return r
})

// installSizeMoveHook subclasses hwnd (must run on its UI thread).
func installSizeMoveHook(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	_, _, _ = procSetWindowSubclass.Call(hwnd, sizeMoveProc, 1, 0)
}
