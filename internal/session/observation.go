// Package session aggregates live DJ-session data from many independent Sources
// (DJ software + controllers), fuses their fields by per-field source priority and
// freshness in the Merger, and fans the unified state out to Sinks (stream push, OBS
// files, the session recorder). Each source self-describes its capabilities so the UI
// can show coverage gaps. Canonical field names match Traktor's wire keys (see fields.go)
// so a Traktor-only setup is byte-identical on the rave.page ingest API.
package session

import "time"

// ScopeKind identifies the kind of entity an Observation is about.
type ScopeKind string

const (
	ScopeDeck       ScopeKind = "deck"       // ID = "A".."D"
	ScopeChannel    ScopeKind = "channel"    // ID = "1".."4"
	ScopeMaster     ScopeKind = "master"     // ID = ""
	ScopeNowPlaying ScopeKind = "nowPlaying" // ID = "" - derived audible track (recorder)
)

// Scope identifies which entity an observation/field belongs to.
// JSON tags = the featurehost wire format (subprocess sources emit Observations).
type Scope struct {
	Kind ScopeKind `json:"kind"`
	ID   string    `json:"id,omitempty"` // deck letter / channel number; "" for master/nowPlaying
}

// Key is a stable map key for a scope.
func (s Scope) Key() string {
	if s.ID == "" {
		return string(s.Kind)
	}
	return string(s.Kind) + ":" + s.ID
}

// splitKey reverses Scope.Key.
func splitKey(key string) (ScopeKind, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return ScopeKind(key[:i]), key[i+1:]
		}
	}
	return ScopeKind(key), ""
}

// Observation is a normalized state delta from a Source for one scope. All Fields share
// the observation's Source/Confidence/TS (a source equally trusts its own reading). A
// source that fuses sub-feeds of differing trust (e.g. MIDI title vs MIDI bpm) emits them
// under distinct Source IDs. Loaded marks a deck.loaded boundary: the Merger clears the
// scope's prior winners before applying Fields (full replacement, parity with Traktor).
// JSON roundtrip note: Fields int64 values decode as float64 - fine, every numeric
// consumer type-switches both, and unix-millis fit float64's 53-bit mantissa exactly.
type Observation struct {
	Source     string         `json:"source"`           // stable source ID (see priority.go)
	TS         time.Time      `json:"ts"`               // observation time; zero => merger stamps now
	Scope      Scope          `json:"scope"`            //
	Fields     map[string]any `json:"fields,omitempty"` // canonical field name → concrete value (string|float64|bool|int64)
	Confidence float64        `json:"confidence"`       // 0..1 self-rated confidence for these fields
	Loaded     bool           `json:"loaded,omitempty"` // deck.loaded boundary
}

// Update is a merged state change emitted to subscribers. State carries only the fields
// that won the merge for this scope. Type mirrors the rave.page ingest event vocabulary.
type Update struct {
	Type  string         `json:"type"` // "deck.loaded"|"deck.update"|"channel.update"|"master.clock"|"nowPlaying.update"
	Scope Scope          `json:"scope"`
	State map[string]any `json:"state,omitempty"` // accepted field changes
	TS    time.Time      `json:"ts"`
}

// updateType derives the ingest-compatible event type for a scope.
func updateType(s Scope, loaded bool) string {
	switch s.Kind {
	case ScopeDeck:
		if loaded {
			return "deck.loaded"
		}
		return "deck.update"
	case ScopeChannel:
		return "channel.update"
	case ScopeMaster:
		return "master.clock"
	default:
		return string(s.Kind) + ".update"
	}
}
