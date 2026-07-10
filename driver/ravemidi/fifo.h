// Message-granular MIDI ring FIFO. NonPagedPoolNx only (HVCI). Spinlock-guarded:
// producers/consumers run up to DISPATCH_LEVEL (miniport stream + IOCTL paths).
// SPDX-License-Identifier: AGPL-3.0-or-later
// No kernel-header include here: TUs include <ntddk.h> or <portcls.h> first
// (they are alternative top-level headers and must not both be pulled in).
#pragma once

#define RAVEMIDI_FIFO_BYTES 8192  // power of two (ring index masks)
#define RAVEMIDI_POOL_TAG 'dmvR'  // "Rvmd"

// Byte ring. Writers push complete MIDI messages (short msg or sysex chunk);
// reads drain whatever is available. Overflow drops the incoming message whole
// (never splits) and counts it — a stalled reader must not corrupt framing.
typedef struct _RAVEMIDI_FIFO {
    KSPIN_LOCK Lock;
    ULONG Head;         // next write
    ULONG Tail;         // next read
    ULONG Dropped;      // messages dropped on overflow
    UCHAR Buf[RAVEMIDI_FIFO_BYTES];
} RAVEMIDI_FIFO;

VOID RaveFifoInit(_Out_ RAVEMIDI_FIFO* f);
// Push one complete message. Returns FALSE (and counts) if it doesn't fit.
BOOLEAN RaveFifoPush(_Inout_ RAVEMIDI_FIFO* f, _In_reads_bytes_(len) const UCHAR* msg, _In_ ULONG len);
// Pop up to cap bytes; returns bytes copied (0 = empty).
ULONG RaveFifoPop(_Inout_ RAVEMIDI_FIFO* f, _Out_writes_bytes_(cap) UCHAR* dst, _In_ ULONG cap);
ULONG RaveFifoCount(_In_ RAVEMIDI_FIFO* f);
