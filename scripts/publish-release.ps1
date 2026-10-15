# ==============================================================================
# Enterprise Endpoint Management - Publish GitHub Release Script
# ==============================================================================
param(
    [string]$Version = "",
    [switch]$Draft = $false,
    [switch]$SkipFrontend = $false
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot
$RepoRoot = Resolve-Path (Join-Path $ScriptRoot "..")

Push-Location $RepoRoot
try {
    # 1. Resolve Version
    if (-not $Version) {
        $VersionFile = Join-Path $RepoRoot "VERSION"
        if (Test-Path $VersionFile) {
            $Version = (Get-Content $VersionFile -Raw).Trim()
        } else {
            $Version = "1.0.0"
        }
    }
    $CleanVer = $Version.TrimStart('v')
    $Tag = "v$CleanVer"

    Write-Host "==========================================================" -ForegroundColor Cyan
    Write-Host "   Publishing GitHub Release: $Tag" -ForegroundColor Cyan
    Write-Host "==========================================================" -ForegroundColor Cyan

    # 2. Check gh CLI authentication
    $ghAuth = gh auth status 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Error "GitHub CLI (gh) is not authenticated. Please run 'gh auth login' first."
        return
    }

    $DistDir = Join-Path $RepoRoot "dist"
    if (Test-Path $DistDir) { Remove-Item -Recurse -Force $DistDir }
    New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

    # 2b. Rebuild the web console so the server binary embeds the current UI.
    # Skip with -SkipFrontend when the committed dist bundle is already current.
    if (-not $SkipFrontend -and (Test-Path (Join-Path $RepoRoot "web-console\package.json"))) {
        Write-Host "[0/5] Building web console (embeds into server binary)..." -ForegroundColor Cyan
        Push-Location (Join-Path $RepoRoot "web-console")
        try {
            npm ci
            npm run build
        }
        finally {
            Pop-Location
        }
    }

    # 3. Compile Linux Binaries
    Write-Host "[1/5] Compiling Linux server & agent binaries..." -ForegroundColor Cyan
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    go build -ldflags "-s -w" -o "$DistDir/server-linux-amd64" ./server/cmd/server
    go build -ldflags "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$CleanVer" -o "$DistDir/agent-linux-amd64" ./agent/cmd/agent
    tar -czf "$DistDir/endpoint-server-linux-amd64.tar.gz" -C "$DistDir" server-linux-amd64

    # 4. Compile Windows Binaries
    Write-Host "[2/5] Compiling Windows server & agent binaries..." -ForegroundColor Cyan
    $env:GOOS = "windows"
    go build -ldflags "-s -w" -o "$DistDir/server-windows-amd64.exe" ./server/cmd/server
    go build -ldflags "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$CleanVer" -o "$DistDir/agent-windows-amd64.exe" ./agent/cmd/agent

    # 5. Build NSIS Windows Installers & Uninstallers
    Write-Host "[3/5] Building Windows NSIS Setup Installers and Uninstallers..." -ForegroundColor Cyan
    Copy-Item "$DistDir/agent-windows-amd64.exe" "$RepoRoot/agent-windows-amd64.exe" -Force
    Copy-Item "$DistDir/server-windows-amd64.exe" "$RepoRoot/server-windows-amd64.exe" -Force
    & powershell.exe -ExecutionPolicy Bypass -File (Join-Path $RepoRoot "packaging\windows\build.ps1") -Version $CleanVer
    & powershell.exe -ExecutionPolicy Bypass -File (Join-Path $RepoRoot "packaging\windows\build-server.ps1") -Version $CleanVer

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
    if ($NsisPath) {
        $parts = $CleanVer -split '\.'
        $numeric = [int[]]@()
        foreach ($p in $parts) {
            if ($p -match '^\d+$') { $numeric += [int]$p }
        }
        while ($numeric.Count -lt 4) { $numeric += 0 }
        $VersionNum = ($numeric[0..3] -join '.')

        Push-Location (Join-Path $RepoRoot "packaging\windows")
        try {
            Write-Host "      Building EndpointAgent-Uninstall.exe..." -ForegroundColor DarkGray
            & $NsisPath /DVERSION=$CleanVer /DVERSION_NUM=$VersionNum agent-uninstall.nsi
            Write-Host "      Building EndpointServer-Uninstall.exe..." -ForegroundColor DarkGray
            & $NsisPath /DVERSION=$CleanVer /DVERSION_NUM=$VersionNum server-uninstall.nsi
        } finally {
            Pop-Location
        }
    }

    $SetupExe = Join-Path $RepoRoot "packaging\windows\EndpointAgent-Setup.exe"
    if (Test-Path $SetupExe) {
        Copy-Item $SetupExe "$DistDir/EndpointAgent-Setup.exe" -Force
    }
    $AgentUninstExe = Join-Path $RepoRoot "packaging\windows\EndpointAgent-Uninstall.exe"
    if (Test-Path $AgentUninstExe) {
        Copy-Item $AgentUninstExe "$DistDir/EndpointAgent-Uninstall.exe" -Force
    }
    $ServerSetupExe = Join-Path $RepoRoot "packaging\windows\EndpointServer-Setup.exe"
    if (Test-Path $ServerSetupExe) {
        Copy-Item $ServerSetupExe "$DistDir/EndpointServer-Setup.exe" -Force
    }
    $ServerUninstExe = Join-Path $RepoRoot "packaging\windows\EndpointServer-Uninstall.exe"
    if (Test-Path $ServerUninstExe) {
        Copy-Item $ServerUninstExe "$DistDir/EndpointServer-Uninstall.exe" -Force
    }

    # 5b. Publish SHA256 checksums alongside the binaries so an operator can
    # verify an asset before letting it replace a production server binary.
    Write-Host "[3b/5] Generating checksums..." -ForegroundColor Cyan
    $Hashes = Get-ChildItem "$DistDir" -File | ForEach-Object {
        $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
        "$h  $($_.Name)"
    }
    $Hashes | Set-Content -Encoding ASCII (Join-Path $DistDir "checksums.txt")

    # 6. Push Git Tag
    Write-Host "[4/5] Pushing git tag $Tag to origin..." -ForegroundColor Cyan
    git tag -f "$Tag"
    git push origin "$Tag" --force

    # 7. Create/Upload Release Assets via gh CLI
    Write-Host "[5/5] Creating release on GitHub and uploading assets..." -ForegroundColor Cyan
    $Assets = Get-ChildItem "$DistDir" -File | Select-Object -ExpandProperty FullName

    $DraftFlag = if ($Draft) { @("--draft") } else { @() }

    # Check if release exists
    $ReleaseExists = $false
    try {
        & gh release view "$Tag" >$null 2>$null
        if ($LASTEXITCODE -eq 0) { $ReleaseExists = $true }
    } catch {}

    $Notes = "Release " + $Tag + ": Cross-platform server, agent, and setup installers."
    if (-not $ReleaseExists) {
        Write-Host "Creating new GitHub release for $Tag..." -ForegroundColor Cyan
        & gh release create "$Tag" $Assets --title "Enterprise Endpoint Management $Tag" --notes $Notes @DraftFlag
    } else {
        Write-Host "Release $Tag already exists, uploading/clobbering assets..." -ForegroundColor Yellow
        & gh release upload "$Tag" $Assets --clobber
    }

    Write-Host "==========================================================" -ForegroundColor Green
    Write-Host "   [+] Successfully published $Tag to GitHub Releases!" -ForegroundColor Green
    Write-Host "   URL: https://github.com/henokakunemail-stack/UEM/releases/tag/$Tag" -ForegroundColor Green
    Write-Host "==========================================================" -ForegroundColor Green
}
finally {
    Pop-Location
}
