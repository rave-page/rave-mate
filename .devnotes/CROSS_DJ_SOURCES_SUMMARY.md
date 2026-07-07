# Cross-DJ-software sources - Serato / VirtualDJ / Rekordbox

Extends the DJ-data hub (Traktor + MIDI) to **Serato, VirtualDJ, and Rekordbox**: live
now-playing + collection read, plus a rekordbox MIDI-map generator. All opt-in (off by
default), all stdlib. See `docs/DJ_SOURCES.md` for the merged coverage table.

## What shipped

| Area | Package | Notes |
|---|---|---|
| Serato decoder | `internal/serato` | One TLV envelope decoder for `database V2`, crates, `History` sessions. 4 unit tests. |
| Serato source | `…/sources/seratosrc` | Collection + live now-playing (watch newest `History/Sessions/*.session`, ~1.5s poll). |
| VirtualDJ parser | `internal/virtualdj` | `database.xml` (per-drive merge; **BPM = 60/seconds-per-beat**). Test covers conversion. |
| VirtualDJ source | `…/sources/virtualdjsrc` | 3 backends: NetCtl HTTP poll, OS2L server (self-hosted stdlib mDNS `_os2l._tcp` + TCP/JSON), tracklist file. |
| Rekordbox live | `…/sources/rekordboxsrc` | db-poll (`djmdSongHistory`, ~60s lag) + Windows memory-read (offset-seeded). `rekordboxdb.OpenDecrypted` added. |
| Rekordbox→PRO DJ LINK | `app.go` | `rekordboxsrc.NewResolver` feeds the existing `prodjlink` source so CDJ track ids resolve to text. |
| Rekordbox MIDI map | `internal/rekordboxmap` | Importable CSV on the same CC layout as `midi.custom`. Test covers columns + a known row. |
| CLI | `cmd/rave-mate` | `import serato` / `import virtualdj` collection preview. |
| UI | `ui/view_settings_djsources.go` | Per-source cards with transparent caveats + rekordbox MIDI export. |
| CI | `.gitlab-ci.yml`, `Makefile`, `scripts/fetch-spout.sh` | Windows build now `-tags spout` (DLL shipped end-to-end). PipeWire/Syphon documented (not yet implemented in Go). |

## Channel trade-offs (surfaced in each UI card)

- **Serato** - local-only, zero setup; ~1–2s now-playing lag.
- **VirtualDJ NetCtl** - full metadata, but needs **Pro 2023+** + one-time manual plugin install.
- **VirtualDJ OS2L** - zero-config (auto-discovered), but **no track text** (BPM/beat only).
- **Rekordbox db-poll** - safe, reuses the key, but **~60s lag**.
- **Rekordbox memory-read** - real-time, **Windows-only**, **fragile** (per-version offsets).
- **Pro DJ Link** - best CDJ data, but needs Pioneer hardware on the LAN.

## Verification

- `go build ./... && go vet ./...` clean (Windows); new packages cross-compile clean for
  linux/darwin. `go test` green (serato, virtualdj, rekordboxmap, session/aggregator).
- Live: launched the tray exe (WMI), enabled all three sources - cards reached **watching
  sessions** / **listening** / **polling**; daemon stable, clean quit. (Config restored to off.)
- **Not verified** (no such software/data on the dev box): end-to-end now-playing against a
  real Serato/VirtualDJ/Rekordbox session, and OS2L against a live VirtualDJ.

## Operator TODOs / assumptions to confirm on real data

- **Serato session adat field ids** (`internal/serato`): deck 0-vs-1 base, BPM scaling, the
  0x2d play-time-vs-duration ambiguity - seeded from open-source parsers; confirm on a real set.
- **VirtualDJ NetCtl** units (`get_time` ms, `get_bpm` raw) + plugin port/auth defaults.
- **Rekordbox memory offsets** (`memory_windows.go`): **placeholders** (`seeded:false`) - the
  backend self-disables until real offsets are filled in per rekordbox version (cf. grufkork/rkbx_link).
- **Rekordbox MIDI CSV** columns + `KnobSliderHiRes` 7-bit-vs-14-bit - re-seed from a real export.
- **PipeWire / Syphon video-share backends do not exist in Go yet** - only the Spout (Windows)
  sender is implemented. CI wires Spout; the other two are net-new cgo work (documented turnkey
  in `docs/VIDEOSHARE_BACKENDS.md`).
