# attest-submit.ps1 - SKELETON: submit an EV-signed cab to Microsoft Hardware
# Dashboard (attestation signing) via the Hardware API. PowerShell 5.1+.
#
# Docs: learn.microsoft.com/windows-hardware/drivers/dashboard/dashboard-api
#       learn.microsoft.com/windows-hardware/drivers/dashboard/manage-product-submissions
# Reference client: github.com/Microsoft/SDCM
#
# Prereqs (one-time, see CHECKLIST.md):
#   Partner Center Hardware account + Entra app associated with it, role
#   "Hardware / Driver Submitter". NO secrets in this file or the repo -
#   config via environment:
#     RVM_TENANT_ID     Entra tenant id
#     RVM_CLIENT_ID     Entra app (client) id
#     RVM_CLIENT_SECRET Entra app client secret
#
# Usage:
#   .\attest-submit.ps1 -CabPath .\disk1\ravemidi.cab -ProductName "ravemidi 1.2.3"
#
# Marked SKELETON: requestedSignatures ids below cover Win10/11 x64 client and
# MUST be confirmed against the current id list before first real use
# (learn.microsoft.com/windows-hardware/drivers/dashboard/get-product-data).

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string]$CabPath,
    [Parameter(Mandatory = $true)] [string]$ProductName,
    # Confirm current values: dashboard "requestedSignatures" ids change per OS release.
    [string[]]$RequestedSignatures = @(
        "WINDOWS_v100_X64_RS3_FULL",     # Win10 1709+ x64 (needs-confirmation)
        "WINDOWS_v100_X64_CO_FULL"       # Win11 x64 (needs-confirmation)
    ),
    [int]$PollSeconds = 30,
    [int]$TimeoutMinutes = 90
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

foreach ($v in "RVM_TENANT_ID", "RVM_CLIENT_ID", "RVM_CLIENT_SECRET") {
    if (-not (Get-Item "env:$v" -ErrorAction SilentlyContinue)) { throw "env $v not set" }
}
if (-not (Test-Path $CabPath)) { throw "cab not found: $CabPath" }
$CabPath = (Resolve-Path $CabPath).Path

# --- 1. Entra client-credentials token (resource = manage.devcenter.microsoft.com)
$tokenResp = Invoke-RestMethod -Method Post `
    -Uri "https://login.microsoftonline.com/$env:RVM_TENANT_ID/oauth2/token" `
    -ContentType "application/x-www-form-urlencoded" `
    -Body @{
        grant_type    = "client_credentials"
        client_id     = $env:RVM_CLIENT_ID
        client_secret = $env:RVM_CLIENT_SECRET
        resource      = "https://manage.devcenter.microsoft.com"
    }
$headers = @{ Authorization = "Bearer $($tokenResp.access_token)" }
$api = "https://manage.devcenter.microsoft.com/v2.0/my/hardware"

# --- 2. Create product
$productBody = @{
    productName        = $ProductName
    testHarness        = "attestation"
    deviceType         = "internalExternal"
    requestedSignatures = $RequestedSignatures
    deviceMetadataIds  = @()
    firmwareVersion    = "0"
    isTestSign         = $false
    isFlightSign       = $false
    markettingNames    = @()
    selectedProductTypes = @{}   # SKELETON: fill per current API schema if required
    additionalAttributes = @{}
} | ConvertTo-Json -Depth 8
$product = Invoke-RestMethod -Method Post -Uri "$api/products" -Headers $headers `
    -ContentType "application/json" -Body $productBody
Write-Host "product id: $($product.id)"

# --- 3. Create submission (returns SAS upload URL)
$submissionBody = @{
    name = "$ProductName submission"
    type = "initial"
} | ConvertTo-Json
$submission = Invoke-RestMethod -Method Post -Uri "$api/products/$($product.id)/submissions" `
    -Headers $headers -ContentType "application/json" -Body $submissionBody
Write-Host "submission id: $($submission.id)"

$sasUrl = ($submission.downloads.items | Where-Object { $_.type -eq "initialPackage" }).url
if (-not $sasUrl) { throw "no initialPackage SAS url in submission response" }

# --- 4. Upload cab to Azure Blob via SAS (block blob PUT; fine for cab-sized files)
Invoke-RestMethod -Method Put -Uri $sasUrl `
    -Headers @{ "x-ms-blob-type" = "BlockBlob" } `
    -InFile $CabPath -ContentType "application/octet-stream" | Out-Null
Write-Host "cab uploaded"

# --- 5. Commit
Invoke-RestMethod -Method Post -Uri "$api/products/$($product.id)/submissions/$($submission.id)/commit" `
    -Headers $headers -ContentType "application/json" -Body "{}" | Out-Null
Write-Host "committed; polling..."

# --- 6. Poll commitStatus until CommitComplete / CommitFailed
$deadline = (Get-Date).AddMinutes($TimeoutMinutes)
while ($true) {
    Start-Sleep -Seconds $PollSeconds
    $state = Invoke-RestMethod -Method Get `
        -Uri "$api/products/$($product.id)/submissions/$($submission.id)" -Headers $headers
    Write-Host ("{0}  commitStatus={1}  workflow={2}/{3}" -f (Get-Date -Format s), `
        $state.commitStatus, $state.workflowStatus.currentStep, $state.workflowStatus.state)
    if ($state.commitStatus -eq "CommitFailed") { throw "submission failed: $($state | ConvertTo-Json -Depth 6)" }
    if ($state.workflowStatus.state -eq "failed") { throw "workflow failed: $($state | ConvertTo-Json -Depth 6)" }
    $signed = ($state.downloads.items | Where-Object { $_.type -eq "signedPackage" }).url
    if ($signed) {
        $out = Join-Path (Split-Path $CabPath) "ravemidi-signed.zip"
        Invoke-WebRequest -Uri $signed -OutFile $out
        Write-Host "signed package -> $out"
        break
    }
    if ((Get-Date) -gt $deadline) { throw "timeout after $TimeoutMinutes min" }
}
