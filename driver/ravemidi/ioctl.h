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

#define RAVEMIDI_PROTOCOL_VERSION 1
#define RAVEMIDI_MAX_NAME 32   // WCHARs incl NUL; winmm szPname caps at 31+NUL
#define RAVEMIDI_MAX_PORTS 16
#define RAVEMIDI_MAX_MIRROR 8            // concurrent mirror groups
#define RAVEMIDI_MAX_MIRROR_OUT 4        // fan-out ports per mirror
#define RAVEMIDI_MAX_IFACE 256           // WCHARs: source KS interface symlink

// Port kind = which winmm endpoints the port exposes.
// OUT_ONLY: apps see an INPUT-only port; rave-mate writes into it (the LED-echo killer).
// IN_ONLY:  apps see an OUTPUT-only port; rave-mate reads what apps send.
// BIDI:     both endpoints, app<->rave-mate.
// LOOPBACK: classic cable — app render echoed to app capture (no rave-mate in path).
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
