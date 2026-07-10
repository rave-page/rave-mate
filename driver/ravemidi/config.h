// Managed-input config persistence: one REG_BINARY value "Config" under the
// driver service's Parameters key, written kernel-side (SET_CONFIG) so the
// unelevated rave-mate never needs registry rights. Re-read at StartDevice /
// RELOAD_CONFIG. All functions PASSIVE_LEVEL only.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once
#include "ioctl.h"

NTSTATUS RaveConfigInit(PCUNICODE_STRING RegistryPath);  // DriverEntry: capture "<path>\Parameters"
VOID RaveConfigRelease();
// Hard-validate + normalize in place: version/count caps, NUL-pad every WCHAR
// field, clamp flags to 0/1, zero trailing inputs, reject empty/dup ids and
// inputs with no source. FALSE = reject blob.
BOOLEAN RaveConfigSanitize(RAVEMIDI_CONFIG* cfg);
NTSTATUS RaveConfigSave(const RAVEMIDI_CONFIG* cfg);
NTSTATUS RaveConfigLoad(RAVEMIDI_CONFIG* out);  // sanitized; STATUS_NOT_FOUND if absent/corrupt
