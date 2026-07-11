# sign-cab.ps1 - EV-sign the ravemidi attestation cab before Partner Center upload.
# Run on the machine holding the EV token (CI never signs). PowerShell 5.1+.
#
# Usage:
#   .\sign-cab.ps1 -CabPath .\disk1\ravemidi.cab
#   .\sign-cab.ps1 -CabPath .\disk1\ravemidi.cab -SubjectName "Your Company GmbH" -TimestampUrl http://timestamp.digicert.com
#
# Notes:
# - Signs the CAB only. Kernel load trust comes from Microsoft's attestation
#   signature on the .sys, not from this cert.
# - Cert must be registered to the Partner Center account (EV recommended).
# - /fd sha256 + RFC3161 /tr timestamp are mandatory (SHA-2 policy).

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$CabPath,

    # Substring of the signing cert subject; default picks the only code-signing
    # cert on the token when omitted.
    [string]$SubjectName = "",

    # RFC3161 timestamp server of YOUR CA (DigiCert/Sectigo/Certum/SSL.com all publish one).
    [string]$TimestampUrl = "http://timestamp.digicert.com",

    # Optional explicit signtool.exe path; otherwise resolved from PATH / Windows Kits.
    [string]$SignTool = ""
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path $CabPath)) { throw "cab not found: $CabPath" }
$CabPath = (Resolve-Path $CabPath).Path

if ($SignTool -eq "") {
    $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($cmd) { $SignTool = $cmd.Source }
}
if ($SignTool -eq "") {
    $kitRoots = @("${env:ProgramFiles(x86)}\Windows Kits\10\bin", "$env:ProgramFiles\Windows Kits\10\bin") |
        Where-Object { $_ -and (Test-Path $_) }
    $found = Get-ChildItem $kitRoots -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match '\\x64\\' } | Select-Object -First 1
    if ($found) { $SignTool = $found.FullName }
}
if ($SignTool -eq "") { throw "signtool.exe not found; install Windows SDK or pass -SignTool" }

Write-Host "signtool: $SignTool"
Write-Host "cab     : $CabPath"

$args = @("sign", "/fd", "sha256", "/td", "sha256", "/tr", $TimestampUrl, "/v")
if ($SubjectName -ne "") { $args += @("/n", $SubjectName) }
# /a = pick best cert automatically when no subject given (token cert via CSP/KSP)
else { $args += "/a" }
$args += $CabPath

& $SignTool @args
if ($LASTEXITCODE -ne 0) { throw "signtool sign failed ($LASTEXITCODE)" }

& $SignTool verify /pa /v $CabPath
if ($LASTEXITCODE -ne 0) { throw "signtool verify failed ($LASTEXITCODE)" }

Write-Host ""
Write-Host "OK - upload the signed cab at https://partner.microsoft.com/dashboard/hardware"
Write-Host "or run build\attest\attest-submit.ps1 for the API flow."
