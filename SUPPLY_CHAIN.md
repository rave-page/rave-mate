# Supply-chain policy

Mirrors the web repo's pnpm guard (`minimum-release-age=10080`, see `../.npmrc`):
**reject any dependency version published less than 7 days ago.** The Sept-2025+
npm worms (`chalk`, `debug`, `shai-hulud`) all shipped malicious versions yanked
within hours; a one-week soak neutralises them without manual triage.

Go has no native `minimum-release-age`, so we enforce it with policy + a checker.

## Rules

1. **Never `go get pkg@latest`.** Always pin an exact version you have verified is
   ≥7 days old: `go get pkg@vX.Y.Z`. (The agent harness blocks `@latest`.)
2. **Verify before adding.** Check publish time:
   ```
   go list -m -json <module>@<version> | grep Time
   ```
   Reject if `Time` is < 7 days ago.
3. **`go.sum` is the integrity anchor.** Committed + verified against
   `sum.golang.org` (`GONOSUMCHECK` must stay unset). Never delete entries by hand.
4. **`-mod=readonly` in CI.** Builds may not mutate `go.mod`/`go.sum`; drift fails
   the pipeline (see `.gitlab-ci.yml`).
5. **`govulncheck` on every pipeline.** Known-CVE gate, separate from the soak.
6. **Minimise the surface.** Prefer the stdlib. Every new direct dep needs a one-line
   justification in this file's table below. Indirect deps inherit the same soak.
7. **Urgent patch exception.** If a fresh release is genuinely required (active CVE),
   add the exact `module version` to `.modage-allow` with a dated reason - never
   weaken the global window.

## Enforcement

`scripts/check-release-age.sh` walks every `require` in `go.mod`, queries the proxy
for each version's publish `Time`, and fails on anything younger than 7 days unless
listed in `.modage-allow`. Wired into `pnpm`-less CI as the `supply_chain` job.

## No-dependency notes

- **MIDI input** (`internal/midi`): Windows reads `winmm.dll` directly via stdlib
  `syscall` (`midiInOpen` + a `syscall.NewCallback` trampoline) - **no third-party MIDI
  library added**, honouring "prefer the stdlib". Other OSes report unsupported until a
  vetted, soaked backend lands (a portable driver would need its own justification row).

## Runtime-fetched external binaries

`internal/mediatools` lets Settings download the external CLI tools the app shells out to
(no Go module added; not linked into the binary). Each download is **SHA-256-verified**
before it is unpacked into the app-managed bin dir (`<configDir>/bin`); a mismatch aborts
the install. Windows-only (the pinned archives are the Windows builds); other OSes fall
back to a manual link + PATH discovery.

| Tool | Source | Integrity anchor |
|---|---|---|
| ffmpeg + ffprobe | `gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip` | same-origin `.sha256` sidecar (gyan rebuilds the fixed URL; the sidecar is fetched + checked at install time rather than hard-pinned to a stale digest). |
| Chromaprint `fpcalc` | GitHub release `acoustid/chromaprint v1.5.1` (immutable asset) | hard-pinned `sha256:36b478e16aa69f757f376645db0d436073a42c0097b6bb2677109e7835b59bbc`. |

## Build-time native SDKs (vendored - `spout` / `vr` / `abletonlink` build tags)

Not Go modules + not in the default build. The `videoshare` Spout backend (Windows GPU video
sharing, `-tags spout`) links the prebuilt Spout2 SDK, fetched into `third_party/spout/`
(git-ignored), never committed. Two fetchers, **same URL + SHA-256 pin**: `scripts/fetch-spout.ps1`
(Windows dev) and `scripts/fetch-spout.sh` (POSIX - used by the GitLab `build:windows` mingw
cross-build, where PowerShell isn't available). Both SHA-256-verify the download before extracting,
then build a MinGW import lib (`libSpoutLibrary.a`) from the DLL via `gendef` (mingw-w64-tools) +
`dlltool` (binutils-mingw-w64). Only the single `extern "C" GetSpout()` export is linked;
everything else dispatches through the COM-like vtable, so no MSVC↔MinGW C++ ABI coupling. The
default build (no `-tags spout`) never touches it. RUNTIME: the spout-tagged exe load-time-links
`SpoutLibrary.dll`, so CI ships the DLL beside the exe (installer + raw-exe feed). Linux/macOS
backends (PipeWire `-tags pipewire`, Syphon `-tags syphon`) are not yet implemented - see
`docs/VIDEOSHARE_BACKENDS.md`.

| SDK | Source | Integrity anchor |
|---|---|---|
| Spout2 `2.007.017` (`SpoutLibrary`, MT/static-CRT) | GitHub release `leadedge/Spout2 2.007.017`, asset `Spout-SDK-binaries_2-007-017_1.zip` (BSD-2-Clause) | hard-pinned `sha256:695F20E3505FA0DA51B2EB959AF359F5D9E2C914BB9676E9118D19F6A5424BF4`. |
| Spout2 `2.007.017` **source** (patched `SpoutLibrary.dll` rebuild) | GitHub tag archive `leadedge/Spout2` `2.007.017.zip` (BSD-2-Clause) | hard-pinned `sha256:1CC0C958EE14AF614744AC40C054C2FD7DF17F8C5B94EB8ECFEACB37F5B06460` in `scripts/build-spout-dll.ps1`. |

**Patched-DLL exception:** `third_party/spout/bin/SpoutLibrary.dll` IS committed. The official
binary crashes its host process when `wglDXOpenDeviceNV` fails (`LinkGLDXtextures` overflows a
`char[128]` into a static-CRT `__fastfail` - killed the daemon 3× 2026-08-04/-10). The committed
DLL = pinned source + `third_party/spout/patches/spoutgl-linkgldx-bufferoverflow.patch` (buffer
128→256), reproduced by `scripts/build-spout-dll.ps1` (MSVC /MT + PDB; the marker
`bin/SpoutLibrary.dll.patched` holds the DLL's SHA-256). Both fetch scripts keep the patched DLL
while the marker exists (header + import lib still come from the release zip). Bonus: built from
the source tag, its vtable matches the vendored `SpoutLibrary.h` (the official binary's didn't -
see the vtable-window note in `internal/videoshare/spout_shim.cpp`).

### OpenVR (vendored, committed - `vr` build tag)

The `vroverlay` OpenVR/SteamVR backend (`internal/vroverlay/openvr.go`, `-tags vr`) renders VR
overlays. Unlike Spout it is **committed** under `internal/vroverlay/sdk/` (~840 KB) - three files
from `ValveSoftware/openvr` tag `v2.15.6` (2026-03-27, BSD-3-Clause), well past the 7-day soak:

| File | sha256 |
|---|---|
| `openvr_capi.h` (flat C API header) | `3d32d8eb5bcbe2e250610123aee8142c68f0e7d4da823f87513b6a2a333078d7` |
| `openvr_api.lib` (win64 import lib) | `a0bf57c5920f569e8d21ab3e5bc95bac4b73e2016217f8b5b93495a2a7197bbb` |
| `openvr_api.dll` (win64 runtime) | `bab8ac6ef64e68a9ca53315b0014d131088584b2efdfa6db511d67ec03cfcb4a` |

The flat header exposes interfaces as `__stdcall` fn-table structs; the cgo preamble defines small C
helpers that call the table (the C compiler owns the ABI - no hand-written Go struct layout). The
global entry points (`VR_InitInternal`/`VR_GetGenericInterface`/…) sit behind `#if 0` in the header,
so the preamble declares them itself; cgo links them via `-l:openvr_api.dll`. `-std=gnu11` is forced
(the header's `typedef char bool` breaks under GCC's C23 default). RUNTIME: a `vr`-tagged exe
load-time-links `openvr_api.dll`, so ship the DLL beside the exe. The default build (no `-tags vr`)
uses a no-op stub (`stub.go`) and never touches the SDK. No Go-module dependency was added (the only
existing Go binding, `tbogdala/openvr-go`, is archived/2017 - unusable).

### Ableton Link (vendored, fetched on demand - `abletonlink` build tag)

The real Ableton Link session (`internal/abletonlink/link_cgo.go`, `-tags abletonlink`) drives
tempo/phrase sync. Not a Go module + not in the default build (which ships `link_stub.go`, inert).
`scripts/fetch-link.sh` fetches two **SHA-256-pinned source tarballs** into `third_party/link/`
(git-ignored, never committed): `Ableton/link` and its `chriskohlhoff/asio` submodule (pulled as a
tarball by the exact commit the Link tag references, so no `git submodule` in CI). Link is header-only
C++ **except** the `abl_link` C wrapper; the script compiles `abl_link.cpp` + the header-only Link
runtime + asio into a static `lib/libabl_link.a` with the (mingw cross) `g++`, so the cgo package
links only `abl_link.h` + the archive (`-labl_link -lstdc++`). Statically linked (no runtime DLL); the
mingw C++ runtime folds into the exe via `-extldflags '-static -static-libstdc++'` (build:windows).
On Windows Link links `ws2_32`/`iphlpapi`/`winmm` (in the cgo LDFLAGS). Verified: the mingw
`GOOS=windows … -tags abletonlink` cross-build links clean.

**LICENSE (compatible).** Ableton Link is **dual-licensed GPLv2-or-later/commercial**, and rave-mate
is now **AGPL-3.0-or-later**. AGPL-3.0 is compatible with Link's GPLv2-or-later option, so Link may be
used under the GPL option with **no separate commercial Link grant required** to distribute the
`abletonlink`-tagged artifacts. Building for dev/CI and shipping are both fine under the AGPL.

| SDK | Source | Integrity anchor |
|---|---|---|
| Ableton Link `Link-3.1.5` (commit `45931b8`, 2025-12-01, GPLv2+/commercial) | `codeload.github.com/Ableton/link/tar.gz/45931b8bd2a7066f1950f2a0beab81a0c51f28fe` | hard-pinned `sha256:4fcabe1eb377dd29c1b3884757f9d44fb067be3bfd7e3f6b534f2452b19d7227`. |
| asio-standalone (commit `231cb29`, Link submodule pin, BSL-1.0) | `codeload.github.com/chriskohlhoff/asio/tar.gz/231cb29bab30f82712fcd54faaea42424cc6e710` | hard-pinned `sha256:5def09efbd4be199dd6ddca53a2c99b9eef696f6b430910d896594b04ff59108`. |

## Approved direct dependencies

### App module (`rave.page/mate` - compiled into the shipped binary)

| Module | Version | Pinned | Why |
|---|---|---|---|
| `fyne.io/fyne/v2` | v2.7.4 | 2026-05-12 | Native cross-platform GUI: system tray, real window, notifications, no webview/JS. |
| `github.com/oapi-codegen/runtime` | v1.4.1 | 2026-05-19 | Tiny runtime helper imported by the generated API client (param styling). |
| `github.com/coder/websocket` | v1.8.14 | 2025-09-05 | Zero-dependency WS server for the Local Studio loopback channel. Go has no stdlib WS server; hand-rolling RFC 6455 framing is riskier than this minimal, audited lib. All channel crypto stays stdlib (crypto/ecdh, crypto/hkdf, crypto/hmac). |
| `golang.org/x/sys` | v0.39.0 | (Go team) | Windows registry (URL-scheme registration) + DPAPI (token-at-rest encryption). Already transitive via Fyne. |
| `golang.org/x/net` | v0.48.0 | (Go team) | `x/net/ipv4` for per-interface multicast joins (`JoinGroup`/`SetMulticastInterface`/loopback) in the pure-stdlib mDNS LAN peer discovery (`internal/discovery`) - far safer than hand-rolling Windows multicast socket options via raw `syscall`. **Already present** (was transitive via Fyne/http2); promoted indirect→direct, no new download. The DNS wire codec + all peer-link crypto stay stdlib. |
| `github.com/dhowden/tag` | v0.0.0-20240417053706-3d75831295e8 | 2024-04-17 | Pure-Go (no cgo) embedded-tag reader (ID3v1/2, MP4, FLAC, Ogg) for the Music-library file browser: artist/title/album/genre/BPM/key on loose files not in a DJ collection. Read-only; ffprobe still supplies codec/duration. |
| `github.com/fsnotify/fsnotify` | v1.9.0 | 2025-04-04 | Cross-platform filesystem notifications (the de-facto Go standard) for the automation engine's file-arrival watchers - a new media file in a watched folder triggers its action chain. Only transitive dep is golang.org/x/sys (already present). |
| `go.etcd.io/bbolt` | v1.4.3 | 2025-08-19 | Embedded pure-Go KV store (etcd's engine) - rave-mate's local persistence: analysis cache (waveform/tags/fingerprint, mtime-invalidated) + automations / scheduled jobs / run history. Single crash-safe file, no cgo/server. Chosen over SQLite for minimal footprint given the KV-shaped access (the DJ library is already held in memory from the NML). Only transitive dep is golang.org/x/sys (already present). |
| `github.com/ebitengine/oto/v3` | v3.3.2 | 2025-01-19 | The audio OUTPUT device (WASAPI on Windows, pure-Go, no cgo) - the SOLE audio backend since beep (`gopxl/beep`) was retired. Drives BOTH the native `internal/audio` engine (player child; ~15ms buffer, 0-latency cue Space) AND the Fyne A/V trim player (`internal/mediaplayer`; s16le). Only transitive is golang.org/x/sys (already present). |
| `github.com/hajimehoshi/go-mp3` | v0.3.4 | 2021-08-11 | Pure-Go MP3 (MPEG-1/2 Layer III) decoder for `internal/audio` - builds its own frame index so seek is byte-accurate with no rescan. Zero transitive deps. |
| `github.com/jfreymuth/oggvorbis` | v1.0.5 | 2022-04-15 | Pure-Go Ogg/Vorbis decoder for `internal/audio` - yields float32 directly + SetPosition gives sample-accurate seek via the granule index. Transitive: jfreymuth/vorbis (same author, stays indirect). |
| `github.com/mewkiz/flac` | v1.0.12 | 2024-08-05 | Pure-Go FLAC decoder for `internal/audio`. NewSeek gives sample-accurate seek via the SEEKTABLE, or an index built on demand for seektable-less files - O(log n), never a rescan. Distinct from go-flac (tag container). |
| `github.com/bogem/id3v2/v2` | v2.1.4 | 2023-02-09 | Precise ID3v2 tag writer (TBPM/TKEY/TCON/COMM) for the analysis→file-tags feature (`internal/tagwrite`): writes the DJ-software BPM/key/genre into MP3 files so other software reads them. Writes are atomic (temp+rename) + revertible (`tag_edits` table). Pure-Go, zero transitive deps. |
| `github.com/go-flac/go-flac` | v1.0.0 | 2023-05-22 | FLAC metadata-block read/write container for `internal/tagwrite` (the FLAC half of analysis→file-tags). Pure-Go, no transitive deps. |
| `github.com/go-flac/flacvorbis` | v0.2.0 | 2023-05-22 | Vorbis-comment parse/build on top of go-flac - writes BPM/INITIALKEY/KEY/GENRE/COMMENT into FLAC files. Pure-Go. M4A/WAV/AIFF tag-writing deliberately deferred (no clean soaked writer; abema/go-mp4 is low-level). |
| `gioui.org` | v0.10.0 | 2026-05-18 | Gio immediate-mode GUI - the incremental Fyne replacement (media/graphics surfaces first, see `GIO_MIGRATION.md`). Pure-Go GPU backend on Windows (d3d11, **no cgo**), windows run off the main thread beside Fyne. v0.10.1 (2026-06-27) rejected: <7d soak at adoption. Transitives (`gioui.org/shader` v1.0.8 2023-10, `go-text/typesetting` v0.3.4, `eliasnaur.com/font`, `golang.org/x/exp{,/shiny}` 2025-04) all well past soak; stays indirect-only. |
| `modernc.org/sqlite` | v1.50.1 | 2026-05-10 | **Pure-Go** SQLite (no cgo) for the relational library store (`internal/libdb`): persists the imported DJ collection (tracks + cues/beatgrid as JSON) + play-history sessions so the library survives restarts and "Refresh" upserts only the delta instead of re-importing the whole NML every launch. Supersedes the bbolt-only design for library data (relational joins for track↔history↔recording matching). Pure-Go keeps the worker/service paths cgo-free (only Fyne forces cgo). Transitive: modernc.org/{libc,mathutil,memory}, dustin/go-humanize, ncruces/go-strftime, remyoudompheng/bigfft - all soaked. |
| `github.com/webview/webview_go` | v0.0.0-20240831120633-6173450d4dd6 | 2024-08-31 | Native OS webview host (WebView2/WebKitGTK/Cocoa) for the Go-driven HTML/CSS renderer (`internal/webui`, opt-in `features.ui.renderer="webview"`) - the incremental Fyne replacement. Already vetted + pinned in the sibling `rave-app` module (same commit); reused identically. cgo (rave-mate is already cgo via Fyne), zero transitive deps beyond `golang.org/x/sys` (already present). Go renders every view + drives the DOM through the binding; there is NO web server and NO JS framework (one ~90-line transport/introspection runtime). Falls back to Fyne on nocgo builds. |

### Tool module (`tools/genapi` - build-time only, NOT shipped)

Isolated in its own `go.mod` so oapi-codegen's heavy dep tree never reaches the app
binary. Run via `make generate-api`.

| Module | Version | Pinned | Why |
|---|---|---|---|
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.7.1 | 2026-06-05 | Generates the filtered Go API client from `/openapi.json`. 2.7.1 closes GHSA-rjwr-m7qx-3fjr (server-description comment injection; we generate from our own spec, defense in depth). |
| `github.com/getkin/kin-openapi` | v0.144.0 | 2026-07-23 | OpenAPI 3 loader; lets us fix up the spec before generation. 0.144.0 closes GHSA-r277-6w6q-xmqw (critical, ValidationHandler fail-open) + GHSA-jpcw-4wr7-c3vq — neither path reachable from genapi (pure codegen, no request validation), fixed anyway. **One-time soak exemption** (user-approved 2026-07-25, `.modage-allow`): release was 2d old; verified before adoption — go checksum-DB verify clean, module-proxy bytes byte-identical to the GitHub v0.144.0 tag (0 differing files), release cut by the long-time maintainer (fenollp), and a danger-pattern scan of every added line in the v0.135.0→v0.144.0 diff found no exec/syscall/net/unsafe/linkname additions outside testdata. Exemption covers EXACTLY this version; future bumps re-enter the normal 7-day soak. Transitives pulled by the bump (jsonpointer v0.22.5, swag/jsonname v0.25.5, oasdiff/yaml v0.1.1, yaml3 v0.0.14, jsonschema/v6 v6.0.2, regexp2 v1.11.0, testify/v2 v2.4.0) are all ≥30d old — no exemption needed. |

### Native toolchain (build-time only, NOT a Go dep)

| Tool | Version | Why |
|---|---|---|
| `zig` | >= 0.16.0 (winget zig.zig) | Builds `native/zigcore` (C-ABI static lib: sinc resampler + waveform kernels) for `-tags zigdsp` builds. Build-time only; untagged builds never need it. No third-party Zig packages (no `build.zig.zon` deps) — any future one gets a row here + the 7-day soak. |
