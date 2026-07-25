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
| live | Zig (`native/zigui/src/live.zig`; full + 10 tick fragments via `rz_ui_render_live_frag`) | `TestZigLiveGolden` |

| vrchat | Zig (`native/zigui/src/vrchat.zig`; full + `#vrc-status-region`/`#vrc-editor`/`#vrc-campaths`/`#vrc-photos-body`) | `TestZigVRChatGolden` |
| vrchat ▸ groups | Zig (`native/zigui/src/vrcgroups.zig`; `#vrcg-body` sub-view) | `TestZigVRCGroupsGolden` |
| worlds | Zig (`native/zigui/src/worlds.zig`; full + `#world-linkhint`/`#world-gh`/`#world-st-<key>`/`#world-unity-rows`) | `TestZigWorldsGolden` |

| midimon (MIDI-tab fragments) | Zig (`native/zigui/src/midimon.zig`; monitor card + `#midi-monitor` rows + driver wire trace) | `TestZigMIDIMonGolden`, `TestZigMIDITraceGolden` |
| midictl | Zig (`native/zigui/src/midictl.zig` + `midictl_ctls.zig` + `midictl_uimap.zig`; full tab + `#midi-active` + `#midi-ctlstat-<i>`) | `TestZigMIDICtlGolden` |
| automations | Zig (`native/zigui/src/automations.zig`; full + `#auto-body` fragment) | `TestZigAutomationsGolden` |
| overlays | Zig (`native/zigui/src/overlays.zig`; full + #ovl-appearance/#ovl-spout/#ovl-strip/#ovl-st-* fragments) | `TestZigOverlaysGolden` |
| twitch | Zig (`native/zigui/src/twitch.zig`; full + #twitch-obs/#twitch-presets/#twitch-feed fragments) | `TestZigTwitchGolden` |
| editor | Zig (`native/zigui/src/editor.zig`; full + #ed-preview fragment) | `TestZigEditorGolden` |
| settings | Zig (`native/zigui/src/settings.zig`; full + `#set-content` pane + `#stset-<id>` status) | `TestZigSettingsGolden`, `TestZigSettingsStatusGolden` |
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

## Go-workaround awareness (see ZIG_MIGRATION.md "Why Zig")

Zig's origin is a DAW that refused to compromise on UI performance — our Go render layer
carries Go-runtime workarounds (string-builder reuse, tick/patch throttles sized for GC
pressure, precomputed caches dodging per-render alloc). During phase A, replicate them
where they shape the DOM (parity!); FLAG them in port notes. Phase B (Zig shell) revisits
each — many are unnecessary under explicit allocators + no GC.

VRChat-port notes (tabs #3/#4): the tab + its Groups sub-view are separate exports (the tab
embeds `#vrcg-body`, actions patch it alone). components.go grew caller-resolved data-label
variants (`kvDL`/`statusRowDL`/`fieldExDL`, wrappers delegate) mirroring `toggleRowDL`.
Components ported to `components.zig`: `kv` · `statusRow` · `fieldEx` ·
`cardOpen/cardHeadClose/cardClose` · `itemRowOpen/itemRowClose` · `mdOpen/mdSplit/mdClose`
(masterDetail) · `fpairOpen/fpairClose` · `num(i64)` (Go `%d`). Two things stay resolved
Go-side because Zig has no equivalent: the photo cell's `title=%q` (Go strconv quoting) is
pre-quoted into state (`titleQ`, emitted verbatim), and pre-rendered fragments from other
subsystems (campath 3-D viewer SVG, play button, `tipTopic` tooltips) travel as trusted raw
strings. Nil-slice gotcha: nested zero-value state structs marshal `null` for their slices and
Zig slice parsing rejects null — every slice field carries `,omitempty` so Zig falls back to
its `&.{}` default.

Worlds-port notes (tab #5): the ws-help prose paragraphs, hand-written card titles and the
add-list placeholder/submit label were Go **source literals inserted unescaped** — several carry
apostrophes, so escaping them would change the DOM. They travel in state and BOTH renderers emit
them raw (documented in the header of worlds.zig + the state block). Everything user-derived
(names, URLs, paths, gist errors) stays escaped. `#world-st-<key>` ids stay raw too, matching Go.

MIDI-port notes (tabs #3+#4, midimon fragments then the whole midictl tab):
- Tooltips stay Go: `tipTopic(id)` markup (tooltip.go, keybind grid + link list) rides in
  state as a PRE-RENDERED HTML string and Zig `raw`s it. Same for smart-select labels that
  carry a tooltip (`selectBoxTip`) — state holds the resolved `selState` plus its
  `<span class=ss-label>…</span>` HTML (`selHTMLRaw` / Zig `selectBoxRaw`).
- Floats never cross the ABI: knob/fader `--v`/`--rot` are `trimNum`'d Go-side into strings.
- Non-nil slices are mandatory — a nil Go slice marshals to `null` and the Zig parser
  rejects it, silently falling the WHOLE tab back to Go. `emptySel()` (smartselect.go) is
  the guard for zero-value `selState`s inside heterogeneous rows.
- A fragment whose HTML is legitimately EMPTY (`#midi-ctlstat-<i>` before the MIDI child
  reports) makes `renderJSON` return NULL ⇒ `ok=false` ⇒ the Go fallback renders the same
  empty string. Golden tests must accept that (see `TestZigMIDICtlGolden`).
- Components added to `components.zig` (`// --- midi ---` block):
  `cardOpen(title,head)`/`cardTrailClose`/`cardClose` (Go `card`, streaming) ·
  `statusRow(variant,label,data_label,line)` (Go `statusRowDL`) ·
  `fchip(label,val,act,active)` · `toggleRowTip(label,dl,act,on,tip_html)` (Go
  `toggleRowTipDL`) · `itemRowOpen(title,sub)`/`itemRowClose` (Go `itemRow`, streaming) ·
  `selectBoxRaw(Select,label_html)` (Go `selHTMLRaw`).
- Go helpers extended with caller-resolved-data-label twins (Unicode `ToLower` stays in Go,
  same pattern as `toggleRowDL`): `statusRowDL`, `toggleRowTipDL`, plus
  `resolveSelectBoxTip` and `resolveSmartSelect`/`selHTMLRaw`/`emptySel`.

Media-batch notes (tabs automations/overlays/twitch/editor): `render_media_shared.go`
carries the components.go primitives as JSON-able control state (`uiBtn`/`uiToggle`/
`uiField`/`uiKV`/`uiStatus`/`uiSlider`) because these tabs pass controls around as state,
where a struct beats 8 positional args. Each `html()` delegates to the caller-resolved
Go primitive (`fieldExDL`/`kvDL`/`statusRowDL`/`toggleRowDL`/`slider`), so `dl` is
AUTHORITATIVE for both renderers and the markup has exactly ONE Go source. The
components.zig `media` block is thin wrappers over the flat helpers the other batches
already added — `Field`/`fieldOf` → `fieldEx`, `KV`/`kvOf` → `kv`, `Status`/`statusOf` →
`statusRow` (plus the variant-`""`-renders-nothing rule that Go `ovlStatus` needs),
`Toggle`/`toggleOf` → `toggleRow` — with only `Btn`/`btnOf`/`btnRowOf`/`btnAct` genuinely
new (`btnAct` = `btn` whose act is `prefix++id`, no data-val: the per-row action pattern).
`uiSlider` adopted the motion batch's dual-number shape (floats for Go's `slider()`,
`minS`/`maxS`/`stepS`/`valS` for Zig) so components.zig `Slider` is shared;
`TestUISliderNumberPairsAgree` pins the two representations plus the primitive delegation.
Two Go-resolved tokens ride through the editor state verbatim for the same
formatting reason: `fmt.Sprintf("%q", …)` of the font family + image URL (Go
`strconv.Quote` semantics). Twitch's feed buffer (`ui.go twitchRows`) was converted from
pre-rendered HTML to resolved row state; the streaming cockpit inside `#twitch-obs` stays
render_live.go's renderer and passes through as raw trusted markup (a tab may embed
another tab's renderer, and renderer ownership wins over "one language per view").
Editor gotcha: `align` is a Zig keyword — the json tag is `alignment`. Composite `style`
attributes that Go builds then `attrQ`-escapes as ONE value (editor layer divs, whose
image paint carries `%q` quotes) must be assembled into a scratch `Html` and then
`attrQ`'d — they cannot be streamed.

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

Live-port notes (tab #4): the Live tab is 11 independently tick-patched fragments, so
the ABI got ONE dispatch export - `rz_ui_render_live_frag(kind, kind_len, json…)`,
kinds `transport|np|status|decks|signals|cockpit|link|graph|perf|strip` ("graph" serves
both #live-net and #live-tim) - instead of ten near-identical exports; unknown kind
returns NULL so the bridge falls back. Go keeps every float: sparkline SVGs + graph
legends arrive as trusted raw HTML, and the Link phrase-bar fill arrives pre-formatted
(`pbarPct`, `%.2f%%`) because the client rAF runtime (`__rt 'link'`) rewrites that exact
attribute per frame - `linkPhraseBar(float)` stays for player_realtime_test.go and now
delegates to `linkPhraseBarStr`. Quirks replicated deliberately (golden-gated): the
status card splices `strings.ToLower(k)` into `data-label="…"` UNESCAPED, and the
signals card's rows carry no data-label at all. `cockpitHTML` is shared with the Twitch
tab, so that tab now renders its OBS rows through Zig too.
Components added: `statusRow`, `sectionOpenTip` (both used here).

Settings-port notes (the biggest tab, ~40 cards over 7 sub-tab sections): the split introduced a
BLOCK LIST between the state builder and the renderers - `cardBlocks(id)` (was `cardContent`)
returns `[]setBlock` instead of HTML, and `setBlockHTML` renders each kind through the existing
components.go primitive (`note/noteRaw/hint/empty/field/toggle(+tip,+gated)/select(+tip)/amenu/
kv/fpair/btnrow/pathrow/itemrow/install/installNote/region/form/raw`). That keeps ONE markup
source for both renderers while the ~45 per-card bodies stay readable as data. Verified zero DOM
change by dumping 24 pre/post fixtures (empty + populated config x 7 sections x 4 queries + full
view) and diffing - 0 lines.
- Everything impure is resolved Go-side as usual: config/service snapshots, the cached fs/PATH/
  device probes (`settingsProbes` - a Go-runtime-shaped cache, see below), `strings.ToLower`
  data-labels, all numbers (`strconv`/`trimNum`/`FormatFloat 'g'`), smart-select registration +
  filtering, `tipTopic` tooltip markup, and the SEARCH match: `foldSearch(stripTags(setCardHTML(
  card)))` runs on the Go-rendered card, so the query text never reaches Zig.
- Trusted raw markup (`raw`/`region` blocks) = the WAVE 3 seams, each owned by another file:
  `gridfixCardBody` (settings_gridfix.go), `gridfixModelCardBody` (settings_gridfix_model.go),
  `bridgeCardBody` (bridge_actions.go), `updateFlowHTML` (update_actions.go, inside
  `#inst-update`). settings_vr_managers.go is modal-only - it never enters the tab render.
- Only ONE new components.zig helper was needed (`toggleRowGated`, Go `toggleRowGatedDL` - the
  gated-switch + warn-hint pair); everything else reused. Settings-specific chrome (set-note,
  set-pathrow, set-install, set-cardhead, set-nav, set-search, set-dlgform) stays tab-local in
  settings.zig.
- Composite children (fpair / btn-row / item-row trailing) travel as a NON-recursive `setKid`
  (field|select|amenu|btn) - depth is 1 by construction, so the JSON stays a plain tree and Zig
  needs no recursive parse instantiation.
- Byte-exactness traps replicated: the hand-rolled `set-dlgform` inputs have NO space between the
  `placeholder="…"` attribute and `autocomplete=off` (Go concatenates `attrQ(...)` straight onto
  the literal); the RTSP note splices pre-escaped `&lt;this machine's IP&gt;` between two escaped
  values (carried as a `noteRaw` block); ids (`stset-<id>`, `stnav-<id>`, `set-<sec>`,
  `inst-<key>`, `data-act=toggle:<id>`, form acts) are spliced UNESCAPED both sides.
- Go-runtime workaround, NOT ported behaviour (flagged per ZIG_MIGRATION "Why Zig"):
  `settingsProbes` + `maybeRefreshProbes` (render_settings.go:~1210-1400) exist because the
  blocking `mediatools.Tool.Status()` (PATH scan) / `vrdll.Probe()` / device enumerations ran on
  the render goroutine and froze tab-open for seconds; the 10 s TTL + one-in-flight + "patch once
  when the install state flips" dance is scheduler/GC-shaped, not feature-shaped. A Zig-native
  pass can probe concurrently with an explicit allocator and drop the whole cache. Same for the
  search path: it renders every card in Go to match against, then re-renders the matches in Zig -
  acceptable now (goldens keep it honest), removable once the block state is matched directly.
