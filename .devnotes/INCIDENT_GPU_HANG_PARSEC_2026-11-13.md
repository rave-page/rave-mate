# Incident: suspected GPU driver hang + Parsec problems (2026-11-12/13)

Read-only investigation, 2026-11-13 03:54-04:10. Nothing changed on either machine (one exception:
a temp copy of sus `ctl logs` at `/tmp/.rm_sus_logs.txt`, read and deleted at once).

## TL;DR

- **No GPU driver hang on either machine.** DJ PC: 0 `nvlddmkm` events, 0 Display/4101 TDR events,
  0 LiveKernelEvent 117/141 in 30 days. sus: no amdgpu ring timeout / reset in current boot
  (since 2026-11-11 20:07). The "hang" was the **Parsec view of the DJ PC** freezing / stuttering
  / dropping.
- **Parsec direction:** DJ PC (`lil-biatch`, 192.168.120.4) = Parsec **HOST** (NVENC H.265 1080p,
  3 Mbps target); sus (192.168.120.3) = Parsec **CLIENT**.
- **Proximate cause of the 2 disconnects (03:37:01, 03:50:49):** Parsec host NVENC failure
  `nvEncLockBitstream failed: 8` on the DJ PC, with VRAM free oscillating 140-430 MB/s
  (~159 MB at 03:37:01) and encode latency spiking to 381 / 671 ms in the 2 s before each.
- **Root condition:** the RTX 3060 has run at ≤ 0.5 GB free since ~00:00. Resolume Arena holds
  ~10.0-10.8 GB (it was 2.1 GB at 11-12 01:00). rave-mate's 03:18 `vramPolicy=balanced` deploy
  added ~215 MB (encode/decode child 109 + media child 74 + 4K BGRA sender ~32) and took the card
  from ~500-700 MB free to ~150-400 MB free. That band is where Parsec's NVENC began failing.
- **Arena-leak-per-restart hypothesis:** confirmed but **small**. About +16 MB per rave-mate restart
  while a 4K sender is active, measured on a quiet Arena; 0 MB when no route. At most ~0.5 GB across
  all 32 restarts. Arena's growth of ~8 GB is its own working set (composition/clip activity),
  not rave-mate churn.
- **sus `parsecd` SIGSEGV (03:49:35):** a client-side crash during repeated reconnect attempts
  after the host-side failures. `si_code=SI_TKILL` means it was self-raised, not a GPU fault. The
  amdgpu "non-zero when fini" line is benign teardown info (the same line appears for vesktop's
  normal restart at 01:09:44).

## Clocks

Both machines are on UTC+1. The DJ PC is `W. Europe Standard Time` (CET in November, not WEST),
and sus is CET. **There is no +1 h offset between them.** Cross-check: client disconnect at sus
03:37:04 vs host 03:37:01; reconnect at sus 03:49:51 vs host 03:49:48 (sus ≈ +3 s). All times below
are CET.

## Sources

| What | Where |
|---|---|
| rave-mate DJ PC file log (Info+, all restarts) | `rave-suite/rave-mate-dev/rave-mate-debug.log` (CWD of the dev deploy; lines carry `HH:MM:SS` only, date from `==== rave-mate start` banners) |
| rave-mate DJ PC stale file log | `%LOCALAPPDATA%\Programs\rave-mate\rave-mate-debug.log` (388 MB, unrotated, last write 2026-11-11 23:54, when deploys switched CWD) |
| rave-mate DJ PC ring | `rave-mate ctl logs -n 4000` (only covers 03:18:24+, the last restart) |
| Parsec host | `C:\ProgramData\Parsec\log.txt` (1 rollover generation `log.1.txt`, ~14.6 MB) |
| Resolume | `%LOCALAPPDATA%\Resolume Arena\Resolume Arena log.txt` |
| Windows | System/Application logs, WER 1001, `Win32_ReliabilityRecords` |
| sus | `journalctl` (persistent, 4 GB), `coredumpctl`, `~/.parsec/log.txt`, `/home/dymattic/rave-mate-debug.log`, `pw-dump`, sysfs |

## Timeline (CET)

| Time | Machine | Event | Evidence |
|---|---|---|---|
| 11-10 16:11 | DJ | Parsec host (parsecd pid 19100) started, release10 150-105c | process CreationDate, log head |
| 11-10 21:51 | DJ | Resolume Arena started (pid 30492). **Not restarted since** | process CreationDate |
| 11-11 23:54 → 11-13 03:18 | DJ | **32 rave-mate starts** (banners in repo `rave-mate-debug.log`) | see table §Arena |
| 11-12 01:47-03:01 | DJ | 7 restarts, no media route; Arena flat **2113 MB** | `[gpumem] vram by process` |
| 11-12 04:21:48 | DJ | Arena `Context will be deleted: monitor` + GL re-init → Arena 2113 → ~797 MB | Arena log, gpumem 04:26 |
| 11-12 04:56-14:01 | DJ | 5 restarts with a 4K route active; Arena **828→844→876→892→908** (+16/+32/+16/+16), flat for 8 h between | gpumem |
| 11-12 14:06-14:11 | DJ | Arena **+3809 MB** with no restart. Arena `Auto-saving layout` at 14:09 (UI activity) | gpumem, Arena log |
| 11-12 14:11-20:40 | DJ | Arena 3.0-5.9 GB with ±2 GB swings every few minutes; Parsec healthy (~51 fps, encode lat avg 1.7-1.9 ms) | gpumem, Parsec |
| 11-12 20:40:26-58 | DJ | NIC change: UAC/secure desktop → Parsec `DXGI_ERROR_ACCESS_LOST`; I211 link down 20:40:31-38 + 20:40:46-53; client dormant/back twice; 2× LiveKernelEvent 0x15E (NDIS) | System e1rexpress 27/32, Parsec log, WER |
| 11-12 21:40-21:50 | DJ | Arena step to 8-9 GB | gpumem |
| 11-12 23:35-23:47 | sus | PipeWire link negotiation storm (`no more input formats`, >1000 lines), fixed later | journal |
| 11-12 23:00-24:00 | DJ | free VRAM avg 688 MB (min 141); Parsec encode lat avg 3.7 ms | hourly table |
| 11-13 00:05-00:44 | sus | rave-mate starts **two 4K60 HEVC VA-API encodes every ~9 s** (VRChat + leadbar), DJ side refuses → `broken pipe` → repeat (~13 starts/min) | sus `rave-mate-debug.log` |
| 11-13 00:05-02:05 | DJ | rave-mate re-offers 2 remote sources every ~9 s, refused by VRAM budget (`strict`, reserve 1152 MB); keeps a kept-alive 3840x2160 leadbar Spout sender | DJ log 00:06-00:23 |
| 11-13 00:15:50-00:16 | sus | xdg-desktop-portal screencast sessions (`kwin_wayland` node offered 2560x1394, 950x818) = Vesktop screen-share picker | journal, sus rave-mate log |
| **11-13 00:18-00:20** | DJ | **Parsec degradation onset**: fps 52 → 8, bitrate 2.9 → 0.2 Mbps, encode stalls up to 1.4 s | Parsec host FPS lines |
| 11-13 00:20-02:12 | DJ | Parsec 8-17 fps, 0.1-0.3 Mbps, encode stall spikes 0.6-2.2 s; free VRAM avg 292-384 MB | merged table |
| 11-13 00:23-01:35 | sus | kwin `Atomic commit failed: EBUSY` (~10, then ~80 by 04:00) | journal |
| 11-13 01:09 | sus | Vesktop restarted (1.6.9 → 1.6.8); amdgpu "non-zero when fini" for old vesktop pids | journal |
| 11-13 02:05:37 | DJ | rave-mate restart; from now on it **holds** without offering (`receive HELD ... no offer sent`), so sus encode churn has already stopped (it ended 00:44) | DJ log |
| **11-13 02:12-02:14** | DJ | **Parsec recovers** to ~50 fps (cause not identified, nothing in rave-mate/Arena logs) | Parsec |
| 11-13 03:00 / 03:20 | DJ | Parsec encode stalls 1.43 s / 1.90 s | Parsec |
| **11-13 03:18:24-31** | DJ | rave-mate restart with `vramPolicy=balanced`: opens 4K leadbar Spout sender (fmt 87 = BGRA 3840x2160) + MF HEVC decode child at freeMB 134-385 | ring log |
| 11-13 03:18:33-03:53:58 | sus | leadbar 4K60 hevc_vaapi_native encode, healthy (capDrops 0, forcedIDRs 0, ~240 B/frame) | sus ctl logs |
| 11-13 03:18-03:37 | DJ | free VRAM swings 134↔430 MB every second; rate governor flaps 30↔50 Mbps | ring log |
| **11-13 03:37:00-01** | DJ | Parsec `FPS:41.4/16, L:11.1/381.5` → `nvEncLockBitstream failed: 8` → host closes session (`host_msg_thread_cleanup = -15107`). freeMB 414 at 03:37:00, 159 at 03:37:01 | Parsec host, ring log |
| 11-13 03:37:04 | sus | client `disconnected with status code: -15107` | `~/.parsec/log.txt` |
| 11-13 03:43:56-03:47:50 | sus | 3 reconnect attempts fail (status 6 after 30 s). Host logged no attempt | client log, host log |
| 11-13 03:44:26 | sus | parsecd stdout flush shows the message loop lagging since 22:33:52 (`we're 21 messages behind`) | journal |
| **11-13 03:49:35** | sus | **parsecd pid 2554 SIGSEGV (SI_TKILL)** in `parsecd-150-105c.so+0xa6adc` (thread 3140), during the 4th connect attempt (started 03:48:19). Core 19.2 MB kept | coredumpctl |
| 11-13 03:49:46-48 | sus/DJ | parsecd restarted (pid 523503) and connected; `libvdpau_nvidia.so` probe failure = benign (old process logged it too, at 03:47:52, then fell back to `FFMPEG 4 Hardware`) | journal, host log |
| **11-13 03:50:45-49** | DJ | `L:11.2/298.8` → `L:25.3/671.2` → `nvEncLockBitstream failed: 8` → disconnect; reconnect 03:51:00 | Parsec host |
| 11-13 03:53:03-58 | sus/DJ | leadbar withdrawn, route ended; DJ media child respawned 03:53:56; enc child gone | logs, process list |
| 11-13 03:57 | DJ | Parsec still degraded (`FPS:20.3/10, L:34.4/492`); VRAM 11872/12288 MiB, GPU 59 %, enc 6 % | Parsec, nvidia-smi |

## Arena VRAM vs rave-mate restarts

Method: pair the last `[gpumem] vram by process` sample before each `==== rave-mate start` banner
with the first one after it (samples every 5 min; adapter totals every 1 min).

| Window | Restarts | rave-mate GPU children | Arena before → after | Per-restart step |
|---|---|---|---|---|
| 11-12 01:47-03:01 | 7 | none (rm 0 MB) | 2113 → 2113 | **0 MB** |
| 11-12 04:56-14:01 (Arena idle, flat 8 h between) | 5 | enc+media 167-183 MB, 4K sender | 780 → 828 → 844 → 876 → 892 → 908 | **+48 (1st, route open) / +16 / +32 / +16 / +16** |
| 11-12 17:03-11-13 03:18 (Arena active) | 17 | varies | swings −1087 … +589 | lost in ±0.4-2 GB noise |

Conclusion: about **16 MB per sender re-creation** (≈ one 1080p-class RGBA follow surface plus
driver overhead). It accumulates until Arena restarts, but it is bounded by restart count: ≤ ~0.5 GB
for the 32 restarts here. Arena's rise from 0.8 to 10.5 GB happened **between** restarts, in
steps tied to Arena activity:
- 14:06-14:11: +3.8 GB, with `Auto-saving layout` at 14:09.
- 21:40-21:50: +2.1 GB.
- 00:00-03:53: steady ~10.0-10.8 GB.

Arena's own GL context re-creation at 04:21 dropped it by 1.3 GB, so a restart of Arena reclaims
the bulk. The historical churn class (`SPOUT_INTEROP_VRAM_CHURN.md`: ~25 cycles → interop
`E_OUTOFVIDEOMEMORY`) is still a valid risk on top: 32 cycles happened tonight. However, no
interop-failure (`0x8876017C`) line or Arena interop error was logged.

## Free VRAM vs Parsec host health (hourly)

| Hour | free avg MB | free min | Parsec fps avg | encode lat avg ms | minutes with >300 ms stall |
|---|---|---|---|---|---|
| 11-12 16h | 5123 | 3368 | 51.4 | 1.72 | 0 |
| 18h | 3882 | 2628 | 51.6 | 1.89 | 0 |
| 20h | 2165 | 433 | 51.5 | 2.03 | 0 |
| 21h | 1996 | 230 | 51.6 | 2.55 | 0 |
| 22h | 1380 | 253 | 52.0 | 3.04 | 0 |
| 23h | 688 | 141 | 51.4 | 3.69 | 0 |
| 11-13 00h | 384 | 105 | 24.0 | 6.18 | 13 |
| 01h | 292 | 112 | 12.6 | 6.79 | 14 |
| 02h | 316 | 120 | 41.2 | 5.95 | 14 |
| 03h | 358 | 141 | 50.1 | 5.62 | 20 |

Encode latency rises monotonically as free VRAM falls. Multi-hundred-ms encoder stalls appear only
once average free VRAM is below ~700 MB, and NVENC hard failures only appeared in the 150-400 MB band.

## Causes, ranked

1. **Parsec host NVENC starved on a full RTX 3060 (DJ PC). Confidence: high for the 03:37 and
   03:50 disconnects; medium for the 00:18-02:12 degradation.**
   - Disconnects: `nvEncLockBitstream failed: 8` (`NV_ENC_ERR_INVALID_PARAM` per `nvEncodeAPI.h`;
     NVENC often reports odd status codes when its bitstream/resource allocation fails under
     memory pressure), preceded by encode stalls.
   - Load: Arena ~10.3 GB is the bulk. rave-mate's balanced admission (03:18) removed the last
     ~200 MB of headroom; both failures happened while its 4K route was up (03:18:31-03:53:58).
   - The 00:18-02:12 degradation happened under `strict` with rave-mate ≤ 74 MB on the card, so
     rave-mate's admission change is **not** its cause. What changed at 00:18 and at 02:13 is
     unidentified.
   - Confirm: per-second NVML samples (fb used, enc util, enc session fps/latency) through the
     next event. Bench test with Arena loaded: Parsec stable with ≥ 700 MB free, failing at
     ≤ 300 MB free, and with the rave-mate route toggled on/off.
2. **Static or stalled captured picture (00:18-02:12). Confidence: low-medium; alternative or
   contributing to 1.**
   - fps 8-17 with 0.1-0.3 Mbps is also what Parsec shows when the desktop barely changes
     (DXGI duplication only yields changed frames).
   - Arena kept allocating (±0.5 GB swings), so Arena was not hung.
   - Confirm: Parsec log lacks a capture-rate field; add GPU sampling plus an OBS/Arena fps probe.
3. **Parsec client crash on sus (03:49:35). Confidence: medium that it is a Parsec client bug
   triggered by the reconnect loop.**
   - Not GPU: SI_TKILL, no amdgpu fault, no decoder failure logged tonight. One older
     `Decoder failure, queued_frames=250` exists, at 11-11 22:29.
   - The message loop had been lagging since 22:33.
   - Confirm: report to Parsec with the core; reproduce by forcing host disconnects.
4. **sus 4K VA-API encode churn (00:05-00:44, ~13 encoder sessions/min). Confidence: low as a Parsec cause.**
   - It only overlaps the first 26 min of the degradation window, and the host-side metrics (the
     bad ones) live on the DJ PC.
   - Still wasteful: fixed by the 02:05 build ("HELD ... no offer sent").
5. **Ruled out or no evidence:**
   - GPU TDR / driver reset: none.
   - NIC change (20:40): explains a 30 s blip only; stats normal afterwards.
   - `timeBeginPeriod(1)`: Parsec was healthy 03:18-03:36 with the media child running. No
     evidence either way; Parsec itself typically raises timer resolution.
   - Chronic Windows noise, both pre-dating these changes:
     - NDIS LiveKernelEvent 0x15E roughly every 4 h for ≥ 30 days.
     - Tcpip 4266 UDP port exhaustion every 1-2 days.
     - Both deserve their own ticket (Intel I211 `e1rexpress` driver).
   - Vesktop downgrade: no Parsec change at 01:09.
   - kwin screencast: `pw-dump` shows the only `kwin_wayland` video link is → `vesktop` (Discord
     share). rave-mate only advertised the node, and Parsec (a client) does not capture.

## Immediate safe actions (recommendations only)

1. **Restart Resolume Arena** at the next safe moment. It has been up since 11-10 21:51 and holds
   ~10.5 GB; its 04:21 context re-init proved most of that is reclaimable. Before a set, check
   what grows its working set (composition clip resolutions/codecs, preview monitor). The +3.8 GB
   step followed UI activity at 14:09.
2. **DJ PC rave-mate: set `mediaLink.vramPolicy` to `"strict"`** (`internal/config/config.go:498`,
   `internal/gpubudget/policy.go`) until Arena has headroom. Under balanced, rave-mate admits into
   the exact band (150-400 MB free) where Parsec NVENC failed. Policy follow-up: floor the
   admission on a fixed **system headroom** (≥ 700 MB free after admission, covering other apps'
   NVENC/OBS), not only on rave-mate's own need + 25 %.
3. **Stop the test producers / forget the remembered remote sources** (`leadbar`, `Spout2PW
   (VRChat)`) on the DJ PC so it doesn't auto-reactivate them. Avoid back-to-back rave-mate redeploys
   while Arena is running (+16 MB Arena per cycle with an active sender, and the historical
   interop-churn risk).
4. **Rate governor flaps 30↔50 Mbps every second** around freeMB ~200 (DJ ring 03:18-03:37). Add
   hysteresis.
5. Noise to fix in rave-mate:
   - The fingerprint sweep re-fails the same 7 files (`fpcalc exit status 2`) every minute, forever.
   - Peerlink re-dials sus every ~31 s with no backoff growth.
6. Parsec client: if crashes repeat, attach `coredumpctl` core 2554 to a Parsec support ticket.

## Persistent logging (proposal - nothing applied)

### DJ PC (Windows)

| # | What | Admin? | Notes |
|---|---|---|---|
| W1 | **rave-mate file log already exists** (`internal/debuglog`, Info+ incl. `[gpumem]`/`[gpuwatch]`). It is not "off", it is **CWD-relative**. Fix in code: fixed path `%LOCALAPPDATA%\rave-mate\logs\rave-mate.log`, size rotation (e.g. 5 × 50 MB), full date in each line timestamp (`2006-01-02T15:04:05.000`) | no | The install-dir copy silently went stale at 11-11 23:54 when the dev deploy started launching from the repo CWD; the 388 MB file was never rotated. Dated lines remove the banner-inference step used here |
| W2 | rave-mate `gpumem.csv`: 1 row/min adapter total/free plus per-process top-N (today per-process only every 5 min), plus admission holds/decisions | no | Same rotating dir. Per-process at 1 min would have pinned the Arena steps exactly |
| W3 | **NVML sampler** as a user Scheduled Task at logon: `nvidia-smi --query-gpu=timestamp,memory.used,memory.total,utilization.gpu,utilization.encoder,utilization.decoder,clocks_event_reasons.active,pstate,temperature.gpu,power.draw --format=csv,noheader -l 5` piped through a tiny PowerShell wrapper that writes `%LOCALAPPDATA%\gpulog\nvsmi-YYYYMMDD.csv` and deletes files > 14 days | no (user task) | No GPU context allocated by NVML queries. Optionally `nvidia-smi --query-compute-apps` / `pmon` every 60 s, but per-process util under WDDM is often blank (probe once first). Also `nvidia-smi encodersessions` (per-session fps/latency) every 10 s, which directly tracks Parsec/OBS NVENC |
| W4 | Keep Parsec host log history: the same task copies `C:\ProgramData\Parsec\log.txt` daily to `%LOCALAPPDATA%\gpulog\parsec-YYYYMMDD.txt` | no (file is readable) | Only one rollover generation (`log.1.txt` ~14.6 MB). It contains peer handles/IPs, so keep it local and never paste raw |
| W5 | Event channels: list with `wevtutil el` filtered on `Dxgkrnl`/`LiveDump`, enable the Dxgkrnl operational/admin channel if absent; keep LiveKernelReports default (TDR 0x117/0x141 watchdog dumps on by default) | **yes** | System log 4101 + WER 1001 already capture TDRs; none occurred. `C:\Windows\LiveKernelReports` needs admin to read |
| W6 | On-demand only: `wpr -start GPU -filemode` … `wpr -stop` when a freeze is live (GPUView trace) | **yes** | Not continuous; too heavy |

### sus (Arch) - all via the infra Ansible repo (playbooks from .250), not by hand

| # | What | Root? | Notes |
|---|---|---|---|
| S1 | journald: already persistent (`/var/log/journal`, 4 GB, default config). Pin it with a drop-in `/etc/systemd/journald.conf.d/50-retention.conf`: `SystemMaxUse=4G`, `MaxRetentionSec=30day` | yes | Prevents a future default change from silently losing history |
| S2 | coredumps: defaults work (core kept, 22 MB used; tmpfiles `d /var/lib/systemd/coredump … 2w`). Drop-in `/etc/systemd/coredump.conf.d/50-keep.conf`: `MaxUse=2G`, `KeepFree=10G`; tmpfiles override to `4w` | yes | |
| S3 | **amdgpu devcoredump capture**: on a GPU reset amdgpu writes `/sys/class/devcoredump/devcd*/data`, which auto-expires after ~5 min. Add a udev rule (`SUBSYSTEM=="devcoredump", ACTION=="add"`) starting a oneshot that copies `data` to `/var/lib/amdgpu-devcoredump/<ts>.bin` | yes | The single most useful knob for a real GPU hang. Keep `gpu_recovery=-1` (auto) and the default `lockup_timeout`. Avoid `drm.debug`: far too noisy for continuous use |
| S4 | GPU sampler: systemd service with a 10 s loop (or a 1-min timer), reading `card1/device/{gpu_busy_percent,mem_info_vram_used,mem_info_vram_total,mem_info_gtt_used}` + per-process `drm-memory-vram`/`drm-engine-*` from `/proc/*/fdinfo` (what nvtop reads) → `/var/log/gpustat/gpustat.csv`, plus logrotate daily, keep 14 | yes | card1 = RX 7900 XTX (card0 = 512 MB iGPU). `gpu_busy_percent` read 121 % once, so clamp. `radeontop` is not installed and not needed |
| S5 | rave-mate on sus already persists `/home/dymattic/rave-mate-debug.log` (CWD = home). Same W1 code fix: fixed `~/.local/state/rave-mate/logs/`, rotated, dated lines | no | |
| S6 | Parsec client: `~/.parsec/log.txt` (small, appends) + parsecd stdout in the persistent journal (user unit `app-parsecd@…`). Nothing to add | - | |

## Open questions

- What started the 00:18 Parsec degradation and what ended it at 02:13? The logs contain no
  trigger; W3 + W2 would answer it next time.
- Was the 03:37 / 03:50 NVENC failure an allocation failure (needs NVML fb samples at 1 s) or an
  NVENC session-level issue? `nvidia-smi encodersessions` would show it.
