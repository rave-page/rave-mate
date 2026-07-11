// ravemidi control-plane protocol. Shared contract: mirrored byte-for-byte by
// internal/midi/ravemidi_windows.go (DeviceIoControl via syscall, no cgo).
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once

// Control device: \\.\RaveMidiCtl (symbolic link), interface GUID below.
// {B5E7A3F4-6C21-4D0B-9E58-2A47C0D91F63}
#define RAVEMIDI_CTL_GUID \
    { 0xB5E7A3F4, 0x6C21, 0x4D0B, { 0x9E, 0x58, 0x2A, 0x47, 0xC0, 0xD9, 0x1F, 0x63 } }
#define RAVEMIDI_CTL_DOSNAME L"\\DosDevices\\RaveMidiCtl"
#define RAVEMIDI_CTL_NTNAME  L"\\Device\\RaveMidiCtl"

#define RAVEMIDI_PROTOCOL_VERSION 2  // v2: INPUT_CFG.Filter + BIDI fan-outs + trace
#define RAVEMIDI_MAX_NAME 32   // WCHARs incl NUL; winmm szPname caps at 31+NUL
#define RAVEMIDI_MAX_PORTS 16
#define RAVEMIDI_MAX_MIRROR 8            // concurrent mirror groups
#define RAVEMIDI_MAX_MIRROR_OUT 4        // fan-out ports per mirror
#define RAVEMIDI_MAX_IFACE 256           // WCHARs: source KS interface symlink

// Port kind = which winmm endpoints the port exposes.
// OUT_ONLY: apps see an INPUT-only port; rave-mate writes into it (the LED-echo killer).
// IN_ONLY:  apps see an OUTPUT-only port; rave-mate reads what apps send.
// BIDI:     both endpoints. There is NO internal render->capture path: capture is fed
//           by the driver (tap/IOCTL_WRITE), render drains to IOCTL_READ + the device
//           feedback tee. An app on a BIDI port can never receive its own output —
//           loop-free by construction. Managed fan-outs are BIDI so DJ software gets
//           controller MIDI down AND can light LEDs up.
// LOOPBACK: classic cable — app render echoed to app capture, EXCEPT back to the
//           writing process itself (self-echo suppression: the loopMIDI feedback-loop
//           killer — an app holding both ends never hears itself).
typedef enum _RAVEMIDI_PORT_KIND {
    RaveMidiPortOutOnly  = 0,
    RaveMidiPortInOnly   = 1,
    RaveMidiPortBidi     = 2,
    RaveMidiPortLoopback = 3,
} RAVEMIDI_PORT_KIND;

#pragma pack(push, 1)

typedef struct _RAVEMIDI_CREATE_PORT_IN {
    ULONG Version;                    // RAVEMIDI_PROTOCOL_VERSION
    ULONG Kind;                       // RAVEMIDI_PORT_KIND
    WCHAR Name[RAVEMIDI_MAX_NAME];    // NUL-terminated friendly name
} RAVEMIDI_CREATE_PORT_IN;

typedef struct _RAVEMIDI_CREATE_PORT_OUT {
    ULONG PortId;
} RAVEMIDI_CREATE_PORT_OUT;

typedef struct _RAVEMIDI_PORT_REF {
    ULONG PortId;
} RAVEMIDI_PORT_REF;

// WRITE in-buffer: header then raw MIDI bytes (message-aligned: complete short
// messages / complete sysex chunks; no running status across calls).
typedef struct _RAVEMIDI_WRITE_IN {
    ULONG PortId;
    ULONG ByteCount;                  // bytes following this header
    // UCHAR Midi[ByteCount];
} RAVEMIDI_WRITE_IN;

// READ: in-buffer RAVEMIDI_PORT_REF; IRP pends until app render data arrives,
// completes with raw MIDI bytes in the out-buffer (inverted call).

// Mirror group: driver taps a hardware controller's MIDI capture pin and fans it
// into already-created OUT_ONLY ports. rave-mate first CREATE_PORTs the outputs,
// then CREATE_MIRROR references the source KS interface + those port ids. The tap
// lives in the driver, so the DJ software keeps getting controller MIDI even if
// rave-mate exits/crashes.
typedef struct _RAVEMIDI_CREATE_MIRROR_IN {
    ULONG Version;                          // RAVEMIDI_PROTOCOL_VERSION
    ULONG OutputCount;                      // 1..RAVEMIDI_MAX_MIRROR_OUT
    ULONG OutputPortIds[RAVEMIDI_MAX_MIRROR_OUT];
    WCHAR SourceInterface[RAVEMIDI_MAX_IFACE];  // KS capture-pin device-interface symlink
} RAVEMIDI_CREATE_MIRROR_IN;

typedef struct _RAVEMIDI_CREATE_MIRROR_OUT {
    ULONG MirrorId;
} RAVEMIDI_CREATE_MIRROR_OUT;

typedef struct _RAVEMIDI_MIRROR_REF {
    ULONG MirrorId;
} RAVEMIDI_MIRROR_REF;

// QUERY_PORT: live counters for bring-up + health surfacing (in: RAVEMIDI_PORT_REF).
typedef struct _RAVEMIDI_PORT_STATS {
    ULONG PortId;
    ULONG Kind;
    ULONG StreamCount;      // open pin instances
    ULONG CaptureRunning;   // capture stream in KSSTATE_RUN
    ULONG ToAppBytes;       // buffered toward app capture pin
    ULONG FromAppBytes;     // buffered from app render pin
    ULONG ToAppDropped;
    ULONG FromAppDropped;
    ULONG NewStreamCalls;
    ULONG LastSetState;     // last KSSTATE seen (0xFFFFFFFF = never)
    ULONG ReadCalls;        // miniport stream Read invocations
    ULONG ReadZeroCalls;    // Reads answered with 0 bytes
    ULONG LastReadBufLen;   // BufferLength portcls passed to the last Read
    ULONG NotifyCalls;      // IPortMidi->Notify kicks
    ULONG WriteIoctls;      // IOCTL_RAVEMIDI_WRITE count
    ULONG StreamWriteCalls; // app render-pin Write invocations
    ULONGLONG ReadBytesTotal;
} RAVEMIDI_PORT_STATS;

// ── Managed inputs (persistent, driver-owned) ─────────────────────────────────
// A managed input = one hardware controller the DRIVER binds autonomously: it
// creates the ports, taps the device, and keeps forwarding even when rave-mate is
// closed. rave-mate only edits this config (SET_CONFIG persists it kernel-side to
// the service Parameters key - no admin rights needed in userland - and applies it
// live). The driver re-applies it at StartDevice, re-binds on PnP arrival, and
// retries with backoff while the device is absent or busy.
//
// Per input the driver creates:
//   - ONE reserved BIDI port "<Name> (rave-mate)" - rave-mate reconnects here
//     seamlessly after a relaunch; with Feedback=1 its app-bound writes are also
//     forwarded to the device's render pin (LED feedback).
//   - OutCount extra OUT_ONLY ports (the DJ-software-facing one-way ports).
// Managed ports/taps have NO owner file object: handle close never tears them down.

#define RAVEMIDI_MAX_INPUTS 8

// Filter bits: message classes dropped on the tap -> FAN-OUT path (the reserved
// rave-mate port always receives everything, so learn/monitor stay complete).
// Kills the "MIDI-learn caught aftertouch, now every key fires the binding" class
// of mapping bugs without touching the controller.
#define RAVEMIDI_FILTER_CHANPRESSURE 0x01  // Dn channel pressure (keybed aftertouch)
#define RAVEMIDI_FILTER_POLYPRESSURE 0x02  // An polyphonic aftertouch
#define RAVEMIDI_FILTER_PITCHBEND    0x04  // En pitch bend
#define RAVEMIDI_FILTER_ACTIVESENSE  0x08  // FE active sensing
#define RAVEMIDI_FILTER_CLOCK        0x10  // F8/F9 timing tick
#define RAVEMIDI_FILTER_VALID        0x1F

typedef struct _RAVEMIDI_INPUT_CFG {
    WCHAR Id[RAVEMIDI_MAX_NAME];           // stable id assigned by rave-mate
    WCHAR Name[RAVEMIDI_MAX_NAME];         // friendly base name (port naming)
    WCHAR SourceMatch[RAVEMIDI_MAX_NAME];  // case-insensitive substring vs device FriendlyName
    WCHAR SourceIface[RAVEMIDI_MAX_IFACE]; // optional exact KS symlink ("" = use SourceMatch)
    ULONG Thru;                            // 1 = device capture -> all out ports
    ULONG Feedback;                        // 1 = app render on reserved/fan-out ports -> device render pin
    ULONG Filter;                          // RAVEMIDI_FILTER_* mask (fan-outs only)
    ULONG OutCount;                        // extra BIDI fan-out ports (0..RAVEMIDI_MAX_MIRROR_OUT)
    WCHAR OutNames[RAVEMIDI_MAX_MIRROR_OUT][RAVEMIDI_MAX_NAME];
} RAVEMIDI_INPUT_CFG;

typedef struct _RAVEMIDI_CONFIG {
    ULONG Version;                         // RAVEMIDI_PROTOCOL_VERSION
    ULONG InputCount;                      // 0..RAVEMIDI_MAX_INPUTS
    RAVEMIDI_INPUT_CFG Inputs[RAVEMIDI_MAX_INPUTS];
} RAVEMIDI_CONFIG;

// QUERY_INPUT: in ULONG index (0..InputCount-1); STATUS_NO_MORE_ENTRIES past end.
typedef struct _RAVEMIDI_INPUT_STATUS {
    WCHAR Id[RAVEMIDI_MAX_NAME];
    WCHAR Name[RAVEMIDI_MAX_NAME];
    ULONG Bound;                           // capture tap open + running
    ULONG FeedbackBound;                   // render pin open (Feedback=1 inputs)
    ULONG RetryCount;                      // bind attempts since last success
    WCHAR BoundIface[RAVEMIDI_MAX_IFACE];  // "" while unbound
    ULONG ReservedPortId;
    ULONG OutCount;
    ULONG OutPortIds[RAVEMIDI_MAX_MIRROR_OUT];
} RAVEMIDI_INPUT_STATUS;

// ── Trace (bring-up + live diagnosis) ─────────────────────────────────────────
// Per-port ring of the last RAVEMIDI_TRACE_ENTRIES data events. Snapshot via
// QUERY_TRACE (in: RAVEMIDI_PORT_REF); Seq is monotonic per port so pollers dedupe.
// TAP_RAW entries (raw KS reads off the tapped hardware pin, pre-parse) land in the
// tap's FIRST fan-in port ring (managed: the reserved port).

#define RAVEMIDI_TRACE_ENTRIES 128
#define RAVEMIDI_TRACE_BYTES 12

typedef enum _RAVEMIDI_TRACE_DIR {
    RaveTraceTapRaw      = 0,  // raw KS read completion from the tapped device
    RaveTraceToApp       = 1,  // bytes pushed toward the app capture pin
    RaveTraceReadPop     = 2,  // bytes handed to portcls via miniport Read
    RaveTraceFromApp     = 3,  // bytes the app wrote on the render pin
    RaveTraceFeedbackOut = 4,  // framed message written to the device render pin
    RaveTraceLoopDrop    = 5,  // loopback write suppressed (self-echo)
} RAVEMIDI_TRACE_DIR;

typedef struct _RAVEMIDI_TRACE_ENTRY {
    ULONGLONG Seq;
    ULONGLONG Time100ns;               // KeQueryInterruptTime at capture
    ULONG Dir;                         // RAVEMIDI_TRACE_DIR
    ULONG Len;                         // full event length (may exceed TRACE_BYTES)
    UCHAR Bytes[RAVEMIDI_TRACE_BYTES]; // first bytes of the event
    UCHAR Pad[4];
} RAVEMIDI_TRACE_ENTRY;                // 40 bytes

typedef struct _RAVEMIDI_TRACE_OUT {
    ULONG PortId;
    ULONG Count;                       // valid entries in E (oldest-first)
    ULONGLONG NextSeq;                 // next Seq the port will assign
    RAVEMIDI_TRACE_ENTRY E[RAVEMIDI_TRACE_ENTRIES];
} RAVEMIDI_TRACE_OUT;

#pragma pack(pop)

#define RAVEMIDI_DEVICE_TYPE 0x8F63u  // arbitrary, > 0x8000 per FILE_DEVICE_* rules

#define IOCTL_RAVEMIDI_CREATE_PORT \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x800, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_DESTROY_PORT \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x801, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_WRITE \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x802, METHOD_BUFFERED, FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_READ \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x803, METHOD_BUFFERED, FILE_READ_DATA)
#define IOCTL_RAVEMIDI_CREATE_MIRROR \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x804, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_DESTROY_MIRROR \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x805, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_QUERY_PORT \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x806, METHOD_BUFFERED, FILE_READ_DATA)
// managed-input config: SET validates + persists (service Parameters key, written
// kernel-side) + applies live; GET returns the persisted blob; QUERY_INPUT = live
// bind status; RELOAD re-reads the persisted blob (manual "reload driver config").
#define IOCTL_RAVEMIDI_SET_CONFIG \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x807, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_GET_CONFIG \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x808, METHOD_BUFFERED, FILE_READ_DATA)
#define IOCTL_RAVEMIDI_QUERY_INPUT \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x809, METHOD_BUFFERED, FILE_READ_DATA)
#define IOCTL_RAVEMIDI_RELOAD_CONFIG \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x80A, METHOD_BUFFERED, FILE_READ_DATA | FILE_WRITE_DATA)
#define IOCTL_RAVEMIDI_QUERY_TRACE \
    CTL_CODE(RAVEMIDI_DEVICE_TYPE, 0x80B, METHOD_BUFFERED, FILE_READ_DATA)
