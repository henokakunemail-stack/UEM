import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  Activity,
  AlertTriangle,
  Clock,
  FileText,
  HardDrive,
  Laptop,
  ListChecks,
  Plus,
  RefreshCw,
  Wrench,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import { useToast } from '../context/ToastContext'
import type {
  DeviceGroupDTO,
  DeviceDTO,
  MaintenanceJobDTO,
  MaintenanceProgressDTO,
  MaintenanceTaskDTO,
  TaskInfoDTO,
} from '../types/api'

// --- Presentation helpers (local, like describeInterval in TasksSchedulerPage) ---

function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1)
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`
}

function formatTime(v?: string | null): string {
  if (!v) return '—'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

// The job's own status vocabulary (model.go:84-89) mapped onto the four
// status-pill variants the stylesheet actually defines. Anything unexpected
// falls to 'primary' rather than rendering an unstyled pill.
function jobPill(status: string): string {
  if (status === 'completed') return 'online'
  if (status === 'failed') return 'danger'
  if (status === 'partial') return 'warning'
  return 'primary'
}

// Task statuses are a different vocabulary from job statuses (model.go:91-98),
// so they get their own map: 'completed' is the good outcome here, not
// 'online', and 'skipped' is a neutral outcome, not a failure.
function taskPill(status: string): string {
  if (status === 'completed') return 'online'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'primary'
  if (status === 'dispatched') return 'warning'
  return 'offline' // pending, skipped, anything unknown
}

const isRunningStatus = (s: string) => s === 'running' || s === 'pending' || s === 'dispatched'

// maintenance.Job.Status, as a type guard. JobProgress.status arrives as a bare
// string, so this is the one place the console decides whether a polled status
// is one the job vocabulary actually defines.
const isJobStatus = (s: string): s is MaintenanceJobDTO['status'] =>
  s === 'running' || s === 'completed' || s === 'partial' || s === 'failed'

export const MaintenancePage: React.FC = () => {
  // Dispatching a maintenance task to endpoints is RoleTechnician server-side
  // (mirrors PatchesPage's scan/install gate). A viewer can read job history
  // and task output but must not be offered a button that only ever 403s.
  const { can } = usePermission()
  const canAct = can('technician')

  const [jobs, setJobs] = useState<MaintenanceJobDTO[]>([])
  const [catalog, setCatalog] = useState<TaskInfoDTO[]>([])
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // Job detail
  const [detailJob, setDetailJob] = useState<MaintenanceJobDTO | null>(null)
  const [detailTasks, setDetailTasks] = useState<MaintenanceTaskDTO[]>([])
  const [progress, setProgress] = useState<MaintenanceProgressDTO | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [logTask, setLogTask] = useState<MaintenanceTaskDTO | null>(null)

  // New-job form
  const [isRunModalOpen, setIsRunModalOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [newRun, setNewRun] = useState({
    name: '',
    task_type: '',
    target_type: 'all',
    target_id: '',
  })

  // Disruptive-task gate. window.confirm could not hold the pending state, and
  // the dispatch below needs a real dialog so the button can show progress and
  // the operator can back out after the request is in flight.
  const [disruptivePending, setDisruptivePending] = useState(false)

  // Poll timer for the open job. Held in a ref so loadProgress can be a stable
  // callback and the effect that owns the interval does not restart on every
  // render of the detail state.
  const pollRef = useRef<number | null>(null)
  // Bumped by every openDetail/closeDetail, so a fetch that resolves after the
  // user has moved on is dropped instead of overwriting the newer selection or
  // re-arming a poll for a modal that is already closed.
  const detailReqRef = useRef(0)

  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  const loadProgress = useCallback(async (jobId: string) => {
    try {
      // Tasks are refetched with the progress, not just the progress: the
      // per-device table is the reason the modal exists, and a table frozen at
      // its open-time snapshot shows every row still 'pending' under a bar
      // climbing to 100%. A failed task read is not fatal — the bar is still
      // worth updating — so it is swallowed and the previous rows stay.
      const [p, tasks] = await Promise.all([
        api.getMaintenanceJobProgress(jobId),
        api.getMaintenanceJobTasks(jobId).catch(() => null),
      ])
      setProgress(p)
      if (tasks) setDetailTasks(tasks)
      // Re-read the job row too: the status flip from 'running' to a terminal
      // one happens in the job's own columns, and a list still reading
      // 'running' under a progress bar at 100% reads as a broken page.
      // A zero-task job that still reads 'running' has nothing left to
      // converge, so stopping there too keeps an empty run from ticking forever.
      if (p.status !== 'running' || (p.total_tasks === 0 && p.remaining === 0)) {
        stopPolling()
        // JobProgress.Status is a bare string on the wire, so narrow it against
        // the same vocabulary maintenance.Job uses before writing it back onto
        // a job row. An unrecognised value keeps the job looking running
        // rather than being written into a row that claims a status the server
        // never defined.
        const next = isJobStatus(p.status) ? p.status : 'running'
        setJobs((prev) => prev.map((j) => (j.id === jobId ? { ...j, status: next } : j)))
        setDetailJob((prev) => (prev && prev.id === jobId ? { ...prev, status: next } : prev))
      }
    } catch (err: any) {
      // A poll failure is not worth a toast every 3s; the next tick usually
      // succeeds. Only stop the loop if the job is genuinely gone.
      if (err?.message?.includes('404') || err?.message?.includes('not found')) {
        stopPolling()
        toast.error(err.message, 'Job Vanished')
      }
    }
  }, [stopPolling, toast])

  // Deliberately not a useCallback. The effect below owns no cleanup through
  // it, and ToastContext builds its context value as a bare object literal with
  // no useMemo — so a `toast` dep handed every toast in the app a new
  // loadData identity, the effect tore down, and its `return stopPolling` killed
  // the 3s poll openDetail had just started. House pattern is a plain async fn
  // with a []-dep effect; see PatchesPage.tsx:35-58.
  const loadData = async () => {
    setLoading(true)
    try {
      const [jobList, catalogList, groupList, deviceList] = await Promise.all([
        api.getMaintenanceJobs(50, 0).catch(() => []),
        api.getMaintenanceTaskCatalog().catch(() => []),
        api.getDeviceGroups().catch(() => []),
        api.getDevices(200, 0).catch(() => ({ devices: [], count: 0, total: 0, limit: 200, offset: 0 })),
      ])
      setJobs(jobList)
      setCatalog(catalogList)
      setGroups(groupList)
      setDevices(deviceList.devices || [])
    } catch (err: any) {
      const errMsg = err.message || 'Failed to load maintenance data'
      setMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, 'Load Failed')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  // Teardown lives in its own effect so the mount-once load above cannot
  // clear the interval. stopPolling is useCallback([]) and therefore stable,
  // so this effect also runs exactly once: it stops polling when the page
  // unmounts, and nothing else can reach the interval.
  useEffect(() => stopPolling, [stopPolling])

  const openDetail = async (job: MaintenanceJobDTO) => {
    const req = ++detailReqRef.current
    setDetailJob(job)
    setDetailTasks([])
    setProgress(null)
    setDetailLoading(true)
    try {
      const [tasks, prog] = await Promise.all([
        api.getMaintenanceJobTasks(job.id),
        api.getMaintenanceJobProgress(job.id).catch(() => null),
      ])
      // A newer openDetail, or a close, has moved on. Applying now would
      // render job A's rows under job B and re-arm a poll for a closed modal.
      if (req !== detailReqRef.current) return
      setDetailTasks(tasks)
      setProgress(prog)
      if (job.status === 'running') {
        stopPolling()
        pollRef.current = window.setInterval(() => loadProgress(job.id), 3000)
      }
    } catch (err: any) {
      if (req !== detailReqRef.current) return
      const errMsg = err.message || 'Failed to load job detail'
      setMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, 'Job Detail Failed')
    } finally {
      // Only the newest request owns the spinner; an abandoned one must not
      // clear it while the current fetch is still in flight.
      if (req === detailReqRef.current) setDetailLoading(false)
    }
  }

  const closeDetail = () => {
    detailReqRef.current++
    stopPolling()
    setDetailJob(null)
    setProgress(null)
  }

  // The currently selected catalog entry, so the disruptive warning and the
  // description under the chooser both read off the server's own copy of the
  // task rather than a hardcoded label.
  const selectedTask = catalog.find((c) => c.id === newRun.task_type) || null

  const openRunModal = () => {
    // Pre-fill the name from the server's own label so history stays readable
    // without the operator typing a title every time. The id comes from
    // `catalog`, not from the current selection: on a first open the modal's
    // state is still empty, and reading it here left the select showing
    // "Loading task catalog..." behind a live form.
    const fallbackId = catalog[0]?.id || ''
    const chosen = catalog.find((c) => c.id === newRun.task_type) || catalog[0] || null
    setNewRun({
      name: chosen ? chosen.label : '',
      task_type: chosen ? chosen.id : fallbackId,
      target_type: 'all',
      target_id: '',
    })
    setIsRunModalOpen(true)
  }

  // Validates the form, then dispatches. Split from the submit handler so the
  // disruptive ConfirmDialog can call the dispatch half after the operator says
  // yes, without re-validating or losing the form state.
  const dispatchRun = async () => {
    setSubmitting(true)
    try {
      const res = await api.runMaintenance({
        name: newRun.name || selectedTask?.label || 'Maintenance Job',
        task_type: newRun.task_type,
        target_type: newRun.target_type as 'device' | 'group' | 'all',
        target_id: newRun.target_type === 'all' ? '' : newRun.target_id,
      })
      const doneText = `Job dispatched: ${res.dispatched_live} of ${res.total_targets} devices live`
        + (res.skipped > 0 ? `, ${res.skipped} skipped as offline` : '')
        + '.'
      setMsg({ type: 'success', text: doneText })
      toast.success(doneText, 'Maintenance Started')
      setDisruptivePending(false)
      setIsRunModalOpen(false)
      await loadData()
      // Open it straight away: the operator's next question is always "is it
      // running, and how far along".
      await openDetail(res.job)
    } catch (err: any) {
      const errMsg = err.message || 'Failed to start maintenance job'
      setMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, 'Dispatch Failed')
    } finally {
      setSubmitting(false)
    }
  }

  const handleRun = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newRun.task_type) {
      toast.error('Pick a maintenance task first', 'Nothing To Run')
      return
    }
    // The server ignores target_id for 'all' but a group or device target
    // without one resolves to zero devices and dispatches nothing.
    if (newRun.target_type !== 'all' && !newRun.target_id) {
      toast.error(`Select a ${newRun.target_type} to target`, 'Nothing To Run')
      return
    }
    // The catalog is the source of truth for disruption, so the confirmation is
    // driven by what the server says rather than by a hardcoded list of ids.
    if (selectedTask?.disruptive) {
      setDisruptivePending(true)
      return
    }
    await dispatchRun()
  }

  // Bytes freed across the whole job. Prefer the server aggregate; fall back to
  // summing the loaded task rows when the progress read is unavailable.
  const jobBytesFreed = progress
    ? progress.bytes_freed
    : detailTasks.reduce((acc, t) => acc + (t.bytes_freed || 0), 0)

  const jobPercent = progress ? progress.percent : 0

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Device Maintenance</h2>
          <p className="page-subtitle">
            Fleet-wide disk cleanup, memory hygiene, log maintenance and health scans
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {canAct && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={openRunModal}
              disabled={catalog.length === 0}
            >
              <Plus size={16} />
              <span>New Maintenance Job</span>
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

      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Total Jobs</span>
            <ListChecks className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{jobs.length}</div>
          <span className="kpi-hint">Most recent 50 maintenance runs</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Running Now</span>
            <Activity className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">{jobs.filter((j) => j.status === 'running').length}</div>
          <span className="kpi-hint">Jobs with devices still reporting</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Task Types</span>
            <Wrench className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{catalog.length}</div>
          <span className="kpi-hint">Served by the server, not hardcoded</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Fleet Failed</span>
            <AlertTriangle className="kpi-icon text-danger" size={20} />
          </div>
          <div className="kpi-value text-danger">{jobs.reduce((a, j) => a + (j.failed || 0), 0)}</div>
          <span className="kpi-hint">Per-device task failures across these jobs</span>
        </div>
      </div>

      {/* Job history */}
      <div className="table-card">
        <div className="table-toolbar">
          <h3 className="section-title">Maintenance Job History</h3>
          <span className="last-sync">{jobs.length} job(s)</span>
        </div>
        <DataTable label="Maintenance job history">
          <table className="data-table">
            <thead>
              <tr>
                <th>Job</th>
                <th>Task</th>
                <th>Target</th>
                <th>Progress</th>
                <th>Bytes Freed</th>
                <th>Status</th>
                <th>Started</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={8} className="text-center py-8 text-muted">
                    <span className="spinner-inline"></span> Loading maintenance history...
                  </td>
                </tr>
              ) : jobs.length === 0 ? (
                <tr>
                  <td colSpan={8} className="text-center py-8 text-muted">
                    No maintenance jobs recorded yet.
                  </td>
                </tr>
              ) : (
                jobs.map((j) => {
                  const finished = (j.completed || 0) + (j.failed || 0) + (j.skipped || 0)
                  const pct = j.total_tasks > 0 ? Math.round((finished / j.total_tasks) * 100) : 0
                  const running = isRunningStatus(j.status)
                  return (
                    <tr key={j.id}>
                      <td>
                        <div className="font-semibold text-main">{j.name || 'Untitled job'}</div>
                        <div className="text-sm text-dim">by {j.created_by}</div>
                      </td>
                      <td>
                        <span className="os-badge">{catalog.find((c) => c.id === j.task_type)?.label || j.task_type}</span>
                      </td>
                      <td>
                        <span className="badge-target">{j.target_type}</span>
                      </td>
                      <td>
                        <div className="progress-bar-bg" title={`${finished} of ${j.total_tasks} done`}>
                          <div
                            className={`progress-bar-fill ${
                              j.failed > 0 ? 'danger' : running ? 'warning' : ''
                            }`}
                            style={{ width: `${pct}%` }}
                          ></div>
                        </div>
                        <div className="text-sm text-dim">
                          {j.completed} ok / {j.failed} failed / {j.skipped} skipped
                        </div>
                      </td>
                      <td className="text-sm text-muted">per-task in detail</td>
                      <td>
                        <span className={`status-pill ${jobPill(j.status)}`}>{j.status}</span>
                      </td>
                      <td className="timestamp-cell">{formatTime(j.started_at)}</td>
                      <td>
                        <div className="row-actions">
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => openDetail(j)}
                            title="Per-device task detail"
                          >
                            <Laptop size={14} />
                            <span>Detail</span>
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </DataTable>
      </div>

      {/* Job detail modal */}
      <Modal
        open={detailJob !== null}
        onClose={closeDetail}
        title={detailJob?.name || 'Maintenance Job'}
        size="lg"
        description={
          detailJob
            ? `${detailJob.task_type} · target ${detailJob.target_type} · started ${formatTime(
                detailJob.started_at,
              )}`
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={closeDetail}>
            Close
          </button>
        }
      >
        {/* Children props are evaluated while this renders, before Modal gets
            a chance to bail out on `open` — so the null check has to live here,
            not in a `detailJob!` assertion. */}
        {detailLoading ? (
          <div className="py-8 text-center text-muted">
            <span className="spinner-inline"></span> Loading per-device tasks...
          </div>
        ) : !detailJob ? null : (
          <>
            <div className="form-row">
              <div className="form-field">
                <span className="form-label">Status</span>
                <span className={`status-pill ${jobPill(detailJob.status)}`}>
                  {progress?.status || detailJob.status}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">Completed</span>
                <span className="text-main">
                  {progress ? `${progress.completed} / ${progress.total_tasks}` : '—'}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">Failed</span>
                <span className={progress && progress.failed > 0 ? 'text-danger' : 'text-main'}>
                  {progress ? progress.failed : '—'}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">Skipped (offline)</span>
                <span className="text-muted">{progress ? progress.skipped : '—'}</span>
              </div>
              <div className="form-field">
                <span className="form-label">Bytes Freed</span>
                <span className="text-success">{formatBytes(jobBytesFreed)}</span>
              </div>
              <div className="form-field">
                <span className="form-label">Reboot Required</span>
                <span className={progress && progress.reboot_required > 0 ? 'text-warning' : 'text-muted'}>
                  {progress ? `${progress.reboot_required} device(s)` : '—'}
                </span>
              </div>
            </div>

            <div className="form-field">
              <span className="form-label">Overall Progress — {jobPercent.toFixed(1)}%</span>
              <div className="progress-bar-bg">
                <div
                  className={`progress-bar-fill ${
                    progress && progress.failed > 0 ? 'danger' : detailJob.status === 'running' ? 'warning' : ''
                  }`}
                  style={{ width: `${Math.min(100, Math.max(0, jobPercent))}%` }}
                ></div>
              </div>
            </div>

            <DataTable label="Per-device tasks">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Device</th>
                    <th>Status</th>
                    <th>Step</th>
                    <th>Exit</th>
                    <th>Bytes Freed</th>
                    <th>Reboot</th>
                    <th>Log</th>
                  </tr>
                </thead>
                <tbody>
                  {detailTasks.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="text-center py-8 text-muted">
                        No task rows for this job yet.
                      </td>
                    </tr>
                  ) : (
                    detailTasks.map((t) => (
                      <tr key={t.id}>
                        <td>
                          <span className="font-semibold text-main">
                            {t.hostname || t.device_id.slice(0, 12)}
                          </span>
                        </td>
                        <td>
                          <span className={`status-pill ${taskPill(t.status)}`}>{t.status}</span>
                        </td>
                        <td className="text-sm text-muted">{t.step || '—'}</td>
                        <td>
                          {t.exit_code === null || t.exit_code === undefined ? (
                            <span className="text-dim">—</span>
                          ) : (
                            <span className="exit-code-badge">{t.exit_code}</span>
                          )}
                        </td>
                        <td className="text-sm text-success">
                          {t.bytes_freed > 0 ? formatBytes(t.bytes_freed) : '—'}
                        </td>
                        <td>
                          {t.reboot_required ? (
                            <span className="status-pill warning">reboot</span>
                          ) : (
                            <span className="text-dim">—</span>
                          )}
                        </td>
                        <td>
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => setLogTask(t)}
                            disabled={!t.output_log && !t.error_message}
                          >
                            <FileText size={14} />
                            <span>Log</span>
                          </button>
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </DataTable>
          </>
        )}
      </Modal>

      {/* Output log viewer */}
      <Modal
        open={logTask !== null}
        onClose={() => setLogTask(null)}
        title="Task Output"
        size="lg"
        description={
          logTask
            ? `${logTask.hostname || logTask.device_id} · ${logTask.task_type} · ${logTask.status}${
                logTask.step ? ` · step: ${logTask.step}` : ''
              }`
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setLogTask(null)}>
            Close
          </button>
        }
      >
        <pre className="terminal-log-output">
          {logTask?.output_log || logTask?.error_message || '(No output recorded)'}
        </pre>
      </Modal>

      {/* New job modal */}
      <Modal
        open={isRunModalOpen}
        onClose={() => !submitting && setIsRunModalOpen(false)}
        title="New Maintenance Job"
        size="md"
        dismissible={!submitting}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsRunModalOpen(false)}
              disabled={submitting}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="maintenance-run-form"
              className="btn btn-primary"
              disabled={submitting || catalog.length === 0}
            >
              {submitting ? <span className="btn-loading">Dispatching...</span> : 'Run Maintenance'}
            </button>
          </>
        }
      >
        <form id="maintenance-run-form" onSubmit={handleRun}>
          <div className="form-group">
            <label className="form-label" htmlFor="maint-name">
              Job Name
            </label>
            <input
              id="maint-name"
              type="text"
              className="form-input"
              placeholder={selectedTask?.label || 'e.g. Nightly Temp Cleanup'}
              value={newRun.name}
              onChange={(e) => setNewRun({ ...newRun, name: e.target.value })}
            />
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="maint-task">
              Maintenance Task
            </label>
            <select
              id="maint-task"
              className="form-select"
              required
              value={newRun.task_type}
              onChange={(e) => {
                const picked = e.target.value
                const info = catalog.find((c) => c.id === picked)
                setNewRun({
                  ...newRun,
                  task_type: picked,
                  // Re-label the default name as the choice changes, but
                  // do not clobber a name the operator already typed.
                  name:
                    newRun.name && newRun.name !== catalog.find((c) => c.id === newRun.task_type)?.label
                      ? newRun.name
                      : info?.label || '',
                })
              }}
            >
              {catalog.length === 0 && <option value="">Loading task catalog...</option>}
              {catalog.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                  {c.disruptive ? ' (disruptive)' : ''}
                </option>
              ))}
            </select>
            {/* Label and description come from the server's TaskCatalog,
                so a task type added server-side shows up here with no
                console change. */}
            {selectedTask && (
              <p className="form-hint">
                {selectedTask.disruptive ? 'Disruptive: ' : ''}
                {selectedTask.description}
              </p>
            )}
            {catalog.length === 0 && (
              <p className="form-hint">
                The server served an empty task catalog, so no maintenance task can be run.
              </p>
            )}
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="maint-scope">
              Target Scope
            </label>
            <select
              id="maint-scope"
              className="form-select"
              value={newRun.target_type}
              onChange={(e) => setNewRun({ ...newRun, target_type: e.target.value, target_id: '' })}
            >
              <option value="all">All Enrolled Devices</option>
              <option value="group">Device Group</option>
              <option value="device">Single Device</option>
            </select>
          </div>

          {newRun.target_type === 'group' && (
            <div className="form-group">
              <label className="form-label" htmlFor="maint-group">
                Group
              </label>
              <select
                id="maint-group"
                className="form-select"
                required
                value={newRun.target_id}
                onChange={(e) => setNewRun({ ...newRun, target_id: e.target.value })}
              >
                <option value="">-- Select Group --</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name} ({g.member_count} device{g.member_count === 1 ? '' : 's'})
                  </option>
                ))}
              </select>
              {groups.length === 0 && (
                <p className="form-hint">No device groups defined. Create one under Devices first.</p>
              )}
            </div>
          )}

          {newRun.target_type === 'device' && (
            <div className="form-group">
              <label className="form-label" htmlFor="maint-device">
                Device
              </label>
              <select
                id="maint-device"
                className="form-select"
                required
                value={newRun.target_id}
                onChange={(e) => setNewRun({ ...newRun, target_id: e.target.value })}
              >
                <option value="">-- Select Device --</option>
                {devices.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.hostname} ({d.os_name}, {d.status})
                  </option>
                ))}
              </select>
            </div>
          )}

          {selectedTask?.disruptive && (
            <div className="security-notice">
              <AlertTriangle size={14} />
              <span>The server marks this task disruptive. You will be asked to confirm before it runs.</span>
            </div>
          )}
        </form>
      </Modal>

      <ConfirmDialog
        open={disruptivePending}
        title="Run disruptive maintenance task?"
        message={
          selectedTask
            ? `"${selectedTask.label}" is flagged disruptive by the server and may interrupt users on every targeted device.\n\n${selectedTask.description}\n\nContinue with target "${
                newRun.target_type === 'all'
                  ? 'all devices'
                  : newRun.target_type === 'group'
                  ? groups.find((g) => g.id === newRun.target_id)?.name || newRun.target_id
                  : devices.find((d) => d.id === newRun.target_id)?.hostname || newRun.target_id
              }"?`
            : ''
        }
        confirmLabel="Run anyway"
        pending={submitting}
        onConfirm={dispatchRun}
        onCancel={() => setDisruptivePending(false)}
      />

      {/* Two standing caveats an operator otherwise has to discover the hard
          way: an offline target is not a failure, and the poll cadence. */}
      <div className="last-sync">
        <HardDrive size={12} />
        <span>Offline targets are recorded as skipped, not failed.</span>
        <Clock size={12} />
        <span>Running jobs refresh every 3 seconds.</span>
      </div>
    </div>
  )
}
