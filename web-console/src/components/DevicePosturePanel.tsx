import { useEffect, useState } from 'react'
import { Flame, HardDrive, History, Info, KeyRound, Lock, ShieldCheck } from 'lucide-react'
import { api } from '../services/api'
import type { DevicePostureResponse, PostureSummaryValue } from '../types/api'
import { useT } from '../i18n/core'

interface Props {
  deviceId: string
  /** Changes whenever a new inventory snapshot lands, so posture refreshes with it. */
  refreshKey?: string
}

function tone(v: PostureSummaryValue): string {
  switch (v) {
    case 'on':
      return 'ok'
    case 'partial':
      return 'warn'
    case 'off':
      return 'bad'
    case 'reported':
      return 'neutral'
    default:
      return 'unknown'
  }
}

function StateBadge({ value }: { value: PostureSummaryValue | undefined }) {
  const { t } = useT()
  const v: PostureSummaryValue = value ?? 'unknown'
  return <span className={`posture-badge posture-${tone(v)}`}>{t(`posture.state_${v}`)}</span>
}

function Reason({ text }: { text?: string }) {
  return text ? <span className="posture-reason">{text}</span> : null
}

/** The security tab of the device modal. It shows what the agent observed and
 *  never fills a gap: a control the agent could not read says so, with the
 *  agent's reason, and a device whose agent predates posture says that instead
 *  of an empty, all-clear-looking panel. */
export function DevicePosturePanel({ deviceId, refreshKey }: Props) {
  const { t } = useT()
  const [data, setData] = useState<DevicePostureResponse | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    setError(null)
    api
      .getDevicePosture(deviceId)
      .then((d) => {
        if (active) setData(d)
      })
      .catch((e: unknown) => {
        if (active) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      active = false
    }
  }, [deviceId, refreshKey])

  if (error) return <p className="posture-reason">{t('posture.load_failed', { error })}</p>
  if (!data) return <p className="posture-collected">{t('posture.loading')}</p>
  if (!data.reported || !data.posture || !data.summary) {
    return (
      <div className="empty-state" data-testid="posture-not-reported">
        <Info size={28} />
        <p>
          <strong>{t('posture.not_reported_title')}</strong>
        </p>
        <p>{t('posture.not_reported_body')}</p>
      </div>
    )
  }

  const p = data.posture
  const s = data.summary
  const pw = p.password_policy
  const avProducts = p.antivirus.products ?? []
  const users = p.logged_on_users ?? []

  return (
    <div className="tab-pane">
      {data.collected_at && (
        <p className="posture-collected">
          {t('posture.collected', { when: new Date(data.collected_at).toLocaleString() })}
        </p>
      )}
      <div className="spec-grid">
        <div className="spec-card" data-testid="posture-disk">
          <div className="spec-header">
            <HardDrive size={16} />
            <h4>{t('posture.disk_encryption')}</h4>
          </div>
          <StateBadge value={s.disk_encryption} />
          <div className="spec-meta">
            <Reason text={p.disk_encryption.reason} />
            {(p.disk_encryption.volumes ?? []).map((v) => (
              <span key={v.mount}>
                {v.mount}
                {v.system ? ` (${t('posture.volume_system')})` : ''}:{' '}
                {v.protected ? t('posture.volume_protected') : t('posture.volume_not_protected')}
                {v.method && v.method !== 'None' ? `, ${v.method}` : ''}
                {v.percent_encrypted !== undefined
                  ? `, ${t('posture.percent', { value: Math.round(v.percent_encrypted) })}`
                  : ''}
                {v.status ? ` — ${v.status}` : ''}
              </span>
            ))}
          </div>
        </div>

        <div className="spec-card" data-testid="posture-av">
          <div className="spec-header">
            <ShieldCheck size={16} />
            <h4>{t('posture.antivirus')}</h4>
          </div>
          <StateBadge value={s.antivirus} />
          <div className="spec-meta">
            <Reason text={p.antivirus.reason} />
            {p.antivirus.state === 'reported' && avProducts.length === 0 && <span>{t('posture.av_none')}</span>}
            {avProducts.map((a) => (
              <span key={a.name}>
                {a.name}: {a.enabled ? t('posture.av_enabled') : t('posture.av_disabled')}
                {a.real_time !== undefined
                  ? `, ${a.real_time ? t('posture.av_realtime_on') : t('posture.av_realtime_off')}`
                  : ''}
                {a.up_to_date !== undefined
                  ? `, ${a.up_to_date ? t('posture.av_up_to_date') : t('posture.av_outdated')}`
                  : ''}
                {a.signature_age_days !== undefined
                  ? `, ${t('posture.av_signature_age', { days: a.signature_age_days })}`
                  : ''}
              </span>
            ))}
          </div>
        </div>

        <div className="spec-card" data-testid="posture-fw">
          <div className="spec-header">
            <Flame size={16} />
            <h4>{t('posture.firewall')}</h4>
          </div>
          <StateBadge value={s.firewall} />
          <div className="spec-meta">
            <Reason text={p.firewall.reason} />
            {(p.firewall.profiles ?? []).map((f) => (
              <span key={f.name}>
                {t(f.enabled ? 'posture.fw_profile_on' : 'posture.fw_profile_off', { name: f.name })}
              </span>
            ))}
          </div>
        </div>

        <div className="spec-card">
          <div className="spec-header">
            <KeyRound size={16} />
            <h4>{t('posture.password_policy')}</h4>
          </div>
          <StateBadge value={s.password_policy} />
          <div className="spec-meta">
            <Reason text={pw.reason} />
            {pw.min_length !== undefined && <span>{t('posture.pw_min_length', { value: pw.min_length })}</span>}
            {pw.max_age_days !== undefined && (
              <span>
                {pw.max_age_days === 0
                  ? t('posture.pw_never_expires')
                  : t('posture.pw_max_age', { value: pw.max_age_days })}
              </span>
            )}
            {pw.history_count !== undefined && <span>{t('posture.pw_history', { value: pw.history_count })}</span>}
            {pw.lockout_threshold !== undefined && (
              <span>
                {pw.lockout_threshold === 0
                  ? t('posture.pw_no_lockout')
                  : t('posture.pw_lockout', { value: pw.lockout_threshold })}
              </span>
            )}
          </div>
        </div>

        <div className="spec-card">
          <div className="spec-header">
            <Lock size={16} />
            <h4>{t('posture.screen_lock')}</h4>
          </div>
          <StateBadge value={s.screen_lock} />
          <div className="spec-meta">
            <Reason text={p.screen_lock.reason} />
            {p.screen_lock.state === 'reported' && (
              <span>
                {p.screen_lock.timeout_seconds
                  ? t('posture.lock_timeout', { value: Math.round(p.screen_lock.timeout_seconds / 60) })
                  : t('posture.lock_not_enforced')}
              </span>
            )}
          </div>
        </div>

        <div className="spec-card">
          <div className="spec-header">
            <Info size={16} />
            <h4>{t('posture.other')}</h4>
          </div>
          <div className="spec-meta">
            {p.last_boot_utc && (
              <span>{t('posture.last_boot', { value: new Date(p.last_boot_utc).toLocaleString() })}</span>
            )}
            {p.bios_release_date && <span>{t('posture.bios_date', { value: p.bios_release_date })}</span>}
            {users.length > 0 && <span>{t('posture.users', { value: users.join(', ') })}</span>}
          </div>
        </div>
      </div>

      {(data.history ?? []).length > 0 && (
        <div className="posture-history">
          <h4 className="section-title">
            <History size={16} />
            {t('posture.history_title')}
          </h4>
          <ul>
            {(data.history ?? []).map((h) => (
              <li key={h.changed_at}>
                <span className="posture-history-when">{new Date(h.changed_at).toLocaleString()}</span>
                <span>{t('posture.disk_encryption')}</span>
                <StateBadge value={h.disk_encryption} />
                <span>{t('posture.antivirus')}</span>
                <StateBadge value={h.antivirus} />
                <span>{t('posture.firewall')}</span>
                <StateBadge value={h.firewall} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
