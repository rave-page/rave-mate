// Managed-input engine: driver-owned ports + autonomous bind/retry to hardware
// controllers. Config persists in the registry (config.h) and is re-applied at
// StartDevice, so forwarding survives rave-mate exit/crash AND reboot; rave-mate
// is only a config manager that reconnects via the reserved "<Name> (rave-mate)"
// BIDI port.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once
#include "ioctl.h"

VOID RaveManagedBoot(PDEVICE_OBJECT Fdo);  // StartDevice: start engine + apply persisted config
VOID RaveManagedStop();                    // PnP STOP/REMOVE + unload fallback (idempotent)
NTSTATUS RaveManagedApply(const RAVEMIDI_CONFIG* cfg);   // cfg pre-sanitized (config.h)
NTSTATUS RaveManagedQuery(ULONG index, RAVEMIDI_INPUT_STATUS* out);
VOID RaveManagedKickFeedback();            // DISPATCH-safe: wake worker to drain feedback tees
