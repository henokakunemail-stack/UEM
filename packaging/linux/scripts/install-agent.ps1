#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Installs Endpoint Management Agent on Linux using PowerShell Core (pwsh).
.DESCRIPTION
    Configures systemd daemon running as root, copies binary, and handles enrollment.
.PARAMETER ServerUrl
    The central management server URL (default http://localhost:8443).
.PARAMETER EnrollToken
    Optional one-time enrollment token from Web Console.
.PARAMETER BinaryPath
    Optional path to agent Linux binary (agent-linux-amd64).
.PARAMETER InstallBin
    Destination binary path. Default is /usr/local/bin/endpoint-agent.
.PARAMETER CredsPath
    Path to credentials JSON. Default is /etc/endpoint-agent/creds.json.
#>
[CmdletBinding()]
param(
    [string]$ServerUrl = "http://localhost:8443",
    [string]$EnrollToken = "",
    [string]$BinaryPath = "",
    [string]$InstallBin = "/usr/local/bin/endpoint-agent",
    [string]$CredsPath = "/etc/endpoint-agent/creds.json"
)

$ErrorActionPreference = "Stop"

# 1. Require root privileges
$uid = (id -u 2>$null)
if ($uid -ne "0") {
    Write-Error "[-] This script requires root privileges. Please run with sudo pwsh $PSCommandPath"
    exit 1
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Agent - Linux Installer (PowerShell)" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Locate Linux binary
$resolvedBin = $null
$candidates = @(
    $BinaryPath,
    (Join-Path $PSScriptRoot "endpoint-agent"),
    (Join-Path $PSScriptRoot "agent-linux-amd64"),
    (Join-Path $PSScriptRoot "..\..\agent-linux-amd64"),
    (Join-Path $PSScriptRoot "..\..\agent\cmd\agent\agent-linux-amd64"),
    "/tmp/agent-linux-amd64"
)
foreach ($c in $candidates) {
    if ($c -and (Test-Path $c)) {
        $resolvedBin = $c
        break
    }
}

if (-not $resolvedBin) {
    Write-Error "[-] Agent Linux binary not found. Pass -BinaryPath <path_to_agent-linux-amd64>."
    exit 1
}
Write-Host "[+] Using agent binary: $resolvedBin" -ForegroundColor Green

# 3. Create target directory
$binDir = [System.IO.Path]::GetDirectoryName($InstallBin)
$credsDir = [System.IO.Path]::GetDirectoryName($CredsPath)
if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir -Force | Out-Null }
if (-not (Test-Path $credsDir)) { New-Item -ItemType Directory -Path $credsDir -Force | Out-Null }
chmod 700 $credsDir 2>$null

# 4. Copy binary and set permissions
Write-Host "[*] Installing binary to $InstallBin..." -ForegroundColor Yellow
Copy-Item -Path $resolvedBin -Destination $InstallBin -Force
chmod 0755 $InstallBin

# 5. Perform Enrollment if token provided
if ($EnrollToken) {
    Write-Host "[*] Enrolling device with $ServerUrl..." -ForegroundColor Cyan
    & $InstallBin -server $ServerUrl -enroll $EnrollToken -creds $CredsPath
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "[-] Enrollment returned exit code $LASTEXITCODE. Service will continue installation."
    } else {
        Write-Host "[+] Device enrolled successfully." -ForegroundColor Green
    }
}

# 6. Install systemd service
Write-Host "[*] Registering systemd service 'endpoint-agent'..." -ForegroundColor Yellow
& $InstallBin -server $ServerUrl -creds $CredsPath -service install
if ($LASTEXITCODE -ne 0) {
    Write-Warning "[-] Direct service install exited with code $LASTEXITCODE. Re-attempting via systemctl..."
}

# 7. Start service
Write-Host "[*] Starting systemd service 'endpoint-agent'..." -ForegroundColor Cyan
& $InstallBin -service start
if ($LASTEXITCODE -ne 0) {
    systemctl start endpoint-agent
}

Start-Sleep -Seconds 1
$status = (systemctl is-active endpoint-agent 2>$null)
if ($status -eq "active") {
    Write-Host "[+] Endpoint Agent is ACTIVE and RUNNING under systemd." -ForegroundColor Green
} else {
    Write-Warning "[-] Service status: $status. Check 'journalctl -u endpoint-agent -n 50'."
}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Linux Agent Installation Complete!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
