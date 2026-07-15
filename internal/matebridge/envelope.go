package matebridge

import (
	"encoding/json"
	"fmt"
)

// This file defines the RUNTIME gist envelope the WORLD downloads (VRCStringDownloader ->
// VRC.SDK3.Data.VRCJson). rave-mate is the SOLE writer (extends internal/vrcperm + internal/github).
// Every module gist shares the SAME top-level keys so the world's parser gates uniformly:
//
//	schema           string  "rave.live/<kind>@<major>" - reject on prefix/major mismatch
//	contractVersion  int     == ContractVersion
//	seq              int64   MONOTONIC per module; THE SEQ-GATE (world commits only if seq>committedSeq)
//	updatedAt        string  RFC3339 UTC (diagnostics only; seq is the gate, not the timestamp)
//
// TWO carriage forms, both with those keys:
//   - BUNDLE  (Envelope.Modules): small, low-cadence config fetched as ONE gist - pointer + config +
//     performersLive together. Good for the instance link + a booking's live roster.
//   - SINGLE-MODULE (Envelope.Module inlined by kind): one gist per module for INDEPENDENT cadence and
//     the out-of-order-completion fix (each RaveLiveModule is its own IUdonEventReceiver). Prefer this
//     for high-rate modules (captions, events).
//
// Numbers arrive to VRCJson as Double; config/user VALUES are written as JSON STRINGS by contract
// (dodges the Double->int ambiguity). Objects decode to DataDictionary; the world reads every field
// via TryGetValue(key, TokenType, out). Keep each gist KB-sized; split large data across module URLs.

// Gist schema tags (kind@major).
const (
	SchemaBundle     = "rave.live/bundle@1"
	SchemaPointer    = "rave.live/pointer@1"
	SchemaConfig     = "rave.live/config@1"
	SchemaUsers      = "rave.live/users@1"
	SchemaPerformers = "rave.live/performers@1"
	SchemaCaptions   = "rave.live/captions@1"
	SchemaEvents     = "rave.live/events@1"
	SchemaEmoji      = "rave.live/emoji@1"
	SchemaAccess     = "rave.live/access@1"
)

// Module keys inside a bundle Envelope.Modules map (also the single-module inline key).
const (
	ModulePointer    = "pointer"
	ModuleConfig     = "config"
	ModuleUsers      = "users"
	ModulePerformers = "performersLive"
	ModuleCaptions   = "captions"
	ModuleEvents     = "events"
	ModuleEmoji      = "emoji"
	ModuleAccess     = "access"
)

// Envelope is the common gist wrapper. Modules is present in a BUNDLE gist (each value is a module
// payload, opaque here). For a SINGLE-MODULE gist, Modules is nil and the payload object is inlined at
// the top level under its module key (parsed directly by that module's decoder against Schema).
type Envelope struct {
	Schema          string                     `json:"schema"`
	ContractVersion int                        `json:"contractVersion"`
	Seq             int64                      `json:"seq"`
	UpdatedAt       string                     `json:"updatedAt"`
	Modules         map[string]json.RawMessage `json:"modules,omitempty"`
}

// ── module payloads ────────────────────────────────────────────────────────────────────────────

// PointerModule resolves the world's active identity WITHOUT a readable instance/group id. Udon can
// only read display names, so rave-mate stamps InstanceOwnerName (the authorized account that opened
// the instance) + a ByOperator table; the world matches present display names to pick its config,
// disambiguating concurrent instances with ZERO sync. ConfigURL is the next gist to pull for the
// resolved group's settings. JoinInfo is DISPLAY-ONLY (Udon cannot join an instance).
type PointerModule struct {
	Default           string        `json:"default"` // fallback profileId when no operator present
	ByOperator        []OperatorRef `json:"byOperator"`
	ActiveGroupID     string        `json:"activeGroupId,omitempty"`   // provenance; world never reads a group id at runtime
	ActiveGroupName   string        `json:"activeGroupName,omitempty"` // display text only
	InstanceOwnerName string        `json:"instanceOwnerName,omitempty"`
	InstanceToken     string        `json:"instanceToken,omitempty"` // rave.page-assigned; matches a build-time-baked token for single-event worlds
	ConfigURL         string        `json:"configUrl,omitempty"`     // gist raw URL of the resolved config module
	JoinInfo          JoinInfo      `json:"joinInfo"`
}

// OperatorRef maps a present operator display name to a profile + priority. The world scans present
// display names and picks the highest-priority operator PRESENT (operator-presence resolution).
type OperatorRef struct {
	Operator string `json:"operator"` // VRChat display name (exact, case-sensitive)
	Profile  string `json:"profileId"`
	Priority int    `json:"priority"`
}

// JoinInfo is the off-world join affordance the world DISPLAYS (never actuates). DeepLink is a
// vrchat:// launch URL; WebLink an https://vrch.at/ page; Label the human caption.
type JoinInfo struct {
	DeepLink string `json:"deepLink,omitempty"`
	WebLink  string `json:"webLink,omitempty"`
	Label    string `json:"label,omitempty"`
}

// ConfigModule is the active group's typed settings. Values are dotted-key -> JSON-STRING (coerced on
// read in-world), NOT native numbers/bools.
type ConfigModule struct {
	Profiles []ConfigProfile `json:"profiles"`
}

// ConfigProfile is one selectable config profile.
type ConfigProfile struct {
	ID     string            `json:"id"`
	Label  string            `json:"label,omitempty"`
	Values map[string]string `json:"values"`
}

// UsersModule is per-user config keyed by VRChat DISPLAY NAME (the only cross-boundary identity Udon
// exposes; exact, case-sensitive; stale-on-rename is accepted for cosmetic config).
type UsersModule struct {
	Users []UserConfig `json:"users"`
}

// UserConfig is one user's config row.
type UserConfig struct {
	Name   string            `json:"name"` // VRChat display name
	Values map[string]string `json:"values"`
}

// PerformersLiveModule is the Twitch performer roster. rave-mate decides who is LIVE off-world (Twitch
// Helix, over internal/twitch) and writes this; the world's RaveLivePerformerPlayer picks the assigned
// or highest-priority live performer's StreamURL and hands it to a video player (owner/master single
// writer, >=5s rate-limited).
type PerformersLiveModule struct {
	Performers []Performer `json:"performers"`
}

// Performer is one performer entry. Key is the STABLE identity synced in-world (never a list index).
// AssignedPlayerIDs binds this performer to specific in-world video players; FallbackKey names the
// next performer to try when this one is offline.
type Performer struct {
	Key               string   `json:"key"`
	DisplayName       string   `json:"displayName"`
	TwitchLogin       string   `json:"twitchLogin,omitempty"`
	StreamURL         string   `json:"streamUrl,omitempty"` // sanctioned playback URL (VRChat video-allowlisted host)
	Live              bool     `json:"live"`
	Priority          int      `json:"priority"`
	AssignedPlayerIDs []string `json:"assignedPlayerIds,omitempty"`
	FallbackKey       string   `json:"fallbackKey,omitempty"`
}

// CaptionsModule is live STT. Deduped by per-line Seq; a single Final:false interim line is replaced
// in place until final. rave-mate publishes a short tail (~3-8 lines) for missed-poll catch-up.
type CaptionsModule struct {
	Speaker    string        `json:"speaker,omitempty"`
	Lang       string        `json:"lang,omitempty"`
	TTLSeconds int           `json:"ttlSeconds,omitempty"`
	Lines      []CaptionLine `json:"lines"`
}

// CaptionLine is one caption. Seq is monotonic within the module.
type CaptionLine struct {
	Seq   int64  `json:"seq"`
	T     string `json:"t,omitempty"` // RFC3339 UTC or elapsed marker
	Text  string `json:"text"`
	Final bool   `json:"final"`
}

// EventsModule is an append-only Twitch/rave.page event log. Deduped by monotonic Event.ID high-water;
// the world seeds max on join WITHOUT firing (no history replay). type=chat carries meta.emotes.
type EventsModule struct {
	WindowStart string      `json:"windowStart,omitempty"`
	Events      []LiveEvent `json:"events"`
}

// LiveEvent is one event row. Meta is a free-form per-type bag (opaque here; e.g. emotes[] for chat).
// Type is a MIRROR of the Go/TS source of truth: follow | sub | cheer | raid | chat | * .
type LiveEvent struct {
	ID   int64                      `json:"id"`
	TS   string                     `json:"ts,omitempty"`
	Type string                     `json:"type"`
	User string                     `json:"user,omitempty"`
	Meta map[string]json.RawMessage `json:"meta,omitempty"`
}

// EmojiModule maps a Twitch emote name to an index into the world's PRE-AUTHORED VRCUrl[] atlas
// (VRCUrl cannot be built at runtime, so only a pre-declared set is loadable).
type EmojiModule struct {
	Emotes []EmoteRef `json:"emotes"`
}

// EmoteRef is one name->index mapping.
type EmoteRef struct {
	Name     string `json:"name"`
	URLIndex int    `json:"urlIndex"`
}

// ── envelope serialization (rave-mate = sole writer; internal/vrcperm calls these) ─────────────

// commonKeys are the reserved top-level envelope keys; a module key must not collide with them.
var commonKeys = map[string]bool{"schema": true, "contractVersion": true, "seq": true, "updatedAt": true, "modules": true}

// MarshalSingle renders a SINGLE-MODULE gist: the common envelope keys plus the module payload
// inlined at the TOP LEVEL under moduleKey (payload embedded verbatim - the world reads it via
// TryGetValue(moduleKey, ...)). schema is the module's own "rave.live/<kind>@<major>". Keys emit
// sorted (map marshal); order is irrelevant to the world's key-lookup parse, but a duplicate key
// would hard-fail VRCJson, so a moduleKey colliding with a common key is rejected.
func MarshalSingle(schema string, seq int64, updatedAt, moduleKey string, payload json.RawMessage) ([]byte, error) {
	if commonKeys[moduleKey] {
		return nil, fmt.Errorf("matebridge: module key %q collides with a reserved envelope key", moduleKey)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("matebridge: empty payload for module %q", moduleKey)
	}
	schemaB, _ := json.Marshal(schema)
	cvB, _ := json.Marshal(ContractVersion)
	seqB, _ := json.Marshal(seq)
	uaB, _ := json.Marshal(updatedAt)
	obj := map[string]json.RawMessage{
		"schema":          schemaB,
		"contractVersion": cvB,
		"seq":             seqB,
		"updatedAt":       uaB,
		moduleKey:         payload,
	}
	return json.MarshalIndent(obj, "", "  ")
}

// MarshalBundle renders a BUNDLE gist (SchemaBundle): the common keys plus a modules map (each
// value a module payload). One seq gates the whole bundle - use it for co-versioned low-cadence
// config (pointer + config + performersLive together); prefer MarshalSingle for independent
// per-module cadence + the out-of-order-completion fix.
func MarshalBundle(seq int64, updatedAt string, modules map[string]json.RawMessage) ([]byte, error) {
	if len(modules) == 0 {
		return nil, fmt.Errorf("matebridge: empty bundle")
	}
	return json.MarshalIndent(Envelope{
		Schema:          SchemaBundle,
		ContractVersion: ContractVersion,
		Seq:             seq,
		UpdatedAt:       updatedAt,
		Modules:         modules,
	}, "", "  ")
}
