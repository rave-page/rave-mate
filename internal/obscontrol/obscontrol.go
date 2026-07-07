// Package obscontrol exposes OBS stream/recording control + live status across linked rave-mate
// instances over the event bus. The instance running the OBS bridge polls its obs-websocket session
// and broadcasts a Status (streaming/recording, computed bitrate, congestion, dropped frames); any
// instance (e.g. a VR PC) can render that Status and send a directed Cmd to start/stop the stream or
// recording on a chosen instance. Mirrors the twitch.Manager bus pattern.
package obscontrol

// Bus topics + the OBS capability advertised by an instance with a live OBS connection.
const (
	TopicStatus = "obs.status" // broadcast: one instance's OBS state (per-tick while connected)
	TopicCmd    = "obs.cmd"    // broadcast w/ Target: start/stop stream or recording
	CapOBS      = "obs"
)

// Command actions (Cmd.Action).
const (
	ActStreamStart  = "stream.start"
	ActStreamStop   = "stream.stop"
	ActStreamToggle = "stream.toggle"
	ActRecordStart  = "record.start"
	ActRecordStop   = "record.stop"
	ActRecordToggle = "record.toggle"
	ActRecordPause  = "record.pause"
	ActMicToggle    = "mic.toggle" // toggle mute of the input named in Cmd.Arg
)

// Status is one OBS source's state (TopicStatus payload). One node may publish several (its local OBS
// plus any direct LAN remotes it connects to), each with a distinct ID.
type Status struct {
	ID           string  `json:"id"`           // stable source id (node id for a node's own OBS; obs@host:port for a direct remote)
	Label        string  `json:"label"`        // human label (hostname)
	Connected    bool    `json:"connected"`    // obs-websocket session up
	Streaming    bool    `json:"streaming"`    // stream output active
	Recording    bool    `json:"recording"`    // record output active
	Reconnecting bool    `json:"reconnecting"` // stream output mid-reconnect
	StreamSec    float64 `json:"streamSec"`    // stream elapsed seconds
	RecSec       float64 `json:"recSec"`       // record elapsed seconds
	BitrateKbps  int     `json:"bitrateKbps"`  // computed from OutputBytes delta
	Congestion   float64 `json:"congestion"`   // 0..1 network strain
	Skipped      int     `json:"skipped"`      // skipped (dropped) frames
	Total        int     `json:"total"`        // total output frames
}

// DropPct returns the skipped/total frame ratio as a percentage (0 when no frames yet).
func (s Status) DropPct() float64 {
	if s.Total <= 0 {
		return 0
	}
	return float64(s.Skipped) / float64(s.Total) * 100
}

// Cmd is a TopicCmd payload: an action targeted at one source (Target=source id) or any local OBS
// (Target=="").
type Cmd struct {
	Target string `json:"target,omitempty"`
	Action string `json:"action"`
	Arg    string `json:"arg,omitempty"` // action parameter (e.g. mic.toggle input name)
}

// Instance is a Status tagged with the node that published it + whether that's the local node. The
// source id (for targeting commands) is Status.ID.
type Instance struct {
	Node  string `json:"node"`
	Local bool   `json:"local"`
	Status
}

// Remote is an OBS instance reached directly over the LAN (no rave-mate on that PC).
type Remote struct {
	ID       string
	Label    string
	Host     string
	Port     int
	Password string
}
