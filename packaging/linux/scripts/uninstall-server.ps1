#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Uninstalls Endpoint Management Server on Linux using PowerShell Core (pwsh).
.DESCRIPTION
    Stops service, removes systemd unit, and cleans server files.
.PARAMETER PurgeDatabase
    Switch to permanently remove database files and backups.
.PARAMETER PurgeConfig
    Switch to permanently remove configuration and log files.
.PARAMETER InstallBin
    Installed binary path. Default is /usr/local/bin/endpoint-server.
.PARAMETER DataDir
    Path for database and backups. Default is /var/lib/endpoint-mgmt-server.
.PARAMETER EnvFile
    Path to environment configuration. Default is /etc/endpoint-mgmt-server/server.env.
#>
[CmdletBinding()]
param(
    [switch]$PurgeDatabase = $false,
    [switch]$PurgeConfig = $true,
    [string]$InstallBin = "/usr/local/bin/endpoint-server",
    [string]$DataDir = "/var/lib/endpoint-mgmt-server",
    [string]$EnvFile = "/etc/endpoint-mgmt-server/server.env"
)

$ErrorActionPreference = "Stop"

# 1. Require root privileges
$uid = (id -u 2>$null)
if ($uid -ne "0") {
    Write-Error "[-] This script requires root privileges. Please run with sudo pwsh $PSCommandPath"
    exit 1
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Server - Linux Uninstaller (PowerShell)" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Stop and disable systemd service
Write-Host "[*] Stopping and disabling systemd service 'endpoint-mgmt-server'..." -ForegroundColor Yellow
systemctl stop endpoint-mgmt-server 2>$null
systemctl disable endpoint-mgmt-server 2>$null

$unitFile = "/etc/systemd/system/endpoint-mgmt-server.service"
if (Test-Path $unitFile) {
    Remove-Item -Path $unitFile -Force -ErrorAction SilentlyContinue
    systemctl daemon-reload 2>$null
}

# 3. Remove binary
Write-Host "[*] Removing server binary from $InstallBin..." -ForegroundColor Yellow
if (Test-Path $InstallBin) {
    Remove-Item -Path $InstallBin -Force -ErrorAction SilentlyContinue
}

# 4. Clean config if requested
$confDir = [System.IO.Path]::GetDirectoryName($EnvFile)
if ($PurgeConfig) {
    Write-Host "[*] Removing configuration and logs from $confDir..." -ForegroundColor Yellow
    if (Test-Path $confDir) {
        Remove-Item -Path $confDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# 5. Clean database if requested
if ($PurgeDatabase) {
    Write-Host "[*] Purging database and backups from $DataDir..." -ForegroundColor Yellow
    if (Test-Path $DataDir) {
        Remove-Item -Path $DataDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Write-Host "[+] Database purged." -ForegroundColor Green
} else {
    Write-Host "[*] Retaining database in $DataDir (use -PurgeDatabase to permanently remove)." -ForegroundColor Cyan
}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Linux Server Uninstalled Successfully!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
