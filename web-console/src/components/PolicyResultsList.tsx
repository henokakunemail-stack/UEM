import React from 'react'
import { useT } from '../i18n/core'
import { SETTING_KEYS, settingLabelKey, type SettingKey } from '../services/systemPolicy'
import type { SettingResultDTO } from '../types/api'

/**
 * The settings a device did not enforce, and why. A device used to report one
 * status for the whole policy, so a Linux laptop that skipped every Windows-only
 * restriction showed the same "Enforced" as a Windows machine that applied them.
 */
export const PolicyResultsList: React.FC<{ results?: SettingResultDTO[] }> = ({ results }) => {
  const { t } = useT()
  const gaps = (results ?? []).filter((r) => r.outcome !== 'applied')
  if (gaps.length === 0) return null
  const label = (setting: string) =>
    (SETTING_KEYS as readonly string[]).includes(setting) ? t(settingLabelKey(setting as SettingKey)) : setting
  return (
    <details className="mt-1">
      <summary className="text-xs text-muted cursor-pointer">
        {t('policy.results_summary', { count: gaps.length })}
      </summary>
      <ul className="text-xs mt-1 space-y-0.5">
        {gaps.map((r) => (
          <li key={r.setting}>
            <span className="text-main font-medium">{label(r.setting)}</span>
            {': '}
            <span className={r.outcome === 'failed' ? 'text-red-400' : 'text-muted'}>
              {t(r.outcome === 'failed' ? 'policy.outcome_failed' : 'policy.outcome_unsupported')}
            </span>
            {r.reason && <span className="text-muted"> ({r.reason})</span>}
          </li>
        ))}
      </ul>
    </details>
  )
}
