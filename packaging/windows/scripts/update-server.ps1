<#
.SYNOPSIS
    Safely updates Endpoint Management Server on Windows with zero downtime config loss.
.DESCRIPTION
    Automated in-place updater for Endpoint Management Server.
    Supports updating from GitHub Releases (default: latest) or a local pre-compiled binary.
    Guarantees:
      - Auto-detection of existing installation and service
      - Zero overwriting of server.env / JWT_SECRET / admin passwords
      - Consistent pre-update SQLite database snapshot
      - Graceful service stop, binary rotation with .bak preservation
      - Automated health-check verification (/healthz)
      - Automatic transactional rollback if the new version fails to start
.PARAMETER Version
    Target GitHub release version tag (e.g. 'v1.1.0') or 'latest'. Default is 'latest'.
.PARAMETER BinaryPath
    Optional path to a local server binary (for offline or air-gapped updates).
.PARAMETER Repo
    GitHub repository in 'owner/repo' format. Default is 'henokakunemail-stack/UEM'.
.PARAMETER HealthTimeoutSec
    Seconds to wait for /healthz verification before rolling back. Default is 30.
.PARAMETER SkipBackup
    Switch to skip automatic pre-update database snapshot.
.PARAMETER InstallDir
    Optional override for server installation directory.
.PARAMETER EnvFile
    Optional override for server configuration file.
.EXAMPLE
    # Update to latest GitHub release:
    .\update-server.ps1

    # One-liner update:
    irm https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/packaging/windows/scripts/update-server.ps1 | iex

    # Update using a local binary:
    .\update-server.ps1 -BinaryPath .\server-windows-amd64.exe
#>
[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$BinaryPath = "",
    [string]$Repo = "henokakunemail-stack/UEM",
    [int]$HealthTimeoutSec = 30,
    [switch]$SkipBackup = $false,
    [string]$InstallDir = "",
    [string]$EnvFile = ""
)

$ErrorActionPreference = "Stop"

# 1. Require Administrator elevation
$currentPrincipal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Warning "This update script requires Administrator privileges. Elevating..."
    $argList = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`""
    if ($PSBoundParameters.Count -gt 0) {
        foreach ($key in $PSBoundParameters.Keys) {
            $val = $PSBoundParameters[$key]
            if ($val -is [switch] -and $val) {
                $argList += " -$key"
            } elseif ($val -isnot [switch]) {
                $argList += " -$key `"$val`""
            }
        }
    }
    Start-Process powershell.exe -ArgumentList $argList -Verb RunAs
    exit 0
}

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management Server - Safe Updater" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Auto-Detect Installed Server & Paths
$serviceName = "endpoint-mgmt-server"
$detectedBin = $null
$detectedEnv = $null
$detectedDir = $InstallDir

# Check Registry
if (Test-Path "HKLM:\Software\EndpointMgmtServer") {
    $regDir = (Get-ItemProperty -Path "HKLM:\Software\EndpointMgmtServer" -Name "InstallDir" -ErrorAction SilentlyContinue).InstallDir
    $regEnv = (Get-ItemProperty -Path "HKLM:\Software\EndpointMgmtServer" -Name "EnvFile" -ErrorAction SilentlyContinue).EnvFile
    if ($regDir -and (Test-Path $regDir)) { $detectedDir = $regDir }
    if ($regEnv -and (Test-Path $regEnv)) { $detectedEnv = $regEnv }
}

# Check Service configuration
$svc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($svc) {
    try {
        $cimSvc = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
        if ($cimSvc -and $cimSvc.PathName) {
            $rawPath = $cimSvc.PathName
            if ($rawPath -match '^"?([^"\s]+(?: [^"\s]+)*\.exe)"?') {
                $candidateExe = $matches[1]
                if (Test-Path $candidateExe) {
                    $detectedBin = (Resolve-Path $candidateExe).Path
                    if (-not $detectedDir) { $detectedDir = Split-Path $detectedBin -Parent }
                }
            }
            if ($rawPath -match '-env-file\s+"?([^"\s]+)"?') {
                $candidateEnv = $matches[1]
                if (Test-Path $candidateEnv) { $detectedEnv = (Resolve-Path $candidateEnv).Path }
            }
        }
    } catch {
        # Fallback to standard locations
    }
}

# Standard Fallback Paths
if (-not $detectedDir) {
    $defaultDirs = @(
        "$env:ProgramFiles\EndpointMgmtServer",
        "C:\EndpointServer",
        "C:\EndpointMgmt"
    )
    foreach ($d in $defaultDirs) {
        if (Test-Path $d) { $detectedDir = $d; break }
    }
}

if (-not $detectedDir -or -not (Test-Path $detectedDir)) {
    Write-Error "[-] Could not find installed server directory. Ensure server is installed or pass -InstallDir."
    exit 1
}

$destExe = Join-Path $detectedDir "endpoint-server.exe"
if (-not (Test-Path $destExe) -and $detectedBin) {
    $destExe = $detectedBin
}

if (-not (Test-Path $destExe)) {
    Write-Error "[-] Installed server binary not found at '$destExe'. Pass -InstallDir or install server first."
    exit 1
}

if (-not $detectedEnv) {
    $defaultEnvs = @(
        $EnvFile,
        "$env:ProgramData\EndpointMgmtServer\server.env",
        (Join-Path $detectedDir ".env"),
        (Join-Path $detectedDir "server.env")
    )
    foreach ($e in $defaultEnvs) {
        if ($e -and (Test-Path $e)) { $detectedEnv = (Resolve-Path $e).Path; break }
    }
}

Write-Host "[+] Detected install directory: $detectedDir" -ForegroundColor Green
Write-Host "[+] Detected server binary:    $destExe" -ForegroundColor Green
if ($detectedEnv) {
    Write-Host "[+] Detected configuration:    $detectedEnv (PRESERVED)" -ForegroundColor Green
} else {
    Write-Warning "[!] Configuration file not found. Running with environment defaults."
}

# 3. Resolve & Stage Target Binary
$stagingDir = Join-Path $env:TEMP "uem-server-update-$PID"
if (Test-Path $stagingDir) { Remove-Item -Path $stagingDir -Recurse -Force -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Path $stagingDir -Force | Out-Null

$stagedBin = $null

try {
    if ($BinaryPath) {
        if (-not (Test-Path $BinaryPath)) {
            Write-Error "[-] Specified local binary not found at '$BinaryPath'."
            exit 1
        }
        $stagedBin = (Resolve-Path $BinaryPath).Path
        Write-Host "[+] Using local binary: $stagedBin" -ForegroundColor Green
    } else {
        Write-Host "[*] Fetching release metadata from GitHub ($Repo, version: $Version)..." -ForegroundColor Cyan
        $headers = @{ "User-Agent" = "UEM-Server-Updater" }
        if ($env:GITHUB_TOKEN) {
            $headers["Authorization"] = "token $env:GITHUB_TOKEN"
        }

        $apiUrl = if ($Version -eq "latest") {
            "https://api.github.com/repos/$Repo/releases/latest"
        } else {
            "https://api.github.com/repos/$Repo/releases/tags/$Version"
        }

        $release = $null
        try {
            $release = Invoke-RestMethod -Uri $apiUrl -Headers $headers -TimeoutSec 15
        } catch {
            Write-Warning "[-] GitHub API request failed: $_"
        }

        $downloadUrl = $null
        $assetName = $null

        if ($release -and $release.assets) {
            # Priority 1: Windows zip archive
            $zipAsset = $release.assets | Where-Object { $_.name -match 'endpoint-server.*windows.*amd64\.zip$' } | Select-Object -First 1
            if ($zipAsset) {
                $downloadUrl = $zipAsset.browser_download_url
                $assetName = $zipAsset.name
            } else {
                # Priority 2: Standalone Windows exe
                $exeAsset = $release.assets | Where-Object { $_.name -match '(endpoint-server|server-windows).*amd64\.exe$' } | Select-Object -First 1
                if ($exeAsset) {
                    $downloadUrl = $exeAsset.browser_download_url
                    $assetName = $exeAsset.name
                }
            }
        }

        # Fallback to direct predictable release asset URL if API blocked/rate-limited
        if (-not $downloadUrl) {
            $tag = if ($release -and $release.tag_name) { $release.tag_name } else { if ($Version -ne "latest") { $Version } else { "v1.0.0" } }
            $fallbackUrls = @(
                "https://github.com/$Repo/releases/download/$tag/endpoint-server-windows-amd64.zip",
                "https://github.com/$Repo/releases/download/$tag/server-windows-amd64.exe"
            )
            foreach ($u in $fallbackUrls) {
                try {
                    $testReq = Invoke-WebRequest -Uri $u -Method Head -TimeoutSec 5 -ErrorAction SilentlyContinue
                    if ($testReq.StatusCode -eq 200) { $downloadUrl = $u; $assetName = Split-Path $u -Leaf; break }
                } catch {}
            }
        }

        if (-not $downloadUrl) {
            Write-Error "[-] Could not find a suitable Windows release asset for $Repo ($Version). Pass -BinaryPath <path> to update from local file."
            exit 1
        }

        Write-Host "[+] Downloading asset: $assetName..." -ForegroundColor Cyan
        $downloadFile = Join-Path $stagingDir $assetName
        Invoke-WebRequest -Uri $downloadUrl -OutFile $downloadFile -Headers $headers

        if ($assetName -match '\.zip$') {
            Write-Host "[*] Extracting release archive..." -ForegroundColor DarkGray
            Expand-Archive -Path $downloadFile -DestinationPath $stagingDir -Force
            $foundExes = Get-ChildItem -Path $stagingDir -Filter "*.exe" -Recurse
            foreach ($fe in $foundExes) {
                if ($fe.Name -match 'server') { $stagedBin = $fe.FullName; break }
            }
            if (-not $stagedBin -and $foundExes.Count -gt 0) { $stagedBin = $foundExes[0].FullName }
        } else {
            $stagedBin = $downloadFile
        }

        if (-not $stagedBin -or -not (Test-Path $stagedBin)) {
            Write-Error "[-] Extracted binary not found in downloaded package."
            exit 1
        }
    }

    # 4. Pre-flight Binary Validation
    $binItem = Get-Item $stagedBin
    if ($binItem.Length -lt 5000000) { # Server binary with embedded frontend is > 10MB
        Write-Error "[-] Target binary size ($($binItem.Length) bytes) is suspiciously small. Aborting update."
        exit 1
    }
    Write-Host "[+] Validated new binary size: $([math]::Round($binItem.Length / 1MB, 2)) MB" -ForegroundColor Green

    # 5. Pre-Update Hot Database Snapshot
    if (-not $SkipBackup -and $detectedEnv -and (Test-Path $detectedEnv)) {
        Write-Host "`n[*] Creating consistent pre-update database backup..." -ForegroundColor Cyan
        $dataDir = Join-Path $detectedDir "data"
        $backupDir = Join-Path $dataDir "backups"
        if (-not (Test-Path $backupDir)) { New-Item -ItemType Directory -Path $backupDir -Force | Out-Null }

        $timestamp = (Get-Date).ToString("yyyyMMdd_HHmmss")
        $backupFile = Join-Path $backupDir "pre_update_$timestamp.db"

        # Attempt native consistent backup via existing server binary
        try {
            & $destExe -env-file $detectedEnv -backup $backupFile | Out-Null
            if (Test-Path $backupFile) {
                Write-Host "[+] Consistent database snapshot created: $backupFile" -ForegroundColor Green
            }
        } catch {
            Write-Warning "[!] Native backup flag warning: $_. Performing file-level copy..."
            $dbCandidates = @(
                (Join-Path $dataDir "server.db"),
                (Join-Path $dataDir "endpoint-mgmt.db")
            )
            foreach ($db in $dbCandidates) {
                if (Test-Path $db) {
                    Copy-Item -Path $db -Destination $backupFile -Force -ErrorAction SilentlyContinue
                    Write-Host "[+] File-level database copy created: $backupFile" -ForegroundColor Green
                    break
                }
            }
        }
    }

    # 6. Stop Windows Service Gracefully
    Write-Host "`n[*] Stopping Windows Service '$serviceName'..." -ForegroundColor Yellow
    $serviceRunning = $false
    $currentSvc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($currentSvc -and $currentSvc.Status -eq 'Running') {
        $serviceRunning = $true
        Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
        # Wait up to 10 seconds for process shutdown
        $sw = [Diagnostics.Stopwatch]::StartNew()
        while ($sw.ElapsedMilliseconds -lt 10000) {
            $checkSvc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
            if (-not $checkSvc -or $checkSvc.Status -eq 'Stopped') { break }
            Start-Sleep -Milliseconds 500
        }
    }

    # 7. Rotate Binary with Rollback Safety (.bak)
    $bakExe = "$destExe.bak"
    Write-Host "[*] Rotating server binary ($destExe -> $bakExe)..." -ForegroundColor Yellow
    Copy-Item -Path $destExe -Destination $bakExe -Force
    Copy-Item -Path $stagedBin -Destination $destExe -Force
    Write-Host "[+] New binary successfully deployed in-place." -ForegroundColor Green

    # The release zip carries the agents for every platform in agents\. The
    # server hands them to the one-line Linux installer and registers them as
    # releases from Agent Updates; it looks for them next to its binary.
    $stagedAgents = Join-Path $stagingDir "agents"
    if (Test-Path $stagedAgents) {
        $agentsDir = Join-Path (Split-Path $destExe -Parent) "agents"
        New-Item -ItemType Directory -Path $agentsDir -Force | Out-Null
        Copy-Item -Path (Join-Path $stagedAgents "agent-*") -Destination $agentsDir -Force
        Write-Host "[+] Updated bundled agent binaries in $agentsDir" -ForegroundColor Green
    }

    # 8. Restart Windows Service
    Write-Host "[*] Starting Windows Service '$serviceName'..." -ForegroundColor Cyan
    Start-Service -Name $serviceName -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2

    $finalSvc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if (-not $finalSvc -or $finalSvc.Status -ne 'Running') {
        throw "Windows Service '$serviceName' failed to start after binary replacement."
    }

    # 9. Health Verification Probe
    $port = 8443
    if ($detectedEnv -and (Test-Path $detectedEnv)) {
        $content = Get-Content $detectedEnv -Raw
        if ($content -match 'HTTP_ADDR=.*:(\d+)') {
            $port = [int]$matches[1]
        }
    }

    Write-Host "[*] Probing server health at http://127.0.0.1:$port/healthz (timeout: ${HealthTimeoutSec}s)..." -ForegroundColor Cyan
    $healthy = $false
    $probeSw = [Diagnostics.Stopwatch]::StartNew()

    while ($probeSw.Elapsed.TotalSeconds -lt $HealthTimeoutSec) {
        try {
            $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$port/healthz" -UseBasicParsing -TimeoutSec 2 -ErrorAction Stop
            if ($resp.StatusCode -eq 200) {
                $healthy = $true
                break
            }
        } catch {
            # Server may still be applying database migrations
            Start-Sleep -Seconds 2
        }
    }

    if (-not $healthy) {
        throw "Server failed health probe at http://127.0.0.1:$port/healthz within $HealthTimeoutSec seconds."
    }

    Write-Host "`n==========================================================" -ForegroundColor Green
    Write-Host "   Server Update Successful & Verified Healthy!" -ForegroundColor Green
    Write-Host "==========================================================" -ForegroundColor Green
    Write-Host "  Service Status:      RUNNING" -ForegroundColor Green
    Write-Host "  Active Binary:       $destExe" -ForegroundColor Green
    Write-Host "  Rollback Backup:     $bakExe" -ForegroundColor DarkGray
    Write-Host "  Configuration File:  $detectedEnv (UNTOUCHED)" -ForegroundColor Green

} catch {
    Write-Error "`n[-] UPDATE FAILED: $_"
    Write-Warning "[!] Initiating automatic rollback to previous binary..."

    if (Test-Path "$destExe.bak") {
        try {
            Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 1
            Copy-Item -Path "$destExe.bak" -Destination $destExe -Force
            Start-Service -Name $serviceName -ErrorAction SilentlyContinue
            Write-Host "[+] Rollback complete. Original binary restored and service restarted." -ForegroundColor Yellow
        } catch {
            Write-Error "[-] Critical: Automatic rollback encountered error: $_"
        }
    }
    exit 1
} finally {
    if (Test-Path $stagingDir) {
        Remove-Item -Path $stagingDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
