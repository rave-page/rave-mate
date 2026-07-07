//go:build windows

package tray

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"rave.page/mate/internal/sysnotify"
)

// Win32 tray via Shell_NotifyIcon. A hidden (never-shown) message window owns the tray icon and
// its right-click popup menu. The message loop runs on its own locked OS thread (the webview owns
// the main thread). All bindings are stdlib syscall - no external dependency. Ported from
// rave-app/internal/tray/tray_windows.go, trimmed to the rave-mate menu (Open/Check/Quit).

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procSetForegroundWin = user32.NewProc("SetForegroundWindow")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procLoadIconW        = user32.NewProc("LoadIconW")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procExtractIconW     = shell32.NewProc("ExtractIconW")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy   = 0x0002
	wmClose     = 0x0010
	wmCommand   = 0x0111
	wmApp       = 0x8000
	wmTrayCB    = wmApp + 1 // our NOTIFYICONDATA callback message
	wmBalloon   = wmApp + 2 // "fire a balloon from the pending title/body" (posted to the msg window)
	wmRButtonUp = 0x0205
	wmLButtonUp = 0x0202

	nimAdd     = 0x0
	nimModify  = 0x1
	nimDelete  = 0x2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10 // balloon (szInfo/szInfoTitle) present
	niifInfo   = 0x1  // NIIF_INFO: info-icon glyph on the balloon

	mfString     = 0x0
	mfSeparator  = 0x800
	tpmReturnCmd = 0x0100
	tpmRightBtn  = 0x0002

	idiApplication = 32512

	idShow  = 1
	idCheck = 2
	idQuit  = 3
)

type point struct{ x, y int32 }

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type msg struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             syscall.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            syscall.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     syscall.Handle
}

type tray struct {
	opt  Options
	hwnd syscall.Handle
	nid  notifyIconData

	balloonMu sync.Mutex // guards the pending balloon payload (WM_BALLOON carries no data)
	pendTitle string
	pendBody  string
}

// Start installs the tray icon + menu and runs its message loop on a dedicated OS thread. The
// returned stop tears the icon + window down. err if the window/icon couldn't be created.
func Start(opt Options) (func(), error) {
	t := &tray{opt: opt}
	ready := make(chan error, 1)
	go t.run(ready)
	if err := <-ready; err != nil {
		return func() {}, err
	}
	return t.stop, nil
}

func (t *tray) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("RaveMateTrayWindow")

	wc := wndClassEx{
		style:         0,
		lpfnWndProc:   syscall.NewCallback(t.wndProc),
		hInstance:     syscall.Handle(hInst),
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- fmt.Errorf("tray: RegisterClassExW: %v", err)
		return
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("tray: CreateWindowExW: %v", err)
		return
	}
	t.hwnd = syscall.Handle(hwnd)

	t.nid = notifyIconData{
		hWnd:             t.hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCB,
		hIcon:            loadAppIcon(),
	}
	t.nid.cbSize = uint32(unsafe.Sizeof(t.nid))
	copyTip(&t.nid.szTip, t.opt.Tooltip)
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid))); r == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- fmt.Errorf("tray: Shell_NotifyIcon(ADD): %v", err)
		return
	}

	// Register the reliable native-notification path (Shell_NotifyIcon balloon) for sysnotify.Send.
	sysnotify.SetNative(t.notify)

	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	sysnotify.SetNative(nil) // tray gone - fall back to the generic path
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
}

// notify queues a balloon payload and posts WM_BALLOON so the icon-owning thread fires it
// (Shell_NotifyIcon must be driven from that thread). Safe to call from any goroutine.
func (t *tray) notify(title, body string) error {
	if t.hwnd == 0 {
		return fmt.Errorf("tray: no window")
	}
	t.balloonMu.Lock()
	t.pendTitle, t.pendBody = title, body
	t.balloonMu.Unlock()
	if r, _, err := procPostMessageW.Call(uintptr(t.hwnd), wmBalloon, 0, 0); r == 0 {
		return fmt.Errorf("tray: PostMessage(WM_BALLOON): %v", err)
	}
	return nil
}

// showBalloon fills szInfoTitle/szInfo from the pending payload and fires a NIF_INFO balloon via
// NIM_MODIFY, then clears NIF_INFO so later tip-only modifies don't re-pop it. Runs on the tray
// thread (from wndProc).
func (t *tray) showBalloon() {
	t.balloonMu.Lock()
	title, body := t.pendTitle, t.pendBody
	t.balloonMu.Unlock()

	copyField(t.nid.szInfoTitle[:], title)
	copyField(t.nid.szInfo[:], body)
	t.nid.dwInfoFlags = niifInfo
	t.nid.uFlags |= nifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&t.nid)))
	t.nid.uFlags &^= nifInfo
}

// stop closes the hidden window (→ WM_DESTROY → PostQuitMessage → loop exits → icon removed).
func (t *tray) stop() {
	if t.hwnd != 0 {
		procPostMessageW.Call(uintptr(t.hwnd), wmClose, 0, 0)
	}
}

func (t *tray) wndProc(hwnd syscall.Handle, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTrayCB:
		switch uint32(lParam) {
		case wmRButtonUp:
			t.showMenu()
		case wmLButtonUp:
			t.dispatch(idShow)
		}
		return 0
	case wmBalloon:
		t.showBalloon()
		return 0
	case wmCommand:
		t.dispatch(uint32(wParam & 0xffff))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

// showMenu pops the right-click menu at the cursor and dispatches the chosen item. TPM_RETURNCMD
// makes TrackPopupMenu return the id directly (no WM_COMMAND round-trip). A nil callback omits its
// item so the menu never offers an action that does nothing.
func (t *tray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendItem(menu, idShow, or(t.opt.OpenLabel, "Open rave-mate"))
	if t.opt.OnCheckUpdates != nil {
		appendSep(menu)
		appendItem(menu, idCheck, or(t.opt.CheckLabel, "Check for updates"))
	}
	appendSep(menu)
	appendItem(menu, idQuit, or(t.opt.QuitLabel, "Quit"))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWin.Call(uintptr(t.hwnd)) // so the menu auto-dismisses on focus loss
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightBtn,
		uintptr(pt.x), uintptr(pt.y), 0, uintptr(t.hwnd), 0)
	if cmd != 0 {
		t.dispatch(uint32(cmd))
	}
}

// dispatch runs the action for a menu id off the message loop so a slow/blocking callback (and
// Quit, which tears this loop down) can't deadlock the tray thread.
func (t *tray) dispatch(id uint32) {
	var fn func()
	switch id {
	case idShow:
		fn = t.opt.OnShow
	case idCheck:
		fn = t.opt.OnCheckUpdates
	case idQuit:
		fn = t.opt.OnQuit
	}
	if fn != nil {
		go fn()
	}
}

func appendItem(menu uintptr, id uint32, label string) {
	p, _ := syscall.UTF16PtrFromString(label)
	procAppendMenuW.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(p)))
}

func appendSep(menu uintptr) { procAppendMenuW.Call(menu, mfSeparator, 0, 0) }

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// loadAppIcon extracts the exe's first icon (the committed brand .syso, same source as the Fyne
// tray's icon.png), falling back to the stock application icon.
func loadAppIcon() syscall.Handle {
	if exe, err := os.Executable(); err == nil {
		if p, err := syscall.UTF16PtrFromString(exe); err == nil {
			hInst, _, _ := procGetModuleHandleW.Call(0)
			h, _, _ := procExtractIconW.Call(hInst, uintptr(unsafe.Pointer(p)), 0)
			if h != 0 && h != 1 { // 1 = file has no icons
				return syscall.Handle(h)
			}
		}
	}
	h, _, _ := procLoadIconW.Call(0, idiApplication)
	return syscall.Handle(h)
}

// copyField writes s into a fixed NOTIFYICONDATA UTF-16 field, truncated to len(dst)-1 + NUL,
// zeroing any trailing slot (szInfo 256 / szInfoTitle 64).
func copyField(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		u = []uint16{0} // e.g. embedded NUL - fall back to empty
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, u)
}

// copyTip writes s (truncated to 127 UTF-16 units + NUL) into a NOTIFYICONDATA tip field.
func copyTip(dst *[128]uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst[:], u)
}
