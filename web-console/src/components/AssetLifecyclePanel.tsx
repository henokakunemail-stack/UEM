import React, { useCallback, useEffect, useState } from 'react'
import { Download, RefreshCw, Save } from 'lucide-react'
import { DataTable } from './ui/DataTable'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { useT } from '../i18n/core'
import type { AssetCategory, BudgetLine, LifecycleReport, LifecycleSettings, LifecycleUnit } from '../types/api'

const CATEGORIES: AssetCategory[] = ['laptop', 'desktop', 'server', 'other']

const rupiah = (v: number) =>
  new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(v)

const date = (iso?: string) => {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString()
}

const gb = (b?: number) => (b === undefined ? '—' : `${(b / 2 ** 30).toFixed(1)} GB`)

function unitName(u: LifecycleUnit): string {
  return [u.asset_tag, u.hostname].filter(Boolean).join(' / ') || u.device_id || '—'
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

/** The asset lifecycle dashboard: age, replacement budget, book value,
 *  spec floor, wasted license seats and what expires soon. Every age shows
 *  where it came from, because a BIOS date is an estimate, not a fact. */
export const AssetLifecyclePanel: React.FC = () => {
  const { t, lang } = useT()
  const toast = useToast()
  const { can } = usePermission()
  const isAdmin = can('admin')
  const [rep, setRep] = useState<LifecycleReport | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [downloading, setDownloading] = useState(false)
  const [draft, setDraft] = useState<LifecycleSettings | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const r = await api.getAssetLifecycle()
      setRep(r)
      setDraft(r.settings)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const download = async () => {
    setDownloading(true)
    try {
      const blob = await api.downloadLifecycleReport()
      await saveBlob(blob, `ringkasan-aset-${new Date().toISOString().slice(0, 10)}.pdf`)
    } catch (e) {
      toast.error(t('lifecycle.download_failed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setDownloading(false)
    }
  }

  const save = async () => {
    if (!draft) return
    setSaving(true)
    try {
      await api.saveLifecycleSettings(draft)
      toast.success(t('lifecycle.settings_saved'))
      await load()
    } catch (e) {
      toast.error(t('lifecycle.settings_failed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setSaving(false)
    }
  }

  if (loading && !rep) return <div className="table-card lifecycle-section">{t('lifecycle.loading')}</div>
  if (error && !rep) {
    return (
      <div className="alert-banner alert-error" role="alert">
        <span>{t('lifecycle.load_failed', { error })}</span>
      </div>
    )
  }
  if (!rep) return null

  const f = rep.forecast
  const maxBucket = Math.max(1, ...rep.age_buckets.map((b) => b.count))
  const maxYear = Math.max(1, ...f.years.map((y) => y.cost))
  const overdueUnits = rep.units.filter((u) => u.overdue)
  const belowSpec = rep.units.filter((u) => u.below_spec.length > 0)
  const lineCost = (l: BudgetLine) =>
    l.units_without_cost > 0
      ? t('lifecycle.cost_with_missing', { cost: rupiah(l.cost), n: l.units_without_cost })
      : rupiah(l.cost)
  const ageText = (u: LifecycleUnit) =>
    u.age_years === undefined
      ? t('lifecycle.age_unknown')
      : t('lifecycle.age_value', { years: u.age_years.toLocaleString(lang === 'id' ? 'id-ID' : 'en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 }), source: t(`lifecycle.source_${u.age_source}`) })

  return (
    <div className="lifecycle">
      <div className="lifecycle-toolbar">
        <span className="text-dim">{t('lifecycle.as_of', { when: new Date(rep.generated_at).toLocaleString() })}</span>
        <button type="button" className="btn btn-secondary" onClick={() => void load()} disabled={loading}>
          <RefreshCw size={16} className={loading ? 'spinning' : ''} />
          <span>{t('lifecycle.refresh')}</span>
        </button>
        <button type="button" className="btn btn-primary" onClick={() => void download()} disabled={downloading}>
          <Download size={16} />
          <span>{downloading ? t('lifecycle.downloading') : t('lifecycle.download_pdf')}</span>
        </button>
      </div>

      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header"><span className="kpi-label">{t('lifecycle.kpi_units')}</span></div>
          <div className="kpi-value">{rep.units.length}</div>
          <span className="kpi-hint">{t('lifecycle.kpi_units_hint', { n: f.missing_age })}</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header"><span className="kpi-label">{t('lifecycle.kpi_overdue')}</span></div>
          <div className={rep.overdue > 0 ? 'kpi-value text-danger' : 'kpi-value'}>{rep.overdue}</div>
          <span className="kpi-hint">{lineCost(f.overdue)}</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header"><span className="kpi-label">{t('lifecycle.kpi_book')}</span></div>
          <div className="kpi-value">{rupiah(rep.book_value.book_total)}</div>
          <span className="kpi-hint">
            {t('lifecycle.kpi_book_hint', { purchase: rupiah(rep.book_value.purchase_total), n: rep.book_value.assets })}
          </span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header"><span className="kpi-label">{t('lifecycle.kpi_waste')}</span></div>
          <div className={rep.unused_seat_value + rep.allocated_not_installed_value > 0 ? 'kpi-value text-warning' : 'kpi-value'}>
            {rupiah(rep.unused_seat_value + rep.allocated_not_installed_value)}
          </div>
          <span className="kpi-hint">{t('lifecycle.kpi_waste_hint')}</span>
        </div>
      </div>

      <div className="lifecycle-grid">
        <section className="table-card lifecycle-section" aria-labelledby="lc-age">
          <h3 id="lc-age" className="lifecycle-title">{t('lifecycle.age_title')}</h3>
          <ul className="lifecycle-bars">
            {rep.age_buckets.map((b) => (
              <li key={b.key}>
                <span>{t(`lifecycle.bucket_${b.key.replace('+', 'plus').replace('-', '_')}`)}</span>
                <span className="lifecycle-bar"><span style={{ width: `${(b.count / maxBucket) * 100}%` }} /></span>
                <span className="lifecycle-count">{b.count}</span>
              </li>
            ))}
          </ul>
          <h4 className="lifecycle-sub">{t('lifecycle.sources_title')}</h4>
          <ul className="lifecycle-sources">
            {(['purchase', 'bios', 'os_install', 'unknown'] as const).map((s) => (
              <li key={s}>
                <span className={s === 'purchase' ? 'posture-badge posture-ok' : s === 'unknown' ? 'posture-badge posture-neutral' : 'posture-badge posture-warn'}>
                  {t(`lifecycle.source_${s}`)}
                </span>
                <span>{rep.age_sources[s]}</span>
              </li>
            ))}
          </ul>
          {f.missing_age > 0 && <p className="text-dim lifecycle-note">{t('lifecycle.missing_age', { n: f.missing_age })}</p>}
        </section>

        <section className="table-card lifecycle-section" aria-labelledby="lc-budget">
          <h3 id="lc-budget" className="lifecycle-title">{t('lifecycle.budget_title')}</h3>
          <ul className="lifecycle-bars">
            <li>
              <span>{t('lifecycle.budget_overdue')}</span>
              <span className="lifecycle-bar lifecycle-bar-bad"><span style={{ width: f.overdue.cost > 0 ? '100%' : '0%' }} /></span>
              <span className="lifecycle-count">{t('lifecycle.units_cost', { n: f.overdue.units, cost: lineCost(f.overdue) })}</span>
            </li>
            {f.years.map((y) => (
              <li key={y.label}>
                <span>{y.label}</span>
                <span className="lifecycle-bar"><span style={{ width: `${(y.cost / maxYear) * 100}%` }} /></span>
                <span className="lifecycle-count">{t('lifecycle.units_cost', { n: y.units, cost: lineCost(y) })}</span>
              </li>
            ))}
          </ul>
          <details className="lifecycle-quarters">
            <summary>{t('lifecycle.quarters')}</summary>
            <DataTable label={t('lifecycle.quarters')}>
              <table className="data-table">
                <thead>
                  <tr><th>{t('lifecycle.col_quarter')}</th><th>{t('lifecycle.col_units')}</th><th>{t('lifecycle.col_cost')}</th></tr>
                </thead>
                <tbody>
                  {f.quarters.map((q) => (
                    <tr key={q.label}><td>{q.label}</td><td>{q.units}</td><td>{lineCost(q)}</td></tr>
                  ))}
                </tbody>
              </table>
            </DataTable>
          </details>
          <p className="text-dim lifecycle-note">{t('lifecycle.budget_note')}</p>
        </section>
      </div>

      <section className="table-card lifecycle-section" aria-labelledby="lc-overdue">
        <h3 id="lc-overdue" className="lifecycle-title">{t('lifecycle.overdue_title', { n: overdueUnits.length })}</h3>
        {overdueUnits.length === 0 ? (
          <p className="text-dim">{t('lifecycle.none')}</p>
        ) : (
          <DataTable label={t('lifecycle.overdue_title', { n: overdueUnits.length })}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('lifecycle.col_unit')}</th><th>{t('lifecycle.col_category')}</th><th>{t('lifecycle.col_age')}</th>
                  <th>{t('lifecycle.col_due')}</th><th>{t('lifecycle.col_replacement')}</th>
                </tr>
              </thead>
              <tbody>
                {overdueUnits.map((u) => (
                  <tr key={(u.asset_id || '') + (u.device_id || '')}>
                    <td>{unitName(u)}</td>
                    <td>{t(`lifecycle.cat_${u.category}`)}</td>
                    <td>{ageText(u)}</td>
                    <td>{date(u.due_date)}</td>
                    <td>{u.replacement_cost === undefined ? t('lifecycle.cost_unset') : rupiah(u.replacement_cost)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </DataTable>
        )}
      </section>

      <section className="table-card lifecycle-section" aria-labelledby="lc-spec">
        <h3 id="lc-spec" className="lifecycle-title">{t('lifecycle.spec_title', { n: belowSpec.length })}</h3>
        <p className="text-dim lifecycle-note">
          {t('lifecycle.spec_rule', { ram: rep.settings.min_ram_gb, disk: rep.settings.min_disk_free_percent })}
        </p>
        {belowSpec.length > 0 && (
          <DataTable label={t('lifecycle.spec_title', { n: belowSpec.length })}>
            <table className="data-table">
              <thead>
                <tr><th>{t('lifecycle.col_unit')}</th><th>{t('lifecycle.col_ram')}</th><th>{t('lifecycle.col_disk')}</th><th>{t('lifecycle.col_issue')}</th></tr>
              </thead>
              <tbody>
                {belowSpec.map((u) => (
                  <tr key={(u.asset_id || '') + (u.device_id || '')}>
                    <td>{unitName(u)}</td>
                    <td>{gb(u.ram_bytes)}</td>
                    <td>{u.disk_free_pct === undefined ? '—' : `${Math.round(u.disk_free_pct)}%`}</td>
                    <td>{u.below_spec.map((s) => t(`lifecycle.issue_${s}`)).join(', ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </DataTable>
        )}
      </section>

      <section className="table-card lifecycle-section" aria-labelledby="lc-lic">
        <h3 id="lc-lic" className="lifecycle-title">{t('lifecycle.licenses_title')}</h3>
        {rep.licenses.length === 0 ? (
          <p className="text-dim">{t('lifecycle.no_licenses')}</p>
        ) : (
          <DataTable label={t('lifecycle.licenses_title')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('lifecycle.col_software')}</th><th>{t('lifecycle.col_seats')}</th><th>{t('lifecycle.col_allocated')}</th>
                  <th>{t('lifecycle.col_installed')}</th><th>{t('lifecycle.col_unused')}</th><th>{t('lifecycle.col_alloc_no_install')}</th>
                </tr>
              </thead>
              <tbody>
                {rep.licenses.map((l) => (
                  <tr key={l.license_id}>
                    <td>{l.software_name}</td>
                    <td>{l.total_seats}</td>
                    <td>{l.allocated}</td>
                    <td>{l.installed}</td>
                    {l.applicable ? (
                      <>
                        <td>{t('lifecycle.seats_value', { n: l.unused_seats, value: l.unused_value === undefined ? t('lifecycle.cost_unset') : rupiah(l.unused_value) })}</td>
                        <td>
                          {t('lifecycle.alloc_value', {
                            n: l.allocated_not_installed,
                            value: l.allocated_not_installed_value === undefined ? t('lifecycle.cost_unset') : rupiah(l.allocated_not_installed_value),
                          })}
                          {l.allocated_unverified > 0 && (
                            <span className="text-dim"> · {t('lifecycle.unverified', { n: l.allocated_unverified })}</span>
                          )}
                        </td>
                      </>
                    ) : (
                      <td colSpan={2} className="text-dim">{t('lifecycle.site_license')}</td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </DataTable>
        )}
        <p className="text-dim lifecycle-note">{t('lifecycle.licenses_note')}</p>
      </section>

      <div className="lifecycle-grid">
        <section className="table-card lifecycle-section" aria-labelledby="lc-lexp">
          <h3 id="lc-lexp" className="lifecycle-title">{t('lifecycle.expiring_licenses')}</h3>
          {rep.expiring_licenses.length === 0 ? (
            <p className="text-dim">{t('lifecycle.none')}</p>
          ) : (
            <ul className="lifecycle-list">
              {rep.expiring_licenses.map((e) => (
                <li key={e.license_id}>
                  <span>{e.software_name}</span>
                  <span className={e.days_left < 0 ? 'posture-badge posture-bad' : 'posture-badge posture-warn'}>
                    {e.days_left < 0 ? t('lifecycle.expired_ago', { n: -e.days_left }) : t('lifecycle.days_left', { n: e.days_left })}
                  </span>
                  <span className="text-dim">{date(e.expires_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <section className="table-card lifecycle-section" aria-labelledby="lc-wexp">
          <h3 id="lc-wexp" className="lifecycle-title">{t('lifecycle.expiring_warranties')}</h3>
          <p className="text-dim lifecycle-note">{t('lifecycle.warranties_expired', { n: rep.warranties_expired })}</p>
          {rep.expiring_warranties.length === 0 ? (
            <p className="text-dim">{t('lifecycle.none')}</p>
          ) : (
            <ul className="lifecycle-list">
              {rep.expiring_warranties.map((w) => (
                <li key={w.asset_id}>
                  <span>{[w.asset_tag, w.hostname].filter(Boolean).join(' / ')}</span>
                  <span className="posture-badge posture-warn">{t('lifecycle.days_left', { n: w.days_left })}</span>
                  <span className="text-dim">{date(w.expires_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      {draft && (
        <section className="table-card lifecycle-section" aria-labelledby="lc-settings">
          <h3 id="lc-settings" className="lifecycle-title">{t('lifecycle.settings_title')}</h3>
          {!isAdmin && <p className="text-dim lifecycle-note">{t('lifecycle.settings_admin_only')}</p>}
          <div className="lifecycle-settings">
            {CATEGORIES.map((c) => (
              <fieldset key={c} disabled={!isAdmin}>
                <legend>{t(`lifecycle.cat_${c}`)}</legend>
                <label className="form-label">
                  {t('lifecycle.useful_life')}
                  <input
                    type="number"
                    className="form-input"
                    min={1}
                    max={15}
                    value={draft.categories[c].useful_life_years}
                    onChange={(e) =>
                      setDraft({ ...draft, categories: { ...draft.categories, [c]: { ...draft.categories[c], useful_life_years: Number(e.target.value) } } })
                    }
                  />
                </label>
                <label className="form-label">
                  {t('lifecycle.replacement_cost')}
                  <input
                    type="number"
                    className="form-input"
                    min={0}
                    step={100000}
                    value={draft.categories[c].replacement_cost}
                    onChange={(e) =>
                      setDraft({ ...draft, categories: { ...draft.categories, [c]: { ...draft.categories[c], replacement_cost: Number(e.target.value) } } })
                    }
                  />
                </label>
              </fieldset>
            ))}
            <fieldset disabled={!isAdmin}>
              <legend>{t('lifecycle.spec_floor')}</legend>
              <label className="form-label">
                {t('lifecycle.min_ram')}
                <input type="number" className="form-input" min={0} max={1024} value={draft.min_ram_gb}
                  onChange={(e) => setDraft({ ...draft, min_ram_gb: Number(e.target.value) })} />
              </label>
              <label className="form-label">
                {t('lifecycle.min_disk')}
                <input type="number" className="form-input" min={0} max={100} value={draft.min_disk_free_percent}
                  onChange={(e) => setDraft({ ...draft, min_disk_free_percent: Number(e.target.value) })} />
              </label>
            </fieldset>
          </div>
          <p className="text-dim lifecycle-note">{t('lifecycle.settings_note')}</p>
          {isAdmin && (
            <button type="button" className="btn btn-primary" onClick={() => void save()} disabled={saving}>
              <Save size={16} />
              <span>{saving ? t('lifecycle.saving') : t('lifecycle.save')}</span>
            </button>
          )}
        </section>
      )}
    </div>
  )
}
