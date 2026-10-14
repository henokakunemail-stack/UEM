import React, { useEffect, useState, useRef } from 'react'
import {
  ArrowUpCircle,
  FolderArchive,
  Layers,
  Play,
  RefreshCw,
  ShieldCheck,
  UploadCloud,
  X,
  FileText,
  CheckCircle2,
  XCircle,
  Monitor,
  Terminal,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import type { AgentReleaseDTO, UpdateCampaignDTO, CampaignTaskDetail, CampaignLogItem } from '../types/api'

export type UpdatesTab = 'releases' | 'campaigns'

interface AgentUpdatesPageProps {
  activeTab: UpdatesTab
  onTabChange: (tab: UpdatesTab) => void
}

export const AgentUpdatesPage: React.FC<AgentUpdatesPageProps> = ({ activeTab: subTab, onTabChange: setSubTab }) => {
  // Publishing a binary and starting a fleet-wide rollout are both RoleAdmin on
  // the server. A viewer can still read the catalog and watch campaign progress.
  const { can } = usePermission()
  const canAdmin = can('admin')
  const [releases, setReleases] = useState<AgentReleaseDTO[]>([])
  const [campaigns, setCampaigns] = useState<UpdateCampaignDTO[]>([])
  const [loading, setLoading] = useState(true)

  // Modals
  const [isReleaseModalOpen, setIsReleaseModalOpen] = useState(false)
  const [isCampaignModalOpen, setIsCampaignModalOpen] = useState(false)
  const [isDetailsModalOpen, setIsDetailsModalOpen] = useState(false)
  const [selectedCampaign, setSelectedCampaign] = useState<UpdateCampaignDTO | null>(null)
  const [campaignTasks, setCampaignTasks] = useState<CampaignTaskDetail[]>([])
  const [campaignLogs, setCampaignLogs] = useState<CampaignLogItem[]>([])
  const [detailsLoading, setDetailsLoading] = useState(false)
  const [detailSubTab, setDetailSubTab] = useState<'tasks' | 'logs'>('tasks')

  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // Upload Release Form
  const [relVersion, setRelVersion] = useState('')
  const [relOS, setRelOS] = useState('windows')
  const [relArch, setRelArch] = useState('amd64')
  const [relChangelog, setRelChangelog] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)

  // New Campaign Form
  const [newCampaign, setNewCampaign] = useState({
    name: '',
    target_version: '',
    target_type: 'all',
    target_id: '',
    batch_size: 25,
    stagger_interval_sec: 60,
  })

  const loadData = async () => {
    setLoading(true)
    try {
      const [rList, cList] = await Promise.all([
        api.getAgentReleases().catch(() => []),
        api.getUpdateCampaigns().catch(() => []),
      ])
      setReleases(rList)
      setCampaigns(cList)
    } catch (err: any) {
      setMsg({ type: 'error', text: err.message || 'Failed to load update catalog' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  // Auto-refresh campaign details when inspecting an in_progress campaign
  const selectedCampaignRef = useRef(selectedCampaign)
  selectedCampaignRef.current = selectedCampaign

  useEffect(() => {
    if (!isDetailsModalOpen || !selectedCampaign) return

    fetchCampaignDetails(selectedCampaign.id)

    // Poll every 3s if campaign is in_progress
    if (selectedCampaign.status === 'in_progress') {
      const timer = setInterval(async () => {
        if (!selectedCampaignRef.current) return
        try {
          const [tasks, logs, cList] = await Promise.all([
            api.getCampaignTasks(selectedCampaignRef.current.id),
            api.getCampaignLogs(selectedCampaignRef.current.id),
            api.getUpdateCampaigns(),
          ])
          setCampaignTasks(tasks)
          setCampaignLogs(logs)
          setCampaigns(cList)
          const updated = cList.find((c) => c.id === selectedCampaignRef.current?.id)
          if (updated) {
            setSelectedCampaign(updated)
          }
        } catch {
          // ignore background polling errors
        }
      }, 3000)
      return () => clearInterval(timer)
    }
  }, [isDetailsModalOpen, selectedCampaign?.id, selectedCampaign?.status])

  const fetchCampaignDetails = async (campaignId: string) => {
    setDetailsLoading(true)
    try {
      const [tasks, logs] = await Promise.all([
        api.getCampaignTasks(campaignId).catch(() => []),
        api.getCampaignLogs(campaignId).catch(() => []),
      ])
      setCampaignTasks(tasks)
      setCampaignLogs(logs)
    } catch (err: any) {
      toast.error(err.message || 'Failed to load campaign details', 'Error')
    } finally {
      setDetailsLoading(false)
    }
  }

  const handleOpenDetails = (c: UpdateCampaignDTO) => {
    setSelectedCampaign(c)
    setIsDetailsModalOpen(true)
    setDetailSubTab('tasks')
  }

  const handleUploadRelease = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedFile) {
      setMsg({ type: 'error', text: 'Please select an agent binary file to upload' })
      return
    }

    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', selectedFile)
      fd.append('version', relVersion)
      fd.append('os_name', relOS)
      fd.append('arch', relArch)
      fd.append('changelog', relChangelog)

      await api.uploadAgentRelease(fd)
      const successText = `Release v${relVersion} (${relOS}/${relArch}) uploaded and registered.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Release Uploaded')
      setIsReleaseModalOpen(false)
      setSelectedFile(null)
      setRelVersion('')
      setRelChangelog('')
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to upload release binary'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Upload Failed')
    } finally {
      setUploading(false)
    }
  }

  const handleCreateCampaign = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createUpdateCampaign(newCampaign)
      const successText = `Update campaign '${newCampaign.name}' launched successfully.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Campaign Launched')
      setIsCampaignModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to launch update campaign'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Launch Failed')
    }
  }

  const handleStartCampaign = async (id: string, name: string) => {
    try {
      const res = await api.startUpdateCampaign(id)
      const successText = res.message || `Campaign '${name}' started: ${res.dispatched_live} of ${res.total_targets} targets dispatched live.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Rollout Started')
      loadData()
      if (selectedCampaign?.id === id) {
        fetchCampaignDetails(id)
      }
    } catch (err: any) {
      const errorText = err.message || 'Failed to start campaign'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Start Failed')
    }
  }

  const handleCancelCampaign = async (id: string, name: string) => {
    if (!window.confirm(`Batalkan update campaign '${name}'? Task yang belum selesai akan dihentikan.`)) {
      return
    }
    try {
      await api.cancelUpdateCampaign(id)
      toast.success(`Campaign '${name}' telah dibatalkan.`, 'Campaign Cancelled')
      loadData()
      if (selectedCampaign?.id === id) {
        fetchCampaignDetails(id)
      }
    } catch (err: any) {
      toast.error(err.message || 'Gagal membatalkan campaign', 'Cancel Failed')
    }
  }

  const formatFileSize = (bytes: number) => {
    if (!bytes) return '—'
    const mb = bytes / (1024 * 1024)
    return `${mb.toFixed(2)} MB`
  }

  const formatStatusPill = (status: string) => {
    switch (status) {
      case 'success':
      case 'completed':
        return 'online'
      case 'in_progress':
      case 'dispatched':
      case 'downloading':
      case 'verifying':
      case 'swapping':
        return 'warning'
      case 'failed':
      case 'rollback':
      case 'cancelled':
        return 'danger'
      default:
        return 'offline'
    }
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Agent Self-Update & Rollout Campaigns</h2>
          <p className="page-subtitle">
            Autonomous in-place binary upgrades, SHA-256 verification, and phased canary deployments
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {canAdmin && subTab === 'releases' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsReleaseModalOpen(true)}>
              <UploadCloud size={16} />
              <span>Publish Release Binary</span>
            </button>
          )}
          {canAdmin && subTab === 'campaigns' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsCampaignModalOpen(true)}>
              <Play size={16} />
              <span>Launch Update Campaign</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label="Dismiss message">
            <X size={14} />
          </button>
        </div>
      )}

      {/* KPI Cards */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Available Releases</span>
            <ArrowUpCircle className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{releases.length}</div>
          <span className="kpi-hint">Binaries across OS & CPU architectures</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Active Campaigns</span>
            <Layers className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">
            {campaigns.filter((c) => c.status === 'in_progress').length}
          </div>
          <span className="kpi-hint">Fleet rollout waves underway</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Integrity Enforced</span>
            <ShieldCheck className="kpi-icon text-success" size={20} />
          </div>
          <div className="kpi-value text-success">100%</div>
          <span className="kpi-hint">Pre-execution SHA-256 validation</span>
        </div>
      </div>

      {/* Tabs */}
      <div className="tabs-nav">
        <button
          type="button"
          className={`tab-btn ${subTab === 'releases' ? 'active' : ''}`}
          onClick={() => setSubTab('releases')}
        >
          <FolderArchive size={16} />
          <span>Release Catalog ({releases.length})</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'campaigns' ? 'active' : ''}`}
          onClick={() => setSubTab('campaigns')}
        >
          <Layers size={16} />
          <span>Rollout Campaigns ({campaigns.length})</span>
        </button>
      </div>

      {/* Tab 1: Releases Catalog */}
      {subTab === 'releases' && (
        <div className="table-card">
          <DataTable label="Agent releases">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Version</th>
                  <th>Platform & Arch</th>
                  <th>Filename</th>
                  <th>Size</th>
                  <th>SHA-256 Checksum</th>
                  <th>Uploaded By</th>
                  <th>Published Date</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {releases.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="text-center py-8 text-muted">
                      No agent releases published yet. Click "Publish Release Binary" to upload an agent executable.
                    </td>
                  </tr>
                ) : (
                  releases.map((r) => (
                    <tr key={r.id}>
                      <td>
                        <span className="font-semibold text-primary">v{r.version}</span>
                      </td>
                      <td>
                        <span className="os-badge">{r.os_name} / {r.arch}</span>
                      </td>
                      <td className="font-mono text-sm">{r.file_name}</td>
                      <td>{formatFileSize(r.file_size)}</td>
                      <td>
                        <span className="font-mono text-sm text-dim" title={r.sha256_checksum}>
                          {r.sha256_checksum ? r.sha256_checksum.slice(0, 16) + '...' : '—'}
                        </span>
                      </td>
                      <td>{r.uploaded_by}</td>
                      <td className="text-sm text-muted">{new Date(r.created_at).toLocaleString()}</td>
                      <td>
                        <span className={`status-pill ${r.is_active ? 'online' : 'offline'}`}>
                          {r.is_active ? 'Active' : 'Archived'}
                        </span>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Tab 2: Campaigns */}
      {subTab === 'campaigns' && (
        <div className="table-card">
          <DataTable label="Update campaigns">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Campaign Name</th>
                  <th>Target Version</th>
                  <th>Scope</th>
                  <th>Batch / Stagger</th>
                  <th>Progress & Impact</th>
                  <th>Status</th>
                  <th>Created At</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {campaigns.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="text-center py-8 text-muted">
                      No rollout campaigns created yet. Click "Launch Update Campaign" to roll out an upgrade.
                    </td>
                  </tr>
                ) : (
                  campaigns.map((c) => {
                    const total = c.total_tasks || 0
                    const succ = c.succeeded_tasks || 0
                    const fail = c.failed_tasks || 0
                    const pend = c.pending_tasks || 0
                    const percent = total > 0 ? Math.round((succ / total) * 100) : c.status === 'completed' ? 100 : 0

                    return (
                      <tr key={c.id}>
                        <td className="font-semibold text-main">{c.name}</td>
                        <td>
                          <span className="font-semibold text-primary">v{c.target_version}</span>
                        </td>
                        <td>
                          <span className="badge-target">{c.target_type}</span>
                        </td>
                        <td className="text-sm">
                          {c.batch_size} devs / {c.stagger_interval_sec}s
                        </td>
                        <td>
                          {total > 0 ? (
                            <div style={{ minWidth: 150 }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.8rem', marginBottom: 3 }}>
                                <span className="font-semibold">{succ}/{total} ({percent}%)</span>
                                <span>
                                  <span className="text-success" title="Succeeded">✓{succ}</span>{' '}
                                  {fail > 0 && <span className="text-danger" title="Failed">✗{fail} </span>}
                                  {pend > 0 && <span className="text-warning" title="Pending">⧖{pend}</span>}
                                </span>
                              </div>
                              <div style={{ width: '100%', height: 6, background: 'var(--border-color, #e2e8f0)', borderRadius: 3, overflow: 'hidden' }}>
                                <div
                                  style={{
                                    width: `${percent}%`,
                                    height: '100%',
                                    background: fail > 0 && percent === 0 ? 'var(--danger-color, #ef4444)' : 'var(--primary-color, #3b82f6)',
                                    borderRadius: 3,
                                    transition: 'width 0.3s ease',
                                  }}
                                />
                              </div>
                            </div>
                          ) : (
                            <span className="text-sm text-muted">
                              {c.status === 'completed' ? `Already v${c.target_version}` : '—'}
                            </span>
                          )}
                        </td>
                        <td>
                          <span className={`status-pill ${formatStatusPill(c.status)}`}>
                            {c.status}
                          </span>
                        </td>
                        <td className="text-sm text-muted">{new Date(c.created_at).toLocaleString()}</td>
                        <td>
                          <div className="action-buttons" style={{ display: 'flex', gap: 6 }}>
                            <button
                              type="button"
                              className="btn-action"
                              onClick={() => handleOpenDetails(c)}
                              title="Lihat rincian agen terdampak dan riwayat log real-time"
                            >
                              <FileText size={14} />
                              <span>Logs & Impact</span>
                            </button>

                            {canAdmin && (c.status === 'draft' || c.status === 'pending') && (
                              <button
                                type="button"
                                className="btn-action"
                                onClick={() => handleStartCampaign(c.id, c.name)}
                                title="Dispatch update tasks to resolved target devices"
                              >
                                <Play size={14} />
                                <span>Start</span>
                              </button>
                            )}

                            {canAdmin && c.status === 'in_progress' && (
                              <button
                                type="button"
                                className="btn-action text-danger"
                                onClick={() => handleCancelCampaign(c.id, c.name)}
                                title="Batalkan eksekusi campaign ini"
                              >
                                <XCircle size={14} />
                                <span>Cancel</span>
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

      {/* Campaign Details & Impacted Agents Modal */}
      {selectedCampaign && (
        <Modal
          open={isDetailsModalOpen}
          onClose={() => setIsDetailsModalOpen(false)}
          title={`Campaign: ${selectedCampaign.name}`}
          size="lg"
          footer={
            <div style={{ display: 'flex', justifyContent: 'space-between', width: '100%', alignItems: 'center' }}>
              <div style={{ fontSize: '0.85rem' }} className="text-muted">
                {selectedCampaign.status === 'in_progress' ? (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    <RefreshCw size={14} className="spinning text-primary" />
                    <span>Auto-refreshing real-time progress every 3s...</span>
                  </span>
                ) : (
                  <span>Finished on {new Date(selectedCampaign.updated_at).toLocaleString()}</span>
                )}
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => fetchCampaignDetails(selectedCampaign.id)}
                  disabled={detailsLoading}
                >
                  <RefreshCw size={14} className={detailsLoading ? 'spinning' : ''} />
                  <span>Refresh</span>
                </button>
                {canAdmin && selectedCampaign.status === 'in_progress' && (
                  <button
                    type="button"
                    className="btn btn-danger-outline"
                    onClick={() => handleCancelCampaign(selectedCampaign.id, selectedCampaign.name)}
                  >
                    <XCircle size={14} />
                    <span>Cancel Rollout</span>
                  </button>
                )}
                <button type="button" className="btn btn-secondary" onClick={() => setIsDetailsModalOpen(false)}>
                  Close
                </button>
              </div>
            </div>
          }
        >
          <div>
            {/* Modal Campaign Summary Bar */}
            <div
              style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(130px, 1fr))',
                gap: 12,
                padding: 12,
                borderRadius: 8,
                background: 'var(--bg-subtle, #f8fafc)',
                border: '1px solid var(--border-color, #e2e8f0)',
                marginBottom: 16,
              }}
            >
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>Target Version</span>
                <span className="font-semibold text-primary" style={{ fontSize: '1.1rem' }}>v{selectedCampaign.target_version}</span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>Campaign Status</span>
                <span className={`status-pill ${formatStatusPill(selectedCampaign.status)}`}>
                  {selectedCampaign.status}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>Total Impacted</span>
                <span className="font-semibold" style={{ fontSize: '1.1rem' }}>{campaignTasks.length} Devices</span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>Succeeded</span>
                <span className="font-semibold text-success" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((t) => t.status === 'success').length}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>In-Progress / Queued</span>
                <span className="font-semibold text-warning" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((t) => ['pending', 'dispatched', 'downloading', 'verifying', 'swapping'].includes(t.status)).length}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>Failed / Reverted</span>
                <span className="font-semibold text-danger" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((t) => ['failed', 'rollback'].includes(t.status)).length}
                </span>
              </div>
            </div>

            {/* Modal Subtabs */}
            <div className="tabs-nav" style={{ marginBottom: 12 }}>
              <button
                type="button"
                className={`tab-btn ${detailSubTab === 'tasks' ? 'active' : ''}`}
                onClick={() => setDetailSubTab('tasks')}
              >
                <Monitor size={15} />
                <span>Impacted Agents ({campaignTasks.length})</span>
              </button>
              <button
                type="button"
                className={`tab-btn ${detailSubTab === 'logs' ? 'active' : ''}`}
                onClick={() => setDetailSubTab('logs')}
              >
                <Terminal size={15} />
                <span>Rollout Activity Logs ({campaignLogs.length})</span>
              </button>
            </div>

            {/* Subtab 1: Impacted Agents Table */}
            {detailSubTab === 'tasks' && (
              <div>
                {detailsLoading && campaignTasks.length === 0 ? (
                  <div className="py-8 text-center text-muted">
                    <RefreshCw size={20} className="spinning inline-block" /> Memuat data perangkat terdampak...
                  </div>
                ) : campaignTasks.length === 0 ? (
                  <div className="py-8 text-center text-muted">
                    {selectedCampaign.status === 'completed' ? (
                      <div>
                        <CheckCircle2 size={32} className="text-success inline-block mb-2" />
                        <p className="font-semibold">Semua perangkat target telah menggunakan Agent v{selectedCampaign.target_version}.</p>
                        <p className="text-sm">Tidak ada task yang perlu dieksekusi.</p>
                      </div>
                    ) : (
                      <p>Belum ada task pembaruan yang dibuat untuk campaign ini. Tekan tombol "Start" untuk mendistribusikan.</p>
                    )}
                  </div>
                ) : (
                  <div style={{ maxHeight: 400, overflowY: 'auto' }}>
                    <table className="data-table">
                      <thead>
                        <tr>
                          <th>Hostname & Device</th>
                          <th>Platform</th>
                          <th>Version Shift</th>
                          <th>Update Status</th>
                          <th>Catatan / Diagnostic</th>
                          <th>Completed At</th>
                        </tr>
                      </thead>
                      <tbody>
                        {campaignTasks.map((t) => (
                          <tr key={t.id}>
                            <td>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                                <span
                                  style={{
                                    width: 8,
                                    height: 8,
                                    borderRadius: '50%',
                                    background: t.device_online_status === 'online' ? '#10b981' : '#94a3b8',
                                    display: 'inline-block',
                                  }}
                                  title={t.device_online_status === 'online' ? 'Online' : 'Offline'}
                                />
                                <span className="font-semibold text-main">{t.hostname || t.device_id}</span>
                              </div>
                              <span className="font-mono text-dim" style={{ fontSize: '0.75rem' }}>{t.device_id.slice(0, 12)}...</span>
                            </td>
                            <td>
                              <span className="os-badge">{t.os_name || 'windows'}</span>
                            </td>
                            <td>
                              <span className="text-muted" style={{ fontSize: '0.85rem' }}>v{t.from_version}</span>
                              <span style={{ margin: '0 4px' }}>→</span>
                              <span className="font-semibold text-primary" style={{ fontSize: '0.85rem' }}>v{t.target_version}</span>
                            </td>
                            <td>
                              <span className={`status-pill ${formatStatusPill(t.status)}`}>
                                {t.status}
                              </span>
                            </td>
                            <td style={{ maxWidth: 220 }}>
                              {t.error_message ? (
                                <span className="text-danger" style={{ fontSize: '0.8rem', wordBreak: 'break-word' }}>
                                  {t.error_message}
                                </span>
                              ) : t.status === 'success' ? (
                                <span className="text-success" style={{ fontSize: '0.8rem' }}>
                                  Berhasil terupdate ke v{t.target_version}
                                </span>
                              ) : t.status === 'swapping' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  Mengganti biner dan me-restart agent
                                </span>
                              ) : t.status === 'downloading' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  Sedang mengunduh file biner...
                                </span>
                              ) : t.status === 'verifying' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  Verifikasi checksum & digital signature
                                </span>
                              ) : t.status === 'dispatched' ? (
                                <span className="text-muted" style={{ fontSize: '0.8rem' }}>
                                  Terkirim via websocket ke agent
                                </span>
                              ) : (
                                <span className="text-muted" style={{ fontSize: '0.8rem' }}>
                                  Antrean pending (menunggu agent online)
                                </span>
                              )}
                            </td>
                            <td className="text-sm text-muted">
                              {t.completed_at ? new Date(t.completed_at).toLocaleTimeString() : t.dispatched_at ? new Date(t.dispatched_at).toLocaleTimeString() : '—'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            )}

            {/* Subtab 2: Activity Logs Timeline */}
            {detailSubTab === 'logs' && (
              <div>
                {campaignLogs.length === 0 ? (
                  <div className="py-8 text-center text-muted">
                    Belum ada rekaman audit log untuk campaign ini.
                  </div>
                ) : (
                  <div
                    style={{
                      maxHeight: 400,
                      overflowY: 'auto',
                      background: 'var(--bg-card, #1e293b)',
                      color: 'var(--text-light, #f1f5f9)',
                      padding: 12,
                      borderRadius: 6,
                      fontFamily: 'monospace',
                      fontSize: '0.8rem',
                      lineHeight: '1.5',
                    }}
                  >
                    {campaignLogs.map((log) => {
                      let parsedDetails: any = null
                      try {
                        parsedDetails = JSON.parse(log.details)
                      } catch {
                        parsedDetails = log.details
                      }

                      const timeStr = new Date(log.created_at).toLocaleTimeString()
                      const actionTag = log.action.replace('agent_update.', '').toUpperCase()

                      return (
                        <div
                          key={log.id}
                          style={{
                            borderBottom: '1px solid rgba(255,255,255,0.07)',
                            padding: '6px 0',
                            display: 'flex',
                            gap: 10,
                          }}
                        >
                          <span style={{ color: '#94a3b8', minWidth: 70 }}>[{timeStr}]</span>
                          <span
                            style={{
                              color:
                                actionTag.includes('SUCCESS') || actionTag.includes('COMPLETED')
                                  ? '#10b981'
                                  : actionTag.includes('FAIL') || actionTag.includes('CANCEL')
                                  ? '#ef4444'
                                  : '#38bdf8',
                              fontWeight: 'bold',
                              minWidth: 140,
                            }}
                          >
                            [{actionTag}]
                          </span>
                          <span style={{ color: '#f8fafc', flex: 1 }}>
                            {typeof parsedDetails === 'object' && parsedDetails !== null ? (
                              <span>
                                {parsedDetails.reason && <span>{parsedDetails.reason} </span>}
                                {parsedDetails.status && <span>Status: {parsedDetails.status} </span>}
                                {parsedDetails.error && <span style={{ color: '#f87171' }}>Error: {parsedDetails.error} </span>}
                                {parsedDetails.version && <span>Target: v{parsedDetails.version} </span>}
                                {parsedDetails.task_id && <span style={{ color: '#94a3b8' }}>(task: {parsedDetails.task_id.slice(0, 8)})</span>}
                              </span>
                            ) : (
                              String(log.details)
                            )}
                          </span>
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )}
          </div>
        </Modal>
      )}

      {/* Publish Release Modal */}
      <Modal
        open={isReleaseModalOpen}
        onClose={() => setIsReleaseModalOpen(false)}
        title="Publish Agent Binary Release"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsReleaseModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="agent-release-form" className="btn btn-primary" disabled={uploading}>
              {uploading ? 'Uploading & Hashing...' : 'Publish Release'}
            </button>
          </>
        }
      >
        <form id="agent-release-form" onSubmit={handleUploadRelease}>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-version">
              Version Number
            </label>
            <input
              id="rel-version"
              type="text"
              className="form-input"
              required
              placeholder="e.g. 1.2.0"
              value={relVersion}
              onChange={(e) => setRelVersion(e.target.value)}
            />
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="rel-os">
                Target OS
              </label>
              <select id="rel-os" className="form-select" value={relOS} onChange={(e) => setRelOS(e.target.value)}>
                <option value="windows">Windows</option>
                <option value="linux">Linux</option>
                <option value="darwin">macOS (Darwin)</option>
              </select>
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="rel-arch">
                Architecture
              </label>
              <select id="rel-arch" className="form-select" value={relArch} onChange={(e) => setRelArch(e.target.value)}>
                <option value="amd64">x86_64 (amd64)</option>
                <option value="arm64">ARM64 (Apple Silicon / aarch64)</option>
              </select>
            </div>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-file">
              Executable Binary File
            </label>
            <input
              id="rel-file"
              type="file"
              className="form-input"
              required
              onChange={(e) => {
                if (e.target.files && e.target.files.length > 0) {
                  setSelectedFile(e.target.files[0])
                }
              }}
            />
            <span className="form-hint">Server will calculate and verify SHA-256 checksum automatically.</span>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-changelog">
              Changelog / Release Notes
            </label>
            <textarea
              id="rel-changelog"
              className="form-textarea"
              rows={3}
              placeholder="Summary of bug fixes, enhancements, or security patches..."
              value={relChangelog}
              onChange={(e) => setRelChangelog(e.target.value)}
            />
          </div>
        </form>
      </Modal>

      {/* Launch Campaign Modal */}
      <Modal
        open={isCampaignModalOpen}
        onClose={() => setIsCampaignModalOpen(false)}
        title="Launch Phased Rollout Campaign"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsCampaignModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="update-campaign-form" className="btn btn-primary">
              Start Campaign
            </button>
          </>
        }
      >
        <form id="update-campaign-form" onSubmit={handleCreateCampaign}>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-name">
              Campaign Name
            </label>
            <input
              id="campaign-name"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Q4 2026 Fleet Upgrade to v1.2.0"
              value={newCampaign.name}
              onChange={(e) => setNewCampaign({ ...newCampaign, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-version">
              Target Release Version
            </label>
            <select
              id="campaign-version"
              className="form-select"
              required
              value={newCampaign.target_version}
              onChange={(e) => setNewCampaign({ ...newCampaign, target_version: e.target.value })}
            >
              <option value="">-- Select Release Version --</option>
              {Array.from(new Set(releases.map((r) => r.version))).map((v) => (
                <option key={v} value={v}>
                  v{v}
                </option>
              ))}
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-scope">
              Rollout Scope
            </label>
            <select
              id="campaign-scope"
              className="form-select"
              value={newCampaign.target_type}
              onChange={(e) => setNewCampaign({ ...newCampaign, target_type: e.target.value })}
            >
              <option value="all">Entire Fleet (All Connected Agents)</option>
              <option value="group">Branch Site / Device Group</option>
            </select>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="campaign-batch">
                Batch Size (Devices / Wave)
              </label>
              <input
                id="campaign-batch"
                type="number"
                className="form-input"
                min={1}
                value={newCampaign.batch_size}
                onChange={(e) => setNewCampaign({ ...newCampaign, batch_size: Number(e.target.value) })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="campaign-stagger">
                Stagger Interval (Seconds)
              </label>
              <input
                id="campaign-stagger"
                type="number"
                className="form-input"
                min={10}
                value={newCampaign.stagger_interval_sec}
                onChange={(e) =>
                  setNewCampaign({ ...newCampaign, stagger_interval_sec: Number(e.target.value) })
                }
              />
            </div>
          </div>
        </form>
      </Modal>
    </div>
  )
}
