#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Uninstalls Endpoint Management Agent on Linux using PowerShell Core (pwsh).
.DESCRIPTION
    Stops systemd daemon, disables unit, removes binary, and optionally cleans data.
.PARAMETER PurgeData
    Switch to permanently remove /etc/endpoint-agent credentials and cache.
.PARAMETER InstallBin
    Installed binary path. Default is /usr/local/bin/endpoint-agent.
.PARAMETER CredsPath
    Path to credentials JSON. Default is /etc/endpoint-agent/creds.json.
#>
[CmdletBinding()]
param(
    [switch]$PurgeData = $false,
    [string]$UninstallToken = "",
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
Write-Host "   Enterprise Endpoint Management Agent - Linux Uninstaller (PowerShell)" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 1b. Dual-verification check: prevent unauthorized removal of enrolled endpoints
if ((Test-Path $CredsPath) -and (Test-Path $InstallBin)) {
    if ([string]::IsNullOrWhiteSpace($UninstallToken)) {
        if ([Environment]::UserInteractive) {
            $UninstallToken = Read-Host "Enter Administrator Uninstall Verification Token"
        }
    }
    if ([string]::IsNullOrWhiteSpace($UninstallToken)) {
        Write-Error "[-] ERROR: Managed device requires an admin-issued uninstall token."
        exit 1
    }

    Write-Host "[*] Verifying uninstallation authorization with central server..." -ForegroundColor Yellow
    & $InstallBin -creds $CredsPath -verify-uninstall $UninstallToken
    if ($LASTEXITCODE -ne 0) {
        Write-Error "[-] ERROR: Verification failed. Invalid or expired uninstallation token."
        exit 1
    }
    Write-Host "[+] Uninstallation authorized." -ForegroundColor Green
}

# 2. Stop and disable systemd service
Write-Host "[*] Stopping and disabling systemd service 'endpoint-agent'..." -ForegroundColor Yellow
if (Test-Path $InstallBin) {
    & $InstallBin -service stop 2>$null
    if (-not [string]::IsNullOrWhiteSpace($UninstallToken)) {
        & $InstallBin -creds $CredsPath -service uninstall -uninstall-token $UninstallToken 2>$null
    } else {
        & $InstallBin -service uninstall 2>$null
    }
}
systemctl stop endpoint-agent 2>$null
systemctl disable endpoint-agent 2>$null

$unitFile = "/etc/systemd/system/endpoint-agent.service"
if (Test-Path $unitFile) {
    Remove-Item -Path $unitFile -Force -ErrorAction SilentlyContinue
    systemctl daemon-reload 2>$null
}

# 3. Remove binary
Write-Host "[*] Removing binary from $InstallBin..." -ForegroundColor Yellow
if (Test-Path $InstallBin) {
    Remove-Item -Path $InstallBin -Force -ErrorAction SilentlyContinue
}

# 4. Clean data if requested
$credsDir = [System.IO.Path]::GetDirectoryName($CredsPath)
if ($PurgeData) {
    Write-Host "[*] Purging credentials from $credsDir..." -ForegroundColor Yellow
    if (Test-Path $credsDir) {
        Remove-Item -Path $credsDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Write-Host "[+] Data purged." -ForegroundColor Green
} else {
    Write-Host "[*] Retaining credentials in $credsDir." -ForegroundColor Cyan
}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Linux Agent Uninstalled Successfully!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
