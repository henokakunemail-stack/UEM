# scripts/Read-AgentCommand.ps1
#
# Usage: . $PSScriptRoot/Read-AgentCommand.ps1
#        $env = Read-AgentCommand -Ws $wsAgent -CommandName 'update.apply' `
#                 -CancellationToken $ctSource.Token
#
# A freshly connected mock agent is sent whatever the server has queued for it
# before the test's own command. The filter module's reconnect hook is the one
# that fires on every connect: it re-pushes the compiled policy to any device
# that is not already reporting 'synced' at the current version, which is every
# device in an e2e run, because nothing has reported yet (SyncOnReconnect in
# server/modules/networkfilter/handler.go). So the first frame on a brand-new
# connection is filter.apply, not the command the test is there to check.
#
# Reading exactly one frame and asserting on it meant every e2e that started a
# mock agent failed with "expected X, got: filter.apply" -- a correct server,
# asserted against as though it were broken. This helper skips the frames the
# test is not looking for, with a bound so a missing command still fails.

# Reads from $Ws until a frame whose `command` is $CommandName arrives, or the
# timeout expires. Returns the decoded frame; throws otherwise.
#
# $Id is optional and narrows the match to the one frame the caller caused.
# Without it a test can match a stale frame the server sent earlier: the filter
# reconnect hook pushes the compiled policy at connect time, and if no policy
# existed yet that frame carries an empty rule set under a different version --
# so a sync test would match the connect-time frame and read zero rules from a
# server that had dispatched three. Both the filter envelope and the update
# command carry the version / task id as the envelope `id`, which is the value
# the caller already holds from the HTTP call that caused the dispatch.
function Read-AgentCommand {
    param(
        [Parameter(Mandatory)] $Ws,
        [Parameter(Mandatory)] [string]$CommandName,
        [Parameter(Mandatory)] $CancellationToken,
        [string]$Id,
        [int]$TimeoutMs = 8000
    )
    $deadline = [System.DateTime]::Now.AddMilliseconds($TimeoutMs)
    $buffer = New-Object byte[] 8192
    $seg = New-Object System.ArraySegment[byte] ($buffer, 0, $buffer.Length)

    while ($true) {
        $remaining = [int]([math]::Ceiling(($deadline - [System.DateTime]::Now).TotalMilliseconds))
        if ($remaining -le 0) {
            throw "Timed out waiting for '$CommandName' on the agent socket"
        }
        $recvTask = $Ws.ReceiveAsync($seg, $CancellationToken)
        $recvTask.Wait($remaining) | Out-Null

        $text = [System.Text.Encoding]::UTF8.GetString($buffer, 0, $recvTask.Result.Count)
        $env = $text | ConvertFrom-Json
        if ($env.command -ne $CommandName) { continue }
        if ($Id -and $env.id -ne $Id) {
            # A frame of the right type but from an earlier dispatch. Skipping it
            # silently would hide a real ordering bug, so it is named here.
            Write-Host "    [..] skipping stale '$CommandName' frame (id $($env.id)), waiting for '$Id'" -ForegroundColor DarkGray
            continue
        }
        return $env
    }
}
