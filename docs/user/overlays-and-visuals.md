# Overlays & visuals

All overlay sinks render the same fused session state - enable any combination.

## Overlay outputs

| Output | Use in OBS as | Notes |
|---|---|---|
| **Browser overlay** | Browser source (local URL on the card) | Every loaded deck + faders/EQ + cover art, SSE-live |
| **PNG deck cards** | Image source per deck | Native renderer, zero browser overhead |
| **obs-websocket renderer** | (drives OBS text/image inputs directly) | No sources to add manually; needs the OBS bridge |
| **Waveform panel** | part of the overlays | Scrolling waveform + EQ/FX strip; per-track peaks cached on first play |
| **Now-playing file** | Text source reading `now_playing.txt/json` | simplest possible integration |

Styling (colors, opacity, layout) lives in the overlay settings cards; overlays share the
brand-token look by default. Per-VRChat-world overlay layouts can auto-apply when you travel
(see the VRChat guide).

## Video share (Spout)

Windows GPU texture share: rave-mate publishes overlay/grid video as a Spout sender that OBS,
Resolume, or VRChat tooling can consume without capture overhead. Needs a `spout` build +
`SpoutLibrary.dll` beside the exe.

## Visual editor & media editor

- **Editor tab**: poster/thumbnail composer for event artwork (layers, blend modes, text).
- **Visual editor**: layer-based live visual compositing (see in-app help; evolving surface).

## Caveats

- Browser overlay + obs-ws renderer both on = double now-playing; pick one per scene.
- PNG cards write to disk each update - keep them on an SSD/ramdisk if you run many decks.
