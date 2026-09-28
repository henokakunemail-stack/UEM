import React, { useCallback, useEffect, useState } from 'react'
import {
  Monitor,
  MousePointer,
  Eye,
  Play,
  Loader2,
  AlertCircle,
  Search,
  History,
  HardDrive,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceDTO, DeviceListResponse, RemoteControlSessionDTO } from '../types/api'

/**
 * Remote control landing page. The live desktop itself lives in
 * RemoteControlModal — this page is the entry point and the audit trail.
 *
 * Two things it deliberately does not do:
 * - It does not offer the control button for a device that is offline. The
 *   server rejects the session with 409 anyway, and a button that always fails
 *   teaches the operator to click faster rather than to check the status.
 * - It does not show a permission error for viewers. The backend gates session
 *   start at technician, and hiding the action is clearer than letting a
 *   technician-only click return a 403.
 */
export const RemoteControlPage: React.FC<{
  onOpenSession?: (device: DeviceDTO) => void
}> = ({ onOpenSession }) => {
  const { can } = usePermission()
  const canOperate = can('technician')
  const toast = useToast()

  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [devicesLoading, setDevicesLoading] = useState(true)
  const [deviceError, setDeviceError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [sessions, setSessions] = useState<RemoteControlSessionDTO[]>([])
  const [sessionsLoading, setSessionsLoading] = useState(false)

  const loadDevices = useCallback(async () => {
    setDevicesLoading(true)
    setDeviceError(null)
    try {
      const res: DeviceListResponse = await api.getDevices(50, 0, 'online')
      setDevices(res.devices || [])
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to load endpoints'
      setDeviceError(msg)
      toast.error(msg, 'Endpoints Unavailable')
    } finally {
      setDevicesLoading(false)
    }
  }, [toast])

  useEffect(() => {
    loadDevices()
  }, [loadDevices])

  // Session history is per-device, so it follows whichever endpoint is
  // selected. Nothing is fetched until one is: 50 parallel history requests on
  // page load would be 50x the work for a list the operator reads one row of.
  const [selected, setSelected] = useState<DeviceDTO | null>(null)

  useEffect(() => {
    if (!selected) {
      setSessions([])
      return
    }
    let active = true
    setSessionsLoading(true)
    api
      .getRemoteControlSessions(selected.id, 20)
      .then((rows) => {
        if (active) setSessions(rows || [])
      })
      .catch(() => {
        // History is supplementary. A failure here must not blank the page or
        // block starting a session, so it degrades to an empty list.
        if (active) setSessions([])
      })
      .finally(() => {
        if (active) setSessionsLoading(false)
      })
    return () => {
      active = false
    }
  }, [selected])

  const filtered = devices.filter((d) => {
    if (!query) return true
    const term = query.toLowerCase()
    return (
      d.hostname.toLowerCase().includes(term) ||
      d.id.toLowerCase().includes(term) ||
      (d.site || '').toLowerCase().includes(term)
    )
  })

  const handleStart = (device: DeviceDTO) => {
    onOpenSession?.(device)
  }

  return (
    <div className="page-container remote-control-page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Remote Control</h1>
          <p className="page-subtitle">Live interactive desktop sessions on online endpoints</p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={loadDevices}
            disabled={devicesLoading}
          >
            <Monitor size={16} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {!canOperate && (
        <div className="notification-banner" role="status">
          <Eye size={18} />
          <span>
            You are signed in with view-only access. You can watch session history, but starting a
            desktop session requires the technician role.
          </span>
        </div>
      )}

      {deviceError && (
        <div className="notification-banner error" role="alert">
          <AlertCircle size={18} />
          <span>{deviceError}</span>
        </div>
      )}

      <div className="split-grid">
        <div className="table-card">
          <div className="card-header">
            <div>
              <h2 className="card-title">
                <Monitor size={16} /> Online endpoints
              </h2>
              <p className="card-subtitle">
                {devicesLoading
                  ? 'Loading endpoints...'
                  : `${filtered.length} online endpoint${filtered.length === 1 ? '' : 's'}`}
              </p>
            </div>
            <div className="search-wrap">
              <Search size={16} className="search-icon" aria-hidden="true" />
              <input
                type="text"
                className="search-input"
                placeholder="Filter by hostname, ID, or site"
                aria-label="Filter online endpoints"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
          </div>

          <DataTable label="Online endpoints">
            <thead>
              <tr>
                <th>Endpoint</th>
                <th>OS</th>
                <th>Site</th>
                <th>Last heartbeat</th>
                <th className="text-right">Session</th>
              </tr>
            </thead>
            <tbody>
              {devicesLoading ? (
                <tr>
                  <td colSpan={5} className="text-center py-8">
                    <Loader2 size={20} className="spinning" />
                  </td>
                </tr>
              ) : filtered.length === 0 ? (
                <tr>
                  <td colSpan={5} className="text-center py-8">
                    <div className="empty-state">
                      <Monitor size={28} />
                      <p>
                        {devices.length === 0
                          ? 'No endpoints are online. Remote control needs a live agent connection.'
                          : 'No endpoints match this filter.'}
                      </p>
                    </div>
                  </td>
                </tr>
              ) : (
                filtered.map((d) => (
                  <tr
                    key={d.id}
                    className={selected?.id === d.id ? 'row-selected' : undefined}
                    onClick={() => setSelected(d)}
                  >
                    <td data-label="Endpoint">
                      <strong className="device-name">{d.hostname}</strong>
                      <span className="device-id font-mono">{d.id.substring(0, 16)}...</span>
                    </td>
                    <td data-label="OS">
                      <span className="os-name">{d.os_name}</span>
                    </td>
                    <td data-label="Site">
                      <span className="site-badge">{d.site || 'HQ'}</span>
                    </td>
                    <td data-label="Last heartbeat">
                      <span className="timestamp-cell">
                        {d.last_seen_at ? new Date(d.last_seen_at).toLocaleTimeString() : 'Never'}
                      </span>
                    </td>
                    <td className="text-right" data-label="Session">
                      <button
                        type="button"
                        className="btn btn-sm btn-primary"
                        onClick={(e) => {
                          e.stopPropagation()
                          handleStart(d)
                        }}
                        disabled={!canOperate}
                        title={
                          canOperate
                            ? 'Open a live remote desktop session'
                            : 'Starting a session requires the technician role'
                        }
                        aria-label={`Start remote desktop session on ${d.hostname}`}
                      >
                        <Play size={12} />
                        <span>Start</span>
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </DataTable>
        </div>

        <div className="table-card">
          <div className="card-header">
            <div>
              <h2 className="card-title">
                <History size={16} /> Session history
              </h2>
              <p className="card-subtitle">
                {selected
                  ? `${selected.hostname} — last ${sessions.length} session${sessions.length === 1 ? '' : 's'}`
                  : 'Select an endpoint to see its sessions'}
              </p>
            </div>
          </div>

          {!selected ? (
            <div className="empty-state">
              <HardDrive size={28} />
              <p>Select an endpoint to load its remote control history.</p>
            </div>
          ) : (
            <DataTable label="Remote control session history">
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Operator</th>
                  <th>Mode</th>
                  <th>Frames</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {sessionsLoading ? (
                  <tr>
                    <td colSpan={5} className="text-center py-8">
                      <Loader2 size={20} className="spinning" />
                    </td>
                  </tr>
                ) : sessions.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="text-center py-8">
                      <div className="empty-state">
                        <History size={28} />
                        <p>No remote control sessions recorded for this endpoint.</p>
                      </div>
                    </td>
                  </tr>
                ) : (
                  sessions.map((s) => (
                    <tr key={s.id}>
                      <td data-label="Started">
                        <span className="timestamp-cell">
                          {new Date(s.started_at).toLocaleString()}
                        </span>
                      </td>
                      <td data-label="Operator">{s.operator_name || s.operator_id}</td>
                      <td data-label="Mode">
                        <span className={`site-badge ${s.session_mode === 'full_control' ? 'control' : ''}`}>
                          {s.session_mode === 'full_control' ? 'Control' : 'View only'}
                        </span>
                      </td>
                      <td data-label="Frames">
                        <span className="font-mono text-sm">{s.frames_transmitted}</span>
                      </td>
                      <td data-label="Status">
                        <span className={`status-pill ${s.status === 'active' ? 'online' : 'offline'}`}>
                          <span className="dot"></span>
                          {s.status.toUpperCase()}
                        </span>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </DataTable>
          )}
        </div>
      </div>

      <p className="filter-hint">
        <MousePointer size={12} /> A session sends this operator's mouse and keyboard to the
        endpoint in real time. Every session is recorded in the audit trail with its operator and
        frame count.
      </p>
    </div>
  )
}
