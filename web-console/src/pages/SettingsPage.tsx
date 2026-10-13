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
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import type {
  BackupFileDTO,
  DirectoryConfigDTO,
  DirectoryContactDTO,
  DirectorySyncPlanDTO,
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

export interface SettingsPageProps {
  activeTab?: 'directory' | 'backup'
  onTabChange?: (tab: 'directory' | 'backup') => void
}

export function SettingsPage({ activeTab = 'directory', onTabChange }: SettingsPageProps) {
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

  const toast = useToast()

  // Load directory config on mount
  useEffect(() => {
    let stopped = false
    setLoadingDir(true)
    api.getDirectoryConfig()
      .then((saved) => { if (!stopped) setConfig(saved) })
      .catch((err: any) => {
        if (!stopped) toast.error(err.message || 'Failed to load directory settings', 'Load Failed')
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
      toast.error(err.message || 'Failed to load backup files', 'Load Failed')
    } finally {
      setLoadingBackups(false)
    }
  }, [])

  // Load backups when switching to backup tab
  useEffect(() => {
    if (activeTab === 'backup') {
      void loadBackups()
    }
  }, [activeTab, loadBackups])

  // Reconnection poller after database restore
  useEffect(() => {
    if (!reconnecting) return
    let stopped = false
    const timer = setInterval(async () => {
      setReconnectCount((c) => c + 1)
      try {
        const res = await fetch('/healthz')
        if (res.ok && !stopped) {
          clearInterval(timer)
          toast.success('Server is back online with restored database.', 'Restore Complete')
          setTimeout(() => {
            window.location.reload()
          }, 1200)
        }
      } catch {
        // Server still restarting / reconnecting, keep polling
      }
    }, 2000)
    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [reconnecting])

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
      toast.error(err.message || 'Failed to load directory settings', 'Load Failed')
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
      toast.success('Directory settings saved.', 'Settings Saved')
    } catch (err: any) {
      toast.error(err.message || 'Failed to save directory settings', 'Save Failed')
    } finally {
      setSavingDir(false)
    }
  }

  const testConnection = async () => {
    setTestingDir(true)
    setTestResult(null)
    try {
      const res = await api.testDirectoryConnection()
      setTestResult({ ok: res.ok, message: `${res.message} ${res.entries_seen} entries matched the filter.` })
      if (res.ok) toast.success(`Directory reachable — ${res.entries_seen} entries.`, 'Connection OK')
    } catch (err: any) {
      setTestResult({ ok: false, message: err.message || 'Could not reach the directory' })
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
        toast.error('The directory answered but matched nothing — check the base DN and the search filter.', 'Nothing Matched')
      }
    } catch (err: any) {
      toast.error(err.message || 'Preview failed', 'Preview Failed')
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
        toast.success('Already up to date — no changes.', 'Sync Complete')
      } else {
        toast.success(
          `${applied.adds.length} added, ${applied.updates.length} updated, ${applied.deactivations.length} deactivated.`,
          'Sync Complete'
        )
      }
    } catch (err: any) {
      toast.error(err.message || 'Sync failed', 'Sync Failed')
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
      toast.success(`Created manual snapshot: ${created.filename}`, 'Snapshot Created')
      await loadBackups()
    } catch (err: any) {
      toast.error(err.message || 'Failed to create snapshot', 'Backup Failed')
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
      toast.success(`Live database snapshot downloaded (${formatBytes(blob.size)}).`, 'Download Complete')
    } catch (err: any) {
      toast.error(err.message || 'Failed to download live database', 'Download Failed')
    } finally {
      setDownloadingActive(false)
    }
  }

  const handleDownloadFile = async (filename: string) => {
    setDownloadingFile(filename)
    try {
      const blob = await api.downloadBackup(filename)
      triggerBlobDownload(blob, filename)
      toast.success(`Downloaded ${filename} (${formatBytes(blob.size)}).`, 'Download Complete')
    } catch (err: any) {
      toast.error(err.message || 'Failed to download backup file', 'Download Failed')
    } finally {
      setDownloadingFile(null)
    }
  }

  const handleConfirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteBackup(deleteTarget.filename)
      toast.success(`Deleted snapshot: ${deleteTarget.filename}`, 'Snapshot Deleted')
      setDeleteTarget(null)
      await loadBackups()
    } catch (err: any) {
      toast.error(err.message || 'Failed to delete snapshot', 'Delete Failed')
    } finally {
      setDeleting(false)
    }
  }

  const handleConfirmRestore = async () => {
    if (!restoreTarget) return
    setRestoring(true)
    try {
      const res = await api.restoreBackup(restoreTarget.filename)
      toast.info(res.message || 'Restore verified. Server is restarting...', 'Restoring Database')
      setRestoreTarget(null)
      setReconnecting(true)
    } catch (err: any) {
      toast.error(err.message || 'Restore verification failed', 'Restore Failed')
      setRestoring(false)
    }
  }

  const handleConfirmUploadRestore = async () => {
    if (!uploadFile) return
    setRestoring(true)
    try {
      const res = await api.uploadAndRestoreBackup(uploadFile)
      toast.info(res.message || 'Uploaded backup verified. Server restarting...', 'Restoring Database')
      setConfirmUploadRestore(false)
      setUploadFile(null)
      setReconnecting(true)
    } catch (err: any) {
      toast.error(err.message || 'Upload & restore failed', 'Restore Failed')
      setRestoring(false)
    }
  }

  const getBackupTypeBadge = (b: BackupFileDTO) => {
    if (b.filename.includes('pre-restore')) {
      return <span className="status-pill warning"><span className="dot" />Safety Snapshot</span>
    }
    if (b.is_auto) {
      return <span className="status-pill online"><span className="dot" />Automatic (Daily)</span>
    }
    if (b.filename.includes('uploaded')) {
      return <span className="status-pill primary"><span className="dot" />Uploaded .db</span>
    }
    return <span className="status-pill primary"><span className="dot" />Manual Snapshot</span>
  }

  const pending = plan ? plan.adds.length + plan.updates.length + plan.deactivations.length : 0
  const busyDir = syncingDir || applyingSync
  const totalBackupBytes = backups.reduce((acc, b) => acc + b.size, 0)

  return (
    <div className="page-container">
      {/* Top Header */}
      <div className="page-header">
        <div>
          <h1 className="page-title">Settings</h1>
          <p className="page-subtitle">
            {activeTab === 'directory'
              ? 'Directory (LDAP / Active Directory) synchronization and account mapping'
              : 'Enterprise SQLite hot snapshot backup, file download, and point-in-time disaster recovery'}
          </p>
        </div>
        <div className="header-controls">
          {activeTab === 'directory' ? (
            <button type="button" className="btn btn-secondary" onClick={reloadDir} disabled={loadingDir}>
              <RefreshCw size={16} className={loadingDir ? 'spinning' : ''} />
              <span>Reload</span>
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
                <span>{creatingBackup ? 'Creating Snapshot…' : 'Create Snapshot'}</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={handleDownloadActive}
                disabled={downloadingActive || creatingBackup}
              >
                <Download size={16} className={downloadingActive ? 'spinning' : ''} />
                <span>{downloadingActive ? 'Streaming Live .db…' : 'Download Live Database'}</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={loadBackups}
                disabled={loadingBackups}
              >
                <RefreshCw size={16} className={loadingBackups ? 'spinning' : ''} />
                <span>Reload</span>
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
            <span>Directory sync</span>
          </button>
          <button
            type="button"
            className={`tab-btn ${activeTab === 'backup' ? 'active' : ''}`}
            onClick={() => onTabChange?.('backup')}
          >
            <Database size={16} />
            <span>Backup &amp; restore ({backups.length})</span>
          </button>
        </div>
      </div>

      {/* Directory Tab Content */}
      {activeTab === 'directory' && (
        <>
          <div className="security-notice">
            <PlugZap size={16} />
            <div>
              <strong>Synced people are not console accounts.</strong> They have no password and cannot log
              in here. Their only use is as the list a hardware asset's PIC is chosen from.
            </div>
          </div>

          <form className="table-card" onSubmit={saveDir}>
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><PlugZap size={18} /></span>
                <div>
                  <h2 className="card-title">Connection</h2>
                  <span className="text-muted text-sm">
                    Use a read-only service account, never your own login.
                  </span>
                </div>
              </div>
            </div>

            <div className="table-responsive">
              <div className="form-group">
                <label className="form-label" htmlFor="dir-host">Host</label>
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
                  <label className="form-label" htmlFor="dir-port">Port</label>
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
                  <span className="form-hint">636 is LDAPS; 389 with TLS on is StartTLS.</span>
                </div>
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-tls">Transport</label>
                  <select
                    id="dir-tls"
                    className="form-select"
                    value={config.use_tls ? '1' : '0'}
                    onChange={(e) => updateConfig({ use_tls: e.target.value === '1' })}
                  >
                    <option value="1">TLS</option>
                    <option value="0">Plain</option>
                  </select>
                  <span className="form-hint">
                    Plain sends the bind password in the clear. Only for a tunnel you control.
                  </span>
                </div>
              </div>

              <div className="form-group">
                <label className="form-label" htmlFor="dir-basedn">Base DN</label>
                <input
                  id="dir-basedn"
                  className="form-input"
                  placeholder="DC=example,DC=com"
                  value={config.base_dn}
                  onChange={(e) => updateConfig({ base_dn: e.target.value })}
                />
                <span className="form-hint">The subtree to search. Everything under it is read.</span>
              </div>

              <div className="form-row">
                <div className="form-field">
                  <label className="form-label" htmlFor="dir-binddn">Bind DN</label>
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
                  <label className="form-label" htmlFor="dir-filter">Search filter</label>
                  <input
                    id="dir-filter"
                    className="form-input"
                    value={config.search_filter}
                    onChange={(e) => updateConfig({ search_filter: e.target.value })}
                  />
                </div>
              </div>

              <div className="form-group">
                <span className="form-label">Bind password</span>
                <p className="text-muted text-sm">
                  {config.bind_password_configured
                    ? 'Set in the LDAP_BIND_PASSWORD environment variable. It is never shown, never stored in the database, and cannot be changed from here.'
                    : 'Not set. Export LDAP_BIND_PASSWORD and restart the server before syncing.'}
                </p>
              </div>

              <div className="row-actions">
                <button type="submit" className="btn btn-primary" disabled={savingDir || loadingDir}>
                  <Save size={16} />
                  <span>{savingDir ? 'Saving…' : 'Save settings'}</span>
                </button>
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={testConnection}
                  disabled={testingDir || loadingDir || syncingDir}
                >
                  <PlugZap size={16} />
                  <span>{testingDir ? 'Testing…' : 'Test connection'}</span>
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
                  <h2 className="card-title">Sync</h2>
                  <span className="text-muted text-sm">
                    Preview shows what would change. Nothing is written until Apply.
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
                  <span>Preview sync</span>
                </button>
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => setConfirmApply(true)}
                  disabled={busyDir || loadingDir || plan === null || pending === 0}
                >
                  <Save size={16} />
                  <span>Apply</span>
                </button>
              </div>

              {!plan && (
                <p className="text-muted">
                  Run a preview to see what a sync would do. Nothing has been read from the directory yet.
                </p>
              )}

              {plan && plan.total_in_directory === 0 && (
                <div className="error-banner">
                  <XCircle size={16} />
                  <span>
                    The directory answered but returned no entries. A base DN that matches nothing looks
                    exactly like an up-to-date directory, so check the base DN and filter before trusting
                    a zero here.
                  </span>
                </div>
              )}

              {plan && pending === 0 && plan.total_in_directory > 0 && (
                <div className="alert-banner">
                  <CheckCircle2 size={16} />
                  <span>
                    Nothing to do. {plan.unchanged} of {plan.total_in_directory} entries already match.
                  </span>
                </div>
              )}

              {plan && pending > 0 && (
                <>
                  <PlanTable title="To add" rows={plan.adds} />
                  <PlanTable title="To update" rows={plan.updates} />
                  <PlanTable title="To deactivate" rows={plan.deactivations} />
                </>
              )}
            </div>
          </div>

          <ConfirmDialog
            open={confirmApply}
            title="Apply directory sync"
            message={
              plan
                ? `Write ${plan.adds.length} new, ${plan.updates.length} updated and ${plan.deactivations.length} deactivated contacts? ` +
                  'Deactivated contacts drop out of every PIC dropdown and cannot come back without another sync. ' +
                  'The server re-reads the directory on confirm, so these numbers can shift if it changed in the meantime.'
                : ''
            }
            confirmLabel="Apply sync"
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
              <strong>Enterprise Disaster Recovery:</strong> Live database hot snapshots use SQLite{' '}
              <code>VACUUM INTO</code> for non-blocking, defragmented snapshots. Before any restore, an
              automated pre-restore safety snapshot (<code>endpoint-mgmt-pre-restore-*.db</code>) is
              captured, and all incoming backup files are verified via <code>PRAGMA quick_check</code>.
            </div>
          </div>

          {/* Stats KPI Cards */}
          <div className="kpi-grid">
            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">Available Snapshots</span>
                <span className="kpi-icon"><Database size={16} /></span>
              </div>
              <div className="kpi-value">{backups.length}</div>
              <div className="kpi-hint">Historical files in server backup storage</div>
            </div>

            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">Total Backup Storage</span>
                <span className="kpi-icon"><HardDrive size={16} /></span>
              </div>
              <div className="kpi-value">{formatBytes(totalBackupBytes)}</div>
              <div className="kpi-hint">Retained database snapshots size</div>
            </div>

            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">Latest Snapshot</span>
                <span className="kpi-icon"><CheckCircle2 size={16} /></span>
              </div>
              <div className="kpi-value text-base font-semibold">
                {backups.length > 0 ? formatDateTime(backups[0].created_at) : 'None'}
              </div>
              <div className="kpi-hint">
                {backups.length > 0 ? backups[0].filename : 'No snapshots taken yet'}
              </div>
            </div>
          </div>

          {/* Upload External Backup Card */}
          <div className="table-card" style={{ marginBottom: '1.5rem' }}>
            <div className="card-header">
              <div className="card-title-group">
                <span className="card-icon"><UploadCloud size={18} /></span>
                <div>
                  <h2 className="card-title">Upload &amp; Restore External Backup</h2>
                  <span className="text-muted text-sm">
                    Upload a <code>.db</code> file from another host or offline storage. Verified with quick_check prior to restore.
                  </span>
                </div>
              </div>
            </div>

            <div className="table-responsive">
              <div className="form-row" style={{ alignItems: 'flex-end', gap: '1rem' }}>
                <div className="form-field" style={{ flex: 1 }}>
                  <label className="form-label" htmlFor="upload-backup-file">Select SQLite Database File (*.db)</label>
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
                      Selected: <strong>{uploadFile.name}</strong> ({formatBytes(uploadFile.size)})
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
                    <span>Upload &amp; Restore</span>
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
                  <h2 className="card-title">Server Backup Snapshots</h2>
                  <span className="text-muted text-sm">
                    Snapshots stored in the server backup directory. Select a file to download, restore, or delete.
                  </span>
                </div>
              </div>
            </div>

            <div className="table-wrapper">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Filename</th>
                    <th>Type</th>
                    <th>Size</th>
                    <th>Created At</th>
                    <th style={{ textAlign: 'right' }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {loadingBackups ? (
                    <tr>
                      <td colSpan={5} className="text-center py-8 text-muted">
                        Loading backup files…
                      </td>
                    </tr>
                  ) : backups.length === 0 ? (
                    <tr>
                      <td colSpan={5} className="text-center py-8 text-muted">
                        No backup files found. Click &quot;Create Snapshot&quot; above to take a manual hot snapshot.
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
                              title="Download backup file"
                            >
                              <Download size={14} className={downloadingFile === b.filename ? 'spinning' : ''} />
                              <span>Download</span>
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              style={{ borderColor: 'rgba(239, 68, 68, 0.4)', color: '#f87171' }}
                              onClick={() => setRestoreTarget(b)}
                              title="Restore system from this backup"
                            >
                              <RotateCcw size={14} />
                              <span>Restore</span>
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => setDeleteTarget(b)}
                              title="Delete backup snapshot"
                            >
                              <Trash2 size={14} />
                              <span>Delete</span>
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
            title="Restore Database from Snapshot"
            message={
              restoreTarget ? (
                <div>
                  <p style={{ marginBottom: '0.75rem' }}>
                    Are you sure you want to restore the database from{' '}
                    <strong>{restoreTarget.filename}</strong>?
                  </p>
                  <div className="alert-banner" style={{ borderColor: 'rgba(234, 179, 8, 0.4)', color: '#fde047' }}>
                    <AlertTriangle size={18} />
                    <span>
                      An automatic safety backup (<code>endpoint-mgmt-pre-restore-*.db</code>) will be created immediately before restore.
                      The server service will restart to reload the new database.
                    </span>
                  </div>
                </div>
              ) : ''
            }
            confirmLabel={restoring ? 'Verifying & Restoring…' : 'Verify & Restore'}
            pending={restoring}
            onConfirm={handleConfirmRestore}
            onCancel={() => setRestoreTarget(null)}
          />

          {/* Confirm Upload & Restore Dialog */}
          <ConfirmDialog
            open={confirmUploadRestore}
            title="Restore from Uploaded Database"
            message={
              uploadFile ? (
                <div>
                  <p style={{ marginBottom: '0.75rem' }}>
                    Are you sure you want to upload and restore from{' '}
                    <strong>{uploadFile.name}</strong> ({formatBytes(uploadFile.size)})?
                  </p>
                  <div className="alert-banner" style={{ borderColor: 'rgba(234, 179, 8, 0.4)', color: '#fde047' }}>
                    <AlertTriangle size={18} />
                    <span>
                      The uploaded file will be verified using <code>PRAGMA quick_check</code>.
                      An automatic safety snapshot will be taken before the active database is replaced.
                    </span>
                  </div>
                </div>
              ) : ''
            }
            confirmLabel={restoring ? 'Uploading & Restoring…' : 'Upload & Restore'}
            pending={restoring}
            onConfirm={handleConfirmUploadRestore}
            onCancel={() => setConfirmUploadRestore(false)}
          />

          {/* Confirm Delete Dialog */}
          <ConfirmDialog
            open={deleteTarget !== null}
            title="Delete Backup Snapshot"
            message={
              deleteTarget
                ? `Are you sure you want to delete "${deleteTarget.filename}" (${formatBytes(deleteTarget.size)})? This cannot be undone.`
                : ''
            }
            confirmLabel={deleting ? 'Deleting…' : 'Delete Snapshot'}
            pending={deleting}
            onConfirm={handleConfirmDelete}
            onCancel={() => setDeleteTarget(null)}
          />

          {/* Reconnecting Modal Overlay */}
          <Modal
            open={reconnecting}
            onClose={() => {}}
            title="Restoring Database…"
            size="sm"
            dismissible={false}
          >
            <div style={{ textAlign: 'center', padding: '1.5rem 0' }}>
              <RefreshCw size={36} className="spinning text-primary" style={{ margin: '0 auto 1rem', display: 'block' }} />
              <h3 style={{ marginBottom: '0.5rem', fontWeight: 600 }}>Applying Database Snapshot</h3>
              <p className="text-muted text-sm">
                The database has been replaced and the server service is restarting.
              </p>
              <p className="text-muted text-xs" style={{ marginTop: '0.5rem' }}>
                Polling server health (/healthz): {reconnectCount * 2}s elapsed…
              </p>
            </div>
          </Modal>
        </>
      )}
    </div>
  )
}

function PlanTable({ title, rows }: { title: string; rows: DirectoryContactDTO[] }) {
  if (rows.length === 0) return null
  return (
    <>
      <h3 className="section-title">{title} ({rows.length})</h3>
      <div className="table-wrapper">
        <table className="data-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Username</th>
              <th>Email</th>
              <th>Department</th>
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
