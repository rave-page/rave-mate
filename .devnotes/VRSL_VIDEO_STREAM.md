# VRSL DMX-over-video stream (rave-mate side)

Live DMX rig -> VRSL video grid -> ffmpeg -> RTMP/WHIP push -> transcode service -> VRChat.

**Canonical spec** = the frozen world-repo contract, NOT here. Keep both ends in sync:
`world_building_2-tooltips/.devnotes/VRSL_VIDEO_STREAM_CONTRACT.md` (v1, frozen 2026-07-16).
This note is the rave-mate-side mirror + the constants the Go duplicates.

## What shipped (branch `agent-vrsl-video-stream`)
- `internal/config` `StreamFeature` (`Features.Stream`, configVersion 30->31, off by default):
  `URL, StreamKey, Mode, ColorMode, Universes, FPS, BitrateKbps, Encoder, Transport` + `Resolved*()`.
- `internal/vrslgrid/composite.go` - the 16:9 compositor (`RenderComposite`): high-byte grid at the
  rightmost 208px, extended adds a left low-byte mirror grid + a 32px-cell metadata band.
- `internal/vrslstream` - in-proc supervised ffmpeg RTMP/WHIP push module (`Streamer`), producer
  (dirty-flag + 1fps keepalive, frameCounter) + supervisor (capped backoff, KillTree, bounded chan).
- webui card + `ctl stream-status` + module `vrslstream`.

## Frozen format constants the Go duplicates (must match the world reader)
Base VRSL grid (`internal/vrslgrid/grid.go`): CellPx=16, ColsPerUni=13, RowsPerUni=40, ChPerUni=512,
DeadCells=8, GridWidthPx=208. Per-universe cell: `x=ch%13, y=ch/13`; universes stack vertically.

Composite frame (`composite.go`): width fixed 1920 (strip = 208/1920), height = max(1080, gridHeight)
rounded even. Regions:
- Right strip `x[1712,1920)` = high-byte grid (stock VRSL reads only this).
- Left mirror `x[0,208)` = low-byte grid (extended only).
- Metadata band 32px cells from `x=216` (extended only):
  - Row 0: col0/1/2 calibration triad 0/128/255; col3=MAGIC 'R'(0x52), col4='V'(0x56), col5=version(1),
    col6=flags (bit0 rgb9, bit1 loFrameValid), col7=baseUniverse(lo byte), col8=universeCount,
    col9=frameCounter (0..255, advances every emitted frame incl. keepalives), col10=CRC8.
  - Row 1: col0=lookId, col1=sceneId, col2=blackout (0=normal,255=blackout), col3+ reserved.
- **CRC8**: poly 0x07, init 0, MSB-first, no reflection, no xorout. Input byte order (reader recomputes
  identically): every universe's 512 HIGH bytes (block order), then every universe's 512 LOW bytes,
  then semantic lanes [lookId, sceneId, blackout].

## 8-bit source note
Art-Net is 8-bit/channel. Extended mode BIT-REPLICATES low=high so `high<<8|high = high*257` -> /65535
== high/255 exactly (lossless 16-bit); loFrameValid is set. A future true-16-bit source overwrites the
low byte with the real fine channel. lookId/sceneId/blackout are written 0 (reserved for a future
VJ-state integration; the reader tolerates/ignores unknown lanes).

## Colorspace (load-bearing)
LINEAR, no gamma. ffmpeg feeds `rawvideo -pix_fmt rgba`, output `-pix_fmt yuv420p -color_range pc`
(FULL range: byte v -> luma v), NO `-vf` colorspace/gamma filter. `mono` survives 4:2:0; `rgb9` doesn't
(default `mono`). The extended calibration triad lets the world correct residual transcode skew.

## Isolation decision: IN-PROC (rtspserve-style), NOT featurehost
CLAUDE.md mandates a featurehost child for ffmpeg-spawning features UNLESS the low-throughput
rtspserve carve-out applies. It applies here (documented in the `internal/vrslstream` package header):
1. Bounded, low throughput: rasterize a tiny synthetic grid from the in-memory DMX store -> ffmpeg
   stdin. No media INGEST/decode/cross-PC route. ffmpeg owns network egress; rave-mate never buffers
   media bytes. Frame chan bounded (cap 4, drop-newest).
2. Decisive: the feature MUST read the SAME `artnet.Store` the in-proc `dmx.Router` owns, so one
   Art-Net listener serves both. A featurehost child would need a second UDP :6454 listener (can't
   co-bind) or a high-rate cross-process copy of the whole universe store every frame - the opposite
   of isolation. Sharing the store by pointer is simpler AND lower-throughput.
3. Supervised like rtspserve: capped 1->10s backoff, KillTree + AssignToJob, tailWriter stderr.

The stream owns a best-effort Art-Net listener only when the DMX plane is off (else it reads the
shared store DMX feeds). No new deps (stdlib + shell to ffmpeg).

## User config
Enable Settings -> VRSL video stream; set Transport (rtmp/whip), Push URL, Stream key (RTMP only),
Mode (standard/extended), Colour mode (mono/rgb9), universes, fps, bitrate, encoder. The VRChat world's
video player loads the SAME stream URL the service serves (RTMP: the play/HLS URL your VRCDN/Twitch/
custom service exposes for `rtmp://.../<key>`; WHIP: the service's playback URL). `ctl stream-status`
shows the push state.

## Follow-ups / not done here (world side + niceties)
- World side (page.rave.stage `RaveVRSLGridReader` extend, Stage Manager "Add VRSL Video Stream",
  fixture fine channels, signalPresent gating) - separate effort, tracked in the frozen contract.
- Encoder "auto" currently falls back to libx264 (no hw probe yet).
- lookId/sceneId/blackout lanes are written 0 (no VJ-state plumbing yet).
- UI screenshot verification (`ctl screenshot-all`) not run here - manual follow-up (needs a running
  build; webui compiles clean).
