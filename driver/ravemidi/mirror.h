// Mirror-tap: kernel KS client that reads a hardware controller's MIDI capture
// pin and fans it into OUT_ONLY/BIDI virtual ports. Because the tap lives in the
// driver, the DJ software keeps receiving controller MIDI even if rave-mate exits.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once
#include "ioctl.h"

VOID RaveMirrorInit();                                    // once, from StartDevice
NTSTATUS RaveMirrorCreate(PFILE_OBJECT creator, const RAVEMIDI_CREATE_MIRROR_IN* in,
                          ULONG inLen, ULONG* outId);
NTSTATUS RaveMirrorDestroy(PFILE_OBJECT caller, ULONG id);
VOID RaveMirrorDestroyForFile(PFILE_OBJECT f);           // handle close: drop its taps
