import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, ChevronDown, ChevronRight, FileText, Scale } from 'lucide-react'
import { api } from '../services/api'
import { DataTable } from './ui/DataTable'
import { useT } from '../i18n/core'
import { useToast } from '../context/ToastContext'
import type {
  ComplianceFrameworkAssessment,
  ComplianceFrameworkInfo,
  ComplianceFrameworkList,
  FrameworkStatus,
} from '../types/api'

interface Props {
  profileId: string
  canGenerate: boolean
  isAdmin: boolean
  /** Called after a framework report is archived, so the archive reloads. */
  onGenerated: () => void
}

function tone(s: FrameworkStatus): string {
  switch (s) {
    case 'supported':
      return 'ok'
    case 'partial':
      return 'warn'
    case 'gap':
      return 'bad'
    case 'out_of_scope':
      return 'neutral'
    default:
      return 'unknown'
  }
}

function thisMonth(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

/** One choice per family: a family with several versions (BPR) is picked by
 *  period on the server, so the console offers the family, not each file. */
function choices(list: ComplianceFrameworkInfo[]): { key: string; label: string }[] {
  const byFamily = new Map<string, ComplianceFrameworkInfo[]>()
  list.forEach((f) => byFamily.set(f.family, [...(byFamily.get(f.family) ?? []), f]))
  return Array.from(byFamily.entries()).map(([family, fs]) =>
    fs.length === 1 ? { key: fs[0].id, label: fs[0].name } : { key: family, label: fs.map((f) => f.name).join(' / ') },
  )
}

export function ComplianceFrameworks({ profileId, canGenerate, isAdmin, onGenerated }: Props) {
  const { t } = useT()
  const toast = useToast()
  const [list, setList] = useState<ComplianceFrameworkList | null>(null)
  const [key, setKey] = useState('')
  const [period, setPeriod] = useState(thisMonth())
  const [data, setData] = useState<ComplianceFrameworkAssessment | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [generating, setGenerating] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const [showOut, setShowOut] = useState(false)

  useEffect(() => {
    api
      .getComplianceFrameworks()
      .then((l) => {
        setList(l)
        const first = choices(l.frameworks ?? [])[0]
        if (first) setKey(first.key)
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  const opts = useMemo(() => choices(list?.frameworks ?? []), [list])

  const assess = async () => {
    if (!key || !profileId) return
    setLoading(true)
    setError(null)
    try {
      setData(await api.getFrameworkAssessment(key, profileId, period))
    } catch (e: unknown) {
      setData(null)
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  const generate = async () => {
    setGenerating(true)
    try {
      const rep = await api.generateFrameworkReport(key, profileId, period)
      toast.success(t('compliance.generated', { id: rep.id }), t('compliance.generated_title'))
      onGenerated()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : String(e), t('compliance.generate_failed'))
    } finally {
      setGenerating(false)
    }
  }

  const inScope = (data?.controls ?? []).filter((c) => c.status !== 'out_of_scope')
  const outScope = (data?.controls ?? []).filter((c) => c.status === 'out_of_scope')

  return (
    <div className="table-card compliance-frameworks" data-testid="compliance-frameworks">
      <h3 className="compliance-section-title">
        <Scale size={18} /> {t('compliance.fw_title')}
      </h3>
      <p className="text-dim text-sm compliance-section-sub">{t('compliance.fw_desc')}</p>

      {isAdmin && list && (list.errors ?? []).length > 0 && (
        <div className="alert-banner alert-error" role="alert">
          <AlertTriangle size={18} />
          <div>
            <strong>{t('compliance.fw_template_errors')}</strong>
            <ul className="compliance-evidence">
              {list.errors.map((e) => (
                <li key={e.file}>
                  {e.file}: {e.error}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}

      <div className="compliance-toolbar compliance-fw-toolbar">
        <select className="form-select" aria-label={t('compliance.fw_choose')} value={key} onChange={(e) => setKey(e.target.value)}>
          {opts.map((o) => (
            <option key={o.key} value={o.key}>
              {o.label}
            </option>
          ))}
        </select>
        <input
          type="month"
          className="form-input"
          aria-label={t('compliance.fw_period')}
          value={period}
          onChange={(e) => setPeriod(e.target.value)}
        />
        <button type="button" className="btn btn-secondary btn-sm" onClick={assess} disabled={loading || !key}>
          {loading ? t('compliance.fw_assessing') : t('compliance.fw_assess')}
        </button>
        {canGenerate && (
          <button type="button" className="btn btn-primary btn-sm" onClick={generate} disabled={generating || !key}>
            <FileText size={14} />
            <span>{generating ? t('compliance.generating') : t('compliance.fw_generate')}</span>
          </button>
        )}
      </div>
      {isAdmin && list && <p className="text-dim text-xs">{t('compliance.fw_template_dir', { dir: list.template_dir })}</p>}
      {error && <p className="posture-reason">{error}</p>}

      {data && (
        <>
          <p className="compliance-scope">
            {data.template.name} · {data.template.regulation} · {t('compliance.fw_reviewed', { date: data.template.reviewed_at })}
            {data.template.origin === 'custom' && ` · ${t('compliance.fw_custom', { file: data.template.file ?? '' })}`}
          </p>
          <div className="compliance-fw-counts" data-testid="fw-counts">
            {(['supported', 'partial', 'gap', 'no_data', 'out_of_scope'] as const).map((s) => (
              <span key={s} className={`posture-badge posture-${tone(s)}`}>
                {t(`compliance.fw_status_${s}`)}: {data.summary[s]}
              </span>
            ))}
          </div>
          {(data.template.pending ?? []).length > 0 && (
            <ul className="compliance-evidence">
              {(data.template.pending ?? []).map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
          <DataTable label={t('compliance.fw_title')}>
            <thead>
              <tr>
                <th>{t('compliance.fw_col_ref')}</th>
                <th>{t('compliance.fw_col_control')}</th>
                <th>{t('compliance.col_status')}</th>
              </tr>
            </thead>
            <tbody>
              {inScope.map((c) => (
                <FrameworkRow key={c.ref} c={c} open={open === c.ref} onToggle={() => setOpen(open === c.ref ? null : c.ref)} />
              ))}
            </tbody>
          </DataTable>
          {outScope.length > 0 && (
            <div className="compliance-fw-out">
              <button type="button" className="compliance-expand" aria-expanded={showOut} onClick={() => setShowOut(!showOut)}>
                {showOut ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                {t('compliance.fw_out_of_scope', { count: outScope.length })}
              </button>
              {showOut && (
                <ul className="compliance-evidence">
                  {outScope.map((c) => (
                    <li key={c.ref}>
                      <strong>{c.ref_verified ? c.ref : t('compliance.fw_ref_unverified')}</strong> {c.title}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          <p className="text-dim text-xs">{t('compliance.fw_disclaimer')}</p>
        </>
      )}
    </div>
  )
}

function FrameworkRow({
  c,
  open,
  onToggle,
}: {
  c: ComplianceFrameworkAssessment['controls'][number]
  open: boolean
  onToggle: () => void
}) {
  const { t } = useT()
  return (
    <>
      <tr className="compliance-row" onClick={onToggle} data-testid={`fw-row-${c.ref}`}>
        <td>
          <button type="button" className="compliance-expand" aria-expanded={open}>
            {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            <strong>{c.ref_verified ? c.ref : t('compliance.fw_ref_unverified')}</strong>
          </button>
        </td>
        <td>{c.title}</td>
        <td>
          <span className={`posture-badge posture-${tone(c.status)}`}>{t(`compliance.fw_status_${c.status}`)}</span>
        </td>
      </tr>
      {open && (
        <tr className="compliance-detail">
          <td colSpan={3}>
            <dl>
              {(c.evidence ?? []).map((e) => (
                <div key={e.source} className="compliance-detail-item">
                  <dt>
                    <span className={`posture-badge posture-${tone(e.status)}`}>{t(`compliance.fw_status_${e.status}`)}</span> {e.title}
                  </dt>
                  <dd>
                    <div>{e.summary}</div>
                    {(e.details ?? []).length > 0 && (
                      <ul className="compliance-evidence">
                        {(e.details ?? []).map((d, i) => (
                          <li key={i}>{d}</li>
                        ))}
                      </ul>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
            {c.note && <p className="text-dim text-xs">{c.note}</p>}
          </td>
        </tr>
      )}
    </>
  )
}
