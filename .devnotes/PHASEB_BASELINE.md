# Phase-B render baseline (measured)

Reference numbers every later phase-B increment cites. Before this file, `internal/webui` had
ZERO benchmarks - every claim about the Zig bridge was unfalsifiable.

Benchmarks: `internal/webui/render_bench_zig_test.go` (tagged, per-tab Go-vs-Zig) +
`internal/webui/render_bench_test.go` (untagged half). Live counters: `zigui.PerfCounts()` +
`ctl perf` section `[zigui]`.

## Machine + build

| | |
|---|---|
| CPU | AMD Ryzen 9 5950X 16C/32T, windows/amd64 (GOMAXPROCS 32) |
| Go | go1.26.5 · Zig 0.16.0 (`x86_64-windows-gnu`) |
| Commit | `fd8b271` on `feature/zig-phaseb-bench` (base: `eb82d93` development) |
| Date | 2026-07-25 |
| Lib | `native/zigui/zig-out/lib/libraveui.a`, 12.4 MB, `bash scripts/build-zig.sh` |

## Reproduce

```sh
export PATH="$LOCALAPPDATA/Microsoft/WinGet/Links:$PATH"   # zig 0.16
bash scripts/build-zig.sh
# tagged: Go vs Zig vs bridge, 10 tabs
GOWORK=off go test -count=2 -tags "zigdsp zigui zigvr" ./internal/webui -run '^$' -bench . -benchtime 1s
# untagged (stub build): Go path only
GOWORK=off go test -count=2 ./internal/webui -run '^$' -bench . -benchtime 1s
```

## Method (read before quoting a number)

- **min of 6 samples** (three full-suite runs × `-count=2`). This box runs a 4-agent fleet, so
  runs are contaminated by parallel `zig build` / `go build`: worst samples were 2-4× the best
  (`RenderZig/appgroups` 4.4 µs best vs 12.6 µs under load). Contention only ever ADDS time, so
  the minimum is the least-wrong estimator. Per-tab spreads are in "Ranges" below - do NOT read
  a <20% delta as a regression on this machine.
- One representative **`populated` fixture per tab** (`tracklist` for publish, `singleEdit` for
  player). The golden suites still own the branchy states; these are cost samples, not coverage.
- Every case is **parity-gated before timing** (`zigBenchState`): Zig must return ok=true AND
  byte-equal Go, else the benchmark fails. A bench can never quietly measure a fallback.
- What each column contains:
  - **Go** = pure Go renderer, state already built. No bridge.
  - **Zig** = `rz_ui_render_<tab>` = **std.json parse + render + result copy + free**. NOT a
    renderer-only number - see finding 2.
  - **bridge** = `stateJSON()` + Zig = what a render costs the app today.
  - **marshal** = `stateJSON()` (encoding/json) alone.
- `state B` / `html B` are exact sizes (custom bench metrics `state_B` / `html_B`).

## Tagged: Go vs Zig vs bridge (µs/op, min of 6)

| tab | state B | html B | Go | Zig (parse+render) | bridge | marshal | marshal % of bridge | bridge / Go |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| appgroups | 529 | 1 433 | 4.5 | 4.4 | 5.4 | 1.0 | 18% | 1.22× |
| motion | 1 150 | 1 800 | 4.2 | 6.4 | 8.7 | 1.8 | 21% | 2.10× |
| logs | 1 388 | 2 865 | 7.4 | 9.7 | 12.4 | 2.7 | 22% | 1.68× |
| automations | 2 018 | 5 697 | 18.4 | 14.1 | 17.7 | 3.6 | 20% | 0.96× |
| publish | 2 991 | 5 181 | 14.6 | 18.9 | 24.3 | 5.3 | 22% | 1.66× |
| live | 4 553 | 7 152 | 18.6 | 24.5 | 34.0 | 7.4 | 22% | 1.83× |
| peers | 6 904 | 10 593 | 33.8 | 42.1 | 58.7 | 12.9 | 22% | 1.74× |
| settings | 7 351 | 10 516 | 30.2 | 44.4 | 61.4 | 15.2 | 25% | 2.03× |
| library | 11 215 | 15 422 | 47.5 | 82.5 | 111.3 | 23.3 | 21% | 2.34× |
| player | 28 999 | 27 641 | 39.8 | 74.3 | 116.3 | 37.7 | 32% | 2.92× |

Ranges (min-max over the 6 samples, µs): appgroups Go 4.5-5.0 / Zig 4.4-12.6 / bridge 5.4-9.2 ·
logs 7.4-8.5 / 9.7-25.5 / 12.4-18.3 · motion 4.2-5.3 / 6.4-22.5 / 8.7-10.3 · automations
18.4-25.9 / 14.1-44.5 / 17.7-21.8 · publish 14.6-19.6 / 18.9-113.0 / 24.3-42.3 · live 18.6-27.3 /
24.5-77.7 / 34.0-47.6 · peers 33.8-47.9 / 42.1-166.4 / 58.7-70.5 · settings 30.2-45.4 /
44.4-162.7 / 61.4-72.3 · library 47.5-116.2 / 82.5-197.1 / 111.3-123.5 · player 39.8-196.7 /
74.3-220.9 / 116.3-148.8.

**bridge ≈ marshal + Zig** within 2-5% on every tab (library 105.7 vs 111.3, player 112.0 vs
116.3), so the two halves are additive - cgo call overhead is noise at these sizes.

## Untagged (stub build): Go path only

Fixtures for the 10 tabs live in `//go:build zigui` golden files, so the untagged half benches
what IS reachable without them: the settings tab off `newSettingsTestUI()` (zero config, one
sub-tab - a DIFFERENT, smaller state than the tagged settings row) and the four dialog families
whose fixtures dialog-sweep-A deliberately kept untagged.

| bench | state B | html B | untagged µs | same code, tagged build µs |
|---|--:|--:|--:|--:|
| SettingsStateBuild (impure half) | - | - | 172.3 | 167.8 |
| SettingsRenderGo | - | 9 262 | 30.2 | 26.5 |
| SettingsMarshal | 6 361 | - | 13.1 | 13.2 |
| Dialog txtExport render | - | 2 151 | 4.1 | 4.3 |
| Dialog fixTimes render | - | 990 | 1.6 | 1.8 |
| Dialog patMgr render | - | 933 | 1.9 | 2.0 |
| Dialog presetEditor render | - | 5 965 | 11.6 | 11.1 |
| Dialog marshals (txt/fix/pat/preset) | 617/467/196/3 740 | - | 1.08/0.96/0.48/6.87 | 1.04/0.91/0.43/6.38 |

Identical code in both builds lands within ±14% (mostly ±5%): **linking libraveui does not
measurably change the Go path**, so tagged-build Go columns are valid untagged references.

## Cost model (least-squares over the table)

| slope | ns per byte |
|---|--:|
| Zig `rz_ui_render_*` (parse+render) vs **state** bytes (player excluded) | **6.9** |
| Go `encoding/json` marshal vs **state** bytes | **1.33** |
| Go renderer vs **html** bytes | **1.63** |

Player is excluded from the Zig fit because its 29 kB state is mostly ONE raw SVG string
(`mpWaveSVG` stays Go by design): 29 kB of structure-free bytes parse ~2.3× cheaper than the
fit predicts. Parse cost tracks STRUCTURE, not size.

## Findings (what phase B must act on)

1. **The phase-A bridge is a net LOSS at today's state sizes.** `bridge / Go` is 1.2-2.9× on
   every tab except automations (0.96×). Rendering the Library tab through Zig costs 111 µs
   where pure Go costs 48 µs. Phase A bought correctness + a migration path, not speed - the
   speed argument only lands once the round trip goes.
2. **The expensive half is the ZIG side, not the Go marshal.** Marshal is 18-25% of bridge time
   (32% for player); the remaining 75-80% is `Zig(parse+render)`, whose per-state-byte slope
   (6.9 ns/B) is **5× Go's marshal slope** (1.33 ns/B). A binary wire that only removes
   `json.Marshal` recovers at most a quarter of the tax - **it must also remove the Zig-side
   `std.json` parse** (wave B-2's TLV decoder does; that is the whole point).
3. **Zig renderer-only time is NOT measurable today.** Every export parses. Recommend one
   parse-free entry point (or agent 2's `_v2` TLV export) so `Go render` vs `Zig render` can be
   compared honestly; until then treat the Zig column as parse-dominated. Rough bound from the
   slopes: at 11 kB of state, parse plausibly accounts for the majority of library's 82.5 µs.
4. **On settings, neither renderer is the bottleneck.** The state BUILD is 168-172 µs vs 26-30 µs
   to render it (~5.6×). That builder is the flagged Go-runtime workaround (`settingsProbes` +
   `maybeRefreshProbes`, ZIG_UI_GUIDE "Settings-port notes") plus a full card render for search
   matching. Biggest single win on that tab, and it is renderer-independent.
5. **Tick budgets are safe either way.** The ~1 Hz `livePush` funnel and the 30 fps rAF surfaces
   never rebuild whole tabs; the worst full-tab bridge render (player, 116 µs) is 0.35% of one
   16.7 ms frame. Nothing here is a live regression - it is headroom being spent for nothing.
6. **Marshal % is remarkably flat (~21%)** across two orders of magnitude of state size, which is
   why the Go-side-only optimisations (buffer reuse, `omitempty`) can be ignored: the shape of
   the tax is structural, not allocation noise.

## Live counters (the same tax, in the running app)

`ctl perf` → section `[zigui]` (registered from `webui.New`, additive):

```
renders 12345 · zig 1.04s total · avg 84µs · state 42.1 MB (avg 3.5 kB)
marshals 12348 · json 383ms total · avg 31µs · 43.0 MB (avg 3.5 kB) · 26.9% of bridge time
fallbacks none
```

- `zigui.PerfCounts()` (untagged `internal/zigui/perf.go`, atomics, both builds) - `NoteRender`
  from the cgo render funnel, `NoteMarshal` from webui `stateJSON`. Zero in stub builds.
- Overhead: two `time.Now()` per render on each side, against renders costing µs-ms.
- Read it WITH `FallbackCounts()` (same section): a fallback on a whole-view renderer means the
  Zig number is measuring fewer renders than you think.

## Wave B-2: v1 (JSON) vs v2 (RZW1 binary) per view

Same box + method as above (**min of 6** = three runs × `-count=2`, `-benchtime 500ms`), commit
`feature/zig-phaseb-wire2`. Each number is the WHOLE dispatch a tick pays: state serialization +
the Zig render (parse + render + copy). Fixture = the golden `populated` state; fragment rows use
that fixture's sub-state.

```sh
GOWORK=off go test -count=2 -tags "zigdsp zigui zigvr" ./internal/webui -run '^$' -bench WireBench -benchtime 500ms
```

| view | v1 json ns/op | v2 wire ns/op | Δ | doc B vs json B (golden set) |
|---|--:|--:|--:|---|
| live (full cockpit) | 33 786 | 16 190 | **-52%** | 24 789 / 67 456 = 36.7% |
| live `#live-transport` frag | 3 790 | 2 896 | -24% | (in the set above) |
| live `#live-perf2` frag | 3 624 | 1 885 | **-48%** | (in the set above) |
| motion (full tab) | 21 674 | 11 472 | **-47%** | 15 091 / 24 586 = 61.4% |
| motion `#mo-body` frag | 24 243 | 12 774 | **-47%** | (same document) |
| publish (full tab) | 24 701 | 12 507 | **-49%** | 20 223 / 48 766 = 41.5% |
| publish `#pub-hero` frag | 5 938 | 4 400 | -26% | (in the set above) |
| settings (full tab, `libmedia`) | 64 953 | 32 651 | **-50%** | 139 005 / 203 734 = 68.2% |
| settings `#stset-<id>` frag | 901 | 913 | ~0% | (~60 B of state; see the allocation note) |
| library (full tab) | 124 435 | 38 668 | **-69%** | 72 850 / 209 353 = 34.8% |
| library `#lib-body` frag | 121 232 | 39 131 | **-68%** | (in the set above) |
| library cue-census cell | 1 183 | 838 | **-29%** | (~90 B of state) |
| player (full, `singleEdit`) | 123 343 | 41 513 | **-66%** | 1 610 114 / 2 061 725 = 78.1% |
| player `#mp-tp` transport frag | 9 448 | 3 450 | **-63%** | (in the set above) |
| player `#mp-export` frag | 68 609 | 20 080 | **-71%** | (in the set above) |
| automations (full tab) | 17 857 | 12 399 | **-31%** | 8 156 / 19 655 = 41.5% |
| automations `#auto-body` frag | 17 832 | 12 945 | **-27%** | (in the set above) |
| peers (full tab) | 56 457 | 24 735 | **-56%** | 23 865 / 90 741 = 26.3% |
| peers `#peers-body` frag | 55 429 | 26 266 | **-53%** | (same document set) |

**Encoder allocation: the flat prealloc was a real regression, now fixed.** With
`NewWireWriter` preallocating a flat 2 × 1 KiB + a 64-entry intern map, the SMALLEST fragment
(`#stset-<id>`, ~60 B of state) cost **1 569 ns vs the JSON path's 932** - v2 was 68% slower than
what it replaces, because two 1 KiB buffers and a 2.5 kB map dwarf the work. A flat 256 B instead
cost the Live cockpit 12 extra allocations (16.2 → 22.6 µs). Both buffers and the intern map are
now sized from **what the previous document of the same message needed** (`wireSizeHints`, one
atomic per root id, +25% headroom; capacity only - `TestWireSizeHintIsCapacityOnly` pins that the
bytes are unchanged). Result:

| bench | flat 1 KiB | adaptive | v1 json |
|---|--:|--:|--:|
| `#stset-<id>` status frag | 1 569 ns / 5 792 B / 9 allocs | **913 ns / 1 568 B / 9** | 901 ns / 232 B / 4 |
| live full cockpit | 16 190 ns / 20 704 B / 11 | **16 109 ns / 24 032 B / 9** | 33 544 ns / 14 422 B / 4 |
| `#log-view` 400-line tail | 158 297 ns (pilot) / 22 allocs | **144 525 ns / 109 152 B / 9** | 379 877 ns / 126 022 B / 4 |
| logs-tail encode only | 44 020 ns (pilot) / 17 allocs | **41 668 ns / 7 allocs** | 98 356 ns (marshal) |

v2 still allocates more BYTES than `json.Marshal` on tiny fragments (1 568 vs 232): two buffers
plus the map floor. Time is at parity there and 2-2.6× better everywhere else, so the remaining
gap is not worth a pool. Benchmark numbers are mildly order-dependent now (the first document of
a message pays the cold hint) - min-of-N over full-suite runs absorbs it.

**Player's 29 kB raw SVG, measured as promised.** The document ratio on player is the WORST of
the fan-out (78.1% vs library's 34.8%): its state is dominated by one huge `mpWaveSVG` string, so
the arena has nothing to intern and the only saving is JSON's escaping of that string. The TIME
delta is still -66%, because the win was never the document size - it is removing the Zig-side
`std.json` parse (B0 finding 2). Player also allocates the most (165 kB/op): the SVG is copied
into the arena, then the finished document is copied again by `Finish`. A zero-copy "big string"
wiretype (offset into the caller's buffer instead of the arena) would fix that class; it is not
needed for correctness and nothing in the tick path renders the full player.

## Gaps / caveats

- Fixtures for the 10 tagged tabs were NOT moved out of their `//go:build zigui` golden files -
  three sibling wave-B1 branches are editing those same files, and a move would collide. That is
  the only reason the untagged table is a subset.
- Fragment renderers: the two B3 pilot surfaces (Live tick, `#log-view`) ARE benched now - see
  "Phase B3 - fragment scheduler" below. Still unbenched: the `_body` fragments of the un-piloted
  tabs and the nine player patch targets.
- `-benchtime 1s` with default N; no `benchstat` (no new deps). If a later increment needs
  statistical confidence, run `-count=10` on a quiet box and compare mins.

## Phase B3 — fragment scheduler (tick surfaces)

The B0 gap ("fragment renderers are not benched yet - the ~1 Hz tick patches them far more often
than a full tab is rendered") is closed for the two pilot surfaces.
Bench: `internal/webui/tick_sched_bench_test.go` (tagged). Same box/method as above: **min of 6**
(three runs x `-count=2`, `-benchtime 1s`), parity-gated before timing (`tickBenchParity`: the
scheduler's ids, order AND bytes must match the Go renderers, so a bench can never measure a
fallback).

**Re-measured after the wave B-2 composition** — the numbers below compare B3 against the
**binary** per-fragment path (wave B-2 gave every live fragment its own `_v2` export), not against
the JSON one. That is the honest baseline and it is a much harder one: the pre-composition figures
(-43% / -45%) were measured against per-fragment JSON, which no longer exists.

Rows:

| row | what it does |
|---|---|
| `legacy_zig` | pre-B3: per fragment one RZW1 document + one cgo call (`RenderLiveFragV2`) |
| `legacy_go` | the same per-fragment loop with the Go renderers (stub build / fallback cost) |
| `sched_all` | B3: one encode, ONE cgo call, every fragment comes back (cold cache / all changed) |
| `sched_same` | B3 steady state: one encode, one cgo call, NOTHING comes back |
| `*_quoted` | the same plus `jsQuote` per patched fragment — what the tick pays before the Eval |

### Live tab tick — 12 fragments, 5 087 B of HTML, doc 2 857 B (3 174 B with prev hashes)

| row | µs/op (min of 6) | B/op | allocs/op |
|---|--:|--:|--:|
| legacy_zig (10 docs + 10 cgo calls) | 29.0 | 41 104 | 196 |
| legacy_go | 21.0 | 35 593 | 166 |
| sched_all | **20.5** | 28 032 | 34 |
| sched_same | **16.8** | 15 976 | **9** |
| legacy_zig_quoted | 47.3 | 57 822 | 231 |
| **sched_all_quoted** | **34.5** | 44 629 | 69 |
| encode_wire (ONE doc) | 7.1 | 15 832 | 7 |
| encode_wire_perfrag (TEN docs) | 9.0 | 17 696 | 70 |

**-29% on the dispatch (29.0 → 20.5 µs), -42% in steady state (16.8 µs), -27% on the full quoted
tick (47.3 → 34.5 µs), allocations 196 → 34 → 9.** Twelve cgo crossings and twelve TLV parses
become one; ten `WireWriter`s become one (9.0 → 7.1 µs, 70 → 7 allocs).

**First time the Zig path is not a loss on this surface:** `sched_all` (20.5 µs) matches pure Go
(21.0 µs) and `sched_same` beats it by 20%. B0 finding 1 ("the bridge is a net loss") is finally
neutralised here — by B-2 killing the parse and B3 killing the per-fragment crossings together.

### `#log-view` tick — 400-line tail, 61 400 B of HTML, doc 9 231 B vs 51 862 B of JSON

| row | µs/op (min of 6) | B/op | allocs/op |
|---|--:|--:|--:|
| legacy_zig_v1 (pre-B1 JSON) | 375.4 | 126 599 | 4 |
| legacy_zig_v2 (B-1 binary, the shipped path) | 149.9 | 109 152 | 9 |
| legacy_go | **130.0** | 324 606 | 1 220 |
| sched_all | 169.3 | 174 744 | 12 |
| **sched_same** | **140.4** | 43 624 | 9 |
| legacy_zig_v2_quoted | 260.9 | 319 694 | 13 |
| legacy_go_quoted | 240.3 | 565 635 | 1 226 |
| sched_all_quoted | 284.1 | 394 188 | 17 |

Read this one carefully — it is NOT a straight win:

- **Tail changed:** the scheduler is 13% slower than the single-fragment `_v2` export (169 vs
  150 µs), 9% slower quoted (284 vs 261 µs). The extra is the 61 kB copy into the reply buffer that
  the direct export avoids (it hands its render buffer straight out) plus the hash.
- **Tail unchanged:** 140.4 µs and NOTHING leaves the lib — no 61 kB copy across the ABI, no
  86 202 B `jsQuote`, no eval-queue entry, and no cross-process `ExecuteScript` with an 86 kB script
  (the last of which dwarfs every number in this table and is not measured here). Against the
  legacy 261 µs of Go-side work for a swap that changes nothing, that is **-46% CPU plus the whole
  downstream**. With a filter active (`level=error` + a search box) this is the common case, which
  is why the pilot exists.
- **Hash choice matters at this size.** FNV-1a-64 consumes one byte per round: ~50 µs on the 51 kB
  tail, more than it saves. Wyhash reads 64 bits at a time (~7 µs). Recorded because the first
  implementation used FNV and the numbers said so.

### Findings

1. **B3 is the first phase-B change that makes the Zig tick path cheaper than pure Go** (Live tick:
   20.5 / 16.8 µs vs 21.0 µs), and it gets there by removing CROSSINGS + Go-side churn, not by
   rendering faster.
2. **The composition halved the headline.** Against per-fragment JSON B3 measured -43%/-45%;
   against wave B-2's per-fragment binary path it is -29%/-27%. Two independent optimisations on the
   same tax do not add up — quote the post-composition figure.
3. **Pure Go is still the cheapest renderer for the log tail** (130.0 vs 169.3 µs) — B0 finding 1
   survives on that surface. What Zig buys there is the allocation profile (1 220 → 12 allocs) and,
   with B3, the suppression.
4. **A single big fragment is the wrong shape for a batching scheduler.** For `#log-view` the batch
   adds a copy the direct export doesn't need; the win comes purely from dedup. A Go-side hash of
   the Go-rendered tail would deliver the same suppression at 130 µs — worth considering if the log
   tail stays Go-rendered. Batching pays where there are MANY fragments (the Live tick).
5. **One document beats ten** even before the ABI: 7.1 vs 9.0 µs and 7 vs 70 allocations, with
   wave B-2's per-message prealloc hints applying to the tick roots (ids 100/101) automatically.
6. **Steady state is where the tick lives.** `sched_same` (16.8 µs, 9 allocs, zero eval traffic) is
   the number to quote for a UI sitting on the Live tab.
