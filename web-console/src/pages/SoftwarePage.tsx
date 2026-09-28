import React, { useCallback, useEffect, useState } from 'react'
import {
  Package,
  UploadCloud,
  Play,
  Rocket,
  CheckCircle2,
  XCircle,
  Clock,
  Trash2,
  Terminal,
  Layers,
  Search,
  RefreshCw,
  AlertCircle,
  X,
  Server,
} from 'lucide-react'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import { api } from '../services/api'
import type {
  SoftwarePackageDTO,
  SoftwareDeploymentDTO,
  DeploymentTaskDTO,
} from '../types/api'

type PackageType = 'msi' | 'exe' | 'deb' | 'rpm' | 'pkg' | 'script'

export type SoftwareTab = 'packages' | 'deployments'

interface SoftwarePageProps {
  activeTab: SoftwareTab
  onTabChange: (tab: SoftwareTab) => void
}

export const SoftwarePage: React.FC<SoftwarePageProps> = ({ activeTab, onTabChange }) => {
  // Server-side: package upload and deployment creation are technician-or-above,
  // package deletion is admin-only. Reads are open to any authenticated user.
  const { can } = usePermission()
  const canDeploy = can('technician')
  const canAdmin = can('admin')
  const [packages, setPackages] = useState<SoftwarePackageDTO[]>([])
  const [deployments, setDeployments] = useState<SoftwareDeploymentDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [statusMsg, setStatusMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // Upload Modal State
  const [showUploadModal, setShowUploadModal] = useState(false)
  const [uploadName, setUploadName] = useState('')
  const [uploadVersion, setUploadVersion] = useState('')
  const [uploadOS, setUploadOS] = useState<'windows' | 'linux' | 'macos'>('windows')
  const [uploadType, setUploadType] = useState<PackageType>('msi')
  const [uploadInstallArgs, setUploadInstallArgs] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)

  // The format is derived from the chosen file rather than asked for, because
  // the one field an operator can get wrong -- declaring an .exe as 'msi' --
  // produces a task that fails on the endpoint with an exit code nobody can
  // read. Deriving it removes the mismatch instead of explaining it after the
  // fact.
  const detectPackageType = (file: File | null): PackageType => {
    const ext = file?.name.toLowerCase().match(/\.[^.]+$/)?.[0] ?? ''
    if (ext === '.msi' || ext === '.msp') return 'msi'
    if (ext === '.exe') return 'exe'
    if (ext === '.deb') return 'deb'
    if (ext === '.rpm') return 'rpm'
    if (ext === '.pkg' || ext === '.mpkg') return 'pkg'
    return 'script'
  }

  // An .exe with no silent flags opens a setup window on the endpoint and
  // stalls there, so the form blocks the upload instead of dispatching it.
  const needsSilentArgs = uploadType === 'exe' && uploadInstallArgs.trim() === ''

  // Deployment Wizard Modal State
  const [showDeployModal, setShowDeployModal] = useState(false)
  const [deployPackageId, setDeployPackageId] = useState('')
  const [deployName, setDeployName] = useState('')
  const [deployTargetType, setDeployTargetType] = useState<'all' | 'group' | 'device'>('all')
  const [deployTargetId, setDeployTargetId] = useState('')
  const [deploying, setDeploying] = useState(false)

  // Task Details Modal State
  const [viewingDeployment, setViewingDeployment] = useState<SoftwareDeploymentDTO | null>(null)
  const [deploymentTasks, setDeploymentTasks] = useState<DeploymentTaskDTO[]>([])
  const [loadingTasks, setLoadingTasks] = useState(false)
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null)

  // Package deletion moved off window.confirm so the dialog can hold a pending
  // state while the request is in flight.
  const [packagePendingDelete, setPackagePendingDelete] = useState<{ id: string; name: string } | null>(null)
  const [deletingPackage, setDeletingPackage] = useState(false)

  const fetchData = useCallback(async () => {
    setLoading(true)
    try {
      const [pkgs, deps] = await Promise.all([
        api.getPackages(),
        api.getDeployments(),
      ])
      setPackages(pkgs)
      setDeployments(deps)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to fetch data'
      setStatusMsg({ type: 'error', text: msg })
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  const handleUpload = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedFile) {
      setStatusMsg({ type: 'error', text: 'Please select an installer file' })
      return
    }
    if (needsSilentArgs) {
      setStatusMsg({
        type: 'error',
        text: 'This .exe package needs silent-install arguments. Without them the installer opens a window on the endpoint and blocks there.',
      })
      return
    }

    setUploading(true)
    setStatusMsg(null)
    try {
      const formData = new FormData()
      formData.append('name', uploadName)
      formData.append('version', uploadVersion)
      formData.append('os_target', uploadOS)
      formData.append('package_type', uploadType)
      formData.append('install_args', uploadInstallArgs)
      formData.append('file', selectedFile)

      await api.uploadPackage(formData)
      const successText = `Package ${uploadName} uploaded successfully`
      setStatusMsg({ type: 'success', text: successText })
      toast.success(successText, 'Package Uploaded')
      setShowUploadModal(false)
      setUploadName('')
      setUploadVersion('')
      setUploadInstallArgs('')
      setSelectedFile(null)
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Upload failed'
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, 'Upload Failed')
    } finally {
      setUploading(false)
    }
  }

  const handleDeletePackage = async () => {
    if (!packagePendingDelete) return
    const { id, name } = packagePendingDelete
    setDeletingPackage(true)
    try {
      await api.deletePackage(id)
      const successText = `Package ${name} deleted successfully`
      setStatusMsg({ type: 'success', text: successText })
      toast.info(successText)
      setPackagePendingDelete(null)
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to delete package'
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg)
    } finally {
      setDeletingPackage(false)
    }
  }

  const handleCreateDeployment = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!deployPackageId || !deployName) {
      setStatusMsg({ type: 'error', text: 'Please complete all required fields' })
      return
    }

    setDeploying(true)
    setStatusMsg(null)
    try {
      const res = await api.createDeployment({
        name: deployName,
        package_id: deployPackageId,
        target_type: deployTargetType,
        target_id: deployTargetId,
      })
      const successText = `Deployment initiated! ${res.tasks_total} target(s) queued, ${res.dispatched_live} dispatched live.`
      setStatusMsg({
        type: 'success',
        text: successText,
      })
      toast.success(successText, 'Deployment Dispatched')
      setShowDeployModal(false)
      setDeployName('')
      setDeployPackageId('')
      setDeployTargetId('')
      onTabChange('deployments')
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to trigger deployment'
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, 'Deployment Failed')
    } finally {
      setDeploying(false)
    }
  }

  const handleOpenTasks = async (dep: SoftwareDeploymentDTO) => {
    setViewingDeployment(dep)
    setLoadingTasks(true)
    try {
      const tasks = await api.getDeploymentTasks(dep.id)
      setDeploymentTasks(tasks)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to load tasks'
      setStatusMsg({ type: 'error', text: msg })
    } finally {
      setLoadingTasks(false)
    }
  }

  const filteredPackages = packages.filter((p) =>
    p.name.toLowerCase().includes(search.toLowerCase()) ||
    p.file_name.toLowerCase().includes(search.toLowerCase()) ||
    p.version.toLowerCase().includes(search.toLowerCase())
  )

  const filteredDeployments = deployments.filter((d) =>
    d.name.toLowerCase().includes(search.toLowerCase()) ||
    (d.package_name && d.package_name.toLowerCase().includes(search.toLowerCase()))
  )

  const formatBytes = (bytes: number): string => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
  }

  return (
    <div className="page-container">
      {/* Top Header */}
      <div className="page-header">
        <div>
          <h1 className="page-title">
            <Package size={20} className="text-primary" />
            Software Deployment
          </h1>
          <p className="page-subtitle">
            Silent multi-platform distribution with integrity verification and progress tracking
          </p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => fetchData()}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {canDeploy && (
            <>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setShowUploadModal(true)}
              >
                <UploadCloud size={16} />
                <span>Upload Package</span>
              </button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  setDeployPackageId(packages[0]?.id || '')
                  setDeployName(`Deploy ${packages[0]?.name || 'Package'} - ${new Date().toLocaleDateString()}`)
                  setShowDeployModal(true)
                }}
                disabled={packages.length === 0}
              >
                <Play size={16} />
                <span>New Deployment</span>
              </button>
            </>
          )}
        </div>
      </div>

      {/* Notifications */}
      {statusMsg && (
        <div className={`alert-banner ${statusMsg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>
            {statusMsg.type === 'success' ? <CheckCircle2 size={16} /> : <AlertCircle size={16} />}
            {statusMsg.text}
          </span>
          <button
            type="button"
            onClick={() => setStatusMsg(null)}
            className="close-btn"
            aria-label="Dismiss message"
          >
            <X size={16} />
          </button>
        </div>
      )}

      {/* Stats Cards */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Repository Packages</span>
            <span className="kpi-icon">
              <Package size={16} />
            </span>
          </div>
          <div className="kpi-value">{packages.length}</div>
          <div className="kpi-hint">Cross-platform installers ready</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Active Deployments</span>
            <span className="kpi-icon">
              <Play size={16} />
            </span>
          </div>
          <div className="kpi-value">
            {deployments.filter((d) => d.status === 'running').length}
          </div>
          <div className="kpi-hint">In-progress rollout jobs</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Total Endpoints Targeted</span>
            <span className="kpi-icon">
              <Server size={16} />
            </span>
          </div>
          <div className="kpi-value">
            {deployments.reduce((acc, d) => acc + d.total_tasks, 0)}
          </div>
          <div className="kpi-hint">Across all deployment tasks</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Execution Success Rate</span>
            <span className="kpi-icon">
              <CheckCircle2 size={16} />
            </span>
          </div>
          <div className="kpi-value">
            {(() => {
              const total = deployments.reduce((acc, d) => acc + d.total_tasks, 0)
              const success = deployments.reduce((acc, d) => acc + d.success_tasks, 0)
              return total > 0 ? `${((success / total) * 100).toFixed(1)}%` : '100%'
            })()}
          </div>
          <div className="kpi-hint">Cryptographic verified runs</div>
        </div>
      </div>

      {/* Tabs & Search */}
      <div className="table-toolbar">
        <div className="tabs-nav">
          <button
            type="button"
            className={`tab-btn ${activeTab === 'packages' ? 'active' : ''}`}
            onClick={() => onTabChange('packages')}
          >
            <Package size={16} />
            <span>Package Repository ({packages.length})</span>
          </button>
          <button
            type="button"
            className={`tab-btn ${activeTab === 'deployments' ? 'active' : ''}`}
            onClick={() => onTabChange('deployments')}
          >
            <Rocket size={16} />
            <span>Deployments &amp; Rollouts ({deployments.length})</span>
          </button>
        </div>

        <div className="search-wrap">
          <Search size={16} className="search-icon" />
          <input
            type="text"
            className="search-input"
            placeholder={activeTab === 'packages' ? 'Search packages...' : 'Search deployments...'}
            aria-label={activeTab === 'packages' ? 'Search packages' : 'Search deployments'}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* TAB 1: Package Repository */}
      {activeTab === 'packages' && (
        <div className="table-card">
          <DataTable label="Package repository">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Package Name</th>
                  <th>Target OS</th>
                  <th>Type</th>
                  <th>File &amp; Size</th>
                  <th>SHA-256 Hash</th>
                  <th>Install Flags</th>
                  <th className="text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {filteredPackages.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="py-8 text-center text-muted">
                      No software packages in repository. Click "Upload Package" to add your first installer.
                    </td>
                  </tr>
                ) : (
                  filteredPackages.map((pkg) => (
                    <tr key={pkg.id}>
                      <td>
                        <div className="card-title-group">
                          <Package size={16} className="card-icon" />
                          <div>
                            <div className="font-semibold text-main">{pkg.name}</div>
                            <div className="text-sm text-muted">v{pkg.version}</div>
                          </div>
                        </div>
                      </td>
                      <td>
                        <span className="os-badge">{pkg.os_target}</span>
                      </td>
                      <td>
                        <span className="os-badge font-mono">{pkg.package_type}</span>
                      </td>
                      <td>
                        <div title={pkg.file_name}>{pkg.file_name}</div>
                        <div className="text-sm text-dim">{formatBytes(pkg.file_size)}</div>
                      </td>
                      <td>
                        <span className="font-mono text-sm text-dim" title={pkg.sha256}>
                          {pkg.sha256.substring(0, 10)}...{pkg.sha256.substring(pkg.sha256.length - 8)}
                        </span>
                      </td>
                      <td>
                        <span className="font-mono text-sm text-muted">
                          {pkg.install_args || '(default silent)'}
                        </span>
                      </td>
                      <td className="text-right">
                        <div className="action-buttons">
                          {canDeploy && (
                            <button
                              type="button"
                              className="btn btn-sm btn-secondary"
                              onClick={() => {
                                setDeployPackageId(pkg.id)
                                setDeployName(`Deploy ${pkg.name} v${pkg.version}`)
                                setShowDeployModal(true)
                              }}
                            >
                              <Play size={14} />
                              <span>Deploy</span>
                            </button>
                          )}
                          {canAdmin && (
                            <button
                              type="button"
                              className="btn btn-sm btn-secondary"
                              onClick={() => setPackagePendingDelete({ id: pkg.id, name: pkg.name })}
                              aria-label={`Delete package ${pkg.name}`}
                            >
                              <Trash2 size={14} className="text-danger" />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* TAB 2: Deployments & Rollouts */}
      {activeTab === 'deployments' && (
        <div className="table-card">
          <DataTable label="Deployments and rollouts">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Deployment Name</th>
                  <th>Package</th>
                  <th>Target Scope</th>
                  <th>Status</th>
                  <th>Progress</th>
                  <th>Started</th>
                  <th className="text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {filteredDeployments.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="py-8 text-center text-muted">
                      No deployments initiated yet. Select a package and click "Deploy" to start a rollout.
                    </td>
                  </tr>
                ) : (
                  filteredDeployments.map((dep) => {
                    const pct = dep.total_tasks > 0 ? (dep.success_tasks / dep.total_tasks) * 100 : 0
                    return (
                      <tr key={dep.id}>
                        <td>
                          <div className="card-title-group">
                            <Layers size={16} className="card-icon" />
                            <span className="font-semibold text-main">{dep.name}</span>
                          </div>
                        </td>
                        <td>
                          <div>{dep.package_name || dep.package_id}</div>
                          <div className="text-sm text-dim">v{dep.package_version || 'latest'}</div>
                        </td>
                        <td>
                          <span className="os-badge">
                            {dep.target_type} {dep.target_id ? `(${dep.target_id})` : ''}
                          </span>
                        </td>
                        <td>
                          {dep.status === 'completed' ? (
                            <span className="status-pill online">
                              <CheckCircle2 size={12} /> Completed
                            </span>
                          ) : dep.status === 'running' ? (
                            <span className="status-pill primary">
                              <Clock size={12} className="spinning" /> In Progress
                            </span>
                          ) : (
                            <span className="status-pill danger">
                              <XCircle size={12} /> Failed
                            </span>
                          )}
                        </td>
                        <td>
                          <div className="text-sm text-muted">
                            {dep.success_tasks}/{dep.total_tasks} ({pct.toFixed(0)}%)
                          </div>
                          <div className="progress-bar-bg">
                            <div
                              className={`progress-bar-fill ${dep.failed_tasks > 0 ? 'warning' : 'healthy'}`}
                              style={{ width: `${pct}%` }}
                            />
                          </div>
                        </td>
                        <td className="text-sm text-muted">
                          {new Date(dep.created_at).toLocaleString()}
                        </td>
                        <td className="text-right">
                          <button
                            type="button"
                            className="btn btn-sm btn-secondary"
                            onClick={() => handleOpenTasks(dep)}
                          >
                            <Terminal size={14} />
                            <span>View Tasks</span>
                          </button>
                        </td>
                      </tr>
                    )
                  })
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* UPLOAD PACKAGE MODAL */}
      <Modal
        open={showUploadModal}
        onClose={() => !uploading && setShowUploadModal(false)}
        title="Upload Software Package"
        size="md"
        dismissible={!uploading}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setShowUploadModal(false)}
              disabled={uploading}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="upload-package-form"
              className="btn btn-primary"
              disabled={uploading || needsSilentArgs}
            >
              {uploading ? (
                <>
                  <RefreshCw size={16} className="spinning" />
                  <span>Computing SHA-256 &amp; Uploading...</span>
                </>
              ) : (
                'Upload &amp; Index'
              )}
            </button>
          </>
        }
      >
        <form id="upload-package-form" onSubmit={handleUpload}>
          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-name">
                Package Name
              </label>
              <input
                id="pkg-name"
                type="text"
                className="form-input"
                required
                placeholder="e.g. 7-Zip Archiver"
                value={uploadName}
                onChange={(e) => setUploadName(e.target.value)}
              />
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-version">
                Version
              </label>
              <input
                id="pkg-version"
                type="text"
                className="form-input"
                required
                placeholder="e.g. 24.08"
                value={uploadVersion}
                onChange={(e) => setUploadVersion(e.target.value)}
              />
            </div>
          </div>

          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-os">
                Target Operating System
              </label>
              <select
                id="pkg-os"
                className="form-select"
                value={uploadOS}
                onChange={(e) => setUploadOS(e.target.value as typeof uploadOS)}
              >
                <option value="windows">Windows</option>
                <option value="linux">Linux</option>
                <option value="macos">macOS</option>
              </select>
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-type">
                Package Format
              </label>
              <select
                id="pkg-type"
                className="form-select font-mono"
                value={uploadType}
                onChange={(e) => setUploadType(e.target.value as typeof uploadType)}
              >
                {uploadOS === 'windows' && (
                  <>
                    <option value="msi">MSI (Windows Installer)</option>
                    <option value="exe">EXE (Executable)</option>
                    <option value="script">PowerShell Script (.ps1)</option>
                  </>
                )}
                {uploadOS === 'linux' && (
                  <>
                    <option value="deb">DEB (Debian/Ubuntu)</option>
                    <option value="rpm">RPM (RHEL/CentOS)</option>
                    <option value="script">Shell Script (.sh)</option>
                  </>
                )}
                {uploadOS === 'macos' && (
                  <>
                    <option value="pkg">PKG (Apple Installer)</option>
                    <option value="script">Shell Script (.sh)</option>
                  </>
                )}
              </select>
            </div>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="pkg-args">
              Silent Install Arguments
              {uploadType === 'exe' && <span className="form-label-required">required for .exe</span>}
            </label>
            <input
              id="pkg-args"
              type="text"
              className="form-input font-mono"
              required={uploadType === 'exe'}
              placeholder={
                uploadType === 'msi'
                  ? '/qn /norestart'
                  : uploadType === 'exe'
                    ? '/S   (NSIS) · /VERYSILENT (Inno Setup) · /s (WinRAR SFX)'
                    : ''
              }
              value={uploadInstallArgs}
              onChange={(e) => setUploadInstallArgs(e.target.value)}
            />
            <span className="form-hint">
              {uploadType === 'exe'
                ? 'Required. An .exe with no arguments opens a setup window on the endpoint and blocks there, so there is no safe default — the flag depends on which installer built it.'
                : 'Leave blank to use the default silent flags.'}
            </span>
            {needsSilentArgs && selectedFile && (
              <span className="form-hint form-hint-error">
                {selectedFile.name} has no silent-install arguments yet. Add them above before uploading.
              </span>
            )}
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="pkg-file">
              Installer Binary File
            </label>
            <input
              id="pkg-file"
              type="file"
              className="form-input"
              required
              accept=".msi,.msp,.exe,.deb,.rpm,.pkg,.mpkg,.ps1,.sh,.bat,.cmd"
              onChange={(e) => {
                const file = e.target.files?.[0] || null
                setSelectedFile(file)
                if (file) setUploadType(detectPackageType(file))
              }}
            />
            {selectedFile && (
              <span className="form-hint">
                {selectedFile.name} · detected as <strong>{uploadType}</strong> ·{' '}
                {formatBytes(selectedFile.size)}
              </span>
            )}
          </div>
        </form>
      </Modal>

      {/* DEPLOYMENT WIZARD MODAL */}
      <Modal
        open={showDeployModal}
        onClose={() => !deploying && setShowDeployModal(false)}
        title="Launch Software Deployment"
        size="md"
        dismissible={!deploying}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setShowDeployModal(false)}
              disabled={deploying}
            >
              Cancel
            </button>
            <button type="submit" form="deploy-form" className="btn btn-primary" disabled={deploying}>
              {deploying ? (
                <>
                  <RefreshCw size={16} className="spinning" />
                  <span>Dispatching Jobs...</span>
                </>
              ) : (
                'Start Deployment'
              )}
            </button>
          </>
        }
      >
        <form id="deploy-form" onSubmit={handleCreateDeployment}>
          <div className="form-group">
            <label className="form-label" htmlFor="deploy-name">
              Deployment Name
            </label>
            <input
              id="deploy-name"
              type="text"
              className="form-input"
              required
              value={deployName}
              onChange={(e) => setDeployName(e.target.value)}
            />
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="deploy-package">
              Software Package
            </label>
            <select
              id="deploy-package"
              className="form-select"
              value={deployPackageId}
              onChange={(e) => setDeployPackageId(e.target.value)}
            >
              {packages.map((pkg) => (
                <option key={pkg.id} value={pkg.id}>
                  {pkg.name} v{pkg.version} ({pkg.os_target} - {pkg.package_type})
                </option>
              ))}
            </select>
          </div>

          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="deploy-scope">
                Target Scope
              </label>
              <select
                id="deploy-scope"
                className="form-select"
                value={deployTargetType}
                onChange={(e) => setDeployTargetType(e.target.value as typeof deployTargetType)}
              >
                <option value="all">All Compatible Devices</option>
                <option value="group">Static Device Group</option>
                <option value="device">Single Endpoint</option>
              </select>
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="deploy-target-id">
                Target ID
              </label>
              <input
                id="deploy-target-id"
                type="text"
                className="form-input"
                disabled={deployTargetType === 'all'}
                placeholder={deployTargetType === 'all' ? '(Applies to all fleet)' : 'Enter Group ID or Device ID'}
                value={deployTargetId}
                onChange={(e) => setDeployTargetId(e.target.value)}
              />
            </div>
          </div>

          <div className="security-notice">
            <AlertCircle size={16} />
            <span>
              Online endpoints receive execution triggers immediately via persistent WebSocket transport.
              Packages are cryptographically validated against SHA-256 before silent background installation.
            </span>
          </div>
        </form>
      </Modal>

      {/* VIEW TASKS MODAL */}
      <Modal
        open={viewingDeployment !== null}
        onClose={() => setViewingDeployment(null)}
        title="Deployment Tasks"
        size="lg"
        description={
          viewingDeployment
            ? `${viewingDeployment.name} · package ${viewingDeployment.package_name} · target ${viewingDeployment.target_type}`
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setViewingDeployment(null)}>
            Close
          </button>
        }
      >
        {loadingTasks ? (
          <div className="py-8 text-center text-muted">
            <RefreshCw size={24} className="spinning" />
            <div>Loading execution logs...</div>
          </div>
        ) : deploymentTasks.length === 0 ? (
          <div className="py-8 text-center text-muted">No tasks generated for this deployment.</div>
        ) : (
          deploymentTasks.map((t) => (
            <div key={t.id} className="form-group">
              <div className="card-title-group">
                <Server size={16} className="text-muted" />
                <span className="font-mono font-semibold text-main">{t.hostname || t.device_id}</span>
                {t.site && <span className="os-badge">{t.site}</span>}
                <span
                  className={`status-pill ${
                    t.status === 'success'
                      ? 'online'
                      : t.status === 'failed'
                        ? 'danger'
                        : t.status === 'installing' || t.status === 'downloading'
                          ? 'primary'
                          : 'offline'
                  }`}
                >
                  {t.status}
                </span>
                {(t.output_log || t.error_message) && (
                  <button
                    type="button"
                    className="btn-action"
                    onClick={() => setExpandedTaskId(expandedTaskId === t.id ? null : t.id)}
                  >
                    {expandedTaskId === t.id ? 'Hide Logs' : 'View Logs'}
                  </button>
                )}
              </div>

              {expandedTaskId === t.id && (
                <>
                  {t.exit_code !== undefined && t.exit_code !== null && (
                    <div className="text-sm text-muted font-mono">
                      Exit Code: <span className="exit-code-badge">{t.exit_code}</span>
                    </div>
                  )}
                  {t.error_message && <div className="alert-banner alert-error">{t.error_message}</div>}
                  {t.output_log && <pre className="terminal-log-output">{t.output_log}</pre>}
                </>
              )}
            </div>
          ))
        )}
      </Modal>

      <ConfirmDialog
        open={packagePendingDelete !== null}
        title="Delete package"
        message={
          packagePendingDelete
            ? `Delete package "${packagePendingDelete.name}"? This action cannot be undone.`
            : ''
        }
        confirmLabel="Delete"
        pending={deletingPackage}
        onConfirm={handleDeletePackage}
        onCancel={() => setPackagePendingDelete(null)}
      />
    </div>
  )
}
