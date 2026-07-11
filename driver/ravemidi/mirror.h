// KS-client machinery: owner-less capture taps (hardware controller MIDI fanned
// into virtual ports), render-pin client (managed feedback), and the legacy
// IOCTL mirror groups built on top. Taps live in the driver, so DJ software
// keeps receiving controller MIDI even if rave-mate exits.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once
#include "ioctl.h"

typedef struct _RAVE_PORT RAVE_PORT;  // full definition in miniport.h

// -------- owner-less capture tap (shared by legacy mirrors + managed inputs) ---
typedef struct _RAVE_TAP RAVE_TAP;
// OnDead: the read loop hit a persistent failure (device pulled/reset). Called
// once from the tap thread which then exits. Must NOT tear the tap down itself
// (set a flag + wake a worker; RaveTapClose joins the thread). NULL = legacy
// behavior: retry reads forever.
typedef VOID (*RAVE_TAP_DEAD_CB)(PVOID Ctx);

// Iface must be a vetted/enumerated KS interface symlink (callers validate).
// Outs are borrowed — caller guarantees they outlive the tap (refs or ownership).
// FilterMask (RAVEMIDI_FILTER_*) drops matching messages for Outs[1..] only —
// Outs[0] (managed: the reserved rave-mate port) always gets the full stream.
NTSTATUS RaveTapOpen(PCWSTR Iface, RAVE_PORT* const* Outs, ULONG OutCount,
                     ULONG FilterMask, RAVE_TAP_DEAD_CB OnDead, PVOID DeadCtx, RAVE_TAP** OutTap);
VOID RaveTapClose(RAVE_TAP* Tap);  // stop + join thread + release KS handles (ports untouched)

// TRUE only if Name is a currently-enumerated KS (Render?RENDER:CAPTURE)/AUDIO
// interface — blocks opening arbitrary NT paths.
BOOLEAN RaveIsKnownIface(PCWSTR Name, BOOLEAN Render);

// -------- render-pin client (managed feedback: reserved-port writes -> device) -
NTSTATUS RaveKsOpenRenderPin(PCWSTR Iface, HANDLE* FilterH, PFILE_OBJECT* FilterFo,
                             HANDLE* PinH, PFILE_OBJECT* PinFo);
VOID RaveKsCloseRenderPin(HANDLE FilterH, PFILE_OBJECT FilterFo, HANDLE PinH, PFILE_OBJECT PinFo);
#define RAVEMIDI_FEEDBACK_CHUNK 512  // max bytes per render write (one KSMUSICFORMAT record)
NTSTATUS RaveKsWriteMidi(PFILE_OBJECT Pin, const UCHAR* Bytes, ULONG Len);  // PASSIVE only

// -------- legacy IOCTL mirror groups (owned by creator handle, close-cleaned) --
VOID RaveMirrorInit();                                    // once, from StartDevice
NTSTATUS RaveMirrorCreate(PFILE_OBJECT creator, const RAVEMIDI_CREATE_MIRROR_IN* in,
                          ULONG inLen, ULONG* outId);
NTSTATUS RaveMirrorDestroy(PFILE_OBJECT caller, ULONG id);
VOID RaveMirrorDestroyForFile(PFILE_OBJECT f);           // handle close: drop its taps
