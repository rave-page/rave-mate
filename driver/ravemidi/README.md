# ravemidi — rave-mate virtual MIDI kernel driver

Windows WDM/PortCls adapter exposing dynamically created virtual MIDI ports to classic
winmm apps (Rekordbox/Traktor/Serato). Key feature: **one-way ports** — apps see an
input-only MIDI port, so DJ software cannot echo LED feedback back into itself.
To our knowledge the first open-source kernel virtual-MIDI-port driver.

Design + research notes: `../../.devnotes/RAVEMIDI_DRIVER_DESIGN.md`.
rave-mate integration: `internal/midi/ravemidi_windows.go` (prefers ravemidi, falls back
to teVirtualMIDI when the driver isn't installed).

## Status

Scaffold. Control-plane protocol (`ioctl.h`), INF, build files, FIFO in place; PortCls
miniport + adapter in progress. Not yet buildable/installable.

## Build

Requires VS Build Tools (VCTools + Spectre-mitigated libs) + WDK ≥ 10.0.26100
(installed WDK, or NuGet packages `Microsoft.Windows.SDK.CPP{,.x64}` +
`Microsoft.Windows.WDK.x64` pinned to one version). GitHub CI: `windows-2022` runner
ships a WDK.

```
msbuild ravemidi.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Dev install (test machine / VM only)

Secure Boot OFF + `bcdedit /set testsigning on` + reboot; self-signed cert in
LocalMachine Root + TrustedPublisher; `Inf2Cat` + `signtool sign /fd sha256`; then:

```
pnputil /add-driver ravemidi.inf /install
devgen /add /bus ROOT /hardwareid Root\ravemidi
```

## Release signing

Microsoft attestation via Partner Center (EV cert required for account registration;
plain Authenticode on the .sys will NOT load on Secure Boot systems). Windows Server is
not supported (attestation excludes it). See the design doc for the full pipeline.

## License

AGPL-3.0-or-later (rave-mate). Note: forks that modify the driver cannot reuse our
Microsoft signature — re-sign under your own Partner Center account or run test-signed.
