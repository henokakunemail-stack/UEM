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

// An absolute date, not "3d ago". The empty-state panel is a compliance claim
// and "3d ago" invites reading it as current; an auditor needs the date itself.
function formatScanTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return 'an unrecorded time'
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
          text: err instanceof Error ? err.message : 'Failed to load patch data',
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
        err instanceof Error ? err.message : 'Failed to load patches for this device'
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
      const text = 'Patch scan dispatched to device'
      setActionMsg({ type: 'success', text })
      toast.info(text, 'Scan Triggered')
      setTimeout(loadData, 2000)
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : 'Scan failed'
      setActionMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, 'Scan Failed')
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
        text: 'These patches have no KB or package identifier to install by',
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
      const msg = `Install dispatched for ${patchIds.length} patches`
      setActionMsg({ type: 'success', text: msg })
      toast.success(msg, 'Rollout Started')
      closeDetail()
      setTimeout(loadData, 2000)
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : 'Install failed'
      setActionMsg({ type: 'error', text: errMsg })
      toast.error(errMsg, 'Install Failed')
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
          <h2 className="page-title">Patch Management &amp; Compliance</h2>
          <p className="page-subtitle">
            Enterprise OS patch monitoring, vulnerability remediation, and scheduled update rollouts
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
            <span>Refresh</span>
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
            aria-label="Dismiss notification"
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
              <span className="kpi-label">Vulnerable Devices</span>
              <ShieldAlert className="kpi-icon text-danger" size={20} />
            </div>
            <div className="kpi-value text-danger">{summary.vulnerable_devices}</div>
            <span className="kpi-hint">Devices with at least one missing update</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Missing Patches</span>
              <Clock className="kpi-icon text-warning" size={20} />
            </div>
            <div className="kpi-value text-warning">{summary.total_missing_patches}</div>
            <span className="kpi-hint">Individual updates awaiting install</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Critical / Security</span>
              <ShieldCheck className="kpi-icon text-danger" size={20} />
            </div>
            <div className="kpi-value text-danger">{summary.critical_security_patches}</div>
            <span className="kpi-hint">High priority CVE remediations</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Reboot Pending</span>
              <RotateCcw className="kpi-icon text-primary" size={20} />
            </div>
            <div className="kpi-value">{summary.reboot_pending_devices}</div>
            <span className="kpi-hint">Devices needing a restart to finalize</span>
          </div>
        </div>
      ) : (
        !loading && (
          <div className="alert-banner alert-error" role="status">
            <ShieldAlert size={18} />
            <span>
              Patch summary is unavailable — these figures are not a zero-patch fleet. Reload to
              retry.
            </span>
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
              placeholder="Filter the loaded devices by hostname, site, or OS…"
              aria-label="Filter loaded devices"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
            />
          </div>
        </div>

        <DataTable label="Devices and their patch actions">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>OS &amp; Architecture</th>
              <th>Branch Site</th>
              <th>Status</th>
              <th>Patch Actions</th>
            </tr>
          </thead>
          <tbody>
            {filteredDevices.length === 0 ? (
              <tr>
                <td colSpan={5} className="text-center py-8 text-muted">
                  No devices matching the current filter.
                </td>
              </tr>
            ) : (
              filteredDevices.map((d) => (
                <tr key={d.id}>
                  <td data-label="Hostname">
                    <div className="device-host-cell">
                      <span className="host-name">{d.hostname}</span>
                      <span className="device-id-sub font-mono">{d.id.slice(0, 12)}...</span>
                    </div>
                  </td>
                  <td data-label="OS & Architecture">
                    <span className="os-badge">
                      {d.os_name} {d.os_version}
                    </span>
                  </td>
                  <td data-label="Branch Site">{d.site || 'Default Site'}</td>
                  <td data-label="Status">
                    <span className={`status-pill ${d.status === 'online' ? 'online' : 'offline'}`}>
                      {d.status}
                    </span>
                  </td>
                  <td data-label="Patch Actions">
                    <div className="action-buttons">
                      <button
                        type="button"
                        className="btn-action"
                        onClick={() => handleOpenPatches(d)}
                        title="View available patches"
                      >
                        <ShieldCheck size={15} />
                        <span>View Patches</span>
                      </button>
                      {canAct && (
                        <button
                          type="button"
                          className="btn-action"
                          onClick={() => handleScan(d.id)}
                          disabled={scanningId === d.id || d.status !== 'online'}
                          title="Trigger WUA / Package Manager Scan"
                        >
                          <RefreshCw
                            size={14}
                            className={scanningId === d.id ? 'animate-spin' : ''}
                          />
                          <span>Scan</span>
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
        title={selectedDevice ? `Patch Assessment: ${selectedDevice.hostname}` : ''}
        description={
          selectedDevice
            ? `${selectedDevice.os_name} ${selectedDevice.os_version} — Site: ${selectedDevice.site || 'Default'}`
            : undefined
        }
        size="lg"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={closeDetail}>
              Close
            </button>
            {canAct && devicePatches.length > 0 && selectedDevice?.status === 'online' && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => handleInstallAll(selectedDevice.id)}
                disabled={installing}
              >
                <Play size={16} />
                <span>{installing ? 'Dispatching…' : 'Install All Patches'}</span>
              </button>
            )}
          </>
        }
      >
        {patchesLoading ? (
          <div className="py-8 text-center text-muted">
            <div className="spinner-inline"></div> Loading patch manifest...
          </div>
        ) : patchesError ? (
          <div className="py-8 text-center">
            <ShieldAlert size={40} className="text-danger mx-auto mb-2" />
            <h4 className="text-danger font-semibold">Could not load patches</h4>
            <p className="text-muted text-sm mt-1">{patchesError}</p>
          </div>
        ) : devicePatches.length === 0 ? (
          <div className="py-8 text-center">
            {lastScanAt ? (
              <>
                <CheckCircle2 size={40} className="text-success mx-auto mb-2" />
                <h4 className="text-success font-semibold">No pending updates</h4>
                <p className="text-muted text-sm mt-1">
                  This device reported nothing outstanding as of its last scan
                  ({formatScanTime(lastScanAt)}). That is what Windows Update and the
                  package manager had on offer then, not a standing promise — scan again
                  after the next update cycle.
                </p>
              </>
            ) : (
              <>
                <ShieldAlert size={40} className="text-warning mx-auto mb-2" />
                <h4 className="text-warning font-semibold">Never scanned</h4>
                <p className="text-muted text-sm mt-1">
                  This device has never reported a patch scan, so nothing is known about
                  what it is missing. An empty list here means nobody has looked, not that
                  the machine is up to date.
                </p>
              </>
            )}
          </div>
        ) : (
          <div className="patch-list">
            <div className="patch-list-header text-sm text-muted mb-2">
              Found {devicePatches.length} available updates
            </div>
            {devicePatches.some((p) => p.category === 'driver') && (
              <div className="alert-banner alert-error">
                <strong>This list includes driver and firmware updates.</strong>{' '}
                Windows Update offers those here too, and Install All will push all of
                them. A graphics driver is the one to watch: if the machine goes dark
                after the install, that is the likely cause, and a restart normally
                brings it back. Windows may also require a restart before a driver
                install can proceed at all.
              </div>
            )}
            <DataTable label={`Patches for ${selectedDevice?.hostname ?? 'device'}`}>
              <thead>
                <tr>
                  <th>KB / Reference</th>
                  <th>Title</th>
                  <th>Severity</th>
                  <th>Category</th>
                  <th>Reboot</th>
                </tr>
              </thead>
              <tbody>
                {devicePatches.map((p) => (
                  <tr key={p.id}>
                    <td data-label="KB / Reference" className="font-mono text-sm">
                      {p.kb_id || p.patch_id}
                    </td>
                    <td data-label="Title">{p.title}</td>
                    <td data-label="Severity">
                      <span className={`badge-severity ${p.severity?.toLowerCase() || 'medium'}`}>
                        {p.severity || 'Important'}
                      </span>
                    </td>
                    <td data-label="Category">
                      {/* A driver carries no KB article — its identifier is the
                          WUA UpdateID GUID, shown in the first column — and it is
                          firmware rather than a security fix, so it must not be
                          counted as one. */}
                      {p.category === 'driver' ? (
                        <span className="badge-severity medium">Driver / Firmware</span>
                      ) : (
                        p.category || 'Security Update'
                      )}
                    </td>
                    <td data-label="Reboot">
                      {p.reboot_required ? (
                        <span className="badge-severity critical">
                          <RotateCcw size={12} /> Required
                        </span>
                      ) : (
                        <span className="text-muted text-sm">No</span>
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
