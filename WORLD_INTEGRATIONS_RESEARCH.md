# VRChat world integrations - research + build order (task #32 + display-prefab addition)

Verified 2026-07 against live docs. Feature = **World Sync**: rave-mate publishes GitHub gists
(permission lists, posters, events, now-playing) that VRChat worlds poll via string loading; a
`page.rave.mate` Unity-plugin extension wires prefab/component URLs automatically.

## 1. VRChat remote string loading (the transport) - VERIFIED

Source: [String Loading](https://creators.vrchat.com/worlds/udon/string-loading/) (creators.vrchat.com).

- **Trusted-URL allowlist includes `gist.githubusercontent.com`** ✔ (also `*.github.io`,
  `pastebin.com`, `*.disbridge.com`, `*.vrcdn.cloud`). `raw.githubusercontent.com` is **NOT**
  allowlisted - gists are the right vehicle, plain repo raw URLs are not.
- Untrusted hosts require each player to enable "Allow Untrusted URLs" - unacceptable for a
  permission system; stay on the allowlist.
- Limits: 1 string download / 5 s per client (excess queued, random order, queue cap 1000);
  100 MB max. Our lists are KBs - irrelevant except: don't ship many separate gists per world,
  each costs a 5 s slot on world load.
- API: `VRCStringDownloader.LoadUrl(VRCUrl, IUdonEventReceiver)` →
  `OnStringLoadSuccess/OnStringLoadError(IVRCStringDownload)`; `Result` (UTF-8 string),
  `ResultBytes`, `ErrorCode`.
- Gist raw URL `https://gist.githubusercontent.com/{user}/{gist_id}/raw/{file}` (no revision SHA)
  serves the latest revision but is **CDN-cached ~5 min**
  ([community discussion](https://github.com/orgs/community/discussions/46691)). Consequence:
  world-side propagation latency = our publish interval + ≤5 min. Fine for perms/posters/events;
  now-playing shows "live-ish" (1–6 min lag) - documented, acceptable.

## 2. Permission systems in worlds (targets) - VERIFIED

| System | Remote ingest | List format | Our output |
|---|---|---|---|
| **VideoTXL** (AccessTXL `Remote Whitelist`) | yes - Remote String URL via VRC string loading; Refresh on Start / Periodic Refresh (secs) / synced reload ([docs](https://vrctxl.github.io/Docs/docs/access-txl/whitelist-sources/remote-whitelist)) | **newline display names** OR JSON with configurable array path (`embedded/names`) + entry path | newline displayNames (works verbatim) |
| **ProTV 3.x** (ArchiTechAnon) | **no built-in remote list** - `TVManagedWhitelist` is an in-editor/in-world username list; extension point = custom `TVAuthPlugin` (`_IsAuthorizedUser()`/`_IsSuperUser()`) ([docs](https://protv.dev/guides/auth-plugins)) | in-scene arrays | newline displayNames + our own tiny `TVAuthPlugin` reader (C#, unverified) is the path; generic format still usable by hand-rolled loaders |
| **USharpVideo** | no remote ingest - editor-time allowed-users array + domain whitelist ([repo](https://github.com/MerlinVR/USharpVideo)) | in-scene array | not remotely feedable; document only |
| Generic/hand-rolled | whatever the world's Udon parses | usually newline names or JSON | ship BOTH: newline + JSON envelope |

Start targets: **VideoTXL (newline)** + **generic newline-displayName** + **JSON envelope** (one
format doubles for VideoTXL-JSON mode via array path `users`).

## 3. VRChat Groups API + runtime membership - VERIFIED

Endpoints (api.vrchat.cloud/api/1, session-cookie auth - our `internal/vrchat` client):

- `GET /users/{userId}/groups` - a user's (public) groups; own id → my groups. ([ref](https://vrchat.community/reference/get-user-groups))
- `GET /groups?query=&n=&offset=` - search by name/shortCode. ([ref](https://vrchat.community/reference/search-groups))
- `GET /groups/{groupId}` - group detail incl. `myMember`.
- `GET /groups/{groupId}/roles` - role list (id, name, permissions, isManagementRole…); needs only
  auth + valid group id. ([ref](https://vrchat.community/reference/get-group-roles))
- `GET /groups/{groupId}/members?n=&offset=&roleId=` - paginated members incl. `roleIds`;
  `roleId` filter server-side. ([ref](https://vrchat.community/reference/get-group-members))

**Runtime verdict: Udon exposes NO group-membership/role API.** `VRCPlayerApi` has no group
surface; it's an open feature request
([GetInstanceGroupRole](https://feedback.vrchat.com/udon/p/getinstancegrouprole-method-for-vrcplayerapi),
[ask.vrchat.com thread](https://ask.vrchat.com/t/feature-request-give-udon-u-access-to-instance-information-group-information/27923)).
⇒ role-based permissions MUST be materialized outside the world: rave-mate expands
group-role → current member displayNames into the gist periodically. **Privacy/consent framing**:
that publishes member display names to an unlisted-but-public gist. Mitigations: publish
displayNames only (no user ids in world-facing output), publish only roles the operator explicitly
selects, note in UI that members of the chosen role become publicly listed.

**Grants to groups you are NOT in**: role list = readable for any valid group id (auth only).
Member list = visibility-gated: works for public groups / members with public visibility; private
groups or hidden members return 403/404/empty pages. So foreign-group role entries are allowed in
the UI but expansion is best-effort - refresher records per-entry expansion errors and keeps the
last good expansion instead of silently emptying the list.

**VRChat API etiquette**: no published rate limits; identifying User-Agent required (client
already sends it). Refresher paces page fetches (100/page, ≥1 s between pages) and runs on
minutes-scale intervals.

## 4. GitHub: device flow + gists - VERIFIED

Source: [Authorizing OAuth apps - device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow), [Gists REST](https://docs.github.com/en/rest/gists/gists).

- Device flow: `POST https://github.com/login/device/code` (`client_id`, `scope`, Accept: json) →
  `device_code` (40 ch), `user_code` (XXXX-XXXX), `verification_uri`
  (https://github.com/login/device), `expires_in` (900 s), `interval` (min 5 s).
  Poll `POST https://github.com/login/oauth/access_token` with
  `grant_type=urn:ietf:params:oauth:grant-type:device_code`; errors: `authorization_pending`,
  `slow_down` (+5 s), `expired_token`, `access_denied`, `device_flow_disabled`. Cap: 50 token
  submissions/h/app. Device flow must be enabled on the OAuth app; **client id is public**
  (Twitch-style bundled default; user-overridable). OAuth-app tokens don't expire by default →
  no refresh machinery. Fallback: paste a classic PAT with `gist` scope (works without any app).
- Gists: `POST /gists` (create, `public:false` = secret), `PATCH /gists/{id}` (update files),
  `GET /gists/{id}`; scope `gist`; api.github.com, `Accept: application/vnd.github+json`.
  **Secret gists are unlisted, not private** - anyone with the URL can read (that's exactly what
  the world needs; also why we keep user ids out of published output).
- Rate limits: 5000 req/h authed core; secondary limit ~80 content-writes/min. Our budget:
  diff-only writes; now-playing throttled to ≥60 s; everything else minutes-scale. Trivial.

## 5. Display prefabs (scope addition) - image path VERIFIED

Source: [Image Loading](https://creators.vrchat.com/worlds/udon/image-loading/).

- `VRCImageDownloader` has its **own allowlist** - exact hosts: `*.disbridge.com`,
  `dl.dropbox.com`, `dl.dropboxusercontent.com`, `*.github.io`, `images4.imagebam.com`,
  `i.ibb.co`, `images2.imgbox.com`, `i.imgur.com`, `i.postimg.cc`, `i.redd.it`, `pbs.twimg.com`,
  `*.vrcdn.cloud`, `assets.vrchat.com`, `i.ytimg.com`. Limits: 1 image/5 s, ≤2048×2048, ≤32 MB.
- **`gist.githubusercontent.com` is NOT image-allowlisted; neither is rave.page.** Honest
  constraint: gists carry the *metadata* (JSON: image URL + caption + link), and the image URL
  itself must live on an allowlisted host. Practical options, in order:
  1. `*.github.io` - a GitHub Pages site (even a repo the user owns) serves images; same GitHub
     account we already link. Not automated in v1.
  2. `i.imgur.com` / `i.ibb.co` etc. - user pastes an already-hosted image URL.
  3. rave.page could only work by serving media under a `*.github.io`-style allowlisted domain it
     doesn't own → **not possible**; getting rave.page onto VRChat's allowlist is a VRChat-side
     ask, out of our control. Documented, not assumed.
  ⇒ v1: prefab JSON carries `img` (must be on an image-allowlisted host - UI validates + warns),
  `caption`, `link`. Text-only rendering when `img` is empty/untrusted.
- **Runtime-URL constraint (verified)**: `VRCUrl` **cannot be constructed at runtime** in Udon -
  URLs are baked at build time or come from a `VRCUrlInputField`
  ([external URLs doc](https://creators.vrchat.com/worlds/udon/external-urls/),
  [feedback thread](https://feedback.vrchat.com/udon/p/allow-construction-of-vrcurl-at-runtime)).
  Consequence: a gist can NOT point the prefab at a new image URL after build. Prefab design:
  image `VRCUrl` slots are pre-wired in the inspector; the gist dynamically selects a slot index
  + supplies all TEXT (captions, links, events, track info). Fully dynamic = text; images =
  build-time slots (swap content by re-uploading to the same image URL, e.g. a github.io path).

## 6. Now-playing redaction dependency

Now-playing channel consumes the session layer's **redacted** unified output (ID-redaction feature
in progress by another agent), never raw deck state. Until that lands, the publisher takes a
`func() NowPlaying` provider injected at wiring time; app wiring currently feeds it from the
aggregator's public snapshot and MUST be switched to the redacted surface when it exists.
Cadence: min 60 s between gist writes, diff-only, only while a session is live.

## Build order (phases)

1. **Research doc** (this file).
2. **`internal/github`** - device-flow auth (sealed token `github.bin` via shared/secureseal, PAT
   fallback, Twitch-pattern) + stdlib gist CRUD client. Tests: httptest.
3. **`internal/vrcperm`** - list model (user + group-role entries), output formats (newline /
   VideoTXL / JSON envelope), group-role expander over `internal/vrchat` (+ new groups/friends
   endpoints there), gist publisher (**one gist per list**, one file per format - a world needs a
   stable per-list URL; per-world bundling would couple unrelated lists) + display channels
   (posters/events/nowplaying JSON), periodic refresher (interval + jitter, diff-only writes).
   Tests: formats, expander, refresher diffing, fake HTTP.
4. **Config + app wiring** - `WorldSync` feature (configVersion → 22), Services entry, module
   lifecycle.
5. **UI** - Settings: GitHub card (code display + poll). New "Worlds" view: permission lists
   (add/remove entries via friends browser + group/role browser + group search + favorites),
   per-list gist URL + last-publish state; posters/events/now-playing channel cards.
6. **Unity plugin** - file-based handoff (`Assets/rave.page/WorldSync/sources.json` written by
   rave-mate, mirroring the motion-take flow) + `PERM-SOURCES` ctl query + editor window that
   wires VideoTXL Remote String URL / copies URLs; minimal UdonSharp runtime readers (poster /
   events / now-playing). **C# unverified - not compiled here.**
