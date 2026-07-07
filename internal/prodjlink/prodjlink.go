// Package prodjlink is a passive Pro DJ Link listener: it reads the status packets Pioneer
// CDJ/XDJ players broadcast on the LAN (UDP 50002) and decodes the now-playing state - player
// number, loaded rekordbox track id + source slot, play/on-air/master flags, track BPM, pitch,
// effective tempo, and beat. Pure stdlib (net). Offsets per Deep-Symmetry beat-link. Read-only:
// we never announce a virtual device or query metadata, just observe broadcasts.
package prodjlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strings"
)

// StatusPort is the UDP port CDJs broadcast status packets on.
const StatusPort = 50002

// magic prefixes every Pro DJ Link packet ("Qspt1WmJOL").
var magic = []byte{0x51, 0x73, 0x70, 0x74, 0x31, 0x57, 0x6d, 0x4a, 0x4f, 0x4c}

const statusType = 0x0a // packet type byte (offset 0x0a) for CDJ status

// Slot is where a loaded track came from.
type Slot uint8

const (
	SlotNone       Slot = 0
	SlotCD         Slot = 1
	SlotSD         Slot = 2
	SlotUSB        Slot = 3
	SlotCollection Slot = 4 // rekordbox collection (link)
	SlotUSB2       Slot = 7
)

func (s Slot) String() string {
	switch s {
	case SlotCD:
		return "CD"
	case SlotSD:
		return "SD"
	case SlotUSB, SlotUSB2:
		return "USB"
	case SlotCollection:
		return "rekordbox"
	default:
		return "none"
	}
}

// TrackType classifies the loaded track.
type TrackType uint8

const (
	TrackNone       TrackType = 0
	TrackRekordbox  TrackType = 1
	TrackUnanalyzed TrackType = 2
	TrackCDAudio    TrackType = 5
)

// Status is one decoded CDJ status packet (now-playing for a single player).
type Status struct {
	Player       int       // this player's device number
	Name         string    // device name (e.g. "CDJ-3000")
	TrackID      uint32    // rekordbox id of the loaded track (0 = none)
	SourcePlayer int       // player the track was loaded from
	Slot         Slot      // media slot the track came from
	Type         TrackType //
	Playing      bool      // transport running
	OnAir        bool      // fader up on the mixer (audible)
	Master       bool      // tempo master
	TrackBPM     float64   // the track's analyzed BPM (at 0 pitch)
	Pitch        float64   // tempo multiplier (1.0 = 0% pitch)
	EffectiveBPM float64   // TrackBPM * Pitch (the BPM actually playing)
	Beat         int       // beat within the bar/track (-1 if unknown)
}

// ParseStatus decodes a CDJ status packet. ok=false if the bytes aren't a status packet.
func ParseStatus(p []byte) (Status, bool) {
	if len(p) < 0x94 || !bytes.Equal(p[:10], magic) || p[statusType] != statusType {
		return Status{}, false
	}
	flags := p[0x89]
	st := Status{
		Player:       int(p[0x21]),
		Name:         strings.TrimRight(string(p[0x0b:0x1f]), "\x00 "),
		SourcePlayer: int(p[0x28]),
		Slot:         Slot(p[0x29]),
		Type:         TrackType(p[0x2a]),
		TrackID:      binary.BigEndian.Uint32(p[0x2c:0x30]),
		Playing:      flags&0x40 != 0,
		Master:       flags&0x20 != 0,
		OnAir:        flags&0x08 != 0,
	}
	if bpm := binary.BigEndian.Uint16(p[0x92:0x94]); bpm != 0xffff {
		st.TrackBPM = float64(bpm) / 100
	}
	// pitch: 3-byte big-endian, 0x100000 == 100% (no pitch bend).
	pitch := uint32(p[0x8d])<<16 | uint32(p[0x8e])<<8 | uint32(p[0x8f])
	st.Pitch = float64(pitch) / 0x100000
	st.EffectiveBPM = st.TrackBPM * st.Pitch
	st.Beat = -1
	if len(p) >= 0xa4 {
		if b := binary.BigEndian.Uint32(p[0xa0:0xa4]); b != 0xffffffff {
			st.Beat = int(b)
		}
	}
	return st, true
}

// Listen binds UDP :50002 and invokes onStatus for every decoded CDJ status broadcast until
// ctx is cancelled. Returns the bind error if the port is taken (e.g. Rekordbox itself running).
func Listen(ctx context.Context, onStatus func(Status)) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: StatusPort, IP: net.IPv4zero})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if st, ok := ParseStatus(buf[:n]); ok {
			onStatus(st)
		}
	}
}
