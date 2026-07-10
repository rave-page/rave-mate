// Managed-input config persistence (service Parameters key, REG_BINARY "Config").
// SPDX-License-Identifier: AGPL-3.0-or-later
#include <ntddk.h>
#include "fifo.h"    // RAVEMIDI_POOL_TAG
#include "config.h"

#define RAVE_TAG RAVEMIDI_POOL_TAG

static UNICODE_STRING g_ParamsPath;   // "<RegistryPath>\Parameters", pool-backed

#pragma code_seg("INIT")
NTSTATUS RaveConfigInit(PCUNICODE_STRING RegistryPath)
{
    DECLARE_CONST_UNICODE_STRING(suffix, L"\\Parameters");
    USHORT len = (USHORT)(RegistryPath->Length + suffix.Length);
    PWCH buf = (PWCH)ExAllocatePool2(POOL_FLAG_PAGED, (SIZE_T)len + sizeof(WCHAR), RAVE_TAG);
    if (!buf) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    RtlCopyMemory(buf, RegistryPath->Buffer, RegistryPath->Length);
    RtlCopyMemory((PUCHAR)buf + RegistryPath->Length, suffix.Buffer, suffix.Length);
    buf[len / sizeof(WCHAR)] = 0;
    g_ParamsPath.Buffer = buf;
    g_ParamsPath.Length = len;
    g_ParamsPath.MaximumLength = len + sizeof(WCHAR);
    return STATUS_SUCCESS;
}
#pragma code_seg()

#pragma code_seg("PAGE")
VOID RaveConfigRelease()
{
    PAGED_CODE();
    if (g_ParamsPath.Buffer) {
        ExFreePoolWithTag(g_ParamsPath.Buffer, RAVE_TAG);
        g_ParamsPath.Buffer = nullptr;
        g_ParamsPath.Length = g_ParamsPath.MaximumLength = 0;
    }
}
#pragma code_seg()

// Terminate at cch-1 worst case, then zero everything past the first NUL so
// sanitized blobs compare bytewise (config diff) and never leak stack garbage.
static VOID NulPad(WCHAR* s, ULONG cch)
{
    s[cch - 1] = 0;
    BOOLEAN z = FALSE;
    for (ULONG i = 0; i < cch; i++) {
        if (z) {
            s[i] = 0;
        } else if (s[i] == 0) {
            z = TRUE;
        }
    }
}

static BOOLEAN NoCtlChars(const WCHAR* s)
{
    for (; *s; s++) {
        if (*s < 0x20) {
            return FALSE;
        }
    }
    return TRUE;
}

#pragma code_seg("PAGE")
BOOLEAN RaveConfigSanitize(RAVEMIDI_CONFIG* cfg)
{
    PAGED_CODE();
    if (cfg->Version != RAVEMIDI_PROTOCOL_VERSION || cfg->InputCount > RAVEMIDI_MAX_INPUTS) {
        return FALSE;
    }
    if (cfg->InputCount < RAVEMIDI_MAX_INPUTS) {
        RtlZeroMemory(&cfg->Inputs[cfg->InputCount],
                      (RAVEMIDI_MAX_INPUTS - cfg->InputCount) * sizeof(RAVEMIDI_INPUT_CFG));
    }
    for (ULONG i = 0; i < cfg->InputCount; i++) {
        RAVEMIDI_INPUT_CFG* in = &cfg->Inputs[i];
        NulPad(in->Id, RAVEMIDI_MAX_NAME);
        NulPad(in->Name, RAVEMIDI_MAX_NAME);
        NulPad(in->SourceMatch, RAVEMIDI_MAX_NAME);
        NulPad(in->SourceIface, RAVEMIDI_MAX_IFACE);
        if (in->OutCount > RAVEMIDI_MAX_MIRROR_OUT) {
            return FALSE;
        }
        for (ULONG j = 0; j < RAVEMIDI_MAX_MIRROR_OUT; j++) {
            if (j < in->OutCount) {
                NulPad(in->OutNames[j], RAVEMIDI_MAX_NAME);
                if (!NoCtlChars(in->OutNames[j])) {
                    return FALSE;
                }
            } else {
                RtlZeroMemory(in->OutNames[j], sizeof(in->OutNames[j]));
            }
        }
        if (!in->Id[0] || !in->Name[0] ||
            !NoCtlChars(in->Id) || !NoCtlChars(in->Name) || !NoCtlChars(in->SourceMatch)) {
            return FALSE;
        }
        if (!in->SourceMatch[0] && !in->SourceIface[0]) {
            return FALSE;  // unbindable input
        }
        in->Thru = in->Thru ? 1 : 0;
        in->Feedback = in->Feedback ? 1 : 0;
        for (ULONG k = 0; k < i; k++) {  // Ids drive the live diff — must be unique
            if (RtlCompareMemory(cfg->Inputs[k].Id, in->Id, sizeof(in->Id)) == sizeof(in->Id)) {
                return FALSE;
            }
        }
    }
    return TRUE;
}
#pragma code_seg()

#pragma code_seg("PAGE")
static NTSTATUS OpenParamsKey(BOOLEAN create, PHANDLE key)
{
    PAGED_CODE();
    if (!g_ParamsPath.Buffer) {
        return STATUS_DEVICE_NOT_READY;
    }
    OBJECT_ATTRIBUTES oa;
    InitializeObjectAttributes(&oa, &g_ParamsPath, OBJ_KERNEL_HANDLE | OBJ_CASE_INSENSITIVE,
                               nullptr, nullptr);
    if (create) {
        return ZwCreateKey(key, KEY_READ | KEY_WRITE, &oa, 0, nullptr, REG_OPTION_NON_VOLATILE, nullptr);
    }
    return ZwOpenKey(key, KEY_READ, &oa);
}
#pragma code_seg()

#pragma code_seg("PAGE")
NTSTATUS RaveConfigSave(const RAVEMIDI_CONFIG* cfg)
{
    PAGED_CODE();
    HANDLE key = nullptr;
    NTSTATUS st = OpenParamsKey(TRUE, &key);
    if (!NT_SUCCESS(st)) {
        return st;
    }
    UNICODE_STRING vn;
    RtlInitUnicodeString(&vn, L"Config");
    st = ZwSetValueKey(key, &vn, 0, REG_BINARY, (PVOID)cfg, sizeof(*cfg));
    ZwClose(key);
    return st;
}
#pragma code_seg()

#pragma code_seg("PAGE")
NTSTATUS RaveConfigLoad(RAVEMIDI_CONFIG* out)
{
    PAGED_CODE();
    HANDLE key = nullptr;
    NTSTATUS st = OpenParamsKey(FALSE, &key);
    if (!NT_SUCCESS(st)) {
        return STATUS_NOT_FOUND;
    }
    ULONG sz = sizeof(KEY_VALUE_PARTIAL_INFORMATION) + sizeof(RAVEMIDI_CONFIG);
    KEY_VALUE_PARTIAL_INFORMATION* info =
        (KEY_VALUE_PARTIAL_INFORMATION*)ExAllocatePool2(POOL_FLAG_PAGED, sz, RAVE_TAG);
    if (!info) {
        ZwClose(key);
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    UNICODE_STRING vn;
    RtlInitUnicodeString(&vn, L"Config");
    ULONG rl = 0;
    st = ZwQueryValueKey(key, &vn, KeyValuePartialInformation, info, sz, &rl);
    ZwClose(key);
    // exact-size REG_BINARY or reject — versioned struct, no partial decode
    if (NT_SUCCESS(st) && info->Type == REG_BINARY && info->DataLength == sizeof(RAVEMIDI_CONFIG)) {
        RtlCopyMemory(out, info->Data, sizeof(RAVEMIDI_CONFIG));
        st = RaveConfigSanitize(out) ? STATUS_SUCCESS : STATUS_NOT_FOUND;
    } else {
        st = STATUS_NOT_FOUND;
    }
    ExFreePoolWithTag(info, RAVE_TAG);
    return st;
}
#pragma code_seg()
