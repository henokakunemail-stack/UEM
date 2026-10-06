import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Activity,
  AlertCircle,
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
  Search,
  Terminal,
  TerminalSquare,
  Users,
} from 'lucide-react'
import { DeviceDetailModal } from '../components/DeviceDetailModal'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceDTO, DeviceGroupDTO, DeviceListResponse } from '../types/api'
import { PAGE_SIZES, buildDevicesQuery, parseDevicesQuery } from './devicesQuery'
import type { DeviceStatus } from './devicesQuery'
import { DeviceGroupsPanel } from './DeviceGroupsPanel'
import { GroupMembersModal } from './GroupMembersModal'
import { EnrollTokenModal } from '../components/EnrollTokenModal'

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
  // Page size is a view preference, not a server filter, so it stays out of
  // the URL — a hand-edited ?limit=100000 would ask for the whole fleet.
  const [pageSize, setPageSize] = useState<(typeof PAGE_SIZES)[number]>(10)
  const [selectedDevice, setSelectedDevice] = useState<DeviceDTO | null>(null)
  const [retireTarget, setRetireTarget] = useState<DeviceDTO | null>(null)
  const [enrollOpen, setEnrollOpen] = useState(false)
  const [retiring, setRetiring] = useState(false)
  const [actionMsg, setActionMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(
    null
  )
  const toast = useToast()

  const { status, site, page, q, deviceId, tab, group } = query
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

  const fetchDevices = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getDevices(pageSize, offset, status, site, group)
      setData(res)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to fetch devices'
      setActionMsg({ type: 'error', text: msg })
    } finally {
      setLoading(false)
    }
  }, [group, offset, pageSize, site, status])

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
  useEffect(() => {
    setSelected(new Set())
  }, [group, tab])

  const confirmAssign = async () => {
    if (!assignTarget || selected.size === 0) return
    setAssigning(true)
    try {
      const added = await api.addGroupMembers(assignTarget.id, [...selected])
      toast.success(
        added === selected.size
          ? `Added ${added} device(s) to "${assignTarget.name}"`
          : `Added ${added} of ${selected.size} selected; the rest were already members`,
        'Devices Added'
      )
      setSelected(new Set())
      setAssignTarget(null)
      await loadGroups()
    } catch (err: unknown) {
      toast.error(
        err instanceof Error ? err.message : 'Failed to add devices to group',
        'Add Failed'
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
        const msg = err instanceof Error ? err.message : 'Failed to load device'
        setActionMsg({ type: 'error', text: `Could not open device: ${msg}` })
        toast.error(msg, 'Device Not Found')
      })
    return () => {
      active = false
    }
  }, [deviceId, toast])

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
      const successText = `Device ${retireTarget.hostname} retired successfully.`
      setActionMsg({ type: 'success', text: successText })
      toast.success(successText, 'Device Retired')
      setRetireTarget(null)
      fetchDevices()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Retire failed'
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, 'Retire Failed')
    } finally {
      setRetiring(false)
    }
  }

  const handlePing = async (device: DeviceDTO) => {
    try {
      const res = await api.pingDevice(device.id)
      const successText = `Ping sent to ${device.hostname} (Command: ${res.command_id})`
      setActionMsg({ type: 'success', text: successText })
      toast.info(successText, 'Ping Dispatched')
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Ping failed'
      setActionMsg({ type: 'error', text: msg })
      toast.error(msg, 'Ping Failed')
    }
  }

  return (
    <div className="page-container devices-page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Fleet Endpoint Inventory</h1>
          <p className="page-subtitle">
            Manage enrolled workstations, servers, and telemetry data
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
              <span>Generate Enrollment Token</span>
            </button>
          )}
          <button
            type="button"
            className="btn btn-secondary"
            onClick={fetchDevices}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {/* Devices / Groups. The tab lives in the URL so a group view is a
          shareable link and the browser back button steps between them. */}
      <div className="btn-group" role="tablist" aria-label="Devices sections">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'devices'}
          className={`btn ${tab === 'devices' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setQuery({ tab: 'devices', group: '' })}
        >
          <Laptop size={16} />
          <span>Devices</span>
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'groups'}
          className={`btn ${tab === 'groups' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setQuery({ tab: 'groups', group: '' })}
        >
          <Layers size={16} />
          <span>Groups</span>
        </button>
      </div>

      {actionMsg && (
        <div className={`notification-banner ${actionMsg.type}`} role="status">
          {actionMsg.type === 'success' ? <CheckCircle2 size={18} /> : <AlertCircle size={18} />}
          <span>{actionMsg.text}</span>
          <button
            type="button"
            className="banner-dismiss"
            onClick={() => setActionMsg(null)}
            aria-label="Dismiss notification"
          >
            &times;
          </button>
        </div>
      )}

      {tab === 'groups' ? (
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
            placeholder="Filter this page by hostname, device ID, OS, or site…"
            aria-label="Filter the endpoints on this page"
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
            aria-label="Filter by status"
          >
            <option value="">All Statuses</option>
            <option value="online">Online Only</option>
            <option value="offline">Offline Only</option>
            <option value="retired">Retired Only</option>
          </select>

          <select
            value={site}
            onChange={(e) => setQuery({ site: e.target.value, page: 1 })}
            className="select-input"
            aria-label="Filter by site"
          >
            <option value="">All Sites</option>
            <option value="hq">Headquarters (HQ)</option>
            <option value="branch-a">Branch A</option>
            <option value="branch-b">Branch B</option>
          </select>

          <select
            value={group}
            onChange={(e) => setQuery({ group: e.target.value, page: 1 })}
            className="select-input"
            aria-label="Filter by group"
          >
            <option value="">All Groups</option>
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
            aria-label="Endpoints per page"
          >
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {n} / page
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* Selection bar. Only rendered when something is checked, so the page is
          unchanged for the common case of reading the fleet without editing it. */}
      {canRetire && selected.size > 0 && (
        <div className="filter-bar" role="region" aria-label="Selection actions">
          <span className="filter-hint" style={{ margin: 0 }}>
            {selected.size} endpoint{selected.size === 1 ? '' : 's'} selected
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
              aria-label="Target group for the selection"
            >
              <option value="">Choose a group...</option>
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
              <span>{assigning ? 'Adding...' : 'Add to group'}</span>
            </button>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => {
                setSelected(new Set())
                setAssignTarget(null)
              }}
            >
              <span>Clear</span>
            </button>
          </div>
        </div>
      )}

      {groups.length === 0 && canRetire && tab === 'devices' && (
        <p className="filter-hint">
          No groups exist yet. Create one on the Groups tab to target a set of devices with a
          single run.
        </p>
      )}

      {/* The text filter runs on the current page only — the server has no
          search endpoint, so this narrows 10–50 rows, not the fleet. */}
      {q && (
        <p className="filter-hint">
          Showing {filteredItems.length} of {(data.devices || []).length} endpoints on this page
          matching “{q}”. Clear the box to see all {data.total}.
        </p>
      )}

      {/* Table */}
      <div className="table-card">
        <DataTable label="Fleet endpoints">
          <thead>
            <tr>
              {canRetire && <th style={{ width: 36 }}>Select</th>}
              <th>Status</th>
              <th>Hostname &amp; Identity</th>
              <th>Operating System</th>
              <th>Site</th>
              <th>Agent</th>
              <th>Last Heartbeat</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={canRetire ? 8 : 7} className="text-center py-8">
                  <div className="table-loader">
                    <RefreshCw size={24} className="spinning" />
                    <span>Loading fleet records...</span>
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
                        ? 'No endpoints in this group match the current filters'
                        : 'No endpoints match the current filters'}
                    </p>
                  </div>
                </td>
              </tr>
            ) : (
              filteredItems.map((d: DeviceDTO) => {
                const isRetired = Boolean(d.retired_at)
                return (
                  <tr key={d.id} className="device-row">
                    {canRetire && (
                      <td data-label="Select">
                        <input
                          type="checkbox"
                          checked={selected.has(d.id)}
                          onChange={() => toggleDevice(d.id)}
                          aria-label={`Select ${d.hostname}`}
                        />
                      </td>
                    )}
                    <td data-label="Status">
                      <span className={`status-pill ${isRetired ? 'retired' : d.status}`}>
                        <span className="dot"></span>
                        {isRetired ? 'RETIRED' : d.status.toUpperCase()}
                      </span>
                    </td>
                    <td data-label="Hostname & Identity">
                      {/* A cell that opens a modal is a button, not a div with
                          an onClick. */}
                      <button
                        type="button"
                        className="device-identity-cell"
                        onClick={() => setSelectedDevice(d)}
                        title="View hardware specs & software"
                      >
                        <strong className="device-name">{d.hostname}</strong>
                        <span className="device-id font-mono">{d.id.substring(0, 16)}...</span>
                      </button>
                    </td>
                    <td data-label="Operating System">
                      <div className="os-cell">
                        <span className="os-name">{d.os_name}</span>
                        <span className="os-version">{d.os_version}</span>
                      </div>
                    </td>
                    <td data-label="Site">
                      <span className="site-badge">{d.site || 'HQ'}</span>
                    </td>
                    <td data-label="Agent">
                      <span className="font-mono text-sm">{d.agent_version || '0.1.0'}</span>
                    </td>
                    <td data-label="Last Heartbeat">
                      <span className="timestamp-cell">
                        {d.last_seen_at ? new Date(d.last_seen_at).toLocaleTimeString() : 'Never'}
                      </span>
                    </td>
                    <td className="text-right" data-label="Actions">
                      <div className="row-actions">
                        <button
                          type="button"
                          className="btn btn-sm btn-secondary"
                          onClick={() => setSelectedDevice(d)}
                          title="View Hardware, Disks & Software"
                        >
                          Specs
                        </button>
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={() => onOpenExec?.(d)}
                            title="Run Remote Command"
                            aria-label={`Run remote command on ${d.hostname}`}
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
                            title="Open Interactive Terminal"
                            aria-label={`Open interactive terminal on ${d.hostname}`}
                          >
                            <Terminal size={12} />
                          </button>
                        )}
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={() => onOpenRemoteControl?.(d)}
                            title="Open Remote Desktop Screen & Control"
                            aria-label={`Open remote desktop control for ${d.hostname}`}
                          >
                            <Monitor size={12} />
                          </button>
                        )}
                        {d.status === 'online' && !isRetired && canManage && (
                          <button
                            type="button"
                            className="btn btn-sm btn-secondary"
                            onClick={() => handlePing(d)}
                            title="Send Ping"
                            aria-label={`Send ping to ${d.hostname}`}
                          >
                            <Activity size={12} />
                          </button>
                        )}
                        {canRetire && !isRetired && (
                          <button
                            type="button"
                            className="btn btn-sm btn-danger-outline"
                            onClick={() => setRetireTarget(d)}
                            title="Retire Device"
                            aria-label={`Retire ${d.hostname}`}
                          >
                            <Archive size={12} />
                          </button>
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
            Showing {data.total > 0 ? offset + 1 : 0} to{' '}
            {Math.min(offset + (data.devices || []).length, data.total)} of {data.total} endpoints
          </div>
          <div className="pagination-controls">
            <button
              type="button"
              className="btn btn-sm btn-secondary"
              onClick={() => setQuery({ page: Math.max(1, page - 1) })}
              disabled={page <= 1}
            >
              <ChevronLeft size={16} />
              <span>Previous</span>
            </button>
            <span className="page-indicator">
              Page {page} of {totalPages}
            </span>
            <button
              type="button"
              className="btn btn-sm btn-secondary"
              onClick={() => setQuery({ page: page + 1 })}
              disabled={page >= totalPages}
            >
              <span>Next</span>
              <ChevronRight size={16} />
            </button>
          </div>
        </div>
      </div>
        </>
      )}

      <ConfirmDialog
        open={Boolean(retireTarget)}
        title="Retire endpoint"
        message={
          retireTarget
            ? `Retire "${retireTarget.hostname}"? It stops reporting telemetry and is excluded from fleet health. This can be reversed with Restore.`
            : ''
        }
        confirmLabel="Retire device"
        pending={retiring}
        onConfirm={handleRetire}
        onCancel={() => setRetireTarget(null)}
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
    </div>
  )
}
