// Package mp4meta post-processes MP4 files with a zero-dep box parser (stdlib only).
// Currently: inject the Spherical Video V1 uuid box into the video trak so players
// (VLC/YouTube-class) recognize 360° equirectangular content and enable look-around.
package mp4meta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// SphericalUUID is the Spherical Video V1 box UUID (ffcc8263-f855-4a93-8814-587a02521fdd).
var SphericalUUID = [16]byte{0xff, 0xcc, 0x82, 0x63, 0xf8, 0x55, 0x4a, 0x93, 0x88, 0x14, 0x58, 0x7a, 0x02, 0x52, 0x1f, 0xdd}

// SphericalXML is the V1 RDF payload (equirectangular, stitched by rave-mate).
const SphericalXML = `<?xml version="1.0"?><rdf:SphericalVideo xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:GSpherical="http://ns.google.com/videos/1.0/spherical/"><GSpherical:Spherical>true</GSpherical:Spherical><GSpherical:Stitched>true</GSpherical:Stitched><GSpherical:StitchingSoftware>rave-mate</GSpherical:StitchingSoftware><GSpherical:ProjectionType>equirectangular</GSpherical:ProjectionType></rdf:SphericalVideo>`

// InjectSphericalV1 inserts the Spherical Video V1 uuid box as the last child of the
// video trak, fixing ancestor (trak/moov) sizes and shifting stco/co64 chunk offsets
// that point past the insertion (the +faststart moov-before-mdat layout). Idempotent:
// an already-tagged file is left untouched. Rewrites via temp file + rename.
func InjectSphericalV1(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	tops, err := scanTop(f, st.Size())
	if err != nil {
		return err
	}
	var moov *topBox
	for i := range tops {
		if tops[i].typ == "moov" {
			moov = &tops[i]
		}
	}
	if moov == nil {
		return errors.New("mp4: no moov box")
	}
	buf := make([]byte, moov.size)
	if _, err := f.ReadAt(buf, moov.off); err != nil {
		return err
	}
	patched, err := injectMoov(buf, moov.off)
	if err != nil {
		return err
	}
	if patched == nil {
		return nil // already tagged
	}
	tmp := path + ".sph.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	tail := moov.off + moov.size
	_, err = io.Copy(out, io.NewSectionReader(f, 0, moov.off))
	if err == nil {
		_, err = out.Write(patched)
	}
	if err == nil {
		_, err = io.Copy(out, io.NewSectionReader(f, tail, st.Size()-tail))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = f.Close() // Windows: source must be closed before rename-over
	return os.Rename(tmp, path)
}

// ── box scanning ──

type topBox struct {
	off, size int64
	typ       string
}

// scanTop walks top-level boxes (64-bit largesize aware - mdat can exceed 4 GiB).
func scanTop(r io.ReaderAt, fileSize int64) ([]topBox, error) {
	var out []topBox
	var hdr [16]byte
	for off := int64(0); off < fileSize; {
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hl := int64(8)
		switch size {
		case 0: // box extends to EOF
			size = fileSize - off
		case 1: // 64-bit largesize
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return nil, err
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hl = 16
		}
		if size < hl || off+size > fileSize {
			return nil, fmt.Errorf("mp4: bad box %q size %d at %d", typ, size, off)
		}
		out = append(out, topBox{off, size, typ})
		off += size
	}
	return out, nil
}

type box struct {
	off, size int // relative to the buffer holding the box
	typ       string
}

// children lists child boxes in buf[start:end) (32-bit sizes only - fine inside moov).
func children(buf []byte, start, end int) ([]box, error) {
	var out []box
	for off := start; off < end; {
		if off+8 > end {
			return nil, fmt.Errorf("mp4: truncated box header at %d", off)
		}
		size := int(binary.BigEndian.Uint32(buf[off:]))
		typ := string(buf[off+4 : off+8])
		if size < 8 || off+size > end {
			return nil, fmt.Errorf("mp4: bad box %q size %d at %d", typ, size, off)
		}
		out = append(out, box{off, size, typ})
		off += size
	}
	return out, nil
}

// findChild returns the first direct child of the given type (ok=false if absent).
func findChild(buf []byte, start, end int, typ string) (box, bool) {
	kids, err := children(buf, start, end)
	if err != nil {
		return box{}, false
	}
	for _, k := range kids {
		if k.typ == typ {
			return k, true
		}
	}
	return box{}, false
}

// trakIsVideo reports whether trak's mdia/hdlr handler_type is 'vide'.
func trakIsVideo(moov []byte, trak box) bool {
	mdia, ok := findChild(moov, trak.off+8, trak.off+trak.size, "mdia")
	if !ok {
		return false
	}
	hdlr, ok := findChild(moov, mdia.off+8, mdia.off+mdia.size, "hdlr")
	if !ok || hdlr.size < 20 {
		return false
	}
	// hdlr: 8 header + 4 version/flags + 4 pre_defined + 4 handler_type
	return string(moov[hdlr.off+16:hdlr.off+20]) == "vide"
}

// injectMoov returns a patched moov with the spherical uuid box appended inside the
// video trak, or nil if already tagged. moovOff = moov's absolute file offset (for
// chunk-offset fixups).
func injectMoov(moov []byte, moovOff int64) ([]byte, error) {
	kids, err := children(moov, 8, len(moov))
	if err != nil {
		return nil, err
	}
	var vtrak box
	found := false
	for _, k := range kids {
		if k.typ == "trak" && trakIsVideo(moov, k) {
			vtrak, found = k, true
			break
		}
	}
	if !found {
		return nil, errors.New("mp4: no video trak")
	}
	tk, err := children(moov, vtrak.off+8, vtrak.off+vtrak.size)
	if err != nil {
		return nil, err
	}
	for _, c := range tk {
		if c.typ == "uuid" && c.size >= 24 && bytes.Equal(moov[c.off+8:c.off+24], SphericalUUID[:]) {
			return nil, nil // already tagged
		}
	}
	ub := buildSphericalBox()
	insAt := vtrak.off + vtrak.size // append as trak's last child
	out := make([]byte, 0, len(moov)+len(ub))
	out = append(out, moov[:insAt]...)
	out = append(out, ub...)
	out = append(out, moov[insAt:]...)
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))                  // moov size
	binary.BigEndian.PutUint32(out[vtrak.off:], uint32(vtrak.size+len(ub))) // trak size
	shiftChunkOffsets(out, 8, len(out), moovOff+int64(insAt), int64(len(ub)))
	return out, nil
}

func buildSphericalBox() []byte {
	payload := []byte(SphericalXML)
	b := make([]byte, 0, 24+len(payload))
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(24+len(payload)))
	copy(hdr[4:], "uuid")
	b = append(b, hdr[:]...)
	b = append(b, SphericalUUID[:]...)
	return append(b, payload...)
}

// stco/co64 live under trak/mdia/minf/stbl - the only containers we recurse.
var offsetContainers = map[string]bool{"trak": true, "mdia": true, "minf": true, "stbl": true}

// shiftChunkOffsets adds delta to every stco/co64 entry ≥ insAbs (absolute file offset).
// Offsets before the insertion (mdat-before-moov layout) are untouched.
func shiftChunkOffsets(buf []byte, start, end int, insAbs, delta int64) {
	kids, err := children(buf, start, end)
	if err != nil {
		return
	}
	for _, k := range kids {
		switch {
		case offsetContainers[k.typ]:
			shiftChunkOffsets(buf, k.off+8, k.off+k.size, insAbs, delta)
		case k.typ == "stco" && k.size >= 16:
			n := int(binary.BigEndian.Uint32(buf[k.off+12:]))
			for i := 0; i < n && k.off+16+4*(i+1) <= k.off+k.size; i++ {
				p := k.off + 16 + 4*i
				if v := int64(binary.BigEndian.Uint32(buf[p:])); v >= insAbs {
					binary.BigEndian.PutUint32(buf[p:], uint32(v+delta))
				}
			}
		case k.typ == "co64" && k.size >= 16:
			n := int(binary.BigEndian.Uint32(buf[k.off+12:]))
			for i := 0; i < n && k.off+16+8*(i+1) <= k.off+k.size; i++ {
				p := k.off + 16 + 8*i
				if v := binary.BigEndian.Uint64(buf[p:]); int64(v) >= insAbs {
					binary.BigEndian.PutUint64(buf[p:], v+uint64(delta))
				}
			}
		}
	}
}
