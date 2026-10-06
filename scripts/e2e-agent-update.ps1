# scripts/e2e-agent-update.ps1
# Live E2E test for Fase 13: Agent Self-Update & Rollout Management
# Validates Binary Release Management, Cryptographic SHA-256 Verification,
# Rollout Campaigns, RBAC Enforcement, WebSocket Update Dispatch,
# Atomic Binary Swap & Backup, Version Promotion, and Forensic Audit Logging.

$ErrorActionPreference = 'Stop'

$port = 18455
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-update.db"
$serverExe = Join-Path $env:TEMP "emserver-update.exe"
$releaseDir = Join-Path $env:TEMP "em-releases-test"
$mockAgentExe = Join-Path $env:TEMP "mock_running_agent.exe"
$dummyReleaseFile = Join-Path $env:TEMP "agent_release_v1_2_0.bin"

# Clean slate
Remove-Item $dbPath -ErrorAction SilentlyContinue
Remove-Item $releaseDir -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item $mockAgentExe -ErrorAction SilentlyContinue
Remove-Item ($mockAgentExe + ".old") -ErrorAction SilentlyContinue
Remove-Item $dummyReleaseFile -ErrorAction SilentlyContinue
Get-Process emserver-update -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

# Create directories
New-Item -ItemType Directory -Path $releaseDir -Force | Out-Null

# Seed mock running binary and release payload
Set-Content -Path $mockAgentExe -Value "RUNNING_AGENT_BINARY_V1_0_0"
$newBinaryBytes = [System.Text.Encoding]::UTF8.GetBytes("UPGRADED_AGENT_BINARY_V1_2_0_SECURE_PAYLOAD")
[System.IO.File]::WriteAllBytes($dummyReleaseFile, $newBinaryBytes)

$hasher = [System.Security.Cryptography.SHA256]::Create()
$expectedHashBytes = $hasher.ComputeHash($newBinaryBytes)
$expectedSha256 = [System.BitConverter]::ToString($expectedHashBytes).Replace("-", "").ToLower()

Write-Host "=== FASE 13 E2E: AGENT SELF-UPDATE & ROLLOUT MANAGEMENT ===" -ForegroundColor Cyan

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
$env:JWT_SECRET = "e2e-agent-update-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_update_password"

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
    $adminLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "admin"; password = "admin_update_password" } | ConvertTo-Json) -ContentType "application/json"
    $adminToken = $adminLogin.access_token
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Write-Host "  [PASS] Admin authenticated" -ForegroundColor Green

    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "techupdate"; password = "Password123!"; role = "technician"; display_name = "Tech Update" } | ConvertTo-Json) -ContentType "application/json"
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "viewerupdate"; password = "Password123!"; role = "viewer"; display_name = "Viewer Update" } | ConvertTo-Json) -ContentType "application/json"

    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "techupdate"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $techHeaders = @{ Authorization = "Bearer $($techLogin.access_token)" }
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewerupdate"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $viewerHeaders = @{ Authorization = "Bearer $($viewerLogin.access_token)" }
    Write-Host "  [PASS] Technician and Viewer accounts created and authenticated" -ForegroundColor Green

    # 4. Test RBAC: Viewer must be blocked from uploading releases
    Write-Host "`n3. Testing RBAC on Release Upload and Campaign Creation..."
    $rbacBlocked = $false
    try {
        Invoke-RestMethod -Uri "$base/api/agent-updates/campaigns" -Method POST -Headers $viewerHeaders -Body (@{
            name = "Forbidden Campaign"
            target_version = "1.2.0"
            target_type = "all"
        } | ConvertTo-Json) -ContentType "application/json"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            $rbacBlocked = $true
        }
    }
    if (-not $rbacBlocked) {
        throw "Security violation: Viewer was able to create update campaign"
    }
    Write-Host "  [PASS] Viewer correctly blocked from campaign creation (HTTP 403)" -ForegroundColor Green

    # 5. Admin Uploads New Agent Release (v1.2.0 windows/amd64)
    Write-Host "`n4. Admin Uploading Agent Release Binary v1.2.0..."
    $boundary = [System.Guid]::NewGuid().ToString()
    $LF = "`r`n"
    $bodyBytes = New-Object System.Collections.Generic.List[byte]

    # Form fields
    $fields = @{
        "version" = "1.2.0"
        "os_name" = "windows"
        "arch" = "amd64"
        "changelog" = "Security updates and network sinkhole engine"
    }
    foreach ($k in $fields.Keys) {
        $part = "--$boundary$LF" +
                "Content-Disposition: form-data; name=`"$k`"$LF$LF" +
                "$($fields[$k])$LF"
        $bodyBytes.AddRange([System.Text.Encoding]::UTF8.GetBytes($part))
    }

    # File part
    $fileHeader = "--$boundary$LF" +
                  "Content-Disposition: form-data; name=`"file`"; filename=`"emagent_v1_2_0.exe`"$LF" +
                  "Content-Type: application/octet-stream$LF$LF"
    $bodyBytes.AddRange([System.Text.Encoding]::UTF8.GetBytes($fileHeader))
    $bodyBytes.AddRange($newBinaryBytes)
    $bodyBytes.AddRange([System.Text.Encoding]::UTF8.GetBytes("$LF--$boundary--$LF"))

    $uploadReq = [System.Net.HttpWebRequest]::Create("$base/api/agent-updates/releases")
    $uploadReq.Method = "POST"
    $uploadReq.Headers.Add("Authorization", "Bearer $adminToken")
    $uploadReq.ContentType = "multipart/form-data; boundary=$boundary"
    $uploadStream = $uploadReq.GetRequestStream()
    $uploadStream.Write($bodyBytes.ToArray(), 0, $bodyBytes.Count)
    $uploadStream.Close()

    $uploadResp = $uploadReq.GetResponse()
    $streamReader = New-Object System.IO.StreamReader($uploadResp.GetResponseStream())
    $uploadJson = $streamReader.ReadToEnd() | ConvertFrom-Json
    $streamReader.Close()
    $uploadResp.Close()

    $releaseId = $uploadJson.id
    if ($uploadJson.sha256_checksum -ne $expectedSha256) {
        throw "Checksum mismatch on upload: Expected $expectedSha256, got $($uploadJson.sha256_checksum)"
    }
    Write-Host "  [PASS] Release v1.2.0 uploaded: ID=$releaseId, SHA256=$($uploadJson.sha256_checksum)" -ForegroundColor Green

    # 6. Admin Creates Update Campaign
    Write-Host "`n5. Admin Creating Rollout Campaign..."
    $campResp = Invoke-RestMethod -Uri "$base/api/agent-updates/campaigns" -Method POST -Headers $adminHeaders -Body (@{
        name = "Fleet Rollout Q3 - v1.2.0"
        description = "Rolling out v1.2.0 across all corporate endpoints"
        target_version = "1.2.0"
        target_type = "all"
        batch_size = 25
        stagger_interval_sec = 30
    } | ConvertTo-Json) -ContentType "application/json"
    $campaignId = $campResp.id
    Write-Host "  [PASS] Campaign created: $campaignId (Target Version: 1.2.0)" -ForegroundColor Green

    # 7. Enroll Test Device (Initial Version: 1.0.0)
    Write-Host "`n6. Enrolling Test Device with Initial Version 1.0.0..."
    $enrollTokenResp = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body (@{
        hostname = "UPDATE-TEST-PC-01"
        os_name = "windows"
        site = "Bandung-Branch"
    } | ConvertTo-Json) -ContentType "application/json"

    $enrollResp = Invoke-RestMethod -Uri "$base/api/agent/enroll" -Method POST -Body (@{
        enrollment_token = $enrollTokenResp.enrollment_token
    } | ConvertTo-Json) -ContentType "application/json"
    $deviceId = $enrollResp.device_id
    $deviceSecret = $enrollResp.device_secret

    # Connect WebSocket mock agent
    $wsScheme = "ws://localhost:$port/api/agent/connect"
    $wsAgent = New-Object System.Net.WebSockets.ClientWebSocket
    $wsAgent.Options.SetRequestHeader("X-Device-Id", $deviceId)
    $wsAgent.Options.SetRequestHeader("X-Device-Secret", $deviceSecret)
    $ctSource = New-Object System.Threading.CancellationTokenSource
    $wsAgent.ConnectAsync([uri]$wsScheme, $ctSource.Token).Wait(5000) | Out-Null

    # Send hello frame registering version 1.0.0
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
    Write-Host "  [PASS] Device $deviceId connected online with v1.0.0" -ForegroundColor Green
    Start-Sleep -Milliseconds 500

    # 8. Technician Triggers On-Demand Device Update Dispatch
    Write-Host "`n7. Technician Triggering Update Dispatch to Device..."
    $dispatchResp = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/update/dispatch" -Method POST -Headers $techHeaders -Body (@{
        target_version = "1.2.0"
    } | ConvertTo-Json) -ContentType "application/json"
    if ($dispatchResp.status -ne "dispatched") {
        throw "Expected status 'dispatched', got: $($dispatchResp.status)"
    }
    $taskId = $dispatchResp.task_id
    Write-Host "  [PASS] Update dispatched: TaskID=$taskId, Target=1.2.0" -ForegroundColor Green

    # 9. Verify Agent Receives update.apply Command
    Write-Host "`n8. Agent Receiving and Validating Update Envelope..."
    # The reconnect hook pushes the filter policy to a device that has not
    # reported 'synced' yet, which is every device in this run. That frame
    # arrives first and is not the one under test here. The envelope id is the
    # task id the dispatch just created, so matching on it selects this command.
    $cmdEnv = Read-AgentCommand -Ws $wsAgent -CommandName 'update.apply' `
        -CancellationToken $ctSource.Token -Id $taskId
    $updatePayload = $cmdEnv.payload
    if ($updatePayload.sha256_checksum -ne $expectedSha256) {
        throw "SHA256 mismatch in update command payload"
    }
    Write-Host "  [PASS] Agent received update.apply with download URL: $($updatePayload.download_url)" -ForegroundColor Green

    # 10. Agent Downloads and Verifies SHA-256 Checksum
    Write-Host "`n9. Agent Downloading and Cryptographically Verifying Payload..."
    $downloadUrl = "$base$($updatePayload.download_url)"
    $wc = New-Object System.Net.WebClient
    $wc.Headers.Add("X-Device-Id", $deviceId)
    $wc.Headers.Add("X-Device-Secret", $deviceSecret)
    $tempDownloadPath = Join-Path $env:TEMP "downloaded_agent.tmp"
    $wc.DownloadFile($downloadUrl, $tempDownloadPath)

    $downloadedBytes = [System.IO.File]::ReadAllBytes($tempDownloadPath)
    $downloadedHashBytes = $hasher.ComputeHash($downloadedBytes)
    $downloadedSha256 = [System.BitConverter]::ToString($downloadedHashBytes).Replace("-", "").ToLower()

    if ($downloadedSha256 -ne $expectedSha256) {
        throw "Downloaded payload failed checksum verification"
    }
    Write-Host "  [PASS] Downloaded binary hash matches release SHA-256: $downloadedSha256" -ForegroundColor Green

    # 11. Simulating Atomic Binary Swap on Endpoint
    Write-Host "`n10. Simulating Atomic Binary Swap (Rename Running Exe & Move New)..."
    # Backup current running binary: mock_running_agent.exe -> mock_running_agent.exe.old
    Move-Item -Path $mockAgentExe -Destination ($mockAgentExe + ".old") -Force
    # Move new downloaded binary into place: downloaded_agent.tmp -> mock_running_agent.exe
    Move-Item -Path $tempDownloadPath -Destination $mockAgentExe -Force

    # Verify new binary content and old backup content
    $swappedContent = Get-Content -Path $mockAgentExe -Raw
    $backupContent = Get-Content -Path ($mockAgentExe + ".old") -Raw

    if (-not $swappedContent.Contains("UPGRADED_AGENT_BINARY_V1_2_0_SECURE_PAYLOAD")) {
        throw "Active binary does not contain upgraded content"
    }
    if (-not $backupContent.Contains("RUNNING_AGENT_BINARY_V1_0_0")) {
        throw "Backup binary does not contain original content"
    }
    Write-Host "  [PASS] Atomic swap completed successfully. Backup preserved at $($mockAgentExe).old" -ForegroundColor Green

    # 12. Agent Reports Success and Version Promotion
    Write-Host "`n11. Agent Reporting Success Status and Version Promotion..."
    # The device credential headers are required: the real agent sets both
    # (agent/shared/update/engine.go:194-195) and the endpoint authenticates the
    # agent with them. Without them the report is rejected with 401, so the test
    # was failing on a call the production agent makes correctly.
    $reportHeaders = @{ "X-Device-Id" = $deviceId; "X-Device-Secret" = $deviceSecret }
    $reportResp = Invoke-RestMethod -Uri "$base/api/agent/devices/$deviceId/update/report" -Method POST -Headers $reportHeaders -Body (@{
        task_id = $taskId
        status = "success"
        target_version = "1.2.0"
    } | ConvertTo-Json) -ContentType "application/json"
    if ($reportResp.status -ne "recorded") {
        throw "Expected recorded status, got: $($reportResp.status)"
    }

    # Query device status to verify version promotion
    $deviceStatus = Invoke-RestMethod -Uri "$base/api/devices/$deviceId" -Method GET -Headers $techHeaders
    if ($deviceStatus.agent_version -ne "1.2.0") {
        throw "Device agent_version was not promoted to 1.2.0: $($deviceStatus.agent_version)"
    }
    Write-Host "  [PASS] Device agent_version successfully promoted to: $($deviceStatus.agent_version)" -ForegroundColor Green

    # 13. Verify Audit Trail
    Write-Host "`n12. Verifying Forensic Audit Logging..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/audit-logs" -Method GET -Headers $adminHeaders
    $uploadAudit = $auditLogs.logs | Where-Object { $_.action -eq "agent_update.release_upload" }
    $dispatchAudit = $auditLogs.logs | Where-Object { $_.action -eq "agent_update.device_dispatched" }
    $completeAudit = $auditLogs.logs | Where-Object { $_.action -eq "agent_update.completed" }

    if (-not $uploadAudit) { throw "Expected audit log for agent_update.release_upload" }
    if (-not $dispatchAudit) { throw "Expected audit log for agent_update.device_dispatched" }
    if (-not $completeAudit) { throw "Expected audit log for agent_update.completed" }

    Write-Host "  [PASS] Audit logs verified for release upload, device dispatch, and update completion" -ForegroundColor Green

    # Clean up websockets
    $wsAgent.Dispose()

    Write-Host "`n========================================================" -ForegroundColor Green
    Write-Host "   ALL 12 FASE 13 CRITERIA PASSED LIVE E2E VERIFICATION  " -ForegroundColor Green
    Write-Host "========================================================" -ForegroundColor Green
} finally {
    Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    Remove-Item $dbPath -ErrorAction SilentlyContinue
    Remove-Item $serverExe -ErrorAction SilentlyContinue
    Remove-Item $releaseDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item $mockAgentExe -ErrorAction SilentlyContinue
    Remove-Item ($mockAgentExe + ".old") -ErrorAction SilentlyContinue
    Remove-Item $dummyReleaseFile -ErrorAction SilentlyContinue
}
