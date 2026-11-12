# zigmedia STEP 0 — measure before porting (instrumentation)

Owner directive 2026-11-12: realtime paths are native Zig/C, never Go (CLAUDE.md "Realtime paths are
native"). STEP 0 = per-stage, per-frame timing so a hang is ATTRIBUTED, not assumed, and the Go-runtime
evidence to confirm/deny the diagnosis. Pure measurement, no behaviour change. Deploy immediately to
localize the owner's 4K60 micro-hangs (`seqGaps=27 recovered=28 pli=5 policyDrops=135`, one 10s window
publishedFps 46, loss 0.0% on clean 1 GbE — i.e. software, not the wire).

## What ships

- `internal/medialink/framestages.go` — `StageHist`: lock-free, allocation-free inter-frame INTERVAL
  histogram per pipeline stage. `Mark(nowNs)` runs on the realtime goroutine (atomics only, single-
  writer per stage); `Snapshot()` computes p50/p99/max/mean + counts of intervals >25/>50/>100 ms OFF
  the realtime thread. Process-monotonic clock (`monoNs`), independent of the media clock the child
  slews. Fixed buckets around 16.67 ms (60 fps) + a coarse tail for hangs.
- `internal/medialink/procruntime.go` — `ProcRuntime`: reads `/gc/pauses:seconds` (STW GC) and
  `/sched/latencies:seconds` (goroutine scheduling latency) and reports the DELTA per sample →
  p50/p99/max + counts >1/>5/>10 ms. This is the direct "is the Go runtime the hang" evidence. Sampled
  in the MEDIA CHILD process (the right runtime) at ~1 Hz by the telemetry emit.

## Stages instrumented (receiver = the owner's pain)

Marked on the Go data path (`internal/medialink/router.go`), surfaced per route in `RouteStat.Stages`
(flows child→daemon via `mediaTelemetry.Routes`) and printed by `ctl routes`:

| stage | mark site | what a spike means |
|---|---|---|
| `rx`  | runReceive, media frame out of `ReadFrame` | transport: socket rx / reassembly / recv-side Go pause |
| `jb`  | runJitter, after `jb.pop`                   | jitter buffer release scheduling (Go timer/scheduler) |
| `dec` | runJitter, after the decode/publish `sink.Write` returns | decode-submit BACKPRESSURE: the decode child's ring full = decode or Spout publish stalling |
| `enc` | runSend, after `src.Next`                   | sender: encoded AU cadence (source 120→60 pick / encoder) |
| `tx`  | runSend, after `WriteFrame`                 | sender: packetize + queue-to-pacer cadence |

`ctl media-perf` prints the media-plane Go GC/scheduler pause distributions.

## Reading it

- `rx` smooth + `jb`/`dec` spiky + `ctl media-perf` shows GC/sched pauses → **Go runtime on the frame
  path is the hang** (confirms the directive; port transport+jitter first).
- `rx` spiky with quiet GC → wire/socket/reassembly (unlikely on a clean LAN, but measured not assumed).
- `dec` spiky alone → decode child / Spout publish backpressure (native already; lower priority).

## Deferred (add if the first data points there)

- True "decoded" and "published" stages live INSIDE the native encode child (`native/zigenc`) and the
  mediaroute `spoutSink` — a different package/process. `dec` (submit backpressure) is the proxy for
  now; a dedicated publish-interval histogram on `spoutSink` is a cheap follow-up.
- "last datagram SENT" (inside the pacer/sendLoop) vs `tx` (packetize+queue). Raw capture cadence
  (inside the source/encoder child). Add per the sender-side work (STEP 2).

Acceptance instrument for the port: after STEP 1 (native transport), assert no stage interval > 2 frame
periods at any receive stage and zero steady-state allocations (same histograms, native-side).
