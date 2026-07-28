// Package framedebug gives every media-pipeline stage a deterministic content oracle: a hash that
// answers "when did the PICTURE last change", plus on-demand PNG capture of the actual frames.
//
// Why it exists: a 4K route shipped a bit-identical frame for 48 minutes while fps:58.5,
// capStaleMs:16, dropped:0 and encFails:0 all read healthy, because every counter we had timed OUR
// tick rather than the content (#58). Diagnosing it took a throwaway probe and hours. A stage that
// stops changing must be visible as a number, and the frames themselves must be dumpable.
//
// Content hash, not byte volume: an encoded 4K frame stays 3-5 kB whether the picture moves or not
// (keyframes alone account for that), and a synthetic pattern compresses so well that a MOVING 720p
// source measured 184 B/AU. Volume cannot see a freeze; a hash can.
package framedebug

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// sampleStride walks the pixel buffer sparsely. Co-prime with every plausible row width, so it
	// drifts across columns instead of sampling one column forever - a full hash of 33 MB at 60 fps
	// is waste, a single-column hash is blind to a change one column wide.
	sampleStride = 997
	// maxShots bounds a tap: this is a debug capture, not a recorder. Arm() past the cap is refused
	// rather than silently trimmed (a truncated capture reads as a complete one).
	maxShots = 16
	// defaultScale downsamples a dump by 4 (4K -> 960x540). Viewable, and a 33 MB frame is not a
	// 30 MB PNG. Crop overrides it: reading small in-frame text needs full resolution.
	defaultScale = 4
)

// Stats is one stage's content verdict. StalledMs is the age of the last CHANGE, not of the last
// frame - that distinction is the whole point: a frozen source still delivers frames on time.
type Stats struct {
	Frames    uint64 // frames observed
	Changes   uint64 // frames whose hash differed from the one before
	Hash      uint64 // last content hash
	W, H      int
	StalledMs int64 // since the content last changed (-1 = nothing observed yet)
	Shots     int   // captures currently held
	Armed     int   // captures still pending
	// MovedFrac is the fraction of the picture that changed on the last frame that changed at all,
	// and PeakFrac the largest ever seen. A boolean "it changed" is not enough: a desktop capture
	// whose only live element is a clock changes constantly and still looks like a still image.
	// Under staticFrac the picture is static-with-a-live-element, not moving footage.
	MovedFrac float64
	PeakFrac  float64
}

// StaticFrac is the ceiling under which a changing picture is still, for practical purposes, a
// static image with a small live element (a clock, a VU meter, a timer). Measured: a frozen 4K
// desktop capture with a ticking tray clock moved 0.5% of the picture; a live 720p webcam through
// the same path moved 6.5%. The gap is an order of magnitude, so the threshold sits between them.
const StaticFrac = 0.02

// Static reports a picture that changes but does not really move.
func (s Stats) Static() bool { return s.Frames > 0 && s.Changes > 0 && s.PeakFrac < StaticFrac }

// Hash is a deterministic sparse content hash: same pixels -> same value, across processes and runs.
// FNV-1a inlined over the sampled bytes (no allocation, no hash.Hash interface dispatch per frame).
func Hash(pix []byte) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(pix); i += sampleStride {
		h = (h ^ uint64(pix[i])) * 1099511628211
	}
	return h
}

// Sample hashes and records the sampled bytes in one pass. dst is grown as needed and
// returned, so a steady stream reuses the same backing array.
func Sample(pix, dst []byte) ([]byte, uint64) {
	n := (len(pix) + sampleStride - 1) / sampleStride
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	h := uint64(14695981039346656037)
	for i, j := 0, 0; i < len(pix); i, j = i+sampleStride, j+1 {
		b := pix[i]
		dst[j] = b
		h = (h ^ uint64(b)) * 1099511628211
	}
	return dst, h
}

// DiffFrac is the fraction of sampled bytes that differ. This is the number a boolean "changed"
// cannot give, and the difference is not academic: a 4K desktop capture whose ONLY moving element is
// a clock reports "changed" on ~3 frames a second while looking, to a human and to Resolume, like a
// still image. 0.5% moving and 6% moving are different findings and must not share a verdict.
func DiffFrac(a, b []byte) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}
	diff := 0
	for i := range n {
		if a[i] != b[i] {
			diff++
		}
	}
	return float64(diff) / float64(n)
}

// Shot is one captured frame on disk.
type Shot struct {
	Seq  int
	At   time.Time
	W, H int
	Hash uint64
	Path string
}

// Recorder tracks one named stage. Safe for concurrent use; Frame is on the hot path and takes the
// lock only to update scalars (the PNG encode happens off-lock).
type Recorder struct {
	name string

	mu         sync.Mutex
	frames     uint64
	changes    uint64
	hash       uint64
	haveHash   bool
	w, h       int
	lastChange time.Time
	armed      int
	scale      int
	crop       image.Rectangle
	seq        int
	shots      []Shot
	sample     []byte // sampled bytes of the last frame (magnitude needs the previous frame, not just its hash)
	spare      []byte // the frame-before-last's buffer, recycled
	movedFrac  float64
	peakFrac   float64
}

var (
	regMu sync.Mutex
	reg   = map[string]*Recorder{}
	dir   = filepath.Join(os.TempDir(), "rave-mate-frames")
)

// For returns the recorder for a stage, creating it on first use. Stage names are pipeline
// positions: "src" (what the sender's texture holds), "enc" (encoder input), "dec" (decoded on the
// receiver), "out" (what the republished Spout sender exposes).
func For(stage string) *Recorder {
	regMu.Lock()
	defer regMu.Unlock()
	if r := reg[stage]; r != nil {
		return r
	}
	r := &Recorder{name: stage, scale: defaultScale}
	reg[stage] = r
	return r
}

// SetDir points captures at a directory (the daemon uses its config dir). Created on demand.
func SetDir(d string) {
	regMu.Lock()
	defer regMu.Unlock()
	if d != "" {
		dir = d
	}
}

// Dir reports where captures land.
func Dir() string {
	regMu.Lock()
	defer regMu.Unlock()
	return dir
}

// Names lists the stages seen so far, sorted for stable output.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Snapshot reports every stage's stats in one pass - the "which stage stopped changing" answer.
func Snapshot() map[string]Stats {
	regMu.Lock()
	rs := make([]*Recorder, 0, len(reg))
	for _, r := range reg {
		rs = append(rs, r)
	}
	regMu.Unlock()
	out := make(map[string]Stats, len(rs))
	for _, r := range rs {
		out[r.name] = r.Stats()
	}
	return out
}

// Arm requests the next n frames be written as PNGs. scale <= 0 uses the default; a non-empty crop
// is taken at FULL resolution (small in-frame text does not survive a downsample).
func (r *Recorder) Arm(n, scale int, crop image.Rectangle) error {
	if n <= 0 || n > maxShots {
		return fmt.Errorf("framedebug: arm %d frames: want 1..%d", n, maxShots)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return fmt.Errorf("framedebug: capture dir: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = n
	r.scale = scale
	if r.scale <= 0 {
		r.scale = defaultScale
	}
	r.crop = crop
	return nil
}

// Frame observes one frame: stall bookkeeping always, a PNG only while armed. img.Pix must stay
// valid for the call (the sink's pooled buffer is fine - the encode happens before Frame returns).
func (r *Recorder) Frame(img *image.NRGBA) {
	if img == nil {
		return
	}
	now := time.Now()

	r.mu.Lock()
	next, h := Sample(img.Pix, r.scratch())
	r.frames++
	if !r.haveHash || h != r.hash {
		if r.haveHash {
			r.changes++
			if f := DiffFrac(next, r.sample); f > 0 {
				r.movedFrac = f
				r.peakFrac = max(r.peakFrac, f)
			}
		}
		r.haveHash = true
		r.lastChange = now
	}
	r.spare, r.sample = r.sample, next
	r.hash = h
	r.w, r.h = img.Rect.Dx(), img.Rect.Dy()
	shoot := r.armed > 0
	var scale, seq int
	var crop image.Rectangle
	if shoot {
		r.armed--
		r.seq++
		scale, crop, seq = r.scale, r.crop, r.seq
	}
	r.mu.Unlock()

	if !shoot {
		return
	}
	path := filepath.Join(Dir(), fmt.Sprintf("%s-%03d.png", r.name, seq))
	if err := writePNG(path, img, scale, crop); err != nil {
		return // a failed dump must not disturb the pipeline; Shots() simply won't list it
	}
	r.mu.Lock()
	if len(r.shots) >= maxShots {
		r.shots = r.shots[1:] // bounded ring: drop-oldest
	}
	r.shots = append(r.shots, Shot{Seq: seq, At: now, W: r.w, H: r.h, Hash: h, Path: path})
	r.mu.Unlock()
}

// Stats reports this stage's verdict.
func (r *Recorder) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Stats{Frames: r.frames, Changes: r.changes, Hash: r.hash, W: r.w, H: r.h,
		StalledMs: -1, Shots: len(r.shots), Armed: r.armed,
		MovedFrac: r.movedFrac, PeakFrac: r.peakFrac}
	if r.haveHash {
		s.StalledMs = time.Since(r.lastChange).Milliseconds()
	}
	return s
}

// Shots lists the captures held for this stage, oldest first.
func (r *Recorder) Shots() []Shot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Shot(nil), r.shots...)
}

// EncodePNG renders a frame the same way a capture does and returns the PNG bytes, for callers that
// ship it over a wire instead of to disk (a frame captured on a PEER machine).
func EncodePNG(src *image.NRGBA, scale int, crop image.Rectangle) ([]byte, error) {
	if src == nil {
		return nil, fmt.Errorf("framedebug: no frame")
	}
	if scale <= 0 {
		scale = defaultScale
	}
	out, err := render(src, scale, crop)
	if err != nil {
		return nil, err
	}
	var buf bytesBuffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.b, nil
}

// bytesBuffer is a minimal io.Writer sink - bytes.Buffer would pull the whole package in for one
// append.
type bytesBuffer struct{ b []byte }

func (w *bytesBuffer) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// writePNG downsamples by scale, or takes a full-resolution crop when one is set.
func writePNG(path string, src *image.NRGBA, scale int, crop image.Rectangle) error {
	out, err := render(src, scale, crop)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

// render downsamples by scale, or takes a full-resolution crop when one is set.
func render(src *image.NRGBA, scale int, crop image.Rectangle) (*image.NRGBA, error) {
	var out *image.NRGBA
	if !crop.Empty() {
		c := crop.Intersect(src.Rect)
		if c.Empty() {
			return nil, fmt.Errorf("framedebug: crop %v outside frame %v", crop, src.Rect)
		}
		out = image.NewNRGBA(image.Rect(0, 0, c.Dx(), c.Dy()))
		for y := 0; y < c.Dy(); y++ {
			si := (c.Min.Y+y)*src.Stride + c.Min.X*4
			copy(out.Pix[y*out.Stride:][:c.Dx()*4], src.Pix[si:si+c.Dx()*4])
		}
	} else {
		w, h := src.Rect.Dx()/scale, src.Rect.Dy()/scale
		if w < 1 || h < 1 {
			w, h = src.Rect.Dx(), src.Rect.Dy()
			scale = 1
		}
		out = image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				si := y*scale*src.Stride + x*scale*4
				if si+3 >= len(src.Pix) {
					continue
				}
				copy(out.Pix[y*out.Stride+x*4:][:4], src.Pix[si:si+4])
			}
		}
	}
	// Opaque: a decoded frame's alpha is whatever the source left there, and a 0-alpha dump renders
	// as an empty image in every viewer - the exact failure this package exists to make visible.
	for i := 3; i < len(out.Pix); i += 4 {
		out.Pix[i] = 255
	}
	return out, nil
}

// scratch hands Frame a buffer to sample into without clobbering the previous frame's samples, which
// the magnitude comparison still needs. Caller holds mu. Two buffers alternate, so a steady stream
// allocates nothing after the first two frames.
func (r *Recorder) scratch() []byte {
	if cap(r.spare) == 0 {
		return nil
	}
	out := r.spare
	r.spare = r.sample
	return out
}
