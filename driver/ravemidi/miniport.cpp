// ravemidi IMiniportMidi implementation: per-kind filter descriptors + streams.
// SPDX-License-Identifier: AGPL-3.0-or-later
#include "miniport.h"
#include "managed.h"

// ntddk-only export (portcls pulls wdm.h); prototype matches ntddk.h exactly.
extern "C" NTKERNELAPI HANDLE NTAPI PsGetCurrentProcessId(VOID);

#define RAVE_TAG RAVEMIDI_POOL_TAG

// -------- data ranges (layout dictated by KSDATARANGE_MUSIC / KSDATAFORMAT) ----
static KSDATARANGE_MUSIC MidiDataRange =
{
    {
        sizeof(KSDATARANGE_MUSIC), 0, 0, 0,
        STATICGUIDOF(KSDATAFORMAT_TYPE_MUSIC),
        STATICGUIDOF(KSDATAFORMAT_SUBTYPE_MIDI),
        STATICGUIDOF(KSDATAFORMAT_SPECIFIER_NONE)
    },
    STATICGUIDOF(KSMUSIC_TECHNOLOGY_PORT),
    0, 0, 0xFFFF
};
static PKSDATARANGE MidiDataRanges[] = { PKSDATARANGE(&MidiDataRange) };

static KSDATARANGE BridgeDataRange =
{
    sizeof(KSDATARANGE), 0, 0, 0,
    STATICGUIDOF(KSDATAFORMAT_TYPE_MUSIC),
    STATICGUIDOF(KSDATAFORMAT_SUBTYPE_MIDI_BUS),
    STATICGUIDOF(KSDATAFORMAT_SPECIFIER_NONE)
};
static PKSDATARANGE BridgeDataRanges[] = { &BridgeDataRange };

// -------- pin builders --------------------------------------------------------
// winmm lists a midiIn per MIDI capture pin and a midiOut per render pin,
// independently — an out-only port (apps see INPUT, the echo-killer) is simply
// a filter with only the capture streaming pin.

#define RAVE_PIN_STREAM(flow) \
    { 1, 1, 0, nullptr, { 0, nullptr, 0, nullptr, \
      RTL_NUMBER_OF(MidiDataRanges), MidiDataRanges, \
      (flow), KSPIN_COMMUNICATION_SINK, (GUID*)&KSCATEGORY_AUDIO, nullptr, { 0 } } }

#define RAVE_PIN_BRIDGE(flow) \
    { 0, 0, 0, nullptr, { 0, nullptr, 0, nullptr, \
      RTL_NUMBER_OF(BridgeDataRanges), BridgeDataRanges, \
      (flow), KSPIN_COMMUNICATION_NONE, (GUID*)&KSCATEGORY_AUDIO, nullptr, { 0 } } }

// OutOnly: [0] capture stream (apps' midi INPUT), [1] bridge in.
static PCPIN_DESCRIPTOR PinsOutOnly[] =
{
    RAVE_PIN_STREAM(KSPIN_DATAFLOW_OUT),
    RAVE_PIN_BRIDGE(KSPIN_DATAFLOW_IN),
};
static PCCONNECTION_DESCRIPTOR ConnOutOnly[] =
{
    { PCFILTER_NODE, 1, PCFILTER_NODE, 0 },
};
static GUID CatsOutOnly[] =
{
    STATICGUIDOF(KSCATEGORY_AUDIO),
    STATICGUIDOF(KSCATEGORY_CAPTURE),
};

// InOnly: [0] render stream (apps' midi OUTPUT), [1] bridge out.
static PCPIN_DESCRIPTOR PinsInOnly[] =
{
    RAVE_PIN_STREAM(KSPIN_DATAFLOW_IN),
    RAVE_PIN_BRIDGE(KSPIN_DATAFLOW_OUT),
};
static PCCONNECTION_DESCRIPTOR ConnInOnly[] =
{
    { PCFILTER_NODE, 0, PCFILTER_NODE, 1 },
};
static GUID CatsInOnly[] =
{
    STATICGUIDOF(KSCATEGORY_AUDIO),
    STATICGUIDOF(KSCATEGORY_RENDER),
};

// Bidi + Loopback: [0] render, [1] capture, [2] bridge out, [3] bridge in.
static PCPIN_DESCRIPTOR PinsBidi[] =
{
    RAVE_PIN_STREAM(KSPIN_DATAFLOW_IN),
    RAVE_PIN_STREAM(KSPIN_DATAFLOW_OUT),
    RAVE_PIN_BRIDGE(KSPIN_DATAFLOW_OUT),
    RAVE_PIN_BRIDGE(KSPIN_DATAFLOW_IN),
};
static PCCONNECTION_DESCRIPTOR ConnBidi[] =
{
    { PCFILTER_NODE, 0, PCFILTER_NODE, 2 },
    { PCFILTER_NODE, 3, PCFILTER_NODE, 1 },
};
static GUID CatsBidi[] =
{
    STATICGUIDOF(KSCATEGORY_AUDIO),
    STATICGUIDOF(KSCATEGORY_RENDER),
    STATICGUIDOF(KSCATEGORY_CAPTURE),
};

// -------- filter automation: per-pin factory name ------------------------------
// Windows MIDI Services (midisrv) names MIDI1 ports from KSPROPERTY_PIN_NAME.
// portcls has no default answer for descriptors without a Name GUID (the query
// fails), so WMS fell back to generic endpoint naming. One static handler serves
// every port: the target miniport resolves its own RAVE_PORT at request time.
static NTSTATUS RavePinNameHandler(PPCPROPERTY_REQUEST req);

static PCPROPERTY_ITEM RaveFilterPropItems[] =
{
    {
        &KSPROPSETID_Pin,
        KSPROPERTY_PIN_NAME,
        PCPROPERTY_ITEM_FLAG_GET | PCPROPERTY_ITEM_FLAG_BASICSUPPORT,
        RavePinNameHandler
    },
};
DEFINE_PCAUTOMATION_TABLE_PROP(RaveFilterAutomation, RaveFilterPropItems);

#pragma code_seg("PAGE")
static NTSTATUS RavePinNameHandler(PPCPROPERTY_REQUEST req)
{
    PAGED_CODE();
    if (!req || !req->MajorTarget) {
        return STATUS_INVALID_PARAMETER;
    }
    if (req->Verb & KSPROPERTY_TYPE_BASICSUPPORT) {
        if (req->ValueSize < sizeof(ULONG)) {
            req->ValueSize = sizeof(ULONG);
            return STATUS_BUFFER_OVERFLOW;
        }
        *(PULONG)req->Value = KSPROPERTY_TYPE_BASICSUPPORT | KSPROPERTY_TYPE_GET;
        req->ValueSize = sizeof(ULONG);
        return STATUS_SUCCESS;
    }
    if (!(req->Verb & KSPROPERTY_TYPE_GET)) {
        return STATUS_INVALID_DEVICE_REQUEST;
    }
    if (req->InstanceSize < sizeof(ULONG)) {  // KSP_PIN tail: PinId (+ Reserved)
        return STATUS_INVALID_PARAMETER;
    }
    // Filter-scoped request: MajorTarget is the miniport. QI pins the object and
    // yields our exact PMINIPORTMIDI, so the downcast is safe.
    PMINIPORTMIDI mm = nullptr;
    if (!NT_SUCCESS(req->MajorTarget->QueryInterface(IID_IMiniportMidi, (PVOID*)&mm))) {
        return STATUS_INVALID_PARAMETER;
    }
    RaveMiniport* mp = static_cast<RaveMiniport*>(mm);
    RAVE_PORT* p = mp->Ctx();
    NTSTATUS st;
    if (!p) {
        st = STATUS_DEVICE_NOT_READY;
    } else {
        ULONG pinId = *(PULONG)req->Instance;
        ULONG pinCount = (p->Kind == RaveMidiPortOutOnly || p->Kind == RaveMidiPortInOnly) ? 2u : 4u;
        if (pinId >= pinCount) {
            st = STATUS_INVALID_PARAMETER;
        } else {
            // Every pin carries the port name; WMS strips the (identical) filter
            // name from it, so combined names never duplicate.
            SIZE_T chars = 0;
            while (p->Name[chars]) {
                chars++;
            }
            ULONG bytes = (ULONG)((chars + 1) * sizeof(WCHAR));
            if (req->ValueSize == 0) {
                req->ValueSize = bytes;
                st = STATUS_BUFFER_OVERFLOW;
            } else if (req->ValueSize < bytes) {
                st = STATUS_BUFFER_TOO_SMALL;
            } else {
                RtlCopyMemory(req->Value, p->Name, bytes);  // Name immutable post-create
                req->ValueSize = bytes;
                st = STATUS_SUCCESS;
            }
        }
    }
    mm->Release();
    return st;
}
#pragma code_seg()

#define RAVE_FILTER(pins, conns, cats) \
    { 0, &RaveFilterAutomation, sizeof(PCPIN_DESCRIPTOR), RTL_NUMBER_OF(pins), (pins), \
      sizeof(PCNODE_DESCRIPTOR), 0, nullptr, \
      RTL_NUMBER_OF(conns), (conns), RTL_NUMBER_OF(cats), (cats) }

static PCFILTER_DESCRIPTOR FilterOutOnly = RAVE_FILTER(PinsOutOnly, ConnOutOnly, CatsOutOnly);
static PCFILTER_DESCRIPTOR FilterInOnly  = RAVE_FILTER(PinsInOnly,  ConnInOnly,  CatsInOnly);
static PCFILTER_DESCRIPTOR FilterBidi    = RAVE_FILTER(PinsBidi,    ConnBidi,    CatsBidi);

// -------- shared-state helpers -------------------------------------------------
VOID RavePortNotifyToApp(RAVE_PORT* port)
{
    // DISPATCH-safe: Notify is documented callable at <= DISPATCH_LEVEL. The port
    // then calls RaveStream::Read to drain ToApp into the winmm client's buffer.
    if (port->CaptureRunning && port->PortMidi && port->ServiceGroup) {
        InterlockedIncrement(&port->NotifyCalls);
        port->PortMidi->Notify(port->ServiceGroup);
    }
}

VOID RaveTracePush(RAVE_PORT* port, ULONG dir, const UCHAR* bytes, ULONG len)
{
    // Bounded ring, overwrite-oldest; spinlock so DISPATCH-level writers serialize.
    KIRQL irql;
    KeAcquireSpinLock(&port->TraceLock, &irql);
    RAVEMIDI_TRACE_ENTRY* e = &port->Trace[port->TraceSeq % RAVEMIDI_TRACE_ENTRIES];
    e->Seq = port->TraceSeq++;
    e->Time100ns = KeQueryInterruptTime();
    e->Dir = dir;
    e->Len = len;
    ULONG n = (len < RAVEMIDI_TRACE_BYTES) ? len : RAVEMIDI_TRACE_BYTES;
    RtlZeroMemory(e->Bytes, RAVEMIDI_TRACE_BYTES);
    if (n && bytes) {
        RtlCopyMemory(e->Bytes, bytes, n);
    }
    KeReleaseSpinLock(&port->TraceLock, irql);
}

VOID RavePortDeliverFromApp(RAVE_PORT* port)
{
    // Pair app-render bytes (FromApp ring) with pended IOCTL_RAVEMIDI_READ IRPs.
    // Runs from the miniport stream Write (<= DISPATCH_LEVEL) and from the READ
    // path; IoCsqRemoveNextIrp + IoCompleteRequest are both DISPATCH-safe.
    for (;;) {
        if (RaveFifoCount(&port->FromApp) == 0) {
            break;
        }
        PIRP irp = IoCsqRemoveNextIrp(&port->ReadCsq, nullptr);
        if (!irp) {
            break;  // no waiter; bytes stay buffered for the next READ
        }
        PIO_STACK_LOCATION s = IoGetCurrentIrpStackLocation(irp);
        ULONG cap = s->Parameters.DeviceIoControl.OutputBufferLength;
        ULONG n = RaveFifoPop(&port->FromApp, (UCHAR*)irp->AssociatedIrp.SystemBuffer, cap);
        if (n) {
            RaveTracePush(port, RaveTraceReadPop, (UCHAR*)irp->AssociatedIrp.SystemBuffer, n);
        }
        irp->IoStatus.Status = STATUS_SUCCESS;
        irp->IoStatus.Information = n;  // may be 0 if a concurrent drain won the race
        IoCompleteRequest(irp, IO_NO_INCREMENT);
    }
}

// -------- RaveMiniport ---------------------------------------------------------
NTSTATUS CreateRaveMiniport(PDEVICE_OBJECT Fdo, RAVE_PORT* ctx, PUNKNOWN* OutUnknown)
{
    PAGED_CODE();
    *OutUnknown = nullptr;

    RaveMiniport* mp = new (NonPagedPoolNx, RAVE_TAG) RaveMiniport(nullptr, ctx);
    if (!mp) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    InterlockedIncrement(&ctx->OwnerRefs);  // miniport share — dropped in ~RaveMiniport
    mp->AddRef();  // our local ref
    ctx->Miniport = mp;

    PPORT port = nullptr;
    NTSTATUS st = PcNewPort(&port, CLSID_PortMidi);
    if (NT_SUCCESS(st)) {
        // Virtual device: no start-IRP, no resources (sysvad's dynamic sideband
        // endpoints init the same way).
        st = port->Init(Fdo, nullptr, PUNKNOWN(PMINIPORTMIDI(mp)), nullptr, nullptr);
    }
    if (NT_SUCCESS(st)) {
        st = port->QueryInterface(IID_IPortMidi, (PVOID*)&ctx->PortMidi);
    }
    if (NT_SUCCESS(st)) {
        *OutUnknown = PUNKNOWN(port);  // caller owns this ref (registered subdevice)
    } else {
        if (port) {
            port->Release();
        }
        ctx->Miniport = nullptr;
    }
    mp->Release();  // port holds its own ref on success
    return st;
}

RaveMiniport::~RaveMiniport()
{
    // Runs when the LAST filter handle drops (often svchost/wdmaud, long after
    // DestroyPort) — m_Ctx stays valid because this object holds an OwnerRefs
    // share; without it these writes land in freed pool (BSOD 0x50, 2026-07-13).
    if (m_Ctx) {
        m_Ctx->ServiceGroup = nullptr;
        m_Ctx->Miniport = nullptr;
        RavePortDeref(m_Ctx);
        m_Ctx = nullptr;
    }
    if (m_ServiceGroup) {
        m_ServiceGroup->Release();
        m_ServiceGroup = nullptr;
    }
    if (m_Port) {
        m_Port->Release();
        m_Port = nullptr;
    }
}

STDMETHODIMP_(NTSTATUS) RaveMiniport::NonDelegatingQueryInterface(REFIID Interface, PVOID* Object)
{
    if (IsEqualGUIDAligned(Interface, IID_IUnknown)) {
        *Object = PVOID(PUNKNOWN(PMINIPORTMIDI(this)));
    } else if (IsEqualGUIDAligned(Interface, IID_IMiniport)) {
        *Object = PVOID(PMINIPORT(this));
    } else if (IsEqualGUIDAligned(Interface, IID_IMiniportMidi)) {
        *Object = PVOID(PMINIPORTMIDI(this));
    } else {
        *Object = nullptr;
        return STATUS_INVALID_PARAMETER;
    }
    PUNKNOWN(*Object)->AddRef();
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveMiniport::Init(
    PUNKNOWN UnknownAdapter, PRESOURCELIST ResourceList,
    PPORTMIDI Port, PSERVICEGROUP* ServiceGroup)
{
    PAGED_CODE();
    UNREFERENCED_PARAMETER(UnknownAdapter);
    UNREFERENCED_PARAMETER(ResourceList);

    m_Port = Port;
    m_Port->AddRef();

    NTSTATUS st = PcNewServiceGroup(&m_ServiceGroup, nullptr);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    if (m_Ctx) {
        m_Ctx->ServiceGroup = m_ServiceGroup;
    }
    *ServiceGroup = m_ServiceGroup;
    m_ServiceGroup->AddRef();  // out-param ref for the port
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveMiniport::GetDescription(PPCFILTER_DESCRIPTOR* OutFilterDescriptor)
{
    PAGED_CODE();
    switch (m_Ctx ? m_Ctx->Kind : RaveMidiPortBidi) {
    case RaveMidiPortOutOnly:
        *OutFilterDescriptor = &FilterOutOnly;
        break;
    case RaveMidiPortInOnly:
        *OutFilterDescriptor = &FilterInOnly;
        break;
    default:
        *OutFilterDescriptor = &FilterBidi;
        break;
    }
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveMiniport::DataRangeIntersection(
    ULONG PinId, PKSDATARANGE DataRange, PKSDATARANGE MatchingDataRange,
    ULONG OutputBufferLength, PVOID ResultantFormat, PULONG ResultantFormatLength)
{
    UNREFERENCED_PARAMETER(PinId);
    UNREFERENCED_PARAMETER(DataRange);
    UNREFERENCED_PARAMETER(MatchingDataRange);
    UNREFERENCED_PARAMETER(OutputBufferLength);
    UNREFERENCED_PARAMETER(ResultantFormat);
    UNREFERENCED_PARAMETER(ResultantFormatLength);
    return STATUS_NOT_IMPLEMENTED;  // port driver default intersection
}

STDMETHODIMP_(void) RaveMiniport::Service()
{
    // Capture drain happens in stream Read; nothing per-service.
}

STDMETHODIMP_(NTSTATUS) RaveMiniport::NewStream(
    PMINIPORTMIDISTREAM* Stream, PUNKNOWN OuterUnknown, POOL_TYPE PoolType,
    ULONG Pin, BOOLEAN Capture, PKSDATAFORMAT DataFormat, PSERVICEGROUP* ServiceGroup)
{
    PAGED_CODE();
    UNREFERENCED_PARAMETER(PoolType);
    UNREFERENCED_PARAMETER(DataFormat);

    if (!m_Ctx) {
        return STATUS_DEVICE_NOT_READY;
    }
    // Validate pin/direction against the kind's descriptor.
    BOOLEAN ok = FALSE;
    switch (m_Ctx->Kind) {
    case RaveMidiPortOutOnly:
        ok = (Pin == 0 && Capture);
        break;
    case RaveMidiPortInOnly:
        ok = (Pin == 0 && !Capture);
        break;
    default:  // Bidi / Loopback
        ok = (Pin == 0 && !Capture) || (Pin == 1 && Capture);
        break;
    }
    if (!ok) {
        return STATUS_INVALID_PARAMETER;
    }

    InterlockedIncrement(&m_Ctx->NewStreamCalls);
    RaveStream* s = new (NonPagedPoolNx, RAVE_TAG) RaveStream(OuterUnknown);
    if (!s) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    s->AddRef();
    NTSTATUS st = s->Init(this, Capture);
    if (NT_SUCCESS(st)) {
        st = s->QueryInterface(IID_IMiniportMidiStream, (PVOID*)Stream);
        if (NT_SUCCESS(st) && Capture && m_ServiceGroup) {
            *ServiceGroup = m_ServiceGroup;
            m_ServiceGroup->AddRef();
        }
    }
    s->Release();
    return st;
}

// -------- RaveStream -----------------------------------------------------------
NTSTATUS RaveStream::Init(RaveMiniport* Miniport, BOOLEAN Capture)
{
    PAGED_CODE();
    m_Miniport = Miniport;
    m_Miniport->AddRef();
    m_Capture = Capture;
    m_State = KSSTATE_STOP;
    // Pin creation runs in the opening app's context — this pid identifies the
    // client for loopback self-echo suppression.
    m_Pid = PsGetCurrentProcessId();
    RAVE_PORT* p = Miniport->Ctx();
    if (Capture) {
        p->CapturePid = m_Pid;
    }
    InterlockedIncrement(&p->StreamCount);
    return STATUS_SUCCESS;
}

RaveStream::~RaveStream()
{
    if (m_Miniport) {
        RAVE_PORT* p = m_Miniport->Ctx();
        if (p) {
            if (m_Capture) {
                InterlockedExchange(&p->CaptureRunning, 0);
                if (p->CapturePid == m_Pid) {
                    p->CapturePid = nullptr;
                }
            }
            InterlockedDecrement(&p->StreamCount);
        }
        m_Miniport->Release();
        m_Miniport = nullptr;
    }
}

STDMETHODIMP_(NTSTATUS) RaveStream::NonDelegatingQueryInterface(REFIID Interface, PVOID* Object)
{
    if (IsEqualGUIDAligned(Interface, IID_IUnknown)) {
        *Object = PVOID(PUNKNOWN(PMINIPORTMIDISTREAM(this)));
    } else if (IsEqualGUIDAligned(Interface, IID_IMiniportMidiStream)) {
        *Object = PVOID(PMINIPORTMIDISTREAM(this));
    } else {
        *Object = nullptr;
        return STATUS_INVALID_PARAMETER;
    }
    PUNKNOWN(*Object)->AddRef();
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveStream::SetFormat(PKSDATAFORMAT DataFormat)
{
    PAGED_CODE();
    if (!DataFormat) {
        return STATUS_INVALID_PARAMETER;
    }
    // Only raw MIDI bytes are offered in our data ranges; accept matching sets.
    if (!IsEqualGUIDAligned(DataFormat->MajorFormat, KSDATAFORMAT_TYPE_MUSIC) ||
        !IsEqualGUIDAligned(DataFormat->SubFormat, KSDATAFORMAT_SUBTYPE_MIDI)) {
        return STATUS_INVALID_PARAMETER;
    }
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveStream::SetState(KSSTATE State)
{
    // Runs at <= DISPATCH_LEVEL; keep nonpaged.
    m_State = State;
    if (m_Miniport && m_Miniport->Ctx()) {
        InterlockedExchange(&m_Miniport->Ctx()->LastSetState, (LONG)State);
    }
    if (m_Capture && m_Miniport) {
        RAVE_PORT* p = m_Miniport->Ctx();
        if (p) {
            InterlockedExchange(&p->CaptureRunning, (State == KSSTATE_RUN) ? 1 : 0);
            if (State == KSSTATE_RUN && RaveFifoCount(&p->ToApp) > 0) {
                RavePortNotifyToApp(p);  // deliver anything buffered pre-RUN
            }
        }
    }
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveStream::Read(PVOID BufferAddress, ULONG BufferLength, PULONG BytesRead)
{
    // Port calls this after Notify, possibly at DISPATCH_LEVEL.
    *BytesRead = 0;
    if (!m_Capture || m_State != KSSTATE_RUN || !m_Miniport) {
        return STATUS_SUCCESS;
    }
    RAVE_PORT* p = m_Miniport->Ctx();
    if (p) {
        InterlockedIncrement(&p->ReadCalls);
        InterlockedExchange(&p->LastReadBufLen, (LONG)BufferLength);
        *BytesRead = RaveFifoPop(&p->ToApp, (UCHAR*)BufferAddress, BufferLength);
        if (*BytesRead == 0) {
            InterlockedIncrement(&p->ReadZeroCalls);
        } else {
            InterlockedAdd64(&p->ReadBytesTotal, (LONG64)*BytesRead);
            RaveTracePush(p, RaveTraceReadPop, (UCHAR*)BufferAddress, *BytesRead);
        }
    }
    return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) RaveStream::Write(PVOID BufferAddress, ULONG BytesToWrite, PULONG BytesWritten)
{
    // App render data. Consume fully even on FIFO overflow (drop, never stall
    // the port's send loop).
    *BytesWritten = BytesToWrite;
    if (m_Capture || !m_Miniport) {
        return STATUS_INVALID_DEVICE_REQUEST;
    }
    if (m_State != KSSTATE_RUN || BytesToWrite == 0) {
        return STATUS_SUCCESS;
    }
    RAVE_PORT* p = m_Miniport->Ctx();
    if (!p) {
        return STATUS_SUCCESS;
    }
    InterlockedIncrement(&p->StreamWriteCalls);
    RaveTracePush(p, RaveTraceFromApp, (const UCHAR*)BufferAddress, BytesToWrite);
    if (p->Kind == RaveMidiPortLoopback) {
        // Self-echo suppression: never hand a process back its own bytes. An app
        // holding both ends of the cable (Rekordbox in+out) cannot loop itself.
        if (p->CapturePid && p->CapturePid == m_Pid) {
            InterlockedIncrement(&p->LoopSuppressed);
            RaveTracePush(p, RaveTraceLoopDrop, (const UCHAR*)BufferAddress, BytesToWrite);
            return STATUS_SUCCESS;
        }
        RaveFifoPush(&p->ToApp, (const UCHAR*)BufferAddress, BytesToWrite);
        RaveTracePush(p, RaveTraceToApp, (const UCHAR*)BufferAddress, BytesToWrite);
        RavePortNotifyToApp(p);
    } else {
        RaveFifoPush(&p->FromApp, (const UCHAR*)BufferAddress, BytesToWrite);
        if (p->FeedbackArm) {
            // managed reserved port: tee to the device render pin (IOCTL_READ
            // still sees FromApp — the tee never consumes)
            RaveFifoPush(&p->Feedback, (const UCHAR*)BufferAddress, BytesToWrite);
            RaveManagedKickFeedback();
        }
        RavePortDeliverFromApp(p);
    }
    return STATUS_SUCCESS;
}
