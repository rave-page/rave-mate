# World Bridge Contract - rave-mate ↔ VRChat world

Canonical, versioned contract between **rave-mate** (this repo) and the **VRChat world toolkit**
(`page.rave.mate` Unity editor plugin + `page.rave.live` Udon runtime). Both sides MIRROR this file;
the world repo keeps a copy at `.devnotes/MATE_WORLD_CONTRACT.md`. Go source of truth for every shape:
`internal/matebridge/` (`contract.go`, `envelope.go`). Change a shape here + there together.

`contractVersion = 1`. Bumped only on a BREAKING wire change; echoed on every loopback response (body +
`X-Rave-Contract-Version`) and every gist envelope. Additive fields never bump it. A client refuses /
warns on a MAJOR mismatch.

## Trust model (enforced by architecture, not policy)

Two physical channels, split by lifetime + trust. rave-mate holds every credential (VRChat session,
Twitch token, GitHub token) on the authorized machine. Neither channel ever hands a credential to the
editor or the world.

```
1. EDIT-TIME   Unity editor C# ──127.0.0.1 loopback HTTP/JSON──► rave-mate   (authenticated, ephemeral)
2. RUNTIME     world ◄──VRCStringDownloader PULL── gist.githubusercontent.com  (rave-mate = sole writer)
```

The editor has NO rave.page login, so channel 1 is NOT the account-bound Local Studio WS channel
(`internal/studio`: ECDH P-256 + `/auth/me` mutual identity + per-frame HMAC). It reuses that channel's
**principle** - loopback bind + a local secret - not its account handshake:

- `127.0.0.1`-only bind is the primary trust boundary.
- A rave-mate-minted **bearer capability token** (opaque, rotated per rave-mate process) is the app-layer
  guard against other local processes. It is NEVER the rave.page account token and NEVER a VRChat /
  Twitch / GitHub credential.

Only DERIVED / sanctioned results cross either boundary: a display-name list, a stream URL, an active-config
pointer. Raw session / identity / auth data never does. `page.rave.mate` introduces ZERO VRChat / Twitch /
GitHub API surface - it is a sanctioned CLIENT of rave-mate.

---

## Channel 1 - Edit-time loopback RPC

**Transport.** HTTP/JSON, `http://127.0.0.1:<port>/v1/`. Stateless request/response. The editor uses one
static `HttpClient` (Timeout 15 s), `ConfigureAwait(false)` on every await, marshals completion back to
the main thread before touching any Unity API. Rebuild-signal checks are a PLAIN interval / on-focus poll
- never a long-poll (a held-open request stalls the single shared client).

**Port.** First free of `47623, 47624, 47625, 47626, 47627` (distinct from Local Studio WS 47615-19,
rave-mate ctl 47620, peerlink LAN 47631-35).

**Discovery + auth.** rave-mate writes a handshake file at `<UserConfigDir>/rave-mate/editor-bridge.json`
(Windows `%APPDATA%\rave-mate\editor-bridge.json`; macOS `~/Library/Application Support/rave-mate/`;
Linux `~/.config/rave-mate/`), mode `0600`, removed on shutdown:

```jsonc
{
  "schema": "rave.mate/editor-bridge@1",
  "port": 47623,
  "token": "<opaque hex capability>",
  "contractVersion": 1,
  "pid": 12345,
  "raveMateVersion": "development-abc1234"
}
```

The editor reads `{port, token}` at connect, holds the token IN MEMORY ONLY (never EditorPrefs, never
committed, never logged), and sends it on every request:

```
Authorization: Bearer <token>
X-Rave-Client: unity/<pkgVersion>     (advisory, logged)
Accept: application/json
```

Every response carries `X-Rave-Contract-Version: 1`.

**Offline is the DEFAULT expected state**, never an error. Connection-refused / 401 / timeout /
malformed-JSON all collapse to one editor status line. `401` is a distinct-but-graceful state ("rave-mate
up, not authorized - reopen the editor bridge"), separate from refused but equally non-fatal. `501` on a
route family means that feature isn't wired/enabled on this rave-mate (grey the tool). The toolkit builds
and ships unchanged with rave-mate absent.

**Error shape (RFC 7807, mirrors vrbooking).** Media type `application/problem+json`:

```jsonc
{ "type": "https://rave.page/problems/unauthorized", "title": "unauthorized",
  "status": 401, "detail": "...", "contractVersion": 1 }
```

`type` slugs: `unauthorized` (401) · `bad-request` (400) · `not-found` (404) · `upstream` (502, a VRChat/
GitHub call failed - detail is deliberately generic, never leaks an upstream token) · `not-implemented`
(501) · `internal` (500).

### Routes (`/v1`)

| Method + path | Request | Response |
|---|---|---|
| `GET /v1/health` | - | `Health` |
| `GET /v1/vrchat/friends?offset=&n=&offline=` | - | `FriendsResponse` |
| `GET /v1/vrchat/groups` | - | `GroupsResponse` |
| `GET /v1/vrchat/groups/{groupId}/members?roleId=&offset=&n=` | - | `GroupMembersResponse` |
| `POST /v1/vrchat/resolve` | `ResolveRequest` | `ResolveResponse` |
| `GET /v1/presets?kind=&sinceSeq=` | - | `PresetListResponse` |
| `GET /v1/presets/{kind}/{id}` | - | `PresetEnvelope` |
| `PUT /v1/presets/{kind}/{id}` | `PresetEnvelope` | `PresetPutResponse` |
| `GET /v1/settings/{projectId}` | - | `Settings` |
| `GET /v1/rebuild-signals?sinceSeq=` | - | `RebuildSignalsResponse` |
| `POST /v1/worldsync/gist` | `PublishRosterRequest` | `PublishRosterResponse` |

```jsonc
// Health - handshake + heartbeat. capabilities gates which editor tools light up.
{ "ok": true, "raveMateVersion": "development-abc1234", "contractVersion": 1,
  "capabilities": ["vrchat","worldsync","presets","settings"] }

// FriendsResponse. id is editor-side PROVENANCE only - it is NEVER written to the world runtime.
// online is DERIVED from VRChat status so the editor needn't know VRChat's status vocabulary.
{ "contractVersion": 1, "friends": [
  { "id": "usr_…", "displayName": "DJ Nyx", "status": "active", "online": true } ] }

// GroupsResponse. id is the grp_ id regardless of endpoint shape.
{ "contractVersion": 1, "groups": [
  { "id": "grp_…", "name": "Rave Collective", "shortCode": "RAVE", "memberCount": 812 } ] }

// GroupMembersResponse. Materialized to a display-name ROSTER at author time (the world can never test
// membership live). partial=true when best-effort (private group / hidden members / pagination cut).
{ "contractVersion": 1, "partial": false, "members": [
  { "id": "usr_…", "displayName": "DJ Nyx", "roleIds": ["role_…"] } ] }

// ResolveRequest / ResolveResponse. id -> CURRENT display name (names drift, ids are stable). "" name =
// unresolvable; keep the id and retry later. kind ∈ user|group.
{ "ids": ["usr_…","grp_…"] }
{ "contractVersion": 1, "resolved": [ { "id": "usr_…", "displayName": "DJ Nyx", "kind": "user" } ] }

// Settings - config rave-mate changed while Unity was CLOSED. seq monotonic; the editor compares it to a
// persisted last-seen on domain-load / focus-gain. moduleUrls -> stamp onto RaveLiveModule behaviours.
{ "contractVersion": 1, "seq": 7, "updatedAt": "2026-07-14T00:00:00Z",
  "moduleUrls": ["https://gist.githubusercontent.com/…/raw/pointer.json"],
  "configValues": { "venue": "Warehouse 9" }, "rebuildScopes": ["parallax-backdrop"] }

// RebuildSignalsResponse - plain poll. Pass seq back as sinceSeq. Each signal names what to re-bake.
{ "contractVersion": 1, "seq": 12, "signals": [
  { "seq": 12, "scope": "parallax-backdrop", "objectName": "Skyline", "reason": "layer inputs changed" } ] }

// PublishRosterRequest / PublishRosterResponse - editor hands rave-mate a resolved roster; rave-mate
// (owns the GitHub token) publishes a gist via internal/vrcperm and returns the world-facing URLs.
{ "kind": "perm", "name": "Warehouse 9 lineup", "names": ["DJ Nyx","VJ Kilo"] }
{ "contractVersion": 1, "gistId": "abc", "rawUrl": "https://gist.githubusercontent.com/…/raw/allow.txt",
  "jsonUrl": "https://gist.githubusercontent.com/…/raw/allow.json", "seq": 1 }
```

**Preset round-trip.** `payload` is the world's existing per-module DTO VERBATIM - opaque to Go
(`json.RawMessage`), interpreted only by `RavePresetCodec` on the editor side. Kinds are discrete units:
`backdrop` · `foliage` · `stageRig` · `cameraPath` · `dmxMap` · `fixtureType`.

```jsonc
{ "schema": "rave.preset", "contractVersion": 1, "kind": "backdrop",
  "id": "skyline", "name": "Skyline", "updatedUtc": "2026-07-14T00:00:00Z", "source": "unity",
  "seq": 3,                      // provenance/ordering; ALSO the runtime SEQ-GATE if republished to a gist
  "coordSpace": "world",         // "world" | "directorLocal" (path serialization)
  "assetRefs": [ { "name": "Skyline", "guid": "<optional>" } ],   // GUID + name-fallback so renames survive
  "payload": { /* TemplateData | TemplateDto | RaveRigPreset | RaveFixtureType | RigPath | RigMap */ } }
```

JsonUtility conventions Go honors in `payload`: `Color`→`{r,g,b,a}` 0..1; `Vector3`→`{x,y,z}`;
`Vector2`→`{x,y}`; enums as INT; NO Dictionary; NO polymorphism; null strings→`""`.

---

## Channel 2 - Runtime gist envelope (world pulls)

rave-mate is the SOLE writer (extends `internal/vrcperm` + `internal/github`). Worlds poll gist raw URLs
via `VRCStringDownloader` (`gist.githubusercontent.com` is VRChat string-allowlisted;
`raw.githubusercontent.com` and `api.rave.page` are NOT - always default to gist raw). Latest-revision
gist raw URLs are CDN-cached ~5 min, so "live" latency = `max(pollInterval, ~5 min CDN TTL)`. Per-player
download budget: 1 string / 5 s - the world enforces a `>=5s` global floor across all modules.

### Common envelope (every module gist)

```jsonc
{ "schema": "rave.live/<kind>@<major>",  // reject on prefix/major mismatch, keep last-good
  "contractVersion": 1,
  "seq": 42,                             // MONOTONIC per module; THE SEQ-GATE (commit only if seq>committedSeq)
  "updatedAt": "2026-07-14T00:00:00Z",   // diagnostics only; seq is the gate, not the timestamp
  "modules": { … } }                     // BUNDLE form only (see below)
```

**SEQ-GATE is a hard rave-mate obligation:** `seq` MUST strictly increase on every write of a given
module, or the world ignores fresh writes (seq not advanced) or accepts stale ones (seq reused). One
monotonic counter per module, persisted across restarts.

Two carriage forms, same top-level keys:

- **BUNDLE** (`modules` map present): small, low-cadence config as ONE gist - `pointer` + `config` +
  `performersLive` together. Each map value is the module payload below.
- **SINGLE-MODULE** (`modules` absent, payload inlined at top level under its kind key): one gist per
  module for INDEPENDENT cadence and the out-of-order-completion fix (each `RaveLiveModule` is its own
  `IUdonEventReceiver`). Prefer for high-rate modules (`captions`, `events`).

Numbers reach VRCJson as `Double`; config/user VALUES are written as JSON STRINGS (dodges the Double→int
ambiguity). Objects decode to `DataDictionary`; the world reads every field via
`TryGetValue(key, TokenType, out)`. No duplicate keys (hard-fails the whole VRCJson parse). Keep each gist
KB-sized. rave-mate blocklist-scrubs all UGC (STT, chat) BEFORE the gist write.

### Module payloads (`internal/matebridge/envelope.go`)

```jsonc
// pointer  (rave.live/pointer@1) - see "Instance/group-id pointer" below
{ "default": "main",
  "byOperator": [ { "operator": "DJ Nyx", "profileId": "nyx-set", "priority": 10 } ],
  "activeGroupId": "grp_…", "activeGroupName": "Rave Collective",   // provenance/display only
  "instanceOwnerName": "DJ Nyx",           // runtime correlation key (Udon can't read instance/group id)
  "instanceToken": "wh9-fri",              // rave.page-assigned; matches a build-time-baked token (optional)
  "configUrl": "https://gist.githubusercontent.com/…/raw/config.json",
  "joinInfo": { "deepLink": "vrchat://launch?ref=…", "webLink": "https://vrch.at/…", "label": "Join the set" } }

// config  (rave.live/config@1) - values are dotted-key -> JSON-STRING, coerced on read
{ "profiles": [ { "id": "nyx-set", "label": "Nyx", "values": { "fog.density": "0.4" } } ] }

// users  (rave.live/users@1) - keyed by VRChat DISPLAY NAME (exact, case-sensitive)
{ "users": [ { "name": "DJ Nyx", "values": { "camera.mode": "orbit" } } ] }

// performersLive  (rave.live/performers@1) - see "Twitch performer payload" below
{ "performers": [ { "key": "nyx", "displayName": "DJ Nyx", "twitchLogin": "djnyx",
  "streamUrl": "https://…", "live": true, "priority": 10,
  "assignedPlayerIds": ["mainStage"], "fallbackKey": "kilo" } ] }

// captions  (rave.live/captions@1) - dedup by per-line seq; one final:false interim replaced in place
{ "speaker": "DJ Nyx", "lang": "en", "ttlSeconds": 4,
  "lines": [ { "seq": 108, "t": "2026-07-14T00:00:00Z", "text": "welcome to the warehouse", "final": true } ] }

// events  (rave.live/events@1) - append-only; dedup by monotonic id high-water; seed max on join, no replay
{ "windowStart": "2026-07-14T00:00:00Z",
  "events": [ { "id": 5501, "ts": "…", "type": "follow", "user": "raver42", "meta": {} } ] }
//   type ∈ follow | sub | cheer | raid | chat | *  (MIRROR of the Go/TS source of truth); chat carries meta.emotes

// emoji  (rave.live/emoji@1) - name -> index into the world's PRE-AUTHORED VRCUrl[] atlas (VRCUrl not runtime-built)
{ "emotes": [ { "name": "PogChamp", "urlIndex": 0 } ] }
```

---

## Instance/group-id pointer mechanism

**Constraint (verified):** Udon exposes NO instance/group id, world id, or instance number; only display
names are runtime-readable, and `isInstanceOwner` is unreliable in group instances. So the world's
identity is resolved OUTSIDE the world and gist-written.

**DISCOVER.** rave-mate knows which authorized VRChat account is signed in
(`vrchat.Manager.CurrentUser().DisplayName`) and, from `internal/vrcloc`, the current world/instance/group
that account is in. It stamps `instanceOwnerName` (the account that opened the instance) into the pointer.
For a dedicated single-event world, rave.page also assigns an `instanceToken` that the world builder bakes
in at author time (via `/v1/settings`). At runtime the world reads local + master `VRCPlayerApi.displayName`
and matches `instanceOwnerName` (or any present name in `byOperator[]`) to confirm it is the linked
instance. This is per-client deterministic and identical across clients ⇒ ZERO sync; concurrent instances
disambiguate by which operator is present (operator-presence resolution). Residual accepted ceiling: two
instances with no operator present + no in-world controller both fall back to `pointer.default`.

**LINK.** rave-mate / rave.page assign the active VRChat group + instance and publish the pointer gist.
`activeGroupId` is provenance only - the world never reads a group id at runtime.

**JOIN.** Udon CANNOT join an instance. "Join the linked instance" resolves to an OFF-WORLD deep link in
`joinInfo` (`vrchat://` / `https://vrch.at/…`), surfaced via rave.page / rave-mate or shown as info text
in-world. The world only DISPLAYS the affordance, never actuates it.

The pointer gist raw URL is stamped into the world at author time (`RaveLiveModule.urls`, via `/v1/settings`
`moduleUrls`). It MUST be a `gist.githubusercontent.com` URL (`api.rave.page` is not VRChat-allowlisted).

---

## Twitch performer payload

rave-mate decides who is LIVE off-world (Twitch Helix, `internal/twitch` - `Helix.GetStream` / EventSub),
scrubs it, and writes the `performersLive` module. The world's `RaveLivePerformerPlayer`:

1. Filters to `live: true`.
2. For each in-world video player (matched via `assignedPlayerIds`) picks the assigned performer, else the
   highest-`priority` live performer, else follows `fallbackKey`, else a static idle URL.
3. Hands the resolved `streamUrl` to a real video player. Play-OUT is OWNER/MASTER-SINGLE-WRITER; non-owners
   converge off a synced `VRCUrl` + a bumped int. VRChat rate-limits URL loads (~1 / 5 s instance-wide) ⇒
   the world QUEUEs/debounces, never loads per selection change.

`key` is the STABLE identity synced in-world (via the one Manual-synced `Selection` behaviour) - NEVER a
list index into the per-client-downloaded roster (indices disagree across clients). `streamUrl` must be on a
VRChat video-allowlisted host. When MULTIPLE performers are live, an access-gated in-world picker syncs the
chosen `key`.

---

## Versioning + compatibility

- `contractVersion` is a single major shared by both channels. Breaking change ⇒ bump + both repos update
  this doc together. The world refuses a MAJOR mismatch on the loopback (`Health.contractVersion`) and on
  each gist envelope; additive fields are ignored by older clients.
- Per-module gist `schema` carries its own `@<major>` so one module can evolve without a full bump.
- `seq` is monotonic-per-module and rave-mate-owned; it is the ONLY dedup/ordering signal the world trusts
  (per-module DTO version fields, if any, are decorative).

## ToS posture

All privileged fetch (VRChat friends/groups/session, Twitch who-is-live, GitHub gist writes) happens ONLY
in rave-mate on the authorized machine. The editor talks ONLY to `127.0.0.1` and holds no third-party
credential. The world (Udon) consumes ONLY sanctioned gist raw strings + sanctioned video URLs; it has no
write path and cannot open a socket. Any feature requiring the editor or world to authenticate to VRChat /
Twitch / GitHub directly is out of scope by construction.

## Go source of truth

- `internal/matebridge/contract.go` - loopback DTOs, `Problem` (+ `ErrBadRequest` sentinel), discovery
  file, constants.
- `internal/matebridge/envelope.go` - gist envelope + module payloads + `MarshalSingle`/`MarshalBundle`.
- `internal/matebridge/server.go` - loopback server + gateway seams (`Directory`, `Presets`,
  `SettingsStore`, `RosterPublisher`) + the optional `Availabler` liveness interface.
- `internal/gistseq` - the persisted monotonic per-module SEQ-GATE counter (`Open`/`Next`/`Peek`).
- `internal/matepreset` - file-backed `Presets` store (`<cfg>/mate-presets/<kind>/<id>.json`).
- `internal/vrcperm/live.go` - the enveloped rave.live/* gist WRITER + `PublishRoster` (extends the
  flat `allow.txt`/posters/events writers).
- `internal/app/editorbridge.go` - app-side adapters (`directoryGateway` over `vrchat.Manager`,
  `settingsGateway` over `config`, `rosterGateway` over `vrcperm.Service`) + `pointerProvider` (from
  `vrchat.Manager` + `vrctools`/`vrcloc`) + the `editorbridge` module (gated on `WorldSync.Enabled`).

### App-side implementation status + decisions (v1)

- **WIRED, not a stub.** The server is constructed + started in `internal/app` as the `editorbridge`
  module. Handlers return real data; a genuinely-missing capability stays 501 + drops from `/health`.
- **Enablement = reuse `WorldSync.Enabled`** (no new toggle). The editor bridge is the edit-time half
  of the same world-integration feature; its runtime half is WorldSync's gist refresher. One flag = one
  feature. A gateway further self-gates LIVE via `Availabler.Available()`: `vrchat` needs a signed-in
  session, `worldsync` needs GitHub linked, so `/health` capabilities track login/link state between
  heartbeats without a wire change.
- **SEQ-GATE storage = a JSON ledger** (`<cfg>/worldsync_seq.json`) via `internal/gistseq`, keyed per
  module (`pointer`/`config`/`performers`/`roster:<slug>`/`preset:<kind>`). A seq is consumed ONLY on an
  actual write (diff-only hashes the INNER payload, so a changing `seq`/`updatedAt` never self-triggers).
  Shared by the gist writer AND the preset store. A lost ledger risks at most a one-time reset.
- **Flat + enveloped COEXIST.** `allow.txt`/`posters.json`/`events.json`/`nowplaying.json` stay flat for
  VideoTXL/ProTV/RaveAccessControl; the new rave.live/* module gists (pointer/config/performers) carry
  the `{schema,contractVersion,seq,updatedAt,<module>}` envelope. `PublishRoster` writes the FLAT
  allow.txt/allow.json (page.rave.access consumes the flat list) and returns a `seq` for provenance.
- **Carriage = SINGLE-MODULE** for pointer/config/performers (one gist + one seq each, independent
  cadence + the out-of-order-completion fix). `MarshalBundle` is provided but unused by v1.
- **Pointer** stamps `instanceOwnerName` = the signed-in VRChat display name and seeds
  `byOperator=[{operator, profileId:"main", priority:10}]`; `activeGroupId/Name` + a best-effort
  `joinInfo.deepLink` (`vrchat://launch?ref=rave.page&id=<world>:<instance>`) come from the location
  timeline when known. Publishing is opt-in (`WorldSync.PointerOn`).
- **`online`** is derived in `directoryGateway`: VRChat `""`/`offline` ⇒ offline, any other status ⇒
  online (the editor never sees VRChat's status vocabulary).
- **Preset payload** is stored verbatim as opaque JSON (whitespace may normalize; data is identical).
  Unknown kind / traversal id ⇒ `ErrBadRequest` ⇒ 400 (distinct from 502 upstream).
- **Not yet sourced (plumbing only, no data):** the `config` + `performersLive` module writers exist and
  are tested, but rave-mate has no config-profile / Twitch-performer mapping yet, so nothing publishes
  them. `rebuild-signals` returns an empty poll (rave-mate edits nothing needing a re-bake). `webLink`
  in `joinInfo` is left empty (not derivable from the location).
