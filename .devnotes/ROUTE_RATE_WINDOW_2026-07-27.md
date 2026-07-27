# Route panel follow-up: the rate window and the latency sign (2026-07-27)

Branch `fix/route-rate-window` off `development` @32305d0. Field verification of the previous
branch (merged 32305d0) confirmed the freeze fix and the latency-epoch fix and surfaced two
survivors. Both are fixed here; both had a hypothesis that turned out to be WRONG before the real
one was reproduced, which is recorded below because the wrong ones look right.

## Defect 1 - a healthy route displayed 0.1 Mbps

Field: receive route, cumulative `Bytes` advancing ~0.96 Mbps sustained, peer's encoder telemetry
independently `kbps:781`, panel showing `0.1 Mbps` in five readings out of six with one `2.3 Mbps`.
0.1 Mbps is the reading that meant black frames all week - a working route displaying the number
for catastrophically broken is worse than the frozen counter it replaced.

### Hypotheses falsified first

1. **"numerator and divisor come from different spans."** They cannot: `count()` writes
   `rateAt`/`rateBytes` together under `st.mu`, and it is the only writer. Reading the code proves
   the pairing; a live in-proc probe (`TestReportedRateTracksCumulativeBytes/paced`) confirms the
   shipped code tracks truth to within ~8% on a steady stream.
2. **"the stream pauses between bursts, so most windows land in the quiet part."** Falsified by
   experiment: a source that idles between bursts does NOT reproduce it. The window is
   FRAME-driven, so with no frames in the gaps it closes on the first frame of the next burst and
   therefore always spans exactly one burst. The shipped code passes that arm.

### Actual cause, reproduced

Frames arrive at a **constant cadence** while the **bytes clump** at a longer period - small
inter-frames continuously plus a keyframe-sized payload every few seconds (the field route's
`kf 8 → 14` over 20 s ⇒ a clump about every 3 s). The window kept closing on schedule because
frames kept coming, but a window that fell between two clumps measured the trickle alone.

`clumpSource` (40 fps × 300 B + a 120 kB clump every 1.3 s, mean 803 kbps) against the shipped 1 s
window prints, verbatim:

```
displayed=100798 bps (truth 803214)
displayed=1056091 bps
displayed=97843 bps
displayed=1055226 bps
displayed=97840 bps
```

`97840 bps` renders as **`0.1 Mbps`**. That is the field signature - low readings at ~1/8 of truth
punctuated by overshoots - reproduced from the arrival shape alone. The arithmetic was never
wrong; the instrument was too short.

### Fix

A **4 s sliding window** over a ring of `(t, cumulative bytes, cumulative frames)` samples at
250 ms granularity. Bounded: `rateSpan/rateSampleEvery + 2` slots (17 × 32 B ≈ 550 B per route),
drop-oldest, never grows with traffic.

- `count()` only appends and evicts, on the route's own goroutine - the boundaries still never move
  with a poller, so the reader-driven defect the previous branch fixed stays fixed.
- `snapshot()` derives numerator and divisor from the SAME two endpoints, so it cannot disagree
  with `Bytes/elapsed`. It remains a pure read.
- The divisor extends to `now` rather than to the last counted frame, so a stalling route decays
  smoothly instead of freezing its last value; `rateStale` (3 s) still zeroes it outright.

Cost of the longer window: a genuine rate change takes up to 4 s to be fully reflected. That is the
right trade for a panel whose job is "healthy or broken at a glance" - the previous 1 s window was
responsive and wrong.

## Defect 2 - p50 rendered above p95

Field: `latency 29.0 ms/26.1 ms p50/p95`. Checked and ruled out, in order:

- **percentile index off-by-one** - `percentiles()` returns `s[n/2]`, `s[(n*95)/100]`, `s[n-1]`
  over an ascending sort: monotone by construction.
- **reversed sort** - the comparator is `s[i] < s[j]`.
- **swapped labels** - all seven locale strings order `{p50}` before `{p95}`, and both renderers
  pass them in that order.

What is left is the formatter. `fmtMs` / `fmtMsNs` take the **absolute value**. Given an ordered
pair, `displayed(p50) > displayed(p95)` is possible only when `p50 < 0 < p95` and `|p50| > p95` -
so the median transit is negative. The earlier epoch-sized inversion was the same abs() with a
much larger negative; fixing the epoch removed the symptom's magnitude, not its cause.

A negative median is legitimate here: `arrival − PTS` is measured on our media clock while PTS was
rebased onto the SENDER's, and both `SoftwareClock`s are process-relative. Until the sync tier
aligns them, a residual offset of tens of ms straddles zero - the **spread** is meaningful, the
absolute value is not. `fmtLat` now preserves the sign in both renderers (jitter keeps `fmtMs`; it
is non-negative by construction), so the panel reads `−29.0 ms/26.1 ms` next to the clock
tier/lock line that explains it.

## Gates (verbatim)

```
gofmt -l .                                              (clean)
GOWORK=off go vet ./...                                 (clean)
GOWORK=off go vet -tags spout ./...                     (clean)
GOWORK=off go build ./...                               OK
GOWORK=off go test ./...                                EXIT=0, 161 ok, 0 FAIL
bash scripts/build-zig.sh                               ravezig+rave-probe / raveui+rave-shell /
                                                        ravevr / rave-mate-enc built (0.16.0)
GOWORK=off go build -tags "zigdsp zigui zigvr encembed" ./...              TAG1-OK
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...     TAG2-OK
zig fmt --check src/  (native/zigui)   src/root.zig, src/wire_gen.zig  EXIT=1  ← PRE-EXISTING
zig build test --summary all           232/232 tests passed
GOWORK=off go test -tags zigui ./internal/webui -run TestZig              ok
```

`zig fmt --check` failing on `root.zig` / `wire_gen.zig` is pre-existing on `development` (verified
last branch against `git show origin/development:...`); neither file is touched here.

The hardware-MFT tests the coordinator flagged as GPU-contention flakes (`TestOpenSizeTable`,
`TestProcSession1080p60`, `TestProcTwoSessionsPerSessionDevice`) passed in this run, but the box's
GPU load was not under this agent's control - treat a green there as unconfirmed rather than proof.

## Non-vacuity

| gate | proves | how it was falsified |
|---|---|---|
| `TestRateWindowSurvivesClumpedBytes` | no reading below half the true mean under clumped bytes | `rateSpan` back to 1 s ⇒ `reading 0 = 96000 bps against a mean of 834462` |
| `TestReportedRateTracksCumulativeBytes/clumped` | live route: every reading within 2× of the cumulative slope | shipped `telemetry.go` ⇒ `displayed=100798 / 97843 / 97840 bps (truth 803214)` |
| `TestLatencyPercentilesAreOrdered` | p50 ≤ p95 ≤ max on a distribution straddling zero | ordering holds on values; the gate pins the premise the renderers broke |
| `ui.TestFmtRouteStat`, `webui.TestFmtLatKeepsTheSign` | the rendered sign survives | abs() restored ⇒ `negative p50 lost its sign (p50 > p95 on screen)` |

Also recorded: the *falsified* reproduction (a source that pauses between bursts) is kept out of
the suite deliberately - it passes on the broken code and would have been a vacuous gate.

## Neighbouring defects found, NOT fixed

1. **The two media clocks are not aligned in the field.** A negative median transit is the
   evidence. `SoftwareClock` is process-relative on both ends and only the sync tier (`AddSample` /
   `MirrorNow`) brings them into a common domain; until it locks, absolute e2e latency is an
   offset, not a duration. The panel now shows the sign honestly, but the alignment itself is the
   real fix and is untouched here. The clock line already reports tier + lock beside it.
2. `mediapipe`'s `rate` helper (`OutFPS`/`CapFPS`/`DecFPS`) still has BOTH defects this file fixed
   for the route rate: it closes its window on READ, and the window is 500 ms. A 10 s
   `routeTelemetry` line therefore reports whatever span the last unrelated reader closed, over a
   window short enough to miss a clump. Same two bugs, same fix shape. (Carried over from the
   previous branch's list, now with the second defect attached.)
3. Items 2-4 of the previous branch's list (the general `UIAnimAllowed` tick-gate exposure,
   `watchStreaming` treating OBS process presence as streaming, `spoutSink.Write` returning nil for
   a dropped frame) are filed on the coordinator's side and unchanged.
