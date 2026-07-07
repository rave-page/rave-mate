# LAN peer link - Phase 1 + 2

Lets rave-mate instances on a LAN **discover each other, link securely, and remember the
connection**. Foundation for the future (Phase 3+) library-sync / play-state-bridging /
remote-control features. Pure peer-to-peer - **no rave.page API** (that's a later path).

## What ships now

### Phase 1 - foundations
- **`internal/wirecrypto`** - shared stdlib crypto + canonical-JSON (ECDH-P256, HKDF, HMAC),
  extracted from `studio` (byte-exact TS parity preserved). `studio` keeps unexported aliases.
- **`internal/secureseal`** - OS at-rest sealing (Windows DPAPI; no-op elsewhere), promoted
  from `auth`. Used by `auth` + `identity`.
- **`internal/identity`** - stable long-term **Ed25519** node identity + `NodeID =
  b64url(sha256(pub))`, persisted in the bbolt store (seed sealed where available). nil store
  → ephemeral (degraded, logged).
- **`libdb` `change_log`** - append-only history of every tracked mutation (`play_count`,
  `last_played`, `rating`, `bpm`, `key`, `genre`, `comment`, `cues`, `beatgrid`) + recorder
  `play_event`s. Per-node Lamport `seq`; portable `track_hash =
  b64url(sha256(lower(artist)|lower(title)|round(dur)))` (+ `track_fp` reserved for
  fingerprints). Instrumented at the import upsert (`tracks.go`), `tagsync.Apply`, and
  recorder confirm. `RevertChange` re-applies an old value + flags it. This is the rollback
  backbone the merge engine will build on.

### Phase 2 - discovery + secure paired link + remembered peers
- **`internal/discovery`** - pure-stdlib mDNS/DNS-SD (`_ravemate._tcp.local.`) responder +
  browser; own minimal DNS wire codec (PTR/SRV/TXT/A, compression-pointer parse);
  `x/net/ipv4` per-interface multicast + `SetMulticastLoopback` + `SO_REUSEADDR` so two
  instances on one host find each other. TTL-0 goodbye, self-filter, 35s expiry.
- **`internal/peerlink`** - authenticated key exchange over a LAN websocket: ephemeral ECDH
  (forward secrecy) **authenticated by each node's Ed25519 identity signing the canonical
  transcript** (both id pubs + eph pubs + nonces). **SAS** = 6 digits from
  `HKDF(sessionKey, transcript)`, shown on both screens **only after both signatures verify**;
  a relay MITM runs two sessions → two codes → humans reject. Trust-on-pair persists the
  peer's key; reconnect verifies against it (no SAS) and **rejects a changed key for a known
  node** (`ErrKeyChanged`). MAC'd control frames (seq + HMAC); keepalive; auto-reconnect.
- **`internal/peers`** - remembered peers over the bbolt store (`BucketPeers`).
- **Wiring** - `config.Features.Peers` (v4, opt-in); a `"peers"` module whose toggle is the
  discovery on/off **button**; a Settings card + a **Peers tab** (connections / on-network /
  remembered-offline, Pair/Connect/Forget) + a SAS-confirm dialog.

## Security model (load-bearing)
1. Identity pubs are inside the **signed** transcript.
2. SAS hashes the **full** transcript.
3. SAS is shown **only after both Ed25519 signatures verify**.
4. A changed identity key for a **known** node id is **rejected**.

The LAN transport is plaintext `ws://`; control frames are HMAC-authenticated. **Phase 3
must add AEAD** (the sessionKey is already derived) before any library data crosses the link.

## Verification
- Unit: wirecrypto TS-parity; identity load/sign; `change_log` append/seq/revert + TrackHash;
  discovery wire round-trips + compression + self-filter/expire; peerlink **SAS MITM
  property**, tampered-auth rejection, trusted + changed-key reconnect; peers store round-trip.
- Live (`-tags manual`): two `discovery` instances find each other same-host (<0.5s); two
  `peerlink` Managers pair over real websockets + persist mutual trust (<0.2s).
- `go build ./... && go vet ./... && go test ./...` clean.

## Deferred to Phase 3+ (post-review)
- Library metadata **sync + CRDT merge** (per-node play-count G-counters → summed total;
  LWW metadata; keep per-node granularity; rollback via `change_log`).
- Cross-machine **track identity** reconciliation (fingerprint + `track_hash`; today
  play_events key on artist|title only, so they don't yet join import events by hash).
- **Write-back** of merged values into the DJ collection - automatic, but only when the DJ
  app is closed / not holding the file (detect the lock holder), with a pre-write backup.
- AEAD encryption of the link; play-state bridging; remote library/settings control.
