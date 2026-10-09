import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  Activity,
  Cpu,
  Database,
  HardDrive,
  Info,
  Layers,
  Network,
  RefreshCw,
  Search,
  Server,
  Trash2,
} from 'lucide-react'
import { api } from '../services/api'
import { usePermission } from '../hooks/usePermission'
import { DataTable, Modal } from './ui'
import type { DeviceDTO, DeviceInventorySnapshot, SoftwareInfo } from '../types/api'

interface DeviceDetailModalProps {
  device: DeviceDTO | null
  onClose: () => void
}

// How often to ask whether a new snapshot has landed, and how long to keep
// asking. Both are deliberate: the old code made exactly one refetch three
// seconds after the click, which is a guess, and a guess that silently returns
// the snapshot you already had whenever the agent is slower than three seconds.
const COLLECT_POLL_MS = 2000
const COLLECT_TIMEOUT_TICKS = 30

// Past this age the snapshot is no longer a description of the machine, and the
// operator should be told so in the same breath as the timestamp. Without it the
// only way to tell a fresh list from a two-day-old one is to read the date and
// do arithmetic.
const SNAPSHOT_STALE_AFTER_MS = 60 * 60 * 1000

// formatAge renders an elapsed duration in the largest unit that still reads as
// a whole number, because "5 minutes ago" is a fact an operator can act on and
// "312,000 milliseconds ago" is not. A snapshot whose clock is behind ours
// (agent clock skew, a timezone written into the timestamp) produces a negative
// age; it is reported as "just now" rather than as a negative number, since
// being unable to measure the age is not evidence the data is old.
function formatAge(ms: number): string {
  if (ms < 0) return 'just now'
  const minutes = Math.floor(ms / 60000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  const days = Math.floor(hours / 24)
  return `${days} day${days === 1 ? '' : 's'} ago`
}

export const DeviceDetailModal: React.FC<DeviceDetailModalProps> = ({ device, onClose }) => {
  const [inventory, setInventory] = useState<DeviceInventorySnapshot | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeTab, setActiveTab] = useState<'hardware' | 'software' | 'network'>('hardware')
  const [softwareSearch, setSoftwareSearch] = useState('')
  const [actionMsg, setActionMsg] = useState<string | null>(null)
  const [uninstallTarget, setUninstallTarget] = useState<SoftwareInfo | null>(null)
  const [uninstalling, setUninstalling] = useState(false)
  const canUninstall = usePermission().can('technician')

  const [activeUninstall, setActiveUninstall] = useState<{
    name: string
    status: 'sending' | 'running' | 'done' | 'failed'
    message?: string
  } | null>(null)
  const [confirmError, setConfirmError] = useState<string | null>(null)

  // The collect poll, and the collected_at it is waiting for. See handleCollect:
  // the snapshot's own timestamp is the only completion signal that cannot be
  // faked by a clock, because it only moves when the agent uploads a new one.
  const pollRef = useRef<number | null>(null)
  const [collecting, setCollecting] = useState(false)

  const uninstallPollRef = useRef<number | null>(null)
  const graceTimerRef = useRef<number | null>(null)

  // Idempotent: called from the poll body on success, on the timeout, from the
  // cleanup effect, and from the error path. Clearing an already-cleared
  // interval is harmless, but nulling the ref twice is what makes a second
  // clear a no-op instead of a leak.
  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  const stopUninstallPolling = useCallback(() => {
    if (uninstallPollRef.current !== null) {
      window.clearInterval(uninstallPollRef.current)
      uninstallPollRef.current = null
    }
    if (graceTimerRef.current !== null) {
      window.clearTimeout(graceTimerRef.current)
      graceTimerRef.current = null
    }
  }, [])

  useEffect(() => {
    if (!device) return
    let active = true
    setLoading(true)
    api
      .getDeviceInventory(device.id)
      .then((inv) => {
        if (active) setInventory(inv)
      })
      .catch(() => {
        if (active) setInventory(null)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [device])

  // Teardown in its own effect, separate from the load effect above: a cleanup
  // folded into that one would run on every re-render of its dependencies and
  // kill the poll the click handler had just armed. It has to sit above the
  // `if (!device) return null` below, because a hook after an early return is
  // conditional, and React will refuse to render the modal at all once it sees
  // the hook count change between renders.
  useEffect(() => {
    return () => {
      stopPolling()
      stopUninstallPolling()
    }
  }, [stopPolling, stopUninstallPolling])

  if (!device) return null

  const handleCollect = async (opts?: { preserveBanner?: boolean } | React.MouseEvent) => {
    const preserveBanner =
      opts && 'preserveBanner' in opts ? Boolean(opts.preserveBanner) : false
    const deviceId = device.id
    // Read the timestamp BEFORE asking for a collection. Anything that arrives
    // afterwards with this value unchanged is the snapshot we already had, and
    // waiting for it to differ is what proves the agent did the work rather
    // than the clock running.
    const before = inventory?.collected_at ?? null

    setCollecting(true)
    if (!preserveBanner) {
      setActionMsg(null)
    }
    try {
      const res = await api.collectInventory(deviceId)
      // 202 queued means the endpoint was offline between render and click. The
      // server accepted the request but nothing will run until the agent next
      // reports, so a spinner here would be waiting for an event this device
      // cannot produce yet.
      if (res.status === 'queued') {
        setActionMsg(
          'The endpoint is offline. The collection is queued and will run on its next report.'
        )
        setCollecting(false)
        return
      }

      if (!preserveBanner) {
        setActionMsg('Collection dispatched. Waiting for the agent to report a new snapshot...')
      }

      let ticks = 0
      stopPolling()
      pollRef.current = window.setInterval(async () => {
        ticks++
        // The bound is a real deadline, not a formality: a device that accepts
        // the request and never reports must not leave the operator watching a
        // spinner forever, because "still waiting" and "will never finish" look
        // identical without it.
        if (ticks >= COLLECT_TIMEOUT_TICKS) {
          stopPolling()
          setCollecting(false)
          setActionMsg(
            'The endpoint accepted the collection but has not reported a new snapshot yet. ' +
              'It may be offline, or the agent may still be scanning. Use Collect Inventory again later.'
          )
          return
        }

        const updated = await api.getDeviceInventory(deviceId).catch(() => null)
        // A failed tick is not a failed collection. Swallowing it here is what
        // the old code did for the only fetch it ever made, and that is how a
        // 500 looked identical to success. If the network is genuinely gone the
        // ticks run out above and say so.
        if (!updated) return

        // before === null means the device has never reported a snapshot, so
        // there is no earlier timestamp to compare against: the arrival of any
        // snapshot is the event.
        const arrived = before === null ? true : updated.collected_at !== before
        setInventory(updated)
        if (!arrived) return

        stopPolling()
        setCollecting(false)
        setActiveUninstall((current) => {
          if (!current) return null
          const stillThere = updated.software.some(
            (s) => s.name.toLowerCase() === current.name.toLowerCase()
          )
          if (stillThere) {
            return {
              name: current.name,
              status: 'failed',
              message: `Uninstall finished, but "${current.name}" was still detected in device inventory. Software may still be present on endpoint.`,
            }
          }
          return {
            name: current.name,
            status: 'done',
            message: `"${current.name}" was successfully removed from device inventory.`,
          }
        })
        setActionMsg(
          `Snapshot collected: ${updated.software.length} program(s) reported at ` +
            `${new Date(updated.collected_at).toLocaleTimeString()}.`
        )
      }, COLLECT_POLL_MS)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Action failed'
      setActionMsg(`Collection request failed: ${msg}`)
      setCollecting(false)
    }
  }

  const handlePing = async () => {
    setActionMsg(null)
    try {
      const res = await api.pingDevice(device.id)
      setActionMsg(`Ping response: ${res.status} (Command ID: ${res.command_id})`)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Action failed'
      setActionMsg(`Ping failed: ${msg}`)
    }
  }

  const handleUninstall = async () => {
    if (!uninstallTarget) return
    const name = uninstallTarget.name
    setUninstalling(true)
    setConfirmError(null)
    setActiveUninstall({
      name,
      status: 'sending',
      message: `Sending uninstall request for "${name}" to ${device.hostname}...`,
    })

    try {
      const res = await api.uninstallDeviceSoftware(device.id, name)
      // Success dispatch: close confirmation modal, show running status
      setUninstallTarget(null)
      setUninstalling(false)
      setActiveUninstall({
        name,
        status: 'running',
        message: `Uninstall request for "${name}" sent to ${device.hostname}. Running silent uninstaller...`,
      })

      // Poll command result to display verified outcome to operator
      const cmdId = res.command_id
      let attempts = 0
      const maxAttempts = 20

      stopUninstallPolling()
      uninstallPollRef.current = window.setInterval(async () => {
        attempts++
        try {
          const cmd = await api.getDeviceCommand(device.id, cmdId)
          if (cmd.status === 'done' || cmd.status === 'failed') {
            stopUninstallPolling()
            let resultText = ''
            if (cmd.result) {
              try {
                const parsed = JSON.parse(cmd.result)
                resultText = parsed.result || cmd.result
              } catch {
                resultText = cmd.result
              }
            }
            if (cmd.status === 'done') {
              setActiveUninstall({
                name,
                status: 'done',
                message: `Uninstall completed: ${resultText || name}`,
              })
              // Wait 2s grace delay for Windows registry cleanup before re-collecting
              graceTimerRef.current = window.setTimeout(() => {
                handleCollect({ preserveBanner: true })
              }, 2000)
            } else {
              setActiveUninstall({
                name,
                status: 'failed',
                message: `Uninstall refused or failed: ${resultText || name}`,
              })
            }
          } else if (attempts >= maxAttempts) {
            stopUninstallPolling()
            setActiveUninstall({
              name,
              status: 'failed',
              message:
                `Uninstall request for "${name}" is still executing on ${device.hostname}. ` +
                `Use Collect Inventory to refresh the software list once completed.`,
            })
          }
        } catch {
          if (attempts >= maxAttempts) {
            stopUninstallPolling()
            setActiveUninstall({
              name,
              status: 'failed',
              message: `Failed to query uninstall status for "${name}".`,
            })
          }
        }
      }, 1500)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Request failed'
      setConfirmError(`Uninstall of "${name}" was not sent: ${msg}`)
      setActiveUninstall(null)
      setUninstalling(false)
    }
  }

  const hw = inventory?.hw || inventory?.hardware
  const snapshotAge = inventory?.collected_at
    ? Date.now() - new Date(inventory.collected_at).getTime()
    : null
  const snapshotIsStale = snapshotAge !== null && snapshotAge > SNAPSHOT_STALE_AFTER_MS
  const ramBytes = hw?.ram_total_bytes || inventory?.ram_bytes || inventory?.hw_ram_bytes
  const ramGB = ramBytes ? (ramBytes / (1024 * 1024 * 1024)).toFixed(1) : null

  const softwareList = inventory?.software || []
  const filteredSoftware = softwareList.filter(
    (s) =>
      s.name.toLowerCase().includes(softwareSearch.toLowerCase()) ||
      (s.publisher && s.publisher.toLowerCase().includes(softwareSearch.toLowerCase()))
  )

  return (
    <Modal
      open={device !== null}
      onClose={onClose}
      size="lg"
      title={
        <span className="modal-title-group">
          <Server size={22} className="modal-icon" />
          {device.hostname}
        </span>
      }
      description={`ID: ${device.id} • ${device.site || 'HQ'} • OS: ${device.os_name} ${device.os_version}`}
      footer={
        <>
          <span className="snapshot-timestamp">
            Last Inventory Snapshot:{' '}
            {inventory?.collected_at
              ? `${new Date(inventory.collected_at).toLocaleString()} (${formatAge(snapshotAge!)}${
                  snapshotIsStale ? ' — this list may be out of date' : ''
                })`
              : 'Never'}
          </span>
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Close
          </button>
        </>
      }
    >
      {actionMsg && (
        <div className="action-notification" role="status">
          <Info size={16} />
          <span>{actionMsg}</span>
        </div>
      )}

      <div className="device-overview-bar">
        <div className="overview-item">
          <span className="overview-label">STATUS</span>
          <span className={`status-pill ${device.status}`}>{device.status.toUpperCase()}</span>
        </div>
        <div className="overview-item">
          <span className="overview-label">AGENT VERSION</span>
          <span className="overview-val">{device.agent_version || '0.1.0'}</span>
        </div>
        <div className="overview-item">
          <span className="overview-label">LAST SEEN</span>
          <span className="overview-val">
            {device.last_seen_at ? new Date(device.last_seen_at).toLocaleTimeString() : 'Never'}
          </span>
        </div>
        <div className="overview-item actions">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={handlePing}
            disabled={device.status !== 'online'}
          >
            <Activity size={14} />
            <span>Ping</span>
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => handleCollect()}
            disabled={collecting || device.status !== 'online'}
          >
            <RefreshCw size={14} className={collecting ? 'spinning' : ''} />
            <span>{collecting ? 'Collecting...' : 'Collect Inventory'}</span>
          </button>
        </div>
      </div>

      <div className="modal-tabs" role="tablist" aria-label="Device inventory">
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'hardware'}
          className={`tab-btn ${activeTab === 'hardware' ? 'active' : ''}`}
          onClick={() => setActiveTab('hardware')}
        >
          <Cpu size={16} />
          <span>Hardware Specs</span>
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'software'}
          className={`tab-btn ${activeTab === 'software' ? 'active' : ''}`}
          onClick={() => setActiveTab('software')}
        >
          <Layers size={16} />
          <span>Installed Software ({softwareList.length})</span>
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'network'}
          className={`tab-btn ${activeTab === 'network' ? 'active' : ''}`}
          onClick={() => setActiveTab('network')}
        >
          <Network size={16} />
          <span>Network & Interfaces</span>
        </button>
      </div>

      <div className="modal-content" role="tabpanel">
        {loading ? (
          <div className="loading-state">
            <RefreshCw size={24} className="spinning" />
            <span>Loading device telemetry and inventory snapshot...</span>
          </div>
        ) : !inventory ? (
          <div className="empty-state">
            <Database size={28} />
            <p>No inventory snapshot has been received from this device yet.</p>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => handleCollect()}
              disabled={collecting || device.status !== 'online'}
            >
              Collect Inventory
            </button>
          </div>
        ) : (
          <>
            {activeTab === 'hardware' && (
              <div className="tab-pane hardware-pane">
                <div className="spec-grid">
                  <div className="spec-card">
                    <div className="spec-header">
                      <Cpu size={16} />
                      <h4>Processor (CPU)</h4>
                    </div>
                    <p className="spec-val">
                      {hw?.cpu?.name || inventory.hw_cpu_model || 'Unknown CPU'}
                    </p>
                    <div className="spec-meta">
                      <span>Cores: {hw?.cpu?.number_of_cores || 'N/A'}</span>
                      <span>Logical Threads: {hw?.cpu?.logical_processors || 'N/A'}</span>
                    </div>
                  </div>

                  <div className="spec-card">
                    <div className="spec-header">
                      <Database size={16} />
                      <h4>Installed Memory (RAM)</h4>
                    </div>
                    <p className="spec-val">{ramGB ? `${ramGB} GB` : 'Not reported'}</p>
                    <div className="spec-meta">
                      <span>Query Method: CIM Win32_ComputerSystem</span>
                    </div>
                  </div>

                  <div className="spec-card">
                    <div className="spec-header">
                      <Server size={16} />
                      <h4>System Identity</h4>
                    </div>
                    <p className="spec-val">
                      {hw?.model?.vendor || 'Unknown'} {hw?.model?.product || ''}
                    </p>
                    <div className="spec-meta">
                      <span>Serial: {hw?.model?.serial_number || 'N/A'}</span>
                    </div>
                  </div>
                </div>

                <h4 className="section-title">
                  <HardDrive size={16} />
                  <span>Mounted Storage Volumes</span>
                </h4>
                <div className="disk-list">
                  {hw?.disks && hw.disks.length > 0 ? (
                    hw.disks.map((d) => {
                      const totalGB = (d.total_bytes / (1024 * 1024 * 1024)).toFixed(1)
                      const freeGB = (d.free_bytes / (1024 * 1024 * 1024)).toFixed(1)
                      const usedPct =
                        d.total_bytes > 0
                          ? Math.round(((d.total_bytes - d.free_bytes) / d.total_bytes) * 100)
                          : 0
                      return (
                        <div key={d.name} className="disk-card">
                          <div className="disk-header">
                            <strong>Volume {d.name}</strong>
                            <span>{d.filesystem || 'NTFS'}</span>
                          </div>
                          <div className="progress-bar-bg">
                            <div
                              className={`progress-bar-fill ${
                                usedPct > 85 ? 'danger' : usedPct > 70 ? 'warning' : 'primary'
                              }`}
                              style={{ width: `${usedPct}%` }}
                            ></div>
                          </div>
                          <div className="disk-footer">
                            <span>
                              Free: {freeGB} GB ({100 - usedPct}%)
                            </span>
                            <span>Total: {totalGB} GB</span>
                          </div>
                        </div>
                      )
                    })
                  ) : (
                    <p className="subtext">No disk volumes detected</p>
                  )}
                </div>
              </div>
            )}

            {activeTab === 'software' && (
              <div className="tab-pane software-pane">
                <div className="search-bar">
                  <Search size={16} className="search-icon" />
                  <input
                    id="device-software-filter"
                    type="search"
                    className="search-input"
                    aria-label="Filter installed software"
                    placeholder="Filter installed packages or applications..."
                    value={softwareSearch}
                    onChange={(e) => setSoftwareSearch(e.target.value)}
                  />
                </div>

                {activeUninstall && (
                  <div
                    className={`action-notification in-pane ${
                      activeUninstall.status === 'failed' ? 'error' : ''
                    }`}
                    role="status"
                  >
                    {activeUninstall.status === 'sending' ||
                    activeUninstall.status === 'running' ? (
                      <RefreshCw size={16} className="spinning" />
                    ) : (
                      <Info size={16} />
                    )}
                    <span>
                      {activeUninstall.message ||
                        `Uninstalling ${activeUninstall.name}...`}
                    </span>
                  </div>
                )}

                <DataTable label="Installed software">
                  <table>
                    <thead>
                      <tr>
                        <th>Application Name</th>
                        <th>Version</th>
                        <th>Publisher / Vendor</th>
                        {canUninstall && (
                          <th className="text-right">
                            <span className="visually-hidden">Actions</span>
                          </th>
                        )}
                      </tr>
                    </thead>
                    <tbody>
                      {filteredSoftware.length === 0 ? (
                        <tr>
                          <td colSpan={canUninstall ? 4 : 3} className="text-center py-4">
                            No matching software entries
                          </td>
                        </tr>
                      ) : (
                        filteredSoftware.map((s, idx) => {
                          const isTarget = activeUninstall?.name === s.name
                          const isUninstallInProgress =
                            activeUninstall?.status === 'sending' ||
                            activeUninstall?.status === 'running'

                          return (
                            <tr key={`${s.name}-${idx}`}>
                              <td>
                                <div
                                  style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '0.5rem',
                                    flexWrap: 'wrap',
                                  }}
                                >
                                  <strong>{s.name}</strong>
                                  {isTarget && (
                                    <>
                                      {isUninstallInProgress && (
                                        <span
                                          className="status-pill warning"
                                          style={{
                                            display: 'inline-flex',
                                            alignItems: 'center',
                                            gap: '0.25rem',
                                            padding: '0.125rem 0.5rem',
                                            fontSize: '0.75rem',
                                          }}
                                        >
                                          <RefreshCw size={11} className="spinning" />
                                          <span>Uninstalling...</span>
                                        </span>
                                      )}
                                      {activeUninstall.status === 'done' && (
                                        <span
                                          className="status-pill online"
                                          style={{
                                            padding: '0.125rem 0.5rem',
                                            fontSize: '0.75rem',
                                          }}
                                        >
                                          Uninstalled
                                        </span>
                                      )}
                                      {activeUninstall.status === 'failed' && (
                                        <span
                                          className="status-pill danger"
                                          style={{
                                            padding: '0.125rem 0.5rem',
                                            fontSize: '0.75rem',
                                          }}
                                        >
                                          Failed
                                        </span>
                                      )}
                                    </>
                                  )}
                                </div>
                              </td>
                              <td>{s.version || '—'}</td>
                              <td>{s.publisher || '—'}</td>
                              {canUninstall && (
                                <td className="text-right">
                                  <button
                                    type="button"
                                    className="btn btn-danger-outline btn-sm"
                                    aria-label={`Uninstall ${s.name}`}
                                    disabled={
                                      device.status !== 'online' ||
                                      isUninstallInProgress
                                    }
                                    title={
                                      device.status !== 'online'
                                        ? 'Endpoint is offline'
                                        : isUninstallInProgress
                                        ? 'Uninstall in progress'
                                        : 'Request silent uninstall'
                                    }
                                    onClick={() => {
                                      setUninstallTarget(s)
                                      setConfirmError(null)
                                    }}
                                  >
                                    {isTarget && isUninstallInProgress ? (
                                      <RefreshCw size={15} className="spinning" />
                                    ) : (
                                      <Trash2 size={15} />
                                    )}
                                  </button>
                                </td>
                              )}
                            </tr>
                          )
                        })
                      )}
                    </tbody>
                  </table>
                </DataTable>
              </div>
            )}

            {activeTab === 'network' && (
              <div className="tab-pane network-pane">
                <DataTable label="Network interfaces">
                  <table>
                    <thead>
                      <tr>
                        <th>Interface Name</th>
                        <th>Hardware MAC Address</th>
                        <th>Assigned IP Addresses</th>
                      </tr>
                    </thead>
                    <tbody>
                      {hw?.nics && hw.nics.length > 0 ? (
                        hw.nics.map((nic) => (
                          <tr key={nic.name}>
                            <td>
                              <strong>{nic.name}</strong>
                            </td>
                            <td className="font-mono">{nic.mac || '—'}</td>
                            <td>{nic.ips && nic.ips.length > 0 ? nic.ips.join(', ') : '—'}</td>
                          </tr>
                        ))
                      ) : (
                        <tr>
                          <td colSpan={3} className="text-center">
                            No network interfaces reported
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </DataTable>
              </div>
            )}
          </>
        )}
      </div>

      {/* Confirmation. There is no field here for uninstall arguments, and that
          is the point: asking an operator to type the silent switch of a program
          they are trying to remove is asking them to guess, and a wrong guess is
          what puts a window on the endpoint. The agent reads the switches off the
          endpoint's own registry or refuses. */}
      <Modal
        open={uninstallTarget !== null}
        onClose={() => {
          if (!uninstalling) {
            setUninstallTarget(null)
            setConfirmError(null)
          }
        }}
        size="md"
        title={
          <span className="modal-title-group">
            <Trash2 size={22} className="modal-icon" />
            Request uninstall
          </span>
        }
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => {
                setUninstallTarget(null)
                setConfirmError(null)
              }}
              disabled={uninstalling}
            >
              Cancel
            </button>
            <button
              type="button"
              className="btn btn-danger-outline"
              onClick={handleUninstall}
              disabled={uninstalling}
            >
              {uninstalling ? 'Sending...' : 'Send uninstall request'}
            </button>
          </>
        }
      >
        {confirmError && (
          <div
            className="action-notification error in-pane"
            role="alert"
            style={{ marginBottom: '1rem' }}
          >
            <Info size={16} />
            <span>{confirmError}</span>
          </div>
        )}
        <p>
          Send an uninstall request to{' '}
          <strong>{device.hostname}</strong> for:
        </p>
        <p className="subtext">
          <strong>{uninstallTarget?.name}</strong>
          {uninstallTarget?.version ? ` ${uninstallTarget.version}` : ''}
        </p>
        <p className="subtext">
          The agent works out the silent uninstall switches from the endpoint
          itself. If the program records no verifiable silent command, the request
          is refused and nothing is run -- no window ever opens on the endpoint.
        </p>
        <p className="subtext">
          This sends a request. It does not confirm removal: the agent can still
          refuse once it receives it. Use Collect Inventory afterwards to collect a new
          inventory -- the row disappears only if the program really was
          removed.
        </p>
      </Modal>
    </Modal>
  )
}
