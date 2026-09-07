# Incident 2026-09-07: Resolume interop error, 4K relay sawtooth, wrong VRAM max on the VR PC

## Symptoms

1. Resolume "cannot create OpenGL/DirectX interop" dialog mid-set in every recent set;
   Resolume log shows the zombie signature "another instance is already running" twice
   at 2026-09-06 02:53.
2. VRChat relay (Spout sender "rave-mate link VRCSender1", 4K60,
   VR PC -> DJ PC -> Spout -> Resolume) plays slower than real time then jumps forward;
   the 720p30 webcam relay is fine; Spout->NDI->Spout is fine.
3. VRAM watchdog toast on the VR PC showed the APU iGPU's maximum, not the RX 7900 XTX's 24 GB.

## Root cause 1 - stale build deployed 2026-09-02 17:03

Exe built from `fix-publish-token-refresh` (merge-base with development 08b144f, 2026-07-27),
114 commits behind origin/development 85022aa. Missing:

- 4159257 / 70ab8f8 / 33e7313 zero-copy capture + native GPU decode default + route decode telemetry
- 7a067d1 interop pre-flight
- 12b25a7 deck-sender keep-alive
- 7cd37ba failed-DLL-load cache fix
- 5315f61 + 85022aa gpumem watchdog

Evidence: `go version -m` shows mod (devel), tags spout,vr, no version stamp,
vcs.revision = rave-suite superproject sha; no [gpumem] line and no
"route decode telemetry" line in the log after 09-02 14:34.

## Sawtooth mechanism

Without native decode the 4K route runs ffmpeg child -> raw RGBA pipe -> per-frame CPU
Spout SendImage (33 MB/frame); measured ceiling about 13.5 distinct fps at 4K (comments in
internal/mediapipe/decode.go and internal/config/config.go). The jitter buffer paces H.264
AUs by PTS (internal/medialink/jitterbuf.go) and the sink write blocks (router.go runJitter),
so playback lags; overflow / stale catch-up burst-drops to the next keyframe (about 2 s GOP)
= the jump. 720p30 is about 18x less data, so it stays smooth. The native zero-copy publish
logged publishedFps 60 gpuPublish true on 08-20.

## Interop / VRAM mechanism

The stale build churned deck senders per track (Sender.Remove on gate-out), the leak 12b25a7
fixed. A residual grower exists on the nightly lineage too (09-01 build 298 hit 11.1/12 GB):
the media-route republish sender was destroyed and recreated on every route restart, peer flap
or media-child respawn, so every receiver re-registered interop against a new shared texture.
Fixed by keep-alive of republish senders + a 2 min media-child linger (this change set).
The gpumem watchdog had never observed a set before this.

## VR PC VRAM max

gpumem summed dedicated segment CommitLimits per adapter; an APU UMA carve-out counts as
dedicated, so the iGPU was a "real" adapter and, with a budget under 1 GB, the auto threshold
max(1 GB, 8 percent) fired on it at once. Fix: KMTQAITYPE_GETSEGMENTSIZE budget (DXGI parity),
KMTQAITYPE_ADAPTERTYPE flags (drop software / indirect / paravirtualized, mark
HybridIntegrated), primary = largest non-integrated budget, watchdog arms only on the primary,
`[gpu memory]` section in ctl perf / ctl remote-perf.

## Forensics that worked

`go version -m <exe>` (mind the superproject-sha trap); decimal vs hex winErr in the interop
WARN dates a binary before/after 12b25a7; the Resolume log line "another instance is already
running" marks the post-dialog restart; `ctl remote-logs <filter>` reads the peer's log.

## Verify next set

grep `route decode telemetry` for in:3840x2160 -> decode:native gpuPublish:true publishedFps
about 60 pubStalledMs:0 restarts:0; no "native MF decode unavailable" line; at most one
"receive sink open" per sender per session; [gpumem] vram curve flat and "vram by process"
names any grower; Resolume shows no dialog; on the VR PC `ctl remote-perf` lists the
RX 7900 XTX as [primary] and the APU as [integrated].

## Process fix

Stamped dev builds (Makefile + CLAUDE.md build row) and scripts/deploy-local.ps1, which
refuses trees without origin/development and backs up the installed exe.

## Dev-on-set-PC: keep the client updatable

The rig is developed on the same PC that runs live sets. A plain dev build stamps `version.FeedURL=""`,
and the in-app self-updater is INERT on an empty FeedURL: `internal/app/app.go:2872` builds
`selfupdate.New(version.FeedURL, version.BuildNum(), version.UpdatePubKey)`, the updater polls
`Feed.Available(ctx)` every `updater.DefaultInterval` (5 min), and `selfupdate.Available` returns
update-available only when `rel.Build > u.current`. Empty FeedURL => no feed => never updates. On
2026-09-02 such a dev exe sat in the install dir and froze the client on a stale build for 5 days.

Arming fix (`scripts/build-local.ps1`, POSIX twin `make build-local`): stamp the dev build with
`FeedURL=<nightly feed>`, `Channel=nightly`, and `Build = the CURRENT nightly build` read from
`<FEED>latest.json` at build time; keep the DEFAULT `UpdatePubKey` (do NOT empty it) so the signed
nightly manifest still verifies against the real release pubkey. Effect: during the session no
published nightly is `> Build`, so the dev build STICKS and is testable; the next pushed nightly
(`Build = current+N`, strictly greater) auto-replaces it on the 5-min interval. Feed unreachable ->
`Build=0` (any nightly then supersedes it) with a warning. Full-feature tags
(`spout vr abletonlink zigdsp zigui zigvr encembed shellembed`) + static MinGW runtime match the
nightly recipe, so the deployed dev build is never the crippled `spout,vr`-only build.

`scripts/deploy-local.ps1` now REFUSES (exit 6) any exe whose `go version -m` lacks the full-feature
markers (`-tags=` must contain `zigui` AND `encembed`) or the arming stamps
(`version.FeedURL=<nightly feed>` AND `version.Channel=nightly`) - exactly the 2026-09-02 mistake. It
also gained `-Build` (build full+armed via build-local.ps1, then deploy) and `-RestoreNightly`
(download `<FEED>latest.json` -> `installer_url`, verify `installer_sha256` with Get-FileHash SHA256
[exit 7 on mismatch], run the installer `/S`, print the manual-restart note). All prior guards stay
(origin/development exit 2, missing exit 1, unstamped exit 3, Resolume/OBS-live exit 4, copy exit 5);
it still never restarts the app.

A live-set mid-update is NOT a new risk: the nightly already auto-updates on the same 5-min interval
whenever a strictly-greater Build appears. Pushes happen at end of session, and the scheduled nightly
fires ~07:00 local (cron `0 5 * * *` = 05:00 UTC), not during a set.
