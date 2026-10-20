import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  CheckCircle2,
  Clock,
  Play,
  RefreshCw,
  RotateCcw,
  Search,
  ShieldAlert,
  ShieldCheck,
  X,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import type { DeviceDTO, PatchDetailDTO, PatchSummaryDTO } from '../types/api'
import { useT } from '../i18n/core'

// An absolute date, not "3d ago". The empty-state panel is a compliance claim
// and "3d ago" invites reading it as current; an auditor needs the date itself.
function formatScanTime(iso: string, t: (key: string) => string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return t('patches.unrecorded_time')
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export const PatchesPage: React.FC = () => {
  const { can } = usePermission()
  // Scan and install both dispatch work to endpoints, which the server gates at
  // technician and above. Hiding the buttons keeps a viewer from clicking into
  // a 403 instead of telling them the action is not theirs to take.
  const canAct = can('technician')
  const [summary, setSummary] = useState<PatchSummaryDTO | null>(null)
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedDevice, setSelectedDevice] = useState<DeviceDTO | null>(null)
  const [devicePatches, setDevicePatches] = useState<PatchDetailDTO[]>([])
  const [patchesLoading, setPatchesLoading] = useState(false)
  const [patchesError, setPatchesError] = useState<string | null>(null)
  const [scanningId, setScanningId] = useState<string | null>(null)
  const [installing, setInstalling] = useState(false)
  // Bumped by every open/close so a slower earlier request cannot paint its
  // rows under a device the operator has since selected.
  const patchReqRef = useRef(0)
  const [actionMsg, setActionMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(
    null
  )
  const toast = useToast()
  const { t } = useT()

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      // A summary failure must not read as a healthy fleet: the old code
      // substituted all-zeroes, which rendered four green KPI cards for a
      // server that could not answer.
      const [sumRes, devRes] = await Promise.allSettled([
        api.getPatchSummary(),
        api.getDevices(100, 0),
      ])
      if (sumRes.status === 'fulfilled') setSummary(sumRes.value)
      else setSummary(null)
      if (devRes.status === 'fulfilled') setDevices(devRes.value.devices || [])
      if (sumRes.status === 'rejected' && devRes.status === 'rejected') {
        const err = (devRes as PromiseRejectedResult).reason
        setActionMsg({
          type: 'error',
          text: err instanceof Error ? err.message : t('patches.load_failed'),
        })
      }
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  const closeDetail = useCallback(() => {
    patchReqRef.current++
    setSelectedDevice(null)
    setDevicePatches([])
    setPatchesError(null)
  }, [])

  const handleOpenPatches = async (device: DeviceDTO) => {
    // Clicking a second device before the first fetch lands would let the slow
    // response paint device A's manifest under device B's name, and "Install
    // All" would then dispatch A's patches to B. The generation counter drops
    // any response that is no longer the newest request.
    const req = ++patchReqRef.current
    setSelectedDevice(device)
    setPatchesLoading(true)
    setPatchesError(null)
    setDevicePatches([])
    try {
      // The server returns a bare array, not a {patches} envelope, and is
      // asked only for 'missing' so installed rows never read as pending work.
      const rows = await api.getDevicePatches(device.id)
      if (req !== patchReqRef.current) return
      setDevicePatches(rows)
    } catch (err: unknown) {
      if (req !== patchReqRef.current) return
      // An empty list here would read as "fully compliant", which is the
      // opposite of what a failed fetch means.
      setPatchesError(
        err instanceof Error ? err.message : t('patches.device_load_failed')
      )
    } finally {
      // Only the newest request owns the spinner.
      if (req === patchReqRef.current) setPatchesLoading(false)
    }
  }

  // The empty-state panel claims compliance, so it has to know when the claim
  // was earned. Null here means the device has never reported a scan at all, and
  // the panel says that instead of calling it compliant.
  const lastScanAt = selectedDevice?.last_patch_scan_at ?? null

  const handleScan = async (deviceId: string) => {
    setScanningId(deviceId)
    try {
      await api.scanDevicePatches(deviceId)
      const text = t('patches.scan_dispatched')
      setActionMsg({ type: 'success', text })
      toast.info(text, t('patches.scan_triggered'))
      setTimeout(loadData, 2000)
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : t('patches.scan_failed')
      setActionMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, t('patches.scan_failed_title'))
    } finally {
      setScanningId(null)
    }
  }

  const handleInstallAll = async (deviceId: string) => {
    // The agent matches on the vendor-facing identifier — the KB article on
    // Windows, the package name on Linux — and not on our row UUID. Sending
    // `p.id` would dispatch a job that silently installs nothing.
    const patchIds = devicePatches.map((p) => p.kb_id || p.patch_id).filter(Boolean)
    if (patchIds.length === 0) {
      setActionMsg({
        type: 'error',
        text: t('patches.no_identifier'),
      })
      return
    }
    setInstalling(true)
    try {
      // 'no_reboot', not 'suppress': the server's vocabulary is exactly
      // RebootPolicyNoReboot / RebootPolicyRebootIfNeeded (model.go:32-33) and
      // does not validate the field, so the old value was persisted verbatim,
      // written into the audit trail, and forwarded to the agent as garbage.
      await api.installDevicePatches(deviceId, patchIds, 'no_reboot')
      const msg = t('patches.install_dispatched', { count: patchIds.length })
      setActionMsg({ type: 'success', text: msg })
      toast.success(msg, t('patches.rollout_started'))
      closeDetail()
      setTimeout(loadData, 2000)
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : t('patches.install_failed')
      setActionMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, t('patches.install_failed_title'))
    } finally {
      setInstalling(false)
    }
  }

  const filteredDevices = devices.filter((d) => {
    const term = searchTerm.toLowerCase()
    if (!term) return true
    return (
      d.hostname.toLowerCase().includes(term) ||
      (d.site || '').toLowerCase().includes(term) ||
      d.os_name.toLowerCase().includes(term)
    )
  })

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('patches.title')}</h2>
          <p className="page-subtitle">
            {t('patches.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={loadData}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'animate-spin' : ''} />
            <span>{t('patches.refresh')}</span>
          </button>
        </div>
      </div>

      {actionMsg && (
        <div
          className={`alert-banner ${actionMsg.type === 'error' ? 'alert-error' : 'alert-success'}`}
          role="status"
        >
          <span>{actionMsg.text}</span>
          <button
            type="button"
            onClick={() => setActionMsg(null)}
            className="close-btn"
            aria-label={t('patches.dismiss_notification')}
          >
            <X size={14} />
          </button>
        </div>
      )}

      {/* KPI Cards */}
      {summary ? (
        <div className="kpi-grid">
          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('patches.kpi_vulnerable')}</span>
              <ShieldAlert className="kpi-icon text-danger" size={20} />
            </div>
            <div className="kpi-value text-danger">{summary.vulnerable_devices}</div>
            <span className="kpi-hint">{t('patches.kpi_vulnerable_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('patches.kpi_missing')}</span>
              <Clock className="kpi-icon text-warning" size={20} />
            </div>
            <div className="kpi-value text-warning">{summary.total_missing_patches}</div>
            <span className="kpi-hint">{t('patches.kpi_missing_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('patches.kpi_critical')}</span>
              <ShieldCheck className="kpi-icon text-danger" size={20} />
            </div>
            <div className="kpi-value text-danger">{summary.critical_security_patches}</div>
            <span className="kpi-hint">{t('patches.kpi_critical_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('patches.kpi_reboot')}</span>
              <RotateCcw className="kpi-icon text-primary" size={20} />
            </div>
            <div className="kpi-value">{summary.reboot_pending_devices}</div>
            <span className="kpi-hint">{t('patches.kpi_reboot_hint')}</span>
          </div>
        </div>
      ) : (
        !loading && (
          <div className="alert-banner alert-error" role="status">
            <ShieldAlert size={18} />
            <span>{t('patches.summary_unavailable')}</span>
          </div>
        )
      )}

      {/* Fleet Patch Table */}
      <div className="table-card">
        <div className="table-toolbar">
          <div className="search-wrap">
            <Search size={16} className="search-icon" aria-hidden="true" />
            <input
              type="text"
              className="search-input"
              placeholder={t('patches.filter_placeholder')}
              aria-label={t('patches.filter_label')}
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
            />
          </div>
        </div>

        <DataTable label={t('patches.devices_table_label')}>
          <thead>
            <tr>
              <th>{t('patches.col_hostname')}</th>
              <th>{t('patches.col_os')}</th>
              <th>{t('patches.col_site')}</th>
              <th>{t('patches.col_status')}</th>
              <th>{t('patches.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {filteredDevices.length === 0 ? (
              <tr>
                <td colSpan={5} className="text-center py-8 text-muted">
                  {t('patches.no_devices')}
                </td>
              </tr>
            ) : (
              filteredDevices.map((d) => (
                <tr key={d.id}>
                  <td data-label={t('patches.col_hostname')}>
                    <div className="device-host-cell">
                      <span className="host-name">{d.hostname}</span>
                      <span className="device-id-sub font-mono">{d.id.slice(0, 12)}...</span>
                    </div>
                  </td>
                  <td data-label={t('patches.col_os')}>
                    <span className="os-badge">
                      {d.os_name} {d.os_version}
                    </span>
                  </td>
                  <td data-label={t('patches.col_site')}>{d.site || t('patches.default_site')}</td>
                  <td data-label={t('patches.col_status')}>
                    <span className={`status-pill ${d.status === 'online' ? 'online' : 'offline'}`}>
                      {d.status}
                    </span>
                  </td>
                  <td data-label={t('patches.col_actions')}>
                    <div className="action-buttons">
                      <button
                        type="button"
                        className="btn-action"
                        onClick={() => handleOpenPatches(d)}
                        title={t('patches.view_patches_title')}
                      >
                        <ShieldCheck size={15} />
                        <span>{t('patches.view_patches')}</span>
                      </button>
                      {canAct && (
                        <button
                          type="button"
                          className="btn-action"
                          onClick={() => handleScan(d.id)}
                          disabled={scanningId === d.id || d.status !== 'online'}
                          title={t('patches.scan_title')}
                        >
                          <RefreshCw
                            size={14}
                            className={scanningId === d.id ? 'animate-spin' : ''}
                          />
                          <span>{t('patches.scan')}</span>
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </DataTable>
      </div>

      {/* Device Patch Detail Modal — the shared <dialog> handles focus, Escape
          and the backdrop, so this no longer needs its own overlay plumbing. */}
      <Modal
        open={Boolean(selectedDevice)}
        onClose={closeDetail}
        title={selectedDevice ? t('patches.assessment_title', { hostname: selectedDevice.hostname }) : ''}
        description={
          selectedDevice
            ? t('patches.assessment_description', {
                os: `${selectedDevice.os_name} ${selectedDevice.os_version}`,
                site: selectedDevice.site || t('patches.default'),
              })
            : undefined
        }
        size="lg"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={closeDetail}>
              {t('common.close')}
            </button>
            {canAct && devicePatches.length > 0 && selectedDevice?.status === 'online' && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => handleInstallAll(selectedDevice.id)}
                disabled={installing}
              >
                <Play size={16} />
                <span>{installing ? t('patches.dispatching') : t('patches.install_all')}</span>
              </button>
            )}
          </>
        }
      >
        {patchesLoading ? (
          <div className="py-8 text-center text-muted">
            <div className="spinner-inline"></div> {t('patches.loading_manifest')}
          </div>
        ) : patchesError ? (
          <div className="py-8 text-center">
            <ShieldAlert size={40} className="text-danger mx-auto mb-2" />
            <h4 className="text-danger font-semibold">{t('patches.could_not_load')}</h4>
            <p className="text-muted text-sm mt-1">{patchesError}</p>
          </div>
        ) : devicePatches.length === 0 ? (
          <div className="py-8 text-center">
            {lastScanAt ? (
              <>
                <CheckCircle2 size={40} className="text-success mx-auto mb-2" />
                <h4 className="text-success font-semibold">{t('patches.no_pending')}</h4>
                <p className="text-muted text-sm mt-1">
                  {t('patches.no_pending_body', { time: formatScanTime(lastScanAt, t) })}
                </p>
              </>
            ) : (
              <>
                <ShieldAlert size={40} className="text-warning mx-auto mb-2" />
                <h4 className="text-warning font-semibold">{t('patches.never_scanned')}</h4>
                <p className="text-muted text-sm mt-1">
                  {t('patches.never_scanned_body')}
                </p>
              </>
            )}
          </div>
        ) : (
          <div className="patch-list">
            <div className="patch-list-header text-sm text-muted mb-2">
              {t('patches.found_updates', { count: devicePatches.length })}
            </div>
            {devicePatches.some((p) => p.category === 'driver') && (
              <div className="alert-banner alert-error">
                <strong>{t('patches.driver_warning_title')}</strong>{' '}
                {t('patches.driver_warning_body')}
              </div>
            )}
            <DataTable label={t('patches.patches_table_label', { hostname: selectedDevice?.hostname ?? t('patches.device') })}>
              <thead>
                <tr>
                  <th>{t('patches.col_kb')}</th>
                  <th>{t('patches.col_title')}</th>
                  <th>{t('patches.col_severity')}</th>
                  <th>{t('patches.col_category')}</th>
                  <th>{t('patches.col_reboot')}</th>
                </tr>
              </thead>
              <tbody>
                {devicePatches.map((p) => (
                  <tr key={p.id}>
                    <td data-label={t('patches.col_kb')} className="font-mono text-sm">
                      {p.kb_id || p.patch_id}
                    </td>
                    <td data-label={t('patches.col_title')}>{p.title}</td>
                    <td data-label={t('patches.col_severity')}>
                      <span className={`badge-severity ${p.severity?.toLowerCase() || 'medium'}`}>
                        {p.severity || t('patches.severity_important')}
                      </span>
                    </td>
                    <td data-label={t('patches.col_category')}>
                      {/* A driver carries no KB article — its identifier is the
                          WUA UpdateID GUID, shown in the first column — and it is
                          firmware rather than a security fix, so it must not be
                          counted as one. */}
                      {p.category === 'driver' ? (
                        <span className="badge-severity medium">{t('patches.driver_firmware')}</span>
                      ) : (
                        p.category || t('patches.security_update')
                      )}
                    </td>
                    <td data-label={t('patches.col_reboot')}>
                      {p.reboot_required ? (
                        <span className="badge-severity critical">
                          <RotateCcw size={12} /> {t('patches.reboot_required')}
                        </span>
                      ) : (
                        <span className="text-muted text-sm">{t('patches.reboot_no')}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </DataTable>
          </div>
        )}
      </Modal>
    </div>
  )
}
