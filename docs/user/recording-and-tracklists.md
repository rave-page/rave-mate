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

## Post-set reconcile

When Traktor writes its history NML (on close), rave-mate auto-reconciles it against captures/
recordings - no manual step.
