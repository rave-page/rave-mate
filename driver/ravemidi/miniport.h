// ravemidi PortCls MIDI miniport + per-port shared state.
// Original implementation against the documented PortCls DDI (IMiniportMidi /
// IMiniportMidiStream / IPortMidi); DMusUART sample used as read-only reference
// (MS-LPL — not derived from). SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once

#include <portcls.h>
#include <stdunk.h>
#include "kalloc.h"
#include "ioctl.h"
#include "fifo.h"

class RaveMiniport;

// Per-port shared state. Owned by the adapter's port manager; referenced by the
// miniport + IOCTL paths. Lifetime: created on IOCTL_CREATE_PORT, torn down on
// IOCTL_DESTROY_PORT / creator-handle cleanup (parked while streams are open).
typedef struct _RAVE_PORT {
    LIST_ENTRY Link;
    ULONG Id;
    ULONG Kind;                       // RAVEMIDI_PORT_KIND
    WCHAR Name[RAVEMIDI_MAX_NAME];
    WCHAR RefString[16];              // subdevice ref: L"RavePort<N>"
    PFILE_OBJECT CreatorFile;         // control-device handle that created it
    PUNKNOWN PortUnknown;             // IPort (registered subdevice), our ref
    PPORTMIDI PortMidi;               // for Notify(), our ref
    RaveMiniport* Miniport;           // weak (PortUnknown keeps it alive)
    PSERVICEGROUP ServiceGroup;       // capture notify target (miniport's, weak)
    RAVEMIDI_FIFO ToApp;              // IOCTL_WRITE -> app capture pin
    RAVEMIDI_FIFO FromApp;            // app render pin -> IOCTL_READ
    RAVEMIDI_FIFO Feedback;           // tee of FromApp -> device render pin (managed reserved port)
    volatile LONG FeedbackArm;        // managed engine arms while its render pin is bound
    IO_CSQ ReadCsq;                   // pended IOCTL_READ IRPs (cancel-safe)
    KSPIN_LOCK ReadLock;
    LIST_ENTRY ReadIrps;
    volatile LONG CaptureRunning;     // a capture stream is in KSSTATE_RUN
    volatile LONG StreamCount;        // open pin instances (blocks destroy)
    volatile LONG MirrorRefs;         // mirror groups fanning into this port (blocks destroy)
    // live counters for IOCTL_RAVEMIDI_QUERY_PORT (bring-up + health)
    volatile LONG NewStreamCalls;
    volatile LONG LastSetState;       // -1 = never
    volatile LONG ReadCalls;
    volatile LONG ReadZeroCalls;
    volatile LONG LastReadBufLen;
    volatile LONG NotifyCalls;
    volatile LONG WriteIoctls;
    volatile LONG StreamWriteCalls;
    volatile LONG64 ReadBytesTotal;
} RAVE_PORT;

// Cross-TU (mirror.cpp): reference an OUT_ONLY/BIDI port by id so a mirror can fan
// into it; the ref blocks the port's destroy until released.
RAVE_PORT* RaveRefOutputPort(ULONG id);
VOID RaveUnrefOutputPort(RAVE_PORT* p);

// Cross-TU (managed.cpp): driver-owned ports with NO creator file object —
// handle-close cleanup skips them, only the managed engine destroys them.
NTSTATUS RavePortCreateOwnerless(ULONG kind, PCWSTR name, RAVE_PORT** outPort);
NTSTATUS RavePortDestroyById(ULONG id);   // NULL-caller destroy; STATUS_DEVICE_BUSY while pinned

// Adapter-side helpers implemented in adapter.cpp, called from miniport streams.
VOID RavePortDeliverFromApp(RAVE_PORT* port);   // drain FromApp into pended READs
VOID RavePortNotifyToApp(RAVE_PORT* port);      // kick capture service if running

NTSTATUS CreateRaveMiniport(PDEVICE_OBJECT Fdo, RAVE_PORT* ctx, PUNKNOWN* OutUnknown);

class RaveMiniport : public IMiniportMidi, public CUnknown
{
private:
    RAVE_PORT* m_Ctx;
    PPORTMIDI m_Port;
    PSERVICEGROUP m_ServiceGroup;

public:
    DECLARE_STD_UNKNOWN();
    RaveMiniport(PUNKNOWN OuterUnknown, RAVE_PORT* ctx)
        : CUnknown(OuterUnknown), m_Ctx(ctx), m_Port(nullptr), m_ServiceGroup(nullptr) {}
    ~RaveMiniport();

    RAVE_PORT* Ctx() { return m_Ctx; }

    // IMiniport
    STDMETHODIMP_(NTSTATUS) GetDescription(PPCFILTER_DESCRIPTOR* OutFilterDescriptor);
    STDMETHODIMP_(NTSTATUS) DataRangeIntersection(
        ULONG PinId, PKSDATARANGE DataRange, PKSDATARANGE MatchingDataRange,
        ULONG OutputBufferLength, PVOID ResultantFormat, PULONG ResultantFormatLength);

    // IMiniportMidi
    STDMETHODIMP_(NTSTATUS) Init(
        PUNKNOWN UnknownAdapter, PRESOURCELIST ResourceList,
        PPORTMIDI Port, PSERVICEGROUP* ServiceGroup);
    STDMETHODIMP_(void) Service();
    STDMETHODIMP_(NTSTATUS) NewStream(
        PMINIPORTMIDISTREAM* Stream, PUNKNOWN OuterUnknown, POOL_TYPE PoolType,
        ULONG Pin, BOOLEAN Capture, PKSDATAFORMAT DataFormat, PSERVICEGROUP* ServiceGroup);
};

class RaveStream : public IMiniportMidiStream, public CUnknown
{
private:
    RaveMiniport* m_Miniport;     // ref held (AddRef in Init)
    BOOLEAN m_Capture;
    KSSTATE m_State;

public:
    DECLARE_STD_UNKNOWN();
    RaveStream(PUNKNOWN OuterUnknown)
        : CUnknown(OuterUnknown), m_Miniport(nullptr), m_Capture(FALSE), m_State(KSSTATE_STOP) {}
    ~RaveStream();

    NTSTATUS Init(RaveMiniport* Miniport, BOOLEAN Capture);

    // IMiniportMidiStream
    STDMETHODIMP_(NTSTATUS) SetFormat(PKSDATAFORMAT DataFormat);
    STDMETHODIMP_(NTSTATUS) SetState(KSSTATE State);
    STDMETHODIMP_(NTSTATUS) Read(PVOID BufferAddress, ULONG BufferLength, PULONG BytesRead);
    STDMETHODIMP_(NTSTATUS) Write(PVOID BufferAddress, ULONG BytesToWrite, PULONG BytesWritten);
};
