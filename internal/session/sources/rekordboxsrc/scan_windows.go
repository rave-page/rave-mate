//go:build windows

package rekordboxsrc

import (
	"fmt"
	"sort"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Memory pointer-scanner for SEEDING per-version offsets (see memory_windows.go). Given a known
// on-screen string (a loaded track's title/artist), it finds the string in rekordbox.exe, then
// reverse-scans the process pointer graph for stable module-base→…→field chains pointing at it -
// the rkbx_link technique, but for the string fields rkbx_link doesn't cover. Output chains are
// printed in the `base hop… final` form ready to paste into the offsets map (then VALIDATE across
// a rekordbox restart - a real chain is module-relative and survives ASLR; a fluke won't).
//
// This is an operator tool, not part of the running app. Invoke: rave-mate rbxscan "<string>".

const (
	memCommit    = 0x1000
	pageGuard    = 0x100
	pageNoAccess = 0x01
)

var procVirtualQueryEx = modKernel32.NewProc("VirtualQueryEx")

// memBasicInfo mirrors MEMORY_BASIC_INFORMATION (x64).
type memBasicInfo struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

type region struct{ base, size uint64 }

// ptrEntry is one 8-aligned pointer-sized value read from the target (addr holds value val).
type ptrEntry struct{ addr, val uint64 }

// RunScan attaches to rekordbox.exe and hunts pointer chains to needle (a UTF-16/UTF-8 string).
// Prints candidate static chains. rekordbox must be running with the string visible on a deck.
func RunScan(needle string) error {
	if needle == "" {
		return fmt.Errorf("usage: rave-mate rbxscan \"<loaded track title or artist>\"")
	}
	pid, ok := findProcess(procName)
	if !ok {
		return fmt.Errorf("rekordbox.exe not running")
	}
	base, exePath, size, ok := moduleInfo(pid, procName)
	if !ok {
		return fmt.Errorf("rekordbox.exe module not found")
	}
	h, _, _ := procOpenProcess.Call(processQueryInfo|processVMRead, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("OpenProcess failed (run rave-mate elevated / same user)")
	}
	defer func() { _, _, _ = procCloseHandle.Call(h) }()
	rd := &memReader{handle: h, base: base}
	modLo, modHi := base, base+size
	fmt.Printf("rekordbox %s  module base=%#x size=%#x\n", verShow(fileVersion(exePath)), base, size)

	regions := enumRegions(h)
	fmt.Printf("scanning %d committed regions for %q …\n", len(regions), needle)

	// Phase 1: locate the string bytes (try UTF-16LE then UTF-8).
	strAddrs := findBytes(rd, regions, utf16LE(needle))
	enc := "utf16"
	if len(strAddrs) == 0 {
		strAddrs = findBytes(rd, regions, []byte(needle))
		enc = "utf8"
	}
	if len(strAddrs) == 0 {
		return fmt.Errorf("string %q not found in rekordbox memory - is it loaded on a deck right now?", needle)
	}
	fmt.Printf("found string at %d address(es) [%s]: %s\n", len(strAddrs), enc, hexList(strAddrs, 6))

	// Phase 2: build the pointer map once (8-aligned values pointing into committed space).
	fmt.Println("building pointer map (this takes a moment) …")
	pmap := buildPointerMap(rd, regions, modLo, modHi)
	fmt.Printf("pointer map: %d pointers\n", len(pmap))

	// Reverse-scan BOTH the inline copies (chain resolves straight to the bytes) AND any pointer-held
	// copies (a pointer holds the buffer, one deref above). rekordbox keeps several copies of a
	// track's strings (deck struct / browser / cache); the deck one we want has been observed INLINE.
	const maxDepth, maxOff, maxPerLevel = 7, 0x800, 4000
	var ptrFields []uint64
	for _, s := range strAddrs {
		ptrFields = append(ptrFields, pointersTo(pmap, s, 0)...)
	}
	inlineChains := scanAll(pmap, strAddrs, modLo, modHi, maxDepth, maxOff, maxPerLevel)
	ptrChains := scanAll(pmap, ptrFields, modLo, modHi, maxDepth, maxOff, maxPerLevel)
	if len(inlineChains)+len(ptrChains) == 0 {
		return fmt.Errorf("no static (module-relative) chain found within depth %d", maxDepth)
	}
	fmt.Printf("\n=== INLINE chains for %q (read direct, no final deref): %d ===\n", needle, len(inlineChains))
	for _, c := range inlineChains {
		fmt.Println("  " + c)
	}
	fmt.Printf("\n=== POINTER-HELD chains for %q (final field holds a ptr to the buffer): %d ===\n", needle, len(ptrChains))
	for _, c := range ptrChains {
		fmt.Println("  " + c)
	}
	return nil
}

// scanAll reverse-scans every target to static chains, deduped, in stable order.
func scanAll(pmap []ptrEntry, targets []uint64, modLo, modHi uint64, maxDepth, maxOff, maxPerLevel int) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		for _, c := range reverseScan(pmap, t, modLo, modHi, maxDepth, maxOff, maxPerLevel) {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// reverseScan walks holders upward from target until a holder lies inside the module (static).
// Returns chains as "BASE HOP… FINAL" hex (module-relative base), matching the offsets format.
func reverseScan(pmap []ptrEntry, target, modLo, modHi uint64, maxDepth, maxOff, maxPerLevel int) []string {
	type node struct {
		addr uint64
		offs []uint64 // collected deepest→shallowest: [final, hop_m, …]
	}
	var out []string
	frontier := []node{{addr: target}}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []node
		for _, nd := range frontier {
			for _, h := range pointersInto(pmap, nd.addr, uint64(maxOff)) {
				off := nd.addr - h.val
				offs := append(append([]uint64{}, nd.offs...), off)
				if h.addr >= modLo && h.addr < modHi { // static holder → chain complete
					out = append(out, formatChain(h.addr-modLo, offs))
					continue
				}
				next = append(next, node{addr: h.addr, offs: offs})
				if len(next) >= maxPerLevel {
					break
				}
			}
			if len(next) >= maxPerLevel {
				break
			}
		}
		frontier = next
	}
	return out
}

// formatChain renders base + collected offsets (deepest-first) as "BASE HOP… FINAL".
func formatChain(base uint64, offsDeepFirst []uint64) string {
	// offsDeepFirst = [final, hop_m, …, hop_1]; reverse → [hop_1, …, hop_m, final].
	n := len(offsDeepFirst)
	ord := make([]uint64, n)
	for i, v := range offsDeepFirst {
		ord[n-1-i] = v
	}
	s := fmt.Sprintf("%X", base)
	for _, v := range ord {
		s += fmt.Sprintf(" %X", v)
	}
	return s
}

// pointersTo returns map entries whose value == addr+delta (delta usually 0).
func pointersTo(pmap []ptrEntry, addr, delta uint64) []uint64 {
	want := addr + delta
	lo := sort.Search(len(pmap), func(i int) bool { return pmap[i].val >= want })
	var out []uint64
	for i := lo; i < len(pmap) && pmap[i].val == want; i++ {
		out = append(out, pmap[i].addr)
	}
	return out
}

// pointersInto returns holders whose value lands in [addr-maxOff, addr] (a pointer into the struct
// that contains addr). pmap is sorted by val.
func pointersInto(pmap []ptrEntry, addr, maxOff uint64) []ptrEntry {
	lo := sort.Search(len(pmap), func(i int) bool { return pmap[i].val >= addr-maxOff })
	hi := sort.Search(len(pmap), func(i int) bool { return pmap[i].val > addr })
	if lo < 0 {
		lo = 0
	}
	return pmap[lo:hi]
}

// buildPointerMap reads every committed region and records 8-aligned values that point into
// committed space, sorted by value for range queries.
func buildPointerMap(rd *memReader, regions []region, _, _ uint64) []ptrEntry {
	lo, hi := regionBounds(regions)
	var out []ptrEntry
	for _, rg := range regions {
		buf, ok := rd.readAt(rg.base, int(min64(rg.size, 512<<20)))
		if !ok {
			continue
		}
		for i := 0; i+8 <= len(buf); i += 8 {
			v := u64(buf[i:])
			if v >= lo && v < hi {
				out = append(out, ptrEntry{addr: rg.base + uint64(i), val: v})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].val < out[j].val })
	return out
}

// findBytes returns absolute addresses where pat occurs in any region.
func findBytes(rd *memReader, regions []region, pat []byte) []uint64 {
	if len(pat) == 0 {
		return nil
	}
	var out []uint64
	for _, rg := range regions {
		buf, ok := rd.readAt(rg.base, int(min64(rg.size, 512<<20)))
		if !ok {
			continue
		}
		for i := 0; ; {
			j := indexBytes(buf[i:], pat)
			if j < 0 {
				break
			}
			out = append(out, rg.base+uint64(i+j))
			i += j + 1
			if len(out) > 64 {
				return out
			}
		}
	}
	return out
}

// enumRegions lists readable committed regions of the target via VirtualQueryEx.
func enumRegions(h uintptr) []region {
	var out []region
	var addr uintptr
	var mbi memBasicInfo
	for i := 0; i < 1<<20; i++ {
		r, _, _ := procVirtualQueryEx.Call(h, addr, uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
		if r == 0 {
			break
		}
		readable := mbi.State == memCommit && mbi.Protect&pageGuard == 0 && mbi.Protect&pageNoAccess == 0 && mbi.Protect != 0
		if readable && mbi.RegionSize > 0 {
			out = append(out, region{base: uint64(mbi.BaseAddress), size: uint64(mbi.RegionSize)})
		}
		nextAddr := mbi.BaseAddress + mbi.RegionSize
		if nextAddr <= addr {
			break
		}
		addr = nextAddr
	}
	return out
}

// moduleInfo returns base, exe path, and size of module name within pid.
func moduleInfo(pid uint32, name string) (uint64, string, uint64, bool) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapModule|th32csSnapMod32, uintptr(pid))
	if snap == 0 || snap == ^uintptr(0) {
		return 0, "", 0, false
	}
	defer func() { _, _, _ = procCloseHandle.Call(snap) }()
	var me moduleEntry32
	me.dwSize = uint32(unsafe.Sizeof(me))
	if r, _, _ := procModule32FirstW.Call(snap, uintptr(unsafe.Pointer(&me))); r == 0 {
		return 0, "", 0, false
	}
	for {
		if eqFold(syscall.UTF16ToString(me.szModule[:]), name) {
			return uint64(me.modBaseAddr), syscall.UTF16ToString(me.szExePath[:]), uint64(me.modBaseSize), true
		}
		if r, _, _ := procModule32NextW.Call(snap, uintptr(unsafe.Pointer(&me))); r == 0 {
			return 0, "", 0, false
		}
	}
}

// ── tiny helpers ─────────────────────────────────────────────────────────────

func regionBounds(rs []region) (lo, hi uint64) {
	lo = ^uint64(0)
	for _, r := range rs {
		if r.base < lo {
			lo = r.base
		}
		if r.base+r.size > hi {
			hi = r.base + r.size
		}
	}
	if lo == ^uint64(0) {
		lo = 0
	}
	return
}

func u64(b []byte) uint64 {
	_ = b[7]
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, len(u)*2)
	for i, c := range u {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}

func indexBytes(hay, needle []byte) int {
	if len(needle) == 0 || len(hay) < len(needle) {
		return -1
	}
	first := needle[0]
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i] != first {
			continue
		}
		if bytesEqual(hay[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func hexList(xs []uint64, max int) string {
	s := ""
	for i, x := range xs {
		if i >= max {
			s += fmt.Sprintf(" …(+%d)", len(xs)-max)
			break
		}
		s += fmt.Sprintf(" %#x", x)
	}
	return s
}
