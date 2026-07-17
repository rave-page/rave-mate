# Cue Editor (drops & cue patterns)

Beat-precise hotcue / memory-cue editing on the library waveform, built around **cue
patterns**: DJs usually cue the same way on every track — one cue at the drop, then
cues at fixed beat distances around it. Mark the drops once, save your pattern once,
apply it to any number of prepared tracks.

## Opening

Library → select a track with a beatgrid → **Prepare cues** (the button shows in
Collection, Browse and Playlists once the track is in the collection). Whole-set entry:
**Prepare cues (playlist)** in a playlist's action row, or **Prepare cues (folder)** in
the Browse toolbar - eligible tracks (in collection + beatgrid) become the mass-apply
selection, the collection focuses on that playlist, and the editor opens on the first
track; tracks without a grid are skipped with a count. The waveform expands to full tab
width above the browser with the beatgrid, cue flags, drop markers and beat cursor
rendered on it. The strip above the wave shows track identity, cursor position (time +
bar.beat), beat-jump size, each drop (click = jump) and the cue count; the right rail
holds the actions.

## Marking

| Input | Action |
|---|---|
| Left-click wave | move beat cursor (snaps to nearest beat) — on a marker: select just it |
| Ctrl+click | toggle a cue/drop marker in or out of the selection |
| Right-click | add **memory cue** at that beat |
| Shift+right-click | add **drop marker** |
| Ctrl+right-click | remove cue + drop markers at that beat |
| Drag | rubber-band-select cues **and drops** (pattern save / bulk delete) |
| ← / → | walk beats · Shift = beat-jump (size: Shift+↑/↓, 1–64) |
| T / Enter | add drop at cursor · +Shift removes |
| Del / Backspace | delete **every selected cue and drop** in one action |
| Ctrl+Z | undo the last edit (one-deep; press again = redo — nudge runs count as one edit) |
| Hold Space | audition from cursor — release **pauses** with the decoder parked on the cursor, so the next press starts instantly (the engine lets the file go after ~90 s idle) |
| Ctrl+↑ / ↓ | zoom the wave in / out on the beat cursor (hold for continuous) |
| Ctrl+← / → | nudge the whole beatgrid 10 ms (Ctrl+Shift: 1 ms) — **cues and drops move with the grid** |
| P | add the track to the **preparation playlist** · already in? a toast says so — **hold P ≈1 s** to remove it again |

Keys work only while the Library tab has focus and no input field is active. The ⓘ
tooltip in the strip above the wave shows this table as a key-cap grid.

Every action above (plus library browsing and back/forward) is also MIDI-mappable to a DJ
controller — pads, buttons, knobs and endless encoders, with hold-audition mapping to the exact
hold-Space press/release semantics. MIDI tab → **Control rave-mate** → Learn; details in
[MIDI_MAPPING.md](MIDI_MAPPING.md#control-rave-mate-from-the-controller-ui-mappings--shipped).

### Selection

The rubber band selects cues **and drops** — selected cues glow in their color, drops
in amber. Click a marker to select only it; Ctrl+click adds/removes single markers.
**Del** (or Backspace) removes the whole selection at once: cues from the library,
drops from library + file tag (one debounced tag write), all journaled in the change
log. Empty selection = Del is a no-op.

Numbers along the top of the waveform show the **beat distance between neighbouring
markers** (cues + drops); flags at the bottom carry the hotcue pad number (or `M` for
memory cues) — hover for name + time.

## Drops: stored twice (synced thrice)

Drop markers are a rave-mate enrichment: they live in the library database (keyed by
file path — they survive collection re-imports) **and** in the file itself
(`RAVEMATE_DROPS` ID3 TXXX frame / FLAC Vorbis comment), so they travel with your
music. Formats without tag support show a ⚠ in the strip (database-only). Every
change is journaled in the change log. The tag write is a debounced write-behind
(latest state wins); while the audio engine still holds the file open it waits and
retries until the file is free — the library stays authoritative throughout, and
your waveform analysis cache survives the rewrite.

Library sync to rave.page carries them too: each uploaded track includes its drop
markers (`drops_ms`, integer ms), so waveforms on your rave.page profile show your
drops. Edits and clears propagate on the next sync — cleared tracks send an explicit
empty list; tracks you never marked leave the server untouched.

The **NO DROPS** filter chip (collection toolbar) lists unprepared tracks; the CUES
column shows each track's census — ◆n drop markers, ⚑n cues.

## Patterns

1. On a track that already has your cues: drag-select them, name the pattern, **Save
   as pattern**. Offsets are stored in beats relative to the nearest drop (or the
   cursor).
2. On a prepared track: assign a pattern per drop (they can differ), then **Apply
   patterns (hotcues)** or **Apply as memory cues**. Cues that don't fit the span
   (track start ↔ drop 1, previous drop ↔ drop N) are cut — the drop is always the
   anchor. Occupied pad slots are reallocated (or the cue is demoted to a memory cue
   when none is free); duplicates are skipped.
3. **Mass apply**: tick tracks in the list (rows highlight amber) and use the same
   two buttons in the selection bar — every checked track's own drops anchor the
   assigned patterns; tracks without drops or grid are skipped and counted.

**Convert all hotcues → memory cues** demotes a track's pads in one step (positions +
names kept) for software that supports memory cues. **Assign pads: memory cues →
hotcues** is the reverse — free pad slots go to memory cues in time order (Traktor
shows a memory cue as a flag but pads can't fire it; this makes them pad-triggerable).
**Clear cues…** removes the mode's musical cues (confirmed; grid anchors, load/fade
markers and drops are kept).

**Manage patterns…** opens the pattern library: rename a pattern (drop assignments
follow immediately), **overwrite** its cues with the current wave selection (anchored
at the nearest drop), or delete it. Tracks that already received cues from a pattern
keep them — patterns are stamps, not links.

### Batch: the checked tracks

Once collection rows are ticked, the rail grows a **{n} checked tracks** section:
apply the assigned patterns (hotcues or memory), **Assign pads**, **Hotcues → memory**
and **Clear cues** across every checked row — each track anchored on its own drops;
tracks without drops or grid are skipped and counted.

## Preparation playlist (the P key)

Pick a manual playlist as your **preparation playlist** — the picker sits in the
collection toolbar and in the cue-editor rail (same selection, persisted), with a
create-new entry right in the list. **P** then adds the current track — the open
cue-editor track, or the selected collection row — to it. If the track is already in,
a toast tells you; **keep holding P for about a second** and it's removed again
(release earlier and nothing happens). Smart and DJ-imported playlists aren't offered:
smart membership is rule-driven, and imported lists get replaced on the next DJ sync.

## Target software modes (different cues per app)

The **Target software** picker at the top of the rail scopes the editor to one DJ app
(installed ones are badged *detected*). In a mode, new cues — right-click memory cues
and pattern applies alike — are tagged for that software only; other apps' cues stay
visible but dimmed (the flag tooltip names their app), don't collide with the mode's
cues, and don't consume its pad slots. **All software** (the default) edits the shared
layer every app sees. So a track can carry one cue set for Traktor and another for
Rekordbox, and each write-back exports only what its target should see: the shared
layer plus that app's own cues.

Each mode remembers its own **defaults** (the collapsible section under the picker):

| Default | Effect |
|---|---|
| **Hotcue pad limit** | caps how many hotcues survive an apply or a write (Traktor pads = 8). The cues **closest to each drop** win; excess pads demote to memory cues. |
| **Split the pad budget evenly across drops** | with several drops, each drop keeps its nearest cues (remainder to earlier drops; spare capacity refills globally). Off = one global closest-to-a-drop ranking. |
| **Overwrite existing cues when applying patterns** | pattern applies **replace** the mode's existing cues instead of adding around them (the apply buttons warn how many would be cleared). |
| **Always promote memory cues to pads when writing to {app}** | the write-back promotes memory cues to free pads on the way out — the library keeps them as memory cues. |

## Write cues to your DJ software

Cue edits persist to the rave-mate library (and drops to the file tag) immediately.
Pushing them into your DJ software is a separate, explicit step: the rail's **Write cues
to DJ software** section detects installed libraries and shows one **Write cues to
{app} (n)** button per target — the same backup-first router the beatgrid fixer uses.

Scope: the checked collection rows (the mass-apply selection), or just the open track
when nothing is checked. Only tracks with at least one musical cue are written; each
write **replaces** that software's hotcues/memory cues/loops for those tracks with the
cues shown in rave-mate. Beatgrids are never touched (that's the [beatgrid
fixer](BEATGRID_FIXER.md)'s job), and drop markers are **not** exported — they only
become cues via an applied pattern.

The export honours the target's scope and defaults: only the shared layer + that app's
own cues ship; the app's pad limit is enforced (closest-to-drop, split across drops per
its default) and, if enabled, memory cues are promoted to pads on the way out. The
button count shows how many tracks carry cues that software would receive.

Per software:

| Target | Written to | Notes |
|---|---|---|
| **Traktor** | `collection.nml` (backup first) | non-grid `CUE_V2` replaced; grid cues + TEMPO untouched. Restart Traktor to pick it up. |
| **Rekordbox** | exported collection XML (backup first) | `POSITION_MARK` hotcues (Num ≥ 0) + memory cues (Num = -1) + loops; import via **File → Import Collection**. The live `master.db` is deliberately not written — cues there are tied to ANLZ analysis data and a partial write would desync the library. |
| **VirtualDJ** | `database.xml` (backup first) | hotcues → `<Poi Type="cue" Num="1..8">`, memory cues → remix points (`Type="remix"`), loops carry `Size` in beats. Refused while VirtualDJ runs (it rewrites the database on exit). |
| **Serato** | the audio files ("Serato Markers2" tag, MP3 GEOB / FLAC vorbis) | verified temp-write per file, refused while Serato runs; a stale legacy `Serato Markers_` tag is removed (it would shadow the new cues). Memory cues have no Serato equivalent and are skipped — pad cues + saved loops only. MP4/AIFF/Ogg/WAV are left alone. |

Library-file targets are backed up into the standard backup folder before every write.
After a write the button turns into a "written" note; changing any cue re-arms it.

## Remote tracks (paired peer)

"Prepare cues" inside a remote-controlled Library (Multi-PC → Remote Library) runs the editor
**locally**: the peer's audio file is copied to this computer once (cached; progress dialog with
cancel) and waveform, beat-walking, edits and audition audio all happen here — the link can even
drop mid-edit without losing anything. Edits stay local until **Save cues to \<peer\>**, which
writes cues + beatgrid + drops back into the peer's library and file tags. If the peer's cue
data changed since you fetched the track, saving raises a conflict dialog (overwrite / re-fetch
& discard / cancel). After a save, the write-back buttons target the DJ software installed on
the **peer**. Playlist/folder cue-prep still executes on the peer.
