import React, { useEffect, useState } from 'react'
import {
  CalendarClock,
  Code2,
  FileCode,
  FileText,
  History,
  Laptop,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import type { DeviceRunDTO, ScheduleDTO, ScriptDTO, TaskRunDTO } from '../types/api'

// The server reads an interval trigger as a MINUTE count and multiplies it by
// time.Minute, so `schedule_expr` holds minutes -- not seconds, which is what
// this used to assume. An operator who typed the "3600" the field used to
// suggest was asking for a task every 2400 hours, and the console rendered the
// same number back as "Every 1h", so nothing on screen disagreed with anything
// else and nothing fired on time either.
function describeInterval(expr: string): string {
  const mins = Number(expr)
  if (!Number.isFinite(mins) || mins <= 0) return expr || '—'
  if (mins % 1440 === 0) return `${mins / 1440}d`
  if (mins % 60 === 0) return `${mins / 60}h`
  return `${mins}m`
}

export type SchedulerTab = 'scripts' | 'schedules' | 'runs'

interface TasksSchedulerPageProps {
  activeTab: SchedulerTab
  onTabChange: (tab: SchedulerTab) => void
}

export const TasksSchedulerPage: React.FC<TasksSchedulerPageProps> = ({ activeTab: subTab, onTabChange: setSubTab }) => {
  // Every script and schedule write on the server is RoleAdmin (only the
  // manual /trigger call is technician), so a viewer gets the read-only tabs
  // with no create/delete affordances that could only ever 403.
  const { can } = usePermission()
  const canAdmin = can('admin')
  const [scripts, setScripts] = useState<ScriptDTO[]>([])
  const [schedules, setSchedules] = useState<ScheduleDTO[]>([])
  const [runs, setRuns] = useState<TaskRunDTO[]>([])
  const [loading, setLoading] = useState(true)

  // Modals
  const [isScriptModalOpen, setIsScriptModalOpen] = useState(false)
  const [isScheduleModalOpen, setIsScheduleModalOpen] = useState(false)
  const [viewLogRun, setViewLogRun] = useState<DeviceRunDTO | null>(null)
  const [viewRunDevices, setViewRunDevices] = useState<TaskRunDTO | null>(null)
  const [runDevices, setRunDevices] = useState<DeviceRunDTO[]>([])
  const [devicesLoading, setDevicesLoading] = useState(false)

  // Script deletion moved off window.confirm so the dialog can hold a pending
  // state while the request is in flight.
  const [scriptPendingDelete, setScriptPendingDelete] = useState<{ id: string; name: string } | null>(null)
  const [deletingScript, setDeletingScript] = useState(false)

  // Form states
  const [newScript, setNewScript] = useState({
    name: '',
    description: '',
    script_type: 'powershell',
    script_content: '',
  })
  const [newSchedule, setNewSchedule] = useState({
    name: '',
    script_id: '',
    target_type: 'all',
    target_id: '',
    schedule_type: 'interval',
    // The server stores both interval and cron triggers in one `schedule_expr`
    // column, parsed per `schedule_type`. There is no separate interval_seconds
    // or cron_expr field on the wire. The interval branch reads MINUTES, so the
    // default here is the hourly value the old "3600 seconds" placeholder was
    // reaching for.
    schedule_expr: '60',
    // A schedule created from this modal is active by definition. Without this
    // the server decodes a missing key as false, the scheduler skips the job
    // forever, and the operator sees a Paused row under an "activated" toast.
    is_enabled: true,
  })
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  const loadData = async () => {
    setLoading(true)
    try {
      const [scList, schList, runList] = await Promise.all([
        api.getScripts().catch(() => []),
        api.getSchedules().catch(() => []),
        api.getTaskRuns().catch(() => []),
      ])
      setScripts(scList)
      setSchedules(schList)
      setRuns(runList)
    } catch (err: any) {
      setMsg({ type: 'error', text: err.message || 'Failed to load task scheduler data' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  const openRunDevices = async (run: TaskRunDTO) => {
    setViewRunDevices(run)
    setDevicesLoading(true)
    try {
      setRunDevices(await api.getRunDeviceRuns(run.id))
    } catch {
      setRunDevices([])
    } finally {
      setDevicesLoading(false)
    }
  }

  const handleCreateScript = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createScript(newScript)
      const successText = `Script '${newScript.name}' created successfully with SHA-256 hash.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Script Registered')
      setIsScriptModalOpen(false)
      setNewScript({ name: '', description: '', script_type: 'powershell', script_content: '' })
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to create script'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Creation Failed')
    }
  }

  const handleDeleteScript = async () => {
    if (!scriptPendingDelete) return
    const { id, name } = scriptPendingDelete
    setDeletingScript(true)
    try {
      await api.deleteScript(id)
      const infoText = `Script '${name}' deleted.`
      setMsg({ type: 'success', text: infoText })
      toast.info(infoText)
      setScriptPendingDelete(null)
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to delete script'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText)
    } finally {
      setDeletingScript(false)
    }
  }

  const handleCreateSchedule = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createSchedule(newSchedule)
      const successText = `Schedule '${newSchedule.name}' created and activated.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Schedule Activated')
      setIsScheduleModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to create schedule'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Schedule Failed')
    }
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Task Scheduler & Script Repository</h2>
          <p className="page-subtitle">
            Automated maintenance scripts, recurring background jobs, and distributed fleet executions
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {canAdmin && subTab === 'scripts' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsScriptModalOpen(true)}>
              <Plus size={16} />
              <span>New Script</span>
            </button>
          )}
          {canAdmin && subTab === 'schedules' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsScheduleModalOpen(true)}>
              <Plus size={16} />
              <span>New Schedule</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label="Dismiss message">
            <X size={14} />
          </button>
        </div>
      )}

      {/* Sub Tabs */}
      <div className="tabs-nav">
        <button
          type="button"
          className={`tab-btn ${subTab === 'scripts' ? 'active' : ''}`}
          onClick={() => setSubTab('scripts')}
        >
          <Code2 size={16} />
          <span>Script Repository ({scripts.length})</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'schedules' ? 'active' : ''}`}
          onClick={() => setSubTab('schedules')}
        >
          <CalendarClock size={16} />
          <span>Schedules ({schedules.length})</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'runs' ? 'active' : ''}`}
          onClick={() => setSubTab('runs')}
        >
          <History size={16} />
          <span>Execution History ({runs.length})</span>
        </button>
      </div>

      {/* Tab 1: Script Repository */}
      {subTab === 'scripts' && (
        <div className="table-card">
          <DataTable label="Scripts">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Script Name</th>
                  <th>Shell Type</th>
                  <th>Description</th>
                  <th>SHA-256 Checksum</th>
                  <th>Author</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {scripts.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-8 text-muted">
                      No scripts in repository. Click "New Script" to create one.
                    </td>
                  </tr>
                ) : (
                  scripts.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div className="font-semibold text-main">
                          <FileCode size={16} className="text-primary" />{' '}
                          <span>{s.name}</span>
                        </div>
                      </td>
                      <td>
                        <span className="os-badge">{s.script_type}</span>
                      </td>
                      <td>{s.description || '—'}</td>
                      <td>
                        <span className="font-mono text-sm text-dim" title={s.sha256_hash}>
                          {s.sha256_hash ? s.sha256_hash.slice(0, 16) + '...' : '—'}
                        </span>
                      </td>
                      <td>{s.created_by}</td>
                      <td>
                        {canAdmin && (
                          <button
                            type="button"
                            className="btn-action text-danger"
                            onClick={() => setScriptPendingDelete({ id: s.id, name: s.name })}
                            title="Delete script"
                          >
                            <Trash2 size={14} />
                            <span>Delete</span>
                          </button>
                        )}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Tab 2: Schedules */}
      {subTab === 'schedules' && (
        <div className="table-card">
          <DataTable label="Schedules">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Schedule Name</th>
                  <th>Script</th>
                  <th>Target Fleet</th>
                  <th>Frequency</th>
                  <th>Status</th>
                  <th>Next Execution</th>
                </tr>
              </thead>
              <tbody>
                {schedules.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-8 text-muted">
                      No recurring schedules configured.
                    </td>
                  </tr>
                ) : (
                  schedules.map((sch) => (
                    <tr key={sch.id}>
                      <td className="font-semibold text-main">{sch.name}</td>
                      <td>{sch.script_name || sch.script_id.slice(0, 12)}</td>
                      <td>
                        <span className="badge-target">{sch.target_type}</span>
                      </td>
                      <td>
                        {sch.schedule_type === 'cron'
                          ? `Cron: ${sch.schedule_expr}`
                          : `Every ${describeInterval(sch.schedule_expr)}`}
                      </td>
                      <td>
                        <span className={`status-pill ${sch.is_enabled ? 'online' : 'offline'}`}>
                          {sch.is_enabled ? 'Active' : 'Paused'}
                        </span>
                      </td>
                      <td className="text-sm text-muted">
                        {sch.next_run_at ? new Date(sch.next_run_at).toLocaleString() : '—'}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Tab 3: Execution History */}
      {subTab === 'runs' && (
        <div className="table-card">
          <DataTable label="Execution history">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Run ID</th>
                  <th>Schedule</th>
                  <th>Script</th>
                  <th>Status</th>
                  <th>Triggered At</th>
                  <th>Completed At</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {runs.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      No execution logs recorded yet.
                    </td>
                  </tr>
                ) : (
                  runs.map((r) => (
                    <tr key={r.id}>
                      <td className="font-mono text-sm">{r.id.slice(0, 12)}...</td>
                      <td>{r.schedule_name || r.schedule_id.slice(0, 12)}</td>
                      <td>{r.script_name || r.script_id.slice(0, 12)}</td>
                      <td>
                        <span
                          className={`status-pill ${
                            r.status === 'completed'
                              ? 'online'
                              : r.status === 'failed'
                                ? 'danger'
                                : 'warning'
                          }`}
                        >
                          {r.status}
                        </span>
                      </td>
                      <td className="text-sm text-muted">
                        {new Date(r.triggered_at).toLocaleString()}
                      </td>
                      <td className="text-sm text-muted">
                        {r.completed_at ? new Date(r.completed_at).toLocaleString() : '—'}
                      </td>
                      <td>
                        {/* A run has no device or exit code of its own — that
                            lives on the per-device rows underneath it. */}
                        <button
                          type="button"
                          className="btn-action"
                          onClick={() => openRunDevices(r)}
                        >
                          <Laptop size={14} />
                          <span>Per-Device</span>
                        </button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Per-device results under one run */}
      <Modal
        open={viewRunDevices !== null}
        onClose={() => setViewRunDevices(null)}
        title="Per-Device Results"
        size="lg"
        description={
          viewRunDevices
            ? `${viewRunDevices.schedule_name || viewRunDevices.schedule_id} · ${
                viewRunDevices.script_name || viewRunDevices.script_id
              }`
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setViewRunDevices(null)}>
            Close
          </button>
        }
      >
        {devicesLoading ? (
          <div className="py-8 text-center text-muted">
            <div className="spinner-inline"></div> Loading device results...
          </div>
        ) : runDevices.length === 0 ? (
          <div className="py-8 text-center text-muted">No device rows for this run yet.</div>
        ) : (
          <DataTable label="Per-device results">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Site</th>
                  <th>Status</th>
                  <th>Exit Code</th>
                  <th>Log Output</th>
                </tr>
              </thead>
              <tbody>
                {runDevices.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <span className="font-semibold text-main">
                        {d.hostname || d.device_id.slice(0, 12)}
                      </span>
                    </td>
                    <td>{d.site || '—'}</td>
                    <td>
                      <span
                        className={`status-pill ${
                          d.status === 'success'
                            ? 'online'
                            : d.status === 'failed'
                              ? 'danger'
                              : 'warning'
                        }`}
                      >
                        {d.status}
                      </span>
                    </td>
                    <td className="font-mono">
                      {d.exit_code === null || d.exit_code === undefined ? '—' : d.exit_code}
                    </td>
                    <td>
                      <button
                        type="button"
                        className="btn-action"
                        onClick={() => setViewLogRun(d)}
                      >
                        <FileText size={14} />
                        <span>View Log</span>
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </DataTable>
        )}
      </Modal>

      {/* Create Script Modal */}
      <Modal
        open={isScriptModalOpen}
        onClose={() => setIsScriptModalOpen(false)}
        title="Create Maintenance Script"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsScriptModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="script-form" className="btn btn-primary">
              Save Script
            </button>
          </>
        }
      >
        <form id="script-form" onSubmit={handleCreateScript}>
          <div className="form-group">
            <label className="form-label" htmlFor="script-title">
              Script Title
            </label>
            <input
              id="script-title"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Flush DNS & Restart Spooler"
              value={newScript.name}
              onChange={(e) => setNewScript({ ...newScript, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-shell">
              Shell Environment
            </label>
            <select
              id="script-shell"
              className="form-select"
              value={newScript.script_type}
              onChange={(e) => setNewScript({ ...newScript, script_type: e.target.value })}
            >
              <option value="powershell">PowerShell (Windows)</option>
              <option value="cmd">Command Prompt (CMD)</option>
              <option value="bash">Bash (Linux / macOS)</option>
              <option value="sh">POSIX Shell (/bin/sh)</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-description">
              Description (Optional)
            </label>
            <input
              id="script-description"
              type="text"
              className="form-input"
              placeholder="Purpose of this script"
              value={newScript.description}
              onChange={(e) => setNewScript({ ...newScript, description: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-body">
              Script Body
            </label>
            <textarea
              id="script-body"
              className="form-textarea font-mono"
              rows={8}
              required
              placeholder="# Enter script commands here..."
              value={newScript.script_content}
              onChange={(e) => setNewScript({ ...newScript, script_content: e.target.value })}
            />
          </div>
        </form>
      </Modal>

      {/* Create Schedule Modal */}
      <Modal
        open={isScheduleModalOpen}
        onClose={() => setIsScheduleModalOpen(false)}
        title="Schedule Recurring Maintenance"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsScheduleModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="schedule-form" className="btn btn-primary">
              Activate Schedule
            </button>
          </>
        }
      >
        <form id="schedule-form" onSubmit={handleCreateSchedule}>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-name">
              Schedule Name
            </label>
            <input
              id="schedule-name"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Daily Temp File Cleanup"
              value={newSchedule.name}
              onChange={(e) => setNewSchedule({ ...newSchedule, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-script">
              Target Script
            </label>
            <select
              id="schedule-script"
              className="form-select"
              required
              value={newSchedule.script_id}
              onChange={(e) => setNewSchedule({ ...newSchedule, script_id: e.target.value })}
            >
              <option value="">-- Select Script --</option>
              {scripts.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} ({s.script_type})
                </option>
              ))}
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-target">
              Target Fleet
            </label>
            <select
              id="schedule-target"
              className="form-select"
              value={newSchedule.target_type}
              onChange={(e) => setNewSchedule({ ...newSchedule, target_type: e.target.value })}
            >
              <option value="all">All Enrolled Devices</option>
              <option value="group">Branch Site / Group</option>
              <option value="device">Single Device</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-trigger">
              Trigger Type
            </label>
            <select
              id="schedule-trigger"
              className="form-select"
              value={newSchedule.schedule_type}
              onChange={(e) =>
                setNewSchedule({
                  ...newSchedule,
                  schedule_type: e.target.value,
                  // Pre-fill the matching expression shape so the field
                  // below is never an interval number typed into a cron
                  // schedule (or vice versa).
                  schedule_expr: e.target.value === 'cron' ? '0 0 * * *' : '60',
                })
              }
            >
              <option value="interval">Fixed Interval</option>
              <option value="cron">Cron Expression</option>
              <option value="once">Run Once</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-expr">
              {newSchedule.schedule_type === 'cron' ? 'Cron Expression' : 'Interval (minutes)'}
            </label>
            <input
              id="schedule-expr"
              type="text"
              className="form-input"
              required
              placeholder={newSchedule.schedule_type === 'cron' ? '0 3 * * * (03:00 daily)' : '60'}
              value={newSchedule.schedule_expr}
              onChange={(e) =>
                setNewSchedule({
                  ...newSchedule,
                  schedule_expr:
                    newSchedule.schedule_type === 'interval'
                      ? String(Number(e.target.value) || 0)
                      : e.target.value,
                })
              }
            />
          </div>
        </form>
      </Modal>

      {/* View Output Log Modal */}
      <Modal
        open={viewLogRun !== null}
        onClose={() => setViewLogRun(null)}
        title="Job Execution Output"
        size="lg"
        description={viewLogRun?.id}
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setViewLogRun(null)}>
            Close
          </button>
        }
      >
        <pre className="terminal-log-output">
          {viewLogRun?.output_log || viewLogRun?.error_message || '(No output recorded)'}
        </pre>
      </Modal>

      <ConfirmDialog
        open={scriptPendingDelete !== null}
        title="Delete script"
        message={
          scriptPendingDelete
            ? `Delete script '${scriptPendingDelete.name}'? Its schedules will stop resolving and this cannot be undone.`
            : ''
        }
        confirmLabel="Delete"
        pending={deletingScript}
        onConfirm={handleDeleteScript}
        onCancel={() => setScriptPendingDelete(null)}
      />
    </div>
  )
}
