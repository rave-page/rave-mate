// ravemidi adapter: DriverEntry, PortCls AddDevice, control device + IOCTL
// dispatch, dynamic-port manager. SPDX-License-Identifier: AGPL-3.0-or-later
//
// SCAFFOLD: structure + control-plane + port lifecycle are here; the KS streaming
// internals (capture Notify drain, mirror KS-client tap) are marked TODO and land
// with the miniport data path. Not yet buildable end-to-end. See
// ../../.devnotes/RAVEMIDI_DRIVER_DESIGN.md.

#include <portcls.h>
#include <ntstrsafe.h>
#include <wdmsec.h>
#include "ioctl.h"
#include "miniport.h"

#define RAVE_TAG RAVEMIDI_POOL_TAG

// -------- global port registry (one adapter devnode, N virtual ports) ----------
typedef struct _RAVE_ADAPTER {
    PDEVICE_OBJECT Fdo;
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
    a->IdSeq = 0;
    ExInitializeFastMutex(&a->PortsLock);
    InitializeListHead(&a->Ports);
    NTSTATUS st = RaveCreateCtlDevice(a);
    if (!NT_SUCCESS(st)) {
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
        // TODO: resolve the subdevice's KSCATEGORY_AUDIO interface symlink and
        // IoSetDeviceInterfacePropertyData(DEVPKEY_DeviceInterface_FriendlyName, p->Name)
        // (sysvad BthhfpDevice.cpp:3049 pattern) so szPname shows p->Name not the default.
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

    // TODO: IUnregisterSubdevice on p->PortUnknown before freeing; complete any
    // pended READ IRPs with STATUS_CANCELLED.
    if (p->PortUnknown) {
        p->PortUnknown->Release();
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
        case IOCTL_RAVEMIDI_READ:
            // TODO: pend on p->ReadCsq; completed by RavePortDeliverFromApp when
            // an app render stream Writes. Inverted-call pattern.
            st = STATUS_NOT_IMPLEMENTED;
            break;
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
