# DMX & lighting

## Art-Net plane

rave-mate ingests console DMX via Art-Net (UDP 6454) into a universe store, and can re-emit to
another target. Use it to bridge a hardware desk into VR/stream visuals.

## VRSL video grid

Renders selected universes as a VRSL-compatible video grid (Spout sender or PNG) - VRChat
worlds running VRSL read your real console's DMX from the stream video. Grid mode/universe
mapping on the DMX card.

## DMX → MIDI (VRChat `--midi` worlds)

Converts universe channels to MIDI CC on a virtual port for VRChat worlds that take MIDI input.
Change-detected + rate-capped (VRChat crashes above ~128 MIDI events/frame - the bridge hard-
caps below that). Needs a virtual MIDI cable (e.g. loopMIDI).

## Timecode

House SMPTE clock for the whole rig: LTC audio output, MIDI Timecode, Art-Net TimeCode - one
master (elected across paired instances), everything chases. See multi-pc.md.

## Caveats

- Art-Net listener binds :6454 - only one Art-Net node per host IP.
- VRSL grid + Spout need a `spout` build on Windows.
