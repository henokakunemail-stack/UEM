# ==============================================================================
# Build script for the Windows Server NSIS Installer
# ==============================================================================
#
# Mirrors build.ps1 (the agent). Kept separate because the two produce different
# programs with different service names, and an operator rolling out to a fleet
# usually wants one without the other.

param(
    [string]$Version = "1.0.0",
    [switch]$SkipCompile = $false
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot
$RepoRoot = Resolve-Path (Join-Path $ScriptRoot "..\..")

Push-Location $RepoRoot
try {
    $ServerExe = Join-Path $RepoRoot "server-windows-amd64.exe"

    if (-not $SkipCompile -or -not (Test-Path $ServerExe)) {
        Write-Host "[1/2] Compiling Windows server binary (CGO_ENABLED=0, GOOS=windows, GOARCH=amd64)..." -ForegroundColor Cyan
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        # The console is embedded into the binary, so the frontend must be built
        # first or the installer ships a server that serves a stale UI. Vite is
        # configured to emit straight into server/cmd/server/dist, which is the
        # go:embed source, so there is nothing to copy afterwards.
        if (Test-Path (Join-Path $RepoRoot "web-console\package.json")) {
            Write-Host "      Building the web console (vite writes into server/cmd/server/dist)..." -ForegroundColor Cyan
            Push-Location (Join-Path $RepoRoot "web-console")
            try {
                & npm run build
                if ($LASTEXITCODE -ne 0) { throw "web-console build failed with exit code $LASTEXITCODE" }
            } finally {
                Pop-Location
            }
        }
        & go build -ldflags="-s -w" -o $ServerExe ./server/cmd/server
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE"
        }
        Write-Host "      Server binary compiled: $ServerExe" -ForegroundColor Green
    } else {
        Write-Host "[1/2] Reusing existing server binary: $ServerExe" -ForegroundColor Yellow
    }

    Write-Host "[2/2] Compiling NSIS Installer..." -ForegroundColor Cyan
    # Windows PowerShell 5.1 is what ships with the OS and has no null-
    # conditional operator, so the lookup is written the long way.
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
        Write-Host "[-] makensis.exe not found on PATH or in the default directories." -ForegroundColor Yellow
        Write-Host "    Install it with:  winget install NSIS.NSIS" -ForegroundColor White
        exit 1
    }

    Push-Location $ScriptRoot
    try {
        & $NsisPath /DVERSION=$Version server.nsi
        if ($LASTEXITCODE -eq 0) {
            $OutExe = Join-Path $ScriptRoot "EndpointServer-Setup.exe"
            Write-Host "[+] Installer built: $OutExe" -ForegroundColor Green
        } else {
            throw "makensis failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
} finally {
    Pop-Location
}
