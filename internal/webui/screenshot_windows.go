//go:build windows

package webui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procPrintWindow            = user32.NewProc("PrintWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

type winRect struct{ left, top, right, bottom int32 }

type bitmapInfoHeader struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPPM, YPPM             int32
	ClrUsed, ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// showWindow raises + foregrounds the window (ctl show / tray restore).
func showWindow(h uintptr) {
	if h == 0 {
		return
	}
	const swShow = 5
	_, _, _ = procShowWindow.Call(h, swShow)
	_, _, _ = procSetForegroundWindow.Call(h)
}

// revealWindow makes h visible WITHOUT stealing focus.
//
// The B5 window child NEEDS this. featurehost spawns children with sysexec.Hide (STARTF_USESHOWWINDOW
// + SW_HIDE) to stop a console window appearing, and Windows applies that show state to the process's
// FIRST top-level window - so the child's WebView2 window is created hidden. Symptoms: no visible UI
// under RAVE_MATE_SHELL=proc, and every capture solid black (a window that was never shown has
// nothing to render). Verified: capturing the same window after a ShowWindow goes from 0.00% to
// 97.89% non-black pixels.
func revealWindow(h uintptr) {
	if h == 0 {
		return
	}
	const swShowNoActivate = 4
	_, _, _ = procShowWindow.Call(h, swShowNoActivate)
}

// captureRegionLocal captures u's window from THIS process. The in-proc shell's path; under the B5
// procShell the capture runs in the child instead (see screenshot.go).
func (u *UI) captureRegionLocal(path string, x, y, w, h int) error {
	if u.shell == nil {
		return fmt.Errorf("no window")
	}
	return captureHWND(u.shell.hwnd(), path, x, y, w, h)
}

// captureHWND PrintWindows hwnd into a DIB and PNG-encodes it. x/y/w/h in device px; w<=0||h<=0 =
// full window. PW_RENDERFULLCONTENT is required for GPU/WebView2 surfaces.
//
// The window must be SHOWN: PrintWindow returns whatever the window has rendered, and a window that
// was never shown has rendered nothing - the capture comes back solid black. That was the B5
// procShell defect (the child inherited SW_HIDE; see revealWindow below and screenshot.go).
func captureHWND(hwnd uintptr, path string, x, y, w, h int) error {
	if hwnd == 0 {
		return fmt.Errorf("no window handle")
	}
	var r winRect
	_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	fullW, fullH := int(r.right-r.left), int(r.bottom-r.top)
	if fullW <= 0 || fullH <= 0 {
		return fmt.Errorf("bad window rect %dx%d", fullW, fullH)
	}

	hdcWin, _, _ := procGetDC.Call(hwnd)
	if hdcWin == 0 {
		return fmt.Errorf("GetDC failed")
	}
	defer func() { _, _, _ = procReleaseDC.Call(hwnd, hdcWin) }()
	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcWin)
	hbmp, _, _ := procCreateCompatibleBitmap.Call(hdcWin, uintptr(fullW), uintptr(fullH))
	old, _, _ := procSelectObject.Call(hdcMem, hbmp)
	const pwRenderFullContent = 2
	_, _, _ = procPrintWindow.Call(hwnd, hdcMem, pwRenderFullContent)

	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(fullW)
	bi.Header.Height = -int32(fullH) // negative = top-down rows
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = 0 // BI_RGB
	buf := make([]byte, fullW*fullH*4)
	const dibRGBColors = 0
	_, _, _ = procGetDIBits.Call(hdcMem, hbmp, 0, uintptr(fullH), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)

	_, _, _ = procSelectObject.Call(hdcMem, old)
	_, _, _ = procDeleteObject.Call(hbmp)
	_, _, _ = procDeleteDC.Call(hdcMem)

	img := image.NewRGBA(image.Rect(0, 0, fullW, fullH))
	for i := 0; i < fullW*fullH; i++ {
		img.Pix[i*4] = buf[i*4+2]   // R (from BGRA)
		img.Pix[i*4+1] = buf[i*4+1] // G
		img.Pix[i*4+2] = buf[i*4]   // B
		img.Pix[i*4+3] = 255
	}

	var out image.Image = img
	if w > 0 && h > 0 {
		out = img.SubImage(image.Rect(x, y, x+w, y+h).Intersect(img.Bounds()))
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, out)
}
