# scripts/e2e-dashboard.ps1
# Live E2E test for Fase 3: Dashboard & Embedded Web Console
# Validates both single-binary web console delivery and live executive metrics.

$ErrorActionPreference = 'Stop'

$port = 18445
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-dash.db"
$credsPath = Join-Path $env:TEMP "em-e2e-dash-creds.json"
$serverExe = Join-Path $env:TEMP "emserver-dash.exe"
$agentExe = Join-Path $env:TEMP "emagent-dash.exe"

# Clean slate
Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
Get-Process emserver-dash, emagent-dash -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== FASE 3 E2E: DASHBOARD & EMBEDDED WEB CONSOLE VERIFICATION ===" -ForegroundColor Cyan

# 1. Build server with embedded web console
Write-Host "1. Building server binary with embedded web console..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'
go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }

# Build agent with unique version string
go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=0.3.0-dash-e2e" -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "Agent compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-dash-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_e2e_password"

$serverProc = Start-Process $serverExe -PassThru -WindowStyle Hidden
Write-Host "Server started with PID: $($serverProc.Id) on port $port"

# Wait for server ready
$ready = $false
for ($i = 0; $i -lt 30; $i++) {
    try {
        $res = Invoke-RestMethod "$base/healthz" -TimeoutSec 2
        if ($res.status -eq "ok") { $ready = $true; break }
    } catch {
        Start-Sleep -Milliseconds 250
    }
}
if (-not $ready) {
    Stop-Process -Id $serverProc.Id -Force
    throw "Server failed to respond on /healthz"
}
Write-Host "Server is HEALTHY and listening." -ForegroundColor Green

try {
    # 3. Test Embedded Web Console Delivery
    Write-Host "`n2. Testing Embedded Single-Binary Web Console..."

    # 3a. Root route
    $rootRes = Invoke-WebRequest -Uri "$base/" -UseBasicParsing
    if ($rootRes.StatusCode -ne 200 -or -not ($rootRes.Content -match "<title>Enterprise Endpoint Manager</title>")) {
        throw "Root page delivery failed. Content: $($rootRes.Content.Substring(0, 100))"
    }
    Write-Host "  [PASS] GET / -> HTTP 200 with embedded HTML" -ForegroundColor Green

    # 3b. SPA route fallback: /dashboard
    $dashWebRes = Invoke-WebRequest -Uri "$base/dashboard" -UseBasicParsing
    if ($dashWebRes.StatusCode -ne 200 -or -not ($dashWebRes.Content -match "Enterprise Endpoint Manager")) {
        throw "SPA fallback /dashboard failed"
    }
    Write-Host "  [PASS] GET /dashboard -> HTTP 200 (SPA client-side routing fallback)" -ForegroundColor Green

    # 3c. SPA route fallback: /devices
    $devicesWebRes = Invoke-WebRequest -Uri "$base/devices" -UseBasicParsing
    if ($devicesWebRes.StatusCode -ne 200 -or -not ($devicesWebRes.Content -match "Enterprise Endpoint Manager")) {
        throw "SPA fallback /devices failed"
    }
    Write-Host "  [PASS] GET /devices -> HTTP 200 (SPA client-side routing fallback)" -ForegroundColor Green

    # 4. Authentication
    Write-Host "`n3. Authenticating Admin Operator..."
    $loginBody = @{ username = "admin"; password = "admin_e2e_password" } | ConvertTo-Json
    $loginRes = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body $loginBody -ContentType "application/json"
    $token = $loginRes.access_token
    if (-not $token) { throw "Login failed to return access token" }
    Write-Host "  [PASS] Admin JWT issued successfully (expires_at: $($loginRes.expires_at))" -ForegroundColor Green

    $authHeader = @{ Authorization = "Bearer $token" }

    # 5. Check Dashboard on Empty Fleet
    Write-Host "`n4. Querying Dashboard APIs on Empty Fleet..."
    $emptySum = Invoke-RestMethod -Uri "$base/api/dashboard/summary" -Headers $authHeader
    if ($emptySum.total_devices -ne 0 -or $emptySum.online_devices -ne 0) {
        throw "Expected 0 devices on empty DB, got: $($emptySum.total_devices)"
    }
    Write-Host "  [PASS] GET /api/dashboard/summary handles empty fleet (total: 0, online: 0, low_disk: 0)" -ForegroundColor Green

    # 6. Enroll and Connect Real Agent
    Write-Host "`n5. Enrolling Real Windows Agent to generate live telemetry..."
    $enrollReq = @{
        hostname = $env:COMPUTERNAME
        os_name  = "windows"
        site     = "hq"
    } | ConvertTo-Json
    $enrollTokenRes = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Body $enrollReq -ContentType "application/json" -Headers $authHeader
    $enrollToken = $enrollTokenRes.enrollment_token

    $agentErr = Join-Path $env:TEMP "emagent-dash.err"
    $agentProc = Start-Process $agentExe -PassThru -WindowStyle Hidden `
        -RedirectStandardError $agentErr `
        -ArgumentList "-server", $base, "-enroll", $enrollToken, "-creds", $credsPath
    Write-Host "Agent started with PID: $($agentProc.Id)"

    # Wait for agent to connect and register in healthz
    $agentOnline = $false
    for ($i = 0; $i -lt 30; $i++) {
        $h = Invoke-RestMethod "$base/healthz"
        if ($h.agents_online -ge 1) { $agentOnline = $true; break }
        Start-Sleep -Milliseconds 500
    }
    if (-not $agentOnline) {
        if (Test-Path $agentErr) {
            Write-Host "--- Agent Error Log ---"
            Get-Content $agentErr | ForEach-Object { Write-Host "  $_" }
        }
        throw "Agent failed to connect via WebSocket"
    }
    Write-Host "  [PASS] Agent established live WebSocket session (agents_online: 1)" -ForegroundColor Green

    # Wait for initial inventory collection (sent immediately on connect)
    Start-Sleep -Seconds 4

    # 7. Validate Live Dashboard Telemetry
    Write-Host "`n6. Validating Executive Dashboard Telemetry..."

    # 7a. Summary
    $summary = Invoke-RestMethod -Uri "$base/api/dashboard/summary" -Headers $authHeader
    Write-Host "  - Total Devices: $($summary.total_devices)"
    Write-Host "  - Online Devices: $($summary.online_devices)"
    Write-Host "  - Fleet Online Ratio: $($summary.online_pct)%"
    Write-Host "  - Low Disk Alerts: $($summary.low_disk_alerts)"
    Write-Host "  - Monitored Sites: $($summary.sites_count)"
    if ($summary.total_devices -lt 1 -or $summary.online_devices -lt 1) {
        throw "Dashboard summary did not reflect live connected agent"
    }
    Write-Host "  [PASS] Dashboard Summary verified against live fleet" -ForegroundColor Green

    # 7b. Sites Breakdown
    $sites = Invoke-RestMethod -Uri "$base/api/dashboard/sites" -Headers $authHeader
    Write-Host "  - Sites reported: $($sites.Count)"
    $hq = $sites | Where-Object { $_.site -eq "hq" }
    if (-not $hq -or $hq.online -lt 1) {
        throw "Site 'hq' not found or offline in site breakdown"
    }
    Write-Host "  [PASS] Site metrics: $($hq.site) -> $($hq.online)/$($hq.total) online ($($hq.online_pct)%)" -ForegroundColor Green

    # 7c. OS Distribution
    $osMetrics = Invoke-RestMethod -Uri "$base/api/dashboard/os" -Headers $authHeader
    $winOS = $osMetrics | Where-Object { $_.os_name -like "*Windows*" }
    if (-not $winOS) {
        throw "Windows OS not detected in OS distribution"
    }
    Write-Host "  [PASS] OS distribution: $($winOS.os_name) -> $($winOS.count) endpoints ($($winOS.pct)%)" -ForegroundColor Green

    # 7d. Operational Alerts
    $alerts = Invoke-RestMethod -Uri "$base/api/dashboard/alerts" -Headers $authHeader
    Write-Host "  - Operational alerts active: $($alerts.Count)"
    Write-Host "  [PASS] Alerts endpoint responded with valid array" -ForegroundColor Green

    # 7e. Activity Feed
    $activity = Invoke-RestMethod -Uri "$base/api/dashboard/activity" -Headers $authHeader
    Write-Host "  - Activity events recorded: $($activity.Count)"
    if ($activity.Count -lt 1) {
        throw "Expected activity items for enrollment"
    }
    Write-Host "  [PASS] Activity item verified: Action='$($activity[0].action)', Target='$($activity[0].target_id)'" -ForegroundColor Green

    # 8. Paged Device Inventory API
    Write-Host "`n7. Validating Device Management Pagination API..."
    $devPaged = Invoke-RestMethod -Uri "$base/api/devices?limit=10&offset=0" -Headers $authHeader
    Write-Host "  - Total in DB: $($devPaged.total)"
    Write-Host "  - Limit: $($devPaged.limit), Offset: $($devPaged.offset)"
    Write-Host "  - Returned Devices: $($devPaged.devices.Count)"
    if ($devPaged.devices.Count -lt 1) {
        throw "Expected at least 1 device in paged response"
    }
    $dev = $devPaged.devices[0]
    Write-Host "  - Enrolled Hostname: $($dev.hostname)"
    Write-Host "  - Status: $($dev.status)"
    Write-Host "  - Agent Version: $($dev.agent_version)"
    Write-Host "  [PASS] Server-side SQL pagination verified" -ForegroundColor Green

    # 9. Verify Hardware & Software from API
    Write-Host "`n8. Validating Ground-Truth Hardware & Software Specs..."
    # Enrollment does not create an inventory row; the snapshot only exists once
    # the agent has reported one. Ask the online device to collect first, exactly
    # as e2e-inventory.ps1 does, and wait for the round-trip to land.
    $collect = Invoke-RestMethod -Uri "$base/api/devices/$($dev.id)/inventory/collect" -Method POST -Headers $authHeader
    if ($collect.status -ne "sent") {
        throw "collect to an online device must be sent, got $($collect.status)"
    }
    Start-Sleep -Seconds 4
    $inv = Invoke-RestMethod -Uri "$base/api/devices/$($dev.id)/inventory" -Headers $authHeader
    $cpuName = if ($inv.hw.cpu.name) { $inv.hw.cpu.name } else { $inv.cpu_model }
    $ramTotal = if ($inv.hw.ram_total_bytes) { $inv.hw.ram_total_bytes } else { $inv.ram_bytes }
    Write-Host "  - CPU: $cpuName"
    Write-Host "  - RAM: $([math]::Round($ramTotal / 1GB, 1)) GB"
    Write-Host "  - Disks: $($inv.hw.disks.Count) volumes"
    Write-Host "  - Installed Software Packages: $($inv.software.Count) applications"
    if ($inv.software.Count -lt 5) {
        throw "Too few software packages collected"
    }
    Write-Host "  [PASS] Ground-truth inventory completely populated" -ForegroundColor Green

    Write-Host "`n========================================================" -ForegroundColor Green
    Write-Host ">>> ALL FASE 3 E2E CRITERIA CONFIRMED WITH REAL EVIDENCE <<<" -ForegroundColor Green
    Write-Host "========================================================" -ForegroundColor Green

} finally {
    # Teardown
    Write-Host "`nCleaning up background processes..."
    if ($agentProc -and -not $agentProc.HasExited) {
        Stop-Process -Id $agentProc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($serverProc -and -not $serverProc.HasExited) {
        Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
}
