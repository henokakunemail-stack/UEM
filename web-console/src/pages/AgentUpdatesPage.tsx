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
import { useT } from '../i18n/core'

// Campaign, task and target values are server enums. Known ones get a
// translated label; anything else is shown exactly as the server sent it.
const STATUS_KEYS: Record<string, string> = {
  draft: 'updates.status_draft',
  pending: 'updates.status_pending',
  in_progress: 'updates.status_in_progress',
  completed: 'updates.status_completed',
  cancelled: 'updates.status_cancelled',
  dispatched: 'updates.status_dispatched',
  downloading: 'updates.status_downloading',
  verifying: 'updates.status_verifying',
  swapping: 'updates.status_swapping',
  success: 'updates.status_success',
  failed: 'updates.status_failed',
  rollback: 'updates.status_rollback',
}
const TARGET_KEYS: Record<string, string> = {
  all: 'updates.target_all_short',
  group: 'updates.target_group_short',
}

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
  const { t } = useT()
  const statusLabel = (s: string) => (STATUS_KEYS[s] ? t(STATUS_KEYS[s]) : s)

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
      setMsg({ type: 'error', text: err.message || t('updates.load_failed') })
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
      toast.error(err.message || t('updates.details_load_failed'), t('updates.error'))
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
      setMsg({ type: 'error', text: t('updates.select_file') })
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
      const successText = t('updates.release_uploaded', { version: relVersion, os: relOS, arch: relArch })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('updates.release_uploaded_title'))
      setIsReleaseModalOpen(false)
      setSelectedFile(null)
      setRelVersion('')
      setRelChangelog('')
      loadData()
    } catch (err: any) {
      const errorText = err.message || t('updates.upload_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('updates.upload_failed_title'))
    } finally {
      setUploading(false)
    }
  }

  const handleCreateCampaign = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createUpdateCampaign(newCampaign)
      const successText = t('updates.campaign_launched', { name: newCampaign.name })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('updates.campaign_launched_title'))
      setIsCampaignModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || t('updates.launch_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('updates.launch_failed_title'))
    }
  }

  const handleStartCampaign = async (id: string, name: string) => {
    try {
      const res = await api.startUpdateCampaign(id)
      const successText = res.message || t('updates.campaign_started', { name, live: res.dispatched_live, total: res.total_targets })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('updates.rollout_started'))
      loadData()
      if (selectedCampaign?.id === id) {
        fetchCampaignDetails(id)
      }
    } catch (err: any) {
      const errorText = err.message || t('updates.start_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('updates.start_failed_title'))
    }
  }

  const handleCancelCampaign = async (id: string, name: string) => {
    if (!window.confirm(t('updates.cancel_confirm', { name }))) {
      return
    }
    try {
      await api.cancelUpdateCampaign(id)
      toast.success(t('updates.campaign_cancelled', { name }), t('updates.campaign_cancelled_title'))
      loadData()
      if (selectedCampaign?.id === id) {
        fetchCampaignDetails(id)
      }
    } catch (err: any) {
      toast.error(err.message || t('updates.cancel_failed'), t('updates.cancel_failed_title'))
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
          <h2 className="page-title">{t('updates.title')}</h2>
          <p className="page-subtitle">
            {t('updates.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('updates.refresh')}</span>
          </button>
          {canAdmin && subTab === 'releases' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsReleaseModalOpen(true)}>
              <UploadCloud size={16} />
              <span>{t('updates.publish_release_binary')}</span>
            </button>
          )}
          {canAdmin && subTab === 'campaigns' && (
            <button type="button" className="btn btn-primary" onClick={() => setIsCampaignModalOpen(true)}>
              <Play size={16} />
              <span>{t('updates.launch_campaign')}</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label={t('updates.dismiss_message')}>
            <X size={14} />
          </button>
        </div>
      )}

      {/* KPI Cards */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('updates.kpi_releases')}</span>
            <ArrowUpCircle className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{releases.length}</div>
          <span className="kpi-hint">{t('updates.kpi_releases_hint')}</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('updates.kpi_active')}</span>
            <Layers className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">
            {campaigns.filter((c) => c.status === 'in_progress').length}
          </div>
          <span className="kpi-hint">{t('updates.kpi_active_hint')}</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('updates.kpi_integrity')}</span>
            <ShieldCheck className="kpi-icon text-success" size={20} />
          </div>
          <div className="kpi-value text-success">100%</div>
          <span className="kpi-hint">{t('updates.kpi_integrity_hint')}</span>
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
          <span>{t('updates.tab_releases', { count: releases.length })}</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'campaigns' ? 'active' : ''}`}
          onClick={() => setSubTab('campaigns')}
        >
          <Layers size={16} />
          <span>{t('updates.tab_campaigns', { count: campaigns.length })}</span>
        </button>
      </div>

      {/* Tab 1: Releases Catalog */}
      {subTab === 'releases' && (
        <div className="table-card">
          <DataTable label={t('updates.releases_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('updates.col_version')}</th>
                  <th>{t('updates.col_platform')}</th>
                  <th>{t('updates.col_filename')}</th>
                  <th>{t('updates.col_size')}</th>
                  <th>{t('updates.col_checksum')}</th>
                  <th>{t('updates.col_uploaded_by')}</th>
                  <th>{t('updates.col_published')}</th>
                  <th>{t('updates.col_status')}</th>
                </tr>
              </thead>
              <tbody>
                {releases.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="text-center py-8 text-muted">
                      {t('updates.no_releases')}
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
                          {r.is_active ? t('updates.active') : t('updates.archived')}
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
          <DataTable label={t('updates.campaigns_table_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('updates.col_campaign_name')}</th>
                  <th>{t('updates.col_target_version')}</th>
                  <th>{t('updates.col_scope')}</th>
                  <th>{t('updates.col_batch_stagger')}</th>
                  <th>{t('updates.col_progress')}</th>
                  <th>{t('updates.col_status')}</th>
                  <th>{t('updates.col_created_at')}</th>
                  <th>{t('updates.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {campaigns.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="text-center py-8 text-muted">
                      {t('updates.no_campaigns')}
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
                          <span className="badge-target">{TARGET_KEYS[c.target_type] ? t(TARGET_KEYS[c.target_type]) : c.target_type}</span>
                        </td>
                        <td className="text-sm">
                          {t('updates.batch_stagger', { batch: c.batch_size, stagger: c.stagger_interval_sec })}
                        </td>
                        <td>
                          {total > 0 ? (
                            <div style={{ minWidth: 150 }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.8rem', marginBottom: 3 }}>
                                <span className="font-semibold">{succ}/{total} ({percent}%)</span>
                                <span>
                                  <span className="text-success" title={t('updates.succeeded')}>✓{succ}</span>{' '}
                                  {fail > 0 && <span className="text-danger" title={t('updates.failed')}>✗{fail} </span>}
                                  {pend > 0 && <span className="text-warning" title={t('updates.pending')}>⧖{pend}</span>}
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
                              {c.status === 'completed' ? t('updates.already_on', { version: c.target_version }) : '—'}
                            </span>
                          )}
                        </td>
                        <td>
                          <span className={`status-pill ${formatStatusPill(c.status)}`}>
                            {statusLabel(c.status)}
                          </span>
                        </td>
                        <td className="text-sm text-muted">{new Date(c.created_at).toLocaleString()}</td>
                        <td>
                          <div className="action-buttons" style={{ display: 'flex', gap: 6 }}>
                            <button
                              type="button"
                              className="btn-action"
                              onClick={() => handleOpenDetails(c)}
                              title={t('updates.logs_impact_title')}
                            >
                              <FileText size={14} />
                              <span>{t('updates.logs_impact')}</span>
                            </button>

                            {canAdmin && (c.status === 'draft' || c.status === 'pending') && (
                              <button
                                type="button"
                                className="btn-action"
                                onClick={() => handleStartCampaign(c.id, c.name)}
                                title={t('updates.start_title')}
                              >
                                <Play size={14} />
                                <span>{t('updates.start')}</span>
                              </button>
                            )}

                            {canAdmin && c.status === 'in_progress' && (
                              <button
                                type="button"
                                className="btn-action text-danger"
                                onClick={() => handleCancelCampaign(c.id, c.name)}
                                title={t('updates.cancel_title')}
                              >
                                <XCircle size={14} />
                                <span>{t('common.cancel')}</span>
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
          title={t('updates.campaign_title', { name: selectedCampaign.name })}
          size="lg"
          footer={
            <div style={{ display: 'flex', justifyContent: 'space-between', width: '100%', alignItems: 'center' }}>
              <div style={{ fontSize: '0.85rem' }} className="text-muted">
                {selectedCampaign.status === 'in_progress' ? (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    <RefreshCw size={14} className="spinning text-primary" />
                    <span>{t('updates.auto_refreshing')}</span>
                  </span>
                ) : (
                  <span>{t('updates.finished_on', { date: new Date(selectedCampaign.updated_at).toLocaleString() })}</span>
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
                  <span>{t('updates.refresh')}</span>
                </button>
                {canAdmin && selectedCampaign.status === 'in_progress' && (
                  <button
                    type="button"
                    className="btn btn-danger-outline"
                    onClick={() => handleCancelCampaign(selectedCampaign.id, selectedCampaign.name)}
                  >
                    <XCircle size={14} />
                    <span>{t('updates.cancel_rollout')}</span>
                  </button>
                )}
                <button type="button" className="btn btn-secondary" onClick={() => setIsDetailsModalOpen(false)}>
                  {t('common.close')}
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
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.col_target_version')}</span>
                <span className="font-semibold text-primary" style={{ fontSize: '1.1rem' }}>v{selectedCampaign.target_version}</span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.campaign_status')}</span>
                <span className={`status-pill ${formatStatusPill(selectedCampaign.status)}`}>
                  {statusLabel(selectedCampaign.status)}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.total_impacted')}</span>
                <span className="font-semibold" style={{ fontSize: '1.1rem' }}>{t('updates.devices_count', { count: campaignTasks.length })}</span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.succeeded')}</span>
                <span className="font-semibold text-success" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((task) => task.status === 'success').length}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.in_progress_queued')}</span>
                <span className="font-semibold text-warning" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((task) => ['pending', 'dispatched', 'downloading', 'verifying', 'swapping'].includes(task.status)).length}
                </span>
              </div>
              <div>
                <span className="form-label" style={{ marginBottom: 2 }}>{t('updates.failed_reverted')}</span>
                <span className="font-semibold text-danger" style={{ fontSize: '1.1rem' }}>
                  {campaignTasks.filter((task) => ['failed', 'rollback'].includes(task.status)).length}
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
                <span>{t('updates.tab_impacted', { count: campaignTasks.length })}</span>
              </button>
              <button
                type="button"
                className={`tab-btn ${detailSubTab === 'logs' ? 'active' : ''}`}
                onClick={() => setDetailSubTab('logs')}
              >
                <Terminal size={15} />
                <span>{t('updates.tab_logs', { count: campaignLogs.length })}</span>
              </button>
            </div>

            {/* Subtab 1: Impacted Agents Table */}
            {detailSubTab === 'tasks' && (
              <div>
                {detailsLoading && campaignTasks.length === 0 ? (
                  <div className="py-8 text-center text-muted">
                    <RefreshCw size={20} className="spinning inline-block" /> {t('updates.loading_impacted')}
                  </div>
                ) : campaignTasks.length === 0 ? (
                  <div className="py-8 text-center text-muted">
                    {selectedCampaign.status === 'completed' ? (
                      <div>
                        <CheckCircle2 size={32} className="text-success inline-block mb-2" />
                        <p className="font-semibold">{t('updates.all_on_version', { version: selectedCampaign.target_version })}</p>
                        <p className="text-sm">{t('updates.no_tasks_needed')}</p>
                      </div>
                    ) : (
                      <p>{t('updates.no_tasks_yet')}</p>
                    )}
                  </div>
                ) : (
                  <div style={{ maxHeight: 400, overflowY: 'auto' }}>
                    <table className="data-table">
                      <thead>
                        <tr>
                          <th>{t('updates.col_hostname_device')}</th>
                          <th>{t('updates.col_platform_short')}</th>
                          <th>{t('updates.col_version_shift')}</th>
                          <th>{t('updates.col_update_status')}</th>
                          <th>{t('updates.col_notes')}</th>
                          <th>{t('updates.col_completed_at')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {campaignTasks.map((task) => (
                          <tr key={task.id}>
                            <td>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                                <span
                                  style={{
                                    width: 8,
                                    height: 8,
                                    borderRadius: '50%',
                                    background: task.device_online_status === 'online' ? '#10b981' : '#94a3b8',
                                    display: 'inline-block',
                                  }}
                                  title={task.device_online_status === 'online' ? t('updates.online') : t('updates.offline')}
                                />
                                <span className="font-semibold text-main">{task.hostname || task.device_id}</span>
                              </div>
                              <span className="font-mono text-dim" style={{ fontSize: '0.75rem' }}>{task.device_id.slice(0, 12)}...</span>
                            </td>
                            <td>
                              <span className="os-badge">{task.os_name || 'windows'}</span>
                            </td>
                            <td>
                              <span className="text-muted" style={{ fontSize: '0.85rem' }}>v{task.from_version}</span>
                              <span style={{ margin: '0 4px' }}>→</span>
                              <span className="font-semibold text-primary" style={{ fontSize: '0.85rem' }}>v{task.target_version}</span>
                            </td>
                            <td>
                              <span className={`status-pill ${formatStatusPill(task.status)}`}>
                                {statusLabel(task.status)}
                              </span>
                            </td>
                            <td style={{ maxWidth: 220 }}>
                              {task.error_message ? (
                                <span className="text-danger" style={{ fontSize: '0.8rem', wordBreak: 'break-word' }}>
                                  {task.error_message}
                                </span>
                              ) : task.status === 'success' ? (
                                <span className="text-success" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_success', { version: task.target_version })}
                                </span>
                              ) : task.status === 'swapping' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_swapping')}
                                </span>
                              ) : task.status === 'downloading' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_downloading')}
                                </span>
                              ) : task.status === 'verifying' ? (
                                <span className="text-warning" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_verifying')}
                                </span>
                              ) : task.status === 'dispatched' ? (
                                <span className="text-muted" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_dispatched')}
                                </span>
                              ) : (
                                <span className="text-muted" style={{ fontSize: '0.8rem' }}>
                                  {t('updates.note_pending')}
                                </span>
                              )}
                            </td>
                            <td className="text-sm text-muted">
                              {task.completed_at ? new Date(task.completed_at).toLocaleTimeString() : task.dispatched_at ? new Date(task.dispatched_at).toLocaleTimeString() : '—'}
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
                    {t('updates.no_logs')}
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
                                {parsedDetails.status && <span>{t('updates.log_status', { status: parsedDetails.status })} </span>}
                                {parsedDetails.error && <span style={{ color: '#f87171' }}>{t('updates.log_error', { error: parsedDetails.error })} </span>}
                                {parsedDetails.version && <span>{t('updates.log_target', { version: parsedDetails.version })} </span>}
                                {parsedDetails.task_id && <span style={{ color: '#94a3b8' }}>{t('updates.log_task', { id: parsedDetails.task_id.slice(0, 8) })}</span>}
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
        title={t('updates.publish_modal_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsReleaseModalOpen(false)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="agent-release-form" className="btn btn-primary" disabled={uploading}>
              {uploading ? t('updates.uploading') : t('updates.publish_release')}
            </button>
          </>
        }
      >
        <form id="agent-release-form" onSubmit={handleUploadRelease}>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-version">
              {t('updates.version_number')}
            </label>
            <input
              id="rel-version"
              type="text"
              className="form-input"
              required
              placeholder={t('updates.version_placeholder')}
              value={relVersion}
              onChange={(e) => setRelVersion(e.target.value)}
            />
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="rel-os">
                {t('updates.target_os')}
              </label>
              <select id="rel-os" className="form-select" value={relOS} onChange={(e) => setRelOS(e.target.value)}>
                <option value="windows">Windows</option>
                <option value="linux">Linux</option>
                <option value="darwin">macOS (Darwin)</option>
              </select>
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="rel-arch">
                {t('updates.architecture')}
              </label>
              <select id="rel-arch" className="form-select" value={relArch} onChange={(e) => setRelArch(e.target.value)}>
                <option value="amd64">x86_64 (amd64)</option>
                <option value="arm64">ARM64 (Apple Silicon / aarch64)</option>
              </select>
            </div>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-file">
              {t('updates.binary_file')}
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
            <span className="form-hint">{t('updates.binary_hint')}</span>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="rel-changelog">
              {t('updates.changelog')}
            </label>
            <textarea
              id="rel-changelog"
              className="form-textarea"
              rows={3}
              placeholder={t('updates.changelog_placeholder')}
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
        title={t('updates.launch_modal_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsCampaignModalOpen(false)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="update-campaign-form" className="btn btn-primary">
              {t('updates.start_campaign')}
            </button>
          </>
        }
      >
        <form id="update-campaign-form" onSubmit={handleCreateCampaign}>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-name">
              {t('updates.col_campaign_name')}
            </label>
            <input
              id="campaign-name"
              type="text"
              className="form-input"
              required
              placeholder={t('updates.campaign_name_placeholder')}
              value={newCampaign.name}
              onChange={(e) => setNewCampaign({ ...newCampaign, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-version">
              {t('updates.target_release_version')}
            </label>
            <select
              id="campaign-version"
              className="form-select"
              required
              value={newCampaign.target_version}
              onChange={(e) => setNewCampaign({ ...newCampaign, target_version: e.target.value })}
            >
              <option value="">{t('updates.select_version')}</option>
              {Array.from(new Set(releases.map((r) => r.version))).map((v) => (
                <option key={v} value={v}>
                  v{v}
                </option>
              ))}
            </select>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="campaign-scope">
              {t('updates.rollout_scope')}
            </label>
            <select
              id="campaign-scope"
              className="form-select"
              value={newCampaign.target_type}
              onChange={(e) => setNewCampaign({ ...newCampaign, target_type: e.target.value })}
            >
              <option value="all">{t('updates.scope_all')}</option>
              <option value="group">{t('updates.scope_group')}</option>
            </select>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="campaign-batch">
                {t('updates.batch_size')}
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
                {t('updates.stagger_interval')}
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
