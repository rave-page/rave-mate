# ravemidi.sys BSOD 0x50 — use-after-free in miniport teardown (2026-07-13)

## Symptom
Two whole-PC crashes while MIDI-learning a controller with Serato also running (contending for
the same physical controller). Crash #1: clean bugcheck. Crash #2: hard hang (fans ramping),
hard-reset.

## Root cause (from the minidump — authoritative)
`C:\Windows\Minidump\071326-9671-01.dmp`, `kd !analyze -v`:
- **BugCheck 0x50 PAGE_FAULT_IN_NONPAGED_AREA**, args `(…, 2, fffff802`3be519f7, 2)` = a **write to
  freed nonpaged pool**. `IMAGE_NAME ravemidi.sys`, `PROCESS_NAME svchost.exe` (audiosrv/wdmaud).
- Faulting instr `ravemidi+0x19f7` = `mov qword ptr [rax+98h],0`, disassembled to
  **`RaveMiniport::~RaveMiniport`** writing `m_Ctx->ServiceGroup = nullptr` (offset 0x98 in RAVE_PORT).

### The bug
`DestroyPort` freed the `RAVE_PORT` when `StreamCount == 0` (no open *streaming pin*). But an open
*filter handle* (wdmaud/midisrv/DJ app — no pin instantiated) still referenced the PortCls port
object. When that last handle closed **after** the free, PortCls ran `~RaveMiniport`, which
dereferenced the already-freed `m_Ctx` → write to freed pool → 0x50. `StreamCount` only counted
pin instances, not filter-handle references, so teardown raced the audio stack's handle close.
The Serato/MIDI-learn contention just made a mid-life destroy while a handle was open far more likely.

## Fix (share-counted RAVE_PORT lifetime)
`RAVE_PORT.OwnerRefs` (InterlockedIncrement/Decrement, freed on last deref via `RavePortDeref`):
- created with 1 (port-manager share); `CreateRaveMiniport` takes a 2nd share for the miniport.
- `DestroyPort` / unload / rollback drop the **manager** share (no longer free directly).
- `~RaveMiniport` drops the **miniport** share — the block outlives DestroyPort until the last
  filter handle closes, so `m_Ctx` is always valid in the dtor.
Files: `miniport.h` (OwnerRefs + RavePortDeref decl), `adapter.cpp` (init=1, DestroyPort/unload/
rollback → RavePortDeref, RavePortDeref impl), `miniport.cpp` (CreateRaveMiniport +1, ~RaveMiniport
deref).

## Also fixed
- **Freeze #2 (runaway CPU)**: `mirror.cpp` TapThread busy-spun a KS `IOCTL_KS_READ_STREAM` with no
  back-off when a contended pin returned instant empty completions → 100% CPU kernel thread. Added
  a 5ms back-off after 8 consecutive zero-byte reads.
- **Serato THRU adoption** (user ask, not spoofing — forwarding): `managed.cpp` first fan-out THRU
  port now clones the source controller's name verbatim so Serato's controller matching opens it
  while ravemidi holds the physical pin. Serato native-HID gating may still reject non-authorized
  hardware — MIDI-mapping mode works, native-HID controllers TBD.

## Local build (dev) — toolchain notes
VS Community lacked the WDK VS integration (the vsix step of the WDK install didn't run; only the
SDK/Dev17 *content* installed). Reconstructed the `WindowsKernelModeDriver10.0` toolset shim under
`VC\v170\...\PlatformToolsets\` (Toolset.props imports v143 + DesignTime `WDK.props` + `WindowsDriver.Default.props`).
Build (local test-signed only — CI keeps Spectre + Code Analysis for the release/attestation build):
`msbuild ravemidi.vcxproj /p:Configuration=Release /p:Platform=x64 /p:SpectreMitigation=false
/p:WindowsTargetPlatformVersion=10.0.26100.0 /p:TargetPlatformVersion=10.0.26100.0 /p:DDKPlatform=x64
/p:ForceImportAfterCppProps=<wdk-forceafter.props> /p:ForceImportAfterCppTargets=<wdk-forceafter.targets>`
(the two force-after files pull the kernel platform props/targets + relax WX/analyze for the local
build). Test-signed with `CN=ravemidi test signing` (thumbprint 5ABE42A8…), version bumped to
5.0.0.1 in `build/testsign/`.

## Activation (needs elevation + a reboot — NOT done automatically)
The running driver is the buggy one; unloading it live risks tripping the same UAF during teardown
while audiosrv holds a port handle. Safe path: `pnputil /add-driver build\testsign\ravemidi.inf /install`
(defers to reboot if the devnode is busy), then **reboot**. Until then, avoid MIDI-learn while
another app holds the controller.
