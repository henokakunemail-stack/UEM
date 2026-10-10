<#
.SYNOPSIS
    Installs Endpoint Management Agent as a Windows Service running as LocalSystem (Administrator).
.DESCRIPTION
    Automated script for headless, GPO, SCCM, Intune, or manual PowerShell deployment.
.PARAMETER ServerUrl
    The central management server URL (e.g. http://localhost:8443 or https://mgmt.example.com).
.PARAMETER EnrollToken
    Optional one-time enrollment token generated from the Web Console.
.PARAMETER BinaryPath
    Optional path to the agent binary. Defaults to search alongside script or repo root.
.PARAMETER InstallDir
    Target directory for agent binary. Default is C:\Program Files\EndpointAgent.
.PARAMETER DataDir
    Directory for credentials and logs. Default is C:\ProgramData\EndpointAgent.
#>
[CmdletBinding()]
param(
    [string]$ServerUrl = "http://localhost:8443",
    [string]$EnrollToken = "",
    [string]$BinaryPath = "",
    [string]$InstallDir = "$env:ProgramFiles\EndpointAgent",
    [string]$DataDir = "$env:ProgramData\EndpointAgent"
)

$ErrorActionPreference = "Stop"

# 1. Require Administrator elevation
$currentPrincipal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Warning "This script requires Administrator privileges. Attempting elevation..."
    $argList = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`""
    if ($PSBoundParameters.Count -gt 0) {
        foreach ($key in $PSBoundParameters.Keys) {
            $argList += " -$key `"$($PSBoundParameters[$key])`""
        }
    }
    Start-Process powershell.exe -ArgumentList $argList -Verb RunAs
    exit 0
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Agent - PowerShell Installer" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Locate Agent Binary
$resolvedBin = $null
$candidates = @(
    $BinaryPath,
    (Join-Path $PSScriptRoot "endpoint-agent.exe"),
    (Join-Path $PSScriptRoot "..\..\agent-windows-amd64.exe"),
    (Join-Path $PSScriptRoot "..\agent-windows-amd64.exe"),
    (Join-Path $PSScriptRoot "..\..\agent\cmd\agent\agent-windows-amd64.exe"),
    (Join-Path $InstallDir "endpoint-agent.exe")
)
foreach ($c in $candidates) {
    if ($c -and (Test-Path $c)) {
        $resolvedBin = (Resolve-Path $c).Path
        break
    }
}

if (-not $resolvedBin) {
    Write-Error "[-] Could not find agent executable ('agent-windows-amd64.exe' or 'endpoint-agent.exe'). Pass -BinaryPath <path>."
    exit 1
}
Write-Host "[+] Using agent binary: $resolvedBin" -ForegroundColor Green

# 3. Create target directories
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
if (-not (Test-Path $DataDir)) {
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
}

$destExe = Join-Path $InstallDir "endpoint-agent.exe"
$credsPath = Join-Path $DataDir "creds.json"

# 4. Stop existing service if running
Write-Host "[*] Checking existing Windows Service 'endpoint-agent'..." -ForegroundColor Yellow
$svc = Get-Service -Name "endpoint-agent" -ErrorAction SilentlyContinue
if ($svc) {
    Write-Host "    Stopping existing service..." -ForegroundColor Yellow
    Stop-Service -Name "endpoint-agent" -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
}

# 5. Copy binary
Write-Host "[*] Copying binary to $destExe..." -ForegroundColor Yellow
Copy-Item -Path $resolvedBin -Destination $destExe -Force

# 6. Run enrollment if token provided
if ($EnrollToken) {
    Write-Host "[*] Enrolling device with server $ServerUrl..." -ForegroundColor Cyan
    & $destExe -server $ServerUrl -enroll $EnrollToken -creds $credsPath
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "[-] Device enrollment returned exit code $LASTEXITCODE. Service will continue installation."
    } else {
        Write-Host "[+] Device enrolled successfully." -ForegroundColor Green
    }
}

# 7. Install / Re-register Windows Service as LocalSystem
Write-Host "[*] Registering Windows Service 'endpoint-agent' (LocalSystem / Administrator)..." -ForegroundColor Yellow
if ($svc) {
    & $destExe -service uninstall
    Start-Sleep -Seconds 1
}
& $destExe -server $ServerUrl -creds $credsPath -service install
if ($LASTEXITCODE -ne 0) {
    Write-Warning "[-] Direct service install exited with code $LASTEXITCODE. Ensuring registration via sc.exe..."
    & sc.exe create endpoint-agent binPath= "`"$destExe`" -server `"$ServerUrl`" -creds `"$credsPath`"" start= auto obj= "LocalSystem" DisplayName= "Enterprise Endpoint Management Agent"
}

# 8. Start Service
Write-Host "[*] Starting Windows Service 'endpoint-agent'..." -ForegroundColor Cyan
& $destExe -service start
if ($LASTEXITCODE -ne 0) {
    & sc.exe start endpoint-agent
}
Start-Sleep -Seconds 2

# 9. Verify Service status
$finalSvc = Get-Service -Name "endpoint-agent" -ErrorAction SilentlyContinue
if ($finalSvc -and $finalSvc.Status -eq 'Running') {
    Write-Host "[+] Endpoint Management Agent is RUNNING as LocalSystem." -ForegroundColor Green
} else {
    Write-Warning "[-] Service status: $($finalSvc.Status). Check logs in $DataDir or Event Viewer."
}

# 10. Record Registry configuration for system tracking
try {
    $regKey = "HKLM:\Software\EndpointAgent"
    if (-not (Test-Path $regKey)) { New-Item -Path $regKey -Force | Out-Null }
    Set-ItemProperty -Path $regKey -Name "InstallDir" -Value $InstallDir
    Set-ItemProperty -Path $regKey -Name "ServerURL" -Value $ServerUrl
    Set-ItemProperty -Path $regKey -Name "DataDir" -Value $DataDir
    Set-ItemProperty -Path $regKey -Name "CredsPath" -Value $credsPath
} catch {
    Write-Verbose "Registry tracking write skipped."
}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Agent Installation Completed Successfully!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
