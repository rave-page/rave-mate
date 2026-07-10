// GUID instantiation TU. Defining INITGUID before <portcls.h> emits storage for
// the PortCls/COM class + interface GUIDs (CLSID_PortMidi, IID_IPortMidi,
// IID_IMiniport(Midi)(Stream), IID_IUnknown) in exactly this one object file.
// Every other TU sees them as extern declarations. Do NOT define INITGUID anywhere
// else or link ksguid.lib (would double-define). KS media format GUIDs used via
// STATICGUIDOF are selectany consts and need no INITGUID.
// SPDX-License-Identifier: AGPL-3.0-or-later
#include <initguid.h>
#include <portcls.h>
#include <devpkey.h>  // INITGUID also emits DEVPKEY_* storage (DEFINE_DEVPROPKEY)
