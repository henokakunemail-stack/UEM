#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Installs Endpoint Management Server on Linux using PowerShell Core (pwsh).
.DESCRIPTION
    Sets up systemd service, copies server binary, and configures runtime environment.
.PARAMETER ListenAddr
    Interface and port to bind (default :8443).
.PARAMETER AdminPassword
    Initial administrator password for bootstrap.
.PARAMETER BinaryPath
    Optional path to server binary (server-linux-amd64).
.PARAMETER InstallBin
    Destination binary path. Default is /usr/local/bin/endpoint-server.
.PARAMETER DataDir
    Path for database and backups. Default is /var/lib/endpoint-mgmt-server.
.PARAMETER EnvFile
    Path to environment configuration. Default is /etc/endpoint-mgmt-server/server.env.
#>
[CmdletBinding()]
param(
    [string]$ListenAddr = ":8443",
    [string]$AdminPassword = "",
    [string]$BinaryPath = "",
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
Write-Host "   Enterprise Endpoint Management Server - Linux Installer (PowerShell)" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Locate Linux server binary
$resolvedBin = $null
$candidates = @(
    $BinaryPath,
    (Join-Path $PSScriptRoot "endpoint-server"),
    (Join-Path $PSScriptRoot "server-linux-amd64"),
    (Join-Path $PSScriptRoot "..\..\server-linux-amd64"),
    (Join-Path $PSScriptRoot "..\..\server\cmd\server\server-linux-amd64"),
    "/tmp/server-linux-amd64"
)
foreach ($c in $candidates) {
    if ($c -and (Test-Path $c)) {
        $resolvedBin = $c
        break
    }
}

if (-not $resolvedBin) {
    Write-Error "[-] Server Linux binary not found. Pass -BinaryPath <path_to_server-linux-amd64>."
    exit 1
}
Write-Host "[+] Using server binary: $resolvedBin" -ForegroundColor Green

# 3. Create target directories
$binDir = [System.IO.Path]::GetDirectoryName($InstallBin)
$confDir = [System.IO.Path]::GetDirectoryName($EnvFile)
$backupDir = Join-Path $DataDir "backups"

if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir -Force | Out-Null }
if (-not (Test-Path $confDir)) { New-Item -ItemType Directory -Path $confDir -Force | Out-Null }
if (-not (Test-Path $DataDir)) { New-Item -ItemType Directory -Path $DataDir -Force | Out-Null }
if (-not (Test-Path $backupDir)) { New-Item -ItemType Directory -Path $backupDir -Force | Out-Null }

# 4. Copy binary and set permissions
Write-Host "[*] Installing server binary to $InstallBin..." -ForegroundColor Yellow
Copy-Item -Path $resolvedBin -Destination $InstallBin -Force
chmod 0755 $InstallBin

# 5. Generate server.env configuration
Write-Host "[*] Writing configuration to $EnvFile..." -ForegroundColor Yellow
$dbFile = Join-Path $DataDir "server.db"
$logFile = Join-Path $DataDir "server.log"
$envLines = @(
    "# Runtime configuration for endpoint-mgmt-server",
    "HTTP_ADDR=$ListenAddr",
    "DB_PATH=$dbFile",
    "BACKUP_DIR=$backupDir",
    "LOG_FILE=$logFile",
    "LOG_LEVEL=info",
    "ALLOWED_ORIGIN_DOMAINS=",
    "JWT_SECRET="
)
if ($AdminPassword) {
    $envLines += "ADMIN_PASSWORD=$AdminPassword"
}
[IO.File]::WriteAllLines($EnvFile, $envLines, [Text.Encoding]::UTF8)
chmod 0600 $EnvFile

# 6. Install systemd unit
Write-Host "[*] Registering systemd service 'endpoint-mgmt-server'..." -ForegroundColor Yellow
$unitContent = @"
[Unit]
Description=Enterprise Endpoint Management Server
After=network.target

[Service]
Type=simple
ExecStart=$InstallBin -env-file $EnvFile
Restart=always
RestartSec=5s
LimitNOFILE=65535
WorkingDirectory=$DataDir

[Install]
WantedBy=multi-user.target
"@
$unitFile = "/etc/systemd/system/endpoint-mgmt-server.service"
[IO.File]::WriteAllText($unitFile, $unitContent, [Text.Encoding]::UTF8)

systemctl daemon-reload
systemctl enable endpoint-mgmt-server
systemctl restart endpoint-mgmt-server
Start-Sleep -Seconds 2

$status = (systemctl is-active endpoint-mgmt-server 2>$null)
if ($status -eq "active") {
    Write-Host "[+] Server is ACTIVE and listening on $ListenAddr." -ForegroundColor Green
} else {
    Write-Warning "[-] Service status: $status. Check 'journalctl -u endpoint-mgmt-server -n 50'."
}

Write-Host "==========================================================" -ForegroundColor Green
Write-Host "[+] Linux Server Installation Complete!" -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
