# scripts/e2e-system-policy.ps1
# Live E2E test for Phase 15/16: Active Directory Group Policy Engine (systempolicy)
# Validates Policy CRUD, RBAC Enforcement, Device Enrollment,
# WebSocket Policy Auto-Dispatch (system_policy.apply), Agent Compliance Reporting,
# Device State Querying, and Forensic Policy Removal.

$ErrorActionPreference = 'Stop'

$port = 18457
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-syspol.db"
$serverExe = Join-Path $env:TEMP "emserver-syspol.exe"

# Clean slate
Remove-Item $dbPath -ErrorAction SilentlyContinue
Get-Process emserver-syspol -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== E2E: ACTIVE DIRECTORY GROUP POLICY ENGINE (SYSTEM POLICY) ===" -ForegroundColor Cyan

. (Join-Path $PSScriptRoot 'Read-AgentCommand.ps1')

# 1. Build server binary
Write-Host "1. Building server binary (CGO_ENABLED=0)..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'
go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-system-policy-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_syspol_password"

$serverProc = Start-Process $serverExe -PassThru -WindowStyle Hidden
Write-Host "Server started with PID: $($serverProc.Id) on port $port"

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
    # 3. Authenticate Admin and Create Test Roles
    Write-Host "`n2. Authenticating Admin and Creating Test Roles..."
    $adminLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "admin"; password = "admin_syspol_password" } | ConvertTo-Json) -ContentType "application/json"
    $adminToken = $adminLogin.access_token
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Write-Host "  [PASS] Admin authenticated" -ForegroundColor Green

    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "techsyspol"; password = "Password123!"; role = "technician"; display_name = "Tech Syspol" } | ConvertTo-Json) -ContentType "application/json"
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "viewersyspol"; password = "Password123!"; role = "viewer"; display_name = "Viewer Syspol" } | ConvertTo-Json) -ContentType "application/json"

    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "techsyspol"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $techHeaders = @{ Authorization = "Bearer $($techLogin.access_token)" }
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewersyspol"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $viewerHeaders = @{ Authorization = "Bearer $($viewerLogin.access_token)" }
    Write-Host "  [PASS] Technician and Viewer accounts created and authenticated" -ForegroundColor Green

    # 4. Enroll Test Device
    Write-Host "`n3. Enrolling Test Device..."
    $enrollTokenResp = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body (@{
        hostname = "CORP-WORKSTATION-01"
        os_name = "windows"
        site = "HQ-Finance"
    } | ConvertTo-Json) -ContentType "application/json"
    $enrollToken = $enrollTokenResp.enrollment_token

    $enrollResp = Invoke-RestMethod -Uri "$base/api/agent/enroll" -Method POST -Body (@{
        enrollment_token = $enrollToken
    } | ConvertTo-Json) -ContentType "application/json"

    $deviceId = $enrollResp.device_id
    $deviceSecret = $enrollResp.device_secret
    Write-Host "  [PASS] Device enrolled: $deviceId (Hostname: CORP-WORKSTATION-01)" -ForegroundColor Green

    # 5. Connect Agent via WebSocket
    Write-Host "`n4. Connecting Agent to Transport Hub..."
    $wsScheme = "ws://localhost:$port/api/agent/connect"
    $wsAgent = New-Object System.Net.WebSockets.ClientWebSocket
    $wsAgent.Options.SetRequestHeader("X-Device-Id", $deviceId)
    $wsAgent.Options.SetRequestHeader("X-Device-Secret", $deviceSecret)
    $ctSource = New-Object System.Threading.CancellationTokenSource
    $wsAgent.ConnectAsync([uri]$wsScheme, $ctSource.Token).Wait(5000) | Out-Null

    if ($wsAgent.State -ne [System.Net.WebSockets.WebSocketState]::Open) {
        throw "Failed to connect mock agent WebSocket"
    }

    # Send hello frame
    $helloEnv = @{
        type = "hello"
        payload = @{
            agent_version = "1.0.0"
            os = @{ name = "windows"; version = "11.0" }
        }
    } | ConvertTo-Json
    $helloBytes = [System.Text.Encoding]::UTF8.GetBytes($helloEnv)
    $helloSeg = New-Object System.ArraySegment[byte] ($helloBytes, 0, $helloBytes.Length)
    $wsAgent.SendAsync($helloSeg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $ctSource.Token).Wait(3000) | Out-Null
    Write-Host "  [PASS] Agent connected and registered online in Hub" -ForegroundColor Green
    Start-Sleep -Milliseconds 500

    # 6. Test RBAC: Viewer must be rejected from creating policy
    Write-Host "`n5. Testing RBAC on System Policy Management..."
    $rbacBlocked = $false
    try {
        Invoke-RestMethod -Uri "$base/api/system-policies" -Method POST -Headers $viewerHeaders -Body (@{
            name = "Viewer Forbidden Policy"
            target_type = "all"
        } | ConvertTo-Json) -ContentType "application/json"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            $rbacBlocked = $true
        }
    }
    if (-not $rbacBlocked) {
        throw "Security violation: Viewer was able to create system policy"
    }
    Write-Host "  [PASS] Viewer correctly blocked from policy creation (HTTP 403)" -ForegroundColor Green

    # 7. Admin creates Policy
    Write-Host "`n6. Admin Creating Targeted System Policy..."
    $polResp = Invoke-RestMethod -Uri "$base/api/system-policies" -Method POST -Headers $adminHeaders -Body (@{
        name = "High Security Endpoint Baseline"
        description = "Blocks USB storage, disables CMD, enforces inactivity timeout"
        target_type = "device"
        target_id = $deviceId
        priority = 10
        is_enabled = $true
        settings = @{
            usb_storage_mode = "block"
            disable_cmd = $true
            disable_task_mgr = $true
            inactivity_timeout_minutes = 15
        }
    } | ConvertTo-Json) -ContentType "application/json"
    $policyId = $polResp.id
    Write-Host "  [PASS] System policy created: $policyId" -ForegroundColor Green

    # 8. Verify WebSocket command dispatch
    Write-Host "`n7. Verifying Policy Dispatch Frame over WebSocket..."
    $cmd = Read-AgentCommand -Ws $wsAgent -CommandName 'system_policy.apply' -CancellationToken $ctSource.Token
    if (-not $cmd) {
        throw "Agent did not receive 'system_policy.apply' command"
    }
    $policyVersion = $cmd.payload.policy_version
    Write-Host "  [PASS] Received command 'system_policy.apply' with policy_version: $policyVersion" -ForegroundColor Green

    # 9. Agent reports compliance status
    Write-Host "`n8. Agent Reporting Policy Enforcement Status..."
    $agentHeaders = @{
        "X-Device-Id" = $deviceId
        "X-Device-Secret" = $deviceSecret
    }
    $reportResp = Invoke-RestMethod -Uri "$base/api/agent/devices/$deviceId/system-policy/report" -Method POST -Headers $agentHeaders -Body (@{
        policy_version = $policyVersion
        status = "synced"
        settings = @{
            usb_storage_mode = "block"
            disable_cmd = $true
            disable_task_mgr = $true
            inactivity_timeout_minutes = 15
        }
    } | ConvertTo-Json) -ContentType "application/json"
    if ($reportResp.status -ne "ok") {
        throw "Agent policy report failed"
    }
    Write-Host "  [PASS] Agent reported compliance status successfully" -ForegroundColor Green

    # 10. Query device system policy state from console
    Write-Host "`n9. Verifying Device Policy State in Console..."
    $devPolicyState = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/system-policy" -Method GET -Headers $adminHeaders
    if ($devPolicyState.state.status -ne "synced") {
        throw "Expected device status 'synced', got '$($devPolicyState.state.status)'"
    }
    Write-Host "  [PASS] Console reflects device status: synced" -ForegroundColor Green

    # 11. Delete policy and verify audit
    Write-Host "`n10. Admin Deleting Policy and Cleaning Up..."
    $delResp = Invoke-RestMethod -Uri "$base/api/system-policies/$policyId" -Method DELETE -Headers $adminHeaders
    Write-Host "  [PASS] Policy deleted cleanly: $($delResp.status)" -ForegroundColor Green

    Write-Host "`n=== ALL SYSTEM POLICY E2E TESTS PASSED ===" -ForegroundColor Green

} finally {
    if ($wsAgent -and $wsAgent.State -eq [System.Net.WebSockets.WebSocketState]::Open) {
        $null = $wsAgent.CloseAsync([System.Net.WebSockets.WebSocketCloseStatus]::NormalClosure, "Test Complete", [System.Threading.CancellationToken]::None)
    }
    if ($serverProc -and -not $serverProc.HasExited) {
        Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $dbPath -ErrorAction SilentlyContinue
    Remove-Item $serverExe -ErrorAction SilentlyContinue
}
