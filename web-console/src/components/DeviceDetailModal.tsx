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
} from 'lucide-react'
import { api } from '../services/api'
import { DataTable, Modal } from './ui'
import type { DeviceDTO, DeviceInventorySnapshot } from '../types/api'

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
                      </tr>
                    </thead>
                    <tbody>
                      {filteredSoftware.length === 0 ? (
                        <tr>
                          <td colSpan={3} className="text-center py-4">
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
    </Modal>
  )
}
