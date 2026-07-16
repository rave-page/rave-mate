package crewlink

// wire.go - relay payload wire shapes (CREW_RELAY_CONTRACT.md §4). This file IS the source of
// truth for the client-level payloads: JSON, discriminated by "t", explicit lowerCamel tags,
// opaque to the relay server (zero-knowledge stance - the server validates base64 + size only).
//
// pose mirrors mocapnode.Packet minus Quats/Present: mocapmaster.PoseStore.Accept recomputes
// both from Rots+BoneMask and never trusts incoming values, so the wire drops them.

import (
	"encoding/json"
	"time"

	"rave.page/mate/internal/mocapnode"
	"rave.page/mate/internal/mocappanel"
)

// Frame type discriminators (the "t" tag).
const (
	FrameTypePose = "pose" // node → master (directed)
	FrameTypeSync = "sync" // clock probe: node pings, master pongs
	FrameTypeCtrl = "ctrl" // master → node/room
)

// Ctrl ops.
const (
	CtrlOpKick   = "kick"
	CtrlOpConfig = "config"
)

// WireHeader mirrors mocappanel.Header field-for-field (wire-frozen lowerCamel tags).
type WireHeader struct {
	Version              uint16     `json:"version"`
	Flags                uint16     `json:"flags"`
	SourceTag            uint32     `json:"sourceTag"`
	SessionNonce         uint16     `json:"sessionNonce"`
	PanelSeq             uint32     `json:"panelSeq"`
	ServerTimeMs         int64      `json:"serverTimeMs"`
	NetUtcTicks          int64      `json:"netUtcTicks"`
	BpmX100              uint16     `json:"bpmX100"`
	DownbeatServerTimeMs int64      `json:"downbeatServerTimeMs"`
	BoneSlots            int        `json:"boneSlots"`
	DancerCount          int        `json:"dancerCount"`
	FrameCounter         uint32     `json:"frameCounter"`
	StageMin             [3]float64 `json:"stageMin"`
	StageSize            [3]float64 `json:"stageSize"`
}

// WireDancer mirrors mocappanel.Dancer minus Quats/Present (the master's store recomputes
// them from Rots+BoneMask; incoming values are untrusted and never cross the wire).
type WireDancer struct {
	LocalID  uint16    `json:"localId"`
	Flags    uint16    `json:"flags"`
	BoneMask uint32    `json:"boneMask"`
	HipsQ    [3]uint16 `json:"hipsQ"`
	Rots     []uint32  `json:"rots"`
}

// PoseFrame is one decoded panel frame, node → master (directed). CapturedAtNs is int64 unix
// nanoseconds IN THE MASTER'S CLOCK DOMAIN (contract §5): the node stamps it from its
// sync-disciplined SoftwareClock, so the master's staleness window applies directly.
type PoseFrame struct {
	T            string       `json:"t"` // FrameTypePose
	CapturedAtNs int64        `json:"capturedAtNs"`
	Header       WireHeader   `json:"header"`
	Dancers      []WireDancer `json:"dancers"`
}

// SyncFrame is the pairwise clock probe (medialink SyncPing/SyncPong semantics over the
// relay). Ping carries T1 only (initiator clock ns at send); the pong echoes ID+T1 and adds
// T2 (responder recv) / T3 (responder send) in the responder's clock domain. The initiator at
// T4 computes offset = ((T2−T1)+(T3−T4))/2 and RTT = (T4−T1)−(T3−T2).
type SyncFrame struct {
	T  string `json:"t"` // FrameTypeSync
	ID uint32 `json:"id"`
	T1 int64  `json:"t1"`
	T2 int64  `json:"t2,omitempty"`
	T3 int64  `json:"t3,omitempty"`
}

// IsPong reports whether the frame is a responder answer (T2/T3 stamped).
func (s SyncFrame) IsPong() bool { return s.T2 != 0 || s.T3 != 0 }

// CtrlFrame is a master-issued control frame ("kick": drop your session; "config": the
// master-authoritative panel geometry, informational for nodes).
type CtrlFrame struct {
	T         string      `json:"t"`  // FrameTypeCtrl
	Op        string      `json:"op"` // CtrlOpKick | CtrlOpConfig
	BoneSlots int         `json:"boneSlots,omitempty"`
	StageMin  *[3]float64 `json:"stageMin,omitempty"`
	StageSize *[3]float64 `json:"stageSize,omitempty"`
}

// frameType peeks the "t" discriminator without a full decode ("" on garbage).
func frameType(payload []byte) string {
	var head struct {
		T string `json:"t"`
	}
	if json.Unmarshal(payload, &head) != nil {
		return ""
	}
	return head.T
}

// PoseFromPacket converts one decoded node packet to its wire shape. capturedAtNs is the
// master-domain stamp (contract §5) - the caller derives it from the disciplined clock.
// Quats/Present are dropped by construction; slices are shared read-only.
func PoseFromPacket(pkt mocapnode.Packet, capturedAtNs int64) PoseFrame {
	h := pkt.Header
	dancers := make([]WireDancer, len(pkt.Dancers))
	for i := range pkt.Dancers {
		d := &pkt.Dancers[i]
		dancers[i] = WireDancer{
			LocalID: d.LocalID, Flags: d.Flags, BoneMask: d.BoneMask,
			HipsQ: d.HipsQ, Rots: d.Rots,
		}
	}
	return PoseFrame{
		T:            FrameTypePose,
		CapturedAtNs: capturedAtNs,
		Header: WireHeader{
			Version: h.Version, Flags: h.Flags, SourceTag: h.SourceTag,
			SessionNonce: h.SessionNonce, PanelSeq: h.PanelSeq,
			ServerTimeMs: h.ServerTimeMs, NetUtcTicks: h.NetUtcTicks,
			BpmX100: h.BpmX100, DownbeatServerTimeMs: h.DownbeatServerTimeMs,
			BoneSlots: h.BoneSlots, DancerCount: h.DancerCount, FrameCounter: h.FrameCounter,
			StageMin: h.StageMin, StageSize: h.StageSize,
		},
		Dancers: dancers,
	}
}

// Packet rebuilds the mocapnode.Packet for master ingest: CapturedAt = time.Unix(0, ns) in
// the master domain; Quats/Present nil - mocapmaster.PoseStore.Accept recomputes them.
func (p PoseFrame) Packet() mocapnode.Packet {
	h := p.Header
	dancers := make([]mocappanel.Dancer, len(p.Dancers))
	for i := range p.Dancers {
		d := &p.Dancers[i]
		dancers[i] = mocappanel.Dancer{
			LocalID: d.LocalID, Flags: d.Flags, BoneMask: d.BoneMask,
			HipsQ: d.HipsQ, Rots: d.Rots,
		}
	}
	return mocapnode.Packet{
		CapturedAt: time.Unix(0, p.CapturedAtNs),
		Header: mocappanel.Header{
			Version: h.Version, Flags: h.Flags, SourceTag: h.SourceTag,
			SessionNonce: h.SessionNonce, PanelSeq: h.PanelSeq,
			ServerTimeMs: h.ServerTimeMs, NetUtcTicks: h.NetUtcTicks,
			BpmX100: h.BpmX100, DownbeatServerTimeMs: h.DownbeatServerTimeMs,
			BoneSlots: h.BoneSlots, DancerCount: h.DancerCount, FrameCounter: h.FrameCounter,
			StageMin: h.StageMin, StageSize: h.StageSize,
		},
		Dancers: dancers,
	}
}
