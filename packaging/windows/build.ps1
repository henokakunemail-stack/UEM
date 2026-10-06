# ==============================================================================
# Build script for Windows Agent NSIS Installer
# ==============================================================================
# ==============================================================================
# $Version defaults to the repository's VERSION file, which is the single source
# of truth. It is still overridable from the command line, because the release
# job numbers installers by CI run number rather than by the file.
# ==============================================================================
param(
    [string]$Version = "",
    [switch]$SkipCompile = $false
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot
$RepoRoot = Resolve-Path (Join-Path $ScriptRoot "..\..")

if (-not $Version) {
    $VersionFile = Join-Path $RepoRoot "VERSION"
    if (-not (Test-Path $VersionFile)) {
        throw "VERSION file not found at $VersionFile. Pass -Version explicitly, or restore the file."
    }
    # trimEnd: a file written with a trailing newline otherwise produces a version
    # string with an embedded CR/LF, which NSIS writes into the registry verbatim.
    $Version = (Get-Content $VersionFile -Raw).trim()
    if ($Version -notmatch '^\d+\.\d+\.\d+') {
        throw "VERSION contains '$Version', which does not look like a release version (expected e.g. 1.4.0)"
    }
    Write-Host "Version from VERSION file: $Version" -ForegroundColor DarkGray
}
# VIProductVersion wants four numeric components; "1.0.0" alone is rejected by
# makensis, so pad it. A prerelease suffix like "1.0.0-rc1" is not valid for
# the field and is caught by the same numeric check the release path applies.
$VersionNum = "$Version.0"

Push-Location $RepoRoot
try {
    $AgentExe = Join-Path $RepoRoot "agent-windows-amd64.exe"

    if (-not $SkipCompile -or -not (Test-Path $AgentExe)) {
        Write-Host "[1/2] Compiling Windows agent binary (CGO_ENABLED=0, GOOS=windows, GOARCH=amd64)..." -ForegroundColor Cyan
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        # -X stamps the version the agent reports in its inventory, which is how an
        # operator tells a 1.4.0 endpoint from a 1.3.0 one. It was declared and
        # documented in osinfo.go but no build script ever passed it, so every
        # binary reported the compiled-in 0.1.0 default.
        $Ldflags = "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$Version"
        & go build -ldflags="$Ldflags" -o $AgentExe ./agent/cmd/agent
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE"
        }
        Write-Host "      Agent binary compiled: $AgentExe" -ForegroundColor Green
    } else {
        Write-Host "[1/2] Reusing existing agent binary: $AgentExe" -ForegroundColor Yellow
    }

    Write-Host "[2/2] Compiling NSIS Installer..." -ForegroundColor Cyan
    # Windows PowerShell 5.1 is what ships with the OS and has no null-
    # conditional operator, so the lookup is written the long way.
    $Cmd = Get-Command makensis.exe -ErrorAction SilentlyContinue
    $NsisPath = if ($Cmd) { $Cmd.Source } else { $null }
    if (-not $NsisPath) {
        # Check standard default installation paths
        $Candidates = @(
            "$env:ProgramFiles\NSIS\makensis.exe",
            "${env:ProgramFiles(x86)}\NSIS\makensis.exe"
        )
        foreach ($c in $Candidates) {
            if (Test-Path $c) {
                $NsisPath = $c
                break
            }
        }
    }

    if (-not $NsisPath) {
        Write-Host "[-] makensis.exe not found on PATH or default directories." -ForegroundColor Yellow
        Write-Host "    To install NSIS on Windows, run:" -ForegroundColor Yellow
        Write-Host "      winget install NSIS.NSIS" -ForegroundColor White
        Write-Host "    Then re-run this script." -ForegroundColor Yellow
        exit 1
    }

    Push-Location $ScriptRoot
    try {
        & $NsisPath /DVERSION=$Version /DVERSION_NUM=$VersionNum agent.nsi
        if ($LASTEXITCODE -eq 0) {
            $OutExe = Join-Path $ScriptRoot "EndpointAgent-Setup.exe"
            Write-Host "[+] Installer built successfully: $OutExe" -ForegroundColor Green
        } else {
            throw "makensis failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
} finally {
    Pop-Location
}
