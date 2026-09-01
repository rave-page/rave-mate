//go:build windows

package gpumem

import (
	"fmt"
	"sort"
	"syscall"
	"unsafe"

	"rave.page/mate/internal/sysactivity"
)

// D3DKMT lives in gdi32.dll (stdlib syscall, no cgo/COM). Same pattern as internal/midi
// (winmm) + internal/sysactivity (user32/kernel32).
var (
	gdi32 = syscall.NewLazyDLL("gdi32.dll")

	procEnumAdapters2    = gdi32.NewProc("D3DKMTEnumAdapters2")
	procQueryStatistics  = gdi32.NewProc("D3DKMTQueryStatistics")
	procQueryAdapterInfo = gdi32.NewProc("D3DKMTQueryAdapterInfo")
	procCloseAdapter     = gdi32.NewProc("D3DKMTCloseAdapter")

	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess = kernel32.NewProc("OpenProcess")
	procCloseHandle = kernel32.NewProc("CloseHandle")
)

const (
	// D3DKMT_QUERYSTATISTICS_TYPE
	qsAdapter        = 0
	qsSegment        = 3
	qsProcessSegment = 4
	// KMTQUERYADAPTERINFOTYPE
	kmtAdapterAddress  = 6
	kmtRegistryInfo    = 8
	processQueryInfo   = 0x0400 // PROCESS_QUERY_INFORMATION (no VM_READ needed for D3DKMT)
	maxEnumAdapters    = 64     // D3DKMTEnumAdapters2 buffer cap; a box enumerates ~30-40 handles
	maxProcSweep       = 2048   // cap the process sweep; drop-past-cap (a real box has <1k procs)
	segInfoResidentOff = 16     // BytesResident offset in SEGMENT_INFORMATION
	segInfoApertureOff = 40     // Aperture flag offset in SEGMENT_INFORMATION
)

type luid struct {
	Low  uint32
	High int32
}

// adapterInfo mirrors D3DKMT_ADAPTERINFO (x64: 20 bytes).
type adapterInfo struct {
	hAdapter     uint32
	adapterLuid  luid
	numOfSources uint32
	bPrecise     uint32
}

// enumAdapters2 mirrors D3DKMT_ENUMADAPTERS2.
type enumAdapters2 struct {
	numAdapters uint32
	_           uint32
	pAdapters   uintptr
}

// queryStatistics mirrors D3DKMT_QUERYSTATISTICS. The result union is a fixed 776-byte blob
// read at known offsets; segmentID is the input query element that follows it. The x64 SDK
// header pins the total via C_ASSERT(sizeof == 0x328) - asserted in the test.
type queryStatistics struct {
	typ         uint32
	adapterLuid luid
	_           uint32
	hProcess    uintptr
	result      [776]byte
	segmentID   uint32
	_           uint32
}

// queryAdapterInfo mirrors D3DKMT_QUERYADAPTERINFO.
type queryAdapterInfo struct {
	hAdapter uint32
	typ      uint32
	pData    uintptr
	dataSize uint32
	_        uint32
}

// adapterRegistryInfo mirrors D3DKMT_ADAPTERREGISTRYINFO (4 * WCHAR[MAX_PATH]).
type adapterRegistryInfo struct {
	AdapterString [260]uint16
	BiosString    [260]uint16
	DacType       [260]uint16
	ChipType      [260]uint16
}

type closeAdapterArg struct{ hAdapter uint32 }

// realAdapter is a deduped physical adapter with dedicated-VRAM totals + per-segment aperture map.
type realAdapter struct {
	name     string
	luid     luid
	luidStr  string
	budget   uint64 // bytes
	used     uint64 // bytes (resident)
	aperture []bool // segment index -> is shared/system (excluded from totals)
}

type winSampler struct{}

func newSampler() Sampler { return winSampler{} }

func (winSampler) Sample() ([]AdapterUsage, error) {
	reals := collectAdapters()
	out := make([]AdapterUsage, 0, len(reals))
	for _, r := range reals {
		out = append(out, AdapterUsage{
			Name: r.name, LUID: r.luidStr,
			BudgetMB: r.budget >> 20, UsedMB: r.used >> 20, FreeMB: freeMB(r.budget, r.used),
		})
	}
	return out, nil
}

func (winSampler) SampleProcesses(topN int) ([]AdapterProcs, error) {
	reals := collectAdapters()
	if len(reals) == 0 {
		return nil, nil
	}
	per := make([][]ProcUsage, len(reals))
	procs, _ := sysactivity.ListProcesses() // ok=false -> empty sweep (fail open)
	for i, p := range procs {
		if i >= maxProcSweep {
			break // bounded: a pathological process table can't stall the daemon
		}
		if p.PID == 0 {
			continue // System Idle
		}
		h, _, _ := procOpenProcess.Call(processQueryInfo, 0, uintptr(p.PID))
		if h == 0 {
			continue // protected/elevated process - skipped silently (same-user is enough)
		}
		for ai, r := range reals {
			var total uint64
			for s, ap := range r.aperture {
				if ap {
					continue
				}
				if b, ok := procSegmentCommitted(r.luid, h, uint32(s)); ok {
					total += b
				}
			}
			if mb := total >> 20; mb > 0 {
				per[ai] = append(per[ai], ProcUsage{Name: p.Name, PID: p.PID, MB: mb})
			}
		}
		_, _, _ = procCloseHandle.Call(h)
	}
	out := make([]AdapterProcs, 0, len(reals))
	for ai, r := range reals {
		list := per[ai]
		sort.Slice(list, func(a, b int) bool { return list[a].MB > list[b].MB })
		if topN > 0 && len(list) > topN {
			list = list[:topN]
		}
		out = append(out, AdapterProcs{Adapter: r.name, Procs: list})
	}
	return out, nil
}

// collectAdapters enumerates every D3DKMT adapter, keeps the ones with real dedicated VRAM,
// and dedupes physical GPUs that WDDM enumerates under several LUIDs. The dedupe key is the
// PCI address (ADAPTERADDRESS), falling back to the driver registry name; an adapter that
// resolves neither is a phantom alias of a real GPU and is dropped (observed: the RTX 3060
// appears twice reporting identical memory - once named with valid pci 9:0.0, once as an
// unnamed alias whose registry name is empty and whose ADAPTERADDRESS returns out-of-range
// garbage, both rejected by identity()).
func collectAdapters() []realAdapter {
	var reals []realAdapter
	seen := map[string]bool{}
	for _, ad := range enumAdapters() {
		budget, used, aperture := adapterTotals(ad.adapterLuid)
		key, name := identity(ad.hAdapter)
		closeAdapter(ad.hAdapter)
		if budget == 0 {
			continue // software renderer / iGPU / aperture-only - not a real dedicated GPU
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if name == "" {
			name = "GPU " + key
		}
		reals = append(reals, realAdapter{
			name: name, luid: ad.adapterLuid,
			luidStr:  fmt.Sprintf("%d:%d", ad.adapterLuid.High, ad.adapterLuid.Low),
			budget:   budget,
			used:     used,
			aperture: aperture,
		})
	}
	return reals
}

// enumAdapters returns the D3DKMT adapter handles (each must be closed by the caller). Null
// probe for the count, then one fill into a fixed buffer (capped at maxEnumAdapters).
func enumAdapters() []adapterInfo {
	var ea enumAdapters2
	if st, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&ea))); st != 0 || ea.numAdapters == 0 {
		return nil
	}
	n := ea.numAdapters
	if n > maxEnumAdapters {
		n = maxEnumAdapters
	}
	buf := make([]adapterInfo, n)
	ea.numAdapters = n
	ea.pAdapters = uintptr(unsafe.Pointer(&buf[0]))
	if st, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&ea))); st != 0 {
		return nil
	}
	if ea.numAdapters < n {
		buf = buf[:ea.numAdapters]
	}
	return buf
}

// adapterTotals sums dedicated (non-aperture) segment CommitLimit + BytesResident and returns
// the per-segment aperture map. budget==0 => not a real dedicated adapter.
func adapterTotals(l luid) (budget, used uint64, aperture []bool) {
	nb, ok := adapterSegmentCount(l)
	if !ok || nb == 0 {
		return 0, 0, nil
	}
	aperture = make([]bool, nb)
	for s := uint32(0); s < nb; s++ {
		limit, resident, ap, ok := segment(l, s)
		if !ok {
			continue
		}
		aperture[s] = ap
		if !ap {
			budget += limit
			used += resident
		}
	}
	return budget, used, aperture
}

func adapterSegmentCount(l luid) (uint32, bool) {
	qs := queryStatistics{typ: qsAdapter, adapterLuid: l}
	if st, _, _ := procQueryStatistics.Call(uintptr(unsafe.Pointer(&qs))); st != 0 {
		return 0, false
	}
	return le32(qs.result[0:]), true // ADAPTER_INFORMATION.NbSegments
}

// segment reads one SEGMENT_INFORMATION: CommitLimit (segment size), BytesResident, and the
// aperture flag (shared system memory vs dedicated VRAM).
func segment(l luid, id uint32) (commitLimit, resident uint64, aperture, ok bool) {
	qs := queryStatistics{typ: qsSegment, adapterLuid: l, segmentID: id}
	if st, _, _ := procQueryStatistics.Call(uintptr(unsafe.Pointer(&qs))); st != 0 {
		return 0, 0, false, false
	}
	return le64(qs.result[0:]), le64(qs.result[segInfoResidentOff:]), le32(qs.result[segInfoApertureOff:]) != 0, true
}

// procSegmentCommitted reads PROCESS_SEGMENT_INFORMATION.BytesCommitted (this process's
// dedicated-VRAM commitment on one segment).
func procSegmentCommitted(l luid, h uintptr, id uint32) (uint64, bool) {
	qs := queryStatistics{typ: qsProcessSegment, adapterLuid: l, hProcess: h, segmentID: id}
	if st, _, _ := procQueryStatistics.Call(uintptr(unsafe.Pointer(&qs))); st != 0 {
		return 0, false
	}
	return le64(qs.result[0:]), true
}

// identity resolves an adapter's dedupe key (pci address, else registry name) + display name.
func identity(h uint32) (key, name string) {
	name = adapterName(h)
	if bus, dev, fn, ok := adapterAddr(h); ok {
		return fmt.Sprintf("pci %d:%d.%d", bus, dev, fn), name
	}
	if name != "" {
		return "name " + name, name
	}
	return "", name
}

func adapterName(h uint32) string {
	var ri adapterRegistryInfo
	qai := queryAdapterInfo{hAdapter: h, typ: kmtRegistryInfo, pData: uintptr(unsafe.Pointer(&ri)), dataSize: uint32(unsafe.Sizeof(ri))}
	if st, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&qai))); st != 0 {
		return ""
	}
	return syscall.UTF16ToString(ri.AdapterString[:])
}

// adapterAddr returns the PCI bus/device/function. Phantom alias handles return SUCCESS with
// all-ones garbage (bus 0xffffffff, dev/fn 0xffff), so validate against PCI ranges (device is
// 5-bit <=31, function 3-bit <=7); an out-of-range triplet is treated as unresolved.
func adapterAddr(h uint32) (bus, dev, fn uint32, ok bool) {
	var a [3]uint32 // D3DKMT_ADAPTERADDRESS{BusNumber, DeviceNumber, FunctionNumber}
	qai := queryAdapterInfo{hAdapter: h, typ: kmtAdapterAddress, pData: uintptr(unsafe.Pointer(&a[0])), dataSize: uint32(unsafe.Sizeof(a))}
	if st, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&qai))); st != 0 {
		return 0, 0, 0, false
	}
	if a[1] > 31 || a[2] > 7 {
		return 0, 0, 0, false // out-of-range -> phantom alias, not a real PCI device
	}
	return a[0], a[1], a[2], true
}

func closeAdapter(h uint32) {
	ca := closeAdapterArg{hAdapter: h}
	_, _, _ = procCloseAdapter.Call(uintptr(unsafe.Pointer(&ca)))
}

func freeMB(budget, used uint64) uint64 {
	if used >= budget {
		return 0
	}
	return (budget - used) >> 20
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le64(b []byte) uint64 {
	return uint64(le32(b)) | uint64(le32(b[4:]))<<32
}
