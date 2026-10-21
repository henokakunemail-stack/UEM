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
  Terminal,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import { useT } from '../i18n/core'
import type { DeviceDTO, DeviceListResponse, RemoteControlSessionDTO } from '../types/api'

// Display label key per session status; the status value comes from the server.
const SESSION_STATUS_KEYS: Record<string, string> = {
  active: 'remote_control.status_active',
  ended: 'remote_control.status_ended',
  rejected: 'remote_control.status_rejected',
}

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
  onOpenTerminal?: (device: DeviceDTO, shell: string) => void
}> = ({ onOpenSession, onOpenTerminal }) => {
  const { t } = useT()
  const { can } = usePermission()
  const canOperate = can('technician')
  const toast = useToast()

  const isWindows = (osName?: string) => {
    if (!osName) return false
    const s = osName.toLowerCase()
    return s.includes('windows') || s.includes('win')
  }
  // Linux endpoints stream an X11 desktop (agent 1.5.4+) and keep the terminal.
  const isLinux = (osName?: string) => (osName ?? '').toLowerCase().includes('linux')

  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [devicesLoading, setDevicesLoading] = useState(true)
  const [deviceError, setDeviceError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [sessions, setSessions] = useState<RemoteControlSessionDTO[]>([])
  const [sessionsLoading, setSessionsLoading] = useState(false)
  const [sessionsFailed, setSessionsFailed] = useState(false)

  // Disambiguate duplicate hostnames across online fleet
  const hostnameCounts = React.useMemo(() => {
    const counts = new Map<string, number>()
    for (const d of devices) {
      const h = (d.hostname || '').toLowerCase()
      counts.set(h, (counts.get(h) || 0) + 1)
    }
    return counts
  }, [devices])

  const loadDevices = useCallback(async () => {
    setDevicesLoading(true)
    setDeviceError(null)
    try {
      const res: DeviceListResponse = await api.getDevices(50, 0, 'online')
      setDevices(res.devices || [])
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('remote_control.load_failed')
      setDeviceError(msg)
      toast.error(msg, t('remote_control.toast_endpoints_unavailable'))
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
    setSessionsFailed(false)
    api
      .getRemoteControlSessions(selected.id, 20)
      .then((rows) => {
        if (active) setSessions(rows || [])
      })
      .catch(() => {
        // History is supplementary. A failure here must not blank the page or
        // block starting a session, but it is said as a failure: an empty
        // list read as "this device was never controlled".
        if (!active) return
        setSessions([])
        setSessionsFailed(true)
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
          <h1 className="page-title">{t('remote_control.title')}</h1>
          <p className="page-subtitle">{t('remote_control.subtitle')}</p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={loadDevices}
            disabled={devicesLoading}
          >
            <Monitor size={16} />
            <span>{t('remote_control.refresh')}</span>
          </button>
        </div>
      </div>

      {!canOperate && (
        <div className="notification-banner" role="status">
          <Eye size={18} />
          <span>
            {t('remote_control.view_only_notice')}
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
                <Monitor size={16} /> {t('remote_control.online_endpoints')}
              </h2>
              <p className="card-subtitle">
                {devicesLoading
                  ? t('remote_control.loading_endpoints')
                  : filtered.length === 1
                    ? t('remote_control.online_count_one')
                    : t('remote_control.online_count', { count: filtered.length })}
              </p>
            </div>
            <div className="search-wrap">
              <Search size={16} className="search-icon" aria-hidden="true" />
              <input
                type="text"
                className="search-input"
                placeholder={t('remote_control.filter_placeholder')}
                aria-label={t('remote_control.filter_label')}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
          </div>

          <DataTable label={t('remote_control.online_endpoints')}>
            <thead>
              <tr>
                <th>{t('remote_control.col_endpoint')}</th>
                <th>{t('remote_control.col_os')}</th>
                <th>{t('remote_control.col_site')}</th>
                <th>{t('remote_control.col_last_heartbeat')}</th>
                <th className="text-right">{t('remote_control.col_session')}</th>
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
                          ? t('remote_control.empty_online')
                          : t('remote_control.empty_filter')}
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
                    <td data-label={t('remote_control.col_endpoint')}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.375rem', flexWrap: 'wrap' }}>
                        <strong className="device-name">{d.hostname}</strong>
                        {(hostnameCounts.get((d.hostname || '').toLowerCase()) || 0) > 1 && (
                          <span
                            className="badge-severity warning"
                            style={{
                              fontSize: '0.625rem',
                              padding: '0.125rem 0.375rem',
                              textTransform: 'none',
                              display: 'inline-flex',
                              alignItems: 'center',
                              gap: '0.25rem',
                            }}
                            title={t('remote_control.duplicate_hostname_title')}
                          >
                            {t('remote_control.duplicate_hostname', { id: d.id.slice(0, 8) })}
                          </span>
                        )}
                      </div>
                      <span className="device-id font-mono">{d.id.substring(0, 16)}...</span>
                    </td>
                    <td data-label={t('remote_control.col_os')}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.375rem', flexWrap: 'wrap' }}>
                        <span className="os-name">{d.os_name}</span>
                        {isWindows(d.os_name) || isLinux(d.os_name) ? (
                          <span
                            style={{
                              fontSize: '0.625rem',
                              padding: '0.125rem 0.375rem',
                              borderRadius: '9999px',
                              background: 'rgba(59, 130, 246, 0.15)',
                              color: 'var(--primary, #3b82f6)',
                              border: '1px solid rgba(59, 130, 246, 0.3)',
                              fontWeight: 500,
                            }}
                            title={isLinux(d.os_name) ? t('remote_control.badge_desktop_linux_title') : t('remote_control.badge_desktop_title')}
                          >
                            {isLinux(d.os_name) ? t('remote_control.badge_desktop_linux') : t('remote_control.badge_desktop')}
                          </span>
                        ) : (
                          <span
                            style={{
                              fontSize: '0.625rem',
                              padding: '0.125rem 0.375rem',
                              borderRadius: '9999px',
                              background: 'rgba(245, 158, 11, 0.15)',
                              color: '#f59e0b',
                              border: '1px solid rgba(245, 158, 11, 0.3)',
                              fontWeight: 500,
                            }}
                            title={t('remote_control.badge_terminal_only_title')}
                          >
                            {t('remote_control.badge_terminal_only')}
                          </span>
                        )}
                      </div>
                    </td>
                    <td data-label={t('remote_control.col_site')}>
                      <span className="site-badge">{d.site || 'HQ'}</span>
                    </td>
                    <td data-label={t('remote_control.col_last_heartbeat')}>
                      <span className="timestamp-cell">
                        {d.last_seen_at ? new Date(d.last_seen_at).toLocaleTimeString() : t('remote_control.never')}
                      </span>
                    </td>
                    <td className="text-right" data-label={t('remote_control.col_session')}>
                      {isLinux(d.os_name) && (
                        <span style={{ display: 'inline-flex', gap: '0.375rem' }}>
                          <button
                            type="button"
                            className="btn btn-sm btn-primary"
                            onClick={(e) => {
                              e.stopPropagation()
                              handleStart(d)
                            }}
                            disabled={!canOperate}
                            title={canOperate ? t('remote_control.start_title') : t('remote_control.start_requires_technician')}
                            aria-label={t('remote_control.start_label', { hostname: d.hostname })}
                          >
                            <Play size={12} />
                            <span>{t('remote_control.start')}</span>
                          </button>
                          <button
                            type="button"
                            className="btn btn-sm btn-secondary"
                            onClick={(e) => {
                              e.stopPropagation()
                              onOpenTerminal?.(d, 'bash')
                            }}
                            disabled={!canOperate || !onOpenTerminal}
                            title={t('remote_control.terminal_title')}
                            aria-label={t('remote_control.terminal_label', { hostname: d.hostname })}
                          >
                            <Terminal size={12} />
                            <span>{t('remote_control.terminal')}</span>
                          </button>
                        </span>
                      )}
                      {isLinux(d.os_name) ? null : isWindows(d.os_name) ? (
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
                              ? t('remote_control.start_title')
                              : t('remote_control.start_requires_technician')
                          }
                          aria-label={t('remote_control.start_label', { hostname: d.hostname })}
                        >
                          <Play size={12} />
                          <span>{t('remote_control.start')}</span>
                        </button>
                      ) : (
                        <button
                          type="button"
                          className="btn btn-sm btn-secondary"
                          onClick={(e) => {
                            e.stopPropagation()
                            if (onOpenTerminal) {
                              const isMac = (d.os_name || '').toLowerCase().includes('darwin') || (d.os_name || '').toLowerCase().includes('mac')
                              onOpenTerminal(d, isMac ? 'zsh' : 'bash')
                            } else {
                              toast.info(t('remote_control.terminal_hint'), t('remote_control.toast_terminal_available'))
                            }
                          }}
                          disabled={!canOperate}
                          title={t('remote_control.terminal_title')}
                          aria-label={t('remote_control.terminal_label', { hostname: d.hostname })}
                        >
                          <Terminal size={12} />
                          <span>{t('remote_control.terminal')}</span>
                        </button>
                      )}
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
                <History size={16} /> {t('remote_control.session_history')}
              </h2>
              <p className="card-subtitle">
                {selected
                  ? sessions.length === 1
                    ? t('remote_control.history_count_one', { hostname: selected.hostname })
                    : t('remote_control.history_count', { hostname: selected.hostname, count: sessions.length })
                  : t('remote_control.select_endpoint_sessions')}
              </p>
            </div>
          </div>

          {!selected ? (
            <div className="empty-state">
              <HardDrive size={28} />
              <p>{t('remote_control.select_endpoint_history')}</p>
            </div>
          ) : (
            <DataTable label={t('remote_control.history_table_label')}>
              <thead>
                <tr>
                  <th>{t('remote_control.col_started')}</th>
                  <th>{t('remote_control.col_operator')}</th>
                  <th>{t('remote_control.col_mode')}</th>
                  <th>{t('remote_control.col_frames')}</th>
                  <th>{t('remote_control.col_status')}</th>
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
                        <p role={sessionsFailed ? 'alert' : undefined}>
                          {sessionsFailed ? t('remote_control.history_load_failed') : t('remote_control.empty_history')}
                        </p>
                      </div>
                    </td>
                  </tr>
                ) : (
                  sessions.map((s) => (
                    <tr key={s.id}>
                      <td data-label={t('remote_control.col_started')}>
                        <span className="timestamp-cell">
                          {new Date(s.started_at).toLocaleString()}
                        </span>
                      </td>
                      <td data-label={t('remote_control.col_operator')}>{s.operator_name || s.operator_id}</td>
                      <td data-label={t('remote_control.col_mode')}>
                        <span className={`site-badge ${s.session_mode === 'full_control' ? 'control' : ''}`}>
                          {s.session_mode === 'full_control' ? t('remote_control.mode_control') : t('remote_control.mode_view_only')}
                        </span>
                      </td>
                      <td data-label={t('remote_control.col_frames')}>
                        <span className="font-mono text-sm">{s.frames_transmitted}</span>
                      </td>
                      <td data-label={t('remote_control.col_status')}>
                        <span className={`status-pill ${s.status === 'active' ? 'online' : 'offline'}`}>
                          <span className="dot"></span>
                          {(SESSION_STATUS_KEYS[s.status] ? t(SESSION_STATUS_KEYS[s.status]) : s.status).toUpperCase()}
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
        <MousePointer size={12} /> {t('remote_control.footer_hint')}
      </p>
    </div>
  )
}
