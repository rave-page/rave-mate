package medialink

// Negotiation rides the EXISTING peerlink control plane (JSON on ChanBus / eventbus): instances
// advertise their media sources + sinks, one side offers a route, the other answers with the media
// socket address. The AEAD key is NOT carried here - it's derived (transport.go) from the peerlink
// session secret the paired instances already share. peerbridge marshals these onto the bus.

// Bus topics for media-plane negotiation + the timecode plane.
const (
	TopicAdvert = "media.advert"
	TopicOffer  = "media.offer"
	TopicAnswer = "media.answer"
	TopicTC     = "media.tc" // §4 master TC announces (~4 Hz + on jump; P3, tcplane.go)
)

// Transport selects the media socket profile (§2.1 reservation). Absent/empty = TCP.
const (
	TransportTCP = "tcp" // P1–P7 (default): AEAD over a reliable stream
	TransportUDP = "udp" // §2.5 datagram profile, implemented P8 (explicit nonce + replay window)
)

// FEC is the reserved per-stream forward-error-correction descriptor (§2.5 / D9). Absent = off.
// Scheme "xor" = SMPTE 2022-1-style interleaved; "rs" = Reed-Solomon (RFC 8627-style). k data /
// n total packets per block. Repair data rides KindRepair frames. Never negotiated ON in P1–P7.
type FEC struct {
	Scheme string `json:"scheme"` // "" = none, "xor", "rs"
	K      int    `json:"k,omitempty"`
	N      int    `json:"n,omitempty"`
}

// Caps is the additive session-capability extension (P2+; a "negotiated extension" under the §2.1
// v1 rule - never a field change). Absent = P1 peer: no meta-frame generation, P1 codec echo.
// On an Advert it's the node-wide capability set; on an Offer it's what the requester supports;
// on an Answer it's the granted intersection - each side only emits what the other granted.
type Caps struct {
	Report bool `json:"report,omitempty"` // RFC 3550-style report meta frames (§7, P2)
	Sync   bool `json:"sync,omitempty"`   // NTP-style clock-sync meta frames (§2.3 tier 2, P2)
	Clock  bool `json:"clock,omitempty"`  // §4 media.clock: TC-master candidate (advert-level, P3)

	// §3.2 codec matrix (codec.go). Advert: the node's full sets. Offer: the requester's
	// Decoders. Answer: Encoders holds the ONE encoder chosen for the route.
	Encoders []string `json:"enc,omitempty"` // probed working video encoders (ffmpeg names)
	Decoders []string `json:"dec,omitempty"` // decodable video codecs (Decode* names)
}

// SourceDesc describes an available media source on an instance.
type SourceDesc struct {
	ID       string  `json:"id"`   // stable id on the owning node
	Name     string  `json:"name"` // human label ("OBS Spout", "Logitech Brio")
	Kind     Kind    `json:"kind"`
	Codec    Codec   `json:"codec"`              // native/preferred codec
	Width    int     `json:"width,omitempty"`    // video
	Height   int     `json:"height,omitempty"`   // video
	FPS      float64 `json:"fps,omitempty"`      // video
	Sample   int     `json:"sample,omitempty"`   // audio sample rate (Hz)
	Channels int     `json:"channels,omitempty"` // audio
}

// SinkDesc describes a place an instance can present an incoming stream (a Spout sender name, an
// audio output device).
type SinkDesc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
}

// Advert announces a node's media sources + sinks on the bus (Node filled by the bus origin).
type Advert struct {
	Node    string       `json:"node,omitempty"`
	Sources []SourceDesc `json:"sources,omitempty"`
	Sinks   []SinkDesc   `json:"sinks,omitempty"`
	Caps    *Caps        `json:"caps,omitempty"` // node capability set (P2+; absent = P1 peer)
}

// Offer requests a route: stream SourceID (on Target) into the requester's SinkID. The requester
// (offer origin on the bus) owns the sink + dials; Target owns the source + listens. Codec is the
// requester's negotiated choice. Transport/NACK/FEC are the §2.1 reservations (absent = TCP,off,off).
type Offer struct {
	Session   string `json:"session"`          // unique per route
	Target    string `json:"target,omitempty"` // node id that owns SourceID (answers this offer)
	SourceID  string `json:"source,omitempty"`
	SinkID    string `json:"sink,omitempty"`
	Codec     Codec  `json:"codec"`
	Transport string `json:"transport,omitempty"` // "" = tcp (§2.1)
	NACK      bool   `json:"nack,omitempty"`      // requester supports NACK/retransmit (§2.5)
	FEC       *FEC   `json:"fec,omitempty"`       // reserved (§2.5)
	Caps      *Caps  `json:"caps,omitempty"`      // requester session caps (P2+; absent = P1 peer)
	Bitrate   int    `json:"br,omitempty"`        // video bitrate budget, kbps (P4 additive; 0 = sender default)
}

// Answer responds to an Offer. On accept, Addr is the "host:port" of the media listener to dial and
// Stream is the stream id to expect within that Conn. The dialer is the medialink initiator.
// Transport/NACK/FEC echo the negotiated result (subset of what the offer asked, absent = default).
type Answer struct {
	Session   string `json:"session"`
	Accept    bool   `json:"accept"`
	Reason    string `json:"reason,omitempty"`
	Addr      string `json:"addr,omitempty"`
	Codec     Codec  `json:"codec"`
	Stream    uint16 `json:"stream"`
	Transport string `json:"transport,omitempty"` // "" = tcp (§2.1)
	NACK      bool   `json:"nack,omitempty"`      // NACK/retransmit armed both ends (§2.5)
	FEC       *FEC   `json:"fec,omitempty"`       // reserved (§2.5)
	Caps      *Caps  `json:"caps,omitempty"`      // granted session caps (P2+; absent = P1 peer)
}
