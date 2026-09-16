# Design & UX rules

Binding for every agent doing visual work in `internal/webui` (and the Zig render
layer that mirrors it). Ported + adapted from rave.page's design system (2026-09-07
external designer review + the perception research in `PRINCIPLES.md`), fitted to
rave-mate's architecture (a Go-rendered HTML/CSS **webview** with a global-CSS
**recipe** kit, not a Tailwind-utility component package) and domain (a native
desktop live-set control app, not a mobile events web app). `PRINCIPLES.md` is the
research layer; this is the rule layer. A rule here binds like `CLAUDE.md`.

Sources of truth, one each:
- **Tokens** — `internal/webui/assets/ds/colors_and_type.css` `:root`
  (`--color-brand-*`, `--color-success|warning|error|info`, `--space-*`,
  `--radius-*`, `--text-*`, `--motion-*`, HSL `--background`/`--foreground`/
  `--muted-foreground`/`--border`). Never hardcode hex/px in a view; never invent a
  raw palette value. New token → add here with a one-line intent comment. These are
  **copied sources** from `rave-page-design-system/` — sync from there, never fork.
- **Recipes** — the `.rp-*` class recipes in `assets/ds/styles.css` (`.rp-card`,
  `.rp-chip`, `.rp-btn`, `.rp-badge`, `.rp-field`, `.rp-switch`, `.rp-empty`,
  `.rp-stat`, …) plus the Go helpers that emit them (`internal/webui/components.go`:
  `emptyState`, the select-box builders, …). This is the "kit".
- **Capability map** — `internal/webui/CAPABILITIES.md`: one capability → one
  canonical recipe/helper.

## Architecture note (why "recipe-first")

rave.page consumes a `@rave-page/ui` package of Tailwind primitives; rave-mate
renders HTML from Go and styles it with semantic global classes. The rules are the
same; the unit differs. Here the "primitive" is a **class recipe** (`.rp-chip`,
`.rp-card`) plus the Go helper that renders it. "Kit-first" reads as
**recipe-first**: a new visual pattern is added as a recipe (+ a helper in
`components.go`), then consumed — never as a third inline-styled variant in a
`render_*.go`. **Zig direction:** per CLAUDE.md the GUI is migrating to
`native/zigui/`; new view logic is Zig-first and the Go `render_*.go` renderers are
the byte-exact golden reference. A recipe/rule added here is the contract BOTH sides
render to — the golden test pins them together.

## Rules

- **Recipe-first.** New visual pattern → add/extend a `.rp-*` recipe (+ a
  `components.go` helper), then consume it. A third hand-rolled variant of an
  existing pattern (an inline-styled pill, a bespoke card box) is a defect. Grep
  `styles.css` + `components.go` before writing new `border-radius: 999px … padding`
  or an inline-styled box in a `render_*.go`.
- **Chips vs badges (P16).** Every pill that is a **control** (filter, toggle,
  sub-nav, tier selector) = the `.rp-chip` recipe (border + hover + real target).
  Every pill that is **state/label** = `.rp-badge` (`--success|warning|error|info|
  secondary|outline`, `.rp-badge__dot`) — no per-call colour override, no ad-hoc
  coloured pill, no control signifier on a static label. A fact that is not
  clickable is plain text in position, not a pill.
- **Colour = intent (P4).** One accent hue per surface (the brand hue,
  `--color-brand-base`). Category (device kind, source, log level, tab, section) is
  **position** — a lane/section/column — never a colour. State is a **status token**
  (`.rp-badge--*` / `--color-success|warning|error|info`), used sparingly. Never
  rainbow-colour a repeated grid of rows/cards; never add a second accent to mean
  "new/saved". Gradient-clipped text is allowed ONLY on the brand wordmark, never on
  a heading, value, or body run.
- **Type.** Display face (`--font-display`, Orbitron) for headings, section labels,
  eyebrows, big numerals, brand chrome. Body/UI text — paragraphs, inputs, chips,
  table cells, log lines — uses the **body face** (`--font-body`), which must be a
  readable sans, NOT a display face (rave.page's #1 readability finding: display
  faces are not text faces). CLAUDE.md § Design tokens already states this
  ("Orbitron = display; sans = body; mono = logs"). **Known drift (see Decision
  log):** `colors_and_type.css` currently sets `--font-body`/`--font-mono` to
  Orbitron — a tracked defect, not a licence to keep shipping body copy in a display
  face. Numerals that must align (counts, times, sizes, bitrates) use
  `font-variant-numeric: tabular-nums`.
- **Contrast.** Body text ≥ 4.5:1, glyphs/large text ≥ 3:1 on their surface.
  `--muted-foreground` (#a3a3a8, ≈7.9:1) is the lowest body-text colour; tertiary
  meta wants a dedicated checked token (`--muted-foreground-2`, to be added — see
  Decision log), never an ad-hoc `opacity:` or `rgba(…/.4)` on text.
- **Card grammar (P8).** One card recipe per row context (`.rp-card`). One left
  edge for every row; leading icon/media in a fixed gutter so text aligns with or
  without it; symmetric inset (`--space-4`/`--space-5`); the action/footer row
  (`.rp-card__foot`) pinned to the bottom so ragged content never misaligns a grid.
  Render nothing for an empty field — never a `min-height` placeholder row. Identity
  + state + the one action live on the same card.
- **IA budget per surface (P1, P16).** One nav device per axis. One surface per
  concept — a live route/session appears once. A primary control strip is ≤ 4
  equally weighted controls; the rest live behind one disclosure/menu. Exactly one
  filled primary per surface (`.rp-btn--primary`/`--go`); the rest
  `.rp-btn--outline`/`--ghost`; `.rp-btn--destructive` never beside the primary.
  One counter per concept; no vanity metrics.
- **Default to the live/relevant state (P2).** A dense surface opens on what is
  true and actionable now (connected device, running route, current session,
  last-used target), ordered live/recent, never alphabetical, never a blank "pick
  first". Every list has a real `.rp-empty` (`emptyState`) — icon + what's missing +
  what to do next — never a bare blank.
- **Motion (P5, P14).** ≤ one continuous motion cue per viewport, marking live/now;
  transitions ≤ `--motion-slow`. All motion degrades under `prefers-reduced-motion`
  (the block in `colors_and_type.css` — keep new animated recipes inside it) AND
  yields to the activity governor while a set/stream runs (`UIAnimAllowed`). The
  static rendering is the design.
- **Density as a shape (P7).** Level/VRAM/fps/bitrate/route-health/waveform render
  as a shape (bar/spark/area in the single hue), not a wall of counters or a
  per-field badge storm. A counter stays only where the number IS the answer.
- **Media never autoplays (P11).** Player, world/VRChat thumbnails, flipbook and
  camera-path previews load/play on explicit press; every fact a preview carries is
  also in text beside it.
- **No browser-native UI.** No `<select>`, `alert/confirm/prompt`, native
  date/color pickers, or raw `<input type=file>` picker chrome. Use the kit
  select-box helpers (`components.go`), the app's dialog recipes, and the in-app file
  browser (`internal/localmedia` `ListDirectory`, surfaced through the webview),
  never a native OS dialog for in-view browsing.
- **Reachability three ways (P15).** Every control is reachable by mouse, keyboard
  focus order, AND the `ctl` control plane — each interactive element carries a
  stable id/label so `ctl snapshot`/`click`/`read` can drive it. This is the a11y
  contract and the test contract at once.
- **One capability, one component.** `CAPABILITIES.md` maps each UI capability
  (select, chip, badge/status, empty state, card, search field, file browser, …) to
  its canonical recipe/helper. Before building or fixing a capability: read the map,
  `./harness.ps1 context "<capability words>"`, grep synonyms
  (`Select|Chip|Badge|Card|Field|Empty|Browse|Picker`), then extend the canonical
  one. A sibling for the same job is a defect. Discovery is per capability, not per
  route.
- **Rules can be wrong.** When a reviewer traces a defect to a rule here, fix the
  rule (dated Decision-log entry below), then the code — never work around it
  silently.
- **Gates.** `go build ./... && go vet ./... && go test ./...` clean (incl. the
  `internal/webui` golden tests, which pin the Go render == the Zig render). Then the
  **binding visual gate** (CLAUDE.md): `rave-mate ctl screenshot-all <dir>` against
  the running build — sweep every tab, eyeball the shots, fix `⚠OVERFLOW` and obvious
  issues even if pre-existing. A dense-surface composition change also gets the page
  audit below. (A design-lint ratchet over `render_*.go` — banned hex, inline
  styles, opacity-on-text, chip/badge confusion — is a candidate follow-up; see
  Decision log.)

## Page audit (before a route composition change lands)

When a surface gains/loses/reorders a section, control, or counter: `ctl
screenshot-all` (or `ctl screenshot` per state) at the normal and a narrow window
width; list its sections + controls + counters; assert no concept appears twice;
count distinct chip/badge recipes (target 1 each); confirm exactly one filled
primary. A green golden path on one card is not a pass. Put the audit in the PR/commit
notes.

## Decision log

Dated changes to the rules themselves. An entry here overrides older prose above.

- **2026-09-16 — one in-app file browser, native dialog demoted to an escape hatch.** The Browse
  contract (`pick-dir:`/`pick-file:`/`pick-save:`) opened a NATIVE Windows dialog everywhere, in
  direct conflict with the "No browser-native UI … the in-app file browser … never a native OS
  dialog for in-view browsing" rule above (the code had drifted from the doc). It now opens an
  in-app modal browser (`internal/webui/pick_browser.go` + `pick_browser_render.go`, the `.pk-*`
  recipe), backed by `internal/localmedia` (listing) + the new `internal/shellplaces` (OS Quick
  Access), reusing the existing `.rp-*`/`.libnav`/`.field-input` recipes — a pure-Go modal on the
  established `libRenameModal` pattern (patched into `__modal`, no Zig twin, so no golden churn).
  It replaces the native dialog at every ~26 Browse sites via the unchanged `pick-apply:` contract,
  works headless + in remote/virtual sessions (it browses THIS daemon's filesystem, so no dialog
  pops on the controlled machine — the old `library.mirror.noPicker` refusal is gone), and mirrors
  the OS Quick Access pins in a **QUICK ACCESS** sidebar group (COM enumeration; Win11 26xxx does
  not expose a per-item pinned flag, so the group is the Explorer Quick-Access folder set — a
  faithful superset of the user's pins). The native dialog survives ONLY as the "System dialog…"
  escape hatch (Windows). `CAPABILITIES.md` File/dir browser row updated. Follow-up: the Library
  Browse tab is still a sibling in-app browser (shares the data layer, separate renderer) — full
  render unification (one Zig-mirrored component, two hosts) is tracked, not done here.
- **2026-09-16 — Run now = schedule semantics; single-file is the secondary; runs are
  coordinated.** The Automations "Run now" modal used to make you pick ONE file (`RunManual`, no
  match rules). It now DEFAULTS to the automation's own conditions — a sweep identical to a
  schedule fire (`Service.RunSweep` → the shared `sweepRun`, so manual and scheduled can never
  diverge) — and renders the active conditions as `.rp-badge` state badges, a bounded live preview
  (name · age · size, "and N more", a total line), a real `.rp-empty` with why-hints when nothing
  matches, exactly ONE filled primary "Run on N files", and a secondary switch back to the
  single-file flow (`RunManual`, trigger `manual-file`, keeping the ignores-match copy). Every run
  path (schedule sweep, watch-file, manual sweep, manual file, interactive) funnels through the new
  `internal/automation` run coordinator: an automation never overlaps itself (a second sweep
  coalesces into one pending run, shown "coalesced at HH:MM"), path-overlapping automations
  serialize, heavy (ffmpeg) chains share `MaxHeavyRuns` slots (default 1) and defer while a stream
  is live — reusing the ONE `governor.BackgroundAllowed` gate, never a second — and the queue is
  bounded. The tab gained a live status region (running · queued with reasons; version-gated
  tick), a per-card state badge (running/queued/deferred), schedule rows show coalesced, and the
  Run-now modal states a pending conflict BEFORE you press Run ("Will wait — …", the primary
  becomes "Queue run"). Badges are state, chips are controls (P16), one filled primary (P16), the
  preview + queue + coalescing are all bounded. Wire: `AutoRunNow`(110) + `AutoBodyState`(42)
  appended fields; nested `ArBadge`/`ArPrevRow`/`AutoCoord(Row)` added; no new root id (the new
  state nests under the existing dialog/tab roots). Go `render_*.go` + `native/zigui` mirror +
  golden (v1 JSON + v2 RZW1) pin them byte-for-byte. Coordinator rules: `.devnotes/
  SCHEDULE_CONDITIONS_SUMMARY.md` § Run coordinator.

- **2026-09-16 — via-peer session federation UI.** When an external-platform feature (VRChat,
  Twitch, World-Sync) is served by a paired instance because there is no local session, the
  surface shows the borrowed identity as a state **badge** (`.rp-badge` / `statusRow`), a **hint**
  line for WHERE the session lives ("via peer &lt;name&gt;"), and ALWAYS keeps the local sign-in
  control (a local sign-in overrides federation and holds the session on this instance). Never a
  control-styled pill for the via-peer fact, never hide the local sign-in. Established by the
  VRChat via-peer trio; Twitch + World-Sync follow it. See `.devnotes/PEER_LINK_SUMMARY.md`
  § Session federation.

- **2026-09-15 — conventions ported.** rave.page Design & UX rules +
  `PRINCIPLES.md` (perception research) adapted to rave-mate — recipe-first for the
  Go-webview `.rp-*` kit; desktop control-surface domain. Same P1–P16 numbering as
  rave.page/shingadayo. P10 marked N/A (no per-item energy signal); P3 reframed to
  the live-state landmark; P15 reframed from 44 px-touch to pointer + keyboard +
  `ctl` reachability (rave-mate is a desktop, pointer-precise app). Added
  `CAPABILITIES.md`.
- **2026-09-15 — typography drift flagged (not yet fixed).** `colors_and_type.css`
  sets `--font-body` and `--font-mono` to Orbitron ("the WHOLE brand"), contradicting
  both CLAUDE.md § Design tokens ("Orbitron = display; sans = body; mono = logs") and
  the research (display faces are not text faces). The rule stands as written above;
  aligning the CSS (a readable body/mono face; Orbitron kept for display/brand
  chrome) is a visual change to make + verify on the webview via `ctl screenshot-all`
  under a separate, owner-reviewed change — tracked, not silently applied here.
- **2026-09-15 — tokens to add.** `--muted-foreground-2` (a contrast-checked
  tertiary-text token, ≈5.5:1) for timestamps/counts, so tertiary text stops reaching
  for opacity; and a `tabular-nums` utility/recipe for numeric columns. Until added,
  use `--muted-foreground` for all secondary text and accept non-aligned numerals —
  never an opacity-derived text colour.
- **2026-09-15 — design-lint ratchet (candidate).** rave.page/shingadayo enforce the
  rules with a `design:check` script; rave-mate's equivalent is the golden tests +
  `ctl screenshot-all`. A grep/`go test`-based ratchet over `render_*.go` (banned raw
  hex, inline `style=`, opacity-on-text, `.rp-chip` used for state / `.rp-badge` used
  as a control) is a candidate addition once the current tree is clean — deferred to
  avoid false positives on legacy views.
- **2026-09-16 — Live surface regrouped into four chunks (P1).** The Live tab was 11
  flat full-width sections. It now groups into four named chunks + the ambient bottom
  strip: **STREAM & PICTURE** (auto-live landmark + OBS cockpit), **DECKS** (the deck
  grid is the single now-playing truth), **SIGNALS** (signal sources + the Link phrase
  row folded beside), **SYSTEM** (connection status + net/timing/perf graphs, behind
  one disclosure, collapsed by default — least glance-critical, P1/P2). Chunk titles are
  resolved Go-side (`live.group.*`) and carried on `liveState` (wire fields 35-38) so
  the Zig renderer gets the localized name. Fragment ids are unchanged, so every ~1 Hz
  tick still lands. `render_live.go liveHTML` + `native/zigui/src/live.zig render` are
  the byte-exact pair (golden gate).
- **2026-09-16 — now-playing LCD retired (P8).** `#live-np` duplicated the audible deck
  tile. The deck grid is now the single now-playing truth: the LCD is no longer rendered
  or ticked (removed from `liveHTML`, `live.zig`, `liveTickIDs`, `liveTickLegacy`,
  `tick.zig runLive`). Wire id 12 (`LiveNP`) + the `LiveState.NP` field + `liveNPHTML`
  are kept RESERVED and still parity-tested (`assertFrag "np"`); a clean removal of the
  message + field is a follow-up. Strip duplicates removed too: the recorder file and the
  system-headroom figure each appeared twice (transport rec-state + strip; SYSTEM perf
  well + strip) — the strip is now ambient overflow only (Twitch login / OBS / capture /
  DMX / timecode).
- **2026-09-16 — one filled primary on Live (P16).** The tab had two filled primaries
  (`tc-start` `rp-btn--go` + every cockpit stream button `rp-btn--primary`). Now exactly
  one: **arm/stop recording** (`arec-toggle` → `rp-btn--primary`) — capturing the set is
  the highest-stakes, one-way action here; streaming is auto (OBS-driven) and timecode is
  secondary, so both are `rp-btn--outline`. Mirrored in `live.zig`.
- **2026-09-16 — sparklines: series-by-hue → small multiples (P4/P7).** The Live SYSTEM
  graphs encoded series by HUE (net 4 hues, timing a 5-hue cycle, perf 4 hues) with inline
  `style="color:#…"` legends — colour as category, the exact P4 violation. Replaced with
  **small multiples**: one spark per series, stacked, single brand hue (`sparkMint`; an in/out
  or app/sys pair uses one luminance step, `sparkMintDim`), the label at the LEFT of its own
  row (identity by position), no coloured legend, no inline colour. Timing = one row per peer.
  `graph.go` palette cut to the one hue + its dim step; `sparkMultiHTML` is the builder; net/tim
  keep the `liveGraphSt` shape (rows in `Graph`, a plain summary caption in `Legend`), perf's
  headroom line uses `.spark-head` (the hue via a class, not inline). New CSS `.sparkmulti`/
  `.spark-row`/`.spark-lbl`. No golden-fixture churn (the graph fields are raw inputs both
  renderers embed identically); the only Zig change is the perf head span.
- **2026-09-16 — disclosure recipe added (`.rp-disclosure`).** New capability: a
  collapsible titled group, used for the Live SYSTEM chunk to keep the least-critical
  content out of the first glance (P1/P2). Recipe in `assets/ds/styles.css`
  (`.rp-disclosure` + `.rp-disclosure__sum`), consumed inline over the recipe as a
  native `<details>/<summary>` (semantic, keyboard- and `ctl`-reachable; not a banned
  browser dialog). Sub-headings within a chunk use `.sec-sub` (a lighter `.sec-title`).
  Added to `CAPABILITIES.md`.
