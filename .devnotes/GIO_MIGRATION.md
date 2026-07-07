# GIO_MIGRATION.md - Fyne → Gio, incrementally

Plan of record for replacing Fyne with Gio (`gioui.org` v0.10.0, SUPPLY_CHAIN.md).
Why: density (24dp controls / 4–6dp pads / 12–13sp text vs Fyne's chunky defaults),
immediate-mode control over media/graphics surfaces, pure-Go d3d11 on Windows (no cgo).
Porting unit: `internal/giokit` (theme = brand tokens + Orbitron via the shared
`internal/ui/fonts` embeds, dense widgets, window host, ctl Registry).

## Phase order (media/graphics first, forms LAST)

1. ✅ **Foundation** - giokit theme/widgets/window/registry; gio dep soaked + pinned.
2. ✅ **Player window** - `giokit/playerwin`: mpv `--wid` into the Gio HWND + dense
   transport.
3. 🔶 **Player everywhere** - SHIPPED: playerwin is the DEFAULT pop-out target
   (`openPlayerWindow`); `features.player.gioWindow` is tri-state (unset/true = Gio,
   explicit false = legacy - Settings → Library & media; darwin/mpv-missing auto-fall
   back to the Fyne path). SHIPPED: collapsible waveform strip under the video rect
   (giokit.Wave: vector clip.Path bars, scrub-to-seek, deckclock velocity-PLL playhead;
   peaks from the shared `ui.resolvePeaks` store-cache + probe-worker source - video
   files analyse only when the strip first opens). REMAINING: jump-marker rail (.cue
   markers) + cue snapping, then retire the Fyne pop-out path.
4. **Visual editor canvas** - port `ve_canvas`/`ve_editor` rendering to a Gio window
   (immediate-mode fits the layer/blend editor far better than Fyne rasters).
5. **Waveforms / studio detail** - full waveform view (zoom/pan, cue/beatgrid overlays)
   + player panel as a Gio aux window; giokit.Wave is the seed. Evaluate perf against
   the Fyne raster path before switching defaults.
6. **Library grid** - dense list/grid (giokit.List) in a Gio window; the biggest UX win.
7. **Forms/settings LAST** - settings tabs, dialogs, wizards stay Fyne until giokit
   grows inputs/selects/dialogs; port only when the main window flips.
8. **End state** - Gio main window owns nav + all surfaces; tray stays `fyne.io/systray`-
   equivalent (extract or replace), notifications move to a native path; remove
   `fyne.io/fyne/v2` from go.mod.

## Coexistence rules (Fyne main + Gio aux, single process)

- Fyne owns: main window, tray, notifications, dialogs, config UI - until phase 8.
- Gio surfaces are AUX WINDOWS opened from Fyne (`giokit.NewWindow`), each with its own
  event-loop goroutine. Windows/Linux fine off the main thread; **macOS: Gio needs the
  process main thread (Fyne owns it) → Gio aux windows unsupported on darwin; every Gio
  entry point keeps a non-Gio fallback** (playerwin: mpv popout / Fyne player).
- Cross-framework calls: Gio→Fyne via `fyne.Do` (e.g. playerwin export → Fyne dialog);
  Fyne→Gio via `Window.Invalidate`/state + never touching Gio widgets off its loop.
- One theme truth: brand tokens live in giokit theme + `internal/ui/theme.go`, both fed
  by `internal/ui/fonts`. Consolidate the remaining per-package TTF copies
  (visualeditor/deckcard/vroverlay/mediaeditor) onto `internal/ui/fonts` opportunistically.
- Feature flags: every migrated surface ships behind an add-only config bool
  (zero-value = Fyne) until verified live, then flips default via a `*bool` tri-state
  (nil = new default, explicit false = legacy opt-out) - `features.player.gioWindow`
  is the template; no config version bump needed.

## ctl parity (non-negotiable before a surface's default flips)

Fyne surfaces are verified via `rave-mate ctl snapshot/tap/click`. Gio surfaces are now
equally drivable - SHIPPED: `giokit.NewWindow` auto-registers each window's Registry in
a package-level window registry (`WindowOpts.CtlName`; unique slug IDs, e.g. `player`,
`player#2`), and the instance server + CLI expose

- `rave-mate ctl gio-snapshot` - list open Gio windows (`id<TAB>title`)
- `rave-mate ctl gio-snapshot <windowID>` - labeled-control tree (kind "label" bounds)
- `rave-mate ctl gio-tap <windowID> <controlID>` - synthesized activation (queued to the
  window's next frame; e.g. `gio-tap player wave.toggle`)

Screenshot parity comes free since Gio windows own an HWND (OS-level capture, as
`screenshot-region` does today).

## Definition of done per phase

Build/vet/test/lint clean (both tag sets) + live ctl verification of the golden path +
neighbouring surfaces, on the running app. No surface flips its default without ctl
parity + a session of real use.
