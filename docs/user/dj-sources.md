# DJ sources & the session hub

rave-mate fuses every enabled source into ONE live "what's playing" state (the Live tab). Each
source is a Settings card; enable only what you run.

## Sources

| Source | How it works | Caveats |
|---|---|---|
| **Traktor Pro 4** | Traktor's broadcast/metadata hits a local HTTP listener (port 8080). One-click controller-mapping manager included. | Needs the Traktor-side mapping active; see the card's setup help |
| **Traktor NML** | Reads collection/history files for metadata + post-set history reconcile | File-based: history lands when Traktor writes it (on close) |
| **Pioneer Pro DJ Link** | Listens on the CDJ/XDJ LAN protocol for live deck state | Same L2 network as the players required |
| **Serato** | Watches the `_Serato_` History sessions | Now-playing granularity = Serato's session writes |
| **VirtualDJ** | NetCtl/OS2L + tracklist file | OS2L advertised via mDNS; VDJ connects automatically |
| **Rekordbox** | DB-poll + memory-read for live now-playing | Memory-read is version-sensitive; DB-poll is the safe default |
| **MIDI-in** | A MIDI source (Denon stock map or custom CC map) drives deck/fader state | Needs a (virtual) MIDI port; use MIDI learn in Keybinds for customs |

## The merger

Sources emit normalized observations; the hub fuses them **per-field by priority + freshness
(TTL)** - e.g. Pro DJ Link fader + Serato track title can coexist. The Session settings page
shows source priorities and lets you reorder them. "Now playing" = the audible deck (playing +
highest fader, stale data ignored after 2 min).

## Feeding things

The unified state feeds: overlays, the now-playing file sink (OBS text source), the recorder,
the rave.page stream publisher, Twitch title variables, and World Sync's now-playing card.
