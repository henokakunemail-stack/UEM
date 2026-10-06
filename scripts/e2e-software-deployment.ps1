# scripts/e2e-software-deployment.ps1
# Live E2E test for Fase 4: Software Deployment & Silent Multi-OS Installation
# Validates package upload with SHA-256, WebSocket command dispatch, agent download,
# checksum verification, silent execution, task progress reporting, and web console integration.

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http

$port = 18446
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-soft.db"
$credsPath = Join-Path $env:TEMP "em-e2e-soft-creds.json"
$pkgStorageDir = Join-Path $env:TEMP "em-e2e-packages"
$serverExe = Join-Path $env:TEMP "emserver-soft.exe"
$agentExe = Join-Path $env:TEMP "emagent-soft.exe"

# Clean slate
Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
Remove-Item $pkgStorageDir -Recurse -Force -ErrorAction SilentlyContinue
Get-Process emserver-soft, emagent-soft -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== FASE 4 E2E: SOFTWARE DEPLOYMENT & AGENT INSTALLER VERIFICATION ===" -ForegroundColor Cyan

# 1. Build server and agent binaries
Write-Host "1. Building server and agent binaries..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'

go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }

go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=0.4.0-soft-e2e" -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "Agent compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-soft-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_soft_password"

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
    # 3. Test Embedded Web Console Route /software
    Write-Host "`n2. Verifying Single-Binary Web Console Routing..."
    $softWebRes = Invoke-WebRequest -Uri "$base/software" -UseBasicParsing
    if ($softWebRes.StatusCode -ne 200 -or -not ($softWebRes.Content -match "Enterprise Endpoint Manager")) {
        throw "SPA fallback /software failed"
    }
    Write-Host "  [PASS] GET /software -> HTTP 200 (Single-Binary Embedded SPA routing)" -ForegroundColor Green

    # 4. Authenticate Admin Operator
    Write-Host "`n3. Authenticating Admin Operator..."
    $loginBody = @{ username = "admin"; password = "admin_soft_password" } | ConvertTo-Json
    $loginRes = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body $loginBody -ContentType "application/json"
    $token = $loginRes.access_token
    if (-not $token) { throw "Login failed" }
    Write-Host "  [PASS] Admin JWT issued successfully" -ForegroundColor Green

    $authHeader = @{ Authorization = "Bearer $token" }

    # 5. Enroll and Connect Live Agent
    Write-Host "`n4. Enrolling and Connecting Live Endpoint Agent..."
    $enrollTokenBody = @{ hostname = "E2E-WINDOWS-VM"; os_name = "windows"; site = "Jakarta-HQ" } | ConvertTo-Json
    $enrollTokenRes = Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $authHeader -Body $enrollTokenBody -ContentType "application/json"
    $enrollToken = $enrollTokenRes.enrollment_token
    if (-not $enrollToken) { throw "Failed to generate enrollment token" }

    # Start live agent with enrollment in background
    Write-Host "  Starting agent with enrollment token..."
    $agentErr = Join-Path $env:TEMP "emagent-soft.err"
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

    # 6. Verify Capability: software.install
    $dev = Invoke-RestMethod -Uri "$base/api/devices/$deviceId" -Headers $authHeader
    Write-Host "  [PASS] Device verified in fleet: $($dev.hostname) ($($dev.os_name))" -ForegroundColor Green

    # 7. Upload Software Package (A real silent PowerShell script package for validation)
    Write-Host "`n5. Uploading Software Package to Repository..."
    $testScriptContent = @'
# E2E Test Installer Payload
Write-Output "Installing Endpoint Management Utility v1.0.0"
Write-Output "Deployment verification successful at $(Get-Date -Format o)"
exit 0
'@
    $testScriptPath = Join-Path $env:TEMP "e2e_installer.ps1"
    Set-Content -Path $testScriptPath -Value $testScriptContent -Encoding UTF8

    # Calculate expected SHA-256
    $sha256Managed = [System.Security.Cryptography.SHA256]::Create()
    $fileBytes = [System.IO.File]::ReadAllBytes($testScriptPath)
    $hashBytes = $sha256Managed.ComputeHash($fileBytes)
    $expectedSHA256 = [System.BitConverter]::ToString($hashBytes).Replace("-", "").ToLowerInvariant()
    $sha256Managed.Dispose()

    # Form-data multipart upload using HttpClient
    $client = New-Object System.Net.Http.HttpClient
    $client.DefaultRequestHeaders.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $token)

    $content = New-Object System.Net.Http.MultipartFormDataContent
    $content.Add((New-Object System.Net.Http.StringContent("E2E Test Utility")), "name")
    $content.Add((New-Object System.Net.Http.StringContent("1.0.0")), "version")
    $content.Add((New-Object System.Net.Http.StringContent("windows")), "os_target")
    $content.Add((New-Object System.Net.Http.StringContent("script")), "package_type")
    $content.Add((New-Object System.Net.Http.StringContent("")), "install_args")

    $fileStream = [System.IO.File]::OpenRead($testScriptPath)
    $fileContent = New-Object System.Net.Http.StreamContent($fileStream)
    $fileContent.Headers.ContentType = New-Object System.Net.Http.Headers.MediaTypeHeaderValue("application/octet-stream")
    $content.Add($fileContent, "file", "e2e_installer.ps1")

    $uploadResponse = $client.PostAsync("$base/api/software/packages", $content).Result
    $fileStream.Close()

    if (-not $uploadResponse.IsSuccessStatusCode) {
        $errBody = $uploadResponse.Content.ReadAsStringAsync().Result
        throw "Package upload failed: $($uploadResponse.StatusCode) - $errBody"
    }

    $pkgJSON = $uploadResponse.Content.ReadAsStringAsync().Result | ConvertFrom-Json
    $packageId = $pkgJSON.id
    if ($pkgJSON.sha256 -ne $expectedSHA256) {
        throw "Checksum mismatch: expected $expectedSHA256, got $($pkgJSON.sha256)"
    }
    Write-Host "  [PASS] Package uploaded: $($pkgJSON.name) v$($pkgJSON.version)" -ForegroundColor Green
    Write-Host "  [PASS] Computed SHA-256: $($pkgJSON.sha256)" -ForegroundColor Green
    Write-Host "  [PASS] File Size: $($pkgJSON.file_size) bytes" -ForegroundColor Green

    # 8. Query Package Repository List
    Write-Host "`n6. Querying Package Repository..."
    $pkgs = Invoke-RestMethod -Uri "$base/api/software/packages" -Headers $authHeader
    if ($pkgs.Count -lt 1 -or $pkgs[0].id -ne $packageId) {
        throw "Package repository listing failed"
    }
    Write-Host "  [PASS] GET /api/software/packages returned $($pkgs.Count) package(s)" -ForegroundColor Green

    # 9. Create Deployment Job Targeting the Device
    Write-Host "`n7. Launching Live Software Deployment Job..."
    $deployBody = @{
        name = "Live Rollout: E2E Test Utility"
        package_id = $packageId
        target_type = "device"
        target_id = $deviceId
    } | ConvertTo-Json

    $deployRes = Invoke-RestMethod -Uri "$base/api/software/deployments" -Method POST -Headers $authHeader -Body $deployBody -ContentType "application/json"
    $deploymentId = $deployRes.deployment.id
    if (-not $deploymentId) { throw "Deployment creation failed" }
    Write-Host "  [PASS] Deployment created (ID: $deploymentId)" -ForegroundColor Green
    Write-Host "  [PASS] Total tasks: $($deployRes.tasks_total), Live dispatched: $($deployRes.dispatched_live)" -ForegroundColor Green

    # 10. Wait for Agent Execution and Progress Reporting
    Write-Host "`n8. Waiting for Agent to Download, Verify Checksum, and Execute Installer..."
    $taskCompleted = $false
    $taskResult = $null

    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 500
        $tasks = Invoke-RestMethod -Uri "$base/api/software/deployments/$deploymentId/tasks" -Headers $authHeader
        if ($tasks.Count -ge 1) {
            $t = $tasks[0]
            Write-Host "  Task status: $($t.status) | Device: $($t.hostname)"
            if ($t.status -eq "success" -or $t.status -eq "failed") {
                $taskCompleted = $true
                $taskResult = $t
                break
            }
        }
    }

    if (-not $taskCompleted) {
        if (Test-Path $agentErr) {
            Write-Host "--- Agent Log Output ---"
            Get-Content $agentErr | ForEach-Object { Write-Host "  $_" }
        }
        throw "Task did not complete within timeout"
    }

    if ($taskResult.status -ne "success") {
        throw "Task execution failed: Exit code: $($taskResult.exit_code), Error: $($taskResult.error_message), Log: $($taskResult.output_log)"
    }

    Write-Host "  [PASS] Task completed successfully!" -ForegroundColor Green
    Write-Host "  [PASS] Exit Code: $($taskResult.exit_code)" -ForegroundColor Green
    Write-Host "  [PASS] Output Log: $($taskResult.output_log)" -ForegroundColor Green

    # 11. Verify Deployment Status Completion and Aggregations
    Write-Host "`n9. Verifying Deployment Status Aggregations..."
    $depFinal = Invoke-RestMethod -Uri "$base/api/software/deployments/$deploymentId" -Headers $authHeader
    if ($depFinal.status -ne "completed") {
        throw "Expected deployment status 'completed', got: $($depFinal.status)"
    }
    if ($depFinal.success_tasks -ne 1 -or $depFinal.failed_tasks -ne 0) {
        throw "Unexpected task counts: success=$($depFinal.success_tasks), failed=$($depFinal.failed_tasks)"
    }
    Write-Host "  [PASS] Deployment status: $($depFinal.status) (completed_at: $($depFinal.completed_at))" -ForegroundColor Green
    Write-Host "  [PASS] Aggregations: $($depFinal.success_tasks)/$($depFinal.total_tasks) tasks succeeded" -ForegroundColor Green

    # 12. Verify Audit Trail Entry
    Write-Host "`n10. Verifying Audit Trail..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/audit-logs" -Headers $authHeader
    $uploadAudit = $auditLogs.logs | Where-Object { $_.action -eq "software.upload" }
    # The audit action is the verb that ran, not the noun the test named. The
    # handler logs "software." + req.Action (handler.go createDeployment), and
    # req.Action is 'install' or 'uninstall' -- 'software.deploy' is not a value
    # that path can produce, so this assertion failed on every run against a
    # server that had recorded the event correctly.
    $deployAudit = $auditLogs.logs | Where-Object { $_.action -eq "software.install" }

    if (-not $uploadAudit) { throw "Missing audit log for software.upload" }
    if (-not $deployAudit) { throw "Missing audit log for software.install" }
    Write-Host "  [PASS] Audit record for software.upload confirmed (Actor: $($uploadAudit.actor_id))" -ForegroundColor Green
    Write-Host "  [PASS] Audit record for software.install confirmed (Actor: $($deployAudit.actor_id))" -ForegroundColor Green

    Write-Host "`n=======================================================" -ForegroundColor Green
    Write-Host "FASE 4 E2E VERIFICATION PASSED WITH 100% SUCCESS!" -ForegroundColor Green
    Write-Host "All criteria met: Package Repo, SHA-256 verification, WebSocket command dispatch, silent agent execution, live status tracking, and web console integration." -ForegroundColor Green
    Write-Host "=======================================================" -ForegroundColor Green

} finally {
    Write-Host "`nCleaning up background processes..."
    if ($agentProc -and -not $agentProc.HasExited) {
        Stop-Process -Id $agentProc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($serverProc -and -not $serverProc.HasExited) {
        Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $testScriptPath -ErrorAction SilentlyContinue
    Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
}
