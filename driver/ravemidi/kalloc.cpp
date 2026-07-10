// SPDX-License-Identifier: AGPL-3.0-or-later
#include <ntddk.h>
#include "kalloc.h"
#include "fifo.h"  // RAVEMIDI_POOL_TAG (fallback tag)

#pragma warning(disable:4595)  // non-member operator new/delete may not be inline (kernel)

void* __cdecl operator new(size_t sz, POOL_TYPE pool, ULONG tag)
{
    POOL_FLAGS flags = (pool == PagedPool || pool == PagedPoolCacheAligned)
                           ? POOL_FLAG_PAGED
                           : POOL_FLAG_NON_PAGED;  // NonPagedPoolNx maps here (NX default)
    return ExAllocatePool2(flags, sz ? sz : 1, tag);
}

void __cdecl operator delete(void* p)
{
    if (p) {
        ExFreePool(p);
    }
}

void __cdecl operator delete(void* p, size_t sz)
{
    UNREFERENCED_PARAMETER(sz);
    if (p) {
        ExFreePool(p);
    }
}

void __cdecl operator delete(void* p, POOL_TYPE pool, ULONG tag)
{
    UNREFERENCED_PARAMETER(pool);
    UNREFERENCED_PARAMETER(tag);
    if (p) {
        ExFreePool(p);
    }
}
