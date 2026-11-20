import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DevicePostureResponse } from '../types/api'

const api = vi.hoisted(() => ({ getDevicePosture: vi.fn() }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))

import { DevicePosturePanel } from './DevicePosturePanel'
import { I18nProvider } from '../i18n/I18nContext'

function show(resp: DevicePostureResponse) {
  api.getDevicePosture.mockResolvedValue(resp)
  render(
    <I18nProvider lang="en">
      <DevicePosturePanel deviceId="dev-1" />
    </I18nProvider>,
  )
}

describe('DevicePosturePanel', () => {
  beforeEach(() => vi.clearAllMocks())

  // An agent that predates posture must not render as a clean panel.
  it('says posture is not reported for an old agent', async () => {
    show({ reported: false, reason: 'old agent' })
    const box = await screen.findByTestId('posture-not-reported')
    expect(box.textContent).toContain('Security posture not reported')
    expect(screen.queryByText('On')).toBeNull()
  })

  it('shows each control with its state and the agent reason for a gap', async () => {
    show({
      reported: true,
      collected_at: '2026-11-19T01:00:00Z',
      summary: { disk_encryption: 'unknown', antivirus: 'off', firewall: 'partial', password_policy: 'reported', screen_lock: 'reported' },
      posture: {
        schema_version: 1,
        disk_encryption: { state: 'unknown', reason: 'BitLocker: Access denied' },
        antivirus: { state: 'reported', products: [{ name: 'Windows Defender', enabled: false, real_time: false }] },
        firewall: { state: 'reported', profiles: [{ name: 'Domain', enabled: true }, { name: 'Public', enabled: false }] },
        password_policy: { state: 'reported', min_length: 0, max_age_days: 0, lockout_threshold: 10 },
        screen_lock: { state: 'reported' },
      },
      history: [],
    })
    const disk = within(await screen.findByTestId('posture-disk'))
    expect(disk.getByText('Unknown')).toBeTruthy()
    expect(disk.getByText('BitLocker: Access denied')).toBeTruthy()

    const av = within(screen.getByTestId('posture-av'))
    expect(av.getByText('Off')).toBeTruthy()
    expect(av.getByText(/Windows Defender: Disabled/)).toBeTruthy()

    const fw = within(screen.getByTestId('posture-fw'))
    expect(fw.getByText('Partial')).toBeTruthy()
    expect(fw.getByText('Public: off')).toBeTruthy()

    expect(screen.getByText('Maximum age: never expires')).toBeTruthy()
    expect(screen.getByText('Lockout after 10 failed attempts')).toBeTruthy()
    expect(screen.getByText('No machine-wide inactivity lock is enforced')).toBeTruthy()
  })
})
