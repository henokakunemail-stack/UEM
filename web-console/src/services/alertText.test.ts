import { describe, expect, it } from 'vitest'
import { dashboardAlertText, incidentText } from './alertText'
import { translate } from '../i18n/core'
import type { AlertIncidentDTO, AlertItem } from '../types/api'

const tID = (key: string, vars?: Record<string, string | number>) => translate('id', key, vars)

const incident = (over: Partial<AlertIncidentDTO>): AlertIncidentDTO => ({
  id: 'i1', rule_id: 'r1', severity: 'warning', device_id: 'd1', hostname: 'PC-1',
  title: 'Low Disk Space: PC-1 (5.0% free)', message: 'English text', status: 'open',
  trigger_count: 1, first_triggered_at: '', last_triggered_at: '', ...over,
})

describe('alert text in the reader language', () => {
  it('words an incident from its rule type and measured value', () => {
    expect(incidentText(incident({ rule_type: 'disk_low', value: 5, threshold: 15 }), tID)).toBe('PC-1: sisa disk 5.0% (ambang 15%)')
    expect(incidentText(incident({ rule_type: 'device_offline', value: 42.4 }), tID)).toBe('PC-1: offline selama 42 menit')
    expect(incidentText(incident({ rule_type: 'critical_patch', value: 3 }), tID)).toBe('PC-1: 3 patch kritis belum terpasang')
  })

  it('falls back to the server text when it cannot', () => {
    expect(incidentText(incident({ rule_type: 'disk_low', value: null }), tID)).toBe('Low Disk Space: PC-1 (5.0% free)')
    expect(incidentText(incident({ rule_type: 'ram_high', value: 9 }), tID)).toBe('Low Disk Space: PC-1 (5.0% free)')
  })

  it('words a dashboard disk alert', () => {
    const a = { id: 'a', type: 'low_disk', severity: 'warning', device_id: 'd', hostname: 'h', site: '', message: 'English', timestamp: '', value: 3 } as AlertItem
    expect(dashboardAlertText(a, tID)).toBe('Sisa ruang disk rendah: 3.0% tersisa')
  })
})
