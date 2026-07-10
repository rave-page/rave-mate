// Package seratolib reads the Serato DJ library and writes per-FILE beatgrids (Serato reads
// grids from the audio files' tags, not from `database V2`). Pure stdlib. Byte layouts, from
// the public reverse-engineering docs (Holzhaus/serato-tags, cross-checked vs Mixxx):
//
// # Serato BeatGrid payload (shared by all container formats)
//
//	[2]  header      0x01 0x00
//	[4]  count       uint32 BE - number of markers
//	     markers     count-1 NON-TERMINAL then 1 TERMINAL:
//	[8]    non-terminal: position float32 BE (seconds) + beats-till-next uint32 BE
//	[8]    terminal:     position float32 BE (seconds) + BPM float32 BE
//	[1]  footer      1 byte, semantics unknown ("apparently random"); we write 0x00
//
// A constant grid = count 1, one terminal marker {startS, bpm}.
//
// # MP3 (ID3v2.3/2.4)
//
// GEOB frame, description "Serato BeatGrid". Frame body:
//
//	[1] text encoding (Serato writes 0x00 latin1)
//	    MIME "application/octet-stream" NUL-terminated
//	    filename, empty (one NUL; UTF-16 encodings use double NUL)
//	    description "Serato BeatGrid" NUL-terminated (per encoding)
//	    payload (raw, NOT base64)
//
// The splicer in id3.go rewrites ONLY this frame: every other frame is copied byte-exact,
// v2.3 plain / v2.4 syncsafe frame sizes are honored, v2.3 tag-level unsynchronisation is
// removed (flag cleared - resynced content is what frame sizes describe), extended headers
// are dropped (their CRC would go stale), padding length is preserved, and the audio bytes
// after the tag are never touched.
//
// # FLAC
//
// VORBIS_COMMENT field SERATO_BEATGRID. Value = base64 WITHOUT padding, '\n' inserted after
// every 72 chars (Serato's own writer convention). Decoded blob:
//
//	"application/octet-stream" NUL, NUL (empty filename), "Serato BeatGrid" NUL, payload
//
// flac.go splices only the VORBIS_COMMENT block; every other METADATA_BLOCK and the audio
// frames are copied byte-exact.
//
// MP4/M4A (freeform atom ----:com.serato.dj:beatgrid) is NOT supported: editing moov-resident
// atoms shifts mdat offsets (stco/co64 fix-ups) - too risky for users' files. AIFF/Ogg/WAV are
// also unsupported. WriteBeatgrid returns ErrUnsupported for those.
//
// # Library (`_Serato_` dir)
//
// `database V2` + `Subcrates/*.crate` share one envelope: [4-byte ASCII tag][uint32 BE
// length][payload] (decoded by internal/serato). Track paths (`pfil`/`ptrk`) are stored
// drive-relative on Windows; ReadDatabase resolves them against the volume that holds the
// `_Serato_` dir.
//
// Integrity strategy for writes: the new file is built fully in memory, semantically verified
// (re-parse: beatgrid round-trips, all untouched frames/blocks byte-identical, audio region
// byte-identical), written to a same-dir temp file, fsynced, read back and compared
// byte-for-byte against the built buffer, and only then renamed over the original. Writes are
// refused while a Serato DJ process is running.
package seratolib

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"rave.page/mate/internal/musiclib"
)

// ErrUnsupported marks a file format WriteBeatgrid can't safely rewrite (MP4/AIFF/Ogg/WAV).
var ErrUnsupported = errors.New("seratolib: unsupported file format for beatgrid write")

// ErrSeratoRunning refuses writes while Serato DJ is open (it caches + rewrites file tags).
var ErrSeratoRunning = errors.New("seratolib: Serato DJ is running - close it before writing beatgrids")

const (
	beatgridDesc = "Serato BeatGrid"
	octetStream  = "application/octet-stream"
)

// encodeBeatgrid serializes markers (musiclib ms-based) into a Serato BeatGrid payload.
// Non-terminal beats-till-next is derived from marker spacing x BPM and must land on an
// integer (tolerance 0.05 beats), else the grid isn't representable and an error returns.
func encodeBeatgrid(markers []musiclib.GridMarker) ([]byte, error) {
	if len(markers) == 0 {
		return nil, errors.New("seratolib: empty beatgrid")
	}
	out := make([]byte, 0, 2+4+8*len(markers)+1)
	out = append(out, 0x01, 0x00)
	out = binary.BigEndian.AppendUint32(out, uint32(len(markers)))
	for i, m := range markers {
		if m.BPM <= 0 {
			return nil, fmt.Errorf("seratolib: marker %d has BPM %v", i, m.BPM)
		}
		out = binary.BigEndian.AppendUint32(out, math.Float32bits(float32(m.PositionMs/1000)))
		if i == len(markers)-1 {
			out = binary.BigEndian.AppendUint32(out, math.Float32bits(float32(m.BPM)))
			continue
		}
		deltaS := (markers[i+1].PositionMs - m.PositionMs) / 1000
		if deltaS <= 0 {
			return nil, fmt.Errorf("seratolib: markers %d..%d not ascending", i, i+1)
		}
		beats := deltaS * m.BPM / 60
		rounded := math.Round(beats)
		if rounded < 1 || math.Abs(beats-rounded) > 0.05 {
			return nil, fmt.Errorf("seratolib: segment %d spans %.3f beats (not integral)", i, beats)
		}
		out = binary.BigEndian.AppendUint32(out, uint32(rounded))
	}
	return append(out, 0x00), nil
}

// decodeBeatgrid parses a Serato BeatGrid payload into musiclib markers (ms-based).
// Non-terminal marker BPM is reconstructed from beats-till-next / spacing.
func decodeBeatgrid(b []byte) ([]musiclib.GridMarker, error) {
	if len(b) < 6 || b[0] != 0x01 {
		return nil, errors.New("seratolib: bad beatgrid header")
	}
	count := int(binary.BigEndian.Uint32(b[2:6]))
	if count <= 0 || len(b) < 6+8*count {
		return nil, fmt.Errorf("seratolib: beatgrid count %d overruns %d-byte payload", count, len(b))
	}
	pos := make([]float64, count)
	raw := make([]uint32, count)
	for i := 0; i < count; i++ {
		off := 6 + 8*i
		pos[i] = float64(math.Float32frombits(binary.BigEndian.Uint32(b[off:])))
		raw[i] = binary.BigEndian.Uint32(b[off+4:])
	}
	out := make([]musiclib.GridMarker, count)
	for i := 0; i < count; i++ {
		m := musiclib.GridMarker{PositionMs: pos[i] * 1000}
		if i == count-1 {
			m.BPM = float64(math.Float32frombits(raw[i]))
		} else if d := pos[i+1] - pos[i]; d > 0 {
			m.BPM = float64(raw[i]) * 60 / d
		}
		out[i] = m
	}
	return out, nil
}
