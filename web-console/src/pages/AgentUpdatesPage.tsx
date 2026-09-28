import React, { useEffect, useState } from 'react'
import {
  ArrowUpCircle,
  FolderArchive,
  Layers,
  Play,
  RefreshCw,
  ShieldCheck,
  UploadCloud,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import type { AgentReleaseDTO, UpdateCampaignDTO } from '../types/api'

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
      const successText = `Campaign '${name}' started: ${res.dispatched_live} of ${res.total_targets} targets dispatched live.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Rollout Started')
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to start campaign'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Start Failed')
    }
  }

  const formatFileSize = (bytes: number) => {
    if (!bytes) return '—'
    const mb = bytes / (1024 * 1024)
    return `${mb.toFixed(2)} MB`
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
                  <th>Batch / Interval</th>
                  <th>Status</th>
                  <th>Created At</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {campaigns.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      No rollout campaigns created yet. Click "Launch Update Campaign" to roll out an upgrade.
                    </td>
                  </tr>
                ) : (
                  campaigns.map((c) => (
                    <tr key={c.id}>
                      <td className="font-semibold text-main">{c.name}</td>
                      <td>
                        <span className="font-semibold text-primary">v{c.target_version}</span>
                      </td>
                      <td>
                        <span className="badge-target">{c.target_type}</span>
                      </td>
                      <td>
                        {c.batch_size} devices / {c.stagger_interval_sec}s
                      </td>
                      <td>
                        <span
                          className={`status-pill ${
                            c.status === 'completed'
                              ? 'online'
                              : c.status === 'in_progress'
                              ? 'warning'
                              : 'offline'
                          }`}
                        >
                          {c.status}
                        </span>
                      </td>
                      <td className="text-sm text-muted">{new Date(c.created_at).toLocaleString()}</td>
                      <td>
                        <div className="action-buttons">
                          {/* Creating a campaign only writes a draft row. Nothing is
                              dispatched to a single endpoint until /start is called,
                              so without this button a rollout never leaves the database. */}
                          {canAdmin && (c.status === 'draft' || c.status === 'pending') && (
                            <button
                              type="button"
                              className="btn-action"
                              onClick={() => handleStartCampaign(c.id, c.name)}
                              title="Dispatch update tasks to resolved target devices"
                            >
                              <Play size={15} />
                              <span>Start</span>
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
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
