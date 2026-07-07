//go:build windows

package webcam

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Native UVC PTZ/exposure control: a DirectShow COM shim over stdlib syscall (the internal/midi
// winmm approach - no cgo, no new deps). Each operation is self-contained on a locked OS thread
// (CoInitializeEx → enumerate the video-input category → match FriendlyName → bind IBaseFilter →
// QI IAMCameraControl / IAMVideoProcAmp → call → release everything → CoUninitialize): stateless,
// so no COM object ever crosses threads and no handle is held that could exclusive-lock a driver.
// Binding the control interfaces is independent of the ffmpeg capture stream (MEDIALINK §5).
//
// COM interface pointers stay pointer-typed (*comObject) end to end - never round-tripped
// through uintptr - so the unsafe.Pointer rules (and go vet) hold.

var (
	ole32              = syscall.NewLazyDLL("ole32.dll")
	oleaut32           = syscall.NewLazyDLL("oleaut32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
	procCoCreateInst   = ole32.NewProc("CoCreateInstance")
	procVariantClear   = oleaut32.NewProc("VariantClear")
	procSysStringLen   = oleaut32.NewProc("SysStringLen")
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidSystemDeviceEnum = guid{0x62BE5D10, 0x60EB, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidICreateDevEnum     = guid{0x29840822, 0x5B84, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	clsidVideoInputCat    = guid{0x860BB310, 0x5D01, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidIBaseFilter        = guid{0x56A86895, 0x0AD4, 0x11CE, [8]byte{0xB0, 0x3A, 0x00, 0x20, 0xAF, 0x0B, 0xA7, 0x70}}
	iidIPropertyBag       = guid{0x55272A00, 0x42CB, 0x11CE, [8]byte{0x81, 0x35, 0x00, 0xAA, 0x00, 0x4B, 0xB8, 0x51}}
	iidIAMCameraControl   = guid{0xC6E13370, 0x30AC, 0x11D0, [8]byte{0xA1, 0x8C, 0x00, 0xA0, 0xC9, 0x11, 0x89, 0x56}}
	iidIAMVideoProcAmp    = guid{0xC6E13360, 0x30AC, 0x11D0, [8]byte{0xA1, 0x8C, 0x00, 0xA0, 0xC9, 0x11, 0x89, 0x56}}
)

const (
	coinitApartment  = 0x2 // COINIT_APARTMENTTHREADED (DirectShow convention)
	clsctxInprocSrv  = 0x1
	hrSFalse         = 1
	hrRPCChangedMode = 0x80010106
	vtBSTR           = 8
)

// comVariant mirrors the 24-byte x64 VARIANT (vt + reserved + 16-byte union). val holds the BSTR
// pointer when vt == VT_BSTR.
type comVariant struct {
	vt  uint16
	_   [3]uint16
	val unsafe.Pointer
	_   uintptr
}

// comObject is any COM interface pointer: the first word is the vtable.
type comObject struct {
	vtbl *[64]uintptr
}

// call invokes vtable slot n (receiver prepended); returns the HRESULT.
func (o *comObject) call(slot int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

func (o *comObject) release() {
	if o != nil {
		o.call(2) // IUnknown::Release
	}
}

// withCOM runs fn with COM initialized on a locked OS thread (per-call apartment; see file doc).
func withCOM(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	switch uint32(hr) {
	case 0, hrSFalse:
		defer func() { _, _, _ = procCoUninitialize.Call() }()
	case hrRPCChangedMode:
		// thread already initialized in the other model - usable without a matching uninit
	default:
		return fmt.Errorf("webcam: CoInitializeEx failed: 0x%08x", uint32(hr))
	}
	return fn()
}

// bindFilter binds the DirectShow capture filter whose FriendlyName equals device (the same
// name ffmpeg's dshow `video="…"` takes). Caller releases the returned filter.
func bindFilter(device string) (*comObject, error) {
	var devEnum *comObject
	hr, _, _ := procCoCreateInst.Call(
		uintptr(unsafe.Pointer(&clsidSystemDeviceEnum)), 0, clsctxInprocSrv,
		uintptr(unsafe.Pointer(&iidICreateDevEnum)), uintptr(unsafe.Pointer(&devEnum)))
	if hr != 0 || devEnum == nil {
		return nil, fmt.Errorf("webcam: CoCreateInstance(SystemDeviceEnum): 0x%08x", uint32(hr))
	}
	defer devEnum.release()

	var enum *comObject
	// ICreateDevEnum::CreateClassEnumerator(category, ppEnum, flags) - S_FALSE = empty category.
	if r := devEnum.call(3, uintptr(unsafe.Pointer(&clsidVideoInputCat)),
		uintptr(unsafe.Pointer(&enum)), 0); r != 0 || enum == nil {
		return nil, fmt.Errorf("webcam: no video capture devices present")
	}
	defer enum.release()

	for {
		var mon *comObject
		var fetched uint32
		// IEnumMoniker::Next(1, &moniker, &fetched)
		if enum.call(3, 1, uintptr(unsafe.Pointer(&mon)), uintptr(unsafe.Pointer(&fetched))) != 0 || mon == nil {
			break
		}
		name := monikerFriendlyName(mon)
		if name == device {
			var filter *comObject
			// IMoniker::BindToObject(pbc, pmkToLeft, riid, ppv) - vtable slot 8
			r := mon.call(8, 0, 0, uintptr(unsafe.Pointer(&iidIBaseFilter)), uintptr(unsafe.Pointer(&filter)))
			mon.release()
			if r != 0 || filter == nil {
				return nil, fmt.Errorf("webcam: bind %q failed: 0x%08x", device, uint32(r))
			}
			return filter, nil
		}
		mon.release()
	}
	return nil, fmt.Errorf("webcam: device %q not found", device)
}

// monikerFriendlyName reads the moniker's FriendlyName property ("" on any failure).
func monikerFriendlyName(mon *comObject) string {
	var bag *comObject
	// IMoniker::BindToStorage(pbc, pmkToLeft, riid, ppv) - vtable slot 9
	if mon.call(9, 0, 0, uintptr(unsafe.Pointer(&iidIPropertyBag)), uintptr(unsafe.Pointer(&bag))) != 0 || bag == nil {
		return ""
	}
	defer bag.release()
	key, err := syscall.UTF16PtrFromString("FriendlyName")
	if err != nil {
		return ""
	}
	var v comVariant
	// IPropertyBag::Read(pszPropName, pVar, pErrorLog)
	if bag.call(3, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)), 0) != 0 {
		return ""
	}
	defer func() { _, _, _ = procVariantClear.Call(uintptr(unsafe.Pointer(&v))) }()
	if v.vt != vtBSTR || v.val == nil {
		return ""
	}
	n, _, _ := procSysStringLen.Call(uintptr(v.val))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(v.val), n))
}

// queryIface QIs obj for iid; nil when unsupported. Caller releases.
func queryIface(obj *comObject, iid *guid) *comObject {
	var out *comObject
	if obj.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))) != 0 {
		return nil
	}
	return out
}

// Both IAMCameraControl and IAMVideoProcAmp share the vtable shape:
//
//	3 GetRange(prop, *min, *max, *step, *default, *capsFlags)
//	4 Set(prop, value, flags)
//	5 Get(prop, *value, *flags)

func uvcGetRange(iface *comObject, prop int32) (min, max, step, def, caps int32, ok bool) {
	r := iface.call(3, uintptr(prop),
		uintptr(unsafe.Pointer(&min)), uintptr(unsafe.Pointer(&max)), uintptr(unsafe.Pointer(&step)),
		uintptr(unsafe.Pointer(&def)), uintptr(unsafe.Pointer(&caps)))
	return min, max, step, def, caps, r == 0
}

func uvcGet(iface *comObject, prop int32) (value, flags int32, ok bool) {
	r := iface.call(5, uintptr(prop),
		uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&flags)))
	return value, flags, r == 0
}

func uvcSetRaw(iface *comObject, prop, value, flags int32) error {
	if r := iface.call(4, uintptr(prop), uintptr(value), uintptr(flags)); r != 0 {
		return fmt.Errorf("webcam: property set failed: 0x%08x", uint32(r))
	}
	return nil
}

// uvcProps reads the full property table (range + value + auto state) for a device. Properties
// the device lacks are omitted.
func uvcProps(device string) ([]PropState, error) {
	var out []PropState
	err := withCOM(func() error {
		filter, err := bindFilter(device)
		if err != nil {
			return err
		}
		defer filter.release()
		camCtl := queryIface(filter, &iidIAMCameraControl)
		defer camCtl.release()
		procAmp := queryIface(filter, &iidIAMVideoProcAmp)
		defer procAmp.release()
		for _, p := range propCatalog {
			iface := camCtl
			if p.Iface == ifaceProcAmp {
				iface = procAmp
			}
			if iface == nil {
				continue
			}
			min, max, step, def, caps, ok := uvcGetRange(iface, p.Index)
			if !ok {
				continue // device lacks this property - graceful no-op
			}
			st := PropState{ID: p.ID, Label: p.Label, Min: min, Max: max, Step: step, Default: def,
				CanAuto: caps&uvcFlagAuto != 0}
			if v, fl, ok := uvcGet(iface, p.Index); ok {
				st.Value, st.Auto = v, fl&uvcFlagAuto != 0
			} else {
				st.Value = def
			}
			out = append(out, st)
		}
		return nil
	})
	return out, err
}

// uvcSet sets one property (clamped/step-snapped) or switches it to auto.
func uvcSet(device, propID string, value int32, auto bool) error {
	p, ok := propByID(propID)
	if !ok {
		return fmt.Errorf("webcam: unknown property %q", propID)
	}
	return withCOM(func() error {
		filter, err := bindFilter(device)
		if err != nil {
			return err
		}
		defer filter.release()
		iid := &iidIAMCameraControl
		if p.Iface == ifaceProcAmp {
			iid = &iidIAMVideoProcAmp
		}
		iface := queryIface(filter, iid)
		if iface == nil {
			return fmt.Errorf("webcam: device has no %s control", p.Label)
		}
		defer iface.release()
		min, max, step, def, caps, ok := uvcGetRange(iface, p.Index)
		if !ok {
			return fmt.Errorf("webcam: device does not support %s", p.Label)
		}
		if auto {
			if caps&uvcFlagAuto == 0 {
				return fmt.Errorf("webcam: %s has no auto mode", p.Label)
			}
			// drivers want a valid value alongside the auto flag - the default is always valid
			return uvcSetRaw(iface, p.Index, def, uvcFlagAuto)
		}
		return uvcSetRaw(iface, p.Index, clampProp(value, min, max, step), uvcFlagManual)
	})
}
