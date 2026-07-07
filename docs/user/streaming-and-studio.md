# Streaming & studio

## rave.page live publishing

With Stream Bridge on and your account signed in, going live publishes your merged session
state (deck/track updates + heartbeat) to your rave.page stream - listeners see the tracklist
in real time. Start/end from the Live tab cockpit.

## Local Studio channel (web app ↔ desktop)

The rave.page web app can connect to your rave-mate over a loopback WebSocket (ports
47615-47619) for local-file powers the browser doesn't have: file browse, media pick, transcode
jobs. Security: mutual identity check against your login, per-frame HMAC over an ECDH session
key, strict origin allowlist - only YOUR browser session on YOUR machine can talk to it.

## RTSP performer chain

Serves a local `rtspt://` stream (ffmpeg-encoded) that VRChat AVPro video players accept - feed
a performer camera or your overlay video into a VRChat world from the same box, no external
relay. Configure source + encoder in Settings → RTSP.

## Caveats

- Stream Bridge needs at least one DJ source live; check the Live tab signal strip.
- Studio channel pairs to the signed-in account - sign in on both web and desktop.
