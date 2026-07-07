//go:build windows

package winshot

import (
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	xdraw "golang.org/x/image/draw"
)

// maxVRViewDim caps the captured VR-View's longest side so the encoded PNG fits the peer-link control
// frame (~768 KiB base64) when fetched remotely. Plenty to read the overlay; full mirrors are huge.
const maxVRViewDim = 1600

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procEnumWindows          = user32.NewProc("EnumWindows")
	procIsWindowVisible      = user32.NewProc("IsWindowVisible")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procGetWindowRect        = user32.NewProc("GetWindowRect")
	procGetDC                = user32.NewProc("GetDC")
	procReleaseDC            = user32.NewProc("ReleaseDC")
	procPrintWindow          = user32.NewProc("PrintWindow")
	procCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject         = gdi32.NewProc("SelectObject")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
	procDeleteDC             = gdi32.NewProc("DeleteDC")
	procGetDIBits            = gdi32.NewProc("GetDIBits")
)

const (
	pwRenderFullContent = 0x00000002 // PrintWindow flag: include GPU/DWM-composited content (the VR mirror)
	biRGB               = 0
	dibRGBColors        = 0
)

type rect struct{ Left, Top, Right, Bottom int32 }

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// enum state is package-level (guarded by findMu) so the EnumWindows callback is registered once
// (syscall.NewCallback allocations are permanent + capped) and never captures a Go closure.
var (
	findMu       sync.Mutex
	findMatch    func(string) bool
	findHwnd     uintptr
	enumCallback = syscall.NewCallback(enumProc)
)

func enumProc(hwnd uintptr, _ uintptr) uintptr {
	if findHwnd != 0 {
		return 0 // already found - stop enumerating
	}
	if r, _, _ := procIsWindowVisible.Call(hwnd); r == 0 {
		return 1
	}
	if t := windowText(hwnd); t != "" && findMatch(t) {
		findHwnd = hwnd
		return 0
	}
	return 1
}

// findWindow returns the first visible top-level window whose title satisfies match (0 = none).
func findWindow(match func(string) bool) uintptr {
	findMu.Lock()
	defer findMu.Unlock()
	findMatch = match
	findHwnd = 0
	_, _, _ = procEnumWindows.Call(enumCallback, 0)
	return findHwnd
}

func windowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	_, _, _ = procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// CaptureVRView finds the SteamVR VR-View / headset-mirror window and writes it as a PNG to path.
// Prefers the most specific title; falls back to any SteamVR window. Caller gates on the opt-in.
func CaptureVRView(path string) error {
	if path == "" {
		return errors.New("winshot: empty path")
	}
	hwnd := findBest()
	if hwnd == 0 {
		return errors.New("winshot: no SteamVR VR-View window found (is the VR View open?)")
	}
	img, err := captureWindow(hwnd)
	if err != nil {
		return err
	}
	img = downscale(img, maxVRViewDim)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// JPEG for a photographic VR mirror (a PNG of it is ~10× bigger and blows the peer-link frame);
	// PNG for anything else. Chosen by the output extension.
	if lower := strings.ToLower(path); strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
		return jpeg.Encode(f, img, &jpeg.Options{Quality: 82})
	}
	return png.Encode(f, img)
}

// downscale shrinks src so its longest side is ≤ maxDim (preserving aspect); returns src unchanged if
// already small enough.
func downscale(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	m := max(w, h)
	if m <= maxDim || m == 0 {
		return src
	}
	nw, nh := w*maxDim/m, h*maxDim/m
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// findBest scans for the most specific VR-mirror window first, then any SteamVR window.
func findBest() uintptr {
	for _, hint := range []string{"vr view", "headset window", "openvr"} {
		if h := findWindow(hasHint(hint)); h != 0 {
			return h
		}
	}
	return findWindow(MatchVRView)
}

func hasHint(hint string) func(string) bool {
	return func(t string) bool { return strings.Contains(strings.ToLower(t), hint) }
}

// captureWindow grabs hwnd's full window rect via PrintWindow → GetDIBits → RGBA image.
func captureWindow(hwnd uintptr) (image.Image, error) {
	var r rect
	if ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return nil, errors.New("winshot: GetWindowRect failed")
	}
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 {
		return nil, errors.New("winshot: window has zero size")
	}
	hdcWin, _, _ := procGetDC.Call(hwnd)
	if hdcWin == 0 {
		return nil, errors.New("winshot: GetDC failed")
	}
	defer func() { _, _, _ = procReleaseDC.Call(hwnd, hdcWin) }()

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcWin)
	if hdcMem == 0 {
		return nil, errors.New("winshot: CreateCompatibleDC failed")
	}
	defer func() { _, _, _ = procDeleteDC.Call(hdcMem) }()

	hbm, _, _ := procCreateCompatibleBmp.Call(hdcWin, uintptr(w), uintptr(h))
	if hbm == 0 {
		return nil, errors.New("winshot: CreateCompatibleBitmap failed")
	}
	defer func() { _, _, _ = procDeleteObject.Call(hbm) }()

	if old, _, _ := procSelectObject.Call(hdcMem, hbm); old == 0 {
		return nil, errors.New("winshot: SelectObject failed")
	}
	if ret, _, _ := procPrintWindow.Call(hwnd, hdcMem, pwRenderFullContent); ret == 0 {
		return nil, errors.New("winshot: PrintWindow failed")
	}

	bi := bitmapInfo{}
	bi.Header.BiSize = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.BiWidth = int32(w)
	bi.Header.BiHeight = int32(-h) // top-down
	bi.Header.BiPlanes = 1
	bi.Header.BiBitCount = 32
	bi.Header.BiCompression = biRGB

	buf := make([]byte, w*h*4)
	if ret, _, _ := procGetDIBits.Call(hdcMem, hbm, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors); ret == 0 {
		return nil, errors.New("winshot: GetDIBits failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ { // BGRA → RGBA; force opaque (PrintWindow leaves alpha 0)
		img.Pix[i*4+0] = buf[i*4+2]
		img.Pix[i*4+1] = buf[i*4+1]
		img.Pix[i*4+2] = buf[i*4+0]
		img.Pix[i*4+3] = 0xff
	}
	return img, nil
}
