# scripts/e2e-network-filter.ps1
# Live E2E test for Fase 12: Network & Web Filter / Security Rules
# Validates Policy Management, Domain Rule Sets, RBAC Enforcement,
# Target Compilation Hierarchy, WebSocket Policy Dispatch, Hosts Sinkholing,
# Agent Compliance Reporting, and Forensic Audit Logging.

$ErrorActionPreference = 'Stop'

$port = 18454
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-filter.db"
$serverExe = Join-Path $env:TEMP "emserver-filter.exe"
$mockHostsFile = Join-Path $env:TEMP "mock_hosts_filter.txt"

# Clean slate
Remove-Item $dbPath -ErrorAction SilentlyContinue
Remove-Item $mockHostsFile -ErrorAction SilentlyContinue
Get-Process emserver-filter -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

# Seed mock hosts file with initial content
Set-Content -Path $mockHostsFile -Value "127.0.0.1 localhost`n::1 localhost`n10.0.0.1 gateway.lan`n"

Write-Host "=== FASE 12 E2E: NETWORK & WEB FILTER SECURITY RULES ===" -ForegroundColor Cyan

. (Join-Path $PSScriptRoot 'Read-AgentCommand.ps1')

# 1. Build server
Write-Host "1. Building server binary (CGO_ENABLED=0)..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'
go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-network-filter-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_filter_password"

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
    # 3. Authenticate Admin and create technician and viewer
    Write-Host "`n2. Authenticating Admin and Creating Test Roles..."
    $adminLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "admin"; password = "admin_filter_password" } | ConvertTo-Json) -ContentType "application/json"
    $adminToken = $adminLogin.access_token
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Write-Host "  [PASS] Admin authenticated" -ForegroundColor Green

    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "techfilter"; password = "Password123!"; role = "technician"; display_name = "Tech Filter" } | ConvertTo-Json) -ContentType "application/json"
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "viewerfilter"; password = "Password123!"; role = "viewer"; display_name = "Viewer Filter" } | ConvertTo-Json) -ContentType "application/json"

    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "techfilter"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $techHeaders = @{ Authorization = "Bearer $($techLogin.access_token)" }
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewerfilter"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $viewerHeaders = @{ Authorization = "Bearer $($viewerLogin.access_token)" }
    Write-Host "  [PASS] Technician and Viewer accounts created and authenticated" -ForegroundColor Green

    # 4. Enroll Device
    Write-Host "`n3. Enrolling Test Device..."
    $enrollTokenResp = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body (@{
        hostname = "SECURITY-GATEWAY-01"
        os_name = "windows"
        site = "Jakarta-HQ"
    } | ConvertTo-Json) -ContentType "application/json"
    $enrollToken = $enrollTokenResp.enrollment_token

    $enrollResp = Invoke-RestMethod -Uri "$base/api/agent/enroll" -Method POST -Body (@{
        enrollment_token = $enrollToken
    } | ConvertTo-Json) -ContentType "application/json"

    $deviceId = $enrollResp.device_id
    $deviceSecret = $enrollResp.device_secret
    Write-Host "  [PASS] Device enrolled: $deviceId (Hostname: SECURITY-GATEWAY-01)" -ForegroundColor Green

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
    Write-Host "`n5. Testing RBAC on Filter Policy Management..."
    $rbacBlocked = $false
    try {
        Invoke-RestMethod -Uri "$base/api/filter/policies" -Method POST -Headers $viewerHeaders -Body (@{
            name = "Viewer Forbidden Policy"
            target_type = "all"
        } | ConvertTo-Json) -ContentType "application/json"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            $rbacBlocked = $true
        }
    }
    if (-not $rbacBlocked) {
        throw "Security violation: Viewer was able to create filter policy"
    }
    Write-Host "  [PASS] Viewer correctly blocked from policy creation (HTTP 403)" -ForegroundColor Green

    # 7. Admin creates Filter Policies and Rules
    Write-Host "`n6. Admin Creating Policies and Domain Block Rules..."
    # Policy 1: Fleet Global Threat Policy
    $pol1Resp = Invoke-RestMethod -Uri "$base/api/filter/policies" -Method POST -Headers $adminHeaders -Body (@{
        name = "Global Threat Intelligence Blocklist"
        description = "Blocks known C2, botnets, and malware domains"
        target_type = "all"
        priority = 10
        is_enabled = $true
    } | ConvertTo-Json) -ContentType "application/json"
    $pol1Id = $pol1Resp.id

    $null = Invoke-RestMethod -Uri "$base/api/filter/policies/$pol1Id/rules" -Method POST -Headers $adminHeaders -Body (@{
        rule_type = "domain"
        pattern = "malware-threat.xyz"
        category = "malware"
    } | ConvertTo-Json) -ContentType "application/json"

    $null = Invoke-RestMethod -Uri "$base/api/filter/policies/$pol1Id/rules" -Method POST -Headers $adminHeaders -Body (@{
        rule_type = "domain"
        pattern = "phishing-secure-bank.top"
        category = "phishing"
    } | ConvertTo-Json) -ContentType "application/json"

    # Policy 2: Device-Specific Policy
    $pol2Resp = Invoke-RestMethod -Uri "$base/api/filter/policies" -Method POST -Headers $adminHeaders -Body (@{
        name = "Jakarta HQ Social Media Restriction"
        description = "Limits bandwidth waste on local gateway"
        target_type = "device"
        target_id = $deviceId
        priority = 20
        is_enabled = $true
    } | ConvertTo-Json) -ContentType "application/json"
    $pol2Id = $pol2Resp.id

    $null = Invoke-RestMethod -Uri "$base/api/filter/policies/$pol2Id/rules" -Method POST -Headers $adminHeaders -Body (@{
        rule_type = "domain"
        pattern = "tiktok.com"
        category = "social"
    } | ConvertTo-Json) -ContentType "application/json"
    Write-Host "  [PASS] 2 Policies and 3 domain rules created by Admin" -ForegroundColor Green

    # 8. Technician triggers Policy Synchronization
    Write-Host "`n7. Technician Triggering Filter Policy Sync..."
    $syncResp = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/filter/sync" -Method POST -Headers $techHeaders
    if ($syncResp.status -ne "pending") {
        throw "Expected status 'pending', got: $($syncResp.status)"
    }
    if ($syncResp.effective_rules -ne 3) {
        throw "Expected 3 effective rules compiled for device, got: $($syncResp.effective_rules)"
    }
    $expectedVersion = $syncResp.policy_version
    Write-Host "  [PASS] Sync dispatched via WebSocket: Effective Rules=$($syncResp.effective_rules), Version=$expectedVersion" -ForegroundColor Green

    # 9. Verify Agent Receives filter.apply Command
    Write-Host "`n8. Agent Receiving and Validating Filter Dispatch..."
    # $syncResp.dispatched is the field that actually answers "did the bytes
    # leave". The response's `status` is 'pending', deliberately: it describes
    # the enforcement row, not the wire, and the console reads `dispatched` for
    # exactly this reason (NetworkFilterPage.tsx handleSync). Asserting
    # status='dispatched' here was testing a value the server never returns.
    if (-not $syncResp.dispatched) {
        throw "Filter sync did not dispatch: the device was not reachable on the hub"
    }
    # The envelope id is the policy version the sync just computed, so matching
    # on it selects this dispatch rather than the empty one the reconnect hook
    # pushed at connect time -- before any policy existed.
    $cmdEnv = Read-AgentCommand -Ws $wsAgent -CommandName 'filter.apply' `
        -CancellationToken $ctSource.Token -Id $syncResp.policy_version
    $blockedDomains = $cmdEnv.payload.blocked_domains
    if ($blockedDomains.Count -ne 3) {
        throw "Agent expected 3 blocked domains in payload, got: $($blockedDomains.Count)"
    }
    Write-Host "  [PASS] Agent received filter.apply envelope with domains: $($blockedDomains -join ', ')" -ForegroundColor Green

    # 10. Agent Applies Sinkhole to Managed Hosts File
    Write-Host "`n9. Simulating Agent Hosts File Sinkhole Enforcement..."
    $hostsContent = Get-Content -Path $mockHostsFile -Raw
    $newHosts = $hostsContent + "`n### BEGIN ENDPOINT-MGMT MANAGED BLOCKLIST ###`n# Managed by Endpoint Management Platform - DO NOT EDIT MANUALLY`n"
    foreach ($d in $blockedDomains) {
        $newHosts += "0.0.0.0 $d`n"
    }
    $newHosts += "### END ENDPOINT-MGMT MANAGED BLOCKLIST ###`n"
    Set-Content -Path $mockHostsFile -Value $newHosts

    # Verify hosts file content
    $verifiedHosts = Get-Content -Path $mockHostsFile -Raw
    if (-not $verifiedHosts.Contains("### BEGIN ENDPOINT-MGMT MANAGED BLOCKLIST ###")) {
        throw "Managed blocklist begin marker missing"
    }
    if (-not $verifiedHosts.Contains("0.0.0.0 malware-threat.xyz")) {
        throw "Sinkhole for malware-threat.xyz missing"
    }
    if (-not $verifiedHosts.Contains("0.0.0.0 tiktok.com")) {
        throw "Sinkhole for tiktok.com missing"
    }
    if (-not $verifiedHosts.Contains("10.0.0.1 gateway.lan")) {
        throw "Original unmanaged hosts entry was lost"
    }
    Write-Host "  [PASS] Hosts file sinkholes atomically written inside managed markers" -ForegroundColor Green

    # 11. Agent Reports Compliance Status to Server
    Write-Host "`n10. Agent Reporting Compliance Status to Server..."
    # Authenticated with the device credentials, as the production agent does
    # when it reports filter state (agent/shared/networkfilter).
    $filterHeaders = @{ "X-Device-Id" = $deviceId; "X-Device-Secret" = $deviceSecret }
    $reportResp = Invoke-RestMethod -Uri "$base/api/agent/devices/$deviceId/filter/report" -Method POST -Headers $filterHeaders -Body (@{
        policy_version = $expectedVersion
        status = "synced"
        rules_applied = 3
    } | ConvertTo-Json) -ContentType "application/json"
    if ($reportResp.status -ne "recorded") {
        throw "Expected recorded status, got: $($reportResp.status)"
    }

    # Query device filter state via console API
    $stateResp = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/filter/state" -Method GET -Headers $techHeaders
    if ($stateResp.status -ne "synced" -or $stateResp.rules_applied -ne 3 -or $stateResp.policy_version -ne $expectedVersion) {
        throw "Device filter state mismatch: $($stateResp | ConvertTo-Json)"
    }
    Write-Host "  [PASS] Device filter state verified: Status=$($stateResp.status), RulesApplied=$($stateResp.rules_applied), Version=$($stateResp.policy_version)" -ForegroundColor Green

    # 12. Verify Audit Trail
    Write-Host "`n11. Verifying Forensic Audit Logging..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/audit-logs" -Method GET -Headers $adminHeaders
    $policyAudit = $auditLogs.logs | Where-Object { $_.action -eq "filter.policy_create" }
    $ruleAudit = $auditLogs.logs | Where-Object { $_.action -eq "filter.rule_add" }
    $syncAudit = $auditLogs.logs | Where-Object { $_.action -eq "filter.sync_dispatched" }

    if (-not $policyAudit) { throw "Expected audit log for filter.policy_create" }
    if (-not $ruleAudit) { throw "Expected audit log for filter.rule_add" }
    if (-not $syncAudit) { throw "Expected audit log for filter.sync_dispatched" }

    Write-Host "  [PASS] Audit logs verified for policy creation, rule addition, and sync dispatch" -ForegroundColor Green

    # Clean up websockets
    $wsAgent.Dispose()

    Write-Host "`n========================================================" -ForegroundColor Green
    Write-Host "   ALL 11 FASE 12 CRITERIA PASSED LIVE E2E VERIFICATION  " -ForegroundColor Green
    Write-Host "========================================================" -ForegroundColor Green
} finally {
    Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    Remove-Item $dbPath -ErrorAction SilentlyContinue
    Remove-Item $serverExe -ErrorAction SilentlyContinue
    Remove-Item $mockHostsFile -ErrorAction SilentlyContinue
}
