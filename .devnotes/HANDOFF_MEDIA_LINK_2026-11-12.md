# Media link handoff (2026-11-12) — context for fresh agents

Read with: `CLAUDE.md` ("Realtime paths are native: Zig (or C), never Go" — HARD), `.devnotes/ZIG_MIGRATION.md`
"Why Zig", `ZIGMEDIA_DESIGN.md`, `ZIGMEDIA_TRANSPORT.md`, `ZIGMEDIA_STEP0_STATUS.md`, `UDP_TRANSPORT.md`,
`PWCAPTURE.md`, `ADAPTIVE_BUDGET.md`, `VRAM_BUDGET_AND_PLANNER.md`.

## Rig (live, owner watches it)
- Sender `sus`: Arch, RX 7900 XTX, 192.168.120.3, headless `rave-mate --service` (systemd --user unit, `/opt/rave-mate`,
  ctl 127.0.0.1:47620). Source = PipeWire node `spout2pw.VRChat.VRCSender1` (VRChat via Spout2PW/libfunnel, DMA-BUF
  single-plane at 4K, top-down, VRChat renders 120 fps, node negotiates 60). Test producers: `/tmp/pw4ktest/`
  (`funnel-producer -bar|-stdin|-double`, `start2.sh`). NOTHING is ever compiled or installed on sus (owner rule);
  Linux binaries come from the pinned container (`scripts/build-linux.ps1 -Tags pipewire`); the lead deploys via the
  infra playbook. ssh: `ssh -o BatchMode=yes root@192.168.120.3`; run as user:
  `runuser -u dymattic -- env XDG_RUNTIME_DIR=/run/user/1000 …`. Use `pgrep -f "[f]unnel-producer"` (self-excluding).
- Receiver = this Windows PC (RTX 3060 12 GB shared with Resolume + OBS + Parsec): installed app
  `%LOCALAPPDATA%\Programs\rave-mate\rave-mate.exe`, only the lead deploys/restarts (`scripts/deploy-local.ps1`).
  Republishes as Spout sender `rave-mate link Spout2PW (VRChat): VRCSender1` (3840x2160).
- Peer id of sus on the receiver: `cze9ZwhbT-3MCn00g02_6xvRiGUJUYFhBljPV4hlJiU`. Receiver config currently:
  `mediaLink.transport=tcp`, `bitrateMode=fixed`, `bitrateKbps=50000` (owner-facing safety settings, lead-owned).
- Never stop/restart the owner's routes; no `route-stop … all`; own producer + source names only; read-only ctl is fine.
- GPU gate on the Windows PC: ≥ 4 GB free VRAM, runs ≤ 15 s, abort < 3 GB.

## What exists (all on `development`)
- Linux sender: `internal/pwcapture` C shim (libpipewire dlopen, DMA-BUF → VA zero-copy `h264/hevc_vaapi_native`,
  CQP quality mode + closed-loop bitrate ceiling, SPSC AU ring, dedicated encode thread, PTS-grid 120→60,
  `AttachNativeTX` → `rm_tx_submit_au`). ffmpeg VAAPI readback path = fallback.
- Windows: MF/D3D11 encode+decode in the Zig child `native/zigenc` (live coded-resolution cap; destination geometry
  invariant), VRAM admission budget `internal/gpubudget`, planner `internal/encoderscan`, adaptive budget
  (`internal/medialink/adaptbudget.go`; auto mode fixed for fps=0 but NOT yet re-verified live → receiver is on `fixed`).
- Transport (legacy Go, `internal/medialink`): TCP + UDP (fragmentation, NACK, pacer, per-route key schedule,
  auth-before-bind, replay-safe rebind, bye, start-up grace), congestion controller, one route per (peer,source),
  fast re-offer with backoff, remembered receives. Wire spec = `UDP_TRANSPORT.md`.
- Native Zig transport `native/zigmedia` (C ABI v2 `include/ravemedia.h`): wire core (goldens vs Go), UDP sender
  runtime (`rm_tx_*`), receiver core (socket RX Linux, reassembler, PTS-locked playout with transit-min base +
  jitter-sized margin, hrtimer, histogram), end-to-end native↔native on loopback; Go hook
  `medialink.NativeTXSource/NativeTX` (router supervises, `impl=native`). Last commit 417e23eb fixed large
  multi-fragment frames / pacing / PTS domain / native telemetry — NOT yet verified on the rig.
- Instrumentation: `ctl routes` per-stage interval histograms (`net`, `rx`, `jb`, `dec`, `enc`, `tx`), `ctl media-perf`
  (Go GC/sched pauses), "playout hitch" log lines, `ctl logs -n N`.

## Measured facts (do not re-derive)
- Go GC/scheduler pauses are NOT the hitch cause (max 0.5 ms). Sender is even on a clean source (tx max ~19 ms).
- Receiver host (this PC) stalls at kernel/driver level: ping sus→receiver 0 % loss, 215/120 000 replies > 5 ms,
  10 > 20 ms, max 37 ms; arrival gaps up to ~140 ms; hitches 50–90 ms a few per 5 min, cause `wake/arrival`.
  Receiver NIC drops were fixed by the owner (rx buffers 2048, EEE off): 0 errors/1 M packets.
- First live native-TX test (before 417e23eb): attach + InitialSeq + rebind OK; large frames failed
  (reasmDrops, NACKs unanswered), small frames played but `lateRate=1.00` (clock domain). Lead reverted to TCP.
- After a receiver NIC link reset the peers stayed `paired`, never reconnected until restart (bug).
- Synthetic bar route delivers ~1 fps after a restart (`fps wire=3 out=0`) while VRChat is fine (bug).

## Owner directives
1. Realtime path native (Zig/C); Go = control only. All new development Zig-first.
2. "Not perfect lab conditions": do NOT chase where the receiver's delays come from — make rave-mate ride through
   them: playout that budgets for arrival delays (grow fast, decay slow, no burst/jump — a miss = one repeated frame),
   UDP default with deadline-aware recovery, sender that never stalls/drops on a receiver hiccup, chaos acceptance
   suite (profile "this rig": receiver stalls 20–140 ms at 0.5–2/min, 0.01–0.05 % loss, small bursts, mild reorder;
   plus "wifi-ish", "congested"), latency ↔ smoothness presets (default cap ≈ 150 ms), numbers visible in `ctl routes`.
3. Budget adaptive per system (auto) — keep `fixed` on the live receiver until auto is verified on the picture.
4. No builds on sus. NDI rejected. No OBS on the gaming PC.

## Open work, ordered
A. Stopgap on the legacy Go receiver: grow-fast/decay-slow lateness-quantile playout margin (was in progress, uncommitted work lost).
B. Verify 417e23eb in the Linux container against the real Go receiver at 4K60-realistic sizes; per-route transport
   override so ONE test route can run UDP+native while others stay TCP; then rig test on a test source.
C. Native Windows receiver in the Zig child: Winsock RX (first-byte ts) → auth → reassembler → adaptive PTS-locked
   playout (CreateWaitableTimerEx HIGH_RESOLUTION) → D3D11 decode → Spout publish, in-child publish histogram,
   receiver-side Go hook; resilience behaviour + chaos suite.
D. Native TCP sender path; soak with failing-allocator guard.
E. Peer link reconnect after link flap; bar-route 1 fps bug.
F. Linux: quality-mode table + PSNR/SSIM; native-path enc/tx histogram.
G. Budget: sender→receiver EncoderFeedback wire, aggregate link sharing, live re-verification of auto.
