// Non-deprecated kernel operator new/delete. stdunk.h's built-ins call the
// deprecated ExAllocatePoolWithTag (C4996 under /WX); defining _NEW_DELETE_OPERATORS_
// (a compiler /D in the vcxproj) suppresses those, and we supply ExAllocatePool2
// (HVCI-clean, zeroing) versions here. See stdunk.h:181.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once

// Placement forms used by CUnknown-derived types: `new (PoolType, 'tag') T(...)`.
_IRQL_requires_max_(DISPATCH_LEVEL) void* __cdecl operator new(size_t sz, POOL_TYPE pool, ULONG tag);
void __cdecl operator delete(void* p);
void __cdecl operator delete(void* p, size_t sz);
void __cdecl operator delete(void* p, POOL_TYPE pool, ULONG tag);  // matched placement-delete
