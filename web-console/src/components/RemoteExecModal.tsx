import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  AlertCircle,
  ChevronRight,
  Clock,
  Play,
  RefreshCw,
  Terminal,
} from 'lucide-react'
import { api } from '../services/api'
import { DataTable, Modal } from './ui'
import type { DeviceDTO, RemoteExecutionDTO } from '../types/api'

interface RemoteExecModalProps {
  device: DeviceDTO | null
  onClose: () => void
}

export const RemoteExecModal: React.FC<RemoteExecModalProps> = ({ device, onClose }) => {
  const [shell, setShell] = useState<string>('powershell')
  const [command, setCommand] = useState<string>('')
  const [timeoutSec, setTimeoutSec] = useState<number>(60)
  const [running, setRunning] = useState(false)
  const [history, setHistory] = useState<RemoteExecutionDTO[]>([])
  const [selectedExec, setSelectedExec] = useState<RemoteExecutionDTO | null>(null)
  const [error, setError] = useState<string | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const availableShells =
    device?.os_name === 'windows'
      ? [
          { value: 'powershell', label: 'PowerShell' },
          { value: 'cmd', label: 'Command Prompt' },
        ]
      : device?.os_name === 'macos'
        ? [
            { value: 'bash', label: 'Bash' },
            { value: 'sh', label: 'Sh' },
          ]
        : [
            { value: 'bash', label: 'Bash' },
            { value: 'sh', label: 'Sh' },
          ]

  const fetchHistory = useCallback(async () => {
    if (!device) return
    try {
      const rows = await api.getExecutions(device.id)
      setHistory(rows || [])
      if (selectedExec) {
        const updated = (rows || []).find((r) => r.id === selectedExec.id)
        if (updated) {
          setSelectedExec(updated)
        }
      }
    } catch {
      // Silent fail: history refresh is best-effort
    }
  }, [device, selectedExec])

  useEffect(() => {
    if (!device) return
    setShell(device.os_name === 'windows' ? 'powershell' : 'bash')
    setCommand('')
    setError(null)
    setSelectedExec(null)
    fetchHistory()
  }, [device, fetchHistory])

  // Poll while any execution is still running
  useEffect(() => {
    const hasRunning = history.some((h) => h.status === 'running' || h.status === 'pending')
    if (hasRunning && !pollRef.current) {
      pollRef.current = setInterval(fetchHistory, 1500)
    } else if (!hasRunning && pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
    }
  }, [history, fetchHistory])

  if (!device) return null

  const handleRun = async () => {
    if (!command.trim()) {
      setError('Command cannot be empty.')
      return
    }
    setRunning(true)
    setError(null)
    try {
      const res = await api.runRemoteCommand(device.id, shell, command, timeoutSec)
      setSelectedExec(res.execution)
      await fetchHistory()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to dispatch command'
      setError(msg)
    } finally {
      setRunning(false)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      handleRun()
    }
  }

  const statusBadge = (status: string) => {
    const cls =
      status === 'completed'
        ? 'status-success'
        : status === 'failed' || status === 'timeout'
          ? 'status-failed'
          : 'status-running'
    return <span className={`status-pill ${cls}`}>{status.toUpperCase()}</span>
  }

  return (
    <Modal
      open={device !== null}
      onClose={onClose}
      size="xl"
      title={
        <span className="modal-title-group">
          <Terminal size={20} /> Remote Command Execution
        </span>
      }
      description={
        <>
          {device.hostname} · {device.os_name} ·{' '}
          <span className="font-mono">{device.id.substring(0, 16)}</span>
        </>
      }
      // A dispatched command is in flight: only a deliberate click may navigate
      // away, and Escape must not take the result with it.
      dismissible={!running}
    >
      <div className="modal-body">
        {error && (
          <div className="notification-banner error" role="alert">
            <AlertCircle size={18} />
            <span>{error}</span>
          </div>
        )}

        <div className="exec-form">
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="exec-shell">
                Shell Runtime
              </label>
              <select
                id="exec-shell"
                className="select-input"
                value={shell}
                onChange={(e) => setShell(e.target.value)}
              >
                {availableShells.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-field" style={{ maxWidth: '180px' }}>
              <label className="form-label" htmlFor="exec-timeout">
                <Clock size={12} style={{ display: 'inline', marginRight: 4 }} />
                Timeout (s)
              </label>
              <input
                id="exec-timeout"
                type="number"
                className="text-input"
                min={5}
                max={300}
                value={timeoutSec}
                onChange={(e) => setTimeoutSec(Number(e.target.value))}
              />
            </div>
          </div>

          <div className="form-field">
            <label className="form-label" htmlFor="exec-command">
              Command Payload
            </label>
            <textarea
              id="exec-command"
              className="exec-textarea"
              rows={4}
              placeholder={
                shell === 'powershell'
                  ? 'Get-Service | Where-Object Status -eq "Running"'
                  : 'systemctl status --type=service --state=running'
              }
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              onKeyDown={handleKeyDown}
              spellCheck={false}
            />
            <span className="form-hint">Ctrl + Enter to execute</span>
          </div>

          <button
            type="button"
            className="btn btn-primary"
            onClick={handleRun}
            disabled={running || !command.trim()}
          >
            {running ? <RefreshCw size={16} className="spinning" /> : <Play size={16} />}
            <span>{running ? 'Dispatching...' : 'Run Command'}</span>
          </button>
        </div>

        {selectedExec && (
          <div className="exec-output-section">
            <div className="exec-output-header">
              <h3 className="section-title">Execution Output</h3>
              {statusBadge(selectedExec.status)}
              {selectedExec.exit_code !== null && selectedExec.exit_code !== undefined && (
                <span className="exit-code-badge">Exit Code: {selectedExec.exit_code}</span>
              )}
            </div>
            <div className="exec-output-terminal" aria-live="polite">
              {selectedExec.output ? (
                <pre className="terminal-pre">{selectedExec.output}</pre>
              ) : (
                <span className="terminal-placeholder">
                  No output captured. {selectedExec.error_message || ''}
                </span>
              )}
              {selectedExec.error_message && selectedExec.output && (
                <pre className="terminal-pre terminal-error">{selectedExec.error_message}</pre>
              )}
            </div>
          </div>
        )}

        <div className="exec-history-section">
          <div className="exec-history-header">
            <h3 className="section-title">Execution History</h3>
            <button type="button" className="btn btn-sm btn-secondary" onClick={fetchHistory}>
              <RefreshCw size={14} />
              <span>Refresh</span>
            </button>
          </div>
          {history.length === 0 ? (
            <div className="empty-state">
              <Terminal size={28} />
              <p>No remote commands executed on this endpoint yet.</p>
            </div>
          ) : (
            <DataTable label="Remote command execution history">
              <table>
                <thead>
                  <tr>
                    <th>Status</th>
                    <th>Command</th>
                    <th>Shell</th>
                    <th>Operator</th>
                    <th>Exit</th>
                    <th>Started</th>
                    <th>
                      <span className="form-hint">Output</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {history.map((h) => (
                    <tr
                      key={h.id}
                      className={`exec-row ${selectedExec?.id === h.id ? 'row-selected' : ''}`}
                    >
                      <td>{statusBadge(h.status)}</td>
                      <td>
                        <span
                          className="font-mono text-sm exec-command-cell"
                          title={h.command_text}
                        >
                          {h.command_text.length > 64
                            ? h.command_text.substring(0, 64) + '...'
                            : h.command_text}
                        </span>
                      </td>
                      <td>
                        <span className="shell-badge">{h.shell_type}</span>
                      </td>
                      <td>{h.operator_name || h.operator_id.substring(0, 8)}</td>
                      <td className="font-mono text-sm">
                        {h.exit_code !== null && h.exit_code !== undefined ? h.exit_code : '—'}
                      </td>
                      <td className="timestamp-cell">
                        {new Date(h.started_at).toLocaleString()}
                      </td>
                      <td className="text-right">
                        <button
                          type="button"
                          className="btn btn-sm btn-secondary"
                          onClick={() => setSelectedExec(h)}
                          title="View Output"
                          aria-label={`View output for ${h.command_text}`}
                        >
                          <ChevronRight size={12} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </DataTable>
          )}
        </div>
      </div>
    </Modal>
  )
}
