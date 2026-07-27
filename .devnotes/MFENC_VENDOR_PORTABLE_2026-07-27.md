# mfenc vendor portability + the AMD concurrent-session AV (2026-07-27)

Branch `fix/mfenc-vendor-portable` off `origin/development` @ c3ed1f5.
Reference: `docs/dev/MF_NATIVE_ENCODE.md` (matrix, contract lessons, ledger, degrade rules).

## Root cause, with the evidence split

| Finding | Evidence | Status |
|---|---|---|
| The AMD axis is **session multiplexing / saturation**, not vendor support | Coordinator's field runs: single session clean 1/1 (real pixels, 0 drops); 2 sessions wedge 3/3 (timeout +2.2 s, child AV later) | **fixed by hypothesis, AMD run pending** |
| Every session built its OWN D3D11 device + `IMFDXGIDeviceManager`, so 2 sessions = 2 device managers on one adapter in one process | Code read; no reference MF pipeline does this | **fixed**: one device per child, refcounted, `RAVE_MATE_MFENC_DEVICE=session` to A/B |
| Child per-frame wait (2 s) == parent `encodeWait` (2 s), so a SATURATED encoder ended the route instead of dropping a frame | Field timeout lands at exactly 2.2 s; failing runs always had a 4K60 50 Mbps route live | **fixed**: 250 ms + `RC_BUSY` drop vs 4 s parent deadline |
| Drive mode was taken from `QI(IMFMediaEventGenerator)`, not `MF_TRANSFORM_ASYNC` | ffmpeg reads the attribute for the same reason; the MS software MFT is sync | **fixed** (hardening, NOT the AMD cause - see below) |
| Poison counter reset every route → safety net could never engage | Field log: `consecutive fails 1` on every route | **fixed**: (adapter, encoder) ledger, crash-to-crash |
| Mid-route native failure ENDED the route (ffmpeg substitution existed only at open time) | Code read + the "black stream, healthy counters" field shape | **fixed**: in-place substitution |
| `Stats()` read the shm mapping after `Close()` unmapped it | Reproduced as a 0xc0000005 in the mediapipe gate while adding a counter | **fixed** (my own regression, caught by the gate) |
| ~~Stale configured adapter LUID~~ **RETRACTED** - not stale at all | `requested=0x163a8` MATCHED `adapter=0x163a8` = a Radeon RX **7900 XTX** (discrete). The real bug was `ctl encoder-scan` never listing that adapter | **fixed elsewhere**: `internal/encoderscan` rendered its adapter line from PDH utilization counters, so an IDLE dGPU was invisible; now rendered from the DXGI enumeration |

### The drive-mode fix is NOT the AMD root cause - be precise about this

AMD's MFT is genuinely async: a single AMD session works, and it worked under the OLD
QI-based drive selection. So `MF_TRANSFORM_ASYNC` on AMD is almost certainly 1 and this fix
changes nothing there. It is still correct and load-bearing for portability (the MS software
MFT is sync, and a sync MFT that exposes the event generator would hang), and the new
`opened.drive` field finally answers the question per rig. I tried to reproduce the hang
locally with the software MFT: it returns `E_NOINTERFACE` (0x80004002) for
`IMFMediaEventGenerator`, so it cannot stand in for the failure. **No local reproduction of
the AMD failure exists on this box.**

## Verification split

**Verified by execution on this box (2× RTX 3060), all after the changes:**
- `TestProcTwoSessionsOneChild` - 720p + 480p concurrent in one child: 45/45 AUs each,
  ~35 KB/frame (real content), child alive, `drive=async` resolved correctly.
- `TestProcFieldTupleTwoSessions` - the exact field tuple, 4K60 50 Mbps live while a 720p30
  session opens: 720p 60/60 AUs, 4K 138 AUs, busyDrops 0, child alive.
- `TestProcTwoSessionsPerSessionDevice` - the A/B knob: byte-identical to the default on
  NVIDIA, i.e. **NVIDIA cannot distinguish the two device policies**. Only AMD can.
- `TestProcSoftwareTierEncodesRealContent` - forced software tier binds `H264 Encoder MFT`,
  `drive=sync`, 40 AUs, 8456 B/frame (real picture, asserted).
- `TestProcSoftwareTierTwoSessions` - software tier survives multiplexing too.
- `TestProcHardwareOnlyPolicyStillWorks`, `TestProcSession1080p60` (280 fps sustained, p50
  6.5 ms / p99 23.7 ms), `TestProcSession4K60CrashTuple`.
- `TestZeroCopyLiveSession` (`-tags spout`) - 91 AUs, capFrames 90, capFPS 60, skips 0,
  `capFlags=0x5`, encBusy 0.24 ms, **orientation + colour verified by decoding the
  bitstream** (top r=232 b=1, bottom r=2 b=243). NO regression on the zero-copy path.
- `TestDecodeLiveSession` - 40 AUs in / 40 frames published, destination texture bands
  correct, 0 errors.
- Ledger tests (6) - non-vacuous: crashes 2 MINUTES apart still accumulate 1→2→3 and poison,
  which the old per-spawn counter could not do.
- Gates: `gofmt -l` clean, `go vet ./...` clean, `go vet -tags spout ./...` clean,
  `go build -tags "zigdsp zigui zigvr encembed"` and `-tags "spout vr zigdsp zigui zigvr
  encembed"` clean, `go test ./...` clean except a flaky PDH counter test in `encoderscan`
  (untouched by this branch, passes 3/3 on retry), `zig fmt --check` + `zig build test` clean,
  `bash scripts/build-zig.sh` clean.
- NOTE: the live spout gates SKIP unless `SpoutLibrary.dll` is reachable. I ran them with
  `RAVE_MATE_CONFIG_DIR` pointed at a scratch dir containing `bin/SpoutLibrary.dll` (copied
  from the installed build). Without that they silently skip - worth knowing before trusting
  a green run.

**Reasoned, NOT verified:**
- Everything AMD. Both fixes for the concurrent-session AV are hypotheses.
- Intel/QSV entirely (no rig).
- The WARP rung of the software tier (this box always has a hardware video device).

## What I need run on the AMD box

Deploy this build, then in order:

1. `go test -tags spout ./internal/mfenc -run 'TestProcTwoSessionsOneChild' -v`
   → 720p + 480p, no 4K. **Passing here + failing in step 2 = capacity, not multiplexing.**
2. `go test -tags spout ./internal/mfenc -run 'TestProcFieldTupleTwoSessions' -v`
   → the exact field tuple. This is the headline result.
3. `RAVE_MATE_MFENC_DEVICE=session go test ... -run 'TestProcTwoSessionsPerSessionDevice' -v`
   → the A/B. **Failing here while step 1 passes proves the per-child device is the fix.**
4. `RAVE_MATE_MFENC_SW=1 go test ... -run 'TestProcSoftwareTier' -v`
   → confirms the last rung works on AMD hardware too.

Then the real two-route repro through `amd-verify.sh` (both PCs, judged on bitrate), which the
gates cannot cover.

For every run I want the **child stderr tail**, which now carries per-open lines like:

```
mfenc stage: bound AMDh264Encoder drive=async tier=hw aware=1
mfenc stage: device: reusing the child's shared D3D11 device (refs=2)
mfenc stage: drive=async async_attr=true evgen=true (qi hr=0x00000000) provides=true outSize=0 sw=false
```

`async_attr` is the value the old trace could not tell us. If a crash happens, the supervisor
warning now ends with `last stage: <call>` naming the faulting call, plus `last feed rc N`.

`outSize=0` with `provides=true` is fine and was checked: the CPU output-sample path is only
entered when `enc_provides` is false, so `outSize` is never used to size or index anything;
`ProcessOutput` is called with a NULL sample when the MFT provides its own.

## Deliberately not done

- **Advertising the software tier.** It stays a within-session last rung so a box with an
  ffmpeg hardware encoder but no hardware MFT keeps negotiating the better engine.
- **Serialising sessions onto one MFT.** The child multiplexes by design; N routes at once is
  a normal thing to want, and a shared MFT would serialise unrelated routes.
- **Sharing the NV12 pool / video processor across sessions.** Both are geometry-specific and
  already per-session, which is correct - the in-flight cap is per `Enc`, verified by reading
  it (the brief asked whether it was global: it is not).

## Open / next

- AMD runs above. Until then the concurrent-session fix is unproven.
- Intel rig for the matrix's third row.
- `TestProcFourSessionsOneChild` is behind `RAVE_MATE_MFENC_SOAK=1` (four concurrent encodes
  saturate a laptop GPU); worth running on the AMD box deliberately.


## OUTCOME: AMD field verify PASS (2026-07-27)

Deployed to both PCs; the exact two-route repro (4K60 spout + concurrent 720p webcam) ran **60+ s
with zero failures**, against 3/3 deaths in ~2.2 s with `0xc0000005` before. Telemetry named the
active path: `device=child(shared=true)` on both sessions, `async_attr=true`, `poisoned:false`,
`ledgerFails:0`, `degraded:""`, no child exit.

The content oracle did its job: webcam ~3.2 kB/frame at 742-785 kbps vs the black spout route at
**255 B/frame** - exactly the separation it exists to make.

### Corrections I got wrong and am recording

1. **There was no stale LUID.** I inherited "single AMD iGPU box" and propagated it. The box has a
   discrete **Radeon RX 7900 XTX** and the configured LUID was correct all along. The anomaly was
   `ctl encoder-scan` omitting that adapter - a scan-rendering bug, now fixed (below).
2. **The async-drive fix was never the AMD root cause** (already recorded, restated because the
   field data now confirms `async_attr=true`: AMD's MFT really is async, so the old QI-based
   selection reached the same answer there).

### Limitation: the two fixes are JOINTLY confirmed, not isolated

Both landed together, and choosing the old per-session device needs `RAVE_MATE_MFENC_DEVICE=session`
- a process launch with custom env on a machine with no toolchain and no remote-exec. So the
combination is proven; neither the per-child device NOR the saturation-budget change is proven
sufficient on its own. Isolating them needs an AMD box with a toolchain or a user-run launch with
that env var. **Do not describe the shared-device change as independently proven.**

### encoderscan: idle adapters were invisible (fixed here)

`Report.String()` rendered its `adapters:` line from `AdapterEncPct`, which is keyed by **PDH
`\GPU Engine` samples**. An idle discrete GPU has no engine instances, so it never appeared; the
iGPU driving the display always did. `AdapterNames` (DXGI) had both the whole time.

- adapters are now listed from the DXGI enumeration unioned with PDH, each row carrying its encoder
  FAMILY and free VRAM, and idle adapters read `enc=? (idle: no GPU-Engine counters)` rather than
  vanishing or faking a measured 0%;
- two same-vendor GPUs make the encoder→adapter join ambiguous by design (`adapterForFamily`
  refuses to guess), which rendered as a bare `device=?`. The scan now says so explicitly.
- `enumAdapters` hardened: HRESULTs masked to 32 bits before comparison (the Win64 ABI leaves RAX's
  upper half undefined, so a full-width compare can miss `DXGI_ERROR_NOT_FOUND`), a bad index is
  SKIPPED instead of abandoning the walk (it used to truncate the list at the first odd adapter),
  and the walk is bounded.
- `TestReportListsIdleAdaptersFieldTopology` reproduces the SUS topology exactly and was **proven
  non-vacuous**: restoring the old renderer makes it fail with "idle discrete GPU … missing".

NOTE: `avoid-busiest` (`ResolveDevice` PolicyAvoid) iterates `Adapters()` (DXGI), so it did see
both adapters - the coordinator's concern that it could only pick the iGPU appears not to hold for
that code path specifically. The SCAN output was genuinely wrong, and that is what a human reads
before pinning a device.

### Still argued, never executed

- **Intel/QSV**: no rig. Same code path as AMD/NVIDIA, nothing Intel-specific - but that is
  reasoning.
- **WARP rung** of the software tier: this box always has a hardware video device.
