# Twitch worker + persistent chat log

Problem: chat/alerts only existed while the webui Twitch tab buffered them in RAM from app
start; a set played without the tab (or across a restart) had no readable chat.

## Architecture (feature/twitch-worker)

- **`feature twitch` child** (`featurehost/feat_twitch.go`): owns `twitch.Manager` - auth
  (Device Code Flow, sealed `twitch.bin`), EventSub WS, viewer/chatter polling, helix ops.
  Streams `ev`/`viewers`/`chatters`/`state` up; RPCs: `auth.start|poll|logout`,
  `chat.send|moderate`, `title.apply|set`, `categories.search`; event `kick`.
- **`TwitchProxy`** (`featurehost/twitchproxy.go`): daemon-side surface (SignedIn, Self,
  `Auth()` device flow, Kick, SendChat, Moderate, ApplyTitlePreset, SetTitle,
  SearchCategories, SetOnEvent). Republishes child events onto the eventbus (webui, Fyne,
  vroverlay, vrctools, peer mesh unchanged), syncs `CapTwitch` advert from `state`,
  subscribes `TopicSendChat`/`TopicModerate` and forwards into the child (owner-side peer
  serving), routes ops to the owning peer when not connected locally (old Manager parity).
- **Auth ownership**: child. Refresh tokens rotate where helix lives → `twitch.bin` stays
  single-writer. Daemon proxies the device-flow RPCs; sign-in state mirrored via `state`
  events. Consequence: sign-in needs the feature toggle ON (child spawned) - the settings
  card already couples them.
- **`twitch.ChatLog`** (`internal/twitch/chatlog.go`): append-only JSONL per local day,
  `<config>/twitch-chat/YYYY-MM-DD.jsonl`, line = `twitch.Event` (TS ms). Caps: 10 MB/day
  (drop-newest, warn once), 14 days AND 50 MB total (oldest-file prune on Open + day
  rotation). Written daemon-side from a bus subscription → also captures paired-peer
  events. Never closed (process-lifetime; single-line writes).
- **webui**: `subscribeTwitch` seeds `twitchRows` from `ChatLog.Recent(250)` with
  `— YYYY-MM-DD —` separators (`.tw-sep`); live events append as before.
- **Config v35**: removed dead `Twitch.AutoConnect` (never read; an opt-out would recreate
  the "missed chat" bug with no UI to unstick it). Old configs load fine.

## Tests

- `chatlog_test.go`: append/recent order, cross-day seed, reopen persistence, day-cap
  drop-newest, age + total-size prune.
- `twitch_e2e_test.go`: real child (isolated `RAVE_MATE_CONFIG_DIR`), signed-out mirror,
  no-peer routing error, not-connected title error, logout round-trip, unknown method.

i18n: no new strings (date separator is language-neutral).

Commits: 68b597e (chatlog), e7dae93 (child+proxy), 9b6d072 (wiring+config+seed).
Live ctl verify pending (coordinator, post-merge).
