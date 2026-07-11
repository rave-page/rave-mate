package seratolib

// "Serato Markers2" cue/loop write-back. Byte layouts from the public reverse-engineering
// docs (Holzhaus/serato-tags, cross-checked vs Mixxx + triseratops):
//
// # Envelope (MP3 GEOB body; also the FLAC comment's decoded payload)
//
//	[2] header 0x01 0x01
//	    base64 (std alphabet, no padding, '\n' after every 72 chars) of the INNER data
//	    NUL padding to a minimum total of 470 bytes (Serato's own writer convention)
//
// FLAC stores the whole envelope base64'd AGAIN inside the SERATO_MARKERS_V2 vorbis
// comment ("application/octet-stream\0\0Serato Markers2\0" + envelope) - the same outer
// wrapper as SERATO_BEATGRID (flac.go).
//
// # Inner data
//
//	[2] header 0x01 0x01, then entries until a 0x00 name terminator / end:
//	    name  NUL-terminated ASCII ("CUE", "LOOP", "COLOR", "BPMLOCK", "FLIP")
//	    [4]   payload length uint32 BE
//	    [n]   payload
//
// CUE payload:   0x00, index u8, position-ms u32 BE, 0x00, RGB[3], 0x00 0x00, name NUL
// LOOP payload:  0x00, index u8, start-ms u32 BE, end-ms u32 BE, 0xFFFFFFFF,
//	              color u32 BE (0x0027AAE1 default), 0x00, locked u8, name NUL
//
// Non-CUE/LOOP entries (track COLOR, BPMLOCK, FLIP) are preserved byte-exact on write.
// A legacy "Serato Markers_" GEOB is DROPPED when writing MP3s: Serato prefers it over
// Markers2, so leaving a stale copy would shadow the new cues (Mixxx does the same).
// Same integrity discipline as beatgrids: in-memory build, semantic verify, temp+fsync+
// readback+rename; refused while Serato DJ runs. MP4/AIFF/Ogg/WAV stay ErrUnsupported.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/musiclib"
)

const (
	markers2Desc  = "Serato Markers2"
	markersV1Desc = "Serato Markers_"
	markers2Key   = "SERATO_MARKERS_V2"
	m2MinBody     = 470 // Serato NUL-pads the envelope to at least this
)

// m2LoopColor is Serato's default loop color (0x0027AAE1).
const m2LoopColor = 0x0027AAE1

// m2CueColors are Serato DJ Pro's default per-pad cue colors (stored values).
var m2CueColors = [8][3]byte{
	{0xCC, 0x00, 0x00}, {0xCC, 0x88, 0x00}, {0x00, 0x00, 0xCC}, {0xCC, 0xCC, 0x00},
	{0x00, 0xCC, 0x00}, {0xCC, 0x00, 0xCC}, {0x00, 0xCC, 0xCC}, {0x88, 0x00, 0xCC},
}

// m2Entry is one Markers2 entry; unknown types round-trip raw.
type m2Entry struct {
	name string
	data []byte
}

// ── inner data codec ──

// encodeM2Inner serializes entries into the inner Markers2 data.
func encodeM2Inner(entries []m2Entry) []byte {
	out := []byte{0x01, 0x01}
	for _, e := range entries {
		out = append(out, e.name...)
		out = append(out, 0x00)
		out = binary.BigEndian.AppendUint32(out, uint32(len(e.data)))
		out = append(out, e.data...)
	}
	return append(out, 0x00) // terminator
}

// decodeM2Inner parses the inner Markers2 data into entries.
func decodeM2Inner(b []byte) ([]m2Entry, error) {
	if len(b) < 2 || b[0] != 0x01 || b[1] != 0x01 {
		return nil, errors.New("seratolib: bad markers2 inner header")
	}
	var out []m2Entry
	pos := 2
	for pos < len(b) && b[pos] != 0x00 {
		nul := bytes.IndexByte(b[pos:], 0x00)
		if nul < 0 {
			return nil, errors.New("seratolib: unterminated markers2 entry name")
		}
		name := string(b[pos : pos+nul])
		pos += nul + 1
		if pos+4 > len(b) {
			return nil, errors.New("seratolib: truncated markers2 entry length")
		}
		n := int(binary.BigEndian.Uint32(b[pos:]))
		pos += 4
		if n < 0 || pos+n > len(b) {
			return nil, fmt.Errorf("seratolib: markers2 entry %q overruns payload", name)
		}
		out = append(out, m2Entry{name: name, data: b[pos : pos+n]})
		pos += n
	}
	return out, nil
}

// ── envelope codec (shared MP3/FLAC) ──

// encodeM2Body wraps inner into the 0x01 0x01 + base64 + NUL-pad envelope.
func encodeM2Body(inner []byte) []byte {
	s := base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(inner)
	var b strings.Builder
	for len(s) > 72 {
		b.WriteString(s[:72])
		b.WriteByte('\n')
		s = s[72:]
	}
	b.WriteString(s)
	out := append([]byte{0x01, 0x01}, b.String()...)
	for len(out) < m2MinBody {
		out = append(out, 0x00)
	}
	return out
}

// decodeM2Body unwraps the envelope back to the inner data.
func decodeM2Body(b []byte) ([]byte, error) {
	if len(b) < 2 || b[0] != 0x01 || b[1] != 0x01 {
		return nil, errors.New("seratolib: bad markers2 envelope header")
	}
	inner, err := tolerantB64(string(b[2:]))
	if err != nil {
		return nil, fmt.Errorf("seratolib: markers2 base64: %w", err)
	}
	return inner, nil
}

// ── cue/loop entry codec ──

// encodeM2Cue builds a CUE entry payload.
func encodeM2Cue(index int, posMs uint32, color [3]byte, name string) []byte {
	out := make([]byte, 0, 13+len(name))
	out = append(out, 0x00, byte(index))
	out = binary.BigEndian.AppendUint32(out, posMs)
	out = append(out, 0x00, color[0], color[1], color[2], 0x00, 0x00)
	out = append(out, name...)
	return append(out, 0x00)
}

// encodeM2Loop builds a LOOP entry payload (unlocked, default color).
func encodeM2Loop(index int, startMs, endMs uint32, name string) []byte {
	out := make([]byte, 0, 21+len(name))
	out = append(out, 0x00, byte(index))
	out = binary.BigEndian.AppendUint32(out, startMs)
	out = binary.BigEndian.AppendUint32(out, endMs)
	out = append(out, 0xFF, 0xFF, 0xFF, 0xFF)
	out = binary.BigEndian.AppendUint32(out, m2LoopColor)
	out = append(out, 0x00, 0x00) // reserved + locked=0
	out = append(out, name...)
	return append(out, 0x00)
}

// decodeM2CuePoint converts a CUE/LOOP entry back into a musiclib cue (ok=false for
// other entry types or short payloads).
func decodeM2CuePoint(e m2Entry) (musiclib.CuePoint, bool) {
	switch e.name {
	case "CUE":
		if len(e.data) < 13 {
			return musiclib.CuePoint{}, false
		}
		return musiclib.CuePoint{
			Kind:    musiclib.CueHot,
			Hotcue:  int(e.data[1]),
			StartMs: float64(binary.BigEndian.Uint32(e.data[2:])),
			Name:    cutNUL(e.data[12:]),
		}, true
	case "LOOP":
		if len(e.data) < 21 {
			return musiclib.CuePoint{}, false
		}
		start := float64(binary.BigEndian.Uint32(e.data[2:]))
		end := float64(binary.BigEndian.Uint32(e.data[6:]))
		return musiclib.CuePoint{
			Kind:    musiclib.CueLoop,
			Hotcue:  int(e.data[1]),
			StartMs: start,
			LenMs:   end - start,
			Name:    cutNUL(e.data[20:]),
		}, true
	}
	return musiclib.CuePoint{}, false
}

// cutNUL returns the string up to the first NUL.
func cutNUL(b []byte) string {
	if i := bytes.IndexByte(b, 0x00); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// cuesToM2Entries converts a portable cue list into CUE + LOOP entries. Serato has no
// memory-cue concept: only pad cues (Hotcue 0-7) and loops are representable; everything
// else is counted in skipped. Loop indexes fall back to the first free loop slot.
func cuesToM2Entries(cues []musiclib.CuePoint) (entries []m2Entry, skipped int) {
	usedLoop := map[int]bool{}
	var loops []musiclib.CuePoint
	for _, c := range cues {
		switch {
		case c.Kind == musiclib.CueLoop && c.LenMs > 0:
			loops = append(loops, c)
			if c.Hotcue >= 0 && c.Hotcue < 8 {
				usedLoop[c.Hotcue] = true
			}
		case c.Kind == musiclib.CueHot && c.Hotcue >= 0 && c.Hotcue < 8:
			color := m2CueColors[c.Hotcue]
			entries = append(entries, m2Entry{name: "CUE",
				data: encodeM2Cue(c.Hotcue, u32ms(c.StartMs), color, c.Name)})
		case c.Kind == musiclib.CueGrid:
			// grids are the beatgrid writer's job - not a skip
		default:
			skipped++ // memory/load/fade cues have no Markers2 representation
		}
	}
	next := 0
	for _, c := range loops {
		idx := c.Hotcue
		if idx < 0 || idx > 7 {
			for next < 8 && usedLoop[next] {
				next++
			}
			if next > 7 {
				skipped++
				continue
			}
			idx = next
			usedLoop[idx] = true
		}
		entries = append(entries, m2Entry{name: "LOOP",
			data: encodeM2Loop(idx, u32ms(c.StartMs), u32ms(c.StartMs+c.LenMs), c.Name)})
	}
	return entries, skipped
}

// u32ms rounds a ms position into the uint32 the tag stores.
func u32ms(ms float64) uint32 {
	if ms <= 0 {
		return 0
	}
	return uint32(math.Round(ms))
}

// ── file I/O ──

// readMarkers2Bytes dispatches Markers2 envelope extraction by extension.
func readMarkers2Bytes(path string, data []byte) ([]byte, bool, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return readID3Geob(data, markers2Desc)
	case ".flac":
		return readFLACComment(data, markers2Key, markers2Desc)
	}
	return nil, false, ErrUnsupported
}

// ReadCues reads the file's Serato Markers2 pad cues + loops (found=false when the file
// has no Markers2 tag).
func ReadCues(path string) ([]musiclib.CuePoint, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return readCuesBytes(path, data)
}

func readCuesBytes(path string, data []byte) ([]musiclib.CuePoint, bool, error) {
	body, found, err := readMarkers2Bytes(path, data)
	if err != nil || !found {
		return nil, false, err
	}
	inner, err := decodeM2Body(body)
	if err != nil {
		return nil, false, err
	}
	entries, err := decodeM2Inner(inner)
	if err != nil {
		return nil, false, err
	}
	var out []musiclib.CuePoint
	for _, e := range entries {
		if c, ok := decodeM2CuePoint(e); ok {
			out = append(out, c)
		}
	}
	return out, true, nil
}

// WriteCues writes cues' pad cues + loops into the file's Serato Markers2 tag, preserving
// every non-CUE/LOOP entry (COLOR, BPMLOCK, FLIP). Refuses while Serato is running.
// Returns how many cues had no Serato representation and were skipped.
func WriteCues(path string, cues []musiclib.CuePoint) (skipped int, err error) {
	if seratoRunning() {
		return 0, ErrSeratoRunning
	}
	return writeCuesFile(path, cues)
}

// writeCuesFile splices the cues into path's Markers2 tag with the beatgrid writer's
// integrity discipline (build in memory, verify semantically, temp+fsync+readback+rename).
func writeCuesFile(path string, cues []musiclib.CuePoint) (int, error) {
	newEntries, skipped := cuesToM2Entries(cues)
	orig, err := os.ReadFile(path)
	if err != nil {
		return skipped, err
	}
	// preserve non-CUE/LOOP entries from an existing tag
	var kept []m2Entry
	if body, found, rerr := readMarkers2Bytes(path, orig); rerr == nil && found {
		if inner, derr := decodeM2Body(body); derr == nil {
			if old, perr := decodeM2Inner(inner); perr == nil {
				for _, e := range old {
					if e.name != "CUE" && e.name != "LOOP" {
						kept = append(kept, e)
					}
				}
			}
		}
	} else if rerr != nil && errors.Is(rerr, ErrUnsupported) {
		return skipped, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
	}
	body := encodeM2Body(encodeM2Inner(append(kept, newEntries...)))
	var built []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		// drop a stale legacy Markers_ GEOB - Serato would prefer it over Markers2
		built, err = spliceID3Geob(orig, body, markers2Desc, map[string]bool{markersV1Desc: true})
	case ".flac":
		built, err = spliceFLACComment(orig, markers2Key, markers2Desc, body)
	default:
		return skipped, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
	}
	if err != nil {
		return skipped, err
	}
	if err := verifyCueSplice(path, orig, built, newEntries); err != nil {
		return skipped, fmt.Errorf("seratolib: verify %s: %w", filepath.Base(path), err)
	}
	return skipped, commitAtomic(path, built)
}

// verifyCueSplice proves built carries exactly the wanted CUE/LOOP entries and that
// nothing outside the managed Serato marker tags changed.
func verifyCueSplice(path string, orig, built []byte, want []m2Entry) error {
	body, found, err := readMarkers2Bytes(path, built)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("markers2 missing after splice")
	}
	inner, err := decodeM2Body(body)
	if err != nil {
		return err
	}
	entries, err := decodeM2Inner(inner)
	if err != nil {
		return err
	}
	var got []m2Entry
	for _, e := range entries {
		if e.name == "CUE" || e.name == "LOOP" {
			got = append(got, e)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("cue entry count %d != %d", len(got), len(want))
	}
	for i := range want {
		if got[i].name != want[i].name || !bytes.Equal(got[i].data, want[i].data) {
			return fmt.Errorf("entry %d (%s) did not round-trip", i, want[i].name)
		}
	}
	managed := map[string]bool{markers2Desc: true, markersV1Desc: true}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return verifyID3Untouched(orig, built, managed)
	case ".flac":
		return verifyFLACUntouched(orig, built, []string{markers2Key + "="})
	}
	return nil
}

// ApplyCuesSerato writes each update's cues into the track FILE's Serato Markers2 tag
// (Serato reads cues from files; database V2 is deliberately not rewritten). Refuses
// while Serato runs. Unsupported formats are skipped (not a failure); per-file failures
// are joined while the rest still apply. Memory cues have no Serato representation and
// are silently omitted per file (pad cues + loops only).
func ApplyCuesSerato(seratoDir string, updates []musiclib.CueUpdate) (musiclib.WritebackResult, error) {
	var res musiclib.WritebackResult
	if len(updates) == 0 {
		return res, nil
	}
	if _, err := resolveDir(seratoDir); err != nil {
		return res, err
	}
	if seratoRunning() {
		return res, ErrSeratoRunning
	}
	var errs []error
	for _, up := range updates {
		if up.Path == "" {
			continue
		}
		if _, err := writeCuesFile(up.Path, up.Cues); err != nil {
			if errors.Is(err, ErrUnsupported) {
				continue // e.g. .m4a - leave the file alone
			}
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(up.Path), err))
			continue
		}
		res.Updated++
	}
	return res, errors.Join(errs...)
}
