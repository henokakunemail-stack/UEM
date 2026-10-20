import React, { useEffect, useState } from 'react'
import {
  CalendarClock,
  FileText,
  History,
  Laptop,
  Plus,
  RefreshCw,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import type { DeviceRunDTO, ScheduleDTO, ScriptDTO, TaskRunDTO } from '../types/api'
import { useT, type Vars } from '../i18n/core'

type TFn = (key: string, vars?: Vars) => string

// The server reads an interval trigger as a MINUTE count and multiplies it by
// time.Minute, so `schedule_expr` holds minutes -- not seconds, which is what
// this used to assume. An operator who typed the "3600" the field used to
// suggest was asking for a task every 2400 hours, and the console rendered the
// same number back as "Every 1h", so nothing on screen disagreed with anything
// else and nothing fired on time either.
function describeInterval(expr: string, t: TFn): string {
  const mins = Number(expr)
  if (!Number.isFinite(mins) || mins <= 0) return expr || '—'
  if (mins % 1440 === 0) return t('scheduler.interval_days', { n: mins / 1440 })
  if (mins % 60 === 0) return t('scheduler.interval_hours', { n: mins / 60 })
  return t('scheduler.interval_minutes', { n: mins })
}

// Run and target values are server enums. Known ones get a translated label;
// anything else is shown exactly as the server sent it.
const STATUS_KEYS: Record<string, string> = {
  pending: 'scheduler.status_pending',
  running: 'scheduler.status_running',
  completed: 'scheduler.status_completed',
  success: 'scheduler.status_success',
  failed: 'scheduler.status_failed',
}
const TARGET_KEYS: Record<string, string> = {
  all: 'scheduler.target_all_short',
  group: 'scheduler.target_group_short',
  device: 'scheduler.target_device_short',
}
function enumLabel(map: Record<string, string>, value: string, t: TFn): string {
  return map[value] ? t(map[value]) : value
}

export type SchedulerTab = 'schedules' | 'runs'

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
  const { t } = useT()

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
      setMsg({ type: 'error', text: err.message || t('scheduler.load_failed') })
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
      const created = await api.createScript(newScript)
      const successText = t('scheduler.script_created', { name: newScript.name })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('scheduler.script_registered'))
      setIsScriptModalOpen(false)
      setNewScript({ name: '', description: '', script_type: 'powershell', script_content: '' })
      await loadData()
      if (created?.id) {
        setNewSchedule((prev) => ({ ...prev, script_id: created.id }))
      }
      setIsScheduleModalOpen(true)
    } catch (err: any) {
      const errorText = err.message || t('scheduler.script_create_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('scheduler.creation_failed'))
    }
  }

  const handleCreateSchedule = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createSchedule(newSchedule)
      const successText = t('scheduler.schedule_created', { name: newSchedule.name })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('scheduler.schedule_activated'))
      setIsScheduleModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || t('scheduler.schedule_create_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('scheduler.schedule_failed'))
    }
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('scheduler.title')}</h2>
          <p className="page-subtitle">
            {t('scheduler.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('scheduler.refresh')}</span>
          </button>
          {canAdmin && subTab === 'schedules' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsScheduleModalOpen(true)}>
              <Plus size={16} />
              <span>{t('scheduler.new_schedule')}</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label={t('scheduler.dismiss_message')}>
            <X size={14} />
          </button>
        </div>
      )}

      {/* Sub Tabs */}
      <div className="tabs-nav">
        <button
          type="button"
          className={`tab-btn ${subTab === 'schedules' ? 'active' : ''}`}
          onClick={() => setSubTab('schedules')}
        >
          <CalendarClock size={16} />
          <span>{t('scheduler.tab_schedules', { count: schedules.length })}</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'runs' ? 'active' : ''}`}
          onClick={() => setSubTab('runs')}
        >
          <History size={16} />
          <span>{t('scheduler.tab_runs', { count: runs.length })}</span>
        </button>
      </div>

      {/* Tab 1: Schedules */}
      {subTab === 'schedules' && (
        <div className="table-card">
          <DataTable label={t('scheduler.schedules_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('scheduler.col_schedule_name')}</th>
                  <th>{t('scheduler.col_script')}</th>
                  <th>{t('scheduler.col_target')}</th>
                  <th>{t('scheduler.col_frequency')}</th>
                  <th>{t('scheduler.col_status')}</th>
                  <th>{t('scheduler.col_next_run')}</th>
                </tr>
              </thead>
              <tbody>
                {schedules.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-8 text-muted">
                      {t('scheduler.no_schedules')}
                    </td>
                  </tr>
                ) : (
                  schedules.map((sch) => (
                    <tr key={sch.id}>
                      <td className="font-semibold text-main">{sch.name}</td>
                      <td>{sch.script_name || sch.script_id.slice(0, 12)}</td>
                      <td>
                        <span className="badge-target">{enumLabel(TARGET_KEYS, sch.target_type, t)}</span>
                      </td>
                      <td>
                        {/* A schedule_type the server refuses to create can still
                            exist in a database written before the check. It is
                            shown as never-run rather than described as an
                            interval, because describing "0 0 * * *" as an
                            interval tells the operator it is firing. */}
                        {sch.schedule_type === 'interval' ? (
                          t('scheduler.every', { interval: describeInterval(sch.schedule_expr, t) })
                        ) : (
                          <span className="text-muted">
                            {t('scheduler.unsupported_trigger', { type: sch.schedule_type })}
                          </span>
                        )}
                      </td>
                      <td>
                        <span className={`status-pill ${sch.is_enabled ? 'online' : 'offline'}`}>
                          {sch.is_enabled ? t('scheduler.active') : t('scheduler.paused')}
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
          <DataTable label={t('scheduler.runs_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('scheduler.col_run_id')}</th>
                  <th>{t('scheduler.col_schedule')}</th>
                  <th>{t('scheduler.col_script')}</th>
                  <th>{t('scheduler.col_status')}</th>
                  <th>{t('scheduler.col_triggered_at')}</th>
                  <th>{t('scheduler.col_completed_at')}</th>
                  <th>{t('scheduler.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {runs.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      {t('scheduler.no_runs')}
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
                          {enumLabel(STATUS_KEYS, r.status, t)}
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
                          <span>{t('scheduler.per_device')}</span>
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
        title={t('scheduler.per_device_results')}
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
            {t('common.close')}
          </button>
        }
      >
        {devicesLoading ? (
          <div className="py-8 text-center text-muted">
            <div className="spinner-inline"></div> {t('scheduler.loading_device_results')}
          </div>
        ) : runDevices.length === 0 ? (
          <div className="py-8 text-center text-muted">{t('scheduler.no_device_rows')}</div>
        ) : (
          <DataTable label={t('scheduler.per_device_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('scheduler.col_device')}</th>
                  <th>{t('scheduler.col_site')}</th>
                  <th>{t('scheduler.col_status')}</th>
                  <th>{t('scheduler.col_exit_code')}</th>
                  <th>{t('scheduler.col_log')}</th>
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
                        {enumLabel(STATUS_KEYS, d.status, t)}
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
                        <span>{t('scheduler.view_log')}</span>
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
        title={t('scheduler.create_script_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsScriptModalOpen(false)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="script-form" className="btn btn-primary">
              {t('scheduler.save_script')}
            </button>
          </>
        }
      >
        <form id="script-form" onSubmit={handleCreateScript}>
          <div className="form-group">
            <label className="form-label" htmlFor="script-title">
              {t('scheduler.script_title')}
            </label>
            <input
              id="script-title"
              type="text"
              className="form-input"
              required
              placeholder={t('scheduler.script_title_placeholder')}
              value={newScript.name}
              onChange={(e) => setNewScript({ ...newScript, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-shell">
              {t('scheduler.shell')}
            </label>
            <select
              id="script-shell"
              className="form-select"
              value={newScript.script_type}
              onChange={(e) => setNewScript({ ...newScript, script_type: e.target.value })}
            >
              <option value="powershell">PowerShell (Windows)</option>
              <option value="cmd">{t('scheduler.shell_cmd')}</option>
              <option value="bash">Bash (Linux / macOS)</option>
              <option value="sh">POSIX Shell (/bin/sh)</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-description">
              {t('scheduler.description_optional')}
            </label>
            <input
              id="script-description"
              type="text"
              className="form-input"
              placeholder={t('scheduler.description_placeholder')}
              value={newScript.description}
              onChange={(e) => setNewScript({ ...newScript, description: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="script-body">
              {t('scheduler.script_body')}
            </label>
            <textarea
              id="script-body"
              className="form-textarea font-mono"
              rows={8}
              required
              placeholder={t('scheduler.script_body_placeholder')}
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
        title={t('scheduler.create_schedule_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsScheduleModalOpen(false)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="schedule-form" className="btn btn-primary">
              {t('scheduler.activate_schedule')}
            </button>
          </>
        }
      >
        <form id="schedule-form" onSubmit={handleCreateSchedule}>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-name">
              {t('scheduler.col_schedule_name')}
            </label>
            <input
              id="schedule-name"
              type="text"
              className="form-input"
              required
              placeholder={t('scheduler.schedule_name_placeholder')}
              value={newSchedule.name}
              onChange={(e) => setNewSchedule({ ...newSchedule, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem' }}>
              <label className="form-label" htmlFor="schedule-script" style={{ marginBottom: 0 }}>
                {t('scheduler.target_script')}
              </label>
              {canAdmin && (
                <button
                  type="button"
                  className="btn-action"
                  style={{ fontSize: '0.75rem', padding: '2px 8px' }}
                  onClick={() => {
                    setIsScheduleModalOpen(false)
                    setIsScriptModalOpen(true)
                  }}
                >
                  <Plus size={12} />
                  <span>{t('scheduler.custom_script')}</span>
                </button>
              )}
            </div>
            <select
              id="schedule-script"
              className="form-select"
              required
              value={newSchedule.script_id}
              onChange={(e) => setNewSchedule({ ...newSchedule, script_id: e.target.value })}
            >
              <option value="">{t('scheduler.select_script')}</option>
              {scripts.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} ({s.script_type})
                </option>
              ))}
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-target">
              {t('scheduler.col_target')}
            </label>
            <select
              id="schedule-target"
              className="form-select"
              value={newSchedule.target_type}
              onChange={(e) => setNewSchedule({ ...newSchedule, target_type: e.target.value })}
            >
              <option value="all">{t('scheduler.target_all')}</option>
              <option value="group">{t('scheduler.target_group')}</option>
              <option value="device">{t('scheduler.target_device')}</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-trigger">
              {t('scheduler.trigger_type')}
            </label>
            <select
              id="schedule-trigger"
              className="form-select"
              value={newSchedule.schedule_type}
              onChange={(e) =>
                setNewSchedule({
                  ...newSchedule,
                  schedule_type: e.target.value,
                  // Pre-fill the expression shape so the field below is never
                  // left holding whatever the previous trigger wanted.
                  schedule_expr: '60',
                })
              }
            >
              <option value="interval">{t('scheduler.fixed_interval')}</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="schedule-expr">
              {t('scheduler.interval_label')}
            </label>
            <input
              id="schedule-expr"
              type="text"
              className="form-input"
              required
              placeholder="60"
              value={newSchedule.schedule_expr}
              onChange={(e) =>
                setNewSchedule({
                  ...newSchedule,
                  schedule_expr: String(Number(e.target.value) || 0),
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
        title={t('scheduler.output_title')}
        size="lg"
        description={viewLogRun?.id}
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setViewLogRun(null)}>
            {t('common.close')}
          </button>
        }
      >
        <pre className="terminal-log-output">
          {viewLogRun?.output_log || viewLogRun?.error_message || t('scheduler.no_output')}
        </pre>
      </Modal>
    </div>
  )
}
