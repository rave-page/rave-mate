# DJ-data aggregation - feature summary

Multi-source system that connects to DJ software/controllers by every viable means at once,
fuses their data into one live session, and feeds stream/overlay/recording sinks. Full
design + source matrix: `docs/DJ_SOURCES.md`. MIDI mapping: `docs/MIDI_MAPPING.md`.

## What shipped

- **Layered framework** (`internal/session/`): normalized `Observation` envelope, canonical
  field vocabulary (= Traktor's wire keys → zero ingest wire-break), `Source`/`Sink`
  interfaces, capability self-description, and a **Merger** that picks a per-field winner by
  source priority + freshness (TTL) + confidence.
- **Hub** (`internal/session/aggregator`): the always-on `session` module; starts enabled
  sources/sinks and live-`Reconcile()`s on a settings change.
- **Sources**: `traktorsrc` (existing :8080 HTTP feed, adapted), `nmlsrc` (collection.nml
  metadata enrichment - fills album/genre/key/bpm incl. decks C/D, fsnotify-reloaded),
  `midisrc` (custom CC map → deck/channel state + Denon HC4500 stock map → A/B titles).
  Planned stubs registered for visibility: `icecastsrc`, `nowplayingsrc` (macOS), `qmlsrc`.
- **MIDI driver** (`internal/midi`): Windows via `winmm.dll` through stdlib `syscall` -
  **no new dependency**; verified enumerating real hardware ports. `!windows` = stub.
- **Sinks**: stream publisher **repointed onto the merger** (wire-identical for Traktor-only
  setups); `filesink` (`now_playing.{json,txt}` for OBS); `recorder` (confirmed-play
  tracklist with per-track start/end → local store, auto-records across a live stream,
  export txt/CSV/JSON).
- **UI**: **Session** tab (merged state + provenance + live source-coverage matrix),
  **Recordings** tab (active tracklist + past recordings + export/delete), and
  Settings → "DJ data sources & outputs" cards (NML / MIDI / recorder / now-playing files).

## The payoff

Comprehensive live session info → automatic DJ-set tracklists and a precise recording span,
captured live from whatever connection methods the user has, with graceful degradation as
sources drop in/out.

## Notable design choices

- Canonical field names equal Traktor's existing keys, so a Traktor-only setup produces
  byte-identical ingest payloads - the merger-as-hub refactor is safe.
- The Denon decoder is **best-effort** (slot/encoding varies by hardware) and ranked below
  the HTTP/QML feed; the custom CC map is deterministic (we own the spec).
- Per-field priority + TTL means a high-priority source dropping out ages out and a
  lower-priority source transparently takes over.

## Tests

`internal/session` (merger priority/TTL/confidence + now-playing derivation), `traktorsrc`
(wire-fidelity), `filesink`, `recorder` (confirm/switch/flap state machine + export),
`nmlsrc` (NML parse/index), `midisrc` (custom + Denon decoders). `go test ./...` clean;
`go vet` + `golangci-lint` clean; `go.mod` unchanged.

## Follow-ups

Author the binary `RavePage-State.tsi` (Traktor TSI format); implement the planned sources;
portable (mac/linux) MIDI backend (needs a soaked dependency); optional live tracklist
import from Traktor History `.nml`.
