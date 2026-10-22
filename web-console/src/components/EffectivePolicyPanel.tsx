import React, { useEffect, useState } from 'react'
import { api } from '../services/api'
import { DataTable } from './ui/DataTable'
import { useT } from '../i18n/core'
import { formatSettingValue, settingLabelKey, settingRows } from '../services/systemPolicy'
import type { DeviceDTO, DeviceGroupDTO, DeviceSystemPolicyDTO, SettingSourceDTO } from '../types/api'

interface Props {
  devices: DeviceDTO[]
  groups: DeviceGroupDTO[]
}

/**
 * What one device enforces and which policy decided each setting. With several
 * policies aimed at a device (fleet, groups, the device itself) an operator had
 * no way to tell which one had won, which is how a lifted USB block went
 * unnoticed.
 */
export const EffectivePolicyPanel: React.FC<Props> = ({ devices, groups }) => {
  const { t } = useT()
  const [deviceId, setDeviceId] = useState('')
  const [data, setData] = useState<DeviceSystemPolicyDTO | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)

  useEffect(() => {
    if (!deviceId) {
      setData(null)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(false)
    api
      .getDeviceSystemPolicy(deviceId)
      .then((d) => {
        if (!cancelled) setData(d)
      })
      .catch(() => {
        if (!cancelled) {
          setData(null)
          setError(true)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [deviceId])

  const scopeText = (src: SettingSourceDTO) => {
    if (src.target_type === 'all') return t('policy.target_entire_fleet')
    if (src.target_type === 'group') {
      const g = groups.find((x) => x.id === src.target_id)
      return t('policy.target_group', { name: g ? g.name : src.target_id.substring(0, 8) })
    }
    return t('policy.scope_device')
  }

  return (
    <div className="table-card">
      <div className="card-header-bar">
        <div className="form-group" style={{ minWidth: '18rem' }}>
          <label className="form-label" htmlFor="effective-device">
            {t('policy.effective_device_label')}
          </label>
          <select
            id="effective-device"
            className="form-select"
            value={deviceId}
            onChange={(e) => setDeviceId(e.target.value)}
          >
            <option value="">{t('policy.select_device_placeholder')}</option>
            {devices.map((d) => (
              <option key={d.id} value={d.id}>
                {d.hostname}
              </option>
            ))}
          </select>
        </div>
        <span className="text-sm text-dim">{t('policy.effective_intro')}</span>
      </div>

      {error && (
        <p className="text-danger" role="alert">
          {t('policy.effective_load_failed')}
        </p>
      )}

      <DataTable label={t('policy.effective_table_label')}>
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('policy.col_setting')}</th>
              <th>{t('policy.col_effective_value')}</th>
              <th>{t('policy.col_decided_by')}</th>
            </tr>
          </thead>
          <tbody>
            {!deviceId || !data ? (
              <tr>
                <td colSpan={3} className="text-center py-6 text-muted">
                  {loading ? t('common.loading') : t('policy.effective_pick_device')}
                </td>
              </tr>
            ) : (
              settingRows(data.compiled.settings, data.sources).map((row) => (
                <tr key={row.key}>
                  <td className="text-main">{t(settingLabelKey(row.key))}</td>
                  <td>
                    {row.source ? (
                      <span className="font-medium text-main">{formatSettingValue(row.key, row.value, t)}</span>
                    ) : (
                      <span className="text-muted">{t('policy.not_configured')}</span>
                    )}
                  </td>
                  <td>
                    {row.source ? (
                      <>
                        <div className="text-main">{row.source.policy_name}</div>
                        <div className="text-xs text-muted">
                          {scopeText(row.source)} · {t('policy.col_priority')} {row.source.priority}
                        </div>
                      </>
                    ) : (
                      <span className="text-xs text-muted">{t('policy.decided_by_nobody')}</span>
                    )}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </DataTable>
    </div>
  )
}
