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

| peers | Zig (`native/zigui/src/peers.zig`; full + `#peers-body` fragment) | `TestZigPeersGolden` |
| library_remote | Zig (`native/zigui/src/libremote.zig`; the `.lib-target` control switcher) | `TestZigLibRemoteGolden` |

| publish | Zig (`native/zigui/src/publish.zig`; full + `#pub-hero` tick fragment) | `TestZigPublishGolden` |
| publish ▸ remote peer | Zig (`native/zigui/src/publish.zig` `renderRemote`; full view) | `TestZigPublishRemoteGolden` |

| settings | Zig (`native/zigui/src/settings.zig`; full + `#set-content` pane + `#stset-<id>` status) | `TestZigSettingsGolden`, `TestZigSettingsStatusGolden` |
| library ▸ peer mirror | Zig (`native/zigui/src/libviews.zig`; `#lib-body` + `#rmirror-banner`) | `TestZigLibMirrorGolden` |
| library ▸ remote cue edit | Zig (`libviews.zig`; `#lib-body` + `#rce-info` + the rail's save section) | `TestZigRCEGolden` |
| library ▸ modals | Zig (`libviews.zig`; smart-rules editor + relocate-missing) | `TestZigLibModalsGolden` |

| settings ▸ sub-views | Zig (`native/zigui/src/settings_sub.zig`; gridfix + gridfix model + account bridge card bodies + `#inst-update`) | `TestZigSettingsSubGolden`, `TestZigSettingsSubBlocksGolden` |
| library | Zig (`native/zigui/src/library.zig` + `library_kit.zig` + `library_sections.zig` + `library_detail.zig`; full tab + `#lib-body`/`#lib-detail`/`#lib-queue-body`/`#ce-cell-<hash>`) | `TestZigLibraryGolden`, `TestZigLibraryQueueGolden`, `TestZigLibraryCueCellGolden` |
| library ▸ cue editor | Zig (`native/zigui/src/cueedit.zig`; `#ce-topbar` + full-width wave strip + the `#lib-detail` editor rail) | `TestZigCueEditTopbarGolden`, `TestZigCueEditWaveGolden`, `TestZigCueEditRailGolden`, `TestZigCueEditRailInDetail` |


| library ▸ fixer subviews | Zig (`native/zigui/src/libfixers.zig`; nav rail · gridfix rail + `#gf-live` · fixer results (gridfix/tagfix) · tag editor · prep picker · compat section) | `TestZigLibFixNavRailGolden`, `TestZigLibFixPrepGolden`, `TestZigLibFixGFRailGolden`, `TestZigLibFixGFLiveGolden`, `TestZigLibFixResultsGolden`, `TestZigLibFixTagEditGolden`, `TestZigLibFixCompatGolden` |

| **Dialogs** | | |
| dialogs ▸ shared choice | Zig (`components.zig` `Choice`/`choiceDialog`; confirm / format picker / row context menu - 6 call sites) | `TestZigDialogsAGolden` (`choice/*`) |
| publish ▸ text export | Zig (`native/zigui/src/dialogs_a.zig` `renderTxtExport`) | `TestZigDialogsAGolden` (`txtExport/*`) |
| publish ▸ export preview | Zig (`dialogs_a.zig` `renderExportPrev`; local + remote arms) | `TestZigDialogsAGolden` (`exportPrev/*`) |
| publish ▸ rename set | Zig (`dialogs_a.zig` `renderRename`) | `TestZigDialogsAGolden` (`rename/*`) |
| publish ▸ fix start times | Zig (`dialogs_a.zig` `renderFix`) | `TestZigDialogsAGolden` (`fix/*`) |
| player ▸ export preset editor | Zig (`dialogs_a.zig` `renderPreset`; loudness block stays raw) | `TestZigDialogsAGolden` (`preset/*`) |
| cue editor ▸ pattern manager | Zig (`dialogs_a.zig` `renderPatMgr`) | `TestZigDialogsAGolden` (`patMgr/*`) |

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

Library fixer-subview notes (wave 3, the seams the library port carried as `Raw`): the nav rail,
beatgrid-fixer rail + results table, tag-fixer results + per-track editor, prep-playlist picker and
"works well together" section now cross the ABI as STRUCTURED state - `libNavSt` / `libGFSt` (+
`libGFLiveSt`) / `libFixResSt{gf|tf}` / `libTagEdSt` / `selState` / `libCompatSecSt` in the new
`render_library_fixers.go` (pure renderers + state types) with the impure builders staying in their
feature files (`library_navrail.go`, `library_gridfix.go`, `library_tagfix.go`, `library_prep.go`,
`library_compat.go`). `libBodySt.NavRail`, `libCollSt.Prep`/`.Results`, `libDetailSt.TagEditor`/
`.Compat` changed type in place (json keys unchanged) and the gridfix rail got its OWN detail kind
(`libDetailGF`) instead of sharing the generic `raw` with the cue-edit rail.
- **Two Go helpers that look identical are not**: `gfStat` ESCAPES its number, `gfTile` splices
  `fmt.Sprint(int)` RAW. Unifying them would have been a silent DOM change on any adversarial
  fixture; `libfixers.zig` keeps both and the golden fixtures feed `1&2` through the stat path.
  Same class of quirk: a results row's status token (`FIX`/`OK`/`SKIP`/`ERR`) is spliced unescaped
  into BOTH the chip class (pre-lowered Go-side, `stLow`) and the chip text, nav-row icons are
  glyph literals emitted raw, and `tf-sel:<i>` / `libnav-hd` truncation markers stay raw.
- Ordering is state, not renderer logic: `gfDoneState` resolves the rail's hints (no-targets →
  per-target applied → apply error → prepped) into ONE ordered `Hints` slice while the write
  actions accumulate separately, because the Go original interleaved `b.WriteString(hint(...))`
  with `acts = append(...)` and emitted the button column last. The health card needed an explicit
  `NoteAfter` flag - the engine-missing branch notes BEFORE its button row, the ready branch AFTER.
- The calibration stage is NOT its own kind: it is `libGFRunning` with an empty tile set (the Go
  `gfCalRunningHTML` differed from `gfRunningHTML` only in the title + the tile-less fragment).
- `#gf-live` is the one independently patched fragment here (the run goroutine `u.eval`s it ~2 Hz
  from `gfRunTracks`/`gfCalibrate`), so `gfLiveInner` became `gfLiveState`/`gfCalLiveState` +
  `libGFLiveHTML` + a `gfLiveRender` bridge - the live patch now renders through Zig too.
- `prepSelectHTML` survives for the cue-editor rail (`library_cueedit.go` embeds markup); it
  delegates to `prepSelectState` + `selHTML`, so the `ssRegister` side effect keeps happening at
  exactly the same point in the render and both surfaces share ONE markup source.
- Proof of zero DOM change: a literal-multiset diff over the touched renderers (452 → 415 markup
  literals, **zero added** - every drop is dedup of the per-stage `insp-hd`/`set-note`/`gf-current`
  emits) plus a throwaway test that transcribed the pre-split renderers verbatim and asserted 15
  byte-identical pairs (health both note orders, done with interleaved hints/acts/notes, confirm
  with a zero-count scope row, both `#gf-live` variants, results table + empty, tf group list with
  a capped group, tag editor open/closed, compat rows/empty).
- No new components.zig helper and no tab-local variant was needed: the subviews reuse
  `progressBar`/`btnRowOf`/`btnOf`/`toggleOf`/`hint`/`badge`/`emptyState`/`fchip`/`itemRow*`/
  `selectBox` plus `library_kit.zig`'s `pageSub`/`btnRowOf1`/`chip`/`pbField`. `libfixers.zig`
  keeps only its own chrome (libnav rows, gf-stat/gf-tile/set-note, trk-row result rows, tf-grp).

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

Peers-port notes (peers batch): the whole tab is ONE `#peers-body` funnel (peers_actions.go
patches it ~1 Hz), so the split is `peersState`/`peersBodyState` (impure) vs
`peersHTML`/`peersBodyHTML` (pure) plus one pure renderer per section. Details worth knowing:
- **A state builder can carry a side effect and its ORDER is load-bearing.** `peerBannerState`
  auto-clears a MIDI-forwarding target whose peer dropped, and `peerConnsState` reads
  `Forwarding()` afterwards to decide Control vs Stop control. The builder assigns
  `st.Banner` before `st.Conns` with a comment; swapping them changes the DOM for one tick.
- Three lists (connections / discovered / remembered) collapsed into ONE `peerListSt` +
  `peerRowHTML` (optional dot, name, `np-artist` tail, `btnRow`, plus the bridged deck lines
  that render as SIBLINGS after the row div). Go picks the empty-state text per reason
  (discovery off vs still searching), so the renderers stay pure.
- Every number is pre-formatted Go-side: clock/sync/route/pipeline telemetry strings, UVC
  `min/max/step/value` (`strconv.FormatInt`, Go `%d`), and the transfer progress-bar width.
  `progressBar` was split like `linkPhraseBar`: `progressBar(frac,cap)` →
  `progressBarStr(progressPct(frac), cap)`, and both renderers use the *Str form → ONE markup
  source (`TestProgressBarDelegatesToPct`). NOTE the name: `pbarPct` was already taken by
  render_live.go with a DIFFERENT contract (0..100, `%.2f%%`) - a state-name collision that
  only surfaced at compile time.
- Raw (trusted) fields, matching the Go source literals they replace: the receive-row `◂ `
  mark, the `data-label="peer counts"` / `"controlling"` / `"spout sender"` literals, and the
  cam-prop `oninput` display-only handler.
- The webcam card's two device/mode pickers are smart selects → `resolveSelectBox` + `selHTML`
  (`selectBox()` on the Go path is exactly that pair). `camPend` (the pending device/mode that
  survives the 1 Hz re-render) stays a Go global read by the state builder.
- Exactly-one-of choices ride as explicit flags, never "empty means the other": `xferProgSt`
  has `isBadge` (badge vs button) and `bar` (progress bar vs muted text), `peerCamSt` has
  `gated`. An implicit "" rule would have silently diverged on a blank i18n string.
- Components added to `components.zig` (`// --- peers ---`): `progressBar(pct,caption)` only
  (Go `progressBarStr`; empty caption falls back to the percentage). Everything else reused
  panel/emptyState/hint/section/dot/btn+btnOf/btnRowOf/btnRowOpen+Close/subTabs/selectBox/
  toggleOf/fieldOf/badge unchanged - no tab-local variants were needed.

library_remote-port notes: `render_library_remote.go` is plumbing (peer enumeration + the
typed remotectl client) with exactly ONE renderer, `targetSwitcherHTML` - the "Controlling
[This computer ▾]" row. Split into `targetSwitcherState` (impure: `virtual()`, connected
peers, current target, smart-select registration) + `targetSwitcherHTMLOf` (pure). Its only
caller is render_library.go, which keeps calling `u.targetSwitcherHTML(id, act)` unchanged, so
a later Library port can either keep embedding the returned markup as trusted raw HTML or lift
`libRemoteSt` into its own state. `Show=false` (headless remote session / no peer connected)
renders "" - and an empty fragment makes `renderJSON` return NULL, so the bridge falls back to
Go which renders the same empty string (same rule as `#midi-ctlstat-<i>`).

Publish-port notes (tab #14): the tab is one renderer with two data worlds (local recorder vs a
peer over remotectl), so `renderPublish` dispatches on `libRemoteTarget()` and each world has its
OWN state + export (`pubSt` / `pubRemSt`, `rz_ui_render_publish` / `_remote`; the remote view is a
whole-view export because it re-frames panel + switcher + `#publish-body` itself and has no tick
fragments - live status stays on the controlled box). Raw pass-throughs, both trusted: the unified player/editor
(`player.go mpHTML("publish")`, embedded in the captures pane AND in the no-selection card when a
loose capture is pinned) and the peer target switcher (`targetSwitcherHTML`, which registers a
smart select as a side effect - the state builder calls it, exactly where the old renderer did).
Progress bars adopt the motion/media dual-number shape: `pubBarSt` carries the float for Go's
`progressBar()` AND Go's `%.1f%%` string for Zig (`TestPubBarNumberPairsAgree` pins them). Tracklist
rows carry the RESOLVED `data-ctx` value instead of the two Go spellings (the non-editable branch
spliced `"pub-tctx:" + esc(path)` by hand, the editable one `attrQ`'d - byte-identical because the
prefix has no escapable characters) and a `lead` kind (`resolving|none|chk`) whose glyph (…/·) is a
literal in both renderers. The capture rows' `⋯` menu needed a resolved twin of `actionMenu`
(`resolveActionMenu` + `actionMenuHTML` in actionmenu.go, an appended block - `actionMenu` itself is
untouched for the Go-rendered library/settings tabs; `TestActionMenuResolvedParity` pins the two to
the same bytes). Components added to `components.zig` (`// --- publish ---`): `progressBar(pct,cap)`
(pct pre-formatted, empty caption falls back to pct like Go) and `actionMenu(Select)` (the `amenu`
wrapper around a bare `selectBox`). NOT ported (dialogs/modals stay Go, wave 3+): `publish_export.go`
(`pubTxtOpen` text-export dialog, `pubFixModal` time-fix preview), `publish_actions.go` modals
(rename/delete/capture-delete/track context menus), `pbuilder.go` (`mpPresetModal`), and everything
`player.go` renders.

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

Settings sub-view notes (the four wave-3 seams the settings port left as raw HTML - now structured
state): `gridfixCardBody`/`gridfixModelCardBody`/`bridgeCardBody`/`updateFlowHTML` became
`gridfixCardState`+`gridfixCardStateOf` / `gridfixModelState`+`gridfixModelSel`+`gridfixModelStateOf` /
`bridgeCardState`+`bridgeCardStateOf`+`bridgeGateState` / `updateFlowState`+`updFlowStateOf`, with the
PURE renderers in `render_settings_sub_html.go` and the Zig mirrors in `settings_sub.zig`. The card
bodies now cross inside the settings state as four new `setBlock` kinds (`gridfix`, `gridfixmodel`,
`bridge`, `updregion`) - `sbRaw`/`sbRegion` are gone from those cards - and each body ALSO has its own
export (`rz_ui_render_settings_{gridfix,gridfixmodel,bridge,updflow}`) because `#inst-update` is
patched on its own (`patchUpd`) and the goldens pin each body directly.
- **Impure facts travel as a small "bits" struct where the source is unmockable.** `bridgeBits`
  (relay `bridge.State` + gate URI/secret/enrolled/persistent/sessions) and the explicit
  `updater.Status` / `gridfix.EnvStatus` / `train.TrainEvent` parameters keep the state MAPPING
  testable without a live `authz.Gate` (bbolt + OS secret store) or a running `updater.Manager`
  (unexported status). Side-effect order preserved: `g.Sessions()` still reaps expired tokens, and
  `Enrolled()`/`Persistent()` are still skipped while an enrolment is pending.
- Zero DOM change proven by a throwaway harness: verbatim copies of the four pre-split emitters vs
  the new state+renderer over **100 fixtures** (9 env × 7 config for gridfix, 12 model, 14 bridge,
  11 update states) - 0 diffs, then deleted. The permanent guard that stayed is
  `TestSettingsSubStatesHaveNoNullSlices` (nil-slice → JSON null → Zig parse reject → silent
  whole-tab fallback).
- Quirks replicated deliberately: the gridfix "engine ready" line `esc()`s the version string INTO
  the hint text, which `hint()` escapes AGAIN (double-escaped in the DOM); the model picker passes
  the current LABEL to `smartSelect` as its `cur` value, so the resolver's value match never fires;
  `progressBar(0, line)` rides as the pre-formatted `progressPct(0)`; the update flow's error hint
  keys off a PREFIXED string (`errPrefixed`) so an empty error stays empty.
- Only ONE new components.zig helper (`// --- settings-sub ---` block): `listRowOpen`/`listRowClose`
  (Go `listRow`, settings_actions.go - the trusted-session rows). Everything else reused.
- **Silent-fallback visibility (new, cross-cutting):** `internal/zigui/fallback.go` (UNTAGGED, so
  both the cgo build and the stub expose it) counts every `ok=false` render keyed by the `Render*`
  wrapper (`runtime.Caller`, failure path only; `len(state)==0` is not counted - nothing was asked
  of Zig) and exposes `zigui.FallbackCounts()`. webui's `logZigFallbacks` (called from the ~1 Hz
  `livePush` tick) logs the sorted tally at DEBUG, at most once a minute and only when it changed.
  Expect benign counts from the legitimately-empty fragments (`#midi-ctlstat-<i>`, hidden
  library-remote switcher, `#pub-hero` without a recorder, hidden update flow); a count on a
  whole-view renderer is the real smell.

Library-port notes (the biggest tab, 2768 lines): split into `render_library_state.go`
(impure) + pure renderers, then four Zig files - `library.zig` (tab + body dispatch),
`library_kit.zig` (the helpers that live in render_library.go rather than components.go),
`library_sections.zig` (the eight sections), `library_detail.zig` (inspector + encode builder).
- **`selState.Rows` needed `,omitempty`** (smartselect.go). A zero-value select - the playlist
  facet when the DB has no playlists, the cloud menu without a syncer - marshalled `"rows":null`
  and the Zig parser rejected it, so the WHOLE tab silently fell back to Go. This is the
  nil-slice gotcha one level deeper than the earlier batches hit it: it is not enough for the
  TAB state's slices to be non-nil, every nested reusable state must tolerate its zero value.
- Delegation so each component keeps ONE markup source: `pbFieldEx`->`pbFieldExDL`,
  `pbSelect`/`pbSelectTip`->`resolvePbSelect`/`resolvePbSelectTip` (+`selHTML`/`selHTMLRaw`),
  `keyPillHTML`->`libKeyPillState`+`libKeyPillHTML`, `actionMenu`->`resolveActionMenu`+
  `actionMenuHTML`, `progressBar`->`progressPct`+`progressBarStr`. The literal-multiset diff against
  the pre-split file is the proof of zero DOM change.
- components.zig gained only the two layout ports it was missing (`mdWideOpen`,
  `triOpen`/`triMid`/`triClose`). At the development merge the library `progressBar` and
  `amenu` were deduped against the peers+publish `progressBar`/`actionMenu` (markup-identical;
  the shared ones win, `library_kit.amenu` now delegates). No existing helper was modified and
  no tab-local variant was needed.
- Name collisions bite in the golden test too: `libDetailSel` is a state-kind const, so the
  fixture helper had to become `libDetailFixture`. `inline` and `goto` are Zig keywords - the
  json tags are `inlineActs` / `gotoLbl`.
- Raw seams (wave 3): the tab embeds nine other renderers' output as trusted markup - target
  switcher, nav rail, cue-edit wave + rail, remote mirror / remote cue-edit bodies, gridfix +
  tagfix panels, prepare-select, compat section, player, shared loudness block. The Camelot
  wheel SVG stays Go by design (float math + `%.2f`), like the campath viewer in the motion batch.
  SINCE RESOLVED: nav rail, gridfix rail + results, tagfix results + editor, prepare-select and
  the compat section became structured state (see "Library fixer-subview notes" below); the target
  switcher, cue-edit surfaces, remote bodies, player and loudness block are still raw.
- Go-runtime workarounds replicated for parity, flagged for phase B: the version-keyed render
  memos (`collViewSig`, `plRowsVer`, `smartCounts`, facet counts, `onDiskCk`) and the 2 s/5 s
  freshness TTLs exist because a full 23k scan, a per-row `os.Stat` or a DB `COUNT` on the
  SINGLE serialized action goroutine froze the tab - they are latency workarounds for Go's
  webui threading model, not for the DOM. The phase-A bridge also adds a per-render
  state->JSON->parse round trip that phase B removes.

Cue-editor-port notes (the deepest single subview, ~2000 lines of library_cueedit.go): three
exports because three surfaces patch independently - `rz_ui_render_cueedit_topbar` (`#ce-topbar`,
re-patched on EVERY cursor move / edit), `_wave` (the full-width strip the library embeds as
`libBodySt.CEWave` and library_remotecue.go `rceBody` wraps in `.ce-fullwave`), `_rail` (the
`#lib-detail` inner in cue-edit mode). Split into `render_library_cueedit.go` (state + pure
renderers + bridges); `library_cueedit.go` keeps 3-line delegations so `cePatchRail`,
`cePatchWave` and `rceBody` call the same names as before.
- **The 30 fps surface stays Go by design.** `player.go mpHTML("library")` - the waveform SVG,
  beatgrid/marker geometry, `%.2f` coords and every id the client rAF runtime (`__rt 'ph'`,
  `<ph>`/`<ph>-veil`/clock) rewrites per frame - rides as ONE trusted raw `player` field.
  Nothing in the topbar or rail is `__rt`-driven, so the ported markup carries no rAF contract.
  Same rule as keywheelSVG/campath: pre-rendered float-math fragments are never ported.
- Other Go-resolved strings that ride raw (spliced unescaped in Go too, so escaping them would
  change the DOM): `pubClock` readouts (`cursor`, drop `when`), `ceBarBeat` (`barBeat`), the
  `ce-goto:%f` acts (Go `%f` of a float64), the `DROP <n>` tags, the ▸/▾ prefs arrow, and
  `tipTopic("cue-edit")`. Other renderers' output stays raw as well: `prepSel`
  (library_prep.go), `writeBack` (library_cuewrite.go `ceWriteHTML` **or**
  library_remotecue.go `rceSaveHTML` - the rce path is untouched and stays Go).
- **Side-effect ORDER in the state builder is load-bearing** (same lesson as the peers batch):
  `ceRailState` must call `ceWriteHTML`/`rceSaveHTML` (they lock ceSt) BEFORE taking `c.mu`, and
  `ceDefaultsState` (registers the pad select) BEFORE `u.cePatterns()` opens the store and the
  assign pickers register - exactly the sequence the pre-split renderer had. A collapsed
  defaults block registers NOTHING, so the collapsed/open branch must skip resolution too.
- Exactly-one-of and "is it shown" choices ride as explicit flags (`hasRce`, `hasSel`, `hasPats`,
  `hasDrops`, `showOwNote`, `showNoDrops`, `showDelHint`, `hasPromote`, `hasGrid`, `placed`),
  never "empty string means the other branch" - a blank i18n value would silently diverge.
  The two exceptions are faithful to Go's OWN conditions (`meta != ""`, `label != ""` in selHTML).
- Quirk replicated deliberately (golden-gated): the per-drop pattern picker is registered with
  the pattern NAME as `cur` while its rows carry pattern IDs, so no row is ever marked current
  and `CurLabel` passes straight through.
- Zero new components.zig helpers - `selectBox`/`toggleOf`/`btnOf`/`btnRowOpen+Close`,
  `library_kit.fieldRaw` and `library_kit.hints` covered everything; the four bits of
  cue-edit chrome (`pbLabel`, `setNote`, `btnRow1/2`, `btnCol2`) are tab-local in cueedit.zig.
  No existing helper was modified and no tab-local variant of one was needed.
- Zero-DOM-change proof: the multiset of markup literals in the pre-split functions vs the
  split ones is identical (73 literals, 0 diff), plus a deliberate one-byte Zig perturbation
  that failed 9 golden subtests before revert. The library suite's `cueEdit` fixture now
  embeds the REAL wave + rail markup instead of stubs, so the seam is pinned from both sides.
- NOT ported (modal, wave 4+): `cePatternManagerHTML` (the manage-patterns dialog) - dialogs
  stay Go, same rule as the publish batch.

## Modal ports (recipe — wave 4 dialog sweep follows this)

First modal ports: the two Library dialogs (`libSmartModalHTML`, `libRelocModalHTML`). A modal
differs from a tab in exactly two ways: it renders into the modal root instead of a patch
target, and its markup is wrapped by `components.go modal(title, body, footer)`. Everything
else is the tab recipe.

1. **Split like a tab.** `<name>ModalState(...)` (impure: locks, i18n, smart-select
   registration) + pure `<name>ModalHTMLOf(st)` that ENDS with `modal(st.Title, body, footer)`.
   Keep the exported entry point's signature (`func (u *UI) libSmartModalHTML(s *libSt) string`)
   so every `u.openModal(u.<x>ModalHTML(s))` call site is untouched — modals are re-opened from
   many action handlers and each one is a full re-render.
2. **Bridge inside that entry point** (`zigui.Available()` → `zigui.Render<X>Modal` → Go
   fallback). The state travels as ONE struct; the modal chrome is part of the Zig output, so
   `openModal` sees the same bytes either way.
3. **components.zig gained the modal bracket triple** (`// --- libviews ---`):
   `modalOpen(title)` → body → `modalFoot()` → footer → `modalClose()`, plus
   `modalFootDefault()` for Go's default footer. NOTE: that default footer's label `"Close"` is
   a HARDCODED ENGLISH literal in `components.go modal()` (not an i18n key) — replicated
   verbatim; don't "fix" it on one side only.
4. **Live sub-patches inside a modal stay Go.** `#lib-sr-count` is patched via
   `c.textContent = <text>` (`libSRQuiet`), not innerHTML — so the count is a plain state string
   (`Count`), and no fragment export is needed. Check every `__patch`/`textContent` site inside
   the dialog before deciding you need a `_frag` export.
5. **Modal-local smart selects resolve exactly like tab ones.** `pbSelect` → `resolvePbSelect`,
   and a bare `smartSelect(id, "", act, cur, opts)` → `resolveSmartSelect(id, act, cur, opts)`
   + `selHTML` (label "" ⇒ byte-identical). The opts closure keeps reading `ssFilter(id)` and
   capping server-side — filtering NEVER crosses the ABI (the compat picker's 60-row cap and
   its Unicode `strings.ToLower` stay in Go).
6. **Golden fixtures for a modal** = the tab set plus the dialog's own state axes: closed /
   open+filtered / open+no-matches (`ss-none`), the create-vs-edit title+confirm pair, the
   conditional sub-block (compat depth chips), and every list cap (relocate: 200 rows + the
   "showing" line). Escaping fixtures must include the ids/acts spliced UNESCAPED
   (`lib-reloc-skip:<i>` is index-derived, emitted raw both sides).

libviews-port notes (wave 3: mirror / remote-cue-edit / modals):
- **Side-effect ORDER is part of the split.** `libMirrorState` opens the ruiMsg session, cancels
  the previous one and only THEN resolves the banner — the old renderer built the banner after
  flipping `status = connecting`, so resolving it first would render one stale tick. Same
  discipline as `peerBannerState` before `peerConnsState`.
- `rceBody` embeds the local inspector: it now carries `libDetailSt` as structured state and
  renders through `libDetailWrapHTML` / Zig `library_detail.render`, which removes the nested
  per-render bridge call the old `libDetailWrap(s)` made. `#lib-detail` / `#rce-info` ids and
  patch contracts are unchanged.
- **Raw seam left deliberately: `rceBodySt.Wave` = `ceWaveHTML()`** (library_cueedit.go). The cue
  editor is a separate port; its topbar + waveform canvas + `ce-*` acts ride as trusted markup.
  `rceSaveHTML()` (the rce arm of the rail) IS ported and keeps its signature, so
  `ceRailHTML`'s `wb = rs` splice needs no change whichever renderer owns the rail.
- **The StateSHA contract was not touched.** `rceDirtyLocked()` / `r.baseSHA` are read by the
  state builders exactly where the renderers read them; no hashing, no state computation and no
  save/conflict path moved. Explicit `status` enums (`busy|dirty|saved|clean`) + `hasWrites`
  carry the branch instead of "empty means the other arm".
- Trusted raw fields: the mirror banner's `tipTopic("remote-library")` markup, and the session
  status spliced UNESCAPED into `class="rmirror-bar rmirror-<status>"` (one of four consts) —
  both replicated raw.
- Zero-DOM-change proof: a throwaway dump test rendered 36 fixtures (banner × every status,
  no-link arm, rce info/save/body × set+dirty+saved+escaping arms, both modals × empty/
  populated/edit/open-filtered/no-match/capped) in a HEAD worktree and in the split tree —
  `diff -r` clean. The Zig golden then re-checks 48 subtests byte-for-byte.

Dialog-sweep-A notes (wave 4: the publish/transcode dialog family - `publish_export.go`,
`publish_actions.go`, `publish_remote_actions.go`, `pbuilder.go`, `library_cueedit.go`'s pattern
manager). State + pure renderers in `render_dialogs_a.go`, Zig mirror in `dialogs_a.zig`, one export
per dialog in the `// --- dialogs-a ---` blocks. Every entry point kept its signature, so the
`u.openModal(...)` call sites are untouched.
- **SIX dialogs collapsed into ONE renderer.** The capture-remove confirm, export-format picker,
  delete-set confirm, remote delete confirm and both tracklist context menus differ only in
  title / message / button list / where the buttons sit, so `components.zig` gained ONE shared
  `Choice`/`choiceDialog` (append-only) instead of six near-copy ports. Three explicit flags carry
  the shape: `hasMsg` (a blank message still emits an empty `.np-artist` - a blank i18n string must
  not switch arms), `msgRaw` (the hand-written English literals that QUOTE an already-escaped file
  name; escaping the whole line would turn those quotes into `&#34;`), `inBody` (btn-row inside
  `.modal-body`, so the footer falls back to Go's default Close). `hiddenField`/`labeledInput` also
  moved into components.zig - every form modal in the repo needs them.
- **No `_frag` export was needed anywhere in this batch.** Each dialog's "live preview" is a WHOLE
  re-open: `pub-txt-*` re-calls `pubTxtOpen`, `mp-pf:` re-calls `mpPresetModal`, `ce-pat-*` re-calls
  `cePatternManagerHTML`. A grep for `__patch`/`textContent` inside these dialogs' ids finds only
  `pub-hero` (a tab fragment, already ported) - checked before adding exports, per the modal recipe.
- Raw (trusted) pass-throughs, each matching an UNESCAPED Go splice: the shared loudness block
  (`components.go loudnessFields` - same seam the library encode builder keeps), the time-fix
  preview's clock readouts (`time.Format("15:04:05")`, `pubClock`) and row numbers
  (`fmt.Sprint(i+1)`), and `pubExportModal`'s note literal.
- Go helpers gained caller-resolved twins so each component keeps ONE markup source:
  `labeledInput` → `labeledInputDL`, and the dialogs resolve their selects through the existing
  `resolveSmartSelect`/`resolvePbSelect`/`resolvePbSelectTip` (+`selHTML`/`selHTMLRaw`). Two selects
  here are registered with an EXPLICIT id (`pub-txt-preset`, `pub-fix-opener`), so the state builder
  calls `resolveSmartSelect(id, act, cur, opts)` and then sets `.Label` - byte-identical to what
  `smartSelect(id, label, …)` did, and the `ssRegister` side effect still happens at the same point
  in the render.
- `pubTrackCtxModal` and `pubTrackCtx2` now share `pubCompatBtns(path)` (mark / find / copy path).
  The `path == ""` arm stays in `pubTrackCtx2` alone - `pubTrackCtxModal` never guarded it, and
  hoisting the guard would have changed its DOM for a crafted empty arg.
- Zero-DOM-change proof: a throwaway harness transcribed all eight pre-split emitters verbatim and
  diffed them against state+renderer over the full fixture set (capture-delete × 3 names,
  format picker × 2 ids, export preview × 15 payload/format pairs, rename × 9, delete × 2 arms,
  both context menus, 8 text-export states, 9 time-fix states, 9 pattern-manager states,
  12 preset-editor states) - 0 diffs, then deleted. The permanent guards that stayed are
  `TestDialogsAStatesHaveNoNullSlices` and the 61-subtest `TestZigDialogsAGolden`; a deliberate
  one-byte Zig perturbation (`pub-track-l` → `pub-track-L`) failed 13 subtests before revert.
- Fixture file `render_dialogs_a_test.go` is UNTAGGED on purpose so the byte-parity harness and the
  tagged Zig gate share one fixture set - a new state axis is exercised by both.
