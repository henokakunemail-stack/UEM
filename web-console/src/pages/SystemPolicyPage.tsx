import React, { useEffect, useState, useRef } from 'react'
import {
  CheckCircle2,
  HardDrive,
  Image as ImageIcon,
  Plus,
  Power,
  RefreshCw,
  Send,
  Shield,
  Sliders,
  Trash2,
  Edit2,
  Upload,
  Lock,
  Flame,
  Terminal,
  UserCheck,
} from 'lucide-react'
import { api } from '../services/api'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import { KPICard } from '../components/KPICard'
import { useToast } from '../context/ToastContext'
import type {
  DeviceDTO,
  DeviceGroupDTO,
  DeviceSystemPolicyStateDTO,
  SystemPolicyDTO,
  SystemPolicySettingsDTO,
  WallpaperStyle,
  WindowsUpdateMode,
} from '../types/api'

const DEFAULT_SETTINGS: SystemPolicySettingsDTO = {
  usb_storage_mode: 'allow',
  wallpaper_enabled: false,
  wallpaper_url: '',
  wallpaper_style: 'fill',
  wallpaper_locked: true,
  firewall_enabled: false,
  defender_protection_enabled: false,
  disable_powershell: false,
  disable_autoplay: false,
  screensaver_timeout_minutes: 0,
  screensaver_password_required: false,
  inactivity_timeout_minutes: 0,
  disable_task_mgr: false,
  disable_cmd: false,
  disable_registry_tools: false,
  disable_control_panel: false,
  windows_update_mode: 'default',
  admin_account_enabled: false,
  admin_account_username: '',
  admin_account_password: '',
  admin_account_full_name: '',
  admin_account_description: '',
  admin_account_never_expires: true,
}

const PolicyScopeFields: React.FC<{
  targetType: string
  targetId: string
  groups: DeviceGroupDTO[]
  devices: DeviceDTO[]
  idPrefix: string
  onChange: (next: { target_type: 'all' | 'group' | 'device'; target_id: string }) => void
}> = ({ targetType, targetId, groups, devices, idPrefix, onChange }) => (
  <>
    <div className="form-group">
      <label className="form-label" htmlFor={`${idPrefix}-scope`}>
        Target Scope
      </label>
      <select
        id={`${idPrefix}-scope`}
        className="form-select"
        value={targetType}
        onChange={(e) =>
          onChange({
            target_type: e.target.value as 'all' | 'group' | 'device',
            target_id: '',
          })
        }
      >
        <option value="all">Entire Fleet (All Devices)</option>
        <option value="group">Device Group</option>
        <option value="device">Single Device</option>
      </select>
    </div>

    {targetType === 'group' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-group`}>
          Select Device Group
        </label>
        <select
          id={`${idPrefix}-group`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: 'group', target_id: e.target.value })}
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

    {targetType === 'device' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-device`}>
          Select Device
        </label>
        <select
          id={`${idPrefix}-device`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: 'device', target_id: e.target.value })}
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
  </>
)

export const SystemPolicyPage: React.FC = () => {
  const { can } = usePermission()
  const toast = useToast()
  const isAdmin = can('admin')
  const isTechnician = can('technician')

  const [activeTab, setActiveTab] = useState<'policies' | 'compliance'>('policies')
  const [policies, setPolicies] = useState<SystemPolicyDTO[]>([])
  const [deviceStates, setDeviceStates] = useState<DeviceSystemPolicyStateDTO[]>([])
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [loading, setLoading] = useState(true)

  // Policy Modal state
  const [modalOpen, setModalOpen] = useState(false)
  const [editingPolicy, setEditingPolicy] = useState<SystemPolicyDTO | null>(null)
  const [formName, setFormName] = useState('')
  const [formDescription, setFormDescription] = useState('')
  const [formTargetType, setFormTargetType] = useState<'all' | 'group' | 'device'>('all')
  const [formTargetId, setFormTargetId] = useState('')
  const [formPriority, setFormPriority] = useState(100)
  const [formIsEnabled, setFormIsEnabled] = useState(true)
  const [formSettings, setFormSettings] = useState<SystemPolicySettingsDTO>(DEFAULT_SETTINGS)
  const [modalTab, setModalTab] = useState<'general' | 'peripherals' | 'desktop' | 'security' | 'accounts'>('general')
  const [showAdminPassword, setShowAdminPassword] = useState(false)

  // Confirm delete dialog
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false)
  const [policyToDelete, setPolicyToDelete] = useState<SystemPolicyDTO | null>(null)
  const [deletePending, setDeletePending] = useState(false)

  // Force sync dialog
  const [syncingDeviceId, setSyncingDeviceId] = useState<string | null>(null)

  // Wallpaper upload
  const wallpaperInputRef = useRef<HTMLInputElement>(null)
  const [uploadingWallpaper, setUploadingWallpaper] = useState(false)

  const handleWallpaperFileSelect = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    if (!file.type.startsWith('image/')) {
      toast.error('Please select a valid image file (PNG, JPG, BMP, WebP)')
      return
    }
    setUploadingWallpaper(true)
    try {
      const res = await api.uploadWallpaper(file)
      setFormSettings((prev) => ({
        ...prev,
        wallpaper_url: res.wallpaper_url,
      }))
      toast.success('Wallpaper image uploaded and staged successfully')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Wallpaper upload failed')
    } finally {
      setUploadingWallpaper(false)
      if (wallpaperInputRef.current) {
        wallpaperInputRef.current.value = ''
      }
    }
  }

  const loadData = async () => {
    setLoading(true)
    try {
      const [policiesData, statesData, groupsData, devicesData] = await Promise.all([
        api.getSystemPolicies(),
        api.getSystemPolicyStates(),
        api.getDeviceGroups(),
        api.getDevices(),
      ])
      setPolicies(policiesData)
      setDeviceStates(statesData)
      setGroups(groupsData)
      setDevices(devicesData.devices || [])
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load system policies')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  const handleOpenCreate = () => {
    setEditingPolicy(null)
    setFormName('')
    setFormDescription('')
    setFormTargetType('all')
    setFormTargetId('')
    setFormPriority(100)
    setFormIsEnabled(true)
    setFormSettings(DEFAULT_SETTINGS)
    setModalTab('general')
    setShowAdminPassword(false)
    setModalOpen(true)
  }

  const handleOpenEdit = (p: SystemPolicyDTO) => {
    setEditingPolicy(p)
    setFormName(p.name)
    setFormDescription(p.description || '')
    setFormTargetType(p.target_type)
    setFormTargetId(p.target_id || '')
    setFormPriority(p.priority)
    setFormIsEnabled(p.is_enabled)
    setFormSettings({
      ...DEFAULT_SETTINGS,
      ...(p.settings || {}),
    })
    setModalTab('general')
    setShowAdminPassword(false)
    setModalOpen(true)
  }

  const handleSavePolicy = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      if (editingPolicy) {
        await api.updateSystemPolicy(editingPolicy.id, {
          name: formName,
          description: formDescription,
          target_type: formTargetType,
          target_id: formTargetId,
          priority: formPriority,
          is_enabled: formIsEnabled,
          settings: formSettings,
        })
        toast.success('Policy updated successfully')
      } else {
        await api.createSystemPolicy({
          name: formName,
          description: formDescription,
          target_type: formTargetType,
          target_id: formTargetId,
          priority: formPriority,
          is_enabled: formIsEnabled,
          settings: formSettings,
        })
        toast.success('Policy created successfully')
      }
      setModalOpen(false)
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Save policy failed')
    }
  }

  const handleToggleEnabled = async (p: SystemPolicyDTO) => {
    try {
      await api.updateSystemPolicy(p.id, { is_enabled: !p.is_enabled })
      toast.info(`Policy ${p.name} ${p.is_enabled ? 'disabled' : 'enabled'}`)
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Toggle policy failed')
    }
  }

  const handleApplyNow = async (p: SystemPolicyDTO) => {
    try {
      await api.applySystemPolicy(p.id)
      toast.info(`Applying policy ${p.name} to online targets...`)
      setTimeout(loadData, 1500)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Apply policy failed')
    }
  }

  const handleDeletePolicy = async () => {
    if (!policyToDelete) return
    setDeletePending(true)
    try {
      await api.deleteSystemPolicy(policyToDelete.id)
      toast.info(`Policy ${policyToDelete.name} deleted`)
      setDeleteConfirmOpen(false)
      setPolicyToDelete(null)
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete policy failed')
    } finally {
      setDeletePending(false)
    }
  }

  const handleForceSyncDevice = async (deviceId: string) => {
    setSyncingDeviceId(deviceId)
    try {
      await api.syncDeviceSystemPolicy(deviceId)
      toast.success('Sync command dispatched to device')
      setTimeout(loadData, 2000)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Sync device failed')
    } finally {
      setSyncingDeviceId(null)
    }
  }

  const resolveTargetName = (targetType: string, targetId: string) => {
    if (targetType === 'all') return 'Entire Fleet'
    if (targetType === 'group') {
      const g = groups.find((item) => item.id === targetId)
      return g ? `Group: ${g.name}` : `Group (${targetId.substring(0, 8)})`
    }
    if (targetType === 'device') {
      const d = devices.find((item) => item.id === targetId)
      return d ? `Device: ${d.hostname}` : `Device (${targetId.substring(0, 8)})`
    }
    return targetType
  }

  // Summary Metrics
  const totalPolicies = policies.length
  const activePolicies = policies.filter((p) => p.is_enabled).length
  const usbRestrictedCount = policies.filter(
    (p) => p.is_enabled && p.settings?.usb_storage_mode === 'block'
  ).length
  const wallpaperEnforcedCount = policies.filter(
    (p) => p.is_enabled && p.settings?.wallpaper_enabled
  ).length
  const syncedDevicesCount = deviceStates.filter((s) => s.status === 'synced').length

  return (
    <div className="space-y-6">
      {/* Header bar */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <Sliders className="h-6 w-6 text-primary" />
            <h1 className="text-2xl font-bold text-main tracking-tight">Group Policy (GPO)</h1>
          </div>
          <p className="text-sm text-muted mt-1">
            Active Directory-style centralized endpoint management: USB port storage locking, corporate wallpaper, inactivity screen lock, and OS restrictions.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="btn btn-secondary flex items-center gap-2"
            onClick={loadData}
            disabled={loading}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? 'spinning' : ''}`} />
            <span>Refresh</span>
          </button>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary flex items-center gap-2"
              onClick={handleOpenCreate}
            >
              <Plus className="h-4 w-4" />
              <span>Create Policy</span>
            </button>
          )}
        </div>
      </div>

      {/* KPI Overview Cards */}
      <div className="kpi-grid">
        <KPICard
          title="ACTIVE POLICIES"
          value={`${activePolicies} / ${totalPolicies}`}
          subtitle="Enforced across fleet"
          icon={<Shield size={20} />}
          variant="primary"
        />
        <KPICard
          title="USB MASS STORAGE LOCK"
          value={usbRestrictedCount > 0 ? 'Enforced' : 'Unrestricted'}
          subtitle="Mouse & keyboard functional"
          icon={<HardDrive size={20} />}
          variant={usbRestrictedCount > 0 ? 'warning' : 'neutral'}
        />
        <KPICard
          title="CORPORATE WALLPAPER"
          value={wallpaperEnforcedCount > 0 ? `${wallpaperEnforcedCount} Policies` : 'Off'}
          subtitle="Desktop background managed"
          icon={<ImageIcon size={20} />}
          variant="neutral"
        />
        <KPICard
          title="FLEET COMPLIANCE"
          value={syncedDevicesCount}
          subtitle="Synced endpoints"
          icon={<CheckCircle2 size={20} />}
          variant="success"
        />
      </div>

      {/* Main Tabs */}
      <div className="flex gap-2 pb-2">
        <button
          type="button"
          className={`btn ${activeTab === 'policies' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setActiveTab('policies')}
        >
          Policies ({policies.length})
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'compliance' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setActiveTab('compliance')}
        >
          Device Compliance ({deviceStates.length})
        </button>
      </div>

      {/* Tab: Policies */}
      {activeTab === 'policies' && (
        <div className="table-card">
          <DataTable label="Group policies">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Policy Name</th>
                  <th>Target Scope</th>
                  <th>Priority</th>
                  <th>Enforced Rules</th>
                  <th>Status</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {policies.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-6 text-muted">
                      No system policies defined yet. Click &apos;Create Policy&apos; to configure endpoint GPO rules.
                    </td>
                  </tr>
                ) : (
                  policies.map((p) => {
                    const s = p.settings || DEFAULT_SETTINGS
                    return (
                      <tr key={p.id}>
                        <td>
                          <div className="font-semibold text-main">{p.name}</div>
                          {p.description && <div className="text-xs text-muted">{p.description}</div>}
                        </td>
                        <td>
                          <span className="badge-target">
                            {resolveTargetName(p.target_type, p.target_id)}
                          </span>
                        </td>
                        <td>
                          <span className="text-xs text-muted font-mono">{p.priority}</span>
                        </td>
                        <td>
                          <div className="flex flex-wrap gap-1">
                            {s.usb_storage_mode === 'block' && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-red-500/10 text-red-400 border border-red-500/20">
                                USB Blocked
                              </span>
                            )}
                            {s.usb_storage_mode === 'read_only' && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-amber-500/10 text-amber-400 border border-amber-500/20">
                                USB Read-Only
                              </span>
                            )}
                            {s.wallpaper_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-blue-500/10 text-blue-400 border border-blue-500/20">
                                Wallpaper{s.wallpaper_locked ? ' (Locked)' : ''}
                              </span>
                            )}
                            {s.firewall_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-cyan-500/10 text-cyan-400 border border-cyan-500/20">
                                Firewall
                              </span>
                            )}
                            {s.defender_protection_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
                                Defender
                              </span>
                            )}
                            {s.disable_autoplay && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-orange-500/10 text-orange-400 border border-orange-500/20">
                                No Autoplay
                              </span>
                            )}
                            {s.disable_powershell && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-yellow-500/10 text-yellow-400 border border-yellow-500/20">
                                No PS
                              </span>
                            )}
                            {s.screensaver_timeout_minutes ? (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-pink-500/10 text-pink-400 border border-pink-500/20">
                                SS {s.screensaver_timeout_minutes}m{s.screensaver_password_required ? ' (Pwd)' : ''}
                              </span>
                            ) : null}
                            {s.inactivity_timeout_minutes > 0 && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-purple-500/10 text-purple-400 border border-purple-500/20">
                                Lock {s.inactivity_timeout_minutes}m
                              </span>
                            )}
                            {s.disable_task_mgr && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                No TaskMgr
                              </span>
                            )}
                            {s.disable_cmd && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                No CMD
                              </span>
                            )}
                            {s.disable_registry_tools && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                No Regedit
                              </span>
                            )}
                            {s.disable_control_panel && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                No Control Panel
                              </span>
                            )}
                            {s.windows_update_mode === 'auto_install' && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                                Auto Update
                              </span>
                            )}
                            {s.admin_account_enabled && s.admin_account_username && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                                Admin: {s.admin_account_username}
                              </span>
                            )}
                          </div>
                        </td>
                        <td>
                          <span
                            className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium ${
                              p.is_enabled
                                ? 'bg-emerald-500/10 text-emerald-400'
                                : 'bg-zinc-500/10 text-zinc-400'
                            }`}
                          >
                            <span
                              className={`h-1.5 w-1.5 rounded-full ${
                                p.is_enabled ? 'bg-emerald-400' : 'bg-zinc-400'
                              }`}
                            />
                            {p.is_enabled ? 'Enabled' : 'Disabled'}
                          </span>
                        </td>
                        <td>
                          <div className="flex items-center gap-1 justify-end">
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title={p.is_enabled ? 'Disable' : 'Enable'}
                                onClick={() => handleToggleEnabled(p)}
                              >
                                <Power size={15} />
                              </button>
                            )}
                            {isTechnician && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title="Apply now to online devices"
                                onClick={() => handleApplyNow(p)}
                              >
                                <Send size={15} />
                              </button>
                            )}
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title="Edit policy"
                                onClick={() => handleOpenEdit(p)}
                              >
                                <Edit2 size={15} />
                              </button>
                            )}
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-red-400"
                                title="Delete policy"
                                onClick={() => {
                                  setPolicyToDelete(p)
                                  setDeleteConfirmOpen(true)
                                }}
                              >
                                <Trash2 size={15} />
                              </button>
                            )}
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
      )}

      {/* Tab: Compliance */}
      {activeTab === 'compliance' && (
        <div className="table-card">
          <DataTable label="Device compliance">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Policy Version</th>
                  <th>Enforcement Status</th>
                  <th>Active Settings</th>
                  <th>Last Applied</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {deviceStates.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-6 text-muted">
                      No device policy states reported yet. Online devices sync when connected or triggered.
                    </td>
                  </tr>
                ) : (
                  deviceStates.map((s) => {
                    const d = devices.find((item) => item.id === s.device_id)
                    const applied = s.applied_settings || DEFAULT_SETTINGS
                    let badge = 'bg-zinc-500/10 text-zinc-400'
                    let label = 'Awaiting Sync'
                    if (s.status === 'synced') {
                      badge = 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                      label = 'Enforced (Synced)'
                    } else if (s.status === 'failed') {
                      badge = 'bg-red-500/10 text-red-400 border border-red-500/20'
                      label = 'Failed'
                    }

                    return (
                      <tr key={s.device_id}>
                        <td>
                          <div className="font-semibold text-main">{d ? d.hostname : s.device_id}</div>
                          <div className="text-xs text-muted">
                            {d ? `${d.os_name} ${d.os_version || ''}` : 'Unknown OS'}
                          </div>
                        </td>
                        <td>
                          <span className="font-mono text-xs text-muted">
                            {s.policy_version || 'none'}
                          </span>
                        </td>
                        <td>
                          <span className={`px-2 py-0.5 rounded-full text-xs font-medium ${badge}`}>
                            {label}
                          </span>
                          {s.error_message && (
                            <div className="text-xs text-red-400 mt-1 max-w-xs truncate" title={s.error_message}>
                              {s.error_message}
                            </div>
                          )}
                        </td>
                        <td>
                          <div className="text-xs text-muted space-y-0.5">
                            <div>USB: <span className="text-main font-medium">{applied.usb_storage_mode}</span></div>
                            <div>Wallpaper: <span className="text-main font-medium">{applied.wallpaper_enabled ? 'Enforced' : 'Off'}</span></div>
                            <div>Timeout: <span className="text-main font-medium">{applied.inactivity_timeout_minutes ? `${applied.inactivity_timeout_minutes}m` : 'Disabled'}</span></div>
                            <div>Admin: <span className="text-main font-medium">{applied.admin_account_enabled ? applied.admin_account_username || 'Enforced' : 'Unmanaged'}</span></div>
                          </div>
                        </td>
                        <td>
                          <span className="text-xs text-muted">
                            {s.last_applied_at ? new Date(s.last_applied_at).toLocaleString() : 'Never'}
                          </span>
                        </td>
                        <td>
                          <div className="text-right">
                            {isTechnician && (
                              <button
                                type="button"
                                className="btn btn-secondary text-xs px-2.5 py-1 flex items-center gap-1.5 ml-auto"
                                disabled={syncingDeviceId === s.device_id}
                                onClick={() => handleForceSyncDevice(s.device_id)}
                              >
                                <RefreshCw className={`h-3 w-3 ${syncingDeviceId === s.device_id ? 'spinning' : ''}`} />
                                <span>Force Sync</span>
                              </button>
                            )}
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
      )}

      {/* Create / Edit Policy Modal */}
      <Modal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        title={editingPolicy ? `Edit Policy: ${editingPolicy.name}` : 'Create New System Policy'}
        size="lg"
        footer={
          <div className="flex items-center justify-end gap-2 w-full">
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setModalOpen(false)}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="policy-form"
              className="btn btn-primary"
            >
              {editingPolicy ? 'Save Changes' : 'Create Policy'}
            </button>
          </div>
        }
      >
        <div className="space-y-4">
          {/* Modal Category Nav */}
          <div className="flex gap-1 pb-2">
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'general' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('general')}
            >
              General &amp; Scope
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'peripherals' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('peripherals')}
            >
              USB &amp; Storage
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'desktop' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('desktop')}
            >
              Wallpaper
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'security' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('security')}
            >
              Security Restrictions
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'accounts' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('accounts')}
            >
              Admin Account
            </button>
          </div>

          <form id="policy-form" onSubmit={handleSavePolicy} className="space-y-4">
            {/* General Tab */}
            {modalTab === 'general' && (
              <div className="space-y-4">
                <div className="form-group">
                  <label className="form-label" htmlFor="policy-name">Policy Name *</label>
                  <input
                    id="policy-name"
                    type="text"
                    required
                    className="form-input"
                    placeholder="e.g. Finance USB Lockout & Corporate Branding"
                    value={formName}
                    onChange={(e) => setFormName(e.target.value)}
                  />
                </div>

                <div className="form-group">
                  <label className="form-label" htmlFor="policy-desc">Description</label>
                  <textarea
                    id="policy-desc"
                    className="form-input"
                    rows={2}
                    placeholder="Provide context on when and why this GPO is enforced..."
                    value={formDescription}
                    onChange={(e) => setFormDescription(e.target.value)}
                  />
                </div>

                <PolicyScopeFields
                  idPrefix="form"
                  targetType={formTargetType}
                  targetId={formTargetId}
                  groups={groups}
                  devices={devices}
                  onChange={(next) => {
                    setFormTargetType(next.target_type)
                    setFormTargetId(next.target_id)
                  }}
                />

                <div className="grid grid-cols-2 gap-4">
                  <div className="form-group">
                    <label className="form-label" htmlFor="policy-prio">
                      Priority (Lower = Higher Precedence)
                    </label>
                    <input
                      id="policy-prio"
                      type="number"
                      min={1}
                      max={1000}
                      className="form-input"
                      value={formPriority}
                      onChange={(e) => setFormPriority(Number(e.target.value))}
                    />
                  </div>
                  <div className="form-group flex flex-col justify-end">
                    <label className="flex items-center gap-2 cursor-pointer pt-2">
                      <input
                        type="checkbox"
                        className="rounded"
                        checked={formIsEnabled}
                        onChange={(e) => setFormIsEnabled(e.target.checked)}
                      />
                      <span className="text-sm font-medium text-main">Enable this policy immediately</span>
                    </label>
                  </div>
                </div>
              </div>
            )}

            {/* Peripherals Tab */}
            {modalTab === 'peripherals' && (
              <div className="space-y-4">
                <div style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
                  <h4 className="text-sm font-semibold text-main flex items-center gap-2">
                    <HardDrive className="h-4 w-4 text-primary" />
                    USB Port Storage Restriction Mode
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    Disabling USB storage blocks USB flash drives and external disks exclusively.
                    <strong> USB mice, keyboards, webcams, and printers use standard HID drivers and remain 100% active.</strong>
                  </p>
                </div>

                <div className="space-y-2">
                  <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.75rem', padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', cursor: 'pointer' }}>
                    <input
                      type="radio"
                      name="usb_mode"
                      value="allow"
                      checked={formSettings.usb_storage_mode === 'allow'}
                      onChange={() =>
                        setFormSettings({ ...formSettings, usb_storage_mode: 'allow' })
                      }
                      className="mt-1"
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Allow All USB Storage (Default)</div>
                      <div className="text-xs text-muted">Endpoints can read and write to USB flash drives normally.</div>
                    </div>
                  </label>

                  <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.75rem', padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid rgba(239, 68, 68, 0.4)', background: 'rgba(239, 68, 68, 0.05)', cursor: 'pointer' }}>
                    <input
                      type="radio"
                      name="usb_mode"
                      value="block"
                      checked={formSettings.usb_storage_mode === 'block'}
                      onChange={() =>
                        setFormSettings({ ...formSettings, usb_storage_mode: 'block' })
                      }
                      className="mt-1"
                    />
                    <div>
                      <div className="text-sm font-medium text-red-400">Lock &amp; Block USB Storage Devices</div>
                      <div className="text-xs text-muted">
                        Disables USB mass storage driver (`USBSTOR`) and disk drive policies. Flash drives cannot be mounted. Mouse and keyboard are completely unaffected.
                      </div>
                    </div>
                  </label>

                  <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.75rem', padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid rgba(245, 158, 11, 0.4)', background: 'rgba(245, 158, 11, 0.05)', cursor: 'pointer' }}>
                    <input
                      type="radio"
                      name="usb_mode"
                      value="read_only"
                      checked={formSettings.usb_storage_mode === 'read_only'}
                      onChange={() =>
                        setFormSettings({ ...formSettings, usb_storage_mode: 'read_only' })
                      }
                      className="mt-1"
                    />
                    <div>
                      <div className="text-sm font-medium text-amber-400">Read-Only USB Storage (Write Protect)</div>
                      <div className="text-xs text-muted">
                        Users can view and read files on USB flash drives, but cannot write, copy, or exfiltrate data to the USB drive.
                      </div>
                    </div>
                  </label>
                </div>

                <div className="pt-3" style={{ borderTop: '1px solid var(--border-subtle)' }}>
                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_autoplay || false}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_autoplay: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Disable USB &amp; Media Autoplay/Autorun (`NoDriveTypeAutoRun`)</div>
                      <div className="text-xs text-muted">
                        Prevents executables or scripts on plugged-in USB storage and optical media from automatically launching.
                      </div>
                    </div>
                  </label>
                </div>
              </div>
            )}

            {/* Desktop Wallpaper Tab */}
            {modalTab === 'desktop' && (
              <div className="space-y-4">
                <div style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
                  <h4 className="text-sm font-semibold text-main flex items-center gap-2">
                    <ImageIcon className="h-4 w-4 text-primary" />
                    Corporate Desktop Wallpaper Management
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    Enforces corporate wallpaper across endpoints via Group Policy registry and instantly updates user desktops without requiring logoff or reboot.
                  </p>
                </div>

                <div className="form-group">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded"
                      checked={formSettings.wallpaper_enabled}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, wallpaper_enabled: e.target.checked })
                      }
                    />
                    <span className="text-sm font-medium text-main">Enforce corporate wallpaper on target machines</span>
                  </label>
                </div>

                {formSettings.wallpaper_enabled && (
                  <>
                    <div className="form-group">
                      <label className="form-label">
                        Upload Corporate Wallpaper Image
                      </label>
                      <div className="flex items-center gap-3">
                        <input
                          ref={wallpaperInputRef}
                          type="file"
                          accept="image/png,image/jpeg,image/bmp,image/webp"
                          onChange={handleWallpaperFileSelect}
                          className="hidden"
                          id="wallpaper-file-input"
                        />
                        <button
                          type="button"
                          className="btn btn-secondary flex items-center gap-2"
                          disabled={uploadingWallpaper}
                          onClick={() => wallpaperInputRef.current?.click()}
                        >
                          <Upload className={`h-4 w-4 ${uploadingWallpaper ? 'spinning' : ''}`} />
                          <span>{uploadingWallpaper ? 'Uploading...' : 'Browse & Upload Image'}</span>
                        </button>
                        <span className="text-xs text-muted">Supports PNG, JPG, BMP up to 10MB</span>
                      </div>
                      {formSettings.wallpaper_url && (
                        <div className="mt-2 flex items-center gap-3" style={{ padding: '0.5rem', borderRadius: '0.375rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface)' }}>
                          <img
                            src={formSettings.wallpaper_url}
                            alt="Wallpaper preview"
                            className="h-14 w-24 object-cover rounded"
                            style={{ border: '1px solid var(--border-subtle)' }}
                            onError={(e) => {
                              (e.target as HTMLElement).style.display = 'none'
                            }}
                          />
                          <div className="text-xs font-mono text-muted truncate max-w-sm">
                            {formSettings.wallpaper_url}
                          </div>
                        </div>
                      )}
                    </div>

                    <div className="form-group">
                      <label className="form-label" htmlFor="wallpaper-url">
                        Wallpaper Image URL or File Path *
                      </label>
                      <input
                        id="wallpaper-url"
                        type="text"
                        required={formSettings.wallpaper_enabled}
                        className="form-input"
                        placeholder="/api/system-policies/wallpapers/... or https://company.corp/assets/branding.jpg"
                        value={formSettings.wallpaper_url || ''}
                        onChange={(e) =>
                          setFormSettings({ ...formSettings, wallpaper_url: e.target.value })
                        }
                      />
                      <span className="form-hint">
                        Uploaded image is distributed to all managed endpoints and applied to current desktops.
                      </span>
                    </div>

                    <div className="form-group">
                      <label className="form-label" htmlFor="wallpaper-style">
                        Wallpaper Fitting Style
                      </label>
                      <select
                        id="wallpaper-style"
                        className="form-select"
                        value={formSettings.wallpaper_style || 'fill'}
                        onChange={(e) =>
                          setFormSettings({
                            ...formSettings,
                            wallpaper_style: e.target.value as WallpaperStyle,
                          })
                        }
                      >
                        <option value="fill">Fill Screen (Recommended)</option>
                        <option value="fit">Fit Screen (Keep Aspect Ratio)</option>
                        <option value="stretch">Stretch to Screen</option>
                        <option value="center">Center Image</option>
                        <option value="tile">Tile Across Screen</option>
                      </select>
                    </div>

                    <div className="form-group pt-1">
                      <label className="flex items-start gap-2 cursor-pointer" style={{ padding: '0.625rem', borderRadius: '0.375rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
                        <input
                          type="checkbox"
                          className="rounded mt-0.5"
                          checked={formSettings.wallpaper_locked ?? true}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, wallpaper_locked: e.target.checked })
                          }
                        />
                        <div>
                          <div className="text-sm font-medium text-main flex items-center gap-1.5">
                            <Lock className="h-3.5 w-3.5 text-primary" />
                            Lock Desktop Wallpaper (`NoChangingWallpaper`)
                          </div>
                          <div className="text-xs text-muted">
                            Prevents local users from modifying or replacing the desktop background via Windows Settings or right-click context menu.
                          </div>
                        </div>
                      </label>
                    </div>
                  </>
                )}
              </div>
            )}

            {/* Security Restrictions Tab */}
            {modalTab === 'security' && (
              <div className="space-y-4">
                <div style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
                  <h4 className="text-sm font-semibold text-main flex items-center gap-2">
                    <Shield className="h-4 w-4 text-primary" />
                    OS Hardening &amp; System Security Restrictions
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    Enforce Windows Defender, firewall, PowerShell restrictions, and lock down sensitive management tools to prevent tampering.
                  </p>
                </div>

                {/* Firewall & Defender */}
                <div className="space-y-2.5 pb-2" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.firewall_enabled || false}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, firewall_enabled: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main flex items-center gap-1.5">
                        <Flame className="h-3.5 w-3.5 text-orange-400" />
                        Enforce Windows Defender Firewall
                      </div>
                      <div className="text-xs text-muted">
                        Forces firewall active across Domain, Private, and Public network profiles (`EnableFirewall = 1`).
                      </div>
                    </div>
                  </label>

                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.defender_protection_enabled || false}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, defender_protection_enabled: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main flex items-center gap-1.5">
                        <Shield className="h-3.5 w-3.5 text-emerald-400" />
                        Enforce Real-Time Antivirus Protection
                      </div>
                      <div className="text-xs text-muted">
                        Prevents local users from disabling Windows Defender real-time, behavior, and on-access protection.
                      </div>
                    </div>
                  </label>

                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_powershell || false}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_powershell: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main flex items-center gap-1.5">
                        <Terminal className="h-3.5 w-3.5 text-amber-400" />
                        Restrict PowerShell Script Execution
                      </div>
                      <div className="text-xs text-muted">
                        Sets PowerShell execution policy to `Restricted` to block script execution by unauthorized users.
                      </div>
                    </div>
                  </label>
                </div>

                {/* Screen Saver & Inactivity */}
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pb-2" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                  <div className="form-group">
                    <label className="form-label" htmlFor="inactivity-timeout">
                      Screen Inactivity Lock Timeout
                    </label>
                    <select
                      id="inactivity-timeout"
                      className="form-select"
                      value={formSettings.inactivity_timeout_minutes}
                      onChange={(e) =>
                        setFormSettings({
                          ...formSettings,
                          inactivity_timeout_minutes: Number(e.target.value),
                        })
                      }
                    >
                      <option value={0}>Disabled (No screen lock)</option>
                      <option value={5}>5 minutes</option>
                      <option value={10}>10 minutes</option>
                      <option value={15}>15 minutes (Standard Corporate)</option>
                      <option value={30}>30 minutes</option>
                      <option value={60}>60 minutes</option>
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label" htmlFor="screensaver-timeout">
                      Screen Saver Timeout &amp; Security
                    </label>
                    <select
                      id="screensaver-timeout"
                      className="form-select"
                      value={formSettings.screensaver_timeout_minutes || 0}
                      onChange={(e) =>
                        setFormSettings({
                          ...formSettings,
                          screensaver_timeout_minutes: Number(e.target.value),
                        })
                      }
                    >
                      <option value={0}>Disabled</option>
                      <option value={5}>5 minutes</option>
                      <option value={10}>10 minutes</option>
                      <option value={15}>15 minutes</option>
                      <option value={30}>30 minutes</option>
                    </select>
                    <label className="flex items-center gap-2 cursor-pointer mt-2">
                      <input
                        type="checkbox"
                        className="rounded"
                        checked={formSettings.screensaver_password_required || false}
                        onChange={(e) =>
                          setFormSettings({
                            ...formSettings,
                            screensaver_password_required: e.target.checked,
                          })
                        }
                      />
                      <span className="text-xs text-main font-medium">Require password on resume (`ScreenSaverIsSecure`)</span>
                    </label>
                  </div>
                </div>

                {/* Restriction Switches */}
                <div className="space-y-3 pt-2">
                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_task_mgr}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_task_mgr: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Disable Task Manager (`DisableTaskMgr`)</div>
                      <div className="text-xs text-muted">Prevents standard users from terminating security agents or processes.</div>
                    </div>
                  </label>

                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_cmd}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_cmd: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Disable Command Prompt (`DisableCMD`)</div>
                      <div className="text-xs text-muted">Blocks cmd.exe execution for standard users.</div>
                    </div>
                  </label>

                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_registry_tools}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_registry_tools: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Disable Registry Editor (`DisableRegistryTools`)</div>
                      <div className="text-xs text-muted">Blocks regedit.exe to prevent tampering with local registry keys.</div>
                    </div>
                  </label>

                  <label className="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded mt-0.5"
                      checked={formSettings.disable_control_panel}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, disable_control_panel: e.target.checked })
                      }
                    />
                    <div>
                      <div className="text-sm font-medium text-main">Disable Control Panel &amp; Settings (`NoControlPanel`)</div>
                      <div className="text-xs text-muted">Prevents access to Windows Settings app and classic Control Panel.</div>
                    </div>
                  </label>
                </div>

                {/* Windows Update Mode */}
                <div className="form-group pt-2">
                  <label className="form-label" htmlFor="wu-mode">
                    Windows Update Policy (`AUOptions`)
                  </label>
                  <select
                    id="wu-mode"
                    className="form-select"
                    value={formSettings.windows_update_mode}
                    onChange={(e) =>
                      setFormSettings({
                        ...formSettings,
                        windows_update_mode: e.target.value as WindowsUpdateMode,
                      })
                    }
                  >
                    <option value="default">Windows Default (Unmanaged)</option>
                    <option value="auto_install">Enforce Automatic Download &amp; Install</option>
                    <option value="notify_only">Notify Before Downloading Updates</option>
                  </select>
                </div>
              </div>
            )}

            {/* Managed Local Admin Account Tab */}
            {modalTab === 'accounts' && (
              <div className="space-y-4">
                <div style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
                  <h4 className="text-sm font-semibold text-main flex items-center gap-2">
                    <UserCheck className="h-4 w-4 text-primary" />
                    Managed Local Administrator Account Provisioning
                  </h4>
                  <p className="text-xs text-muted mt-1 leading-relaxed">
                    Deploy a standardized local administrator account across all target endpoints. Resolves differing admin credentials and guarantees central IT support access during troubleshooting.
                  </p>
                </div>

                <div className="form-group">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="rounded"
                      checked={formSettings.admin_account_enabled || false}
                      onChange={(e) =>
                        setFormSettings({ ...formSettings, admin_account_enabled: e.target.checked })
                      }
                    />
                    <span className="text-sm font-medium text-main">
                      Enforce Managed Local Administrator on Endpoints
                    </span>
                  </label>
                </div>

                {formSettings.admin_account_enabled && (
                  <div className="space-y-4 p-4 rounded-lg bg-zinc-900/60 border border-zinc-700/60">
                    <div className="grid grid-cols-2 gap-4">
                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-username">Admin Username *</label>
                        <input
                          id="admin-username"
                          type="text"
                          required
                          className="form-input"
                          placeholder="e.g. corpadmin, sysadmin"
                          value={formSettings.admin_account_username || ''}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_username: e.target.value.trim() })
                          }
                        />
                        <span className="text-xs text-muted">Created/updated as local admin on target endpoints</span>
                      </div>

                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-password">Admin Password *</label>
                        <div className="relative">
                          <input
                            id="admin-password"
                            type={showAdminPassword ? 'text' : 'password'}
                            required
                            className="form-input pr-14"
                            placeholder="Unified admin password"
                            value={formSettings.admin_account_password || ''}
                            onChange={(e) =>
                              setFormSettings({ ...formSettings, admin_account_password: e.target.value })
                            }
                          />
                          <button
                            type="button"
                            className="absolute right-2 top-1/2 -translate-y-1/2 text-xs text-muted hover:text-main px-1.5 py-0.5 rounded"
                            onClick={() => setShowAdminPassword(!showAdminPassword)}
                          >
                            {showAdminPassword ? 'Hide' : 'Show'}
                          </button>
                        </div>
                        <span className="text-xs text-muted">Synchronized across matching endpoints</span>
                      </div>
                    </div>

                    <div className="grid grid-cols-2 gap-4">
                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-fullname">Full Name (Display Name)</label>
                        <input
                          id="admin-fullname"
                          type="text"
                          className="form-input"
                          placeholder="e.g. Corporate Support Administrator"
                          value={formSettings.admin_account_full_name || ''}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_full_name: e.target.value })
                          }
                        />
                      </div>

                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-desc">Account Description</label>
                        <input
                          id="admin-desc"
                          type="text"
                          className="form-input"
                          placeholder="e.g. Managed Support Administrator Account"
                          value={formSettings.admin_account_description || ''}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_description: e.target.value })
                          }
                        />
                      </div>
                    </div>

                    <div className="form-group">
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input
                          type="checkbox"
                          className="rounded"
                          checked={formSettings.admin_account_never_expires !== false}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_never_expires: e.target.checked })
                          }
                        />
                        <span className="text-sm text-main">
                          Password Never Expires (Prevents account lockout on disconnected machines)
                        </span>
                      </label>
                    </div>
                  </div>
                )}
              </div>
            )}
          </form>
        </div>
      </Modal>

      {/* Delete Confirmation Dialog */}
      <ConfirmDialog
        open={deleteConfirmOpen}
        onCancel={() => {
          setDeleteConfirmOpen(false)
          setPolicyToDelete(null)
        }}
        onConfirm={handleDeletePolicy}
        title="Delete System Policy"
        message={`Are you sure you want to delete policy "${policyToDelete?.name}"? Endpoints will revert to lower precedence policy or default settings.`}
        confirmLabel="Delete Policy"
        pending={deletePending}
      />
    </div>
  )
}
export default SystemPolicyPage
