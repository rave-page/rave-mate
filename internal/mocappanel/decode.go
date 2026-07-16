package mocappanel

// decode.go - single-frame decode (stateless core) + the stateful stream Decoder
// (MAGIC hysteresis, frameCounter liveness, boneMask acceptance) per contract §5/§7.

import (
	"errors"
	"fmt"
	"image"
)

// ErrNoMagic marks a frame whose MAGIC cells miss (per-byte, +-MagicTol, post-calibration).
// The stateful Decoder rides out up to 2 consecutive misses on a locked stream.
var ErrNoMagic = errors.New("mocappanel: MAGIC mismatch")

// DecodeFrame decodes one captured panel frame. Frame-level rejects: geometry (v1 requires
// native 1920x1080), degenerate calibration, MAGIC miss, unreadable/invalid header. Dancer
// problems reject the dancer, bone problems the bone - never the frame.
func DecodeFrame(img image.Image) (Header, []Dancer, error) {
	if err := checkGeometry(img); err != nil {
		return Header{}, nil, err
	}
	return DecodeSampled(ImageSampler(img))
}

// DecodeSampled decodes one panel from an arbitrary sampler: sample(x,y) returns the captured
// RGB at CANONICAL panel coords. A rectifying node (contract §8b) inverse-maps only the cell
// centres through its homography - no geometry check here, the sampler owns it. Same reject
// semantics as DecodeFrame otherwise.
func DecodeSampled(sample func(x, y int) (r, g, b uint8)) (Header, []Dancer, error) {
	h, dancers, _, err := decodeSampled(sample)
	return h, dancers, err
}

// decodeSampled is the stateless core; also yields the Calib (MidWarn) for the stateful Decoder.
func decodeSampled(sample func(x, y int) (r, g, b uint8)) (Header, []Dancer, Calib, error) {
	cal, err := calibrate(sample)
	if err != nil {
		return Header{}, nil, Calib{}, err
	}
	if !magicOK(sample, cal) {
		return Header{}, nil, Calib{}, ErrNoMagic
	}
	h, err := parseHeader(sample, cal)
	if err != nil {
		return Header{}, nil, Calib{}, err
	}
	return h, parseDancers(sample, cal, h), cal, nil
}

// checkGeometry enforces the v1 native-1920x1080 rule (log expected-vs-actual; no fallback).
func checkGeometry(img image.Image) error {
	b := img.Bounds()
	if b.Dx() != CanvasW || b.Dy() != CanvasH {
		return fmt.Errorf("mocappanel: bad geometry: want %dx%d, got %dx%d", CanvasW, CanvasH, b.Dx(), b.Dy())
	}
	return nil
}

// ImageSampler adapts an in-memory image to the sampler contract (honours a non-zero bounds
// origin). Callers keep (x,y) inside the canonical canvas.
func ImageSampler(img image.Image) func(x, y int) (r, g, b uint8) {
	b := img.Bounds()
	return func(x, y int) (uint8, uint8, uint8) {
		r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)
	}
}

// magicOK checks the four MAGIC bytes independently within +-MagicTol post-calibration.
func magicOK(sample func(x, y int) (r, g, b uint8), cal Calib) bool {
	ok := func(col int, want uint16) bool {
		x, y := MetaSample(col)
		px := cal.Apply(rawSample(sample, x, y))
		return absDiff(px[0], uint8(want>>8)) <= MagicTol && absDiff(px[1], uint8(want)) <= MagicTol
	}
	return ok(ColMagic0, Magic0) && ok(ColMagic1, Magic1)
}

// parseHeader reads meta cols 5..34 (reserved cols ignored for forward-compat; the v1.1 TR
// fiducial at col 59 is never read). Any parity-invalid header cell rejects the frame - without
// a trusted S/D the data region cannot be parsed.
func parseHeader(sample func(x, y int) (r, g, b uint8), cal Calib) (Header, error) {
	var cells [MetaCols]uint16
	for c := ColVersion; c < ColReserved; c++ {
		v, ok := cal.metaCell(sample, c)
		if !ok {
			return Header{}, fmt.Errorf("mocappanel: header cell %d parity-invalid", c)
		}
		cells[c] = v
	}
	h := Header{
		Version:              cells[ColVersion],
		Flags:                cells[ColFlags],
		SourceTag:            get32(cells[:], ColSourceTag),
		SessionNonce:         cells[ColSessionNonce],
		PanelSeq:             get32(cells[:], ColPanelSeq),
		ServerTimeMs:         int64(get64(cells[:], ColServerTimeMs)),
		NetUtcTicks:          int64(get64(cells[:], ColNetUtcTicks)),
		BpmX100:              cells[ColBpmX100],
		DownbeatServerTimeMs: int64(get64(cells[:], ColDownbeatMs)),
		BoneSlots:            int(cells[ColBoneSlots]),
		DancerCount:          int(cells[ColDancerCount]),
		FrameCounter:         get32(cells[:], ColFrameCounter),
	}
	for i := 0; i < 3; i++ {
		h.StageMin[i] = float64(int16(cells[ColStageMin+i])) / StageFixedScale
		h.StageSize[i] = float64(cells[ColStageSize+i]) / StageFixedScale
	}
	if h.Version != Version {
		return Header{}, fmt.Errorf("mocappanel: unsupported version %d (decoder speaks %d)", h.Version, Version)
	}
	if h.BoneSlots < 1 || h.BoneSlots > BoneSlotMax {
		return Header{}, fmt.Errorf("mocappanel: boneSlots %d outside 1..%d", h.BoneSlots, BoneSlotMax)
	}
	if h.DancerCount > MaxDancers {
		return Header{}, fmt.Errorf("mocappanel: dancerCount %d exceeds v1 cap %d", h.DancerCount, MaxDancers)
	}
	if n := h.DancerCount * Stride(h.BoneSlots); n > MaxDataCells {
		return Header{}, fmt.Errorf("mocappanel: D*stride %d exceeds budget %d", n, MaxDataCells)
	}
	return h, nil
}

// parseDancers reads each dancer block. Rejected (skipped) dancers: unreadable header cells,
// present bit clear, or missing mandatory core bones (mask & CoreMask != CoreMask - an
// undefined root is worse than no puppet). Invalid bone cells only mark that bone absent.
func parseDancers(sample func(x, y int) (r, g, b uint8), cal Calib, h Header) []Dancer {
	dancers := make([]Dancer, 0, h.DancerCount)
	for d := 0; d < h.DancerCount; d++ {
		base := d * Stride(h.BoneSlots)
		var hd [DancerHeaderCells]uint16
		ok := true
		for i := range hd {
			v, valid := cal.dataCell(sample, base+i)
			if !valid {
				ok = false
				break
			}
			hd[i] = v
		}
		if !ok {
			continue
		}
		mask := uint32(hd[OffBoneMask])<<16 | uint32(hd[OffBoneMask+1])
		if hd[OffFlags]&DancerPresent == 0 || mask&CoreMask != CoreMask {
			continue
		}
		dc := Dancer{
			LocalID:  hd[OffLocalID],
			Flags:    hd[OffFlags],
			BoneMask: mask,
			HipsQ:    [3]uint16{hd[OffHips], hd[OffHips+1], hd[OffHips+2]},
			Rots:     make([]uint32, h.BoneSlots),
			Quats:    make([][4]float64, h.BoneSlots),
			Present:  make([]bool, h.BoneSlots),
		}
		for k := 0; k < h.BoneSlots; k++ {
			if mask>>k&1 == 0 {
				continue
			}
			hi, okHi := cal.dataCell(sample, base+OffBones+2*k)
			lo, okLo := cal.dataCell(sample, base+OffBones+2*k+1)
			if !okHi || !okLo {
				continue // parity-invalid -> this bone absent this frame
			}
			w := uint32(hi)<<16 | uint32(lo)
			dc.Rots[k] = w
			q, okQ := UnpackQuat(w)
			if !okQ {
				continue // norm-reject -> absent
			}
			dc.Quats[k] = q
			dc.Present[k] = true
		}
		dancers = append(dancers, dc)
	}
	return dancers
}

func get32(cells []uint16, col int) uint32 {
	return uint32(cells[col])<<16 | uint32(cells[col+1])
}

func get64(cells []uint16, col int) uint64 {
	var v uint64
	for i := 0; i < 4; i++ {
		v = v<<16 | uint64(cells[col+i])
	}
	return v
}

// Decoder state thresholds (contract §5/§7).
const (
	DefaultStalenessWindow = 15 // frames without a frameCounter advance before !Live
	magicMissLimit         = 3  // locked stream rides out 2 consecutive MAGIC misses, drops on the 3rd
	maskAcceptFrames       = 2  // identical consecutive frames before a boneMask change applies
)

// Decoder wraps DecodeFrame with per-stream state: MAGIC 3-miss drop hysteresis, frameCounter
// liveness, and 2-frame boneMask acceptance per dancer (kills LSB flicker/flapping). One
// Decoder per capture stream; not safe for concurrent use.
type Decoder struct {
	StalenessWindow int // 0 = DefaultStalenessWindow

	locked      bool
	magicMiss   int
	haveCounter bool
	lastCounter uint32
	stale       int
	masks       map[uint16]*maskState // keyed by dancer LocalID
	MidWarn     bool                  // last good frame's calibration MID sanity (contract §2)
}

type maskState struct {
	accepted uint32
	pending  uint32
	pendingN int
}

// NewDecoder returns a Decoder with contract defaults.
func NewDecoder() *Decoder {
	return &Decoder{StalenessWindow: DefaultStalenessWindow, masks: map[uint16]*maskState{}}
}

// Decode processes one captured frame. On ErrNoMagic a locked stream stays locked for up to two
// consecutive misses (transient noise), dropping on the third. Returned dancers carry the
// ACCEPTED boneMask; wire-mask bits not yet accepted have Present forced false.
func (dec *Decoder) Decode(img image.Image) (Header, []Dancer, error) {
	if err := checkGeometry(img); err != nil {
		return Header{}, nil, err
	}
	return dec.DecodeSampled(ImageSampler(img))
}

// DecodeSampled is Decode over an arbitrary canonical-coord sampler (rectified capture,
// contract §8b) - same stream state, no geometry check.
func (dec *Decoder) DecodeSampled(sample func(x, y int) (r, g, b uint8)) (Header, []Dancer, error) {
	h, dancers, cal, err := decodeSampled(sample)
	if err != nil {
		if errors.Is(err, ErrNoMagic) && dec.locked {
			dec.magicMiss++
			dec.stale++ // no counter advance observed this frame
			if dec.magicMiss >= magicMissLimit {
				dec.reset()
			}
		}
		return Header{}, nil, err
	}
	dec.magicMiss = 0
	dec.locked = true
	dec.MidWarn = cal.MidWarn

	// Liveness: frameCounter advancing within the staleness window.
	if !dec.haveCounter || h.FrameCounter != dec.lastCounter {
		dec.haveCounter = true
		dec.lastCounter = h.FrameCounter
		dec.stale = 0
	} else {
		dec.stale++
	}

	for i := range dancers {
		dec.applyMaskHysteresis(&dancers[i])
	}
	return h, dancers, nil
}

// Live reports stream liveness: MAGIC-locked AND frameCounter advanced within the window.
// Not live -> consumers ease to idle/recorded take, never freeze.
func (dec *Decoder) Live() bool {
	w := dec.StalenessWindow
	if w <= 0 {
		w = DefaultStalenessWindow
	}
	return dec.locked && dec.stale < w
}

// applyMaskHysteresis rewrites d to the accepted mask: a changed wire mask only applies after
// maskAcceptFrames identical consecutive frames; until then bones outside the accepted mask
// are forced absent (their wire data may be valid but is not yet trusted).
func (dec *Decoder) applyMaskHysteresis(d *Dancer) {
	ms := dec.masks[d.LocalID]
	if ms == nil {
		dec.masks[d.LocalID] = &maskState{accepted: d.BoneMask} // first sight: accept as-is
		return
	}
	switch d.BoneMask {
	case ms.accepted:
		ms.pendingN = 0
	case ms.pending:
		ms.pendingN++
		if ms.pendingN >= maskAcceptFrames {
			ms.accepted, ms.pendingN = d.BoneMask, 0
		}
	default:
		ms.pending, ms.pendingN = d.BoneMask, 1
	}
	if d.BoneMask == ms.accepted {
		return
	}
	d.BoneMask = ms.accepted
	for k := range d.Present {
		if ms.accepted>>k&1 == 0 && d.Present[k] {
			d.Present[k] = false
			d.Quats[k] = [4]float64{}
		}
	}
}

// reset drops all stream state (unlock): the next MAGIC-good frame locks a fresh stream.
func (dec *Decoder) reset() {
	dec.locked = false
	dec.magicMiss = 0
	dec.haveCounter = false
	dec.lastCounter = 0
	dec.stale = 0
	dec.masks = map[uint16]*maskState{}
	dec.MidWarn = false
}
