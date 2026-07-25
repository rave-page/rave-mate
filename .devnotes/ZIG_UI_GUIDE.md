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
| player (unified media player/editor) | Zig (`native/zigui/src/player.zig`; full component + `#mp-<host>-root`/`-vid`/`-wave`/`-tp`/`-edit`/`-export`/`-ro`/`-hov`) | `TestZigPlayerGolden` |


| **Dialogs** | | |
| dialogs ▸ shared choice | Zig (`components.zig` `Choice`/`choiceDialog`; confirm / format picker / row context menu - 6 call sites) | `TestZigDialogsAGolden` (`choice/*`) |
| publish ▸ text export | Zig (`native/zigui/src/dialogs_a.zig` `renderTxtExport`) | `TestZigDialogsAGolden` (`txtExport/*`) |
| publish ▸ export preview | Zig (`dialogs_a.zig` `renderExportPrev`; local + remote arms) | `TestZigDialogsAGolden` (`exportPrev/*`) |
| publish ▸ rename set | Zig (`dialogs_a.zig` `renderRename`) | `TestZigDialogsAGolden` (`rename/*`) |
| publish ▸ fix start times | Zig (`dialogs_a.zig` `renderFix`) | `TestZigDialogsAGolden` (`fix/*`) |
| player ▸ export preset editor | Zig (`dialogs_a.zig` `renderPreset`; loudness block stays raw) | `TestZigDialogsAGolden` (`preset/*`) |
| cue editor ▸ pattern manager | Zig (`dialogs_a.zig` `renderPatMgr`) | `TestZigDialogsAGolden` (`patMgr/*`) |

| vrchat ▸ groups dialogs | Zig (`native/zigui/src/dialogs_b.zig`; `#vrcg-role-body` · `#vrcg-inv-list` · roles + invite shells · kick/ban + post-delete confirms) | `TestZigVgDialogsGolden` |
| worlds ▸ dialogs | Zig (`dialogs_b.zig`; list editor · poster editor · friend/group/role pickers + `#world-fr-list`/`#world-grp-list`/`#world-role-list` · GitHub device code) | `TestZigWsDialogsGolden` |
| automations ▸ editor | Zig (`dialogs_b.zig` `AeModal`; block-list form + step cards) | `TestZigAutoEditorGolden` |
| automations ▸ run now | Zig (`dialogs_b.zig` `ArModal`) | `TestZigAutoRunNowGolden` |
| automations ▸ schedule editor | Zig (`dialogs_b.zig` `AsModal`) | `TestZigAutoScheduleGolden` |
| motion ▸ point-cloud viewer dialogs | Zig (`dialogs_b.zig`; viewer shell chrome + GPU prompt) | `TestZigPCViewerGolden` |
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
- Tooltips were pre-rendered HTML in phase A (`tipTopic(id)` in state, Zig `raw`s it); since
  **B-1b shard 2** they cross as structured `tipSt` and the tab renderer composes the card.
  Same for smart-select labels that carry a tooltip (`selectBoxTip`): state holds the resolved
  `selState` plus a structured `ssLabelSt`, not a `<span class=ss-label>…</span>` string. The raw
  fields survive only as the dual-field bridge until the post-merge cleanup.
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

Player-port notes (wave 4 - the most embedded surface: the library inspector Player body and BOTH
publish captures panes ride it, and it is the 30 fps one). `mpHTML(host)` keeps its exact signature,
so the three embed sites needed ZERO edits; the split is `render_player.go` (state builders + PURE
renderers + bridges) while `player.go` keeps the float/SVG engine and the raw seams. NINE exports,
one per patch target (`player_actions.go mpPatch*` patches root/vid/wave/tp/edit/export/ro/hov
independently), each with its OWN state builder - building the whole state for a tiny `-ro` patch
would register smart selects the Go path never registered at that point.
- **The 30 fps waveform stays GO by design** (same rule as keywheelSVG / the campath viewer):
  `mpWaveSVG` + `mpLoudPath` + `mpWaveLoudViz` own all `%.1f`/`%.2f` geometry, the beatgrid/cue/
  drop/trim markers, the spectral band paths AND the `mp-<host>-ph` / `mp-<host>-ph-veil` ids the
  client rAF runtime (`shell.go __rt 'ph'`) rewrites per frame. It crosses as ONE raw `svg` field;
  `player_realtime_test.go` still pins it from the Go side. Nothing ported carries an rAF contract.
- **Host is a state field, ids are composed in Zig** - with one QUIRK replicated: `mp-<host>-root`
  ESCAPES the host (`mpHTML` called `html.EscapeString`), every other id splices it RAW. Composite
  attribute values (`attrQ("mp-hin:"+host)`) need no scratch buffer: `esc` is per-byte, so
  `"` + esc(prefix) + esc(host) + `"` is byte-exact (`attrComposite` in player.zig).
- **The `<video>` element's inline JS handlers ride as attrQ VALUES, not raw markup.** They are
  built Go-side (they carry the `%.3f` config volume and the `mp-vtick:`/`mp-verr:` acts) and both
  renderers attrQ them identically - so the `data-mse` / `data-mse-src` MSE swap that `__mse` drives
  keeps its exact bytes. `mse != ""` selects the MSE variant; an explicit flag, never "empty means".
- **Two Go emitters that looked identical were NOT.** `mpEncChip` opens `class=wchip` (unquoted,
  `data-label="enc-chip"`), `mpLoudChip` opens `class="wchip loud"` (quoted, `data-label="lufs-chip"`
  + two `wc-link` rows). They are now ONE `mpChipSt`/`renderChip` with a `loud` flag; the two
  hardcoded EBU/LUFS URLs travel in state and are spliced UNESCAPED both sides (Go source literals).
  Both "dim" loading pills splice their i18n text UNESCAPED while the seek-table chip beside them
  ESCAPES its - replicated, golden-gated.
- Same class of quirk in the hover readout: `mpReadoutLine` escapes the `@ clock · M x LUFS` and
  momentary-at-playhead branches but returns the measuring/hover-hint i18n strings RAW - carried as
  `{text, raw}` rather than "fix"ing one side.
- **Raw seam left deliberately (phase A): the shared loudness block** (`components.go
  loudnessFields`) incl. its `extraHTML` (`mpLoudExtraHTML` gain-plan line + pre-listen toggle) and
  the standalone "preset normalizes without an override" copy. Same precedent as
  `libDetailSt.Loudness`: ONE components.go markup source shared with the library preset builder +
  automation transcode steps; porting it belongs to a components-level pass, not the player.
  **CLOSED in phase B-1a** (see "Phase B-1a" below) - only `LoudExtra` and the tooltips stay raw.
- Side-effect ORDER is load-bearing (peers/cue-edit lesson again): the state builders register the
  smart selects in the pre-split EMIT order - `mp-track-<host>` / `mp-more-<host>` (transport) before
  `mp-auto-<host>`, then `mp-preset-<host>-<i>` per media, then `mp-scope-<host>`.
- Every number is pre-formatted Go-side: clocks (`pubClock`/`pubClockF`/`mpSignedClock`), the
  `progressPct` "%.1f%%" bars, LUFS/kbps/size/`mmss`/`humanBytes` chip rows, and the `%.2f` marker
  offsets that double as the jump-to-track select's VALUES. The transport sliders reuse
  `render_media_shared.go uiSlider`'s dual-number shape.
- Zig keyword traps: `align` and `export` are keywords, so the json tags are `alignRow` /
  `exportPane`; the media-switch items reuse `components.zig Tab` directly (identical shape) instead
  of a local type, so no per-render conversion buffer is needed.
- Zero new components.zig helpers and no tab-local variant: `btnOf`/`btnRowOpen+Close`/`fieldOf`/
  `slider`/`selectBox`/`subTabs`/`hint`/`progressBar` covered everything. components.go,
  components.zig, render_media_shared.go and all three embed sites are byte-for-byte untouched.
- Zero-DOM-change proof (the double proof): (1) a throwaway dump harness rendered **22 mpSt fixtures
  x 9 surfaces** through the SAME entry points in a HEAD worktree and in the split tree -
  `diff -r` clean (loopback media URLs carry a random port+token, normalized identically on both
  sides); (2) a markup-literal multiset diff vs HEAD (217 -> 210) whose every drop is the dedup of
  the two chip emitters and whose every add is a doc-comment string or a split of an existing
  literal - zero new markup. Then a deliberate one-byte Zig perturbation failed 66 golden
  assertions before revert.
- Go-runtime workarounds replicated for parity, flagged for phase B: the per-render `mpEngineState`
  re-reads (the transport, the hover line and `mpPlayheadAxis` each snapshot the proxy mirror
  separately - a Go-side memo would change nothing in the DOM but exists only because the render
  runs on the serialized act worker), `mpResync`'s "re-emit the root fragment after every render"
  dance (a race workaround for slow Go renders being overwritten by async analysis applies), and the
  phase-A per-render state->JSON->parse round trip itself. The `mpPushRealtime` rAF hand-off is NOT
  a workaround - it is the feature.

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
  (`components.go loudnessFields` - same seam the library encode builder keeps; structured in phase
  B-1a), the time-fix
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

Dialog-sweep-B notes (wave 4: the feature-tab dialog families — VRChat ▸ Groups, Worlds,
Automations, point-cloud viewer). All six families follow the modal recipe above; one Zig file
(`native/zigui/src/dialogs_b.zig`) holds every mirror, exports live in the `// --- dialogs-b ---`
blocks of root.zig / raveui.h / zigui.go / zigui_stub.go.
- **Files with NO i18n keys carry their prose as state anyway.** vrchat_groups + worlds dialogs are
  hardcoded English source literals; several are spliced UNESCAPED by Go (ws-help paragraphs carry
  apostrophes, the friends-list overflow marker, form placeholders, submit-button labels), so they
  travel in state and BOTH renderers emit them raw — same rule the Worlds TAB port set. Only fixed
  sentence fragments that never vary stay as renderer literals in both files
  (`? This cannot be undone.`, the kick/ban `<b>…</b> from …?` frame, the reloc-style `→ `).
- The kick/ban confirm's **verb is raw in the body and escaped in the button**: Go splices
  `verb + " <b>"` unescaped but passes the same string through `btn()`. Replicated exactly.
- **Three in-modal patch targets got their own exports** (`#vrcg-role-body`, `#vrcg-inv-list`,
  `#world-fr-list`/`#world-grp-list`/`#world-role-list`) and the shells embed the same renderer, so
  a shell render and a later patch cannot diverge. The role-list "Loading roles…" arm folded into
  the fragment state (`loading`), replacing the shell's hardcoded placeholder markup.
- **Two more side-effect-ordered builders** (peers/cueedit lesson again): `wsListEditorState` writes
  `wsState.editList` before rendering (entry actions index it) and `wsGroupListState` records
  `wsState.pickGroups` in DISPLAY order (fav/roles acts index that). `vgInviteListState` writes back
  `shownFriends` — the invite-pick indices are the filtered order, not the friends order.
- **The automations forms are a BLOCK LIST** (`aeBlockSt`, settings-port shape) shared by the
  automation editor AND the schedule editor: `field|fpair|toolbar|toggle|select|selraw|fpairsel|
  hint|pbhint|raw`. `dlgFieldSt` is a local twin of `uiField` with a `tip` — the media batch's
  `uiField` has no tooltip field and components.zig `Field` already carries `tip`, so a local Go
  struct with matching json tags needed ZERO components.zig change. `selraw` (the tooltip'd trigger
  picker, Go `smartSelectRaw`) and `fpairsel` (the daily hour/minute pair) are the only kinds the
  editor itself didn't need.
- Raw seams in the automations dialogs: every `tip` (tipTopic) and the loudness override block
  (`loudnessFields`, float-formatted Go-side) — renderer ownership wins, same rule as the media
  batch. The loudness half is structured as of phase B-1a (block kind `raw` → `loud`).
- **Explicit flags over "empty means the other branch"**, as ever: the run-now footer carries
  `gated` + `why` vs `variant` (btnGated names the missing precondition instead of hiding the Run
  button), and `erases` (Go `autoChainDeletes`) gates both the acknowledgement block and the footer
  wording.
- The point-cloud dialogs hand-roll their chrome (extra `pcv-modal` class, `pcv-close` on scrim/✕
  so GL is disposed), so the bracket is LOCAL to that pair in both renderers — components.zig
  `modalOpen` is untouched. Only structural chrome crosses the ABI: `#pcv-canvas` is pc_viewer.js
  (THREE.js) and the transport controls carry no `data-act` by design (no Go round-trip per frame).
- **Zero new components.zig helpers and zero modifications** in this whole batch — the existing kit
  (`modalOpen/Foot/FootDefault/Close`, `fieldOf`, `toggleOf`, `kvOf`, `btnOf/btnRowOf`, `btnGated`,
  `selectBox/selectBoxRaw`, `itemRow*`, `card*`, `section*`, `fpair*`, `hint`, `emptyState`) covered
  everything; the family-specific chrome (`wsHelp`, `wsPosterField`, the pcv bracket) is local to
  dialogs_b.zig.
- Proof per family: the golden gate (106 subtests) plus a literal-multiset diff of the pre-split
  emitters vs the split ones — every drop is dedup (chrome that existed twice now once) or a
  `fmt.Sprintf` → concatenation, zero content lost. A deliberate one-byte Zig perturbation failed 4
  subtests before revert (and confirmed `go test` needs `-count=1` after a lib rebuild — the cgo
  archive is NOT part of the test cache key, so a stale PASS is possible without it).

Not ported, with reasons (dialog sweep B):
- `actionmenu.go` — already fully ported in the publish/library batches: `actionMenu()` delegates to
  `resolveActionMenu` + `actionMenuHTML`, mirrored as components.zig `actionMenu`. Nothing left.
- `pickers.go` / `pickers_windows.go` / `pick_actions.go` — **no HTML at all**. These are the native
  OS dialog bindings (IFileDialog) plus the `pick-dir:`/`pick-file:` act redispatch; the only markup
  involved is the `Browse…` button the CALLING surface renders (already ported per tab).
- `tooltip.go` `tipTopic` — deferred in phase A, **PORTED in phase B-1b** (shard 1: the mechanism
  + the 4 densest consumers; shard 2: every remaining call site). The phase-A assessment below is
  kept as the record of WHY it waited; the phase-B sections after it are the shipped design.

**tipTopic assessment (why the tooltip primitive stays Go).** `renderTip` is 40 lines of markup and
would port cleanly in isolation (a `label.tt` + hidden checkbox + inline SVG + `tt-card`, one
keybind grid, `\n\n`-split body paragraphs, a link list). The cost is not the renderer — it is the
CONTRACT. `tipTopic(id)` is called 70 times across 18 files and its output is embedded INSIDE other
components (`fieldEx`'s tip slot, `ss-label` spans, `sectionOpenTip`, `pb-hint` tails, card heads),
which is exactly why every migrated tab already carries it as a pre-rendered trusted raw string.
Porting it would mean either (a) threading a structured `tipSt` through every one of those call
sites and every state struct that currently has a `tip []const u8` field — a breaking change to
~15 already-merged state contracts and their goldens, for markup that is byte-identical either way
— or (b) exporting `rz_ui_render_tip` and having Go call INTO Zig per tooltip during a Go render,
which adds a state→JSON→parse round trip per tooltip (multiple per card, ~40 on the settings tab)
on the serialized actWorker and buys nothing while the surrounding renderer is still Go. Neither is
worth it in phase A. Two further reasons to wait: `renderTip` resolves i18n INSIDE itself
(`i18n.T("help."+id+".title")`, plus `i18n.T` per keybind row and per group header) so a port needs
the whole `helpTopics` registry (keybind grids for cue-edit/wave-nav, `virtualMIDILinks`) resolved
into state, and `kbEmph` splits the action label on the first whitespace to bold the leading verb —
locale-dependent text processing that belongs on the Go side of the seam. Recommendation: port
`tipTopic` in **phase B**, when the shell itself is Zig and a tooltip can be composed in-process
without crossing the ABI; until then the pre-rendered-raw contract is correct and already
golden-gated everywhere it appears.

## Phase B — wave B-1

Four parallel shards. B-1a below; the sibling sections that follow cover B-1b (tipTopic →
`tipSt`), the RZW1 binary wire and the B0 baseline.

### B-1a: the shared loudness block (`loudnessFields` → `loudSt`)

Phase A left the loudness block riding through FOUR state contracts as pre-rendered raw HTML
(`libEncSt.Loudness`, `mpPresetDlgSt.Loudness`, `aeBlockSt.Raw`, `mpExMediaSt.Loud`). It is now
structured state; `components.zig` `loudnessFields` (marker block `phaseb-loud`) renders it.

- **Shape:** `components.go loudSt` = `{compact, toggle, tip, chipAct, chips[], iField, tpField,
  raise, hasWarn, warn, extra}`. Every string is final Go-side: i18n resolved, `trimNum`'d floats,
  `%g|%g` chip payloads, `ltChipLabel` text, `strings.ToLower` data-labels. `toggle.on` gates the
  whole body - the same single source Go always had (`o.vals.On` drives both switch and branch), so
  there is no second "shown" flag to desync. `hasWarn` IS explicit (a blank i18n string must not
  switch arms).
- **Two raw seams stayed, deliberately:** `tip` (Go `tipTopic`, owned by B-1b — structured in its
  shard 2) and `extra` (the
  caller's `extraHTML`: the export surface's live gain-plan line + pre-listen toggle, which must
  collapse with the switch). `mpExMediaSt.LoudExtra` also stays raw - it is a *different* line (shown
  when the PRESET normalizes without an override), not part of the block.
- **ONE Go markup source:** `loudnessFields(o)` is now `newLoudSt(o).html()`. The Go renderers call
  `st.Loud.html()`, so the untagged fallback and the Zig mirror render the same tree from the same
  state. Same trick as `progressBarStr` / `toggleRowDL`: a caller-resolved twin, never a fork.
- **`pbField` MOVED** `library_kit.zig` → `components.zig` (library_kit re-exports `PBField` +
  `pbField`, like it already re-exports `Select`/`Btn`/`Tab`). The loudness block needs pb-field
  markup and `components.zig` cannot import `library_kit.zig` without a cycle; duplicating the
  markup would have forked it. This mirrors Go, where `pbFieldExDL` (render_library.go) is the one
  source both paths call. Zero markup change - library_kit's original `pbField` test now doubles as
  an alias-resolution proof.
- **No new exports.** The block is embedded in views that already have exports
  (`RenderLibraryDetail`, `RenderDlgPreset`, `RenderAutoEditor`, `RenderPlayerExport`), so
  `root.zig` / `raveui.h` / `zigui.go` / `zigui_stub.go` are untouched -
  `zigui.FallbackCounts()` gains no whole-view entry by construction.
- **The dual-field bridge collapsed.** A grep proved those four are the ONLY consumers and all four
  migrate in this wave, so the raw fields are dropped instead of bridged (the briefing's end state).
  `aeBlockSt`'s generic `raw` kind went with it - loudness was its only user; the kind is now `loud`.
- **Parity proof (the strongest gate here):** `loudness_test.go` keeps the pre-split implementation
  VERBATIM as `loudnessFieldsLegacy` and asserts byte equality against both `loudnessFields` and
  `loudSt.html()` over a 24-fixture matrix (`loudFx()`): off / off-compact / full / no-raise /
  override-unset / override-I-only / override-TP-only / no-preset / copy-codec / no-audio-codec /
  compact default+Apple+Club+no-match+builder / compact extra / compact copy-codec / chip-tolerance
  edge (`|effI - lt.I| < 0.01`: -14.009 matches, -14.02 does not) / long decimals / zero / positive
  / escaping / unicode. `loudFx()` is UNTAGGED on purpose, so the same matrix drives all four Zig
  golden suites (24 subtests each: `TestZigLibEncLoudnessGolden`, `TestZigPresetLoudnessGolden`,
  `TestZigAeLoudnessGolden`, `TestZigPlayerLoudnessGolden`) - a new state axis is exercised
  everywhere at once.
- Two contract tests ride along: `TestLoudStResolvesEverythingGoSide` (data-labels lowercased
  Go-side, `number` input types, chips only in compact, nothing behind an off switch) and
  `TestLoudStNoNullSlices` (`chips` needs `,omitempty` + Zig `&.{}` - the usual null-slice trap).
- **Gotcha worth repeating:** `omitempty` does nothing for a struct field, so a zero-value nested
  `loudSt` still marshals fully - fine here (all its own slices are omitempty), but never rely on
  `omitempty` to make a nested struct disappear.

## tipTopic → structured `tipSt` (phase B-1b shard 1) — SHIPPED

The phase-A assessment above ruled out both bad options. What shipped is neither: the tooltip is
composed **in-process by the tab renderer it sits in** (no per-tooltip ABI round trip), and the
state contracts grew a `tipSt` **beside** the raw string, so nothing already merged broke.

**Split.** `tooltip.go` now has two halves. `tipState(id,title,body,keys,links)` resolves
EVERYTHING locale- and registry-dependent Go-side — `helpTopics` prose (`help.<id>.title/.body`),
`virtualMIDILinks()`, one `i18n.T` per group header / action label / `@`-prefixed mouse-gesture
word, the `kbEmph` first-whitespace verb split (now `kbSplit`), and the `

` body paragraph
split with its trim-and-drop-empties rule. `renderTipSt(tipSt)` is then PURE markup and is the
golden reference for `components.zig renderTip`. `renderTip(...)` = `renderTipSt(tipState(...))`,
so `tip()`, `tipTopic()` and `tooltip_test.go` are byte-unchanged.

**State shape** (`omitempty` on every nested slice, per the null-slice gotcha):
`tipSt{id,title,keys[],paras[],links[]}` · `tipKbSt{hasGroup,group,chips[],verb,rest}` ·
`tipChipSt{text,sep}` · `tipLinkSt{label,url}`. Two decisions worth keeping:
- **`hasGroup` is an explicit bool**, not "group is non-empty". The section dedup runs on the i18n
  KEY (`r.Group != curGroup`), so two distinct keys that resolve to the same text still emit two
  headers — and a catalog that maps a group key to `""` still emits an empty header, exactly as the
  pre-split renderer did. "Empty means the other branch" would have silently dropped it.
- **`verb`/`rest`** carry the kbEmph split, not the raw label: the split point is locale-dependent
  text processing and belongs on the Go side of the seam.

**Dual-field bridge.** Every migrated contract has `tipSt` (structured) next to `tip` (legacy
pre-rendered). Both renderers resolve through one helper — Go `tipOr(*tipSt, raw)`, Zig
`components.tipOr(h, ?Tip, raw)` — structured wins, else raw. So the 14 files NOT in this shard
(`automations_runnow.go`, `bridge_actions.go`, `library_mirror.go`, `player.go`,
`render_library_cueedit.go`, `render_library_state.go`, `render_live.go`, `render_midictl*.go`,
`render_motion.go`, `render_vrchat*.go`, `update_actions.go`, `components.go`'s own
`selectBoxTip`/`resolveSelectBoxTip`) are literally untouched and keep working. Wave B-2 flips
them (see "shard 2" below); the raw fields die only when the last one is gone.

**Consumers migrated (shard 1):** `render_settings.go` (13 sites — card head, `sbFieldTip`,
`sbToggleTip`, fpair kids), `render_player.go` (4: `tipWave` / `tp.tipVideo` /
`editBox.tipTrim` / `alignRow.tipAlign`), `automations_editor.go` (8), `automations_schedules.go`
(7, including the `selraw` **ss-label** — `aeLabelSt{text,tip}` replaces a pre-rendered
`<span class=ss-label>` string).

**Where the Zig code lives, and what it deliberately does NOT touch.**
- `components.zig` marker block `// --- phaseb-tip ---`: `Tip`/`TipKb`/`TipChip`/`TipLink` +
  `renderTip` + `tipOr`. The existing shared `Field` struct and `fieldEx` are **unchanged** —
  `Field` is used by every migrated tab, so `dialogs_b.zig` grows a local `DlgField` twin
  (`Field` + `tipSt`) instead, exactly the precedent the dialog sweep set for `dlgFieldSt`.
- Primitives that take the tooltip as a **string** (`fieldEx`, `toggleRowTip`, the ss-label) are
  fed from a scratch `Html` buffer:
  ```zig
  var tb = Html.init(h.a);
  defer tb.deinit();
  try c.tipOr(&tb, b.tipSt, b.tip);
  try c.fieldEx(h, .., tb.b.items);
  ```
  One allocation per tooltip, and — the point — **zero duplication** of `fieldEx`'s markup. Never
  re-emit a primitive's HTML to avoid a buffer; that is how the two renderers drift.
- `rz_ui_render_tip` exists (root.zig / raveui.h / zigui.go / zigui_stub.go marker blocks) purely
  as a **parity-gate export**. Production never calls it per tooltip — the tab renderer composes.

**Gates.** `zigui_golden_tip_test.go` is the primitive's own gate: **527 byte-equality subtests** =
every `helpTopics` id (73) × every installed locale (7) + 16 edge fixtures (empty, escaping-heavy,
unicode, long-verbose, blank-paragraph runs, CRLF body, chip edges, repeated/empty kb groups,
multi-link, tab-split verb, ad-hoc `tip()`). It also asserts `tipTopic(id) == renderTipSt(tipState)`
per topic, so the split itself is proven lossless. A one-byte Zig perturbation
(`tt-title`→`tt-titl`) failed **all 527**. Per-consumer suites got tooltip fixtures on top of their
existing states (settings 77 · player 45 · automations 66 subtests), covering
present / absent / multi-link / keybind-grid + the raw-bridge fallback in every locale. The
automations tip test carries an **inertness guard**: the grid mutation must change the bytes and
emit `tt-kb-keys`, and the raw-bridge variant must reproduce the structured bytes exactly — a
fixture whose mutation reaches no tooltip is worse than no fixture.

**Standing rule.** Help texts are LONG and verbose BY DESIGN (owner directive, on record): the app
teaches while it is used. Never trim, truncate, elide or "summarise" a `help.*` string, and never
add a length cap to the renderer.

### shard 2 (wave B-2) — the remaining 14 files, plus the ss-label

Note first: the per-tab notes ABOVE that describe tooltips as "pre-rendered raw markup in state"
are the phase-A record. Shard 2 makes them historical — no state contract ships tooltip HTML.

Shard 2 finishes the sweep: **no state builder produces tooltip markup any more.** `tipTopic(` has
ZERO production callers, pinned by `TestNoProductionCallerShipsRawTooltipMarkup` (untagged, in
`tooltip_test.go`) — a **source scan**, because a missed call site renders correctly today and
would only break when the raw bridge fields are dropped post-merge, far from the cause. Two
surfaces have no state contract to carry a `*tipSt` and call the new `tooltip.go tipTopicHTML`
(= `tipOr(tipTopicSt(id), "")`): the nav-rail update block (`update_actions.go`, Go-rendered shell)
and the pre-listen row (`player.go`), which lives inside the loudness block's caller-owned
`extraHTML`. When that extra block is lifted to state, its tip travels as `tipSt` like the rest.

**Flipped:** `components.go` (`selectBoxTip`/`resolveSelectBoxTip` + `loudSt`, i.e. all four
loudness surfaces at once) · `render_settings.go` (`sbSelectTip`) · `render_library_state.go`
(`resolvePbSelectTip`/`libSelTip` → encode builder + preset dialog) · `render_midictl*.go` (7
tooltips + 4 ss-labels) · `render_live.go` (4 section heads) · `render_motion.go` (2 preview cards)
· `render_vrchat.go` + `render_vrchat_groups.go` · `automations_runnow.go` · `bridge_actions.go` ·
`library_mirror.go` · `render_library_cueedit.go` · `player.go` · `update_actions.go`.

**The ss-label became state too.** `<span class=ss-label>` + escaped label + tooltip was
pre-rendered in FOUR places (`components.go` twice, `render_library_state.go`, and shard 1's
`aeLabelSt`). It is now one type, `components.go ssLabelSt{text,tip}` / `components.zig SsLabel`,
with `aeLabelSt` a Go **alias** of it and `dialogs_b.zig AeLabel = c.SsLabel` — no fork. The
select-plus-label dispatch is one helper per side: Go `ssSelHTML(sel, *ssLabelSt, raw)` and Zig
`selectBoxTipOr` (structured → legacy raw → the plain label the select state carries; `selHTML`
with an empty `Label` is byte-identical to `selHTMLRaw` with an empty label, which is what made
collapsing the three arms safe). `selectBoxTip` now delegates to `resolveSelectBoxTip` +
`selHTMLRaw`, so the Go-only path shares the resolver instead of a second copy of the span.

**New Zig helper: `tipBuf`.** Many primitives take the tooltip as a STRING (`sectionOpenTip`,
`cardOpen`, `cardLabel`, `toggleRowTip`, `fieldEx`). `c.tipBuf(h, tipSt, raw)` returns a
CALLER-OWNED scratch `Html` (`var tb = try c.tipBuf(...); defer tb.deinit();`), which replaced five
hand-rolled buffers. The rule it protects is shard 1's: one allocation per tooltip, and **never**
re-emit a primitive's markup to avoid the buffer.

**Two traps worth remembering:**
- **A `?Tip` on the Go side is invisible to a Zig struct that only has the raw field.**
  `dialogs_b.zig ArModal.file` was `components.Field` (raw `tip` only), so the run-now dialog's
  structured tooltip was silently DROPPED on the Zig path — Go rendered the card, Zig did not. Fix:
  the local `DlgField` twin + `renderDlgField`. Whenever you add `tipSt` to a Go state, check what
  the Zig side actually decodes into; `ignore_unknown_fields` makes the miss silent. Caught by the
  existing per-tab golden suite, not by the new fixtures.
- **A structural flag computed from the raw tip string must move to the RESOLVED tooltip.**
  `vrcgroups.zig` decided the announcement card HEAD with `annTitle.len != 0 or annTip.len != 0`
  (mirroring Go `card()`, where the trailing string gates the head). With the tooltip structured
  the raw string is empty, so the head vanished. It now tests `tipBuf`'s output — gated by a
  no-title fixture where absent vs present flips the whole `card-head` element.

**Non-append-only edit:** `Loud.tipSt` inside `components.zig`'s `phaseb-loud` marker block. The
field it replaces lives there; there is no way to bridge it from another block (same class of
unavoidable edit as B0's counter inside `zigui.go render()`).

**Gate.** `zigui_golden_tip2_test.go` (`//go:build zigui`) — ONE file instead of edits spread over
ten per-tab golden files, which also keeps it clear of the wire fan-out touching those same files.
`tip2Sweep` drives a surface through 4 tooltip SHAPES (absent / plain prose / multi-link /
23-row keybind grid, all real registry topics so a catalog change reaches the fixtures) × 2 locales
(`en` + `ja`), each with its raw-bridge twin, through the tab's real export and fixture builder:
**144 byte-equality subtests** over loud · aeSelRaw · setSelect · setSelectKid · libEncSel ·
presetSel · midiSsLabels · midiTips · live · motionCam · motionStudio · vrcEditor · vrcgAnn ·
vrcgAnnNoTitle · arModal · bridgeCard · mirrorBanner · ceTopbar. Each subtest carries the shard-1
inertness guard (the fixture must CHANGE the document bytes, a grid fixture must emit
`tt-kb-keys`, the raw arm must reproduce the structured bytes). Falsified by execution:
`ss-label` → `ss-labeX` in `components.zig` failed 64 assertions; restoring one `tipTopic(` call
failed the source guard with file:line.

**The raw fields stay.** `tip`/`labelHtml`/`selLbl`/`portLbl`… and `tipOr`'s raw arm are still
there on purpose: dropping them is a separate post-merge cleanup, so this shard's diff stays
additive against the sibling wave-B-2 branches. That cleanup must also drop the label bridges and
`tipTopic` itself (only `tooltip_test.go` / the golden suites still use it, as the pre-split
reference).
## Phase B — RZW1 binary state wire (wave B-1 pilots: appgroups + logs)

The phase-A bridge pays a state→JSON→parse round trip on EVERY render (flagged twice above as
the phase-B tax). Wave B-1 replaces it with a length-prefixed TLV document for two pilot views.
JSON exports STAY; the binary ones land alongside and the bridge dispatches **v2 → v1 → Go**
(`internal/webui/wire.go zigWire`), so a stale lib or a malformed document degrades instead of
breaking. Both downgrades are visible in `zigui.FallbackCounts()` (`Render<X>V2` = the binary
export declined, `Render<X>` = the JSON one did).

**ONE schema generates BOTH sides.** `internal/zigui/wiregen/schema.go` lists the messages
(Go type ↔ Zig type, field number, kind, ref) and emits `internal/webui/wire_gen.go` (encoder
methods) + `native/zigui/src/wire_gen.zig` (decoders writing into the EXISTING renderer state
structs, so v1 and v2 feed the same renderers - that is what makes byte equality provable).
Regenerate with `GOWORK=off go run ./internal/zigui/wiregen` (or the `//go:generate` line in
`internal/zigui/wire.go`); never hand-edit either output. Wave B-2 fans this out to the
remaining ~101 state structs by adding rows, not code. Rationale: hand-mirroring 300+ structs
across a C ABI is silent memory corruption waiting to happen.

**Format** (documented in `internal/zigui/wire.go`; decoder `native/zigui/src/wire.zig`):
magic `RZW1`, u16 message id, u32 schema hash (FNV-1a of the schema text), u32 arena length,
ONE strings arena, then a field-tagged body terminated by a 0 byte. Tag = uvarint
`num<<3 | wiretype`; wiretypes varint / string(arena off+len) / struct(u32 len + body) /
list(uvarint count + u32 len + bodies). Field number 0 is the terminator, never a tag.
- **Strings decode ZERO-COPY** as slices into the caller's buffer (valid for the render);
  only lists allocate, from one parse arena freed by `Parsed.deinit()`.
- **The omitempty/null hazard is now unrepresentable.** There is no null; zero values are
  absent tags; an empty list IS the absent tag and absent decodes to the zero value. The bug
  class that silently dropped a whole tab to Go (nil slice → JSON `null` → Zig parse reject)
  cannot be expressed. `TestWireEmptyListsAreAbsentNotNull` + `TestWireZeroValuesAreAbsent`
  pin it, including nested and all-zero states.
- **Unknown field numbers are skipped** (every payload is self-delimiting) - the replacement
  for std.json's `ignore_unknown_fields`, pinned by `TestZigWireSkipsUnknownFields` across all
  four wiretypes. An unknown WIRETYPE is not skippable and is rejected: additive changes only,
  and anything else trips the schema hash.
- **Message id + schema hash in the header** mean a stale `libraveui.a` or a document sent to
  the wrong export is refused, not mis-decoded.

**Bounds discipline (the fuzz gate rests on it):** a struct/list payload length is checked
against the REMAINING bytes of its parent body and the child reader sees exactly that slice; a
list count is checked against its payload length (every element body costs >= 1 byte, its
terminator) so a huge count cannot drive an allocation bigger than the input; a body must end
exactly on its terminator (no truncation, no trailing garbage).

**Gates.** Three-way byte equality Go == v1(JSON) == v2(binary) over the FULL existing fixture
sets, full document AND every fragment (`zigui_wire_test.go`; 12 fixtures x 2 surfaces), with
`FallbackCounts()` asserted unchanged across the run. Mutation fuzz on the real exports
(`zigui_wire_fuzz_test.go`): 1575 cases (18 base documents x 10 corruption classes x 2 reps x
4 exports, cross-fed + 120 random buffers + 15 adversarial documents) → 1321 clean rejections,
254 renders, zero crashes (stable run to run: the base list is SORTED, or the fixture maps'
random iteration order defeats the fixed seed and a failure stops reproducing). Two canaries make "no OOB" falsifiable: every buffer is copied into
the middle of a **poison-filled allocation** and only the inner slice crosses the ABI (a read
past the end drags the marker into the HTML), and each buffer renders twice with the results
compared. Verified by execution - deleting the arena bounds check in `wire.zig str()` made the
gate fail on the second mutant.

**Numbers** (Ryzen 9 5950X; whole dispatch = serialize + Zig render): appgroups full
5840→4003 ns/op, body 5469→3403, logs full (400-line tail) 410818→158297, `#log-view`
415823→162025 (-61%), serialize alone 85714→44020 (-49%). Documents are 80.5% (appgroups) /
51.1% (logs fixtures) / **17.8%** (full 400-line tail: 9225 B vs 51862 B) of the JSON.
The ~1 Hz `#log-view` tick - the reason this pilot exists - saves ~253 us per tick.
`WireWriter` preallocates 1 KiB per buffer: re-growing two slices per render was 14 of 31
allocs and ~9% of encode time (the GC pressure this format exists to remove).

**When you add a message (wave B-2):** add the row to `schema.go`, regenerate, add the
`_v2` export in your root.zig/raveui.h marker block, add the binding + stub, switch the bridge
to `zigWire(...)`, and extend the owning golden suite to assert Go == v1 == v2. Field numbers
are the wire contract: append only, never renumber, never reuse. `kUint` exists in the schema
kinds but no pilot field uses it yet (renderers take pre-formatted strings by design - rule 6:
Go formats every number).

**Four kinds the pilots did not need** (wave B-2, `schema.go` helpers `sa` / `op` / `ov` / `sl`;
all four are append-only additions to the codec, the wiretype set is unchanged):

| kind | Go ↔ Zig | why it exists |
|---|---|---|
| `kStrAlways` (`sa`) | `string` ↔ `[]const u8` with a **non-zero default** | absent means "the Zig default", and `fill: []const u8 = "0.00%"` / `stepS = "1"` are not "". The JSON path always sends the field, so v2 must too: `WireWriter.StrAlways` emits the tag with off 0 / len 0. Without it the two paths diverge on exactly the states where the field is empty. |
| `kOptPtr` (`op`) | `*T` ↔ `?T` | tag present iff the pointer is non-nil (motion's inactive section is `nil`). |
| `kOptVal` (`ov`) | `T` ↔ `?T` | tag **always** present: JSON always sends the object, so `null` must be unreachable. `Struct` drops an all-zero message (absent = zero value, which is correct for a value field but would decode as `null` here), so both opt kinds use `OptStruct`, which keeps the tag. |
| `kStrList` (`sl`) | `[]string` ↔ `[]const []const u8` | encoded as a list of single-field element bodies (field 1 = the string), so `Reader.strList` reuses the list bounds discipline verbatim and `skip()` stays closed over four wiretypes. |

The rule behind all of them: **a Zig field default that is not the zero value is a v1/v2
divergence waiting to happen.** `sa` is the fix; the scan is mechanical (any non-omitempty Go
field whose Zig counterpart has a non-empty default). Proven by execution: flipping live's
`Fill` back from `sa` to `s` makes `TestWireStrAlwaysKeepsEmptyFill` fail with the Zig default
(`width:0.00%`) rendered where Go renders `width:`. Note that the six live golden fixtures all
carry a non-empty `Fill`, so the whole-fixture gate does NOT catch it - a hazard needs its own
fixture, not a bigger suite.

**Fanned-out views (wave B-2).** `_v2` exports live in the `phaseb-wire` block of root.zig; the
fragment exports keep their JSON twin's `kind` selector, and every fragment is its own root
message so the header id still refuses a document built for a different fragment
(`TestZigWireLiveFragIdsAreDistinct`). Each tab registers its exports + fuzz base documents in
`internal/webui/zigui_wire_b2_test.go` (one block per tab; the fuzz cross-feeds every mutated
document to every export, so the matrix grows on its own).

| view | root ids | exports | fixtures gated |
|---|---|---|---|
| appgroups (pilot) | 1 | full + `#appgroups-body` | 6 |
| logs (pilot) | 2, 3 | full + `#log-view` | 6 |
| live | 10-20 | full + 10 tick fragments (`live_frag_v2`) | 6 × 12 surfaces |
| motion | 21 | full + `#mo-body` (one message, like appgroups) | 7 × 2 surfaces |
| publish | 22, 23 | full + `#pub-hero` | 13 × 2 surfaces (12 heroes) |
| settings | 24-26 | full + `#set-content` + `#stset-<id>` | 18 × 2 surfaces + 6 status states |
| library | 27-31 | full + `#lib-body` + `#lib-detail` + `#lib-queue-body` + cue cell | 21 × 3 surfaces + 3 queue + 4 cell |
| player | 32-40 | full + the nine `#mp-*` patch targets | 178 surfaces over the fixture set |
| automations | 41, 42 | full + `#auto-body` | 6 × 2 surfaces |
| peers | 43, 44 | full + `#peers-body` | 7 × 2 surfaces |

**Fan-out state after wave B-2:** 174 messages, root ids 1-3 (pilots) + 10-44 (this wave), 31
exported `_v2` symbols over 40 render surfaces (live's ten fragments share one kind-dispatched
export), 288 135 fuzz cases. Ids 45-49 are free inside wire2's partition; 100-149 belong to
the fragment scheduler. Documents run **26-78%** of the JSON they replace (peers best, player
worst - see PHASEB_BASELINE.md for why one 29 kB SVG sets that floor), and the whole dispatch is
**27-69% faster** on every view.

**Merge composition with tip2 (B-1b shard 2).** tip2 added `*tipSt` / `*ssLabelSt` fields to eight
states this block had already frozen (`liveState` ×4, `moCamSt`, `moStudioSt`, `setBlock`,
`setKid`, `bridgeSt`, `libSelTip`, `loudSt`) plus the new shared `ssLabelSt` message. All are
`kOptPtr` (nil = no tooltip) and were appended INSIDE the existing messages, so documents already
in flight stay readable. **The merge produced ZERO textual conflicts and still broke the wire** -
which is the whole point of the gate: settings, library and player failed immediately with
`v1==v2: diverges at byte …"tt-mp-loudnes"…`.

**live and motion did NOT fail, and that is the lesson.** Their fixture sets leave the new tooltip
fields nil, so the tab gates stayed green while v2 silently dropped every tooltip - tip2's
`DlgField` gotcha class exactly. The fix is a fixture, not more sweeps of the same states:
`wireTipSweep` (the wire twin of `tip2Sweep`) drives each affected surface through all four tooltip
variants × locales, three ways, with tip2's own inertness guards (the tooltip must change the
bytes; the keybind grid must emit `tt-kb-keys`) plus the raw dual-field arm through v2. Verified by
execution: deleting LiveState's four rows makes `TestZigWireTip2Live` fail while
`TestZigWireThreeWayLive` still passes.

**Keeping the schema honest is mechanical.** The composition was derived by an audit that re-reads
every schema row against the current Go+Zig structs and prints the missing fields with their next
free numbers (11 fields across 8 messages; it also confirmed tip2 changed no `Tip`/`TipKb`/`TipLink`
shape). Re-run it after every merge that touches state structs - "the golden gate will catch it" is
true, but only for states a fixture exercises.

**FallbackCounts assertions are per-export.** `zigui.FallbackCounts()` is process-wide and other
suites drive their own headless UIs concurrently, so a global "no new fallbacks" assertion is
load-dependent (it once failed the logs gate on a stray `RenderLibRemote +1`). Each gate now names
the exports it drives (`assertNoNewFallbacksIn`) and the player's exact-delta variant filters by
prefix; `TestWireFallbackAssertionIsNotVacuous` pins that the narrowed check still catches a
downgrade on a key it names, because a typo'd name would otherwise make every caller green.

**Adding a field to a migrated state is now a two-sided edit.** A Go state field with a JSON tag
and its Zig counterpart are only connected through `schema.go`; add the field without a schema row
and v2 silently stops carrying it - the three-way gate turns that into a byte-diff failure, which
is exactly why the gate covers every fixture rather than a sample. Same for a Zig-side struct
change (a renamed field, a new non-zero default): regenerate and re-read the HAZARD rule above.
## Phase B — B0 baseline instrumentation (bench batch)

Numbers live in **`.devnotes/PHASEB_BASELINE.md`** (machine, commit, tables, cost model, findings).
This section is the mechanism.

- **Benchmarks.** `internal/webui/render_bench_zig_test.go` (`//go:build zigui`) benches four
  families per tab over the EXISTING golden fixtures: `RenderGo` (pure Go renderer) · `RenderZig`
  (Zig export, state pre-marshalled) · `RenderBridge` (`stateJSON` + Zig = today's real cost) ·
  `StateMarshal` (the round trip alone, reports `state_B`). 10 tabs: appgroups logs live motion
  peers automations publish settings library player. `internal/webui/render_bench_test.go` is the
  UNTAGGED half (settings state-build/render/marshal + the four untagged dialog fixtures), so
  `go test -bench` also works on a stub build.
- **Every bench case is parity-gated before timing** (`zigBenchState`): Zig must return ok=true
  AND byte-equal Go. Without that gate a benchmark happily measures a rejected state (JSON null →
  ok=false) and reports a fantastic number for doing nothing.
- **`RenderZig` is NOT renderer-only** - every export parses its JSON first. There is no
  parse-free entry point, so Go-render-vs-Zig-render cannot be compared today; the wave B-2 TLV
  export gives it for free. Fit over the table: Zig parse+render 6.9 ns per STATE byte vs Go
  marshal 1.33 ns/B vs Go render 1.63 ns per HTML byte. Parse cost tracks structure, not size
  (player's 29 kB state is one raw SVG and parses ~2.3× cheaper than the fit).
- **Live counters.** `internal/zigui/perf.go` is UNTAGGED (fallback.go's pattern) so both builds
  expose one counter set: `NoteRender` (cgo render funnel: state bytes + native render ns) +
  `NoteMarshal` (webui `stateJSON`: bytes + json ns) → `zigui.PerfCounts()`. Surfaced as
  `ctl perf` section `[zigui]` (`internal/webui/zigui_perf_probe.go`, registered from `New()`,
  additive) next to the FallbackCounts tally. Cost: two `time.Now()` per render per side.
- **The one non-append-only edit in this batch:** 3 lines inside `render()` in the shared
  `internal/zigui/zigui.go` (t0 / `NoteRender`) - it is the single funnel every Zig render passes
  through, so a counter cannot exist without it. Appended marker blocks merge around it cleanly.
- **Bench gotcha:** `b.ResetTimer()` CLEARS metrics added by `b.ReportMetric` (`b.SetBytes`
  survives it). Report sizes AFTER the loop or they silently vanish from the output.
- **Fixtures were deliberately NOT moved** out of the `//go:build zigui` golden files into untagged
  siblings (the dialogs-A precedent). Three sibling wave-B1 branches were editing those same files;
  a move would have collided. Consequence: the untagged bench table is a subset. Revisit once the
  wave is merged - the tagged tabs' fixtures moving untagged would let one bench file cover both.
- **Measuring on a fleet box:** parallel `zig build`/`go build` inflated worst samples 2-4×. Use
  min-of-N over several `-count=2` runs and treat <20% deltas as noise (method note in
  PHASEB_BASELINE.md).
- Not benched yet: the fragment renderers (`_body`, `#live-*`, the nine player patch targets),
  which the ~1 Hz tick hits far more often than a full tab render. Per-render tax applies per
  FRAGMENT - the next increment should extend the bench there.
## Phase B — B3 fragment scheduler (pilots: live tick + `#log-view`)

B0 finding 5 said the full-tab renders are not a live problem. The FRAGMENTS are what the app
actually pays for: `livePush` hits the active tab's tick ~1 Hz, and the pre-B3 Live tick crossed
the ABI **once per fragment** — up to twelve `stateJSON` marshals, twelve cgo calls, twelve
`std.json` parses, twelve returned strings deduped in Go against `u.frags`. B3 collapses that to
ONE call per tick per surface.

### Shape

Go resolves the whole surface once (`liveTickState()` / `logsLinesState()`), snapshots the hash of
what it last pushed per fragment id, and encodes both into one RZW1 document (root ids **100**
`TkLive` / **101** `TkLogs`). `native/zigui/src/tick.zig` renders EVERY fragment of that surface
through the existing per-fragment renderers, hashes each result (Wyhash-64: FNV-1a costs one byte per round, ~50 us on the 51 kB log tail) and drops the ones
whose hash matches the supplied `prev`. What comes back is a packed **RZF1** list of the changed
fragments only:

```
"RZF1"  4 B   magic
count   u16   changed fragments (0 = header only, 6 bytes: nothing to patch)
entries count x { id_len u16, id, hash u64, html_len u32, html }
```

`internal/zigui` decodes it (`decodeFrags`, bounds-checked; a reply that does not walk to exactly
its end is refused WHOLE, never applied in part), `tick_sched.go` turns each entry into the same
`window.__patch('id',…)` call `tickPatch` produced, and `flushTick` batches the lot into one Eval.
Unchanged fragment HTML never crosses the ABI, is never `jsQuote`d and never enters the eval queue.

### Design decision: hash-return, NOT a Zig-side cache

The alternative was a per-UI cache inside the lib (`rz_ui_tick_*(handle, …)`), which would have let
Zig keep the previous HTML and answer "unchanged" without Go sending anything. Rejected:

1. **Statelessness is the current ABI's whole safety story.** Every existing export is a pure
   `state → HTML` function; that is why one lib serves the visible window, N headless remote-library
   mirrors (`remoteui_host.go` builds a `*UI` per peer session) and the test binary at once, with no
   instance registry, no lifetime rules and no cross-talk. A cache keyed by an opaque handle adds a
   second lifetime to get wrong across a cgo boundary — for a *dedup hint*.
2. **The cache has to be droppable from the Go side anyway.** `patchMain` replaces the DOM and
   `enqueueEval`'s overflow policy drops a queued patch; both must invalidate the dedup state. With
   the hashes in Go that is `u.fragH = nil` under the mutex we already hold. With a Zig-side cache
   it is another export, called from a path that must not fail.
3. **It buys almost nothing.** The saving would be the prev hashes on the wire: 8 bytes + the id per
   fragment — measured at 317 B on the Live surface (2 857 B document → 3 174 B in steady state).
   The renders still happen either way: you cannot hash HTML you have not produced.

So the hashes travel in the document and Go owns the map (`u.fragH` + `u.fragGen`, both under
`fragMu`, beside the legacy `u.frags`). This also makes the *dedup decision auditable from the Go
side*: the parity test can replay it.

**Race:** the prevs are snapshotted with `u.fragGen`; `commitFrags` refuses if the generation moved
while the batch was in flight (a `patchMain` landed mid-tick). The batch is then discarded and the
LEGACY path runs for that tick — it re-renders and re-pushes everything, which is exactly what a
replaced DOM needs. A suppressed fragment can never be withheld from a fresh DOM.

### Parity contract

`tickPatch`'s semantics are reproduced, not approximated: same bytes → suppressed; an id with no
cached hash → always emitted; `patchMain` drops the cache → everything resent; the eval queue's
coalescing key stays the fragment id. The gate (`tick_sched_test.go`) drives the scheduler and
`liveTickLegacy` from ONE state over a scripted mutation sequence and requires the **identical
ordered set of `__patch` calls**, identical enqueued ids and an identical drained eval batch. Since
a `__patch` call embeds `jsQuote(html)`, that equality is also a per-fragment Zig-vs-Go byte-parity
assertion. Proven non-vacuous by execution: swapping two `tickPatch` lines in `liveTickLegacy` fails
the gate.

`liveTickLegacy` therefore stays forever — it is the stub-build path, the declined-batch path AND
the parity reference. Fragment order + presence conditions are a contract between it and
`tick.zig runLive`: change one, change both, or the gate fires.

### Deliberate behaviour change (one, gated)

`#log-view` had NO byte dedup: the seq gate skipped the tick only when the ring had not advanced, so
any new log line re-swapped the whole ~50 kB tail even when the FILTERED view was byte-identical
(level=error + a search box: the common case for a user watching one thing). The scheduler
suppresses that swap. Text selection survives — the same motivation the seq gate was added for. The
gate asserts the new eval sequence equals the legacy sequence with consecutive byte-identical
repeats removed, so the change is exactly "tickPatch dedup, now on this surface too".

### Notes for whoever extends this

- **Per-tick state building got cheaper, not just the ABI crossings.** `liveTickState()` fills only
  what the tick patches; the section titles and the five `tipTopic` tooltip cards that `liveState()`
  builds for the full view are left empty (the tick patches fragment interiors, never section
  headers). Building five tooltip cards a second to throw them away was pure waste.
- **TEXT fragments are part of the surface.** `#live-tc` / `#live-rec-state` carry escaped plain
  text, not a renderer's output. They are `Batch.text()` entries (`html.esc`, byte-identical to Go
  `htmlEscape`) — do not route them through a renderer, and do not derive `#live-tc` from
  `transport.tc`: the tick patches it even when no timecode service is wired (where `transport.tc`
  is empty), so the raw text rides in `TkLive.tc`.
- **`kUint` has its first user.** Rule 6 (Go formats every number) is about RENDERED numbers; a
  dedup hash is not rendered. Sending it as a 16-char hex string per fragment per tick would be
  waste, so `TkPrev.hash` is a varint.
- **The tick envelope rides wave B-2's messages (composed on merge).** The pilot originally carried
  its own `Tk*` mirrors of the live states because the two branches could not see each other's schema
  rows. Wave B-2 defined the same structs as `LiveState` & co, so the mirrors were DELETED: `TkLive`
  now references `LiveState`, `TkLogs` references `LogsLines`, and only the ENVELOPE (`TkPrev`,
  `TkLive`, `TkLogs`; root ids 100/101) is B3's. `tick.zig` names `live.*`/`logs.*` directly instead
  of re-exporting them. Consequence to keep in mind: the tick documents now carry everything those
  states carry, tip2's four structured section tooltips included — `TestTickSchedLiveCarriesTooltips`
  asserts they are really on the wire (the document must GROW when they are set; a silently dropped
  field encodes to identical bytes), because the live fixtures leave them nil and every other gate
  would stay green. Proven by execution: deleting fields 31-34 from the generated `liveState` encoder
  fails it with "tooltip did not reach the tick document: 2857 B with, 2857 B without".
- **The legacy fallback is itself a v2 path now.** Post-B-2, `liveTickLegacy`'s per-fragment renders
  each build their OWN RZW1 document (`RenderLiveFragV2`) — ten documents + ten crossings per tick.
  That is the baseline B3's numbers are measured against; `liveFrag` gained a wire-encoder parameter
  in that wave, which is the kind of signature change a merge with ZERO textual conflicts still
  breaks (it did).
- **Wire hazard spotted while mirroring `live.Link`:** its Zig default is `fill = "0.00%"`, but the
  Go zero value is `""`. On the wire a zero value is an ABSENT tag, so `Fill: ""` decodes to
  `"0.00%"` — harmless today because `renderLink` returns before touching `fill` when
  `available=false`, and Go always formats a non-empty fill when it is true. Any future use of
  `fill` in the unavailable branch makes v1 != v2. A Zig struct default that is not the Go zero
  value is a wire trap; prefer zero-valued defaults in state structs.
- **Adding a surface:** resolve its fragments into one state struct → schema rows (root id from the
  100-149 block) → regenerate → a `runX` in `tick.zig` listing the fragments in patch order → an
  export + binding + stub → in the tick, `if !u.tickXSched(...) { legacy }` → extend the parity gate
  and the fuzz base set. Do NOT let a surface's ids overlap another's: the dedup map is global per
  `*UI`.

### Numbers

Measured tables + method live in `.devnotes/PHASEB_BASELINE.md` "Phase B3 - fragment scheduler",
re-measured after the wave B-2 composition. Headline: the **Live tick** goes 29.0 -> 20.5 us of
dispatch (**-29%**), 16.8 us in steady state (**-42%**), 47.3 -> 34.5 us including the per-fragment
`jsQuote` (**-27%**), with allocations 196 -> 34 -> 9; ten `WireWriter`s become one (9.0 -> 7.1 us).
`sched_all` now MATCHES pure Go (21.0 us) and `sched_same` beats it by 20% - the first surface where
the Zig path is not a loss, which took B-2 killing the parse and B3 killing the per-fragment
crossings together. **`#log-view`** is a wash when the tail changed (169 vs 150 us against B-1's
single-fragment `_v2` export: the batch copies 61 kB the direct export hands straight out) and -46%
plus the entire downstream (86 kB `jsQuote` + eval + cross-process ExecuteScript) when it did not.
Two honest caveats recorded there: **pure Go is still the cheapest renderer for the log tail**, and
**batching only pays where there are MANY fragments** - a single big fragment wants dedup, not a
batch. Quote the post-composition figures: against per-fragment JSON the same change measured
-43%/-45%, and two optimisations on one tax do not add up.

## Phase B — B4b: Library retained state (the TTL/signature memos are gone)

B4's contract: each surface removes a retained-state workaround whose only reason was the Go
runtime. The Library tab carried four, all in `render_library.go`, all now replaced by
`internal/webui/library_deriv.go`. **The DOM does not change; the INPUTS and the timing do.**

### What was there and why it was a workaround

| removed | what the LANE paid per render | why it existed |
|---|---|---|
| `collViewSig` + `collViewIdx` | `fmt.Fprintf`+`sort.Strings`+FNV over three filter maps and the playlist-id set, then the ~23k-track filter+sort INLINE on a miss | the render lane could not afford the scan, and a hash was the cheapest available "did anything move" |
| `plRowsVer` + `plRows` | `PlaylistVersion()` compare, `ListPlaylists()` (per-row `COUNT` subquery) inline on a miss | the query ran 2-3x per render |
| `smartCounts*` | FNV over EVERY smart rule set, per render | each count is a ~23k scan + a compat read |
| `onDiskCk` + `collOnDiskFresh` (5s) | nothing on the lane, but a blind re-`stat` of every rendered row every 5s | `os.Stat` per row froze the render |
| `browseFresh` (2s) | nothing on the lane, but a blind `os.ReadDir` + per-entry `Info` every 2s | a network share wedged the action goroutine |
| `LibraryVersion()` (libdb) | **a `SELECT MAX(seq)` per call, twice per render**, on a `SetMaxOpenConns(1)` handle - it queues behind any writer | it was cheap "enough" when nothing else was |

### What replaces them

- **A comparable key, not a hash.** `libDerivKey{lib,pl,compat,loadGen,ctl}` is compared by struct
  equality: no hashing, no allocation, no map-key sort. Each derivation fills only the fields it
  reads, so a keystroke does not invalidate the playlist rows and a playlist write does not
  re-filter the collection.
- **`ctlVer` + copy-on-write controls.** The collection controls (search/sort/facets/`dropsIdx`) are
  replaced, never mutated in place, and every mutation stamps `ctlTouch()`. That is what lets
  `collViewOf` read them from another goroutine at all - and it fixed a REAL pre-existing race:
  `libWatchApply` wrote `s.tracks[i] = tr` in place while `libSmartCounts` read the same slice
  off-thread, against the invariant its own comment claimed ("replaced wholesale => safe
  off-thread"). It now clones.
- **Compute on the mutation path, off the lane.** `libSetColl` mutates, stamps, then recomputes on
  `u.bg` and patches ONCE with the fresh view. There is no stale intermediate frame - the old code
  recomputed inline during the next render, the new code recomputes before it.
- **Cold fills stay inline** (`libDerive(..., coldAsync=false)`): the old path blocked there too, and
  rendering a placeholder instead would be a DOM change. `smartCounts` is the exception - its cold
  state already rendered the ellipsis badge, so it stays async and keeps that markup.
- **Filesystem freshness is dir-mtime gated, not TTL-gated.** Both sweeps stat the DISTINCT PARENT
  DIRS first and only do the expensive half (N file stats / a full `ReadDir`) when a dir actually
  moved or a row is unknown. A create/delete/rename inside a dir moves its mtime, which is exactly
  what the TTLs were sampling for. Our own file ops call `libFsChanged` for instant re-verification.
- **`LibraryVersion()` is an in-memory epoch** (`libdb.chgVer`): seeded once from `MAX(seq)` (so
  epochs stay comparable across restarts, which the persisted `store` mtime slots need) and advanced
  by `appendTx`. Same single-owner invariant `plVer`/`compatVer`/`tracksVer` already rest on. A
  rolled-back append leaves it one step high: harmless, it is only compared for inequality and stays
  monotonic.

### Ordering hazard the hash was hiding

Smart counts are computed FROM the playlist rows, so stamping them with an epoch the ROWS are not
current for settles the OLD rules' counts under the NEW key - and they stick. The deleted code
hashed the rule TEXT, which accidentally covered this. `libSmartCounts` now refuses to compute until
`plD` is current for the same `PlaylistVersion`; `plD`'s settle re-patches, so it converges.
Found by execution (`TestLibSmartCountsFreshAfterRulesEdit` failed with `30 not > 30`).

### No Zig-side state

Nothing crosses the ABI differently and no state struct changed shape, so the wire schema is
untouched (drift audit: `0 drifted fields`). Per B3's "hash-return, NOT a Zig-side cache" reasoning
the derivations stay Go-side and version-keyed: they key on libdb epochs and Go-owned control state
the lib cannot see, and a stateful export would add a second lifetime across cgo for a cache the Go
side has to be able to drop anyway.

### Gates (`library_deriv_test.go`, untagged - runs in both builds)

1. `TestLibCollViewRetainedMatchesFreshAfterEveryControlAction` - the missed-invalidation gate.
   Every collection-control act is driven through the REAL dispatcher; after each, the retained view
   must equal a from-scratch `collView()`. Non-vacuous by execution: deleting the `ctlTouch` in
   `libSearchDebounced` fails it (`retained 10 rows != fresh 400`). Each step also asserts the view
   is non-empty, so a fixture that filters everything away cannot make a step prove nothing.
2. `TestLibCollViewKeyIsPreciseNotBlanket` - a selection click must NOT recompute (that is what the
   memo was for); a sort must.
3. `TestLibCollViewComputesOffTheLane` / `TestLibPlaylistsRefreshesOffTheLane` - with the runner seam
   queued rather than spawned, the lane must return the OLD value and merely enqueue. This is the
   actWorker constraint as a test.
4. `TestOnDiskFreshnessIsChangeDrivenNotTTL` / `TestBrowseFreshnessIsChangeDrivenNotTTL` - the
   staleness gate, both directions: an unchanged dir must do ZERO file stats / ZERO `ReadDir`
   (counters `onDiskSweeps`/`browseReads`), and a moved dir must be reflected on the next render with
   no wall-clock wait. Non-vacuous: forcing `moved := true` (the old TTL behaviour) fails the first
   half (`1 -> 3 sweeps`, `2 -> 4 reads`).
5. `TestLibSmartCountsFreshAfterRulesEdit`, `TestLibDerivFreshAfterLibraryVersionBump` - a rules edit
   / an external library write is reflected immediately, not after a TTL.
6. `TestLibBodyFromRetainedStateIsByteIdentical` - the DOM-identity gate over a scripted 12-step
   mutation sequence: `#lib-body` rendered off retained state must be byte-identical to the same body
   rendered with every derivation dropped (cold => fresh inline compute). Non-vacuous: making
   `ctlTouch` a no-op fails it (`first diff at 3300`).
7. libdb: `TestLibraryVersionIsInMemoryEpoch` proves the read no longer touches SQL (it still answers
   after `d.db.Close()`, where the query path returned 0) and `TestLibraryVersionSeedsFromDisk` pins
   the cross-restart seed.

The 21x3 library golden suites + the fixers/libviews/libremote suites and the three-way wire gates
are untouched and pass unchanged - they exercise the RENDERERS, which this change does not reach.
That is also why gate 6 exists: a state-builder change needs a state-builder gate.

### Numbers

`.devnotes/PHASEB_BASELINE.md` "Phase B4b". Headline: steady-state handler-lane work for a
collection render **30.6 us -> 126 ns** (43 -> 4 allocs), worst case (a control moved) **47.9 ms ->
73 ns** on the lane because the scan moved to `u.bg`, and the filesystem sweep the 5s TTL re-ran
blind **3.53 ms -> 56 us** (dir stats only).

### If you extend this

- Adding a collView input means adding it to `libCollInputs` AND making its writer copy-on-write +
  `ctlTouch`. Gate 1 catches a forgotten stamp; gate 6 catches the DOM consequence.
- A derivation computed FROM another must check the other's key is current (see the ordering hazard).
- Do NOT put a `time.Since` back in: `s.derivRun` exists so freshness can be tested
  deterministically, and every gate here would still pass with a TTL bolted on top - the sweep
  counters are what forbid it.
- `libRebuildPlFilter` is off the lane from `libSetColl`, but the one-shot cue-prep flow
  (`library_cueedit.go` `ceOpenSet`) still calls it inline. Not the render path; left alone.
