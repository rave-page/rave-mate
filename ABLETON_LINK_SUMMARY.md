# Ableton Link + phrase-sync + delay-compensation

rave-mate publishes the fused DJ master tempo + beat/phase onto an **Ableton Link** session
(quantum = phrase length), so Link-aware visuals (Resolume, Ableton Live, VDMX) follow the DJ
with no manual beat-matching. Adds phrase-aligned Resolume clip triggers and per-source delay
compensation. rave-mate is the Traktor→Link bridge Traktor itself can't be.

## Build gating (CI-safe)

The real Link backend is the official **abl_link C wrapper via cgo**, gated behind the
`abletonlink` build tag and isolated in the featurehost `abletonlink` child. The default build
(no tag) ships an inert `Stub` - the daemon compiles + runs without Link; the feature reports
unavailable. **The normal CI build never compiles Link**, so the pipeline stays green until the
mingw-static toolchain is proven against the SDK.

- `internal/abletonlink/abletonlink.go` - `Session` interface, `State`, `TimeSource` adapter
  (satisfies `mediasync.TimeSource`), `Stub`.
- `link_stub.go` (`!abletonlink || !cgo`) - `NewLink` returns `ErrUnavailable`.
- `link_cgo.go` (`abletonlink && cgo`) - real abl_link binding (capture/commit app session state,
  set tempo, force/request beat, is-playing, num-peers).

### Enabling the real backend (deferred - external dep)

The abl_link cgo build is **deferred behind the tag**. Blocker: the Ableton Link SDK is not
vendored (dual GPLv2+/commercial; keep it an external build-time dep). To enable:

1. Checkout `github.com/Ableton/link` (with the asio submodule) under `third_party/link`.
2. Compile the abl_link wrapper (`extensions/abl_link/src/abl_link.cpp`) + the header-only Link
   runtime into `third_party/link/lib/libabl_link.a` (g++ -std=c++17 + the include dirs in
   `link_cgo.go`; `ar rcs`).
3. `CGO_ENABLED=1 go build -tags abletonlink ./...` with the mingw static toolchain. Wire a CI
   job only once this builds.

**USER TO-DO (distribution-time, non-blocking):** request Ableton's free commercial Link license
(standard grant for Link-enabled apps). Does not block development.

## Architecture

- **Featurehost child** `feat_abletonlink.go` hosts the Session, samples state ~10 Hz → `state`
  events, applies the daemon's tempo/phase `bridge` frames when it owns the tempo.
- **Daemon proxy** `abletonlinkproxy.go` mirrors state, forwards control RPC, pushes the bridge,
  and implements `mediasync.TimeSource` via extrapolated Link musical time.
- **DJ→Link bridge** `internal/app/app_abletonlink.go` reads the session Merger master BPM/phase
  and drives Link tempo + phrase phase; role = config `tempoOwner` (auto/always/follow). Fires the
  Resolume phrase clip on each Link phrase boundary.

## Resolume (`internal/resolume/`)

Resolume 7 joins Link natively (follows tempo/phase). This client adds phrase-aligned clip
triggers + offset nudges on top: OSC send (tempo/resync/tempotap/clip-connect via `internal/osc`)
+ REST (`/api/v1`) tempo readback + clip triggers. Phrase clip (1-based layer/clip) re-triggers on
every Link phrase boundary.

## Delay compensation (`internal/delaycomp/`)

Aligns every source to the **slowest** (only-add-delay → all comps ≥0). `Latency` = RTT + render +
manual; `Plan()` computes per-source ms. `ProbeRTT` reuses `medialink.OffsetEstimator` (min-RTT
filter, one-way = min-RTT/2). Appliers: OBS audio (`SetInputAudioSyncOffset`) + video (idempotent
managed delay filter, `gpu_delay` ≤500ms / `async_delay_filter` above). Opt-in - no live
auto-apply. OBS filter primitives added in `internal/obs/media.go`
(`CreateSourceFilter`/`SetSourceFilterEnabled`/`GetSourceFilterList`) + forwarded through the
featurehost obs child.

## Config (`AbletonLinkFeature`, off by default)

`enabled`, `quantum` (8/16/32), `tempoOwner` (auto/always/follow), `startStopSync`, and
`resolume` (enabled, host, oscPort, restPort, phraseClipLayer/Clip). Per-source delay comp reuses
`mediasync.SourceConfig.StaticOffsetMs`.

## UI + ctl

- Settings: `ablelink` card (streaming section) - enable, quantum, tempo-owner, start/stop sync,
  Resolume host/ports + phrase clip, Resync button.
- Live: "Sync / Ableton Link" panel - phrase-phase bar, tempo/peers summary, Resync, per-source
  chase readout. Reports unavailable without the cgo build.
- ctl: `rave-mate ctl ablelink-status` (JSON state) · `ablelink-resync` (hard phrase realign).
- i18n: English keys under `settings.{body,card,toggle}.ablelink` + `live.ablelink`; other locales
  fall back to English.

## Status

P0–P5 shipped behind the stub (default build green in CI). Real Link cgo backend deferred behind
the `abletonlink` tag pending the SDK checkout + static lib + toolchain proof (above). UI not
visually verified this session - the user's live DJ session (running rave-mate/Traktor/OBS) must
not be restarted; drive `ctl ablelink-status` / screenshot the Live+Settings tabs to verify once
the session ends.
