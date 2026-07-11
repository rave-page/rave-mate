// KS-client implementation: capture taps (open a hardware MIDI capture pin as a
// kernel KS client, pump reads on a system thread, fan bytes into virtual ports),
// render-pin writes (managed feedback), and the legacy IOCTL mirror groups.
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// NOTE: the KS streaming client (pin instantiation, IOCTL_KS_READ/WRITE_STREAM)
// is DDI-correct but needs on-hardware bring-up (a physical controller + a
// test-signed load) before it's trusted — it cannot be unit-tested. Failures at
// open time return cleanly; the pump only runs after a pin is up.
//
// Trust boundary: mirror sources come through the (SDDL-restricted) control
// device and managed sources from the driver's own enumeration; both are vetted
// against currently-enumerated KS interfaces (no arbitrary \Device\... paths),
// and all record parsing uses subtractive, overflow-free bounds checks.

#include <portcls.h>
#include <ksmedia.h>
#include "ioctl.h"
#include "miniport.h"
#include "mirror.h"

#define RAVE_TAG RAVEMIDI_POOL_TAG
#define MIRROR_READ_BUF 1024   // per-read MIDI byte buffer (bounded)
#define MIRROR_MAX_REC  256    // sane cap on a single KSMUSICFORMAT record
#define TAP_MAX_OUT (RAVEMIDI_MAX_MIRROR_OUT + 1)  // managed: reserved + outs
#define TAP_FAIL_LIMIT 3       // consecutive read failures before OnDead fires

typedef struct _RAVE_TAP {
    HANDLE FilterHandle;             // needed by KsCreatePin
    PFILE_OBJECT FilterFileObj;      // for filter property IOCTLs
    HANDLE PinHandle;
    PFILE_OBJECT PinFileObj;         // for pin state + streaming reads
    PVOID ThreadObj;                 // PKTHREAD (referenced), joined in RaveTapClose
    volatile LONG Stop;
    ULONG OutCount;
    RAVE_PORT* Outs[TAP_MAX_OUT];    // borrowed (caller owns lifetime)
    ULONG FilterMask;                // RAVEMIDI_FILTER_*: drop classes for Outs[1..]
    RAVE_TAP_DEAD_CB OnDead;
    PVOID DeadCtx;
} RAVE_TAP;

// TRUE if a message with this status byte is dropped under the mask.
static BOOLEAN FilteredMsg(ULONG mask, UCHAR status)
{
    if (status >= 0xF8) {
        if (status == 0xFE) {
            return (mask & RAVEMIDI_FILTER_ACTIVESENSE) != 0;
        }
        return (status <= 0xF9) && (mask & RAVEMIDI_FILTER_CLOCK) != 0;
    }
    switch (status & 0xF0) {
    case 0xA0: return (mask & RAVEMIDI_FILTER_POLYPRESSURE) != 0;
    case 0xD0: return (mask & RAVEMIDI_FILTER_CHANPRESSURE) != 0;
    case 0xE0: return (mask & RAVEMIDI_FILTER_PITCHBEND) != 0;
    default:   return FALSE;
    }
}

typedef struct _RAVE_MIRROR {
    LIST_ENTRY Link;
    ULONG Id;
    PFILE_OBJECT Creator;
    RAVE_TAP* Tap;
    ULONG OutCount;
    RAVE_PORT* Outs[RAVEMIDI_MAX_MIRROR_OUT];  // ref'd (MirrorRefs blocks destroy)
} RAVE_MIRROR;

static FAST_MUTEX g_MirrorLock;
static LIST_ENTRY g_Mirrors;
static ULONG g_MirrorSeq;

VOID RaveMirrorInit()
{
    ExInitializeFastMutex(&g_MirrorLock);
    InitializeListHead(&g_Mirrors);
    g_MirrorSeq = 0;
}

static SIZE_T MWcsLen(PCWSTR s)  // no CRT dep
{
    SIZE_T n = 0;
    while (s[n]) {
        n++;
    }
    return n;
}

// Skip a leading "\??\" or "\\?\" so the two symlink spellings compare equal.
static PCWSTR NormIface(PCWSTR s)
{
    if (s[0] == L'\\' && s[1] && s[2] && s[3] == L'\\') {
        return s + 4;
    }
    return s;
}

#pragma code_seg("PAGE")
BOOLEAN RaveIsKnownIface(PCWSTR Name, BOOLEAN Render)
{
    PAGED_CODE();
    UNICODE_STRING want;
    RtlInitUnicodeString(&want, NormIface(Name));
    const GUID* cats[2];
    cats[0] = Render ? &KSCATEGORY_RENDER : &KSCATEGORY_CAPTURE;
    cats[1] = &KSCATEGORY_AUDIO;
    for (int c = 0; c < 2; c++) {
        PWSTR list = nullptr;
        if (!NT_SUCCESS(IoGetDeviceInterfaces(cats[c], nullptr, 0, &list)) || !list) {
            continue;
        }
        BOOLEAN found = FALSE;
        for (PWSTR sym = list; *sym; sym += MWcsLen(sym) + 1) {
            UNICODE_STRING have;
            RtlInitUnicodeString(&have, NormIface(sym));
            if (RtlEqualUnicodeString(&have, &want, TRUE)) {
                found = TRUE;
                break;
            }
        }
        ExFreePool(list);
        if (found) {
            return TRUE;
        }
    }
    return FALSE;
}
#pragma code_seg()

// -------- KS client helpers (KsSynchronousIoControlDevice on the FILE_OBJECT) --------
static NTSTATUS KsProp(PFILE_OBJECT fo, PVOID prop, ULONG propLen, PVOID data, ULONG dataLen)
{
    ULONG br = 0;
    return KsSynchronousIoControlDevice(fo, KernelMode, IOCTL_KS_PROPERTY,
                                        prop, propLen, data, dataLen, &br);
}

static NTSTATUS KsGetPinProp(PFILE_OBJECT filter, ULONG propId, ULONG pinId, PVOID out, ULONG outSize)
{
    KSP_PIN kp;
    RtlZeroMemory(&kp, sizeof(kp));
    kp.Property.Set = KSPROPSETID_Pin;
    kp.Property.Id = propId;
    kp.Property.Flags = KSPROPERTY_TYPE_GET;
    kp.PinId = pinId;
    return KsProp(filter, &kp, sizeof(kp), out, outSize);
}

static NTSTATUS OpenMidiPin(HANDLE filter, ULONG pinId, ACCESS_MASK access, PHANDLE pinHandle)
{
    struct {
        KSPIN_CONNECT c;
        KSDATAFORMAT f;
    } conn;
    RtlZeroMemory(&conn, sizeof(conn));
    conn.c.Interface.Set = KSINTERFACESETID_Standard;
    conn.c.Interface.Id = KSINTERFACE_STANDARD_STREAMING;
    conn.c.Medium.Set = KSMEDIUMSETID_Standard;
    conn.c.Medium.Id = KSMEDIUM_STANDARD_DEVIO;
    conn.c.PinId = pinId;
    conn.c.PinToHandle = nullptr;
    conn.c.Priority.PriorityClass = KSPRIORITY_NORMAL;
    conn.c.Priority.PrioritySubClass = 1;
    conn.f.FormatSize = sizeof(KSDATAFORMAT);
    conn.f.MajorFormat = KSDATAFORMAT_TYPE_MUSIC;
    conn.f.SubFormat = KSDATAFORMAT_SUBTYPE_MIDI;
    conn.f.Specifier = KSDATAFORMAT_SPECIFIER_NONE;
    return KsCreatePin(filter, &conn.c, access, pinHandle);
}

static NTSTATUS SetPinState(PFILE_OBJECT pin, KSSTATE state)
{
    KSPROPERTY prop;
    RtlZeroMemory(&prop, sizeof(prop));
    prop.Set = KSPROPSETID_Connection;
    prop.Id = KSPROPERTY_CONNECTION_STATE;
    prop.Flags = KSPROPERTY_TYPE_SET;
    return KsProp(pin, &prop, sizeof(prop), &state, sizeof(state));
}

// Open filter + find a MIDI pin of the wanted dataflow, step it to RUN.
#pragma code_seg("PAGE")
static NTSTATUS OpenMidiFilterPin(PCWSTR iface, KSPIN_DATAFLOW flow, ACCESS_MASK pinAccess,
                                  HANDLE* fh, PFILE_OBJECT* ffo, HANDLE* ph, PFILE_OBJECT* pfo)
{
    PAGED_CODE();
    *fh = nullptr;
    *ffo = nullptr;
    *ph = nullptr;
    *pfo = nullptr;

    UNICODE_STRING path;
    RtlInitUnicodeString(&path, iface);
    OBJECT_ATTRIBUTES oa;
    InitializeObjectAttributes(&oa, &path, OBJ_KERNEL_HANDLE | OBJ_CASE_INSENSITIVE, nullptr, nullptr);
    IO_STATUS_BLOCK iosb;
    HANDLE filter = nullptr;
    ACCESS_MASK facc = GENERIC_READ | SYNCHRONIZE;
    if (flow == KSPIN_DATAFLOW_IN) {
        facc |= GENERIC_WRITE;  // render pins are created for writing
    }
    NTSTATUS st = ZwCreateFile(&filter, facc, &oa, &iosb,
                               nullptr, FILE_ATTRIBUTE_NORMAL, FILE_SHARE_READ | FILE_SHARE_WRITE,
                               FILE_OPEN, FILE_SYNCHRONOUS_IO_NONALERT, nullptr, 0);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    PFILE_OBJECT filterFo = nullptr;
    st = ObReferenceObjectByHandle(filter, 0, *IoFileObjectType, KernelMode, (PVOID*)&filterFo, nullptr);
    if (!NT_SUCCESS(st)) {
        ZwClose(filter);
        return st;
    }

    // Probe each pin of the wanted flow with the MIDI format; first accept wins.
    ULONG pinCount = 0;
    st = KsGetPinProp(filterFo, KSPROPERTY_PIN_CTYPES, 0, &pinCount, sizeof(pinCount));
    HANDLE pin = nullptr;
    for (ULONG i = 0; NT_SUCCESS(st) && i < pinCount && !pin; i++) {
        KSPIN_DATAFLOW f;
        if (!NT_SUCCESS(KsGetPinProp(filterFo, KSPROPERTY_PIN_DATAFLOW, i, &f, sizeof(f)))) {
            continue;
        }
        if (f != flow) {
            continue;
        }
        HANDLE h = nullptr;
        if (NT_SUCCESS(OpenMidiPin(filter, i, pinAccess, &h)) && h) {
            pin = h;
        }
    }
    PFILE_OBJECT pinFo = nullptr;
    if (pin) {
        if (!NT_SUCCESS(ObReferenceObjectByHandle(pin, 0, *IoFileObjectType, KernelMode,
                                                  (PVOID*)&pinFo, nullptr))) {
            ZwClose(pin);
            pin = nullptr;
        }
    }
    if (!pin) {
        ObDereferenceObject(filterFo);
        ZwClose(filter);
        return STATUS_NOT_FOUND;
    }

    // STOP -> ACQUIRE -> PAUSE -> RUN.
    SetPinState(pinFo, KSSTATE_ACQUIRE);
    SetPinState(pinFo, KSSTATE_PAUSE);
    st = SetPinState(pinFo, KSSTATE_RUN);
    if (!NT_SUCCESS(st)) {
        ObDereferenceObject(pinFo);
        ZwClose(pin);
        ObDereferenceObject(filterFo);
        ZwClose(filter);
        return st;
    }
    *fh = filter;
    *ffo = filterFo;
    *ph = pin;
    *pfo = pinFo;
    return STATUS_SUCCESS;
}
#pragma code_seg()

// -------- capture-tap read pump --------
static KSTART_ROUTINE TapThread;
static VOID TapThread(PVOID ctx)
{
    RAVE_TAP* t = (RAVE_TAP*)ctx;
    PUCHAR buf = (PUCHAR)ExAllocatePool2(POOL_FLAG_NON_PAGED, MIRROR_READ_BUF, RAVE_TAG);
    if (!buf) {
        if (t->OnDead && !t->Stop) {
            t->OnDead(t->DeadCtx);
        }
        return;
    }
    ULONG fails = 0;
    while (!t->Stop) {
        KSSTREAM_HEADER hdr;
        RtlZeroMemory(&hdr, sizeof(hdr));
        hdr.Size = sizeof(hdr);
        hdr.PresentationTime.Numerator = 1;
        hdr.PresentationTime.Denominator = 1;
        hdr.FrameExtent = MIRROR_READ_BUF;
        hdr.Data = buf;

        ULONG br = 0;
        NTSTATUS st = KsSynchronousIoControlDevice(t->PinFileObj, KernelMode, IOCTL_KS_READ_STREAM,
                                                   nullptr, 0, &hdr, sizeof(hdr), &br);
        if (!NT_SUCCESS(st)) {
            if (t->Stop) {
                break;  // pin STOP completed our read — normal teardown
            }
            // Managed taps die after TAP_FAIL_LIMIT strikes (device pulled) so the
            // engine can rebind; legacy mirrors (no OnDead) retry forever.
            if (t->OnDead && ++fails >= TAP_FAIL_LIMIT) {
                t->OnDead(t->DeadCtx);
                break;
            }
            LARGE_INTEGER dt;
            dt.QuadPart = -10 * 1000 * 10;  // 10ms back-off on transient error
            KeDelayExecutionThread(KernelMode, FALSE, &dt);
            continue;
        }
        fails = 0;
        // Parse KSMUSICFORMAT records: {TimeDeltaMs, ByteCount} + bytes, DWORD-padded.
        // Subtractive checks only (no off+bc that could wrap); records capped.
        ULONG used = (ULONG)hdr.DataUsed;
        if (used > MIRROR_READ_BUF) {
            used = MIRROR_READ_BUF;  // never trust DataUsed past the buffer
        }
        if (used) {
            // raw pre-parse view -> first fan-in port's ring (diagnosis anchor)
            RaveTracePush(t->Outs[0], RaveTraceTapRaw, buf, used);
        }
        ULONG off = 0;
        while (used - off >= sizeof(KSMUSICFORMAT)) {
            KSMUSICFORMAT* mf = (KSMUSICFORMAT*)(buf + off);
            off += sizeof(KSMUSICFORMAT);
            ULONG bc = mf->ByteCount;
            if (bc == 0 || bc > MIRROR_MAX_REC || bc > used - off) {
                break;
            }
            // fan-out filter: reserved port (index 0) always sees everything
            BOOLEAN drop = (t->FilterMask && FilteredMsg(t->FilterMask, buf[off])) ? TRUE : FALSE;
            for (ULONG i = 0; i < t->OutCount; i++) {
                if (drop && i > 0) {
                    continue;
                }
                RAVE_PORT* o = t->Outs[i];
                if (o->Kind == RaveMidiPortInternal) {
                    // hidden reserved port: no capture pin exists - hand the bytes
                    // to rave-mate's pended IOCTL_READ via the FromApp ring instead
                    RaveFifoPush(&o->FromApp, buf + off, bc);
                    RaveTracePush(o, RaveTraceToApp, buf + off, bc);
                    RavePortDeliverFromApp(o);
                    continue;
                }
                RaveFifoPush(&o->ToApp, buf + off, bc);
                RaveTracePush(o, RaveTraceToApp, buf + off, bc);
                RavePortNotifyToApp(o);
            }
            ULONG pad = (bc + 3u) & ~3u;
            if (pad < bc || pad > used - off) {
                break;  // padding runs past the buffer — last record, stop
            }
            off += pad;
        }
    }
    ExFreePoolWithTag(buf, RAVE_TAG);
}

#pragma code_seg("PAGE")
NTSTATUS RaveTapOpen(PCWSTR Iface, RAVE_PORT* const* Outs, ULONG OutCount,
                     ULONG FilterMask, RAVE_TAP_DEAD_CB OnDead, PVOID DeadCtx, RAVE_TAP** OutTap)
{
    PAGED_CODE();
    *OutTap = nullptr;
    if (OutCount == 0 || OutCount > TAP_MAX_OUT) {
        return STATUS_INVALID_PARAMETER;
    }
    RAVE_TAP* t = (RAVE_TAP*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*t), RAVE_TAG);
    if (!t) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    t->OutCount = OutCount;
    for (ULONG i = 0; i < OutCount; i++) {
        t->Outs[i] = Outs[i];
    }
    t->FilterMask = FilterMask;
    t->OnDead = OnDead;
    t->DeadCtx = DeadCtx;

    NTSTATUS st = OpenMidiFilterPin(Iface, KSPIN_DATAFLOW_OUT, GENERIC_READ,
                                    &t->FilterHandle, &t->FilterFileObj,
                                    &t->PinHandle, &t->PinFileObj);
    if (!NT_SUCCESS(st)) {
        ExFreePoolWithTag(t, RAVE_TAG);
        return st;
    }

    t->Stop = 0;
    t->ThreadObj = nullptr;
    HANDLE threadHandle = nullptr;
    st = PsCreateSystemThread(&threadHandle, THREAD_ALL_ACCESS, nullptr, nullptr, nullptr,
                              TapThread, t);
    if (!NT_SUCCESS(st)) {
        SetPinState(t->PinFileObj, KSSTATE_STOP);
        ObDereferenceObject(t->PinFileObj);
        ZwClose(t->PinHandle);
        ObDereferenceObject(t->FilterFileObj);
        ZwClose(t->FilterHandle);
        ExFreePoolWithTag(t, RAVE_TAG);
        return st;
    }
    ObReferenceObjectByHandle(threadHandle, THREAD_ALL_ACCESS, *PsThreadType, KernelMode,
                              &t->ThreadObj, nullptr);
    ZwClose(threadHandle);
    *OutTap = t;
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveTapClose(RAVE_TAP* Tap)  // waits on the pump thread — PASSIVE, no locks held
{
    PAGED_CODE();
    InterlockedExchange(&Tap->Stop, 1);
    if (Tap->PinFileObj) {
        SetPinState(Tap->PinFileObj, KSSTATE_STOP);  // completes the pump's pending read
    }
    if (Tap->ThreadObj) {
        KeWaitForSingleObject(Tap->ThreadObj, Executive, KernelMode, FALSE, nullptr);
        ObDereferenceObject(Tap->ThreadObj);
    }
    if (Tap->PinFileObj) {
        ObDereferenceObject(Tap->PinFileObj);
    }
    if (Tap->PinHandle) {
        ZwClose(Tap->PinHandle);
    }
    if (Tap->FilterFileObj) {
        ObDereferenceObject(Tap->FilterFileObj);
    }
    if (Tap->FilterHandle) {
        ZwClose(Tap->FilterHandle);
    }
    ExFreePoolWithTag(Tap, RAVE_TAG);
}
#pragma code_seg()

// -------- render-pin client (managed feedback) --------
#pragma code_seg("PAGE")
NTSTATUS RaveKsOpenRenderPin(PCWSTR Iface, HANDLE* FilterH, PFILE_OBJECT* FilterFo,
                             HANDLE* PinH, PFILE_OBJECT* PinFo)
{
    PAGED_CODE();
    return OpenMidiFilterPin(Iface, KSPIN_DATAFLOW_IN, GENERIC_WRITE,
                             FilterH, FilterFo, PinH, PinFo);
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveKsCloseRenderPin(HANDLE FilterH, PFILE_OBJECT FilterFo, HANDLE PinH, PFILE_OBJECT PinFo)
{
    PAGED_CODE();
    if (PinFo) {
        SetPinState(PinFo, KSSTATE_STOP);
        ObDereferenceObject(PinFo);
    }
    if (PinH) {
        ZwClose(PinH);
    }
    if (FilterFo) {
        ObDereferenceObject(FilterFo);
    }
    if (FilterH) {
        ZwClose(FilterH);
    }
}
#pragma code_seg()

#pragma code_seg("PAGE")
NTSTATUS RaveKsWriteMidi(PFILE_OBJECT Pin, const UCHAR* Bytes, ULONG Len)
{
    PAGED_CODE();
    if (Len == 0 || Len > RAVEMIDI_FEEDBACK_CHUNK) {
        return STATUS_INVALID_PARAMETER;
    }
    // One KSMUSICFORMAT record: {TimeDeltaMs=0, ByteCount} + bytes, DWORD-padded.
    union {
        KSMUSICFORMAT mf;  // alignment anchor
        UCHAR raw[sizeof(KSMUSICFORMAT) + ((RAVEMIDI_FEEDBACK_CHUNK + 3u) & ~3u)];
    } pkt;
    RtlZeroMemory(&pkt.mf, sizeof(pkt.mf));
    pkt.mf.ByteCount = Len;
    ULONG pad = (Len + 3u) & ~3u;
    RtlCopyMemory(pkt.raw + sizeof(KSMUSICFORMAT), Bytes, Len);
    for (ULONG i = Len; i < pad; i++) {
        pkt.raw[sizeof(KSMUSICFORMAT) + i] = 0;
    }
    KSSTREAM_HEADER hdr;
    RtlZeroMemory(&hdr, sizeof(hdr));
    hdr.Size = sizeof(hdr);
    hdr.PresentationTime.Numerator = 1;
    hdr.PresentationTime.Denominator = 1;
    hdr.Data = pkt.raw;
    hdr.FrameExtent = sizeof(KSMUSICFORMAT) + pad;
    hdr.DataUsed = sizeof(KSMUSICFORMAT) + pad;
    ULONG br = 0;
    return KsSynchronousIoControlDevice(Pin, KernelMode, IOCTL_KS_WRITE_STREAM,
                                        nullptr, 0, &hdr, sizeof(hdr), &br);
}
#pragma code_seg()

// -------- legacy mirror lifecycle --------
static VOID TeardownMirror(RAVE_MIRROR* m)  // not under g_MirrorLock (tap close joins a thread)
{
    if (m->Tap) {
        RaveTapClose(m->Tap);
    }
    for (ULONG i = 0; i < m->OutCount; i++) {
        RaveUnrefOutputPort(m->Outs[i]);
    }
    ExFreePoolWithTag(m, RAVE_TAG);
}

#pragma code_seg("PAGE")
NTSTATUS RaveMirrorCreate(PFILE_OBJECT creator, const RAVEMIDI_CREATE_MIRROR_IN* in,
                          ULONG inLen, ULONG* outId)
{
    PAGED_CODE();
    UNREFERENCED_PARAMETER(inLen);
    if (in->Version != RAVEMIDI_PROTOCOL_VERSION ||
        in->OutputCount == 0 || in->OutputCount > RAVEMIDI_MAX_MIRROR_OUT) {
        return STATUS_INVALID_PARAMETER;
    }
    // SourceInterface must be NUL-terminated in-bounds...
    BOOLEAN term = FALSE;
    for (ULONG i = 0; i < RAVEMIDI_MAX_IFACE; i++) {
        if (in->SourceInterface[i] == 0) {
            term = TRUE;
            break;
        }
    }
    if (!term) {
        return STATUS_INVALID_PARAMETER;
    }
    // ...and be a real KS capture/audio interface (no arbitrary NT paths).
    if (!RaveIsKnownIface(in->SourceInterface, FALSE)) {
        return STATUS_INVALID_PARAMETER;
    }

    RAVE_MIRROR* m = (RAVE_MIRROR*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*m), RAVE_TAG);
    if (!m) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    m->Creator = creator;
    m->OutCount = in->OutputCount;

    // Reference the output ports (blocks their destroy until this mirror ends).
    for (ULONG i = 0; i < m->OutCount; i++) {
        m->Outs[i] = RaveRefOutputPort(in->OutputPortIds[i]);
        if (!m->Outs[i]) {
            for (ULONG j = 0; j < i; j++) {
                RaveUnrefOutputPort(m->Outs[j]);
            }
            ExFreePoolWithTag(m, RAVE_TAG);
            return STATUS_NOT_FOUND;
        }
    }

    NTSTATUS st = RaveTapOpen(in->SourceInterface, m->Outs, m->OutCount, 0, nullptr, nullptr, &m->Tap);
    if (!NT_SUCCESS(st)) {
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return st;
    }

    ExAcquireFastMutex(&g_MirrorLock);
    m->Id = ++g_MirrorSeq;
    InsertTailList(&g_Mirrors, &m->Link);
    ExReleaseFastMutex(&g_MirrorLock);

    *outId = m->Id;
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
NTSTATUS RaveMirrorDestroy(PFILE_OBJECT caller, ULONG id)
{
    PAGED_CODE();
    RAVE_MIRROR* victim = nullptr;
    ExAcquireFastMutex(&g_MirrorLock);
    for (PLIST_ENTRY e = g_Mirrors.Flink; e != &g_Mirrors; e = e->Flink) {
        RAVE_MIRROR* m = CONTAINING_RECORD(e, RAVE_MIRROR, Link);
        if (m->Id == id) {
            if (caller && m->Creator != caller) {
                ExReleaseFastMutex(&g_MirrorLock);
                return STATUS_ACCESS_DENIED;
            }
            RemoveEntryList(&m->Link);
            victim = m;
            break;
        }
    }
    ExReleaseFastMutex(&g_MirrorLock);
    if (!victim) {
        return STATUS_NOT_FOUND;
    }
    TeardownMirror(victim);  // waits on the pump thread — outside the lock
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveMirrorDestroyForFile(PFILE_OBJECT f)
{
    PAGED_CODE();
    // Managed taps are not in this list — only creator-owned mirrors die with
    // their handle; driver autonomy is unaffected by rave-mate exit.
    for (;;) {
        RAVE_MIRROR* victim = nullptr;
        ExAcquireFastMutex(&g_MirrorLock);
        for (PLIST_ENTRY e = g_Mirrors.Flink; e != &g_Mirrors; e = e->Flink) {
            RAVE_MIRROR* m = CONTAINING_RECORD(e, RAVE_MIRROR, Link);
            if (m->Creator == f) {
                RemoveEntryList(&m->Link);
                victim = m;
                break;
            }
        }
        ExReleaseFastMutex(&g_MirrorLock);
        if (!victim) {
            break;
        }
        TeardownMirror(victim);
    }
}
#pragma code_seg()
