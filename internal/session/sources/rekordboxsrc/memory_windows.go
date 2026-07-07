//go:build windows

package rekordboxsrc

import (
	"context"
	"encoding/binary"
	"math"
	"syscall"
	"time"
	"unsafe"

	"rave.page/mate/internal/session"
)

// ── Per-version memory offsets ───────────────────────────────────────────────
//
// Process-memory now-playing reads rekordbox.exe's address space at fixed, build-specific
// pointer chains - the technique from grufkork/rkbx_link (https://github.com/grufkork/rkbx_link,
// see its offsets.toml). Offsets MUST be seeded from a REAL rekordbox build and WILL break on
// every rekordbox update (the layout shifts). They are OPERATOR-MAINTAINED DATA, not code.
//
// The entries below are PLACEHOLDERS keyed by real version strings with seeded=false and zero
// chains - they will NOT resolve and the backend stays disabled (logged) until a maintainer
// fills in real values from a matching build. We deliberately do NOT guess offsets and present
// them as working. The PLUMBING (find process → open → read chain → emit per deck) is complete
// and correct; only the numeric offsets need seeding.
//
// A ptrChain is module-base-relative: read an 8-byte pointer at base+chain.base, then for each
// hop read an 8-byte pointer at (ptr+hop); the field lives at ptr+final (no final deref). A
// zero-base chain is "not provided". String fields hold a pointer to the char buffer at their
// field address (one extra deref in readString).
type ptrChain struct {
	base  uint64
	hops  []uint64
	final uint64
}

func (c ptrChain) set() bool { return c.base != 0 }

type deckOffsets struct {
	bpm    ptrChain // float32 (effective/current BPM)
	play   ptrChain // int32 (>0 ⇒ playing)
	title  ptrChain // pointer → string buffer
	artist ptrChain // pointer → string buffer
}

type rbOffsets struct {
	seeded     bool     // false ⇒ placeholder, do not use
	utf16      bool     // string buffers are UTF-16 (else UTF-8)
	masterDeck ptrChain // int32 index 0..3 of the deck on master (optional)
	decks      [4]deckOffsets
}

// offsets maps a "major.minor.build" rekordbox version → its memory layout. SEED REAL VALUES
// PER BUILD (from rkbx_link's offsets.toml). The examples below are intentionally non-functional.
var offsets = map[string]rbOffsets{
	"6.7.7": {seeded: false, utf16: true}, // placeholder - needs real offsets from a 6.7.7 build
	"7.0.5": {seeded: false, utf16: true}, // placeholder - needs real offsets from a 7.0.5 build
}

const memPollInterval = 200 * time.Millisecond

// runMemory attaches to rekordbox.exe and emits per-deck observations every ~200ms. Never
// fatal: a missing process, an unsupported/unseeded version, or any ReadProcessMemory failure
// is logged once (state-change gated) and the loop backs off + retries.
func (s *Source) runMemory(ctx context.Context, emit func(session.Observation)) {
	s.log.Info(logSource, "rekordbox memory read started (real-time, per-version offsets)", nil)
	var (
		gate     onceLog
		rd       *memReader
		lastKey  [4]string // title|artist per deck (Loaded boundary)
		verState onceLog
	)
	tick := time.NewTicker(memPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if rd != nil {
				rd.close()
			}
			return
		case <-tick.C:
		}

		if rd == nil {
			r, err := attach(&verState, s)
			if err != nil {
				if gate.changed(err.Error()) {
					s.log.Warn(logSource, "memory attach: "+err.Error(), nil)
				}
				continue
			}
			gate.changed("") // attached
			rd = r
		}
		if !rd.off.seeded {
			// Version known but offsets unseeded: idle until the process restarts.
			if rd.dead() {
				rd.close()
				rd = nil
			}
			continue
		}

		if rd.dead() {
			s.log.Warn(logSource, "rekordbox process gone - will re-attach", nil)
			rd.close()
			rd = nil
			continue
		}

		master := -1
		if rd.off.masterDeck.set() {
			if v, ok := rd.readI32(rd.off.masterDeck); ok {
				master = int(v)
			}
		}
		for i := 0; i < 4; i++ {
			d := rd.off.decks[i]
			fields := map[string]any{}
			if d.bpm.set() {
				if f, ok := rd.readF32(d.bpm); ok && f > 0 {
					fields[session.FieldBPM] = float64(f)
				}
			}
			if d.play.set() {
				if v, ok := rd.readI32(d.play); ok {
					fields[session.FieldIsPlaying] = v > 0
				}
			} else if master == i {
				fields[session.FieldIsPlaying] = true
			}
			title, artist := "", ""
			if d.title.set() {
				title = rd.readString(d.title)
			}
			if d.artist.set() {
				artist = rd.readString(d.artist)
			}
			if title != "" {
				fields[session.FieldTitle] = title
			}
			if artist != "" {
				fields[session.FieldArtist] = artist
			}
			if len(fields) == 0 {
				continue
			}
			key := title + "\x00" + artist
			loaded := key != lastKey[i] && (title != "" || artist != "")
			if loaded {
				lastKey[i] = key
			}
			emit(session.Observation{
				Source:     session.SourceRekordboxMem,
				Scope:      session.Scope{Kind: session.ScopeDeck, ID: string(rune('A' + i))},
				Fields:     fields,
				Confidence: confMem,
				Loaded:     loaded,
			})
		}
	}
}

// ── process attach + memory reads (raw kernel32/version.dll syscalls, no new dep) ────────────

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modVersion  = syscall.NewLazyDLL("version.dll")

	procCreateToolhelp32Snapshot = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = modKernel32.NewProc("Process32FirstW")
	procProcess32NextW           = modKernel32.NewProc("Process32NextW")
	procModule32FirstW           = modKernel32.NewProc("Module32FirstW")
	procModule32NextW            = modKernel32.NewProc("Module32NextW")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procReadProcessMemory        = modKernel32.NewProc("ReadProcessMemory")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")

	procGetFileVersionInfoSizeW = modVersion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = modVersion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = modVersion.NewProc("VerQueryValueW")
)

const (
	th32csSnapProcess = 0x00000002
	th32csSnapModule  = 0x00000008
	th32csSnapMod32   = 0x00000010
	maxPath           = 260
	maxModuleName32   = 255

	processQueryInfo = 0x0400
	processVMRead    = 0x0010

	procName = "rekordbox.exe"
)

type processEntry32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [maxPath]uint16
}

type moduleEntry32 struct {
	dwSize        uint32
	th32ModuleID  uint32
	th32ProcessID uint32
	glblcntUsage  uint32
	proccntUsage  uint32
	modBaseAddr   uintptr
	modBaseSize   uint32
	hModule       uintptr
	szModule      [maxModuleName32 + 1]uint16
	szExePath     [maxPath]uint16
}

type memReader struct {
	handle uintptr
	base   uint64
	off    rbOffsets
}

func (r *memReader) close() {
	if r != nil && r.handle != 0 {
		_, _, _ = procCloseHandle.Call(r.handle)
		r.handle = 0
	}
}

// dead reports whether the target process appears gone (a base-pointer read fails).
func (r *memReader) dead() bool {
	buf := make([]byte, 8)
	var n uintptr
	ok, _, _ := procReadProcessMemory.Call(r.handle, uintptr(r.base), uintptr(unsafe.Pointer(&buf[0])), 8, uintptr(unsafe.Pointer(&n)))
	return ok == 0 || n != 8
}

// attach finds rekordbox.exe, opens it, locates the module base + version, and selects the
// offset table. Returns a reader even for an unseeded version (off.seeded=false) so the caller
// can log the disabled state once; a not-running process is an ordinary error.
func attach(verGate *onceLog, s *Source) (*memReader, error) {
	pid, ok := findProcess(procName)
	if !ok {
		return nil, errNotRunning
	}
	base, exePath, ok := moduleBase(pid, procName)
	if !ok {
		return nil, errNoModule
	}
	h, _, _ := procOpenProcess.Call(processQueryInfo|processVMRead, 0, uintptr(pid))
	if h == 0 {
		return nil, errOpenProcess
	}
	ver := fileVersion(exePath)
	off, known := offsets[ver]
	if !known {
		if verGate.changed(ver) {
			s.log.Warn(logSource, "unsupported rekordbox version "+verShow(ver)+" - memory read disabled", nil)
		}
		return &memReader{handle: h, base: base, off: rbOffsets{seeded: false}}, nil
	}
	if !off.seeded {
		if verGate.changed(ver) {
			s.log.Warn(logSource, "rekordbox "+ver+" offsets not seeded (placeholder) - memory read disabled; seed from a real build (rkbx_link)", nil)
		}
	} else if verGate.changed(ver) {
		s.log.Info(logSource, "rekordbox "+ver+" attached for memory read", map[string]any{"base": base})
	}
	return &memReader{handle: h, base: base, off: off}, nil
}

func verShow(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}

// findProcess returns the pid of the first process named name (case-insensitive).
func findProcess(name string) (uint32, bool) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return 0, false
	}
	defer func() { _, _, _ = procCloseHandle.Call(snap) }()
	var pe processEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if r, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
		return 0, false
	}
	for {
		if eqFold(syscall.UTF16ToString(pe.szExeFile[:]), name) {
			return pe.th32ProcessID, true
		}
		if r, _, _ := procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
			return 0, false
		}
	}
}

// moduleBase returns the load base + exe path of module name within pid.
func moduleBase(pid uint32, name string) (uint64, string, bool) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapModule|th32csSnapMod32, uintptr(pid))
	if snap == 0 || snap == ^uintptr(0) {
		return 0, "", false
	}
	defer func() { _, _, _ = procCloseHandle.Call(snap) }()
	var me moduleEntry32
	me.dwSize = uint32(unsafe.Sizeof(me))
	if r, _, _ := procModule32FirstW.Call(snap, uintptr(unsafe.Pointer(&me))); r == 0 {
		return 0, "", false
	}
	for {
		if eqFold(syscall.UTF16ToString(me.szModule[:]), name) {
			return uint64(me.modBaseAddr), syscall.UTF16ToString(me.szExePath[:]), true
		}
		if r, _, _ := procModule32NextW.Call(snap, uintptr(unsafe.Pointer(&me))); r == 0 {
			return 0, "", false
		}
	}
}

// fileVersion reads "major.minor.build" from a PE's version resource ("" on failure). exePath
// comes from the module snapshot (MODULEENTRY32W.szExePath).
func fileVersion(exePath string) string {
	if exePath == "" {
		return ""
	}
	p, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return ""
	}
	var handle uint32
	size, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&handle)))
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if r, _, _ := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return ""
	}
	sub, _ := syscall.UTF16PtrFromString(`\`)
	var fixed uintptr
	var flen uint32
	if r, _, _ := procVerQueryValueW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)), uintptr(unsafe.Pointer(&fixed)), uintptr(unsafe.Pointer(&flen))); r == 0 || fixed == 0 {
		return ""
	}
	// fixed points inside buf (our own memory); index buf by offset rather than deref the raw
	// uintptr (vet-clean, bounds-checked). VS_FIXEDFILEINFO: dwFileVersionMS@8, dwFileVersionLS@12.
	bufAddr := uintptr(unsafe.Pointer(&buf[0]))
	if fixed < bufAddr || fixed+16 > bufAddr+uintptr(len(buf)) {
		return ""
	}
	off := fixed - bufAddr
	ms := binary.LittleEndian.Uint32(buf[off+8:])
	ls := binary.LittleEndian.Uint32(buf[off+12:])
	major := ms >> 16
	minor := ms & 0xffff
	build := ls >> 16
	return itoa(major) + "." + itoa(minor) + "." + itoa(build)
}

// ── chain resolution + typed reads ───────────────────────────────────────────

func (r *memReader) resolve(c ptrChain) (uint64, bool) {
	p, ok := r.readPtr(r.base + c.base)
	if !ok {
		return 0, false
	}
	for _, h := range c.hops {
		p, ok = r.readPtr(p + h)
		if !ok || p == 0 {
			return 0, false
		}
	}
	return p + c.final, true
}

func (r *memReader) readAt(addr uint64, n int) ([]byte, bool) {
	if addr == 0 || n <= 0 {
		return nil, false
	}
	buf := make([]byte, n)
	var got uintptr
	ok, _, _ := procReadProcessMemory.Call(r.handle, uintptr(addr), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&got)))
	if ok == 0 || int(got) != n {
		return nil, false
	}
	return buf, true
}

func (r *memReader) readPtr(addr uint64) (uint64, bool) {
	b, ok := r.readAt(addr, 8)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b), true
}

func (r *memReader) readF32(c ptrChain) (float32, bool) {
	addr, ok := r.resolve(c)
	if !ok {
		return 0, false
	}
	b, ok := r.readAt(addr, 4)
	if !ok {
		return 0, false
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(b)), true
}

func (r *memReader) readI32(c ptrChain) (int32, bool) {
	addr, ok := r.resolve(c)
	if !ok {
		return 0, false
	}
	b, ok := r.readAt(addr, 4)
	if !ok {
		return 0, false
	}
	return int32(binary.LittleEndian.Uint32(b)), true
}

// readString resolves the chain to a field holding a pointer to the char buffer, then reads up
// to 512 bytes and decodes (UTF-16 or UTF-8) to the first NUL. "" on any failure.
func (r *memReader) readString(c ptrChain) string {
	addr, ok := r.resolve(c)
	if !ok {
		return ""
	}
	buf, ok := r.readPtr(addr)
	if !ok || buf == 0 {
		return ""
	}
	raw, ok := r.readAt(buf, 512)
	if !ok {
		return ""
	}
	if r.off.utf16 {
		u16 := make([]uint16, 0, 256)
		for i := 0; i+1 < len(raw); i += 2 {
			c := binary.LittleEndian.Uint16(raw[i : i+2])
			if c == 0 {
				break
			}
			u16 = append(u16, c)
		}
		return syscall.UTF16ToString(u16)
	}
	for i, b := range raw {
		if b == 0 {
			return string(raw[:i])
		}
	}
	return string(raw)
}

// ── tiny helpers (avoid extra imports) ───────────────────────────────────────

func eqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

type memErr string

func (e memErr) Error() string { return string(e) }

const (
	errNotRunning  = memErr("rekordbox.exe not running")
	errNoModule    = memErr("rekordbox.exe module base not found")
	errOpenProcess = memErr("OpenProcess failed (run rave-mate as the same/elevated user)")
)
