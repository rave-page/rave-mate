# Native decode destination: hold-when-contended, don't recycle (2026-09-11 real-set RCA)

Companion to `SPOUT_INTEROP_VRAM_CHURN.md` (track-cycle churn, fixed) and
`RELAY_SAWTOOTH_AND_INTEROP_2026-09-07.md`. Those closed the SENDER churn; this closes the
DECODE-DESTINATION churn that still bit under VRAM saturation.

## Symptom (real set, DJ PC, build nightly-053fcea #309)

~1-3h into a live set Resolume throws the interop error and the relay drops to a slow CPU path.
`rave-mate-debug.log`:

```
21:08  native decode destination "…spout_scene_capture_sus" recycling
       (AUs arriving but nothing published (frozen destination)) … reopen failed: open timeout ×3
       → pinning this sender to the ffmpeg decode path
22:50 / 01:59  native decode session ended - route re-establishes on the ffmpeg decoder
```

Telemetry up to that point is healthy: `engine:mf-native-decode gpuPublish:true hwaccel:d3d11-dxva
3840x2160@60 0 drops 0 restarts`. The transfer is NOT slow - it breaks only at saturation.

## Mechanism

The card saturates (Resolume alone ~7.9/12 GB; `[gpumem]` free dipped to 169 MB). The decode
child (`native/zigenc/src/dec.zig`) allocates its whole GPU pipeline ONCE at `open()` and holds it
- steady state allocates nothing. Under load `publish()` cannot acquire the destination Spout
texture's keyed mutex within `acquire_ms=3ms` (a RECEIVER - Resolume - / DWM holds it) → returns
`.timeout`, `mtx_timeouts++`, `pub_n` stalls. The picture freezes on the last blitted frame but the
pipeline is fully alive.

`decCheck` (`procdec_windows.go`) read "AUs arriving, `pub_n` not advancing" as a dead destination
and `recycleDest` `close()`+reopen()ed the ENTIRE pipeline. On a full card the reopen can't
re-allocate (device / VP / decoder / OpenSharedResource) → `open timeout` ×3 → sender pinned to the
ffmpeg CPU decode path (readback + re-upload, ~13.5 fps at 4K, and a fresh interop registration
that also fails on the full card). **We destroyed a healthy resident pipeline exactly when we
couldn't rebuild it.** NDI→Spout never does this: one stable sender, no reopen ever.

## Fix

`decCheck` now separates the two causes of a publish stall:

- **`mtxTimeouts` climbing** = decoder producing, publish blocked on the receiver/DWM holding the
  texture. Pipeline alive + GPU-resident → **HOLD** (verdict `spoutHealthy`, reason names the
  contention). Publish resumes the moment the receiver yields the mutex - the same way a stable
  NDI→Spout sender rides out a busy card. No reopen, no new interop on the full card, no ffmpeg
  demotion.
- **no contention** (decoder produced nothing) = genuine wedge → **recycle** as before.
- handle changed (sender re-created) → recycle as before.

`watchDest` threads `mtxTimeouts` into the probe and logs the hold once per episode:
`native decode destination "…" destination contended (receiver/DWM holds the texture) - holding the
pipeline, not recycling`.

Files: `internal/mfenc/procdec_windows.go` (`decProbe`, `decCheck`, `watchDest`),
`internal/mfenc/procdec_windows_test.go` (contended-hold case). `go vet` + `go test -run
TestDecCheckOracle|TestDecWatchdog|TestDecRing ./internal/mfenc` pass.

## Why this beats NDI

We keep both wins NDI can't match - H.264 bandwidth (vs NDI ~250 Mbps near-raw) and GPU zero-copy
decode (vs NDI's system-RAM round trip) - and now add NDI's one advantage: a stable sender that
survives a saturated card instead of thrashing reopens.

## Verify on the NEXT real set (the bug only bites at saturation)

- `[gpumem]` climbs to near-full as before, but the relay must NOT log `recycling` / `reopen
  failed` / `pinning this sender to the ffmpeg decode path`.
- Expect `native decode destination "…" … holding the pipeline, not recycling` while free VRAM is
  low; `route decode telemetry` stays `engine:mf-native-decode gpuPublish:true` (never flips to
  `ffmpeg-decode`).
- Resolume shows no interop error for OUR relay sender across the whole set.

## Follow-ups (NOT done here - keep this change minimal + verifiable)

- VRAM-headroom-gated recycle: even a genuine wedge should not `close()`+reopen while free VRAM is
  below the rebuild cost (it can't succeed). Gate `recycleDest` on the `gpumem` monitor (seam like
  `ZeroCopyDecode`), hold + wait instead.
- Don't pin to the ffmpeg CPU path when the reopen failures were VRAM-pressure timeouts - it's
  heavier, not lighter. Only pin on genuine capability failures.
- Resolume's own 7.9 GB is the saturation source; independent of rave-mate (composition/clip res).
