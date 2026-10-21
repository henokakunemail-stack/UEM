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
import { settleAll } from '../services/settled'
import { useT } from '../i18n/core'
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

// Job and task statuses, and target types, arrive as bare server tokens. The
// token is what the server and the pill colour logic use; only the text shown
// goes through t(). An unknown token is shown as-is.
const STATUS_KEYS: Record<string, string> = {
  running: 'maintenance.status_running',
  completed: 'maintenance.status_completed',
  partial: 'maintenance.status_partial',
  failed: 'maintenance.status_failed',
  pending: 'maintenance.status_pending',
  dispatched: 'maintenance.status_dispatched',
  skipped: 'maintenance.status_skipped',
}

const TARGET_KEYS: Record<string, string> = {
  all: 'maintenance.target_all',
  group: 'maintenance.target_group',
  device: 'maintenance.target_device',
}

type TFn = (key: string, vars?: Record<string, string | number>) => string

const statusLabel = (t: TFn, s: string) => (STATUS_KEYS[s] ? t(STATUS_KEYS[s]) : s)
const targetLabel = (t: TFn, s: string) => (TARGET_KEYS[s] ? t(TARGET_KEYS[s]) : s)

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
  const { t } = useT()

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
        toast.error(err.message, t('maintenance.job_vanished_title'))
      }
    }
  }, [stopPolling, toast, t])

  // Deliberately not a useCallback. The effect below owns no cleanup through
  // it, and ToastContext builds its context value as a bare object literal with
  // no useMemo — so a `toast` dep handed every toast in the app a new
  // loadData identity, the effect tore down, and its `return stopPolling` killed
  // the 3s poll openDetail had just started. House pattern is a plain async fn
  // with a []-dep effect; see PatchesPage.tsx:35-58.
  const [stats, setStats] = useState<{ total: number; running: number; failed_tasks: number } | null>(null)
  const [loadFailed, setLoadFailed] = useState<string[]>([])

  const loadData = async () => {
    setLoading(true)
    try {
      // Each section keeps what it had when it fails, and is named in a
      // banner. A failure used to read as "no jobs" or an empty target list.
      const { data, failed } = await settleAll({
        jobs: () => api.getMaintenanceJobs(50, 0),
        stats: () => api.getMaintenanceStats(),
        catalog: () => api.getMaintenanceTaskCatalog(),
        groups: () => api.getDeviceGroups(),
        devices: () => api.getDevices(200, 0),
      })
      if (data.jobs) setJobs(data.jobs)
      if (data.stats) setStats(data.stats)
      if (data.catalog) setCatalog(data.catalog)
      if (data.groups) setGroups(data.groups)
      if (data.devices) setDevices(data.devices.devices || [])
      setLoadFailed(failed.map((k) => t(`maintenance.section_${k}`)))
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
      const errMsg = err.message || t('maintenance.detail_failed')
      setMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, t('maintenance.detail_failed_title'))
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
        // An empty name is named by the server from the task's own label.
        name: newRun.name || selectedTask?.label || '',
        task_type: newRun.task_type,
        target_type: newRun.target_type as 'device' | 'group' | 'all',
        target_id: newRun.target_type === 'all' ? '' : newRun.target_id,
      })
      const doneText = res.skipped > 0
        ? t('maintenance.dispatched_with_skipped', { live: res.dispatched_live, total: res.total_targets, skipped: res.skipped })
        : t('maintenance.dispatched', { live: res.dispatched_live, total: res.total_targets })
      setMsg({ type: 'success', text: doneText })
      toast.success(doneText, t('maintenance.started_title'))
      setDisruptivePending(false)
      setIsRunModalOpen(false)
      await loadData()
      // Open it straight away: the operator's next question is always "is it
      // running, and how far along".
      await openDetail(res.job)
    } catch (err: any) {
      const errMsg = err.message || t('maintenance.start_failed')
      setMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, t('maintenance.dispatch_failed_title'))
    } finally {
      setSubmitting(false)
    }
  }

  const handleRun = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newRun.task_type) {
      toast.error(t('maintenance.pick_task'), t('maintenance.nothing_to_run'))
      return
    }
    // The server ignores target_id for 'all' but a group or device target
    // without one resolves to zero devices and dispatches nothing.
    if (newRun.target_type !== 'all' && !newRun.target_id) {
      toast.error(
        t(newRun.target_type === 'group' ? 'maintenance.select_group_target' : 'maintenance.select_device_target'),
        t('maintenance.nothing_to_run'),
      )
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
          <h2 className="page-title">{t('maintenance.title')}</h2>
          <p className="page-subtitle">
            {t('maintenance.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('maintenance.refresh')}</span>
          </button>
          {canAct && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={openRunModal}
              disabled={catalog.length === 0}
            >
              <Plus size={16} />
              <span>{t('maintenance.new_job')}</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label={t('maintenance.dismiss')}>
            <X size={14} />
          </button>
        </div>
      )}

      {loadFailed.length > 0 && (
        <div className="alert-banner alert-warning" role="alert">
          <span>{t('common.load_failed_sections', { sections: loadFailed.join(', ') })}</span>
        </div>
      )}

      {/* Totals over every job, not the 50 the history table shows. */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('maintenance.kpi_total')}</span>
            <ListChecks className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{stats ? stats.total : '—'}</div>
          <span className="kpi-hint">{t('maintenance.kpi_total_hint')}</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('maintenance.kpi_running')}</span>
            <Activity className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">{stats ? stats.running : '—'}</div>
          <span className="kpi-hint">{t('maintenance.kpi_running_hint')}</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('maintenance.kpi_task_types')}</span>
            <Wrench className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{catalog.length}</div>
          <span className="kpi-hint">{t('maintenance.kpi_task_types_hint')}</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('maintenance.kpi_failed')}</span>
            <AlertTriangle className="kpi-icon text-danger" size={20} />
          </div>
          <div className="kpi-value text-danger">{stats ? stats.failed_tasks : '—'}</div>
          <span className="kpi-hint">{t('maintenance.kpi_failed_hint')}</span>
        </div>
      </div>

      {/* Job history */}
      <div className="table-card">
        <div className="table-toolbar">
          <h3 className="section-title">{t('maintenance.history_title')}</h3>
          <span className="last-sync">{t('maintenance.job_count', { count: stats ? stats.total : jobs.length })}</span>
        </div>
        <DataTable label={t('maintenance.history_table_label')}>
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('maintenance.col_job')}</th>
                <th>{t('maintenance.col_task')}</th>
                <th>{t('maintenance.col_target')}</th>
                <th>{t('maintenance.col_progress')}</th>
                <th>{t('maintenance.col_bytes_freed')}</th>
                <th>{t('maintenance.col_status')}</th>
                <th>{t('maintenance.col_started')}</th>
                <th>{t('maintenance.col_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={8} className="text-center py-8 text-muted">
                    <span className="spinner-inline"></span> {t('maintenance.loading_history')}
                  </td>
                </tr>
              ) : jobs.length === 0 ? (
                <tr>
                  <td colSpan={8} className="text-center py-8 text-muted">
                    {t('maintenance.empty_history')}
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
                        <div className="font-semibold text-main">{j.name || t('maintenance.untitled_job')}</div>
                        <div className="text-sm text-dim">{t('maintenance.created_by', { user: j.created_by })}</div>
                      </td>
                      <td>
                        <span className="os-badge">{catalog.find((c) => c.id === j.task_type)?.label || j.task_type}</span>
                      </td>
                      <td>
                        <span className="badge-target">{targetLabel(t, j.target_type)}</span>
                      </td>
                      <td>
                        <div className="progress-bar-bg" title={t('maintenance.progress_title', { finished, total: j.total_tasks })}>
                          <div
                            className={`progress-bar-fill ${
                              j.failed > 0 ? 'danger' : running ? 'warning' : ''
                            }`}
                            style={{ width: `${pct}%` }}
                          ></div>
                        </div>
                        <div className="text-sm text-dim">
                          {t('maintenance.progress_summary', { ok: j.completed, failed: j.failed, skipped: j.skipped })}
                        </div>
                      </td>
                      <td className="text-sm text-muted">{t('maintenance.bytes_per_task')}</td>
                      <td>
                        <span className={`status-pill ${jobPill(j.status)}`}>{statusLabel(t, j.status)}</span>
                      </td>
                      <td className="timestamp-cell">{formatTime(j.started_at)}</td>
                      <td>
                        <div className="row-actions">
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => openDetail(j)}
                            title={t('maintenance.detail_title_attr')}
                          >
                            <Laptop size={14} />
                            <span>{t('maintenance.detail')}</span>
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
        title={detailJob?.name || t('maintenance.job_fallback_title')}
        size="lg"
        description={
          detailJob
            ? t('maintenance.detail_description', {
                task: detailJob.task_type,
                target: targetLabel(t, detailJob.target_type),
                started: formatTime(detailJob.started_at),
              })
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={closeDetail}>
            {t('common.close')}
          </button>
        }
      >
        {/* Children props are evaluated while this renders, before Modal gets
            a chance to bail out on `open` — so the null check has to live here,
            not in a `detailJob!` assertion. */}
        {detailLoading ? (
          <div className="py-8 text-center text-muted">
            <span className="spinner-inline"></span> {t('maintenance.loading_tasks')}
          </div>
        ) : !detailJob ? null : (
          <>
            <div className="form-row">
              <div className="form-field">
                <span className="form-label">{t('maintenance.col_status')}</span>
                <span className={`status-pill ${jobPill(detailJob.status)}`}>
                  {statusLabel(t, progress?.status || detailJob.status)}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">{t('maintenance.field_completed')}</span>
                <span className="text-main">
                  {progress ? `${progress.completed} / ${progress.total_tasks}` : '—'}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">{t('maintenance.field_failed')}</span>
                <span className={progress && progress.failed > 0 ? 'text-danger' : 'text-main'}>
                  {progress ? progress.failed : '—'}
                </span>
              </div>
              <div className="form-field">
                <span className="form-label">{t('maintenance.field_skipped')}</span>
                <span className="text-muted">{progress ? progress.skipped : '—'}</span>
              </div>
              <div className="form-field">
                <span className="form-label">{t('maintenance.col_bytes_freed')}</span>
                <span className="text-success">{formatBytes(jobBytesFreed)}</span>
              </div>
              <div className="form-field">
                <span className="form-label">{t('maintenance.field_reboot')}</span>
                <span className={progress && progress.reboot_required > 0 ? 'text-warning' : 'text-muted'}>
                  {progress ? t('maintenance.reboot_devices', { count: progress.reboot_required }) : '—'}
                </span>
              </div>
            </div>

            <div className="form-field">
              <span className="form-label">{t('maintenance.overall_progress', { percent: jobPercent.toFixed(1) })}</span>
              <div className="progress-bar-bg">
                <div
                  className={`progress-bar-fill ${
                    progress && progress.failed > 0 ? 'danger' : detailJob.status === 'running' ? 'warning' : ''
                  }`}
                  style={{ width: `${Math.min(100, Math.max(0, jobPercent))}%` }}
                ></div>
              </div>
            </div>

            <DataTable label={t('maintenance.tasks_table_label')}>
              <table className="data-table">
                <thead>
                  <tr>
                    <th>{t('maintenance.col_device')}</th>
                    <th>{t('maintenance.col_status')}</th>
                    <th>{t('maintenance.col_step')}</th>
                    <th>{t('maintenance.col_exit')}</th>
                    <th>{t('maintenance.col_bytes_freed')}</th>
                    <th>{t('maintenance.col_reboot')}</th>
                    <th>{t('maintenance.col_log')}</th>
                  </tr>
                </thead>
                <tbody>
                  {detailTasks.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="text-center py-8 text-muted">
                        {t('maintenance.empty_tasks')}
                      </td>
                    </tr>
                  ) : (
                    detailTasks.map((task) => (
                      <tr key={task.id}>
                        <td>
                          <span className="font-semibold text-main">
                            {task.hostname || task.device_id.slice(0, 12)}
                          </span>
                        </td>
                        <td>
                          <span className={`status-pill ${taskPill(task.status)}`}>{statusLabel(t, task.status)}</span>
                        </td>
                        <td className="text-sm text-muted">{task.step || '—'}</td>
                        <td>
                          {task.exit_code === null || task.exit_code === undefined ? (
                            <span className="text-dim">—</span>
                          ) : (
                            <span className="exit-code-badge">{task.exit_code}</span>
                          )}
                        </td>
                        <td className="text-sm text-success">
                          {task.bytes_freed > 0 ? formatBytes(task.bytes_freed) : '—'}
                        </td>
                        <td>
                          {task.reboot_required ? (
                            <span className="status-pill warning">{t('maintenance.reboot_badge')}</span>
                          ) : (
                            <span className="text-dim">—</span>
                          )}
                        </td>
                        <td>
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => setLogTask(task)}
                            disabled={!task.output_log && !task.error_message}
                          >
                            <FileText size={14} />
                            <span>{t('maintenance.col_log')}</span>
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
        title={t('maintenance.output_title')}
        size="lg"
        description={
          logTask
            ? t(logTask.step ? 'maintenance.log_description_step' : 'maintenance.log_description', {
                host: logTask.hostname || logTask.device_id,
                task: logTask.task_type,
                status: statusLabel(t, logTask.status),
                step: logTask.step || '',
              })
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setLogTask(null)}>
            {t('common.close')}
          </button>
        }
      >
        <pre className="terminal-log-output">
          {logTask?.output_log || logTask?.error_message || t('maintenance.no_output')}
        </pre>
      </Modal>

      {/* New job modal */}
      <Modal
        open={isRunModalOpen}
        onClose={() => !submitting && setIsRunModalOpen(false)}
        title={t('maintenance.new_job')}
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
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="maintenance-run-form"
              className="btn btn-primary"
              disabled={submitting || catalog.length === 0}
            >
              {submitting ? <span className="btn-loading">{t('maintenance.dispatching')}</span> : t('maintenance.run')}
            </button>
          </>
        }
      >
        <form id="maintenance-run-form" onSubmit={handleRun}>
          <div className="form-group">
            <label className="form-label" htmlFor="maint-name">
              {t('maintenance.job_name_label')}
            </label>
            <input
              id="maint-name"
              type="text"
              className="form-input"
              placeholder={selectedTask?.label || t('maintenance.job_name_placeholder')}
              value={newRun.name}
              onChange={(e) => setNewRun({ ...newRun, name: e.target.value })}
            />
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="maint-task">
              {t('maintenance.task_label')}
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
              {catalog.length === 0 && <option value="">{t('maintenance.loading_catalog')}</option>}
              {catalog.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.disruptive ? t('maintenance.disruptive_option', { label: c.label }) : c.label}
                </option>
              ))}
            </select>
            {/* Label and description come from the server's TaskCatalog,
                so a task type added server-side shows up here with no
                console change. */}
            {selectedTask && (
              <p className="form-hint">
                {selectedTask.disruptive
                  ? t('maintenance.disruptive_hint', { description: selectedTask.description })
                  : selectedTask.description}
              </p>
            )}
            {catalog.length === 0 && (
              <p className="form-hint">
                {t('maintenance.empty_catalog')}
              </p>
            )}
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="maint-scope">
              {t('maintenance.scope_label')}
            </label>
            <select
              id="maint-scope"
              className="form-select"
              value={newRun.target_type}
              onChange={(e) => setNewRun({ ...newRun, target_type: e.target.value, target_id: '' })}
            >
              <option value="all">{t('maintenance.scope_all')}</option>
              <option value="group">{t('maintenance.scope_group')}</option>
              <option value="device">{t('maintenance.scope_device')}</option>
            </select>
          </div>

          {newRun.target_type === 'group' && (
            <div className="form-group">
              <label className="form-label" htmlFor="maint-group">
                {t('maintenance.group_label')}
              </label>
              <select
                id="maint-group"
                className="form-select"
                required
                value={newRun.target_id}
                onChange={(e) => setNewRun({ ...newRun, target_id: e.target.value })}
              >
                <option value="">{t('maintenance.select_group_placeholder')}</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {t(g.member_count === 1 ? 'maintenance.group_option_one' : 'maintenance.group_option', { name: g.name, count: g.member_count })}
                  </option>
                ))}
              </select>
              {groups.length === 0 && (
                <p className="form-hint">{t('maintenance.no_groups')}</p>
              )}
            </div>
          )}

          {newRun.target_type === 'device' && (
            <div className="form-group">
              <label className="form-label" htmlFor="maint-device">
                {t('maintenance.col_device')}
              </label>
              <select
                id="maint-device"
                className="form-select"
                required
                value={newRun.target_id}
                onChange={(e) => setNewRun({ ...newRun, target_id: e.target.value })}
              >
                <option value="">{t('maintenance.select_device_placeholder')}</option>
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
              <span>{t('maintenance.disruptive_notice')}</span>
            </div>
          )}
        </form>
      </Modal>

      <ConfirmDialog
        open={disruptivePending}
        title={t('maintenance.confirm_title')}
        message={
          selectedTask
            ? t('maintenance.confirm_message', {
                label: selectedTask.label,
                description: selectedTask.description,
                target:
                  newRun.target_type === 'all'
                    ? t('maintenance.all_devices')
                    : newRun.target_type === 'group'
                    ? groups.find((g) => g.id === newRun.target_id)?.name || newRun.target_id
                    : devices.find((d) => d.id === newRun.target_id)?.hostname || newRun.target_id,
              })
            : ''
        }
        confirmLabel={t('maintenance.run_anyway')}
        pending={submitting}
        onConfirm={dispatchRun}
        onCancel={() => setDisruptivePending(false)}
      />

      {/* Two standing caveats an operator otherwise has to discover the hard
          way: an offline target is not a failure, and the poll cadence. */}
      <div className="last-sync">
        <HardDrive size={12} />
        <span>{t('maintenance.footer_offline')}</span>
        <Clock size={12} />
        <span>{t('maintenance.footer_refresh')}</span>
      </div>
    </div>
  )
}
