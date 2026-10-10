<#
.SYNOPSIS
    Uninstalls Endpoint Management Server Windows Service and cleans components.
.DESCRIPTION
    Automated script for headless server removal via PowerShell.
.PARAMETER PurgeDatabase
    Switch to permanently delete database (server.db) and backups.
.PARAMETER PurgeConfig
    Switch to permanently delete configuration (server.env) and logs.
.PARAMETER InstallDir
    Target directory for server binary. Default is C:\Program Files\EndpointMgmtServer.
.PARAMETER DataDir
    Directory for configuration and log files. Default is C:\ProgramData\EndpointMgmtServer.
#>
[CmdletBinding()]
param(
    [switch]$PurgeDatabase = $false,
    [switch]$PurgeConfig = $true,
    [string]$InstallDir = "$env:ProgramFiles\EndpointMgmtServer",
    [string]$DataDir = "$env:ProgramData\EndpointMgmtServer"
)

$ErrorActionPreference = "Stop"

# 1. Require Administrator elevation
$currentPrincipal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Warning "This script requires Administrator privileges. Attempting elevation..."
    $argList = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`""
    if ($PurgeDatabase) { $argList += " -PurgeDatabase" }
    if ($PurgeConfig) { $argList += " -PurgeConfig" }
    Start-Process powershell.exe -ArgumentList $argList -Verb RunAs
    exit 0
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Server - PowerShell Uninstaller" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Stop and remove Windows Service
Write-Host "[*] Stopping Windows Service 'endpoint-mgmt-server'..." -ForegroundColor Yellow
$destExe = Join-Path $InstallDir "endpoint-server.exe"
if (Test-Path $destExe) {
    & $destExe -service stop | Out-Null
    Start-Sleep -Seconds 2
    Write-Host "[*] Removing Windows Service 'endpoint-mgmt-server'..." -ForegroundColor Yellow
    & $destExe -service uninstall | Out-Null
} else {
    & sc.exe stop endpoint-mgmt-server | Out-Null
    Start-Sleep -Seconds 2
    & sc.exe delete endpoint-mgmt-server | Out-Null
}

# 3. Clean Firewall Rule
Write-Host "[*] Removing Windows Firewall rule..." -ForegroundColor Yellow
try {
    & netsh advfirewall firewall delete rule name="Endpoint Management Server" | Out-Null
} catch {}

# 4. Remove Binary and installation files
Write-Host "[*] Removing server binary from $InstallDir..." -ForegroundColor Yellow
if (Test-Path $destExe) {
    Remove-Item -Path $destExe -Force -ErrorAction SilentlyContinue
}
$uninstExe = Join-Path $InstallDir "uninstall.exe"
if (Test-Path $uninstExe) {
    Remove-Item -Path $uninstExe -Force -ErrorAction SilentlyContinue
}

# 5. Clean Database if requested
$dbDir = Join-Path $InstallDir "data"
if ($PurgeDatabase) {
    Write-Host "[*] Purging database directory $dbDir..." -ForegroundColor Yellow
    if (Test-Path $dbDir) {
        Remove-Item -Path $dbDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -Path $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "[+] Database and backups purged." -ForegroundColor Green
} else {
    Write-Host "[*] Database retained at $dbDir (use -PurgeDatabase to permanently remove)." -ForegroundColor Cyan
}

# 6. Clean Config and Logs
if ($PurgeConfig) {
    Write-Host "[*] Removing configuration and logs from $DataDir..." -ForegroundColor Yellow
    if (Test-Path $DataDir) {
        Remove-Item -Path $DataDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# 7. Clean Registry keys
try {
    Remove-Item -Path "HKLM:\Software\EndpointMgmtServer" -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -Path "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointMgmtServer" -Recurse -Force -ErrorAction SilentlyContinue
} catch {}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Endpoint Management Server Uninstalled Successfully!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
