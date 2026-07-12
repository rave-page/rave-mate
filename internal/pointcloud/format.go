package pointcloud

// RMPC ("rave-mate point cloud") container - a compact animated point cloud the future
// rave.page web/VR viewer loads. Layout (all integers little-endian):
//
//	"RMPC"                 4-byte magic
//	version                uint16
//	headerLen              uint32
//	header                 headerLen bytes of JSON (Header)
//	colors                 PointCount*3 uint8 RGB    (only if header.HasColor; frame-invariant)
//	frames                 FrameCount blocks, each PointCount*3 uint16 positions
//
// Positions are fixed-point within header.Bounds: q = round((p-min)/(max-min) * 65535),
// so each frame is exactly PointCount*6 bytes -> O(1) seek to frame i. Dequant on the
// viewer: p = min + (q/65535)*(max-min). Colour is stored ONCE (albedo doesn't change with
// pose). Byte stream is gzip-friendly on the wire while staying per-frame random-accessible
// on disk. See .devnotes/POINTCLOUD_FORMAT.md.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// Magic is the 4-byte file signature.
	Magic = "RMPC"
	// Version is the current container version.
	Version = 1
	// QuantMax is the fixed-point range (uint16).
	QuantMax = 0xFFFF
)

// Header is the JSON file header. Version is stamped by the encoder.
type Header struct {
	Version    int    `json:"version"`
	Generator  string `json:"generator"`
	Source     string `json:"source,omitempty"` // take name
	Created    string `json:"created"`          // RFC3339
	FPS        int    `json:"fps"`
	FrameCount int    `json:"frame_count"`
	PointCount int    `json:"point_count"` // constant per frame
	HasColor   bool   `json:"has_color"`
	Bounds     Bounds `json:"bounds"`
	QuantBits  int    `json:"quant_bits"` // 16
}

// Encoder streams RMPC to w. Construct with NewEncoder, then WriteFrame FrameCount times.
type Encoder struct {
	w    io.Writer
	hdr  Header
	ext  [3]float32 // max-min per axis (0 => degenerate axis, all points quantize to 0)
	fbuf []byte     // reused per-frame position buffer
}

// NewEncoder writes the magic, header and (if HasColor) the frame-invariant colour block.
// colors must be nil or exactly PointCount*3 bytes. Version/QuantBits are stamped here.
func NewEncoder(w io.Writer, h Header, colors []byte) (*Encoder, error) {
	if h.PointCount <= 0 {
		return nil, errors.New("pointcloud: PointCount must be > 0")
	}
	if h.HasColor && len(colors) != h.PointCount*3 {
		return nil, fmt.Errorf("pointcloud: colors len %d, want %d", len(colors), h.PointCount*3)
	}
	h.Version = Version
	h.QuantBits = 16
	hb, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, Magic); err != nil {
		return nil, err
	}
	var head [6]byte
	binary.LittleEndian.PutUint16(head[0:2], uint16(h.Version))
	binary.LittleEndian.PutUint32(head[2:6], uint32(len(hb)))
	if _, err := w.Write(head[:]); err != nil {
		return nil, err
	}
	if _, err := w.Write(hb); err != nil {
		return nil, err
	}
	if h.HasColor {
		if _, err := w.Write(colors); err != nil {
			return nil, err
		}
	}
	e := &Encoder{w: w, hdr: h, fbuf: make([]byte, h.PointCount*6)}
	for i := 0; i < 3; i++ {
		e.ext[i] = h.Bounds.Max[i] - h.Bounds.Min[i]
	}
	return e, nil
}

// WriteFrame quantizes and writes one frame; len(pos) must equal PointCount.
func (e *Encoder) WriteFrame(pos [][3]float32) error {
	if len(pos) != e.hdr.PointCount {
		return fmt.Errorf("pointcloud: frame has %d points, want %d", len(pos), e.hdr.PointCount)
	}
	min := e.hdr.Bounds.Min
	for i, p := range pos {
		o := i * 6
		for a := 0; a < 3; a++ {
			var q uint16
			if e.ext[a] > 0 {
				f := (p[a] - min[a]) / e.ext[a]
				q = quant(f)
			}
			binary.LittleEndian.PutUint16(e.fbuf[o+a*2:o+a*2+2], q)
		}
	}
	_, err := e.w.Write(e.fbuf)
	return err
}

// quant maps f (expected 0..1) to a clamped uint16.
func quant(f float32) uint16 {
	v := int(f*float32(QuantMax) + 0.5)
	if v < 0 {
		return 0
	}
	if v > QuantMax {
		return QuantMax
	}
	return uint16(v)
}

// Decoder reads an RMPC stream (round-trip verification + tooling). Header + Colors are read
// on construction; ReadFrame yields dequantized world-space positions.
type Decoder struct {
	r      *bufio.Reader
	hdr    Header
	colors []byte
	ext    [3]float32
	raw    []byte // reused per-frame read buffer
}

// NewDecoder reads magic, header and the colour block.
func NewDecoder(r io.Reader) (*Decoder, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, err
	}
	if string(magic) != Magic {
		return nil, fmt.Errorf("pointcloud: bad magic %q", magic)
	}
	var head [6]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return nil, err
	}
	ver := binary.LittleEndian.Uint16(head[0:2])
	if ver != Version {
		return nil, fmt.Errorf("pointcloud: unsupported version %d", ver)
	}
	hlen := binary.LittleEndian.Uint32(head[2:6])
	hb := make([]byte, hlen)
	if _, err := io.ReadFull(br, hb); err != nil {
		return nil, err
	}
	var h Header
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, err
	}
	d := &Decoder{r: br, hdr: h, raw: make([]byte, h.PointCount*6)}
	for i := 0; i < 3; i++ {
		d.ext[i] = h.Bounds.Max[i] - h.Bounds.Min[i]
	}
	if h.HasColor {
		d.colors = make([]byte, h.PointCount*3)
		if _, err := io.ReadFull(br, d.colors); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Header returns the decoded header.
func (d *Decoder) Header() Header { return d.hdr }

// Colors returns the frame-invariant RGB block (nil when HasColor is false).
func (d *Decoder) Colors() []byte { return d.colors }

// ReadFrame reads and dequantizes the next frame; io.EOF after the last frame.
func (d *Decoder) ReadFrame() ([][3]float32, error) {
	if _, err := io.ReadFull(d.r, d.raw); err != nil {
		return nil, err
	}
	min := d.hdr.Bounds.Min
	out := make([][3]float32, d.hdr.PointCount)
	for i := range out {
		o := i * 6
		for a := 0; a < 3; a++ {
			q := binary.LittleEndian.Uint16(d.raw[o+a*2 : o+a*2+2])
			out[i][a] = min[a] + float32(q)/float32(QuantMax)*d.ext[a]
		}
	}
	return out, nil
}
