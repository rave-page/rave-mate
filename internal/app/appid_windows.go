//go:build windows

package app

// Windows app identity (AppUserModelID). Two things need it:
//
//  1. The taskbar. With the Zig shell as the window host the visible window belongs to
//     rave-shell-<embed-hash>.exe, NOT rave-mate.exe. Windows derives an app identity from the
//     window process's exe path unless told otherwise, so the window (a) never matched the pinned
//     rave-mate.exe shortcut - hence a SECOND taskbar button next to the pin - and (b) changed
//     identity on every update, because the staged child's filename carries the embed's content
//     hash. An explicit id shared by both processes fixes both.
//  2. Toasts. sysnotify's note that a toast fired without a registered AUMID may be dropped is
//     the same missing piece: Windows resolves an AUMID through a Start Menu shortcut carrying it.
//
// Stamping the shortcut does NOT retro-fix a pin the user already made (Explorer keeps its own
// copy of a pin), and this deliberately does not write into the user's User Pinned\TaskBar
// folder. A one-time unpin/re-pin adopts the new identity.
//
// COM is reached through typed vtables rather than slot indices: a wrong index is a silent call
// into the wrong method, and named fields make the layout reviewable against the SDK headers.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"rave.page/mate/internal/shared/logbus"
)

// appUserModelID is shared VERBATIM with native/zigui/src/shell/winshell.zig
// (app_user_model_id). Changing one without the other splits the taskbar button in two again.
const appUserModelID = "RavePage.RaveMate"

var (
	shell32dll = syscall.NewLazyDLL("shell32.dll")
	ole32dll   = syscall.NewLazyDLL("ole32.dll")
	propsysdll = syscall.NewLazyDLL("propsys.dll")

	procSetCurrentProcessExplicitAppUserModelID = shell32dll.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procCoInitializeEx                          = ole32dll.NewProc("CoInitializeEx")
	procCoUninitialize                          = ole32dll.NewProc("CoUninitialize")
	procCoCreateInstance                        = ole32dll.NewProc("CoCreateInstance")
	procPropVariantClear                        = ole32dll.NewProc("PropVariantClear")
	procPropVariantToString                     = propsysdll.NewProc("PropVariantToString")
)

var (
	clsidShellLink   = syscall.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile   = syscall.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPropertyStore = syscall.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}

	pkeyAppUserModelID = propertyKey{ // System.AppUserModel.ID
		fmtid: syscall.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}},
		pid:   5,
	}
)

const (
	vtLPWStr = 31

	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	stgmReadWrite           = 0x00000002

	sFalse          = 0x00000001 // CoInitializeEx: already initialised on this thread - a success
	rpcEChangedMode = 0x80010106
)

type propertyKey struct {
	fmtid syscall.GUID
	pid   uint32
}

// propVariant is the x64 PROPVARIANT: 8-byte header + 16-byte union (24 total). Only VT_LPWSTR is
// used here, whose pointer occupies the first union word. Held as uintptr, not unsafe.Pointer, so
// the GC never inspects a COM-owned address (callers pair it with runtime.KeepAlive).
type propVariant struct {
	vt        uint16
	reserved1 uint16
	reserved2 uint16
	reserved3 uint16
	val       uintptr
	_         uintptr
}

// IPersistFile (ObjIdl.h) - IUnknown + IPersist + the file methods.
type iPersistFile struct{ vtbl *iPersistFileVtbl }

type iPersistFileVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetClassID     uintptr
	IsDirty        uintptr
	Load           uintptr
	Save           uintptr
	SaveCompleted  uintptr
	GetCurFile     uintptr
}

// IPropertyStore (propsys.h).
type iPropertyStore struct{ vtbl *iPropertyStoreVtbl }

type iPropertyStoreVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetCount       uintptr
	GetAt          uintptr
	GetValue       uintptr
	SetValue       uintptr
	Commit         uintptr
}

func (o *iPersistFile) release() {
	_, _, _ = syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
}
func (o *iPropertyStore) release() {
	_, _, _ = syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
}

func hr(code uintptr, op string) error {
	if int32(code) < 0 {
		return fmt.Errorf("%s: hresult 0x%08x", op, uint32(code))
	}
	return nil
}

// setProcessAppUserModelID claims the shared taskbar identity for this process. Must run before
// any window (or tray) is created.
func setProcessAppUserModelID() error {
	p, err := syscall.UTF16PtrFromString(appUserModelID)
	if err != nil {
		return err
	}
	r, _, _ := procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(p)))
	return hr(r, "SetCurrentProcessExplicitAppUserModelID")
}

// startMenuShortcut returns the installer-created Start Menu .lnk path ("" if absent). Per-user
// install (the .nsi never does SetShellVarContext all), so $SMPROGRAMS is the roaming Start Menu.
func startMenuShortcut() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	p := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "rave-mate.lnk")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// shortcutAppID reads a loaded shortcut's System.AppUserModel.ID. PropVariantToString copies into
// OUR buffer, so no COM-owned address is ever converted back to a Go pointer.
func shortcutAppID(store *iPropertyStore) string {
	var pv propVariant
	r, _, _ := syscall.SyscallN(store.vtbl.GetValue, uintptr(unsafe.Pointer(store)),
		uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&pv)))
	if int32(r) < 0 {
		return ""
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.vt != vtLPWStr {
		return ""
	}
	var buf [512]uint16
	r, _, _ = procPropVariantToString.Call(uintptr(unsafe.Pointer(&pv)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if int32(r) < 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

// stampShortcutAppID writes appUserModelID into a .lnk's property store. changed=false means the
// shortcut already carried it - the steady state, since this runs on every boot.
func stampShortcutAppID(lnk string) (changed bool, err error) {
	// COM apartments are per-THREAD: without locking, the goroutine could resume on another
	// thread mid-sequence and use the interface pointers from an uninitialised apartment.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	switch uint32(r) {
	case 0, sFalse:
		defer procCoUninitialize.Call()
	case rpcEChangedMode: // another apartment model on this thread - do NOT tear it down
	default:
		if e := hr(r, "CoInitializeEx"); e != nil {
			return false, e
		}
	}

	lnk16, err := syscall.UTF16PtrFromString(lnk)
	if err != nil {
		return false, err
	}

	var persist *iPersistFile
	r, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&persist)))
	if err = hr(r, "CoCreateInstance(ShellLink)"); err != nil {
		return false, err
	}
	defer persist.release()

	r, _, _ = syscall.SyscallN(persist.vtbl.Load, uintptr(unsafe.Pointer(persist)),
		uintptr(unsafe.Pointer(lnk16)), stgmReadWrite)
	if err = hr(r, "IPersistFile::Load"); err != nil {
		return false, err
	}

	var store *iPropertyStore
	r, _, _ = syscall.SyscallN(persist.vtbl.QueryInterface, uintptr(unsafe.Pointer(persist)),
		uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&store)))
	if err = hr(r, "QueryInterface(IPropertyStore)"); err != nil {
		return false, err
	}
	defer store.release()

	if shortcutAppID(store) == appUserModelID {
		return false, nil
	}

	want, err := syscall.UTF16FromString(appUserModelID)
	if err != nil {
		return false, err
	}
	pv := propVariant{vt: vtLPWStr, val: uintptr(unsafe.Pointer(&want[0]))}
	r, _, _ = syscall.SyscallN(store.vtbl.SetValue, uintptr(unsafe.Pointer(store)),
		uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&pv)))
	runtime.KeepAlive(want) // SetValue copies the string; keep the source alive across the call
	if err = hr(r, "IPropertyStore::SetValue"); err != nil {
		return false, err
	}
	if r, _, _ = syscall.SyscallN(store.vtbl.Commit, uintptr(unsafe.Pointer(store))); int32(r) < 0 {
		return false, hr(r, "IPropertyStore::Commit")
	}
	// Save(nil, TRUE): nil keeps the file it was loaded from.
	if r, _, _ = syscall.SyscallN(persist.vtbl.Save, uintptr(unsafe.Pointer(persist)), 0, 1); int32(r) < 0 {
		return false, hr(r, "IPersistFile::Save")
	}
	return true, nil
}

// initAppIdentity claims the taskbar identity and repairs the Start Menu shortcut. Best-effort:
// a failure costs branding, never startup.
func initAppIdentity(log *logbus.Bus) {
	if err := setProcessAppUserModelID(); err != nil {
		log.Warn("app", "app identity not set - taskbar may not group with the pinned shortcut",
			map[string]any{"error": err.Error(), "appID": appUserModelID})
		return
	}
	lnk := startMenuShortcut()
	if lnk == "" {
		return // portable/dev run, or an install without a Start Menu entry
	}
	changed, err := stampShortcutAppID(lnk)
	switch {
	case err != nil:
		log.Warn("app", "start-menu shortcut app identity not stamped",
			map[string]any{"error": err.Error(), "lnk": lnk})
	case changed:
		// One-time on upgrade. Say what the user must do: Explorer keeps its own copy of a pin.
		log.Info("app", "stamped app identity on the start-menu shortcut - unpin+re-pin rave-mate once to merge the taskbar button",
			map[string]any{"lnk": lnk, "appID": appUserModelID})
	}
}
