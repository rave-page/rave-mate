# Embedded `Api/` QML - third-party (MIT)

These QML/JS files are Traktor's **traktor-api-client** controller mod by Erik Minekus,
vendored verbatim and embedded so rave-mate can install them offline (no runtime download
of third-party code that runs inside Traktor).

- Upstream: https://github.com/ErikMinekus/traktor-api-client
- License: MIT - Copyright (c) 2024 Erik Minekus

The mod makes Traktor's D2 QML surface POST live deck/channel/master state to
`http://localhost:8080` (`deckLoaded/{A-D}`, `updateDeck/{A-D}`, `updateChannel/{1-4}`,
`updateMasterClock`) - the exact endpoints `internal/traktor` listens for.

rave-mate installs these by **patching the live `D2.qml` in place** (two inserted lines:
`import "./Api"` + `ApiModule {}`) and dropping this folder beside it - it never replaces
the stock `D2.qml`, so a Traktor update that rewrites `D2.qml` is recovered by re-applying
the patch instead of clobbering the new stock file. See `internal/traktorqml`.
