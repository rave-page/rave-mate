# File transfer between paired instances (`internal/filexfer`)

Send a file or directory (recursive) to a SAS-paired rave-mate instance. Task #31.

## Transport

- **Control plane**: `file.offer` / `file.answer` on the eventbus (peerlink ChanBus) - the
  medialink negotiation pattern. No keys on the bus.
- **Data plane**: dedicated AEAD TCP listener, ports **47651-47655** (studio 47615-19, ctl
  47620, peerlink 47631-35, medialink 47641-45). Peerlink control frames are MAC'd
  canonical-JSON - wrong tool for GB payloads; medialink's v1 media wire stays frozen, so a
  separate listener per the established precedent.
- **Crypto**: AES-256-GCM, length-framed, counter nonces (medialink transport pattern). Keys
  = HKDF(peerlink `FileSecret`, salt=transfer-id, "filexfer c2s|s2c v1"). `FileSecret` =
  HKDF(session key, transcript, `"rave-peer-file-v1"`) - new, domain-separated from
  MediaSecret. Per-transfer salt → parallel transfers never share keys/nonces.

## Protocol (receiver pulls; sender listens)

offer(bus) → policy gate (enabled? auto|ask) → answer(bus) → receiver dials, preamble =
transfer id → sender: `manifest` → per file: receiver `have{i}` (already complete) or
`get{i,offset}` → chunk frames (≤1 MiB) → `filedone{i,sha256}` → verify → rename `.part` →
final → receiver `done`. `cancel`/`err` ctl any time.

**Resume**: receiver writes `<dest>.part`, hashes the existing prefix on reconnect, and
negotiates the offset in `get`. Sender re-hashes from 0 (digest covers the whole file) but
transmits only the tail. Stalled sends re-offer on peer reconnect + a 10 s retry timer
(bounded); the receiver recognises the id and resumes without re-asking.

## Config (v22)

`features.fileXfer`: `enabled` (default off), `downloadDir` ("" = `<config dir>/downloads`),
`acceptMode` (`ask` default | `auto`).

## Surfaces

- `ui.Services.FileXfer` interface: `SendToPeer(nodeID, path) (id, err)` · `Transfers()` ·
  `Cancel(id)` · `Accept(id, ok)` - the Library "send to a paired instance" action calls this.
- Peers tab → "File transfer" block: pending accepts (Accept/Decline), transfer rows
  (progress bar, rate, cancel), settings row (receive on/off, dir, Ask/Auto).
- App: `filexfer` module (live toggle, zero footprint off); notify seam for ask-mode toasts.

## Tests

Chunk/hash round-trip, resume-from-offset (offset negotiation asserted), corrupt frame
(AEAD) + corrupt resume-prefix (sha mismatch, poisoned `.part` removed), manifest walk +
hostile-path rejection, full dir transfer + multi-queue + cancel + interrupt-resume over
ephemeral loopback, policy gating (disabled/ask/decline/accept). No fixed ports.
