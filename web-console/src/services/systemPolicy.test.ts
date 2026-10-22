import { describe, expect, it } from 'vitest'
import { DEFAULT_CONFIG, explicitDecisions, formatSettingValue, settingRows, toConfig } from './systemPolicy'
import type { SystemPolicySettingsDTO } from '../types/api'

const t = (key: string, vars?: Record<string, string | number>) =>
  vars ? `${key}(${Object.values(vars).join(',')})` : key

describe('systemPolicy helpers', () => {
  // The old form saved usb_storage_mode "allow" for every policy, which is how a
  // wallpaper-only policy lifted a USB block. A new policy decides nothing.
  it('starts a new policy with every setting not set', () => {
    expect(explicitDecisions(DEFAULT_CONFIG)).toEqual([])
    expect(DEFAULT_CONFIG.settings_version).toBe(2)
  })

  it('lists only what a policy decides, including an explicit allow', () => {
    const c = toConfig({ usb_storage_mode: 'allow', disable_task_mgr: true, firewall_enabled: null })
    expect(explicitDecisions(c)).toEqual([
      { key: 'usb_storage_mode', value: 'allow' },
      { key: 'disable_task_mgr', value: true },
    ])
  })

  it('formats decided values in the operator language', () => {
    expect(formatSettingValue('disable_cmd', true, t)).toBe('policy.value_blocked')
    expect(formatSettingValue('disable_cmd', false, t)).toBe('policy.value_allowed')
    expect(formatSettingValue('firewall_enabled', true, t)).toBe('policy.value_enforced')
    expect(formatSettingValue('usb_storage_mode', 'read_only', t)).toBe('policy.value_usb_read_only')
    expect(formatSettingValue('inactivity_timeout_minutes', 0, t)).toBe('policy.value_off')
    expect(formatSettingValue('inactivity_timeout_minutes', 15, t)).toBe('policy.timeout_minutes(15)')
  })

  it('pairs every setting with the policy that decided it, or none', () => {
    const compiled = { usb_storage_mode: 'block', disable_cmd: false } as SystemPolicySettingsDTO
    const fleet = { policy_id: 'p1', policy_name: 'Fleet', target_type: 'all' as const, target_id: '', priority: 100 }
    const rows = settingRows(compiled, { usb_storage_mode: fleet })
    expect(rows).toHaveLength(15)
    expect(rows.find((r) => r.key === 'usb_storage_mode')).toEqual({ key: 'usb_storage_mode', value: 'block', source: fleet })
    expect(rows.find((r) => r.key === 'disable_cmd')?.source).toBeUndefined()
  })
})
