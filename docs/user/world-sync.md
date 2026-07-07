# World Sync - feed VRChat worlds without rebuilds

Publish content as GitHub gists that worlds poll via VRChat string loading
(`gist.githubusercontent.com` is on VRChat's trusted-URL allowlist). Change a list in rave-mate
→ the world updates within your refresh interval + ~5 min of gist CDN cache. Research +
verified platform facts: `WORLD_INTEGRATIONS_RESEARCH.md`.

## Setup

1. Settings → Integrations → **World Sync**: enable + link GitHub (device code, or paste a
   classic PAT with only the `gist` scope). Token is sealed at rest.
2. Link VRChat (needed for the friends browser + group-role expansion).
3. Manage everything on the **Worlds** tab.

## Permission lists

Whitelists for world components (video player access etc.). Entries:
- **Users** - pick from your friends (searchable) or type an exact display name.
- **Group roles** - pick one of your groups (or search any group; favorites supported), then a
  role or "All members".

Each list publishes ONE gist with two files:
- `allow.txt` - display names, one per line → VideoTXL Remote Whitelist (newline mode), ProTV-
  style custom auth plugins, generic loaders.
- `allow.json` - `{"users":[…]}` → VideoTXL JSON mode (array path `users`).

Copy the world URL from the list card into the component (or let the Unity plugin wire it).

**Privacy - read before granting roles**: VRChat worlds cannot check group membership at
runtime, so rave-mate expands a role to its CURRENT member display names and publishes them in
the gist. Gists are unlisted but public to anyone with the URL. Only display names are ever
published, never user ids. While rave-mate runs, the refresher re-expands periodically - people
joining/leaving the role gain/lose access automatically. Expansion of groups you're not in
works only where the member list is public; on visibility loss the last good expansion is kept
(your list never silently empties).

## Display channels

- **Posters**: billboard slots (caption + link + image URL). Images load through VRChat's
  SEPARATE image allowlist - the URL must be on an allowlisted host (`i.imgur.com`, `i.ibb.co`,
  `*.github.io`, …); the UI warns otherwise. Text-only posters always work.
- **Events**: your upcoming rave.page events (title + date) for an events board.
- **Now playing**: while you're live, the audible artist/track + your rave.page link, written
  at most once a minute. Worlds lag 1–6 min (publish cadence + CDN) - it's a vibe card, not a
  clock.

## Unity plugin

Worlds tab → Unity projects → **Write source URLs** drops
`Assets/rave.page/WorldSync/sources.json` into your project. In Unity:
**Tools → rave.page → World Sync** lists the feeds, copies URLs, and wires them into a selected
VideoTXL Remote Whitelist or the included UdonSharp reader prefabs (PosterBoard, EventsBoard,
NowPlayingCard). Prefab **images are build-time slots** - VRChat URLs can't be constructed at
runtime; gists drive all text dynamically.

## Rate limits & etiquette

Refresher: default 10 min (± jitter), diff-only writes (unchanged content = zero API calls),
now-playing floor 60 s. Well inside GitHub's limits. VRChat member paging is throttled.
