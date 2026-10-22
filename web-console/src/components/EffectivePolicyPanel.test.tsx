import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({ getDeviceSystemPolicy: vi.fn() }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))

import { EffectivePolicyPanel } from './EffectivePolicyPanel'
import { I18nProvider } from '../i18n/I18nContext'
import type { DeviceDTO } from '../types/api'

describe('EffectivePolicyPanel', () => {
  it('shows which policy decided each setting and which ones nobody decides', async () => {
    api.getDeviceSystemPolicy.mockResolvedValue({
      compiled: { device_id: 'dev-1', version: 'v', settings: { usb_storage_mode: 'block', disable_task_mgr: false } },
      state: null,
      sources: {
        usb_storage_mode: { policy_id: 'p1', policy_name: 'Fleet USB block', target_type: 'all', target_id: '', priority: 100 },
      },
      matched_policies: [],
    })
    render(
      <I18nProvider lang="en">
        <EffectivePolicyPanel devices={[{ id: 'dev-1', hostname: 'PC-1' } as DeviceDTO]} groups={[]} />
      </I18nProvider>,
    )
    fireEvent.change(screen.getByLabelText('Device'), { target: { value: 'dev-1' } })

    const usbRow = (await screen.findByText('USB storage')).closest('tr') as HTMLElement
    expect(within(usbRow).getByText('Blocked')).toBeTruthy()
    expect(within(usbRow).getByText('Fleet USB block')).toBeTruthy()

    const taskRow = screen.getByText('Task Manager').closest('tr') as HTMLElement
    expect(within(taskRow).getByText('Not configured (OS default)')).toBeTruthy()
    expect(within(taskRow).getByText('No policy decides this')).toBeTruthy()
    await waitFor(() => expect(api.getDeviceSystemPolicy).toHaveBeenCalledWith('dev-1'))
  })
})
