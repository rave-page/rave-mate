# Multi-PC (peer link)

Run rave-mate on several machines (DJ PC, VR PC, stream PC) and they cooperate over the LAN -
no cloud in the path.

## Pairing

Peers find each other via mDNS (`_ravemate._tcp`). Pair once with a 6-digit SAS code shown on
both screens (man-in-the-middle-proof); afterwards they reconnect silently. Each instance has a
stable Ed25519 identity; all control traffic is authenticated + encrypted.

## What flows over the link

- **Live DJ data**: the VR/stream PC sees every playing deck on the DJ PC - artist, title,
  BPM, key, elapsed, fader level where the source provides one - with the audible deck
  highlighted (peer bridge). Works with any live DJ source (Traktor, Serato, VirtualDJ,
  Rekordbox).
- **Remote control**: drive a paired instance's automations, library, and file browser from
  your seat (Peers tab → Remote).
- **Remote Library** (see below): the full Library tab of a paired instance, live-mirrored
  and remote-driven.
- **Media routes**: send video/audio between instances (LAN media plane) with clock sync.
- **OBS control**: see + start/stop any instance's OBS stream/record from one cockpit.
- **Twitch/eventbus**: one instance holds the Twitch connection; others render its chat/alerts
  (e.g. in VR).
- **VRM avatars, library sync** and file transfer between paired boxes.

## Remote Library

Library tab → "Controlling" switcher → pick a paired peer. The peer renders its own Library
in a headless session (its visible window is untouched) and streams the live view here;
every click/key/edit is sent back and **executes on the peer** - beatgrid analysis (gridfix),
cue/drop edits, tag writes, transcodes, playlist changes all read/write the peer's files and
database with the peer's CPU/GPU. Same layout, same features as sitting at that machine.

- Audio auditions play on the **peer's** audio device (by design - commands execute there).
- Covers/thumbnails stream through a token-guarded media proxy; embedded video previews may
  be unavailable remotely. Native file dialogs are refused (they'd pop on the peer's desktop).
- Link drop mid-session: the banner turns amber and the view freezes; the peer cleans its
  session up automatically. Reconnect resumes.
- Transport: the `remoteui` sub-channel of the same Ed25519-authenticated, per-frame-MAC'd
  peer link. Nothing leaves the LAN.

## Environment overrides (rigs + multicast-less networks)

- `RAVE_MATE_PEER_BIND` - peer-listener bind host (default all interfaces). Loopback bind
  (`127.0.0.1`) skips mDNS (not LAN-reachable) - pair with a seed.
- `RAVE_MATE_PEER_PORTS` - comma-separated listener ports (default 47631-47635); isolated
  test instances must not race a real one.
- `RAVE_MATE_PEER_SEED` - comma-separated `host:port` peers to dial directly (5s retry tick);
  discovery-free pairing for same-host rigs or networks without multicast.

## Timecode

One instance is the house-clock master (election on the media plane) and can emit SMPTE
everywhere: LTC audio out, MIDI Timecode, Art-Net TimeCode - lighting desks and video rigs
chase the same clock. OBS media sources can chase it too.

## App groups

Define named sets of applications (your DJ rig: Traktor + OBS + VRChat + …). After a crash (or
on demand, incl. from a peer/VR overlay) rave-mate relaunches everything not running - fast
recovery mid-set.

## Caveats

- Same L2 network required for discovery; pairing is per-machine-pair.
- Peer file/media operations respect the remote instance's configured roots - you browse what
  it exposes, not the whole disk.
