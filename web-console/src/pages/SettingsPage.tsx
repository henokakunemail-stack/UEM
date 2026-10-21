import React, { useCallback, useEffect, useState } from 'react'
import {
  AlertTriangle,
  CheckCircle2,
  Database,
  Download,
  HardDrive,
  PlugZap,
  RefreshCw,
  RotateCcw,
  Save,
  Shield,
  Trash2,
  UploadCloud,
  Users,
  XCircle,
} from 'lucide-react'
import { ApiError, api } from '../services/api'
import { waitForRestore } from './settings/restorePoller'
import { useToast } from '../context/ToastContext'
import { useT } from '../i18n/core'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import type {
  BackupFileDTO,
  DirectoryConfigDTO,
  DirectoryContactDTO,
  DirectorySyncPlanDTO,
  RestoreMarkerDTO,
} from '../types/api'

// Directory sync only. Nothing here creates a console account: a synced contact
// has no password_hash, so the server's login path refuses them like any
// unknown user. The contacts exist so a hardware asset's PIC is a dropdown
// instead of a spelling exercise.

const DEFAULT_FILTER = '(objectClass=person)'

const emptyConfig = (): DirectoryConfigDTO => ({
  host: '',
  port: 636,
  use_tls: true,
  base_dn: '',
  bind_dn: '',
  search_filter: DEFAULT_FILTER,
  source: 'ldap',
  bind_password_configured: false,
})

const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

const formatDateTime = (iso: string) => {
  try {
    const d = new Date(iso)
    return isNaN(d.getTime()) ? iso : d.toLocaleString()
  } catch {
    return iso
  }
}

// Splits a translated sentence on its {name} placeholders and drops the matching
// React node in, so markup such as <code> or <strong> can sit inside text whose
// word order differs per language.
function withNodes(text: string, nodes: Record<string, React.ReactNode>): React.ReactNode {
  return text.split(/(\{\w+\})/g).map((part, i) => {
    const m = /^\{(\w+)\}$/.exec(part)
    return m && m[1] in nodes ? <React.Fragment key={i}>{nodes[m[1]]}</React.Fragment> : part
  })
}

export interface SettingsPageProps {
  activeTab?: 'directory' | 'backup'
  onTabChange?: (tab: 'directory' | 'backup') => void
}

export function SettingsPage({ activeTab = 'directory', onTabChange }: SettingsPageProps) {
  const { t } = useT()
  // Directory Tab State
  const [config, setConfig] = useState<DirectoryConfigDTO>(emptyConfig())
  const [loadingDir, setLoadingDir] = useState(true)
  const [savingDir, setSavingDir] = useState(false)
  const [testingDir, setTestingDir] = useState(false)
  const [syncingDir, setSyncingDir] = useState(false)
  const [plan, setPlan] = useState<DirectorySyncPlanDTO | null>(null)
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null)
  const [confirmApply, setConfirmApply] = useState(false)
  const [applyingSync, setApplyingSync] = useState(false)

  // Backup Tab State
  const [backups, setBackups] = useState<BackupFileDTO[]>([])
  const [loadingBackups, setLoadingBackups] = useState(false)
  const [creatingBackup, setCreatingBackup] = useState(false)
  const [downloadingActive, setDownloadingActive] = useState(false)
  const [downloadingFile, setDownloadingFile] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupFileDTO | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [restoreTarget, setRestoreTarget] = useState<BackupFileDTO | null>(null)
  const [restoring, setRestoring] = useState(false)
  const [uploadFile, setUploadFile] = useState<File | null>(null)
  const [confirmUploadRestore, setConfirmUploadRestore] = useState(false)
  const [reconnecting, setReconnecting] = useState(false)
  const [reconnectCount, setReconnectCount] = useState(0)
  // The restore being waited on, and the server process that took the request:
  // only a different process can report how the restore ended.
  const [pendingRestore, setPendingRestore] = useState<{ id: string; beforeNode: string | null } | null>(null)
  const [lastRestore, setLastRestore] = useState<RestoreMarkerDTO | null>(null)

  const toast = useToast()

  // Load directory config on mount
  useEffect(() => {
    let stopped = false
    setLoadingDir(true)
    api.getDirectoryConfig()
      .then((saved) => { if (!stopped) setConfig(saved) })
      .catch((err: any) => {
        if (!stopped) toast.error(err.message || t('settings.dir_load_failed'), t('settings.toast_load_failed'))
      })
      .finally(() => { if (!stopped) setLoadingDir(false) })
    return () => { stopped = true }
  }, [])

  // Load backups list
  const loadBackups = useCallback(async () => {
    setLoadingBackups(true)
    try {
      const list = await api.getBackups()
      setBackups(list || [])
    } catch (err: any) {
      toast.error(err.message || t('settings.backups_load_failed'), t('settings.toast_load_failed'))
    } finally {
      setLoadingBackups(false)
    }
  }, [])

  // Load backups when switching to backup tab
  useEffect(() => {
    if (activeTab === 'backup') {
      void loadBackups()
      api.getLastRestoreStatus().then(setLastRestore).catch(() => setLastRestore(null))
    }
  }, [activeTab, loadBackups])

  // Wait for the restore to finish in a new server process, then say how it
  // ended. See restorePoller.ts for why the first answer from /healthz is not it.
  useEffect(() => {
    if (!reconnecting || !pendingRestore) return
    let stopped = false
    const ticker = setInterval(() => setReconnectCount((c) => c + 1), 2000)
    void waitForRestore(pendingRestore.beforeNode, pendingRestore.id, {
      fetchNode: (signal) => api.getHealthNode(signal),
      fetchStatus: (id, signal) => api.getRestoreStatus(id, signal),
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      now: () => Date.now(),
    }).then((outcome) => {
      clearInterval(ticker)
      if (stopped) return
      setReconnecting(false)
      setPendingRestore(null)
      setRestoring(false)
      if (outcome === 'succeeded') {
        toast.success(t('settings.restore_back_online') + ' ' + t('settings.restore_signin_again'), t('settings.toast_restore_complete'))
        setTimeout(() => window.location.reload(), 1500)
        return
      }
      if (outcome === 'failed') {
        toast.error(t('settings.restore_failed_rolled_back'), t('settings.toast_restore_failed'))
      } else if (outcome === 'rollback_failed') {
        toast.error(t('settings.restore_failed_rollback_failed'), t('settings.toast_restore_failed'))
      } else {
        toast.error(t('settings.restore_timeout'), t('settings.toast_restore_failed'))
      }
      // The toast goes; the page keeps showing what happened: the new safety
      // snapshot in the list and the outcome in the Last restore banner.
      void loadBackups()
      api.getLastRestoreStatus().then(setLastRestore).catch(() => {})
    })
    return () => {
      stopped = true
      clearInterval(ticker)
    }
  }, [reconnecting, pendingRestore])

  // The server's refusals of a restore request, translated.
  const restoreError = (err: unknown, fallbackKey: string) => {
    if (err instanceof ApiError && err.code === 'restore_in_progress') return t('settings.restore_in_progress')
    if (err instanceof ApiError && err.code === 'safety_snapshot_failed') return t('settings.restore_safety_snapshot_failed')
    return err instanceof Error && err.message ? err.message : t(fallbackKey)
  }

  // Directory handlers
  const updateConfig = (patch: Partial<DirectoryConfigDTO>) => {
    setPlan(null)
    setTestResult(null)
    setConfig((prev) => ({ ...prev, ...patch }))
  }

  const reloadDir = async () => {
    setLoadingDir(true)
    try {
      setConfig(await api.getDirectoryConfig())
    } catch (err: any) {
      toast.error(err.message || t('settings.dir_load_failed'), t('settings.toast_load_failed'))
    } finally {
      setLoadingDir(false)
    }
  }

  const saveDir = async (e: React.FormEvent) => {
    e.preventDefault()
    setSavingDir(true)
    try {
      const saved = await api.updateDirectoryConfig(config)
      setConfig(saved)
      toast.success(t('settings.dir_saved'), t('settings.toast_settings_saved'))
    } catch (err: any) {
      toast.error(err.message || t('settings.dir_save_failed'), t('settings.toast_save_failed'))
    } finally {
      setSavingDir(false)
    }
  }

  const testConnection = async () => {
    setTestingDir(true)
    setTestResult(null)
    try {
      const res = await api.testDirectoryConnection()
      setTestResult({ ok: res.ok, message: t('settings.test_entries_matched', { message: res.message, count: res.entries_seen }) })
      if (res.ok) toast.success(t('settings.test_reachable', { count: res.entries_seen }), t('settings.toast_connection_ok'))
    } catch (err: any) {
      setTestResult({ ok: false, message: err.message || t('settings.test_unreachable') })
    } finally {
      setTestingDir(false)
    }
  }

  const previewSync = async () => {
    setSyncingDir(true)
    setPlan(null)
    try {
      const p = await api.previewDirectorySync()
      setPlan(p)
      if (p.total_in_directory === 0) {
        toast.error(t('settings.preview_nothing_matched'), t('settings.toast_nothing_matched'))
      }
    } catch (err: any) {
      toast.error(err.message || t('settings.preview_failed'), t('settings.toast_preview_failed'))
    } finally {
      setSyncingDir(false)
    }
  }

  const applySync = async () => {
    setApplyingSync(true)
    try {
      const applied = await api.applyDirectorySync()
      setPlan(applied)
      setConfirmApply(false)
      const total = applied.adds.length + applied.updates.length + applied.deactivations.length
      if (total === 0) {
        toast.success(t('settings.sync_up_to_date'), t('settings.toast_sync_complete'))
      } else {
        toast.success(
          t('settings.sync_result', {
            added: applied.adds.length,
            updated: applied.updates.length,
            deactivated: applied.deactivations.length,
          }),
          t('settings.toast_sync_complete')
        )
      }
    } catch (err: any) {
      toast.error(err.message || t('settings.sync_failed'), t('settings.toast_sync_failed'))
    } finally {
      setApplyingSync(false)
    }
  }

  // Backup handlers
  const triggerBlobDownload = (blob: Blob, filename: string) => {
    const objectUrl = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = objectUrl
    link.download = filename
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    setTimeout(() => URL.revokeObjectURL(objectUrl), 60_000)
  }

  const handleCreateSnapshot = async () => {
    setCreatingBackup(true)
    try {
      const created = await api.createBackup()
      toast.success(t('settings.snapshot_created', { filename: created.filename }), t('settings.toast_snapshot_created'))
      await loadBackups()
    } catch (err: any) {
      toast.error(err.message || t('settings.snapshot_create_failed'), t('settings.toast_backup_failed'))
    } finally {
      setCreatingBackup(false)
    }
  }

  const handleDownloadActive = async () => {
    setDownloadingActive(true)
    try {
      const blob = await api.downloadActiveBackup()
      const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)
      triggerBlobDownload(blob, `endpoint-mgmt-live-${stamp}.db`)
      toast.success(t('settings.live_downloaded', { size: formatBytes(blob.size) }), t('settings.toast_download_complete'))
    } catch (err: any) {
      toast.error(err.message || t('settings.live_download_failed'), t('settings.toast_download_failed'))
    } finally {
      setDownloadingActive(false)
    }
  }

  const handleDownloadFile = async (filename: string) => {
    setDownloadingFile(filename)
    try {
      const blob = await api.downloadBackup(filename)
      triggerBlobDownload(blob, filename)
      toast.success(t('settings.file_downloaded', { filename, size: formatBytes(blob.size) }), t('settings.toast_download_complete'))
    } catch (err: any) {
      toast.error(err.message || t('settings.file_download_failed'), t('settings.toast_download_failed'))
    } finally {
      setDownloadingFile(null)
    }
  }

  const handleConfirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteBackup(deleteTarget.filename)
      toast.success(t('settings.snapshot_deleted', { filename: deleteTarget.filename }), t('settings.toast_snapshot_deleted'))
      setDeleteTarget(null)
      await loadBackups()
    } catch (err: any) {
      toast.error(err.message || t('settings.snapshot_delete_failed'), t('settings.toast_delete_failed'))
    } finally {
      setDeleting(false)
    }
  }

  const handleConfirmRestore = async () => {
    if (!restoreTarget) return
    setRestoring(true)
    const beforeNode = await api.getHealthNode().catch(() => null)
    try {
      const res = await api.restoreBackup(restoreTarget.filename)
      toast.info(res.message || t('settings.restore_verified'), t('settings.toast_restoring'))
      setRestoreTarget(null)
      setReconnectCount(0)
      setPendingRestore({ id: res.restore_id || '', beforeNode })
      setReconnecting(true)
    } catch (err: unknown) {
      toast.error(restoreError(err, 'settings.restore_verify_failed'), t('settings.toast_restore_failed'))
      setRestoring(false)
    }
  }

  const handleConfirmUploadRestore = async () => {
    if (!uploadFile) return
    setRestoring(true)
    const beforeNode = await api.getHealthNode().catch(() => null)
    try {
      const res = await api.uploadAndRestoreBackup(uploadFile)
      toast.info(res.message || t('settings.upload_verified'), t('settings.toast_restoring'))
      setConfirmUploadRestore(false)
      setUploadFile(null)
      setReconnectCount(0)
      setPendingRestore({ id: res.restore_id || '', beforeNode })
      setReconnecting(true)
    } catch (err: unknown) {
      toast.error(restoreError(err, 'settings.upload_restore_failed'), t('settings.toast_restore_failed'))
      setRestoring(false)
    }
  }

  const getBackupTypeBadge = (b: BackupFileDTO) => {
    if (b.filename.includes('pre-restore')) {
      return <span className="status-pill warning"><span className="dot" />{t('settings.badge_safety')}</span>
    }
    if (b.is_auto) {
      return <span className="status-pill online"><span className="dot" />{t('settings.badge_auto')}</span>
    }
    if (b.filename.includes('uploaded')) {
      return <span className="status-pill primary"><span className="dot" />{t('settings.badge_uploaded')}</span>
    }
    return <span className="status-pill primary"><span className="dot" />{t('settings.badge_manual')}</span>
  }

  const pending = plan ? plan.adds.length + plan.updates.length + plan.deactivations.length : 0
  const busyDir = syncingDir || applyingSync
  const totalBackupBytes = backups.reduce((acc, b) => acc + b.size, 0)

  return (
    <div className="page-container">
      {/* Top Header */}
      <div className="page-header">
        <div>
          <h1 className="page-title">{t('settings.title')}</h1>
          <p className="page-subtitle">
            {activeTab === 'directory'
              ? t('settings.subtitle_directory')
              : t('settings.subtitle_backup')}
          </p>
        </div>
        <div className="header-controls">
          {activeTab === 'directory' ? (
            <button type="button" className="btn btn-secondary" onClick={reloadDir} disabled={loadingDir}>
              <RefreshCw size={16} className={loadingDir ? 'spinning' : ''} />
              <span>{t('settings.reload')}</span>
            </button>
          ) : (
            <>
              <button
                type="button"
                className="btn btn-primary"
                onClick={handleCreateSnapshot}
                disabled={creatingBackup || downloadingActive}
              >
                <Save size={16} className={creatingBackup ? 'spinning' : ''} />
                <span>{creatingBackup ? t('settings.creating_snapshot') : t('settings.create_snapshot')}</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={handleDownloadActive}
                disabled={downloadingActive || creatingBackup}
              >
                <Download size={16} className={downloadingActive ? 'spinning' : ''} />
                <span>{downloadingActive ? t('settings.streaming_live') : t('settings.download_live')}</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={loadBackups}
                disabled={loadingBackups}
              >
                <RefreshCw size={16} className={loadingBackups ? 'spinning' : ''} />
                <span>{t('settings.reload')}</span>
              </button>
            </>
          )}
        </div>
      </div>

      {/* Tabs Navigation */}
      <div className="table-toolbar" style={{ marginBottom: '1.25rem' }}>
        <div className="tabs-nav" style={{ borderBottom: 'none', marginBottom: 0, paddingBottom: 0 }}>
          <button
            type="button"
            className={`tab-btn ${activeTab === 'directory' ? 'active' : ''}`}
            onClick={() => onTabChange?.('directory')}
          >
            <Users size={16} />
            <span>{t('settings.tab_directory')}</span>
          </button>
          <button
            type="button"
            className={`tab-btn ${activeTab === 'backup' ? 'active' : ''}`}
            onClick={() => onTabChange?.('backup')}
          >
            <Database size={16} />
            <span>{t('settings.tab_backup', { count: backups.length })}</span>
          </button>
        </div>
      </div>

      {/* Directory Tab Content */}
      {activeTab === 'directory' && (
        <>
          <div className="security-notice">
            <PlugZap size={16} />
            <div>
              <strong>{t('settings.notice_not_accounts_title')}</strong> {t('settings.notice_not_accounts_body')}
            </div>
          </div>

          <form className="table-card" onSubmit={saveDir}>
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><PlugZap size={18} /></span>
                <div>
                  <h2 className="card-title">{t('settings.connection_title')}</h2>
                  <span className="text-muted text-sm">
                    {t('settings.connection_hint')}
                  </span>
                </div>
              </div>
            </div>

            <div className="table-responsive">
              <div className="form-group">
                <label className="form-label" htmlFor="dir-host">{t('settings.field_host')}</label>
                <input
                  id="dir-host"
                  className="form-input"
                  required
                  placeholder="ldap.example.com"
                  value={config.host}
                  onChange={(e) => updateConfig({ host: e.target.value })}
                />
              </div>

              <div className="form-row">
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-port">{t('settings.field_port')}</label>
                  <input
                    id="dir-port"
                    className="form-input"
                    type="number"
                    min={1}
                    max={65535}
                    required
                    value={config.port}
                    onChange={(e) => updateConfig({ port: Number(e.target.value) })}
                  />
                  <span className="form-hint">{t('settings.port_hint')}</span>
                </div>
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-tls">{t('settings.field_transport')}</label>
                  <select
                    id="dir-tls"
                    className="form-select"
                    value={config.use_tls ? '1' : '0'}
                    onChange={(e) => updateConfig({ use_tls: e.target.value === '1' })}
                  >
                    <option value="1">TLS</option>
                    <option value="0">{t('settings.transport_plain')}</option>
                  </select>
                  <span className="form-hint">
                    {t('settings.transport_hint')}
                  </span>
                </div>
              </div>

              <div className="form-group">
                <label className="form-label" htmlFor="dir-basedn">{t('settings.field_base_dn')}</label>
                <input
                  id="dir-basedn"
                  className="form-input"
                  placeholder="DC=example,DC=com"
                  value={config.base_dn}
                  onChange={(e) => updateConfig({ base_dn: e.target.value })}
                />
                <span className="form-hint">{t('settings.base_dn_hint')}</span>
              </div>

              <div className="form-row">
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-binddn">{t('settings.field_bind_dn')}</label>
                  <input
                    id="dir-binddn"
                    className="form-input"
                    required
                    placeholder="CN=svc-directory,OU=Service,DC=example,DC=com"
                    value={config.bind_dn}
                    onChange={(e) => updateConfig({ bind_dn: e.target.value })}
                  />
                </div>
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-filter">{t('settings.field_search_filter')}</label>
                  <input
                    id="dir-filter"
                    className="form-input"
                    value={config.search_filter}
                    onChange={(e) => updateConfig({ search_filter: e.target.value })}
                  />
                </div>
              </div>

              <div className="form-group">
                <span className="form-label">{t('settings.field_bind_password')}</span>
                <p className="text-muted text-sm">
                  {config.bind_password_configured
                    ? t('settings.bind_password_set')
                    : t('settings.bind_password_unset')}
                </p>
              </div>

              <div className="row-actions">
                <button type="submit" className="btn btn-primary" disabled={savingDir || loadingDir}>
                  <Save size={16} />
                  <span>{savingDir ? t('settings.saving') : t('settings.save_settings')}</span>
                </button>
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={testConnection}
                  disabled={testingDir || loadingDir || syncingDir}
                >
                  <PlugZap size={16} />
                  <span>{testingDir ? t('settings.testing') : t('settings.test_connection')}</span>
                </button>
              </div>

              {testResult && (
                <div className={testResult.ok ? 'alert-banner' : 'error-banner'}>
                  {testResult.ok ? <CheckCircle2 size={16} /> : <XCircle size={16} />}
                  <span>{testResult.message}</span>
                </div>
              )}
            </div>
          </form>

          <div className="table-card">
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><Users size={18} /></span>
                <div>
                  <h2 className="card-title">{t('settings.sync_title')}</h2>
                  <span className="text-muted text-sm">
                    {t('settings.sync_hint')}
                  </span>
                </div>
              </div>
            </div>

            <div className="table-responsive">
              <div className="row-actions">
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={previewSync}
                  disabled={busyDir || loadingDir}
                >
                  <RefreshCw size={16} className={syncingDir ? 'spinning' : ''} />
                  <span>{t('settings.preview_sync')}</span>
                </button>
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => setConfirmApply(true)}
                  disabled={busyDir || loadingDir || plan === null || pending === 0}
                >
                  <Save size={16} />
                  <span>{t('settings.apply')}</span>
                </button>
              </div>

              {!plan && (
                <p className="text-muted">
                  {t('settings.no_preview_yet')}
                </p>
              )}

              {plan && plan.total_in_directory === 0 && (
                <div className="error-banner">
                  <XCircle size={16} />
                  <span>
                    {t('settings.preview_empty_warning')}
                  </span>
                </div>
              )}

              {plan && pending === 0 && plan.total_in_directory > 0 && (
                <div className="alert-banner">
                  <CheckCircle2 size={16} />
                  <span>
                    {t('settings.nothing_to_do', { unchanged: plan.unchanged, total: plan.total_in_directory })}
                  </span>
                </div>
              )}

              {plan && pending > 0 && (
                <>
                  <PlanTable title={t('settings.plan_add')} rows={plan.adds} />
                  <PlanTable title={t('settings.plan_update')} rows={plan.updates} />
                  <PlanTable title={t('settings.plan_deactivate')} rows={plan.deactivations} />
                </>
              )}
            </div>
          </div>

          <ConfirmDialog
            open={confirmApply}
            title={t('settings.apply_dialog_title')}
            message={
              plan
                ? t('settings.apply_dialog_message', {
                    added: plan.adds.length,
                    updated: plan.updates.length,
                    deactivated: plan.deactivations.length,
                  })
                : ''
            }
            confirmLabel={t('settings.apply_sync')}
            pending={applyingSync}
            onConfirm={applySync}
            onCancel={() => setConfirmApply(false)}
          />
        </>
      )}

      {/* Backup & Restore Tab Content */}
      {activeTab === 'backup' && (
        <>
          <div className="security-notice">
            <Shield size={16} />
            <div>
              <strong>{t('settings.dr_notice_title')}</strong>{' '}
              {withNodes(t('settings.dr_notice_body'), {
                vacuum: <code>VACUUM INTO</code>,
                pattern: <code>endpoint-mgmt-pre-restore-*.db</code>,
                check: <code>PRAGMA quick_check</code>,
              })}
            </div>
          </div>

          {/* The last restore's outcome, read from the server: after a restart
              it is the only place a failed restore is still visible. */}
          {lastRestore && (lastRestore.state === 'succeeded' || lastRestore.state === 'failed') && (
            <div className={lastRestore.state === 'succeeded' ? 'alert-banner' : 'error-banner'} role="status" style={{ marginBottom: '1rem' }}>
              <strong>{t('settings.last_restore_title')}:</strong>{' '}
              {lastRestore.state === 'succeeded'
                ? t('settings.last_restore_succeeded', { time: formatDateTime(lastRestore.finished_at || lastRestore.requested_at || ''), source: lastRestore.source || '' })
                : t('settings.last_restore_failed', {
                    time: formatDateTime(lastRestore.finished_at || lastRestore.requested_at || ''),
                    code: lastRestore.error_code || '',
                    rolled: lastRestore.rolled_back ? t('settings.yes') : t('settings.no'),
                  })}
            </div>
          )}

          {/* Stats KPI Cards */}
          <div className="kpi-grid">
            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">{t('settings.kpi_snapshots')}</span>
                <span className="kpi-icon"><Database size={16} /></span>
              </div>
              <div className="kpi-value">{backups.length}</div>
              <div className="kpi-hint">{t('settings.kpi_snapshots_hint')}</div>
            </div>

            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">{t('settings.kpi_storage')}</span>
                <span className="kpi-icon"><HardDrive size={16} /></span>
              </div>
              <div className="kpi-value">{formatBytes(totalBackupBytes)}</div>
              <div className="kpi-hint">{t('settings.kpi_storage_hint')}</div>
            </div>

            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">{t('settings.kpi_latest')}</span>
                <span className="kpi-icon"><CheckCircle2 size={16} /></span>
              </div>
              <div className="kpi-value text-base font-semibold">
                {backups.length > 0 ? formatDateTime(backups[0].created_at) : t('settings.none')}
              </div>
              <div className="kpi-hint">
                {backups.length > 0 ? backups[0].filename : t('settings.no_snapshots_yet')}
              </div>
            </div>
          </div>

          {/* Upload External Backup Card */}
          <div className="table-card" style={{ marginBottom: '1.5rem' }}>
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><UploadCloud size={18} /></span>
                <div>
                  <h2 className="card-title">{t('settings.upload_title')}</h2>
                  <span className="text-muted text-sm">
                    {withNodes(t('settings.upload_hint'), { ext: <code>.db</code> })}
                  </span>
                </div>
              </div>
            </div>

            <div className="table-responsive">
              <div className="form-row" style={{ alignItems: 'flex-end', gap: '1rem' }}>
                <div className="form-field" style={{ flex: 1 }}>
                  <label className="form-label" htmlFor="upload-backup-file">{t('settings.upload_select_label')}</label>
                  <input
                    id="upload-backup-file"
                    className="form-input"
                    type="file"
                    accept=".db,.sqlite,.sqlite3"
                    onChange={(e) => {
                      const file = e.target.files?.[0] || null
                      setUploadFile(file)
                    }}
                  />
                  {uploadFile && (
                    <span className="form-hint">
                      {withNodes(t('settings.upload_selected'), {
                        name: <strong>{uploadFile.name}</strong>,
                        size: formatBytes(uploadFile.size),
                      })}
                    </span>
                  )}
                </div>
                <div style={{ paddingBottom: '0.25rem' }}>
                  <button
                    type="button"
                    className="btn btn-primary"
                    disabled={!uploadFile || restoring}
                    onClick={() => setConfirmUploadRestore(true)}
                  >
                    <RotateCcw size={16} className={restoring ? 'spinning' : ''} />
                    <span>{t('settings.upload_restore')}</span>
                  </button>
                </div>
              </div>
            </div>
          </div>

          {/* Stored Backups Table */}
          <div className="table-card">
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><Database size={18} /></span>
                <div>
                  <h2 className="card-title">{t('settings.server_snapshots_title')}</h2>
                  <span className="text-muted text-sm">
                    {t('settings.server_snapshots_hint')}
                  </span>
                </div>
              </div>
            </div>

            <div className="table-wrapper">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>{t('settings.col_filename')}</th>
                    <th>{t('settings.col_type')}</th>
                    <th>{t('settings.col_size')}</th>
                    <th>{t('settings.col_created_at')}</th>
                    <th style={{ textAlign: 'right' }}>{t('settings.col_actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {loadingBackups ? (
                    <tr>
                      <td colSpan={5} className="text-center py-8 text-muted">
                        {t('settings.loading_backups')}
                      </td>
                    </tr>
                  ) : backups.length === 0 ? (
                    <tr>
                      <td colSpan={5} className="text-center py-8 text-muted">
                        {t('settings.empty_backups')}
                      </td>
                    </tr>
                  ) : (
                    backups.map((b) => (
                      <tr key={b.filename}>
                        <td className="font-mono text-sm font-semibold">
                          <div className="flex items-center gap-2">
                            <Database size={15} className="text-primary" />
                            <span>{b.filename}</span>
                          </div>
                        </td>
                        <td>{getBackupTypeBadge(b)}</td>
                        <td className="text-sm font-mono">{formatBytes(b.size)}</td>
                        <td className="text-sm text-muted">{formatDateTime(b.created_at)}</td>
                        <td style={{ textAlign: 'right' }}>
                          <div className="action-buttons" style={{ justifyContent: 'flex-end', gap: '0.5rem' }}>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => handleDownloadFile(b.filename)}
                              disabled={downloadingFile === b.filename}
                              title={t('settings.download_title')}
                            >
                              <Download size={14} className={downloadingFile === b.filename ? 'spinning' : ''} />
                              <span>{t('settings.download')}</span>
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              style={{ borderColor: 'rgba(239, 68, 68, 0.4)', color: '#f87171' }}
                              onClick={() => setRestoreTarget(b)}
                              title={t('settings.restore_title')}
                            >
                              <RotateCcw size={14} />
                              <span>{t('settings.restore')}</span>
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => setDeleteTarget(b)}
                              title={t('settings.delete_title')}
                            >
                              <Trash2 size={14} />
                              <span>{t('common.delete')}</span>
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>

          {/* Confirm Restore Dialog */}
          <ConfirmDialog
            open={restoreTarget !== null}
            title={t('settings.restore_dialog_title')}
            message={
              restoreTarget ? (
                <div>
                  <p style={{ marginBottom: '0.75rem' }}>
                    {withNodes(t('settings.restore_dialog_question'), {
                      file: <strong>{restoreTarget.filename}</strong>,
                    })}
                  </p>
                  <div className="alert-banner" style={{ borderColor: 'rgba(234, 179, 8, 0.4)', color: '#fde047' }}>
                    <AlertTriangle size={18} />
                    <span>
                      {withNodes(t('settings.restore_dialog_warning'), {
                        pattern: <code>endpoint-mgmt-pre-restore-*.db</code>,
                      })}
                    </span>
                  </div>
                </div>
              ) : ''
            }
            confirmLabel={restoring ? t('settings.verifying_restoring') : t('settings.verify_restore')}
            pending={restoring}
            onConfirm={handleConfirmRestore}
            onCancel={() => setRestoreTarget(null)}
          />

          {/* Confirm Upload & Restore Dialog */}
          <ConfirmDialog
            open={confirmUploadRestore}
            title={t('settings.upload_dialog_title')}
            message={
              uploadFile ? (
                <div>
                  <p style={{ marginBottom: '0.75rem' }}>
                    {withNodes(t('settings.upload_dialog_question'), {
                      file: <strong>{uploadFile.name}</strong>,
                      size: formatBytes(uploadFile.size),
                    })}
                  </p>
                  <div className="alert-banner" style={{ borderColor: 'rgba(234, 179, 8, 0.4)', color: '#fde047' }}>
                    <AlertTriangle size={18} />
                    <span>
                      {withNodes(t('settings.upload_dialog_warning'), {
                        check: <code>PRAGMA quick_check</code>,
                      })}
                    </span>
                  </div>
                </div>
              ) : ''
            }
            confirmLabel={restoring ? t('settings.uploading_restoring') : t('settings.upload_restore')}
            pending={restoring}
            onConfirm={handleConfirmUploadRestore}
            onCancel={() => setConfirmUploadRestore(false)}
          />

          {/* Confirm Delete Dialog */}
          <ConfirmDialog
            open={deleteTarget !== null}
            title={t('settings.delete_dialog_title')}
            message={
              deleteTarget
                ? t('settings.delete_dialog_message', {
                    filename: deleteTarget.filename,
                    size: formatBytes(deleteTarget.size),
                  })
                : ''
            }
            confirmLabel={deleting ? t('settings.deleting') : t('settings.delete_snapshot')}
            pending={deleting}
            onConfirm={handleConfirmDelete}
            onCancel={() => setDeleteTarget(null)}
          />

          {/* Reconnecting Modal Overlay */}
          <Modal
            open={reconnecting}
            onClose={() => {}}
            title={t('settings.restoring_title')}
            size="sm"
            dismissible={false}
          >
            <div style={{ textAlign: 'center', padding: '1.5rem 0' }}>
              <RefreshCw size={36} className="spinning text-primary" style={{ margin: '0 auto 1rem', display: 'block' }} />
              <h3 style={{ marginBottom: '0.5rem', fontWeight: 600 }}>{t('settings.applying_snapshot')}</h3>
              <p className="text-muted text-sm">
                {t('settings.restarting_hint')}
              </p>
              <p className="text-muted text-xs" style={{ marginTop: '0.5rem' }}>
                {t('settings.polling_health', { seconds: reconnectCount * 2 })}
              </p>
            </div>
          </Modal>
        </>
      )}
    </div>
  )
}

function PlanTable({ title, rows }: { title: string; rows: DirectoryContactDTO[] }) {
  const { t } = useT()
  if (rows.length === 0) return null
  return (
    <>
      <h3 className="section-title">{title} ({rows.length})</h3>
      <div className="table-wrapper">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('settings.plan_col_name')}</th>
              <th>{t('settings.plan_col_username')}</th>
              <th>{t('settings.plan_col_email')}</th>
              <th>{t('settings.plan_col_department')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.external_id}>
                <td className="font-semibold">{c.display_name}</td>
                <td className="font-mono text-sm">{c.username || '—'}</td>
                <td className="text-sm">{c.email || '—'}</td>
                <td className="text-sm">{c.department || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}
