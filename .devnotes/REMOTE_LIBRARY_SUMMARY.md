# Remote Library (live mirror over the peer link)

Epic: ALL Library-tab features remote-controllable across paired peers. From PC A, open PC B's
library; every command (gridfix, cue/drop edits, tag writes, transcodes, playlists) executes ON
B against B's files/db. The view is B's own rendered Library tab, remote-driven; B's visible
window is never touched. Replaces the degraded RPC-rendered remote panels (remotectl library
verbs stay for non-UI callers).

## Architecture

```
B (host)                                        A (controller)
webui ruiHub ── headless UI (virtualShell) ──── ruiHub ── Library mirror (iframe in lib-body)
  │  doc/eval stream (ChanRemoteUI, chunked) ──▶  srcdoc + __rx eval replay
  ◀── act (window.rave payloads) ── input fwd ──  window.rave → parent.__rmirrorPost
  ◀── fetch (/m|/img byte ranges) ── media ─────  /rmt/<sid>/ loopback proxy + rewrite
```

- **Virtual shell + headless session** (`webui/virtualshell.go`): shell seam impl with no
  window; renders emit to a sink, input replays through onAction (serialized act worker).
  `newHeadlessUI` pins the Library tab over the SAME Services as the window UI - no forked
  renderers, no per-feature RPC; heavy work runs in B's normal pipelines. Per-*UI package
  maps released via `releaseUIState`; rendermail keys namespaced per *UI; eval flusher skips
  the size-move gate + ack round-trip for virtual shells.
- **Transport** (`peerlink.ChanRemoteUI`, `webui/remoteui_wire.go` + `remoteui_host.go`):
  JSON frames open/doc/eval/act/close/closed/fetch/fetchres; doc/eval chunked at 4 MiB,
  sequential reassembly ≤24 MiB (drop-whole, next patch repaints). One host session per peer
  (replace-on-open); teardown on close frame, peer disconnect (peer-state listener), or UI
  stop. Rides peerlink's Ed25519 + per-frame-MAC link - only paired peers reach it.
- **Mirror** (`webui/library_mirror.go`): every lib-body render mints a fresh sid + reopens
  (auto-resync). Doc → same-origin iframe srcdoc with injected bridge (window.rave →
  parent → peer; `__rx` executes ONLY the paired host's Go-generated render stream). Banner:
  connecting/live/error + reconnect; peer drop degrades in place. Local section tabs hidden
  in remote mode (mirror carries B's). ctl snapshot/click/read/set/tap/type descend into the
  iframe. Native pickers refused in headless sessions (would pop on B's desktop).
- **Media proxy** (`webui/remoteui_media.go`): B rewrites `http://127.0.0.1:<port>/` →
  placeholder; A rewrites → `http://127.0.0.1:<aport>/rmt/<sid>/`, served by the existing
  loopback media server via byte-range fetch RPC. Only B's token-guarded /m/ + /img/ routes
  reachable. Audio auditions intentionally play on B (PlayerProxy).
- **Rig/test seams** (`peerlink`): `RAVE_MATE_PEER_BIND` (loopback bind skips mDNS),
  `RAVE_MATE_PEER_PORTS`, `RAVE_MATE_PEER_SEED` (direct dial, 5s tick, same SAS trust flow).

## Bounded buffers (cap + policy)

| Queue | Cap | Policy |
|---|---|---|
| virtualShell.acts (remote input) | 256 | drop-newest (flooding controller loses its clicks) |
| headless eval queue (existing) | 512 | coalesce per fragment id, drop-oldest + frags wipe |
| wire chunk | 4 MiB/frame | split; reassembly ≤24 MiB, drop-whole |
| reassembly | 1 in flight/peer | out-of-order/oversize → drop message |
| fetch RPC in flight | 32 | excess request errors (503) |
| fetch chunk | 2 MiB raw | ranged; loops bounded by stream sem |
| media streams | 4 concurrent | 503 beyond; ≤ streams × chunk memory |
| img proxy cache | 32 MiB/proxy | FIFO evict; whole image ≤4 MiB else 502 |
| proxies registered | 4 | FIFO evict |
| iframe eval pend (JS) | 256 | drop-newest until iframe load flush |

## Verified live (two isolated instances, loopback-only, 47721/47722)

SAS pairing via ctl (identical codes) → silent re-pair after restart (seed dial) → mirror
render (B's full tri-pane library incl. B's drives) → remote rekordbox-XML import → cue
editor open + drop add through the mirror → **◆1 asserted in B's own window (B's libdb)** →
transcode executed on B (worker in B's log; output file on disk) → link kill mid-session →
amber "Peer disconnected." + Reconnect on A → open/close cycles leak-free (41→43→41
goroutines on B). Native pickers + chained control blocked in sessions.

## Known gaps / follow-ups

- Embedded video preview inside the mirror untested end-to-end (library detail shows the
  encoder builder, no <video> element on the exercised surfaces); proxy /m/ path is
  unit-tested (ranged + sequential). Degrades per help text.
- mp-edit / Editor-tab jumps are pinned-blocked inside a session (mirror stays on Library).
- A's eval queue treats mirror batches FIFO (B pre-coalesces); an overflow drop self-heals
  on the fragment's next patch.
- Non-webview (Fyne) hosts don't serve sessions - controller shows the timeout hint.
- gridfix engine not installed on the test rig: cockpit renders remotely, engine run not
  exercised (same code path as local run over B's Services).
