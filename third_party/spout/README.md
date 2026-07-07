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
