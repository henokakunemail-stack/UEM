# scripts/e2e-remote-control.ps1
# Live E2E test for Fase 11: Remote Control (Pure Go Screen Capture & Input Relay)
# Validates Session Initiation, Full-Duplex WebSocket Relay, RBAC Enforcement,
# Binary Screen Frame Streaming, Input Event Forwarding, Session Lifecycle, and Audit Logging.

$ErrorActionPreference = 'Stop'

$port = 18453
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-rc.db"
$serverExe = Join-Path $env:TEMP "emserver-rc.exe"

# Clean slate
Remove-Item $dbPath -ErrorAction SilentlyContinue
Get-Process emserver-rc -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== FASE 11 E2E: REMOTE CONTROL (PURE GO RELAY) ===" -ForegroundColor Cyan

# 1. Build server
Write-Host "1. Building server binary (CGO_ENABLED=0)..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'
go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-remote-control-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_rc_password"

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
    $adminLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "admin"; password = "admin_rc_password" } | ConvertTo-Json) -ContentType "application/json"
    $adminToken = $adminLogin.access_token
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Write-Host "  [PASS] Admin authenticated" -ForegroundColor Green

    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "techrc"; password = "Password123!"; role = "technician"; display_name = "Tech RC" } | ConvertTo-Json) -ContentType "application/json"
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "viewerrc"; password = "Password123!"; role = "viewer"; display_name = "Viewer RC" } | ConvertTo-Json) -ContentType "application/json"

    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "techrc"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $techHeaders = @{ Authorization = "Bearer $($techLogin.access_token)" }
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewerrc"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $viewerHeaders = @{ Authorization = "Bearer $($viewerLogin.access_token)" }
    Write-Host "  [PASS] Technician and Viewer accounts created and authenticated" -ForegroundColor Green

    # 4. Enroll Device
    Write-Host "`n3. Enrolling Test Device..."
    $enrollTokenResp = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body (@{
        hostname = "DESKTOP-BRANCH-01"
        os_name = "windows"
        site = "Branch-Jakarta"
    } | ConvertTo-Json) -ContentType "application/json"
    $enrollToken = $enrollTokenResp.enrollment_token

    $enrollResp = Invoke-RestMethod -Uri "$base/api/agent/enroll" -Method POST -Body (@{
        enrollment_token = $enrollToken
    } | ConvertTo-Json) -ContentType "application/json"

    $deviceId = $enrollResp.device_id
    $deviceSecret = $enrollResp.device_secret
    Write-Host "  [PASS] Device enrolled: $deviceId (Hostname: DESKTOP-BRANCH-01)" -ForegroundColor Green

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

    # Send hello frame to mark device online
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

    # Wait for device status to update to online
    Start-Sleep -Milliseconds 500

    # 6. Test RBAC: Viewer must be rejected when initiating session
    Write-Host "`n5. Testing RBAC Access Control on Remote Control..."
    $rbacBlocked = $false
    try {
        Invoke-RestMethod -Uri "$base/api/devices/$deviceId/remotecontrol/session" -Method POST -Headers $viewerHeaders -Body (@{ mode = "full_control" } | ConvertTo-Json) -ContentType "application/json"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            $rbacBlocked = $true
        }
    }
    if (-not $rbacBlocked) {
        throw "Security violation: Viewer was able to initiate remote control session"
    }
    Write-Host "  [PASS] Viewer role correctly blocked with 403 Forbidden" -ForegroundColor Green

    # 7. Technician initiates Remote Control Session (full_control)
    Write-Host "`n6. Initiating Remote Control Session..."
    $sessionResp = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/remotecontrol/session" -Method POST -Headers $techHeaders -Body (@{ mode = "full_control" } | ConvertTo-Json) -ContentType "application/json"
    $sessionId = $sessionResp.id
    if (-not $sessionId) { throw "Expected session ID in response" }
    Write-Host "  [PASS] Remote Control Session initiated: $sessionId (Status: $($sessionResp.status), Mode: $($sessionResp.session_mode))" -ForegroundColor Green

    # Verify agent receives rc.start command on its control socket
    $buffer = New-Object byte[] 4096
    $seg = New-Object System.ArraySegment[byte] ($buffer, 0, $buffer.Length)
    $recvTask = $wsAgent.ReceiveAsync($seg, $ctSource.Token)
    $recvTask.Wait(5000) | Out-Null
    $receivedText = [System.Text.Encoding]::UTF8.GetString($buffer, 0, $recvTask.Result.Count)
    $cmdEnv = $receivedText | ConvertFrom-Json
    if ($cmdEnv.command -ne "rc.start") {
        throw "Agent did not receive rc.start command, got: $($cmdEnv.command)"
    }
    Write-Host "  [PASS] Agent received rc.start command with relay payload" -ForegroundColor Green

    # 8. Connect Operator & Agent WebSockets to Relay
    Write-Host "`n7. Connecting Operator & Agent to Relay Pipeline..."
    $wsOperator = New-Object System.Net.WebSockets.ClientWebSocket
    $opUri = [uri]"ws://localhost:$port/api/devices/$deviceId/remotecontrol/ws?token=$($techLogin.access_token)&session=$sessionId"
    $wsOperator.ConnectAsync($opUri, $ctSource.Token).Wait(5000) | Out-Null
    if ($wsOperator.State -ne [System.Net.WebSockets.WebSocketState]::Open) {
        throw "Operator failed to connect to relay WebSocket"
    }

    $wsAgentRelay = New-Object System.Net.WebSockets.ClientWebSocket
    # The relay authenticates the agent with the device credentials, the same
    # way the transport socket does. Without them the handshake is rejected and
    # the attach fails — which is exactly the bug that made remote control
    # non-functional on every platform until the agent was taught to send them.
    $wsAgentRelay.Options.SetRequestHeader("X-Device-Id", $deviceId)
    $wsAgentRelay.Options.SetRequestHeader("X-Device-Secret", $deviceSecret)
    $agUri = [uri]"ws://localhost:$port/api/agent/devices/$deviceId/remotecontrol/ws?session=$sessionId"
    $wsAgentRelay.ConnectAsync($agUri, $ctSource.Token).Wait(5000) | Out-Null
    if ($wsAgentRelay.State -ne [System.Net.WebSockets.WebSocketState]::Open) {
        throw "Agent failed to connect to relay WebSocket"
    }
    Write-Host "  [PASS] Both Operator and Agent attached to full-duplex relay" -ForegroundColor Green

    # 9. Test Frame Streaming (Agent -> Relay -> Operator)
    Write-Host "`n8. Streaming Binary Screen Frame from Agent to Operator..."
    # 4 bytes header: width=1920 (0x0780), height=1080 (0x0438), followed by JPEG mock bytes
    $framePayload = [byte[]]@(0x07, 0x80, 0x04, 0x38, 0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46)
    $sendSeg = New-Object System.ArraySegment[byte] ($framePayload, 0, $framePayload.Length)
    $wsAgentRelay.SendAsync($sendSeg, [System.Net.WebSockets.WebSocketMessageType]::Binary, $true, $ctSource.Token).Wait(3000) | Out-Null

    # Operator receives frame
    $opBuffer = New-Object byte[] 4096
    $opSeg = New-Object System.ArraySegment[byte] ($opBuffer, 0, $opBuffer.Length)
    $opRecvTask = $wsOperator.ReceiveAsync($opSeg, $ctSource.Token)
    $opRecvTask.Wait(5000) | Out-Null
    if ($opRecvTask.Result.Count -lt 4) {
        throw "Operator received invalid frame bytes"
    }
    $rcvWidth = ([int]$opBuffer[0] -shl 8) -bor [int]$opBuffer[1]
    $rcvHeight = ([int]$opBuffer[2] -shl 8) -bor [int]$opBuffer[3]
    if ($rcvWidth -ne 1920 -or $rcvHeight -ne 1080) {
        throw "Expected 1920x1080 resolution, got: ${rcvWidth}x${rcvHeight}"
    }
    Write-Host "  [PASS] Operator successfully received binary frame (${rcvWidth}x${rcvHeight}, $($opRecvTask.Result.Count) bytes)" -ForegroundColor Green

    # 10. Test Input Injection Forwarding (Operator -> Relay -> Agent)
    Write-Host "`n9. Injecting Operator Input Events to Endpoint..."
    $mouseInput = '{"type":"mouse","action":"click","x":500,"y":300,"button":"left"}'
    $inputBytes = [System.Text.Encoding]::UTF8.GetBytes($mouseInput)
    $inputSeg = New-Object System.ArraySegment[byte] ($inputBytes, 0, $inputBytes.Length)
    $wsOperator.SendAsync($inputSeg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $ctSource.Token).Wait(3000) | Out-Null

    # Agent receives input
    $agBuffer = New-Object byte[] 4096
    $agSeg = New-Object System.ArraySegment[byte] ($agBuffer, 0, $agBuffer.Length)
    $agRecvTask = $wsAgentRelay.ReceiveAsync($agSeg, $ctSource.Token)
    $agRecvTask.Wait(5000) | Out-Null
    $recInputText = [System.Text.Encoding]::UTF8.GetString($agBuffer, 0, $agRecvTask.Result.Count)
    $recInputObj = $recInputText | ConvertFrom-Json
    if ($recInputObj.action -ne "click" -or $recInputObj.x -ne 500 -or $recInputObj.y -ne 300) {
        throw "Agent did not receive valid input event: $recInputText"
    }
    Write-Host "  [PASS] Agent successfully received injected input event: $($recInputObj.action) at ($($recInputObj.x), $($recInputObj.y))" -ForegroundColor Green

    # 11. End Session & Verify Persistence
    Write-Host "`n10. Closing Session and Verifying Telemetry Persistence..."
    $stopResp = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/remotecontrol/sessions/$sessionId/stop" -Method POST -Headers $techHeaders
    if ($stopResp.status -ne "ended") {
        throw "Expected ended status from stop endpoint"
    }

    # Clean up websockets
    $wsOperator.Dispose()
    $wsAgentRelay.Dispose()
    $wsAgent.Dispose()

    # List Sessions and verify database records
    $sessions = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/remotecontrol/sessions" -Method GET -Headers $techHeaders
    if ($sessions.Count -ne 1) {
        throw "Expected 1 session in history, got: $($sessions.Count)"
    }
    $persisted = $sessions[0]
    if ($persisted.status -ne "ended") {
        throw "Persisted status is $($persisted.status), expected ended"
    }
    if ($persisted.frames_transmitted -lt 1) {
        throw "Expected at least 1 frame recorded, got: $($persisted.frames_transmitted)"
    }
    if ($persisted.input_events_count -lt 1) {
        throw "Expected at least 1 input event recorded, got: $($persisted.input_events_count)"
    }
    Write-Host "  [PASS] Session record verified: Status=$($persisted.status), Frames=$($persisted.frames_transmitted), Bytes=$($persisted.bytes_transmitted), Inputs=$($persisted.input_events_count)" -ForegroundColor Green

    # 12. Verify Audit Log
    Write-Host "`n11. Verifying Forensic Audit Logging..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/audit-logs" -Method GET -Headers $adminHeaders
    $startLog = $auditLogs.logs | Where-Object { $_.action -eq "remotecontrol.session_start" }
    $stopLog = $auditLogs.logs | Where-Object { $_.action -eq "remotecontrol.session_stop" }
    if (-not $startLog) {
        throw "Expected audit log for remotecontrol.session_start"
    }
    if (-not $stopLog) {
        throw "Expected audit log for remotecontrol.session_stop"
    }
    Write-Host "  [PASS] Audit logs verified: Start Action=$($startLog.action) (Target=$($startLog.target_id)), Stop Action=$($stopLog.action)" -ForegroundColor Green

    Write-Host "`n========================================================" -ForegroundColor Green
    Write-Host "   ALL 11 FASE 11 CRITERIA PASSED LIVE E2E VERIFICATION  " -ForegroundColor Green
    Write-Host "========================================================" -ForegroundColor Green
} finally {
    Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    Remove-Item $dbPath -ErrorAction SilentlyContinue
    Remove-Item $serverExe -ErrorAction SilentlyContinue
}
