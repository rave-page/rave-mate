// SPDX-License-Identifier: AGPL-3.0-or-later
#include <ntddk.h>
#include "framer.h"

VOID RaveFramerInit(_Out_ RAVE_FRAMER* f)
{
    RtlZeroMemory(f, sizeof(*f));
}

// Expected total length of a message with this status byte (0 = variable/sysex).
static ULONG MsgLen(UCHAR status)
{
    if (status < 0xF0) {
        UCHAR hi = status & 0xF0;
        return (hi == 0xC0 || hi == 0xD0) ? 2u : 3u;  // program/chan-pressure = 2, rest 3
    }
    switch (status) {
    case 0xF1: case 0xF3: return 2;   // MTC quarter-frame, song select
    case 0xF2:            return 3;   // song position
    default:              return 1;   // F4/F5 undefined, F6 tune request
    }
}

static VOID SysFlush(RAVE_FRAMER* f, RAVE_FRAMER_EMIT emit, PVOID ctx)
{
    if (f->SysHave) {
        emit(ctx, f->Sys, f->SysHave);
        f->SysHave = 0;
    }
}

VOID RaveFramerFeed(_Inout_ RAVE_FRAMER* f, _In_reads_bytes_(len) const UCHAR* b, _In_ ULONG len,
                    _In_ RAVE_FRAMER_EMIT emit, _In_ PVOID ctx)
{
    for (ULONG i = 0; i < len; i++) {
        UCHAR c = b[i];
        if (c >= 0xF8) {                       // realtime: passthrough, state untouched
            emit(ctx, &c, 1);
            continue;
        }
        if (c == 0xF0) {                       // sysex start (abandons any partial short msg)
            f->Have = 0;
            f->Status = 0;
            f->InSysEx = TRUE;
            f->SysHave = 0;
            f->Sys[f->SysHave++] = c;
            continue;
        }
        if (f->InSysEx) {
            if (c == 0xF7) {                   // sysex end
                f->Sys[f->SysHave++] = c;      // SysHave < CHUNK guaranteed (flushed below)
                SysFlush(f, emit, ctx);
                f->InSysEx = FALSE;
                continue;
            }
            if (c >= 0x80) {                   // interrupting status aborts the sysex
                SysFlush(f, emit, ctx);
                f->InSysEx = FALSE;            // fall through to status handling
            } else {
                // bound-guarded append (C6386): the invariant (every append path
                // flushes at the cap) keeps SysHave < CHUNK, but make it provable
                if (f->SysHave < RAVE_FRAMER_SYSEX_CHUNK) {
                    f->Sys[f->SysHave++] = c;
                }
                if (f->SysHave >= RAVE_FRAMER_SYSEX_CHUNK) {
                    SysFlush(f, emit, ctx);    // mid-sysex chunk (multi-record legal)
                }
                continue;
            }
        }
        if (c >= 0x80) {                       // new status
            f->Buf[0] = c;
            f->Have = 1;
            f->Status = (c < 0xF0) ? c : 0;    // system common clears running status
            if (MsgLen(c) == 1) {
                emit(ctx, f->Buf, 1);
                f->Have = 0;
            }
            continue;
        }
        // data byte
        if (f->Have == 0) {
            if (!f->Status) {
                continue;                      // stray data, no running status: drop
            }
            f->Buf[0] = f->Status;             // running status resumes
            f->Have = 1;
        }
        f->Buf[f->Have++] = c;
        ULONG want = MsgLen(f->Buf[0]);
        if (f->Have >= want) {
            emit(ctx, f->Buf, want);
            f->Have = 0;                       // running status persists via f->Status
        }
    }
}
