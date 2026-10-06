# ==============================================================================
# Build script for the Windows Server NSIS Installer
# ==============================================================================
#
# Mirrors build.ps1 (the agent). Kept separate because the two produce different
# programs with different service names, and an operator rolling out to a fleet
# usually wants one without the other.

param(
    [string]$Version = "",
    [switch]$SkipCompile = $false
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot
$RepoRoot = Resolve-Path (Join-Path $ScriptRoot "..\..")

# Same VERSION default as build.ps1: the repository's VERSION file is the
# single source of truth; an explicit -Version overrides it for the CI release
# job, which numbers installers by run number.
if (-not $Version) {
    $VersionFile = Join-Path $RepoRoot "VERSION"
    if (-not (Test-Path $VersionFile)) {
        throw "VERSION file not found at $VersionFile. Pass -Version explicitly."
    }
    $Version = (Get-Content $VersionFile -Raw).trim()
    if ($Version -notmatch '^\d+\.\d+\.\d+') {
        throw "VERSION contains '$Version', which does not look like a release version (expected e.g. 1.4.0)"
    }
    Write-Host "Version from VERSION file: $Version" -ForegroundColor DarkGray
}
# VIProductVersion wants exactly four numeric components. Appending ".0" only
# works for a version that already has three: the CI release job passes a bare
# run number ("-Version 10"), which became "10.0" and makensis rejected it on
# every release build. Splitting and re-padding arithmetically handles any
# count, so "10" -> 10.0.0.0 and "1.4.0" -> 1.4.0.0.
$parts = $Version -split '\.'
# [int[]] is load-bearing: a single-element -split result is a scalar in
# PowerShell, not a one-element array, so .Count is 1 and `+= 0` reassigns the
# scalar instead of appending. The while loop below spins forever on a bare
# run number that way, which is the shape the release job passes.
$numeric = [int[]]@()
foreach ($p in $parts) {
    if ($p -notmatch '^\d+$') {
        throw "VERSION '$Version' cannot be rendered as a numeric file version: '$p' is not a number"
    }
    $numeric += [int]$p
}
while ($numeric.Count -lt 4) { $numeric += 0 }
$VersionNum = ($numeric[0..3] -join '.')

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
        # -X stamps the same compiled-in version build.ps1 gives the agent, so an
        # endpoint and its server report consistent version strings.
        $Ldflags = "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$Version"
        & go build -ldflags="$Ldflags" -o $ServerExe ./server/cmd/server
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
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum server.nsi
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
