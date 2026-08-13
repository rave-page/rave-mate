// Package surfacepub publishes frames to a native render surface in the webview shell child as a
// SHARED D3D11 TEXTURE, synchronised by a keyed mutex and found by NAME
// (SDL_WEBVIEW_SURFACE_DESIGN §4.5, phase P3).
//
// Why by name: the design's preferred handshake is out-of-band precisely so the daemon is not a
// courier for a HANDLE. A producer creates `Local\rave-surface-<id>-ctl` plus its ring textures;
// the shell child opens them when the matching [data-surface] element appears. Nothing about the
// surface - not the handle, not the rect, not the pixels - travels over PSH1.
//
// `Local\` and not `Global\`: naming a kernel object in the global namespace needs
// SeCreateGlobalPrivilege (admin). Producer and shell run as the same user in the same session, so
// the session namespace reaches exactly as far as it must and asks for no privilege at all.
//
// GEOMETRY IS NEGOTIATED, NOT ASSUMED. The consumer writes the surface's full rect into the control
// block; the producer renders at exactly that and re-publishes a new ring GENERATION when it
// changes. That is what keeps the present path a 1:1 copy: no scaler on either side, and an element
// scrolled half out of view is CROPPED rather than squashed into what is left of it.
package surfacepub

import (
	"fmt"
	"unsafe"
)

// Ring bound, stated in frames AND bytes as the repo requires of every data-path queue:
//
//	Slots frames, MaxRingBytes total. Nothing else buffers anywhere in this path - the generator
//	renders into one reusable image, uploads it, and the ring is the only thing between the two
//	processes.
//
// Drop policy: the PRODUCER finds no slot released back to it (the consumer is behind) and drops
// its own newest frame - never blocks, never grows. The CONSUMER takes the newest ready slot and
// releases older ready ones unread (drop-oldest). Both halves are counted, never silent.
const (
	Slots        = 2
	MaxRingBytes = 32 << 20 // 32 MiB of shared VRAM: 1920x1080x4x2 = 16.6 MiB, 4K would be 66 MiB
	MinDim       = 240
	MaxDim       = 3840
)

// Control-block layout. MIRRORED BYTE-FOR-BYTE in native/zigui/src/shell/surfsrc.zig - change one,
// change both; ctlVersion is the tripwire if someone doesn't.
const (
	ctlMagic   = 0x52534631 // "RSF1"
	ctlVersion = 1
	ctlBytes   = 4096

	offMagic      = 0
	offVersion    = 4
	offGen        = 8
	offSlots      = 12
	offW          = 16
	offH          = 20
	offFmt        = 24
	offPID        = 28
	offWriteSeq   = 32
	offProdBeatMs = 40
	offWantW      = 48 // consumer → producer
	offWantH      = 52
	offConsBeatMs = 56
	offPresentSeq = 64 // consumer → producer
	offDropCount  = 72
	offSlot0      = 128
	slotStride    = 16 // seq uint64, ptsNs int64
)

// Keyed-mutex keys. Released with keyProducer = the producer may write; released with keyConsumer =
// a frame is ready. A slot only ever sits at one of the two, so neither side can read a torn frame
// and neither can wedge the other: a wait that times out is a dropped frame, by design.
const (
	keyProducer uint64 = 0
	keyConsumer uint64 = 1
)

const dxgiFormatB8G8R8A8Unorm = 87

// CtlName is the file-mapping name a consumer probes for.
func CtlName(id string) string { return `Local\rave-surface-` + id + `-ctl` }

// TexName is one ring slot's shared-texture name. The generation is IN the name because a resize
// cannot recreate a name the consumer still has open (CreateSharedHandle refuses); publishing a new
// generation and letting the consumer notice is the only race-free way to change geometry.
func TexName(id string, gen uint32, slot int) string {
	return fmt.Sprintf(`Local\rave-surface-%s-g%d-s%d`, id, gen, slot)
}

// Stats is the producer's ground truth plus whatever the consumer has written back. Published minus
// PresentSeq is the transport's own loss; Dropped is loss the producer chose.
type Stats struct {
	ID            string `json:"id"`
	Gen           uint32 `json:"gen"`
	W             int    `json:"w"`
	H             int    `json:"h"`
	Published     uint64 `json:"published"`
	Dropped       uint64 `json:"dropped"` // no slot free: the consumer was behind
	WantW         int    `json:"wantW"`   // what the consumer asked us to render at
	WantH         int    `json:"wantH"`
	PresentSeq    uint64 `json:"presentSeq"`    // consumer: last seq it put on screen
	ConsumerDrops uint64 `json:"consumerDrops"` // consumer: ready frames released unread
	ConsumerAgeMs int64  `json:"consumerAgeMs"` // since the consumer last touched the block (-1 = never)
}

// RingBytes is the shared VRAM a w*h ring costs.
func RingBytes(w, h int) int { return w * h * 4 * Slots }

// ValidGeometry gates a requested size against both the per-dimension bounds and the ring's byte
// cap. A producer must not be talked into a 4K ring by a page that reports a 4K rect.
func ValidGeometry(w, h int) error {
	if w < MinDim || h < MinDim || w > MaxDim || h > MaxDim {
		return fmt.Errorf("surfacepub: %dx%d outside %d..%d", w, h, MinDim, MaxDim)
	}
	if n := RingBytes(w, h); n > MaxRingBytes {
		return fmt.Errorf("surfacepub: %dx%d ring is %d MiB, cap is %d MiB", w, h, n>>20, MaxRingBytes>>20)
	}
	return nil
}

// ClampGeometry brings a consumer's wish inside the bounds instead of refusing it: a surface that is
// briefly enormous mid-resize must not stop the producer, it must be served at the largest legal
// size. Aspect is preserved on the byte-cap path so the picture stays the shape of the element.
func ClampGeometry(w, h int) (int, int) {
	if w < MinDim {
		w = MinDim
	}
	if h < MinDim {
		h = MinDim
	}
	if w > MaxDim {
		w = MaxDim
	}
	if h > MaxDim {
		h = MaxDim
	}
	for RingBytes(w, h) > MaxRingBytes {
		w, h = w*3/4, h*3/4
		if w < MinDim || h < MinDim {
			return MinDim, MinDim
		}
	}
	return w, h
}

// ctl is a typed view over the shared control page.
type ctl struct{ p unsafe.Pointer }

func (c ctl) u32(off uintptr) *uint32 { return (*uint32)(unsafe.Add(c.p, off)) }
func (c ctl) u64(off uintptr) *uint64 { return (*uint64)(unsafe.Add(c.p, off)) }
