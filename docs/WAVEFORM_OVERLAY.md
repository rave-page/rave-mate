# Waveform overlay panel

Opt-in scrolling-waveform + combined EQ/FX panel on every now-playing deck card. Shared by all
overlay renderers (browser overlay, per-deck PNG, video-share/Spout/Syphon/PipeWire). Off by
default - enable in Settings → **Overlay** → **Waveform panel**.

## What it shows

- **Scrolling waveform** behind the EQ curve. Scrolls right→left with playback; the **playhead**
  ("now") is fixed at `PlayheadPct` from the left (default 3/4). Played peaks (behind the playhead)
  use the waveform fill; upcoming peaks the same fill dimmed.
- **One merged EQ+filter curve.** The low/mid/high EQ S-curve with the filter rolloff merged in:
  where the filter caps the level below the EQ it's drawn in the filter colour (HP cuts the low end
  from the left; LP the high end from the right) up to where it re-meets the EQ curve, then the EQ
  colour. Smooth-max rounds the join + the colour cross-fades; a feathered drop-shadow keeps the
  line legible over a same-colour waveform.
- **Zoom** = visible seconds across the panel (default 20). Smaller = more zoomed = faster scroll.

## Smooth scroll - velocity-PLL clock

Both the browser and the native renderers run a per-deck **velocity phase-locked loop**
(`internal/deckclock`, mirrored in `overlay.html`) instead of snapping to each raw `elapsedTime`:

- React only to a **fresh** elapsed reading (DJ sources resend the same value up to 60/s over SSE;
  acting on stale repeats causes ripple). EMA the observed rate, apply a **capped proportional
  velocity trim** toward the true position, and **snap** on a big jump (seek / beat-jump > 2.5s).
- Shared constants across JS + Go: `Kp=0.5`, `maxTrim=0.2`, rate EMA `0.85/0.15`, snap `2.5`,
  jump `1.2` - so every output scrolls identically. Unit-tested in `deckclock/pll_test.go`
  (steady = near-constant velocity, no backward steps; seek snaps; stopped holds).

## Flicker-free rendering - offscreen envelope

Per-pixel sampling of raw peaks shimmers. Instead the envelope is **max-aggregated + 3× binomial
smoothed once per track** and cached; each frame samples it with **sub-pixel linear interpolation**.
Browser: a white-mask bitmap (`buildWaveImg`) + `source-in` colouring + `Path2D` clip. Native
(`deckcard`): a cached `[]float64` envelope keyed by `PeaksKey` (the track's `ArtKey`).

## Peaks pipeline

`internal/waveform` - async single-flight ffmpeg decode → uint8 max-abs peak buckets (~60/s),
cached on disk keyed by `DeckSnapshot.ArtKey`. Generated on first play; the panel shows a flat
baseline until it lands, then swaps in. The browser fetches `/peaks/<artKey>.bin`
(`[u32 LE durationMs][u8 peaks]`); native sinks read the resolver directly.

## Appearance - one editor, every output (`overlay-style.json`)

All styling is edited live in the **browser overlay's edit mode** (`?edit=1`) and persisted to
`overlay-style.json`. Every renderer reads that file, so edits apply to the browser source, the PNG
renderer, and Spout/Syphon/PipeWire at once - no second native editor, no drift.

- **Colours + multi-stop gradients** for the waveform fill and the panel background (linear/radial;
  angle `0°`=L→R, `90°`=T→B). The editor's gradient widget: drag stops, add on empty-bar click,
  remove on dbl-click, angle slider, solid↔gradient toggle.
- **Per-band EQ + per-direction FX colours** (`eqColors`: low/mid/high + hp/lp). The curve base
  colour interpolates low→mid→high across x and cross-fades to the cut colour by the filter weight.
- **Card border + radius** (`card`: border none/solid/glow, width, colour, radius).
- **Gradient + colour presets** saved **server-side** (`overlay-presets.json` via `POST /presets`),
  so they survive an OBS browser-cache refresh (localStorage did not).

Schema (`internal/overlaystyle.Style`, optional fields; legacy flat `waveColor`/`waveBgColor` still
read when the `waveFill`/`waveBg` objects are absent):

```jsonc
{
  "zoomSeconds": 20, "playheadPct": 0.75,
  "waveOpacity": 1, "waveBgOpacity": 0.85,
  "waveFill": { "type":"gradient", "grad": { "kind":"linear","angle":90,
                "stops":[{"p":0,"c":"#08F79B"},{"p":1,"c":"#000"}] } },
  "waveBg":   { "type":"solid", "color":"#0a0a0e" },
  "eqColors": { "low":"#ff3030","mid":"#08F79B","high":"#3060ff","hp":"#e23ad0","lp":"#22c9e0" },
  "card":     { "border":"glow","borderW":2,"borderColor":"#F70864","radius":14 }
}
```

`config.OverlayWaveform` keeps the enable + zoom/playhead and flat colour **defaults** (used when
`overlay-style.json` has no override); the live appearance comes from the style file.

## OBS browser-source auto-management

The browser overlay can self-manage its OBS source over obs-websocket (Settings → Overlay → Live
overlay): create the scene + browser source sized to the OBS canvas, nest it in the program scene,
and `refreshnocache` on (re)connect so style changes don't need a manual source reload. Style +
layout edits also push live over SSE, so a running source updates without any reload.

## Smooth scroll cadence per output

Browser (client rAF) + video-share (30fps frame ticker) glide. The PNG sink also runs a **30fps
ticker, but only while the waveform is enabled** (gated behind the signature throttle so a static
frame isn't rewritten), so the PNG scrolls too.
