# ==============================================================================
# Enterprise Endpoint Management - Build Release Assets
# ==============================================================================
# Builds every release asset into dist\:
#
#   server-<os>-<arch>[.exe], agent-<os>-<arch>[.exe]
#       windows/amd64, linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
#   EndpointAgent-Setup.exe, EndpointAgent-Uninstall.exe        (NSIS)
#   EndpointServer-Setup.exe, EndpointServer-Uninstall.exe      (NSIS)
#   endpoint-server-windows-amd64.zip                           (update-server.ps1)
#   endpoint-server-{linux,darwin}-{amd64,arm64}.tar.gz         (server + agents/)
#   endpoint-agent-{linux,darwin}-{amd64,arm64}.tar.gz          (agent + install.sh)
#   checksums.txt
#
# Every server package carries the agents for all five platforms in agents/:
# the server hands them to the one-line installer and registers them as
# releases from Agent Updates, so enrolled devices can be moved onto them.
#
# EndpointAgent-macOS.pkg needs pkgbuild, so it is built by the macOS job in
# .github/workflows/release.yml, which also regenerates checksums.txt.
#
# Used by scripts/publish-release.ps1 and by the release workflow, so a local
# release and a CI release contain the same files built the same way.
# ==============================================================================
param(
    [string]$Version = "",
    [switch]$SkipFrontend = $false,
    # NSIS is required for a release, but a developer checking the archives
    # does not need it installed.
    [switch]$SkipInstallers = $false
)

$ErrorActionPreference = "Stop"
$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")

if (-not $Version) {
    $Version = (Get-Content (Join-Path $RepoRoot "VERSION") -Raw).Trim()
}
$CleanVer = $Version.TrimStart('v')

Push-Location $RepoRoot
try {
    $DistDir = Join-Path $RepoRoot "dist"
    if (Test-Path $DistDir) { Remove-Item -Recurse -Force $DistDir }
    New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

    Write-Host "=== Building release assets for $CleanVer ===" -ForegroundColor Cyan

    # 1. The console is embedded into the server, so it is built first.
    if (-not $SkipFrontend) {
        Write-Host "[1/5] Building web console (embeds into server binary)..." -ForegroundColor Cyan
        Push-Location (Join-Path $RepoRoot "web-console")
        try {
            & npm ci
            if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
            & npm run build
            if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
        } finally {
            Pop-Location
        }
    } else {
        Write-Host "[1/5] Skipping web console build (using committed server/cmd/server/dist)" -ForegroundColor Yellow
    }

    # 2. Go binaries.
    Write-Host "[2/5] Compiling servers and agents for 5 platforms..." -ForegroundColor Cyan
    $Targets = @(
        @{ GOOS = "windows"; GOARCH = "amd64"; Ext = ".exe" },
        @{ GOOS = "linux"; GOARCH = "amd64"; Ext = "" },
        @{ GOOS = "linux"; GOARCH = "arm64"; Ext = "" },
        @{ GOOS = "darwin"; GOARCH = "amd64"; Ext = "" },
        @{ GOOS = "darwin"; GOARCH = "arm64"; Ext = "" }
    )
    $ServerLd = "-s -w -X main.serverVersion=$CleanVer"
    $AgentLd = "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$CleanVer"
    $env:CGO_ENABLED = "0"
    foreach ($t in $Targets) {
        $env:GOOS = $t.GOOS
        $env:GOARCH = $t.GOARCH
        $suffix = "$($t.GOOS)-$($t.GOARCH)$($t.Ext)"
        Write-Host "      server-$suffix, agent-$suffix" -ForegroundColor DarkGray
        & go build -ldflags $ServerLd -o "$DistDir/server-$suffix" ./server/cmd/server
        if ($LASTEXITCODE -ne 0) { throw "go build server-$suffix failed" }
        & go build -ldflags $AgentLd -o "$DistDir/agent-$suffix" ./agent/cmd/agent
        if ($LASTEXITCODE -ne 0) { throw "go build agent-$suffix failed" }
    }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue

    $AgentNames = @("agent-windows-amd64.exe", "agent-linux-amd64", "agent-linux-arm64", "agent-darwin-amd64", "agent-darwin-arm64")

    # 3. NSIS installers, built from the binaries above rather than a second
    # compile, so the installer carries the bytes the checksums describe.
    if (-not $SkipInstallers) {
        Write-Host "[3/5] Building Windows NSIS installers and uninstallers..." -ForegroundColor Cyan
        Copy-Item "$DistDir/agent-windows-amd64.exe" "$RepoRoot/agent-windows-amd64.exe" -Force
        Copy-Item "$DistDir/server-windows-amd64.exe" "$RepoRoot/server-windows-amd64.exe" -Force
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $RepoRoot "packaging\windows\build.ps1") -Version $CleanVer -SkipCompile
        if ($LASTEXITCODE -ne 0) { throw "packaging/windows/build.ps1 failed" }
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $RepoRoot "packaging\windows\build-server.ps1") -Version $CleanVer -SkipCompile -AgentDir $DistDir
        if ($LASTEXITCODE -ne 0) { throw "packaging/windows/build-server.ps1 failed" }

        $Cmd = Get-Command makensis.exe -ErrorAction SilentlyContinue
        $NsisPath = if ($Cmd) { $Cmd.Source } else { $null }
        if (-not $NsisPath) {
            foreach ($c in @("$env:ProgramFiles\NSIS\makensis.exe", "${env:ProgramFiles(x86)}\NSIS\makensis.exe")) {
                if (Test-Path $c) { $NsisPath = $c; break }
            }
        }
        if (-not $NsisPath) { throw "makensis.exe not found; install NSIS or pass -SkipInstallers" }

        # [int[]] is load-bearing: a one-element -split result is a scalar,
        # and += on it reassigns instead of appending.
        $numeric = [int[]]@()
        foreach ($p in ($CleanVer -split '\.')) {
            if ($p -match '^\d+$') { $numeric += [int]$p }
        }
        while ($numeric.Count -lt 4) { $numeric += 0 }
        $VersionNum = ($numeric[0..3] -join '.')

        Push-Location (Join-Path $RepoRoot "packaging\windows")
        try {
            & $NsisPath /DVERSION=$CleanVer /DVERSION_NUM=$VersionNum agent-uninstall.nsi
            if ($LASTEXITCODE -ne 0) { throw "makensis agent-uninstall.nsi failed" }
            & $NsisPath /DVERSION=$CleanVer /DVERSION_NUM=$VersionNum server-uninstall.nsi
            if ($LASTEXITCODE -ne 0) { throw "makensis server-uninstall.nsi failed" }
        } finally {
            Pop-Location
        }
        foreach ($n in @("EndpointAgent-Setup.exe", "EndpointAgent-Uninstall.exe", "EndpointServer-Setup.exe", "EndpointServer-Uninstall.exe")) {
            Copy-Item (Join-Path $RepoRoot "packaging\windows\$n") "$DistDir/$n" -Force
        }
    } else {
        Write-Host "[3/5] Skipping NSIS installers (-SkipInstallers)" -ForegroundColor Yellow
    }

    # 4. Archives. packdist records Unix modes; tar.exe on Windows records 0666
    # and no execute bit, so an unpacked install.sh would not run.
    Write-Host "[4/5] Packing server and agent archives..." -ForegroundColor Cyan
    function Invoke-Pack([string]$Out, [string[]]$Entries) {
        & go run ./scripts/packdist -o "$DistDir/$Out" @Entries
        if ($LASTEXITCODE -ne 0) { throw "packdist $Out failed" }
        Write-Host "      $Out" -ForegroundColor DarkGray
    }
    $AgentEntries = @()
    foreach ($a in $AgentNames) { $AgentEntries += "$DistDir/$a=agents/$($a):x" }

    Invoke-Pack "endpoint-server-windows-amd64.zip" (@("$DistDir/server-windows-amd64.exe=endpoint-server.exe:x") + $AgentEntries)
    foreach ($os in @("linux", "darwin")) {
        foreach ($arch in @("amd64", "arm64")) {
            $ServerEntries = @(
                "$DistDir/server-$os-$arch=endpoint-server:x",
                "packaging/$os/scripts/install-server.sh=install-server.sh:x",
                "packaging/$os/scripts/uninstall-server.sh=uninstall-server.sh:x"
            )
            if ($os -eq "linux") { $ServerEntries += "packaging/linux/scripts/update-server.sh=update-server.sh:x" }
            Invoke-Pack "endpoint-server-$os-$arch.tar.gz" ($ServerEntries + $AgentEntries)
            Invoke-Pack "endpoint-agent-$os-$arch.tar.gz" @(
                "$DistDir/agent-$os-$arch=endpoint-agent:x",
                "packaging/$os/scripts/install-agent.sh=install-agent.sh:x",
                "packaging/$os/scripts/uninstall-agent.sh=uninstall-agent.sh:x"
            )
        }
    }

    # 5. SHA256 checksums so operators can verify assets pre-deploy.
    Write-Host "[5/5] Generating checksums..." -ForegroundColor Cyan
    $Hashes = Get-ChildItem $DistDir -File | Where-Object { $_.Name -ne "checksums.txt" } | Sort-Object Name | ForEach-Object {
        $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
        "$h  $($_.Name)"
    }
    $Hashes | Set-Content -Encoding ASCII (Join-Path $DistDir "checksums.txt")

    Get-ChildItem $DistDir -File | Sort-Object Name | Format-Table Name, @{ N = "MB"; E = { [math]::Round($_.Length / 1MB, 1) } } -AutoSize | Out-String | Write-Host
}
finally {
    Pop-Location
}
