# ravemidi — rave-mate's kernel virtual MIDI driver (design)

Status: DESIGN + scaffold. Research 2026-07-10 (two deep-research passes: driver architecture,
signing/distribution). Goal: rave-mate creates named virtual MIDI ports for classic winmm apps
(Rekordbox/Traktor/Serato) without third-party drivers — including **one-way ports** (apps see
input-only → no LED-echo self-loop) and loopback cables. teVirtualMIDI (loopMIDI's driver)
stays as fallback backend; ravemidi is preferred when installed. Would be the **first
open-source kernel virtual-MIDI-port driver** (none exists; teVirtualMIDI/LoopBe/MIDI Yoke all
closed).

## Why kernel at all

winmm builds its port list from WDM audio filters exposing MIDI pins (wdmaud → sysaudio →
KSCATEGORY_AUDIO). User-mode can't fake that. Windows MIDI Services (Win11 24H2+) has
driverless virtual endpoints, but its **classic-app (winmm) handoff is staged-rollout gated**
(off on user's 26200; forcing it breaks all winmm ports — verified). So kernel driver is the
only reliable path for DJ apps today. Revisit when WMS winmm handoff GA's; then WMS
VirtualMidiTransport becomes a third backend and ravemidi covers Win10/older Win11.

## Driver model (validated by research)

**WDM PortCls adapter + IMiniportMidi miniports, dynamic subdevices.** Exactly teVirtualMIDI's
model (Erichsen built from WDK DMusUART/MPU401 samples). NOT raw KS filter, NOT AVStream,
NOT a bus driver with child PDOs.

- One root-enumerated devnode (`Root\ravemidi`, class MEDIA, INF-installed). Shows once in
  Device Manager; ports come and go without PnP churn.
- Per port: miniport instance → `PcNewPort(CLSID_PortMidi)` → `IPort::Init` →
  `PcRegisterSubdevice(fdo, L"RavePortN", port)`. Remove via `IUnregisterSubdevice` (+ parked
  until last pin closes). Runtime registration is documented-supported (sysvad Bluetooth
  sideband does exactly this).
- Port name shown in winmm (`MIDIINCAPS.szPname`, 31 chars): no COMPONENTID handler → wdmaud
  falls back to the device interface **FriendlyName** → set per subdevice via
  `IoSetDeviceInterfacePropertyData(DEVPKEY_DeviceInterface_FriendlyName)`.
- **One-way ports are free**: winmm lists midiIn per MIDI *capture* pin and midiOut per
  *render* pin independently. A filter descriptor with only a capture pin = input-only port
  (the echo-killer); only render = output-only; both = bidi; two subdevices cross-wired = loopback.
- Pin data range: `KSDATARANGE_MUSIC{ KSDATAFORMAT_TYPE_MUSIC, SUBTYPE_MIDI, SPECIFIER_NONE,
  KSMUSIC_TECHNOLOGY_PORT }`. Categories: KSCATEGORY_AUDIO + RENDER/CAPTURE (omit SYNTHESIZER).
- PortCls does the heavy lifting (pin state machine, KSPROPERTY_PIN_*, IRP queue/cancel,
  KSMUSICFORMAT packing). We implement: filter descriptor, `IMiniportMidi::Init/NewStream`,
  `IMiniportMidiStream::SetFormat/SetState/Read/Write`, capture notify via
  `IPortMidi::Notify(ServiceGroup)` (DISPATCH_LEVEL-safe).

### Reference code
- DMusUART sample (miniport.cpp/mpu.cpp) — the base teVirtualMIDI used; removed from
  Windows-driver-samples HEAD; fetch from repo history (MIT) or uri247/wdk81 mirror.
- sysvad `BthhfpDevice.cpp` — dynamic subdevice register/unregister + FriendlyName.
- ReactOS portcls (`drivers/wdm/audio/backpln/portcls/`) — port-driver internals when behavior
  is unclear.
- microsoft/MIDI `src/api/Libs/MidiKs*` — consumer-side KS MIDI pin usage.

## Control plane (rave-mate ↔ driver)

Private device interface on the FDO: `\\.\RaveMidiCtl` (custom GUID; Go probe already stubbed
in `internal/midi/ravemidi_windows.go`). IOCTLs (METHOD_BUFFERED unless noted):

| IOCTL | In | Out |
|---|---|---|
| CREATE_PORT | `{u32 ver; u32 kind: IN_ONLY/OUT_ONLY/BIDI/LOOPBACK; WCHAR name[32]}` | `{u32 portId}` |
| DESTROY_PORT | `{u32 portId}` | — |
| WRITE (app→port capture side) | `{u32 portId; u8 midi[]}` raw bytes, message-aligned | — |
| READ (render side→app) | `{u32 portId}` | pended IRP, completes with midi bytes (inverted call) |

Data path: DJ-app render pin `Write` → per-port ring FIFO → complete pended READ. rave-mate
WRITE → FIFO → `IPortMidi::Notify` → port pulls via stream `Read` → wdmaud completes winmm
client. Loopback = render FIFO wired to sibling capture. For rave-mate's one-way use, only
CREATE_PORT(OUT_ONLY→apps-see-input) + WRITE matter; READ enables future bidi/learn.

Security: control device SDDL = SYSTEM/Admins full + INTERACTIVE read/write (all IOCTLs
demand ≤ FILE_READ|WRITE_DATA), so the unelevated desktop app can create ports — same
posture as teVirtualMIDI; port/mirror caps bound pool. Names sanitized (len ≤31).
Multi-client on the winmm side: raise pin instance cap >1, merge at **message granularity**
(parse running status/sysex before interleave).

## Origin-aware routing (user requirement 2026-07-10): no self-echo, full duplex

Controllers that RECEIVE feedback (LEDs/motor faders) from the DJ software must keep
working. Rule: every port has two sides — **app side** (winmm endpoints the DJ software
opens) and **mate side** (IOCTL) — and data only crosses sides, never reflects:

| Kind | app sees | app render goes to | app capture fed by | echo? |
|---|---|---|---|---|
| OUT_ONLY | input only | — | mate WRITE | impossible (no app output) |
| IN_ONLY | output only | mate READ | — | impossible (no app input) |
| BIDI | in + out | mate READ (→ rave-mate → physical controller LEDs) | mate WRITE (controller input) | never — sides are distinct emitters |
| LOOPBACK | in + out | its own capture | its own render | cross-process only (v2: self-echo suppressed by owning pid) |

So "one-way" generalizes to "knows the emitter": BIDI is the echo-free duplex cable —
DJ software never re-hears its own output, rave-mate never re-reads its own WRITE, yet
LED feedback flows DJ-app → mate → physical controller. Implemented: miniport render
streams push FromApp (never ToApp) except LOOPBACK; IOCTL WRITE pushes ToApp only.
teVirtualMIDI fallback is origin-aware the same way (SendData = app input; app output =
callback) — Go binding needs a `OpenVirtualBidi` (callback via TE_VM flags 1|2|12) so the
fallback also carries feedback; today it's TX_ONLY.

## Mirror groups (user request 2026-07-10): driver-level controller splitter

Goal: physical controller's MIDI duplicated to N virtual ports **in the driver**, so DJ
software keeps receiving controller input even if rave-mate crashes/exits. Also dissolves
winmm single-client: N apps each get their own mirror port.

- `IOCTL_RAVEMIDI_CREATE_MIRROR {sourceInterfacePath, outputs: [name…]}` — driver opens the
  hardware filter's MIDI **capture pin as a kernel KS client** (ZwCreateFile w/ KSPIN_CONNECT
  on the source device interface + IOCTL_KS_READ_STREAM; same wire protocol microsoft/MIDI's
  MidiKs uses from user mode), parses KSMUSICFORMAT records, pushes bytes into each mirror
  port's ToApp FIFO.
- Persistence: mirror config stored under the driver's service registry key (`Parameters\
  Mirrors`), re-armed at boot + on device arrival via `IoRegisterPlugPlayNotification`
  (KSCATEGORY_CAPTURE interface-change). Fully rave-mate-independent once configured.
- Because the driver holds the (single-client) hardware pin, apps can no longer open the raw
  port — by design; they use the mirror ports. Releasing the mirror releases the hardware.
- rave-mate side: "Splitter" card in the MIDI tab (webui) = config UI only (list hardware
  inputs, create/remove mirror groups via ctl→daemon→IOCTL); no data path through rave-mate.
- Phasing: v1 driver ships CREATE/DESTROY_PORT + WRITE/READ (one-way ports); v1.1 adds
  mirror groups (kernel KS-client tap is the riskiest new surface: format negotiation,
  pin arrival/removal races, IRP streaming at DISPATCH).

## Implementation status

Virtual-port core + mirror-tap implemented; builds green in CI (compile + link +
InfVerif DCH gate). Files: adapter.cpp (control plane, dynamic subdevice register/
unregister, winmm FriendlyName stamp via IoSetDeviceInterfacePropertyData, cancel-safe
IOCTL_READ IO_CSQ, IRP_MJ_CLEANUP), miniport.cpp (per-kind PCFILTER_DESCRIPTOR +
IMiniportMidi/Stream + FIFO data path), mirror.cpp (KS-client controller tap),
fifo.cpp (NX ring), kalloc.cpp (ExAllocatePool2 operators), guids.cpp (INITGUID).

**Needs on-hardware bring-up before trusted** (cannot be unit-tested): the whole thing
requires a test-signed load on a real machine. Specifically validate — (a) a created
port appears in winmm `midiInGetDevCaps`/`midiOutGetDevCaps` with the given name;
(b) OUT_ONLY shows input-only (no echo endpoint); (c) BIDI carries LED feedback without
self-echo; (d) the mirror KS-client actually instantiates a hardware controller's
capture pin (KSPIN_CONNECT/format may need per-device tweaks — open-time failures return
cleanly, so this is safe to iterate) and fans MIDI to its output ports with rave-mate
killed. Dev loop = Hyper-V, Secure Boot off, testsigning on, `devgen /add /bus ROOT`.

### On-hardware bring-up log (2026-07-10, dev box, test-signed, Secure Boot OFF, HVCI off)

VERIFIED WORKING end-to-end up to the KS interface:
- Driver test-signed (self-cert), staged (`pnputil /add-driver`), devnode created. `devgen`
  alone leaves an INERT node (Class/Service empty, no driver bind) — must use `devcon install
  ravemidi.inf Root\ravemidi` which both creates the node AND runs driver matching. After that:
  PnP status OK, service=ravemidi running, FriendlyName "rave-mate virtual MIDI adapter".
- StartDevice ran: `\Device\RaveMidiCtl` + `\GLOBAL??\RaveMidiCtl` symlink exist (verified via
  NtQueryDirectoryObject). Go IOCTL client opens `\\.\RaveMidiCtl` OK.
- `IOCTL_RAVEMIDI_CREATE_PORT` (OUT_ONLY) SUCCEEDS → returns a port id.
- `PcRegisterSubdevice` correctly registers the subdevice's KS device interfaces with the
  RIGHT categories: our devnode `ROOT#MEDIA#0006` gains `#{6994ad04…}` (KSCATEGORY_AUDIO) +
  `#{65e8773d…}` (KSCATEGORY_CAPTURE) while a port is held open, matching FilterOutOnly's
  Cats={AUDIO,CAPTURE}. (No RENDER interface — correct for out-only. Gotcha: 65e8773**d**=CAPTURE,
  65e8773**e**=RENDER, 6994ad04=AUDIO — easy to swap.)

REMAINING BUG (the actual last mile): despite the correctly-registered CAPTURE KS filter,
**wdmaud does NOT create a winmm midiIn device from it** — `midiInGetNumDevs()` unchanged, port
name absent from the midiIn list. So the break is wdmaud's KS-filter→winmm-MIDI enumeration, one
layer BELOW our code. Everything WE register is right; wdmaud is rejecting/ignoring the filter's
MIDI pin. Diagnose with a KERNEL DEBUGGER (WinDbg `!ks`, wdmaud WPP trace) or compare the live
filter's pin/property responses against teVirtualMIDI's (which surfaces fine on the same box) —
NOT with blind CI rebuild+resign+reinstall cycles. Leading hypotheses to check on the target:
(1) wdmaud probes KSPROPERTY_PIN_CTYPES / pin factory and our miniport's answer or NewStream
init fails, so wdmaud drops the filter; (2) the MIDI data-range/pin-communication combo isn't
what wdmaud accepts for a MIDI capture pin; (3) dynamic (post-StartDevice) subdevice needs an
extra kick for wdmaud (which attached at start) to re-enumerate the new filter. Local install
scripts + bring-up test live in driver/ravemidi/build/testsign/ (gitignored) and
internal/midi/ravemidi_manual_test.go (`-tags manual`).

## HVCI + INF gates (release-blocking, per 2026 policy)

- `NonPagedPoolNx` everywhere (POOL_NX_OPTIN), no W^X, no dynamic code, `MdlMappingNoExecute`.
  Verify: `verifier /flags 0x02000000 /driver ravemidi.sys` + functional test with Memory
  Integrity ON (default-on on new Win11).
- INF must pass `InfVerif /h` (declarative/DCH, no coinstallers) — attestation rejects otherwise
  since Apr 2025. Every shipped file referenced by the INF (Feb 2026 policy).
- `.pdb` goes in the submission cab.

## Signing & distribution (research verdict)

- **EV code-signing cert REQUIRED to register** Partner Center Hardware Program (registration
  only; per-submission signing may use ordinary Authenticode registered to the account).
  **Azure Trusted/Artifact Signing is NOT accepted** — no EV, no kernel-mode. Sole proprietor
  viable (CA/B sole-prop EV; SSL.com tier). Key on FIPS token/HSM (no soft PFX since 2023).
- Flow: build → cab (.inf+.sys+.pdb, subfolder) → signtool sign cab → Partner Center
  **attestation** submit (free, ~10min–1h) → Microsoft embeds SHA-2 sig in .sys + returns
  MS-signed .cat → fold into installer. Attestation covers Win10/11 client x64+arm64
  (arm64 needs NTARM64 INF sections). **Windows Server: not supported** (WHQL only — skip,
  like Dokany/WinFsp). WU retail distribution is off the table for attestation (fine —
  GitHub installer distribution unaffected).
- Plain EV Authenticode on the .sys is NOT loadable on SecureBoot Win10 1607+ — the MS
  signature is what gates load; cross-signing trust fully removed Apr 2026.
- CI automation: Microsoft Hardware Dashboard API (Entra client-credentials; SDCM reference
  client; Oracle/WinFsp do exactly this). Add a manual-trigger GitHub Actions job later.
- **Risk**: Partner Center identity re-verification sweeps (Oct 2025+) locked out WireGuard/
  VeraCrypt (Apr 2026, appeals ~60d). Keep verification current; never gate a release on a
  same-week Partner Center action.
- AGPL note: forks can't reuse our MS signature — document (own Partner Center account or
  test-signing); consider Wintun pattern (permissively-granted prebuilt signed binary) later.

## Dev loop

Hyper-V Gen-2 VM: Secure Boot off, `bcdedit /set testsigning on`, self-signed cert in
Root+TrustedPublisher, `Inf2Cat`+`signtool`, `pnputil /add-driver ravemidi.inf /install`,
devnode via `devgen /add /bus ROOT /hardwareid Root\ravemidi` (or SwDeviceCreate from
rave-mate later). Toolchain: VS Build Tools (VCTools + Spectre libs) + NuGet WDK packages
(`Microsoft.Windows.SDK.CPP{,.x64}` + `Microsoft.Windows.WDK.x64`, one pinned version) —
no EWDK ISO. CI: `windows-2022` runner (in-box WDK) or NuGet restore on windows-latest;
CodeQL `windows-drivers` packs = the static-analysis gate (SDV retired).

## Layout + integration

```
driver/ravemidi/
  ravemidi.vcxproj, ravemidi.inf, ravemidi.ddf
  ioctl.h            # shared IOCTL/struct defs (mirrored in Go)
  adapter.cpp        # DriverEntry, AddDevice/StartDevice, control device, port manager
  miniport.{h,cpp}   # IMiniportMidi + stream + filter descriptors (per-kind pin sets)
  fifo.{h,cpp}       # message-granular ring FIFO
```

Go side: `internal/midi/ravemidi_windows.go` — probe `\\.\RaveMidiCtl`; backend order in
`openOut`: ravemidi → teVirtualMIDI → error. IOCTL structs mirror ioctl.h (fixed layout,
no cgo — DeviceIoControl via syscall).

Estimate: 4–7k LOC kernel C++; weeks to prototype, months to harden. Riskiest: signing ops,
WMS 25H2 coexistence (dynamic KS subdevice visibility bug had a 2026-04 controlled-rollout
fix — test early), unregister-while-open races, multi-client merge.

## Managed-input autonomy — implementation notes (2026-07-11, task #78)

Fixes the "forwarding dies with rave-mate" bug: taps/ports used to be owned by the
creating control handle (`RaveMirrorDestroyForFile` on CLOSE). Managed inputs are
driver-owned and persist.

Kernel-side pieces (`driver/ravemidi/`):

- `config.{h,cpp}` — `RAVEMIDI_CONFIG` persisted as REG_BINARY `"Config"` under
  `<RegistryPath>\Parameters` (path captured in DriverEntry, pool copy). ZwCreateKey/
  ZwSetValueKey/ZwQueryValueKey at PASSIVE. `RaveConfigSanitize` hard-validates:
  version, `InputCount<=8`, `OutCount<=4`, NUL-pads every WCHAR field (blobs become
  bytewise-comparable → config diff is `RtlCompareMemory`), clamps Thru/Feedback to
  0/1, rejects empty/dup Ids and inputs with neither SourceMatch nor SourceIface,
  zeroes trailing inputs (no stack garbage to registry).
- `managed.{h,cpp}` — engine. One passive system worker owns all state (KMUTEX; KMUTEX
  not FAST_MUTEX because binding does Zw* file/registry I/O which needs PASSIVE).
  `RaveManagedApply` diffs desired vs live by `Id`: unchanged inputs keep their live
  tap (zero interruption), changed recreate, removed tear down. Inputs are pool
  allocations behind a pointer array so the tap dead-callback ctx stays stable across
  reorders. Worker wakes on: PnP interface-change notification
  (KSCATEGORY_CAPTURE + INCLUDE_EXISTING), feedback tee kicks, apply, and the nearest
  retry deadline (idle engine blocks indefinitely — no polling). Backoff 1s/2s/5s/10s∞.
  Ports whose destroy returns DEVICE_BUSY (open winmm pin, legacy mirror ref) go to a
  graveyard reaped by the worker. Engine stops on IRP_MN_STOP/SURPRISE_REMOVAL/
  REMOVE_DEVICE (new `RavePnpDispatch` hook chains to `PcDispatchIrp`) and re-arms in
  StartDevice via `RaveManagedBoot` (init + load + apply).
- `mirror.{h,cpp}` refactor — KS client split into an owner-less `RAVE_TAP`
  (open filter → probe MIDI capture pin → RUN → read-pump thread) reused by legacy
  mirrors (OnDead=NULL → retry reads forever, exact old behavior) and managed inputs
  (OnDead → 3 consecutive read failures mark the tap dead; worker closes + rebinds).
  Added render-pin client for feedback: `RaveKsOpenRenderPin` (KSPIN_DATAFLOW_IN,
  GENERIC_WRITE, stepped to RUN) + `RaveKsWriteMidi` (one KSMUSICFORMAT record per
  write, ≤512B, DWORD-padded, IOCTL_KS_WRITE_STREAM header in the "out" buffer like
  the read path).
- Feedback tee: `RAVE_PORT` grew a third bounded FIFO `Feedback` + `FeedbackArm`.
  The reserved port's render-stream `Write` (≤DISPATCH) pushes FromApp as before AND,
  when armed, into `Feedback` + `RaveManagedKickFeedback()` (KeSetEvent, DISPATCH-safe).
  Worker drains to the render pin at PASSIVE. Arm only while the render pin is bound;
  stale bytes are discarded before arming (no LED-state replay). IOCTL_READ still sees
  FromApp — the tee never consumes.
- Ownerless ports: `CreatePort` takes `creator=NULL` (`RavePortCreateOwnerless`);
  `DestroyPortsForFile`/`RaveMirrorDestroyForFile` naturally skip them (f never NULL);
  IOCTL DESTROY_PORT on them → ACCESS_DENIED (CreatorFile NULL ≠ caller).
  CSQ peek now honors a PFILE_OBJECT context so IRP_MJ_CLEANUP cancels exactly the
  closing handle's pended READs — including READs rave-mate pended on managed ports it
  doesn't own (else the file object never fully closes).
- Self-tap guard: FriendlyName matching skips interfaces whose symlink tail starts
  with `RavePort` (our own stamped names would otherwise match a SourceMatch and loop).

Known limits / bring-up list:
- All KS streaming paths (tap read, render write) still need on-hardware verification;
  feedback (render-pin) path is entirely untested on hardware.
- Feedback drain shares the bind worker: a long bind attempt (ZwCreateFile on a wedged
  device) can add latency to LED feedback. Split to a second thread if it matters.
- `RaveFifoPop` isn't message-aligned: a >512B sysex chunk can split across two
  KSMUSICFORMAT records on the render pin. Most devices tolerate byte-stream records.
- 8 inputs × up to 5 ports > RAVEMIDI_MAX_PORTS(16) subdevices: port creation for
  late inputs parks in the retry loop until slots free up.
- Local build needs the WDK VS toolset shim (see build notes in session summary); CI
  (windows-2022) unaffected.

## Protocol v2 (2026-07-11): loop-free duplex fan-outs + filter + trace

User correction of intent: the driver's job is bidirectional MIDI WITHOUT loops -
no client (device or app) may receive its own bytes back. Changes:

- **Managed fan-outs are BIDI** (were OUT_ONLY): DJ software gets controller MIDI
  down AND sends LED feedback up. Every armed port's render bytes tee into its own
  `Feedback` FIFO; the worker drains all of an input's FIFOs through **per-port MIDI
  framers** (`framer.cpp` - short-message/running-status/sysex/realtime state machine)
  so interleaved writers never split a message, one `RaveKsWriteMidi` per complete
  message. Loop-free structurally: BIDI has no internal render->capture path.
- **LOOPBACK self-echo suppression**: streams record `PsGetCurrentProcessId()` at
  NewStream (pin creation runs in the opener's context); a loopback render write from
  the capture-owning pid is dropped + counted (`LoopSuppressed`, traced as LoopDrop).
  Caveat: if Windows MIDI Services (midisrv) proxies clients, all pins share midisrv's
  pid - loopback suppression then over-drops; managed BIDI ports are the supported
  path there.
- **`RAVEMIDI_INPUT_CFG.Filter`** (bump to PROTOCOL_VERSION 2, persisted blob size
  changes - old blobs load as NOT_FOUND, rave-mate re-syncs): per-input mask dropping
  aftertouch/poly-pressure/pitch-bend/active-sensing/clock on the tap->fan-out path
  only (reserved port unfiltered). Motivation: Rekordbox MIDI-learn latches onto
  keybed channel pressure -> "any key fires the binding" + "play only while held"
  (press fires it, release-pressure fires it again).
- **`IOCTL_RAVEMIDI_QUERY_TRACE` (0x80B)**: per-port 128-entry ring (seq, interrupt
  time, dir, len, first 12 bytes) - dirs TapRaw(0, raw KS read pre-parse, lands in the
  tap's first fan-in port)/ToApp/ReadPop/FromApp/FeedbackOut/LoopDrop. Purpose: pin
  down on-hardware wire bugs (e.g. wrong bytes at which hop) live from rave-mate's
  MIDI monitor without KD.
