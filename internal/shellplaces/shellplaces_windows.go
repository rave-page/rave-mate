//go:build windows

package shellplaces

// Windows Quick Access enumeration (stdlib syscall, no new deps). The Quick Access shell folder
// (shell:::{679f85cb-0220-4080-b29b-5540cc05aab6}) is the union Explorer's sidebar shows: pinned
// folders + frequent folders + recent files. We bind its BHID_EnumItems handler, walk the items,
// keep the FOLDERS (recent files are noise for a folder sidebar), and resolve each to a filesystem
// path.
//
// Pinned flag: the shell does NOT surface it per item on Windows 11 (26xxx) - System.Home.IsPinned
// is empty and System.IsPinnedToNameSpaceTree is true for EVERY Quick Access item (verified by
// spike). The user's pins come instead from the Quick Access jump list (destlist.go): its DestList
// stream carries a pin-status per target, and quickAccessPins() maps those onto the enumerated
// folders (custom pins; the six default known-folders are stored as shell IDs, not paths, and stay
// frequent here but still appear in PLACES). COM is reached through explicit vtable slots (matching
// internal/webui/pickers_windows.go).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	ole32   = syscall.NewLazyDLL("ole32.dll")

	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
)

type guid struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

var (
	iidShellItem = guid{0x43826d1e, 0xe718, 0x42ee, [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
	iidEnumItems = guid{0x70629033, 0xe363, 0x4a28, [8]byte{0xa5, 0x67, 0x0d, 0xb7, 0x80, 0x06, 0xe6, 0xd7}}
	bhidEnumItem = guid{0x94f60519, 0x2850, 0x4924, [8]byte{0xaa, 0x5a, 0xd1, 0x5e, 0x84, 0x86, 0x80, 0x39}}
)

const (
	coinitApartmentThreaded = 0x2
	sFalse                  = 0x1
	rpcEChangedMode         = 0x80010106

	sigdnFilesysPath   = 0x80058000
	sigdnNormalDisplay = 0x0

	sfgaoFolder = 0x20000000
	sfgaoStream = 0x00400000 // set for .zip/.cab "compressed folder" items - exclude them

	// IUnknown / IShellItem / IEnumShellItems vtable slots
	slotRelease        = 2
	slotBindToHandler  = 3
	slotGetDisplayName = 5
	slotGetAttributes  = 6
	slotEnumNext       = 3

	quickAccessCLSID = "shell:::{679f85cb-0220-4080-b29b-5540cc05aab6}"
	maxScan          = 200 // hard bound on the enum walk
)

type comObj struct{ vtbl *[64]uintptr }

func (o *comObj) call(slot int, a ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, a...)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	return r
}

func pwToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// systemPlaces enumerates the Quick Access folders. Any COM failure yields an empty slice (the
// sidebar simply omits the group) - never an error the UI must handle.
func systemPlaces() []Place {
	// COM apartments are per-thread; lock so the goroutine can't resume elsewhere mid-sequence.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	switch hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); uint32(hr) {
	case 0, sFalse:
		defer func() { _, _, _ = procCoUninitialize.Call() }()
	case rpcEChangedMode: // a different apartment on this thread - reuse it, don't tear it down
	default:
		return nil
	}

	qa, err := syscall.UTF16PtrFromString(quickAccessCLSID)
	if err != nil {
		return nil
	}
	var item *comObj
	if hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(qa)), 0,
		uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item))); int32(hr) < 0 || item == nil {
		return nil
	}
	defer item.call(slotRelease)

	var en *comObj
	if hr := item.call(slotBindToHandler, 0,
		uintptr(unsafe.Pointer(&bhidEnumItem)),
		uintptr(unsafe.Pointer(&iidEnumItems)),
		uintptr(unsafe.Pointer(&en))); int32(hr) < 0 || en == nil {
		return nil
	}
	defer en.call(slotRelease)

	pins := quickAccessPins()
	var out []Place
	for i := 0; i < maxScan; i++ {
		var child *comObj
		var fetched uint32
		if hr := en.call(slotEnumNext, 1, uintptr(unsafe.Pointer(&child)), uintptr(unsafe.Pointer(&fetched))); hr != 0 || fetched == 0 || child == nil {
			break
		}
		if p, ok := placeFromItem(child); ok {
			p.Pinned = pins[strings.ToLower(filepath.Clean(p.Path))]
			out = append(out, p)
		}
		child.call(slotRelease)
	}
	return out
}

// quickAccessPins reads the Quick Access pinned jump list and returns the set (lowercased, cleaned)
// of user-pinned folders that still exist on disk. The existence check self-validates the parse, so
// a wrong-version DestList or a corrupt file yields an empty set (fail-soft to the frequent superset)
// rather than mislabelled pins. Default known-folder pins are stored as shell IDs, not paths, so
// they are not surfaced here - they still appear in QUICK ACCESS (as frequent) and in PLACES.
func quickAccessPins() map[string]bool {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(appData,
		"Microsoft", "Windows", "Recent", "AutomaticDestinations", "f01b4d95cf55d32a.automaticDestinations-ms"))
	if err != nil {
		return nil
	}
	stream, ok := readCFBStream(raw, "DestList")
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, p := range parseDestListPins(stream) {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			set[strings.ToLower(filepath.Clean(p))] = true
		}
	}
	return set
}

// placeFromItem keeps folder items only and resolves the filesystem path + display name.
func placeFromItem(child *comObj) (Place, bool) {
	var attrs uint32
	// GetAttributes(SFGAO_FOLDER|SFGAO_STREAM): keep real filesystem folders (FOLDER set), drop
	// archive "compressed folders" like .zip/.cab (STREAM also set) - they're recent files, not dirs.
	if hr := child.call(slotGetAttributes, sfgaoFolder|sfgaoStream, uintptr(unsafe.Pointer(&attrs))); int32(hr) < 0 ||
		attrs&sfgaoFolder == 0 || attrs&sfgaoStream != 0 {
		return Place{}, false
	}
	var pp *uint16
	if hr := child.call(slotGetDisplayName, sigdnFilesysPath, uintptr(unsafe.Pointer(&pp))); int32(hr) < 0 || pp == nil {
		return Place{}, false // virtual folder with no filesystem path - skip
	}
	path := pwToString(pp)
	_, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pp)))

	name := ""
	var pn *uint16
	if hr := child.call(slotGetDisplayName, sigdnNormalDisplay, uintptr(unsafe.Pointer(&pn))); int32(hr) >= 0 && pn != nil {
		name = pwToString(pn)
		_, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pn)))
	}
	if path == "" {
		return Place{}, false
	}
	return Place{Path: path, Name: name, Pinned: false}, true
}
