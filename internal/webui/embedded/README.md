`rave-shell.exe` is copied here by `scripts/build-zig.sh` (gitignored) and embedded into Windows
builds via the `shellembed` tag (`shellembed_windows.go`). It is the DEFAULT window host, and the
embed is what makes that true on updated installs: the self-updater swaps only the main exe and
consumes no feed assets, so a sidecar-only child would only ever reach fresh installer runs.
Extraction is hash-stamped (`shellstage.go`), so a self-updated exe always runs its own child.
