package timecode

import "encoding/binary"

// artTimeCodePacket builds the 19-byte Art-Net ArtTimeCode packet for tc (Art-Net 4 spec §ArtTimeCode):
//
//	[0:8]  "Art-Net\0"
//	[8:10] OpCode 0x9700, little-endian
//	[10:12] ProtVer 14, big-endian
//	[12]   Filler1 (0)
//	[13]   Filler2 (0)
//	[14]   Frames
//	[15]   Seconds
//	[16]   Minutes
//	[17]   Hours
//	[18]   Type (0=24 1=25 2=29.97DF 3=30)
func artTimeCodePacket(tc Timecode) [19]byte {
	var p [19]byte
	copy(p[0:8], "Art-Net\x00")
	binary.LittleEndian.PutUint16(p[8:10], 0x9700)
	binary.BigEndian.PutUint16(p[10:12], 14)
	// p[12], p[13] filler = 0
	p[14] = byte(tc.F)
	p[15] = byte(tc.S)
	p[16] = byte(tc.M)
	p[17] = byte(tc.H)
	p[18] = artNetType(tc.Rate)
	return p
}
