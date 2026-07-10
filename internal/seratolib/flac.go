package seratolib

// FLAC METADATA_BLOCK splicer: rewrite ONLY the VORBIS_COMMENT block's SERATO_BEATGRID field;
// every other block body and the audio frames are copied byte-exact (block header last-flags
// are recomputed). Layout: "fLaC" then blocks of [1B header: last<<7|type][3B BE length][body];
// type 0 = STREAMINFO (always first), 4 = VORBIS_COMMENT. Vorbis comment body is little-endian:
// u32 vendor length + vendor, u32 count, count x (u32 length + "KEY=value" UTF-8).

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const flacVorbisType = 4

// flacBlock is one METADATA_BLOCK: type + body verbatim.
type flacBlock struct {
	typ  byte
	body []byte
}

// parseFLAC splits data into its metadata blocks; audioOff is the first audio-frame byte.
func parseFLAC(data []byte) ([]flacBlock, int, error) {
	if len(data) < 8 || string(data[:4]) != "fLaC" {
		return nil, 0, errors.New("seratolib: not a FLAC file")
	}
	var blocks []flacBlock
	pos := 4
	for {
		if pos+4 > len(data) {
			return nil, 0, errors.New("seratolib: truncated FLAC metadata block header")
		}
		h := data[pos]
		size := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4
		if pos+size > len(data) {
			return nil, 0, errors.New("seratolib: FLAC metadata block overruns file")
		}
		blocks = append(blocks, flacBlock{typ: h & 0x7F, body: data[pos : pos+size]})
		pos += size
		if h&0x80 != 0 {
			return blocks, pos, nil
		}
	}
}

// renderFLAC serializes blocks (recomputing last-block flags) + the audio region.
func renderFLAC(blocks []flacBlock, audio []byte) ([]byte, error) {
	out := []byte("fLaC")
	for i, b := range blocks {
		if len(b.body) >= 1<<24 {
			return nil, errors.New("seratolib: FLAC metadata block exceeds 24-bit length")
		}
		h := b.typ
		if i == len(blocks)-1 {
			h |= 0x80
		}
		out = append(out, h, byte(len(b.body)>>16), byte(len(b.body)>>8), byte(len(b.body)))
		out = append(out, b.body...)
	}
	return append(out, audio...), nil
}

// vorbisComments decodes a VORBIS_COMMENT body into vendor + comment list.
func vorbisComments(body []byte) (vendor string, comments []string, err error) {
	rd := body
	take := func(n int) ([]byte, error) {
		if n < 0 || n > len(rd) {
			return nil, errors.New("seratolib: truncated vorbis comment block")
		}
		b := rd[:n]
		rd = rd[n:]
		return b, nil
	}
	lenB, err := take(4)
	if err != nil {
		return "", nil, err
	}
	vendB, err := take(int(binary.LittleEndian.Uint32(lenB)))
	if err != nil {
		return "", nil, err
	}
	cntB, err := take(4)
	if err != nil {
		return "", nil, err
	}
	count := int(binary.LittleEndian.Uint32(cntB))
	if count < 0 || count > len(rd) { // each comment needs >=4 bytes; cheap sanity bound
		return "", nil, errors.New("seratolib: implausible vorbis comment count")
	}
	for i := 0; i < count; i++ {
		lb, err := take(4)
		if err != nil {
			return "", nil, err
		}
		cb, err := take(int(binary.LittleEndian.Uint32(lb)))
		if err != nil {
			return "", nil, err
		}
		comments = append(comments, string(cb))
	}
	return string(vendB), comments, nil
}

// renderVorbis serializes vendor + comments into a VORBIS_COMMENT body.
func renderVorbis(vendor string, comments []string) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(vendor)))
	out = append(out, vendor...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(comments)))
	for _, c := range comments {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(c)))
		out = append(out, c...)
	}
	return out
}

// spliceFLACBeatgrid returns a copy of data (a whole FLAC file) with its SERATO_BEATGRID
// vorbis comment replaced by the encoded payload (the comment block is created after
// STREAMINFO when the file has none). All other blocks/comments/audio pass through.
func spliceFLACBeatgrid(data, payload []byte) ([]byte, error) {
	blocks, audioOff, err := parseFLAC(data)
	if err != nil {
		return nil, err
	}
	value := "SERATO_BEATGRID=" + encodeSeratoB64(beatgridDesc, payload)
	idx := -1
	for i, b := range blocks {
		if b.typ == flacVorbisType {
			idx = i
			break
		}
	}
	if idx < 0 {
		nb := flacBlock{typ: flacVorbisType, body: renderVorbis("rave-mate", []string{value})}
		blocks = append(blocks[:1], append([]flacBlock{nb}, blocks[1:]...)...)
		return renderFLAC(blocks, data[audioOff:])
	}
	vendor, comments, err := vorbisComments(blocks[idx].body)
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(comments)+1)
	for _, c := range comments {
		if !strings.HasPrefix(strings.ToUpper(c), "SERATO_BEATGRID=") {
			kept = append(kept, c)
		}
	}
	blocks[idx].body = renderVorbis(vendor, append(kept, value))
	return renderFLAC(blocks, data[audioOff:])
}

// readFLACBeatgrid extracts the SERATO_BEATGRID payload from data (found=false when absent).
func readFLACBeatgrid(data []byte) ([]byte, bool, error) {
	blocks, _, err := parseFLAC(data)
	if err != nil {
		return nil, false, err
	}
	for _, b := range blocks {
		if b.typ != flacVorbisType {
			continue
		}
		_, comments, err := vorbisComments(b.body)
		if err != nil {
			return nil, false, err
		}
		for _, c := range comments {
			if !strings.HasPrefix(strings.ToUpper(c), "SERATO_BEATGRID=") {
				continue
			}
			payload, err := decodeSeratoB64(beatgridDesc, c[len("SERATO_BEATGRID="):])
			if err != nil {
				return nil, false, err
			}
			return payload, true, nil
		}
	}
	return nil, false, nil
}

// encodeSeratoB64 wraps a tag payload the way Serato stores it in vorbis comments:
// "application/octet-stream\0\0<desc>\0<payload>" base64'd WITHOUT padding, '\n' after
// every 72 chars.
func encodeSeratoB64(desc string, payload []byte) string {
	blob := make([]byte, 0, len(octetStream)+2+len(desc)+1+len(payload))
	blob = append(blob, octetStream...)
	blob = append(blob, 0x00, 0x00)
	blob = append(blob, desc...)
	blob = append(blob, 0x00)
	blob = append(blob, payload...)
	s := base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(blob)
	var out strings.Builder
	for len(s) > 72 {
		out.WriteString(s[:72])
		out.WriteByte('\n')
		s = s[72:]
	}
	out.WriteString(s)
	return out.String()
}

// decodeSeratoB64 reverses encodeSeratoB64 tolerantly: whitespace/NULs stripped, padding
// optional, and one trailing junk char (Serato's buggy encoder emits a stray 'A') retried.
func decodeSeratoB64(desc, s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\x00', '=':
			return -1
		}
		return r
	}, s)
	enc := base64.StdEncoding.WithPadding(base64.NoPadding)
	blob, err := enc.DecodeString(clean)
	if err != nil && len(clean) > 0 {
		blob, err = enc.DecodeString(clean[:len(clean)-1]) // stray trailing char quirk
	}
	if err != nil {
		return nil, fmt.Errorf("seratolib: bad base64 in serato comment: %w", err)
	}
	prefix := append(append(append([]byte(octetStream), 0x00, 0x00), desc...), 0x00)
	if !bytes.HasPrefix(blob, prefix) {
		return nil, errors.New("seratolib: serato comment lacks expected header")
	}
	return blob[len(prefix):], nil
}
