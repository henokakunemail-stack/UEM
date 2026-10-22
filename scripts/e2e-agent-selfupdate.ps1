# scripts/e2e-agent-selfupdate.ps1
# Live E2E for agent self-update with real binaries, end to end:
#
#   a running, enrolled agent built at an older version
#   -> the console registers the agent bundled with the server (agents/)
#   -> a campaign targets the bundled version and is started
#   -> the agent downloads it, checks the SHA-256, swaps its own executable,
#      restarts onto it and reconnects reporting the new version
#   -> the task and the campaign finish, and the file on disk is the bundle's
#
# e2e-agent-update.ps1 checks the same server paths with a simulated swap;
# this one runs the agent's real update engine and its real restart.
#
# -OldAgentSource builds the "old" agent from another source tree, e.g. the
# last release extracted with `git archive v1.5.4 -- go.mod go.sum agent
# protocol | tar -x -C <dir>`, which is how an agent already in the field is
# shown to update. It defaults to this repository.
#
# -PrepareOnly stops after the old agent is online and leaves the server and
# agent running, for driving the same flow through the console by hand.
#
# Nothing here touches an installed agent: the test agent runs interactively
# from its own directory with its own credentials, against a server on
# 127.0.0.1, and only processes started from that directory are stopped.
param(
    [int]$Port = 18458,
    [string]$OldAgentSource = "",
    [string]$WorkDir = (Join-Path $env:TEMP "em-e2e-selfupdate"),
    [switch]$PrepareOnly
)

$ErrorActionPreference = 'Stop'
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
if (-not $OldAgentSource) { $OldAgentSource = $repo }
$base = "http://127.0.0.1:$Port"
$oldVersion = "9.9.0"
$newVersion = "9.9.1"
$adminPassword = "admin_selfupdate_password"

$agentDir = Join-Path $WorkDir "agent"
$bundleDir = Join-Path $WorkDir "bundle"
$dataDir = Join-Path $WorkDir "data"
$serverExe = Join-Path $WorkDir "emserver-selfupdate.exe"
$agentExe = Join-Path $agentDir "endpoint-agent.exe"
$credsPath = Join-Path $agentDir "creds.json"

function Stop-TestProcesses {
    # Only what this script started: the test server binary and anything run
    # from the test agent directory (the agent and the copy it respawned).
    Get-Process | Where-Object { $_.Path -and ($_.Path -eq $serverExe -or $_.Path.StartsWith($agentDir, [StringComparison]::OrdinalIgnoreCase)) } |
        Stop-Process -Force -ErrorAction SilentlyContinue
}

Write-Host "=== E2E: AGENT SELF-UPDATE WITH REAL BINARIES ===" -ForegroundColor Cyan
Stop-TestProcesses
Start-Sleep -Milliseconds 500
if (Test-Path $WorkDir) { Remove-Item -Recurse -Force $WorkDir }
New-Item -ItemType Directory -Force -Path $agentDir, $bundleDir, $dataDir | Out-Null

$env:CGO_ENABLED = '0'
Write-Host "1. Building server $newVersion, bundled agent $newVersion, and old agent $oldVersion..."
Push-Location $repo
try {
    go build -ldflags "-X main.serverVersion=$newVersion" -o $serverExe ./server/cmd/server
    if ($LASTEXITCODE -ne 0) { throw "server build failed" }
    go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$newVersion" -o (Join-Path $bundleDir "agent-windows-amd64.exe") ./agent/cmd/agent
    if ($LASTEXITCODE -ne 0) { throw "bundled agent build failed" }
} finally {
    Pop-Location
}
Push-Location $OldAgentSource
try {
    go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$oldVersion" -o $agentExe ./agent/cmd/agent
    if ($LASTEXITCODE -ne 0) { throw "old agent build failed (source: $OldAgentSource)" }
} finally {
    Pop-Location
}
$bundleHash = (Get-FileHash (Join-Path $bundleDir "agent-windows-amd64.exe") -Algorithm SHA256).Hash.ToLower()
Write-Host "  [PASS] built; bundled agent sha256=$bundleHash" -ForegroundColor Green

$env:DB_PATH = Join-Path $dataDir "server.db"
$env:DATA_DIR = $dataDir
$env:AGENT_BUNDLE_DIR = $bundleDir
$env:JWT_SECRET = "e2e-agent-selfupdate-secret-key-32chars-ok"
$env:HTTP_ADDR = "127.0.0.1:$Port"
$env:LOG_LEVEL = "info"
$env:ADMIN_PASSWORD = $adminPassword
$serverLog = Join-Path $WorkDir "server.log"
$server = Start-Process $serverExe -PassThru -WindowStyle Hidden -RedirectStandardError $serverLog `
    -RedirectStandardOutput (Join-Path $WorkDir "server.out.log")
$ready = $false
for ($i = 0; $i -lt 60; $i++) {
    try { if ((Invoke-RestMethod "$base/healthz" -TimeoutSec 2).status -eq 'ok') { $ready = $true; break } } catch { Start-Sleep -Milliseconds 250 }
}
if (-not $ready) { Stop-TestProcesses; throw "server did not become healthy on $base" }
Write-Host "2. Server healthy on $base (PID $($server.Id))"

try {
    $login = Invoke-RestMethod -Method Post -Uri "$base/api/auth/login" -ContentType 'application/json' `
        -Body (@{ username = 'admin'; password = $adminPassword } | ConvertTo-Json)
    $h = @{ Authorization = "Bearer $($login.access_token)" }

    $tok = Invoke-RestMethod -Method Post -Uri "$base/api/devices/enroll-token" -ContentType 'application/json' -Headers $h `
        -Body '{"hostname":"E2E-SELFUPDATE","os_name":"windows","site":"e2e"}'
    $deviceId = $tok.device_id
    $agentLog = Join-Path $WorkDir "agent-first.log"
    $agent = Start-Process $agentExe -PassThru -WindowStyle Hidden -RedirectStandardError $agentLog `
        -RedirectStandardOutput (Join-Path $WorkDir "agent-first.out.log") `
        -ArgumentList "-server", $base, "-enroll", $tok.enrollment_token, "-creds", $credsPath

    $dev = $null
    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Milliseconds 500
        $dev = Invoke-RestMethod -Uri "$base/api/devices/$deviceId" -Headers $h
        if ($dev.status -eq 'online' -and $dev.agent_version -eq $oldVersion) { break }
    }
    if ($dev.status -ne 'online' -or $dev.agent_version -ne $oldVersion) {
        throw "old agent never came online at $oldVersion (status=$($dev.status) version=$($dev.agent_version)); see $agentLog"
    }
    Write-Host "  [PASS] device $deviceId online running agent $oldVersion (PID $($agent.Id))" -ForegroundColor Green

    if ($PrepareOnly) {
        Write-Host ""
        Write-Host "Prepared. Console: $base  (admin / $adminPassword)" -ForegroundColor Yellow
        Write-Host "Bundled agent $newVersion is in $bundleDir; the device runs $oldVersion."
        Write-Host "Stop everything with: Get-Process | Where-Object { `$_.Path -like '$WorkDir*' } | Stop-Process -Force"
        return
    }

    Write-Host "3. Registering the server's bundled agents as releases..."
    $bundled = Invoke-RestMethod -Uri "$base/api/agent-updates/bundled" -Headers $h
    $win = $bundled | Where-Object { $_.file -eq 'agent-windows-amd64.exe' }
    if (-not $win -or -not $win.importable -or $win.version -ne $newVersion -or $win.os_name -ne 'windows') {
        throw "bundled windows agent not offered as importable $newVersion`: $($bundled | ConvertTo-Json -Compress)"
    }
    $imp = Invoke-RestMethod -Method Post -Uri "$base/api/agent-updates/bundled/import" -Headers $h -ContentType 'application/json' -Body '{"files":[]}'
    $rel = $imp.imported | Where-Object { $_.os_name -eq 'windows' }
    if (-not $rel -or $rel.sha256_checksum -ne $bundleHash) { throw "import did not register the bundled agent: $($imp | ConvertTo-Json -Compress -Depth 4)" }
    Write-Host "  [PASS] release $($rel.id) v$($rel.version) $($rel.os_name)/$($rel.arch) registered from the bundle" -ForegroundColor Green

    Write-Host "4. Starting a campaign to $newVersion..."
    $camp = Invoke-RestMethod -Method Post -Uri "$base/api/agent-updates/campaigns" -Headers $h -ContentType 'application/json' `
        -Body (@{ name = 'E2E self-update'; target_version = $newVersion; target_type = 'all'; batch_size = 10; stagger_interval_sec = 5 } | ConvertTo-Json)
    $start = Invoke-RestMethod -Method Post -Uri "$base/api/agent-updates/campaigns/$($camp.id)/start" -Headers $h
    if ($start.dispatched_live -lt 1) { throw "campaign start dispatched nothing: $($start | ConvertTo-Json -Compress)" }
    Write-Host "  [PASS] campaign $($camp.id) started, dispatched_live=$($start.dispatched_live)" -ForegroundColor Green

    Write-Host "5. Waiting for the agent to swap, restart and report $newVersion..."
    $c = $null
    for ($i = 0; $i -lt 120; $i++) {
        Start-Sleep -Seconds 1
        $c = Invoke-RestMethod -Uri "$base/api/agent-updates/campaigns/$($camp.id)" -Headers $h
        $dev = Invoke-RestMethod -Uri "$base/api/devices/$deviceId" -Headers $h
        if ($c.status -in @('completed', 'failed', 'partial') -and $dev.status -eq 'online') { break }
    }
    $tasks = Invoke-RestMethod -Uri "$base/api/agent-updates/campaigns/$($camp.id)/tasks" -Headers $h
    Write-Host "  campaign status=$($c.status); tasks: $(($tasks | ForEach-Object { "$($_.status) $($_.error_message)" }) -join '; ')"
    if ($c.status -ne 'completed') { throw "campaign ended '$($c.status)', expected completed" }
    if ($dev.agent_version -ne $newVersion -or $dev.status -ne 'online') {
        throw "device reports $($dev.agent_version) / $($dev.status) after the update, expected $newVersion online"
    }
    Write-Host "  [PASS] campaign completed; device back online reporting $newVersion" -ForegroundColor Green

    $diskHash = (Get-FileHash $agentExe -Algorithm SHA256).Hash.ToLower()
    if ($diskHash -ne $bundleHash) { throw "the agent on disk ($diskHash) is not the bundled release ($bundleHash)" }
    $running = Get-Process | Where-Object { $_.Path -eq $agentExe }
    if (-not $running) { throw "no agent process is running from $agentExe after the restart" }
    Write-Host "  [PASS] $agentExe is the bundled binary and is running (PID $($running.Id -join ','))" -ForegroundColor Green
    if (Test-Path "$agentExe.old") { Write-Host "  backup of the previous binary kept at $agentExe.old" }

    Write-Host "`n=== AGENT SELF-UPDATE E2E PASSED ===" -ForegroundColor Green
} finally {
    if (-not $PrepareOnly) { Stop-TestProcesses }
}
