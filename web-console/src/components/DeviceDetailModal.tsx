import React, { useEffect, useState } from 'react'
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

export const DeviceDetailModal: React.FC<DeviceDetailModalProps> = ({ device, onClose }) => {
  const [inventory, setInventory] = useState<DeviceInventorySnapshot | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeTab, setActiveTab] = useState<'hardware' | 'software' | 'network'>('hardware')
  const [softwareSearch, setSoftwareSearch] = useState('')
  const [actionMsg, setActionMsg] = useState<string | null>(null)
  const [collecting, setCollecting] = useState(false)
  const [uninstallTarget, setUninstallTarget] = useState<SoftwareInfo | null>(null)
  const [uninstalling, setUninstalling] = useState(false)
  const canUninstall = usePermission().can('technician')

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

  if (!device) return null

  const handleCollect = async () => {
    setCollecting(true)
    setActionMsg(null)
    try {
      await api.collectInventory(device.id)
      setActionMsg('Collection request dispatched. Snapshot will refresh shortly.')
      // Refresh inventory snapshot after 3 seconds
      setTimeout(async () => {
        const updated = await api.getDeviceInventory(device.id).catch(() => null)
        if (updated) setInventory(updated)
        setCollecting(false)
      }, 3000)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Action failed'
      setActionMsg(`Failed: ${msg}`)
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

  // Submitting an uninstall deliberately does NOT refresh the inventory.
  //
  // The snapshot only changes once the agent runs its next collection, and the
  // uninstall has not even started yet -- the server answered when the command
  // was handed over, not when anything was removed. Refetching here would
  // redraw the identical row and read as "nothing happened" or, worse, invite
  // pressing the button again.
  const handleUninstall = async () => {
    if (!uninstallTarget) return
    const name = uninstallTarget.name
    setUninstalling(true)
    try {
      await api.uninstallDeviceSoftware(device.id, name)
      setActionMsg(
        `Uninstall request for "${name}" was sent to ${device.hostname}. ` +
          `The agent runs it silently and refuses if no verified silent command exists. ` +
          `The request was accepted for delivery, which is not a removal: use Refresh ` +
          `to collect a new inventory, and the program will only be gone from this ` +
          `list if the agent actually removed it.`
      )
      setUninstallTarget(null)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Request failed'
      setActionMsg(`Uninstall of "${name}" was not sent: ${msg}`)
    } finally {
      setUninstalling(false)
    }
  }

  const hw = inventory?.hw || inventory?.hardware
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
              ? new Date(inventory.collected_at).toLocaleString()
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
            onClick={handleCollect}
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
              onClick={handleCollect}
              disabled={device.status !== 'online'}
            >
              Trigger On-Demand Collection
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
                        filteredSoftware.map((s, idx) => (
                          <tr key={`${s.name}-${idx}`}>
                            <td>
                              <strong>{s.name}</strong>
                            </td>
                            <td>{s.version || '—'}</td>
                            <td>{s.publisher || '—'}</td>
                            {canUninstall && (
                              <td className="text-right">
                                <button
                                  type="button"
                                  className="btn btn-danger-outline btn-sm"
                                  aria-label={`Uninstall ${s.name}`}
                                  disabled={device.status !== 'online'}
                                  title={
                                    device.status !== 'online'
                                      ? 'Endpoint is offline'
                                      : 'Request silent uninstall'
                                  }
                                  onClick={() => setUninstallTarget(s)}
                                >
                                  <Trash2 size={15} />
                                </button>
                              </td>
                            )}
                          </tr>
                        ))
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
          if (!uninstalling) setUninstallTarget(null)
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
              onClick={() => setUninstallTarget(null)}
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
          refuse once it receives it. Use Refresh afterwards to collect a new
          inventory -- the row disappears only if the program really was
          removed.
        </p>
      </Modal>
    </Modal>
  )
}
