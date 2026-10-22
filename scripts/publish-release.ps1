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

    # 3. Build every asset into dist\ with the same script the release workflow
    # runs, so a local release and a CI release carry the same files.
    Write-Host "[1/3] Building release assets..." -ForegroundColor Cyan
    $BuildArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $ScriptRoot "build-release.ps1"), "-Version", $CleanVer)
    if ($SkipFrontend) { $BuildArgs += "-SkipFrontend" }
    & powershell.exe @BuildArgs
    if ($LASTEXITCODE -ne 0) { throw "build-release.ps1 failed" }
    $DistDir = Join-Path $RepoRoot "dist"

    # 4. Push Git Tag. The tag also starts .github/workflows/release.yml, which
    # rebuilds the same assets, adds EndpointAgent-macOS.pkg (it needs a macOS
    # runner for pkgbuild) and re-uploads them with a checksums.txt covering it.
    Write-Host "[2/3] Pushing git tag $Tag to origin..." -ForegroundColor Cyan
    git tag -f "$Tag"
    git push origin "$Tag" --force

    # 5. Create/Upload Release Assets via gh CLI
    Write-Host "[3/3] Creating release on GitHub and uploading assets..." -ForegroundColor Cyan
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
