# Ableton Link SDK (vendored, fetched on demand)

Backs the `abletonlink` build tag of `internal/abletonlink` (real Link session in the
`abletonlink` featurehost child). Without the tag the package ships `link_stub.go` and the
feature is inert - so a default build never needs this dir.

Not committed - run the fetch script (two SHA-256-verified downloads + a C++ compile, see
`SUPPLY_CHAIN.md`):

```
bash rave-mate/scripts/fetch-link.sh          # native / Linux cross
MINGW_PREFIX=x86_64-w64-mingw32 bash rave-mate/scripts/fetch-link.sh   # mingw Windows cross
```

Produces (only these two - the full Link + asio source trees are compiled in a temp dir and
discarded, mirroring `third_party/spout`):
- `extensions/abl_link/include/abl_link.h` - the C wrapper header the cgo package includes.
- `lib/libabl_link.a` - static archive of `abl_link.cpp` + the header-only Link runtime + asio,
  built by the script with the (cross) C++ toolchain. Linked via cgo `-labl_link -lstdc++`.

Build + run:

```
CGO_ENABLED=1 go build -tags abletonlink ./cmd/rave-mate
```

Upstream: https://github.com/Ableton/link (dual GPLv2+/commercial). Pinned Link-3.1.5 +
asio-standalone (chriskohlhoff/asio) at the submodule commit that tag references.

LICENSE: Link is dual-licensed GPLv2+/commercial and rave-mate is not GPL. Building for
development is fine; DISTRIBUTING a Link-enabled binary requires Ableton's free Link license
grant - request it at https://www.ableton.com/en/link/ before shipping.
