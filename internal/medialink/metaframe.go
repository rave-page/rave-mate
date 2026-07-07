package medialink

import (
	"encoding/json"
	"errors"
)

// Meta-frames ride stream 0 as KindMeta with a JSON payload tagged by MetaType (§2.1). The wire
// FREEZES these types in v1 so the loss machinery (§2.5) bolts on without a break:
//   - MetaReport: RFC 3550 §6.4 SR/RR-semantic stats (sender packet/octet counts + wall↔PTS anchor;
//     receiver fraction-lost, cumulative-lost, interarrival jitter per §A.8). Emitted from P2.
//   - MetaNACK:   RFC 4585-semantic seq-range loss report. Emitted from P8 (UDP profile).
//
// P1 defines + golden-tests the layout; nothing GENERATES reports/NACKs yet (P1 is the transport +
// session floor). Parsers accept them today so a newer peer talking to a P1 peer degrades cleanly.

// MetaType discriminates a meta-frame JSON payload on stream 0.
type MetaType string

const (
	MetaReport MetaType = "report" // RFC 3550 §6.4 SR/RR-style stats (§2.1)
	MetaNACK   MetaType = "nack"   // RFC 4585-style loss NACK (§2.5, reserved)

	// P2 additive types (new frame types under the §2.1 v1 rule; only sent when the session's
	// Caps granted sync - a P1 peer never receives them).
	MetaSync      MetaType = "sync"  // clock-sync request (§2.3 tier 2)
	MetaSyncReply MetaType = "syncr" // clock-sync response
)

// metaStream is the reserved control stream id for meta-frames (RTP SSRC-0 analogue).
const metaStream uint16 = 0

var errNotMeta = errors.New("medialink: frame is not a stream-0 meta frame")

// Report is the RFC 3550 §6.4-semantic periodic stats frame. Sender fields (Packets/Octets/
// WallNanos/PTSNanos) anchor wall↔media-clock; receiver fields (FractionLost/Lost/Jitter) are the
// industry-comparable loss/jitter numbers (§A.8 jitter is in PTS nanosecond units here, not 90 kHz
// RTP ticks). Zero fields are simply "not reported this interval".
type Report struct {
	Type         MetaType `json:"t"`      // always MetaReport
	Stream       uint16   `json:"stream"` // stream the report concerns
	Packets      uint64   `json:"packets,omitempty"`
	Octets       uint64   `json:"octets,omitempty"`
	HighestSeq   uint32   `json:"hi_seq,omitempty"`
	Lost         int64    `json:"lost,omitempty"`      // cumulative packets lost (§6.4.1)
	FractionLost float64  `json:"frac_lost,omitempty"` // fraction lost since last report [0,1]
	Jitter       float64  `json:"jitter,omitempty"`    // interarrival jitter, ns (§A.8)
	WallNanos    int64    `json:"wall_ns,omitempty"`   // sender wall clock at anchor
	PTSNanos     int64    `json:"pts_ns,omitempty"`    // media-clock PTS at anchor
}

// NACK is the RFC 4585-semantic loss report: request retransmit of seqs [From,To] on Stream. A
// zero-length range (From>To) with FrameLevel set is a PLI-style keyframe request (§2.5).
type NACK struct {
	Type       MetaType `json:"t"`      // always MetaNACK
	Stream     uint16   `json:"stream"` // stream the loss concerns
	From       uint32   `json:"from"`   // first missing seq (inclusive)
	To         uint32   `json:"to"`     // last missing seq (inclusive)
	FrameLevel bool     `json:"pli,omitempty"`
}

// SyncPing is the NTP-style (RFC 5905 on-wire semantics, pairwise, §2.3 tier 2) clock probe:
// T1 = requester media-clock at send. Stateless for the requester - the responder echoes T1 back.
type SyncPing struct {
	Type MetaType `json:"t"`  // always MetaSync
	ID   uint32   `json:"id"` // probe id (debug/tracing; correlation rides the echoed T1)
	T1   int64    `json:"t1"` // requester media-clock ns at send
}

// SyncPong answers a SyncPing: T2 = responder media-clock at receive, T3 = at reply send. The
// requester at T4 computes offset = ((T2−T1)+(T3−T4))/2 and RTT = (T4−T1)−(T3−T2).
type SyncPong struct {
	Type MetaType `json:"t"` // always MetaSyncReply
	ID   uint32   `json:"id"`
	T1   int64    `json:"t1"` // echoed
	T2   int64    `json:"t2"`
	T3   int64    `json:"t3"`
}

// MetaFrame builds a stream-0 KindMeta frame from a marshalable meta payload (Report/NACK/sync).
// PTS is the media-clock stamp of the report; Seq is assigned by the route like any other frame.
func MetaFrame(payload any, pts int64) (*Frame, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Frame{Stream: metaStream, Kind: KindMeta, Codec: CodecNone, PTS: pts, Payload: b}, nil
}

// metaType peeks the "t" tag of a meta-frame payload without full decode.
func metaType(f *Frame) (MetaType, error) {
	if f.Kind != KindMeta || f.Stream != metaStream {
		return "", errNotMeta
	}
	var head struct {
		T MetaType `json:"t"`
	}
	if err := json.Unmarshal(f.Payload, &head); err != nil {
		return "", err
	}
	return head.T, nil
}

// decodeMeta decodes a meta-frame payload as T after checking its "t" tag (errNotMeta otherwise).
func decodeMeta[T any](f *Frame, want MetaType) (T, error) {
	var out T
	t, err := metaType(f)
	if err != nil {
		return out, err
	}
	if t != want {
		return out, errNotMeta
	}
	err = json.Unmarshal(f.Payload, &out)
	return out, err
}

// DecodeReport decodes a MetaReport frame (errNotMeta if it isn't one).
func DecodeReport(f *Frame) (Report, error) { return decodeMeta[Report](f, MetaReport) }

// DecodeNACK decodes a MetaNACK frame (errNotMeta if it isn't one).
func DecodeNACK(f *Frame) (NACK, error) { return decodeMeta[NACK](f, MetaNACK) }

// DecodeSyncPing decodes a MetaSync frame (errNotMeta if it isn't one).
func DecodeSyncPing(f *Frame) (SyncPing, error) { return decodeMeta[SyncPing](f, MetaSync) }

// DecodeSyncPong decodes a MetaSyncReply frame (errNotMeta if it isn't one).
func DecodeSyncPong(f *Frame) (SyncPong, error) { return decodeMeta[SyncPong](f, MetaSyncReply) }
