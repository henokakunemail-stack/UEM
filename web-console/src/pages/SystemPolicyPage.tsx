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
import { ApiError, api } from '../services/api'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import { KPICard } from '../components/KPICard'
import { useToast } from '../context/ToastContext'
import { useT } from '../i18n/core'
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
}> = ({ targetType, targetId, groups, devices, idPrefix, onChange }) => {
  const { t } = useT()
  return (
  <>
    <div className="form-group">
      <label className="form-label" htmlFor={`${idPrefix}-scope`}>
        {t('policy.scope_label')}
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
        <option value="all">{t('policy.scope_all')}</option>
        <option value="group">{t('policy.scope_group')}</option>
        <option value="device">{t('policy.scope_device')}</option>
      </select>
    </div>

    {targetType === 'group' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-group`}>
          {t('policy.select_group_label')}
        </label>
        <select
          id={`${idPrefix}-group`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: 'group', target_id: e.target.value })}
        >
          <option value="">{t('policy.select_group_placeholder')}</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {t(g.member_count === 1 ? 'policy.group_option_one' : 'policy.group_option', { name: g.name, count: g.member_count })}
            </option>
          ))}
        </select>
        {groups.length === 0 && (
          <p className="form-hint">{t('policy.no_groups')}</p>
        )}
      </div>
    )}

    {targetType === 'device' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-device`}>
          {t('policy.select_device_label')}
        </label>
        <select
          id={`${idPrefix}-device`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: 'device', target_id: e.target.value })}
        >
          <option value="">{t('policy.select_device_placeholder')}</option>
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
}

export const SystemPolicyPage: React.FC = () => {
  const { can } = usePermission()
  const toast = useToast()
  const { t } = useT()
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
      toast.error(t('policy.wallpaper_invalid_file'))
      return
    }
    setUploadingWallpaper(true)
    try {
      const res = await api.uploadWallpaper(file)
      setFormSettings((prev) => ({
        ...prev,
        wallpaper_url: res.wallpaper_url,
      }))
      toast.success(t('policy.wallpaper_uploaded'))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('policy.wallpaper_upload_failed'))
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
      toast.error(err instanceof Error ? err.message : t('policy.load_failed'))
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
    // The flag is the server's to set; a blank password means "keep the stored
    // one" when one is set, so only a typed password is sent.
    const { admin_account_password_set: _passwordSet, ...settings } = formSettings
    void _passwordSet
    try {
      if (editingPolicy) {
        await api.updateSystemPolicy(editingPolicy.id, {
          name: formName,
          description: formDescription,
          target_type: formTargetType,
          target_id: formTargetId,
          priority: formPriority,
          is_enabled: formIsEnabled,
          settings,
        })
        toast.success(t('policy.updated'))
      } else {
        await api.createSystemPolicy({
          name: formName,
          description: formDescription,
          target_type: formTargetType,
          target_id: formTargetId,
          priority: formPriority,
          is_enabled: formIsEnabled,
          settings,
        })
        toast.success(t('policy.created'))
      }
      setModalOpen(false)
      loadData()
    } catch (err) {
      toast.error(
        err instanceof ApiError && err.code === 'admin_password_required'
          ? t('policy.admin_password_required')
          : err instanceof Error ? err.message : t('policy.save_failed'),
      )
    }
  }

  const handleToggleEnabled = async (p: SystemPolicyDTO) => {
    try {
      await api.updateSystemPolicy(p.id, { is_enabled: !p.is_enabled })
      toast.info(t(p.is_enabled ? 'policy.toggled_disabled' : 'policy.toggled_enabled', { name: p.name }))
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('policy.toggle_failed'))
    }
  }

  const handleApplyNow = async (p: SystemPolicyDTO) => {
    try {
      await api.applySystemPolicy(p.id)
      toast.info(t('policy.applying', { name: p.name }))
      setTimeout(loadData, 1500)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('policy.apply_failed'))
    }
  }

  const handleDeletePolicy = async () => {
    if (!policyToDelete) return
    setDeletePending(true)
    try {
      await api.deleteSystemPolicy(policyToDelete.id)
      toast.info(t('policy.deleted', { name: policyToDelete.name }))
      setDeleteConfirmOpen(false)
      setPolicyToDelete(null)
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('policy.delete_failed'))
    } finally {
      setDeletePending(false)
    }
  }

  const handleForceSyncDevice = async (deviceId: string) => {
    setSyncingDeviceId(deviceId)
    try {
      await api.syncDeviceSystemPolicy(deviceId)
      toast.success(t('policy.sync_dispatched'))
      setTimeout(loadData, 2000)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('policy.sync_failed'))
    } finally {
      setSyncingDeviceId(null)
    }
  }

  const resolveTargetName = (targetType: string, targetId: string) => {
    if (targetType === 'all') return t('policy.target_entire_fleet')
    if (targetType === 'group') {
      const g = groups.find((item) => item.id === targetId)
      return g ? t('policy.target_group', { name: g.name }) : t('policy.target_group_id', { id: targetId.substring(0, 8) })
    }
    if (targetType === 'device') {
      const d = devices.find((item) => item.id === targetId)
      return d ? t('policy.target_device', { name: d.hostname }) : t('policy.target_device_id', { id: targetId.substring(0, 8) })
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
            <h1 className="text-2xl font-bold text-main tracking-tight">{t('policy.title')}</h1>
          </div>
          <p className="text-sm text-muted mt-1">
            {t('policy.subtitle')}
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
            <span>{t('policy.refresh')}</span>
          </button>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary flex items-center gap-2"
              onClick={handleOpenCreate}
            >
              <Plus className="h-4 w-4" />
              <span>{t('policy.create_policy')}</span>
            </button>
          )}
        </div>
      </div>

      {/* KPI Overview Cards */}
      <div className="kpi-grid">
        <KPICard
          title={t('policy.kpi_active_title')}
          value={`${activePolicies} / ${totalPolicies}`}
          subtitle={t('policy.kpi_active_subtitle')}
          icon={<Shield size={20} />}
          variant="primary"
        />
        <KPICard
          title={t('policy.kpi_usb_title')}
          value={usbRestrictedCount > 0 ? t('policy.enforced') : t('policy.unrestricted')}
          subtitle={t('policy.kpi_usb_subtitle')}
          icon={<HardDrive size={20} />}
          variant={usbRestrictedCount > 0 ? 'warning' : 'neutral'}
        />
        <KPICard
          title={t('policy.kpi_wallpaper_title')}
          value={wallpaperEnforcedCount > 0 ? t('policy.kpi_wallpaper_value', { count: wallpaperEnforcedCount }) : t('policy.off')}
          subtitle={t('policy.kpi_wallpaper_subtitle')}
          icon={<ImageIcon size={20} />}
          variant="neutral"
        />
        <KPICard
          title={t('policy.kpi_compliance_title')}
          value={syncedDevicesCount}
          subtitle={t('policy.kpi_compliance_subtitle')}
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
          {t('policy.tab_policies', { count: policies.length })}
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'compliance' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setActiveTab('compliance')}
        >
          {t('policy.tab_compliance', { count: deviceStates.length })}
        </button>
      </div>

      {/* Tab: Policies */}
      {activeTab === 'policies' && (
        <div className="table-card">
          <DataTable label={t('policy.table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('policy.col_name')}</th>
                  <th>{t('policy.scope_label')}</th>
                  <th>{t('policy.col_priority')}</th>
                  <th>{t('policy.col_rules')}</th>
                  <th>{t('policy.col_status')}</th>
                  <th>{t('policy.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {policies.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-6 text-muted">
                      {t('policy.empty_policies')}
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
                                {t('policy.badge_usb_blocked')}
                              </span>
                            )}
                            {s.usb_storage_mode === 'read_only' && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-amber-500/10 text-amber-400 border border-amber-500/20">
                                {t('policy.badge_usb_read_only')}
                              </span>
                            )}
                            {s.wallpaper_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-blue-500/10 text-blue-400 border border-blue-500/20">
                                {s.wallpaper_locked ? t('policy.badge_wallpaper_locked') : t('policy.badge_wallpaper')}
                              </span>
                            )}
                            {s.firewall_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-cyan-500/10 text-cyan-400 border border-cyan-500/20">
                                {t('policy.badge_firewall')}
                              </span>
                            )}
                            {s.defender_protection_enabled && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
                                {t('policy.badge_defender')}
                              </span>
                            )}
                            {s.disable_autoplay && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-orange-500/10 text-orange-400 border border-orange-500/20">
                                {t('policy.badge_no_autoplay')}
                              </span>
                            )}
                            {s.disable_powershell && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-yellow-500/10 text-yellow-400 border border-yellow-500/20">
                                {t('policy.badge_no_ps')}
                              </span>
                            )}
                            {s.screensaver_timeout_minutes ? (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-pink-500/10 text-pink-400 border border-pink-500/20">
                                {t(s.screensaver_password_required ? 'policy.badge_screensaver_pwd' : 'policy.badge_screensaver', { minutes: s.screensaver_timeout_minutes })}
                              </span>
                            ) : null}
                            {s.inactivity_timeout_minutes > 0 && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-purple-500/10 text-purple-400 border border-purple-500/20">
                                {t('policy.badge_lock', { minutes: s.inactivity_timeout_minutes })}
                              </span>
                            )}
                            {s.disable_task_mgr && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                {t('policy.badge_no_taskmgr')}
                              </span>
                            )}
                            {s.disable_cmd && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                {t('policy.badge_no_cmd')}
                              </span>
                            )}
                            {s.disable_registry_tools && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                {t('policy.badge_no_regedit')}
                              </span>
                            )}
                            {s.disable_control_panel && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-zinc-500/10 text-zinc-300 border border-zinc-500/20">
                                {t('policy.badge_no_control_panel')}
                              </span>
                            )}
                            {s.windows_update_mode === 'auto_install' && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                                {t('policy.badge_auto_update')}
                              </span>
                            )}
                            {s.admin_account_enabled && s.admin_account_username && (
                              <span className="px-1.5 py-0.5 rounded text-xs bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                                {t('policy.badge_admin', { name: s.admin_account_username })}
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
                            {p.is_enabled ? t('policy.enabled') : t('policy.disabled')}
                          </span>
                        </td>
                        <td>
                          <div className="flex items-center gap-1 justify-end">
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title={p.is_enabled ? t('policy.action_disable') : t('policy.action_enable')}
                                onClick={() => handleToggleEnabled(p)}
                              >
                                <Power size={15} />
                              </button>
                            )}
                            {isTechnician && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title={t('policy.action_apply_now')}
                                onClick={() => handleApplyNow(p)}
                              >
                                <Send size={15} />
                              </button>
                            )}
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-main"
                                title={t('policy.action_edit')}
                                onClick={() => handleOpenEdit(p)}
                              >
                                <Edit2 size={15} />
                              </button>
                            )}
                            {isAdmin && (
                              <button
                                type="button"
                                className="btn-action p-1.5 text-muted hover:text-red-400"
                                title={t('policy.action_delete')}
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
          <DataTable label={t('policy.compliance_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('policy.col_device')}</th>
                  <th>{t('policy.col_policy_version')}</th>
                  <th>{t('policy.col_enforcement')}</th>
                  <th>{t('policy.col_active_settings')}</th>
                  <th>{t('policy.col_last_applied')}</th>
                  <th>{t('policy.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {deviceStates.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center py-6 text-muted">
                      {t('policy.empty_states')}
                    </td>
                  </tr>
                ) : (
                  deviceStates.map((s) => {
                    const d = devices.find((item) => item.id === s.device_id)
                    // What the device itself reported. Falling back to the
                    // defaults here showed "USB: allow, Firewall: off" for a
                    // device that had never reported anything -- a state the
                    // console invented, presented as one it had observed.
                    const applied = s.applied_settings
                    let badge = 'bg-zinc-500/10 text-zinc-400'
                    let label = t('policy.status_awaiting')
                    if (s.status === 'synced') {
                      badge = 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                      label = t('policy.status_synced')
                    } else if (s.status === 'failed') {
                      badge = 'bg-red-500/10 text-red-400 border border-red-500/20'
                      label = t('policy.status_failed')
                    }

                    return (
                      <tr key={s.device_id}>
                        <td>
                          <div className="font-semibold text-main">{d ? d.hostname : s.device_id}</div>
                          <div className="text-xs text-muted">
                            {d ? `${d.os_name} ${d.os_version || ''}` : t('policy.unknown_os')}
                          </div>
                        </td>
                        <td>
                          <span className="font-mono text-xs text-muted">
                            {s.policy_version || t('policy.version_none')}
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
                          {applied ? (
                            <div className="text-xs text-muted space-y-0.5">
                              <div>{t('policy.applied_usb')} <span className="text-main font-medium">{applied.usb_storage_mode}</span></div>
                              <div>{t('policy.applied_wallpaper')} <span className="text-main font-medium">{applied.wallpaper_enabled ? t('policy.enforced') : t('policy.off')}</span></div>
                              <div>{t('policy.applied_timeout')} <span className="text-main font-medium">{applied.inactivity_timeout_minutes ? t('policy.timeout_minutes', { minutes: applied.inactivity_timeout_minutes }) : t('policy.disabled')}</span></div>
                              <div>{t('policy.applied_admin')} <span className="text-main font-medium">{applied.admin_account_enabled ? applied.admin_account_username || t('policy.enforced') : t('policy.unmanaged')}</span></div>
                            </div>
                          ) : (
                            <span className="text-xs text-muted">{t('common.not_reported')}</span>
                          )}
                        </td>
                        <td>
                          <span className="text-xs text-muted">
                            {s.last_applied_at ? new Date(s.last_applied_at).toLocaleString() : t('policy.never')}
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
                                <span>{t('policy.force_sync')}</span>
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
        title={editingPolicy ? t('policy.edit_title', { name: editingPolicy.name }) : t('policy.create_title')}
        size="lg"
        footer={
          <div className="flex items-center justify-end gap-2 w-full">
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="policy-form"
              className="btn btn-primary"
            >
              {editingPolicy ? t('policy.save_changes') : t('policy.create_policy')}
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
              {t('policy.tab_general')}
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'peripherals' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('peripherals')}
            >
              {t('policy.tab_usb')}
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'desktop' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('desktop')}
            >
              {t('policy.tab_wallpaper')}
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'security' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('security')}
            >
              {t('policy.tab_security')}
            </button>
            <button
              type="button"
              className={`btn btn-sm ${modalTab === 'accounts' ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => setModalTab('accounts')}
            >
              {t('policy.tab_admin')}
            </button>
          </div>

          <form id="policy-form" onSubmit={handleSavePolicy} className="space-y-4">
            {/* General Tab */}
            {modalTab === 'general' && (
              <div className="space-y-4">
                <div className="form-group">
                  <label className="form-label" htmlFor="policy-name">{t('policy.name_label')}</label>
                  <input
                    id="policy-name"
                    type="text"
                    required
                    className="form-input"
                    placeholder={t('policy.name_placeholder')}
                    value={formName}
                    onChange={(e) => setFormName(e.target.value)}
                  />
                </div>

                <div className="form-group">
                  <label className="form-label" htmlFor="policy-desc">{t('policy.description_label')}</label>
                  <textarea
                    id="policy-desc"
                    className="form-input"
                    rows={2}
                    placeholder={t('policy.description_placeholder')}
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
                      {t('policy.priority_label')}
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
                      <span className="text-sm font-medium text-main">{t('policy.enable_immediately')}</span>
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
                    {t('policy.usb_heading')}
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    {t('policy.usb_intro')}
                    <strong> {t('policy.usb_intro_strong')}</strong>
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
                      <div className="text-sm font-medium text-main">{t('policy.usb_allow_title')}</div>
                      <div className="text-xs text-muted">{t('policy.usb_allow_desc')}</div>
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
                      <div className="text-sm font-medium text-red-400">{t('policy.usb_block_title')}</div>
                      <div className="text-xs text-muted">
                        {t('policy.usb_block_desc')}
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
                      <div className="text-sm font-medium text-amber-400">{t('policy.usb_ro_title')}</div>
                      <div className="text-xs text-muted">
                        {t('policy.usb_ro_desc')}
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
                      <div className="text-sm font-medium text-main">{t('policy.autoplay_title')}</div>
                      <div className="text-xs text-muted">
                        {t('policy.autoplay_desc')}
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
                    {t('policy.wallpaper_heading')}
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    {t('policy.wallpaper_intro')}
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
                    <span className="text-sm font-medium text-main">{t('policy.wallpaper_enforce')}</span>
                  </label>
                </div>

                {formSettings.wallpaper_enabled && (
                  <>
                    <div className="form-group">
                      <label className="form-label">
                        {t('policy.wallpaper_upload_label')}
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
                          <span>{uploadingWallpaper ? t('policy.uploading') : t('policy.browse_upload')}</span>
                        </button>
                        <span className="text-xs text-muted">{t('policy.upload_hint')}</span>
                      </div>
                      {formSettings.wallpaper_url && (
                        <div className="mt-2 flex items-center gap-3" style={{ padding: '0.5rem', borderRadius: '0.375rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface)' }}>
                          <img
                            src={formSettings.wallpaper_url}
                            alt={t('policy.wallpaper_preview_alt')}
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
                        {t('policy.wallpaper_url_label')}
                      </label>
                      <input
                        id="wallpaper-url"
                        type="text"
                        required={formSettings.wallpaper_enabled}
                        className="form-input"
                        placeholder={t('policy.wallpaper_url_placeholder')}
                        value={formSettings.wallpaper_url || ''}
                        onChange={(e) =>
                          setFormSettings({ ...formSettings, wallpaper_url: e.target.value })
                        }
                      />
                      <span className="form-hint">
                        {t('policy.wallpaper_url_hint')}
                      </span>
                    </div>

                    <div className="form-group">
                      <label className="form-label" htmlFor="wallpaper-style">
                        {t('policy.wallpaper_style_label')}
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
                        <option value="fill">{t('policy.style_fill')}</option>
                        <option value="fit">{t('policy.style_fit')}</option>
                        <option value="stretch">{t('policy.style_stretch')}</option>
                        <option value="center">{t('policy.style_center')}</option>
                        <option value="tile">{t('policy.style_tile')}</option>
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
                            {t('policy.wallpaper_lock_title')}
                          </div>
                          <div className="text-xs text-muted">
                            {t('policy.wallpaper_lock_desc')}
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
                    {t('policy.security_heading')}
                  </h4>
                  <p className="text-xs text-muted mt-1">
                    {t('policy.security_intro')}
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
                        {t('policy.firewall_title')}
                      </div>
                      <div className="text-xs text-muted">
                        {t('policy.firewall_desc')}
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
                        {t('policy.defender_title')}
                      </div>
                      <div className="text-xs text-muted">
                        {t('policy.defender_desc')}
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
                        {t('policy.powershell_title')}
                      </div>
                      <div className="text-xs text-muted">
                        {t('policy.powershell_desc')}
                      </div>
                    </div>
                  </label>
                </div>

                {/* Screen Saver & Inactivity */}
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pb-2" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                  <div className="form-group">
                    <label className="form-label" htmlFor="inactivity-timeout">
                      {t('policy.inactivity_label')}
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
                      <option value={0}>{t('policy.inactivity_disabled')}</option>
                      <option value={5}>{t('policy.minutes_option', { minutes: 5 })}</option>
                      <option value={10}>{t('policy.minutes_option', { minutes: 10 })}</option>
                      <option value={15}>{t('policy.minutes_standard', { minutes: 15 })}</option>
                      <option value={30}>{t('policy.minutes_option', { minutes: 30 })}</option>
                      <option value={60}>{t('policy.minutes_option', { minutes: 60 })}</option>
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label" htmlFor="screensaver-timeout">
                      {t('policy.screensaver_label')}
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
                      <option value={0}>{t('policy.disabled')}</option>
                      <option value={5}>{t('policy.minutes_option', { minutes: 5 })}</option>
                      <option value={10}>{t('policy.minutes_option', { minutes: 10 })}</option>
                      <option value={15}>{t('policy.minutes_option', { minutes: 15 })}</option>
                      <option value={30}>{t('policy.minutes_option', { minutes: 30 })}</option>
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
                      <span className="text-xs text-main font-medium">{t('policy.screensaver_password')}</span>
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
                      <div className="text-sm font-medium text-main">{t('policy.taskmgr_title')}</div>
                      <div className="text-xs text-muted">{t('policy.taskmgr_desc')}</div>
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
                      <div className="text-sm font-medium text-main">{t('policy.cmd_title')}</div>
                      <div className="text-xs text-muted">{t('policy.cmd_desc')}</div>
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
                      <div className="text-sm font-medium text-main">{t('policy.regedit_title')}</div>
                      <div className="text-xs text-muted">{t('policy.regedit_desc')}</div>
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
                      <div className="text-sm font-medium text-main">{t('policy.control_panel_title')}</div>
                      <div className="text-xs text-muted">{t('policy.control_panel_desc')}</div>
                    </div>
                  </label>
                </div>

                {/* Windows Update Mode */}
                <div className="form-group pt-2">
                  <label className="form-label" htmlFor="wu-mode">
                    {t('policy.wu_label')}
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
                    <option value="default">{t('policy.wu_default')}</option>
                    <option value="auto_install">{t('policy.wu_auto')}</option>
                    <option value="notify_only">{t('policy.wu_notify')}</option>
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
                    {t('policy.admin_heading')}
                  </h4>
                  <p className="text-xs text-muted mt-1 leading-relaxed">
                    {t('policy.admin_intro')}
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
                      {t('policy.admin_enforce')}
                    </span>
                  </label>
                </div>

                {formSettings.admin_account_enabled && (
                  <div className="space-y-4 p-4 rounded-lg bg-zinc-900/60 border border-zinc-700/60">
                    <div className="grid grid-cols-2 gap-4">
                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-username">{t('policy.admin_username_label')}</label>
                        <input
                          id="admin-username"
                          type="text"
                          required
                          className="form-input"
                          placeholder={t('policy.admin_username_placeholder')}
                          value={formSettings.admin_account_username || ''}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_username: e.target.value.trim() })
                          }
                        />
                        <span className="text-xs text-muted">{t('policy.admin_username_hint')}</span>
                      </div>

                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-password">
                          {formSettings.admin_account_password_set ? t('policy.admin_password_label_optional') : t('policy.admin_password_label')}
                        </label>
                        <div className="relative">
                          <input
                            id="admin-password"
                            type={showAdminPassword ? 'text' : 'password'}
                            required={!formSettings.admin_account_password_set}
                            autoComplete="new-password"
                            className="form-input pr-14"
                            placeholder={
                              formSettings.admin_account_password_set
                                ? t('policy.admin_password_keep_placeholder')
                                : t('policy.admin_password_placeholder')
                            }
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
                            {showAdminPassword ? t('policy.hide') : t('policy.show')}
                          </button>
                        </div>
                        <span className="text-xs text-muted">
                          {formSettings.admin_account_password_set ? t('policy.admin_password_stored_hint') : t('policy.admin_password_hint')}
                        </span>
                      </div>
                    </div>

                    <div className="grid grid-cols-2 gap-4">
                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-fullname">{t('policy.admin_fullname_label')}</label>
                        <input
                          id="admin-fullname"
                          type="text"
                          className="form-input"
                          placeholder={t('policy.admin_fullname_placeholder')}
                          value={formSettings.admin_account_full_name || ''}
                          onChange={(e) =>
                            setFormSettings({ ...formSettings, admin_account_full_name: e.target.value })
                          }
                        />
                      </div>

                      <div className="form-group">
                        <label className="form-label" htmlFor="admin-desc">{t('policy.admin_desc_label')}</label>
                        <input
                          id="admin-desc"
                          type="text"
                          className="form-input"
                          placeholder={t('policy.admin_desc_placeholder')}
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
                          {t('policy.admin_never_expires')}
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
        title={t('policy.delete_title')}
        message={t('policy.delete_message', { name: String(policyToDelete?.name) })}
        confirmLabel={t('policy.delete_confirm')}
        pending={deletePending}
      />
    </div>
  )
}
export default SystemPolicyPage
