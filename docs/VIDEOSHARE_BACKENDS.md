# Video-share backends (internal/videoshare) - build + CI

`internal/videoshare` publishes each loaded deck's now-playing card as a live GPU/IPC video
frame so any compatible receiver (OBS, Resolume, TouchDesigner, VRChat, vMix, …) pulls deck
visuals straight from memory - no file, no browser source, no window capture. One named sender
per deck ("RaveMate Deck A" …).

Related: cross-PC video routes ENCODE captured Spout frames via the native MF hardware
pipeline on Windows (no ffmpeg child) - see `docs/dev/MF_NATIVE_ENCODE.md`.

Transport is selected at **build time** by a per-platform build tag. The default build (no tag)
ships the no-op backend (`sender_noop.go`, tag `!spout && !syphon && !pipewire`): the sink runs
(gate + render) but publishes nothing.

| Platform | Tag | Backend | Status |
|---|---|---|---|
| Windows | `spout` | Spout2 (DirectX 11 shared texture) | **Shipped** - CI `build:windows` |
| Linux | `pipewire` | PipeWire (SPA video node) | **Not implemented** (no `sender_pipewire.go`) |
| macOS | `syphon` | Syphon (Metal/OpenGL shared texture) | **Not implemented** (no `sender_syphon.go`, no mac CI job) |

A tag whose `sender_*.go` is absent **does not compile** (`undefined: newSender, backendName`),
so a tag is wired into CI only once its backend file lands.

## Windows - Spout (shipped)

- **Files:** `sender_spout.go` (cgo), `spout_shim.{h,cpp}` (C++ flat-C wrappers over `SpoutLibrary.h`).
- **SDK:** Spout2 `2.007.017`, fetched (SHA-256-pinned) into `third_party/spout/` by
  `scripts/fetch-spout.ps1` (Windows dev) or `scripts/fetch-spout.sh` (POSIX/CI). Produces
  `include/SpoutLibrary.h`, `bin/SpoutLibrary.dll` (MT/static-CRT), `lib/libSpoutLibrary.a`
  (MinGW import lib via `gendef` + `dlltool`).
- **Build deps (CI mingw cross):** `gcc-mingw-w64 g++-mingw-w64-x86-64` (the shim is C++ → need
  `CXX=x86_64-w64-mingw32-g++`), `mingw-w64-tools` (gendef), `curl unzip` (fetch). `<windows.h>`/
  `<d3d11.h>` come from the mingw headers - no extra SDK.
- **Build:** `CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ go build -tags spout …`
  Local Windows dev: `make build-spout`.
- **RUNTIME (critical):** the exe **load-time-links** `SpoutLibrary.dll` (confirmed: `objdump -p`
  lists `DLL Name: SpoutLibrary.dll`; running without it → `0xc0000135` STATUS_DLL_NOT_FOUND).
  The DLL MUST sit next to `rave-mate.exe`. CI ships it: build artifact → both NSIS installers
  (`-DSPOUT_DLL` / `-DMATE_DLL`) + the raw-exe feed (`deploy` copies `SpoutLibrary.dll`).
- **Self-update caveat:** the in-app updater self-swaps only the exe. A pre-spout install updating
  to a spout exe needs the DLL already present beside it. Fresh installer installs are fine; the
  raw-exe feed carries the DLL. If updating a legacy install, deliver the first spout release via
  the full installer (or extend `internal/selfupdate` to fetch the sibling DLL).

## Linux - PipeWire (to implement)

Backend not written. To turn on once `sender_pipewire.go` (tag `pipewire`, defining `newSender`
+ `backendName`) lands:

1. `.gitlab-ci.yml` `build:linux` before_script → add `pkg-config` (already have a C toolchain)
   and the PipeWire dev headers: **`libpipewire-0.3-dev`** (pulls `libspa-0.2-dev`). Skip the dev
   dep only if the backend uses pure-Go PipeWire bindings (check its build constraints / cgo use).
2. `build:linux` go build → add `-tags pipewire` (keep `CGO_ENABLED=1` if the backend is cgo).
3. RUNTIME: PipeWire is part of the modern Linux desktop session (`libpipewire-0.3-0`); no file
   ships beside the binary. Receiver side is any PipeWire video consumer (OBS PipeWire capture).

## macOS - Syphon (to implement; no mac CI today)

Backend not written and there is **no macOS CI runner**. Do not block on it. When a mac job +
`sender_syphon.go` (tag `syphon`) exist:

1. Needs a macOS runner (Syphon is a macOS framework - no cross-build from Linux). Gate the job
   so its absence can't block the proven win/linux tracks (`rules` on a mac runner tag +
   `allow_failure: true` initially, like `build:app:windows`).
2. Build deps: Xcode command-line tools + the **Syphon.framework** (link `-framework Syphon`
   plus the Metal/OpenGL/Foundation frameworks the backend uses) via cgo `LDFLAGS`. The framework
   is fetched/vendored + SHA-pinned the same way as the Spout SDK (add a SUPPLY_CHAIN.md row).
3. Build: `CGO_ENABLED=1 go build -tags syphon …` on the mac runner.
4. RUNTIME: bundle `Syphon.framework` inside the `.app` (`Contents/Frameworks/`); receivers are
   any Syphon client (Resolume, VDMX, OBS Syphon source).
