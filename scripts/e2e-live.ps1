# scripts/e2e-live.ps1
# Live E2E for Fase 1: starts the real server + agent binaries and walks the
# full flow — login, enrollment token, agent enroll, WS connect, command,
# offline transition. Nothing is mocked: these are the production binaries.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts/e2e-live.ps1

$ErrorActionPreference = 'Stop'

$port = 18443
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-live.db"
$credsPath = Join-Path $env:TEMP "em-e2e-creds.json"
$agentErr = Join-Path $env:TEMP "em-agent.err"

# Clean slate from any previous run.
Remove-Item $dbPath, $credsPath, $agentErr -ErrorAction SilentlyContinue
Get-Process emserver, emagent -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

$env:DB_PATH = $dbPath
$env:JWT_SECRET = 'e2e-live-secret-0123456789abcdef0123456789abcdef'
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = 'info'
$env:ADMIN_PASSWORD = 'admin12345'

$serverExe = Join-Path $env:TEMP 'emserver.exe'
$agentExe = Join-Path $env:TEMP 'emagent.exe'

# Both binaries are built here rather than required to exist already. The two
# scripts that launch the real agent (this one and e2e-inventory) documented a
# manual build step in a header comment and then threw when the runner had not
# done it, so the CI job failed before testing anything. Building is what CI is
# for, and the Go toolchain is a declared step of every job that runs a script.
$env:CGO_ENABLED = '0'
& go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "server build failed" }
& go build -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "agent build failed" }

# Query-Sqlite runs a SELECT against the SQLite DB without any external module:
# it shells out to a tiny Go helper (scripts/querysqlite) that prints rows as
# JSON. Built once on first use.
function Query-Sqlite {
    param([string]$Path, [string]$Query)
    $helper = Join-Path $env:TEMP 'em-querysqlite.exe'
    # Always rebuild: a stale binary here silently breaks every check below.
    $src = Join-Path $PSScriptRoot 'querysqlite\main.go'
    & go build -o $helper $src 2>&1 | Out-Null
    if (-not (Test-Path $helper)) { throw "failed to build querysqlite helper" }
    $out = & $helper -db $Path -query $Query
    if ($LASTEXITCODE -ne 0) { throw "sqlite query failed: $out" }
    # ConvertFrom-Json on a JSON array yields one object per row, but piping it
    # through this function flattens a single-row result to a bare object; the
    # caller's @() wrapper then cannot recover the count. Collect into a
    # variable first and wrap explicitly, so .Count is always right.
    $rows = $out | ConvertFrom-Json
    if ($rows -isnot [array]) { $rows = @($rows) }
    , $rows
}

$srv = Start-Process $serverExe -PassThru -WindowStyle Hidden
try {
    Write-Host "==> server PID $($srv.Id) on port $port"

    # Wait for health.
    $ok = $false
    for ($i = 0; $i -lt 50; $i++) {
        try { Invoke-RestMethod "$base/healthz" -ErrorAction Stop | Out-Null; $ok = $true; break } catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $ok) { throw 'server did not become healthy' }
    Write-Host 'OK   healthz responded'

    # 1. Admin login.
    $login = Invoke-RestMethod -Method Post -Uri "$base/api/auth/login" `
        -ContentType 'application/json' `
        -Body '{"username":"admin","password":"admin12345"}'
    $token = $login.access_token
    if (-not $token) { throw 'login returned no access token' }
    Write-Host "OK   admin login, token length $($token.Length)"

    # 2. Admin creates a one-time enrollment token.
    $tok = Invoke-RestMethod -Method Post -Uri "$base/api/devices/enroll-token" `
        -ContentType 'application/json' -Headers @{Authorization = "Bearer $token"} `
        -Body '{"hostname":"PC-CABANG-01","os_name":"windows","site":"cabang-surabaya"}'
    if (-not $tok.enrollment_token) { throw 'no enrollment token returned' }
    Write-Host "OK   enrollment token issued for device $($tok.device_id)"

    # 3. Agent enrolls with the one-time token and connects. The agent performs
    #    the token exchange itself — this is the realistic onboarding path.
    $ag = Start-Process $agentExe -PassThru -WindowStyle Hidden `
        -RedirectStandardError $agentErr `
        -ArgumentList "-server", $base, "-enroll", $tok.enrollment_token, "-creds", $credsPath
    Write-Host "==> agent PID $($ag.Id)"
    Start-Sleep -Seconds 2

    if ($ag.HasExited) {
        Write-Host '--- agent stderr ---'
        Get-Content $agentErr -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "    $_" }
        throw "agent exited early (exit code $($ag.ExitCode))"
    }

    $dev = Invoke-RestMethod -Uri "$base/api/devices/$($tok.device_id)" -Headers @{Authorization = "Bearer $token"}
    if ($dev.status -ne 'online') { throw "device should be online, got $($dev.status)" }
    Write-Host "OK   agent enrolled + connected via WS, agent=$($dev.agent_version) os=$($dev.os_version)"

    # 4. Token replay must fail now that the agent consumed it.
    $replay = $null
    try {
        $replay = Invoke-RestMethod -Method Post -Uri "$base/api/agent/enroll" -ContentType 'application/json' `
            -Body (@{enrollment_token = $tok.enrollment_token} | ConvertTo-Json -Compress)
    } catch {
        $code = $_.Exception.Response.StatusCode.value__
        if ($code -ne 401) { throw "token replay should be 401, got $code" }
        Write-Host 'OK   token replay rejected (401)'
    }
    if ($replay) { throw 'token replay unexpectedly succeeded' }

    # 5. Ping command round-trip.
    $ping = Invoke-RestMethod -Method Post -Uri "$base/api/devices/$($tok.device_id)/ping" `
        -Headers @{Authorization = "Bearer $token"}
    if ($ping.status -ne 'sent') { throw "ping should be sent, got $($ping.status)" }
    Start-Sleep -Milliseconds 800

    # Query-Sqlite already returns a proper array even for one row.
    $rows = Query-Sqlite -Path $dbPath -Query "SELECT status, result FROM agent_commands WHERE command_type='ping'"
    if ($rows.Count -eq 0) { throw 'no ping command row found in DB' }
    if ($rows[0].status -ne 'done') { throw "command not done: $($rows | Out-String)" }
    Write-Host "OK   ping round-trip done, result=$($rows[0].result)"

    # 6. Agent disconnect -> offline. Stop the agent's process tree. If the
    #    agent shuts down gracefully it closes the socket itself; if the kill
    #    leaves a half-open socket the server's ping/pong read deadline still
    #    flips the device to offline, just later.
    Get-Process -Id $ag.Id -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    $children = Get-CimInstance Win32_Process -Filter "ParentProcessId=$($ag.Id)" -ErrorAction SilentlyContinue
    foreach ($ch in $children) {
        Stop-Process -Id $ch.ProcessId -Force -ErrorAction SilentlyContinue
    }
    $offline = $false
    $dev2 = $null
    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Milliseconds 300
        $dev2 = Invoke-RestMethod -Uri "$base/api/devices/$($tok.device_id)" -Headers @{Authorization = "Bearer $token"}
        if ($dev2.status -eq 'offline') { $offline = $true; break }
    }
    if (-not $offline) {
        throw "device should be offline after disconnect, got $($dev2.status)"
    }
    Write-Host 'OK   device marked offline after agent disconnect'

    # 7. Audit log must have recorded the enrollment and the login.
    $logs = Query-Sqlite -Path $dbPath -Query "SELECT action FROM audit_logs ORDER BY created_at"
    $actions = ($logs | ForEach-Object { $_.action }) -join ', '
    Write-Host "OK   audit trail ($($logs.Count) entries): $actions"
    if ($logs.Count -lt 2) { throw "audit log should have at least 2 entries, got $($logs.Count)" }

    # 8. The disconnect must have been recorded, not just the connect — this is
    #    the audit trail remote-session compliance depends on.
    $dis = Query-Sqlite -Path $dbPath -Query "SELECT action FROM audit_logs WHERE action='agent.disconnect'"
    if ($dis.Count -eq 0) { throw 'no agent.disconnect audit entry recorded' }

    Write-Host ''
    Write-Host 'ALL LIVE E2E CHECKS PASSED' -ForegroundColor Green
}
finally {
    Get-Process emagent, emserver -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
}
