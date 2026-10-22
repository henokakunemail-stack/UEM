import type {
  SettingSourceDTO,
  SystemPolicyConfigDTO,
  SystemPolicySettingsDTO,
} from '../types/api'

type Translate = (key: string, vars?: Record<string, string | number>) => string

// Every decision a policy can make, in the order the console lists them. The
// server and agent spell these the same way (protocol.SettingKeys).
export const SETTING_KEYS = [
  'usb_storage_mode',
  'wallpaper_enabled',
  'inactivity_timeout_minutes',
  'disable_task_mgr',
  'disable_cmd',
  'disable_registry_tools',
  'disable_control_panel',
  'windows_update_mode',
  'firewall_enabled',
  'defender_protection_enabled',
  'disable_powershell',
  'disable_autoplay',
  'screensaver_timeout_minutes',
  'screensaver_password_required',
  'admin_account_enabled',
] as const

export type SettingKey = (typeof SETTING_KEYS)[number]

// A new policy decides nothing until the operator sets something. The old form
// started from "allow"/off for every field and saved all of them, which is how
// a wallpaper-only policy lifted a USB block.
export const DEFAULT_CONFIG: SystemPolicyConfigDTO = {
  settings_version: 2,
  usb_storage_mode: null,
  wallpaper_enabled: null,
  wallpaper_url: '',
  wallpaper_style: 'fill',
  wallpaper_locked: true,
  firewall_enabled: null,
  defender_protection_enabled: null,
  disable_powershell: null,
  disable_autoplay: null,
  screensaver_timeout_minutes: null,
  screensaver_password_required: null,
  inactivity_timeout_minutes: null,
  disable_task_mgr: null,
  disable_cmd: null,
  disable_registry_tools: null,
  disable_control_panel: null,
  windows_update_mode: null,
  admin_account_enabled: null,
  admin_account_username: '',
  admin_account_password: '',
  admin_account_full_name: '',
  admin_account_description: '',
  admin_account_never_expires: true,
}

/** A policy as the server sent it, with every field present for the form. */
export function toConfig(raw: Partial<SystemPolicyConfigDTO> | null | undefined): SystemPolicyConfigDTO {
  return { ...DEFAULT_CONFIG, ...(raw ?? {}), settings_version: 2 }
}

// true means "restrict" for these; for the rest true means "enforce".
const RESTRICTION_KEYS = new Set<SettingKey>([
  'disable_task_mgr',
  'disable_cmd',
  'disable_registry_tools',
  'disable_control_panel',
  'disable_powershell',
  'disable_autoplay',
])
const MINUTE_KEYS = new Set<SettingKey>(['inactivity_timeout_minutes', 'screensaver_timeout_minutes'])

export const settingLabelKey = (key: SettingKey) => `policy.setting_${key}`

/** Labels for the two explicit values of a yes/no setting: [true, false]. */
export function boolValueKeys(key: SettingKey): [string, string] {
  return RESTRICTION_KEYS.has(key)
    ? ['policy.value_blocked', 'policy.value_allowed']
    : ['policy.value_enforced', 'policy.value_not_enforced']
}

/** One decided value in the operator's language. */
export function formatSettingValue(key: SettingKey, value: unknown, t: Translate): string {
  if (key === 'usb_storage_mode') return t(`policy.value_usb_${String(value)}`)
  if (key === 'windows_update_mode') {
    return t({ auto_install: 'policy.wu_auto', notify_only: 'policy.wu_notify' }[String(value)] ?? 'policy.wu_default')
  }
  if (MINUTE_KEYS.has(key)) {
    const n = Number(value)
    return n > 0 ? t('policy.timeout_minutes', { minutes: n }) : t('policy.value_off')
  }
  const [on, off] = boolValueKeys(key)
  return t(value ? on : off)
}

/** Whether an explicit value tightens the endpoint (shown with a warning tone). */
export function isRestrictive(key: SettingKey, value: unknown): boolean {
  if (key === 'usb_storage_mode') return value === 'block' || value === 'read_only'
  if (key === 'windows_update_mode') return value === 'auto_install' || value === 'notify_only'
  if (MINUTE_KEYS.has(key)) return Number(value) > 0
  return value === true
}

/** The settings a policy actually decides, in console order. */
export function explicitDecisions(config: SystemPolicyConfigDTO): { key: SettingKey; value: unknown }[] {
  return SETTING_KEYS.filter((k) => config[k] !== null && config[k] !== undefined).map((k) => ({
    key: k,
    value: config[k],
  }))
}

export interface EffectiveRow {
  key: SettingKey
  value: unknown
  /** The policy that decided it; undefined when no policy did. */
  source?: SettingSourceDTO
}

/** One row per setting: what the device enforces and which policy decided it. */
export function settingRows(
  compiled: SystemPolicySettingsDTO,
  sources: Record<string, SettingSourceDTO> | undefined,
): EffectiveRow[] {
  const s = compiled as unknown as Record<string, unknown>
  return SETTING_KEYS.map((key) => ({ key, value: s[key], source: sources?.[key] }))
}
