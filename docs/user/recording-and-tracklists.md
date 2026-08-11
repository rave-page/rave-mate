# Recording & tracklists

Everything for finishing a set lives on the **Publish** tab.

## Tracklist recorder

Records a confirmed-play tracklist from the session hub: a track counts once it's been audible
for the confirm window (default 30 s - filters teasing/cueing). Export/link from Publish.

## Set capture (Icecast receiver)

rave-mate runs a local Icecast2-compatible receiver: point Traktor's broadcast at it and your
set records to disk while the in-band metadata feeds now-playing. Captures auto-link to the
recorded tracklist. Metadata-only mode captures the tracklist without audio.

## Audio recorder

Native device capture to FLAC - record the master out of any interface. Can arm/disarm in sync
with OBS recording, or manually. Caveat: pick the correct device in the card; loopback devices
vary by OS.

## OBS recording link

With the OBS bridge enabled (obs-websocket, default port 4455), finished OBS recordings link to
the tracklist exactly like Icecast captures - one click from Publish to a labeled recording.

## Fingerprinting

With `fpcalc` (Chromaprint) installed, each track's span in a captured set is fingerprinted -
the backbone for later re-identification and library reconciliation. Optional; heavy work runs
in worker subprocesses.

## Crash recovery

A crash mid-set can't lose the capture anymore. On the next start rave-mate:

- kills a capture ffmpeg the dead session left running (it would otherwise record silence
  into the set file indefinitely),
- scans the capture folders for finished files no set knows about (an OBS recording whose
  finish it missed, a killed native capture), repairs an unfinalized FLAC header when needed
  (lossless; the original stays beside as `.orig`), registers them and links each to the set
  recorded over the same span,
- warns once if OBS is STILL recording from before the crash - stop it in OBS when the set
  is over and the file links up like any other.

## Post-set reconcile

When Traktor writes its history NML (on close), rave-mate auto-reconciles it against captures/
recordings - no manual step.

## Works-together marks from the tracklist

A set you played IS compatibility data - the Publish tracklist lets you keep it. Rows that
resolve to your library (the deck-reported file path, the reconciled history path, or a unique
artist+title match) get a checkbox: select 2+ and mark them **works-together**
(blend / double drop / energy) from the selection bar or the right-click menu. Marks land in
the same store as the Library tab's, so Find-compatible discovery, the smart-playlist
"Compatible with track" rule and the sorted-copy set builder all see them. Rows without a
library match show a dim dot instead (hover it for why) - they become markable once the file
is in your library or the set is history-matched.
