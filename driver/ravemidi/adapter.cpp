// ravemidi adapter: DriverEntry, PortCls AddDevice, control device + IOCTL
// dispatch, dynamic-port manager. SPDX-License-Identifier: AGPL-3.0-or-later
//
// Virtual-port core is complete: control plane, dynamic subdevice register/
// unregister, winmm FriendlyName stamp, and the cancel-safe IOCTL_READ pend.
// Mirror-tap (driver-level controller splitter) lives in mirror.cpp.
// See ../../.devnotes/RAVEMIDI_DRIVER_DESIGN.md.

#include <portcls.h>
#include <ntstrsafe.h>
#include <wdmsec.h>
#include <devpkey.h>
#include "ioctl.h"
#include "miniport.h"

#ifndef LOCALE_NEUTRAL
#define LOCALE_NEUTRAL 0
#endif

#define RAVE_TAG RAVEMIDI_POOL_TAG

// -------- global port registry (one adapter devnode, N virtual ports) ----------
typedef struct _RAVE_ADAPTER {
    PDEVICE_OBJECT Fdo;
    PDEVICE_OBJECT Pdo;               // for IoGetDeviceInterfaces (FriendlyName stamp)
    FAST_MUTEX PortsLock;             // guards Ports list + IdSeq
    LIST_ENTRY Ports;                 // RAVE_PORT.Link
    ULONG IdSeq;
    PDEVICE_OBJECT CtlDevice;         // \Device\RaveMidiCtl
} RAVE_ADAPTER;

static RAVE_ADAPTER* g_Adapter;       // single root devnode

// -------- forward decls ----------
DRIVER_ADD_DEVICE RaveAddDevice;
static NTSTATUS RaveStartDevice(PDEVICE_OBJECT, PIRP, PRESOURCELIST);
static NTSTATUS RaveCreateCtlDevice(RAVE_ADAPTER*);
DRIVER_DISPATCH RaveCtlDispatch;
DRIVER_UNLOAD RaveUnload;

extern "C" DRIVER_INITIALIZE DriverEntry;
static PDRIVER_UNLOAD g_PcUnload;  // PortCls's unload, chained from RaveUnload

#pragma code_seg("INIT")
extern "C" NTSTATUS DriverEntry(PDRIVER_OBJECT DriverObject, PUNICODE_STRING RegistryPath)
{
    // NX nonpaged pool by default = HVCI requirement (POOL_NX_OPTIN in project).
    ExInitializeDriverRuntime(DrvRtPoolNxOptIn);

    // PcInitializeAdapterDriver wires the standard PortCls dispatch table
    // (Pnp/Power/System-control), AddDevice and DriverUnload.
    NTSTATUS st = PcInitializeAdapterDriver(DriverObject, RegistryPath, RaveAddDevice);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    // Keep PortCls's dispatch for its devices, but tap CREATE/CLOSE/DEVICE_CONTROL
    // for our control device (routed by DeviceExtension tag in RaveCtlDispatch).
    DriverObject->MajorFunction[IRP_MJ_DEVICE_CONTROL] = RaveCtlDispatch;
    DriverObject->MajorFunction[IRP_MJ_CREATE] = RaveCtlDispatch;
    DriverObject->MajorFunction[IRP_MJ_CLEANUP] = RaveCtlDispatch;
    DriverObject->MajorFunction[IRP_MJ_CLOSE] = RaveCtlDispatch;
    g_PcUnload = DriverObject->DriverUnload;
    DriverObject->DriverUnload = RaveUnload;
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveUnload(PDRIVER_OBJECT DriverObject)
{
    PAGED_CODE();
    RAVE_ADAPTER* a = g_Adapter;
    if (a) {
        g_Adapter = nullptr;
        if (a->CtlDevice) {
            UNICODE_STRING dos;
            RtlInitUnicodeString(&dos, RAVEMIDI_CTL_DOSNAME);
            IoDeleteSymbolicLink(&dos);
            IoDeleteDevice(a->CtlDevice);
        }
        // Ports are torn down by handle cleanup before unload (driver can't unload
        // with open handles); the list is empty here in the normal path.
        if (a->Pdo) {
            ObDereferenceObject(a->Pdo);
        }
        ExFreePoolWithTag(a, RAVE_TAG);
    }
    if (g_PcUnload) {
        g_PcUnload(DriverObject);
    }
}
#pragma code_seg()

#pragma code_seg("PAGE")
NTSTATUS RaveAddDevice(PDRIVER_OBJECT DriverObject, PDEVICE_OBJECT PhysicalDeviceObject)
{
    PAGED_CODE();
    // maxObjects=RAVEMIDI_MAX_PORTS subdevices under this FDO.
    return PcAddAdapterDevice(DriverObject, PhysicalDeviceObject, RaveStartDevice,
                              RAVEMIDI_MAX_PORTS, 0);
}
#pragma code_seg()

#pragma code_seg("PAGE")
static NTSTATUS RaveStartDevice(PDEVICE_OBJECT DeviceObject, PIRP Irp, PRESOURCELIST ResourceList)
{
    PAGED_CODE();
    UNREFERENCED_PARAMETER(Irp);
    UNREFERENCED_PARAMETER(ResourceList);

    if (g_Adapter) {
        return STATUS_SUCCESS;  // already started (surprise-restart)
    }
    RAVE_ADAPTER* a = (RAVE_ADAPTER*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*a), RAVE_TAG);
    if (!a) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    a->Fdo = DeviceObject;
    a->Pdo = IoGetLowerDeviceObject(DeviceObject);  // PDO for interface enum; deref in unload
    a->IdSeq = 0;
    ExInitializeFastMutex(&a->PortsLock);
    InitializeListHead(&a->Ports);
    NTSTATUS st = RaveCreateCtlDevice(a);
    if (!NT_SUCCESS(st)) {
        if (a->Pdo) {
            ObDereferenceObject(a->Pdo);
        }
        ExFreePoolWithTag(a, RAVE_TAG);
        return st;
    }
    g_Adapter = a;
    // TODO(mirror v1.1): re-arm persisted mirror groups from Parameters\Mirrors,
    // register IoRegisterPlugPlayNotification(KSCATEGORY_CAPTURE).
    return STATUS_SUCCESS;
}
#pragma code_seg()

// -------- control device ----------
// DeviceExtension marker so shared MajorFunction handlers can tell our control
// device from PortCls's audio FDO/subdevices.
static const ULONG kCtlTag = 'RvCd';
typedef struct _RAVE_CTL_EXT { ULONG Tag; } RAVE_CTL_EXT;

#pragma code_seg("PAGE")
static NTSTATUS RaveCreateCtlDevice(RAVE_ADAPTER* a)
{
    PAGED_CODE();
    UNICODE_STRING nt, dos;
    RtlInitUnicodeString(&nt, RAVEMIDI_CTL_NTNAME);
    RtlInitUnicodeString(&dos, RAVEMIDI_CTL_DOSNAME);

    PDEVICE_OBJECT dev = nullptr;
    // SDDL: SYSTEM + Administrators full; no world access (control plane is admin-only,
    // matching the rave-mate service running elevated). Tightened further at install
    // via INF to also grant the service SID.
    DECLARE_CONST_UNICODE_STRING(sddl, L"D:P(A;;GA;;;SY)(A;;GA;;;BA)");
    static const GUID clsGuid = RAVEMIDI_CTL_GUID;
    NTSTATUS st = IoCreateDeviceSecure(a->Fdo->DriverObject, sizeof(RAVE_CTL_EXT), &nt,
                                       RAVEMIDI_DEVICE_TYPE, FILE_DEVICE_SECURE_OPEN,
                                       FALSE, &sddl, &clsGuid, &dev);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    ((RAVE_CTL_EXT*)dev->DeviceExtension)->Tag = kCtlTag;
    dev->Flags |= DO_BUFFERED_IO;
    st = IoCreateSymbolicLink(&dos, &nt);
    if (!NT_SUCCESS(st)) {
        IoDeleteDevice(dev);
        return st;
    }
    a->CtlDevice = dev;
    dev->Flags &= ~DO_DEVICE_INITIALIZING;
    return STATUS_SUCCESS;
}
#pragma code_seg()

// -------- port lifecycle ----------
static RAVE_PORT* FindPortLocked(RAVE_ADAPTER* a, ULONG id)
{
    for (PLIST_ENTRY e = a->Ports.Flink; e != &a->Ports; e = e->Flink) {
        RAVE_PORT* p = CONTAINING_RECORD(e, RAVE_PORT, Link);
        if (p->Id == id) {
            return p;
        }
    }
    return nullptr;
}

static BOOLEAN NameSane(const WCHAR* n)
{
    SIZE_T len = 0;
    for (; len < RAVEMIDI_MAX_NAME; len++) {
        WCHAR c = n[len];
        if (c == 0) {
            break;
        }
        if (c < 0x20) {
            return FALSE;  // no control chars
        }
    }
    return (len > 0 && len < RAVEMIDI_MAX_NAME) ? TRUE : FALSE;  // non-empty, NUL-terminated in-bounds
}

// -------- winmm FriendlyName stamp --------
// After PcRegisterSubdevice, set DEVPKEY_DeviceInterface_FriendlyName on the port's
// KSCATEGORY_AUDIO interface so wdmaud reports p->Name in MIDIINCAPS.szPname (else it
// falls back to a generic default). Best-effort: the port still works if this fails.
static SIZE_T RaveWcsLen(PCWSTR s)  // no CRT dep (NODEFAULTLIB kernel link)
{
    SIZE_T n = 0;
    while (s[n]) {
        n++;
    }
    return n;
}

#pragma code_seg("PAGE")
static VOID StampFriendlyName(RAVE_ADAPTER* a, PCWSTR refString, PCWSTR name)
{
    PAGED_CODE();
    if (!a->Pdo) {
        return;
    }
    PWSTR list = nullptr;
    NTSTATUS st = IoGetDeviceInterfaces(&KSCATEGORY_AUDIO, a->Pdo,
                                        DEVICE_INTERFACE_INCLUDE_NONACTIVE, &list);
    if (!NT_SUCCESS(st) || !list) {
        return;
    }
    UNICODE_STRING ref;
    RtlInitUnicodeString(&ref, refString);
    ULONG nameBytes = (ULONG)((RaveWcsLen(name) + 1) * sizeof(WCHAR));
    for (PWSTR sym = list; *sym; sym += RaveWcsLen(sym) + 1) {
        // The interface symlink ends with "\<refString>" — match the tail exactly so
        // "RavePort1" doesn't match "RavePort10".
        PCWSTR tail = nullptr;
        for (PCWSTR c = sym; *c; c++) {
            if (*c == L'\\') {
                tail = c + 1;
            }
        }
        if (!tail) {
            continue;
        }
        UNICODE_STRING t;
        RtlInitUnicodeString(&t, tail);
        if (!RtlEqualUnicodeString(&t, &ref, TRUE)) {
            continue;
        }
        UNICODE_STRING symU;
        RtlInitUnicodeString(&symU, sym);
        IoSetDeviceInterfacePropertyData(&symU, &DEVPKEY_DeviceInterface_FriendlyName,
            LOCALE_NEUTRAL, PLUGPLAY_PROPERTY_PERSISTENT, DEVPROP_TYPE_STRING,
            nameBytes, (PVOID)name);
    }
    ExFreePool(list);
}
#pragma code_seg()

// -------- cancel-safe queue for pended IOCTL_RAVEMIDI_READ IRPs --------
// IRPs park here until an app render stream Writes (miniport -> RavePortDeliverFromApp
// dequeues + completes). Cancellation + handle-close are handled by the CSQ / CLEANUP.
static IO_CSQ_INSERT_IRP RaveCsqInsert;
static IO_CSQ_REMOVE_IRP RaveCsqRemove;
static IO_CSQ_PEEK_NEXT_IRP RaveCsqPeek;
static IO_CSQ_ACQUIRE_LOCK RaveCsqAcquireLock;
static IO_CSQ_RELEASE_LOCK RaveCsqReleaseLock;
static IO_CSQ_COMPLETE_CANCELED_IRP RaveCsqCompleteCanceled;

static VOID RaveCsqInsert(PIO_CSQ Csq, PIRP Irp)
{
    RAVE_PORT* p = CONTAINING_RECORD(Csq, RAVE_PORT, ReadCsq);
    InsertTailList(&p->ReadIrps, &Irp->Tail.Overlay.ListEntry);
}
static VOID RaveCsqRemove(PIO_CSQ, PIRP Irp)
{
    RemoveEntryList(&Irp->Tail.Overlay.ListEntry);
}
static PIRP RaveCsqPeek(PIO_CSQ Csq, PIRP Irp, PVOID Context)
{
    UNREFERENCED_PARAMETER(Context);
    RAVE_PORT* p = CONTAINING_RECORD(Csq, RAVE_PORT, ReadCsq);
    PLIST_ENTRY next = Irp ? Irp->Tail.Overlay.ListEntry.Flink : p->ReadIrps.Flink;
    if (next == &p->ReadIrps) {
        return nullptr;
    }
    return CONTAINING_RECORD(next, IRP, Tail.Overlay.ListEntry);
}
_IRQL_raises_(DISPATCH_LEVEL)
static VOID RaveCsqAcquireLock(PIO_CSQ Csq, PKIRQL Irql)
{
    RAVE_PORT* p = CONTAINING_RECORD(Csq, RAVE_PORT, ReadCsq);
    KeAcquireSpinLock(&p->ReadLock, Irql);
}
_IRQL_requires_(DISPATCH_LEVEL)
static VOID RaveCsqReleaseLock(PIO_CSQ Csq, KIRQL Irql)
{
    RAVE_PORT* p = CONTAINING_RECORD(Csq, RAVE_PORT, ReadCsq);
    KeReleaseSpinLock(&p->ReadLock, Irql);
}
static VOID RaveCsqCompleteCanceled(PIO_CSQ, PIRP Irp)
{
    Irp->IoStatus.Status = STATUS_CANCELLED;
    Irp->IoStatus.Information = 0;
    IoCompleteRequest(Irp, IO_NO_INCREMENT);
}

// Complete (cancel) every pended READ owned by a closing file handle. Called on
// IRP_MJ_CLEANUP so the handle can close, and again on port destroy (then empty).
static VOID FlushReadsForFile(RAVE_ADAPTER* a, PFILE_OBJECT f)
{
    ExAcquireFastMutex(&a->PortsLock);
    for (PLIST_ENTRY e = a->Ports.Flink; e != &a->Ports; e = e->Flink) {
        RAVE_PORT* p = CONTAINING_RECORD(e, RAVE_PORT, Link);
        if (p->CreatorFile != f) {
            continue;
        }
        PIRP irp;
        while ((irp = IoCsqRemoveNextIrp(&p->ReadCsq, nullptr)) != nullptr) {
            irp->IoStatus.Status = STATUS_CANCELLED;
            irp->IoStatus.Information = 0;
            IoCompleteRequest(irp, IO_NO_INCREMENT);
        }
    }
    ExReleaseFastMutex(&a->PortsLock);
}

#pragma code_seg("PAGE")
static NTSTATUS CreatePort(RAVE_ADAPTER* a, PFILE_OBJECT creator, const RAVEMIDI_CREATE_PORT_IN* in, ULONG* outId)
{
    PAGED_CODE();
    if (in->Version != RAVEMIDI_PROTOCOL_VERSION || in->Kind > RaveMidiPortLoopback || !NameSane(in->Name)) {
        return STATUS_INVALID_PARAMETER;
    }
    RAVE_PORT* p = (RAVE_PORT*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*p), RAVE_TAG);
    if (!p) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    p->Kind = in->Kind;
    p->CreatorFile = creator;
    RtlCopyMemory(p->Name, in->Name, sizeof(p->Name));
    p->Name[RAVEMIDI_MAX_NAME - 1] = 0;
    RaveFifoInit(&p->ToApp);
    RaveFifoInit(&p->FromApp);
    KeInitializeSpinLock(&p->ReadLock);
    InitializeListHead(&p->ReadIrps);
    IoCsqInitialize(&p->ReadCsq, RaveCsqInsert, RaveCsqRemove, RaveCsqPeek,
                    RaveCsqAcquireLock, RaveCsqReleaseLock, RaveCsqCompleteCanceled);
    p->CaptureRunning = 0;
    p->StreamCount = 0;

    ExAcquireFastMutex(&a->PortsLock);
    p->Id = ++a->IdSeq;
    RtlStringCchPrintfW(p->RefString, RTL_NUMBER_OF(p->RefString), L"RavePort%lu", p->Id);
    InsertTailList(&a->Ports, &p->Link);
    ExReleaseFastMutex(&a->PortsLock);

    // Register the PortCls subdevice for this port's direction, then stamp the
    // winmm-visible name onto its device interface.
    NTSTATUS st = CreateRaveMiniport(a->Fdo, p, &p->PortUnknown);  // builds IMiniportMidi w/ per-kind filter
    if (NT_SUCCESS(st)) {
        st = PcRegisterSubdevice(a->Fdo, p->RefString, p->PortUnknown);
    }
    if (NT_SUCCESS(st)) {
        StampFriendlyName(a, p->RefString, p->Name);  // winmm szPname = p->Name
        *outId = p->Id;
        return STATUS_SUCCESS;
    }
    // rollback
    ExAcquireFastMutex(&a->PortsLock);
    RemoveEntryList(&p->Link);
    ExReleaseFastMutex(&a->PortsLock);
    if (p->PortUnknown) {
        p->PortUnknown->Release();
    }
    ExFreePoolWithTag(p, RAVE_TAG);
    return st;
}
#pragma code_seg()

#pragma code_seg("PAGE")
static NTSTATUS DestroyPort(RAVE_ADAPTER* a, PFILE_OBJECT caller, ULONG id)
{
    PAGED_CODE();
    ExAcquireFastMutex(&a->PortsLock);
    RAVE_PORT* p = FindPortLocked(a, id);
    if (!p) {
        ExReleaseFastMutex(&a->PortsLock);
        return STATUS_NOT_FOUND;
    }
    if (caller && p->CreatorFile != caller) {
        ExReleaseFastMutex(&a->PortsLock);
        return STATUS_ACCESS_DENIED;  // only the creator (or cleanup) tears down
    }
    if (p->StreamCount > 0) {
        ExReleaseFastMutex(&a->PortsLock);
        return STATUS_DEVICE_BUSY;    // park: a winmm client still holds a pin
    }
    RemoveEntryList(&p->Link);
    ExReleaseFastMutex(&a->PortsLock);

    // Cancel any still-pended READs (normally already flushed on CLEANUP).
    PIRP irp;
    while ((irp = IoCsqRemoveNextIrp(&p->ReadCsq, nullptr)) != nullptr) {
        irp->IoStatus.Status = STATUS_CANCELLED;
        irp->IoStatus.Information = 0;
        IoCompleteRequest(irp, IO_NO_INCREMENT);
    }
    // Tear down the winmm-visible subdevice, then drop our port refs.
    if (p->PortUnknown) {
        PUNREGISTERSUBDEVICE unreg = nullptr;
        if (NT_SUCCESS(p->PortUnknown->QueryInterface(IID_IUnregisterSubdevice, (PVOID*)&unreg))) {
            unreg->UnregisterSubdevice();
            unreg->Release();
        }
        if (p->PortMidi) {
            p->PortMidi->Release();
            p->PortMidi = nullptr;
        }
        p->PortUnknown->Release();
        p->PortUnknown = nullptr;
    }
    ExFreePoolWithTag(p, RAVE_TAG);
    return STATUS_SUCCESS;
}
#pragma code_seg()

// Drop every port a closing control handle created (crash-safety).
static VOID DestroyPortsForFile(RAVE_ADAPTER* a, PFILE_OBJECT f)
{
    for (;;) {
        ExAcquireFastMutex(&a->PortsLock);
        RAVE_PORT* victim = nullptr;
        for (PLIST_ENTRY e = a->Ports.Flink; e != &a->Ports; e = e->Flink) {
            RAVE_PORT* p = CONTAINING_RECORD(e, RAVE_PORT, Link);
            if (p->CreatorFile == f) {
                victim = p;
                break;
            }
        }
        ExReleaseFastMutex(&a->PortsLock);
        if (!victim) {
            break;
        }
        DestroyPort(a, f, victim->Id);
    }
}

// -------- IOCTL dispatch ----------
NTSTATUS RaveCtlDispatch(PDEVICE_OBJECT DeviceObject, PIRP Irp)
{
    RAVE_CTL_EXT* ext = (RAVE_CTL_EXT*)DeviceObject->DeviceExtension;
    if (!ext || ext->Tag != kCtlTag) {
        // Not our control device — hand back to PortCls's dispatch.
        return PcDispatchIrp(DeviceObject, Irp);
    }
    PIO_STACK_LOCATION s = IoGetCurrentIrpStackLocation(Irp);
    NTSTATUS st = STATUS_SUCCESS;
    ULONG_PTR info = 0;
    RAVE_ADAPTER* a = g_Adapter;

    switch (s->MajorFunction) {
    case IRP_MJ_CREATE:
        break;
    case IRP_MJ_CLEANUP:
        // Handle closing: cancel its pended READs so CLOSE can proceed.
        if (a) {
            FlushReadsForFile(a, s->FileObject);
        }
        break;
    case IRP_MJ_CLOSE:
        if (a) {
            DestroyPortsForFile(a, s->FileObject);
        }
        break;
    case IRP_MJ_DEVICE_CONTROL: {
        ULONG code = s->Parameters.DeviceIoControl.IoControlCode;
        ULONG inLen = s->Parameters.DeviceIoControl.InputBufferLength;
        ULONG outLen = s->Parameters.DeviceIoControl.OutputBufferLength;
        PVOID buf = Irp->AssociatedIrp.SystemBuffer;  // buffered IO
        if (!a) {
            st = STATUS_DEVICE_NOT_READY;
            break;
        }
        switch (code) {
        case IOCTL_RAVEMIDI_CREATE_PORT: {
            if (inLen < sizeof(RAVEMIDI_CREATE_PORT_IN) || outLen < sizeof(RAVEMIDI_CREATE_PORT_OUT)) {
                st = STATUS_BUFFER_TOO_SMALL;
                break;
            }
            ULONG id = 0;
            st = CreatePort(a, s->FileObject, (RAVEMIDI_CREATE_PORT_IN*)buf, &id);
            if (NT_SUCCESS(st)) {
                ((RAVEMIDI_CREATE_PORT_OUT*)buf)->PortId = id;
                info = sizeof(RAVEMIDI_CREATE_PORT_OUT);
            }
            break;
        }
        case IOCTL_RAVEMIDI_DESTROY_PORT: {
            if (inLen < sizeof(RAVEMIDI_PORT_REF)) {
                st = STATUS_BUFFER_TOO_SMALL;
                break;
            }
            st = DestroyPort(a, s->FileObject, ((RAVEMIDI_PORT_REF*)buf)->PortId);
            break;
        }
        case IOCTL_RAVEMIDI_WRITE: {
            if (inLen < sizeof(RAVEMIDI_WRITE_IN)) {
                st = STATUS_BUFFER_TOO_SMALL;
                break;
            }
            RAVEMIDI_WRITE_IN* w = (RAVEMIDI_WRITE_IN*)buf;
            if (inLen < sizeof(RAVEMIDI_WRITE_IN) + w->ByteCount) {
                st = STATUS_INVALID_PARAMETER;
                break;
            }
            ExAcquireFastMutex(&a->PortsLock);
            RAVE_PORT* p = FindPortLocked(a, w->PortId);
            ExReleaseFastMutex(&a->PortsLock);
            if (!p) {
                st = STATUS_NOT_FOUND;
                break;
            }
            RaveFifoPush(&p->ToApp, (UCHAR*)(w + 1), w->ByteCount);
            RavePortNotifyToApp(p);  // kick capture service if a stream is running
            break;
        }
        case IOCTL_RAVEMIDI_READ: {
            if (inLen < sizeof(RAVEMIDI_PORT_REF) || outLen == 0) {
                st = STATUS_INVALID_PARAMETER;
                break;
            }
            ExAcquireFastMutex(&a->PortsLock);
            RAVE_PORT* p = FindPortLocked(a, ((RAVEMIDI_PORT_REF*)buf)->PortId);
            ExReleaseFastMutex(&a->PortsLock);
            if (!p) {
                st = STATUS_NOT_FOUND;
                break;
            }
            // Pend cancel-safely, then try to satisfy immediately if data already
            // waits (avoids the lost-wakeup race with a concurrent app Write).
            IoMarkIrpPending(Irp);
            IoCsqInsertIrp(&p->ReadCsq, Irp, nullptr);
            RavePortDeliverFromApp(p);
            return STATUS_PENDING;  // owns the IRP now — skip shared completion
        }
        default:
            st = STATUS_INVALID_DEVICE_REQUEST;
            break;
        }
        break;
    }
    default:
        st = STATUS_INVALID_DEVICE_REQUEST;
        break;
    }

    Irp->IoStatus.Status = st;
    Irp->IoStatus.Information = info;
    IoCompleteRequest(Irp, IO_NO_INCREMENT);
    return st;
}
