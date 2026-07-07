# VRChat link

Client-side VRChat account link: login + 2FA, sealed session, realtime presence via
the pipeline socket, and an opt-in uplink that vaults the session on rave.page. Port of
the web `app/src/vrchat/` flow to idiomatic Go - but the web app proxies VRChat through
rave.page (`/vrc-proxy`), whereas rave-mate talks to `api.vrchat.cloud` **directly** from
the user's machine (no server in the loop unless they opt into the uplink).

## Pieces

| pkg / file | role |
|---|---|
| `internal/vrchat/client.go` | stdlib `net/http` client vs `api.vrchat.cloud/api/1`: Basic-auth login, 2FA verify, `auth`+`twoFactorAuth` cookie capture/resume, current-user, logout |
| `internal/vrchat/store.go` | sealed session at rest (`secureseal` → `vrchat.bin`); cookies only, never credentials, never plaintext |
| `internal/vrchat/manager.go` | account state machine + persistence + change/unlink hooks |
| `internal/vrchat/pipeline.go` | `wss://pipeline.vrchat.cloud` receiver (`coder/websocket`) |
| `internal/featurehost/feat_vrchat.go` | child process hosting the pipeline (crash-isolated) |
| `internal/featurehost/vrchatproxy.go` | daemon-side status mirror + event fan-out + token push |
| `internal/api/client.go` | opt-in uplink: `StoreVrchatToken` / `TestVrchatConnection` / `DeleteVrchatCredentials` |
| `internal/ui/view_settings.go` | `vrchatCard` - sign-in form, 2FA, unlink, status |
| `internal/vrchat/profile.go` | `UpdateStatus`/`UpdateBio` (PUT /users/{id}) + Manager wrappers - see `FLIPBOOK_SUMMARY.md` |
| `internal/ui/view_vrchat.go` | VRChat **tab**: status/bio editor + animated-emoji generator - see `FLIPBOOK_SUMMARY.md` |
| `internal/flipbook/` | video/GIF → VRChat emoji sprite sheet - see `FLIPBOOK_SUMMARY.md` |

## Auth flow

1. **Login** - `GET /auth/user` with `Authorization: Basic base64(encodeURIComponent(user):encodeURIComponent(pass))`.
   `encodeURIComponent` parity matters (matches the web client + VRChat's spec). The
   password is used for this one request and never stored or logged.
2. **2FA** - if the response carries `requiresTwoFactorAuth`, the `auth` cookie is already
   captured. `POST /auth/twofactorauth/{totp|otp|emailotp}/verify` with `{code}` →
   `twoFactorAuth` cookie.
3. **Session** - subsequent calls send both cookies. `GET /auth/user` doubles as
   validation + resume check.

Required identifying `User-Agent` on every request (`rave-mate/1.0 (…; contact@rave.page)`)
per VRChat API policy.

## State machine (`Manager`)

`logged-out → awaiting-2FA → logged-in`. `Resume` restores the sealed cookies and validates
them against `/auth/user`: expired/2FA-stale → wipe + drop; network error → keep the blob
for a later retry. Session persisted **only** when `RememberSession` is on. `Unlink` logs
out server-side + wipes. `OnChange` (multi-listener) drives the UI and the pipeline token;
`OnUnlink` fires only on explicit user unlink (not session expiry).

## Pipeline (child process)

`feat_vrchat` runs `wss://pipeline.vrchat.cloud/?authToken=<auth>` in a `featurehost`
child - a WS-parser fault kills only the child, the Host restarts it. The token rides the
query string and is never logged. Behaviour:

- No token → idle until an `auth` parent event delivers one.
- Connect → forward `{type,content}` frames (content is JSON-in-a-string, decoded) to the
  daemon as `pipe` events → fan out to subscribers.
- Drop / reject → 15s backoff redial (VRChat rate-limits aggressively). An `{err:…}` frame
  = token rejected.
- Login/logout pushes a new token via `VrchatProxy.SetAuth` → child hot-swaps without a
  respawn; a respawn re-reads the current cookie from the manager.

## Opt-in rave.page uplink

Off by default. When **Share session with rave.page** is on, login vaults the session via
`POST /auth/vrchat/token` (server-side group/event features); explicit unlink or toggle-off
calls `DELETE /auth/vrchat/credentials`. Gated on `cfg.Features.VRChat.Uplink` **and** a
rave.page sign-in - nothing leaves the machine otherwise. The rave.page bearer + the VRChat
cookies are never logged (redacted `loggingDoer`).

Generated ops added to `tools/genapi` `includeOps`: `storeVrchatToken`,
`testVrchatConnection`, `deleteVrchatCredentials`.

## Config

```jsonc
"vrchat": {
  "enabled": false,          // module on/off (pipeline child)
  "rememberSession": true,   // seal session at rest + auto-resume
  "uplink": false            // share session token with rave.page (opt-in)
}
```

## Security invariants

- Credentials touch only the VRChat login request; never persisted, never logged.
- Session cookies sealed at rest (DPAPI); if no OS secret store, **not** written to disk.
- Pipeline auth token only in the WS query string - never in a log line.
- Uplink is opt-in + sign-in gated; redacted request logging throughout.

## Not ported

The web `VrchatService` group/friend/world/notification surface (50+ ops) is rave.page
server-side; rave-mate only needs login + session + pipeline + the 3 uplink ops. Add more
to `includeOps` + the `api` adapter if a desktop feature needs them.
