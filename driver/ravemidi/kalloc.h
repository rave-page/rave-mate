// Non-deprecated kernel operator new/delete. stdunk.h's built-ins call the
// deprecated ExAllocatePoolWithTag (C4996 under /WX); defining _NEW_DELETE_OPERATORS_
// (a compiler /D in the vcxproj) suppresses those, and we supply ExAllocatePool2
// (HVCI-clean, zeroing) versions here. See stdunk.h:181.
// SPDX-License-Identifier: AGPL-3.0-or-later
#pragma once

// Placement new used by CUnknown-derived types: `new (PoolType, 'tag') T(...)`.
// stdunk.lib owns the plain operator delete(void*) — don't redefine it here (LNK2005);
// ExFreePool frees ExAllocatePool2 memory fine. We provide the sized + placement
// deletes stdunk.lib lacks.
_IRQL_requires_max_(DISPATCH_LEVEL) void* __cdecl operator new(size_t sz, POOL_TYPE pool, ULONG tag);
void __cdecl operator delete(void* p, size_t sz);
void __cdecl operator delete(void* p, POOL_TYPE pool, ULONG tag);  // matched placement-delete
