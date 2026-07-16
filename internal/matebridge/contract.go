// Package matebridge defines the WORLD-FACING integration contract between rave-mate and the
// VRChat world toolkit (page.rave.mate editor plugin + page.rave.live Udon runtime). It carries
// the wire types for TWO physical channels, split by lifetime + trust:
//
//  1. EDIT-TIME loopback RPC (this file + server.go) - the Unity EDITOR (C#, no rave.page login)
//     calls http://127.0.0.1:<port>/v1/ with a rave-mate-minted bearer token discovered from a
//     user-only local file. Friends/groups + id->name, preset round-trip, project-settings read,
//     rebuild-signal poll. 127.0.0.1 bind is the primary trust boundary; the token defends against
//     other local processes. This is NOT the account-bound Local Studio WS channel (internal/studio,
//     ECDH + /auth/me): the editor has no Zitadel session, so it reuses the loopback+local-secret
//     PRINCIPLE, not that account handshake. No VRChat/Twitch/GitHub credential ever crosses here.
//
//  2. RUNTIME gist envelope (envelope.go) - the WORLD pulls gist raw strings via VRCStringDownloader
//     (gist.githubusercontent.com is VRChat-allowlisted). rave-mate is the SOLE writer (extends the
//     existing internal/vrcperm + internal/github gist plumbing). Udon CANNOT read the instance/group
//     id, so identity is rave-mate-assigned and gist-written (the Pointer module).
//
// STATUS: wired. internal/app constructs the server (editorbridge.go) with adapters over
// vrchat.Manager (Directory), a file-backed preset store (internal/matepreset), config (Settings),
// and vrcperm.Service (RosterPublisher); the enveloped gist writer lives in internal/vrcperm and the
// SEQ-GATE counter in internal/gistseq. A gateway self-gates via the optional Availabler interface
// (server.go) so /v1/health + routes reflect LIVE readiness (signed-in session / GitHub link), not
// just wiring. A genuinely-absent capability is still nil => 501 + dropped from /health. Keep additive.
//
// Canonical contract doc: docs/WORLD_BRIDGE_CONTRACT.md (the shared source of truth both repos mirror;
// the world repo mirrors it at .devnotes/MATE_WORLD_CONTRACT.md). Change types here + that doc together.
package matebridge

import (
	"encoding/json"
	"errors"
)

// ErrBadRequest, when wrapped by a gateway error, maps that error to a 400 bad-request response;
// any other gateway error is a 502 upstream. Lets a gateway distinguish a caller-input fault
// (unknown preset kind, illegal id) from a genuine downstream (VRChat/GitHub) failure.
var ErrBadRequest = errors.New("bad request")

// ContractVersion is the world-bridge contract major. Bumped on any breaking wire change; echoed on
// every loopback response (body + X-Rave-Contract-Version header) and every gist envelope so a client
// refuses / warns on a major mismatch. Additive fields do NOT bump it.
const ContractVersion = 1

// PathPrefix is the loopback RPC path prefix. All editor-bridge routes live under it.
const PathPrefix = "/v1"

// PortRange is the editor-bridge loopback listen range (first free port wins), distinct from the
// Local Studio WS range (47615-47619), rave-mate ctl (47620) and peerlink LAN (47631-47635).
var PortRange = []int{47623, 47624, 47625, 47626, 47627}

// DiscoveryFile is the basename rave-mate writes under the OS config dir (config.Dir():
// <UserConfigDir>/rave-mate/, e.g. %APPDATA%/rave-mate on Windows). The editor reads {port, token}
// from it at connect. Written 0600 (best-effort; the parent dir is already per-user). The token is
// an opaque, per-process capability secret - NEVER the rave.page account token, NEVER a VRChat /
// Twitch / GitHub credential. Held in editor memory only; never persisted to EditorPrefs, never logged.
const DiscoveryFile = "editor-bridge.json"

// DiscoverySchema tags the discovery file so a stale/foreign file is rejected.
const DiscoverySchema = "rave.mate/editor-bridge@1"

// Discovery is the JSON rave-mate writes to DiscoveryFile and the editor reads to connect.
type Discovery struct {
	Schema          string `json:"schema"` // == DiscoverySchema
	Port            int    `json:"port"`
	Token           string `json:"token"` // opaque bearer capability; rotated per rave-mate process
	ContractVersion int    `json:"contractVersion"`
	PID             int    `json:"pid"`             // rave-mate process id (staleness check)
	RaveMateVersion string `json:"raveMateVersion"` // version.Version
}

// AuthHeader is the HTTP header carrying the bearer token: "Authorization: Bearer <token>".
const AuthHeader = "Authorization"

// ClientHeader identifies the calling editor build, e.g. "unity/0.1.0". Advisory (logged), not a gate.
const ClientHeader = "X-Rave-Client"

// ContractHeader echoes ContractVersion on every response.
const ContractHeader = "X-Rave-Contract-Version"

// ── RFC 7807 error (mirrors vrbooking's problem+json posture) ────────────────────────────────────

// ProblemContentType is the RFC 7807 media type used for every error body.
const ProblemContentType = "application/problem+json"

// Problem is an RFC 7807 error body. Type is a stable, dereferenceable-ish slug the client can switch
// on; Status mirrors the HTTP status; ContractVersion is echoed so the client can flag a mismatch even
// on the error path. 401 (unauthorized) is a DISTINCT graceful state for the editor - "rave-mate up,
// not authorized" - separate from connection-refused, and never fatal to the toolkit.
type Problem struct {
	Type            string `json:"type"`   // e.g. "about:blank" or "https://rave.page/problems/unauthorized"
	Title           string `json:"title"`  // short human summary
	Status          int    `json:"status"` // HTTP status code
	Detail          string `json:"detail,omitempty"`
	ContractVersion int    `json:"contractVersion"`
}

// Stable Problem.Type slugs (relative to the problems namespace).
const (
	ProblemUnauthorized   = "https://rave.page/problems/unauthorized"
	ProblemBadRequest     = "https://rave.page/problems/bad-request"
	ProblemNotFound       = "https://rave.page/problems/not-found"
	ProblemUpstream       = "https://rave.page/problems/upstream" // VRChat/GitHub call failed
	ProblemNotImplemented = "https://rave.page/problems/not-implemented"
	ProblemInternal       = "https://rave.page/problems/internal"
)

// ── health ───────────────────────────────────────────────────────────────────────────────────────

// Health is GET /v1/health: handshake + heartbeat. Capabilities advertises which optional feature
// families are live right now (vrchat requires a signed-in VRChat session, worldsync requires a linked
// GitHub) so the editor greys the right tools without guessing.
type Health struct {
	OK              bool     `json:"ok"`
	RaveMateVersion string   `json:"raveMateVersion"`
	ContractVersion int      `json:"contractVersion"`
	Capabilities    []string `json:"capabilities"`
}

// Capability slugs advertised in Health.Capabilities.
const (
	CapVRChat    = "vrchat"    // signed-in VRChat session: friends/groups/resolve usable
	CapWorldSync = "worldsync" // GitHub linked: publish-to-gist usable
	CapPresets   = "presets"   // preset round-trip available
	CapSettings  = "settings"  // project settings + rebuild signals available
)

// ── VRChat directory (mirror of internal/vrchat wire shapes; DERIVED, sanctioned output only) ──────

// Friend mirrors the sanctioned slice of a VRChat friend. Online is derived from Status (VRChat
// "active"/"join me"/"ask me" => online; "offline"/"" => offline) so the editor need not know VRChat's
// status vocabulary. No id ever reaches the world runtime - ids are editor-side PROVENANCE only.
type Friend struct {
	ID          string `json:"id"` // usr_... - kept editor-side for re-resolve; never written to the world
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	Online      bool   `json:"online"`
}

// FriendsResponse wraps GET /v1/vrchat/friends.
type FriendsResponse struct {
	ContractVersion int      `json:"contractVersion"`
	Friends         []Friend `json:"friends"`
}

// Group mirrors the sanctioned slice of a VRChat group (search result / membership). ID is the grp_
// id regardless of endpoint shape (vrchat.Group.EffectiveID).
type Group struct {
	ID          string `json:"id"` // grp_...
	Name        string `json:"name"`
	ShortCode   string `json:"shortCode,omitempty"`
	MemberCount int    `json:"memberCount"`
}

// GroupsResponse wraps GET /v1/vrchat/groups.
type GroupsResponse struct {
	ContractVersion int     `json:"contractVersion"`
	Groups          []Group `json:"groups"`
}

// GroupMember is one member row flattened from vrchat.GroupMember (displayName lifted out of the
// nested user object). Group membership is materialized to a display-name ROSTER at author time because
// the world can never test membership live.
type GroupMember struct {
	ID          string   `json:"id"` // userId
	DisplayName string   `json:"displayName"`
	RoleIDs     []string `json:"roleIds,omitempty"`
}

// GroupMembersResponse wraps GET /v1/vrchat/groups/{groupId}/members. Partial reports whether the
// expansion was best-effort (private group / hidden members / pagination cut) so the editor can warn
// instead of silently under-listing.
type GroupMembersResponse struct {
	ContractVersion int           `json:"contractVersion"`
	Members         []GroupMember `json:"members"`
	Partial         bool          `json:"partial,omitempty"`
}

// ResolveRequest is POST /v1/vrchat/resolve: id -> current display name (names drift, ids are stable).
type ResolveRequest struct {
	IDs []string `json:"ids"` // usr_... and/or grp_...
}

// Resolved is one id->name mapping. Kind is "user" | "group". DisplayName is "" when unresolvable
// (deleted / not visible); the caller keeps the id for a later retry.
type Resolved struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
}

// ResolveResponse wraps POST /v1/vrchat/resolve.
type ResolveResponse struct {
	ContractVersion int        `json:"contractVersion"`
	Resolved        []Resolved `json:"resolved"`
}

// ── project settings + rebuild signals (rave-mate edits while Unity is CLOSED) ─────────────────────

// Settings is GET /v1/settings/{projectId}: config rave-mate changed while Unity was shut. Seq is
// monotonic; the editor compares it against a persisted last-seen value on domain-load / focus-gain.
// ModuleURLs are the gist raw URLs to stamp onto RaveLiveModule behaviours; ConfigValues are flat
// string settings; RebuildScopes name what needs re-baking.
type Settings struct {
	ContractVersion int               `json:"contractVersion"`
	Seq             int64             `json:"seq"`
	UpdatedAt       string            `json:"updatedAt"` // RFC3339 UTC
	ModuleURLs      []string          `json:"moduleUrls"`
	ConfigValues    map[string]string `json:"configValues"`
	RebuildScopes   []string          `json:"rebuildScopes"`
}

// RebuildSignal is one "this needs re-baking" row. Scope is the subsystem (e.g. "parallax-backdrop",
// "live-module", "dmx-map"); ObjectName names the concrete object; Reason is human text for the banner.
type RebuildSignal struct {
	Seq        int64  `json:"seq"`
	Scope      string `json:"scope"`
	ObjectName string `json:"objectName"`
	Reason     string `json:"reason"`
}

// RebuildSignalsResponse wraps GET /v1/rebuild-signals?sinceSeq=N (a PLAIN interval/on-focus poll,
// never a long-poll - a held-open request would stall the editor's single shared HttpClient).
type RebuildSignalsResponse struct {
	ContractVersion int             `json:"contractVersion"`
	Seq             int64           `json:"seq"` // high-water; pass back as sinceSeq
	Signals         []RebuildSignal `json:"signals"`
}

// ── preset round-trip (payload = the world's existing per-module DTO, VERBATIM + opaque here) ──────

// Preset kinds. Each is a discrete unit so standalone assets round-trip independently.
const (
	PresetBackdrop    = "backdrop"    // BackdropTemplates.TemplateData
	PresetFoliage     = "foliage"     // FoliageTemplateShare.TemplateDto
	PresetStageRig    = "stageRig"    // RaveStageRigPresets.RaveRigPreset
	PresetCameraPath  = "cameraPath"  // lone RaveStagePath waypoints
	PresetDMXMap      = "dmxMap"      // lone RaveStageDMXMap channels
	PresetFixtureType = "fixtureType" // RaveFixtureType
)

// PresetSchema tags a preset envelope.
const PresetSchema = "rave.preset"

// AssetRef pins a referenced asset by GUID with a name fallback so renames survive.
type AssetRef struct {
	Name string `json:"name"`
	GUID string `json:"guid,omitempty"`
}

// PresetEnvelope is one versioned preset. Payload is the world's DTO VERBATIM - opaque to Go
// (json.RawMessage), interpreted only by RavePresetCodec on the editor side. CoordSpace disambiguates
// path serialization ("world" | "directorLocal"); Seq is provenance/ordering AND the runtime gist
// SEQ-GATE value when this preset is republished to a gist.
type PresetEnvelope struct {
	Schema          string          `json:"schema"` // == PresetSchema
	ContractVersion int             `json:"contractVersion"`
	Kind            string          `json:"kind"`
	ID              string          `json:"id"`   // stable slug/guid
	Name            string          `json:"name"` // human name
	UpdatedUTC      string          `json:"updatedUtc"`
	Source          string          `json:"source"` // "unity" | "rave-mate"
	Seq             int64           `json:"seq"`
	CoordSpace      string          `json:"coordSpace,omitempty"` // "world" | "directorLocal"
	AssetRefs       []AssetRef      `json:"assetRefs,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// PresetListResponse wraps GET /v1/presets?kind=&sinceSeq=.
type PresetListResponse struct {
	ContractVersion int              `json:"contractVersion"`
	Presets         []PresetEnvelope `json:"presets"`
}

// PresetPutResponse wraps PUT /v1/presets/{kind}/{id}: accepts a Unity-authored preset, returns the
// assigned monotonic seq.
type PresetPutResponse struct {
	ContractVersion int   `json:"contractVersion"`
	OK              bool  `json:"ok"`
	Seq             int64 `json:"seq"`
}

// ── publish-to-gist (editor hands a resolved roster to rave-mate, which owns the GitHub token) ─────

// PublishRosterRequest is POST /v1/worldsync/gist: the editor hands rave-mate a resolved display-name
// roster (e.g. an event lineup) to publish as a gist for a page.rave.access remoteListUrls entry. The
// editor NEVER holds the GitHub token - rave-mate writes the gist (reusing internal/vrcperm formats).
type PublishRosterRequest struct {
	Kind  string   `json:"kind"`  // "perm" (page.rave.access allow-list) for v1
	Name  string   `json:"name"`  // list label
	Names []string `json:"names"` // resolved VRChat display names (exact, case-sensitive)
}

// PublishRosterResponse returns the world-facing gist URLs + the assigned seq. RawURL is the
// gist.githubusercontent.com latest-revision URL (CDN-cached ~5 min) the world polls; JSONURL is the
// {list,users[]} envelope variant.
type PublishRosterResponse struct {
	ContractVersion int    `json:"contractVersion"`
	GistID          string `json:"gistId"`
	RawURL          string `json:"rawUrl"`
	JSONURL         string `json:"jsonUrl"`
	Seq             int64  `json:"seq"`
}
