// Package artnet is a pure-stdlib Art-Net 4 DMX plane: a UDP listener that ingests ArtDmx
// into a thread-safe universe store + answers ArtPoll for console discovery, and an emitter
// that builds spec-correct ArtDmx (44Hz rate-cap + change-detection + 1Hz keep-alive).
//
// Wire formats are verified against the Art-Net 4 spec (see DMX_TIMECODE_RESEARCH.md). No deps.
package artnet

import (
	"encoding/binary"
	"errors"
	"net"
)

// Art-Net wire constants.
const (
	// Port is the fixed Art-Net UDP port (0x1936).
	Port = 6454

	protVer = 14 // Art-Net protocol revision (ProtVerLo)

	// OpCodes (transmitted little-endian on the wire).
	opDmx        = 0x5000 // ArtDmx
	opPoll       = 0x2000 // ArtPoll
	opPollReply  = 0x2100 // ArtPollReply
	artDmxHeader = 18     // ArtDmx bytes before the DMX payload
	maxDMX       = 512

	// esta is the ESTA manufacturer code advertised in ArtPollReply (0x7FF0 = experimental range).
	esta = 0x7FF0
)

// artID is the null-terminated Art-Net packet magic.
var artID = [8]byte{'A', 'r', 't', '-', 'N', 'e', 't', 0}

// ErrNotArtNet is returned when a datagram lacks the Art-Net magic or carries the wrong opcode.
var ErrNotArtNet = errors.New("artnet: bad magic")

// opcodeOf returns the (little-endian) OpCode of a packet, or 0 if it isn't Art-Net.
func opcodeOf(p []byte) uint16 {
	if len(p) < 10 || [8]byte(p[0:8]) != artID {
		return 0
	}
	return binary.LittleEndian.Uint16(p[8:10])
}

// ArtDmx is a parsed ArtDmx packet.
type ArtDmx struct {
	Sequence byte
	Physical byte
	Universe uint16 // 15-bit Port-Address (Net<<8 | SubUni)
	Data     []byte // DMX slots, len ≤ 512
}

// ParseArtDmx decodes an ArtDmx datagram. Returns ErrNotArtNet for non-Art-Net / wrong opcode
// and a descriptive error for a malformed but Art-Net-tagged packet.
func ParseArtDmx(p []byte) (ArtDmx, error) {
	if opcodeOf(p) != opDmx {
		return ArtDmx{}, ErrNotArtNet
	}
	if len(p) < artDmxHeader {
		return ArtDmx{}, errors.New("artnet: short ArtDmx header")
	}
	n := int(binary.BigEndian.Uint16(p[16:18])) // Length is big-endian in ArtDmx
	if n > maxDMX {
		return ArtDmx{}, errors.New("artnet: DMX length > 512")
	}
	if len(p) < artDmxHeader+n {
		return ArtDmx{}, errors.New("artnet: truncated DMX payload")
	}
	uni := uint16(p[15])<<8 | uint16(p[14]) // Net (hi) | SubUni (lo)
	data := make([]byte, n)
	copy(data, p[artDmxHeader:artDmxHeader+n])
	return ArtDmx{Sequence: p[12], Physical: p[13], Universe: uni & 0x7FFF, Data: data}, nil
}

// BuildArtDmx builds a spec-correct ArtDmx packet for a 15-bit universe. data is clamped to 512;
// an odd slot count is padded to even per the spec (min 2 slots).
func BuildArtDmx(universe uint16, seq, physical byte, data []byte) []byte {
	if len(data) > maxDMX {
		data = data[:maxDMX]
	}
	n := len(data)
	if n%2 == 1 { // spec: slot count must be even
		n++
	}
	if n == 0 {
		n = 2
	}
	p := make([]byte, artDmxHeader+n)
	copy(p[0:8], artID[:])
	binary.LittleEndian.PutUint16(p[8:10], opDmx)
	p[10] = 0 // ProtVerHi
	p[11] = protVer
	p[12] = seq
	p[13] = physical
	p[14] = byte(universe & 0xFF) // SubUni (low)
	p[15] = byte(universe >> 8)   // Net (high)
	binary.BigEndian.PutUint16(p[16:18], uint16(n))
	copy(p[artDmxHeader:], data)
	return p
}

// IsArtPoll reports whether p is an ArtPoll (discovery request).
func IsArtPoll(p []byte) bool { return opcodeOf(p) == opPoll }

// BuildArtPollReply builds a minimal ArtPollReply advertising this node so consoles discover us.
// ip is our bound IPv4; short ≤17 chars, long ≤63 chars. inUniverses are the 15-bit port-addresses
// we ingest (first 4 advertised as input ports).
func BuildArtPollReply(ip net.IP, short, long string, inUniverses []uint16) []byte {
	const size = 239
	p := make([]byte, size)
	copy(p[0:8], artID[:])
	binary.LittleEndian.PutUint16(p[8:10], opPollReply)
	if v4 := ip.To4(); v4 != nil {
		copy(p[10:14], v4)
	}
	binary.LittleEndian.PutUint16(p[14:16], Port) // Port (low byte first)
	p[16] = 0                                     // VersInfoHi
	p[17] = protVer                               // VersInfoLo
	// NetSwitch/SubSwitch (18/19) from the first advertised universe's high bits.
	if len(inUniverses) > 0 {
		p[18] = byte(inUniverses[0] >> 8 & 0x7F) // Net
		p[19] = byte(inUniverses[0] >> 4 & 0x0F) // Sub-Net
	}
	p[23] = 0xF0 // Status1: indicators normal, port-address programmed from network
	binary.LittleEndian.PutUint16(p[24:26], esta)
	putStr(p[26:44], short)   // ShortName (18)
	putStr(p[44:108], long)   // LongName (64)
	putStr(p[108:172], short) // NodeReport (64) - reuse short label
	// Advertise up to 4 input ports carrying the ingested universes.
	np := len(inUniverses)
	if np > 4 {
		np = 4
	}
	binary.BigEndian.PutUint16(p[172:174], uint16(np)) // NumPorts
	for i := 0; i < np; i++ {
		p[174+i] = 0xC0                        // PortTypes: DMX512, input-capable
		p[178+i] = 0x80                        // GoodInput: data received
		p[186+i] = byte(inUniverses[i] & 0x0F) // SwIn: universe low nibble
	}
	p[212] = 0x01 // Style: StNode
	return p
}

// putStr copies s into dst as a null-terminated fixed field (truncated to len(dst)-1).
func putStr(dst []byte, s string) {
	n := copy(dst[:len(dst)-1], s)
	dst[n] = 0
}
