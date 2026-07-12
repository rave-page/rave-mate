# Multi-PC (peer link)

Run rave-mate on several machines (DJ PC, VR PC, stream PC) and they cooperate. On the LAN they
talk directly, with no cloud in the path. Off the LAN they can meet through your rave.page
account - see [Reaching a PC from anywhere](#reaching-a-pc-from-anywhere-account-bridge).

## Pairing

Peers find each other via mDNS (`_ravemate._tcp`). Pair once with a 6-digit SAS code shown on
both screens (man-in-the-middle-proof); afterwards they reconnect silently. Each instance has a
stable Ed25519 identity, and every control frame is signed with a key derived from the pairing,
so nothing on your LAN can forge or tamper with a command.

On the LAN, control frames are authenticated but sent in the clear (a deliberate tradeoff on a
network you own). Over the account bridge they are additionally **encrypted end-to-end** - see
below.

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

- Audio auditioning is disabled remotely: nothing plays out loud on the peer, and its own
  player/inspector state is never touched by your session.
- **Prepare cues is local-first**: opening the cue editor on a peer track copies the audio to
  this computer (progress dialog; copies are cached, so a re-open is instant) and the full
  editor runs HERE - waveform, beat-walking, drop/cue edits and audition audio all local,
  surviving a link drop mid-edit. **Save cues to \<peer\>** writes the result back into the
  peer's library and file tags; if the peer's cue data changed underneath you, a conflict
  dialog offers overwrite / re-fetch / cancel. After a save you can push the cues into the DJ
  software installed on the peer, same as the local write-back.
- **Playlist/folder cue-prep is local-first too**: “Prepare cues” on a peer playlist or folder
  checks each track for a beatgrid (skipped ones are counted), then walks the eligible list
  one track at a time. ↑/↓ or the Prev/Next buttons move through the set - unsaved edits ask
  before they are discarded - and the next track is quietly pre-copied in the background, so
  moving on is usually instant. Saving stays per-track.
- Covers/thumbnails stream through a token-guarded media proxy; embedded video previews may
  be unavailable remotely. Native file dialogs - and anything else that would open on the
  peer's desktop (file/folder openers, external apps, browser sign-ins) - are refused.
- Link drop mid-session: the banner turns amber and the view freezes; the peer cleans its
  session up automatically. Reconnect resumes.
- Transport: the `remoteui` sub-channel of the same Ed25519-authenticated, per-frame-MAC'd
  peer link. Nothing leaves the LAN.

## Reaching a PC from anywhere (account bridge)

The LAN peer link only reaches machines on the same network. The **rave.page account bridge**
reaches this PC from anywhere - a laptop on tour, the rave.page web app on your phone - by
meeting through your rave.page account.

Turn it on in **Settings → Streaming & remote → rave.page account bridge**. It needs you to be
signed in; until then it simply idles.

### rave.page cannot read any of it

rave.page is only a meeting point. It knows which of your devices are online and passes sealed
blobs between them - it cannot open them.

- The two devices run their own key exchange and prove their identities to each other with the
  long-term Ed25519 keys they generated locally.
- Everything after that is encrypted end-to-end (AES-256-GCM) with keys the server never sees.
- The server sees ciphertext, byte counts and "device A is talking to device B". Never a track
  name, a command, or an access token.

### The access gate is an authenticator app, not your password

Getting in is gated by a TOTP authenticator (Aegis, 1Password, Google Authenticator, …) linked
to **this PC**. The secret is generated here, sealed here (Windows DPAPI), and checked here.
rave.page never sees it, so a breach of rave.page cannot let anyone into your machine.

1. **Link an authenticator.** Scan the code (or type the secret) into your app, then type the
   6-digit code back to confirm. Nothing is armed until you confirm - a mis-scan can't lock you
   out.
2. **First connect from a device** needs a valid 6-digit code. That proves you're holding the
   authenticator, which authorizes the pairing - the remote equivalent of comparing SAS digits
   on two screens.
3. **After that**, the devices remember each other's keys and this PC hands the caller a key of
   its own, so you aren't typing codes all day. It rotates on every use, expires after **7 days
   idle**, and you can revoke it any time from the same screen.

Two things worth knowing:

- The code you *confirm* the enrolment with is **used up**. If you pair a device within the same
  30 seconds, use the **next** code your app shows.
- On macOS/Linux there is no OS secret store rave-mate can use yet, so the enrolment is held in
  memory and is lost when rave-mate quits (it is never written to disk unencrypted). The card
  says so. Windows persists it via DPAPI.

### Local Studio from anywhere

Normally the rave.page web app can only drive this machine from a browser running **on it**
(the Local Studio channel is a loopback connection). Switch on **"Serve Local Studio over the
bridge"** and the same channel is offered over the account bridge, so the web app can drive this
PC from any browser you're signed in on.

The protocol isn't weakened for this - same key exchange, same per-message signature, same check
that both ends belong to your account. The bridge adds an encrypted tunnel *underneath* it,
built before Local Studio says a word, so the access token in its handshake never crosses the
relay in the clear.

Leave it off if you only ever use the web app on this PC.

### Caveats

- Both devices must be signed in to the **same** rave.page account.
- Caps: 16 devices and 16 links per account.
- The relay is fire-and-forget: rave-mate re-sends anything that gets lost, so a flaky link
  costs latency, never correctness. Large transfers still belong on the LAN.

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
