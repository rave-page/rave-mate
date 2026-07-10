// SPDX-License-Identifier: AGPL-3.0-or-later
#include <ntddk.h>
#include "fifo.h"

VOID RaveFifoInit(_Out_ RAVEMIDI_FIFO* f)
{
    RtlZeroMemory(f, sizeof(*f));
    KeInitializeSpinLock(&f->Lock);
}

static ULONG fifoFree(const RAVEMIDI_FIFO* f)
{
    return RAVEMIDI_FIFO_BYTES - 1 - ((f->Head - f->Tail) & (RAVEMIDI_FIFO_BYTES - 1));
}

BOOLEAN RaveFifoPush(_Inout_ RAVEMIDI_FIFO* f, _In_reads_bytes_(len) const UCHAR* msg, _In_ ULONG len)
{
    if (len == 0 || len >= RAVEMIDI_FIFO_BYTES) {
        return FALSE;
    }
    KIRQL irql;
    KeAcquireSpinLock(&f->Lock, &irql);
    BOOLEAN ok = (fifoFree(f) >= len) ? TRUE : FALSE;
    if (ok) {
        for (ULONG i = 0; i < len; i++) {
            f->Buf[(f->Head + i) & (RAVEMIDI_FIFO_BYTES - 1)] = msg[i];
        }
        f->Head = (f->Head + len) & (RAVEMIDI_FIFO_BYTES - 1);
    } else {
        f->Dropped++;
    }
    KeReleaseSpinLock(&f->Lock, irql);
    return ok;
}

ULONG RaveFifoPop(_Inout_ RAVEMIDI_FIFO* f, _Out_writes_bytes_(cap) UCHAR* dst, _In_ ULONG cap)
{
    KIRQL irql;
    KeAcquireSpinLock(&f->Lock, &irql);
    ULONG avail = (f->Head - f->Tail) & (RAVEMIDI_FIFO_BYTES - 1);
    ULONG n = avail < cap ? avail : cap;
    for (ULONG i = 0; i < n; i++) {
        dst[i] = f->Buf[(f->Tail + i) & (RAVEMIDI_FIFO_BYTES - 1)];
    }
    f->Tail = (f->Tail + n) & (RAVEMIDI_FIFO_BYTES - 1);
    KeReleaseSpinLock(&f->Lock, irql);
    return n;
}

ULONG RaveFifoCount(_In_ RAVEMIDI_FIFO* f)
{
    KIRQL irql;
    KeAcquireSpinLock(&f->Lock, &irql);
    ULONG avail = (f->Head - f->Tail) & (RAVEMIDI_FIFO_BYTES - 1);
    KeReleaseSpinLock(&f->Lock, irql);
    return avail;
}
