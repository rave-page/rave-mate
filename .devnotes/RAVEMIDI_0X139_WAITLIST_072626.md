# ravemidi 5.0.0.4 — 0x139 CORRUPT_LIST_ENTRY (dispatcher lifetime)

4th ravemidi-class bugcheck on the dev box (after 0x50 UAF, 0x50 usbaudio overflow, 0x133 wedge).

## Dump signature

- 0x139 arg1=3 (corrupt LIST_ENTRY) in `nt!KiProcessThreadWaitList` — ETW APC insertion hits
  `RemoveEntryList` on a severed `KWAIT_BLOCK`.
- Trigger: pending pnputil update cycling the devnode (STOP→START) while rave-mate's midi child
  polls `\\.\RaveMidiCtl`.

## F1 (HIGH — the crash): dispatcher objects re-initialized under live waiters

`ManagedInit` ran `KeInitializeMutex(&g_M.Lock)` / `KeInitializeEvent(&g_M.Wake)` on EVERY
re-START (`RaveStartDevice`→`RaveManagedBoot`; also CANCEL_STOP/CANCEL_REMOVE re-arm). Consumers
peeked `g_M.Started` UNLOCKED (`RaveManagedApply`/`Query`/`GraveOrphan`/`KickFeedback`) — TOCTOU
window spans the whole device restart.

```
IOCTL thread                       PnP path
------------                       --------
Apply: sees Started=TRUE
Apply: KeWait(&g_M.Lock) [BLOCKS]  Stop: joins worker, teardown (holds Lock)
        |                          Stop: Started=FALSE ... releases Lock
        |  (still on the wait      Start: ManagedInit
        |   list — or just         KeInitializeMutex(&g_M.Lock)   <-- wipes WaitListHead
        |   past acquisition)          under the live KWAIT_BLOCK
        v
severed wait entry -> later KiProcessThreadWaitList RemoveEntryList -> 0x139 arg1=3
```

Fix (`managed.cpp`):
- `g_M.DispatcherInit` one-shot: `Lock`/`Wake` initialized once per driver load (g_M = zeroed
  static). `ManagedInit` resets only non-dispatcher state; `RaveManagedStop` never touches the flag.
- Engine-lifetime verdicts race-free: `Apply`/`Query`/`GraveOrphan` now reach `Started`/`Dead`
  under the mutex (acquire → re-check → proceed/back-out). `Stop` flips `Started=FALSE` +
  `Dead=TRUE` under the mutex FIRST; `ManagedInit` wipes state under it; `Started=TRUE` published
  under it.
- `RaveManagedKickFeedback` runs at DISPATCH (miniport render Write) — cannot wait on a KMUTEX.
  Its unlocked `Started` peek + `KeSetEvent` is safe ONLY because the KEVENT is init-once now
  (event re-init was its hazard); worst case = spurious wake, worker tolerates. Documented in-code.

## F2 (MEDIUM): ObReferenceObjectByHandle ignored → unjoinable zombie thread

- `managed.cpp` MWorker create + `mirror.cpp` TapThread create ignored the ObRef status (C6031).
  On failure `ThreadObj` stays NULL → Stop/Close skip the join → zombie worker waits on g_M
  dispatcher objects forever (feeds F1) / tap pump UAFs its freed `RAVE_TAP`.
- Fix: check status; on failure signal `Stop`, complete the pump's pending read (tap:
  `SetPinState(STOP)`), join via `ZwWaitForSingleObject` on the still-open handle (ntifs-only
  export — own extern proto, PsGetCurrentProcessId pattern), only then tear down/free. Join
  failure (impossible on a valid kernel handle) leaks rather than frees under a live thread.

## F3 (documented only, NOT fixed)

`mirror.cpp RaveTapClose`: unbounded pump join; managed callers hold `g_M.Lock` across it — a dead
KS pin that never completes the pending read wedges the engine + every Lock waiter (0x133/hang
family). Tracked comment added; separate fix wave.

## PREfast (the 4 pre-existing: C6031/C6386/C6262/C6387)

| Warning | Site | Resolution |
|---|---|---|
| C6031 | managed.cpp / mirror.cpp ObRef | = F2 (checked + handled) |
| C6031 | adapter.cpp `StampFriendlyName` | status inspected; best-effort continue |
| C6031 | managed.cpp `IoRegisterPlugPlayNotification` | status inspected; Notify nulled on failure |
| C6262 | managed.cpp `TryBindRender` (1104B stack) | discard sink 512→64B (FifoPop = byte drain, loop unaffected) |
| C6386 | framer.cpp sysex append | bound-guarded append (invariant made provable) |
| C6387 | miniport.cpp `port->Init(..., nullptr)` | proven-FP suppress + comment (constant arg — `_Analysis_assume_` can't express it; sysvad pattern, live-proven 5.0.0.x) |

Also C28170 (FbEmit paged-seg missing PAGED_CODE) → `PAGED_CODE()` added.

Verification: local WDK build (memory recipe, PREfast ON via forceafter targets +
`/analyze:WX-`): `/W4 /WX` compile clean, **zero C6xxx**. 23 C28xxx remain — pre-existing
annotation debt (dispatch-type/paged-seg/SAL-consistency), only visible under the local drivers
ruleset, untouched by design (surgical scope).

## Ship

`build/testsign/` (gitignored): ravemidi.sys + .cat signed (`/fd sha256`, test cert 5ABE42A8…),
Inf2Cat 10_X64 clean, INF DriverVer `07/26/2026,5.0.0.4`. NOT installed — activate via
`pnputil /add-driver build/testsign/ravemidi.inf /install` + reboot (never live restart-device).
