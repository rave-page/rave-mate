//go:build windows

package encoderscan

import (
	"fmt"
	"syscall"
	"unsafe"
)

// vram_windows.go reads per-adapter FREE video memory via IDXGIAdapter3::QueryVideoMemoryInfo -
// the only vendor-neutral, dependency-free source of live VRAM pressure (pure syscall, same style
// as dxgi_windows.go; NVML/ADL would add a vendor dep and a driver-version surface).
//
// Free = Budget - CurrentUsage on the LOCAL segment. Budget is what the OS video-memory manager is
// currently willing to give THIS process, so it already accounts for what OBS/Parsec/VRChat hold -
// which is exactly the headroom the encode planner needs. It is not "total VRAM minus used".
//
// Encode SESSION counts have no such API (NVENC sessions are NVML-only, AMF/QSV likewise vendor
// SDKs), so Device.Sessions stays -1 = unknown and the planner skips that ceiling.

var iidIDXGIAdapter3 = guid{0x645967a4, 0x1392, 0x4310, [8]byte{0xa7, 0x98, 0x80, 0x53, 0xce, 0x3e, 0x93, 0xfd}}

// dxgiQueryVideoMemoryInfo mirrors DXGI_QUERY_VIDEO_MEMORY_INFO (4 × UINT64).
type dxgiQueryVideoMemoryInfo struct {
	Budget                  uint64
	CurrentUsage            uint64
	AvailableForReservation uint64
	CurrentReservation      uint64
}

// adapterVRAMFree returns adapter LUID key → free (budgeted) VRAM in MB. Best effort: adapters whose
// query fails are omitted (planner then treats their VRAM ceiling as unknown). Empty on any DXGI error.
func adapterVRAMFree() map[string]float64 {
	out := map[string]float64{}
	var factory unsafe.Pointer
	if r, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); r != 0 || factory == nil {
		return out
	}
	defer func() { _, _, _ = syscall.SyscallN(vtbl(factory, 2 /*Release*/), uintptr(factory)) }()

	const (
		enumAdapters1        = 12 // IDXGIFactory1::EnumAdapters1
		getDesc1             = 10 // IDXGIAdapter1::GetDesc1
		queryVideoMemoryInfo = 14 // IDXGIAdapter3::QueryVideoMemoryInfo
		segmentLocal         = 0  // DXGI_MEMORY_SEGMENT_GROUP_LOCAL
	)
	for i := 0; ; i++ {
		var adapter unsafe.Pointer
		r, _, _ := syscall.SyscallN(vtbl(factory, enumAdapters1), uintptr(factory), uintptr(i), uintptr(unsafe.Pointer(&adapter)))
		if r == dxgiErrorNotFound || adapter == nil || r != 0 {
			break
		}
		var desc dxgiAdapterDesc1
		dr, _, _ := syscall.SyscallN(vtbl(adapter, getDesc1), uintptr(adapter), uintptr(unsafe.Pointer(&desc)))
		var a3 unsafe.Pointer
		qr, _, _ := syscall.SyscallN(vtbl(adapter, 0 /*QueryInterface*/), uintptr(adapter),
			uintptr(unsafe.Pointer(&iidIDXGIAdapter3)), uintptr(unsafe.Pointer(&a3)))
		_, _, _ = syscall.SyscallN(vtbl(adapter, 2 /*Release*/), uintptr(adapter))
		if dr != 0 || desc.Flags&adapterFlagSoftware != 0 || qr != 0 || a3 == nil {
			if a3 != nil {
				_, _, _ = syscall.SyscallN(vtbl(a3, 2), uintptr(a3))
			}
			continue // pre-Win10 DXGI (no IDXGIAdapter3) or software adapter → VRAM stays unknown
		}
		var mem dxgiQueryVideoMemoryInfo
		mr, _, _ := syscall.SyscallN(vtbl(a3, queryVideoMemoryInfo), uintptr(a3),
			0 /*NodeIndex*/, segmentLocal, uintptr(unsafe.Pointer(&mem)))
		_, _, _ = syscall.SyscallN(vtbl(a3, 2 /*Release*/), uintptr(a3))
		if mr != 0 || mem.Budget == 0 {
			continue
		}
		free := float64(0)
		if mem.Budget > mem.CurrentUsage {
			free = float64(mem.Budget-mem.CurrentUsage) / (1024 * 1024)
		}
		luid := fmt.Sprintf("0x%08x_0x%08x", uint32(desc.AdapterLUID.HighPart), desc.AdapterLUID.LowPart)
		out[luid] = free
	}
	return out
}
