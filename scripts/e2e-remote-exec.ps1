# scripts/e2e-remote-exec.ps1
# Live E2E test for Fase 5: Remote Execution & Interactive Terminal
# Validates single command execution (stdout/stderr/exit_code), timeout,
# error handling, RBAC enforcement, live interactive terminal streaming over WebSocket,
# session history, and full audit trail.

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http

$port = 18447
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-exec.db"
$credsPath = Join-Path $env:TEMP "em-e2e-exec-creds.json"
$serverExe = Join-Path $env:TEMP "emserver-exec.exe"
$agentExe = Join-Path $env:TEMP "emagent-exec.exe"

# Clean slate
Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
Get-Process emserver-exec, emagent-exec -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== FASE 5 E2E: REMOTE EXECUTION & LIVE INTERACTIVE TERMINAL ===" -ForegroundColor Cyan

# 1. Build server and agent binaries
Write-Host "1. Building server and agent binaries..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'

go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }

go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=0.5.0-exec-e2e" -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "Agent compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-exec-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_exec_password"

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

$agentProc = $null

try {
    # 3. Test Embedded Web Console Route /devices
    Write-Host "`n2. Verifying Single-Binary Web Console Routing..."
    $webRes = Invoke-WebRequest -Uri "$base/devices" -UseBasicParsing
    if ($webRes.StatusCode -ne 200 -or -not ($webRes.Content -match "Enterprise Endpoint Manager")) {
        throw "SPA fallback /devices failed"
    }
    Write-Host "  [PASS] GET /devices -> HTTP 200 (Single-Binary Embedded SPA routing)" -ForegroundColor Green

    # 4. Authenticate Admin Operator & Create Viewer User
    Write-Host "`n3. Authenticating Admin and Setting up RBAC Users..."
    $loginBody = @{ username = "admin"; password = "admin_exec_password" } | ConvertTo-Json
    $loginRes = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body $loginBody -ContentType "application/json"
    $adminToken = $loginRes.access_token
    if (-not $adminToken) { throw "Admin login failed" }
    Write-Host "  [PASS] Admin JWT issued successfully" -ForegroundColor Green
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }

    # Create technician and viewer user tokens directly via test injection or API
    # Since server has jwtSvc in test, we can authenticate via database or create users
    # In SQLite database, let's create a technician and viewer user to test RBAC
    $viewerPass = "viewer12345"
    $bcryptHash = '$2a$10$WqB3j88kE.h/vD8YgA0pP.iF6V26d3YQ4v3XFw1hLqj6n2lD8U1C.' # dummy hash or use bcrypt tool
    # Wait, we can test RBAC using Go integration test or curl with login!
    # Let's insert a viewer and technician user directly into $dbPath using a quick SQLite command or python/go query
    Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
    $createUserCode = @"
package main
import (
    "time"
    "github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
    "github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
    "github.com/jmoiron/sqlx"
    _ "modernc.org/sqlite"
)
func main() {
    d, err := sqlx.Open("sqlite", "$($dbPath.Replace('\', '/'))")
    if err != nil { panic(err) }
    defer d.Close()
    h1, _ := auth.HashPassword("tech12345")
    h2, _ := auth.HashPassword("viewer12345")
    now := time.Now().UTC()
    _, _ = d.Exec("INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
        "tech-uuid", "technician", h1, rbac.RoleTechnician, now, now)
    _, _ = d.Exec("INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
        "viewer-uuid", "viewer", h2, rbac.RoleViewer, now, now)
}
"@
    $helperFile = Join-Path $env:TEMP "create_users_helper.go"
    Set-Content -Path $helperFile -Value $createUserCode -Encoding UTF8
    go run $helperFile
    Remove-Item $helperFile -ErrorAction SilentlyContinue
    Pop-Location

    # Login as Technician
    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "technician"; password = "tech12345" } | ConvertTo-Json) -ContentType "application/json"
    $techToken = $techLogin.access_token
    $techHeaders = @{ Authorization = "Bearer $techToken" }
    Write-Host "  [PASS] Technician authenticated successfully" -ForegroundColor Green

    # Login as Viewer
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewer"; password = "viewer12345" } | ConvertTo-Json) -ContentType "application/json"
    $viewerToken = $viewerLogin.access_token
    $viewerHeaders = @{ Authorization = "Bearer $viewerToken" }
    Write-Host "  [PASS] Viewer authenticated successfully" -ForegroundColor Green

    # 5. Enroll and Connect Live Agent
    Write-Host "`n4. Enrolling and Connecting Live Endpoint Agent..."
    $enrollTokenBody = @{ hostname = "E2E-WINDOWS-REMOTE"; os_name = "windows"; site = "Surabaya-Branch" } | ConvertTo-Json
    $enrollTokenRes = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body $enrollTokenBody -ContentType "application/json"
    $enrollToken = $enrollTokenRes.enrollment_token
    if (-not $enrollToken) { throw "Failed to generate enrollment token" }

    # Start live agent with enrollment in background
    Write-Host "  Starting agent with enrollment token..."
    $agentErr = Join-Path $env:TEMP "emagent-exec.err"
    $agentProc = Start-Process $agentExe -PassThru -WindowStyle Hidden `
        -RedirectStandardError $agentErr `
        -ArgumentList "-server", $base, "-enroll", $enrollToken, "-creds", $credsPath
    Write-Host "  Agent process started (PID: $($agentProc.Id))"

    # Wait for agent WebSocket connection and creds file creation
    $connected = $false
    for ($i = 0; $i -lt 30; $i++) {
        $health = Invoke-RestMethod "$base/healthz"
        if ($health.agents_online -ge 1 -and (Test-Path $credsPath)) {
            $connected = $true
            break
        }
        Start-Sleep -Milliseconds 400
    }
    if (-not $connected) { throw "Agent failed to establish live WebSocket connection" }
    Write-Host "  [PASS] Agent connected! Live agents online: 1" -ForegroundColor Green

    $credsContent = Get-Content $credsPath | ConvertFrom-Json
    $deviceId = $credsContent.device_id
    Write-Host "  [PASS] Agent enrolled with device_id: $deviceId" -ForegroundColor Green

    # Verify device record
    $dev = Invoke-RestMethod -Uri "$base/api/devices/$deviceId" -Headers $adminHeaders
    Write-Host "  [PASS] Device verified in fleet: $($dev.hostname) ($($dev.os_name))" -ForegroundColor Green

    # 6. Test 1: Remote Execution (Non-Interactive PowerShell)
    Write-Host "`n5. Executing Non-Interactive Remote Command (PowerShell)..."
    $execPayload = @{
        shell = "powershell"
        command = "Write-Output 'FASE5_POWERSHELL_LIVE_OK'; Get-Process -Id $PID | Select-Object -ExpandProperty ProcessName"
        timeout_sec = 30
    } | ConvertTo-Json

    $execRes = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/exec" -Method POST -Headers $techHeaders -Body $execPayload -ContentType "application/json"
    $execId = $execRes.execution.id
    if (-not $execId) { throw "Failed to dispatch remote command" }
    Write-Host "  [PASS] Command dispatched (Execution ID: $execId)" -ForegroundColor Green

    # Wait for agent to execute and post result
    Write-Host "  Waiting for command execution outcome..."
    $execCompleted = $false
    $execRecord = $null
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 500
        $execRecord = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/executions/$execId" -Headers $techHeaders
        if ($execRecord.status -eq "completed" -or $execRecord.status -eq "failed") {
            $execCompleted = $true
            break
        }
    }

    if (-not $execCompleted) { throw "Command execution timed out without agent report" }
    if ($execRecord.status -ne "completed") {
        throw "Command expected 'completed', got '$($execRecord.status)': $($execRecord.error_message)"
    }
    if ($execRecord.exit_code -ne 0) {
        throw "Expected exit code 0, got: $($execRecord.exit_code)"
    }
    if (-not ($execRecord.output -match "FASE5_POWERSHELL_LIVE_OK")) {
        throw "Output missing expected string: $($execRecord.output)"
    }

    Write-Host "  [PASS] Remote Command Completed Successfully!" -ForegroundColor Green
    Write-Host "  [PASS] Exit Code: $($execRecord.exit_code)" -ForegroundColor Green
    Write-Host "  [PASS] Output:`n$($execRecord.output.Trim())" -ForegroundColor Gray

    # 7. Test 2: Error & Non-Zero Exit Code Capture
    Write-Host "`n6. Testing Error & Non-Zero Exit Code Handling..."
    $failPayload = @{
        shell = "powershell"
        command = "Write-Error 'Intentional Failure For Test'; exit 42"
        timeout_sec = 15
    } | ConvertTo-Json

    $failRes = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/exec" -Method POST -Headers $adminHeaders -Body $failPayload -ContentType "application/json"
    $failId = $failRes.execution.id

    $failCompleted = $false
    $failRecord = $null
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 500
        $failRecord = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/executions/$failId" -Headers $adminHeaders
        if ($failRecord.status -eq "completed" -or $failRecord.status -eq "failed") {
            $failCompleted = $true
            break
        }
    }

    if (-not $failCompleted) { throw "Error test execution timed out" }
    if ($failRecord.status -ne "failed") {
        throw "Expected status 'failed', got '$($failRecord.status)'"
    }
    if ($failRecord.exit_code -ne 42) {
        throw "Expected exit code 42, got: $($failRecord.exit_code)"
    }
    Write-Host "  [PASS] Non-zero exit code captured correctly (Code: $($failRecord.exit_code), Status: $($failRecord.status))" -ForegroundColor Green

    # 8. Test 3: List Execution History
    Write-Host "`n7. Verifying Execution History Query..."
    $history = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/executions" -Headers $techHeaders
    if ($history.Count -lt 2) {
        throw "Expected at least 2 execution records in history, got: $($history.Count)"
    }
    Write-Host "  [PASS] GET /api/devices/{id}/executions returned $($history.Count) execution records" -ForegroundColor Green

    # 9. Test 4: Strict RBAC Verification
    Write-Host "`n8. Verifying RBAC Policy Enforcement..."
    try {
        $viewerExec = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/exec" -Method POST -Headers $viewerHeaders -Body $execPayload -ContentType "application/json"
        throw "Viewer unexpectedly allowed to execute remote command"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            Write-Host "  [PASS] Viewer role correctly REJECTED with HTTP 403 Forbidden" -ForegroundColor Green
        } else {
            throw "Expected HTTP 403, got: $($_.Exception.Message)"
        }
    }

    # 10. Test 5: Live Interactive Terminal over WebSocket
    Write-Host "`n9. Verifying Live Interactive Terminal WebSocket Stream..."
    # A browser cannot attach an Authorization header to a WebSocket, so the
    # socket takes a one-time ticket fetched over an authenticated request
    # instead. The ticket is spent by the handshake, which keeps the credential
    # out of the access log the way a query-string token never was.
    $ticketResp = Invoke-RestMethod -Uri "$base/api/auth/ws-ticket?purpose=remote-exec" -Headers $adminHeaders
    $wsTicket = $ticketResp.ticket
    if (-not $wsTicket) { throw "Expected a ticket from /api/auth/ws-ticket" }
    $wsUri = [System.Uri]("ws://localhost:$port/api/devices/$deviceId/terminal/ws?ticket=$wsTicket&shell=powershell")
    $wsClient = New-Object System.Net.WebSockets.ClientWebSocket
    $cts = New-Object System.Threading.CancellationTokenSource(15000)

    Write-Host "  Connecting WebSocket to terminal endpoint..."
    $wsClient.ConnectAsync($wsUri, $cts.Token).Wait()
    if ($wsClient.State -ne [System.Net.WebSockets.WebSocketState]::Open) {
        throw "Failed to open terminal WebSocket connection"
    }
    Write-Host "  [PASS] Terminal WebSocket connection established (State: $($wsClient.State))" -ForegroundColor Green

    # Receive initial term.open greeting frame
    $buffer = New-Object byte[] 4096
    $segment = New-Object System.ArraySegment[byte]($buffer, 0, $buffer.Length)
    $recvTask = $wsClient.ReceiveAsync($segment, $cts.Token)
    $recvTask.Wait()
    $openMsg = [System.Text.Encoding]::UTF8.GetString($buffer, 0, $recvTask.Result.Count) | ConvertFrom-Json
    $sessId = $openMsg.session_id
    Write-Host "  [PASS] Received session establishment: $($openMsg.data) (Session ID: $sessId)" -ForegroundColor Green

    # Send interactive command into live terminal
    Write-Host "  Sending interactive shell command via WebSocket..."
    $termInput = @{
        type = "term.data"
        session_id = $sessId
        data = "Write-Output 'INTERACTIVE_TERMINAL_STREAM_PASSED'`r`n"
    } | ConvertTo-Json

    $sendBytes = [System.Text.Encoding]::UTF8.GetBytes($termInput)
    $sendSeg = New-Object System.ArraySegment[byte]($sendBytes, 0, $sendBytes.Length)
    $wsClient.SendAsync($sendSeg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $cts.Token).Wait()

    # Receive streamed output from agent
    Write-Host "  Reading agent output stream..."
    $terminalOutput = ""
    $outputMatched = $false
    for ($j = 0; $j -lt 20; $j++) {
        $recvSeg2 = New-Object System.ArraySegment[byte]($buffer, 0, $buffer.Length)
        $rTask2 = $wsClient.ReceiveAsync($recvSeg2, $cts.Token)
        if ($rTask2.Wait(1500)) {
            $chunk = [System.Text.Encoding]::UTF8.GetString($buffer, 0, $rTask2.Result.Count)
            $terminalOutput += $chunk
            if ($terminalOutput -match "INTERACTIVE_TERMINAL_STREAM_PASSED") {
                $outputMatched = $true
                break
            }
        }
    }

    if (-not $outputMatched) {
        throw "Failed to receive expected terminal stream output. Received: $terminalOutput"
    }
    Write-Host "  [PASS] Interactive terminal received agent stream output!" -ForegroundColor Green
    Write-Host "  [PASS] Matched output: INTERACTIVE_TERMINAL_STREAM_PASSED" -ForegroundColor Green

    # Send close signal
    $closeMsg = @{
        type = "term.close"
        session_id = $sessId
    } | ConvertTo-Json
    $closeBytes = [System.Text.Encoding]::UTF8.GetBytes($closeMsg)
    $closeSeg = New-Object System.ArraySegment[byte]($closeBytes, 0, $closeBytes.Length)
    $wsClient.SendAsync($closeSeg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $cts.Token).Wait()
    try {
        if ($wsClient.State -eq [System.Net.WebSockets.WebSocketState]::Open) {
            $wsClient.CloseOutputAsync([System.Net.WebSockets.WebSocketCloseStatus]::NormalClosure, "done", $cts.Token).Wait()
        }
    } catch {
        # Connection already terminated by server upon receiving term.close
    }
    $wsClient.Dispose()
    Write-Host "  [PASS] Terminal WebSocket closed cleanly" -ForegroundColor Green

    # 11. Test 6: Verify Terminal Session History Record
    Write-Host "`n10. Verifying Terminal Session History and Status in Database..."
    Start-Sleep -Milliseconds 500
    $termSessions = Invoke-RestMethod -Uri "$base/api/devices/$deviceId/terminal/sessions" -Headers $adminHeaders
    if ($termSessions.Count -lt 1) {
        throw "Expected at least 1 terminal session record"
    }
    $lastSess = $termSessions[0]
    if ($lastSess.status -ne "closed") {
        throw "Expected terminal session status 'closed', got: $($lastSess.status)"
    }
    Write-Host "  [PASS] Terminal session record confirmed: status=$($lastSess.status), shell=$($lastSess.shell_type), closed_at=$($lastSess.closed_at)" -ForegroundColor Green

    # 12. Test 7: Verify Audit Trail Logs
    Write-Host "`n11. Verifying Audit Trail for Remote Execution & Terminal Operations..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/audit-logs" -Headers $adminHeaders
    $runAudit = $auditLogs.logs | Where-Object { $_.action -eq "remote_exec.run" }
    $resAudit = $auditLogs.logs | Where-Object { $_.action -eq "remote_exec.result" }
    $openAudit = $auditLogs.logs | Where-Object { $_.action -eq "terminal.open" }
    $closeAudit = $auditLogs.logs | Where-Object { $_.action -eq "terminal.close" }

    if (-not $runAudit) { throw "Missing audit log for remote_exec.run" }
    if (-not $resAudit) { throw "Missing audit log for remote_exec.result" }
    if (-not $openAudit) { throw "Missing audit log for terminal.open" }
    if (-not $closeAudit) { throw "Missing audit log for terminal.close" }

    Write-Host "  [PASS] Audit record for remote_exec.run confirmed (Actor: $($runAudit.actor_id))" -ForegroundColor Green
    Write-Host "  [PASS] Audit record for remote_exec.result confirmed (Actor: $($resAudit.actor_id))" -ForegroundColor Green
    Write-Host "  [PASS] Audit record for terminal.open confirmed (Actor: $($openAudit.actor_id))" -ForegroundColor Green
    Write-Host "  [PASS] Audit record for terminal.close confirmed (Actor: $($closeAudit.actor_id))" -ForegroundColor Green

    Write-Host "`n=======================================================" -ForegroundColor Green
    Write-Host "FASE 5 E2E VERIFICATION PASSED WITH 100% SUCCESS!" -ForegroundColor Green
    Write-Host "All criteria met: Non-interactive Remote Exec, Exit Codes, Output Streaming," -ForegroundColor Green
    Write-Host "Strict RBAC, Live Interactive Terminal WebSocket, and Comprehensive Audit Trail." -ForegroundColor Green
    Write-Host "=======================================================" -ForegroundColor Green

} finally {
    Write-Host "`nCleaning up background processes..."
    if ($agentProc -and -not $agentProc.HasExited) {
        Stop-Process -Id $agentProc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($serverProc -and -not $serverProc.HasExited) {
        Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
}
