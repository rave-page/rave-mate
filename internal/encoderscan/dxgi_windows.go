//go:build windows

package encoderscan

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// dxgi_windows.go enumerates GPU adapters via DXGI (pure syscall, no cgo, no new dep - same style as
// the PDH sampler). Gives each adapter its LUID (formatted to MATCH the PDH \GPU Engine luid token),
// human description, and dedicated VRAM - so per-process encode util (keyed by LUID) can be bound to
// a named device, and the headroom planner can target a specific adapter instead of "the busiest".

var (
	dxgi                   = syscall.NewLazyDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")
	iidIDXGIFactory1       = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	dxgiErrorNotFound      = uintptr(0x887A0002) // DXGI_ERROR_NOT_FOUND - enumerated past last adapter
	adapterFlagSoftware    = uint32(0x2)         // DXGI_ADAPTER_FLAG_SOFTWARE (Basic Render Driver) - skip
)

// guid mirrors Windows GUID / REFIID.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// dxgiAdapterDesc1 mirrors DXGI_ADAPTER_DESC1 (x64 layout: Description[128]WCHAR, 4×UINT, 3×SIZE_T,
// LUID, Flags).
type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLUID           struct {
		LowPart  uint32
		HighPart int32
	}
	Flags uint32
}

// vtbl reads the i-th method pointer from a COM object's vtable (obj → *vtable → methods[i]).
// Uses unsafe.Slice over the native vtable (no uintptr→pointer arithmetic - COM objects are native
// allocations the Go GC never moves).
func vtbl(obj unsafe.Pointer, i int) uintptr {
	vtable := *(*unsafe.Pointer)(obj)
	return unsafe.Slice((*uintptr)(vtable), i+1)[i]
}

// enumAdapters returns the machine's DXGI adapters (real hardware only; the software Basic Render
// Driver is skipped). Empty (not error) when DXGI has no hardware adapters.
func enumAdapters() ([]AdapterInfo, error) {
	var factory unsafe.Pointer
	if r, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); r != 0 || factory == nil {
		return nil, fmt.Errorf("CreateDXGIFactory1: 0x%x", r)
	}
	defer func() { _, _, _ = syscall.SyscallN(vtbl(factory, 2 /*Release*/), uintptr(factory)) }()

	const enumAdapters1 = 12 // IDXGIFactory1::EnumAdapters1 vtable index
	const getDesc1 = 10      // IDXGIAdapter1::GetDesc1 vtable index
	var out []AdapterInfo
	// Cap the walk: the loop's only other exit is an HRESULT, and a driver that never returns
	// NOT_FOUND must not spin forever.
	for i := 0; i < 64; i++ {
		var adapter unsafe.Pointer
		r64, _, _ := syscall.SyscallN(vtbl(factory, enumAdapters1), uintptr(factory), uintptr(i), uintptr(unsafe.Pointer(&adapter)))
		// HRESULTs are 32-bit and the Win64 ABI leaves RAX's upper half undefined - comparing the
		// full 64-bit return against 0x887A0002 can miss NOT_FOUND and turn the end of the walk
		// into an "error".
		r := uint32(r64)
		if r == uint32(dxgiErrorNotFound) || adapter == nil {
			break
		}
		if r != 0 {
			// SKIP this index, never abandon the walk: aborting here truncated the list at the
			// first odd adapter and every GPU after it silently disappeared.
			continue
		}
		var desc dxgiAdapterDesc1
		dr, _, _ := syscall.SyscallN(vtbl(adapter, getDesc1), uintptr(adapter), uintptr(unsafe.Pointer(&desc)))
		_, _, _ = syscall.SyscallN(vtbl(adapter, 2 /*Release*/), uintptr(adapter))
		if uint32(dr) != 0 {
			continue
		}
		if desc.Flags&adapterFlagSoftware != 0 {
			continue // Basic Render Driver - not an encode target
		}
		out = append(out, AdapterInfo{
			LUID:        fmt.Sprintf("0x%08x_0x%08x", uint32(desc.AdapterLUID.HighPart), desc.AdapterLUID.LowPart),
			Name:        strings.TrimRight(syscall.UTF16ToString(desc.Description[:]), "\x00"),
			VRAMTotalMB: float64(desc.DedicatedVideoMemory) / (1024 * 1024),
		})
	}
	return out, nil
}

// adapterNames returns LUID → description for the current adapters (best effort: empty on error).
func adapterNames() map[string]string {
	m := map[string]string{}
	ads, err := enumAdapters()
	if err != nil {
		return m
	}
	for _, a := range ads {
		m[a.LUID] = a.Name
	}
	return m
}
