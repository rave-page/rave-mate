//go:build windows

package webui

import (
	"sync/atomic"
	"syscall"
)

// Windows runs a modal message loop while the user drags/resizes a window; dispatched evals
// (ExecuteScript) processed inside that loop delay WM_MOVING handling, so the window trails the
// cursor. Subclass the top-level window to observe the modal loop's bounds and let livePush skip
// ticks while it runs.

var (
	comctl32              = syscall.NewLazyDLL("comctl32.dll")
	procSetWindowSubclass = comctl32.NewProc("SetWindowSubclass")
	procDefSubclassProc   = comctl32.NewProc("DefSubclassProc")
)

const (
	wmEnterSizeMove = 0x0231
	wmExitSizeMove  = 0x0232
)

var uiSizeMove atomic.Bool

// inSizeMove reports the user is currently dragging/resizing the window.
func inSizeMove() bool { return uiSizeMove.Load() }

var sizeMoveProc = syscall.NewCallback(func(hwnd, msg, wp, lp, _, _ uintptr) uintptr {
	switch msg {
	case wmEnterSizeMove:
		uiSizeMove.Store(true)
	case wmExitSizeMove:
		uiSizeMove.Store(false)
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
