package midi

// framer splits a raw MIDI byte stream (arbitrary chunk boundaries, running
// status) into complete short messages. Go mirror of driver/ravemidi/framer.cpp,
// minus sysex buffering: winmm's short-message path never delivered sysex either
// (no MIM_LONGDATA handling), so sysex bytes are skipped for parity. Realtime
// bytes (>= 0xF8) pass through immediately, even mid-message.
type framer struct {
	buf     [3]byte
	have    int
	status  byte // running status (channel messages only)
	inSysEx bool
}

// msgLen returns the total length of a short message with this status (0 = sysex).
func msgLen(status byte) int {
	if status < 0xF0 {
		if hi := status & 0xF0; hi == 0xC0 || hi == 0xD0 {
			return 2 // program change / channel pressure
		}
		return 3
	}
	switch status {
	case 0xF0:
		return 0 // sysex start: skipped until F7
	case 0xF1, 0xF3:
		return 2 // MTC quarter-frame, song select
	case 0xF2:
		return 3 // song position
	default:
		return 1 // F4/F5 undefined, F6 tune request
	}
}

// feed consumes one chunk, calling emit per complete message. Data1/Data2 are
// zero where the message is shorter than 3 bytes.
func (f *framer) feed(b []byte, emit func(Message)) {
	for _, c := range b {
		if c >= 0xF8 { // realtime: passthrough, framing state untouched
			emit(Message{Status: c})
			continue
		}
		if c == 0xF0 { // sysex start abandons any partial short message
			f.have = 0
			f.status = 0
			f.inSysEx = true
			continue
		}
		if f.inSysEx {
			if c == 0xF7 {
				f.inSysEx = false
				continue
			}
			if c < 0x80 {
				continue // sysex payload: skipped (winmm parity)
			}
			f.inSysEx = false // interrupting status aborts the sysex
		}
		if c >= 0x80 { // new status
			f.buf[0] = c
			f.have = 1
			if c < 0xF0 {
				f.status = c
			} else {
				f.status = 0 // system common clears running status
			}
			if msgLen(c) == 1 {
				emit(Message{Status: c})
				f.have = 0
			}
			continue
		}
		// data byte
		if f.have == 0 {
			if f.status == 0 {
				continue // stray data with no running status: drop
			}
			f.buf[0] = f.status // running status resumes
			f.have = 1
		}
		f.buf[f.have] = c
		f.have++
		if f.have >= msgLen(f.buf[0]) {
			m := Message{Status: f.buf[0], Data1: f.buf[1]}
			if f.have > 2 {
				m.Data2 = f.buf[2]
			}
			emit(m)
			f.have = 0 // running status persists via f.status
		}
	}
}
