// Mirror-tap implementation: open a hardware MIDI capture pin as a kernel KS
// client, run a system thread that reads it, and fan the bytes into virtual ports.
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// NOTE: the KS streaming client (pin instantiation, IOCTL_KS_READ_STREAM loop)
// is DDI-correct but needs on-hardware bring-up (a physical controller + a
// test-signed load) before it's trusted — it cannot be unit-tested. Failures at
// open time return cleanly to rave-mate; the read loop only runs after a pin is up.
//
// Trust boundary: the control device is SDDL-restricted to SYSTEM+Administrators,
// so only elevated rave-mate can create a mirror. As defense-in-depth we still
// reject any SourceInterface that isn't a currently-enumerated KS capture/audio
// interface (no arbitrary \Device\... or file paths), and all record parsing uses
// subtractive, overflow-free bounds checks.

#include <portcls.h>
#include <ksmedia.h>
#include "ioctl.h"
#include "miniport.h"
#include "mirror.h"

#define RAVE_TAG RAVEMIDI_POOL_TAG
#define MIRROR_READ_BUF 1024   // per-read MIDI byte buffer (bounded)
#define MIRROR_MAX_REC  256    // sane cap on a single KSMUSICFORMAT record

typedef struct _RAVE_MIRROR {
    LIST_ENTRY Link;
    ULONG Id;
    PFILE_OBJECT Creator;
    HANDLE FilterHandle;             // needed by KsCreatePin
    PFILE_OBJECT FilterFileObj;      // for filter property IOCTLs
    HANDLE PinHandle;
    PFILE_OBJECT PinFileObj;         // for pin state + streaming reads
    PVOID ThreadObj;                 // PKTHREAD (referenced), waited on at destroy
    volatile LONG Stop;
    ULONG OutCount;
    RAVE_PORT* Outs[RAVEMIDI_MAX_MIRROR_OUT];
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

// True only if `name` is a currently-enumerated KS capture/audio interface — blocks
// opening arbitrary NT paths through the (admin-only) control device.
#pragma code_seg("PAGE")
static BOOLEAN IsKnownCaptureIface(PCWSTR name)
{
    PAGED_CODE();
    UNICODE_STRING want;
    RtlInitUnicodeString(&want, NormIface(name));
    const GUID* cats[2] = { &KSCATEGORY_CAPTURE, &KSCATEGORY_AUDIO };
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

static NTSTATUS OpenMidiCapturePin(HANDLE filter, ULONG pinId, PHANDLE pinHandle)
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
    return KsCreatePin(filter, &conn.c, GENERIC_READ, pinHandle);
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

// -------- read-pump worker --------
static KSTART_ROUTINE MirrorThread;
static VOID MirrorThread(PVOID ctx)
{
    RAVE_MIRROR* m = (RAVE_MIRROR*)ctx;
    PUCHAR buf = (PUCHAR)ExAllocatePool2(POOL_FLAG_NON_PAGED, MIRROR_READ_BUF, RAVE_TAG);
    if (!buf) {
        return;
    }
    while (!m->Stop) {
        KSSTREAM_HEADER hdr;
        RtlZeroMemory(&hdr, sizeof(hdr));
        hdr.Size = sizeof(hdr);
        hdr.PresentationTime.Numerator = 1;
        hdr.PresentationTime.Denominator = 1;
        hdr.FrameExtent = MIRROR_READ_BUF;
        hdr.Data = buf;

        ULONG br = 0;
        NTSTATUS st = KsSynchronousIoControlDevice(m->PinFileObj, KernelMode, IOCTL_KS_READ_STREAM,
                                                   nullptr, 0, &hdr, sizeof(hdr), &br);
        if (!NT_SUCCESS(st)) {
            if (m->Stop) {
                break;  // pin STOP completed our read — normal teardown
            }
            LARGE_INTEGER dt;
            dt.QuadPart = -10 * 1000 * 10;  // 10ms back-off on transient error
            KeDelayExecutionThread(KernelMode, FALSE, &dt);
            continue;
        }
        // Parse KSMUSICFORMAT records: {TimeDeltaMs, ByteCount} + bytes, DWORD-padded.
        // Subtractive checks only (no off+bc that could wrap); records capped.
        ULONG used = (ULONG)hdr.DataUsed;
        if (used > MIRROR_READ_BUF) {
            used = MIRROR_READ_BUF;  // never trust DataUsed past the buffer
        }
        ULONG off = 0;
        while (used - off >= sizeof(KSMUSICFORMAT)) {
            KSMUSICFORMAT* mf = (KSMUSICFORMAT*)(buf + off);
            off += sizeof(KSMUSICFORMAT);
            ULONG bc = mf->ByteCount;
            if (bc == 0 || bc > MIRROR_MAX_REC || bc > used - off) {
                break;
            }
            for (ULONG i = 0; i < m->OutCount; i++) {
                RaveFifoPush(&m->Outs[i]->ToApp, buf + off, bc);
                RavePortNotifyToApp(m->Outs[i]);
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

// -------- lifecycle --------
static VOID TeardownMirror(RAVE_MIRROR* m)  // not under g_MirrorLock (waits on thread)
{
    InterlockedExchange(&m->Stop, 1);
    if (m->PinFileObj) {
        SetPinState(m->PinFileObj, KSSTATE_STOP);  // completes the worker's pending read
    }
    if (m->ThreadObj) {
        KeWaitForSingleObject(m->ThreadObj, Executive, KernelMode, FALSE, nullptr);
        ObDereferenceObject(m->ThreadObj);
    }
    if (m->PinFileObj) {
        ObDereferenceObject(m->PinFileObj);
    }
    if (m->PinHandle) {
        ZwClose(m->PinHandle);
    }
    if (m->FilterFileObj) {
        ObDereferenceObject(m->FilterFileObj);
    }
    if (m->FilterHandle) {
        ZwClose(m->FilterHandle);
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
    if (!IsKnownCaptureIface(in->SourceInterface)) {
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

    // Open the source filter + get its FILE_OBJECT for property IOCTLs.
    UNICODE_STRING path;
    RtlInitUnicodeString(&path, in->SourceInterface);
    OBJECT_ATTRIBUTES oa;
    InitializeObjectAttributes(&oa, &path, OBJ_KERNEL_HANDLE | OBJ_CASE_INSENSITIVE, nullptr, nullptr);
    IO_STATUS_BLOCK iosb;
    NTSTATUS st = ZwCreateFile(&m->FilterHandle, GENERIC_READ | SYNCHRONIZE, &oa, &iosb, nullptr,
                               FILE_ATTRIBUTE_NORMAL, FILE_SHARE_READ | FILE_SHARE_WRITE,
                               FILE_OPEN, FILE_SYNCHRONOUS_IO_NONALERT, nullptr, 0);
    if (NT_SUCCESS(st)) {
        st = ObReferenceObjectByHandle(m->FilterHandle, 0, *IoFileObjectType, KernelMode,
                                       (PVOID*)&m->FilterFileObj, nullptr);
    }
    if (!NT_SUCCESS(st)) {
        if (m->FilterHandle) {
            ZwClose(m->FilterHandle);
        }
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return st;
    }

    // Find + open a MIDI capture pin (probe each capture pin with the MIDI format).
    ULONG pinCount = 0;
    st = KsGetPinProp(m->FilterFileObj, KSPROPERTY_PIN_CTYPES, 0, &pinCount, sizeof(pinCount));
    m->PinHandle = nullptr;
    m->PinFileObj = nullptr;
    for (ULONG i = 0; NT_SUCCESS(st) && i < pinCount && !m->PinHandle; i++) {
        KSPIN_DATAFLOW flow;
        if (!NT_SUCCESS(KsGetPinProp(m->FilterFileObj, KSPROPERTY_PIN_DATAFLOW, i, &flow, sizeof(flow)))) {
            continue;
        }
        if (flow != KSPIN_DATAFLOW_OUT) {
            continue;  // capture pin data flows out of the filter, toward us
        }
        HANDLE pin = nullptr;
        if (NT_SUCCESS(OpenMidiCapturePin(m->FilterHandle, i, &pin)) && pin) {
            m->PinHandle = pin;  // this pin accepted the MIDI format
        }
    }
    if (m->PinHandle) {
        st = ObReferenceObjectByHandle(m->PinHandle, 0, *IoFileObjectType, KernelMode,
                                       (PVOID*)&m->PinFileObj, nullptr);
        if (!NT_SUCCESS(st)) {
            ZwClose(m->PinHandle);
            m->PinHandle = nullptr;
        }
    }
    if (!m->PinHandle) {
        ObDereferenceObject(m->FilterFileObj);
        ZwClose(m->FilterHandle);
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return STATUS_NOT_FOUND;
    }

    // Step the pin to RUN (STOP -> ACQUIRE -> PAUSE -> RUN).
    SetPinState(m->PinFileObj, KSSTATE_ACQUIRE);
    SetPinState(m->PinFileObj, KSSTATE_PAUSE);
    st = SetPinState(m->PinFileObj, KSSTATE_RUN);
    if (!NT_SUCCESS(st)) {
        ObDereferenceObject(m->PinFileObj);
        ZwClose(m->PinHandle);
        ObDereferenceObject(m->FilterFileObj);
        ZwClose(m->FilterHandle);
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return st;
    }

    // Spawn the read-pump.
    m->Stop = 0;
    m->ThreadObj = nullptr;
    HANDLE threadHandle = nullptr;
    st = PsCreateSystemThread(&threadHandle, THREAD_ALL_ACCESS, nullptr, nullptr, nullptr,
                              MirrorThread, m);
    if (!NT_SUCCESS(st)) {
        SetPinState(m->PinFileObj, KSSTATE_STOP);
        ObDereferenceObject(m->PinFileObj);
        ZwClose(m->PinHandle);
        ObDereferenceObject(m->FilterFileObj);
        ZwClose(m->FilterHandle);
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return st;
    }
    ObReferenceObjectByHandle(threadHandle, THREAD_ALL_ACCESS, *PsThreadType, KernelMode,
                              &m->ThreadObj, nullptr);
    ZwClose(threadHandle);

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
    TeardownMirror(victim);  // waits on the worker thread — outside the lock
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveMirrorDestroyForFile(PFILE_OBJECT f)
{
    PAGED_CODE();
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
