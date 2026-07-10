// Managed-input engine implementation. One passive worker thread owns all
// binding: PnP interface-change notifications and feedback writes wake it; it
// creates driver-owned ports, opens capture taps (mirror.cpp) with capped
// backoff (1s/2s/5s then 10s forever), resolves render pins for LED feedback,
// and tears everything down cleanly on config change / device STOP/REMOVE.
// SPDX-License-Identifier: AGPL-3.0-or-later

#include <portcls.h>
#include <ksmedia.h>
#include <ntstrsafe.h>
#include <devpkey.h>
#include "ioctl.h"
#include "miniport.h"
#include "mirror.h"
#include "config.h"
#include "managed.h"

#ifndef LOCALE_NEUTRAL
#define LOCALE_NEUTRAL 0
#endif

#define RAVE_TAG RAVEMIDI_POOL_TAG
#define M_SEC 10000000ull                 // 100ns units
#define M_FRIENDLY_CCH 128
#define M_GRAVE_MAX (RAVEMIDI_MAX_INPUTS * (RAVEMIDI_MAX_MIRROR_OUT + 1))

typedef struct _RAVE_MINPUT {
    RAVEMIDI_INPUT_CFG Cfg;               // sanitized copy (bytewise-comparable)
    BOOLEAN PortsReady;
    RAVE_PORT* Reserved;                  // BIDI "<Name> (rave-mate)"
    RAVE_PORT* Outs[RAVEMIDI_MAX_MIRROR_OUT];
    RAVE_TAP* Tap;
    volatile LONG TapDead;                // set by tap thread; worker rebinds
    BOOLEAN Bound;
    BOOLEAN FeedbackBound;
    ULONG RetryCount;                     // attempts since last full success
    ULONG BackoffIdx;                     // 0..3 -> 1s,2s,5s,10s
    ULONGLONG NextTry;                    // KeQueryInterruptTime deadline
    WCHAR BoundIface[RAVEMIDI_MAX_IFACE];
    HANDLE RFilter;                       // render-pin handles (Feedback=1)
    PFILE_OBJECT RFilterFo;
    HANDLE RPin;
    PFILE_OBJECT RPinFo;
} RAVE_MINPUT;

static struct {
    BOOLEAN Started;
    BOOLEAN Dead;                         // set under Lock at Stop: Apply refuses
    PDEVICE_OBJECT Fdo;
    KMUTEX Lock;                          // passive-level; guards In/Count/Grave
    KEVENT Wake;                          // auto-reset worker kick
    volatile LONG Stop;
    PVOID ThreadObj;
    PVOID Notify;                         // IoRegisterPlugPlayNotification handle
    ULONG Count;
    RAVE_MINPUT* In[RAVEMIDI_MAX_INPUTS]; // pointers: stable tap-callback ctx across reorders
    ULONG GraveCount;                     // ports whose destroy was DEVICE_BUSY — reaped later
    ULONG Grave[M_GRAVE_MAX];
} g_M;

static const ULONGLONG kBackoff[4] = { 1 * M_SEC, 2 * M_SEC, 5 * M_SEC, 10 * M_SEC };

static VOID MLock()
{
    KeWaitForSingleObject(&g_M.Lock, Executive, KernelMode, FALSE, nullptr);
}

static VOID MUnlock()
{
    KeReleaseMutex(&g_M.Lock, FALSE);
}

static SIZE_T MWcsLen(PCWSTR s)  // no CRT dep
{
    SIZE_T n = 0;
    while (s[n]) {
        n++;
    }
    return n;
}

VOID RaveManagedKickFeedback()
{
    // <= DISPATCH (miniport render Write). Started implies Wake is initialized.
    if (g_M.Started) {
        KeSetEvent(&g_M.Wake, IO_NO_INCREMENT, FALSE);
    }
}

// Tap thread reports death; input outlives the callback (frees only after
// RaveTapClose joins that thread). No locks here — worker does the teardown.
static VOID MTapDead(PVOID Ctx)
{
    RAVE_MINPUT* in = (RAVE_MINPUT*)Ctx;
    InterlockedExchange(&in->TapDead, 1);
    KeSetEvent(&g_M.Wake, IO_NO_INCREMENT, FALSE);
}

static DRIVER_NOTIFICATION_CALLBACK_ROUTINE MPnpNotify;
static NTSTATUS MPnpNotify(PVOID NotificationStructure, PVOID Context)
{
    UNREFERENCED_PARAMETER(NotificationStructure);
    UNREFERENCED_PARAMETER(Context);
    KeSetEvent(&g_M.Wake, IO_NO_INCREMENT, FALSE);  // arrival/removal -> re-evaluate binds
    return STATUS_SUCCESS;
}

static VOID ScheduleRetry(RAVE_MINPUT* in)
{
    in->NextTry = KeQueryInterruptTime() + kBackoff[in->BackoffIdx];
    if (in->BackoffIdx < RTL_NUMBER_OF(kBackoff) - 1) {
        in->BackoffIdx++;
    }
}

// -------- interface matching --------------------------------------------------
// Our own subdevice symlinks end "\RavePortN" — never self-tap (a SourceMatch
// could otherwise hit our own stamped FriendlyNames and loop MIDI back).
static BOOLEAN IsOwnIface(PCWSTR sym)
{
    PCWSTR tail = sym;
    for (PCWSTR c = sym; *c; c++) {
        if (*c == L'\\') {
            tail = c + 1;
        }
    }
    static const WCHAR pfx[] = L"RavePort";
    for (ULONG i = 0; i < RTL_NUMBER_OF(pfx) - 1; i++) {
        if (RtlUpcaseUnicodeChar(tail[i]) != RtlUpcaseUnicodeChar(pfx[i])) {
            return FALSE;
        }
    }
    return TRUE;
}

static BOOLEAN NameContains(PCWSTR hay, PCWSTR needle)  // case-insensitive substring
{
    SIZE_T nl = MWcsLen(needle);
    if (nl == 0) {
        return FALSE;
    }
    SIZE_T hl = MWcsLen(hay);
    if (nl > hl) {
        return FALSE;
    }
    for (SIZE_T i = 0; i + nl <= hl; i++) {
        SIZE_T j = 0;
        while (j < nl && RtlUpcaseUnicodeChar(hay[i + j]) == RtlUpcaseUnicodeChar(needle[j])) {
            j++;
        }
        if (j == nl) {
            return TRUE;
        }
    }
    return FALSE;
}

#pragma code_seg("PAGE")
static BOOLEAN IfaceFriendlyName(PCWSTR sym, WCHAR* buf, ULONG cch)
{
    PAGED_CODE();
    UNICODE_STRING u;
    RtlInitUnicodeString(&u, sym);
    DEVPROPTYPE type = 0;
    ULONG req = 0;
    NTSTATUS st = IoGetDeviceInterfacePropertyData(&u, &DEVPKEY_DeviceInterface_FriendlyName,
                                                   LOCALE_NEUTRAL, 0, cch * sizeof(WCHAR),
                                                   buf, &req, &type);
    if (!NT_SUCCESS(st) || type != DEVPROP_TYPE_STRING) {
        return FALSE;
    }
    buf[cch - 1] = 0;
    return TRUE;
}
#pragma code_seg()

// -------- ports ----------------------------------------------------------------
#pragma code_seg("PAGE")
static VOID GraveOrDestroy(ULONG portId)
{
    PAGED_CODE();
    NTSTATUS st = RavePortDestroyById(portId);
    // BUSY = a winmm client still holds a pin (or a legacy mirror refs it) —
    // park the id, the worker reaps it once released.
    if (st == STATUS_DEVICE_BUSY && g_M.GraveCount < M_GRAVE_MAX) {
        g_M.Grave[g_M.GraveCount++] = portId;
    }
}

static VOID ReapGraveyard()
{
    PAGED_CODE();
    ULONG i = 0;
    while (i < g_M.GraveCount) {
        NTSTATUS st = RavePortDestroyById(g_M.Grave[i]);
        if (st == STATUS_DEVICE_BUSY) {
            i++;
            continue;
        }
        g_M.Grave[i] = g_M.Grave[--g_M.GraveCount];  // done (or gone) — swap-remove
    }
}

static NTSTATUS CreateInputPorts(RAVE_MINPUT* in)
{
    PAGED_CODE();
    WCHAR nm[RAVEMIDI_MAX_NAME];
    NTSTATUS st = RtlStringCchPrintfW(nm, RTL_NUMBER_OF(nm), L"%s (rave-mate)", in->Cfg.Name);
    if (st != STATUS_SUCCESS && st != STATUS_BUFFER_OVERFLOW) {  // overflow = truncated, fine
        return st;
    }
    st = RavePortCreateOwnerless(RaveMidiPortBidi, nm, &in->Reserved);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    for (ULONG i = 0; i < in->Cfg.OutCount; i++) {
        PCWSTR name = in->Cfg.OutNames[i];
        if (!name[0]) {
            RtlStringCchPrintfW(nm, RTL_NUMBER_OF(nm), L"%s Out %lu", in->Cfg.Name, i + 1);
            name = nm;
        }
        st = RavePortCreateOwnerless(RaveMidiPortOutOnly, name, &in->Outs[i]);
        if (!NT_SUCCESS(st)) {  // rollback (e.g. RAVEMIDI_MAX_PORTS exhausted) — retried later
            for (ULONG j = 0; j < i; j++) {
                GraveOrDestroy(in->Outs[j]->Id);
                in->Outs[j] = nullptr;
            }
            GraveOrDestroy(in->Reserved->Id);
            in->Reserved = nullptr;
            return st;
        }
    }
    return STATUS_SUCCESS;
}
#pragma code_seg()

// -------- binding ----------------------------------------------------------------
#pragma code_seg("PAGE")
static VOID DropRender(RAVE_MINPUT* in)
{
    PAGED_CODE();
    if (in->Reserved) {
        InterlockedExchange(&in->Reserved->FeedbackArm, 0);
    }
    if (in->RPinFo || in->RFilterFo) {
        RaveKsCloseRenderPin(in->RFilter, in->RFilterFo, in->RPin, in->RPinFo);
        in->RFilter = nullptr;
        in->RFilterFo = nullptr;
        in->RPin = nullptr;
        in->RPinFo = nullptr;
    }
    in->FeedbackBound = FALSE;
}

static NTSTATUS OpenTapOn(RAVE_MINPUT* in, PCWSTR iface)
{
    PAGED_CODE();
    if (MWcsLen(iface) >= RAVEMIDI_MAX_IFACE) {
        return STATUS_INVALID_PARAMETER;  // can't record in BoundIface — skip
    }
    RAVE_PORT* outs[RAVEMIDI_MAX_MIRROR_OUT + 1];
    ULONG n = 0;
    outs[n++] = in->Reserved;             // rave-mate always sees the controller
    if (in->Cfg.Thru) {                   // Thru: device capture -> all out ports
        for (ULONG i = 0; i < in->Cfg.OutCount; i++) {
            outs[n++] = in->Outs[i];
        }
    }
    InterlockedExchange(&in->TapDead, 0);
    NTSTATUS st = RaveTapOpen(iface, outs, n, MTapDead, in, &in->Tap);
    if (NT_SUCCESS(st)) {
        RtlStringCchCopyW(in->BoundIface, RTL_NUMBER_OF(in->BoundIface), iface);
        in->Bound = TRUE;
    }
    return st;
}

static VOID TryBindCapture(RAVE_MINPUT* in)
{
    PAGED_CODE();
    in->RetryCount++;
    if (in->Cfg.SourceIface[0]) {
        // exact symlink from config — still vetted against live enumeration
        if (RaveIsKnownIface(in->Cfg.SourceIface, FALSE)) {
            OpenTapOn(in, in->Cfg.SourceIface);
        }
    } else {
        PWSTR list = nullptr;
        if (NT_SUCCESS(IoGetDeviceInterfaces(&KSCATEGORY_CAPTURE, nullptr, 0, &list)) && list) {
            for (PWSTR sym = list; *sym && !in->Bound; sym += MWcsLen(sym) + 1) {
                if (IsOwnIface(sym)) {
                    continue;
                }
                WCHAR fn[M_FRIENDLY_CCH];
                if (!IfaceFriendlyName(sym, fn, RTL_NUMBER_OF(fn))) {
                    continue;
                }
                if (!NameContains(fn, in->Cfg.SourceMatch)) {
                    continue;
                }
                OpenTapOn(in, sym);  // non-MIDI matches fail to open — try next candidate
            }
            ExFreePool(list);
        }
    }
    if (in->Bound) {
        in->BackoffIdx = 0;
        in->NextTry = 0;  // feedback bind may proceed immediately
        if (!in->Cfg.Feedback) {
            in->RetryCount = 0;
        }
    } else {
        ScheduleRetry(in);
    }
}

static VOID TryBindRender(RAVE_MINPUT* in)
{
    PAGED_CODE();
    in->RetryCount++;
    WCHAR matchBuf[M_FRIENDLY_CCH];
    PCWSTR match = in->Cfg.SourceMatch;
    if (!match[0]) {
        // exact-iface config: derive the match from the bound capture interface
        if (!IfaceFriendlyName(in->BoundIface, matchBuf, RTL_NUMBER_OF(matchBuf))) {
            ScheduleRetry(in);
            return;
        }
        match = matchBuf;
    }
    PWSTR list = nullptr;
    if (NT_SUCCESS(IoGetDeviceInterfaces(&KSCATEGORY_RENDER, nullptr, 0, &list)) && list) {
        for (PWSTR sym = list; *sym && !in->FeedbackBound; sym += MWcsLen(sym) + 1) {
            if (IsOwnIface(sym)) {
                continue;
            }
            WCHAR fn[M_FRIENDLY_CCH];
            if (!IfaceFriendlyName(sym, fn, RTL_NUMBER_OF(fn))) {
                continue;
            }
            if (!NameContains(fn, match)) {
                continue;
            }
            if (NT_SUCCESS(RaveKsOpenRenderPin(sym, &in->RFilter, &in->RFilterFo,
                                               &in->RPin, &in->RPinFo))) {
                // discard stale feedback queued while unbound, then arm the tee
                UCHAR sink[RAVEMIDI_FEEDBACK_CHUNK];
                while (RaveFifoPop(&in->Reserved->Feedback, sink, sizeof(sink)) != 0) {
                }
                InterlockedExchange(&in->Reserved->FeedbackArm, 1);
                in->FeedbackBound = TRUE;
            }
        }
        ExFreePool(list);
    }
    if (in->FeedbackBound) {
        in->RetryCount = 0;
        in->BackoffIdx = 0;
    } else {
        ScheduleRetry(in);  // capture-only still counts as Bound; keep retrying render
    }
}

static VOID DrainFeedback(RAVE_MINPUT* in)
{
    PAGED_CODE();
    UCHAR buf[RAVEMIDI_FEEDBACK_CHUNK];
    ULONG n;
    while (in->FeedbackBound &&
           (n = RaveFifoPop(&in->Reserved->Feedback, buf, sizeof(buf))) != 0) {
        if (!NT_SUCCESS(RaveKsWriteMidi(in->RPinFo, buf, n))) {
            DropRender(in);  // render pin died — resume retrying
            in->BackoffIdx = 0;
            ScheduleRetry(in);
        }
    }
}

// Close tap first (joins the pump so nothing touches the ports), then render pin,
// then destroy the driver-owned ports (busy ones -> graveyard). Frees the input.
static VOID TeardownInput(RAVE_MINPUT* in)
{
    PAGED_CODE();
    if (in->Tap) {
        RaveTapClose(in->Tap);
        in->Tap = nullptr;
    }
    in->Bound = FALSE;
    DropRender(in);
    if (in->Reserved) {
        GraveOrDestroy(in->Reserved->Id);
        in->Reserved = nullptr;
    }
    for (ULONG i = 0; i < RAVEMIDI_MAX_MIRROR_OUT; i++) {
        if (in->Outs[i]) {
            GraveOrDestroy(in->Outs[i]->Id);
            in->Outs[i] = nullptr;
        }
    }
    ExFreePoolWithTag(in, RAVE_TAG);
}

static VOID HandleTapDeath(RAVE_MINPUT* in)
{
    PAGED_CODE();
    if (in->Tap) {
        RaveTapClose(in->Tap);  // pump already exiting post-callback; join is quick
        in->Tap = nullptr;
    }
    in->Bound = FALSE;
    in->BoundIface[0] = 0;
    DropRender(in);             // device likely gone; render rebinds with capture
    InterlockedExchange(&in->TapDead, 0);
    in->BackoffIdx = 0;
    ScheduleRetry(in);
}
#pragma code_seg()

// -------- worker -----------------------------------------------------------------
#pragma code_seg("PAGE")
static KSTART_ROUTINE MWorker;
static VOID MWorker(PVOID StartContext)
{
    PAGED_CODE();
    UNREFERENCED_PARAMETER(StartContext);
    ULONGLONG waitUntil = 1;  // 0 = idle (block on Wake); first pass runs immediately
    for (;;) {
        LARGE_INTEGER to;
        PLARGE_INTEGER pto = nullptr;
        if (waitUntil) {  // sleep to the nearest retry deadline (floor 50ms)
            ULONGLONG now = KeQueryInterruptTime();
            ULONGLONG dt = (waitUntil > now) ? (waitUntil - now) : 0;
            if (dt < M_SEC / 20) {
                dt = M_SEC / 20;
            }
            to.QuadPart = -(LONGLONG)dt;
            pto = &to;
        }
        KeWaitForSingleObject(&g_M.Wake, Executive, KernelMode, FALSE, pto);
        if (g_M.Stop) {
            break;
        }
        MLock();
        ReapGraveyard();
        ULONGLONG now = KeQueryInterruptTime();
        waitUntil = (g_M.GraveCount != 0) ? (now + M_SEC) : 0;
        for (ULONG i = 0; i < g_M.Count; i++) {
            RAVE_MINPUT* in = g_M.In[i];
            if (in->TapDead) {
                HandleTapDeath(in);
            }
            if (!in->PortsReady && now >= in->NextTry) {
                if (NT_SUCCESS(CreateInputPorts(in))) {
                    in->PortsReady = TRUE;
                    in->BackoffIdx = 0;
                    in->NextTry = 0;
                } else {
                    ScheduleRetry(in);
                }
            }
            if (in->PortsReady && !in->Bound && now >= in->NextTry) {
                TryBindCapture(in);
            }
            if (in->Bound && in->Cfg.Feedback && !in->FeedbackBound && now >= in->NextTry) {
                TryBindRender(in);
            }
            if (in->FeedbackBound) {
                DrainFeedback(in);
            }
            if (!in->PortsReady || !in->Bound || (in->Cfg.Feedback && !in->FeedbackBound)) {
                ULONGLONG due = in->NextTry ? in->NextTry : now + kBackoff[0];
                if (!waitUntil || due < waitUntil) {
                    waitUntil = due;  // fully-satisfied engine blocks until a kick
                }
            }
        }
        MUnlock();
    }
    PsTerminateSystemThread(STATUS_SUCCESS);
}
#pragma code_seg()

// -------- public API ---------------------------------------------------------------
#pragma code_seg("PAGE")
static VOID ManagedInit(PDEVICE_OBJECT Fdo)
{
    PAGED_CODE();
    if (g_M.Started) {
        return;
    }
    g_M.Fdo = Fdo;
    KeInitializeMutex(&g_M.Lock, 0);
    KeInitializeEvent(&g_M.Wake, SynchronizationEvent, FALSE);
    g_M.Stop = 0;
    g_M.Dead = FALSE;
    g_M.Count = 0;
    g_M.GraveCount = 0;
    RtlZeroMemory(g_M.In, sizeof(g_M.In));
    HANDLE th = nullptr;
    if (!NT_SUCCESS(PsCreateSystemThread(&th, THREAD_ALL_ACCESS, nullptr, nullptr, nullptr,
                                         MWorker, nullptr))) {
        return;  // engine stays off; legacy IOCTL paths unaffected
    }
    ObReferenceObjectByHandle(th, THREAD_ALL_ACCESS, *PsThreadType, KernelMode,
                              &g_M.ThreadObj, nullptr);
    ZwClose(th);
    // Interface arrivals wake the worker (INCLUDE_EXISTING fires immediately for
    // already-present devices). Best-effort: the retry tick covers failure.
    IoRegisterPlugPlayNotification(EventCategoryDeviceInterfaceChange,
                                   PNPNOTIFY_DEVICE_INTERFACE_INCLUDE_EXISTING_INTERFACES,
                                   (PVOID)&KSCATEGORY_CAPTURE, Fdo->DriverObject,
                                   MPnpNotify, nullptr, &g_M.Notify);
    g_M.Started = TRUE;
}

VOID RaveManagedBoot(PDEVICE_OBJECT Fdo)
{
    PAGED_CODE();
    ManagedInit(Fdo);
    if (!g_M.Started) {
        return;
    }
    // This is what brings forwarding back after reboot with rave-mate never
    // launched: persisted config -> live apply.
    RAVEMIDI_CONFIG* c = (RAVEMIDI_CONFIG*)ExAllocatePool2(POOL_FLAG_PAGED, sizeof(*c), RAVE_TAG);
    if (!c) {
        return;
    }
    if (NT_SUCCESS(RaveConfigLoad(c))) {
        RaveManagedApply(c);
    }
    ExFreePoolWithTag(c, RAVE_TAG);
}

VOID RaveManagedStop()
{
    PAGED_CODE();
    if (!g_M.Started) {
        return;
    }
    g_M.Started = FALSE;
    if (g_M.Notify) {
        IoUnregisterPlugPlayNotificationEx(g_M.Notify);  // waits for in-flight callbacks
        g_M.Notify = nullptr;
    }
    InterlockedExchange(&g_M.Stop, 1);
    KeSetEvent(&g_M.Wake, IO_NO_INCREMENT, FALSE);
    if (g_M.ThreadObj) {
        KeWaitForSingleObject(g_M.ThreadObj, Executive, KernelMode, FALSE, nullptr);
        ObDereferenceObject(g_M.ThreadObj);
        g_M.ThreadObj = nullptr;
    }
    MLock();
    g_M.Dead = TRUE;  // Apply refuses until the next ManagedInit
    for (ULONG i = 0; i < g_M.Count; i++) {
        TeardownInput(g_M.In[i]);
        g_M.In[i] = nullptr;
    }
    g_M.Count = 0;
    ReapGraveyard();  // best-effort; leftovers freed by the unload safety net
    MUnlock();
    g_M.Stop = 0;
}

NTSTATUS RaveManagedApply(const RAVEMIDI_CONFIG* cfg)
{
    PAGED_CODE();
    if (!g_M.Started) {
        return STATUS_DEVICE_NOT_READY;
    }
    MLock();
    if (g_M.Dead) {
        MUnlock();
        return STATUS_DEVICE_NOT_READY;
    }
    // Diff by Id: unchanged inputs keep their live state (tap stays open, zero
    // MIDI interruption); changed ones recreate; removed ones tear down.
    RAVE_MINPUT* next[RAVEMIDI_MAX_INPUTS] = {};
    for (ULONG i = 0; i < cfg->InputCount; i++) {
        for (ULONG j = 0; j < g_M.Count; j++) {
            RAVE_MINPUT* live = g_M.In[j];
            if (!live ||
                RtlCompareMemory(live->Cfg.Id, cfg->Inputs[i].Id,
                                 sizeof(live->Cfg.Id)) != sizeof(live->Cfg.Id)) {
                continue;
            }
            if (RtlCompareMemory(&live->Cfg, &cfg->Inputs[i],
                                 sizeof(live->Cfg)) == sizeof(live->Cfg)) {
                next[i] = live;       // unchanged — claim it
                g_M.In[j] = nullptr;
            }
            break;                    // same Id but changed: leave for teardown, recreate below
        }
    }
    for (ULONG j = 0; j < g_M.Count; j++) {
        if (g_M.In[j]) {
            TeardownInput(g_M.In[j]);
            g_M.In[j] = nullptr;
        }
    }
    NTSTATUS st = STATUS_SUCCESS;
    for (ULONG i = 0; i < cfg->InputCount; i++) {
        if (next[i]) {
            continue;
        }
        RAVE_MINPUT* in = (RAVE_MINPUT*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*in), RAVE_TAG);
        if (!in) {
            st = STATUS_INSUFFICIENT_RESOURCES;  // partial apply; created inputs still run
            break;
        }
        in->Cfg = cfg->Inputs[i];  // rest zero: unbound, due immediately
        next[i] = in;
    }
    g_M.Count = 0;
    for (ULONG i = 0; i < cfg->InputCount; i++) {
        if (next[i]) {
            g_M.In[g_M.Count++] = next[i];
        }
    }
    KeSetEvent(&g_M.Wake, IO_NO_INCREMENT, FALSE);
    MUnlock();
    return st;
}

NTSTATUS RaveManagedQuery(ULONG index, RAVEMIDI_INPUT_STATUS* out)
{
    PAGED_CODE();
    if (!g_M.Started) {
        return STATUS_DEVICE_NOT_READY;
    }
    MLock();
    if (index >= g_M.Count) {
        MUnlock();
        return STATUS_NO_MORE_ENTRIES;
    }
    RAVE_MINPUT* in = g_M.In[index];
    RtlZeroMemory(out, sizeof(*out));
    RtlCopyMemory(out->Id, in->Cfg.Id, sizeof(out->Id));
    RtlCopyMemory(out->Name, in->Cfg.Name, sizeof(out->Name));
    out->Bound = in->Bound ? 1 : 0;
    out->FeedbackBound = in->FeedbackBound ? 1 : 0;
    out->RetryCount = in->RetryCount;
    if (in->Bound) {
        RtlCopyMemory(out->BoundIface, in->BoundIface, sizeof(out->BoundIface));
    }
    out->ReservedPortId = in->Reserved ? in->Reserved->Id : 0;  // 0 = ports pending
    out->OutCount = in->Cfg.OutCount;
    for (ULONG i = 0; i < in->Cfg.OutCount; i++) {
        out->OutPortIds[i] = in->Outs[i] ? in->Outs[i]->Id : 0;
    }
    MUnlock();
    return STATUS_SUCCESS;
}
#pragma code_seg()
