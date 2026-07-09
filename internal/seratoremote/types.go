package seratoremote

// NumDecks is the deck count the protocol supports (0-based indices 0..3, verified via
// static analysis of the Serato binary - "Deck 0..3 change to INT mode" strings, no Deck 4).
const NumDecks = 4

// ServiceType is the DNS-SD type Serato DJ Pro browses for. We advertise it (empty TXT) and
// accept Serato's inbound TCP connections.
const ServiceType = "_SeratoIOSRemote._tcp.local."

// OSC handshake + status paths (see docs/protocol.md in serato-connect).
const (
	pathAuthorizeRequest  = "/StreamMgmt/Authorize/Request"
	pathAuthorizeResponse = "/StreamMgmt/Authorize/Response"
	pathPairingPair       = "/StreamMgmt/Pairing/Pair"
	pathPairingStatus     = "/StreamMgmt/Pairing/StatusChanged"
	pathPairingUnpair     = "/StreamMgmt/Pairing/UnPair"
	pathPing              = "/Ping"

	// defaultPeerUUID is the peerUUID sent in Pair (,ssi). Fixed so re-pairs are stable.
	defaultPeerUUID = "5241564d-4154-4500-0000-000000000001"

	pathSongTitle    = "/Status/Deck/Song/Title"
	pathSongArtist   = "/Status/Deck/Song/Artist"
	pathSongFilepath = "/Status/Deck/Song/Filepath"
	pathSongValid    = "/Status/Deck/Song/Valid"
	pathPlayhead     = "/Status/Deck/Playhead"
	pathAutoLoopOn   = "/Status/Deck/Loop/AutoLoopOn"
	pathBeatLength   = "/Status/Deck/Loop/BeatLength"
	pathLoopRollOn   = "/Status/Deck/Loop/LoopRollOn"
	pathUpfader      = "/Status/Video/Deck/Mixer/Upfader"
	pathCrossfader   = "/Status/Video/Mixer/Crossfader"
)

// DefaultSubscriptionTopics is the /Register/Status/<topic> set sent on pairing. It is
// UNVERIFIED whether registration gates the stream or is a filter (docs/open-questions.md).
var DefaultSubscriptionTopics = []string{
	"/Register/Status/Deck/Playhead",
	"/Register/Status/Deck/Song/Title",
	"/Register/Status/Deck/Song/Artist",
	"/Register/Status/Deck/Song/Filepath",
	"/Register/Status/Deck/Song/Valid",
	"/Register/Status/Deck/Loop/AutoLoopOn",
	"/Register/Status/Deck/Loop/BeatLength",
	"/Register/Status/Deck/Loop/LoopRollOn",
	"/Register/Status/Video/Deck/Mixer/Upfader",
	"/Register/Status/Video/Mixer/Crossfader",
}

// Track is a deck's loaded track as seen over the Remote protocol (a small field set;
// richer metadata must be looked up from filePath).
type Track struct {
	Title    string
	Artist   string
	FilePath string
	Valid    bool
	HasValid bool // whether Valid was ever reported (distinguishes "not loaded" from "unknown")
}

// Playhead is a deck's live playhead. The three floats' semantics are UNVERIFIED
// (docs/open-questions.md); Raw carries them verbatim, the named fields are best-guesses.
type Playhead struct {
	PositionSeconds float32 // raw[0] - most likely position in seconds
	LengthSeconds   float32 // raw[1] - most likely track length in seconds
	BPM             float32 // raw[2] - most likely BPM (or play-rate)
	Raw             [3]float32
}

// Loop is a deck's loop state.
type Loop struct {
	AutoLoopOn bool
	BeatLength float32
	LoopRollOn bool
}

// DeckChange is emitted on track load/eject for a deck (0-based index).
type DeckChange struct {
	Deck     int
	Track    *Track // nil = ejected
	Previous *Track
}

// PlayheadEvent carries a deck's playhead update (0-based deck index).
type PlayheadEvent struct {
	Deck int
	Playhead
}

// LoopEvent carries a deck's loop-state change (0-based deck index).
type LoopEvent struct {
	Deck int
	Loop
}

// MixerEvent carries a mixer change: either the crossfader or one deck's upfader.
type MixerEvent struct {
	Crossfader    float32
	HasCrossfader bool
	Deck          int     // valid when HasUpfader
	Upfader       float32 // per-deck channel fader
	HasUpfader    bool
}

// FrameEvent is a raw inbound frame surfaced for debug capture logging: the decoded path,
// type-tag, and stringified args (or the hex of a packet that failed to parse).
type FrameEvent struct {
	Path    string
	TypeTag string
	Args    []string
	Hex     string // set when the frame failed to decode
}

// Callbacks receives typed events from a Receiver. Any field may be nil.
type Callbacks struct {
	OnPeerConnected    func(addr string)
	OnPeerDisconnected func(addr string)
	OnPaired           func(addr string)
	OnDeckChange       func(DeckChange)
	OnPlayhead         func(PlayheadEvent)
	OnLoop             func(LoopEvent)
	OnMixer            func(MixerEvent)
	OnPing             func()
	// OnFrame fires for EVERY inbound frame when debug capture is enabled - the key
	// instrumentation for completing the handshake RE against live Serato.
	OnFrame func(FrameEvent)
}
