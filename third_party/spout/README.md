# Spout2 SDK (vendored, fetched on demand)

Backs the `spout` build tag of `internal/videoshare` (Windows GPU video sharing).

Not committed - run the fetch script (SHA-256-verified download, see `SUPPLY_CHAIN.md`):

```
pwsh rave-mate/scripts/fetch-spout.ps1
```

Produces:
- `include/SpoutLibrary.h` - the COM-like C++ interface + `extern "C" GetSpout()` factory.
- `bin/SpoutLibrary.dll` - MT build (static CRT, no VC++ redist). Must sit next to `rave-mate.exe` at runtime.
- `lib/libSpoutLibrary.a` - MinGW import lib (gendef + dlltool) for the single `GetSpout` export.

Build + run:
```
CGO_ENABLED=1 go build -tags spout ./cmd/rave-mate
```

Upstream: https://github.com/leadedge/Spout2 (BSD-2-Clause). Pinned 2.007.017.

## Patched SpoutLibrary.dll rebuild (parked)

The official DLL `__fastfail`s its host process when GL/DX interop creation fails
(`LinkGLDXtextures` overflows a `char[128]`). `pwsh rave-mate/scripts/build-spout-dll.ps1`
rebuilds it from the pinned source tag + `patches/` with the overflow fixed (MSVC `/MT` + PDB +
a `.patched` SHA marker the fetch scripts honour). PARKED, not shipped: the rebuilt DLL's sender
init hangs the media featurehost child (the source tag differs from the official binary's actual
build tree). Shipped protection = the shim's interop pre-flight. See `SUPPLY_CHAIN.md`.
