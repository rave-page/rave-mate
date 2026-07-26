package featurehost

// Wire contract for the "media" feature child (issue #44): the medialink route plane +
// mediaroute receive manager + webcam manager run in ONE isolated subprocess so a media
// RAM/CPU runaway or cgo fault (Spout/DirectShow/ffmpeg) can never starve the daemon host.
//
// Only the three Phase-1 control interfaces cross this boundary (medialink.MediaControl,
// mediaroute.ReceiveControl, webcam.CamControl) - their return types already JSON-marshal, so
// telemetry is mirrored up as whole snapshots and the UI's frequent polls become local reads.
// Frame-bearing internals (Register*/Offer/Close callbacks, Spout handles, raw frames) never
// appear here; they stay in-proc INSIDE the child (mediaroute/webcam call the router directly).
//
// Two hazards the plain-forward model can't handle, addressed below:
//   - The media clock is disciplined child-side (that's where the sync probes live) but the
//     timecode plane reads it daemon-side → the child mirrors its offset up (evMediaClockOffset)
//     and the daemon slews its own mediaClock to match.
//   - The per-peer AEAD MediaSecret is dynamic (peers connect/disconnect at runtime), not a single
//     spawn-time token → secrets are pushed per peer (evMediaSecret) and re-sent in full on every
//     respawn via the Host Init snapshot (mediaInit.Secrets).

import (
	"encoding/json"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/webcam"
)

// ── parent → child ──────────────────────────────────────────────────────────

// mediaInit is the spawn snapshot, re-evaluated on every (re)spawn so a restarted child rebuilds
// full desired state without waiting for live events. Secrets carries ALL currently-known peers.
type mediaInit struct {
	Self     string                  `json:"self"`
	Label    string                  `json:"label,omitempty"` // human label (hostname) for cam Status
	MediaCfg config.MediaLinkFeature `json:"mediaCfg"`
	CamCfg   config.WebcamFeature    `json:"camCfg"`
	Secrets  []peerSecret            `json:"secrets,omitempty"`
	Encoders []string                `json:"encoders,omitempty"` // codec caps from the async probe
	Decoders []string                `json:"decoders,omitempty"`
	SyncPeer string                  `json:"syncPeer,omitempty"` // TC-elected clock master ("" = none)
	// MemLimitMB is the child's job-object RAM cap (0 = uncapped). The child sets its own Go soft
	// memory limit below it so the GC fights a frame-buffer runaway BEFORE the OS kills the process.
	MemLimitMB int `json:"memLimitMb,omitempty"`
}

// peerSecret is one peer's media AEAD key. Secret==nil drops the peer (disconnect).
type peerSecret struct {
	Node   string `json:"node"`
	Secret []byte `json:"secret,omitempty"`
}

// Parent→child event names.
const (
	evMediaSecret   = "secret"   // peerSecret: upsert (Secret set) or drop (Secret nil)
	evMediaSyncPeer = "syncPeer" // syncPeerEvent
	evMediaCodecs   = "codecs"   // codecCaps
	evMediaAdvert   = "advert"   // no payload - trigger a re-advertise
	evMediaBus      = "busDown"  // busEvent: a daemon mesh-bus event forwarded to the child
)

// syncPeerEvent pins the clock-sync master ("" = measure-only / authoritative here).
type syncPeerEvent struct {
	Node string `json:"node"`
}

// codecCaps carries the async probe result (which encoders/decoders to advertise).
type codecCaps struct {
	Encoders []string `json:"encoders,omitempty"`
	Decoders []string `json:"decoders,omitempty"`
}

// busEvent is one media-negotiation event crossing the boundary (advert/offer/answer/tc/cam),
// preserving Origin/Local so "(this PC)" tagging and self-echo suppression survive the hop.
type busEvent struct {
	Topic  string          `json:"topic"`
	Origin string          `json:"origin,omitempty"`
	Local  bool            `json:"local,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ── child → parent ──────────────────────────────────────────────────────────

// Child→parent event names.
const (
	evMediaTelemetry   = "telemetry"   // mediaTelemetry: mirrored control-surface snapshot (~1 Hz)
	evMediaClockOffset = "clockOffset" // clockOffsetEvent: child-disciplined offset → slew daemon clock
	evMediaBusUp       = "busUp"       // busEvent: a child-produced event to republish on the mesh bus
)

// mediaTelemetry is the whole daemon/UI-facing surface mirrored up so poll-style reads
// (Stats/SyncStats/ClockQuality/RemoteVideoSources/Receives/Instances/Encoders) stay local.
type mediaTelemetry struct {
	Routes     []medialink.RouteStat     `json:"routes,omitempty"`
	Sync       []medialink.SyncStat      `json:"sync,omitempty"`
	Clock      medialink.ClockQuality    `json:"clock"`
	Encoders   []string                  `json:"encoders,omitempty"`
	Remote     []mediaroute.RemoteSource `json:"remote,omitempty"`
	Receives   []mediaroute.Receive      `json:"receives,omitempty"`
	Cams       []webcam.Instance         `json:"cams,omitempty"`
	CamRunning bool                      `json:"camRunning,omitempty"` // local cam live → daemon advertises CapCam
}

// clockOffsetEvent mirrors the child media clock's Now() so the daemon's mediaClock - which the
// timecode plane reads - slews to match. We mirror the VALUE (not just the discipline offset)
// because the child and daemon processes have different monotonic bases; MirrorNow absorbs both the
// base skew and the discipline offset. Both clocks then free-run at real-time rate between mirrors.
type clockOffsetEvent struct {
	NowNs  int64  `json:"nowNs"`
	Tier   string `json:"tier,omitempty"`
	Locked bool   `json:"locked"`
}

// ── methods (request/response, Host.Call) ───────────────────────────────────

// Method names for calls that need a return value or delivery confirmation.
const (
	mStartReceive = "startReceive" // startReceiveReq → startReceiveResp
	mStopReceive  = "stopReceive"  // stopReceiveReq → (ok)
	mCommand      = "command"      // commandReq → (ok/err)
)

type startReceiveReq struct {
	Peer     string `json:"peer"`
	SourceID string `json:"sourceId"`
}

type startReceiveResp struct {
	Session string `json:"session"`
}

type stopReceiveReq struct {
	Session string `json:"session"`
}

type commandReq struct {
	Cmd webcam.Cmd `json:"cmd"`
}
