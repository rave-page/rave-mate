# Zig UI guide — options + no-regression migration plan

## Where our UI actually lives

`internal/webui`: **Go renders HTML/CSS strings, drives the DOM through a webview
binding** (C/C++ lib under webview_go). No web server, no JS framework; one small JS
transport runtime (`shell.go`). Design system = copied rave.page CSS + Orbitron in
`assets/ds`. ctl (snapshot/click/tap/type/read/set/screenshot) asserts against the DOM.

That architecture is the asset. The renderer is native code + a C webview — the language
behind it is swappable.

## Zig UI options (assessed)

| Option | Verdict |
|---|---|
| **webview via C ABI (same lib we use today)** | **RECOMMENDED.** Zig consumes `webview.h` natively (`@cImport`/translate-c, no binding layer needed). HTML/CSS/DS assets, layout, theming carry over byte-for-byte → zero visual regression by construction. |
| dvui | Immediate-mode native Zig; young, no HTML/CSS, would repaint every view + lose DS tokens. No. |
| capy | Native-widget abstraction; alpha-grade, widget set nowhere near our surface. No. |
| zgui (Dear ImGui) | Tool-UI aesthetic, not brand UI. Only ever for debug panels. |
| raylib/raygui | Game-loop rendering; wrong fit for a tray/dashboard app. No. |

## No-regression migration plan (mirrors the proven Fyne→webview playbook)

1. **Contract first:** the DOM is the spec. Same element structure, same `data-*` action
   ids, same action-message protocol (`{t,id,form}`), same i18n keys. ctl tooling then
   works identically against a Zig-rendered view — our regression harness comes free.
2. **Render-layer bridge (phase A):** Zig view renderers exported over the C ABI
   (`rz_render_<view>(state_json) -> html`); Go webui calls them instead of its own
   `render_*.go` for migrated views. Go shell, actions, eventbus stay. One view at a
   time, diffable.
3. **Regression gates per view:** `rave-mate ctl snapshot` tree diff (structure) +
   `ctl screenshot-all` pixel eyeball (report.txt OVERFLOW findings) before/after; the
   view flips only when both are clean at default + narrow widths.
4. **Shell swap (phase B, later):** once most views render from Zig, port `shell.go`
   transport + window/tray glue to a Zig binary owning the webview loop; Go daemon keeps
   business logic, talks to the UI process over the existing stdio/ctl patterns.
5. **Never fork the design system:** DS CSS + tokens stay the single source; Zig code
   never hardcodes hex/px (same rule as Go/theme.go today).
6. **i18n:** same 7-locale catalogs, embedded; keys shared so translators see one set.

## Render bridge (phase A) — SHIPPED pipeline

`native/zigui/` (static lib `libraveui.a`, C ABI `rz_ui_*`, ABI-versioned) +
`internal/zigui` (cgo binding, tag `zigui`; pure-Go stub untagged) + per-tab bridge in
webui: Go resolves a `<tab>State` struct (all data + RESOLVED i18n strings — catalogs
stay single-source in Go, rule 6), marshals to JSON, Zig renders HTML byte-identical
to the Go renderer. The Go renderer STAYS: untagged fallback + golden reference.
Escaping contract: Zig `Html.esc` == Go `html.EscapeString` (`& ' < > "` →
`&amp; &#39; &lt; &gt; &#34;`), verified in `native/zigui/src/html.zig` tests.

Build: `make zig` builds zigcore AND zigui (scripts/build-zig.{sh,ps1}); `ZIG=1` adds
tags `zigdsp zigui` (one switch = all Zig natives). Link gotcha: std.json float parsing
pulls f128 intrinsics (`roundq`) not in bundled compiler-rt → binding adds
`-lquadmath` on windows.

### Porting recipe (per tab)

1. Split `render_<tab>.go`: impure state builder (svc/cfg/locks/`i18n.T`) vs pure
   `<tab>HTML(st)` / `<tab>BodyHTML(st)` renderers — zero DOM change (keep helpers).
2. Mirror the pure renderers in `native/zigui/src/<tab>.zig` on `html.Html` +
   `components.zig` (port missing components.go helpers there verbatim, byte-exact).
3. Export `rz_ui_render_<tab>` (+ `_body` for tick-patched fragments) in `root.zig` +
   `include/raveui.h`; add wrappers in `internal/zigui` (cgo file AND stub).
4. Bridge: `render<Tab>()` builds state → `zigui.Available()` → Zig, `ok=false` → Go.
5. Golden test (tag `zigui`) in webui: fixtures empty/unavailable/populated/escaping/
   long/unicode; assert Zig == Go **byte-identical** (full + body).
6. Gates: `zig build test` (native/zigui) · untagged `go build/vet/test ./...` ·
   `go test -tags zigui ./internal/webui -run TestZig` with lib built · live ctl
   snapshot/screenshot after merge.

### Migrated tabs

| Tab | Status | Golden test |
|---|---|---|
| appgroups | Zig (`native/zigui/src/appgroups.zig`) | `TestZigAppGroupsGolden` |
| logs | Zig (`native/zigui/src/logs.zig`; full + `#log-view` lines fragment) | `TestZigLogsGolden` |
| motion | Zig (`native/zigui/src/motion.zig`; full + `#mo-body` fragment) | `TestZigMotionGolden` |
| (all others) | Go | — |

First-port notes: appgroups chosen over logs as pilot — logs drags in the smartSelect
primitive + filter-state locking; appgroups is the smallest full tab yet exercises
panel/emptyState/badge/dot/btn/data-act/i18n interpolation + the ~1 Hz `tickPatch`
body funnel. Zig 0.16: `std.ArrayList` is unmanaged (`.empty` + alloc per
call); never name an identifier `i18n` (`i<digit>` reserved) — state JSON keeps i18n
strings as flat resolved fields.

Logs-port notes (tab #2): smartselect.go split into register (`ssRegister`) +
resolved state (`selState`/`ssResolve`/`resolveSelectBox`) + pure renderers
(`selHTML`/`selInnerHTML`/`selListHTML`) — `ssInner`/`ssListHTML` now delegate, so
the live ss patches and the Zig port share ONE markup source. Filtering + Unicode
`strings.ToLower` (select filter, toggleRow data-label via `toggleRowDL`) resolve
Go-side; Zig only walks rows. Components ported to `components.zig`:
- `toggleRow(label, data_label, act, on)` — Go `toggleRowDL`; data_label = Go-lowered label.
- `subTabs(act_prefix, active, []Tab{val,label})` — segmented control; act = prefix++val.
- `selectBox(Select)` / `selectInner` / `selectList` — smart select from resolved
  `Select{id,label,curLabel,open,filter,rows[]{val,label,sub,badge,cur}}`; rows are
  pre-filtered (only filter-passing rows arrive), empty rows ⇒ `ss-none` "No matches";
  literals "Type to filter…"/"No matches" are hardcoded English (parity w/ smartselect.go).
- `btnGated(label, why)` · `hint(tone, text)` (empty tone → "info") ·
  `sectionOpen/Close(title)` — ready for the next tabs (automations/settings).

## Dev rules when touching UI during migration

- A view lives in exactly ONE renderer at a time (Go or Zig) — no dual maintenance.
- Any new view goes into the CURRENT majority renderer until phase B lands.
- ctl parity is non-negotiable; a view that breaks snapshot/click addressing is a
  regression even if it looks right.
- Screenshot sweep (`ctl screenshot-all`) after every migrated view, both themes if/when
  a light theme exists.

Motion-port notes (tab #3): the impure half owns everything numeric + every
Go-computed fragment - campath viewer SVG (`cpvView`), skeleton/mesh preview
(`moViewHTML`), render progress, `tipTopic` cards, `cpvPlayBtn` - and the state
carries them as trusted raw HTML; Zig only frames them. Numbers never cross as
floats-to-be-formatted: `moSliderSt` carries the floats (Go path feeds the shared
`slider()`) AND their `trimNum` strings (Zig path), so the golden gate detects drift
instead of Zig re-implementing Go float formatting. `moCamPathInfo` split into
`moCamPathInfoText` (state) + escaping wrapper (the live `#mo-cp-info` patch).
Components ported to `components.zig`: `masterDetailOpen/Mid/Close`, `sectionOpenTip`,
`statusRow`, `slider` (+`Slider` state struct - all numbers pre-formatted Go-side).
Gotcha re-confirmed: a *conditionally built* `selState` (the point-cloud density
picker) left `Rows` nil → JSON `null` → Zig parse fails → the tab silently falls back
to Go. Every nested state with a slice must be initialized non-nil even when its
section is hidden.
