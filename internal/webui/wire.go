package webui

import "rave.page/mate/internal/zigui"

// Binary state wire (RZW1) dispatch. The phase-A bridge pays a state→JSON→parse round trip on
// EVERY render; wave B-1 replaces it for two pilot views with a length-prefixed TLV document
// whose encoder (wire_gen.go) and decoder (native/zigui/src/wire_gen.zig) are generated from
// ONE schema (internal/zigui/wiregen). Format + rationale: internal/zigui/wire.go.
//
// Order is v2 → v1 → Go, and every downgrade is visible in zigui.FallbackCounts():
// a NULL from the v2 export counts under Render<X>V2, a NULL from the JSON export under
// Render<X>, and an encoder refusal (over-size document) is recorded explicitly.

// zigWire renders through the binary export, falling back to the JSON export. ok=false means
// both Zig paths declined - the caller renders in Go. js is a thunk so the JSON state is only
// marshalled when the binary path actually failed.
// wireNoV1 is the v1 slot for wire-only fragments (no legacy JSON renderer).
func wireNoV1([]byte) (string, bool) { return "", false }

func zigWire(name string, doc []byte, v2 func([]byte) (string, bool), v1 func([]byte) (string, bool), js func() []byte) (string, bool) {
	if len(doc) == 0 {
		zigui.NoteWireFallback(name) // encoder refused (over-size) - never reached the ABI
	} else if h, ok := v2(doc); ok {
		return h, true
	}
	return v1(js())
}
