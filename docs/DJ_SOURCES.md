# DJ-data aggregation - sources, merger, sinks

rave-mate connects to DJ software/controllers by **every viable means at once** and fuses
them into one live session picture. Each connection method is an independent **Source**; the
**Merger** picks a winner per field by source priority + freshness; **Sinks** consume the
unified state. Code: `internal/session/` (contracts + merger), `internal/session/sources/*`,
`internal/session/sinks/*`, `internal/session/aggregator` (the hub / `session` module).

The **Session** tab shows the merged state with per-field provenance and a live coverage
matrix; the **Recordings** tab shows the auto-generated tracklist. Toggle sources/sinks in
Settings → "DJ data sources & outputs" (applied live, no restart).

## Normalized envelope

Every source emits `Observation{Source, TS, Scope{deck|channel|master, id}, Fields, Confidence,
Loaded}`. Field names are **canonical = Traktor's wire keys** so a Traktor-only setup is
byte-identical on the rave.page ingest API and every other source fills the same vocabulary.

Deck: `title, artist, album, genre, bpm, key, isPlaying, elapsedTime, trackLength, deckType,
loadedAt`. Channel: `fader, eqHigh, eqMid, eqLow, filter, cue`. Master: `bpm, phase`.

## Source × field coverage

| Source (ID) | Status | Provides | Notes |
|---|---|---|---|
| Traktor HTTP (`traktor`) | **live** | all deck/channel/master fields | The :8080 listener. Fed by the api-client **QML mod** - now installed natively (Settings → "Traktor QML feed"; `internal/traktorqml`). Richest live feed when present. |
| Traktor NML (`nml`) | **live** | deck `album, genre, key, bpm` | Collection-accurate metadata enrichment; fills decks C/D + fields the live feed misses. Watches collection.nml. |
| MIDI custom (`midi.custom`) | **live** | deck `isPlaying`; channel `fader, eq*, filter, cue` | Our RavePage-State.tsi CC map. `fader` drives now-playing. See `MIDI_MAPPING.md`. |
| MIDI Denon (`midi.denon`) | **live (best-effort)** | deck A/B `title, artist` | Traktor's stock DN-HC4500 map reused for LCD text. Validate per hardware. |
| Serato (`serato`) | **live** | deck `title, artist, album, genre, bpm, key` | Local files only (no account/internet). Collection = `_Serato_/database V2` + crates; now-playing = newest `History/Sessions/*.session`, ~1–2s lag. See "Cross-DJ-software" below. |
| VirtualDJ NetCtl (`virtualdj.netctl`) | **live** | master `title, artist, bpm, key, isPlaying` | Network Control plugin HTTP poll (~500ms). Full metadata, but needs VirtualDJ **Pro 2023+** + one-time manual plugin install. |
| VirtualDJ OS2L (`virtualdj.os2l`) | **live** | master `bpm` (beat/phase) | We host an mDNS+TCP OS2L server VDJ auto-connects to - **zero config**, but **no track text** (BPM/beat only). |
| VirtualDJ tracklist (`virtualdj.history`) | **live (delayed)** | master `title, artist` | History tracklist file poll. Title/artist only, laggy fallback. |
| Rekordbox DB (`rekordbox.db`) | **live (delayed)** | master `title, artist, bpm, key` | master.db `djmdSongHistory` poll. Reuses the SQLCipher key; **~60s lag** (rekordbox marks "played" ~1min in). |
| Rekordbox memory (`rekordbox.mem`) | **live (Windows; offset-seeded)** | deck `title, artist, bpm, isPlaying` | Reads the rekordbox process memory for real-time data. **Offsets are operator-seeded placeholders** → self-disables with a clear log until seeded per rekordbox version (cf. grufkork/rkbx_link). |
| Pro DJ Link (`prodjlink`) | **live** | deck `bpm, isPlaying`; +`title, artist, key` via resolver | Pioneer CDJ/XDJ UDP broadcasts (50002). Status carries only a rekordbox track id → resolved to text from master.db. Needs hardware on the LAN. |
| Icecast (`icecast`) | **live** | master `title, artist` | Traktor broadcast → local listener; master-only, ~10s delay, hijacks broadcast. |
| macOS Now Playing (`nowplaying`) | _planned_ | master `title, artist` | macOS MediaRemote; master-only. |
| Icecast (`icecast`) | _planned_ | master `title, artist` | Traktor broadcast → local listener; master-only, ~10s delay. |

"Planned" sources are registered (disabled) so the Session tab advertises what each would
add - implementing one is a drop-in (write the `Source`, flip its gate in `app.go`).

## Cross-DJ-software sources (Serato / VirtualDJ / Rekordbox)

Beyond Traktor, rave-mate reads now-playing + collections from the other major DJ apps. All
off by default (opt-in per source card in Settings → DJ sources); collection read is also
exposed via `rave-mate import <serato|virtualdj>` (preview summary). Each app exposes data
differently, so the cards surface the **trade-offs** plainly:

- **Serato** - fully local, no setup. Collection from `_Serato_/database V2` + crates (one
  shared TLV envelope; `internal/serato`); live now-playing by watching the active
  `History/Sessions/*.session`. No Serato account, no internet, no "Start Live Playlist".
  **Per-deck:** Serato logs a history entry per played track carrying its deck; the live
  track on each deck is its latest entry with no `endtime`, so concurrent decks surface
  independently with `isPlaying` (deck idle once Serato writes an `endtime`). No mixer/EQ/
  fader state - Serato exposes none in the history file (see MIDI note below for EQ).
- **VirtualDJ** - collection from `database.xml` (per-drive merge; BPM stored as seconds/beat →
  `60/x`). Three live channels, pick any: **Network Control** (full metadata, needs Pro +
  manual plugin), **OS2L** (zero-config but BPM/beat only - we host the mDNS+TCP server), and
  the **tracklist file** (title/artist, laggy). `internal/virtualdj`, `…/sources/virtualdjsrc`.
- **Rekordbox** - collection read/write already via `internal/rekordboxdb` + libsync. Live
  now-playing adds **db-poll** (safe, ~60s lag) and **memory-read** (real-time, Windows-only,
  offsets must be seeded per version). The **Pro DJ Link** source covers networked CDJ/XDJ
  hardware, with track text resolved from master.db. `…/sources/rekordboxsrc`.
- **MIDI setup** - `internal/rekordboxmap` generates an importable rekordbox MIDI map on the
  **same CC layout** as our `midi.custom` map, so one controller mapping drives both rekordbox
  and rave-mate (Settings → "Rekordbox MIDI mapping"; user imports once - no silent install).

## Connection ladder (richness ↔ stability)

The design goal: **the more connection methods, the more fallbacks.** Each user picks where
they sit on the richness/stability trade-off; the merger fuses whatever is active into one
rich per-track dataset, so methods stack rather than compete.

1. **QML mod (`traktor` HTTP)** - *richest, most invasive.* Full per-deck metadata + position
   + cues. Edits Traktor's D2 QML (patched in place, reversible; `internal/traktorqml`). A
   Traktor update can revert it → Re-apply. Best data; pick this if you want features.
2. **Denon DN-HC4500 MIDI (`midi.denon`)** - *stable A/B titles.* Reuses Traktor's **stock**
   Denon controller mapping (installed via Settings → Traktor mappings, no QML edit) to stream
   deck A/B LCD text over MIDI. Survives Traktor updates untouched; pick this if you want
   stability over features. Covers only A/B title/artist - the merger fills the rest below.
3. **Custom MIDI map (`midi.custom`)** - transport + mixer state (play/fader/EQ/filter/cue) for
   all four decks via an Add-Out TSI (see `MIDI_MAPPING.md`). Also stable (no QML edit).
4. **NML collection (`nml`)** - enriches whatever title/artist the above produce with
   collection-accurate album/genre/key/BPM (+ decks C/D). Always-on, read-only, no setup.

So a stability-first user runs **Denon (A/B) + custom MIDI (mixer) + NML (enrichment)** and
still gets a rich, fused now-playing record without touching Traktor's bundle; a features-first
user adds the QML mod on top and the merger prefers its richer fields automatically.

## Merger (per-field priority + TTL)

`internal/session/priority.go`. For each field a winner is chosen by: **same source always
refreshes**; else a **stale** holder (past the field's TTL) yields to any fresher reading;
else **higher source priority** wins; ties broken by **confidence**, then recency. Example
priorities - `title`: qml > traktor > midi.denon > nml > icecast > nowplaying; `album/genre`:
nml first (collection-accurate); live numeric state: the HTTP/QML feed over MIDI.

## Sinks

| Sink | Output |
|---|---|
| Stream publisher | Repointed onto the merger; batches merged updates → rave.page `/streams/{id}/ingest` (wire-identical to the legacy Traktor path for a Traktor-only setup). |
| Overlay renderers (browser / PNG / video-share) | Per-deck now-playing cards. Optional **scrolling waveform + merged EQ/FX panel** (`docs/WAVEFORM_OVERLAY.md`; `internal/waveform` peaks cache). |
| File sink (`filesink`) | `now_playing.{json,txt}`, rewritten on change, for OBS text/browser sources. |
| Recorder (`recorder`) | Confirmed-play tracklist with per-track start/end times → local store; auto-records across a live stream; export txt/CSV/JSON. **Auto-reconciles** to Traktor's authoritative `History/*.nml` the moment Traktor closes (watcher + periodic sweep; idempotent via `ReconciledAt`), replacing the best-effort live tracklist with ground-truth timestamps. The payoff: automatic, accurate set tracklists + precise recording span. |

## Now-playing & confirmed-play

`UnifiedState.DeriveNowPlaying()` picks the audible deck (the playing deck with the highest
channel fader; elapsed breaks ties). The recorder logs a track once it's been the
now-playing track past the confirm threshold (default 30s, mirrors Traktor's history-commit
rule), capturing start/end times and filling metadata (e.g. NML album) as it arrives.

## Live access to Traktor's collection & history (load-bearing constraint)

Verified against the Traktor Pro 4 manual (§History Playlists / Track Collection) + live
filesystem. **Do not "fix" the recorder to read the live tracklist from a history file - it
does not exist mid-session.**

- **`collection.nml` - live-readable.** Traktor doesn't lock it exclusively; we index all
  ~25k tracks while Traktor runs (see `nmlsrc`). Static per-track metadata
  (title/artist/album/genre/key/BPM/cues/grid) is valid live and is our enrichment source.
  Caveat: play-count / last-played / this-session edits are **flushed to disk only on
  Traktor close** (or a manual right-click → Save Collection) - so the on-disk file lags the
  running session for those mutable fields. Static metadata does not change mid-set, so
  enrichment is unaffected.
- **History `.nml` - NOT live.** Manual: the History Playlist "will be stored in a History
  Playlist file **when Traktor gets closed**"; on relaunch the in-tree History is cleared.
  The running session's played list lives only in Traktor's memory; the dated
  `History/history_*.nml` for the current set appears only at quit. Confirmed empirically:
  with Traktor running, the newest `History/` file is the *previous* session's.
- **Consequence.** The **live** tracklist must be derived from deck state (MIDI/HTTP/QML →
  merger → recorder), which is what we do. The history `.nml` is a **post-session** artifact,
  used only for (a) supplementary metadata of tracks not yet in the collection (`nmlsrc`
  merges the newest one), and (b) Phase-3 reconciliation: matching a finished recording's
  span against the closed session's authoritative tracklist + timestamps.
