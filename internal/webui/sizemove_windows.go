//go:build windows

package webui

import (
	"sync/atomic"
	"syscall"
	"time"

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
	wmEnterSizeMove  = 0x0231
	wmExitSizeMove   = 0x0232
	wmSizing         = 0x0214 // fires continuously during a resize drag
	wmMoving         = 0x0216 // fires continuously during a move drag
	wmCaptureChanged = 0x0215 // mouse capture lost - a size-move can't continue past it
	wmActivate       = 0x0006
	wmSize           = 0x0005
	wmClose          = 0x0010
	wmShowWindow     = 0x0018
	sizeMinimized    = 1 // SIZE_MINIMIZED (wParam of WM_SIZE)
	sizeRestored     = 0 // SIZE_RESTORED
	sizeMaximized    = 2 // SIZE_MAXIMIZED
	swHide           = 0 // SW_HIDE
)

// sizeMoveStale self-heals a size-move latch left set by a swallowed WM_EXITSIZEMOVE (lost mouse
// capture, focus stolen mid-drag, Aero-snap/keyboard move). A real drag emits WM_MOVING/WM_SIZING
// many times/sec so the stamp stays fresh; once the messages stop the stamp goes stale and
// inSizeMove clears the latch - otherwise a missed EXIT wedges evalFlusher forever (Go→JS patches
// die while the page stays client-side responsive: tabs go dead, current tab still works).
const sizeMoveStale = 1500 * time.Millisecond

var (
	uiSizeMove   atomic.Bool
	sizeMoveSeen atomic.Int64 // UnixNano of last enter/move/size message; 0 = none
	// Latched for reportWindowState: the B5 window child has to FORWARD these signals (the daemon's
	// governor + eval gate live in another process now), so they are remembered, not just passed on.
	winFocused   atomic.Bool
	winMinimized atomic.Bool
)

// reportWindowState pushes the current window signals to the parent when this process is the B5
// window child (onWindowState set). No-op in the daemon: the governor calls below already applied.
func reportWindowState() {
	if fn := onWindowState; fn != nil {
		fn(procWin{Focused: winFocused.Load(), Minimized: winMinimized.Load(), SizeMove: uiSizeMove.Load()})
	}
}

// inSizeMove reports the user is currently dragging/resizing the window. Self-clears a latch left
// set by a missed WM_EXITSIZEMOVE (no drag message for sizeMoveStale) so the eval flusher can't wedge.
func inSizeMove() bool {
	if !uiSizeMove.Load() {
		return false
	}
	if seen := sizeMoveSeen.Load(); seen == 0 || time.Since(time.Unix(0, seen)) > sizeMoveStale {
		uiSizeMove.Store(false)
		governor.SetSizeMove(false)
		reportWindowState()
		return false
	}
	return true
}

var sizeMoveProc = syscall.NewCallback(func(hwnd, msg, wp, lp, _, _ uintptr) uintptr {
	switch msg {
	case wmEnterSizeMove:
		sizeMoveSeen.Store(time.Now().UnixNano()) // stamp BEFORE the latch so inSizeMove never sees true+seen==0
		uiSizeMove.Store(true)
		governor.SetSizeMove(true)
		reportWindowState()
	case wmSizing, wmMoving:
		if uiSizeMove.Load() {
			sizeMoveSeen.Store(time.Now().UnixNano()) // keep a live drag fresh
		}
	case wmExitSizeMove:
		uiSizeMove.Store(false)
		governor.SetSizeMove(false)
		reportWindowState()
	case wmCaptureChanged:
		if uiSizeMove.Load() { // capture loss ends any drag - don't wait for a maybe-missing EXIT
			uiSizeMove.Store(false)
			governor.SetSizeMove(false)
			reportWindowState()
		}
	case wmActivate:
		winFocused.Store((wp & 0xffff) != 0)
		governor.SetFocused(winFocused.Load()) // WA_INACTIVE==0 -> lost focus
		reportWindowState()
	case wmSize:
		switch wp {
		case sizeMinimized:
			winMinimized.Store(true)
			governor.SetMinimized(true)
			reportWindowState()
		case sizeRestored, sizeMaximized:
			winMinimized.Store(false)
			governor.SetMinimized(false)
			reportWindowState()
		}
	case wmShowWindow:
		winMinimized.Store(wp == 0)
		governor.SetMinimized(wp == 0) // hidden-to-tray = not being looked at
		reportWindowState()
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
