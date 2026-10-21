import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO } from '../types/api'

const api = vi.hoisted(() => ({ getDevices: vi.fn(), getRemoteControlSessions: vi.fn() }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { RemoteControlPage } from './RemoteControlPage'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

// Only the session history list: a failed read used to render as "no
// sessions", which reads as "this device was never controlled".
describe('RemoteControlPage session history', () => {
  beforeEach(() => vi.clearAllMocks())

  it('says the history could not be loaded', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{ id: 'd1', hostname: 'PC-1', os_name: 'windows', os_version: '11', agent_version: '1', status: 'online', site: '' } as DeviceDTO],
      count: 1, total: 1, limit: 50, offset: 0,
    })
    api.getRemoteControlSessions.mockRejectedValue(new Error('500'))
    render(
      <I18nProvider lang="en">
        <ToastProvider>
          <RemoteControlPage />
        </ToastProvider>
      </I18nProvider>,
    )
    fireEvent.click(await screen.findByText('PC-1'))
    expect(await screen.findByText('Could not load the session history for this device.')).toBeTruthy()
  })
})
