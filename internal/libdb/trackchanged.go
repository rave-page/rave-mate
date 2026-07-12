package libdb

// TopicTrackChanged is the eventbus topic published after a track's cue data (cues /
// beatgrid / drops) mutates - by the local cue editor or a peer's library.writeCueData
// RPC. Subscribers re-read the track (TrackByPath + Drops) and patch their own view.
const TopicTrackChanged = "library.trackchanged"

// TrackChangedEvent is the TopicTrackChanged payload. Origin is an opaque publisher
// token so a UI can skip events it published itself (its state is already patched at
// the edit site).
type TrackChangedEvent struct {
	Path   string `json:"path"`
	Origin string `json:"origin,omitempty"`
}
