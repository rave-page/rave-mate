//go:build windows

package gpuwatch

import (
	"context"
	"encoding/xml"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	wevtapi  = syscall.NewLazyDLL("wevtapi.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsHungAppWindow          = user32.NewProc("IsHungAppWindow")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetCurrentProcessId      = kernel32.NewProc("GetCurrentProcessId")

	procEvtQuery  = wevtapi.NewProc("EvtQuery")
	procEvtNext   = wevtapi.NewProc("EvtNext")
	procEvtRender = wevtapi.NewProc("EvtRender")
	procEvtClose  = wevtapi.NewProc("EvtClose")
)

// start launches the hung-window + TDR detectors on their own goroutines.
func start(ctx context.Context, opt Options) {
	pid, _, _ := procGetCurrentProcessId.Call()
	w := &winWatch{myPID: uint32(pid)}
	w.enumCB = syscall.NewCallback(w.enumProc) // once - NewCallback allocations can't be freed
	go w.runHang(ctx, opt)
	go runTDR(ctx, opt)
}

// ── hung-window detector ─────────────────────────────────────────────────────

type winWatch struct {
	myPID  uint32
	enumCB uintptr
	found  uintptr // written by enumProc during an EnumWindows pass
}

// enumProc (EnumWindows callback) keeps the first visible, captioned top-level window owned by
// this process - the Fyne main window. Return 1 = keep enumerating, 0 = stop.
func (w *winWatch) enumProc(hwnd, _ uintptr) uintptr {
	var pid uint32
	_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != w.myPID {
		return 1
	}
	if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
		return 1 // hidden (tray mode / GLFW helper) - not assessable
	}
	if n, _, _ := procGetWindowTextLengthW.Call(hwnd); n == 0 {
		return 1 // no caption → utility/tooltip window, not the main window
	}
	w.found = hwnd
	return 0
}

// mainWindow returns our main window handle (0 if none visible yet).
func (w *winWatch) mainWindow() uintptr {
	w.found = 0
	_, _, _ = procEnumWindows.Call(w.enumCB, 0)
	return w.found
}

// runHang polls IsHungAppWindow; a window unresponsive for >= HangAfter fires FaultHungWindow once.
func (w *winWatch) runHang(ctx context.Context, opt Options) {
	tick := time.NewTicker(opt.Poll)
	defer tick.Stop()
	var hungSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		h := w.mainWindow()
		if h == 0 {
			hungSince = time.Time{}
			continue
		}
		r, _, _ := procIsHungAppWindow.Call(h)
		if r == 0 {
			hungSince = time.Time{}
			continue
		}
		if hungSince.IsZero() {
			hungSince = time.Now()
			continue
		}
		if d := time.Since(hungSince); d >= opt.HangAfter {
			opt.OnFault(Fault{Kind: FaultHungWindow, Detail: fmt.Sprintf("main window unresponsive for %s", d.Truncate(time.Second))})
			return // one-shot: recovery restarts the process
		}
	}
}

// ── TDR (display-driver reset) detector ──────────────────────────────────────

const (
	evtQueryChannelPath         = 0x1
	evtQueryReverseDirection    = 0x200
	evtQueryTolerateQueryErrors = 0x1000
	evtRenderEventXML           = 1
)

// tdrQuery matches OS-logged display-driver resets. Provider "Display" EventID 4101 is the
// vendor-agnostic "driver stopped responding and has recovered" (the canonical TDR); the vendor
// providers + IDs are belt-and-suspenders for cards that log under their own source. TimeCreated
// bounds the reverse scan to the last hour so a poll never walks the whole System log (the
// unbounded query re-rendered every historical match on machines with a long TDR history).
const tdrQuery = `*[System[Provider[@Name='Display' or @Name='nvlddmkm' or @Name='amdkmdag' or @Name='amdwddmg' or @Name='igfxn'] and (EventID=4101 or EventID=4098 or EventID=4099 or EventID=13 or EventID=14) and TimeCreated[timediff(@SystemTime) <= 3600000]]]`

type evtRecord struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID       int    `xml:"EventID"`
		EventRecordID uint64 `xml:"EventRecordID"`
	} `xml:"System"`
}

// runTDR polls the System event log for new driver-reset records; each new one fires FaultTDR.
// Fire/baseline decisions live in tdrTracker (pure, tested cross-platform).
func runTDR(ctx context.Context, opt Options) {
	if err := procEvtQuery.Find(); err != nil {
		return // wevtapi unavailable (very old Windows) - hung-window detector still covers "stuck"
	}
	chanPtr, _ := syscall.UTF16PtrFromString("System")
	qPtr, _ := syscall.UTF16PtrFromString(tdrQuery)

	var tr tdrTracker
	tick := time.NewTicker(opt.TDRPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		newest, rec, ok := scanTDR(chanPtr, qPtr)
		if !ok {
			continue
		}
		if tr.observe(newest) {
			opt.OnFault(Fault{Kind: FaultTDR, Detail: fmt.Sprintf("%s (event %d)", rec.System.Provider.Name, rec.System.EventID)})
		}
	}
}

// scanTDR queries newest-first (time-bounded by tdrQuery) and returns the newest matching record's
// id + the record itself; newest==0 when the window has no matches. Stops at the FIRST parsed
// record - only the tip matters, and the old walk-until-seen loop degenerated into rendering every
// match whenever the baseline was 0. ok=false when the query can't run.
func scanTDR(chanPtr, qPtr *uint16) (newest uint64, first evtRecord, ok bool) {
	h, _, _ := procEvtQuery.Call(0, uintptr(unsafe.Pointer(chanPtr)), uintptr(unsafe.Pointer(qPtr)),
		evtQueryChannelPath|evtQueryReverseDirection|evtQueryTolerateQueryErrors)
	if h == 0 {
		return 0, first, false
	}
	defer evtClose(h)
	for {
		var hEvent uintptr
		var returned uint32
		r, _, _ := procEvtNext.Call(h, 1, uintptr(unsafe.Pointer(&hEvent)), 200, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 || returned == 0 {
			break // ERROR_NO_MORE_ITEMS / timeout
		}
		x, rendered := renderXML(hEvent)
		evtClose(hEvent)
		if !rendered {
			continue
		}
		var rec evtRecord
		if xml.Unmarshal([]byte(x), &rec) != nil {
			continue // tolerate one unparsable record; try the next
		}
		return rec.System.EventRecordID, rec, true // newest-first: first parsed record is the tip
	}
	return 0, first, true // no matches in the window
}

// renderXML renders one event handle to its System XML (two-call size-then-fill).
// evtClose releases an EvtQuery/event handle (error deliberately ignored - nothing to do on fail).
func evtClose(h uintptr) { _, _, _ = procEvtClose.Call(h) }

func renderXML(hEvent uintptr) (string, bool) {
	var used, props uint32
	_, _, _ = procEvtRender.Call(0, hEvent, evtRenderEventXML, 0, 0, uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if used == 0 {
		return "", false
	}
	buf := make([]uint16, used/2+1)
	r, _, _ := procEvtRender.Call(0, hEvent, evtRenderEventXML, uintptr(used),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if r == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}
