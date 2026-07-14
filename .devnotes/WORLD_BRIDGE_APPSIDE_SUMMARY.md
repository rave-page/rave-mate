# World Bridge - app-side implementation

Turns the frozen `internal/matebridge` stubs (was 501-everywhere) into a live edit-time loopback
server + runtime gist envelope writer. Canonical contract: `docs/WORLD_BRIDGE_CONTRACT.md` (world
mirror: `world_building_2/.devnotes/MATE_WORLD_CONTRACT.md`). Built on branch `agent-mate-world-contract`.

## What shipped

| Piece | Package | Role |
|---|---|---|
| SEQ-GATE counter | `internal/gistseq` | persisted monotonic per-module seq (`Open`/`Next`/`Peek`), atomic JSON ledger at `<cfg>/worldsync_seq.json` |
| Preset store | `internal/matepreset` | file-backed `matebridge.Presets` (`<cfg>/mate-presets/<kind>/<id>.json`), seq-stamped, traversal-guarded |
| Envelope serializers | `internal/matebridge/envelope.go` | `MarshalSingle` (payload inlined under its kind key) + `MarshalBundle` |
| Liveness | `internal/matebridge/server.go` | optional `Availabler` iface → `/health` + routes reflect live login/link state; `ErrBadRequest` → 400 |
| Enveloped writer | `internal/vrcperm/live.go` | `PublishPointer`/`PublishConfig`/`PublishPerformers` (single-module, diff-only on inner payload) + `PublishRoster` (flat allow.txt + seq) |
| Name lookups | `internal/vrchat/users.go` | `UserDisplayName` / `GroupName` for `Resolve` |
| App wiring | `internal/app/editorbridge.go` | `directoryGateway` / `settingsGateway` / `rosterGateway` + `pointerProvider` + `editorbridge` module |
| Config | `internal/config` | `WorldSyncFeature`: `PointerOn`, `PointerGistID`, `ConfigGistID`, `PerformersGistID`, `RosterGists` |

## Frozen open-questions resolved (all documented in the contract doc)

1. **Enablement** = reuse `WorldSync.Enabled` (no new toggle; edit-time half of the same feature). Live
   per-capability self-gating via `Availabler` (vrchat=signed-in, worldsync=GitHub-linked).
2. **SEQ storage** = JSON ledger (`gistseq`), NOT bbolt - single-writer, tiny, atomic tmp+rename. Seq
   consumed only on a real write (diff-only hashes the inner payload).
3. **Flat vs enveloped** = COEXIST. Flat allow.txt/posters/events kept for VideoTXL/access; rave.live/*
   module gists added. `PublishRoster` stays flat (page.rave.access reads the flat list) + returns seq.
4. **Carriage** = single-module for pointer/config/performers (independent seq + out-of-order fix).
5. **`online`** derived: VRChat ""/offline ⇒ false, else true.
6. **Pointer** = `instanceOwnerName` from the signed-in account + `byOperator` seed + best-effort
   `joinInfo.deepLink` from the location timeline.

## Verified

`go build ./... && go vet ./... && go test ./...` clean. Behavioural: `TestEditorBridgeHTTP`
(`internal/app`) drives the wired server over httptest - health capability gating (vrchat greyed while
logged out), preset PUT→GET→LIST through the real file store, 400 on bad kind, settings moduleUrls,
roster publish. Per-package: gistseq monotonic-across-reopen, matepreset round-trip + traversal reject,
vrcperm roster/pointer diff-only + seq, matebridge single-envelope + Availabler flip.

## Not yet sourced (plumbing only)

- `config` + `performersLive` writers exist + tested but have no data source yet (no config-profile /
  Twitch-performer mapping in rave-mate config). Wire a provider + a Twitch who-is-live loop to publish.
- `rebuild-signals` returns empty (nothing rave-mate edits needs a re-bake).
- `joinInfo.webLink` empty (not derivable from location; needs a rave.page-assigned vrch.at link).

## World-side impact (for the world/live workers)

- No WIRE change - all additive Go. Existing `MATE_WORLD_CONTRACT.md` stays valid.
- pointer/config/performers arrive as SINGLE-MODULE gists: common keys + payload under its kind key at
  top level (`TryGetValue("pointer"|"config"|"performersLive", ...)`), `modules` absent.
- Deep link format the world DISPLAYS: `vrchat://launch?ref=rave.page&id=<worldId>:<instanceId>`.
- Roster gist = flat `allow.txt` (+ `allow.json`), NOT enveloped; `hub.remoteListUrls[]` ← `rawUrl`.
