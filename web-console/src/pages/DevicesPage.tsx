import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Activity,
  AlertCircle,
  AlertTriangle,
  Archive,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Filter,
  KeyRound,
  Laptop,
  Layers,
  Monitor,
  RefreshCw,
  RotateCcw,
  Search,
  Terminal,
  TerminalSquare,
  Trash2,
  Users,
} from 'lucide-react'
import { DeviceDetailModal } from '../components/DeviceDetailModal'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { EnrollTokensPanel } from '../components/EnrollTokensPanel'
import { relativeTime } from '../services/relativeTime'

// The server's filter value for devices with no site.
const UNASSIGNED_SITE = '__unassigned__'
import { api } from '../services/api'
import type { DeviceDTO, DeviceGroupDTO, DeviceListResponse } from '../types/api'
import { PAGE_SIZES, UNKNOWN_AGENT_VERSION, buildDevicesQuery, parseDevicesQuery } from './devicesQuery'
import type { DeviceStatus } from './devicesQuery'
import { DeviceGroupsPanel } from './DeviceGroupsPanel'
import { GroupMembersModal } from './GroupMembersModal'
import { EnrollTokenModal } from '../components/EnrollTokenModal'
import { useT } from '../i18n/core'
import { UninstallTokenModal } from '../components/UninstallTokenModal'
import { isNewDevice } from './newDevice'

export const DevicesPage: React.FC<{
  onOpenExec?: (device: DeviceDTO) => void
  onOpenTerminal?: (device: DeviceDTO, shell: string) => void
  onOpenRemoteControl?: (device: DeviceDTO) => void
}> = ({ onOpenExec, onOpenTerminal, onOpenRemoteControl }) => {
  const { can } = usePermission()
  const [searchParams, setSearchParams] = useSearchParams()
  const query = parseDevicesQuery(searchParams)

  const [data, setData] = useState<DeviceListResponse>({
    devices: [],
    count: 0,
    total: 0,
    limit: 10,
    offset: 0,
  })
  const [loading, setLoading] = useState(true)
  // When the list was last fetched: the clock the New badge is measured
  // against, so a refresh is what moves a device out of its first day.
  const [renderedAt, setRenderedAt] = useState(() => Date.now())
  // Page size is a view preference, not a server filter, so it stays out of
  // the URL — a hand-edited ?limit=100000 would ask for the whole fleet.
  const [pageSize, setPageSize] = useState<(typeof PAGE_SIZES)[number]>(10)
  const [selectedDevice, setSelectedDevice] = useState<DeviceDTO | null>(null)
  const [retireTarget, setRetireTarget] = useState<DeviceDTO | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<DeviceDTO | null>(null)
  const [uninstallTokenDevice, setUninstallTokenDevice] = useState<DeviceDTO | null>(null)
  const [enrollOpen, setEnrollOpen] = useState(false)
  const [retiring, setRetiring] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [bulkDeleting, setBulkDeleting] = useState(false)
  const [bulkDeleteConfirmOpen, setBulkDeleteConfirmOpen] = useState(false)
  const [actionMsg, setActionMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(
    null
  )
  const toast = useToast()
  const { t, lang } = useT()

  const { status, site, agent, page, q, deviceId, tab, group } = query
  const offset = (page - 1) * pageSize

  // Group administration. `selected` is the multi-select for the "add to group"
  // action, so it lives here rather than in the panel: the checkboxes are on the
  // Devices table and the action is on the same toolbar.
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [membersGroup, setMembersGroup] = useState<DeviceGroupDTO | null>(null)
  const [assignTarget, setAssignTarget] = useState<DeviceGroupDTO | null>(null)
  const [assigning, setAssigning] = useState(false)

  const loadGroups = useCallback(async () => {
    try {
      setGroups(await api.getDeviceGroups())
    } catch {
      // The group filter degrades to "no group" rather than blocking the device
      // list: a technician who cannot read groups should still see endpoints.
      setGroups([])
    }
  }, [])

  useEffect(() => {
    loadGroups()
  }, [loadGroups])

  const setQuery = useCallback(
    (next: Partial<typeof query>) => {
      setSearchParams(buildDevicesQuery({ ...query, ...next }))
    },
    [query, setSearchParams]
  )

  const [siteOptions, setSiteOptions] = useState<string[]>([])
  const [agentOptions, setAgentOptions] = useState<{ version: string; count: number }[]>([])
  useEffect(() => {
    let active = true
    api.getDeviceAgentVersions().then(
      (versions) => {
        if (active) setAgentOptions(versions)
      },
      () => {
        // Without the list the filter still offers "all"; the table works.
      },
    )
    return () => {
      active = false
    }
  }, [])
  useEffect(() => {
    let active = true
    api.getDashboardSites().then(
      (sites) => {
        if (active) setSiteOptions(sites.map((m) => m.site))
      },
      () => {
        // Without the list the filter still offers "all"; the table works.
      },
    )
    return () => {
      active = false
    }
  }, [])

  const fetchDevices = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getDevices(pageSize, offset, status, site, group, agent)
      setData(res)
      setRenderedAt(Date.now())
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.fetch_failed')
      setActionMsg({ type: 'error', text: msg })
    } finally {
      setLoading(false)
    }
  }, [agent, group, offset, pageSize, site, status, t])

  const toggleDevice = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  // A selection made before switching to the Groups tab, or before changing the
  // group filter, refers to rows the operator can no longer see. Leaving it
  // checked would make "add to group" act on invisible devices.
  // The same goes for the page, status and site: the selection used to survive
  // them, so a bulk action could reach rows on another page.
  useEffect(() => {
    setSelected(new Set())
  }, [group, tab, offset, status, site, agent])

  const confirmAssign = async () => {
    if (!assignTarget || selected.size === 0) return
    setAssigning(true)
    try {
      const added = await api.addGroupMembers(assignTarget.id, [...selected])
      toast.success(
        added === selected.size
          ? t('devices.added_all', { count: added, group: assignTarget.name })
          : t('devices.added_partial', { added, selected: selected.size }),
        t('devices.added_title')
      )
      setSelected(new Set())
      setAssignTarget(null)
      await loadGroups()
    } catch (err: unknown) {
      toast.error(
        err instanceof Error ? err.message : t('devices.add_failed'),
        t('devices.add_failed_title')
      )
    } finally {
      setAssigning(false)
    }
  }

  useEffect(() => {
    fetchDevices()
  }, [fetchDevices])

  // Deep link from the dashboard alert list: ?device_id=… fetches that one
  // device and opens its detail modal, rather than filtering a list by
  // hostname and hoping the first substring match is the right machine.
  // The ref is compared, never reset, so re-navigating to a device already
  // opened once is a no-op — including after the modal is closed and the user
  // clicks the same alert again. Clearing it whenever the id is absent lets the
  // next link to that device reopen it.
  const openedDeviceId = useRef<string | null>(null)
  useEffect(() => {
    if (!deviceId) {
      openedDeviceId.current = null
      return
    }
    if (openedDeviceId.current === deviceId) return
    openedDeviceId.current = deviceId
    let active = true
    api
      .getDevice(deviceId)
      .then((d) => {
        if (active) setSelectedDevice(d)
      })
      .catch((err: unknown) => {
        if (!active) return
        // Let a retry through: the fetch failed, so this id is not "opened".
        openedDeviceId.current = null
        const msg = err instanceof Error ? err.message : t('devices.load_failed')
        setActionMsg({ type: 'error', text: t('devices.open_failed', { error: msg }) })
        toast.error(msg, t('devices.not_found_title'))
      })
    return () => {
      active = false
    }
  }, [deviceId, toast, t])

  // Server-side filtered. q is deliberately NOT in here — the server has no
  // search parameter, so q narrows the rows already on this page only, and
  // the input says so.
  const filteredItems = (data.devices || []).filter((d: DeviceDTO) => {
    if (!q) return true
    const term = q.toLowerCase()
    return (
      d.hostname.toLowerCase().includes(term) ||
      d.id.toLowerCase().includes(term) ||
      d.os_name.toLowerCase().includes(term) ||
      (d.site && d.site.toLowerCase().includes(term))
    )
  })

  const totalPages = Math.max(1, Math.ceil(data.total / pageSize))

  // The backend gates retire at admin, so offering it to a technician only
  // walks them into a 403. Same endpoint, admin only.
  const canManage = can('technician')
  const canRetire = can('admin')

  const handleRetire = async () => {
    if (!retireTarget) return
    setRetiring(true)
    try {
      await api.retireDevice(retireTarget.id)
      const successText = t('devices.retired_success', { hostname: retireTarget.hostname })
      setActionMsg({ type: 'success', text: successText })
      toast.success(successText, t('devices.retired_title'))
      setRetireTarget(null)
      fetchDevices()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.retire_failed')
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, t('devices.retire_failed_title'))
    } finally {
      setRetiring(false)
    }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteDevice(deleteTarget.id)
      const successText = t('devices.deleted_success', { hostname: deleteTarget.hostname, id: deleteTarget.id.slice(0, 8) })
      setActionMsg({ type: 'success', text: successText })
      toast.success(successText, t('devices.deleted_title'))
      setDeleteTarget(null)
      fetchDevices()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.delete_failed')
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, t('devices.delete_failed_title'))
    } finally {
      setDeleting(false)
    }
  }

  // Only the retired devices that are selected and on screen are deleted --
  // the count the confirm dialog showed. It used to send every selected id and
  // swallow each failure, then report success for whatever went through.
  const handleBulkDelete = async () => {
    const ids = (data.devices || []).filter((d) => selected.has(d.id) && d.retired_at).map((d) => d.id)
    if (ids.length === 0) return
    setBulkDeleting(true)
    try {
      let deleted = 0
      let failed = 0
      for (const id of ids) {
        try {
          await api.deleteDevice(id)
          deleted++
        } catch {
          failed++
        }
      }
      if (failed > 0) {
        const text = t('devices.bulk_deleted_partial', { ok: deleted, failed })
        setActionMsg({ type: 'error', text })
        toast.error(text, t('devices.bulk_delete_failed_title'))
      } else {
        const successText = t('devices.bulk_deleted', { count: deleted })
        setActionMsg({ type: 'success', text: successText })
        toast.success(successText, t('devices.bulk_deleted_title'))
      }
      setSelected(new Set())
      setBulkDeleteConfirmOpen(false)
      fetchDevices()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.bulk_delete_failed')
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, t('devices.bulk_delete_failed_title'))
    } finally {
      setBulkDeleting(false)
    }
  }

  const handleRestore = async (device: DeviceDTO) => {
    try {
      await api.restoreDevice(device.id)
      const successText = t('devices.restored', { hostname: device.hostname })
      setActionMsg({ type: 'success', text: successText })
      toast.success(successText, t('devices.restored_title'))
      fetchDevices()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.restore_failed')
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, t('devices.restore_failed_title'))
    }
  }

  const hostnameCounts = React.useMemo(() => {
    const counts = new Map<string, number>()
    for (const d of data.devices || []) {
      if (d.hostname && d.hostname !== 'Pending Enrollment') {
        counts.set(d.hostname, (counts.get(d.hostname) || 0) + 1)
      }
    }
    return counts
  }, [data.devices])

  const selectedRetiredCount = React.useMemo(() => {
    return (data.devices || []).filter((d) => selected.has(d.id) && Boolean(d.retired_at)).length
  }, [data.devices, selected])

  const handlePing = async (device: DeviceDTO) => {
    try {
      const res = await api.pingDevice(device.id)
      const successText = t('devices.ping_sent', { hostname: device.hostname, command_id: res.command_id })
      setActionMsg({ type: 'success', text: successText })
      toast.info(successText, t('devices.ping_sent_title'))
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('devices.ping_failed')
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, t('devices.ping_failed_title'))
    }
  }

  return (
    <div className="page-container devices-page">
      <div className="page-header">
        <div>
          <h1 className="page-title">{t('devices.title')}</h1>
          <p className="page-subtitle">
            {t('devices.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          {can('admin') && (
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setEnrollOpen(true)}
            >
              <KeyRound size={16} />
              <span>{t('devices.generate_enroll')}</span>
            </button>
          )}
          <button
            type="button"
            className="btn btn-secondary"
            onClick={fetchDevices}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('devices.refresh')}</span>
          </button>
        </div>
      </div>

      {/* Devices / Groups. The tab lives in the URL so a group view is a
          shareable link and the browser back button steps between them. */}
      <div className="btn-group" role="tablist" aria-label={t('devices.tabs_label')}>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'devices'}
          className={`btn ${tab === 'devices' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setQuery({ tab: 'devices', group: '' })}
        >
          <Laptop size={16} />
          <span>{t('devices.tab_devices')}</span>
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'groups'}
          className={`btn ${tab === 'groups' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setQuery({ tab: 'groups', group: '' })}
        >
          <Layers size={16} />
          <span>{t('devices.tab_groups')}</span>
        </button>
        {/* Enrollment tokens are admin-only on the server. */}
        {can('admin') && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'tokens'}
            className={`btn ${tab === 'tokens' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setQuery({ tab: 'tokens', group: '' })}
          >
            <KeyRound size={16} />
            <span>{t('devices.tab_tokens')}</span>
          </button>
        )}
      </div>

      {actionMsg && (
        <div className={`notification-banner ${actionMsg.type}`} role="status">
          {actionMsg.type === 'success' ? <CheckCircle2 size={18} /> : <AlertCircle size={18} />}
          <span>{actionMsg.text}</span>
          <button
            type="button"
            className="banner-dismiss"
            onClick={() => setActionMsg(null)}
            aria-label={t('devices.dismiss')}
          >
            &times;
          </button>
        </div>
      )}

      {tab === 'tokens' && can('admin') ? (
        <EnrollTokensPanel />
      ) : tab === 'groups' ? (
        <DeviceGroupsPanel
          onViewDevices={(groupId) => setQuery({ tab: 'devices', group: groupId, page: 1 })}
          onManageMembers={(groupId) => {
            const g = groups.find((x) => x.id === groupId)
            // A group that was deleted in another tab still has to open: the
            // members modal takes the name from the caller, so a missing entry
            // falls back to the id rather than rendering "undefined".
            setMembersGroup(g ?? { id: groupId, name: groupId, description: '', created_at: '', updated_at: '', member_count: 0 })
          }}
        />
      ) : (
        <>
      {/* Filter / Search Bar */}
      <div className="filter-bar">
        <div className="search-wrap">
          <Search size={18} className="search-icon" aria-hidden="true" />
          <input
            type="text"
            className="search-input"
            placeholder={t('devices.search_placeholder')}
            aria-label={t('devices.search_label')}
            value={q}
            onChange={(e) => setQuery({ q: e.target.value })}
          />
        </div>

        <div className="filter-group">
          <Filter size={16} className="filter-icon" aria-hidden="true" />
          <select
            value={status}
            onChange={(e) => setQuery({ status: e.target.value as DeviceStatus, page: 1 })}
            className="select-input"
            aria-label={t('devices.filter_status')}
          >
            <option value="">{t('devices.status_all')}</option>
            <option value="online">{t('devices.status_online')}</option>
            <option value="offline">{t('devices.status_offline')}</option>
            <option value="retired">{t('devices.status_retired')}</option>
          </select>

          <select
            value={site}
            onChange={(e) => setQuery({ site: e.target.value, page: 1 })}
            className="select-input"
            aria-label={t('devices.filter_site')}
          >
            <option value="">{t('devices.site_all')}</option>
            {/* The sites devices actually report, not a fixed hq/branch list. */}
            {siteOptions.map((s) => (
              <option key={s} value={s === 'unassigned' ? UNASSIGNED_SITE : s}>
                {s === 'unassigned' ? t('devices.site_unassigned') : s}
              </option>
            ))}
          </select>

          <select
            value={agent}
            onChange={(e) => setQuery({ agent: e.target.value, page: 1 })}
            className="select-input"
            aria-label={t('devices.filter_agent')}
          >
            <option value="">{t('devices.agent_all')}</option>
            {/* The versions the fleet actually runs, newest first. */}
            {agentOptions.map((a) => (
              <option key={a.version || UNKNOWN_AGENT_VERSION} value={a.version || UNKNOWN_AGENT_VERSION}>
                {a.version
                  ? t('devices.agent_option', { version: a.version, count: a.count })
                  : t('devices.agent_unknown', { count: a.count })}
              </option>
            ))}
          </select>

          <select
            value={group}
            onChange={(e) => setQuery({ group: e.target.value, page: 1 })}
            className="select-input"
            aria-label={t('devices.filter_group')}
          >
            <option value="">{t('devices.group_all')}</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name} ({g.member_count})
              </option>
            ))}
          </select>

          <select
            value={pageSize}
            onChange={(e) => {
              setPageSize(Number(e.target.value) as (typeof PAGE_SIZES)[number])
              setQuery({ page: 1 })
            }}
            className="select-input"
            aria-label={t('devices.page_size_label')}
          >
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {t('devices.per_page', { count: n })}
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* Selection bar. Only rendered when something is checked, so the page is
          unchanged for the common case of reading the fleet without editing it. */}
      {canRetire && selected.size > 0 && (
        <div className="filter-bar" role="region" aria-label={t('devices.selection_actions')}>
          <span className="filter-hint" style={{ margin: 0 }}>
            {t(selected.size === 1 ? 'devices.selected_one' : 'devices.selected_many', { count: selected.size })}
          </span>
          <div className="filter-group">
            <Layers size={16} className="filter-icon" aria-hidden="true" />
            <select
              className="select-input"
              value={assignTarget?.id ?? ''}
              onChange={(e) => {
                const g = groups.find((x) => x.id === e.target.value)
                setAssignTarget(g ?? null)
              }}
              aria-label={t('devices.assign_target_label')}
            >
              <option value="">{t('devices.choose_group')}</option>
              {groups.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name} ({g.member_count})
                </option>
              ))}
            </select>
            <button
              type="button"
              className="btn btn-primary"
              onClick={confirmAssign}
              disabled={!assignTarget || assigning}
            >
              {assigning ? (
                <RefreshCw size={16} className="spinning" />
              ) : (
                <Users size={16} />
              )}
              <span>{assigning ? t('devices.adding') : t('devices.add_to_group')}</span>
            </button>
            {canRetire && selectedRetiredCount > 0 && (
              <button
                type="button"
                className="btn btn-danger"
                onClick={() => setBulkDeleteConfirmOpen(true)}
                disabled={bulkDeleting}
                title={t('devices.bulk_delete_hint')}
              >
                {bulkDeleting ? (
                  <RefreshCw size={16} className="spinning" />
                ) : (
                  <Trash2 size={16} />
                )}
                <span>{t('devices.delete_retired', { count: selectedRetiredCount })}</span>
              </button>
            )}
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => {
                setSelected(new Set())
                setAssignTarget(null)
              }}
            >
              <span>{t('devices.clear')}</span>
            </button>
          </div>
        </div>
      )}

      {groups.length === 0 && canRetire && tab === 'devices' && (
        <p className="filter-hint">
          {t('devices.no_groups_hint')}
        </p>
      )}

      {/* The text filter runs on the current page only — the server has no
          search endpoint, so this narrows 10–50 rows, not the fleet. */}
      {q && (
        <p className="filter-hint">
          {t('devices.filter_hint', {
            shown: filteredItems.length,
            page_count: (data.devices || []).length,
            q,
            total: data.total,
          })}
        </p>
      )}

      {/* Table */}
      <div className="table-card">
        <DataTable label={t('devices.table_label')}>
          <thead>
            <tr>
              {canRetire && <th style={{ width: 36 }}>{t('devices.col_select')}</th>}
              <th>{t('devices.col_status')}</th>
              <th>{t('devices.col_hostname')}</th>
              <th>{t('devices.col_os')}</th>
              <th>{t('devices.col_site')}</th>
              <th>{t('devices.col_agent')}</th>
              <th>{t('devices.col_heartbeat')}</th>
              <th className="text-right">{t('devices.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={canRetire ? 8 : 7} className="text-center py-8">
                  <div className="table-loader">
                    <RefreshCw size={24} className="spinning" />
                    <span>{t('devices.loading_records')}</span>
                  </div>
                </td>
              </tr>
            ) : filteredItems.length === 0 ? (
              <tr>
                <td colSpan={canRetire ? 8 : 7} className="text-center py-8">
                  <div className="empty-state">
                    <Laptop size={32} />
                    <p>
                      {group
                        ? t('devices.empty_group')
                        : t('devices.empty')}
                    </p>
                  </div>
                </td>
              </tr>
            ) : (
              filteredItems.map((d: DeviceDTO) => {
                const isRetired = Boolean(d.retired_at)
                const isPending = d.hostname === 'Pending Enrollment' || (!d.last_seen_at && !isRetired && d.status === 'offline')
                return (
                  <tr key={d.id} className="device-row">
                    {canRetire && (
                      <td data-label={t('devices.col_select')}>
                        <input
                          type="checkbox"
                          checked={selected.has(d.id)}
                          onChange={() => toggleDevice(d.id)}
                          aria-label={t('devices.select_device', { hostname: d.hostname })}
                        />
                      </td>
                    )}
                    <td data-label={t('devices.col_status')}>
                      <span className={`status-pill ${isRetired ? 'retired' : isPending ? 'pending' : d.status}`}>
                        <span className="dot"></span>
                        {isRetired ? t('devices.pill_retired') : isPending ? t('devices.pill_pending') : d.status.toUpperCase()}
                      </span>
                    </td>
                    <td data-label={t('devices.col_hostname')}>
                      {/* A cell that opens a modal is a button, not a div with
                          an onClick. */}
                      <button
                        type="button"
                        className="device-identity-cell"
                        onClick={() => setSelectedDevice(d)}
                        title={t('devices.view_specs')}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                          <strong className="device-name">{d.hostname}</strong>
                          {isNewDevice(d, renderedAt) && (
                            <span
                              className="badge-severity info"
                              style={{ padding: '1px 6px', fontSize: 10, borderRadius: 4 }}
                              title={t('devices.new_title', { time: new Date(d.enrolled_at).toLocaleString() })}
                            >
                              {t('devices.new_badge')}
                            </span>
                          )}
                          {(hostnameCounts.get(d.hostname) ?? 0) > 1 && (
                            <span
                              className="badge-severity warning"
                              style={{ display: 'inline-flex', alignItems: 'center', gap: 3, padding: '1px 6px', fontSize: 10, borderRadius: 4, textTransform: 'none' }}
                              title={t('devices.duplicate_title', { hostname: d.hostname, id: d.id })}
                            >
                              <AlertTriangle size={10} />
                              {t('devices.duplicate', { id: d.id.slice(0, 8) })}
                            </span>
                          )}
                        </div>
                        <span className="device-id font-mono">{d.id.substring(0, 16)}...</span>
                      </button>
                    </td>
                    <td data-label={t('devices.col_os')}>
                      <div className="os-cell">
                        <span className="os-name">{d.os_name}</span>
                        <span className="os-version">{d.os_version}</span>
                      </div>
                    </td>
                    <td data-label={t('devices.col_site')}>
                      <span className="site-badge">{d.site || t('devices.site_unassigned')}</span>
                    </td>
                    <td data-label={t('devices.col_agent')}>
                      <span className="font-mono text-sm">{d.agent_version || '—'}</span>
                    </td>
                    <td data-label={t('devices.col_heartbeat')}>
                      <span className="timestamp-cell">
                        {d.last_seen_at ? (
                          <span title={new Date(d.last_seen_at).toLocaleString()}>{relativeTime(d.last_seen_at, lang)}</span>
                        ) : (
                          t('devices.never')
                        )}
                      </span>
                    </td>
                    <td className="text-right" data-label={t('devices.col_actions')}>
                      <div className="row-actions">
                        <button
                          type="button"
                          className="btn btn-sm btn-secondary"
                          onClick={() => setSelectedDevice(d)}
                          title={t('devices.specs_title')}
                        >
                          {t('devices.specs')}
                        </button>
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={() => onOpenExec?.(d)}
                            title={t('devices.run_command_title')}
                            aria-label={t('devices.run_command_label', { hostname: d.hostname })}
                          >
                            <TerminalSquare size={12} />
                          </button>
                        )}
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={() =>
                              onOpenTerminal?.(
                                d,
                                d.os_name === 'windows' ? 'powershell' : 'bash'
                              )
                            }
                            title={t('devices.terminal_title')}
                            aria-label={t('devices.terminal_label', { hostname: d.hostname })}
                          >
                            <Terminal size={12} />
                          </button>
                        )}
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={() => onOpenRemoteControl?.(d)}
                            title={t('devices.remote_title')}
                            aria-label={t('devices.remote_label', { hostname: d.hostname })}
                          >
                            <Monitor size={12} />
                          </button>
                        )}
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-secondary"
                            onClick={() => handlePing(d)}
                            title={t('devices.ping_title')}
                            aria-label={t('devices.ping_label', { hostname: d.hostname })}
                          >
                            <Activity size={12} />
                          </button>
                        )}
                        {canRetire && (
                          <button
                            type="button"
                            className="btn btn-sm btn-secondary"
                            onClick={() => setUninstallTokenDevice(d)}
                            title={t('devices.uninstall_title')}
                            aria-label={t('devices.uninstall_label', { hostname: d.hostname })}
                          >
                            <KeyRound size={12} />
                          </button>
                        )}
                        {canRetire && !isRetired && (
                          <button
                            type="button"
                            className="btn btn-sm btn-danger-outline"
                            onClick={() => setRetireTarget(d)}
                            title={t('devices.retire_title')}
                            aria-label={t('devices.retire_label', { hostname: d.hostname })}
                          >
                            <Archive size={12} />
                          </button>
                        )}
                        {canRetire && isRetired && (
                          <>
                            <button
                              type="button"
                              className="btn btn-sm btn-secondary"
                              onClick={() => handleRestore(d)}
                              title={t('devices.restore_title')}
                              aria-label={t('devices.restore_label', { hostname: d.hostname })}
                            >
                              <RotateCcw size={12} />
                            </button>
                            <button
                              type="button"
                              className="btn btn-sm btn-danger"
                              onClick={() => setDeleteTarget(d)}
                              title={t('devices.delete_title')}
                              aria-label={t('devices.delete_label', { hostname: d.hostname })}
                            >
                              <Trash2 size={12} />
                            </button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </DataTable>

        {/* Pagination Bar */}
        <div className="pagination-bar">
          <div className="pagination-info">
            {t('devices.pagination', {
              from: data.total > 0 ? offset + 1 : 0,
              to: Math.min(offset + (data.devices || []).length, data.total),
              total: data.total,
            })}
          </div>
          <div className="pagination-controls">
            <button
              type="button"
              className="btn btn-sm btn-secondary"
              onClick={() => setQuery({ page: Math.max(1, page - 1) })}
              disabled={page <= 1}
            >
              <ChevronLeft size={16} />
              <span>{t('devices.previous')}</span>
            </button>
            <span className="page-indicator">
              {t('devices.page_of', { page, total: totalPages })}
            </span>
            <button
              type="button"
              className="btn btn-sm btn-secondary"
              onClick={() => setQuery({ page: page + 1 })}
              disabled={page >= totalPages}
            >
              <span>{t('devices.next')}</span>
              <ChevronRight size={16} />
            </button>
          </div>
        </div>
      </div>
        </>
      )}

      <ConfirmDialog
        open={Boolean(retireTarget)}
        title={t('devices.retire_dialog_title')}
        message={
          retireTarget
            ? t('devices.retire_dialog_message', { hostname: retireTarget.hostname })
            : ''
        }
        confirmLabel={t('devices.retire_confirm')}
        pending={retiring}
        onConfirm={handleRetire}
        onCancel={() => setRetireTarget(null)}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('devices.delete_dialog_title')}
        message={
          deleteTarget
            ? t('devices.delete_dialog_message', { hostname: deleteTarget.hostname, id: deleteTarget.id.slice(0, 8) })
            : ''
        }
        confirmLabel={t('devices.delete_confirm')}
        pending={deleting}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />

      <ConfirmDialog
        open={bulkDeleteConfirmOpen}
        title={t('devices.bulk_dialog_title')}
        message={t('devices.bulk_dialog_message', { count: selectedRetiredCount })}
        confirmLabel={t('devices.bulk_confirm', { count: selectedRetiredCount })}
        pending={bulkDeleting}
        onConfirm={handleBulkDelete}
        onCancel={() => setBulkDeleteConfirmOpen(false)}
      />

      {/* Detail Modal */}
      {selectedDevice && (
        <DeviceDetailModal
          device={selectedDevice}
          onClose={() => {
            setSelectedDevice(null)
            // Drop ?device_id= on close so a refresh does not immediately
            // reopen the same modal.
            if (deviceId) setQuery({ deviceId: null })
          }}
        />
      )}

      {membersGroup && (
        <GroupMembersModal
          open
          groupId={membersGroup.id}
          groupName={membersGroup.name}
          onClose={() => setMembersGroup(null)}
          onChanged={loadGroups}
        />
      )}

      {/* Minting a token pre-registers the device, so the list is stale the moment
          one exists — refresh on the way out rather than making the operator
          guess why a new row has not appeared. */}
      <EnrollTokenModal open={enrollOpen} onClose={() => setEnrollOpen(false)} onEnrolled={fetchDevices} />

      <UninstallTokenModal
        open={Boolean(uninstallTokenDevice)}
        device={uninstallTokenDevice}
        onClose={() => setUninstallTokenDevice(null)}
      />
    </div>
  )
}
