import type { AlertIncidentDTO, AlertItem } from '../types/api'
import type { Vars } from '../i18n/core'

type TFn = (key: string, vars?: Vars) => string

// Alert text is built on the server in English. These rebuild it from the
// rule type and the measured value in the reader's language, and fall back to
// the server's text for anything they do not know.

export function incidentText(i: AlertIncidentDTO, t: TFn): string {
  const host = i.hostname || i.device_id
  const v = i.value
  if (v === null || v === undefined) return i.title || i.message
  switch (i.rule_type) {
    case 'disk_low':
      return t('alerts.text_disk_low', { host, free: v.toFixed(1), threshold: i.threshold ?? '' })
    case 'device_offline':
      return t('alerts.text_offline', { host, minutes: Math.round(v) })
    case 'critical_patch':
      return t('alerts.text_patches', { host, count: Math.round(v) })
    default:
      return i.title || i.message
  }
}

export function dashboardAlertText(a: AlertItem, t: TFn): string {
  if (a.type === 'low_disk' && a.value !== null && a.value !== undefined) {
    return t('dashboard.alert_low_disk', { free: a.value.toFixed(1) })
  }
  if (a.type === 'offline_long') {
    return t('dashboard.alert_offline_since', { date: new Date(a.timestamp).toLocaleString() })
  }
  return a.message
}
