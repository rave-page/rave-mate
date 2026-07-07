// Package peerbridge bridges live DJ data between linked rave-mate instances over the
// peerlink data channel: it broadcasts this node's now-playing to connected peers and stores
// each peer's now-playing for the UI. MIDI bridging + remote control ride the same channel
// (midi.go, control.go). It taps the session Merger (the same live feed the stream publisher
// and now-playing file sink use), so it reflects the fused state across all sources.
package peerbridge

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/session"
)

const logTag = "peerbridge"

// resendInterval re-broadcasts the current now-playing so a peer that connects mid-track gets
// state without waiting for the next change.
const resendInterval = 10 * time.Second

// NowPlaying is the compact now-playing payload exchanged on ChanSession.
type NowPlaying struct {
	Playing bool    `json:"playing"`
	Deck    string  `json:"deck,omitempty"`
	Title   string  `json:"title,omitempty"`
	Artist  string  `json:"artist,omitempty"`
	Album   string  `json:"album,omitempty"`
	BPM     float64 `json:"bpm,omitempty"`
	Key     string  `json:"key,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`
	Length  float64 `json:"length,omitempty"`
	TS      int64   `json:"ts"` // sender unix-ms
}

// RemoteState is a peer's last-known now-playing, as seen by this node.
type RemoteState struct {
	NodeID     string
	NowPlaying NowPlaying
	UpdatedAt  time.Time
}

// linkManager is the peerlink surface the bridge uses (*peerlink.Manager; interface so tests
// can fake the link).
type linkManager interface {
	SetDataHandler(fn func(peerNodeID, channel string, payload []byte))
	SendTo(nodeID, channel string, payload []byte) error
	Broadcast(channel string, payload []byte)
}

// Bridge wires the peerlink data channel to the session hub. Zero value unusable; use New.
type Bridge struct {
	log *logbus.Bus
	mgr linkManager

	mu       sync.Mutex
	remote   map[string]RemoteState // peer node id → last now-playing
	onUpdate func()

	midi midiState // outbound MIDI-forward gate + control target

	// midi/control/bus sinks, set by their respective modules (nil = drop inbound).
	onMIDI    func(peerNodeID string, payload []byte)
	onControl func(peerNodeID string, payload []byte)
	onBus     func(peerNodeID string, payload []byte)
}

// New builds a bridge over the given peer manager.
func New(log *logbus.Bus, mgr linkManager) *Bridge {
	return &Bridge{log: log, mgr: mgr, remote: map[string]RemoteState{}}
}

// SetOnUpdate registers a UI refresh hook (fires when a remote peer's state changes).
func (b *Bridge) SetOnUpdate(fn func()) { b.mu.Lock(); b.onUpdate = fn; b.mu.Unlock() }

// SetMIDISink routes inbound bridged MIDI to fn (the MIDI bridge wires this).
func (b *Bridge) SetMIDISink(fn func(peerNodeID string, payload []byte)) {
	b.mu.Lock()
	b.onMIDI = fn
	b.mu.Unlock()
}

// SetControlSink routes inbound remote-control commands to fn.
func (b *Bridge) SetControlSink(fn func(peerNodeID string, payload []byte)) {
	b.mu.Lock()
	b.onControl = fn
	b.mu.Unlock()
}

// SetBusSink routes inbound ChanBus (eventbus) payloads to fn (the eventbus wires this).
func (b *Bridge) SetBusSink(fn func(peerNodeID string, payload []byte)) {
	b.mu.Lock()
	b.onBus = fn
	b.mu.Unlock()
}

// RemoteStates returns a snapshot of every known peer's now-playing.
func (b *Bridge) RemoteStates() []RemoteState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]RemoteState, 0, len(b.remote))
	for _, s := range b.remote {
		out = append(out, s)
	}
	return out
}

// Start runs the session bridge until ctx cancels: it registers the inbound data handler and
// broadcasts now-playing from the merger on change + a slow heartbeat. Call once when the
// peers module starts; pass the live merger.
func (b *Bridge) Start(ctx context.Context, m *session.Merger) {
	b.mgr.SetDataHandler(b.onData)
	b.log.Info(logTag, "session bridge started", nil)

	ch, unsub := m.Subscribe()
	defer unsub()
	ticker := time.NewTicker(resendInterval)
	defer ticker.Stop()

	var last []byte
	send := func(force bool) {
		np := nowPlayingFrom(m.Snapshot())
		raw, err := json.Marshal(np)
		if err != nil {
			return
		}
		if !force && string(raw) == string(last) {
			return
		}
		last = raw
		b.mgr.Broadcast(peerlink.ChanSession, raw)
	}
	send(true)
	for {
		select {
		case <-ctx.Done():
			b.mgr.SetDataHandler(nil)
			return
		case _, ok := <-ch:
			if !ok {
				b.mgr.SetDataHandler(nil)
				return
			}
			send(false)
		case <-ticker.C:
			send(true) // periodic resend so freshly-connected peers catch up
		}
	}
}

// onData routes an inbound data frame by sub-channel.
func (b *Bridge) onData(peerNodeID, channel string, payload []byte) {
	switch channel {
	case peerlink.ChanSession:
		var np NowPlaying
		if err := json.Unmarshal(payload, &np); err != nil {
			return
		}
		b.mu.Lock()
		b.remote[peerNodeID] = RemoteState{NodeID: peerNodeID, NowPlaying: np, UpdatedAt: time.Now()}
		cb := b.onUpdate
		b.mu.Unlock()
		if cb != nil {
			cb()
		}
	case peerlink.ChanMIDI:
		b.mu.Lock()
		fn := b.onMIDI
		b.mu.Unlock()
		if fn != nil {
			fn(peerNodeID, payload)
		}
	case peerlink.ChanControl:
		b.mu.Lock()
		fn := b.onControl
		b.mu.Unlock()
		if fn != nil {
			fn(peerNodeID, payload)
		}
	case peerlink.ChanBus:
		b.mu.Lock()
		fn := b.onBus
		b.mu.Unlock()
		if fn != nil {
			fn(peerNodeID, payload)
		}
	}
}

// nowPlayingFrom derives the compact payload from a merged snapshot (same fields the
// now-playing file sink writes).
func nowPlayingFrom(st session.UnifiedState) NowPlaying {
	np, ok := st.DeriveNowPlaying()
	out := NowPlaying{TS: time.Now().UnixMilli()}
	if !ok {
		return out
	}
	out.Playing = true
	out.Deck = np.Deck
	out.Title = session.StringField(np.Fields, session.FieldTitle)
	out.Artist = session.StringField(np.Fields, session.FieldArtist)
	out.Album = session.StringField(np.Fields, session.FieldAlbum)
	out.Key = session.StringField(np.Fields, session.FieldKey)
	out.BPM, _ = floatField(np.Fields, session.FieldBPM)
	out.Elapsed, _ = floatField(np.Fields, session.FieldElapsedTime)
	out.Length, _ = floatField(np.Fields, session.FieldTrackLength)
	if playing, ok := boolField(np.Fields, session.FieldIsPlaying); ok {
		out.Playing = playing
	}
	return out
}

func floatField(m map[string]session.FieldValue, f string) (float64, bool) {
	if fv, ok := m[f]; ok {
		switch n := fv.Value.(type) {
		case float64:
			return n, true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		}
	}
	return 0, false
}

func boolField(m map[string]session.FieldValue, f string) (bool, bool) {
	if fv, ok := m[f]; ok {
		if v, ok := fv.Value.(bool); ok {
			return v, true
		}
	}
	return false, false
}
