import React, { useCallback, useEffect, useMemo, useState } from 'react'
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
  Edit2,
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
  DeviceDTO,
  DeviceGroupDTO,
} from '../types/api'
import { useT } from '../i18n/core'

// descKey is a dictionary key, looked up with t() at render time.
const INSTALL_ARG_PRESETS = [
  { label: 'NSIS (/S)', args: '/S', descKey: 'software.preset_nsis_desc' },
  { label: 'Inno Setup (/VERYSILENT ...)', args: '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART', descKey: 'software.preset_inno_desc' },
  { label: 'InstallShield (/s /v"/qn")', args: '/s /v"/qn"', descKey: 'software.preset_installshield_desc' },
  { label: 'Generic SFX (/s)', args: '/s', descKey: 'software.preset_sfx_desc' },
  { label: 'MSI wrapper (/quiet /norestart)', args: '/quiet /norestart', descKey: 'software.preset_msi_desc' },
]

const UNINSTALL_ARG_PRESETS = [
  { label: 'NSIS (/S)', args: '/S' },
  { label: 'Inno Setup (/VERYSILENT ...)', args: '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART' },
  { label: 'MSI (/qn /norestart)', args: '/qn /norestart' },
  { label: 'InstallShield (/s)', args: '/s' },
]

type PackageType = 'msi' | 'exe' | 'deb' | 'rpm' | 'pkg' | 'script'

// Server enums with a translated label; anything else is shown as sent.
const TARGET_KEYS: Record<string, string> = {
  all: 'software.target_all_short',
  group: 'software.target_group_short',
  device: 'software.target_device_short',
}
const TASK_STATUS_KEYS: Record<string, string> = {
  pending: 'software.task_pending',
  queued: 'software.task_queued',
  dispatched: 'software.task_dispatched',
  downloading: 'software.task_downloading',
  installing: 'software.task_installing',
  uninstalling: 'software.task_uninstalling',
  success: 'software.task_success',
  failed: 'software.task_failed',
  failed_lost: 'software.task_failed_lost',
}

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
  const { t } = useT()

  // Upload Modal State
  const [showUploadModal, setShowUploadModal] = useState(false)
  const [uploadName, setUploadName] = useState('')
  const [uploadVersion, setUploadVersion] = useState('')
  const [uploadOS, setUploadOS] = useState<'windows' | 'linux' | 'macos'>('windows')
  const [uploadType, setUploadType] = useState<PackageType>('msi')
  const [uploadInstallArgs, setUploadInstallArgs] = useState('')
  const [uploadUninstallArgs, setUploadUninstallArgs] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)

  // Edit Package Arguments State
  const [editingPackage, setEditingPackage] = useState<SoftwarePackageDTO | null>(null)
  const [editName, setEditName] = useState('')
  const [editVersion, setEditVersion] = useState('')
  const [editInstallArgs, setEditInstallArgs] = useState('')
  const [editUninstallArgs, setEditUninstallArgs] = useState('')
  const [savingEdit, setSavingEdit] = useState(false)

  // Endpoints and Groups
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])

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
  // 'install' is the default so the form behaves exactly as it did before
  // uninstall existed. The choice is explicit rather than inferred from a
  // checkbox, because the two verbs are not reversible: there is no undo on the
  // endpoint, and a mis-set toggle would remove software rather than add it.
  const [deployAction, setDeployAction] = useState<'install' | 'uninstall'>('install')

  // The package the operator picked, so the form can say up front that a package
  // with no uninstall arguments cannot be removed rather than letting the server
  // reject it after a click.
  const selectedDeployPkg = useMemo(
    () => packages.find((p) => p.id === deployPackageId),
    [packages, deployPackageId]
  )

  const selectedDevice = useMemo(
    () => devices.find((d) => d.id === deployTargetId),
    [devices, deployTargetId]
  )

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
      const [pkgs, deps, devListRes, grpList] = await Promise.all([
        api.getPackages(),
        api.getDeployments(),
        api.getDevices(100).catch(() => ({ devices: [] as DeviceDTO[] })),
        api.getDeviceGroups().catch(() => [] as DeviceGroupDTO[]),
      ])
      setPackages(pkgs)
      setDeployments(deps)
      setDevices(devListRes.devices || [])
      setGroups(grpList || [])
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('software.fetch_failed')
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
      setStatusMsg({ type: 'error', text: t('software.select_installer') })
      return
    }
    if (needsSilentArgs) {
      setStatusMsg({
        type: 'error',
        text: t('software.exe_needs_args'),
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
      formData.append('uninstall_args', uploadUninstallArgs)
      formData.append('file', selectedFile)

      await api.uploadPackage(formData)
      const successText = t('software.uploaded', { name: uploadName })
      setStatusMsg({ type: 'success', text: successText })
      toast.success(successText, t('software.uploaded_title'))
      setShowUploadModal(false)
      setUploadName('')
      setUploadVersion('')
      setUploadInstallArgs('')
      setUploadUninstallArgs('')
      setSelectedFile(null)
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('software.upload_failed')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, t('software.upload_failed_title'))
    } finally {
      setUploading(false)
    }
  }

  const handleSaveEdit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!editingPackage) return

    if (editingPackage.package_type === 'exe' && editInstallArgs.trim() === '') {
      const msg = t('software.exe_args_required')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg)
      return
    }

    setSavingEdit(true)
    try {
      await api.updatePackage(editingPackage.id, {
        name: editName,
        version: editVersion,
        install_args: editInstallArgs,
        uninstall_args: editUninstallArgs,
      })
      const successText = t('software.updated', { name: editName })
      setStatusMsg({ type: 'success', text: successText })
      toast.success(successText, t('software.updated_title'))
      setEditingPackage(null)
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('software.update_failed')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, t('software.update_failed_title'))
    } finally {
      setSavingEdit(false)
    }
  }

  const handleDeletePackage = async () => {
    if (!packagePendingDelete) return
    const { id, name } = packagePendingDelete
    setDeletingPackage(true)
    try {
      await api.deletePackage(id)
      const successText = t('software.deleted', { name })
      setStatusMsg({ type: 'success', text: successText })
      toast.info(successText)
      setPackagePendingDelete(null)
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('software.delete_failed')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg)
    } finally {
      setDeletingPackage(false)
    }
  }

  const handleCreateDeployment = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!deployPackageId || !deployName) {
      setStatusMsg({ type: 'error', text: t('software.complete_required') })
      return
    }

    if (deployTargetType === 'device' && !deployTargetId) {
      const msg = t('software.select_endpoint_error')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg)
      return
    }
    if (deployTargetType === 'group' && !deployTargetId) {
      const msg = t('software.select_group_error')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg)
      return
    }

    // The package being chosen is what the server checks, so the form checks it
    // too and says why rather than letting the request come back with a message
    // about a field the operator never saw.
    const selected = packages.find((p) => p.id === deployPackageId)
    if (deployAction === 'uninstall' && selected && !selected.uninstall_args?.trim()) {
      const msg = t('software.no_uninstall_args', { name: selected.name })
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, t('software.cannot_uninstall'))
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
        action: deployAction,
      })
      const isRemoval = deployAction === 'uninstall'
      const successText = t(isRemoval ? 'software.removal_initiated' : 'software.deployment_initiated', {
        total: res.tasks_total,
        live: res.dispatched_live,
      })
      setStatusMsg({
        type: 'success',
        text: successText,
      })
      toast.success(successText, t(isRemoval ? 'software.removal_dispatched' : 'software.deployment_dispatched'))
      setShowDeployModal(false)
      setDeployName('')
      setDeployPackageId('')
      setDeployTargetId('')
      setDeployAction('install')
      onTabChange('deployments')
      fetchData()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('software.deploy_failed')
      setStatusMsg({ type: 'error', text: msg })
      toast.error(msg, t('software.deploy_failed_title'))
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
      const msg = err instanceof Error ? err.message : t('software.tasks_load_failed')
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
            {t('software.title')}
          </h1>
          <p className="page-subtitle">
            {t('software.subtitle')}
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
            <span>{t('software.refresh')}</span>
          </button>
          {canDeploy && (
            <>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setShowUploadModal(true)}
              >
                <UploadCloud size={16} />
                <span>{t('software.upload_package')}</span>
              </button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  setDeployPackageId(packages[0]?.id || '')
                  setDeployName(t('software.default_deploy_name_dated', { name: packages[0]?.name || t('software.package'), date: new Date().toLocaleDateString() }))
                  setShowDeployModal(true)
                }}
                disabled={packages.length === 0}
              >
                <Play size={16} />
                <span>{t('software.new_deployment')}</span>
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
            aria-label={t('software.dismiss_message')}
          >
            <X size={16} />
          </button>
        </div>
      )}

      {/* Stats Cards */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('software.kpi_packages')}</span>
            <span className="kpi-icon">
              <Package size={16} />
            </span>
          </div>
          <div className="kpi-value">{packages.length}</div>
          <div className="kpi-hint">{t('software.kpi_packages_hint')}</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('software.kpi_active')}</span>
            <span className="kpi-icon">
              <Play size={16} />
            </span>
          </div>
          <div className="kpi-value">
            {deployments.filter((d) => d.status === 'running').length}
          </div>
          <div className="kpi-hint">{t('software.kpi_active_hint')}</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('software.kpi_targeted')}</span>
            <span className="kpi-icon">
              <Server size={16} />
            </span>
          </div>
          <div className="kpi-value">
            {deployments.reduce((acc, d) => acc + d.total_tasks, 0)}
          </div>
          <div className="kpi-hint">{t('software.kpi_targeted_hint')}</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('software.kpi_success')}</span>
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
          <div className="kpi-hint">{t('software.kpi_success_hint')}</div>
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
            <span>{t('software.tab_packages', { count: packages.length })}</span>
          </button>
          <button
            type="button"
            className={`tab-btn ${activeTab === 'deployments' ? 'active' : ''}`}
            onClick={() => onTabChange('deployments')}
          >
            <Rocket size={16} />
            <span>{t('software.tab_deployments', { count: deployments.length })}</span>
          </button>
        </div>

        <div className="search-wrap">
          <Search size={16} className="search-icon" />
          <input
            type="text"
            className="search-input"
            placeholder={activeTab === 'packages' ? t('software.search_packages_placeholder') : t('software.search_deployments_placeholder')}
            aria-label={activeTab === 'packages' ? t('software.search_packages') : t('software.search_deployments')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* TAB 1: Package Repository */}
      {activeTab === 'packages' && (
        <div className="table-card">
          <DataTable label={t('software.packages_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('software.col_package_name')}</th>
                  <th>{t('software.col_target_os')}</th>
                  <th>{t('software.col_type')}</th>
                  <th>{t('software.col_file_size')}</th>
                  <th>{t('software.col_hash')}</th>
                  <th>{t('software.col_install_flags')}</th>
                  <th className="text-right">{t('software.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {filteredPackages.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="py-8 text-center text-muted">
                      {t('software.no_packages')}
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
                          {pkg.install_args || t('software.default_silent')}
                        </span>
                      </td>
                      <td className="text-right">
                        <div className="action-buttons">
                          {canDeploy && (
                            <>
                              <button
                                type="button"
                                className="btn btn-sm btn-secondary"
                                title={t('software.edit_switches')}
                                onClick={() => {
                                  setEditingPackage(pkg)
                                  setEditName(pkg.name)
                                  setEditVersion(pkg.version)
                                  setEditInstallArgs(pkg.install_args || '')
                                  setEditUninstallArgs(pkg.uninstall_args || '')
                                }}
                              >
                                <Edit2 size={14} />
                                <span>{t('software.edit')}</span>
                              </button>
                              <button
                                type="button"
                                className="btn btn-sm btn-secondary"
                                onClick={() => {
                                  setDeployPackageId(pkg.id)
                                  setDeployName(t('software.default_deploy_name', { name: pkg.name, version: pkg.version }))
                                  const onlineDev = devices.find((d) => d.status === 'online') || devices[0]
                                  if (onlineDev && !deployTargetId) {
                                    setDeployTargetId(onlineDev.id)
                                  }
                                  setShowDeployModal(true)
                                }}
                              >
                                <Play size={14} />
                                <span>{t('software.deploy')}</span>
                              </button>
                            </>
                          )}
                          {canAdmin && (
                            <button
                              type="button"
                              className="btn btn-sm btn-secondary"
                              onClick={() => setPackagePendingDelete({ id: pkg.id, name: pkg.name })}
                              aria-label={t('software.delete_package_label', { name: pkg.name })}
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
          <DataTable label={t('software.deployments_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('software.col_deployment_name')}</th>
                  <th>{t('software.col_operation')}</th>
                  <th>{t('software.col_package')}</th>
                  <th>{t('software.col_target_scope')}</th>
                  <th>{t('software.col_status')}</th>
                  <th>{t('software.col_progress')}</th>
                  <th>{t('software.col_started')}</th>
                  <th className="text-right">{t('software.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {filteredDeployments.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="py-8 text-center text-muted">
                      {t('software.no_deployments')}
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
                          {/* An install and a removal are opposite outcomes on the
                              endpoint, so the list says which one this is rather
                              than leaving an operator to infer it from the name. */}
                          {dep.action === 'uninstall' ? (
                            <span className="status-pill danger">
                              <Trash2 size={12} /> {t('software.op_uninstall')}
                            </span>
                          ) : (
                            <span className="status-pill online">
                              <Play size={12} /> {t('software.op_install')}
                            </span>
                          )}
                        </td>
                        <td>
                          <div>{dep.package_name || dep.package_id}</div>
                          <div className="text-sm text-dim">v{dep.package_version || t('software.latest')}</div>
                        </td>
                        <td>
                          <span className="os-badge">
                            {TARGET_KEYS[dep.target_type] ? t(TARGET_KEYS[dep.target_type]) : dep.target_type} {dep.target_id ? `(${dep.target_id})` : ''}
                          </span>
                        </td>
                        <td>
                          {dep.status === 'completed' ? (
                            <span className="status-pill online">
                              <CheckCircle2 size={12} /> {t('software.status_completed')}
                            </span>
                          ) : dep.status === 'running' ? (
                            <span className="status-pill primary">
                              <Clock size={12} className="spinning" /> {t('software.status_in_progress')}
                            </span>
                          ) : (
                            <span className="status-pill danger">
                              <XCircle size={12} /> {t('software.status_failed')}
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
                            <span>{t('software.view_tasks')}</span>
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
        title={t('software.upload_modal_title')}
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
              {t('common.cancel')}
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
                  <span>{t('software.uploading')}</span>
                </>
              ) : (
                t('software.upload_index')
              )}
            </button>
          </>
        }
      >
        <form id="upload-package-form" onSubmit={handleUpload}>
          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-name">
                {t('software.col_package_name')}
              </label>
              <input
                id="pkg-name"
                type="text"
                className="form-input"
                required
                placeholder={t('software.name_placeholder')}
                value={uploadName}
                onChange={(e) => setUploadName(e.target.value)}
              />
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-version">
                {t('software.version')}
              </label>
              <input
                id="pkg-version"
                type="text"
                className="form-input"
                required
                placeholder={t('software.version_placeholder')}
                value={uploadVersion}
                onChange={(e) => setUploadVersion(e.target.value)}
              />
            </div>
          </div>

          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="pkg-os">
                {t('software.target_os_full')}
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
                {t('software.package_format')}
              </label>
              <select
                id="pkg-type"
                className="form-select font-mono"
                value={uploadType}
                onChange={(e) => setUploadType(e.target.value as typeof uploadType)}
              >
                {uploadOS === 'windows' && (
                  <>
                    <option value="msi">{t('software.format_msi')}</option>
                    <option value="exe">{t('software.format_exe')}</option>
                    <option value="script">{t('software.format_ps1')}</option>
                  </>
                )}
                {uploadOS === 'linux' && (
                  <>
                    <option value="deb">{t('software.format_deb')}</option>
                    <option value="rpm">{t('software.format_rpm')}</option>
                    <option value="script">{t('software.format_sh')}</option>
                  </>
                )}
                {uploadOS === 'macos' && (
                  <>
                    <option value="pkg">{t('software.format_pkg')}</option>
                    <option value="script">{t('software.format_sh')}</option>
                  </>
                )}
              </select>
            </div>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="pkg-args">
              {t('software.install_args')}
              {uploadType === 'exe' && <span className="form-label-required">{t('software.required_for_exe')}</span>}
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
                    ? '/S (NSIS) · /VERYSILENT (Inno Setup) · /s (WinRAR SFX)'
                    : ''
              }
              value={uploadInstallArgs}
              onChange={(e) => setUploadInstallArgs(e.target.value)}
            />
            {uploadType === 'exe' && (
              <div className="mt-2 flex flex-wrap gap-1.5 items-center">
                <span className="text-xs text-dim mr-1">{t('software.presets')}</span>
                {INSTALL_ARG_PRESETS.map((preset) => (
                  <button
                    key={preset.label}
                    type="button"
                    className="btn btn-sm btn-secondary font-mono text-xs py-0.5 px-2"
                    title={t(preset.descKey)}
                    onClick={() => setUploadInstallArgs(preset.args)}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            )}
            <span className="form-hint">
              {uploadType === 'exe'
                ? t('software.install_args_hint_exe')
                : t('software.install_args_hint')}
            </span>
            {needsSilentArgs && selectedFile && (
              <span className="form-hint form-hint-error">
                {t('software.file_needs_args', { name: selectedFile.name })}
              </span>
            )}
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="pkg-uninstall-args">
              {t('software.uninstall_args')}
              <span className="text-xs text-dim ml-1">{t('software.enables_removal')}</span>
            </label>
            <input
              id="pkg-uninstall-args"
              type="text"
              className="form-input font-mono"
              placeholder={uploadType === 'msi' ? '/qn /norestart' : t('software.uninstall_args_placeholder')}
              value={uploadUninstallArgs}
              onChange={(e) => setUploadUninstallArgs(e.target.value)}
            />
            <div className="mt-2 flex flex-wrap gap-1.5 items-center">
              <span className="text-xs text-dim mr-1">{t('software.presets')}</span>
              {UNINSTALL_ARG_PRESETS.map((preset) => (
                <button
                  key={preset.label}
                  type="button"
                  className="btn btn-sm btn-secondary font-mono text-xs py-0.5 px-2"
                  onClick={() => setUploadUninstallArgs(preset.args)}
                >
                  {preset.label}
                </button>
              ))}
            </div>
            <span className="form-hint">
              {t('software.uninstall_args_hint')}
            </span>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="pkg-file">
              {t('software.installer_file')}
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
                {selectedFile.name} · {t('software.detected_as')} <strong>{uploadType}</strong> ·{' '}
                {formatBytes(selectedFile.size)}
              </span>
            )}
          </div>
        </form>
      </Modal>

      {/* EDIT PACKAGE ARGUMENTS MODAL */}
      <Modal
        open={editingPackage !== null}
        onClose={() => !savingEdit && setEditingPackage(null)}
        title={editingPackage ? t('software.edit_modal_title', { name: editingPackage.name }) : t('software.edit_package')}
        size="md"
        dismissible={!savingEdit}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setEditingPackage(null)}
              disabled={savingEdit}
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="edit-package-form"
              className="btn btn-primary"
              disabled={savingEdit}
            >
              {savingEdit ? (
                <>
                  <RefreshCw size={16} className="spinning" />
                  <span>{t('software.saving')}</span>
                </>
              ) : (
                t('software.save_changes')
              )}
            </button>
          </>
        }
      >
        <form id="edit-package-form" onSubmit={handleSaveEdit}>
          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="edit-pkg-name">
                {t('software.col_package_name')}
              </label>
              <input
                id="edit-pkg-name"
                type="text"
                className="form-input"
                required
                value={editName}
                onChange={(e) => setEditName(e.target.value)}
              />
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="edit-pkg-version">
                {t('software.version')}
              </label>
              <input
                id="edit-pkg-version"
                type="text"
                className="form-input font-mono"
                required
                value={editVersion}
                onChange={(e) => setEditVersion(e.target.value)}
              />
            </div>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="edit-pkg-args">
              {t('software.install_args')}
              {editingPackage?.package_type === 'exe' && <span className="form-label-required">{t('software.required_for_exe')}</span>}
            </label>
            <input
              id="edit-pkg-args"
              type="text"
              className="form-input font-mono"
              required={editingPackage?.package_type === 'exe'}
              value={editInstallArgs}
              onChange={(e) => setEditInstallArgs(e.target.value)}
            />
            {editingPackage?.package_type === 'exe' && (
              <div className="mt-2 flex flex-wrap gap-1.5 items-center">
                <span className="text-xs text-dim mr-1">{t('software.presets')}</span>
                {INSTALL_ARG_PRESETS.map((preset) => (
                  <button
                    key={preset.label}
                    type="button"
                    className="btn btn-sm btn-secondary font-mono text-xs py-0.5 px-2"
                    title={t(preset.descKey)}
                    onClick={() => setEditInstallArgs(preset.args)}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            )}
            <span className="form-hint">
              {t('software.edit_install_args_hint')}
            </span>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="edit-pkg-uninstall-args">
              {t('software.uninstall_args')}
            </label>
            <input
              id="edit-pkg-uninstall-args"
              type="text"
              className="form-input font-mono"
              value={editUninstallArgs}
              onChange={(e) => setEditUninstallArgs(e.target.value)}
            />
            <div className="mt-2 flex flex-wrap gap-1.5 items-center">
              <span className="text-xs text-dim mr-1">{t('software.presets')}</span>
              {UNINSTALL_ARG_PRESETS.map((preset) => (
                <button
                  key={preset.label}
                  type="button"
                  className="btn btn-sm btn-secondary font-mono text-xs py-0.5 px-2"
                  onClick={() => setEditUninstallArgs(preset.args)}
                >
                  {preset.label}
                </button>
              ))}
            </div>
            <span className="form-hint">
              {t('software.edit_uninstall_args_hint')}
            </span>
          </div>
        </form>
      </Modal>

      {/* DEPLOYMENT WIZARD MODAL */}
      <Modal
        open={showDeployModal}
        onClose={() => !deploying && setShowDeployModal(false)}
        title={deployAction === 'uninstall' ? t('software.launch_removal') : t('software.launch_deployment')}
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
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="deploy-form"
              className={deployAction === 'uninstall' ? 'btn btn-danger-outline' : 'btn btn-primary'}
              disabled={deploying}
            >
              {deploying ? (
                <>
                  <RefreshCw size={16} className="spinning" />
                  <span>{t('software.dispatching')}</span>
                </>
              ) : deployAction === 'uninstall' ? (
                t('software.start_removal')
              ) : (
                t('software.start_deployment')
              )}
            </button>
          </>
        }
      >
        <form id="deploy-form" onSubmit={handleCreateDeployment}>
          <div className="form-group">
            <label className="form-label" htmlFor="deploy-name">
              {t('software.col_deployment_name')}
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
              {t('software.software_package')}
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

          <div className="form-group">
            <label className="form-label" htmlFor="deploy-action">
              {t('software.col_operation')}
            </label>
            <select
              id="deploy-action"
              className="form-select"
              value={deployAction}
              onChange={(e) => setDeployAction(e.target.value as typeof deployAction)}
            >
              <option value="install">{t('software.install_package')}</option>
              <option value="uninstall">{t('software.uninstall_package')}</option>
            </select>
            {deployAction === 'uninstall' && deployPackageId && !selectedDeployPkg?.uninstall_args?.trim() && (
              <span className="form-hint form-hint-error">
                {t('software.uninstall_refused', { name: selectedDeployPkg?.name ?? '' })}
              </span>
            )}
            {deployAction === 'uninstall' && (
              <span className="form-hint">
                {t('software.uninstall_explainer')}
              </span>
            )}
          </div>

          <div className="form-row">
            <div className="form-group">
              <label className="form-label" htmlFor="deploy-scope">
                {t('software.col_target_scope')}
              </label>
              <select
                id="deploy-scope"
                className="form-select"
                value={deployTargetType}
                onChange={(e) => {
                  const newType = e.target.value as typeof deployTargetType
                  setDeployTargetType(newType)
                  if (newType === 'device') {
                    const online = devices.find((d) => d.status === 'online') || devices[0]
                    setDeployTargetId(online ? online.id : '')
                  } else if (newType === 'group') {
                    setDeployTargetId(groups[0] ? groups[0].id : '')
                  } else {
                    setDeployTargetId('')
                  }
                }}
              >
                <option value="all">{t('software.scope_all')}</option>
                <option value="group">{t('software.scope_group')}</option>
                <option value="device">{t('software.scope_device')}</option>
              </select>
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="deploy-target-id">
                {deployTargetType === 'device'
                  ? t('software.target_endpoint')
                  : deployTargetType === 'group'
                    ? t('software.target_group')
                    : t('software.col_target_scope')}
              </label>
              {deployTargetType === 'device' ? (
                <>
                  <select
                    id="deploy-target-id"
                    className="form-select"
                    required
                    value={deployTargetId}
                    onChange={(e) => setDeployTargetId(e.target.value)}
                  >
                    <option value="" disabled>
                      {t('software.select_endpoint')}
                    </option>
                    {devices.map((d) => {
                      const isOnline = d.status === 'online'
                      const osMatch =
                        !selectedDeployPkg?.os_target ||
                        selectedDeployPkg.os_target.toLowerCase() === d.os_name.toLowerCase()
                      return (
                        <option key={d.id} value={d.id}>
                          {isOnline ? '🟢' : '⚪'} {d.hostname} ({isOnline ? t('software.online') : t('software.offline')} · {d.os_name}
                          {!osMatch ? ` · ${t('software.os_mismatch')}` : ''})
                        </option>
                      )
                    })}
                  </select>
                  {devices.length === 0 && (
                    <span className="form-hint form-hint-error">
                      {t('software.no_endpoints')}
                    </span>
                  )}
                  {selectedDevice && (
                    <span className="form-hint">
                      {selectedDevice.status === 'online' ? (
                        <span className="text-success font-semibold">
                          🟢 {t('software.device_online', { hostname: selectedDevice.hostname })}
                        </span>
                      ) : (
                        <span className="text-warning">
                          ⚪ {t('software.device_offline', { hostname: selectedDevice.hostname })}
                        </span>
                      )}
                      {selectedDeployPkg?.os_target &&
                        selectedDeployPkg.os_target.toLowerCase() !== selectedDevice.os_name.toLowerCase() && (
                          <div className="text-danger mt-1">
                            ⚠️ {t('software.os_warning_targets')} <strong>{selectedDeployPkg.os_target}</strong>
                            {t('software.os_warning_runs', { hostname: selectedDevice.hostname })}{' '}
                            <strong>{selectedDevice.os_name}</strong>.
                          </div>
                        )}
                    </span>
                  )}
                </>
              ) : deployTargetType === 'group' ? (
                <>
                  <select
                    id="deploy-target-id"
                    className="form-select"
                    required
                    value={deployTargetId}
                    onChange={(e) => setDeployTargetId(e.target.value)}
                  >
                    <option value="" disabled>
                      {t('software.select_group')}
                    </option>
                    {groups.map((grp) => (
                      <option key={grp.id} value={grp.id}>
                        📁 {grp.name} ({grp.member_count === 1 ? t('software.group_members_one', { count: grp.member_count }) : t('software.group_members', { count: grp.member_count })})
                      </option>
                    ))}
                  </select>
                  {groups.length === 0 && (
                    <span className="form-hint form-hint-error">
                      {t('software.no_groups')}
                    </span>
                  )}
                </>
              ) : (
                <input
                  id="deploy-target-id"
                  type="text"
                  className="form-input text-dim"
                  disabled
                  value={selectedDeployPkg?.os_target ? t('software.all_os_endpoints', { os: selectedDeployPkg.os_target }) : t('software.all_compatible_endpoints')}
                />
              )}
            </div>
          </div>

          <div className="security-notice">
            <AlertCircle size={16} />
            <span>{t('software.security_notice')}</span>
          </div>
        </form>
      </Modal>

      {/* VIEW TASKS MODAL */}
      <Modal
        open={viewingDeployment !== null}
        onClose={() => setViewingDeployment(null)}
        title={t('software.tasks_modal_title')}
        size="lg"
        description={
          viewingDeployment
            ? t('software.tasks_modal_description', {
                name: viewingDeployment.name,
                package: viewingDeployment.package_name ?? '',
                target: TARGET_KEYS[viewingDeployment.target_type] ? t(TARGET_KEYS[viewingDeployment.target_type]) : viewingDeployment.target_type,
              })
            : undefined
        }
        footer={
          <button type="button" className="btn btn-secondary" onClick={() => setViewingDeployment(null)}>
            {t('common.close')}
          </button>
        }
      >
        {loadingTasks ? (
          <div className="py-8 text-center text-muted">
            <RefreshCw size={24} className="spinning" />
            <div>{t('software.loading_logs')}</div>
          </div>
        ) : deploymentTasks.length === 0 ? (
          <div className="py-8 text-center text-muted">{t('software.no_tasks')}</div>
        ) : (
          deploymentTasks.map((task) => (
            <div key={task.id} className="form-group">
              <div className="card-title-group">
                <Server size={16} className="text-muted" />
                <span className="font-mono font-semibold text-main">{task.hostname || task.device_id}</span>
                {task.site && <span className="os-badge">{task.site}</span>}
                <span
                  className={`status-pill ${
                    task.status === 'success'
                      ? 'online'
                      : task.status === 'failed' || task.status === 'failed_lost'
                        ? 'danger'
                        : task.status === 'installing' ||
                            task.status === 'uninstalling' ||
                            task.status === 'downloading'
                          ? 'primary'
                          : 'offline'
                  }`}
                >
                  {TASK_STATUS_KEYS[task.status] ? t(TASK_STATUS_KEYS[task.status]) : task.status}
                </span>
                {(task.output_log || task.error_message) && (
                  <button
                    type="button"
                    className="btn-action"
                    onClick={() => setExpandedTaskId(expandedTaskId === task.id ? null : task.id)}
                  >
                    {expandedTaskId === task.id ? t('software.hide_logs') : t('software.view_logs')}
                  </button>
                )}
              </div>

              {expandedTaskId === task.id && (
                <>
                  {task.exit_code !== undefined && task.exit_code !== null && (
                    <div className="text-sm text-muted font-mono">
                      {t('software.exit_code')} <span className="exit-code-badge">{task.exit_code}</span>
                    </div>
                  )}
                  {task.error_message && <div className="alert-banner alert-error">{task.error_message}</div>}
                  {task.output_log && <pre className="terminal-log-output">{task.output_log}</pre>}
                </>
              )}
            </div>
          ))
        )}
      </Modal>

      <ConfirmDialog
        open={packagePendingDelete !== null}
        title={t('software.delete_title')}
        message={
          packagePendingDelete
            ? t('software.delete_confirm', { name: packagePendingDelete.name })
            : ''
        }
        confirmLabel={t('common.delete')}
        pending={deletingPackage}
        onConfirm={handleDeletePackage}
        onCancel={() => setPackagePendingDelete(null)}
      />
    </div>
  )
}
