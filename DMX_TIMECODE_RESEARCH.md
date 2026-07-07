# DMX → VRChat + SMPTE timecode research (2026-07-02)

6 parallel research agents, adversarially verified against primary sources (VRSL shader source,
HNode, Art-Net 4 spec, Resolume manual, obs-websocket protocol.md + OBS C source). Basis for
tasks #19/#20.

## VRChat lighting ingestion - the three real inputs

Worlds can NOT receive OSC (avatar params only, feature request open since 2023). Real paths:

### 1. VRSL (VR Stage Lighting) - DMX-in-video, the club standard
Pipeline: console → **Art-Net UDP :6454** → grid renderer (GridNode $12/closed/Java, or open
HNode → Spout2) → OBS capture → stream → world video player → shader decodes pixels.

Grid spec (verified from MIT `VRSLDMX.cginc` + HNode):
- Horizontal: **1920×208 px strip at frame bottom**, 120×13 cells; vertical: 208×1080 right edge.
- **1 cell = 1 DMX ch = 16×16 px** (1080p); decoder point-samples block center.
- Mono: luminance 0–1 = DMX 0–255. 16-bit via DMX fine channels only.
- Addressing: `x=ch%13, y=ch/13`; universe stride 520 cells = 512ch + 8 dead padding.
- **Extended 9-universe: u1-3→R, u4-6→G, u7-9→B** folded onto u1-3 positions.
- **Linear color space** (v2.7.0+) - wrong gamma skews all values.
- Stream target: 720p30, max bitrate, 1s keyframes; VRCDN for clubs (~1–2s), Twitch/YT worse.
- Local performer path: no Spout/NDI ingest in VRChat - OBS→rtmp→**MediaMTX**→`rtspt://localhost:8554/x`
  in AVPro (~0.5s). rave-mate could own the whole local chain (grid→encode→RTSP), no OBS.
- Worlds: Club Orion, Club Elecami, TG Beach Club, Furality, Sly Fest.

### 2. MIDI → Udon (official, local-client-only)
- 3 events only: NoteOn/NoteOff/CC (ch 0-15, num/val 0-127); world needs VRC Midi Listener.
- `--midi=deviceName`, one device; **loopMIDI-class virtual ports are the standard**.
- Local-only (world must relay via Udon net). **Crash bug: >~128 events/frame kills client - rate-limit.**
- Precedent: VRC-MIDIDMX (realtime DMX-over-MIDI local preview into VRSL/MDMX grids).

### 3. AudioLink - audio-reactive only, no injection API. VRSL-AudioDMX abuses 5.1 channels for
8–12 DMX ch as AM tones. Not a control path for us.

## SMPTE into Resolume

**LTC via audio input is the ONLY native TC input, Arena-only** (no MTC, no Art-Net TC, both
ticketed/unimplemented). Config: Preferences→Audio→input+framerate (SMPTE 1/2); per-clip
Timeline→SMPTE + offset/delay. Same-machine feed = virtual audio cable.

What we must emit:
- **LTC**: 80-bit frames, biphase-mark, 48kHz ~−3dBFS, **slew-limited ~40µs rise** (ST 12-1),
  25fps parity/BGF swap gotcha, fractional samples/frame accumulator at 29.97. Pure Go, ~few
  hundred LOC. No usable Go lib (libltc = LGPL, reference only).
- **MTC**: `F1 nn` quarter-frames (8 per 2 frames, ~8.33ms) + full-frame SysEx locate. For
  desks/DAWs; Resolume only via converter.
- **ArtTimeCode**: 19-byte UDP :6454, OpCode 0x9700 LE, ProtVer14, F/S/M/H order, type 0-3.
  grandMA-class consoles; Resolume ignores.
- **ArtDmx**: 18+512B, OpCode 0x5000; Art-Net 4: unicast to ArtPoll subscribers, ≤44Hz + 1Hz
  keep-alive. sACN has no TC packet - skip unless priority-merge needed.

Scene consensus: **LTC is primary**; MTC second-class; OSC = triggers only.

## OBS sync

- Websocket v5 surface: `SetInputAudioSyncOffset` (−950…+20000ms), media cursor get/set/offset
  (`PLAY` is unpause-only → cold start = `RESTART`), `gpu_delay` (≤500ms) + `async_delay_filter`
  (≤20s, not Media Sources). **Media Source seek is keyframe-snapped (40–80ms err, worst = GOP);
  vlc_source seeks ms-exact** → chase target.
- libobs free-runs on `os_gettime_ns()`; no genlock; corrections are step-jumps. DeckLink/AJA
  discard embedded TC.
- Plugin ecosystem: ~6 hobby projects, nothing production-grade for SMPTE sync. Greenfield.
- **Verdict**: websocket chase loop ≈ 1–3-frame agreement with visible jumps (ship as cheap tier);
  the real answer is a **native OBS plugin**: async-video source ingesting medialink streams,
  `obs_source_output_video()` with TC-mapped timestamps → libobs schedules + syncs audio;
  offset via `CallVendorRequest`. Go precedent: **obs-teleport** (98% Go via cgo, GPL-2.0).
  ≤1 frame skew = software ceiling. OPEN: GPL posture (fork vs clean-room) - user decision.

## Implementation order (Go/Windows, stdlib-first, 0–1 new deps)

1. **Art-Net emitter** (ArtDmx+ArtTimeCode, stdlib net) - unlocks VRSL-via-GridNode/HNode + console TC.
2. **LTC generator → audio out** (pure-Go encoder + WinMM waveOut syscalls) - unlocks Resolume.
3. **MTC out** (WinMM midiOut; Win11 24H2 MIDI Services loopback or loopMIDI).
4. OBS websocket sync tier (goobs - needs SUPPLY_CHAIN soak row).
5. **Own VRSL grid renderer → Spout/window** (spec above; drops GridNode from chain); optional
   full local chain grid→encode→RTSP for the in-VR performer.
6. Native OBS plugin (medialink source + sync filter; obs-teleport pattern).
7. Optional MIDI→VRChat Udon bridge (VRC-MIDIDMX-style; rate-limit under crash bug).

## Open questions
GridNode paid-manual details (RGB toggle, unicast semantics); grid tolerance to encoder settings;
VRChat MIDI focus/Quest behavior; Resolume freewheel; OBS plugin GPL posture + ABI policy;
MTC/ArtTC jitter tolerance (practical: ≪8ms).
