// Package timecode is rave-mate's house SMPTE timecode generator: one master frame clock plus
// three independently-enableable outputs other machines/software chase - LTC (audio-out, the
// SMPTE signal most media software accepts), MTC (MIDI Time Code), and Art-Net TimeCode (UDP for
// lighting consoles). The frame math + LTC waveform reuse internal/medialink (single encoder);
// this package adds the wire formats (MTC quarter-frame / full-frame SysEx, ArtTimeCode packet),
// the master clock, and the platform backends (WinMM waveOut / midiOut via stdlib syscall; UDP via
// stdlib net). Non-Windows audio/MIDI backends are stubs returning "unsupported".
package timecode

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/medialink"
)

// Timecode + Rate are medialink's (single source of truth for frame math).
type (
	Timecode = medialink.Timecode
	Rate     = medialink.Rate
)

// ParseRate maps a config rate token to a medialink.Rate. "29.97"/"29.97df"/"2997" = drop-frame;
// "24"/"25"/"30" = non-drop. Unknown → 30 fps.
func ParseRate(s string) Rate {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "24":
		return medialink.FPS24
	case "25":
		return medialink.FPS25
	case "29.97", "29.97df", "2997", "29.97 df":
		return medialink.FPS2997
	default:
		return medialink.FPS30
	}
}

// ParseStartTC parses "hh:mm:ss:ff" (or ";" before frames) into a Timecode at rate. Empty or
// unparseable → 00:00:00:00. "clock" is handled by the caller (time-of-day jam), not here.
func ParseStartTC(s string, rate Rate) Timecode {
	tc := Timecode{Rate: rate}
	f := strings.FieldsFunc(strings.TrimSpace(s), func(r rune) bool { return r == ':' || r == ';' || r == '.' })
	if len(f) != 4 {
		return tc
	}
	v := make([]int, 4)
	for i := range f {
		n, err := strconv.Atoi(f[i])
		if err != nil {
			return Timecode{Rate: rate}
		}
		v[i] = n
	}
	tc.H, tc.M, tc.S, tc.F = v[0], v[1], v[2], v[3]
	return tc
}

// artNetType maps a Rate to the Art-Net TimeCode type byte: 0=24(film) 1=25(EBU) 2=29.97(DF)
// 3=30(SMPTE). Non-LTC rates fall back to 30.
func artNetType(r Rate) byte {
	switch {
	case r.Nominal == 24:
		return 0
	case r.Nominal == 25:
		return 1
	case r.Nominal == 30 && r.Drop:
		return 2
	default:
		return 3
	}
}

// mtcRateCode maps a Rate to the 2-bit MTC rate field: 00=24 01=25 10=29.97(DF) 11=30.
func mtcRateCode(r Rate) byte {
	switch {
	case r.Nominal == 24:
		return 0
	case r.Nominal == 25:
		return 1
	case r.Nominal == 30 && r.Drop:
		return 2
	default:
		return 3
	}
}
