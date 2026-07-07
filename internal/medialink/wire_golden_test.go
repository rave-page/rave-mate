package medialink

import (
	"encoding/hex"
	"testing"
)

// Golden wire vectors pin the v1 frame format byte-for-byte (§2.1). A change here is a WIRE BREAK -
// intentional only with a protocol-version bump. Covers the reserved KindRepair kind + the reserved
// stream-0 meta report/NACK payloads (§2.5) so the loss machinery can bolt on without a break.
func TestWireGolden(t *testing.T) {
	cases := []struct {
		name string
		f    *Frame
		hex  string
	}{
		{
			// FEC repair frame (Kind 3, §2.5 reservation). Header carries the repair kind; payload opaque.
			name: "repair",
			f:    &Frame{Stream: 1, Kind: KindRepair, Codec: CodecNone, Seq: 5, Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
			hex:  "0001030000000000000500000000000000000000000000000004deadbeef",
		},
		{
			// HEVC keyframe with a 29.97 drop-frame timecode + PTS - the common video header shape.
			name: "video_keyframe_tc",
			f: &Frame{Stream: 7, Kind: KindVideo, Codec: CodecHEVC, Flags: FlagKeyframe, Seq: 100, PTS: 1_500_000_000,
				TC: Timecode{H: 1, M: 2, S: 3, F: 4, Rate: FPS2997}, Payload: []byte("nal")},
			hex: "00070105019e000000640001b3de0000000059682f00000000036e616c",
		},
	}
	for _, c := range cases {
		got := hex.EncodeToString(c.f.marshal(nil))
		if got != c.hex {
			t.Errorf("%s wire mismatch:\n got %s\nwant %s", c.name, got, c.hex)
		}
		// Round-trip the pinned bytes back to a Frame.
		raw, err := hex.DecodeString(c.hex)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parseFrame(raw)
		if err != nil {
			t.Fatalf("%s parse: %v", c.name, err)
		}
		frameEq(t, c.f, f)
	}
}

// Meta-frame golden: report + NACK ride stream 0 as KindMeta JSON (reserved v1 wire, §2.1/§2.5).
func TestMetaFrameGolden(t *testing.T) {
	rep, err := MetaFrame(Report{Type: MetaReport, Stream: 7, Packets: 1000, Octets: 250000, HighestSeq: 999,
		Lost: 3, FractionLost: 0.003, Jitter: 120000, WallNanos: 42, PTSNanos: 24}, 500)
	if err != nil {
		t.Fatal(err)
	}
	const wantReport = "000000000000000000000000000000000000000001f4000000897b2274223a227265706f7274222c2273747265616d223a372c227061636b657473223a313030302c226f6374657473223a3235303030302c2268695f736571223a3939392c226c6f7374223a332c22667261635f6c6f7374223a302e3030332c226a6974746572223a3132303030302c2277616c6c5f6e73223a34322c227074735f6e73223a32347d"
	if got := hex.EncodeToString(rep.marshal(nil)); got != wantReport {
		t.Errorf("report wire mismatch:\n got %s\nwant %s", got, wantReport)
	}
	nack, err := MetaFrame(NACK{Type: MetaNACK, Stream: 7, From: 10, To: 12}, 600)
	if err != nil {
		t.Fatal(err)
	}
	const wantNACK = "00000000000000000000000000000000000000000258000000297b2274223a226e61636b222c2273747265616d223a372c2266726f6d223a31302c22746f223a31327d"
	if got := hex.EncodeToString(nack.marshal(nil)); got != wantNACK {
		t.Errorf("nack wire mismatch:\n got %s\nwant %s", got, wantNACK)
	}

	// Decode helpers round-trip + reject the wrong type.
	gotRep, err := DecodeReport(rep)
	if err != nil || gotRep.Packets != 1000 || gotRep.Stream != 7 || gotRep.Lost != 3 {
		t.Fatalf("DecodeReport = %+v, err=%v", gotRep, err)
	}
	gotNACK, err := DecodeNACK(nack)
	if err != nil || gotNACK.From != 10 || gotNACK.To != 12 {
		t.Fatalf("DecodeNACK = %+v, err=%v", gotNACK, err)
	}
	if _, err := DecodeNACK(rep); err == nil {
		t.Fatal("DecodeNACK must reject a report frame")
	}
	if _, err := DecodeReport(nack); err == nil {
		t.Fatal("DecodeReport must reject a nack frame")
	}
}

// Sync ping/pong golden: the P2 clock-sync meta types (§2.3 tier 2) are additive stream-0 types -
// pinned here so the on-wire JSON shape never drifts.
func TestSyncMetaGolden(t *testing.T) {
	ping, err := MetaFrame(SyncPing{Type: MetaSync, ID: 7, T1: 1_000_000_001}, 1_000_000_001)
	if err != nil {
		t.Fatal(err)
	}
	const wantPing = "0000000000000000000000000000000000003b9aca01000000237b2274223a2273796e63222c226964223a372c227431223a313030303030303030317d"
	if got := hex.EncodeToString(ping.marshal(nil)); got != wantPing {
		t.Errorf("ping wire mismatch:\n got %s\nwant %s", got, wantPing)
	}
	pong, err := MetaFrame(SyncPong{Type: MetaSyncReply, ID: 7, T1: 1_000_000_001, T2: 1_000_500_001, T3: 1_000_600_001}, 1_000_600_001)
	if err != nil {
		t.Fatal(err)
	}
	const wantPong = "0000000000000000000000000000000000003ba3f1c1000000447b2274223a2273796e6372222c226964223a372c227431223a313030303030303030312c227432223a313030303530303030312c227433223a313030303630303030317d"
	if got := hex.EncodeToString(pong.marshal(nil)); got != wantPong {
		t.Errorf("pong wire mismatch:\n got %s\nwant %s", got, wantPong)
	}

	gotPing, err := DecodeSyncPing(ping)
	if err != nil || gotPing.ID != 7 || gotPing.T1 != 1_000_000_001 {
		t.Fatalf("DecodeSyncPing = %+v, err=%v", gotPing, err)
	}
	gotPong, err := DecodeSyncPong(pong)
	if err != nil || gotPong.T2 != 1_000_500_001 || gotPong.T3 != 1_000_600_001 || gotPong.T1 != gotPing.T1 {
		t.Fatalf("DecodeSyncPong = %+v, err=%v", gotPong, err)
	}
	if _, err := DecodeSyncPing(pong); err == nil {
		t.Fatal("DecodeSyncPing must reject a pong frame")
	}
	if _, err := DecodeSyncPong(ping); err == nil {
		t.Fatal("DecodeSyncPong must reject a ping frame")
	}
	// Malformed JSON payload fails decode, never panics.
	bad := &Frame{Stream: 0, Kind: KindMeta, Payload: []byte(`{"t":"sync",`)}
	if _, err := DecodeSyncPing(bad); err == nil {
		t.Fatal("truncated JSON must fail")
	}
	// A media-stream frame is never meta.
	if _, err := DecodeSyncPing(&Frame{Stream: 3, Kind: KindMeta, Payload: []byte(`{"t":"sync"}`)}); err == nil {
		t.Fatal("non-stream-0 frame must be rejected")
	}
}
