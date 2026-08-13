//go:build windows

package surfacepub

import (
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// D3D11 through raw COM vtable syscalls, stdlib only - the same shape internal/encoderscan already
// uses for DXGI, and one that keeps d3d11.dll out of the daemon's import table (it loads lazily, in
// the producer child, or not at all). The Zig side of this transport uses the shared declarations in
// native/zigd3d/src/d3d11.zig; this is the Go side of the SAME contract, so every slot number below
// is the one that file derives.
var (
	d3d11              = syscall.NewLazyDLL("d3d11.dll")
	procD3D11CreateDev = d3d11.NewProc("D3D11CreateDevice")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procTickCount64    = kernel32.NewProc("GetTickCount64")
)

// COM vtable slots (derivation: native/zigd3d/src/d3d11.zig).
const (
	slotQueryInterface = 0
	slotRelease        = 2

	slotDevCreateTexture2D = 5

	slotCtxMap          = 14
	slotCtxUnmap        = 15
	slotCtxCopyResource = 47
	slotCtxFlush        = 111

	slotResCreateSharedHandle = 13

	slotKMAcquireSync = 8
	slotKMReleaseSync = 9
)

var (
	iidKeyedMutex = guid{0x9d8e1289, 0xd7b3, 0x465f, [8]byte{0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d}}
	iidDXGIRes1   = guid{0x30961379, 0x4609, 0x4a41, [8]byte{0x99, 0x8e, 0x54, 0xfe, 0x56, 0x7e, 0xe0, 0xc1}}
)

// stdlib syscall exposes FILE_MAP_READ/WRITE but not the all-access mask. Both ends agree on this
// one, and the section is PAGE_READWRITE, so it is grantable.
const fileMapAllAccess = 0xF001F

// waitTimeout is WAIT_TIMEOUT as AcquireSync returns it: a SUCCESS HRESULT that nonetheless means
// "you do not own the mutex". Treating it as ownership is how a torn frame gets published.
const waitTimeout = 0x102

// acquireMs bounds the producer's wait for a free slot. Short on purpose: a compositor that is busy
// must cost this frame, not the render loop's cadence.
const acquireMs = 4

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// texDesc mirrors D3D11_TEXTURE2D_DESC (x64: 11 x UINT, SampleDesc inlined).
type texDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type mappedSubresource struct {
	pData      uintptr
	RowPitch   uint32
	DepthPitch uint32
}

const (
	driverHardware = 1
	driverWARP     = 5
	flagBGRA       = 0x20
	sdkVersion     = 7

	usageDefault    = 0
	usageDynamic    = 2
	bindSRV         = 8
	bindRTV         = 0x20
	cpuAccessWrite  = 0x10000
	mapWriteDiscard = 4

	miscSharedKeyedMutex = 0x100
	miscSharedNTHandle   = 0x800

	sharedResourceRead  = 0x80000000
	sharedResourceWrite = 0x00000001
)

// vtbl reads the i-th method pointer out of a COM object's vtable. unsafe.Slice over the native
// vtable, no uintptr→pointer arithmetic: COM objects are native allocations the Go GC never moves.
func vtbl(obj unsafe.Pointer, i int) uintptr {
	vtable := *(*unsafe.Pointer)(obj)
	return unsafe.Slice((*uintptr)(vtable), i+1)[i]
}

func vcall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(obj))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(vtbl(obj, slot), all...)
	return r
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		vcall(obj, slotRelease)
	}
}

func comQI(obj unsafe.Pointer, iid *guid) unsafe.Pointer {
	var out unsafe.Pointer
	if hr := vcall(obj, slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); int32(hr) < 0 {
		return nil
	}
	return out
}

func hrErr(what string, hr uintptr) error {
	return fmt.Errorf("surfacepub: %s hr=0x%08X", what, uint32(hr))
}

// ptrOf reinterprets an OS-returned address as a pointer. Legitimate exactly here: the address is a
// file-mapping view / mapped GPU staging buffer - never moves, never GC-managed - converted ONCE at
// the syscall boundary, with all field access derived from it via unsafe.Add.
func ptrOf(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr)) //nolint:govet // OS mapping, not a Go pointer
}

// Pub is one surface's producer endpoint. One publishing goroutine, plus concurrent Stats/Want.
type Pub struct {
	id string

	mu      sync.Mutex
	hmap    syscall.Handle
	view    unsafe.Pointer
	dev     unsafe.Pointer
	ctx     unsafe.Pointer
	gen     uint32
	w, h    int
	tex     [Slots]unsafe.Pointer
	km      [Slots]unsafe.Pointer
	share   [Slots]syscall.Handle // MUST stay open: a named kernel object lives only while a handle does
	staging unsafe.Pointer
	next    int
	seq     uint64
	closed  bool

	published atomic.Uint64
	dropped   atomic.Uint64
}

// Open creates the control block for surface id. No ring yet: the consumer has not said how big the
// picture should be, and guessing is what produces a squashed first frame.
func Open(id string) (*Pub, error) {
	if id == "" {
		return nil, fmt.Errorf("surfacepub: empty surface id")
	}
	name, err := syscall.UTF16PtrFromString(CtlName(id))
	if err != nil {
		return nil, err
	}
	// InvalidHandle = page-file backed. One 4 KiB page, fixed for the endpoint's whole life.
	h, err := syscall.CreateFileMapping(syscall.InvalidHandle, nil, syscall.PAGE_READWRITE, 0, ctlBytes, name)
	if h == 0 {
		return nil, fmt.Errorf("surfacepub: CreateFileMapping(%s): %w", CtlName(id), err)
	}
	addr, err := syscall.MapViewOfFile(h, fileMapAllAccess, 0, 0, ctlBytes)
	if err != nil {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("surfacepub: MapViewOfFile: %w", err)
	}
	p := &Pub{id: id, hmap: h, view: ptrOf(addr)}
	c := p.ctl()
	*c.u32(offMagic) = ctlMagic
	*c.u32(offVersion) = ctlVersion
	*c.u32(offSlots) = Slots
	*c.u32(offFmt) = dxgiFormatB8G8R8A8Unorm
	*c.u32(offPID) = uint32(syscall.Getpid())
	atomic.StoreUint32(c.u32(offGen), 0)
	return p, nil
}

func (p *Pub) ctl() ctl { return ctl{p: p.view} }

// Want reports the size the consumer asked for (0,0 = it has not attached yet).
func (p *Pub) Want() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.view == nil {
		return 0, 0
	}
	c := p.ctl()
	return int(atomic.LoadUint32(c.u32(offWantW))), int(atomic.LoadUint32(c.u32(offWantH)))
}

// Size is the ring's current geometry (0,0 = no ring).
func (p *Pub) Size() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w, p.h
}

// SetGeometry (re)builds the ring at w*h and publishes it as a new generation. A consumer holding
// the previous generation notices the bump and re-opens by name.
func (p *Pub) SetGeometry(w, h int) error {
	if err := ValidGeometry(w, h); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("surfacepub: publisher closed")
	}
	if p.dev == nil {
		if err := p.createDevice(); err != nil {
			return err
		}
	}
	p.releaseRing()
	gen := p.gen + 1

	desc := texDesc{
		Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
		Format: dxgiFormatB8G8R8A8Unorm, SampleCount: 1, SampleQuality: 0,
		Usage: usageDefault, BindFlags: bindSRV | bindRTV,
		// NTHANDLE is what makes the share NAMEABLE, and it is only legal with KEYEDMUTEX - which is
		// the sync we want anyway. Without NTHANDLE the consumer would need the raw HANDLE value,
		// i.e. the daemon back in the middle as a courier.
		MiscFlags: miscSharedKeyedMutex | miscSharedNTHandle,
	}
	for i := 0; i < Slots; i++ {
		var tex unsafe.Pointer
		if hr := vcall(p.dev, slotDevCreateTexture2D, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&tex))); int32(hr) < 0 || tex == nil {
			p.releaseRing()
			return hrErr("CreateTexture2D(shared)", hr)
		}
		p.tex[i] = tex
		res1 := comQI(tex, &iidDXGIRes1)
		if res1 == nil {
			p.releaseRing()
			return fmt.Errorf("surfacepub: texture has no IDXGIResource1 (Windows 8+ required)")
		}
		name, err := syscall.UTF16PtrFromString(TexName(p.id, gen, i))
		if err != nil {
			comRelease(res1)
			p.releaseRing()
			return err
		}
		var sh syscall.Handle
		hr := vcall(res1, slotResCreateSharedHandle, 0, uintptr(sharedResourceRead|sharedResourceWrite),
			uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sh)))
		comRelease(res1)
		if int32(hr) < 0 || sh == 0 {
			p.releaseRing()
			return hrErr("CreateSharedHandle", hr)
		}
		p.share[i] = sh
		km := comQI(tex, &iidKeyedMutex)
		if km == nil {
			p.releaseRing()
			return fmt.Errorf("surfacepub: texture has no IDXGIKeyedMutex")
		}
		p.km[i] = km
	}

	// Staging: a shared texture cannot be CPU-mapped, so a CPU frame lands here first and is copied
	// on the GPU. DYNAMIC + WRITE_DISCARD is the no-stall upload path.
	sdesc := texDesc{
		Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
		Format: dxgiFormatB8G8R8A8Unorm, SampleCount: 1, SampleQuality: 0,
		Usage: usageDynamic, BindFlags: bindSRV, CPUAccessFlags: cpuAccessWrite,
	}
	var stg unsafe.Pointer
	if hr := vcall(p.dev, slotDevCreateTexture2D, uintptr(unsafe.Pointer(&sdesc)), 0, uintptr(unsafe.Pointer(&stg))); int32(hr) < 0 || stg == nil {
		p.releaseRing()
		return hrErr("CreateTexture2D(staging)", hr)
	}
	p.staging = stg

	p.gen, p.w, p.h, p.next, p.seq = gen, w, h, 0, 0
	c := p.ctl()
	*c.u32(offW) = uint32(w)
	*c.u32(offH) = uint32(h)
	for i := 0; i < Slots; i++ {
		atomic.StoreUint64(c.u64(uintptr(offSlot0+i*slotStride)), 0)
	}
	atomic.StoreUint64(c.u64(offWriteSeq), 0)
	atomic.StoreUint32(c.u32(offGen), gen) // LAST: the generation is the consumer's go-ahead
	return nil
}

func (p *Pub) createDevice() error {
	var dev, ctx unsafe.Pointer
	hr, _, _ := procD3D11CreateDev.Call(0, driverHardware, 0, flagBGRA, 0, 0, sdkVersion,
		uintptr(unsafe.Pointer(&dev)), 0, uintptr(unsafe.Pointer(&ctx)))
	if int32(hr) < 0 || dev == nil {
		hr, _, _ = procD3D11CreateDev.Call(0, driverWARP, 0, flagBGRA, 0, 0, sdkVersion,
			uintptr(unsafe.Pointer(&dev)), 0, uintptr(unsafe.Pointer(&ctx)))
		if int32(hr) < 0 || dev == nil {
			return hrErr("D3D11CreateDevice (hardware and WARP)", hr)
		}
	}
	p.dev, p.ctx = dev, ctx
	return nil
}

// Send uploads one frame and publishes it with its source PTS. A frame the consumer has not made
// room for is DROPPED here, counted, and the call returns nil: a diagnostic producer that blocks on
// a busy compositor is a worse bug than a missing frame.
func (p *Pub) Send(img *image.NRGBA, ptsNs int64) error {
	if img == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.staging == nil {
		return nil
	}
	if img.Rect.Dx() != p.w || img.Rect.Dy() != p.h {
		return fmt.Errorf("surfacepub: frame %dx%d does not match ring %dx%d", img.Rect.Dx(), img.Rect.Dy(), p.w, p.h)
	}

	// Pick a slot the consumer has released back to us; try every slot before giving up.
	slot, ok := 0, false
	for i := 0; i < Slots; i++ {
		cand := (p.next + i) % Slots
		hr := vcall(p.km[cand], slotKMAcquireSync, uintptr(keyProducer), acquireMs)
		if int32(hr) >= 0 && hr != waitTimeout {
			slot, ok = cand, true
			break
		}
	}
	if !ok {
		p.dropped.Add(1)
		return nil
	}
	p.next = (slot + 1) % Slots

	if err := p.uploadLocked(img); err != nil {
		vcall(p.km[slot], slotKMReleaseSync, uintptr(keyProducer)) // hand it BACK, not forward
		return err
	}
	vcall(p.ctx, slotCtxCopyResource, uintptr(p.tex[slot]), uintptr(p.staging))
	vcall(p.ctx, slotCtxFlush) // another PROCESS reads this; without the submit it sees the old pixels
	vcall(p.km[slot], slotKMReleaseSync, uintptr(keyConsumer))

	p.seq++
	c := p.ctl()
	base := uintptr(offSlot0 + slot*slotStride)
	atomic.StoreUint64(c.u64(base+8), uint64(ptsNs))
	atomic.StoreUint64(c.u64(base), p.seq)
	atomic.StoreUint64(c.u64(offWriteSeq), p.seq) // LAST: the consumer gates on this
	atomic.StoreUint64(c.u64(offProdBeatMs), uint64(time.Now().UnixMilli()))
	p.published.Add(1)
	return nil
}

// uploadLocked maps the staging texture and writes the frame into it, swizzling NRGBA → BGRA.
func (p *Pub) uploadLocked(img *image.NRGBA) error {
	var m mappedSubresource
	hr := vcall(p.ctx, slotCtxMap, uintptr(p.staging), 0, mapWriteDiscard, 0, uintptr(unsafe.Pointer(&m)))
	if int32(hr) < 0 || m.pData == 0 {
		return hrErr("Map(staging)", hr)
	}
	defer vcall(p.ctx, slotCtxUnmap, uintptr(p.staging), 0)
	w, h := p.w, p.h
	dst := unsafe.Slice((*byte)(ptrOf(m.pData)), int(m.RowPitch)*h)
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+w*4]
		row := dst[y*int(m.RowPitch):]
		for x := 0; x < w*4; x += 4 {
			row[x] = src[x+2] // B
			row[x+1] = src[x+1]
			row[x+2] = src[x] // R
			row[x+3] = src[x+3]
		}
	}
	return nil
}

// Stats snapshots both halves of the transport.
func (p *Pub) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Stats{ID: p.id, Gen: p.gen, W: p.w, H: p.h,
		Published: p.published.Load(), Dropped: p.dropped.Load(), ConsumerAgeMs: -1}
	if p.view == nil {
		return s
	}
	c := p.ctl()
	s.WantW = int(atomic.LoadUint32(c.u32(offWantW)))
	s.WantH = int(atomic.LoadUint32(c.u32(offWantH)))
	s.PresentSeq = atomic.LoadUint64(c.u64(offPresentSeq))
	s.ConsumerDrops = atomic.LoadUint64(c.u64(offDropCount))
	if beat := atomic.LoadUint64(c.u64(offConsBeatMs)); beat != 0 {
		// The consumer stamps GetTickCount64 (uptime ms), not wall time - compare against ours.
		s.ConsumerAgeMs = int64(tickCount64() - beat)
	}
	return s
}

func tickCount64() uint64 {
	r, _, _ := procTickCount64.Call()
	return uint64(r)
}

func (p *Pub) releaseRing() {
	for i := 0; i < Slots; i++ {
		comRelease(p.km[i])
		comRelease(p.tex[i])
		if p.share[i] != 0 {
			_ = syscall.CloseHandle(p.share[i])
		}
		p.km[i], p.tex[i], p.share[i] = nil, nil, 0
	}
	comRelease(p.staging)
	p.staging = nil
	p.w, p.h = 0, 0
	if p.view != nil {
		atomic.StoreUint32(p.ctl().u32(offGen), 0)
	}
}

// Close tears the whole endpoint down. The control block disappears with the last handle, which is
// exactly how the consumer learns the producer is gone.
func (p *Pub) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.releaseRing()
	comRelease(p.ctx)
	comRelease(p.dev)
	p.ctx, p.dev = nil, nil
	if p.view != nil {
		_ = syscall.UnmapViewOfFile(uintptr(p.view))
		p.view = nil
	}
	if p.hmap != 0 {
		_ = syscall.CloseHandle(p.hmap)
		p.hmap = 0
	}
}
