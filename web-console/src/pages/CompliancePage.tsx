import React, { useCallback, useEffect, useMemo, useState } from 'react'
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Download,
  FileCheck2,
  FileText,
  HelpCircle,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
  XCircle,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { ComplianceFrameworks } from '../components/ComplianceFrameworks'
import { usePermission } from '../hooks/usePermission'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { useT } from '../i18n/core'
import type {
  ComplianceControl,
  ComplianceControlStatus,
  ComplianceDeviceResult,
  ComplianceDeviceStatus,
  ComplianceEvaluation,
  ComplianceProfile,
  ComplianceReport,
  ComplianceRules,
  ComplianceVerifyResult,
  DeviceGroupDTO,
} from '../types/api'

const DEFAULT_RULES: ComplianceRules = {
  require_disk_encryption: true,
  require_antivirus: true,
  require_real_time: true,
  max_signature_age_days: 7,
  require_firewall_all_profiles: true,
  require_patches: true,
  max_missing_patch_age_days: 30,
  max_patch_scan_age_days: 7,
  require_password_policy: true,
  min_password_length: 8,
  max_lockout_threshold: 10,
  require_screen_lock: true,
  max_screen_lock_minutes: 15,
  max_posture_age_days: 7,
}

function pct(v: number | null | undefined): string {
  return v === null || v === undefined ? '—' : `${Math.round(v)}%`
}

function shortHash(h: string): string {
  return `${h.slice(0, 12)}…${h.slice(-8)}`
}

function formatWhen(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function statusTone(s: ComplianceDeviceStatus | ComplianceControlStatus): string {
  switch (s) {
    case 'compliant':
    case 'pass':
      return 'ok'
    case 'noncompliant':
    case 'fail':
      return 'bad'
    case 'not_applicable':
      return 'neutral'
    default:
      return 'unknown'
  }
}

function ControlIcon({ status }: { status: ComplianceControlStatus }) {
  const { t } = useT()
  const label = t(`compliance.control_status_${status}`)
  switch (status) {
    case 'pass':
      return <CheckCircle2 size={16} className="text-success" aria-label={label} />
    case 'fail':
      return <XCircle size={16} className="text-danger" aria-label={label} />
    case 'not_applicable':
      return <span className="text-dim" aria-label={label}>—</span>
    default:
      return <HelpCircle size={16} className="text-warning" aria-label={label} />
  }
}

/** Hashes a file in the browser so the PDF never leaves the machine. Web
 *  Crypto exists only in a secure context (HTTPS or localhost); elsewhere the
 *  caller falls back to a pasted hash. */
async function sha256OfFile(file: File): Promise<string> {
  const buf = await file.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', buf)
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

async function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000)
}

export const CompliancePage: React.FC = () => {
  const { t } = useT()
  const toast = useToast()
  const { can } = usePermission()
  const canGenerate = can('technician')
  const isAdmin = can('admin')

  const [profiles, setProfiles] = useState<ComplianceProfile[]>([])
  const [profileId, setProfileId] = useState<string>('')
  const [evaluation, setEvaluation] = useState<ComplianceEvaluation | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [reports, setReports] = useState<ComplianceReport[]>([])
  const [generating, setGenerating] = useState(false)

  const [statusFilter, setStatusFilter] = useState<'all' | ComplianceDeviceStatus>('all')
  const [controlFilter, setControlFilter] = useState<'all' | ComplianceControl>('all')
  const [siteFilter, setSiteFilter] = useState('all')
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)

  const [editing, setEditing] = useState<ComplianceProfile | null>(null)
  const [deleting, setDeleting] = useState<ComplianceProfile | null>(null)
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])

  const loadProfiles = useCallback(async () => {
    const ps = await api.getComplianceProfiles()
    setProfiles(ps ?? [])
    return ps ?? []
  }, [])

  const loadEvaluation = useCallback(
    async (id: string) => {
      setLoading(true)
      setLoadError(null)
      try {
        setEvaluation(await api.getComplianceEvaluation(id || undefined))
      } catch (e: unknown) {
        setEvaluation(null)
        setLoadError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [],
  )

  const loadReports = useCallback(async () => {
    try {
      setReports((await api.getComplianceReports()) ?? [])
    } catch {
      setReports([])
    }
  }, [])

  useEffect(() => {
    loadProfiles()
      .then((ps) => {
        const def = ps.find((p) => p.is_default) ?? ps[0]
        if (def) setProfileId(def.id)
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
    void loadReports()
    api.getDeviceGroups().then(setGroups).catch(() => setGroups([]))
  }, [loadProfiles, loadReports])

  useEffect(() => {
    if (profileId) void loadEvaluation(profileId)
  }, [profileId, loadEvaluation])

  const activeControls = useMemo(
    () => (evaluation?.summary.controls ?? []).map((c) => c.control),
    [evaluation],
  )

  const sites = useMemo(() => {
    const s = new Set<string>()
    evaluation?.devices.forEach((d) => s.add(d.device.site || ''))
    return Array.from(s).sort()
  }, [evaluation])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (evaluation?.devices ?? []).filter((d) => {
      if (statusFilter !== 'all' && d.status !== statusFilter) return false
      if (siteFilter !== 'all' && (d.device.site || '') !== siteFilter) return false
      if (controlFilter !== 'all') {
        const c = (d.controls ?? []).find((x) => x.control === controlFilter)
        if (!c || c.status !== 'fail') return false
      }
      if (q && !d.device.hostname.toLowerCase().includes(q)) return false
      return true
    })
  }, [evaluation, statusFilter, siteFilter, controlFilter, search])

  const handleGenerate = async () => {
    if (!profileId) return
    setGenerating(true)
    try {
      const rep = await api.generateComplianceReport(profileId)
      toast.success(t('compliance.generated', { id: rep.id }), t('compliance.generated_title'))
      await loadReports()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : String(e), t('compliance.generate_failed'))
    } finally {
      setGenerating(false)
    }
  }

  const handleDownload = async (rep: ComplianceReport) => {
    try {
      const blob = await api.downloadComplianceReport(rep.id)
      await saveBlob(blob, `laporan-kepatuhan-${rep.id}.pdf`)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : String(e), t('compliance.download_failed'))
    }
  }

  const handleSaveProfile = async (p: ComplianceProfile) => {
    try {
      const saved = p.id ? await api.updateComplianceProfile(p) : await api.createComplianceProfile(p)
      toast.success(t('compliance.profile_saved', { name: saved.name }))
      setEditing(null)
      await loadProfiles()
      if (saved.id === profileId) await loadEvaluation(saved.id)
      else setProfileId(saved.id)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : String(e), t('compliance.profile_save_failed'))
    }
  }

  const handleDeleteProfile = async () => {
    if (!deleting) return
    try {
      await api.deleteComplianceProfile(deleting.id)
      toast.success(t('compliance.profile_deleted', { name: deleting.name }))
      const ps = await loadProfiles()
      if (deleting.id === profileId) {
        const def = ps.find((p) => p.is_default) ?? ps[0]
        setProfileId(def?.id ?? '')
      }
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : String(e), t('compliance.profile_delete_failed'))
    } finally {
      setDeleting(null)
    }
  }

  const currentProfile = profiles.find((p) => p.id === profileId)
  const sum = evaluation?.summary

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('compliance.title')}</h2>
          <p className="page-subtitle">{t('compliance.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2 flex-wrap">
          <label className="visually-hidden" htmlFor="compliance-profile">
            {t('compliance.profile')}
          </label>
          <select
            id="compliance-profile"
            className="form-select"
            value={profileId}
            onChange={(e) => setProfileId(e.target.value)}
          >
            {profiles.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
                {p.is_default ? ` (${t('compliance.default')})` : ''}
              </option>
            ))}
          </select>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => profileId && loadEvaluation(profileId)}
            disabled={loading}
          >
            <RefreshCw size={15} className={loading ? 'animate-spin' : ''} />
            <span>{t('compliance.refresh')}</span>
          </button>
          {canGenerate && (
            <button type="button" className="btn btn-primary" onClick={handleGenerate} disabled={generating || !profileId}>
              <FileText size={15} />
              <span>{generating ? t('compliance.generating') : t('compliance.generate_pdf')}</span>
            </button>
          )}
        </div>
      </div>

      {loadError && (
        <div className="alert-banner alert-error" role="alert">
          <XCircle size={18} />
          <span>{t('compliance.load_failed', { error: loadError })}</span>
        </div>
      )}

      {evaluation && sum && (
        <>
          <p className="compliance-scope">
            {t('compliance.scope_line', { scope: evaluation.scope, when: formatWhen(evaluation.evaluated_at) })}
          </p>
          <div className="kpi-grid">
            <div className="kpi-card" data-testid="compliance-score">
              <div className="kpi-header">
                <span className="kpi-label">{t('compliance.kpi_score')}</span>
                <ShieldCheck className="kpi-icon text-primary" size={20} />
              </div>
              <div className="kpi-value">{pct(sum.score)}</div>
              <span className="kpi-hint">
                {sum.score === null
                  ? t('compliance.kpi_score_none')
                  : t('compliance.kpi_score_hint', {
                      compliant: sum.compliant,
                      evaluated: sum.compliant + sum.noncompliant,
                    })}
              </span>
            </div>
            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">{t('compliance.kpi_compliant')}</span>
                <CheckCircle2 className="kpi-icon text-success" size={20} />
              </div>
              <div className="kpi-value text-success">{sum.compliant}</div>
              <span className="kpi-hint">{t('compliance.kpi_of_total', { total: sum.devices })}</span>
            </div>
            <div className="kpi-card">
              <div className="kpi-header">
                <span className="kpi-label">{t('compliance.kpi_noncompliant')}</span>
                <XCircle className="kpi-icon text-danger" size={20} />
              </div>
              <div className="kpi-value text-danger">{sum.noncompliant}</div>
              <span className="kpi-hint">{t('compliance.kpi_noncompliant_hint')}</span>
            </div>
            <div className="kpi-card" data-testid="compliance-unknown">
              <div className="kpi-header">
                <span className="kpi-label">{t('compliance.kpi_unknown')}</span>
                <HelpCircle className="kpi-icon text-warning" size={20} />
              </div>
              <div className="kpi-value text-warning">{sum.unknown}</div>
              <span className="kpi-hint">{t('compliance.kpi_unknown_hint')}</span>
            </div>
          </div>

          <div className="table-card compliance-controls">
            <h3 className="compliance-section-title">{t('compliance.by_control')}</h3>
            <ul>
              {(sum.controls ?? []).map((c) => (
                <li key={c.control}>
                  <span className="compliance-control-name">{t(`compliance.control_${c.control}`)}</span>
                  <span className="compliance-bar" aria-hidden="true">
                    <span style={{ width: `${c.pass_percent ?? 0}%` }} />
                  </span>
                  <strong>{pct(c.pass_percent)}</strong>
                  <span className="text-dim">
                    {t('compliance.control_counts', { pass: c.pass, fail: c.fail, unknown: c.unknown })}
                  </span>
                </li>
              ))}
            </ul>
          </div>

          <div className="table-card">
            <div className="table-toolbar compliance-toolbar">
              <div className="search-wrap">
                <Search size={16} className="search-icon" aria-hidden="true" />
                <input
                  type="text"
                  className="search-input"
                  placeholder={t('compliance.search_placeholder')}
                  aria-label={t('compliance.search_placeholder')}
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </div>
              <select
                className="form-select"
                aria-label={t('compliance.filter_status')}
                value={statusFilter}
                onChange={(e) => setStatusFilter(e.target.value as typeof statusFilter)}
              >
                <option value="all">{t('compliance.filter_all_status')}</option>
                {(['noncompliant', 'unknown', 'compliant', 'not_applicable'] as const).map((s) => (
                  <option key={s} value={s}>
                    {t(`compliance.device_status_${s}`)}
                  </option>
                ))}
              </select>
              <select
                className="form-select"
                aria-label={t('compliance.filter_control')}
                value={controlFilter}
                onChange={(e) => setControlFilter(e.target.value as typeof controlFilter)}
              >
                <option value="all">{t('compliance.filter_all_controls')}</option>
                {activeControls.map((c) => (
                  <option key={c} value={c}>
                    {t('compliance.filter_failing', { control: t(`compliance.control_${c}`) })}
                  </option>
                ))}
              </select>
              <select
                className="form-select"
                aria-label={t('compliance.filter_site')}
                value={siteFilter}
                onChange={(e) => setSiteFilter(e.target.value)}
              >
                <option value="all">{t('compliance.filter_all_sites')}</option>
                {sites.map((s) => (
                  <option key={s} value={s}>
                    {s || t('compliance.no_site')}
                  </option>
                ))}
              </select>
            </div>
            <DataTable label={t('compliance.devices_table')}>
              <thead>
                <tr>
                  <th>{t('compliance.col_device')}</th>
                  <th>{t('compliance.col_site')}</th>
                  <th>{t('compliance.col_status')}</th>
                  {activeControls.map((c) => (
                    <th key={c} className="text-center">
                      {t(`compliance.control_short_${c}`)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={3 + activeControls.length} className="text-center py-8 text-muted">
                      {t('compliance.no_devices')}
                    </td>
                  </tr>
                ) : (
                  filtered.map((d) => (
                    <DeviceRows
                      key={d.device.id}
                      d={d}
                      controls={activeControls}
                      open={expanded === d.device.id}
                      onToggle={() => setExpanded(expanded === d.device.id ? null : d.device.id)}
                    />
                  ))
                )}
              </tbody>
            </DataTable>
          </div>
        </>
      )}

      {profileId && (
        <ComplianceFrameworks profileId={profileId} canGenerate={canGenerate} isAdmin={isAdmin} onGenerated={loadReports} />
      )}

      <ReportsSection reports={reports} onDownload={handleDownload} />
      <VerifySection />

      <div className="table-card compliance-profiles">
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <h3 className="compliance-section-title">{t('compliance.profiles_title')}</h3>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              onClick={() =>
                setEditing({
                  id: '',
                  name: '',
                  description: '',
                  rules: { ...DEFAULT_RULES },
                  scope_site: '',
                  scope_group_id: '',
                  is_default: false,
                  monthly_report: false,
                })
              }
            >
              <Plus size={15} />
              <span>{t('compliance.new_profile')}</span>
            </button>
          )}
        </div>
        {!isAdmin && <p className="text-dim text-sm">{t('compliance.profiles_admin_only')}</p>}
        <ul className="compliance-profile-list">
          {profiles.map((p) => (
            <li key={p.id}>
              <div>
                <strong>{p.name}</strong>
                {p.is_default && <span className="posture-badge posture-neutral">{t('compliance.default')}</span>}
                {p.monthly_report && <span className="posture-badge posture-ok">{t('compliance.monthly')}</span>}
                <div className="text-dim text-sm">
                  {p.scope_site || p.scope_group_id
                    ? t('compliance.scope_limited', {
                        site: p.scope_site || '—',
                        group: p.scope_group_id
                          ? (groups.find((g) => g.id === p.scope_group_id)?.name ?? p.scope_group_id)
                          : '—',
                      })
                    : t('compliance.scope_all')}
                </div>
              </div>
              {isAdmin && (
                <div className="action-buttons">
                  <button type="button" className="btn-action" onClick={() => setEditing({ ...p, rules: { ...p.rules } })}>
                    <Pencil size={14} />
                    <span>{t('compliance.edit')}</span>
                  </button>
                  {!p.is_default && (
                    <button type="button" className="btn-action" onClick={() => setDeleting(p)}>
                      <Trash2 size={14} />
                      <span>{t('compliance.delete')}</span>
                    </button>
                  )}
                </div>
              )}
            </li>
          ))}
        </ul>
        {currentProfile && <RulesSummary rules={currentProfile.rules} />}
      </div>

      {editing && (
        <ProfileEditor profile={editing} onCancel={() => setEditing(null)} onSave={handleSaveProfile} />
      )}
      <ConfirmDialog
        open={Boolean(deleting)}
        title={t('compliance.delete_title')}
        message={t('compliance.delete_message', { name: deleting?.name ?? '' })}
        confirmLabel={t('compliance.delete')}
        onConfirm={handleDeleteProfile}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

function DeviceRows({
  d,
  controls,
  open,
  onToggle,
}: {
  d: ComplianceDeviceResult
  controls: ComplianceControl[]
  open: boolean
  onToggle: () => void
}) {
  const { t } = useT()
  const results = d.controls ?? []
  return (
    <>
      <tr className="compliance-row" onClick={onToggle} data-testid={`compliance-row-${d.device.id}`}>
        <td data-label={t('compliance.col_device')}>
          <button type="button" className="compliance-expand" aria-expanded={open}>
            {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            <span className="host-name">{d.device.hostname}</span>
          </button>
        </td>
        <td data-label={t('compliance.col_site')}>{d.device.site || '—'}</td>
        <td data-label={t('compliance.col_status')}>
          <span className={`posture-badge posture-${statusTone(d.status)}`}>
            {t(`compliance.device_status_${d.status}`)}
          </span>
        </td>
        {controls.map((c) => {
          const r = results.find((x) => x.control === c)
          return (
            <td key={c} className="text-center" data-label={t(`compliance.control_${c}`)} title={r?.reason}>
              {r ? <ControlIcon status={r.status} /> : '—'}
            </td>
          )
        })}
      </tr>
      {open && (
        <tr className="compliance-detail">
          <td colSpan={3 + controls.length}>
            <dl>
              {results.map((r) => (
                <div key={r.control} className="compliance-detail-item">
                  <dt>
                    <span className={`posture-badge posture-${statusTone(r.status)}`}>
                      {t(`compliance.control_status_${r.status}`)}
                    </span>{' '}
                    {t(`compliance.control_${r.control}`)}
                  </dt>
                  <dd>
                    <div>{r.reason}</div>
                    {(r.evidence ?? []).length > 0 && (
                      <ul className="compliance-evidence">
                        {(r.evidence ?? []).map((e, i) => (
                          <li key={i}>{e}</li>
                        ))}
                      </ul>
                    )}
                    {r.observed_at && (
                      <div className="text-dim text-xs">{t('compliance.observed_at', { when: formatWhen(r.observed_at) })}</div>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
          </td>
        </tr>
      )}
    </>
  )
}

function ReportsSection({
  reports,
  onDownload,
}: {
  reports: ComplianceReport[]
  onDownload: (r: ComplianceReport) => void
}) {
  const { t } = useT()
  return (
    <div className="table-card">
      <h3 className="compliance-section-title">{t('compliance.archive_title')}</h3>
      <p className="text-dim text-sm compliance-section-sub">{t('compliance.archive_desc')}</p>
      <DataTable label={t('compliance.archive_title')}>
        <thead>
          <tr>
            <th>{t('compliance.col_generated')}</th>
            <th>{t('compliance.col_type')}</th>
            <th>{t('compliance.col_profile')}</th>
            <th>{t('compliance.col_period')}</th>
            <th>{t('compliance.col_by')}</th>
            <th>{t('compliance.col_score')}</th>
            <th>{t('compliance.col_hash')}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {reports.length === 0 ? (
            <tr>
              <td colSpan={8} className="text-center py-8 text-muted">
                {t('compliance.no_reports')}
              </td>
            </tr>
          ) : (
            reports.map((r) => (
              <tr key={r.id}>
                <td data-label={t('compliance.col_generated')}>
                  <div>{formatWhen(r.generated_at)}</div>
                  <div className="text-dim text-xs font-mono">{r.id}</div>
                </td>
                <td data-label={t('compliance.col_type')}>{r.framework_name || t('compliance.kind_device')}</td>
                <td data-label={t('compliance.col_profile')}>{r.profile_name}</td>
                <td data-label={t('compliance.col_period')}>
                  {r.period}
                  {r.kind === 'scheduled' && (
                    <span className="posture-badge posture-ok compliance-kind">{t('compliance.kind_scheduled')}</span>
                  )}
                </td>
                <td data-label={t('compliance.col_by')}>{r.generated_by}</td>
                <td data-label={t('compliance.col_score')}>
                  {r.framework_summary
                    ? t('compliance.fw_score_short', {
                        supported: r.framework_summary.supported,
                        partial: r.framework_summary.partial,
                        gap: r.framework_summary.gap,
                      })
                    : pct(r.summary?.score)}
                </td>
                <td data-label={t('compliance.col_hash')} className="font-mono text-xs" title={r.file_sha256}>
                  {shortHash(r.file_sha256)}
                </td>
                <td>
                  <button type="button" className="btn-action" onClick={() => onDownload(r)}>
                    <Download size={14} />
                    <span>PDF</span>
                  </button>
                </td>
              </tr>
            ))
          )}
        </tbody>
      </DataTable>
    </div>
  )
}

function VerifySection() {
  const { t } = useT()
  const [hash, setHash] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<ComplianceVerifyResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const canHashInBrowser = typeof crypto !== 'undefined' && Boolean(crypto.subtle)

  const check = async (sum: string) => {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.verifyComplianceReport(sum.trim()))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    try {
      const sum = await sha256OfFile(f)
      setHash(sum)
      await check(sum)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <div className="table-card compliance-verify">
      <h3 className="compliance-section-title">
        <FileCheck2 size={18} /> {t('compliance.verify_title')}
      </h3>
      <p className="text-dim text-sm compliance-section-sub">{t('compliance.verify_desc')}</p>
      <div className="compliance-verify-row">
        {canHashInBrowser ? (
          <label className="btn btn-secondary btn-sm">
            <FileText size={14} />
            <span>{t('compliance.verify_choose')}</span>
            <input type="file" accept="application/pdf,.pdf" className="visually-hidden" onChange={onFile} data-testid="verify-file" />
          </label>
        ) : (
          <span className="text-dim text-sm">{t('compliance.verify_no_crypto')}</span>
        )}
        <input
          type="text"
          className="form-input font-mono"
          placeholder={t('compliance.verify_placeholder')}
          aria-label={t('compliance.verify_placeholder')}
          value={hash}
          onChange={(e) => setHash(e.target.value)}
        />
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy || hash.trim() === ''} onClick={() => check(hash)}>
          {t('compliance.verify_check')}
        </button>
      </div>
      {error && <p className="posture-reason">{error}</p>}
      {result &&
        (result.match && result.report ? (
          <div className="alert-banner alert-success" data-testid="verify-match">
            <CheckCircle2 size={18} />
            <span>
              {t('compliance.verify_match', {
                id: result.report.id,
                by: result.report.generated_by,
                when: formatWhen(result.report.generated_at),
              })}
            </span>
          </div>
        ) : (
          <div className="alert-banner alert-error" data-testid="verify-nomatch">
            <XCircle size={18} />
            <span>{t('compliance.verify_nomatch')}</span>
          </div>
        ))}
    </div>
  )
}

function RulesSummary({ rules }: { rules: ComplianceRules }) {
  const { t } = useT()
  const items: string[] = []
  if (rules.require_disk_encryption) items.push(t('compliance.rule_disk'))
  if (rules.require_antivirus)
    items.push(
      t('compliance.rule_av', {
        rt: rules.require_real_time ? t('compliance.rule_av_rt') : '',
        days: rules.max_signature_age_days || '—',
      }),
    )
  if (rules.require_firewall_all_profiles) items.push(t('compliance.rule_fw'))
  if (rules.require_patches)
    items.push(t('compliance.rule_patch', { days: rules.max_missing_patch_age_days, scan: rules.max_patch_scan_age_days || '—' }))
  if (rules.require_password_policy)
    items.push(t('compliance.rule_pw', { min: rules.min_password_length || '—', lockout: rules.max_lockout_threshold || '—' }))
  if (rules.require_screen_lock) items.push(t('compliance.rule_lock', { min: rules.max_screen_lock_minutes }))
  items.push(t('compliance.rule_fresh', { days: rules.max_posture_age_days || '—' }))
  return (
    <div className="compliance-rules">
      <h4>{t('compliance.rules_of_profile')}</h4>
      <ul>
        {items.map((x) => (
          <li key={x}>{x}</li>
        ))}
      </ul>
    </div>
  )
}

function ProfileEditor({
  profile,
  onCancel,
  onSave,
}: {
  profile: ComplianceProfile
  onCancel: () => void
  onSave: (p: ComplianceProfile) => void
}) {
  const { t } = useT()
  const [p, setP] = useState<ComplianceProfile>(profile)
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api.getDeviceGroups().then(setGroups).catch(() => setGroups([]))
  }, [])

  const setRule = <K extends keyof ComplianceRules>(k: K, v: ComplianceRules[K]) =>
    setP((cur) => ({ ...cur, rules: { ...cur.rules, [k]: v } }))

  const num = (k: keyof ComplianceRules, label: string) => (
    <label className="form-group compliance-num">
      <span className="form-label">{label}</span>
      <input
        type="number"
        min={0}
        max={3650}
        className="form-input"
        value={p.rules[k] as number}
        onChange={(e) => setRule(k, Number(e.target.value) as never)}
      />
    </label>
  )
  const check = (k: keyof ComplianceRules, label: string) => (
    <label className="compliance-check">
      <input type="checkbox" checked={p.rules[k] as boolean} onChange={(e) => setRule(k, e.target.checked as never)} />
      <span>{label}</span>
    </label>
  )

  const submit = async () => {
    setSaving(true)
    try {
      await onSave(p)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open
      onClose={onCancel}
      size="lg"
      title={p.id ? t('compliance.edit_profile', { name: profile.name }) : t('compliance.new_profile')}
      footer={
        <>
          <button type="button" className="btn btn-secondary" onClick={onCancel}>
            {t('compliance.cancel')}
          </button>
          <button type="button" className="btn btn-primary" onClick={submit} disabled={saving || p.name.trim() === ''}>
            {t('compliance.save')}
          </button>
        </>
      }
    >
      <div className="compliance-editor">
        <label className="form-group">
          <span className="form-label">{t('compliance.field_name')}</span>
          <input className="form-input" value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} />
        </label>
        <label className="form-group">
          <span className="form-label">{t('compliance.field_description')}</span>
          <input className="form-input" value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
        </label>
        <div className="form-row">
          <label className="form-group">
            <span className="form-label">{t('compliance.field_site')}</span>
            <input
              className="form-input"
              placeholder={t('compliance.field_site_hint')}
              value={p.scope_site}
              onChange={(e) => setP({ ...p, scope_site: e.target.value })}
            />
          </label>
          <label className="form-group">
            <span className="form-label">{t('compliance.field_group')}</span>
            <select className="form-select" value={p.scope_group_id} onChange={(e) => setP({ ...p, scope_group_id: e.target.value })}>
              <option value="">{t('compliance.field_group_all')}</option>
              {groups.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                </option>
              ))}
            </select>
          </label>
        </div>

        <fieldset>
          <legend>{t('compliance.control_disk_encryption')}</legend>
          {check('require_disk_encryption', t('compliance.field_require_disk'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.control_antivirus')}</legend>
          {check('require_antivirus', t('compliance.field_require_av'))}
          {check('require_real_time', t('compliance.field_require_rt'))}
          {num('max_signature_age_days', t('compliance.field_sig_days'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.control_firewall')}</legend>
          {check('require_firewall_all_profiles', t('compliance.field_require_fw'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.control_patches')}</legend>
          {check('require_patches', t('compliance.field_require_patches'))}
          {num('max_missing_patch_age_days', t('compliance.field_patch_days'))}
          {num('max_patch_scan_age_days', t('compliance.field_scan_days'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.control_password_policy')}</legend>
          {check('require_password_policy', t('compliance.field_require_pw'))}
          {num('min_password_length', t('compliance.field_min_len'))}
          {num('max_lockout_threshold', t('compliance.field_lockout'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.control_screen_lock')}</legend>
          {check('require_screen_lock', t('compliance.field_require_lock'))}
          {num('max_screen_lock_minutes', t('compliance.field_lock_minutes'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.field_freshness')}</legend>
          {num('max_posture_age_days', t('compliance.field_posture_days'))}
        </fieldset>
        <fieldset>
          <legend>{t('compliance.field_options')}</legend>
          <label className="compliance-check">
            <input type="checkbox" checked={p.is_default} disabled={profile.is_default} onChange={(e) => setP({ ...p, is_default: e.target.checked })} />
            <span>{t('compliance.field_default')}</span>
          </label>
          <label className="compliance-check">
            <input type="checkbox" checked={p.monthly_report} onChange={(e) => setP({ ...p, monthly_report: e.target.checked })} />
            <span>{t('compliance.field_monthly')}</span>
          </label>
        </fieldset>
      </div>
    </Modal>
  )
}


