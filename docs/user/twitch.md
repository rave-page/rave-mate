# Twitch

## Sign-in

Settings → Twitch → Sign in: a device code opens twitch.tv/activate - no password typed into
rave-mate, no client secret. Tokens are sealed at rest. Scopes: title control, chat read/write,
follows, subs, bits, moderation.

## Twitch tab

- **Chat**: live chat with send box.
- **Alerts**: follows / subs / cheers stream in over one EventSub socket; alerts also publish
  on the internal event bus - VR overlays and paired instances render them.
- **Title presets**: reusable stream-title templates with `{variables}` you fill on apply
  (`{genre} set @ {club}`), optional category set alongside.
- **Moderation**: timeout/ban + message delete from the chat list.

## Speech-to-text → chat

Local Whisper dictation (nothing leaves your machine until you send): push a keybind (desktop
or VR controller), speak, preview, send to chat - or auto-submit on silence. Configure model +
device in Settings → STT; drive it via Keybinds.

## Multi-PC

The instance holding the Twitch connection serves chat/alerts to paired instances - your VR PC
shows chat in-headset while the stream PC owns the connection (see multi-pc.md).

## Caveats

- Device-code sign-in expires after ~15 min - restart it if you waited too long.
- STT needs a Whisper model download on first use (size selectable).
