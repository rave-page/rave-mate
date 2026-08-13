# UI ownership: Go → Zig

Owner directive, 2026-08-13, **hard requirement**: Zig (plus C/C++ where a library demands it) owns
the GUI and rendering layers. Go keeps computational + service logic — workers, media producers,
protocols, storage, app state.

This file is the migration's map. `CLAUDE.md` carries the short rule; the surface-specific half
lives in `SDL_WEBVIEW_SURFACE_DESIGN.md` §1 directive #2 + §4.3.

## Where we actually are (measured, not assumed)

| Layer | Today | Target |
|---|---|---|
| Window, WebView2 host, input, screenshots | **Zig** (`native/zigui/src/shell/`, own process) | unchanged |
| HTML rendering | **Zig** for ~38 view modules; Go renderer kept as byte-golden fallback | Zig only; Go renderers deleted per surface as parity is signed off |
| View-state resolution (business → view struct, i18n, number formatting) | **Go** (`internal/webui/render_*.go` state builders) | **Zig** |
| Wire (state → child) | generated both sides from one schema (`internal/zigui/wiregen`) | becomes the *data* feed, not a render mirror |
| Action handling (`data-act` → state mutation) | **Go** (`internal/webui/*_actions.go`) | split: view-local behaviour → Zig; anything mutating app state stays a Go command |
| Runtime JS (transport, `__patch`, `__rt`, `__mst`, surfaces) | emitted from Go `shell.go` as a string | **Zig** (it is view transport, and the child already owns the DOM) |
| Render surfaces / visual tree / presenter | — | **Zig only** (never the daemon) |

The inversion is smaller than it looks: rendering already crossed. What is left is *who resolves
state* and *who owns the transport*.

## Scope, measured (2026-08-13)

| Body | Lines | Note |
|---|---|---|
| `internal/webui/render_*.go` | ~27.0k | state builders + Go renderers (renderers are the deletable half) |
| `native/zigui/src/*.zig` (views) | ~29.9k | already renders the migrated surfaces |
| `runtimeJS` block in `shell.go` | ~0.93k | step 2, moves whole |
| `internal/webui/*_actions.go` | ~15.3k | step 5, splits - most of it mutates app state and STAYS Go |

Biggest Go-side view modules, Go lines vs their Zig counterpart (0 = no Zig side yet):
`settings` 2464/509 · `library` 2255/163 · `library_state` 1647/0 · `peers` 1391/645 ·
`player` 1266/772 · `publish` 1183/608 · `live` 1166/528 · `vrchat_groups` 1060/0 ·
`editor` 1021/631 · `editor_video` 856/405 · `motion` 805/335 · `midictl_controllers` 706/0 ·
`library_cueedit` 628/0 · `worlds_modals` 582/0 · `publish_remote` 527/0

Read that as the work list, not a ranking: the modules with a `0` never got a Zig side and are
where "Zig-first" bites immediately; the rest need their STATE BUILDER moved, then their Go
renderer deleted.

## Sequence (each step ships + soaks on its own)

1. **Surfaces are born Zig-owned.** Everything in the SDL/DComp plan (P1→P6) is written in the
   child from the start. No `internal/webui/surface.go`, ever. This is the first brick of the new
   ownership, and it is the current work.
2. **Runtime JS moves to Zig.** `shell.go`'s `runtimeJS` const → `native/zigui/src/shell/runtime/`
   (one file per block: transport, patch, rt, mse/mst, surfaces, ctl introspection). Go stops
   emitting view transport. Gate: `ctl snapshot/click/type/read/set` parity + the screenshot sweep.
3. **State resolution moves per view, newest-first.** For each view: port its `*State()` builder
   into its `.zig` module, feed it the raw data over the wire instead of a resolved view struct,
   delete the Go renderer + its golden test once byte-parity is proven against the previous build's
   output. i18n resolution moves with it (the locale JSONs are already data).
4. **Go renderers retire.** When a module has no Go renderer left, its `zigWire(...)` fallback
   argument goes too, and the golden test becomes a Zig unit test.
5. **Action layer splits.** View-local acts (toggle a disclosure, move a slider, drag a grip)
   handled in the child; acts that change app state stay a Go command over the existing lane.

## Rules while migrating

- **Never add new view logic that exists only in Go.** New UI is Zig-first; if a Go renderer is
  written at all it is as the temporary golden reference, in the same commit as its Zig side.
- The byte-golden gate stays until a surface is fully ported — it is what makes this migration
  safe, not bureaucracy. Delete a Go renderer only when its Zig side is the sole producer.
- `ctl` parity is the acceptance test for every step (`snapshot/click/tap/type/read/set/
  screenshot-all`). A step that degrades ctl is not done.
- Cross-platform: the presenter is per-OS behind one seam (win D3D11+DComp, mac CAMetalLayer,
  linux GL/Wayland subsurface — the latter two designed-for, unbuilt). SDL3 stays for input/HID,
  device enumeration, and as a *renderer* (SDL_GPU) feeding our presenter — not as the window or
  present layer.
- Fyne is unaffected: it is the legacy renderer behind the same `frontend` seam and retires on its
  own schedule.
