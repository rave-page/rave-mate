package medialink

import (
	"encoding/binary"
	"errors"
)

// Wire header: fixed 26 bytes, big-endian, followed by the payload. This is the AEAD *plaintext*;
// transport.go seals (header||payload) per frame.
//
//	off size field
//	 0   2  Stream (uint16)
//	 2   1  Kind
//	 3   1  Codec
//	 4   1  Flags
//	 5   1  TCRate  (0=no timecode; else nominal fps in bits0-6, drop in bit7)
//	 6   4  Seq (uint32)
//	10   4  TCFrames (int32, absolute SMPTE frame index; only if TCRate!=0)
//	14   8  PTS (int64, ns)
//	22   4  PayloadLen (uint32)
const headerLen = 26

// maxPayload caps a single frame so a corrupt/hostile length can't force a huge alloc. A 4K BGRA
// keyframe (~33 MiB) is the practical ceiling for uncompressed same-PC frames; encoded frames are
// far smaller.
const maxPayload = 48 << 20

var (
	errShortHeader = errors.New("medialink: short frame header")
	errBadPayload  = errors.New("medialink: payload length out of range")
)

// tcRateByte packs a Rate into the wire byte (0 = none).
func tcRateByte(r Rate) byte {
	if r.Nominal == 0 {
		return 0
	}
	b := byte(r.Nominal & 0x7f)
	if r.Drop {
		b |= 0x80
	}
	return b
}

// rateFromByte reverses tcRateByte.
func rateFromByte(b byte) Rate {
	if b == 0 {
		return Rate{}
	}
	return Rate{Nominal: int(b & 0x7f), Drop: b&0x80 != 0}
}

// marshal appends the frame's wire bytes (header + payload) to dst.
func (f *Frame) marshal(dst []byte) []byte {
	var h [headerLen]byte
	binary.BigEndian.PutUint16(h[0:], f.Stream)
	h[2] = byte(f.Kind)
	h[3] = byte(f.Codec)
	h[4] = byte(f.Flags)
	h[5] = tcRateByte(f.TC.Rate)
	binary.BigEndian.PutUint32(h[6:], f.Seq)
	tcFrames := int32(0)
	if !f.TC.Zero() {
		tcFrames = int32(f.TC.Frames())
	}
	binary.BigEndian.PutUint32(h[10:], uint32(tcFrames))
	binary.BigEndian.PutUint64(h[14:], uint64(f.PTS))
	binary.BigEndian.PutUint32(h[22:], uint32(len(f.Payload)))
	dst = append(dst, h[:]...)
	return append(dst, f.Payload...)
}

// parseFrame decodes a full frame plaintext (header + payload). The payload is copied out of buf.
func parseFrame(buf []byte) (*Frame, error) {
	if len(buf) < headerLen {
		return nil, errShortHeader
	}
	plen := binary.BigEndian.Uint32(buf[22:])
	if plen > maxPayload || int(plen) != len(buf)-headerLen {
		return nil, errBadPayload
	}
	f := &Frame{
		Stream: binary.BigEndian.Uint16(buf[0:]),
		Kind:   Kind(buf[2]),
		Codec:  Codec(buf[3]),
		Flags:  Flag(buf[4]),
		Seq:    binary.BigEndian.Uint32(buf[6:]),
		PTS:    int64(binary.BigEndian.Uint64(buf[14:])),
	}
	if rate := rateFromByte(buf[5]); rate.Nominal != 0 {
		f.TC = TimecodeFromFrames(int64(int32(binary.BigEndian.Uint32(buf[10:]))), rate)
	}
	if plen > 0 {
		f.Payload = append([]byte(nil), buf[headerLen:]...)
	}
	return f, nil
}
