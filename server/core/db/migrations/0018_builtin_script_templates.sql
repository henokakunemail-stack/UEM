-- 0018_builtin_script_templates.sql
-- Starter scripts for the Script Repository, so a fresh install has something
-- to schedule instead of an empty table.
--
-- Every body is plain ASCII PowerShell, which keeps this file readable in any
-- editor and avoids a second encoding for a table that stores script text.
--
-- sha256_hash is sha256(script_content) byte for byte. The server recomputes it
-- on CreateScript and UpdateScript, so a wrong value here would surface as a
-- changed hash the first time an operator edits a template.
--
-- default_args is empty on every row. The dispatcher sends script_content
-- verbatim, so a value there would promise a parameter that nothing reads.
-- The editable part of each script is its first line instead.
--
-- These are fixed snapshots. INSERT OR IGNORE means an operator who edited or
-- deleted a template keeps their version, and name is not a unique key so the
-- same script can also be added by hand under a new name.
--
-- created_by is 'system' because these rows exist before anyone signs in, and
-- the column carries no foreign key.

INSERT OR IGNORE INTO script_templates (
    id, name, description, script_type, script_content, sha256_hash,
    default_args, timeout_seconds, created_by, created_at, updated_at
) VALUES (
    'b1f0a7c2d4e64a1b9c8f0d2e3a5b6c71',
    'Ping Sweep',
    'Pings every host in a /24 network and lists the ones that answered.',
    'powershell',
    '$base = ''10.200.10''   # edit this line: first three octets only
$ips = 1..254 | ForEach-Object { $base + ''.'' + $_ }

# -AsJob pings the whole range in parallel. A plain Test-Connection loop is
# sequential and needs roughly two minutes for 254 hosts, which is long enough
# to trip the scheduler timeout on a healthy network.
$job = Test-Connection -ComputerName $ips -AsJob -ErrorAction Stop
$results = @(Receive-Job -Job $job -Wait)

# Receive-Job returns Win32_PingStatus objects, which carry StatusCode, where
# 0 means success. There is no Status property to read here.
$online = @($results |
    Where-Object { $_.StatusCode -eq 0 } |
    ForEach-Object { $_.Address } |
    Sort-Object -Unique)

Write-Output (''Scanned: '' + $ips.Count)
Write-Output (''Online: '' + $online.Count)
$online -join '', ''',
    'd771fc268f6c5f733af1761e8088a6c6638f06e8487e91c8dcd7bf2a0e74a17c',
    '',
    300,
    'system',
    DATETIME('now'),
    DATETIME('now')
);

INSERT OR IGNORE INTO script_templates (
    id, name, description, script_type, script_content, sha256_hash,
    default_args, timeout_seconds, created_by, created_at, updated_at
) VALUES (
    'c2e1b8d35f75b2c0d9a1e3f4b6c7d82',
    'Restart Windows Service',
    'Stops and starts a Windows service, or starts it when it is already stopped.',
    'powershell',
    '$svc = ''Spooler''   # edit this line: service name
$obj = Get-Service -Name $svc -ErrorAction Stop
if ($obj.Status -ne ''Running'') {
    Start-Service -Name $svc -ErrorAction Stop
    Write-Output (''Started '' + $svc)
} else {
    Stop-Service -Name $svc -Force -ErrorAction Stop
    Start-Sleep -Seconds 2
    Start-Service -Name $svc -ErrorAction Stop
    Write-Output (''Restarted '' + $svc)
}
Get-Service -Name $svc | Format-List Name, Status',
    '60b7e4d3bde570f6b0a2e6433613e0a24fc6c2aa8d680c621da5ad9af41500c4',
    '',
    60,
    'system',
    DATETIME('now'),
    DATETIME('now')
);

INSERT OR IGNORE INTO script_templates (
    id, name, description, script_type, script_content, sha256_hash,
    default_args, timeout_seconds, created_by, created_at, updated_at
) VALUES (
    '06c5f2b79db9f6031e4c7d8f0ab126',
    'Netstat Listening Ports',
    'Shows the process holding a listening TCP port, or every listener when 0 is given.',
    'powershell',
    '$port = 0   # edit this line: TCP port to inspect, 0 lists every listener
if ($port -gt 0) {
    $listeners = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
        Where-Object { $_.LocalPort -eq $port }
} else {
    $listeners = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue
}
$rows = $listeners | ForEach-Object {
    [PSCustomObject]@{
        Address = $_.LocalAddress
        Port    = $_.LocalPort
        PID     = $_.OwningProcess
        Process = (Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue).ProcessName
    }
}
if (-not $rows) {
    Write-Output ''No listening socket matched.''
    exit 0
}
$rows | Sort-Object Port | Format-Table -AutoSize',
    'dabb3f63de825b4b1433985f334b5efabf1d636598b4b104f8e3fc8bb3efcf3a',
    '',
    60,
    'system',
    DATETIME('now'),
    DATETIME('now')
);

INSERT OR IGNORE INTO script_templates (
    id, name, description, script_type, script_content, sha256_hash,
    default_args, timeout_seconds, created_by, created_at, updated_at
) VALUES (
    '17d603c8aceca7124f5d8e9a1bc2347',
    'Kill Process By Name',
    'Force stops every process with a given image name and prints what it stopped.',
    'powershell',
    '$name = ''notepad''   # edit this line: process image name, without .exe
$procs = Get-Process -Name $name -ErrorAction SilentlyContinue
if (-not $procs) {
    Write-Output (''No process named '' + $name)
    exit 0
}
$procs | Select-Object Id, ProcessName, StartTime | Format-Table -AutoSize
$procs | Stop-Process -Force -ErrorAction Stop
Write-Output (''Stopped '' + @($procs).Count + '' process(es) named '' + $name)',
    '2e5fc0ca7063cf227c2d4e2575d2154f323461c7b230788c5bc948c03eebd349',
    '',
    60,
    'system',
    DATETIME('now'),
    DATETIME('now')
);

INSERT OR IGNORE INTO script_templates (
    id, name, description, script_type, script_content, sha256_hash,
    default_args, timeout_seconds, created_by, created_at, updated_at
) VALUES (
    '28e714d9bdfdb8235a6e9f0b2cd3458',
    'Log Disk Usage',
    'Sums the size of each top level folder on a drive and prints the largest first.',
    'powershell',
    '$root = ''C:\''   # edit this line: drive or folder to measure
$rows = Get-ChildItem -LiteralPath $root -Directory -Force -ErrorAction SilentlyContinue |
    ForEach-Object {
        $sum = (Get-ChildItem -LiteralPath $_.FullName -File -Recurse -Force -ErrorAction SilentlyContinue |
            Measure-Object -Property Length -Sum).Sum
        [PSCustomObject]@{
            Folder = $_.FullName
            SizeGB = $(if ($sum) { [math]::Round($sum / 1GB, 2) } else { 0 })
        }
    }
$rows | Sort-Object SizeGB -Descending | Format-Table -AutoSize',
    'e230f4df72ccce830f7d037575f971d137b5ac8a7f8bd12d16ce3a6a774fb7bb',
    '',
    900,
    'system',
    DATETIME('now'),
    DATETIME('now')
);

