// Mirror-tap implementation: open a hardware MIDI capture pin as a kernel KS
// client, run a system thread that reads it, and fan the bytes into virtual ports.
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// NOTE: the KS streaming client (pin instantiation, IOCTL_KS_READ_STREAM loop)
// is DDI-correct but needs on-hardware bring-up (a physical controller + a
// test-signed load) before it's trusted — it cannot be unit-tested. Failures at
// open time return cleanly to rave-mate; the read loop only runs after a pin is up.

#include <portcls.h>
#include <ksmedia.h>
#include "ioctl.h"
#include "miniport.h"
#include "mirror.h"

#define RAVE_TAG RAVEMIDI_POOL_TAG
#define MIRROR_READ_BUF 1024   // per-read MIDI byte buffer (bounded)

typedef struct _RAVE_MIRROR {
    LIST_ENTRY Link;
    ULONG Id;
    PFILE_OBJECT Creator;
    HANDLE FilterHandle;
    HANDLE PinHandle;
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

// -------- KS client helpers --------
// Synchronous KS IOCTL: wait on our own event so the caller need not be a
// synchronous-IO handle. Runs at PASSIVE (worker thread / IOCTL path).
static NTSTATUS KsSyncIoctl(HANDLE h, ULONG ioctl, PVOID in, ULONG inLen, PVOID out, ULONG outLen)
{
    KEVENT ev;
    KeInitializeEvent(&ev, NotificationEvent, FALSE);
    IO_STATUS_BLOCK iosb;
    NTSTATUS st = ZwDeviceIoControlFile(h, &ev, nullptr, nullptr, &iosb, ioctl,
                                        in, inLen, out, outLen);
    if (st == STATUS_PENDING) {
        KeWaitForSingleObject(&ev, Executive, KernelMode, FALSE, nullptr);
        st = iosb.Status;
    }
    return st;
}

static NTSTATUS KsGetPinProp(HANDLE filter, ULONG propId, ULONG pinId, PVOID out, ULONG outSize)
{
    KSP_PIN kp;
    RtlZeroMemory(&kp, sizeof(kp));
    kp.Property.Set = KSPROPSETID_Pin;
    kp.Property.Id = propId;
    kp.Property.Flags = KSPROPERTY_TYPE_GET;
    kp.PinId = pinId;
    return KsSyncIoctl(filter, IOCTL_KS_PROPERTY, &kp, sizeof(kp), out, outSize);
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

static NTSTATUS SetPinState(HANDLE pin, KSSTATE state)
{
    KSPROPERTY prop;
    RtlZeroMemory(&prop, sizeof(prop));
    prop.Set = KSPROPSETID_Connection;
    prop.Id = KSPROPERTY_CONNECTION_STATE;
    prop.Flags = KSPROPERTY_TYPE_SET;
    return KsSyncIoctl(pin, IOCTL_KS_PROPERTY, &prop, sizeof(prop), &state, sizeof(state));
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

        NTSTATUS st = KsSyncIoctl(m->PinHandle, IOCTL_KS_READ_STREAM, nullptr, 0, &hdr, sizeof(hdr));
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
        ULONG used = (ULONG)hdr.DataUsed;
        ULONG off = 0;
        while (off + sizeof(KSMUSICFORMAT) <= used) {
            KSMUSICFORMAT* mf = (KSMUSICFORMAT*)(buf + off);
            off += sizeof(KSMUSICFORMAT);
            ULONG bc = mf->ByteCount;
            if (bc == 0 || off + bc > used) {
                break;
            }
            for (ULONG i = 0; i < m->OutCount; i++) {
                RaveFifoPush(&m->Outs[i]->ToApp, buf + off, bc);
                RavePortNotifyToApp(m->Outs[i]);
            }
            off += (bc + 3u) & ~3u;  // next record is DWORD-aligned
        }
    }
    ExFreePoolWithTag(buf, RAVE_TAG);
}

// -------- lifecycle --------
static VOID TeardownMirror(RAVE_MIRROR* m)  // not under g_MirrorLock (waits on thread)
{
    InterlockedExchange(&m->Stop, 1);
    if (m->PinHandle) {
        SetPinState(m->PinHandle, KSSTATE_STOP);  // completes the worker's pending read
    }
    if (m->ThreadObj) {
        KeWaitForSingleObject(m->ThreadObj, Executive, KernelMode, FALSE, nullptr);
        ObDereferenceObject(m->ThreadObj);
    }
    if (m->PinHandle) {
        ZwClose(m->PinHandle);
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
    // SourceInterface must be NUL-terminated in-bounds.
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

    // Open the source filter.
    UNICODE_STRING path;
    RtlInitUnicodeString(&path, in->SourceInterface);
    OBJECT_ATTRIBUTES oa;
    InitializeObjectAttributes(&oa, &path, OBJ_KERNEL_HANDLE | OBJ_CASE_INSENSITIVE, nullptr, nullptr);
    IO_STATUS_BLOCK iosb;
    NTSTATUS st = ZwCreateFile(&m->FilterHandle, GENERIC_READ | SYNCHRONIZE, &oa, &iosb, nullptr,
                               FILE_ATTRIBUTE_NORMAL, FILE_SHARE_READ | FILE_SHARE_WRITE,
                               FILE_OPEN, FILE_SYNCHRONOUS_IO_NONALERT, nullptr, 0);
    if (!NT_SUCCESS(st)) {
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return st;
    }

    // Find + open a MIDI capture pin (probe each capture pin with the MIDI format).
    ULONG pinCount = 0;
    st = KsGetPinProp(m->FilterHandle, KSPROPERTY_PIN_CTYPES, 0, &pinCount, sizeof(pinCount));
    m->PinHandle = nullptr;
    for (ULONG i = 0; NT_SUCCESS(st) && i < pinCount && !m->PinHandle; i++) {
        KSPIN_DATAFLOW flow;
        if (!NT_SUCCESS(KsGetPinProp(m->FilterHandle, KSPROPERTY_PIN_DATAFLOW, i, &flow, sizeof(flow)))) {
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
    if (!m->PinHandle) {
        ZwClose(m->FilterHandle);
        for (ULONG i = 0; i < m->OutCount; i++) {
            RaveUnrefOutputPort(m->Outs[i]);
        }
        ExFreePoolWithTag(m, RAVE_TAG);
        return STATUS_NOT_FOUND;
    }

    // Step the pin to RUN (STOP -> ACQUIRE -> PAUSE -> RUN).
    SetPinState(m->PinHandle, KSSTATE_ACQUIRE);
    SetPinState(m->PinHandle, KSSTATE_PAUSE);
    st = SetPinState(m->PinHandle, KSSTATE_RUN);
    if (!NT_SUCCESS(st)) {
        ZwClose(m->PinHandle);
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
        SetPinState(m->PinHandle, KSSTATE_STOP);
        ZwClose(m->PinHandle);
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
