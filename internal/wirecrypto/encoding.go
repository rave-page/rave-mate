package wirecrypto

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"sort"
)

// b64url is the unpadded URL-safe base64 used everywhere on the wire (matches the
// TS encoding.ts B64URL alphabet "A-Za-z0-9-_", no padding).
var b64url = base64.RawURLEncoding

// EncB64url encodes to unpadded URL-safe base64.
func EncB64url(b []byte) string { return b64url.EncodeToString(b) }

// DecB64url decodes unpadded URL-safe base64.
func DecB64url(s string) ([]byte, error) { return b64url.DecodeString(s) }

// CanonicalJSON reproduces the TS canonicalJson byte-for-byte: object keys sorted
// recursively, compact, JS-compatible string escaping (no HTML escaping), and numbers
// emitted verbatim from the wire (UseNumber) so a value's textual form round-trips
// identically to the peer that MAC'd it. This is the MAC/transcript input - any drift
// here breaks the channel.
func CanonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanon(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalJSONValue marshals a Go value then canonicalizes it - used for frames we
// build ourselves, so the MAC path is identical to the receive path.
func CanonicalJSONValue(v any) ([]byte, error) {
	raw, err := MarshalNoHTMLEscape(v)
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(raw)
}

func writeCanon(out *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeJSONString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanon(out, t[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanon(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case json.Number:
		out.WriteString(t.String()) // verbatim - preserves the peer's exact number text
	case string:
		return writeJSONString(out, t)
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		// Fallback for non-UseNumber paths (shouldn't happen).
		b, err := MarshalNoHTMLEscape(t)
		if err != nil {
			return err
		}
		out.Write(b)
	}
	return nil
}

// writeJSONString emits a JSON string with JS-JSON.stringify-compatible escaping
// (escapeHTML off: <, >, & stay literal, matching the TS side).
func writeJSONString(out *bytes.Buffer, s string) error {
	b, err := MarshalNoHTMLEscape(s)
	if err != nil {
		return err
	}
	out.Write(b)
	return nil
}

// MarshalNoHTMLEscape JSON-marshals v with HTML escaping disabled and the encoder's
// trailing newline trimmed.
func MarshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder appends a trailing newline; trim it.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ConstantTimeEqualStr compares two strings without leaking length-independent timing
// (used for MAC / auth-tag comparison; mirrors the TS timingSafeEqual on utf8 bytes).
func ConstantTimeEqualStr(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
