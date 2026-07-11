# Account bridge (#64) - rave-mate half

Reach rave-mate instances you are NOT sitting at, through the user's rave.page account.
Primary consumer = the web Local Studio driving any linked instance; secondary = mate↔mate WAN
peering. TOTP is the access gate.

Backend contract: `vrbooking/.devnotes/ACCOUNT_BRIDGE_CONTRACT.md` (7 authed endpoints under
`/realtime/bridge/*`, SSE down + POST up, envelope `{sid, seq, kind, payload_b64}`).

## The load-bearing finding

**peerlink's data plane is plaintext + HMAC** (`link.go:15-19`: *"The LAN transport is plaintext
ws://; Phase 3 will add AEAD"*), and the studio protocol is likewise plaintext JSON + HMAC - its
`client-auth` frame carries the raw bearer `accessToken`.

On a LAN that is a considered tradeoff. Pushed over rave.page's relay **as-is**, the server would
see every remote-control command, the entire RemoteUI Library stream, and live bearer tokens -
which makes the "cryptographically blind server" premise of the whole epic worthless.

So the bridge is **not a dumb pipe**. It is an AEAD tunnel:

```
peerlink AKE      ECDH + Ed25519-signed transcript   (public key material - safe in the clear,
                                                      exactly as on the LAN ws:// transport)
     ↓
AEAD upgrade      AES-256-GCM, per-direction HKDF keys from the handshake secret
     ↓
authz gate        TOTP → pinned identities → pairwise token   (inside the encryption)
     ↓
payload           peerlink Link (mate↔mate)  |  studio protocol (web Local Studio)
```

Nothing above the transport is forked. `remotectl` and the RemoteUI Library mirror work over WAN
unchanged because the tunnel hands peerlink a `Conn` it already knows how to use.

## Packages

| Package | Role |
|---|---|
| `internal/totp` | stdlib RFC 6238 (HMAC-SHA1, 6 digits, 30s, ±1 skew, constant-time across the window). Pinned to the RFC 6238 App-B vectors. |
| `internal/authz` | **the transport-agnostic gate.** TOTP enrolment + pairwise trusted-session tokens, over an abstract `Channel` (Send/Recv). Knows nothing about rave.page. |
| `internal/bridge` | relay client (7 endpoints, own SSE parser) + `Conn` (ARQ + fragmentation + AEAD) + `Manager` (presence, rendezvous, demux). |
| `internal/peerlink` (+`gate.go`) | `Upgrader` / `Gated` / `Authorizer` capabilities; `AdoptConn` (join a foreign transport to the peer link) and `Authenticate` (secure tunnel only, for studio). |
| `internal/studio` (+`transport.go`) | `Conn` interface + `ServeConn` - the byte-exact protocol, now transport-free. |

## Transport-agnostic gate (hard requirement)

The trust root is the **instance**, never rave.page. `authz.Gate` runs over any bidirectional
framed `Channel`: the relay today, the LAN peer link, a future direct ip:port dial **with no
rave.page account at all**. rave.page is one transport plugin, not the trust root - nothing in
`internal/authz` calls it, and its tests drive it over an in-memory pipe.

Credentials:
- **totp** - possession of the enrolled authenticator IS the pairing authorization (the
  SAS-compare equivalent when there is no human at the far end). Bootstrap only.
- **token** - a 256-bit pairwise secret minted per caller after TOTP; rotated on every use,
  hard-expired after 7 days idle, revocable from the UI.

## Security invariants held

- **Zero-knowledge**: the TOTP secret is generated, sealed and verified ON THE INSTANCE.
  rave.page never sees it and cannot verify a code. Secrets/codes/tokens/payloads are never
  logged.
- **Tokens hashed at rest** on the issuing side (sha256) - a store leak yields nothing
  presentable. Tokens held *for* other instances are bearer secrets here, so they're sealed
  (DPAPI); memory-only where the OS has no secret store, **never plaintext on disk** (the policy
  `shared/auth` already applies to tokens).
- **Tokens are pairwise**: bound to the peer id the transport already authenticated, so a token
  lifted off one device won't authorize another.
- **TOTP steps are burned** on use - one counter authorizes at most one pairing, killing replay
  inside the ~90s skew window. (Consequence: the confirming code can't also pair a device in the
  same 30s; the UI says so.)
- **TOTP throttled**: 5 fails → exponential lockout to 30 min, per-peer. A 10^6 code space is
  brute-forceable otherwise by anyone who can reach the channel.
- **Uniform "denied" on the wire** - the gate is not an oracle.
- **Fails closed**: an untrusted peer on a gated transport with no authorizer installed is
  refused. Headless/service mode has no human to ask for a code → the dial aborts, never hangs.
- **AEAD before credentials**: the gate runs only after `Upgrade()`. A bearer credential on a
  cleartext relay would go straight to the operator.
- **Studio ordering**: `peerlink.Authenticate` (tunnel) runs BEFORE `studio.ServeConn`, because
  studio's `client-auth` carries a raw bearer token.

## Bounded buffers (repo hard rule)

| Queue | Cap | Policy |
|---|---|---|
| `Conn` send window | 32 chunks **and** 8 MiB | **backpressure** (`Send` blocks) - no silent loss on a control plane |
| `Conn` inbound chunks | 128 chunks | **drop-newest** - the peer's ARQ retransmits; a drop costs latency, never correctness |
| `Conn` reassembly | 8 MiB (`MaxMessage`) | protocol error → close |
| `Conn` delivered messages | 32 | blocks the reassembler → backpressures via the ARQ |
| `Conn` reorder buffer | 128 chunks / 8 MiB | drop → retransmit |
| bridge `Manager` conns | 16 links (server cap) | server-enforced (409) |

Chunk body 192 KiB, inside the relay's 256 KiB decoded-payload cap.

## Relay hazards handled

- **Fire-and-forget** (Redis pub/sub): 202 = *published*, not *delivered*. `Conn` runs its own
  ARQ (per-chunk seq, cumulative ack, RTO with backoff, 10 retries → fail the link). Verified
  under 33% frame loss.
- **No replay on reconnect / `Last-Event-ID` ignored** → presence is re-`GET /sessions`ed after
  every stream break.
- **403 RELAY_NOT_ACCEPTED until mutual accept** → treated as transient; the ARQ absorbs the
  window where one side has accepted and the other hasn't.
- **413 / 429** → size checked client-side; `Retry-After` decoded.
- Bearer in the **Authorization header**, not `?token=` (a token in a URL lands in proxy logs).
  The contract allows both; we take the safe one.

## Verified against the REAL API vs the fake

**Real (deployed `development.api.rave.page`)** - `internal/bridge/zz_live_test.go`, opt-in via
`RAVE_BRIDGE_LIVE=1`, refuses to run against production:
- All 6 bridge paths (7 ops) are in the live OpenAPI spec; path/method/param/status shapes match.
- Every endpoint 401s unauthenticated (not 404/405) → the routes are deployed as we call them.
- **Found a real bug**: the error body is NOT textbook RFC7807 as the contract doc implies. A
  real 401 is `{"status":"error","trace_id":"…","message":"…","details":{"code":"UNAUTHORIZED"}}`
  - the human text is `message`, not `detail`/`title`. `decodeProblem` read only detail/title, so
  every API error surfaced with empty text. Fixed; the live test pins it.

**Fake (in-process, implements the contract verbatim)** - `internal/bridge/fake_test.go`:
fire-and-forget loss, 403-until-mutual-accept, 413, 429+Retry-After, 404-never-403. Everything
below the auth boundary is fake-verified, because a real end-to-end WAN test needs a signed-in
account (the isolated test instances deliberately are not, and must not be signed in with the
user's credentials).

`TestLiveAuthenticatedRoundTrip` is written and skips unless `RAVE_BRIDGE_TOKEN` is supplied - it
registers two sessions, streams one, mutually accepts and relays a frame. **Run it once with a
real token to close the last gap.**

## Live UI verification

Two isolated instances (`RAVE_MATE_CONFIG_DIR`, `RAVE_MATE_CTL_ADDR` 127.0.0.1:47731/47732,
loopback-only peer bind, never touching the user's live instance on 47620):
- Module starts; signed-out path logs once (gated) and idles: `[bridge] not signed in; the
  account bridge is idle`.
- **Full TOTP enrolment driven end to end**: the instance minted a secret, an *independent Python
  HOTP implementation* computed a code from it, and the Go verifier accepted it → "Authenticator
  linked". The sealed (DPAPI) enrolment then survived a restart.
- `ctl screenshot-all`: 11 tabs, 0 errors, **zero ⚠OVERFLOW**.

## Gaps / follow-ups

1. **Token refresh is not implemented anywhere in rave-mate.** `auth.Manager` stores the refresh
   token but never uses it - no refresh call, no timer, no 401-retry. A long-lived WAN session
   will therefore die when the access token expires and will not come back without a browser
   re-login. This predates the epic but the bridge makes it hurt. **Highest-value follow-up.**
2. **The web (FE) half must implement the tunnel**, not just the relay: an Ed25519 identity in the
   browser (WebCrypto + IndexedDB), peerlink's AKE, the AEAD upgrade, then the authz gate, and
   only then the existing studio client inside it. Wire details below.
3. **No QR code.** No encoder exists in the tree and the 7-day soak rule forbids adding a dep, so
   the otpauth URI + secret render as copyable text. A pure-stdlib QR encoder (~400 LOC, byte
   mode, fixed EC level, inline SVG) is the follow-up - precedent: `internal/discovery` writes its
   own DNS codec, `tools/winicon` its own ICO writer.
4. **mate↔mate WAN dial has no UI yet.** `bridge.Manager.Dial` + `Devices()` are wired and the
   tunnel works, but the Peers tab doesn't yet list account devices with a "Connect" button. The
   settings card shows state; the dial surface is the next increment.
5. `secureseal` is Windows-only, so on macOS/Linux the enrolment is session-scoped. A Keychain /
   libsecret backend is already a documented follow-up in `SUPPLY_CHAIN.md`.

## Wire spec for the FE agent

Inside `payload_b64` (opaque to the server), a bridge `Conn` chunk is:

```
[1B ver=1][1B type: 0=data 1=ack][8B seq BE][8B ack BE][1B flags: bit0=fin][body...]
```

- ARQ: per-chunk `seq`, cumulative `ack` (= highest contiguous seq received + 1), retransmit on
  RTO. A 202 from `/send` means nothing - wait for the ack.
- Fragmentation: a logical message is split into ≤192 KiB chunks; `fin` marks the last.
- AEAD: after the peerlink AKE, the **message** (not the chunk) is sealed with AES-256-GCM.
  Keys: `HKDF-SHA256(sessionKey, salt=transcript, info="rave-peer-link-v1", 32)` → master, then
  `hkdf.Key(sha256, master, nil, "rave-bridge-i2r-v1"|"rave-bridge-r2i-v1", 32)`. Initiator seals
  with i2r and opens r2i; responder mirrors. Nonce = 12B, big-endian per-direction **message**
  counter in bytes 4..12.
- Signal plane (readable by the server - metadata only):
  `{"t":"dial"|"dial-ok"|"dial-no","v":1,"proto":"peerlink"|"studio","reason":"…"}`
- authz gate (inside the AEAD):
  ```
  reached → caller  {"t":"authz-challenge","v":1,"methods":["token","totp"],"nodeId":"…"}
  caller  → reached {"t":"authz-response","v":1,"method":"token","token":"…"}
                    {"t":"authz-response","v":1,"method":"totp","code":"123456","label":"…"}
  reached → caller  {"t":"authz-grant","v":1,"token":"…","expiresAt":<unix ms>}
                    {"t":"authz-deny","v":1,"reason":"denied"}
  ```
  Store the granted token; present it next time to skip the code.

`studio`'s `handshake-ok` now reports `transport: "bridge"` (was hardcoded `"loopback"`).
