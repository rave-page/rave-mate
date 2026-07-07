package timecode

// MIDI Time Code wire formats (MMA MTC spec). Two carriers:
//   - Quarter-frame: a stream of 2-byte messages `F1 nn`, 8 pieces per 2 frames (4/frame → ~8.33ms
//     apart at 30fps). Each piece nn = (piece<<4)|value, where value is a 4-bit nibble of the
//     timecode; the 8 pieces (0..7) together convey one full HH:MM:SS:FF + rate. A receiver
//     reconstructs a timecode 2 frames behind by the time piece 7 arrives - spec-expected.
//   - Full-frame: a SysEx `F0 7F 7F 01 01 hh mm ss ff F7` sent on locate/start/stop so a receiver
//     jumps straight to a position without waiting for 8 quarter-frames.
// Rate is carried in the high nibble of the hours field (2-bit code: 00=24 01=25 10=29.97DF 11=30).

// mtcQuarterFrame returns the quarter-frame data byte nn for piece p (0..7) of tc. The full 2-byte
// message is {0xF1, nn}. Pieces:
//
//	0 frames LSN   1 frames MSN
//	2 seconds LSN  3 seconds MSN
//	4 minutes LSN  5 minutes MSN
//	6 hours LSN    7 hours MSN | (rateCode<<1)
func mtcQuarterFrame(tc Timecode, p int) byte {
	rc := mtcRateCode(tc.Rate)
	var val byte
	switch p & 7 {
	case 0:
		val = byte(tc.F) & 0x0F
	case 1:
		val = (byte(tc.F) >> 4) & 0x0F
	case 2:
		val = byte(tc.S) & 0x0F
	case 3:
		val = (byte(tc.S) >> 4) & 0x0F
	case 4:
		val = byte(tc.M) & 0x0F
	case 5:
		val = (byte(tc.M) >> 4) & 0x0F
	case 6:
		val = byte(tc.H) & 0x0F
	case 7:
		val = ((byte(tc.H) >> 4) & 0x01) | (rc << 1)
	}
	return byte((p&7)<<4) | (val & 0x0F)
}

// mtcFullFrame returns the full-frame SysEx bytes for tc: F0 7F 7F 01 01 hh mm ss ff F7. hh's high
// bits carry the rate code (bits 5-6).
func mtcFullFrame(tc Timecode) []byte {
	rc := mtcRateCode(tc.Rate)
	hh := (rc << 5) | (byte(tc.H) & 0x1F)
	return []byte{0xF0, 0x7F, 0x7F, 0x01, 0x01, hh, byte(tc.M), byte(tc.S), byte(tc.F), 0xF7}
}
