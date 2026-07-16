# Publish video: MSE streaming for fragmented MP4s

## Problem

OBS records fragmented MP4 (tiny moov + ~1.9k moof/mdat pairs on an hour set, mfra at tail).
Chromium/WebView2's demuxer ignores mfra and range-scans EVERY moof before playing or seeking:
~1 GB of reads / 1,867 hops on a 33 GB set → 30 s+ video load on a busy disk, and the same scan
again per seek. Measured via mediahttp serve logging (one range request per moof offset).

## Fix

Feed fMP4 the way it's designed to be fed: MediaSource Extensions.

- `internal/mp4frag` (new, stdlib-only): header-only fMP4 indexer. Parses ftyp+moov (codec
  strings: avc1/hevc/av01/mp4a/opus/flac) + mfra tfra (per-fragment time+offset) → `Index{mime,
  initb64, dur, end, frags[]}`. No mfra (crash-cut file) → sequential moof walk reading tfdt,
  stopping cleanly at corruption (salvages every intact fragment). Never reads media bytes.
- Cache: `store.KindMp4Frag`, keyed path+mtime + `ContractVer` (webui/mpResolveFrag); negative
  sentinel for classic MP4s. Resolve runs in `u.bg` (mpLoadFrag), never in render.
- Serving: `/mi/<tok>` on the loopback media server returns the cached index JSON (same token
  as `/m/`). OPTIONS gets `Access-Control-Max-Age` so Range preflights don't repeat.
- Player: `mpVideoHTML` renders `<video data-mse=<idxURL> data-mse-src=<mediaURL>>` once the
  index is cached (patch target `mp-<host>-vid`; never swapped after `vid.started`).
- JS (`shell.go __mse*`): appends the sanitized init + only fragments around the playhead
  (30 s runway, trim behind, QuotaExceeded retry); seek = binary-search the fragment table.
  ANY failure → plain `src` fallback (the old behavior). Scan hook lives in `__patch`
  (data-mse substring) — NOT a MutationObserver: Init-time scripts run before documentElement
  exists, observing it throws and silently kills the rest of the runtime.

## Two OBS quirks Chromium's MSE parser rejects (its file demuxer tolerates both)

1. FLAC AudioSampleEntry `samplesize=0` → init append hard-errors. Go sanitizes to 16 in the
   InitB64 copy (`sanitizeInit`).
2. tfhd uses ABSOLUTE `base_data_offset` (MSE requires movie-fragment-relative addressing) →
   fragment append errors. JS `__mseFix` drops the 8-byte field, sets DEFAULT_BASE_IS_MOOF,
   shifts every trun data_offset by `(base − moofFileOff − removed)`.
   Both proven by byte-level bisection in Playwright Chromium before wiring in.

## Results (39-track set, 30.9 GB MP4 + 485 MB FLAC, isolated rig)

| | before | after |
|---|---|---|
| play start | ~6 s warm disk, 30 s+ busy | < 2.5 s (fetch pattern: init + 1 fragment) |
| seek to 1:01:54 | new full scan | 230 ms, direct fragment fetch |
| bytes to first frame | ~1 GB (1,867 range hops) | ~19 MB (1 fragment) |

Diagnostics kept: `__jsdbg` act (page-JS → logbus), mediahttp serve Debug log (range/bytes/ms).

## Audio half: FLAC binary-search seek (internal/audio/flac.go)

Same symptom on the audio master (direct FLAC captures): mewkiz `Stream.Seek` needs a
SEEKTABLE; without one (ffmpeg's flac muxer never writes one) it silently runs
`makeSeekTable()` = full-file decode BEFORE the first seek returns. Measured 1.19 s / 12 MiB →
~47 s on the 485 MB hour set, re-paid per open; `PlayFrom` seeks even for 0:00, so plain play
paid it too.

Fix: own the seek. flacDecoder now parses STREAMINFO itself, decodes via `flac/frame` directly,
and `SeekTo` binary-searches the FILE: FLAC frame headers carry their own absolute sample
number + CRC-8, so a probe = "scan ≤64 KB to the next validated header, read its sample
number". O(log n), no index, no cache, any FLAC. Landing is a running-position decode from the
anchor - NOT `frame.SampleNumber()`, which is wrong for the final short frame of a
fixed-blocksize stream (frame number × THIS frame's short size); probes reject that frame too.

Measured (real 485 MB seektable-less capture): open instant, every seek 0.5-30 ms,
sample-exact vs full-decode reference. Pinned by `flac_seek_test.go` (committed seektable-less
fixture; bounded-read gate proves no rescan) + the `-tags manual` on-file suite.
