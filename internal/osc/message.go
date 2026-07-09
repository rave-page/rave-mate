package osc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// This file extends the send-only VRChat UDP client (osc.go) with a full OSC 1.1
// message value type + encode/decode + a stream length-scanner. These are the generic
// OSC primitives a stream-framed consumer needs (e.g. internal/seratoremote), kept here
// so the two OSC dialects share one codec instead of duplicating it. Additive only:
// the existing Client.Send / encode / tracker helpers are untouched.

// ArgKind is an OSC type-tag code for one argument.
type ArgKind byte

const (
	KindInt    ArgKind = 'i' // int32, big-endian
	KindFloat  ArgKind = 'f' // float32, big-endian IEEE-754
	KindString ArgKind = 's' // null-terminated, 4-byte padded
	KindBlob   ArgKind = 'b' // uint32 BE length + raw bytes, 4-byte padded
)

// Arg is one typed OSC argument. Only the field matching Kind is meaningful.
type Arg struct {
	Kind  ArgKind
	Int   int32
	Float float32
	Str   string
	Blob  []byte
}

// ArgInt/ArgFloat/ArgString/ArgBlob are constructors for the four supported arg types.
func ArgInt(v int32) Arg     { return Arg{Kind: KindInt, Int: v} }
func ArgFloat(v float32) Arg { return Arg{Kind: KindFloat, Float: v} }
func ArgString(v string) Arg { return Arg{Kind: KindString, Str: v} }
func ArgBlob(v []byte) Arg   { return Arg{Kind: KindBlob, Blob: v} }

// Message is one OSC message: an address pattern + typed args.
type Message struct {
	Address string
	Args    []Arg
}

// ErrIncomplete means a buffer does not yet hold a full packet (need more bytes).
var ErrIncomplete = errors.New("osc: incomplete packet")

// Msg builds a Message.
func Msg(address string, args ...Arg) Message { return Message{Address: address, Args: args} }

// Encode serializes m to bare OSC 1.1 wire bytes (no stream framing).
func Encode(m Message) []byte {
	out := padString(m.Address)
	tags := make([]byte, 0, len(m.Args)+1)
	tags = append(tags, ',')
	for _, a := range m.Args {
		tags = append(tags, byte(a.Kind))
	}
	out = append(out, padString(string(tags))...)
	var buf [4]byte
	for _, a := range m.Args {
		switch a.Kind {
		case KindInt:
			binary.BigEndian.PutUint32(buf[:], uint32(a.Int))
			out = append(out, buf[:]...)
		case KindFloat:
			binary.BigEndian.PutUint32(buf[:], math.Float32bits(a.Float))
			out = append(out, buf[:]...)
		case KindString:
			out = append(out, padString(a.Str)...)
		case KindBlob:
			binary.BigEndian.PutUint32(buf[:], uint32(len(a.Blob)))
			out = append(out, buf[:]...)
			out = append(out, a.Blob...)
			if pad := len(a.Blob) % 4; pad != 0 {
				out = append(out, make([]byte, 4-pad)...)
			}
		}
	}
	return out
}

// Decode parses one bare OSC message from b. Errors on malformed/truncated input or an
// unsupported type tag.
func Decode(b []byte) (Message, error) {
	addr, off, err := readString(b, 0)
	if err != nil {
		return Message{}, err
	}
	tag, off, err := readString(b, off)
	if err != nil {
		return Message{}, err
	}
	if len(tag) == 0 || tag[0] != ',' {
		return Message{}, fmt.Errorf("osc: type-tag missing leading comma: %q", tag)
	}
	m := Message{Address: addr}
	for _, code := range []byte(tag[1:]) {
		switch ArgKind(code) {
		case KindInt:
			if off+4 > len(b) {
				return Message{}, fmt.Errorf("osc: int32 truncated")
			}
			m.Args = append(m.Args, ArgInt(int32(binary.BigEndian.Uint32(b[off:]))))
			off += 4
		case KindFloat:
			if off+4 > len(b) {
				return Message{}, fmt.Errorf("osc: float32 truncated")
			}
			m.Args = append(m.Args, ArgFloat(math.Float32frombits(binary.BigEndian.Uint32(b[off:]))))
			off += 4
		case KindString:
			var s string
			if s, off, err = readString(b, off); err != nil {
				return Message{}, err
			}
			m.Args = append(m.Args, ArgString(s))
		case KindBlob:
			if off+4 > len(b) {
				return Message{}, fmt.Errorf("osc: blob length truncated")
			}
			n := int(binary.BigEndian.Uint32(b[off:]))
			off += 4
			if n < 0 || off+n > len(b) {
				return Message{}, fmt.Errorf("osc: blob data truncated")
			}
			m.Args = append(m.Args, ArgBlob(append([]byte(nil), b[off:off+n]...)))
			off += pad4(n)
		default:
			return Message{}, fmt.Errorf("osc: unsupported type tag %q", string(rune(code)))
		}
	}
	return m, nil
}

// PacketLen returns the byte length of the single bare OSC packet at the front of b,
// computed from its self-describing structure (address + type-tag + typed args). It
// returns (0, ErrIncomplete) when b is too short to describe a whole packet - the caller
// should read more bytes. It errors on a malformed type-tag or unknown arg type. Mirrors
// the framing scanner in serato-connect (framing.ts oscPacketLength).
func PacketLen(b []byte) (int, error) {
	afterAddr, ok := scanStringEnd(b, 0)
	if !ok {
		return 0, ErrIncomplete
	}
	// Read the tag string to know the arg layout.
	tagStart := afterAddr
	tagEnd := tagStart
	for tagEnd < len(b) && b[tagEnd] != 0 {
		tagEnd++
	}
	if tagEnd >= len(b) {
		return 0, ErrIncomplete
	}
	tag := string(b[tagStart:tagEnd])
	afterTag := tagStart + pad4(tagEnd-tagStart+1)
	if len(tag) == 0 || tag[0] != ',' {
		return 0, fmt.Errorf("osc: type-tag missing leading comma: %q", tag)
	}
	cur := afterTag
	for _, code := range []byte(tag[1:]) {
		switch code {
		case 'i', 'f':
			cur += 4
		case 'h', 'd', 't':
			cur += 8
		case 's', 'S':
			next, sok := scanStringEnd(b, cur)
			if !sok {
				return 0, ErrIncomplete
			}
			cur = next
		case 'b':
			if cur+4 > len(b) {
				return 0, ErrIncomplete
			}
			n := int(binary.BigEndian.Uint32(b[cur:]))
			cur += 4 + pad4(n)
		case 'T', 'F', 'N', 'I':
			// zero-byte typed args
		default:
			return 0, fmt.Errorf("osc: unsupported type tag %q in framing scan", string(rune(code)))
		}
		if cur > len(b) {
			return 0, ErrIncomplete
		}
	}
	return cur, nil
}

// readString decodes a null-terminated, 4-byte-padded OSC string at off, returning the
// value and the offset just past the padded string.
func readString(b []byte, off int) (string, int, error) {
	end := off
	for end < len(b) && b[end] != 0 {
		end++
	}
	if end >= len(b) {
		return "", 0, fmt.Errorf("osc: string not null-terminated")
	}
	return string(b[off:end]), off + pad4(end-off+1), nil
}

// scanStringEnd returns the offset just past a null-terminated padded string at start, or
// ok=false if no terminator is present yet (need more bytes).
func scanStringEnd(b []byte, start int) (int, bool) {
	i := start
	for i < len(b) && b[i] != 0 {
		i++
	}
	if i >= len(b) {
		return 0, false
	}
	return start + pad4(i-start+1), true
}

// pad4 rounds n up to the next multiple of 4.
func pad4(n int) int { return (n + 3) &^ 3 }
