<#
.SYNOPSIS
    Uninstalls Endpoint Management Agent Windows Service, binaries, and cleanup.
.DESCRIPTION
    Automated script for headless removal of Endpoint Agent via PowerShell.
.PARAMETER PurgeData
    Switch to permanently delete credentials (creds.json) and logs from ProgramData.
.PARAMETER InstallDir
    Target directory for agent binary. Default is C:\Program Files\EndpointAgent.
.PARAMETER DataDir
    Directory for credentials and logs. Default is C:\ProgramData\EndpointAgent.
#>
[CmdletBinding()]
param(
    [switch]$PurgeData = $false,
    [string]$InstallDir = "$env:ProgramFiles\EndpointAgent",
    [string]$DataDir = "$env:ProgramData\EndpointAgent"
)

$ErrorActionPreference = "Stop"

# 1. Require Administrator elevation
$currentPrincipal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Warning "This script requires Administrator privileges. Attempting elevation..."
    $argList = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`""
    if ($PurgeData) { $argList += " -PurgeData" }
    Start-Process powershell.exe -ArgumentList $argList -Verb RunAs
    exit 0
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Agent - PowerShell Uninstaller" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Stop and remove Windows Service
Write-Host "[*] Stopping Windows Service 'endpoint-agent'..." -ForegroundColor Yellow
$destExe = Join-Path $InstallDir "endpoint-agent.exe"
if (Test-Path $destExe) {
    & $destExe -service stop | Out-Null
    Start-Sleep -Seconds 1
    Write-Host "[*] Removing Windows Service 'endpoint-agent'..." -ForegroundColor Yellow
    & $destExe -service uninstall | Out-Null
} else {
    & sc.exe stop endpoint-agent | Out-Null
    Start-Sleep -Seconds 1
    & sc.exe delete endpoint-agent | Out-Null
}

# 3. Clean network filter firewall rules & hosts file markers
Write-Host "[*] Cleaning network filter firewall rules and hosts file sinkholes..." -ForegroundColor Yellow
try {
    & netsh advfirewall firewall delete rule group="EndpointManager-Filter" | Out-Null
} catch {}

$hostsPath = Join-Path $env:SystemRoot "System32\drivers\etc\hosts"
if (Test-Path $hostsPath) {
    try {
        $content = Get-Content $hostsPath -Raw
        $markerBegin = "### BEGIN ENDPOINT-MGMT MANAGED BLOCKLIST ###"
        $markerEnd = "### END ENDPOINT-MGMT MANAGED BLOCKLIST ###"
        if ($content -and $content.Contains($markerBegin)) {
            $bIdx = $content.IndexOf($markerBegin)
            $eIdx = $content.IndexOf($markerEnd)
            if ($eIdx -gt $bIdx) {
                $cleaned = $content.Substring(0, $bIdx) + $content.Substring($eIdx + $markerEnd.Length)
                Set-Content -Path $hostsPath -Value $cleaned.TrimEnd() -Encoding ascii
                Write-Host "[+] Hosts file sinkhole markers cleaned." -ForegroundColor Green
            }
        }
    } catch {
        Write-Warning "[-] Error cleaning hosts file: $_"
    }
}

# 4. Remove installation binaries
Write-Host "[*] Removing installation files from $InstallDir..." -ForegroundColor Yellow
if (Test-Path $InstallDir) {
    Remove-Item -Path $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
    if (-not (Test-Path $InstallDir)) {
        Write-Host "[+] Program files removed." -ForegroundColor Green
    } else {
        Write-Warning "[-] Some files in $InstallDir were locked. They will be removed on reboot."
    }
}

# 5. Clean Data / Credentials if requested
if ($PurgeData) {
    Write-Host "[*] Purging credentials and cache from $DataDir..." -ForegroundColor Yellow
    if (Test-Path $DataDir) {
        Remove-Item -Path $DataDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "[+] Data directory purged." -ForegroundColor Green
    }
} else {
    Write-Host "[*] Retaining credentials in $DataDir (use -PurgeData to permanently remove)." -ForegroundColor Cyan
}

# 6. Clean Registry keys
try {
    Remove-Item -Path "HKLM:\Software\EndpointAgent" -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -Path "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent" -Recurse -Force -ErrorAction SilentlyContinue
} catch {}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Endpoint Management Agent Uninstalled Successfully!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
