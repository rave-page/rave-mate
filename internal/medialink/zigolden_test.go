package medialink

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"rave.page/mate/internal/wirecrypto"
)

// TestZigGoldenVectors prints byte-exact wire vectors for the native (Zig) transport port to assert
// against (STEP-1 goldens). Run: GOWORK=off go test ./internal/medialink -run TestZigGoldenVectors -v
// Deterministic (fixed inputs); regenerate + re-pin the Zig test when the wire format changes.
func TestZigGoldenVectors(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	salt := make([]byte, 0, 36)
	for i := 0; i < 16; i++ { // offerNonce
		salt = append(salt, byte(0xA0+i))
	}
	for i := 0; i < 16; i++ { // answerNonce
		salt = append(salt, byte(0xB0+i))
	}
	salt = append(salt, []byte("sess-1234")...) // session

	p := func(name string, b []byte) { fmt.Printf("GOLDEN %s = %s\n", name, hex.EncodeToString(b)) }

	// 1. token
	tok := routeToken(master, salt)
	fmt.Printf("GOLDEN token = %08x\n", tok)

	// 2. per-route key schedule (initiator)
	c, err := newDgCrypto(master, salt, true)
	if err != nil {
		t.Fatal(err)
	}
	a36, _ := wirecrypto.HkdfSha256(master, salt, []byte(dgLabelC2S), 36)
	b36, _ := wirecrypto.HkdfSha256(master, salt, []byte(dgLabelS2C), 36)
	p("c2s_key", a36[:32])
	fmt.Printf("GOLDEN c2s_prefix = %08x\n", binary.BigEndian.Uint32(a36[32:]))
	p("s2c_key", b36[:32])
	fmt.Printf("GOLDEN s2c_prefix = %08x\n", binary.BigEndian.Uint32(b36[32:]))
	_ = c

	// 3. datagram header (media fragment)
	h := dgHeader{Typ: dgTypeMedia, Flags: dgFlagAEAD, Token: tok, Seq: 0x0102030405060708,
		Stream: 0x1122, FrameSeq: 0x33445566, FragIdx: 2, FragCount: 5}
	p("hdr24", h.appendHeader(nil))

	// 4. frame marshal (header||payload)
	f := &Frame{Stream: 0x1122, Kind: KindVideo, Codec: CodecHEVC, Flags: FlagKeyframe,
		Seq: 0x33445566, PTS: 0x0011223344556677, Payload: []byte("hello-au-payload")}
	p("frame_marshal", f.marshal(nil))

	// 5. a full sealed media datagram (one fragment): encodeFrame with the initiator crypto, token, seq0
	enc := newDgEncoder(tok, c, 1200)
	dgs, first, cnt, err := enc.encodeFrame(nil, f)
	if err != nil || len(dgs) != 1 {
		t.Fatalf("encodeFrame: err=%v n=%d", err, len(dgs))
	}
	fmt.Printf("GOLDEN sealed_dg0 first=%d count=%d = %s\n", first, cnt, hex.EncodeToString(dgs[0]))

	// 6. a sealed CONTROL (ping) datagram, seq starts after the media frame (seq=1)
	ping := enc.control(dgTypePing, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	p("sealed_ping_seq1", ping)
}
