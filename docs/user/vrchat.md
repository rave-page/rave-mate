# VRChat

## Account link

Settings → VRChat: username/password go straight to VRChat's API (Basic auth + 2FA) - rave-mate
keeps only the resulting session cookie, sealed at rest; your password is never stored or sent
anywhere else. Optional (off by default) uplink shares the session with rave.page for
server-side event features - strictly opt-in.

## VRChat tab

- **Status & bio**: presence + status text + bio editor with live character counters, presets,
  and `{placeholders}` that auto-resolve from your upcoming rave.page events
  (`{next_event}`, `{next_event_date}`).
- **Emotes**: animated-emoji sprite-sheet generator - pick a video/GIF, choose frame tier/FPS/
  trim/crop/ping-pong, get a VRChat-ready 1024² sheet (upload on the VRChat site; VRC+ needed).

## VRC tools

- **Screenshot organizer**: sorts VRChat photos by world/event (uses the log-derived location
  timeline), with viewer.
- **Camera paths**: auto-backup + restore of VRChat camera paths - survive crashes mid-set;
  named per world.
- **Location timeline**: parsed from the VRChat log; also drives per-world overlay layouts
  (auto-apply your layout when you enter a known world).

## World Sync

Feed VRChat worlds from GitHub gists (permission lists, posters, events, now-playing) - see
[world-sync.md](world-sync.md).

## Caveats

- VRChat has no official API guarantee; endpoints can shift. rave-mate sends an identifying
  User-Agent per VRChat's usage policy and paces requests.
- 2FA sessions expire eventually - the card shows when a re-login is needed.
