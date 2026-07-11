// MIDI byte-stream framer: re-aligns arbitrary byte chunks to complete MIDI
// messages (short messages, running status, sysex chunks, realtime passthrough)
// so device-bound writes are never split mid-message.
// SPDX-License-Identifier: AGPL-3.0-or-later
// No kernel-header include here: TUs include <ntddk.h> or <portcls.h> first
// (they are alternative top-level headers and must not both be pulled in).
#pragma once

#define RAVE_FRAMER_SYSEX_CHUNK 48  // sysex flush granularity (multi-record sysex is legal)

typedef VOID (*RAVE_FRAMER_EMIT)(_In_ PVOID ctx, _In_reads_bytes_(len) const UCHAR* msg, _In_ ULONG len);

typedef struct _RAVE_FRAMER {
    UCHAR Buf[4];                        // pending short-message bytes (incl status)
    ULONG Have;
    UCHAR Status;                        // running status (0 = none)
    BOOLEAN InSysEx;
    UCHAR Sys[RAVE_FRAMER_SYSEX_CHUNK];  // pending sysex chunk
    ULONG SysHave;
} RAVE_FRAMER;

VOID RaveFramerInit(_Out_ RAVE_FRAMER* f);
// Feed len bytes; emit() fires once per complete message / sysex chunk / realtime byte.
// DISPATCH-safe if emit is; caller serializes per-framer.
VOID RaveFramerFeed(_Inout_ RAVE_FRAMER* f, _In_reads_bytes_(len) const UCHAR* b, _In_ ULONG len,
                    _In_ RAVE_FRAMER_EMIT emit, _In_ PVOID ctx);
