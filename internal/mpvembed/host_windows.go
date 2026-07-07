//go:build windows

package mpvembed

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modGdi32    = syscall.NewLazyDLL("gdi32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassW     = modUser32.NewProc("RegisterClassW")
	pCreateWindowExW    = modUser32.NewProc("CreateWindowExW")
	pDestroyWindow      = modUser32.NewProc("DestroyWindow")
	pMoveWindow         = modUser32.NewProc("MoveWindow")
	pShowWindow         = modUser32.NewProc("ShowWindow")
	pDefWindowProcW     = modUser32.NewProc("DefWindowProcW")
	pGetMessageW        = modUser32.NewProc("GetMessageW")
	pTranslateMessage   = modUser32.NewProc("TranslateMessage")
	pDispatchMessageW   = modUser32.NewProc("DispatchMessageW")
	pPostThreadMessageW = modUser32.NewProc("PostThreadMessageW")
	pGetStockObject     = modGdi32.NewProc("GetStockObject")
	pGetModuleHandle    = modKernel32.NewProc("GetModuleHandleW")
	pGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")
)

const (
	wsChild            = 0x40000000
	wsClipChildren     = 0x02000000
	wsClipSiblings     = 0x04000000
	wsExNoParentNotify = 0x00000004 // don't SendMessage WM_PARENTNOTIFY to the parent's thread on create/destroy

	swHide   = 0
	swShowNA = 8 // show without activating (don't steal focus from the Fyne window)

	wmQuit = 0x0012

	blackBrush = 4
)

// wndclassw mirrors Win32 WNDCLASSW.
type wndclassw struct {
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
}

var (
	classOnce sync.Once
	className *uint16
	classErr  error
	// wndProc is retained package-wide: syscall.NewCallback handles must not be GC'd.
	wndProc uintptr
)

// registerClass registers the host window class once (DefWindowProc, black background to avoid a
// white flash before mpv attaches). Idempotent.
func registerClass() (*uint16, error) {
	classOnce.Do(func() {
		name, err := syscall.UTF16PtrFromString("RaveMateMpvHost")
		if err != nil {
			classErr = err
			return
		}
		hInst, _, _ := pGetModuleHandle.Call(0)
		brush, _, _ := pGetStockObject.Call(blackBrush)
		wndProc = syscall.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
			r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
			return r
		})
		// Class style is 0 (no CS_* flags). WS_CLIPCHILDREN/WS_CLIPSIBLINGS are WINDOW styles set on
		// CreateWindowExW below - passing them here as class styles is rejected (ERROR_INVALID_PARAMETER).
		wc := wndclassw{
			style:         0,
			lpfnWndProc:   wndProc,
			hInstance:     hInst,
			hbrBackground: brush,
			lpszClassName: name,
		}
		atom, _, callErr := pRegisterClassW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			classErr = fmt.Errorf("RegisterClassW: %w", callErr)
			return
		}
		className = name
	})
	return className, classErr
}

// Host is a native child window mpv renders into (via --wid). Move/Show/Hide/Destroy manage its
// position + visibility as the placeholder widget scrolls, resizes, or leaves the screen.
type Host struct {
	hwnd    uintptr
	pumpTID uintptr // CreateHosted's owner-thread id (0 = caller-thread host; Destroy differs)
}

// Supported reports whether window embedding is available on this platform (Windows: yes).
func Supported() bool { return true }

// Create makes a hidden child window parented to parent (a Win32 HWND, uintptr) for mpv to render
// into. Call from the UI/main OS thread (window-creation thread affinity). Show it once positioned.
func Create(parent uintptr) (*Host, error) {
	if parent == 0 {
		return nil, fmt.Errorf("mpvembed: nil parent HWND")
	}
	name, err := registerClass()
	if err != nil {
		return nil, err
	}
	hInst, _, _ := pGetModuleHandle.Call(0)
	// Created hidden (no WS_VISIBLE) at 0,0,0,0; positioned + shown by the caller once the rect
	// is known - avoids a black flash at the top-left corner.
	hwnd, _, callErr := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(name)),
		0,
		wsChild|wsClipChildren|wsClipSiblings,
		0, 0, 0, 0,
		parent,
		0,
		hInst,
		0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("mpvembed: CreateWindowExW: %w", callErr)
	}
	return &Host{hwnd: hwnd}, nil
}

// CreateHosted is Create on a DEDICATED pumping owner thread - for callers whose event thread
// cannot own the child. Gio: creating from the frame handler deadlocked (CreateWindowExW
// synchronizes with the parent's thread, which is blocked waiting for the frame to return),
// and cross-thread Move/Show against a non-pumping owner would hang the same way. The spawned
// thread creates the window (WS_EX_NOPARENTNOTIFY so creation never SendMessages the parent)
// and then pumps messages until Destroy. Call from ANY goroutine - but never from inside a
// Gio frame handler (the parent's thread must be free to schedule the child's first paint).
func CreateHosted(parent uintptr) (*Host, error) {
	if parent == 0 {
		return nil, fmt.Errorf("mpvembed: nil parent HWND")
	}
	name, err := registerClass()
	if err != nil {
		return nil, err
	}
	type res struct {
		h   *Host
		err error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread() // window thread affinity: this thread owns + pumps the child
		hInst, _, _ := pGetModuleHandle.Call(0)
		hwnd, _, callErr := pCreateWindowExW.Call(
			wsExNoParentNotify,
			uintptr(unsafe.Pointer(name)),
			0,
			wsChild|wsClipChildren|wsClipSiblings,
			0, 0, 0, 0,
			parent,
			0,
			hInst,
			0,
		)
		if hwnd == 0 {
			ch <- res{nil, fmt.Errorf("mpvembed: CreateWindowExW: %w", callErr)}
			return
		}
		tid, _, _ := pGetCurrentThreadId.Call()
		ch <- res{&Host{hwnd: hwnd, pumpTID: tid}, nil}
		var msg [48]byte // Win32 MSG (48 bytes on amd64)
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
			if int32(r) <= 0 {
				return // WM_QUIT (Destroy) or error
			}
			_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
			_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
		}
	}()
	r := <-ch
	return r.h, r.err
}

// WID returns the child window handle to pass to mpv as --wid.
func (h *Host) WID() uintptr { return h.hwnd }

// Move positions/resizes the host in parent-client (physical-pixel) coordinates.
func (h *Host) Move(x, y, w, ht int) {
	if h == nil || h.hwnd == 0 {
		return
	}
	_, _, _ = pMoveWindow.Call(h.hwnd, uintptr(int32(x)), uintptr(int32(y)), uintptr(int32(w)), uintptr(int32(ht)), 1)
}

// Show makes the host visible without stealing focus.
func (h *Host) Show() {
	if h == nil || h.hwnd == 0 {
		return
	}
	_, _, _ = pShowWindow.Call(h.hwnd, swShowNA)
}

// Hide hides the host (off-screen / covered by a modal / player closed).
func (h *Host) Hide() {
	if h == nil || h.hwnd == 0 {
		return
	}
	_, _, _ = pShowWindow.Call(h.hwnd, swHide)
}

// Destroy tears the host down. Caller-thread hosts (Create): call from the creation thread.
// Pumped hosts (CreateHosted): callable from anywhere - quits the owner thread's pump, which
// destroys the window as the thread exits. Idempotent.
func (h *Host) Destroy() {
	if h == nil || h.hwnd == 0 {
		return
	}
	if h.pumpTID != 0 {
		_, _, _ = pPostThreadMessageW.Call(h.pumpTID, wmQuit, 0, 0)
		h.hwnd, h.pumpTID = 0, 0
		return
	}
	_, _, _ = pDestroyWindow.Call(h.hwnd)
	h.hwnd = 0
}
