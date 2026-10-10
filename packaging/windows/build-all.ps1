# ==============================================================================
# Enterprise Endpoint Management - All-in-One Windows Package Builder
# ==============================================================================
# Builds:
#   1. Server Windows Binary (server-windows-amd64.exe)
#   2. Agent Windows Binary (agent-windows-amd64.exe)
#   3. EndpointServer-Setup.exe      (Server GUI Installer)
#   4. EndpointServer-Uninstall.exe  (Server GUI Standalone Uninstaller)
#   5. EndpointAgent-Setup.exe       (Agent GUI Installer - LocalSystem)
#   6. EndpointAgent-Uninstall.exe   (Agent GUI Standalone Uninstaller)
# ==============================================================================

param(
    [string]$Version = "",
    [switch]$SkipFrontend = $false,
    [switch]$SkipCompile = $false
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot
$RepoRoot = Resolve-Path (Join-Path $ScriptRoot "..\..")

# 1. Resolve Version
if (-not $Version) {
    $VersionFile = Join-Path $RepoRoot "VERSION"
    if (Test-Path $VersionFile) {
        $Version = (Get-Content $VersionFile -Raw).Trim()
    } else {
        $Version = "1.0.0"
    }
}
$parts = $Version -split '\.'
$numeric = [int[]]@()
foreach ($p in $parts) {
    if ($p -match '^\d+$') {
        $numeric += [int]$p
    }
}
while ($numeric.Count -lt 4) { $numeric += 0 }
$VersionNum = ($numeric[0..3] -join '.')

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "   Enterprise Endpoint Management - Windows Packager" -ForegroundColor Cyan
Write-Host "   Version: $Version ($VersionNum)" -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 2. Locate makensis.exe
$Cmd = Get-Command makensis.exe -ErrorAction SilentlyContinue
$NsisPath = if ($Cmd) { $Cmd.Source } else { $null }
if (-not $NsisPath) {
    $Candidates = @(
        "$env:ProgramFiles\NSIS\makensis.exe",
        "${env:ProgramFiles(x86)}\NSIS\makensis.exe"
    )
    foreach ($c in $Candidates) {
        if (Test-Path $c) { $NsisPath = $c; break }
    }
}
if (-not $NsisPath) {
    Write-Error "makensis.exe not found. Install NSIS via 'winget install NSIS.NSIS'."
    exit 1
}
Write-Host "[+] Using NSIS compiler: $NsisPath" -ForegroundColor Green

Push-Location $RepoRoot
try {
    # 3. Build Web Console Frontend (embedded in server)
    if (-not $SkipFrontend -and (Test-Path "web-console\package.json")) {
        Write-Host "`n[1/4] Building Web Console frontend (Vite)..." -ForegroundColor Cyan
        Push-Location "web-console"
        try {
            & npm run build
            if ($LASTEXITCODE -ne 0) { throw "npm run build failed with exit code $LASTEXITCODE" }
        } finally {
            Pop-Location
        }
        Write-Host "      Web console built successfully." -ForegroundColor Green
    } else {
        Write-Host "`n[1/4] Skipping frontend build." -ForegroundColor Yellow
    }

    # 4. Compile Go binaries
    if (-not $SkipCompile) {
        Write-Host "`n[2/4] Compiling Go binaries for Windows AMD64..." -ForegroundColor Cyan
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        $ldflags = "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$Version"

        Write-Host "      Compiling server binary: server-windows-amd64.exe..." -ForegroundColor DarkGray
        & go build -ldflags="$ldflags" -o "server-windows-amd64.exe" ./server/cmd/server
        if ($LASTEXITCODE -ne 0) { throw "Server go build failed" }

        Write-Host "      Compiling agent binary: agent-windows-amd64.exe..." -ForegroundColor DarkGray
        & go build -ldflags="$ldflags" -o "agent-windows-amd64.exe" ./agent/cmd/agent
        if ($LASTEXITCODE -ne 0) { throw "Agent go build failed" }
        Write-Host "      Go binaries compiled successfully." -ForegroundColor Green
    } else {
        Write-Host "`n[2/4] Skipping Go compilation." -ForegroundColor Yellow
    }

    # 5. Compile NSIS Installers and Uninstallers
    Write-Host "`n[3/4] Compiling NSIS GUI packages..." -ForegroundColor Cyan
    Push-Location $ScriptRoot
    try {
        # Server Installer
        Write-Host "      Building EndpointServer-Setup.exe..." -ForegroundColor DarkGray
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum server.nsi
        if ($LASTEXITCODE -ne 0) { throw "makensis server.nsi failed" }

        # Server Uninstaller
        Write-Host "      Building EndpointServer-Uninstall.exe..." -ForegroundColor DarkGray
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum server-uninstall.nsi
        if ($LASTEXITCODE -ne 0) { throw "makensis server-uninstall.nsi failed" }

        # Agent Installer
        Write-Host "      Building EndpointAgent-Setup.exe..." -ForegroundColor DarkGray
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum agent.nsi
        if ($LASTEXITCODE -ne 0) { throw "makensis agent.nsi failed" }

        # Agent Uninstaller
        Write-Host "      Building EndpointAgent-Uninstall.exe..." -ForegroundColor DarkGray
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum agent-uninstall.nsi
        if ($LASTEXITCODE -ne 0) { throw "makensis agent-uninstall.nsi failed" }
    } finally {
        Pop-Location
    }

    # 6. Summary Report
    Write-Host "`n[4/4] Package Summary:" -ForegroundColor Cyan
    $packages = @(
        (Join-Path $ScriptRoot "EndpointServer-Setup.exe"),
        (Join-Path $ScriptRoot "EndpointServer-Uninstall.exe"),
        (Join-Path $ScriptRoot "EndpointAgent-Setup.exe"),
        (Join-Path $ScriptRoot "EndpointAgent-Uninstall.exe")
    )

    foreach ($pkg in $packages) {
        if (Test-Path $pkg) {
            $item = Get-Item $pkg
            $sizeMB = [math]::Round($item.Length / 1MB, 2)
            Write-Host ("  [+] {0,-32} ({1} MB)" -f $item.Name, $sizeMB) -ForegroundColor Green
        } else {
            Write-Host ("  [-] {0,-32} (MISSING)" -f (Split-Path $pkg -Leaf)) -ForegroundColor Red
        }
    }

    Write-Host "`n==========================================================" -ForegroundColor Green
    Write-Host "   All Windows GUI packages generated successfully!" -ForegroundColor Green
    Write-Host "==========================================================" -ForegroundColor Green
} finally {
    Pop-Location
}
