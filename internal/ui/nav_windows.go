//go:build windows

package ui

import (
	"runtime"
	"syscall"
	"unsafe"

	"fyne.io/fyne/v2"
)

var (
	navUser32             = syscall.NewLazyDLL("user32.dll")
	navKernel32           = syscall.NewLazyDLL("kernel32.dll")
	pSetWindowsHookExW    = navUser32.NewProc("SetWindowsHookExW")
	pCallNextHookEx       = navUser32.NewProc("CallNextHookEx")
	pGetMessageW          = navUser32.NewProc("GetMessageW")
	pGetForegroundWindow  = navUser32.NewProc("GetForegroundWindow")
	pGetWindowThreadProc  = navUser32.NewProc("GetWindowThreadProcessId")
	pGetModuleHandleW     = navKernel32.NewProc("GetModuleHandleW")
	pGetCurrentProcessIdN = navKernel32.NewProc("GetCurrentProcessId")
)

const (
	whMouseLL     = 14
	wmXButtonDown = 0x020B
	xbutton1      = 0x0001 // back
	xbutton2      = 0x0002 // forward
)

// msllhookstruct mirrors Win32 MSLLHOOKSTRUCT; the pressed X button is the high word of mouseData.
type msllhookstruct struct {
	pt        struct{ x, y int32 }
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

// installMouseNav installs a low-level mouse hook mapping the X1/X2 (back/forward) buttons to tab
// back/forward while rave-mate is foreground. Fyne's driver drops these buttons, so a global hook
// is the only way on Windows. Best-effort: any failure is a silent no-op (Alt+←/→ still works).
func (u *UI) installMouseNav() { go u.mouseHookLoop() }

func (u *UI) mouseHookLoop() {
	defer func() {
		if r := recover(); r != nil && u.svc.Log != nil {
			u.svc.Log.Debug("ui", "mouse back/forward hook unavailable", map[string]any{"reason": r})
		}
	}()
	runtime.LockOSThread() // the hook + its message pump must share one OS thread
	myPID, _, _ := pGetCurrentProcessIdN.Call()

	// lParam is taken as unsafe.Pointer (it really is a *MSLLHOOKSTRUCT) so reading the struct
	// needs no uintptr→Pointer conversion (vet-clean).
	cb := syscall.NewCallback(func(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
		func() {
			defer func() { _ = recover() }() // never let a panic escape into the Win32 caller
			if int32(nCode) >= 0 && wParam == wmXButtonDown && u.foreground(uint32(myPID)) {
				switch (((*msllhookstruct)(lParam)).mouseData >> 16) & 0xFFFF {
				case xbutton1:
					fyne.Do(u.navBack)
				case xbutton2:
					fyne.Do(u.navForward)
				}
			}
		}()
		ret, _, _ := pCallNextHookEx.Call(0, nCode, wParam, uintptr(lParam))
		return ret
	})

	hMod, _, _ := pGetModuleHandleW.Call(0)
	hook, _, _ := pSetWindowsHookExW.Call(whMouseLL, cb, hMod, 0)
	if hook == 0 {
		return // install failed - keyboard nav still works
	}
	if u.svc.Log != nil {
		u.svc.Log.Debug("ui", "mouse back/forward hook active (X1=back, X2=forward)", nil)
	}
	var msg [64]byte // MSG; we only pump, never read it
	for {
		if r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); int32(r) <= 0 {
			return
		}
	}
}

// foreground reports whether the OS foreground window belongs to this process.
func (u *UI) foreground(myPID uint32) bool {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	_, _, _ = pGetWindowThreadProc.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid == myPID
}
