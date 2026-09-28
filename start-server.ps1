# Start the Enterprise Endpoint Manager server with the redesigned console embedded.
# Run from the repo root:  .\start-server.ps1
#
# JWT_SECRET must be supplied. If it is not already set, this script generates a
# strong 48-byte random secret for this process only (it is NOT written to disk, so
# a later restart without the same value will invalidate existing sessions).
# DB_PATH and HTTP_ADDR keep the values the previous instance was running with.

Set-Location $PSScriptRoot

if (-not $env:JWT_SECRET) {
    # RandomNumberGenerator::Fill only exists on .NET Core; this runs on Windows
    # PowerShell 5.1, so use the Create() instance API instead. A silent fallback
    # here would hand the server a guessable secret.
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $bytes = New-Object byte[] 48
        $rng.GetBytes($bytes)
    } finally {
        $rng.Dispose()
    }
    $env:JWT_SECRET = [Convert]::ToBase64String($bytes)
    Write-Host "JWT_SECRET: generated for this run (not persisted)" -ForegroundColor Yellow
} else {
    Write-Host "JWT_SECRET: using value from environment" -ForegroundColor DarkGray
}

if (-not $env:DB_PATH)   { $env:DB_PATH = 'data/endpoint-mgmt.db' }
if (-not $env:HTTP_ADDR) { $env:HTTP_ADDR = ':8443' }

Write-Host "DB_PATH:   $env:DB_PATH"
Write-Host "HTTP_ADDR: $env:HTTP_ADDR"
Write-Host "Listening. Press Ctrl+C to stop." -ForegroundColor Cyan

.\server.exe
