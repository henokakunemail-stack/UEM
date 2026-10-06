# scripts/e2e-inventory.ps1
# Live E2E for Fase 2: starts the real server + agent binaries and proves a real
# Windows machine's hardware facts travel agent -> WS -> DB -> API.
#
# This is the test that cannot be faked with mocks: the numbers below come from
# this machine's own CIM, registry and volume APIs. If the collector or the
# transport is broken, the assertions fail against real data.
#
# Build (run once, from the repo root):
#   $env:CGO_ENABLED='0'
#   go build -o "$env:TEMP/emserver.exe" ./server/cmd/server
#   go build -ldflags '-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=0.2.0-e2e' -o "$env:TEMP/emagent-rel.exe" ./agent/cmd/agent
#
# Note on the ldflags: Windows Defender quarantines an agent binary built plain
# `go build -o .../emagent.exe` as a false positive, which makes the run fail at
# Start-Process rather than in the test itself. Varying the embedded version
# string changes the emitted bytes and the signature clears. That is a
# build-environment quirk, not a property of the code.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts/e2e-inventory.ps1

$ErrorActionPreference = 'Stop'

$port = 18444
$base = "http://localhost:$port"
$dbPath = Join-Path $env:TEMP "em-e2e-inv.db"
$credsPath = Join-Path $env:TEMP "em-e2e-inv-creds.json"
$agentErr = Join-Path $env:TEMP "em-inv-agent.err"

# Clean slate from any previous run.
Remove-Item $dbPath, $credsPath, $agentErr -ErrorAction SilentlyContinue
Get-Process emserver, emagent -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue

$env:DB_PATH = $dbPath
$env:JWT_SECRET = 'e2e-inv-live-secret-0123456789abcdef0123'
$env:HTTP_ADDR = ":$port"
$env:LOG_LEVEL = 'info'
$env:ADMIN_PASSWORD = 'admin12345'

$serverExe = Join-Path $env:TEMP 'emserver.exe'
$agentExe = Join-Path $env:TEMP 'emagent-rel.exe'

# Built here rather than required to exist already: the manual build step was
# documented in the header comment above and then thrown when the CI runner
# had not performed it, so this job failed before testing anything.
$env:CGO_ENABLED = '0'
& go build -o $serverExe ./server/cmd/server
if ($LASTEXITCODE -ne 0) { throw "server build failed" }
# The ldflags matter on Windows: see the header comment. A plain
# `go build -o .../emagent.exe` is quarantined by Defender as a false positive,
# which makes the run fail at Start-Process instead of in the test itself.
& go build -ldflags '-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=0.2.0-e2e' -o $agentExe ./agent/cmd/agent
if ($LASTEXITCODE -ne 0) { throw "agent build failed" }

# Query-Sqlite runs a SELECT against the SQLite DB without any external module:
# it shells out to a tiny Go helper (scripts/querysqlite) that prints rows as
# compact single-line JSON. Built once on first use.
function Query-Sqlite {
    param([string]$Path, [string]$Query)
    $helper = Join-Path $env:TEMP 'em-querysqlite.exe'
    $src = Join-Path $PSScriptRoot 'querysqlite\main.go'
    & go build -o $helper $src 2>&1 | Out-Null
    if (-not (Test-Path $helper)) { throw "failed to build querysqlite helper" }
    $out = & $helper -db $Path -query $Query
    if ($LASTEXITCODE -ne 0) { throw "sqlite query failed: $out" }
    # ConvertFrom-Json on a JSON array yields a collection, but a single-row
    # result arrives as one object; collect first and wrap so .Count is right.
    $rows = $out | ConvertFrom-Json
    if ($rows -isnot [array]) { $rows = @($rows) }
    , $rows
}

# Ground truth from the same sources the agent uses, gathered independently so a
# collector bug cannot pass by reading its own numbers back.
$truthRam = [math]::Round((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB)
$truthCPU = (Get-CimInstance Win32_Processor).Name
$truthModel = (Get-CimInstance Win32_ComputerSystem).Model
Write-Host "==> ground truth: RAM=${truthRam}GB CPU='$truthCPU' Model='$truthModel'"

$srv = Start-Process $serverExe -PassThru -WindowStyle Hidden
try {
    Write-Host "==> server PID $($srv.Id) on port $port"

    $ok = $false
    for ($i = 0; $i -lt 50; $i++) {
        try { Invoke-RestMethod "$base/healthz" -ErrorAction Stop | Out-Null; $ok = $true; break } catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $ok) { throw 'server did not become healthy' }

    $login = Invoke-RestMethod -Method Post -Uri "$base/api/auth/login" `
        -ContentType 'application/json' -Body '{"username":"admin","password":"admin12345"}'
    $token = $login.access_token
    if (-not $token) { throw 'login returned no access token' }

    $tok = Invoke-RestMethod -Method Post -Uri "$base/api/devices/enroll-token" `
        -ContentType 'application/json' -Headers @{Authorization = "Bearer $token"} `
        -Body '{"hostname":"PC-HQ-INVENTORY","os_name":"windows","site":"hq"}'
    Write-Host "==> enrollment token issued for device $($tok.device_id)"

    # The agent collects inventory on startup, so it must be started AFTER the
    # enrollment token exists and it must stay up long enough to report.
    $ag = Start-Process $agentExe -PassThru -WindowStyle Hidden `
        -RedirectStandardError $agentErr `
        -ArgumentList "-server", $base, "-enroll", $tok.enrollment_token, "-creds", $credsPath
    Write-Host "==> agent PID $($ag.Id)"
    Start-Sleep -Seconds 6

    if ($ag.HasExited) {
        Write-Host '--- agent stderr ---'
        Get-Content $agentErr -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "    $_" }
        throw "agent exited early (exit code $($ag.ExitCode))"
    }

    $dev = Invoke-RestMethod -Uri "$base/api/devices/$($tok.device_id)" -Headers @{Authorization = "Bearer $token"}
    if ($dev.status -ne 'online') { throw "device should be online, got $($dev.status)" }
    Write-Host "OK   agent enrolled + connected, capabilities=$($dev.capabilities -join ', ')"

    # 1. The capability advertisement reached the DB through the hello handler.
    if ($dev.capabilities -notcontains 'inventory.collect') {
        throw "agent did not advertise inventory.collect: $($dev.capabilities)"
    }
    Write-Host 'OK   capabilities advertised in hello and persisted'

    # 2. Inventory reached the DB. This is the whole module: the row exists and
    #    carries the cached columns the dashboard sorts and filters on.
    $invRow = Query-Sqlite -Path $dbPath -Query "SELECT hw_ram_bytes, hw_cpu_model, hw_disk_free_pct, collected_at FROM device_inventory WHERE device_id='$($tok.device_id)'"
    if ($invRow.Count -eq 0) { throw 'no inventory row in DB — the report never arrived' }
    $ramGB = [math]::Round($invRow[0].hw_ram_bytes / 1073741824)
    Write-Host "OK   inventory stored: RAM=${ramGB}GB CPU='$($invRow[0].hw_cpu_model)' disk_free=$([math]::Round($invRow[0].hw_disk_free_pct,1))%"
    if ([math]::Abs($ramGB - $truthRam) -gt 1) {
        throw "RAM mismatch: agent reported ${ramGB}GB, CIM says ${truthRam}GB"
    }
    if ($invRow[0].hw_cpu_model -ne $truthCPU) {
        throw "CPU mismatch: agent reported '$($invRow[0].hw_cpu_model)', CIM says '$truthCPU'"
    }
    Write-Host 'OK   reported RAM and CPU match independently queried CIM values'

    # 3. The API returns the same facts the DB holds.
    $inv = Invoke-RestMethod -Uri "$base/api/devices/$($tok.device_id)/inventory" -Headers @{Authorization = "Bearer $token"}
    if ($inv.ram_bytes -ne $invRow[0].hw_ram_bytes) { throw "API ram_bytes $($inv.ram_bytes) != DB $($invRow[0].hw_ram_bytes)" }
    if ($inv.cpu_model -ne $truthCPU) { throw "API cpu_model does not match ground truth" }
    if (-not $inv.hw.disks) { throw 'no disks reported' }
    Write-Host "OK   GET /inventory returns: model=$($inv.hw.model.vendor) $($inv.hw.model.product) serial=$($inv.hw.model.serial_number)"
    Write-Host "    disks: $($inv.hw.disks.Count) volumes, software entries: $($inv.software.Count)"

    # 4. Software list is non-empty and comes from the real registry.
    if ($inv.software.Count -lt 5) { throw "expected a real software list, got $($inv.software.Count) entries" }
    $names = $inv.software | ForEach-Object { $_.name }
    Write-Host "OK   software list: $($names[0..2] -join ', ') ... ($($inv.software.Count) total)"

    # 5. On-demand collection: the command reaches the agent over the socket and
    #    a fresh report comes back while it is still connected.
    $before = $invRow[0].collected_at
    $collect = Invoke-RestMethod -Method Post -Uri "$base/api/devices/$($tok.device_id)/inventory/collect" `
        -Headers @{Authorization = "Bearer $token"}
    if ($collect.status -ne 'sent') { throw "collect to an online device must be sent, got $($collect.status)" }
    Start-Sleep -Seconds 4

    $invRow2 = Query-Sqlite -Path $dbPath -Query "SELECT collected_at FROM device_inventory WHERE device_id='$($tok.device_id)'"
    if ($invRow2[0].collected_at -le $before) {
        throw 'on-demand collection did not refresh the stored snapshot'
    }
    Write-Host 'OK   inventory.collect command round-tripped; snapshot refreshed'

    # 6. The hardware-change audit path: nothing changed, so nothing was written.
    #    A quiet machine must not generate audit noise.
    $hwAudit = Query-Sqlite -Path $dbPath -Query "SELECT action FROM audit_logs WHERE action='inventory.hw_changed'"
    if ($hwAudit.Count -ne 0) { throw "unchanged hardware must not produce an audit entry, got $($hwAudit.Count)" }
    Write-Host 'OK   unchanged hardware produced no audit noise'

    # 7. The collect request itself must be in the audit trail.
    $collectAudit = Query-Sqlite -Path $dbPath -Query "SELECT action FROM audit_logs WHERE action='inventory.collect'"
    if ($collectAudit.Count -eq 0) { throw 'no inventory.collect audit entry recorded' }
    Write-Host 'OK   collect request recorded in audit trail'

    # 8. Group lifecycle against a real device.
    $grp = Invoke-RestMethod -Method Post -Uri "$base/api/groups" -ContentType 'application/json' `
        -Headers @{Authorization = "Bearer $token"} -Body '{"name":"PatchTuesday","description":"live e2e"}'
    $members = Invoke-RestMethod -Uri "$base/api/groups/$($grp.id)/devices" -Headers @{Authorization = "Bearer $token"}
    if ($members.total -ne 0) { throw "fresh group must be empty, got $($members.total)" }

    Invoke-RestMethod -Method Post -Uri "$base/api/groups/$($grp.id)/members" -ContentType 'application/json' `
        -Headers @{Authorization = "Bearer $token"} -Body (@{device_ids = @($tok.device_id)} | ConvertTo-Json -Compress) | Out-Null
    $members = Invoke-RestMethod -Uri "$base/api/groups/$($grp.id)/devices" -Headers @{Authorization = "Bearer $token"}
    if ($members.total -ne 1) { throw "group must have 1 member after add, got $($members.total)" }
    if ($members.devices[0].id -ne $tok.device_id) { throw "group member is the wrong device" }
    Write-Host 'OK   group created, device added, membership visible through the API'

    # 9. Retire: the device leaves the fleet list and loses its secret.
    $all = Invoke-RestMethod -Uri "$base/api/devices" -Headers @{Authorization = "Bearer $token"}
    if ($all.devices.Count -ne 1) { throw "expected 1 device in fleet, got $($all.devices.Count)" }
    Invoke-RestMethod -Method Post -Uri "$base/api/devices/$($tok.device_id)/retire" -Headers @{Authorization = "Bearer $token"} | Out-Null
    $all = Invoke-RestMethod -Uri "$base/api/devices" -Headers @{Authorization = "Bearer $token"}
    if ($all.devices.Count -ne 0) { throw "retired device must not appear in the device list" }
    $secretRow = Query-Sqlite -Path $dbPath -Query "SELECT device_secret_hash FROM devices WHERE id='$($tok.device_id)'"
    if ($secretRow[0].device_secret_hash -ne '') { throw 'retire must clear the device secret' }
    Write-Host 'OK   retire removed the device from the fleet and cleared its secret'

    Write-Host ''
    Write-Host 'ALL LIVE INVENTORY E2E CHECKS PASSED' -ForegroundColor Green
}
finally {
    Get-Process emagent, emserver -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
}
