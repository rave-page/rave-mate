package avataratlas

// fbxprops.go - binary FBX (Kaydara) container layer: node records, typed properties,
// zlib-compressed arrays. Versions 7100..7700 (7500+ switched record-header fields from
// uint32 to uint64, null records from 13 to 25 bytes). Purely structural - fbx.go builds the
// semantic Document on top. Malformed input NEVER panics (package contract): every length,
// offset and count is validated against the remaining bytes before use, nesting depth and
// array allocations are capped, zlib output is length-bounded.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	fbxMagic  = "Kaydara FBX Binary  \x00" // 21 bytes; version uint32 LE at offset 23
	fbxVerMin = 7100
	fbxVerMax = 7700

	fbxMaxDepth      = 128     // node nesting cap (real files stay < 10)
	fbxMaxArrayBytes = 1 << 28 // 256 MiB decoded-array cap (zip-bomb guard)
)

// fbxProp is one typed property. Scalars normalize into i/f, arrays into ai/af, strings and
// raw blobs into s - the semantic layer never touches wire typecodes beyond typ.
type fbxProp struct {
	typ byte      // wire typecode: Y C I F D L S R b i l f d
	i   int64     // Y C I L
	f   float64   // F D
	s   []byte    // S R
	ai  []int64   // b i l (b as 0/1)
	af  []float64 // f d
}

// fbxNode is one decoded node record.
type fbxNode struct {
	name     string
	props    []fbxProp
	children []*fbxNode
}

// child returns the first child with the given name (document order), nil if none.
func (n *fbxNode) child(name string) *fbxNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// propInt returns integer property k (Y/C/I/L).
func (n *fbxNode) propInt(k int) (int64, bool) {
	if k < 0 || k >= len(n.props) {
		return 0, false
	}
	switch p := n.props[k]; p.typ {
	case 'Y', 'C', 'I', 'L':
		return p.i, true
	}
	return 0, false
}

// propFloat returns float property k (F/D; integer scalars convert).
func (n *fbxNode) propFloat(k int) (float64, bool) {
	if k < 0 || k >= len(n.props) {
		return 0, false
	}
	switch p := n.props[k]; p.typ {
	case 'F', 'D':
		return p.f, true
	case 'Y', 'C', 'I', 'L':
		return float64(p.i), true
	}
	return 0, false
}

// propString returns string property k (S).
func (n *fbxNode) propString(k int) (string, bool) {
	if k < 0 || k >= len(n.props) || n.props[k].typ != 'S' {
		return "", false
	}
	return string(n.props[k].s), true
}

// propBytes returns raw property k (R; S also accepted - some writers store blobs as S).
func (n *fbxNode) propBytes(k int) ([]byte, bool) {
	if k < 0 || k >= len(n.props) {
		return nil, false
	}
	if p := n.props[k]; p.typ == 'R' || p.typ == 'S' {
		return p.s, true
	}
	return nil, false
}

// propFloats returns array property k (f/d).
func (n *fbxNode) propFloats(k int) ([]float64, bool) {
	if k < 0 || k >= len(n.props) {
		return nil, false
	}
	if p := n.props[k]; p.typ == 'f' || p.typ == 'd' {
		return p.af, true
	}
	return nil, false
}

// propInts returns array property k (b/i/l).
func (n *fbxNode) propInts(k int) ([]int64, bool) {
	if k < 0 || k >= len(n.props) {
		return nil, false
	}
	switch p := n.props[k]; p.typ {
	case 'b', 'i', 'l':
		return p.ai, true
	}
	return nil, false
}

// fbxObjName splits an FBX object-name property ("Name\x00\x01Class") into its name part.
func fbxObjName(s string) string {
	if i := bytes.Index([]byte(s), []byte{0, 1}); i >= 0 {
		return s[:i]
	}
	return s
}

// isASCIIFBX sniffs an ASCII FBX file (text format, typically starting "; FBX x.y.z project").
func isASCIIFBX(data []byte) bool {
	head := data
	if len(head) > 256 {
		head = head[:256]
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte(";")) && bytes.Contains(head, []byte("FBX"))
}

// parseFBXTree decodes the container into top-level nodes.
func parseFBXTree(data []byte) (version uint32, roots []*fbxNode, err error) {
	if !bytes.HasPrefix(data, []byte(fbxMagic)) {
		if isASCIIFBX(data) {
			return 0, nil, fmt.Errorf("fbx: ASCII FBX unsupported - export binary FBX")
		}
		return 0, nil, fmt.Errorf("fbx: not a binary FBX (bad Kaydara magic)")
	}
	if len(data) < 27 {
		return 0, nil, fmt.Errorf("fbx: truncated header (%d bytes)", len(data))
	}
	version = binary.LittleEndian.Uint32(data[23:])
	if version < fbxVerMin || version > fbxVerMax {
		return 0, nil, fmt.Errorf("fbx: version %d unsupported (want %d..%d)", version, fbxVerMin, fbxVerMax)
	}
	big := version >= 7500
	pos := 27
	for {
		node, next, err := parseFBXNode(data, pos, big, 0)
		if err != nil {
			return 0, nil, err
		}
		if node == nil { // null record = end of top-level list (footer follows, ignored)
			break
		}
		roots = append(roots, node)
		pos = next
	}
	return version, roots, nil
}

// nullRecLen is the size of an all-zero terminator record.
func nullRecLen(big bool) int {
	if big {
		return 25 // 3*uint64 + nameLen byte
	}
	return 13 // 3*uint32 + nameLen byte
}

// parseFBXNode decodes one node record at pos. Returns (nil, posAfterNull, nil) for a null
// terminator record.
func parseFBXNode(data []byte, pos int, big bool, depth int) (*fbxNode, int, error) {
	if depth > fbxMaxDepth {
		return nil, 0, fmt.Errorf("fbx: node nesting exceeds %d", fbxMaxDepth)
	}
	hdr := nullRecLen(big)
	if pos < 0 || pos+hdr > len(data) {
		return nil, 0, fmt.Errorf("fbx: truncated node header at %d", pos)
	}
	var endOffset, numProps, propListLen uint64
	if big {
		endOffset = binary.LittleEndian.Uint64(data[pos:])
		numProps = binary.LittleEndian.Uint64(data[pos+8:])
		propListLen = binary.LittleEndian.Uint64(data[pos+16:])
	} else {
		endOffset = uint64(binary.LittleEndian.Uint32(data[pos:]))
		numProps = uint64(binary.LittleEndian.Uint32(data[pos+4:]))
		propListLen = uint64(binary.LittleEndian.Uint32(data[pos+8:]))
	}
	nameLen := int(data[pos+hdr-1])
	if endOffset == 0 && numProps == 0 && propListLen == 0 && nameLen == 0 {
		return nil, pos + hdr, nil // null record
	}
	if endOffset <= uint64(pos) || endOffset > uint64(len(data)) {
		return nil, 0, fmt.Errorf("fbx: node at %d: endOffset %d out of range", pos, endOffset)
	}
	end := int(endOffset)
	cur := pos + hdr
	if cur+nameLen > end {
		return nil, 0, fmt.Errorf("fbx: node at %d: name overruns record", pos)
	}
	node := &fbxNode{name: string(data[cur : cur+nameLen])}
	cur += nameLen
	if propListLen > uint64(end-cur) {
		return nil, 0, fmt.Errorf("fbx: node %q: property list %d overruns record", node.name, propListLen)
	}
	// each property is >= 2 bytes (typecode + at least 1 payload byte)
	if numProps > propListLen {
		return nil, 0, fmt.Errorf("fbx: node %q: %d properties in %d bytes", node.name, numProps, propListLen)
	}
	props, err := parseFBXProps(data[cur:cur+int(propListLen)], int(numProps))
	if err != nil {
		return nil, 0, fmt.Errorf("fbx: node %q: %w", node.name, err)
	}
	node.props = props
	cur += int(propListLen)

	// Nested list: children terminated by a null record. Writers omit the list entirely
	// (cur == end) for leaf nodes with properties.
	for cur < end {
		if end-cur == nullRecLen(big) {
			child, next, err := parseFBXNode(data, cur, big, depth+1)
			if err != nil {
				return nil, 0, err
			}
			if child != nil {
				return nil, 0, fmt.Errorf("fbx: node %q: child record overruns nested list", node.name)
			}
			cur = next
			break
		}
		child, next, err := parseFBXNode(data, cur, big, depth+1)
		if err != nil {
			return nil, 0, err
		}
		if child == nil {
			return nil, 0, fmt.Errorf("fbx: node %q: early null record in nested list", node.name)
		}
		if next <= cur { // monotonic progress guard (endOffset validated > pos already)
			return nil, 0, fmt.Errorf("fbx: node %q: non-advancing child record", node.name)
		}
		node.children = append(node.children, child)
		cur = next
	}
	if cur != end {
		return nil, 0, fmt.Errorf("fbx: node %q: nested list ends at %d, record at %d", node.name, cur, end)
	}
	return node, end, nil
}

// fbxElemSize maps array typecodes to element byte width.
func fbxElemSize(t byte) int {
	switch t {
	case 'b':
		return 1
	case 'i', 'f':
		return 4
	case 'l', 'd':
		return 8
	}
	return 0
}

// parseFBXProps decodes n properties from a bounded property region.
func parseFBXProps(b []byte, n int) ([]fbxProp, error) {
	props := make([]fbxProp, 0, n)
	cur := 0
	need := func(k int) error {
		if cur+k > len(b) {
			return fmt.Errorf("property %d truncated", len(props))
		}
		return nil
	}
	for len(props) < n {
		if err := need(1); err != nil {
			return nil, err
		}
		t := b[cur]
		cur++
		p := fbxProp{typ: t}
		switch t {
		case 'Y':
			if err := need(2); err != nil {
				return nil, err
			}
			p.i = int64(int16(binary.LittleEndian.Uint16(b[cur:])))
			cur += 2
		case 'C':
			if err := need(1); err != nil {
				return nil, err
			}
			p.i = int64(b[cur] & 1)
			cur++
		case 'I':
			if err := need(4); err != nil {
				return nil, err
			}
			p.i = int64(int32(binary.LittleEndian.Uint32(b[cur:])))
			cur += 4
		case 'F':
			if err := need(4); err != nil {
				return nil, err
			}
			p.f = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[cur:])))
			cur += 4
		case 'D':
			if err := need(8); err != nil {
				return nil, err
			}
			p.f = math.Float64frombits(binary.LittleEndian.Uint64(b[cur:]))
			cur += 8
		case 'L':
			if err := need(8); err != nil {
				return nil, err
			}
			p.i = int64(binary.LittleEndian.Uint64(b[cur:]))
			cur += 8
		case 'S', 'R':
			if err := need(4); err != nil {
				return nil, err
			}
			l := int64(binary.LittleEndian.Uint32(b[cur:]))
			cur += 4
			if l > int64(len(b)-cur) {
				return nil, fmt.Errorf("property %d: string/raw length %d overruns record", len(props), l)
			}
			p.s = b[cur : cur+int(l)]
			cur += int(l)
		case 'b', 'i', 'l', 'f', 'd':
			if err := need(12); err != nil {
				return nil, err
			}
			count := int64(binary.LittleEndian.Uint32(b[cur:]))
			encoding := binary.LittleEndian.Uint32(b[cur+4:])
			compLen := int64(binary.LittleEndian.Uint32(b[cur+8:]))
			cur += 12
			elem := int64(fbxElemSize(t))
			rawLen := count * elem
			if rawLen > fbxMaxArrayBytes {
				return nil, fmt.Errorf("property %d: array of %d bytes exceeds %d cap", len(props), rawLen, fbxMaxArrayBytes)
			}
			if compLen > int64(len(b)-cur) {
				return nil, fmt.Errorf("property %d: array payload %d overruns record", len(props), compLen)
			}
			var raw []byte
			switch encoding {
			case 0:
				if compLen < rawLen {
					return nil, fmt.Errorf("property %d: raw array payload %d < %d needed", len(props), compLen, rawLen)
				}
				raw = b[cur : cur+int(rawLen)]
			case 1:
				zr, err := zlib.NewReader(bytes.NewReader(b[cur : cur+int(compLen)]))
				if err != nil {
					return nil, fmt.Errorf("property %d: zlib: %w", len(props), err)
				}
				raw = make([]byte, rawLen)
				if _, err := io.ReadFull(zr, raw); err != nil {
					zr.Close()
					return nil, fmt.Errorf("property %d: zlib: %w", len(props), err)
				}
				zr.Close()
			default:
				return nil, fmt.Errorf("property %d: array encoding %d unsupported", len(props), encoding)
			}
			cur += int(compLen)
			switch t {
			case 'b':
				p.ai = make([]int64, count)
				for e := range p.ai {
					p.ai[e] = int64(raw[e] & 1)
				}
			case 'i':
				p.ai = make([]int64, count)
				for e := range p.ai {
					p.ai[e] = int64(int32(binary.LittleEndian.Uint32(raw[e*4:])))
				}
			case 'l':
				p.ai = make([]int64, count)
				for e := range p.ai {
					p.ai[e] = int64(binary.LittleEndian.Uint64(raw[e*8:]))
				}
			case 'f':
				p.af = make([]float64, count)
				for e := range p.af {
					p.af[e] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[e*4:])))
				}
			case 'd':
				p.af = make([]float64, count)
				for e := range p.af {
					p.af[e] = math.Float64frombits(binary.LittleEndian.Uint64(raw[e*8:]))
				}
			}
		default:
			return nil, fmt.Errorf("property %d: unknown typecode %q", len(props), t)
		}
		props = append(props, p)
	}
	return props, nil
}
