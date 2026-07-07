// Package serato is a pure-stdlib decoder for Serato DJ's binary library + history files.
// `database V2`, `Subcrates\*.crate`, and `History\Sessions\*.session` all share ONE
// tag-envelope format: [4-byte ASCII tag][4-byte uint32 BE length][payload], repeated.
// Payload type is dispatched by the tag's first byte: o = nested container (otrk/oent/oren
// + adat), t/p = UTF-16BE text (p* = drive-relative path), u = uint32 BE, s = int32 BE,
// b = single byte, vrsn = version string. Inside History `adat` records the inner fields
// are keyed by NUMERIC tag ids instead (see adat* consts). Decoding is defensive: unknown
// tags are skipped by length; a short/garbage buffer returns an error, never a panic.
package serato

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Track is one decoded library/history track record.
type Track struct {
	Path, Title, Artist, Album, Genre, Key, Comment string
	BPM                                             float64
	LengthSec                                       int
	Deck                                            int
	Played                                          bool
}

// Crate is a named subcrate referencing tracks by (drive-relative) path.
type Crate struct {
	Name       string
	TrackPaths []string
}

// adat numeric field ids (uint32 BE keys inside a History session's adat record).
// Sourced from the chrisle/serato-connect (TS) + nhcarter123/seratohistory (Py) parsers;
// chrisle is the primary (newer, carries played/start/deck), nhcarter fills text gaps.
const (
	adatPath     = 0x02 // file path (UTF-16BE)
	adatTitle    = 0x06
	adatArtist   = 0x07
	adatAlbum    = 0x08
	adatGenre    = 0x09
	adatBPM      = 0x0f // uint32
	adatComment  = 0x11
	adatStart    = 0x1c // uint32 unix secs (start of play) - decoded but unused in Track
	adatDeck     = 0x1f // uint32 deck number
	adatPlayTime = 0x2d // uint32 unix secs (played-at) - unused; nhcarter reads this id as duration
	adatNotes    = 0x31 // UTF-16BE notes - unused
	adatPlayed   = 0x32 // single byte: actually-played flag
	adatKey      = 0x33
	adatDeckStr  = 0x3f // nhcarter's deck id (UTF-16BE text) - older Serato; fallback only
)

// node is a decoded envelope element: a container (children) or a leaf (raw payload).
type node struct {
	tag      string // 4 raw bytes; ASCII for library tags, numeric BE id inside adat
	children []node
	payload  []byte
}

// isContainer reports whether a tag's payload is itself a chunk sequence to recurse.
func isContainer(tag string) bool { return len(tag) == 4 && (tag[0] == 'o' || tag == "adat") }

// decode parses a buffer as a sequence of [tag][len][payload] chunks (recursing containers).
func decode(b []byte) ([]node, error) {
	var nodes []node
	for i := 0; i+8 <= len(b); {
		tag := string(b[i : i+4])
		length := int(binary.BigEndian.Uint32(b[i+4 : i+8]))
		i += 8
		if length < 0 || length > len(b)-i {
			return nil, fmt.Errorf("serato: tag %q length %d overruns %d-byte buffer", tag, length, len(b)-i)
		}
		payload := b[i : i+length]
		i += length
		n := node{tag: tag}
		if isContainer(tag) {
			ch, err := decode(payload)
			if err != nil {
				return nil, err
			}
			n.children = ch
		} else {
			n.payload = payload
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// parseTop reads r fully and decodes it as a top-level chunk sequence.
func parseTop(r io.Reader) ([]node, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// ParseDatabase decodes a `database V2` file into its track records.
func ParseDatabase(r io.Reader) ([]Track, error) {
	nodes, err := parseTop(r)
	if err != nil {
		return nil, err
	}
	var out []Track
	for _, n := range nodes {
		if n.tag == "otrk" {
			out = append(out, trackFromLib(n.children))
		}
	}
	return out, nil
}

// ParseCrate decodes a `*.crate` file into its track-path list (Name set by caller).
func ParseCrate(r io.Reader) (Crate, error) {
	nodes, err := parseTop(r)
	if err != nil {
		return Crate{}, err
	}
	var c Crate
	for _, n := range nodes {
		if n.tag != "otrk" {
			continue
		}
		for _, f := range n.children {
			if f.tag == "ptrk" || f.tag == "pfil" {
				if p := decText(f.payload); p != "" {
					c.TrackPaths = append(c.TrackPaths, p)
				}
			}
		}
	}
	return c, nil
}

// ParseSession decodes a `*.session` history file into its played/loaded track entries.
func ParseSession(r io.Reader) ([]Track, error) {
	nodes, err := parseTop(r)
	if err != nil {
		return nil, err
	}
	var out []Track
	for _, n := range nodes {
		if n.tag != "oent" && n.tag != "oren" {
			continue
		}
		for _, ch := range n.children {
			if ch.tag == "adat" {
				out = append(out, trackFromADAT(ch.children))
			}
		}
	}
	return out, nil
}

// trackFromLib builds a Track from library (otrk) fields keyed by 4-char ASCII tags.
func trackFromLib(fields []node) Track {
	var t Track
	for _, f := range fields {
		switch f.tag {
		case "pfil", "ptrk":
			t.Path = decText(f.payload)
		case "tsng", "ttit":
			t.Title = decText(f.payload)
		case "tart":
			t.Artist = decText(f.payload)
		case "talb":
			t.Album = decText(f.payload)
		case "tgen":
			t.Genre = decText(f.payload)
		case "tkey":
			t.Key = decText(f.payload)
		case "tcmt":
			t.Comment = decText(f.payload)
		case "tbpm":
			t.BPM, _ = strconv.ParseFloat(strings.TrimSpace(decText(f.payload)), 64)
		case "tlen":
			t.LengthSec = parseLen(decText(f.payload))
		}
	}
	return t
}

// trackFromADAT builds a Track from a History adat record's numeric-id fields.
func trackFromADAT(fields []node) Track {
	var t Track
	for _, f := range fields {
		if len(f.tag) != 4 {
			continue
		}
		switch binary.BigEndian.Uint32([]byte(f.tag)) {
		case adatPath:
			t.Path = decText(f.payload)
		case adatTitle:
			t.Title = decText(f.payload)
		case adatArtist:
			t.Artist = decText(f.payload)
		case adatAlbum:
			t.Album = decText(f.payload)
		case adatGenre:
			t.Genre = decText(f.payload)
		case adatComment:
			t.Comment = decText(f.payload)
		case adatKey:
			t.Key = decText(f.payload)
		case adatBPM:
			t.BPM = float64(decU32(f.payload))
		case adatDeck:
			t.Deck = int(decU32(f.payload))
		case adatDeckStr:
			if t.Deck == 0 { // older-Serato text deck, only if the uint32 id wasn't present
				if n, err := strconv.Atoi(strings.TrimSpace(decText(f.payload))); err == nil {
					t.Deck = n
				}
			}
		case adatPlayed:
			t.Played = decByte(f.payload) != 0
		}
	}
	return t
}

// decText decodes a UTF-16BE payload to a string, stripping NULs (incl. the terminator).
func decText(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[i*2:])
	}
	return strings.ReplaceAll(string(utf16.Decode(u)), "\x00", "")
}

// decU32 reads a big-endian uint32 from the front of b (0 if too short).
func decU32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

// decByte reads the first byte of b (0 if empty).
func decByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

// parseLen converts a Serato length string ("m:ss.cc" / "h:mm:ss") to whole seconds.
func parseLen(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var total float64
	for _, p := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return int(total)
}
