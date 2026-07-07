//go:build windows

package encoderscan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// gpu_windows.go samples per-process GPU video-engine utilization from the Windows PDH "GPU Engine"
// performance counters - the same source Task Manager's "Video Encode"/"Video Decode" graphs use.
// Vendor-neutral (NVIDIA/AMD/Intel/iGPU alike), read-only, no GPU work. Counter instance names are
// "pid_<pid>_luid_0x..._0x..._phys_<n>_eng_<n>_engtype_<Type>"; we keep VideoEncode/VideoDecode.

var (
	pdh                    = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW      = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCntrW = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectData     = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFmtArrayW    = pdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery      = pdh.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble       = 0x00000200
	pdhMoreData        = 0x800007D2
	pdhCStatusValid    = 0x00000000
	gpuEngineCounter   = `\GPU Engine(*)\Utilization Percentage`
	pdhCollectInterval = 300 * time.Millisecond
)

// pdhFmtCounterValueItemW mirrors PDH_FMT_COUNTERVALUE_ITEM_W on amd64 (24 bytes: name ptr @0,
// CStatus @8, +4 pad, double @16).
type pdhFmtCounterValueItemW struct {
	szName  *uint16
	cStatus uint32
	_       uint32
	value   float64
}

// gpuDiag holds why the last sampleGPU produced no video-engine samples - the ground truth for
// diagnosing an "empty" result: raw PDH instance count + the engtype histogram (so a vendor that
// labels its encode engine unexpectedly is visible instead of silently dropped).
var (
	gpuDiagMu sync.Mutex
	gpuDiagS  string
)

// gpuDiagNote returns the last sampler diagnostic (engtype histogram / PDH status), "" if healthy.
func gpuDiagNote() string {
	gpuDiagMu.Lock()
	defer gpuDiagMu.Unlock()
	return gpuDiagS
}

func setGPUDiag(s string) {
	gpuDiagMu.Lock()
	gpuDiagS = s
	gpuDiagMu.Unlock()
}

// sampleGPU returns per-process VideoEncode/VideoDecode utilization across all GPU adapters.
func sampleGPU() ([]GPUSample, error) {
	var query uintptr
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&query))); r != 0 {
		return nil, fmt.Errorf("PdhOpenQuery: 0x%x", r)
	}
	defer func() { _, _, _ = procPdhCloseQuery.Call(query) }()

	path, _ := syscall.UTF16PtrFromString(gpuEngineCounter)
	var counter uintptr
	if r, _, _ := procPdhAddEnglishCntrW.Call(query, uintptr(unsafe.Pointer(path)), 0,
		uintptr(unsafe.Pointer(&counter))); r != 0 {
		return nil, fmt.Errorf("PdhAddEnglishCounter: 0x%x", r)
	}
	// Utilization is a rate → two collects with a gap between them.
	if r, _, _ := procPdhCollectData.Call(query); r != 0 {
		return nil, fmt.Errorf("PdhCollectQueryData(1): 0x%x", r)
	}
	time.Sleep(pdhCollectInterval)
	if r, _, _ := procPdhCollectData.Call(query); r != 0 {
		return nil, fmt.Errorf("PdhCollectQueryData(2): 0x%x", r)
	}

	// First call sizes the buffer (returns PDH_MORE_DATA); second fills it.
	var bufSize, itemCount uint32
	r, _, _ := procPdhGetFmtArrayW.Call(counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&itemCount)), 0)
	if r != pdhMoreData {
		if r == 0 || itemCount == 0 {
			setGPUDiag(fmt.Sprintf("PDH \\GPU Engine has 0 instances (sizing status=0x%x) - object empty on this box", r))
			return nil, nil // no GPU-engine instances active
		}
		return nil, fmt.Errorf("PdhGetFormattedCounterArray(size): 0x%x", r)
	}
	if bufSize == 0 || itemCount == 0 {
		setGPUDiag(fmt.Sprintf("PDH \\GPU Engine sizing gave bufSize=%d itemCount=%d", bufSize, itemCount))
		return nil, nil
	}
	buf := make([]byte, bufSize)
	if r, _, _ := procPdhGetFmtArrayW.Call(counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArray(fill): 0x%x", r)
	}

	items := unsafe.Slice((*pdhFmtCounterValueItemW)(unsafe.Pointer(&buf[0])), int(itemCount))
	// Aggregate encode/decode per (pid, adapter luid).
	type key struct {
		pid     int
		adapter string
	}
	agg := map[key]*GPUSample{}
	engHist := map[string]int{} // engtype → count, ALL parsed instances (pre-filter) - diagnostic
	var rawValid, parsed, matched int
	for i := range items {
		if items[i].cStatus != pdhCStatusValid || items[i].szName == nil {
			continue
		}
		rawValid++
		name := utf16PtrToString(items[i].szName)
		pid, luid, eng, ok := parseGPUEngineInstance(name)
		if !ok {
			continue
		}
		parsed++
		engHist[eng]++
		// "encode"/"decode" substring (not just "videoencode") tolerates vendor label variants like
		// "Video_Encode" / "3D_VideoEncode"; no non-video GPU engtype contains those tokens.
		enc := strings.Contains(eng, "encode")
		dec := strings.Contains(eng, "decode")
		if !enc && !dec {
			continue
		}
		matched++
		k := key{pid: pid, adapter: luid}
		s := agg[k]
		if s == nil {
			s = &GPUSample{PID: pid, Adapter: luid}
			agg[k] = s
		}
		if enc {
			s.EncodePct += items[i].value
		} else {
			s.DecodePct += items[i].value
		}
	}
	if matched == 0 {
		// Instances exist but none are video encode/decode - surface the engtype histogram so we
		// SEE what this GPU calls its engines (the AMD-labels-differently case).
		setGPUDiag(fmt.Sprintf("PDH \\GPU Engine: %d instances, %d parsed, 0 video enc/dec - engtypes seen: %s",
			rawValid, parsed, engHistString(engHist)))
	} else {
		setGPUDiag("")
	}
	out := make([]GPUSample, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	return out, nil
}

// engHistString renders an engtype→count histogram, most frequent first, for the diag note.
func engHistString(h map[string]int) string {
	type kv struct {
		eng string
		n   int
	}
	kvs := make([]kv, 0, len(h))
	for e, n := range h {
		kvs = append(kvs, kv{e, n})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].n != kvs[j].n {
			return kvs[i].n > kvs[j].n
		}
		return kvs[i].eng < kvs[j].eng
	})
	var b strings.Builder
	for i, e := range kvs {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s:%d", e.eng, e.n)
	}
	if b.Len() == 0 {
		return "(none)"
	}
	return b.String()
}

// parseGPUEngineInstance pulls (pid, luid-key, engtype) from a GPU-Engine counter instance name
// like "pid_1234_luid_0x00000000_0x0001C3D4_phys_0_eng_2_engtype_VideoEncode". engtype is lowered.
func parseGPUEngineInstance(name string) (pid int, luid, engtype string, ok bool) {
	s := strings.ToLower(name)
	toks := strings.Split(s, "_")
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "pid":
			if i+1 < len(toks) {
				pid, _ = strconv.Atoi(toks[i+1])
			}
		case "luid":
			if i+2 < len(toks) { // luid is a two-part hex key: 0xHIGH_0xLOW
				luid = toks[i+1] + "_" + toks[i+2]
			}
		case "engtype":
			if i+1 < len(toks) {
				engtype = strings.Join(toks[i+1:], "_") // e.g. "videoencode", "video_processing"
			}
		}
	}
	return pid, luid, engtype, pid != 0 && engtype != ""
}

// utf16PtrToString reads a NUL-terminated UTF-16 string from a raw pointer (PDH-owned buffer).
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var u []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return syscall.UTF16ToString(u)
}
