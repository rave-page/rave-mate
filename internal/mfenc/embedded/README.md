`rave-mate-enc.exe` is copied here by `scripts/build-zig.sh` (gitignored) and embedded
into Windows builds via the `encembed` tag (`embed_windows.go`) - self-updated installs
extract it at runtime, so the encoder child always matches the main exe's version.
