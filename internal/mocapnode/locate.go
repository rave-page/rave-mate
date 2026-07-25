package mocapnode

// locate.go - contract §8b locate + rectify: per-fiducial colour blob scan (per-channel
// tolerance +-12 pre-calibration, connected components, largest blob centroid), TL validated by
// the MAGIC1 blob one meta-cell pitch along TL->TR, exact 4-point homography, validity =
// MAGIC1 reprojects within 5 px AND calibration triad sane post-warp. Rectification
// inverse-maps ONLY canonical sample centres (nearest-neighbour; cells are 16 px fat, a <=6 px
// fit residual is absorbed).

import (
	"errors"
	"fmt"
	"math"

	"rave.page/mate/internal/mocappanel"
	"rave.page/mate/internal/zignative"
)

const (
	fidTol        = 12  // per-channel blob match tolerance, pre-calibration (§8b)
	reprojTolPx   = 5.0 // MAGIC1 centre reprojection bound
	identSnapPx   = 1.0 // identity fast-path: anchors within 1 px of canonical at native size
	minBlobPx     = 4   // speckle floor for a fiducial blob
	magic1GateRel = 0.6 // blob-stage MAGIC1 validation radius, in local meta-cell pitches
)

// ErrNoLock marks a frame in which the panel could not be located (missing fiducials, failed
// validation). Wrapped with detail; match with errors.Is.
var ErrNoLock = errors.New("mocapnode: panel not located")

// blob targets, indexed by the constants below.
const (
	tgtTL = iota // MAGIC0 cell colour
	tgtM1        // MAGIC1 - TL validator, not an anchor
	tgtTR
	tgtBL
	tgtBR
	numTargets
)

// canonicalAnchors are the §8b anchor sample points in TL,TR,BL,BR order.
var canonicalAnchors = [4][2]float64{
	{mocappanel.AnchorTLX, mocappanel.AnchorTLY},
	{mocappanel.AnchorTRX, mocappanel.AnchorTRY},
	{mocappanel.AnchorBLX, mocappanel.AnchorBLY},
	{mocappanel.AnchorBRX, mocappanel.AnchorBRY},
}

// targetColors returns the five blob-scan colours. TL/M1 are the MAGIC cells' VALID parity;
// TR/BL/BR the fiducials' INVERTED parity.
func targetColors() [numTargets][3]uint8 {
	var t [numTargets][3]uint8
	r, g, b := mocappanel.CellBytes(mocappanel.Magic0)
	t[tgtTL] = [3]uint8{r, g, b}
	r, g, b = mocappanel.CellBytes(mocappanel.Magic1)
	t[tgtM1] = [3]uint8{r, g, b}
	r, g, b = mocappanel.FidBytes(mocappanel.FidTR)
	t[tgtTR] = [3]uint8{r, g, b}
	r, g, b = mocappanel.FidBytes(mocappanel.FidBL)
	t[tgtBL] = [3]uint8{r, g, b}
	r, g, b = mocappanel.FidBytes(mocappanel.FidBR)
	t[tgtBR] = [3]uint8{r, g, b}
	return t
}

// Lock is a located panel: the canonical->frame homography and the anchors that produced it.
type Lock struct {
	H        Homography    // canonical panel coords -> frame coords
	Anchors  [4][2]float64 // located blob centroids TL,TR,BL,BR (frame coords)
	Identity bool          // native 1920x1080 with anchors on the canonical points: sample directly
}

// Locate runs the full §8b locate on one frame. Expensive (full-frame blob scan) - run it only
// while unlocked; a locked stream revalidates cheaply via Revalidate.
func Locate(f *Frame) (Lock, error) {
	blobs, m1, err := scanBlobs(f)
	if err != nil {
		return Lock{}, err
	}

	// TL validation: the MAGIC1 blob nearest its expected spot must sit one meta-cell pitch
	// along TL->TR. Distances in frame px, scaled off the located TL->TR span.
	tl, tr := blobs[0], blobs[1]
	span := math.Hypot(tr[0]-tl[0], tr[1]-tl[1])
	if span < 1 {
		return Lock{}, fmt.Errorf("%w: TL/TR anchors coincide", ErrNoLock)
	}
	canonSpan := float64(mocappanel.AnchorTRX - mocappanel.AnchorTLX)
	pitch := span * float64(mocappanel.MetaCellPx) / canonSpan
	wantM1 := [2]float64{tl[0] + (tr[0]-tl[0])*float64(mocappanel.MetaCellPx)/canonSpan,
		tl[1] + (tr[1]-tl[1])*float64(mocappanel.MetaCellPx)/canonSpan}
	if math.Hypot(m1[0]-wantM1[0], m1[1]-wantM1[1]) > magic1GateRel*pitch {
		return Lock{}, fmt.Errorf("%w: MAGIC1 blob not one meta-cell pitch from MAGIC0", ErrNoLock)
	}

	h, err := SolveHomography(canonicalAnchors, blobs)
	if err != nil {
		return Lock{}, fmt.Errorf("%w: %v", ErrNoLock, err)
	}

	// Validity 1: MAGIC1 centre reprojects within 5 px of its blob.
	m1x, m1y := mocappanel.MetaSample(mocappanel.ColMagic1)
	px, py := h.Apply(float64(m1x), float64(m1y))
	if math.Hypot(px-m1[0], py-m1[1]) > reprojTolPx {
		return Lock{}, fmt.Errorf("%w: MAGIC1 reprojection off by >%v px", ErrNoLock, reprojTolPx)
	}
	// Validity 2: calibration triad sane post-warp.
	if err := triadSane(f, h); err != nil {
		return Lock{}, fmt.Errorf("%w: %v", ErrNoLock, err)
	}

	lk := Lock{H: h, Anchors: blobs}
	lk.Identity = isIdentity(f, blobs)
	return lk, nil
}

// scanBlobs labels every pixel against the five target colours (targets are >2*tol apart in
// some channel, so a pixel matches at most one), then runs 4-connected components. Returns the
// TL,TR,BL,BR anchor centroids (largest blob each) and the MAGIC1 centroid (chosen later
// against the expected position; largest here too - validation catches a wrong pick).
func scanBlobs(f *Frame) (anchors [4][2]float64, m1 [2]float64, err error) {
	targets := targetColors()
	labels := make([]uint8, f.W*f.H) // 0 = none, 1..numTargets = target+1
	labelPixels(f, targets, labels)

	best := [numTargets]blob{}
	visited := make([]bool, f.W*f.H)
	var stack []int
	for start, lab := range labels {
		if lab == 0 || visited[start] {
			continue
		}
		// BFS one component.
		var bl blob
		stack = append(stack[:0], start)
		visited[start] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%f.W, p/f.W
			bl.n++
			bl.sx += float64(x)
			bl.sy += float64(y)
			for _, q := range [4]int{p - 1, p + 1, p - f.W, p + f.W} {
				if q < 0 || q >= len(labels) || visited[q] || labels[q] != lab {
					continue
				}
				// row wrap guard for horizontal neighbours
				if (q == p-1 || q == p+1) && q/f.W != y {
					continue
				}
				visited[q] = true
				stack = append(stack, q)
			}
		}
		t := int(lab) - 1
		if bl.n >= minBlobPx && bl.n > best[t].n {
			best[t] = bl
		}
	}

	names := [numTargets]string{"TL(MAGIC0)", "MAGIC1", "TR", "BL", "BR"}
	for t := 0; t < numTargets; t++ {
		if best[t].n == 0 {
			return anchors, m1, fmt.Errorf("%w: fiducial %s not found", ErrNoLock, names[t])
		}
	}
	order := [4]int{tgtTL, tgtTR, tgtBL, tgtBR}
	for i, t := range order {
		anchors[i] = best[t].centroid()
	}
	return anchors, best[tgtM1].centroid(), nil
}

// labelPixels runs the per-pixel target classification (zig kernel when linked;
// Go loop = fallback + golden reference).
func labelPixels(f *Frame, targets [numTargets][3]uint8, labels []uint8) {
	if zignative.Available() {
		flat := make([]byte, 0, numTargets*3)
		for _, c := range targets {
			flat = append(flat, c[0], c[1], c[2])
		}
		if zignative.PxLabel(f.Pix, f.Stride, f.W, f.H, f.Fmt.Bpp(), f.Fmt == FmtBGRA, flat, fidTol, labels) {
			return
		}
	}
	labelPixelsGo(f, targets, labels)
}

// labelPixelsGo is the pure-Go labeling pass of scanBlobs.
func labelPixelsGo(f *Frame, targets [numTargets][3]uint8, labels []uint8) {
	for y := 0; y < f.H; y++ {
		row := y * f.Stride
		bpp := f.Fmt.Bpp()
		for x := 0; x < f.W; x++ {
			i := row + x*bpp
			var r, g, b uint8
			if f.Fmt == FmtBGRA {
				r, g, b = f.Pix[i+2], f.Pix[i+1], f.Pix[i]
			} else {
				r, g, b = f.Pix[i], f.Pix[i+1], f.Pix[i+2]
			}
			for t := 0; t < numTargets; t++ {
				c := targets[t]
				if absDiffU8(r, c[0]) <= fidTol && absDiffU8(g, c[1]) <= fidTol && absDiffU8(b, c[2]) <= fidTol {
					labels[y*f.W+x] = uint8(t + 1)
					break
				}
			}
		}
	}
}

type blob struct {
	n      int
	sx, sy float64
}

func (b blob) centroid() [2]float64 { return [2]float64{b.sx / float64(b.n), b.sy / float64(b.n)} }

// triadSane samples the calibration triad through the homography (nearest-neighbour) and gates
// on loose bands: black dark, white bright, mid in the middle. The exact two-point calibration
// runs inside the decode; this only rejects a homography landing off the panel.
func triadSane(f *Frame, h Homography) error {
	read := func(col int) [3]uint8 {
		x, y := mocappanel.MetaSample(col)
		r, g, b := sampleNearest(f, h, x, y)
		return [3]uint8{r, g, b}
	}
	black := read(mocappanel.ColCalBlack)
	mid := read(mocappanel.ColCalMid)
	white := read(mocappanel.ColCalWhite)
	for ch := 0; ch < 3; ch++ {
		if black[ch] > 64 {
			return fmt.Errorf("calibration BLACK ch%d=%d not dark", ch, black[ch])
		}
		if white[ch] < 192 {
			return fmt.Errorf("calibration WHITE ch%d=%d not bright", ch, white[ch])
		}
		if mid[ch] < 88 || mid[ch] > 168 {
			return fmt.Errorf("calibration MID ch%d=%d not mid", ch, mid[ch])
		}
	}
	return nil
}

// isIdentity reports the fast-path condition: exactly canonical geometry with every anchor
// within identSnapPx of its canonical point.
func isIdentity(f *Frame, anchors [4][2]float64) bool {
	if f.W != mocappanel.CanvasW || f.H != mocappanel.CanvasH {
		return false
	}
	for i, a := range anchors {
		if math.Abs(a[0]-canonicalAnchors[i][0]) > identSnapPx ||
			math.Abs(a[1]-canonicalAnchors[i][1]) > identSnapPx {
			return false
		}
	}
	return true
}

// Revalidate is the cheap locked-stream check: the five fiducial centres, projected through the
// lock, must still read their expected colours (+-fidTol). A moved/rescaled window fails here
// and triggers a full re-locate.
func (lk Lock) Revalidate(f *Frame) bool {
	targets := targetColors()
	points := [numTargets][2]int{}
	points[tgtTL] = [2]int{mocappanel.AnchorTLX, mocappanel.AnchorTLY}
	m1x, m1y := mocappanel.MetaSample(mocappanel.ColMagic1)
	points[tgtM1] = [2]int{m1x, m1y}
	points[tgtTR] = [2]int{mocappanel.AnchorTRX, mocappanel.AnchorTRY}
	points[tgtBL] = [2]int{mocappanel.AnchorBLX, mocappanel.AnchorBLY}
	points[tgtBR] = [2]int{mocappanel.AnchorBRX, mocappanel.AnchorBRY}
	for t := 0; t < numTargets; t++ {
		r, g, b := sampleNearest(f, lk.H, points[t][0], points[t][1])
		c := targets[t]
		if absDiffU8(r, c[0]) > fidTol || absDiffU8(g, c[1]) > fidTol || absDiffU8(b, c[2]) > fidTol {
			return false
		}
	}
	return true
}

// Sampler returns the mocappanel sampler for this lock: canonical (x,y) inverse-mapped through
// the homography, nearest-neighbour. Identity locks read pixels directly (fast-path). Only the
// 7860 canonical cell centres are ever sampled - no full-frame warp.
func (lk Lock) Sampler(f *Frame) func(x, y int) (r, g, b uint8) {
	if lk.Identity {
		return func(x, y int) (uint8, uint8, uint8) { return f.RGB(x, y) }
	}
	return func(x, y int) (uint8, uint8, uint8) { return sampleNearest(f, lk.H, x, y) }
}

// sampleNearest maps a canonical point through h and reads the nearest frame pixel (clamped).
func sampleNearest(f *Frame, h Homography, x, y int) (r, g, b uint8) {
	fx, fy := h.Apply(float64(x), float64(y))
	xi := clampInt(int(math.Round(fx)), 0, f.W-1)
	yi := clampInt(int(math.Round(fy)), 0, f.H-1)
	return f.RGB(xi, yi)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absDiffU8(a, b uint8) int {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d
}
