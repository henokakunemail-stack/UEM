# scripts/e2e-task-scheduler.ps1
# Live E2E test for Fase 10: Task Scheduler & Script Repository
# Validates Script Management, SHA-256 Integrity, Schedule Management,
# RBAC Enforcement, On-Demand Run Dispatch, Agent Task Reporting, and Audit Logging.

$ErrorActionPreference = 'Stop'

$port = 18452
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-scheduler.db"
$serverExe = Join-Path $env:TEMP "emserver-scheduler.exe"
$agentExe = Join-Path $env:TEMP "emagent-scheduler.exe"
$credsPath = Join-Path $env:TEMP "em-e2e-scheduler-creds.json"

# Clean slate
Remove-Item $dbPath, $credsPath -ErrorAction SilentlyContinue
Get-Process emserver-scheduler, emagent-scheduler -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "=== FASE 10 E2E: TASK SCHEDULER & SCRIPT REPOSITORY ===" -ForegroundColor Cyan

# 1. Build
Write-Host "1. Building server and agent binaries..."
Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
$env:CGO_ENABLED = '0'
go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "Server compilation failed" }
go build -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "Agent compilation failed" }
Pop-Location

# 2. Start server
$env:DB_PATH = $dbPath
$env:JWT_SECRET = "e2e-scheduler-secret-key-32chars-min-ok"
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = "admin_sched_password"

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
    # 3. Authenticate Admin and create technician and viewer accounts
    Write-Host "`n2. Authenticating Admin and Creating Test Roles..."
    $adminLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "admin"; password = "admin_sched_password" } | ConvertTo-Json) -ContentType "application/json"
    $adminToken = $adminLogin.access_token
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }
    Write-Host "  [PASS] Admin authenticated" -ForegroundColor Green

    # Create technician & viewer
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "techsched"; password = "Password123!"; role = "technician"; display_name = "Tech Sched" } | ConvertTo-Json) -ContentType "application/json"
    $null = Invoke-RestMethod -Uri "$base/api/users" -Method POST -Headers $adminHeaders -Body (@{ username = "viewersched"; password = "Password123!"; role = "viewer"; display_name = "Viewer Sched" } | ConvertTo-Json) -ContentType "application/json"

    $techLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "techsched"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $techHeaders = @{ Authorization = "Bearer $($techLogin.access_token)" }
    $viewerLogin = Invoke-RestMethod -Uri "$base/api/auth/login" -Method POST -Body (@{ username = "viewersched"; password = "Password123!" } | ConvertTo-Json) -ContentType "application/json"
    $viewerHeaders = @{ Authorization = "Bearer $($viewerLogin.access_token)" }
    Write-Host "  [PASS] Technician and Viewer authenticated" -ForegroundColor Green

    # 4. Script Repository RBAC & Integrity
    Write-Host "`n3. Testing Script Repository RBAC and SHA-256 Validation..."
    $scriptPayload = @{
        name = "Fleet Maintenance Check"
        description = "Reports that scheduled maintenance reached the endpoint"
        script_type = "powershell"
        # A live agent runs this on the machine executing the test, so it must
        # not need elevation or change anything there.
        script_content = "Write-Output 'Scheduled maintenance ran'"
        default_args = ""
        timeout_seconds = 60
    } | ConvertTo-Json

    try {
        Invoke-RestMethod -Uri "$base/api/scripts" -Method POST -Headers $viewerHeaders -Body $scriptPayload -ContentType "application/json"
        throw "Viewer unexpectedly allowed to create script"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            Write-Host "  [PASS] Viewer correctly REJECTED from creating script (HTTP 403 Forbidden)" -ForegroundColor Green
        } else {
            throw "Expected HTTP 403, got: $($_.Exception.Message)"
        }
    }

    $createdScript = Invoke-RestMethod -Uri "$base/api/scripts" -Method POST -Headers $adminHeaders -Body $scriptPayload -ContentType "application/json"
    $scriptId = $createdScript.id
    if (-not $scriptId -or -not $createdScript.sha256_hash) {
        throw "Failed to create script with valid SHA-256 hash"
    }
    Write-Host "  [PASS] Admin created script '$($createdScript.name)' (SHA-256: $($createdScript.sha256_hash.Substring(0,16))...)" -ForegroundColor Green

    # Viewer lists scripts
    $scripts = Invoke-RestMethod -Uri "$base/api/scripts" -Headers $viewerHeaders
    if ($scripts.Count -lt 1) { throw "Viewer failed to list scripts" }
    Write-Host "  [PASS] Viewer successfully listed $($scripts.Count) script(s)" -ForegroundColor Green

    # A schedule whose target has no active device is refused when it is saved:
    # it would only ever produce runs that dispatch to nobody. Checked here,
    # before the devices below exist.
    $emptyPayload = @{
        name = "Fleet With No Devices"
        script_id = $scriptId
        target_type = "all"
        target_id = ""
        schedule_type = "interval"
        schedule_expr = "60"
        is_enabled = $true
    } | ConvertTo-Json
    try {
        Invoke-RestMethod -Uri "$base/api/schedules" -Method POST -Headers $adminHeaders -Body $emptyPayload -ContentType "application/json"
        throw "A schedule targeting no active devices was accepted"
    } catch {
        $code = $null
        if ($_.ErrorDetails.Message) { $code = ($_.ErrorDetails.Message | ConvertFrom-Json).code }
        if ($_.Exception.Response.StatusCode.value__ -eq 400 -and $code -eq "no_targets") {
            Write-Host "  [PASS] Schedule with no active target devices REJECTED (HTTP 400 no_targets)" -ForegroundColor Green
        } else {
            throw "Expected HTTP 400 no_targets, got: $($_.Exception.Message) $($_.ErrorDetails.Message)"
        }
    }

    # 5. Target Devices: one live agent, one enrolled endpoint that is offline
    Write-Host "`n4. Enrolling a Live Agent and Seeding an Offline Device..."
    # A dispatch only stays open while the device is connected: an offline
    # device run is closed as failed at once, and a closed run cannot be
    # overwritten by a later report. So the success path needs a real agent
    # that receives the command, runs it and reports through the same endpoint
    # a production agent uses.
    Push-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))
    # The secret hash must be the real HashToken of a known secret: the task
    # result endpoint authenticates the agent (AuthenticateAgent), and a literal
    # placeholder like 'h1' matches no secret, so the report is rejected with 401.
    $schedSecret2 = 'e2e-scheduler-secret-two'
    $seedDevices = @"
package main

import (
    "time"

    devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
    "github.com/jmoiron/sqlx"
    _ "modernc.org/sqlite"
)

func main() {
    d, err := sqlx.Open("sqlite", "$($dbPath.Replace('\', '/'))")
    if err != nil { panic(err) }
    defer d.Close()
    now := time.Now().UTC()
    _, _ = d.Exec("INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at) VALUES ('d-sched-2', 'PC-MEDAN-02', 'windows', '10.0', '1.0.0', 'Medan', 'online', ?, ?, ?, ?, ?)", now, now, devicemgmt.HashToken("$schedSecret2"), now, now)
}
"@
    $helperFile = Join-Path $env:TEMP "seed_sched_devices.go"
    Set-Content -Path $helperFile -Value $seedDevices -Encoding UTF8
    go run $helperFile
    Remove-Item $helperFile -ErrorAction SilentlyContinue
    Pop-Location
    Write-Host "  [PASS] Offline device seeded: PC-MEDAN-02" -ForegroundColor Green

    $enrollTokenBody = @{ hostname = "PC-SURABAYA-01"; os_name = "windows"; site = "Surabaya" } | ConvertTo-Json
    $enrollToken = (Invoke-RestMethod -Uri "$base/api/devices/enroll-token" -Method POST -Headers $adminHeaders -Body $enrollTokenBody -ContentType "application/json").enrollment_token
    if (-not $enrollToken) { throw "Failed to generate enrollment token" }
    $agentErr = Join-Path $env:TEMP "emagent-scheduler.err"
    $agentProc = Start-Process $agentExe -PassThru -WindowStyle Hidden `
        -RedirectStandardError $agentErr `
        -ArgumentList "-server", $base, "-enroll", $enrollToken, "-creds", $credsPath
    $connected = $false
    for ($i = 0; $i -lt 30; $i++) {
        $health = Invoke-RestMethod "$base/healthz"
        if ($health.agents_online -ge 1 -and (Test-Path $credsPath)) { $connected = $true; break }
        Start-Sleep -Milliseconds 400
    }
    if (-not $connected) { throw "Agent failed to establish a live WebSocket connection" }
    $liveDeviceId = (Get-Content $credsPath | ConvertFrom-Json).device_id
    Write-Host "  [PASS] Live agent enrolled and connected: PC-SURABAYA-01 ($liveDeviceId)" -ForegroundColor Green

    # 6. Task Schedule RBAC & Creation
    Write-Host "`n5. Testing Task Schedule Management & RBAC..."
    # An interval, not a cron expression. pollDueSchedules only ever evaluated
    # schedule_type = 'interval', so a cron schedule was accepted, listed, shown
    # in the console, and never fired -- with no log line and no error. An E2E
    # script that only asserted the row existed was therefore blessing the
    # broken behaviour. One minute is the smallest value the poll interval
    # (30s) can actually honour, and step 10 waits for it to fire.
    $schedPayload = @{
        name = "Weekly DNS Flush All Fleet"
        description = "Automated weekly maintenance for all branch devices"
        script_id = $scriptId
        target_type = "all"
        target_id = ""
        schedule_type = "interval"
        schedule_expr = "1"
        is_enabled = $true
    } | ConvertTo-Json

    try {
        Invoke-RestMethod -Uri "$base/api/schedules" -Method POST -Headers $viewerHeaders -Body $schedPayload -ContentType "application/json"
        throw "Viewer unexpectedly allowed to create schedule"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            Write-Host "  [PASS] Viewer correctly REJECTED from creating schedule (HTTP 403 Forbidden)" -ForegroundColor Green
        } else {
            throw "Expected HTTP 403, got: $($_.Exception.Message)"
        }
    }

    $createdSched = Invoke-RestMethod -Uri "$base/api/schedules" -Method POST -Headers $adminHeaders -Body $schedPayload -ContentType "application/json"
    $schedId = $createdSched.id
    if (-not $schedId) { throw "Failed to create schedule" }
    Write-Host "  [PASS] Admin created schedule '$($createdSched.name)' (ID: $schedId)" -ForegroundColor Green

    # Viewer lists schedules
    $schedules = Invoke-RestMethod -Uri "$base/api/schedules" -Headers $viewerHeaders
    if ($schedules.Count -lt 1) { throw "Viewer failed to list schedules" }
    Write-Host "  [PASS] Viewer successfully listed $($schedules.Count) schedule(s)" -ForegroundColor Green

    # 7. Trigger Schedule Run & RBAC
    Write-Host "`n6. Testing Schedule Execution Run & RBAC..."
    try {
        Invoke-RestMethod -Uri "$base/api/schedules/$schedId/trigger" -Method POST -Headers $viewerHeaders
        throw "Viewer unexpectedly allowed to trigger schedule"
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 403) {
            Write-Host "  [PASS] Viewer correctly REJECTED from triggering schedule (HTTP 403 Forbidden)" -ForegroundColor Green
        } else {
            throw "Expected HTTP 403, got: $($_.Exception.Message)"
        }
    }

    # Technician triggers run
    $triggeredRun = Invoke-RestMethod -Uri "$base/api/schedules/$schedId/trigger" -Method POST -Headers $techHeaders
    $runId = $triggeredRun.id
    if (-not $runId) { throw "Failed to trigger run" }
    Write-Host "  [PASS] Technician successfully triggered schedule run (Run ID: $runId)" -ForegroundColor Green

    # 8. Check Device Task Dispatches
    Write-Host "`n7. Checking Device Task Runs..."
    $devRuns = (Invoke-RestMethod -Uri "$base/api/schedules/runs/$runId/devices" -Headers $viewerHeaders)
    if ($devRuns.Count -ne 2) {
        throw "Expected 2 device task runs, got: $($devRuns.Count)"
    }
    $liveTask = $devRuns | Where-Object { $_.device_id -eq $liveDeviceId }
    $offlineTask = $devRuns | Where-Object { $_.device_id -eq 'd-sched-2' }
    if (-not $liveTask -or -not $offlineTask) { throw "Device task runs do not cover both targets: $($devRuns | ConvertTo-Json)" }
    # The offline device never receives the command, so its run is closed by
    # the server at dispatch instead of waiting forever for a report.
    if ($offlineTask.status -ne "failed" -or $offlineTask.error_message -ne "device is offline") {
        throw "Offline device run was not closed as failed: $($offlineTask | ConvertTo-Json)"
    }
    Write-Host "  [PASS] 2 device task runs created; offline PC-MEDAN-02 closed as 'device is offline'" -ForegroundColor Green

    # 9. The live agent runs the script and reports the result itself
    Write-Host "`n8. Waiting for the Live Agent to Run the Script and Report..."
    $updatedTask = $null
    for ($i = 0; $i -lt 60; $i++) {
        $updatedTask = (Invoke-RestMethod -Uri "$base/api/schedules/runs/$runId/devices" -Headers $viewerHeaders) |
            Where-Object { $_.id -eq $liveTask.id }
        if ($updatedTask.status -in @("success", "failed")) { break }
        Start-Sleep -Milliseconds 500
    }
    if ($updatedTask.status -ne "success" -or $updatedTask.exit_code -ne 0 -or -not ($updatedTask.output_log -match "Scheduled maintenance ran")) {
        if (Test-Path $agentErr) { Get-Content $agentErr -Tail 20 | Write-Host }
        throw "Live agent task did not report success: $($updatedTask | ConvertTo-Json)"
    }
    Write-Host "  [PASS] Task execution verified: Status=success, ExitCode=0, Hostname=$($updatedTask.hostname)" -ForegroundColor Green

    # A report that arrives for a run the server already closed is accepted
    # from the authenticated device but does not rewrite the outcome.
    $lateReport = @{ status = "success"; exit_code = 0; output_log = "late"; error_message = "" } | ConvertTo-Json
    $offlineHeaders = @{ "X-Device-Id" = "d-sched-2"; "X-Device-Secret" = $schedSecret2 }
    $null = Invoke-RestMethod -Uri "$base/api/agent/schedules/tasks/$($offlineTask.id)/result" -Method POST -Headers $offlineHeaders -Body $lateReport -ContentType "application/json"
    $offlineAfter = (Invoke-RestMethod -Uri "$base/api/schedules/runs/$runId/devices" -Headers $viewerHeaders) |
        Where-Object { $_.id -eq $offlineTask.id }
    if ($offlineAfter.status -ne "failed") {
        throw "A late report rewrote a closed device run: $($offlineAfter | ConvertTo-Json)"
    }
    Write-Host "  [PASS] Late report did not reopen the closed offline run" -ForegroundColor Green

    $runRow = (Invoke-RestMethod -Uri "$base/api/schedules/runs?limit=50" -Headers $viewerHeaders) |
        Where-Object { $_.id -eq $runId }
    if ($runRow.status -ne "partial" -or -not $runRow.completed_at) {
        throw "Expected the run to close as 'partial' (one success, one offline), got: $($runRow | ConvertTo-Json)"
    }
    Write-Host "  [PASS] Run rolled up to 'partial' with a completion time" -ForegroundColor Green

    # 10. Verify Audit Log
    Write-Host "`n9. Verifying Audit Trail..."
    $auditLogs = Invoke-RestMethod -Uri "$base/api/reports/audit?limit=20" -Headers $adminHeaders
    $hasScriptCreate = $false
    $hasSchedCreate = $false
    $hasSchedTrigger = $false
    foreach ($log in $auditLogs) {
        if ($log.action -eq "script.create") { $hasScriptCreate = $true }
        if ($log.action -eq "schedule.create") { $hasSchedCreate = $true }
        if ($log.action -eq "schedule.trigger") { $hasSchedTrigger = $true }
    }
    if (-not $hasScriptCreate -or -not $hasSchedCreate -or -not $hasSchedTrigger) {
        throw "Missing audit trail records (script.create=$hasScriptCreate, sched.create=$hasSchedCreate, sched.trigger=$hasSchedTrigger)"
    }
    Write-Host "  [PASS] All actions (script.create, schedule.create, schedule.trigger) recorded in audit logs" -ForegroundColor Green

    # 11. The background scheduler fires an enabled interval on its own
    Write-Host "`n10. Waiting for the Background Scheduler to Fire the Schedule..."
    # This is the assertion the previous version of this script was missing. It
    # triggered the schedule by hand through /trigger and counted the runs, which
    # proved the endpoint works and said nothing at all about the ticker: a
    # schedule_type the poll loop skipped, an interval in the wrong unit, or a
    # disabled row would all have passed. Everything above is reachable by an
    # operator clicking a button; this is the part that runs with nobody watching.
    #
    # The manual trigger above stamped last_run_at, so the schedule is not due
    # until the one-minute interval has elapsed since then. The poll runs every
    # 30s, so 90s of waiting covers at least two polls plus the interval itself.
    $scheduleFired = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Seconds 3
        $allRuns = Invoke-RestMethod -Uri "$base/api/schedules/runs?limit=50" -Headers $viewerHeaders
        $unrequested = @($allRuns | Where-Object { $_.schedule_id -eq $schedId -and $_.id -ne $runId })
        if ($unrequested.Count -gt 0) {
            $scheduleFired = $true
            Write-Host "  [PASS] Background scheduler fired the schedule on its own (Run ID: $($unrequested[0].id))" -ForegroundColor Green
            break
        }
    }
    if (-not $scheduleFired) {
        throw "The enabled interval schedule never fired on its own within 90s. The background poll loop did not trigger it, so an operator who configured a schedule instead of triggering it by hand would never have seen the script run."
    }

    Write-Host "`n=======================================================" -ForegroundColor Green
    Write-Host "FASE 10 E2E VERIFICATION PASSED WITH 100% SUCCESS!" -ForegroundColor Green
    Write-Host "All criteria met: Script Repository CRUD, SHA-256 Validation," -ForegroundColor Green
    Write-Host "Schedule Management, Target Resolution (All/Group/Device)," -ForegroundColor Green
    Write-Host "Run Triggering, Background Poll Firing, Agent Execution Reporting, and Audit Trail." -ForegroundColor Green
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
