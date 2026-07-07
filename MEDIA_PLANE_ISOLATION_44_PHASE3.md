# #44 Phase 3 - media-plane subprocess: implementation spec

Isolate medialink + mediaroute + webcam into ONE featurehost child (`rave-mate feature media`).
Phases 1 (interface extraction) + 2 (mem-capped job) are committed. This is the cutover spec.

**Status: BUILT + isolation-verified (commit 8379111).** `mediawire.go` + `feat_media.go` +
`mediaproxy.go` + `SoftwareClock.MirrorNow` + app.go wiring all shipped, behind the
`MediaLink.Subprocess` opt-IN flag (default OFF = in-proc, byte-identical). Enable via
`config.json` → `features.mediaLink.subprocess: true` (or a future Settings toggle).

Single-PC verified live: media child spawns (`feature:media`, listener `proc:media` = truly isolated);
telemetry mirrors to the Peers-tab Media plane UI (clock quality, routes, webcam Instances); killing
the child → host respawns it in ~1s with full state re-seeded from the Init snapshot; daemon + all
other features survive; clean quit tears everything down (0 procs). NOT yet verified: actual cross-PC
frame routing (StartReceive pulling frames) + caps propagation to a peer - needs the two-PC rig. Keep
the default OFF until that passes; this is the subsystem that OOM'd a host and killed Parsec.

## What crosses the boundary (only the Phase-1 interfaces)

`medialink.MediaControl` + `mediaroute.ReceiveControl` + `webcam.CamControl`. Every return type already
JSON-marshals → mirror the whole control surface up as a `mediaTelemetry` snapshot (~1 Hz) so UI polls
are local reads. Frame-bearing internals (`Register*`/`Offer`/`Close` callbacks, Spout handles, raw
`medialink.Frame`) NEVER cross - they stay in-proc INSIDE the child (mediaroute/webcam call the router
directly, same as today).

## Files

- **`internal/featurehost/mediawire.go`** - DONE. Wire contract: `mediaInit` (self/cfg/secrets/codecs/
  syncPeer), parent→child events (`secret`/`syncPeer`/`codecs`/`advert`/`busDown`), child→parent events
  (`telemetry`/`clockOffset`/`busUp`), methods (`startReceive`/`stopReceive`/`command`). Tested.
- **`internal/featurehost/feat_media.go`** - TODO. `init(){ Register("media", …) }`. Runs its OWN
  `eventbus.New(log, self)` (broadcast=nil - no peerlink in the child) so webcam (needs concrete
  `*eventbus.Bus`) and medialink (via a `mediaBus`-style adapter over that bus) share one bus. Builds:
  child `SoftwareClock`; `medialink.New(Options{Self, Bus: adapter, Secrets: pushedSecretStore, Clock,
  Encoder/Decoder: mediapipe.Factories(log), Log})`; `mediaroute.New({Router: router, Cfg, SameHost:
  nil})`; `webcam.New(log, childBus, self, label, camCfg)` + `SetRouter(router)`. `Init` seeds cfg +
  peer secrets. `Start(ctx)`: start all three (none block), then a ~1 Hz loop `rt.Beat()` + emit
  `telemetry` + `clockOffset` (from `router.ClockQuality()`), block on ctx. `HandleEvent`: apply
  secret upsert/drop, syncPeer→`router.SetSyncPeer`, codecs→`router.SetCodecCaps`, advert→
  `router.Advertise`, busDown→inject into child bus. `Handle`: startReceive/stopReceive/command.
- **`internal/featurehost/mediaproxy.go`** - TODO. Three daemon-side proxies over ONE `Host` (name
  "media", `MemLimitMB` set per the media-RSS discipline). Implements the 3 interfaces by forwarding
  (`Start`=Host.Start; `Advertise`/`SetCodecCaps`/`SetSyncPeer`=Host.Send; `StartReceive`/`StopReceive`/
  `Command`=Host.Call; `Stats`/`SyncStats`/`ClockQuality`/`Encoders`/`RemoteVideoSources`/`Receives`/
  `Instances`=local reads off the mirrored `mediaTelemetry` cache). `Init` snapshots cfg + ALL peer
  secrets. `OnReady` re-pushes syncPeer + codecs + all secrets. `OnEvent`: telemetry→cache,
  clockOffset→`mediaClock.SetOffset`, busUp→republish on daemon bus. `OnDown` clears caches.
  `StartReceive` does the same-host guard locally (holds a `sameHost func(peer) bool` dep from app.go,
  since peerMgr is daemon-side) before forwarding.

## The bus bridge (loop-free, dedup-safe) - the tricky part

Child runs a standalone `eventbus.Bus` (broadcast=nil). Bridge a FIXED topic set: medialink
`{advert,offer,answer}` + webcam `{cam.status,cam.cmd}`. NOT `media.tc` - tcPlane stays daemon-side.

Split by the `Local` flag so there is NO echo loop:
- **UP** (child→daemon): child up-forwarder subscribes each topic on the CHILD bus, forwards only
  `e.Local==true` events up (`busUp`). Daemon republishes via `daemonBus.Publish(topic, data)` →
  stamps Origin=self, Local=true, broadcasts to peers.
- **DOWN** (daemon→child): daemon down-forwarder subscribes each topic on the DAEMON bus, forwards only
  `e.Local==false` (remote) events down (`busDown`). Child injects via `childBus.Inbound(origin,
  envelope)` with a bridge-maintained synthetic monotonic seq per origin + a fixed epoch (the daemon
  already deduped/ordered, so monotonic-seq injection always Accepts).

Why loop-free: republished-up events are daemon-Local → the DOWN forwarder (Local==false only) ignores
them. Injected-down events are child-Local==false → the UP forwarder (Local==true only) ignores them.

**Caps gotcha:** webcam calls `childBus.AddCap(CapCam)` → `Advertise()` → broadcast, but the child bus
has broadcast=nil, so CapCam never reaches peers. Fix: the daemon proxy calls
`daemonBus.AddCap(webcam.CapCam)` when the child reports webcam running (add a `camRunning` bool to the
telemetry snapshot and AddCap on the false→true edge), so peer capability discovery still works.

## Clock split (hazard #1)

Router disciplines the CHILD `SoftwareClock` (sync probes live child-side). tcPlane/tcSvc read the
DAEMON `mediaClock`. Child mirrors `ClockQuality().OffsetNs`+Locked up (`clockOffset`); daemon applies
`mediaClock.SetOffset(offsetNs, locked)` (primitive added this phase). Emit on-change + a keepalive so
a stalled child doesn't leave the daemon clock frozen off-domain.

## Secrets (hazard #2)

`SecretProvider.MediaSecret(nodeID)` is per-peer + dynamic. Push each peer's secret on `peerMgr`
connect (`secret` event, Secret set) and drop on disconnect (Secret nil). Re-push the FULL set in
`mediaInit.Secrets` every respawn (Host `Init` re-reads). Miss a re-push → child's AEAD socket silently
fails to key that peer (routes advertise but never connect).

## app.go swap (behind `MediaLink.InProc`, default in-proc)

At ~556-605 / 736-739: if `!cfg.Features.MediaLink.InProc`, construct `mediaProxy`/`mediaRoutesProxy`/
`webcamProxy` instead of the in-proc managers; keep `mediaClock` + `tcPlane` daemon-side; wire the
clock-offset mirror into `mediaClock`; hook `peerMgr` connect/disconnect → per-peer secret push; move
`mediapipe.Factories` into the child. Update the `"peers"`+`"webcam"` module Start/Stop to drive
`proxy.Host().Start/Stop`; set `ui.Services.Media/MediaRoutes/Webcam` (1323-1326) to the proxies;
`TCPlane` stays concrete. `cmd/rave-mate/main.go` needs NO change - `feature` dispatch is generic.

## Verify

Build/vet/test. Then on the two-PC rig with `InProc=false`: media child spawns; kill it →
respawns + re-pushes state; a peer sees this node's advert + CapCam; `StartReceive` pulls frames;
SMPTE TC stays in the media-clock domain (check `ctl tc-status` offset tracks the child). Only flip
the default to subprocess after that passes. NEVER drive a live route on the production machine mid-set.
