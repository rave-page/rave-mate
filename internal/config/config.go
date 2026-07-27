// Package config holds typed, versioned user config persisted as JSON under the OS config
// dir. Every capability is an independently toggleable feature so a disabled feature has
// zero runtime footprint (the module manager never starts it). API URL resolution mirrors
// the web repo: explicit env override, else the development API by default.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/cameraosc"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vrbind"
)

const (
	appDirName = "rave-mate"
	fileName   = "config.json"

	devAPI  = "https://development.api.rave.page"
	prodAPI = "https://api.rave.page"

	// configVersion bumps when the schema changes; Load migrates older files.
	// v2 added TranscodeFeature.Presets (additive; older files load with no custom presets).
	// v3 added the DJ-data aggregation features (NML, MIDI, Recorder, NowPlayingFile);
	// additive - absent keys keep their Default() values via load-over-default.
	// v4 added the LAN peer link (Peers); additive, off by default (opt-in).
	// v5 added the Icecast set-capture receiver (SetCapture); additive, off by default.
	// v6 added cross-DJ-software library sync (LibrarySync); additive, off by default.
	// v7 added the live browser overlay server (OverlayWeb); additive, off by default.
	// v8 added the native PNG per-deck renderer (OverlayPNG) + obs-websocket renderer
	// (OverlayOBS); both additive, off by default.
	// v9 added the GPU/IPC video-share sink (VideoShare: Spout/Syphon/PipeWire); additive, off.
	// v10 added native audio recording (AudioRecord) + SetCapture.MetadataOnly; additive, off.
	// v11 added VideoShare.RenderScale (supersample for crisp 4K display); additive.
	// v12 added cross-DJ-software live now-playing sources: Serato (History sessions),
	// VirtualDJ (NetCtl/OS2L/tracklist), Rekordbox (db-poll/memory-read); all additive, off.
	// v13 added the scrolling-waveform + EQ/FX overlay panel (OverlayWaveform); additive, off.
	// v14 added Twitch integration (chat/alerts/title-control/moderation); additive, off.
	// v15 added VR overlays (OpenVR/SteamVR: chat/alert panels in-headset); additive, off.
	// v16 added application groups (relaunch a DJ-rig app set after a crash); additive, off.
	// v17 added the timecode outputs (LTC audio / MTC / Art-Net TimeCode house clock); additive, off.
	// v18 added the DMX plane (Art-Net ingest/emit + VRSL video grid); additive, off.
	// v19 added the DMX→MIDI VRChat bridge (DMXMIDI); additive, off.
	// v20 added the local RTSP performer chain (RTSPServe); additive, off.
	// v21 added VR wrist quick buttons (VROverlay.QuickButtons) + per-VRChat-world layout
	// bindings (WorldLayouts/WorldLayoutMode); additive - absent keys keep defaults.
	// v22 added file transfer between paired instances (FileXfer); additive, off by default.
	// v23 added World Sync (WorldSync): GitHub-gist-published VRChat world permission
	// lists + poster/events/now-playing display channels; additive, off by default.
	// v24 added the webcam/UVC source (Webcam: dshow capture → Spout + PTZ control);
	// additive, off by default. Later additive at v24 (no bump - zero value = old
	// behaviour): MediaLink (P4 video routes: codec/bitrate + Spout sharing). Also additive
	// at v24 (zero value = OS locale→en): UI.Language (webui i18n locale; internal/i18n).
	// v25 added the Serato Live Playlist remote scrape (Serato.LivePlaylist*); additive, off.
	// v26 added the MIDI mixer channel count (MIDIController.Channels, 1..8); additive
	// (zero value normalized to DefaultMIDIChannels on load).
	// v27 added native multi-controller MIDI-learn (MIDI.Controllers: per-controller input
	// port + learned bindings, optional THRU) + the two-port loopMIDI DJ router
	// (MIDI.Bridge); all additive, absent = off/empty.
	// v28 added the rave.page account bridge (AccountBridge: reach this instance from off-LAN
	// through the account relay; LocalStudio sub-toggle serves the Local Studio channel over
	// it). Additive, off by default. The TOTP secret + trusted-session tokens do NOT live here -
	// they are sealed in the bbolt authz bucket (config.json is plaintext 0600).
	// v29 extended vrbind.MIDIKey (VROverlay.Binds[].midi: port/mode/step/rev - encoder +
	// hold semantics for the desktop-UI actions) + MIDI.DisableUIBinds; all additive, zero
	// values = the old press-edge behavior, no migration.
	// v30 added per-device UI-mapping profiles (MIDI.DisabledBindProfiles: profile keys whose
	// desktop-UI binds are paused). Additive - profiles are DERIVED from each bind's captured
	// port ("" = the any-device profile), so v29 binds keep firing identically; the disable
	// list starts empty (all profiles active).
	// v31 added the VRSL DMX-over-video stream (Stream): renders the DMX universe store into a
	// VRSL-compatible video grid, ffmpeg-encodes it, and pushes RTMP/WHIP to a transcode service
	// for VRChat playback. Additive, off by default (opt-in; needs ffmpeg + a push URL).
	// v32 added the WorldSync hosted publish path (PublishMode/HostedWorldID + mode-agnostic
	// LiveModules pointer store): live modules can publish via rave.page's worldlive API instead
	// of the user's own gist token. Additive - empty PublishMode = "direct" (the old behaviour).
	// (Was v31 on its feature branch; renumbered at the development merge - migrate() is
	// version-agnostic, additive fields carry over regardless of the loaded number.)
	// v33 added the WorldSync hosted per-group access model (AccessOn/AccessRules/AccessUsers/
	// AccessGroups/AccessGistID): the rave.live/access module. Additive - AccessOn off = the
	// old flat allow.txt lists only; group secret codes live here plaintext, hashed on publish.
	// Later additive at v33 (no bump - zero value = off): Mocap (capture master: mocap-panel
	// capture -> pose store -> composite mocap region overlaid on the VRSL stream).
	// v34 added the DMX lighting-cue recorder (LightCue: enable/RecordDir/Hz), the sACN (E1.31)
	// DMX source toggle (DMX.SACN + DMX.SACNUniverses), and WorldSync.LightCuesGistID (the take
	// gist target). All additive, zero values = off/defaults - old configs load unchanged.
	// (Was v31 on its feature branch; renumbered at the development merge.)
	// Later additive at v34 (no bump - zero value = builtin Go workers): Workers.ProbeExe
	// (external probe-worker executable, e.g. the Zig rave-probe; ZIG_MIGRATION P4).
	// v35 removed Twitch.AutoConnect (dead - never read anywhere; the twitch feature now runs
	// as an always-listening child that connects whenever enabled + signed in, so chat/alerts
	// are captured with no UI open). Old configs with the key load fine (unknown-field ignore).
	configVersion = 35

	// DefaultMIDIChannels is the out-of-box MIDI-mixer channel/deck count (decks A..D).
	DefaultMIDIChannels = 4
	// MaxMIDIChannels caps the MIDI-mixer channel/deck count (decks A..H = wire channels 0..7).
	MaxMIDIChannels = 8

	// ArtNetPort is the standard Art-Net UDP port (ArtTimeCode + DMX default target).
	ArtNetPort = 6454

	// IcecastPort is the default local Icecast-source receiver port (Traktor broadcast target).
	IcecastPort = 8000

	// TraktorPort is the default Traktor Pro 4 bridge port (electron/src/main/traktor.ts).
	TraktorPort = 8080

	// OverlayWebPort is the default loopback port for the browser overlay server (OBS Browser source).
	OverlayWebPort = 47640

	// OS2LPort is the default TCP port our OS2L server listens on (advertised via mDNS
	// _os2l._tcp so VirtualDJ auto-discovers + connects).
	OS2LPort = 47641

	// DefaultTwitchClientID is the bundled "Rave-Mate" Twitch application client id. It's a public
	// client id (NOT a secret) - Device Code Flow needs no secret. Users may override it with their
	// own app in TwitchFeature.ClientID.
	DefaultTwitchClientID = "l1xv1ctqoodyyiois97dbyvdij7h6x"
)

// Config is the persisted user configuration.
type Config struct {
	Version     int     `json:"version"`
	APIBaseURL  string  `json:"apiBaseURL"`        // resolved rave.page API base
	StartHidden bool    `json:"startHidden"`       // launch to tray, no window
	WindowW     float32 `json:"windowW,omitempty"` // last user window size (Fyne units); 0 = open at 85% of screen
	WindowH     float32 `json:"windowH,omitempty"`
	// DisableCrashGuardian turns OFF the auto-relaunch-after-crash supervisor. Inverted so the
	// zero value (existing + fresh configs) keeps the guardian ON without a version migration.
	DisableCrashGuardian bool `json:"disableCrashGuardian,omitempty"`
	// PreReleaseWarnedFor is the version string the alpha/beta launch warning was last shown
	// for (once per version; internal dev/CI builds never warn). Additive, no version bump.
	PreReleaseWarnedFor string `json:"preReleaseWarnedFor,omitempty"`
	// UpdateNotifiedFor is the release version the update-available notification (tray balloon +
	// toast) last fired for - each new version notifies exactly once, surviving restarts.
	// Additive, no version bump.
	UpdateNotifiedFor string `json:"updateNotifiedFor,omitempty"`
	// DashboardCards is the ordered enabled dashboard card ids (ui registry);
	// empty = registry defaults.
	DashboardCards []string `json:"dashboardCards,omitempty"`
	Features       Features `json:"features"`
}

// Features is the master capability switchboard. Each field gates one subsystem.
type Features struct {
	Traktor       TraktorFeature      `json:"traktor"`       // DJ-software (Traktor Pro 4) bridge
	StreamBridge  StreamBridgeFeature `json:"streamBridge"`  // live set → rave.page stream ingest (auto-driven by OBS)
	Transcode     TranscodeFeature    `json:"transcode"`     // ffmpeg transcode workers
	Workers       WorkersFeature      `json:"workers"`       // worker-backend overrides (external probe exe)
	StudioChannel Toggle              `json:"studioChannel"` // web↔desktop Local Studio WS channel
	OBS           OBSFeature          `json:"obs"`           // OBS obs-websocket control + settings validation
	Library       Toggle              `json:"library"`       // native file browser + media metadata viewer
	MediaEditor   Toggle              `json:"mediaEditor"`   // poster/thumbnail composer
	Player        PlayerFeature       `json:"player"`        // in-app video player (mpv engine; window-embed)
	Fingerprint   Toggle              `json:"fingerprint"`   // Chromaprint fingerprinting (needs fpcalc)
	VRChat        VRChatFeature       `json:"vrchat"`        // client-side VRChat API bridge
	VRCTools      VRCToolsFeature     `json:"vrcTools"`      // VRChat screenshot organizer + camera-path manager
	VR            Toggle              `json:"vr"`            // VR runtime support (OpenVR/OpenXR)
	VROverlay     VROverlayFeature    `json:"vrOverlay"`     // VR overlays (OpenVR chat/alert panels in-headset)
	Unity         UnityFeature        `json:"unity"`         // Unity-project integration: install the rave.page editor plugin + export motion takes
	Twitch        TwitchFeature       `json:"twitch"`        // Twitch chat/alerts/title-control/moderation
	STT           STTFeature          `json:"stt"`           // speech-to-text (Whisper) → Twitch chat
	WorldSync     WorldSyncFeature    `json:"worldSync"`     // VRChat world gist feeds (perms/posters/events/now-playing)
	Notifications Toggle              `json:"notifications"` // native desktop notifications
	UI            UIFeature           `json:"ui"`            // UI renderer: Fyne (default) or the HTML/CSS webview

	// DJ-data aggregation sources + sinks (the session hub fuses these into one state).
	NML            NMLFeature        `json:"nml"`            // Traktor history/collection file source
	MIDI           MIDIFeature       `json:"midi"`           // MIDI-in source (Denon stock map + custom TSI)
	ProDJLink      ProDJLinkFeature  `json:"proDjLink"`      // Pioneer CDJ/XDJ LAN now-playing source
	Serato         SeratoFeature     `json:"serato"`         // Serato collection + live now-playing (History sessions)
	VirtualDJ      VirtualDJFeature  `json:"virtualDj"`      // VirtualDJ collection + live now-playing (NetCtl/OS2L/tracklist)
	Rekordbox      RekordboxFeature  `json:"rekordbox"`      // Rekordbox live now-playing (db-poll + memory-read)
	Recorder       RecorderFeature   `json:"recorder"`       // confirmed-play tracklist recorder sink
	NowPlayingFile FileSinkFeature   `json:"nowPlayingFile"` // now_playing.{json,txt} for OBS
	OverlayWeb     OverlayWebFeature `json:"overlayWeb"`     // live multi-deck browser overlay (OBS Browser source)
	OverlayPNG     FileSinkFeature   `json:"overlayPng"`     // native per-deck PNG cards (OBS Image source per deck)
	OverlayOBS     OverlayOBSFeature `json:"overlayObs"`     // obs-websocket renderer (drives OBS inputs directly)
	VideoShare     VideoShareFeature `json:"videoShare"`     // GPU/IPC video share (Spout/Syphon/PipeWire)

	OverlayWaveform OverlayWaveformFeature `json:"overlayWaveform"` // scrolling waveform + EQ/FX panel in the overlays

	Peers         PeersFeature         `json:"peers"`         // LAN peer link (discovery + paired connections)
	AccountBridge AccountBridgeFeature `json:"accountBridge"` // reach this instance off-LAN via the rave.page account relay
	FileXfer      FileXferFeature      `json:"fileXfer"`      // file transfer between paired instances
	SetCapture    SetCaptureFeature    `json:"setCapture"`    // local Icecast receiver: captures Traktor's broadcast to disk
	AudioRecord   AudioRecordFeature   `json:"audioRecord"`   // native audio device capture (FLAC), OBS-synced + manual

	LibrarySync LibrarySyncFeature `json:"librarySync"` // cross-DJ-software library sync (hub-merge → targets)

	AppGroups AppGroupsFeature `json:"appGroups"` // relaunch named app sets after a crash (App Groups tab)

	Timecode TimecodeFeature `json:"timecode"` // house SMPTE timecode outputs (LTC audio / MTC / Art-Net)

	DMX DMXFeature `json:"dmx"` // DMX plane: Art-Net ingest/emit + VRSL video grid

	DMXMIDI DMXMIDIFeature `json:"dmxMidi"` // DMX → MIDI CC bridge for VRChat --midi worlds

	LightCue LightCueFeature `json:"lightCue"` // DMX lighting-cue recorder → contract JSON take → gist for the VRChat world

	RTSPServe RTSPServeFeature `json:"rtspServe"` // local RTSP performer chain (ffmpeg encode → rtspt for VRChat AVPro)

	Stream StreamFeature `json:"stream"` // VRSL DMX-over-video stream: render the DMX store → ffmpeg → RTMP/WHIP push for VRChat playback

	Mocap MocapFeature `json:"mocap"` // mocap capture master: panel capture → pose store → composite region overlay on the VRSL stream

	Crew CrewFeature `json:"crew"` // capture-crew relay: uplink/ingest mocap packets through the event's rave.page relay room

	Webcam WebcamFeature `json:"webcam"` // webcam/UVC source: dshow capture → Spout + PTZ/exposure control (medialink P5)

	MediaLink MediaLinkFeature `json:"mediaLink"` // LAN video routes: codec/bitrate + Spout-sender sharing (medialink P4)

	AbletonLink AbletonLinkFeature `json:"abletonLink"` // Ableton Link: publish fused DJ tempo/phrase onto a Link session + Resolume phrase-sync

	MIDIController MIDIControllerFeature `json:"midiController"` // software MIDI test controller (pad/CC surface → loopback port for DJ-app MIDI-learn)

	GridFix GridFixFeature `json:"gridFix"` // neural beatgrid fixer (managed Python beat_this engine + grid-fit writeback)
}

// GridFixFeature configures the beatgrid fixer: a Beat This! neural beat tracker in a
// rave-mate-managed Python venv (installed from Settings, or PythonPath points at a
// user-managed interpreter whose env already has beat-this) + the Go grid-fit engine
// (internal/gridfix) that snaps/creates grid markers in DJ-software collections.
// Additive at v27 (zero value = disabled, no migration).
type GridFixFeature struct {
	Enabled     bool    `json:"enabled"`
	PythonPath  string  `json:"pythonPath,omitempty"`  // base interpreter override ("" = auto-discover py/-3, python3, python)
	Device      string  `json:"device,omitempty"`      // engine preference: "auto" (default: CUDA if installed+working, else CPU) | "cpu" | "cuda"
	MinQuality  float64 `json:"minQuality,omitempty"`  // min grid coverage to auto-fix; 0 = default 0.85
	ThresholdMS float64 `json:"thresholdMs,omitempty"` // ignore marker corrections below this; 0 = default 12
	BiasS       float64 `json:"biasS,omitempty"`       // manual systematic detector offset (s); overridden by BiasExt when calibrated
	// BiasExt is the measured detector bias (s) per lowercase file extension incl.
	// dot, "*" = overall fallback — written by the Collection-tab Calibrate action
	// (gridfix.SummarizeCalibration). Additive; empty = fall back to BiasS.
	BiasExt     map[string]float64 `json:"biasExt,omitempty"`
	LockFixed   bool               `json:"lockFixed,omitempty"`   // set the Traktor LOCK flag on fixed entries
	ActiveModel string             `json:"activeModel,omitempty"` // fine-tuned checkpoint path ("" = builtin final0)
}

// ResolvedMinQuality returns the coverage gate (default 0.85).
func (f GridFixFeature) ResolvedMinQuality() float64 {
	if f.MinQuality > 0 {
		return f.MinQuality
	}
	return 0.85
}

// ResolvedThresholdMS returns the ignore-below correction size (default 12ms).
func (f GridFixFeature) ResolvedThresholdMS() float64 {
	if f.ThresholdMS > 0 {
		return f.ThresholdMS
	}
	return 12.0
}

// ResolvedDevice returns the inference device request (default "auto").
func (f GridFixFeature) ResolvedDevice() string {
	if f.Device != "" {
		return f.Device
	}
	return "auto"
}

// MIDIControllerFeature configures the software MIDI mixer surface: a virtual channel rack (EQ /
// filter / trim / fader knobs + play/cue) that emits MIDI to a loopback port so a DJ app
// (Rekordbox / Serato etc.) can MIDI-learn custom mappings. Device = MIDI-out port-name substring;
// "" = auto (match "loopbe"/LoopBe1, else the first available port). Channels = mixer channel/deck
// count 1..MaxMIDIChannels (0 -> DefaultMIDIChannels on load). Additive at v24 (Device) + v26 (Channels).
type MIDIControllerFeature struct {
	Device   string `json:"device"`
	Channels int    `json:"channels"` // mixer channel/deck count 1..8; 0 = DefaultMIDIChannels
}

// normalize clamps the channel count into 1..MaxMIDIChannels (0/negative -> DefaultMIDIChannels).
func (m *MIDIControllerFeature) normalize() {
	if m.Channels <= 0 {
		m.Channels = DefaultMIDIChannels
	} else if m.Channels > MaxMIDIChannels {
		m.Channels = MaxMIDIChannels
	}
}

// AbletonLinkFeature configures the Ableton Link bridge: rave-mate publishes the session's
// fused master BPM + beat/phase onto a Link session (quantum = phrase length), so Link-aware
// visuals (Resolume, Live, VDMX) follow the DJ's tempo + phrase phase. The real Link backend is
// cgo-gated behind the `abletonlink` build tag + isolated in the featurehost "abletonlink" child;
// the default build reports the feature unavailable. Off by default (opt-in).
type AbletonLinkFeature struct {
	Enabled       bool           `json:"enabled"`
	Quantum       int            `json:"quantum"`       // phrase length in beats: 8|16|32; 0 = 16
	TempoOwner    string         `json:"tempoOwner"`    // "auto" (elect) | "always" (this node drives) | "follow" (never drive); "" = auto
	StartStopSync bool           `json:"startStopSync"` // share transport start/stop across Link peers
	Resolume      ResolumeConfig `json:"resolume"`      // Resolume phrase-sync (clip-trigger-on-phrase + tempo readback)
}

// ResolumeConfig configures the Resolume Arena/Avenue 7 control channel (P3): OSC send (via
// internal/osc) for tempo/resync/clip-connect + REST readback of tempo/beat/phase over the
// Resolume web server. Resolume 7 joins Link natively, so this only adds phrase-aligned clip
// triggers + offset nudges on top of Link tempo/phase sync.
type ResolumeConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`     // "" = 127.0.0.1
	OSCPort  int    `json:"oscPort"`  // Resolume OSC input port; 0 = 7000
	RESTPort int    `json:"restPort"` // Resolume web-server REST port; 0 = 8080

	// Phrase clip: a clip (1-based layer/clip) re-triggered on every Link phrase boundary
	// (phase==0) for phrase-aligned visual restarts. 0/0 = off.
	PhraseClipLayer int `json:"phraseClipLayer,omitempty"`
	PhraseClipClip  int `json:"phraseClipClip,omitempty"`
}

// HasPhraseClip reports whether a phrase-boundary clip trigger is configured.
func (r ResolumeConfig) HasPhraseClip() bool { return r.PhraseClipLayer > 0 && r.PhraseClipClip > 0 }

// AbletonLinkQuanta are the selectable phrase lengths in beats.
var AbletonLinkQuanta = []int{8, 16, 32}

// ResolvedQuantum returns the phrase length in beats (default 16; snapped to 8/16/32).
func (a AbletonLinkFeature) ResolvedQuantum() int {
	switch a.Quantum {
	case 8, 16, 32:
		return a.Quantum
	default:
		return 16
	}
}

// ResolvedTempoOwner returns the tempo-owner role ("auto"|"always"|"follow"; default "auto").
func (a AbletonLinkFeature) ResolvedTempoOwner() string {
	switch a.TempoOwner {
	case "always", "follow":
		return a.TempoOwner
	default:
		return "auto"
	}
}

// ResolvedHost returns the Resolume host (default 127.0.0.1).
func (r ResolumeConfig) ResolvedHost() string {
	if strings.TrimSpace(r.Host) != "" {
		return r.Host
	}
	return "127.0.0.1"
}

// ResolvedOSCPort returns the Resolume OSC input port (default 7000).
func (r ResolumeConfig) ResolvedOSCPort() int {
	if r.OSCPort > 0 {
		return r.OSCPort
	}
	return 7000
}

// ResolvedRESTPort returns the Resolume web-server REST port (default 8080).
func (r ResolumeConfig) ResolvedRESTPort() int {
	if r.RESTPort > 0 {
		return r.RESTPort
	}
	return 8080
}

// MediaLinkFeature tunes the P4 video-route pipeline (MEDIALINK_DESIGN.md §3.2). Additive at
// v24 - zero value keeps prior behaviour (no sender sharing, auto codec, default budget).
type MediaLinkFeature struct {
	ShareVideo  bool   `json:"shareVideo,omitempty"`  // advertise local Spout senders to paired instances
	PreferCodec string `json:"preferCodec,omitempty"` // "hevc"|"h264"|"mjpeg"; "" = auto (§3.2 matrix)
	BitrateKbps int    `json:"bitrateKbps,omitempty"` // per-route video budget; 0 = 20000
	SWOnly      bool   `json:"swOnly,omitempty"`      // advertise software encoders only (diagnostic; tier 4 + CPU warning)
	MaxFPS      int    `json:"maxFps,omitempty"`      // sender-side video fps cap; 0 = 60, -1 = uncapped
	MaxHeight   int    `json:"maxHeight,omitempty"`   // encode downscale policy: 0 = auto (native on hw, 1080p on sw x264), >0 = cap, -1 = never
	// Encode-device preference (SENDER side). DevicePolicy "" = auto: no device flags, the encoder
	// picks (adapter 0) - byte-identical to pre-WP-3 behaviour. "pin" = always EncoderDevice.
	// "avoid-busiest" = the least video-encode-loaded adapter, skipping ones OBS/Parsec hold.
	// EncoderDevice is a DXGI adapter LUID key ("0xHIGH_0xLOW", encoderscan.AdapterInfo.LUID).
	// Encoder pins a concrete encoder name (medialink.EncoderMFNative = the native pipe-free MF
	// engine, "libx264", …); "" = negotiated (§3.2 matrix).
	DevicePolicy  string `json:"devicePolicy,omitempty"`
	EncoderDevice string `json:"encoderDevice,omitempty"`
	Encoder       string `json:"encoder,omitempty"`
	// Subprocess runs the media plane (medialink+mediaroute+webcam) as an isolated, memory-capped
	// featurehost child (#44) so a media RAM/CPU runaway or cgo fault can't starve the host - and so
	// the governor's below-normal demotion of THIS process never throttles a live route.
	// Tri-state: unset/nil = ON (the default since the whole-daemon priority demotion made in-proc
	// media a liability), explicit false = the legacy in-proc plane, explicit true = on. TCPlane +
	// mediaClock stay daemon-side either way and mirror the child's clock.
	Subprocess *bool `json:"subprocess,omitempty"`
	// ZigCapture routes a Spout source's pixels to the native encoder child as a GPU
	// SHARED-TEXTURE HANDLE instead of a host readback: no GPU→CPU copy, no pooled frame
	// buffers, no SHM frame slot (66.4 → 4.0 MB of shared VA per 4K session).
	//
	// Tri-state, DEFAULT ON since zigmedia increment 5 (nil = on); an explicit false keeps the
	// readback, which stays as the fallback and as the parity oracle every zero-copy gate is
	// measured against. Env RAVE_MATE_ZIGMEDIA_CAPTURE=1|0 overrides.
	//
	// Why the default flipped: the path is decided PER SOURCE (a webcam has no shared texture and
	// silently keeps the readback), every rung of the fallback ladder carries real pixels and logs
	// its reason once, and the previously-unexecuted capture branches - IDXGIKeyedMutex,
	// TYPELESS/exotic formats, a restarted sender's changed handle - are now gated by execution on
	// hardware with the decoded PICTURE asserted, not by "no error". The readback also spent the
	// entire life of the vendored SpoutLibrary header/DLL pairing returning BLACK frames
	// (ReceiveImage dispatched to ReceiveTexture), while this path reads real pixels through
	// GetSenderInfo alone - the one SDK call proven to align.
	//
	// Still open at the flip, and the reason a 2-PC pass is still wanted: the wire has never been
	// driven with this on, no 7-day soak has run, and the sender-PC pointer-lag question (§13.1)
	// needs a real sending app. Containment: pacing never polls faster than the negotiated fps and
	// mutex acquires are bounded 1..4 ms - and the readback path acquires the SAME named mutex at
	// the shared capture's rate, so this path contends no harder than the one it replaces.
	ZigCapture *bool `json:"zigCapture,omitempty"`
	// ZigDecode is ZigCapture's receive-side mirror: the native child decodes the incoming AUs
	// and renders them straight into the local video-share sender's GPU texture, instead of
	// ffmpeg pushing 33 MB raw frames per 4K frame back up a stdout pipe and Spout uploading them
	// again. Tri-state, DEFAULT ON since zigmedia increment 5 (nil = on); an explicit false keeps
	// the ffmpeg decode path, which remains the fallback and the parity reference.
	// Env RAVE_MATE_ZIGMEDIA_DECODE=1|0 overrides.
	//
	// Why the default flipped, and why the OLD default was not a safe baseline: measured on the
	// field rig, a 4K route's local republish delivers ~13.5 DISTINCT frames/s while the source
	// encodes at 37 - the CPU SendImage upload of 33 MB/frame IS the ceiling. Leaving this off
	// keeps a measured 3x frame loss on the receive side. The path that replaces it publishes
	// straight into the destination texture with no host frame at all, and its picture is verified
	// end to end (encode → AU → native decode → publish → read back from a SECOND process with its
	// own D3D11 device: correct row AND channel order), which is design §10's stated pre-condition
	// discharged at the independent-consumer level.
	//
	// Still open, and the reason the receive side wants a live pass: no real end-to-end route
	// (peer → jitterbuf → mfDecoder) has been driven - the live gate feeds ProcDecSession directly;
	// no 4K60 receive soak; no HEVC bitstream decoded; and no TRUE hardware decoder MFT exists on
	// the rig that verified it (the MS D3D11-aware software MFT carries the passing run). Each of
	// those failure shapes lands on a rung that keeps real pixels: an open-side refusal runs the
	// ffmpeg decoder with one WARN, and a mid-route dstgone/staleness recycles, then pins the
	// destination to the frame path. The route panel renders `published N` and bytes/frame, so a
	// silent receive-side failure is not expressible.
	ZigDecode *bool `json:"zigDecode,omitempty"`
	// ZigAffinity lets a zero-copy session be RE-PLACED on the adapter that actually owns the
	// sender's shared texture instead of downgrading to the readback path (risk R7: a sender
	// produced by an app on another GPU is invisible to an encoder child pinned to this one).
	// Only ever applies when the encode device is NOT pinned by policy - "never silently move
	// adapters" - and every move logs. Tri-state, default OFF.
	// Env RAVE_MATE_ZIGMEDIA_AFFINITY=1|0 overrides.
	//
	// Deliberately NOT flipped with ZigCapture: the move is live-verified only between two
	// IDENTICAL GPUs (2x RTX 3060, inc-3 M4), so a heterogeneous rig - iGPU + dGPU, where the
	// re-placed adapter may have a much worse encoder or none - is unexercised, and the guard for
	// that case is unit-tested rather than lived. Leaving it off costs a VISIBLE downgrade to a
	// working readback (one warn, a counted downgrade, "downgrades N" on the route panel), which is
	// the honest trade; the warn names this key so an operator on a multi-GPU box can turn it on.
	ZigAffinity *bool `json:"zigAffinity,omitempty"`
}

// ZeroCopyCapture reports whether zero-copy Spout→encoder capture is enabled. Env
// RAVE_MATE_ZIGMEDIA_CAPTURE wins (soak + tests), then the config key, else ON (zigmedia inc 5).
//
// Only an EXPLICIT false opts out. The key is `omitempty` on a *bool, so a pre-flip config carries
// either true (someone opted in) or nothing at all - the same reasoning MediaSubprocess records:
// nobody can be silently pinned to the old path by a stale config, because the old path was never
// persisted as a value.
func (m MediaLinkFeature) ZeroCopyCapture() bool {
	switch os.Getenv("RAVE_MATE_ZIGMEDIA_CAPTURE") {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return m.ZigCapture == nil || *m.ZigCapture
}

// SetZeroCopyCapture sets the zero-copy capture opt-in EXPLICITLY (single write seam).
func (m *MediaLinkFeature) SetZeroCopyCapture(on bool) { v := on; m.ZigCapture = &v }

// ZeroCopyDecode reports whether native GPU-resident decode+publish is enabled. Env
// RAVE_MATE_ZIGMEDIA_DECODE wins (soak + tests), then the config key, else ON (zigmedia inc 5).
// Only an EXPLICIT false opts out - same tri-state migration argument as ZeroCopyCapture.
func (m MediaLinkFeature) ZeroCopyDecode() bool {
	switch os.Getenv("RAVE_MATE_ZIGMEDIA_DECODE") {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return m.ZigDecode == nil || *m.ZigDecode
}

// SetZeroCopyDecode sets the native-decode opt-in EXPLICITLY (single write seam).
func (m *MediaLinkFeature) SetZeroCopyDecode(on bool) { v := on; m.ZigDecode = &v }

// ZeroCopyAffinity reports whether a zero-copy session may be re-placed on the adapter that owns
// the sender's texture. Env RAVE_MATE_ZIGMEDIA_AFFINITY wins, then the config key, else OFF.
func (m MediaLinkFeature) ZeroCopyAffinity() bool {
	switch os.Getenv("RAVE_MATE_ZIGMEDIA_AFFINITY") {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return m.ZigAffinity != nil && *m.ZigAffinity
}

// SetZeroCopyAffinity sets the affinity opt-in EXPLICITLY (single write seam).
func (m *MediaLinkFeature) SetZeroCopyAffinity(on bool) { v := on; m.ZigAffinity = &v }

// MediaSubprocess reports whether the media plane runs in the isolated child (#44). Default (key
// absent) is TRUE; only an explicit false keeps the legacy in-proc plane. Note the old schema
// persisted this field with omitempty on a plain bool, so a pre-flip config could only ever carry
// `true` (opt-in) or no key at all - no user can be silently pinned to the old in-proc path.
func (m MediaLinkFeature) MediaSubprocess() bool { return m.Subprocess == nil || *m.Subprocess }

// SetSubprocess sets the isolation opt-in EXPLICITLY (never leaves the tri-state unset, so an
// opt-out survives a save). The single write seam, so the field's representation can change without
// touching the settings UI.
func (m *MediaLinkFeature) SetSubprocess(on bool) { v := on; m.Subprocess = &v }

// DevicePref returns the sender-side encode-device preference verbatim (policy, adapter LUID key).
// Normalization + resolution live in encoderscan.ResolveDevice - config stays dependency-free.
func (m MediaLinkFeature) DevicePref() (policy, adapter string) {
	return m.DevicePolicy, m.EncoderDevice
}

// PinnedEncoder returns the user-pinned encoder name ("" = negotiate per the §3.2 matrix).
func (m MediaLinkFeature) PinnedEncoder() string { return strings.TrimSpace(m.Encoder) }

// Bitrate returns the effective per-route budget (default 20 Mbps).
func (f MediaLinkFeature) Bitrate() int {
	if f.BitrateKbps <= 0 {
		return 20_000
	}
	return f.BitrateKbps
}

// FPSCap returns the sender-side video frame-rate cap (0 = uncapped). VJ sources can run
// 120+ fps; every capped frame skips capture-copy, encode and crypto entirely.
func (f MediaLinkFeature) FPSCap() int {
	switch {
	case f.MaxFPS < 0:
		return 0
	case f.MaxFPS == 0:
		return 60
	}
	return f.MaxFPS
}

// WebcamFeature configures the webcam/UVC source (MEDIALINK_DESIGN.md §5): an ffmpeg dshow
// capture of the chosen device published as a local Spout sender, plus UVC PTZ/exposure control
// driveable from a paired instance (media.cam.* bus). Off by default; disabled = zero footprint
// (no ffmpeg child, no COM).
type WebcamFeature struct {
	Enabled   bool   `json:"enabled"`
	Device    string `json:"device"`              // dshow video device friendly name; "" = none selected
	Width     int    `json:"width,omitempty"`     // capture size; 0 = device default
	Height    int    `json:"height,omitempty"`    // capture size; 0 = device default
	FPS       int    `json:"fps,omitempty"`       // capture rate; 0 = device default
	AutoStart bool   `json:"autoStart,omitempty"` // start capture with the module (crash-recovery rigs)
}

// DMXFeature configures the DMX plane: an Art-Net listener ingesting console DMX into the
// universe store, the VRSL video-grid renderer (Spout/PNG), and optional re-emit of ingested
// universes to another Art-Net target. Off by default (opt-in).
type DMXFeature struct {
	Enabled    bool    `json:"enabled"`
	ListenAddr string  `json:"listenAddr"`          // Art-Net UDP bind; "" = ":6454" (the fixed Art-Net port)
	EmitTarget string  `json:"emitTarget"`          // re-emit destination "ip:port"; "" = broadcast 255.255.255.255:6454
	ReEmit     bool    `json:"reEmit"`              // forward ingested universes to EmitTarget
	Universes  []int   `json:"universes,omitempty"` // Art-Net port-addresses to render (0-based); empty = defaults per grid mode
	Grid       DMXGrid `json:"grid"`                // VRSL grid sink

	// sACN (E1.31) is an alternate DMX source into the same store. Off by default (Art-Net only).
	SACN          bool  `json:"sacn,omitempty"`          // join E1.31 multicast (UDP 5568) as an extra source
	SACNUniverses []int `json:"sacnUniverses,omitempty"` // universes to join; empty = ResolvedUniverses()
}

// DMXGrid configures the VRSL video-grid sink.
type DMXGrid struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`      // "mono" (default) | "rgb9" (extended 9-universe RGB)
	SpoutName string `json:"spoutName"` // Spout sender name; "" = "rave-mate-vrsl"
	FPSCap    int    `json:"fpsCap"`    // grid render cap; 0 = 30
}

// ResolvedListenAddr returns the Art-Net bind address (default ":6454").
func (d DMXFeature) ResolvedListenAddr() string {
	if d.ListenAddr != "" {
		return d.ListenAddr
	}
	return ":6454"
}

// ResolvedEmitTarget returns the re-emit destination (default limited broadcast :6454).
func (d DMXFeature) ResolvedEmitTarget() string {
	if d.EmitTarget != "" {
		return d.EmitTarget
	}
	return "255.255.255.255:6454"
}

// ResolvedUniverses returns the universes rendered to the grid: the configured list, else
// universe 0 (mono) / 0..8 (rgb9 packs nine).
func (d DMXFeature) ResolvedUniverses() []int {
	if len(d.Universes) > 0 {
		return d.Universes
	}
	if strings.EqualFold(strings.TrimSpace(d.Grid.Mode), "rgb9") {
		return []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	}
	return []int{0}
}

// ResolvedSACNUniverses returns the E1.31 universes to join (configured list, else the
// Art-Net render universes).
func (d DMXFeature) ResolvedSACNUniverses() []int {
	if len(d.SACNUniverses) > 0 {
		return d.SACNUniverses
	}
	return d.ResolvedUniverses()
}

// LightCueFeature configures the DMX lighting-cue recorder: it polls the universe store at Hz,
// captures a delta-encoded step/hold timeline, and saves it as the frozen cross-repo contract
// JSON take (published to a gist for the VRChat world). Needs the DMX plane running (its store
// is the capture source). Off by default (opt-in).
type LightCueFeature struct {
	Enabled   bool   `json:"enabled"`
	RecordDir string `json:"recordDir,omitempty"` // take output dir; "" = <configDir>/dmx_recordings
	Hz        int    `json:"hz,omitempty"`        // decimation rate; 0 = 30, clamped to 44 (emitter cap)
}

// ResolvedHz returns the record decimation rate (default 30, capped at 44 = the Art-Net emitter rate).
func (l LightCueFeature) ResolvedHz() int {
	if l.Hz <= 0 {
		return 30
	}
	if l.Hz > 44 {
		return 44
	}
	return l.Hz
}

// ResolvedRecordDir returns the take output dir (default <configDir>/dmx_recordings).
func (l LightCueFeature) ResolvedRecordDir() string {
	if l.RecordDir != "" {
		return l.RecordDir
	}
	if p, err := DataPath("dmx_recordings.x"); err == nil {
		return filepath.Join(filepath.Dir(p), "dmx_recordings")
	}
	return "dmx_recordings"
}

// ResolvedSpoutName returns the grid's Spout sender name (default "rave-mate-vrsl").
func (g DMXGrid) ResolvedSpoutName() string {
	if g.SpoutName != "" {
		return g.SpoutName
	}
	return "rave-mate-vrsl"
}

// ResolvedFPSCap returns the grid render cap (default 30, clamped 1..60).
func (g DMXGrid) ResolvedFPSCap() int {
	if g.FPSCap <= 0 {
		return 30
	}
	if g.FPSCap > 60 {
		return 60
	}
	return g.FPSCap
}

// DMXMIDIFeature configures the DMX→MIDI bridge: received DMX channels become MIDI CC
// messages on a local MIDI output port (a loopMIDI-class virtual port VRChat is launched
// with via --midi=<port>). Local-client-only by VRChat design. Hard rate-limited with
// change-detection - VRChat crashes above ~128 MIDI events per frame, so the cap is
// clamped well under that even at low headset frame rates. Off by default (opt-in).
type DMXMIDIFeature struct {
	Enabled      bool   `json:"enabled"`
	Device       string `json:"device"`              // MIDI-out port name substring (loopMIDI); "" = first port
	Universes    []int  `json:"universes,omitempty"` // bridged Art-Net universes in MIDI-address order (max 4); empty = universe 0
	MaxPerSecond int    `json:"maxPerSecond"`        // CC messages/s cap; 0 = 400, clamped 50..1000
}

// DMXMIDIMaxUniverses caps bridged universes: 16 MIDI channels × 128 CCs = 2048 addresses
// = 4 universes of 512 channels.
const DMXMIDIMaxUniverses = 4

// ResolvedUniverses returns the bridged universes (default {0}), capped at DMXMIDIMaxUniverses.
func (d DMXMIDIFeature) ResolvedUniverses() []int {
	u := d.Universes
	if len(u) == 0 {
		return []int{0}
	}
	if len(u) > DMXMIDIMaxUniverses {
		u = u[:DMXMIDIMaxUniverses]
	}
	return u
}

// ResolvedRate returns the CC messages/s cap (default 400, clamped 50..1000 - 1000/s stays
// under VRChat's ~128 events/frame crash threshold down to ~8 fps).
func (d DMXMIDIFeature) ResolvedRate() int {
	if d.MaxPerSecond <= 0 {
		return 400
	}
	if d.MaxPerSecond < 50 {
		return 50
	}
	if d.MaxPerSecond > 1000 {
		return 1000
	}
	return d.MaxPerSecond
}

// RTSPServeFeature is the local performer video chain: an ffmpeg subprocess (the existing
// worker precedent) encodes a configured source to H.264 and rave-mate serves it over
// RTSP/TCP - VRChat's AVPro player ingests rtspt://<this-machine>:8554/<path> with
// sub-second latency, replacing the OBS→RTMP→MediaMTX relay. Off by default (opt-in).
type RTSPServeFeature struct {
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`                // ffmpeg input: file, URL, "desktop" (with gdigrab), device…
	InputFormat string `json:"inputFormat,omitempty"` // ffmpeg demuxer (-f): gdigrab, dshow, …; "" = auto by source
	ListenAddr  string `json:"listenAddr"`            // RTSP TCP bind; "" = ":8554"
	Path        string `json:"path"`                  // stream path; "" = "/live"
	FPS         int    `json:"fps"`                   // encode + RTP timestamp rate; 0 = 30
	BitrateKbps int    `json:"bitrateKbps"`           // H.264 bitrate; 0 = 6000
	Passthrough bool   `json:"passthrough"`           // source is already H.264 → copy, no re-encode
}

// ResolvedListenAddr returns the RTSP bind address (default ":8554").
func (r RTSPServeFeature) ResolvedListenAddr() string {
	if strings.TrimSpace(r.ListenAddr) != "" {
		return r.ListenAddr
	}
	return ":8554"
}

// ResolvedPath returns the stream path with a leading slash (default "/live").
func (r RTSPServeFeature) ResolvedPath() string {
	p := strings.TrimSpace(r.Path)
	if p == "" {
		return "/live"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// ResolvedFPS returns the encode/timestamp frame rate (default 30, clamped 1..120).
func (r RTSPServeFeature) ResolvedFPS() int {
	if r.FPS <= 0 {
		return 30
	}
	if r.FPS > 120 {
		return 120
	}
	return r.FPS
}

// ResolvedBitrate returns the H.264 bitrate in kbps (default 6000, clamped 250..50000).
func (r RTSPServeFeature) ResolvedBitrate() int {
	if r.BitrateKbps <= 0 {
		return 6000
	}
	if r.BitrateKbps < 250 {
		return 250
	}
	if r.BitrateKbps > 50000 {
		return 50000
	}
	return r.BitrateKbps
}

// StreamFeature is the VRSL DMX-over-video stream: rave-mate renders the live DMX universe store
// into a VRSL-compatible video grid, ffmpeg-encodes it (LINEAR, no gamma), and pushes RTMP (or
// WHIP) to a transcode service (VRCDN/Twitch/custom) that serves it to VRChat. The world plays the
// stream back and decodes the grid with RaveVRSLGridReader. Off by default (opt-in; needs ffmpeg +
// a push URL). See .devnotes/VRSL_VIDEO_STREAM.md (mirror of the frozen world-repo contract).
type StreamFeature struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`                 // RTMP (rtmp://host/app) or WHIP (https://host/whip) target
	StreamKey   string `json:"streamKey"`           // RTMP stream key (appended as a path segment); WHIP: leave blank, embed auth in URL
	Mode        string `json:"mode"`                // "standard" (stock VRSL, 8-bit) | "extended" (superset: low-byte mirror + metadata); "" = standard
	ColorMode   string `json:"colorMode"`           // grid packing "mono" (default, compression-robust) | "rgb9"; "" = mono
	Universes   []int  `json:"universes,omitempty"` // Art-Net port-addresses to stream (0-based); empty = universe 0 (mono) / 0..8 (rgb9)
	FPS         int    `json:"fps"`                 // encode frame rate; 0 = 30, clamped 1..60
	BitrateKbps int    `json:"bitrateKbps"`         // H.264 bitrate; 0 = derived from frame size
	Encoder     string `json:"encoder"`             // "x264"|"nvenc"|"qsv"|"amf"|"auto"; "" = x264
	Transport   string `json:"transport"`           // "rtmp" (default) | "whip"; "" = rtmp
}

// ResolvedMode returns "standard" or "extended" (default "standard").
func (s StreamFeature) ResolvedMode() string {
	if strings.EqualFold(strings.TrimSpace(s.Mode), "extended") {
		return "extended"
	}
	return "standard"
}

// ResolvedColorMode returns "mono" or "rgb9" (default "mono"; mono survives 4:2:0 transcode).
func (s StreamFeature) ResolvedColorMode() string {
	if strings.EqualFold(strings.TrimSpace(s.ColorMode), "rgb9") {
		return "rgb9"
	}
	return "mono"
}

// ResolvedUniverses returns the streamed universes: the configured list, else universe 0 (mono)
// / 0..8 (rgb9 packs nine into three colour blocks).
func (s StreamFeature) ResolvedUniverses() []int {
	if len(s.Universes) > 0 {
		return s.Universes
	}
	if s.ResolvedColorMode() == "rgb9" {
		return []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	}
	return []int{0}
}

// ResolvedFPS returns the encode frame rate (default 30, clamped 1..60 - grid frames are tiny +
// low-rate; a short GOP at ≤60 locks late-joiners fast).
func (s StreamFeature) ResolvedFPS() int {
	if s.FPS <= 0 {
		return 30
	}
	if s.FPS > 60 {
		return 60
	}
	return s.FPS
}

// ResolvedBitrate returns the H.264 bitrate in kbps (0 = derived from frame size by the encoder;
// clamped 250..50000 when set).
func (s StreamFeature) ResolvedBitrate() int {
	if s.BitrateKbps <= 0 {
		return 0 // 0 = let the encoder derive from pixel rate
	}
	if s.BitrateKbps < 250 {
		return 250
	}
	if s.BitrateKbps > 50000 {
		return 50000
	}
	return s.BitrateKbps
}

// ResolvedEncoder returns the ffmpeg encoder token: "x264"|"nvenc"|"qsv"|"amf"|"auto"
// (default "x264"). "auto" lets the stream module probe for a hardware encoder.
func (s StreamFeature) ResolvedEncoder() string {
	switch strings.ToLower(strings.TrimSpace(s.Encoder)) {
	case "nvenc", "qsv", "amf", "auto":
		return strings.ToLower(strings.TrimSpace(s.Encoder))
	default:
		return "x264"
	}
}

// ResolvedTransport returns "rtmp" or "whip" (default "rtmp").
func (s StreamFeature) ResolvedTransport() string {
	if strings.EqualFold(strings.TrimSpace(s.Transport), "whip") {
		return "whip"
	}
	return "rtmp"
}

// MocapFeature is the mocap capture master: a capture node reads the in-world mocap panel
// (desktop duplication / Spout / dshow), decodes dancer poses (internal/mocapnode), and the
// master (internal/mocapmaster) re-renders the composite mocap region into the VRSL video
// stream each encoder frame (extended mode - the region rides the composite's calibration
// triad). Off by default (opt-in). Additive at v33, no bump - zero value = disabled.
type MocapFeature struct {
	Enabled   bool      `json:"enabled"`
	Source    string    `json:"source"`              // capture source "desktop" (default) | "spout" | "dshow"
	Device    string    `json:"device,omitempty"`    // spout sender / dshow device name
	Monitor   int       `json:"monitor,omitempty"`   // desktop grabber monitor index (ddagrab output_idx)
	FPS       int       `json:"fps"`                 // capture rate; 0 = 30, clamped 1..60
	BoneSlots int       `json:"boneSlots"`           // region bone slots S (stride = 8+2*S); 0 = 22, clamped 1..32
	StageMin  []float64 `json:"stageMin,omitempty"`  // stage bounds min x,y,z (m); missing = (-8,0,-6)
	StageSize []float64 `json:"stageSize,omitempty"` // stage bounds size x,y,z (m); missing/non-positive component = (16,4,12)
}

// ResolvedSource returns "desktop", "spout" or "dshow" (default "desktop").
func (m MocapFeature) ResolvedSource() string {
	switch strings.ToLower(strings.TrimSpace(m.Source)) {
	case "spout":
		return "spout"
	case "dshow":
		return "dshow"
	default:
		return "desktop"
	}
}

// ResolvedFPS returns the capture rate (default 30, clamped 1..60).
func (m MocapFeature) ResolvedFPS() int {
	if m.FPS <= 0 {
		return 30
	}
	if m.FPS > 60 {
		return 60
	}
	return m.FPS
}

// ResolvedBoneSlots returns the region bone-slot count S (default 22, clamped 1..32 -
// mocappanel.BoneSlotMax; fixed for the stream).
func (m MocapFeature) ResolvedBoneSlots() int {
	if m.BoneSlots <= 0 {
		return 22
	}
	if m.BoneSlots > 32 {
		return 32
	}
	return m.BoneSlots
}

// ResolvedStageMin returns the stage bounds min in metres (default (-8,0,-6); a partial
// array falls back whole - min components are free-signed, no per-axis validity rule).
func (m MocapFeature) ResolvedStageMin() [3]float64 {
	if len(m.StageMin) < 3 {
		return [3]float64{-8, 0, -6}
	}
	return [3]float64{m.StageMin[0], m.StageMin[1], m.StageMin[2]}
}

// ResolvedStageSize returns the stage bounds size in metres (default (16,4,12); any
// component <= 0 falls back to its default - the pose store requires all three positive).
func (m MocapFeature) ResolvedStageSize() [3]float64 {
	out := [3]float64{16, 4, 12}
	if len(m.StageSize) >= 3 {
		for i := 0; i < 3; i++ {
			if m.StageSize[i] > 0 {
				out[i] = m.StageSize[i]
			}
		}
	}
	return out
}

// CrewFeature is the capture-crew relay link (CREW_RELAY_CONTRACT.md §6): relay decoded mocap
// panel packets between crew rave-mates through the event's mocap relay room on rave.page.
// Role "node" uplinks this machine's decoded packets to every master present in the room;
// "master" ingests remote crew packets into the local persistent mocap master (server-side
// this role requires event-editor rights). NO token/URL fields: bearer = the signed-in
// account's TokenSource, base = cfg.APIBaseURL (the AccountBridgeFeature precedent - tokens
// never live in plaintext config). Additive at v33, no bump - zero value = disabled.
type CrewFeature struct {
	Enabled bool   `json:"enabled"`
	EventID string `json:"eventId,omitempty"` // rave.page event id (the relay room key)
	Role    string `json:"role,omitempty"`    // "node" (default) | "master"
	Label   string `json:"label,omitempty"`   // human label shown to the crew (e.g. "FOH rig")
}

// ResolvedRole returns "node" or "master" (default "node").
func (c CrewFeature) ResolvedRole() string {
	if strings.EqualFold(strings.TrimSpace(c.Role), "master") {
		return "master"
	}
	return "node"
}

// ResolvedEventID returns the trimmed event id ("" = unset; the module refuses to start).
func (c CrewFeature) ResolvedEventID() string { return strings.TrimSpace(c.EventID) }

// ResolvedLabel returns the trimmed crew label ("" = none).
func (c CrewFeature) ResolvedLabel() string { return strings.TrimSpace(c.Label) }

// TimecodeFeature configures the house SMPTE timecode generator: one master frame clock other
// machines/software chase. Three independent sinks - LTC (audio-out, the SMPTE signal most media
// software accepts; route the chosen device into a virtual audio cable), MTC (MIDI Time Code out a
// virtual MIDI port), and Art-Net TimeCode (UDP for lighting consoles). Enabled arms the module;
// the clock itself is started/stopped from the UI or `rave-mate ctl tc-start`/`tc-stop`. Off by
// default (opt-in). Rate is the frame rate; StartAt is the initial timecode.
type TimecodeFeature struct {
	Enabled bool         `json:"enabled"`
	Rate    string       `json:"rate"`    // "24"|"25"|"29.97"|"30" (29.97 = drop-frame); "" = 30
	StartAt string       `json:"startAt"` // "hh:mm:ss:ff" | "clock" (jam to time-of-day) | "" = 00:00:00:00
	LTC     TCLTCSink    `json:"ltc"`
	MTC     TCMTCSink    `json:"mtc"`
	ArtNet  TCArtNetSink `json:"artnet"`
	// Extra outputs: the one master clock fans out to MANY destinations at once - a distinct LTC
	// audio device (virtual cable) per receiver, a MIDI port per DAW, a host per lighting console.
	LTCExtra    []TCLTCSink    `json:"ltcExtra,omitempty"`
	MTCExtra    []TCMTCSink    `json:"mtcExtra,omitempty"`
	ArtNetExtra []TCArtNetSink `json:"artnetExtra,omitempty"`
}

// LTCSinks returns every enabled LTC audio output (primary + extras) - each streams the same house
// clock to its own device, so one generator drives many virtual cables.
func (t TimecodeFeature) LTCSinks() []TCLTCSink {
	out := make([]TCLTCSink, 0, 1+len(t.LTCExtra))
	if t.LTC.On {
		out = append(out, t.LTC)
	}
	for _, e := range t.LTCExtra {
		if e.On {
			out = append(out, e)
		}
	}
	return out
}

// MTCSinks returns every enabled MTC MIDI output (primary + extras).
func (t TimecodeFeature) MTCSinks() []TCMTCSink {
	out := make([]TCMTCSink, 0, 1+len(t.MTCExtra))
	if t.MTC.On {
		out = append(out, t.MTC)
	}
	for _, e := range t.MTCExtra {
		if e.On {
			out = append(out, e)
		}
	}
	return out
}

// ArtNetSinks returns every enabled Art-Net TimeCode target (primary + extras).
func (t TimecodeFeature) ArtNetSinks() []TCArtNetSink {
	out := make([]TCArtNetSink, 0, 1+len(t.ArtNetExtra))
	if t.ArtNet.On {
		out = append(out, t.ArtNet)
	}
	for _, e := range t.ArtNetExtra {
		if e.On {
			out = append(out, e)
		}
	}
	return out
}

// TCLTCSink is the LTC audio output. Device = OS audio-output device name ("" = system default);
// route it into a virtual audio cable so another app reads SMPTE on its audio input. GainDb sets
// the peak level (default −3 dBFS ≈ 0.7 FS, per SMPTE ST 12-1 headroom).
type TCLTCSink struct {
	On     bool    `json:"on"`
	Device string  `json:"device"`
	GainDb float64 `json:"gainDb"` // 0 = default (−3 dBFS)
}

// TCMTCSink is the MIDI Time Code output. Device = OS MIDI-output port name ("" = first port);
// point it at a virtual MIDI loopback (loopMIDI / Windows MIDI Services) another app listens on.
type TCMTCSink struct {
	On     bool   `json:"on"`
	Device string `json:"device"`
}

// TCArtNetSink is the Art-Net TimeCode UDP output. Addr = destination host:port ("" = broadcast on
// the standard Art-Net port); a lighting console on the LAN chases it.
type TCArtNetSink struct {
	On   bool   `json:"on"`
	Addr string `json:"addr"` // "" = 255.255.255.255:6454
}

// ResolvedGainDb returns the LTC peak level in dBFS (default −3).
func (l TCLTCSink) ResolvedGainDb() float64 {
	if l.GainDb == 0 {
		return -3
	}
	return l.GainDb
}

// ResolvedAddr returns the Art-Net TimeCode target or the broadcast default.
func (a TCArtNetSink) ResolvedAddr() string {
	if a.Addr != "" {
		return a.Addr
	}
	return fmt.Sprintf("255.255.255.255:%d", ArtNetPort)
}

// ResolvedRate returns the configured frame-rate token or the "30" default.
func (t TimecodeFeature) ResolvedRate() string {
	if t.Rate != "" {
		return t.Rate
	}
	return "30"
}

// AppGroupsFeature holds user-defined application groups: named sets of apps that can be
// relaunched together (button / VR bind / MIDI / `rave-mate ctl launch-group`). The crash-recovery
// use case - bring a DJ rig back up after a VR-PC crash. Enabled gates the App Groups tab only;
// launching via ctl/keybind works regardless. Launched apps are detached (outlive rave-mate).
type AppGroupsFeature struct {
	Enabled bool       `json:"enabled"`
	Groups  []AppGroup `json:"groups,omitempty"`
}

// AppGroup is one named set of apps.
type AppGroup struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Apps []AppRef `json:"apps,omitempty"`
}

// AppRef is one launchable app. MatchName (e.g. "vrchat.exe") tests whether it's already running
// before launch - empty falls back to the exe basename. DelayMs staggers launch after the prior
// app. Elevated relaunches via UAC (Windows only; falls back to a normal start elsewhere).
type AppRef struct {
	Path      string   `json:"path"`
	Args      []string `json:"args,omitempty"`
	WorkDir   string   `json:"workDir,omitempty"`
	MatchName string   `json:"matchName,omitempty"`
	DelayMs   int      `json:"delayMs,omitempty"`
	Elevated  bool     `json:"elevated,omitempty"`
}

// LibrarySyncFeature configures cross-DJ-software library sync. rave-mate's merged music.db
// is the source of truth: a job groups tracks by portable hash, builds one canonical track per
// the field-priority rules, then writes it to each target (importable file / live write-back /
// file tags). Off by default (opt-in). Enabled gates the auto-sync scheduler only - manual runs
// work regardless.
type LibrarySyncFeature struct {
	Enabled bool      `json:"enabled"`
	Jobs    []SyncJob `json:"jobs,omitempty"`
}

// SyncJob is one user-defined sync: a scope (what), targets (where + how), rules (field merge +
// cue conversion), and an optional schedule (auto-sync). LastRunAt/LastSummary are updated after
// each run for the UI.
type SyncJob struct {
	ID          string       `json:"id"`
	Label       string       `json:"label"`
	Enabled     bool         `json:"enabled"` // include in auto-sync (manual run ignores this)
	Scope       SyncScope    `json:"scope"`
	Targets     []SyncTarget `json:"targets"`
	Rules       SyncRules    `json:"rules"`
	Auto        SyncSchedule `json:"auto"`
	LastRunAt   string       `json:"lastRunAt,omitempty"`
	LastSummary string       `json:"lastSummary,omitempty"`
}

// SyncScope selects what to sync. Kind: "all" | "dirs" | "playlists" | "tracks".
type SyncScope struct {
	Kind        string   `json:"kind"`
	Dirs        []string `json:"dirs,omitempty"`        // path prefixes (Kind=="dirs")
	Playlists   []int64  `json:"playlists,omitempty"`   // libdb playlist IDs (Kind=="playlists")
	TrackHashes []string `json:"trackHashes,omitempty"` // portable track hashes (Kind=="tracks")
}

// SyncTarget is one destination. App: "traktor" | "rekordbox" | "virtualdj" | "serato". Mode:
// "file" (write importable NML/XML) | "writeback" (live in-place into the app's library;
// serato = constant beatgrids into the audio files' tags, OutputPath = the _Serato_ dir) |
// "tags" (embed metadata into the audio files). OutputPath: file/writeback destination
// ("" = auto-detect the app's collection / a default next to it).
type SyncTarget struct {
	App        string `json:"app"`
	Mode       string `json:"mode"`
	OutputPath string `json:"outputPath,omitempty"`
}

// SyncRules controls the field merge + cue conversion. FieldSource maps a canonical field name
// (beatgrid|cues|rating|key|genre|bpm|comment|playCount) to the source app whose value wins;
// unset fields fall back to the default per-field priority. HotcuesToMemory demotes pad-assigned
// hotcues to memory/stored cues on export (Traktor HOTCUE=-1 / Rekordbox Num=-1). WriteFileTags
// also embeds metadata into the files (independent of a "tags" target).
type SyncRules struct {
	FieldSource     map[string]string `json:"fieldSource,omitempty"`
	HotcuesToMemory bool              `json:"hotcuesToMemory,omitempty"`
	WriteFileTags   bool              `json:"writeFileTags,omitempty"`
}

// SyncSchedule mirrors the automation Schedule shape for auto-sync. Kind: "" (off) | "interval"
// | "daily" | "cron" | "idle". ExcludeAppsRunning blocks a write-back run while a target DJ app
// is open (the live-write safety gate); empty defaults to the job's target apps.
type SyncSchedule struct {
	Enabled            bool     `json:"enabled"`
	Kind               string   `json:"kind"`
	IntervalMinutes    int      `json:"intervalMinutes,omitempty"`
	AtHour             int      `json:"atHour,omitempty"`
	AtMinute           int      `json:"atMinute,omitempty"`
	CronExpr           string   `json:"cronExpr,omitempty"`
	IdleMinutes        int      `json:"idleMinutes,omitempty"`
	ExcludeAppsRunning []string `json:"excludeAppsRunning,omitempty"`
}

// SetCaptureFeature configures the local Icecast-source receiver Traktor broadcasts to.
// The receiver authenticates the source connection, streams the encoded body to a
// timestamped file in SetsDir, and parses the broadcast metadata for now-playing - so a
// captured set is time-linked to the recorder's tracklist (offset = track start − capture
// start). Audio is broadcast-quality lossy (Ogg/MP3) by design (Icecast = encoded stream).
type SetCaptureFeature struct {
	Enabled  bool   `json:"enabled"`
	Port     int    `json:"port"`     // 0 = IcecastPort default
	Mount    string `json:"mount"`    // expected mount path, e.g. "/stream"; "" = accept any
	Username string `json:"username"` // source username; "" = "source" (Icecast default)
	Password string `json:"password"` // source password (required to accept a connection)
	SetsDir  string `json:"setsDir"`  // capture output dir; "" = <configDir>/sets

	// SingleFile records the whole broadcast to one file for as long as the Icecast source
	// stays connected: a brief drop + reconnect within the grace window resumes the same
	// file instead of starting a new one (so a set isn't chopped into fragments by transient
	// network blips or Traktor's encoder restarts). Off = one file per source connection.
	SingleFile            bool `json:"singleFile"`
	ReconnectGraceSeconds int  `json:"reconnectGraceSeconds"` // 0 = default (15s)

	// MetadataOnly keeps the Icecast receiver running for its now-playing/metadata parsing but
	// does NOT persist the broadcast audio to disk - use when native AudioRecord is the canonical
	// (lossless) recording and Icecast is only the metadata source.
	MetadataOnly bool `json:"metadataOnly"`
}

// AudioRecordFeature configures native audio-device capture (an ffmpeg dshow capture of a chosen
// input device - an audio interface or a virtual loopback cable). Default encoding is lossless
// FLAC at the device's native sample rate. FollowOBS auto-starts/stops the capture with OBS
// recording; manual start/stop works regardless. Off by default (opt-in).
type AudioRecordFeature struct {
	Enabled    bool   `json:"enabled"`
	Device     string `json:"device"`     // dshow audio device name; "" = none selected
	Dir        string `json:"dir"`        // output dir; "" = <configDir>/recordings
	Format     string `json:"format"`     // "flac" (default) | "wav" | "mp3" | "aac"
	Bitrate    int    `json:"bitrate"`    // lossy kbps (mp3/aac); 0 = default (320)
	SampleRate int    `json:"sampleRate"` // 0 = auto-detect device native rate
	FollowOBS  bool   `json:"followObs"`  // auto start/stop with OBS recording
	WriteTags  bool   `json:"writeTags"`  // embed the played tracklist into the file metadata
}

// ResolvedFormat returns the configured encoder or the FLAC default.
func (a AudioRecordFeature) ResolvedFormat() string {
	if a.Format != "" {
		return a.Format
	}
	return "flac"
}

// ResolvedBitrate returns the lossy bitrate (kbps) or the 320 default.
func (a AudioRecordFeature) ResolvedBitrate() int {
	if a.Bitrate > 0 {
		return a.Bitrate
	}
	return 320
}

// ResolvedDir returns the configured recordings dir or <configDir>/recordings.
func (a AudioRecordFeature) ResolvedDir() string {
	if a.Dir != "" {
		return a.Dir
	}
	if p, err := DataPath("recordings"); err == nil {
		return p
	}
	return "recordings"
}

// ResolvedPort returns the configured receiver port or the default.
func (s SetCaptureFeature) ResolvedPort() int {
	if s.Port > 0 {
		return s.Port
	}
	return IcecastPort
}

// ResolvedReconnectGrace returns the reconnect grace window for single-file capture (the
// configured seconds, or the 15s default). Only meaningful when SingleFile is on.
func (s SetCaptureFeature) ResolvedReconnectGrace() time.Duration {
	if s.ReconnectGraceSeconds > 0 {
		return time.Duration(s.ReconnectGraceSeconds) * time.Second
	}
	return 15 * time.Second
}

// ResolvedUsername returns the configured source username or the Icecast default.
func (s SetCaptureFeature) ResolvedUsername() string {
	if s.Username != "" {
		return s.Username
	}
	return "source"
}

// ResolvedSetsDir returns the configured capture dir or <configDir>/sets.
func (s SetCaptureFeature) ResolvedSetsDir() string {
	if s.SetsDir != "" {
		return s.SetsDir
	}
	if p, err := DataPath("sets"); err == nil {
		return p
	}
	return "sets"
}

// PeersFeature configures the LAN peer link. Enabled turns on mDNS discovery + the peer
// listener (the discovery on/off button). Nickname is what other instances show for us.
type PeersFeature struct {
	Enabled  bool   `json:"enabled"`
	Nickname string `json:"nickname"` // "" = derive from hostname
	// RemoteCacheMaxMB caps the remote cue-edit content cache (<config dir>/remote_cache,
	// pulled copies of peers' tracks) in MiB. 0 = built-in default (remotecache.DefaultCap).
	RemoteCacheMaxMB int `json:"remoteCacheMaxMB,omitempty"`
}

// RemoteCacheBytes converts the MiB cap to bytes; 0 = caller default.
func (p PeersFeature) RemoteCacheBytes() int64 { return int64(p.RemoteCacheMaxMB) << 20 }

// AccountBridgeFeature configures the rave.page account bridge: reaching THIS instance from
// outside the LAN, through the account's blind relay.
//
// Enabled registers this device on the account so your other devices (and the web Local Studio)
// can find it. LocalStudio additionally serves the Local Studio control channel over the bridge,
// so the web app can drive this machine from anywhere - not only from a browser on this box.
//
// The access gate (TOTP enrolment + trusted-session tokens) is NOT configured here: it lives in
// the sealed authz store, because config.json is plaintext.
type AccountBridgeFeature struct {
	Enabled     bool `json:"enabled"`
	LocalStudio bool `json:"localStudio,omitempty"` // serve the Local Studio channel over the bridge
}

// FileXferFeature configures file transfer between paired instances (send + receive).
// AcceptMode "ask" (default) holds incoming transfers for user confirmation; "auto" saves
// straight into the download dir.
type FileXferFeature struct {
	Enabled     bool   `json:"enabled"`
	DownloadDir string `json:"downloadDir,omitempty"` // "" = <config dir>/downloads
	AcceptMode  string `json:"acceptMode,omitempty"`  // "ask" (default) | "auto"
}

// AutoAccept reports whether incoming transfers skip the confirmation step.
func (f FileXferFeature) AutoAccept() bool { return f.AcceptMode == "auto" }

// ResolvedDownloadDir returns the configured download dir, defaulting to a rave-mate
// downloads dir under the user's config dir ("" only if that can't be resolved).
func (f FileXferFeature) ResolvedDownloadDir() string {
	if d := strings.TrimSpace(f.DownloadDir); d != "" {
		return d
	}
	base, err := Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "downloads")
}

// NMLFeature configures the Traktor NML source (history watch + collection enrichment).
// Empty paths auto-detect the newest Documents\Native Instruments\Traktor * install.
type NMLFeature struct {
	Enabled        bool   `json:"enabled"`
	CollectionPath string `json:"collectionPath"` // "" = auto-detect collection.nml
	HistoryDir     string `json:"historyDir"`     // "" = auto-detect History folder
}

// ProDJLinkFeature configures the passive Pro DJ Link listener (Pioneer CDJ/XDJ on the LAN).
// Read-only: binds UDP 50002 to observe status broadcasts (no virtual device announced).
type ProDJLinkFeature struct {
	Enabled bool `json:"enabled"`
}

// SeratoFeature configures the Serato source: collection (database V2 + crates) read + live
// now-playing from the active History session file. Fully local, no Serato account / internet.
// SeratoDir empty = auto-detect %USERPROFILE%\Music\_Serato_ (+ per-drive _Serato_ folders).
type SeratoFeature struct {
	Enabled     bool   `json:"enabled"`
	SeratoDir   string `json:"seratoDir"`   // "" = auto-detect Music\_Serato_
	NowPlaying  bool   `json:"nowPlaying"`  // watch History\Sessions for live now-playing (~1-2s)
	Remote      bool   `json:"remote"`      // real-time Serato Remote OSC-over-TCP source (opt-in)
	RemoteDebug bool   `json:"remoteDebug"` // log every inbound Remote frame (handshake capture)
	// Live Playlist: remote scrape of serato.com/playlists/<user>/live. Independent opt-in
	// (works without a local Serato install). LivePlaylistURL = full /live URL or a bare
	// username. Zero value = disabled.
	LivePlaylist         bool   `json:"livePlaylist"`
	LivePlaylistURL      string `json:"livePlaylistUrl"`
	LivePlaylistInterval int    `json:"livePlaylistIntervalSec"` // poll cadence seconds (0 = 10s default)
}

// VirtualDJFeature configures the VirtualDJ source: collection (database.xml, per-drive merge)
// + live now-playing via three optional channels. NetCtl = Network Control plugin HTTP poll
// (full metadata; needs VDJ Pro 2023+ and a one-time manual plugin install). OS2L = we host an
// mDNS+TCP OS2L server VDJ auto-connects to (live BPM/beat only, no track text, zero config).
// Tracklist = poll the History tracklist file (title/artist, laggy fallback).
type VirtualDJFeature struct {
	Enabled     bool   `json:"enabled"`
	DatabaseDir string `json:"databaseDir"` // "" = auto-detect Documents\VirtualDJ
	NetCtl      bool   `json:"netCtl"`      // poll Network Control plugin
	NetCtlURL   string `json:"netCtlUrl"`   // "" = http://127.0.0.1:80
	NetCtlAuth  string `json:"netCtlAuth"`  // optional bearer token (plugin "auth" setting)
	OS2L        bool   `json:"os2l"`        // host OS2L server (mDNS _os2l._tcp + TCP/JSON)
	OS2LPort    int    `json:"os2lPort"`    // 0 = OS2LPort default
	Tracklist   bool   `json:"tracklist"`   // poll History tracklist file (delayed fallback)
}

// ResolvedNetCtlURL returns the configured Network Control base URL or the localhost default.
func (v VirtualDJFeature) ResolvedNetCtlURL() string {
	if v.NetCtlURL != "" {
		return v.NetCtlURL
	}
	return "http://127.0.0.1:80"
}

// ResolvedOS2LPort returns the configured OS2L server port or the default.
func (v VirtualDJFeature) ResolvedOS2LPort() int {
	if v.OS2LPort > 0 {
		return v.OS2LPort
	}
	return OS2LPort
}

// RekordboxFeature configures live now-playing from rekordbox (collection read/write already
// lives in libsync). DBPoll polls master.db djmdSongHistory for recently-played - safe, reuses
// the existing SQLCipher decrypt, but ~60s lag (rekordbox marks "played" ~1min in). MemoryRead
// reads the rekordbox process memory for real-time deck/BPM/track - Windows-only, accurate, but
// fragile: it depends on per-version memory offsets and can break on a rekordbox update.
type RekordboxFeature struct {
	Enabled    bool   `json:"enabled"`
	DBPath     string `json:"dbPath"`     // "" = auto-detect master.db
	DBKey      string `json:"dbKey"`      // "" = RAVE_REKORDBOX_KEY env / built-in default
	DBPoll     bool   `json:"dbPoll"`     // poll djmdSongHistory (safe, ~60s lag)
	MemoryRead bool   `json:"memoryRead"` // read process memory (real-time, fragile, Windows-only)
}

// MIDIFeature configures the MIDI-in source. Port names are matched as substrings against
// the OS input-port list (e.g. a loopMIDI virtual port). Empty = that decoder is off.
type MIDIFeature struct {
	Enabled     bool   `json:"enabled"`
	DenonPort   string `json:"denonPort"`   // input port carrying the Denon HC4500 stock map
	CustomPort  string `json:"customPort"`  // input port carrying our custom TSI CC map
	MeshForward bool   `json:"meshForward"` // always-on mesh: mirror local MIDI to every connected peer

	// DisableUIBinds pauses the desktop-UI MIDI mappings (the cueedit/library/nav bind groups
	// in VROverlay.Binds) without deleting them. Inverted so the zero value = mappings active
	// (v29, additive, no migration). VR-group binds are unaffected.
	DisableUIBinds bool `json:"disableUiBinds,omitempty"`
	// DisabledBindProfiles pauses individual per-device UI-mapping profiles (v30). A profile is
	// derived, not stored: every ui.* bind belongs to the profile of the controller whose Port
	// matches the bind's captured port (BindProfileKey), or the any-device profile
	// (BindProfileAny) when the bind has no port. Keys here = controller Port values /
	// BindProfileAny / raw ports with no configured controller.
	DisabledBindProfiles []string `json:"disabledBindProfiles,omitempty"`

	// Controllers = native MIDI-learn maps (v27): each is a physical controller read
	// directly (or via a virtual port), with per-control learned bindings that all feed the
	// shared deck/channel model. Multiple controllers may be open at once on different ports.
	Controllers []MIDIControllerMap `json:"controllers,omitempty"`
	// Bridge = two-port loopMIDI router to a DJ app (v27): peer control + optional controller
	// THRU flow out ToDJPort (the DJ reads it); the DJ's own output (indicators/VU) is read
	// back on FromDJPort. Off unless Enabled + a port is set.
	Bridge MIDIBridge `json:"bridge,omitempty"`
}

// BindProfileAny keys the any-device UI-mapping profile (binds captured without a port).
const BindProfileAny = "*"

// BindProfileKey resolves a bind's captured port to its profile key: "" = the any-device
// profile; a port matching a configured controller (controller Port as case-insensitive
// substring, config order wins) = that controller's Port; anything else = the raw port
// (an un-configured device keeps its own profile rather than silently going global).
func (m MIDIFeature) BindProfileKey(bindPort string) string {
	if bindPort == "" {
		return BindProfileAny
	}
	for _, c := range m.Controllers {
		if c.Port != "" && strings.Contains(strings.ToLower(bindPort), strings.ToLower(c.Port)) {
			return c.Port
		}
	}
	return bindPort
}

// BindProfileDisabled reports whether the profile owning a bind's captured port is paused.
func (m MIDIFeature) BindProfileDisabled(bindPort string) bool {
	key := m.BindProfileKey(bindPort)
	for _, k := range m.DisabledBindProfiles {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// SetBindProfileDisabled pauses/resumes one profile key in DisabledBindProfiles.
func (m *MIDIFeature) SetBindProfileDisabled(key string, off bool) {
	for i, k := range m.DisabledBindProfiles {
		if strings.EqualFold(k, key) {
			if !off {
				m.DisabledBindProfiles = append(m.DisabledBindProfiles[:i], m.DisabledBindProfiles[i+1:]...)
			}
			return
		}
	}
	if off {
		m.DisabledBindProfiles = append(m.DisabledBindProfiles, key)
	}
}

// MIDIBinding maps one learned MIDI message to a control on a deck/channel. Status carries the
// type nibble + MIDI channel captured at learn time; a CC binding matches CC, a Note binding
// matches Note-On/Off. Control is a midimap.Control ID (eqHigh/eqMid/eqLow/filter/trim/fader/
// cue/play). Channel is the 1-based deck/mixer channel the value is applied to.
type MIDIBinding struct {
	Control string `json:"control"`
	Channel int    `json:"channel"`
	Status  byte   `json:"status"`
	Data1   byte   `json:"data1"`
	Invert  bool   `json:"invert,omitempty"` // reverse a continuous value (min<->max)
}

// MIDIControllerMap = one physical controller: an input port + its learned bindings. ThruPort
// (optional) forwards every raw message on to a MIDI-OUT port (a loopMIDI cable the DJ app
// reads) so rave-mate can read the controller AND the DJ app still gets it on single-client
// Windows MIDI - built-in split, no MIDI-OX needed. Empty ThruPort = direct read only (works
// when the driver is multi-client or on Windows MIDI Services).
type MIDIControllerMap struct {
	Name     string        `json:"name"`
	Port     string        `json:"port"`
	ThruPort string        `json:"thruPort,omitempty"`
	Enabled  bool          `json:"enabled"`
	Bindings []MIDIBinding `json:"bindings,omitempty"`
	// DriverFilter: message classes the ravemidi driver drops on the DJ-facing
	// fan-out (keys per midi.FilterKeys). nil = midi.DefaultDriverFilter();
	// empty non-nil = filter nothing. No omitempty: [] must persist.
	DriverFilter []string `json:"driverFilter"`
	// ThruDistinctName: name the ravemidi DJ-facing fan-out "<Name> THRU" instead of
	// cloning the physical device's own name. Default (false) clones the device name so
	// DJ software keyed on the controller name (Serato) matches existing mappings 1:1; the
	// driver holds the real device's pin, so the identically-named clone is what the DJ app
	// opens. Set true for a separate, uniquely-named port (no duplicate in the MIDI list).
	ThruDistinctName bool `json:"thruDistinctName,omitempty"`
}

// MIDIBridge is the two-port loopMIDI DJ router. ToDJPort = MIDI-OUT the DJ app reads (peer
// control lands here, so a paired instance can drive this DJ rig); FromDJPort = MIDI-IN the DJ
// app writes (its own indicator/VU output, fed into the decoders). Either port may be empty.
type MIDIBridge struct {
	Enabled    bool   `json:"enabled"`
	ToDJPort   string `json:"toDjPort,omitempty"`
	FromDJPort string `json:"fromDjPort,omitempty"`
}

// RecorderFeature configures the session recorder. ConfirmSeconds is how long a track must
// be audibly playing before it counts as "played" (mirrors Traktor's history commit rule).
// Export* remember the Publish tab's text-export style (preset id, custom line template,
// header on/off) across sessions.
type RecorderFeature struct {
	Enabled        bool   `json:"enabled"`
	ConfirmSeconds int    `json:"confirmSeconds"` // 0 = default (30)
	ExportPreset   string `json:"exportPreset,omitempty"`
	ExportLine     string `json:"exportLine,omitempty"` // custom per-track template (preset "custom")
	ExportNoHeader bool   `json:"exportNoHeader,omitempty"`
}

// ResolvedConfirmSeconds returns the configured confirm threshold or the default.
func (r RecorderFeature) ResolvedConfirmSeconds() int {
	if r.ConfirmSeconds > 0 {
		return r.ConfirmSeconds
	}
	return 30
}

// FileSinkFeature configures the now-playing file writer (OBS text/browser sources). Empty
// Dir writes into the app config dir.
type FileSinkFeature struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"` // "" = app config dir
}

// OverlayWebFeature configures the live multi-deck browser overlay server (an OBS Browser
// source points at http://127.0.0.1:<port>/). Off by default (opt-in).
type OverlayWebFeature struct {
	Enabled   bool             `json:"enabled"`
	Port      int              `json:"port"`      // 0 = OverlayWebPort default
	OBSSource OverlayOBSSource `json:"obsSource"` // auto-manage the OBS browser source over obs-websocket
}

// OverlayOBSSource auto-creates + maintains the overlay browser source in OBS (requires the OBS
// feature enabled + OBS WebSocket up). The source lives in a dedicated scene, sized to the OBS
// canvas; optionally that scene is also nested into the current program scene.
type OverlayOBSSource struct {
	Enabled       bool   `json:"enabled"`
	Scene         string `json:"scene"`         // dedicated scene name; "" = "rave-mate"
	SourceName    string `json:"sourceName"`    // browser-source input name; "" = "rave-mate overlay"
	Width         int    `json:"width"`         // 0 = match OBS canvas
	Height        int    `json:"height"`        // 0 = match OBS canvas
	NestInProgram bool   `json:"nestInProgram"` // also add the dedicated scene into the current program scene
}

// ResolvedScene returns the dedicated scene name or the default.
func (s OverlayOBSSource) ResolvedScene() string {
	if s.Scene != "" {
		return s.Scene
	}
	return "rave-mate"
}

// ResolvedSourceName returns the browser-source name or the default.
func (s OverlayOBSSource) ResolvedSourceName() string {
	if s.SourceName != "" {
		return s.SourceName
	}
	return "rave-mate overlay"
}

// ResolvedPort returns the configured overlay port or the default.
func (o OverlayWebFeature) ResolvedPort() int {
	if o.Port > 0 {
		return o.Port
	}
	return OverlayWebPort
}

// VideoShareFeature toggles the GPU/IPC video-share sink: each loaded deck's card is published as
// a live video frame over the OS-native sharing API (Windows Spout / macOS Syphon / Linux
// PipeWire) for any compatible receiver. The transport is chosen at build time (-tags
// spout|syphon|pipewire); the default build has no backend and this sink publishes nothing. Off
// by default (opt-in).
type VideoShareFeature struct {
	Enabled bool `json:"enabled"`
	// RenderScale supersamples the per-deck card so it stays crisp when a receiver displays it
	// large (e.g. on a 4K canvas): the card is rendered natively at RenderScale× its base
	// 360×120 (geometry + fonts scaled, not upscaled). 0 = default (2). Clamped 1..8.
	RenderScale int `json:"renderScale"`
}

// ResolvedRenderScale returns the video-share supersample factor (default 2, clamped 1..8).
func (v VideoShareFeature) ResolvedRenderScale() int {
	if v.RenderScale <= 0 {
		return 2
	}
	if v.RenderScale > 8 {
		return 8
	}
	return v.RenderScale
}

// OverlayWaveformFeature configures the combined scrolling-waveform + EQ-curve + FX-cutoff panel
// in the now-playing overlays (native PNG cards, video-share, browser). When enabled each deck
// card gains a full-width waveform that scrolls right→left with playback (playhead fixed at
// PlayheadPct from the left); the EQ curve + a filter-cutoff curve overlay it. Peaks are generated
// on first play (ffmpeg) and cached. Browser + video-share scroll smoothly; PNG updates on state
// change. Off by default (opt-in).
type OverlayWaveformFeature struct {
	Enabled     bool    `json:"enabled"`
	ZoomSeconds float64 `json:"zoomSeconds"` // visible timeframe across the panel (smaller = faster scroll); 0 = 20
	PlayheadPct float64 `json:"playheadPct"` // playhead x from the left (0..1); 0 = 3/4

	// Appearance. Colors are #rrggbb; opacities 0..1. Defaults match the built-in look.
	WaveColor   string  `json:"waveColor"`   // waveform tint (played bars full, upcoming dimmed); "" = brand mint
	WaveOpacity float64 `json:"waveOpacity"` // waveform bar opacity (0..1)
	BgColor     string  `json:"bgColor"`     // waveform canvas background; "" = near-black
	BgOpacity   float64 `json:"bgOpacity"`   // canvas background opacity (0..1; 0 = transparent)
}

// Waveform appearance defaults (also the built-in look when a field is unset).
const (
	defWaveColor   = "#08F79B"
	defWaveOpacity = 1.0
	defWaveBgColor = "#0a0a0e"
	defWaveBgOpac  = 0.78
)

// ResolvedZoomSeconds returns the visible-window seconds (default 20, clamped 2..600).
func (o OverlayWaveformFeature) ResolvedZoomSeconds() float64 {
	if o.ZoomSeconds <= 0 {
		return 20
	}
	if o.ZoomSeconds < 2 {
		return 2
	}
	if o.ZoomSeconds > 600 {
		return 600
	}
	return o.ZoomSeconds
}

// ResolvedPlayheadPct returns the playhead fraction from the left (default 3/4).
func (o OverlayWaveformFeature) ResolvedPlayheadPct() float64 {
	if o.PlayheadPct <= 0 || o.PlayheadPct >= 1 {
		return 0.75
	}
	return o.PlayheadPct
}

// ResolvedWaveColor / ResolvedBgColor return the configured hex colour or the built-in default.
func (o OverlayWaveformFeature) ResolvedWaveColor() string { return orHex(o.WaveColor, defWaveColor) }
func (o OverlayWaveformFeature) ResolvedBgColor() string   { return orHex(o.BgColor, defWaveBgColor) }

// ResolvedWaveOpacity / ResolvedBgOpacity clamp to 0..1 (kept literal - Default sets the base, so
// an explicit 0 means transparent, not "use default").
func (o OverlayWaveformFeature) ResolvedWaveOpacity() float64 { return clampUnit(o.WaveOpacity) }
func (o OverlayWaveformFeature) ResolvedBgOpacity() float64   { return clampUnit(o.BgOpacity) }

// orHex returns s if it parses as #rgb/#rrggbb, else the fallback.
func orHex(s, fallback string) string {
	s = strings.TrimSpace(s)
	if len(s) == 4 || len(s) == 7 {
		if s[0] == '#' {
			return s
		}
	}
	return fallback
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// OverlayOBSFeature configures the obs-websocket renderer: rave-mate creates/updates a text +
// image input per loaded deck directly in OBS's current scene (no browser/file). Reuses the OBS
// feature's connection (host/port/password) - that must be enabled too. Off by default (opt-in).
type OverlayOBSFeature struct {
	Enabled bool `json:"enabled"`
}

// OBSFeature configures the OBS obs-websocket v5 client.
type OBSFeature struct {
	Enabled  bool         `json:"enabled"`
	Host     string       `json:"host"`              // 0 = 127.0.0.1
	Port     int          `json:"port"`              // 0 = 4455
	Password string       `json:"password"`          // empty if OBS auth disabled
	Remotes  []OBSRemote  `json:"remotes,omitempty"` // additional OBS instances on the LAN, connected directly
	Sync     OBSMediaSync `json:"sync,omitempty"`    // media-sync tier: chase OBS media sources to the house clock
}

// OBSMediaSync configures the media-sync tier: keep chosen OBS media sources locked to
// rave-mate's house clock (across the local OBS + any LAN remotes). Off by default.
type OBSMediaSync struct {
	Enabled            bool            `json:"enabled"`
	DeadBandFrames     float64         `json:"deadBandFrames,omitempty"`     // 0 = default (2)
	Fps                float64         `json:"fps,omitempty"`                // 0 = default (30); frame rate for the dead-band
	RestartThresholdMs int             `json:"restartThresholdMs,omitempty"` // 0 = default (1500)
	Sources            []OBSSyncSource `json:"sources,omitempty"`
}

// OBSSyncSource is one media input to keep in sync on a chosen OBS endpoint.
type OBSSyncSource struct {
	Endpoint       string `json:"endpoint"`            // OBS source id: "" / "local" = local OBS; else a remote's ID() (obs@host:port)
	InputName      string `json:"inputName"`           // OBS media input name
	InputKind      string `json:"inputKind,omitempty"` // obs.Kind* hint (auto-detected when empty)
	StaticOffsetMs int    `json:"staticOffsetMs,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// OBSRemote is an additional OBS instance on the LAN that rave-mate connects to directly over
// obs-websocket (no rave-mate needed on that PC). Appears as its own instance in the cockpit + VR.
type OBSRemote struct {
	Name     string `json:"name"`     // friendly label (defaults to host:port)
	Host     string `json:"host"`     // LAN IP / hostname of the OBS PC
	Port     int    `json:"port"`     // obs-websocket port (0 = 4455)
	Password string `json:"password"` // obs-websocket password (empty if auth disabled)
	Enabled  bool   `json:"enabled"`
}

// ResolvedPort returns the remote's obs-websocket port or the default.
func (r OBSRemote) ResolvedPort() int {
	if r.Port > 0 {
		return r.Port
	}
	return 4455
}

// ID is the stable identifier for a remote OBS (host:port) used for routing + status keying.
func (r OBSRemote) ID() string {
	return fmt.Sprintf("obs@%s:%d", r.Host, r.ResolvedPort())
}

// ResolvedName returns the remote's label (defaults to host:port).
func (r OBSRemote) ResolvedName() string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("%s:%d", r.Host, r.ResolvedPort())
}

// ResolvedHost returns the configured OBS host or the default.
func (o OBSFeature) ResolvedHost() string {
	if o.Host != "" {
		return o.Host
	}
	return "127.0.0.1"
}

// ResolvedPort returns the configured obs-websocket port or the default.
func (o OBSFeature) ResolvedPort() int {
	if o.Port > 0 {
		return o.Port
	}
	return 4455
}

// VRChatFeature configures the client-side VRChat bridge. Credentials are never
// stored - only the session cookie, DPAPI-sealed, when RememberSession is on.
// Uplink pushes the session token to rave.page (server-side group/event features);
// strictly opt-in.
type VRChatFeature struct {
	Enabled         bool `json:"enabled"`
	RememberSession bool `json:"rememberSession"` // seal session at rest, auto-resume
	Uplink          bool `json:"uplink"`          // share session token with rave.page

	// ── VRChat tab: status/bio + emoji flipbook tools (all additive, empty/off by default) ──
	StatusPresets []VRChatStatusPreset `json:"statusPresets,omitempty"` // quick-apply presence + status text
	BioPresets    []VRChatBioPreset    `json:"bioPresets,omitempty"`    // quick-apply bio templates (with {vars})
	BioVars       map[string]string    `json:"bioVars,omitempty"`       // manual fallback values for bio {variables}
	FlipbookDir   string               `json:"flipbookDir,omitempty"`   // emoji sprite-sheet output dir ("" = <configDir>/emoji)
}

// VRChatStatusPreset is a saved presence (join me|active|ask me|busy) + status-text pair.
type VRChatStatusPreset struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description"` // statusDescription (≤32 chars)
}

// VRChatBioPreset is a saved bio template. Template may carry {placeholders} (e.g. {next_event},
// {next_event_date}, {next_event_venue}) resolved from rave.page upcoming events or BioVars at save.
type VRChatBioPreset struct {
	Name     string `json:"name"`
	Template string `json:"template"`
}

// ResolvedFlipbookDir returns the configured emoji output dir or <configDir>/emoji.
func (v VRChatFeature) ResolvedFlipbookDir() string {
	if v.FlipbookDir != "" {
		return v.FlipbookDir
	}
	if p, err := DataPath("emoji"); err == nil {
		return p
	}
	return "emoji"
}

// Toggle is a feature with no config beyond on/off.
type Toggle struct {
	Enabled bool `json:"enabled"`
}

// StreamBridgeFeature gates the live set → rave.page now-playing broadcast. Publishing is automatic,
// driven by the OBS stream signal (no manual go-live). PauseLiveSignal suppresses that auto-broadcast
// for private / non-DJ streams. Additive + inverted: zero value = broadcast-on, no version migration.
type StreamBridgeFeature struct {
	Enabled         bool `json:"enabled"`
	PauseLiveSignal bool `json:"pauseLiveSignal,omitempty"`
}

// VROverlayFeature configures VR overlays (OpenVR/SteamVR). Requires SteamVR running + a build with
// the `vr` tag. Overlays render bus-sourced content (Twitch chat, alerts) into the headset - so a
// VR PC shows the chat from another rave-mate instance that owns the Twitch connection.
// VRCToolsFeature configures the VRChat screenshot organizer + camera-path manager. Dirs empty =
// VRChat defaults (Pictures\VRChat, Documents\VRChat\CameraPaths). Organize* enables automatic
// sorting; *Move moves files (else copies). OSCAddr targets VRChat for /dolly path loading.
type VRCToolsFeature struct {
	Enabled          bool   `json:"enabled"`
	PhotosDir        string `json:"photosDir,omitempty"`
	OrganizePhotos   bool   `json:"organizePhotos"`
	PhotoMove        bool   `json:"photoMove"`
	OrganizeByEvent  bool   `json:"organizeByEvent"` // file photos under the rave.page event whose window contains the capture time (primary); world timeline is the fallback
	CamPathsDir      string `json:"camPathsDir,omitempty"`
	OrganizeCamPaths bool   `json:"organizeCamPaths"`
	CamPathMove      bool   `json:"camPathMove"`
	OSCAddr          string `json:"oscAddr,omitempty"` // VRChat OSC target for /dolly load (default 127.0.0.1:9000)

	// Camera-path crash-resilience for live sets. AutoBackup copies every path rave-mate plays into a
	// per-world backup slot; AutoRestore reloads that world's backup a few seconds after you rejoin it
	// while a set is live (OBS streaming/recording or Twitch live). CamPathBackupDir empty = <dataDir>/
	// campath_backups. Limitation: only captures paths rave-mate itself plays - not paths triggered
	// inside VRChat's own dolly UI (VRChat exposes no OSC readback for dolly).
	AutoBackupCamPaths  bool   `json:"autoBackupCamPaths"`
	AutoRestoreCamPaths bool   `json:"autoRestoreCamPaths"`
	CamPathBackupDir    string `json:"camPathBackupDir,omitempty"`

	CamPresets       []cameraosc.Preset `json:"camPresets,omitempty"`       // saved camera look presets (/usercamera params)
	DefaultCamPreset string             `json:"defaultCamPreset,omitempty"` // preset auto-applied after a path loads ("" = none)

	AvatarVRM string `json:"avatarVrm,omitempty"` // .vrm/.glb/.gltf/.fbx avatar for the motion-studio preview + video render
}

// AllCamPresets returns builtin presets followed by the user's saved ones.
func (f VRCToolsFeature) AllCamPresets() []cameraosc.Preset {
	return append(cameraosc.BuiltinPresets(), f.CamPresets...)
}

// STTFeature configures local speech-to-text (Whisper) dictation that posts to Twitch chat.
// Audio devices are dshow names (as ffmpeg lists them); empty = system default. Model is a ggml
// file name (empty = the default base.en). Submit: AutoSubmit posts after SilenceMs of silence;
// otherwise the user posts/discards via keybinds (vrbind ActSTTSend/Discard).
type STTFeature struct {
	Enabled      bool    `json:"enabled"`
	InputDevice  string  `json:"inputDevice,omitempty"`  // mic (dshow name; "" = default)
	OutputDevice string  `json:"outputDevice,omitempty"` // playback device for cues (dshow name; "" = default)
	Model        string  `json:"model,omitempty"`        // ggml model file ("" = default base.en)
	AutoSubmit   bool    `json:"autoSubmit"`             // post automatically after SilenceMs of silence
	SilenceMs    int     `json:"silenceMs,omitempty"`    // trailing-silence timeout for auto-submit (default 1200)
	Threshold    float64 `json:"threshold,omitempty"`    // VAD RMS threshold 0..1 (default 0.015)
}

// ResolvedSilenceMs returns the auto-submit silence timeout (default 1200ms).
func (s STTFeature) ResolvedSilenceMs() int {
	if s.SilenceMs > 0 {
		return s.SilenceMs
	}
	return 1200
}

// UnityFeature configures Unity-project integration: selected project roots that rave-mate
// installs the rave.page editor plugin into + exports motion takes (.anim) to.
type UnityFeature struct {
	Enabled  bool     `json:"enabled"`
	Projects []string `json:"projects,omitempty"` // selected Unity project roots
}

type VROverlayFeature struct {
	Enabled        bool          `json:"enabled"`
	Overlays       []VROverlay   `json:"overlays,omitempty"`
	MicToggle      MicToggleBind `json:"micToggle"`               // OBS mic mute toggle (VR hotkey / MIDI)
	EditHand       string        `json:"editHand"`                // "left"|"right" - wrist hosting the edit badge
	SummonButton   string        `json:"summonButton"`            // face button the SUMMON action binds to: "ax"|"by"|"custom" (default "ax")
	SummonOn       bool          `json:"summonOn"`                // enable the summon button (hold = open/close editor)
	SummonTapHides bool          `json:"summonTapHides"`          // short tap of the summon button shows/hides the overlays
	VRViewCapture  bool          `json:"vrViewCapture"`           // allow ctl screenshot-vr to capture the SteamVR VR-View mirror window (opt-in, Windows-only)
	OSCAddr        string        `json:"oscAddr,omitempty"`       // VRChat OSC target for motion playback (default 127.0.0.1:9000)
	VMCAddr        string        `json:"vmcAddr,omitempty"`       // VMC-protocol receiver for VTuber motion (VSeeFace/Warudo/VNyan; default 127.0.0.1:39539)
	VMCLive        bool          `json:"vmcLive"`                 // stream live VR motion to the VMC receiver while in the headset (VTuber)
	AutoStart      bool          `json:"autoStart"`               // register a SteamVR .vrmanifest + auto-launch with SteamVR
	InProc         bool          `json:"inProc,omitempty"`        // opt-OUT of the supervised VR subprocess (task #4): run OpenVR in the daemon like before
	StickMoveOnly  bool          `json:"stickMoveOnly,omitempty"` // opt-in: disable free-hand grip-grab; move/rotate overlays only via the positioning-menu sticks/buttons (no accidental grabs)
	WristPos       string        `json:"wristPos,omitempty"`      // edit-badge spot on the wrist/hand: "inner"(default, XSOverlay-style watch)|"top"|"back"|"above"|"out"
	WristLarge     bool          `json:"wristLarge,omitempty"`    // edit badge at the old large size (default = small)
	Binds          []vrbind.Bind `json:"binds,omitempty"`         // user keybinds: VR-slot and/or MIDI → app action (OBS rec/stream, overlay show/hide, …)

	// Editor-menu placement (set by dragging the menu in VR). MenuSnap "" = auto (floats above the
	// edit hand); else "left"|"right"|"head"|"world" with the offset below.
	MenuSnap  string  `json:"menuSnap,omitempty"`
	MenuX     float64 `json:"menuX,omitempty"`
	MenuY     float64 `json:"menuY,omitempty"`
	MenuZ     float64 `json:"menuZ,omitempty"`
	MenuYaw   float64 `json:"menuYaw,omitempty"`
	MenuPitch float64 `json:"menuPitch,omitempty"`
	MenuWidth float64 `json:"menuWidth,omitempty"` // metres; 0 = default
	MenuBg    float64 `json:"menuBg,omitempty"`    // menu background opacity 0..1 (0 = use default)

	Layouts []VRLayout `json:"layouts,omitempty"` // named saved overlay layouts

	QuickButtons    []VRQuickButton `json:"quickButtons,omitempty"`    // extra wrist-strip buttons → app actions
	WorldLayouts    []VRWorldLayout `json:"worldLayouts,omitempty"`    // layout bound per VRChat world
	WorldLayoutMode string          `json:"worldLayoutMode,omitempty"` // "off"|"notify"|"auto" ("" = notify)
}

// VRQuickButton is a user-configured wrist-strip button firing an app action.
type VRQuickButton struct {
	Label  string `json:"label"`
	Glyph  string `json:"glyph,omitempty"`  // 1-3 chars drawn on the button ("" = derived from Label)
	Action string `json:"action"`           // vrbind ActionID, or "layout.load" / "campath.load"
	Target string `json:"target,omitempty"` // overlay id / layout name / camera-path file / OBS instance
}

// VRWorldLayout binds a saved layout to a VRChat world (applied per WorldLayoutMode on join).
type VRWorldLayout struct {
	WorldID   string `json:"worldId"`
	WorldName string `json:"worldName,omitempty"` // display only
	Layout    string `json:"layout"`              // VRLayout.Name
	Enabled   bool   `json:"enabled"`
}

// SubprocessEnabled reports whether the overlay stack runs in the supervised `rave-mate feature vr`
// child (default ON for vr-tagged builds - a cgo/OpenVR fault then kills only the child). InProc
// opts out; non-vr builds always run the in-proc stub (no point spawning a child).
func (v VROverlayFeature) SubprocessEnabled(vrBuild bool) bool { return vrBuild && !v.InProc }

// ResolvedWorldLayoutMode returns the per-world layout auto-apply mode (default "notify" -
// non-destructive: suggests, never overwrites, until the user opts into "auto").
func (v VROverlayFeature) ResolvedWorldLayoutMode() string {
	switch v.WorldLayoutMode {
	case "off", "notify", "auto":
		return v.WorldLayoutMode
	}
	return "notify"
}

// VRLayout is a named snapshot of the overlay set + menu placement (save / load / import / export).
type VRLayout struct {
	Name      string      `json:"name"`
	Overlays  []VROverlay `json:"overlays"`
	MenuSnap  string      `json:"menuSnap,omitempty"`
	MenuX     float64     `json:"menuX,omitempty"`
	MenuY     float64     `json:"menuY,omitempty"`
	MenuZ     float64     `json:"menuZ,omitempty"`
	MenuYaw   float64     `json:"menuYaw,omitempty"`
	MenuPitch float64     `json:"menuPitch,omitempty"`
	MenuWidth float64     `json:"menuWidth,omitempty"`
	MenuBg    float64     `json:"menuBg,omitempty"`
}

// ResolvedMenuBg returns the editor-menu background opacity (default 0.94).
func (v VROverlayFeature) ResolvedMenuBg() float64 {
	if v.MenuBg > 0 {
		return v.MenuBg
	}
	return 0.94
}

// ResolvedOSCAddr returns the VRChat OSC target for motion playback (default 127.0.0.1:9000).
func (v VROverlayFeature) ResolvedOSCAddr() string {
	if v.OSCAddr != "" {
		return v.OSCAddr
	}
	return "127.0.0.1:9000"
}

// ResolvedVMCAddr returns the VMC receiver target for VTuber motion (default 127.0.0.1:39539).
func (v VROverlayFeature) ResolvedVMCAddr() string {
	if v.VMCAddr != "" {
		return v.VMCAddr
	}
	return "127.0.0.1:39539"
}

// ResolvedSummonButton returns the face button the summon action binds to: "ax"|"by"|"custom"
// (default "ax"). "custom" = leave summon unbound so the user assigns it in SteamVR.
func (v VROverlayFeature) ResolvedSummonButton() string {
	switch v.SummonButton {
	case "ax", "by", "custom":
		return v.SummonButton
	}
	return "ax"
}

// ResolvedWristPos returns the edit-badge placement preset (default "top" - the known-visible spot;
// "inner" is the XSOverlay watch style, opt-in until its facing is tuned in-headset).
func (v VROverlayFeature) ResolvedWristPos() string {
	switch v.WristPos {
	case "inner", "top", "back", "above", "out":
		return v.WristPos
	}
	return "top"
}

// ResolvedEditHand returns which wrist hosts the edit toggle (default left).
func (v VROverlayFeature) ResolvedEditHand() string {
	if v.EditHand == "right" {
		return "right"
	}
	return "left"
}

// VROverlay is one configurable overlay panel.
type VROverlay struct {
	ID string `json:"id"`
	// Type: "chat" | "alerts" | "obs" | "viewers" | "viewerlist" | live-stats "perf" | "network" | "timing".
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`

	// Placement. SnapTo "" = world-anchored (X/Y/Z in room space); "left"/"right" = parent to that
	// controller with the offset. Angles in degrees.
	SnapTo string  `json:"snapTo"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Z      float64 `json:"z"`
	Yaw    float64 `json:"yaw"`
	Pitch  float64 `json:"pitch"`
	Roll   float64 `json:"roll"`

	WidthM    float64 `json:"widthM"`              // overlay width in meters (height derives from aspect)
	Opacity   float64 `json:"opacity"`             // overall overlay opacity 0..1
	BgOpacity float64 `json:"bgOpacity,omitempty"` // panel background opacity 0..1, independent of Opacity (0 = default)

	// Content + style.
	MaxMessages     int     `json:"maxMessages"`               // 0 = default
	DisplaySeconds  float64 `json:"displaySeconds"`            // per-message fade-out; 0 = persistent
	FontScale       float64 `json:"fontScale"`                 // 0 = default
	HidePlaceholder bool    `json:"hidePlaceholder,omitempty"` // hide the sample/placeholder content when empty (default: show)
	AlwaysShow      bool    `json:"alwaysShow,omitempty"`      // stay visible even when overlays are globally hidden (lock)

	// Toggle binds (show/hide the overlay).
	ToggleAction string `json:"toggleAction"` // VR controller action name ("" = none)
	ToggleMIDI   int    `json:"toggleMidi"`   // MIDI note/CC number (0 = none)
}

// MicToggleBind binds an OBS mic-mute toggle to a VR controller action and/or MIDI input. The OBS
// input (source) is selectable.
type MicToggleBind struct {
	Enabled  bool   `json:"enabled"`
	OBSInput string `json:"obsInput"` // OBS input/source name to mute-toggle
	VRAction string `json:"vrAction"` // VR controller action ("" = none)
	MIDINote int    `json:"midiNote"` // MIDI note/CC (0 = none)
}

// ResolvedMaxMessages returns the chat history depth for an overlay (default 8).
func (o VROverlay) ResolvedMaxMessages() int {
	if o.MaxMessages > 0 {
		return o.MaxMessages
	}
	return 8
}

// ResolvedWidthM returns the overlay width in meters (default 0.5).
func (o VROverlay) ResolvedWidthM() float64 {
	if o.WidthM > 0 {
		return o.WidthM
	}
	return 0.5
}

// ResolvedOpacity returns the overall overlay opacity (default 0.9).
func (o VROverlay) ResolvedOpacity() float64 {
	if o.Opacity > 0 {
		return o.Opacity
	}
	return 0.9
}

// ResolvedBgOpacity returns the panel background opacity (default 0.82 ~ the classic card alpha).
func (o VROverlay) ResolvedBgOpacity() float64 {
	if o.BgOpacity > 0 {
		return o.BgOpacity
	}
	return 0.82
}

// TwitchFeature configures the Twitch integration (chat, alerts, stream-title control, moderation).
// OAuth tokens are never stored here - the access/refresh pair is sealed at rest (twitch.bin) via
// secureseal. Device Code Flow means no client secret. ClientID defaults to the bundled rave-mate
// app; override only to use your own Twitch application.
type TwitchFeature struct {
	Enabled  bool          `json:"enabled"`
	ClientID string        `json:"clientId"`          // "" = bundled DefaultTwitchClientID
	Presets  []TitlePreset `json:"presets,omitempty"` // reusable stream-title templates
}

// ResolvedClientID returns the configured client id or the bundled default.
func (t TwitchFeature) ResolvedClientID() string {
	if t.ClientID != "" {
		return t.ClientID
	}
	return DefaultTwitchClientID
}

// WorldSyncFeature configures VRChat world gist feeds: permission lists + display channels
// (posters/events/now-playing) published as GitHub gists that worlds poll via VRC string
// loading. GitHub token sealed at rest (github.bin), never here. Gist ids are pointers, not
// secrets. Off by default (needs GitHub + VRChat links).
type WorldSyncFeature struct {
	Enabled        bool   `json:"enabled"`
	GitHubClientID string `json:"githubClientId,omitempty"` // OAuth app id for Device Flow; "" = PAT paste only
	RefreshMins    int    `json:"refreshMins,omitempty"`    // list/channel refresh interval; 0 = 10
	NowPlayingSecs int    `json:"nowPlayingSecs,omitempty"` // min secs between now-playing writes; 0 = 60

	Lists []PermList `json:"lists,omitempty"` // permission lists (one gist each)

	Posters       []WorldPoster `json:"posters,omitempty"`       // poster-billboard channel content
	PostersGistID string        `json:"postersGistId,omitempty"` // "" until first publish
	PostersOn     bool          `json:"postersOn,omitempty"`

	EventsOn     bool   `json:"eventsOn,omitempty"` // publish upcoming rave.page events
	EventsGistID string `json:"eventsGistId,omitempty"`

	NowPlayingOn     bool   `json:"nowPlayingOn,omitempty"` // publish live now-playing (redacted session output)
	NowPlayingGistID string `json:"nowPlayingGistId,omitempty"`
	NowPlayingLink   string `json:"nowPlayingLink,omitempty"` // rave.page profile/stream URL shown on the card
	NowPlayingImg    string `json:"nowPlayingImg,omitempty"`  // card image URL (must be VRC image-allowlisted host)

	LightCuesOn     bool   `json:"lightCuesOn,omitempty"`     // publish the current DMX lighting-cue take
	LightCuesGistID string `json:"lightCuesGistId,omitempty"` // "" until first publish

	FavoriteGroups []FavoriteGroup `json:"favoriteGroups,omitempty"` // pinned groups for quick role grants

	// rave.live enveloped module gists (world PULLs) + editor-published rosters. These COEXIST with
	// the flat allow.txt/posters.json above: the flat files stay for VideoTXL/ProTV/RaveAccessControl
	// compat, the enveloped gists carry the SEQ-GATE'd {schema,seq,...} envelope. Gist ids are
	// pointers, not secrets. See docs/WORLD_BRIDGE_CONTRACT.md.
	PointerOn        bool              `json:"pointerOn,omitempty"`        // publish the rave.live/pointer instance link
	PointerGistID    string            `json:"pointerGistId,omitempty"`    // "" until first publish
	ConfigGistID     string            `json:"configGistId,omitempty"`     // rave.live/config module gist
	PerformersGistID string            `json:"performersGistId,omitempty"` // rave.live/performers module gist
	RosterGists      map[string]string `json:"rosterGists,omitempty"`      // editor-published roster slug -> gist id

	// Hosted per-group access (rave.live/access module; see .devnotes/HOSTED_ACCESS_CONTRACT.md).
	// The gist's `global` block is COMPOSED automatically = AccessRules + the union of AccessUsers
	// and every group's Users (authored once, never twice). Group secret codes live ONLY here
	// (plaintext, never published) - the gist carries the FNV-1a hash. Off by default.
	AccessOn     bool                `json:"accessOn,omitempty"`     // publish the rave.live/access module
	AccessRules  AccessRulesConfig   `json:"accessRules,omitempty"`  // global (default) rule toggles
	AccessUsers  []string            `json:"accessUsers,omitempty"`  // non-group global allow-list entries
	AccessGroups []AccessGroupConfig `json:"accessGroups,omitempty"` // code-selectable groups
	AccessGistID string              `json:"accessGistId,omitempty"` // "" until first publish (direct mode)

	// PublishMode selects the live-module publish path: "direct" (default; the user's own
	// gist-scoped token writes the gists) or "hosted" (rave.page's worldlive API creates them
	// under its service account - no gist token needed in-app). Empty = direct.
	PublishMode   string `json:"publishMode,omitempty"`
	HostedWorldID string `json:"hostedWorldId,omitempty"` // target VRChat world id (wrld_…) for hosted publishes

	// LiveModules is the mode-agnostic per-module published pointer (raw URL the world polls +
	// SEQ-GATE value + gist id), keyed by internal target key (pointer|config|performers). BOTH
	// modes write it so the editor bridge bakes URLs + the settings gateway advances seq without
	// knowing the mode. Direct mode also keeps *GistID above (gist identity for republish/heal);
	// hosted mode has only this (rave.page owns the gist).
	LiveModules map[string]LiveModulePub `json:"liveModules,omitempty"`
}

// WorldSync live-module publish modes (WorldSyncFeature.PublishMode).
const (
	WorldSyncModeDirect = "direct"
	WorldSyncModeHosted = "hosted"
)

// LiveModulePub is one live module's last published pointer (mode-agnostic). RawURL is the
// stable world-facing gist raw URL; Seq is the world's SEQ-GATE value; GistID is provenance.
type LiveModulePub struct {
	RawURL string `json:"rawUrl,omitempty"`
	Seq    int64  `json:"seq,omitempty"`
	GistID string `json:"gistId,omitempty"`
}

// ResolvedPublishMode returns the effective publish mode ("hosted" or "direct"; default direct).
func (w WorldSyncFeature) ResolvedPublishMode() string {
	if w.PublishMode == WorldSyncModeHosted {
		return WorldSyncModeHosted
	}
	return WorldSyncModeDirect
}

// ResolvedRefresh returns the refresh interval (default 10 min).
func (w WorldSyncFeature) ResolvedRefresh() time.Duration {
	if w.RefreshMins > 0 {
		return time.Duration(w.RefreshMins) * time.Minute
	}
	return 10 * time.Minute
}

// ResolvedNowPlayingEvery returns the min gap between now-playing writes (default 60 s).
func (w WorldSyncFeature) ResolvedNowPlayingEvery() time.Duration {
	if w.NowPlayingSecs > 0 {
		return time.Duration(w.NowPlayingSecs) * time.Second
	}
	return time.Minute
}

// PermList is one world permission list, published as one gist (allow.txt newline
// displayNames + allow.json envelope).
type PermList struct {
	ID      string      `json:"id"` // stable local id (list-<unix>)
	Name    string      `json:"name"`
	Entries []PermEntry `json:"entries,omitempty"`
	GistID  string      `json:"gistId,omitempty"` // "" until first publish
}

// PermEntry kinds.
const (
	PermEntryUser      = "user"
	PermEntryGroupRole = "groupRole"
)

// PermEntry grants one user or one group role (role "" = all group members).
// Group-role entries are expanded to current member displayNames at publish time -
// members of the chosen role become publicly listed in the gist.
type PermEntry struct {
	Kind      string `json:"kind"` // "user" | "groupRole"
	UserID    string `json:"userId,omitempty"`
	Display   string `json:"display,omitempty"` // displayName (user entries)
	GroupID   string `json:"groupId,omitempty"`
	GroupName string `json:"groupName,omitempty"`
	RoleID    string `json:"roleId,omitempty"` // "" = whole group
	RoleName  string `json:"roleName,omitempty"`
}

// WorldPoster is one billboard slot: image URL (VRC image-allowlisted host) + caption + link.
type WorldPoster struct {
	Img     string `json:"img,omitempty"`
	Caption string `json:"caption,omitempty"`
	Link    string `json:"link,omitempty"`
}

// FavoriteGroup pins a VRChat group in the World Sync UI.
type FavoriteGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AccessRulesConfig are the non-user grant toggles for an access scope (global or per-group).
// Mirrors matebridge.AccessRules (the wire shape) - kept a distinct config type so persisted
// config versions independently of the cross-repo contract.
type AccessRulesConfig struct {
	InstanceOwner bool `json:"instanceOwner,omitempty"`
	Master        bool `json:"master,omitempty"`
	Everyone      bool `json:"everyone,omitempty"`
}

// AccessGroupConfig is one code-selectable access group. Code is the PLAINTEXT secret typed on the
// in-world keypad - it lives ONLY in local config and is NEVER published; the gist carries the
// FNV-1a hash of it. Rules+Users are the group's own scope (replace global when active). ID +
// Instances are rave.page bookkeeping the world ignores (Instances stays empty until group-instance
// detection lands - a follow-up).
type AccessGroupConfig struct {
	ID        string            `json:"id,omitempty"`        // VRChat group id (grp_…), reference only
	Name      string            `json:"name"`                // display name
	Code      string            `json:"code"`                // PLAINTEXT secret code (local only; never published)
	Rules     AccessRulesConfig `json:"rules,omitempty"`     // group scope rule toggles
	Users     []string          `json:"users,omitempty"`     // group allow-list (display names)
	Instances []string          `json:"instances,omitempty"` // optional; rave.page bookkeeping
}

// TitlePreset is a reusable stream-title template with named {variables} the user fills in, so a DJ
// can keep one preset per genre/venue and just tweak the vars. Template uses {var} placeholders
// resolved from Vars (e.g. "{genre} set @ {club} - {event}"). GameName optionally sets the category.
type TitlePreset struct {
	Name     string            `json:"name"`
	Template string            `json:"template"`
	Vars     map[string]string `json:"vars,omitempty"`     // last-used variable values
	GameName string            `json:"gameName,omitempty"` // optional Twitch category to set alongside
}

// TraktorFeature configures the Traktor metadata bridge.
type TraktorFeature struct {
	Enabled        bool   `json:"enabled"`
	Port           int    `json:"port"`           // 0 = TraktorPort default
	LogPayloads    bool   `json:"logPayloads"`    // append raw payloads to jsonl
	MappingVersion string `json:"mappingVersion"` // pinned Traktor version for controller-mapping edits; "" = auto (newest)
}

// Port returns the configured Traktor port or the default.
func (t TraktorFeature) ResolvedPort() int {
	if t.Port > 0 {
		return t.Port
	}
	return TraktorPort
}

// WorkersFeature overrides worker-subprocess backends. ProbeExe points the "probe"
// worker type at an external executable speaking the same newline-JSON stdio protocol
// (the Zig rave-probe from native/zigcore, ZIG_MIGRATION P4). Empty / missing file =
// built-in `rave-mate worker probe`, zero behavior change. Additive at v34 (no bump -
// zero value = builtin).
type WorkersFeature struct {
	ProbeExe string `json:"probeExe,omitempty"`
}

// TranscodeFeature configures the transcode worker pool + user-defined presets.
type TranscodeFeature struct {
	Enabled       bool               `json:"enabled"`
	FfmpegPath    string             `json:"ffmpegPath"`        // "" = auto-detect on PATH
	MaxConcurrent int                `json:"maxConcurrent"`     // 0 = default (2)
	Presets       []transcode.Preset `json:"presets,omitempty"` // custom presets (override builtins by ID)
}

// PlayerFeature configures the in-app video player (mpv engine). Embed renders mpv INTO the app
// window (Windows only, via --wid + a child host window) instead of mpv's own popout window - the
// video sits inline above the transport/trim controls. VO/HWDec/Profile/ExtraArgs tune mpv for the
// embedded present path (defaults: gpu / auto-safe, no profile). Non-Windows or Embed=false keeps
// the popout window.
// UIFeature selects the UI renderer. Default (empty/absent) = the Go-driven HTML/CSS webview
// (rave.page design system, minimal JS); only an explicit "fyne" opts back into the legacy Fyne
// native renderer. The webview needs the OS WebView2 runtime (present on Win11) and a cgo Windows
// build; the seam falls back to Fyne at runtime if the runtime/host is unavailable. Fyne stays
// compiled-in as the fallback until the webview reaches full parity, then it is retired.
type UIFeature struct {
	Renderer string `json:"renderer,omitempty"` // ""|"webview" (default) | "fyne"
	Language string `json:"language,omitempty"` // i18n locale (e.g. "de"); ""=OS locale→en. See internal/i18n.
	// WebviewGPU is an ADVANCED escape hatch for WebView2 GPU compositing. Tri-state: unset/false
	// (default) = GPU compositing OFF (software compositing) so rave-mate's window never contends
	// with a live NVENC/GPU encoder (OBS); explicit true re-enables GPU acceleration for a
	// power user who prefers snappier UI over guaranteed non-interference. Safe-by-default is off.
	WebviewGPU *bool `json:"webviewGpu,omitempty"`
	// ShellImpl selects the window host (ZIG_MIGRATION B6). ""(DEFAULT)|"zig" = the Zig-owned
	// rave-shell exe under the B5 proc shell; "go" pins the in-process Go WebView2 window.
	// Default is zig because the rAF surfaces (graphs, the Ableton Link phrase bar) render and
	// update correctly there - measured on the dev rig 2026-07-27, where the same surfaces stalled
	// in the Go proc child. A missing rave-shell.exe degrades to the in-process Go window with a
	// loud log, so an install that never received the exe still gets the previous behaviour.
	ShellImpl string `json:"shellImpl,omitempty"`
}

// ZigShell reports the Zig window-child exe is wanted. Empty/absent = yes (the default); only an
// explicit "go"/"cgo" pins the in-process Go window. Resolution can still fall back when the exe
// is absent - wanting it is not having it (see webui.resolveZigShellExe).
func (u UIFeature) ZigShell() bool {
	switch strings.ToLower(strings.TrimSpace(u.ShellImpl)) {
	case "go", "cgo":
		return false
	}
	return true
}

// AllowWebviewGPU resolves the escape hatch: false by default (software compositing = low-impact),
// true only when the user explicitly opts back into GPU acceleration.
func (u UIFeature) AllowWebviewGPU() bool { return u.WebviewGPU != nil && *u.WebviewGPU }

// UseWebview reports whether the HTML/CSS webview renderer is selected. Empty/absent renderer =
// webview (the new default); only an explicit "fyne" chooses Fyne.
func (u UIFeature) UseWebview() bool {
	return !strings.EqualFold(strings.TrimSpace(u.Renderer), "fyne")
}

type PlayerFeature struct {
	Embed     bool     `json:"embed"`               // embed mpv into the app window (Windows); false = popout
	VO        string   `json:"vo"`                  // mpv --vo; "" = "gpu"
	HWDec     string   `json:"hwdec"`               // mpv --hwdec; "" = "auto-safe"
	Profile   string   `json:"profile"`             // optional mpv --profile (e.g. "fast"); "" = none
	ExtraArgs []string `json:"extraArgs,omitempty"` // power-user extra mpv flags
	GioWindow *bool    `json:"gioWindow,omitempty"` // player pop-out engine: nil/true = Gio (default), explicit false = legacy Fyne/mpv-popout
	// Volume is the GLOBAL playback gain (0..1) applied to every media surface (audio engine +
	// embedded video); nil = 1.0. Persisted so it survives restarts and view switches.
	Volume *float64 `json:"volume,omitempty"`
	// (audio decode is always the native internal/audio engine now - the legacy beep path + its
	// nativeDecode opt-in flag were retired; AAC/M4A fall through to ffmpeg on the same transport.)
}

// VolumeOr resolves the global playback gain (nil = full volume), clamped to [0,1].
func (p PlayerFeature) VolumeOr() float64 {
	if p.Volume == nil {
		return 1
	}
	v := *p.Volume
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// UseGioWindow resolves the tri-state pop-out engine: unset = Gio (default), explicit
// false = legacy. Callers still gate on platform (Gio aux windows unsupported on darwin).
func (p PlayerFeature) UseGioWindow() bool { return p.GioWindow == nil || *p.GioWindow }

// ResolvedVO returns the mpv video output (default "gpu").
func (p PlayerFeature) ResolvedVO() string {
	if strings.TrimSpace(p.VO) != "" {
		return p.VO
	}
	return "gpu"
}

// ResolvedHWDec returns the mpv hardware-decode mode (default "auto-safe").
func (p PlayerFeature) ResolvedHWDec() string {
	if strings.TrimSpace(p.HWDec) != "" {
		return p.HWDec
	}
	return "auto-safe"
}

// Default returns config with sensible defaults and a resolved API base. Traktor +
// stream + studio + notifications on; transcode on (auto-detects ffmpeg); VRChat + VR
// off (opt-in, need setup).
func Default() Config {
	return Config{
		Version:     configVersion,
		APIBaseURL:  resolveAPIBase(),
		StartHidden: false,
		Features: Features{
			Traktor:      TraktorFeature{Enabled: true, Port: 0, LogPayloads: true},
			StreamBridge: StreamBridgeFeature{Enabled: true}, // PauseLiveSignal=false ⇒ auto-broadcast on

			Transcode:     TranscodeFeature{Enabled: true, MaxConcurrent: 2},
			StudioChannel: Toggle{Enabled: true},
			OBS:           OBSFeature{Enabled: false, Host: "127.0.0.1", Port: 4455},
			Library:       Toggle{Enabled: true},
			MediaEditor:   Toggle{Enabled: true},
			Player:        PlayerFeature{Embed: true, VO: "gpu", HWDec: "auto-safe"}, // embed mpv in-window (Windows); gpu present path
			Fingerprint:   Toggle{Enabled: false},                                    // opt-in; needs fpcalc on PATH
			VRChat:        VRChatFeature{Enabled: false, RememberSession: true},
			VRCTools:      VRCToolsFeature{OrganizeByEvent: true, AutoBackupCamPaths: true, AutoRestoreCamPaths: true}, // event-match is the primary photo organize key; cam-path backup/restore default on for live-set crash-recovery
			VR:            Toggle{Enabled: false},
			VROverlay:     VROverlayFeature{Enabled: false, SummonOn: true, SummonButton: "ax"}, // opt-in; needs SteamVR + `vr` build. Summon = hold A/X to open editor (works OOTB).
			Twitch:        TwitchFeature{Enabled: false},                                        // opt-in; bundled client id
			WorldSync:     WorldSyncFeature{Enabled: false},                                     // opt-in; needs GitHub + VRChat links
			Notifications: Toggle{Enabled: true},

			NML:            NMLFeature{Enabled: true},                                       // auto-detect Traktor files
			MIDI:           MIDIFeature{Enabled: false},                                     // opt-in; needs a virtual MIDI port (loopMIDI)
			ProDJLink:      ProDJLinkFeature{Enabled: false},                                // opt-in; Pioneer CDJ/XDJ on the LAN
			Serato:         SeratoFeature{Enabled: false, NowPlaying: true},                 // opt-in; auto-detect _Serato_
			VirtualDJ:      VirtualDJFeature{Enabled: false, NetCtl: true, Tracklist: true}, // opt-in; auto-detect db
			Rekordbox:      RekordboxFeature{Enabled: false, DBPoll: true},                  // opt-in; live now-playing
			Recorder:       RecorderFeature{Enabled: true, ConfirmSeconds: 30},
			NowPlayingFile: FileSinkFeature{Enabled: false},   // opt-in; for OBS
			OverlayWeb:     OverlayWebFeature{Enabled: false}, // opt-in; browser overlay for OBS
			OverlayPNG:     FileSinkFeature{Enabled: false},   // opt-in; per-deck PNG cards for OBS
			OverlayOBS:     OverlayOBSFeature{Enabled: false}, // opt-in; obs-websocket renderer
			VideoShare:     VideoShareFeature{Enabled: false}, // opt-in; Spout/Syphon/PipeWire share

			OverlayWaveform: OverlayWaveformFeature{ // opt-in; appearance defaults = built-in look
				Enabled: false, ZoomSeconds: 20, PlayheadPct: 0.75,
				WaveColor: defWaveColor, WaveOpacity: defWaveOpacity, BgColor: defWaveBgColor, BgOpacity: defWaveBgOpac,
			},

			Peers:         PeersFeature{Enabled: false},         // opt-in; LAN peer link
			AccountBridge: AccountBridgeFeature{Enabled: false}, // opt-in; reaches this box from off-LAN
			FileXfer:      FileXferFeature{Enabled: false},      // opt-in; peer file transfer (ask before saving)
			SetCapture:    SetCaptureFeature{Enabled: false},    // opt-in; needs Traktor broadcast setup
			AudioRecord:   AudioRecordFeature{Enabled: false, Format: "flac", FollowOBS: true, WriteTags: true},

			LibrarySync: LibrarySyncFeature{Enabled: false}, // opt-in; cross-DJ-software sync

			GridFix: GridFixFeature{Enabled: false}, // opt-in; needs the Python beat engine installed

			AppGroups: AppGroupsFeature{Enabled: false}, // opt-in; relaunch app sets after a crash

			Timecode: TimecodeFeature{Enabled: false, Rate: "30"}, // opt-in; house SMPTE timecode outputs

			DMX: DMXFeature{Enabled: false, Grid: DMXGrid{Enabled: true, Mode: "mono"}}, // opt-in; grid sink on once the plane is

			DMXMIDI: DMXMIDIFeature{Enabled: false}, // opt-in; needs a virtual MIDI port + VRChat --midi

			RTSPServe: RTSPServeFeature{Enabled: false}, // opt-in; needs ffmpeg + a configured source

			Stream: StreamFeature{Enabled: false, Mode: "standard", ColorMode: "mono", Transport: "rtmp", Encoder: "x264", FPS: 30}, // opt-in; needs ffmpeg + a push URL

			AbletonLink: AbletonLinkFeature{ // opt-in; real Link backend needs the `abletonlink` cgo build
				Enabled: false, Quantum: 16, TempoOwner: "auto",
				Resolume: ResolumeConfig{Enabled: false, Host: "127.0.0.1", OSCPort: 7000, RESTPort: 8080},
			},

			MIDIController: MIDIControllerFeature{Channels: DefaultMIDIChannels}, // auto port (loopbe/first) until the user picks one
		},
	}
}

// resolveAPIBase: RAVE_API_BASE_URL override → prod iff RAVE_ENV=production → dev.
func resolveAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("RAVE_API_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if strings.EqualFold(os.Getenv("RAVE_ENV"), "production") {
		return prodAPI
	}
	return devAPI
}

// Dir is the OS-correct per-user config dir for the app, created if absent.
// RAVE_MATE_CONFIG_DIR overrides it (dev/test: isolated state beside a real instance;
// pair with RAVE_MATE_CTL_ADDR so the control socket is isolated too).
func Dir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("RAVE_MATE_CONFIG_DIR")); v != "" {
		if err := os.MkdirAll(v, 0o755); err != nil {
			return "", err
		}
		return v, nil
	}
	if testing.Testing() {
		// Tests must NEVER touch the real per-user config dir. 2026-07-26 incident: webui
		// test fixtures with a zero svc.Cfg exercised saveCfgBG, and every local
		// `go test ./internal/webui` overwrote the developer's REAL config.json with zeros
		// (the "settings wipe" bug). Any test not setting RAVE_MATE_CONFIG_DIR gets a
		// per-process throwaway dir instead.
		return testDir()
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, appDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

var (
	testDirOnce sync.Once
	testDirPath string
	testDirErr  error
)

// testDir lazily creates one throwaway config dir per test process.
func testDir() (string, error) {
	testDirOnce.Do(func() {
		testDirPath, testDirErr = os.MkdirTemp("", "rave-mate-test-cfg-")
	})
	return testDirPath, testDirErr
}

// DataPath joins name onto the app config dir.
func DataPath(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// VRMAvatarsDir is the managed dir holding VRM/GLB avatar models replicated across paired peers.
// Unlike motion recordings, avatars had no canonical home - picked files are imported here (ImportAvatar)
// so they can be advertised to + pulled by peers. Empty if the config dir can't be resolved.
func VRMAvatarsDir() string {
	p, err := DataPath("vr_avatars.x")
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "vr_avatars")
}

// ImportAvatar copies a picked avatar file into VRMAvatarsDir (so peers can replicate it) and returns
// the managed path. A file already in that dir is returned unchanged. Best-effort: the original path is
// returned alongside the error so callers can fall back to using it locally.
func ImportAvatar(src string) (string, error) {
	return importAvatarInto(VRMAvatarsDir(), src)
}

// importAvatarInto is ImportAvatar with an explicit destination dir (testable without the OS config dir).
func importAvatarInto(dir, src string) (string, error) {
	if dir == "" {
		return src, errors.New("no avatars dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return src, err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if filepath.Clean(filepath.Dir(src)) == filepath.Clean(dir) {
		return dst, nil // already managed
	}
	in, err := os.Open(src)
	if err != nil {
		return src, err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(dir, ".avatar-*.tmp")
	if err != nil {
		return src, err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return src, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return src, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return src, err
	}
	copySidecar(src, dst)
	return dst, nil
}

// copySidecar brings `<avatar>.physbones.json` (Unity-exported physbone params,
// consumed by vrmdyn) along with an imported avatar. Best-effort.
func copySidecar(src, dst string) {
	scSrc := strings.TrimSuffix(src, filepath.Ext(src)) + ".physbones.json"
	b, err := os.ReadFile(scSrc)
	if err != nil {
		return
	}
	scDst := strings.TrimSuffix(dst, filepath.Ext(dst)) + ".physbones.json"
	_ = os.WriteFile(scDst, b, 0o644)
}

// AvatarEntry is one avatar model in the managed avatars dir.
type AvatarEntry struct {
	Name string // base filename incl. extension
	Path string
	Size int64
}

// ListAvatars enumerates synced avatar models (*.vrm/*.glb/*.gltf/*.fbx) in VRMAvatarsDir, name-sorted.
func ListAvatars() []AvatarEntry { return listAvatarsIn(VRMAvatarsDir()) }

// listAvatarsIn is ListAvatars with an explicit dir (testable without the OS config dir).
func listAvatarsIn(dir string) []AvatarEntry {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AvatarEntry
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".vrm", ".glb", ".gltf", ".fbx":
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, AvatarEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name()), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load reads + migrates config from disk, falling back to Default for a missing/invalid
// file. Always returns a usable Config; err is non-nil only on unexpected IO failure.
func Load() (Config, error) {
	path, err := DataPath(fileName)
	if err != nil {
		return Default(), err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		// Corrupt file: preserve the evidence, then try the .bak from the last good
		// Save. Silently resetting to defaults (the old behavior) destroyed the
		// user's settings for good the moment anything saved.
		_ = os.Rename(path, path+fmt.Sprintf(".corrupt-%d", time.Now().Unix()))
		if bcfg, ok := loadBak(path); ok {
			return bcfg, fmt.Errorf("config was corrupt; recovered from .bak")
		}
		return Default(), fmt.Errorf("config was corrupt and no usable .bak; reset to defaults")
	}
	if isZeroBugFile(cfg, raw) {
		// 2026-07-26 zero-save bug artifact: a marshaled zero-value Config (version 0, every
		// feature off, empty apiBaseUrl) - valid JSON, so it silently loaded and the daemon
		// booted with everything disabled, then re-persisted the zeros. Treat like corruption:
		// preserve the evidence, recover the last good config from .bak.
		_ = os.Rename(path, path+fmt.Sprintf(".zero-%d", time.Now().Unix()))
		if bcfg, ok := loadBak(path); ok {
			return bcfg, fmt.Errorf("config was a zero-value artifact; recovered from .bak")
		}
		return Default(), fmt.Errorf("config was a zero-value artifact and no usable .bak; reset to defaults")
	}
	if cfg.Version < configVersion {
		migrate(&cfg, raw)
	}
	cfg.Features.MIDIController.normalize() // pre-v26 files have no channels key -> default
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = resolveAPIBase()
	}
	return cfg, nil
}

// loadBak reads + migrates path+".bak"; ok=false when absent/unparseable/itself a zero artifact.
func loadBak(path string) (Config, bool) {
	braw, err := os.ReadFile(path + ".bak")
	if err != nil {
		return Config{}, false
	}
	bcfg := Default()
	if json.Unmarshal(braw, &bcfg) != nil || isZeroBugFile(bcfg, braw) {
		return Config{}, false
	}
	if bcfg.Version < configVersion {
		migrate(&bcfg, braw)
	}
	bcfg.Features.MIDIController.normalize()
	if bcfg.APIBaseURL == "" {
		bcfg.APIBaseURL = resolveAPIBase()
	}
	return bcfg, true
}

// isZeroBugFile detects the zero-save artifact: version 0 with an explicit modern "features"
// object and an empty apiBaseUrl. A LEGIT pre-v1 legacy file also has version 0 but is flat
// (traktorEnable/traktorLog/notifyEnable at top level, no "features" key) - those must keep
// migrating, never be quarantined.
func isZeroBugFile(cfg Config, raw []byte) bool {
	if cfg.Version != 0 || cfg.APIBaseURL != "" {
		return false
	}
	var probe struct {
		Features      json.RawMessage `json:"features"`
		TraktorEnable *bool           `json:"traktorEnable"`
		TraktorLog    *bool           `json:"traktorLog"`
		NotifyEnable  *bool           `json:"notifyEnable"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	legacy := probe.TraktorEnable != nil || probe.TraktorLog != nil || probe.NotifyEnable != nil
	return len(probe.Features) > 0 && !legacy
}

// migrate upgrades a pre-v1 (flat) config to the feature schema. The old file had
// top-level traktorEnable/traktorLog/notifyEnable; map them onto Features and keep the
// other features at their defaults.
func migrate(cfg *Config, raw []byte) {
	var legacy struct {
		TraktorEnable *bool `json:"traktorEnable"`
		TraktorLog    *bool `json:"traktorLog"`
		NotifyEnable  *bool `json:"notifyEnable"`
	}
	_ = json.Unmarshal(raw, &legacy)
	if legacy.TraktorEnable != nil {
		cfg.Features.Traktor.Enabled = *legacy.TraktorEnable
	}
	if legacy.TraktorLog != nil {
		cfg.Features.Traktor.LogPayloads = *legacy.TraktorLog
	}
	if legacy.NotifyEnable != nil {
		cfg.Features.Notifications.Enabled = *legacy.NotifyEnable
	}
	cfg.Version = configVersion
}

// ErrZeroConfig is returned by Save when the receiver is a zero-value Config (Version 0):
// every legitimate Config passed through Default()/Load(), which stamp Version=configVersion.
// A zero save is ALWAYS a bug upstream - twice on 2026-07-26 a marshaled zero Config
// clobbered the user's config.json (the writer held a never-Loaded Config value).
var ErrZeroConfig = errors.New("refusing to save zero-value config (Version 0) - caller holds an unloaded Config")

// zeroSaveTripwire preserves the refused writer's identity: full goroutine stack to
// <configdir>/zero-config-save-<unix>.stack (best-effort). The zero-config writer has so far
// evaded static identification - the next fire names it with file:line.
func zeroSaveTripwire() {
	dir, err := Dir()
	if err != nil {
		return
	}
	p := filepath.Join(dir, fmt.Sprintf("zero-config-save-%d.stack", time.Now().Unix()))
	_ = os.WriteFile(p, debug.Stack(), 0o600)
}

// Save atomically + DURABLY writes config to disk. fsync before rename: without it a
// hard crash can commit the rename while the data blocks are lost, leaving a zeroed
// config (happened in production 2026-07-26). The previous config survives as .bak.
// A zero-value receiver is refused (ErrZeroConfig) - see zeroSaveTripwire.
func (c Config) Save() error {
	if c.Version == 0 {
		zeroSaveTripwire()
		return ErrZeroConfig
	}
	path, err := DataPath(fileName)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// Keep the last good config as .bak (best-effort; source for corrupt-file recovery).
	if _, serr := os.Stat(path); serr == nil {
		_ = os.Remove(path + ".bak")
		_ = os.Rename(path, path+".bak")
	}
	return os.Rename(tmp, path)
}
