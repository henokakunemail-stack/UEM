import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { SoftwareDeploymentDTO } from '../types/api'

const api = vi.hoisted(() => ({
  getPackages: vi.fn(),
  getDeployments: vi.fn(),
  getDevices: vi.fn(),
  getDeviceGroups: vi.fn(),
}))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { SoftwarePage } from './SoftwarePage'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const dep = (over: Partial<SoftwareDeploymentDTO>): SoftwareDeploymentDTO => ({
  id: 'd1', package_id: 'p1', name: 'Rollout', target_type: 'all', target_id: '',
  created_by: 'admin', status: 'running', created_at: '2026-10-21T08:00:00Z',
  total_tasks: 4, success_tasks: 1, failed_tasks: 0, pending_tasks: 2, ...over,
})

function renderPage() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <SoftwarePage activeTab="deployments" onTabChange={() => {}} />
      </ToastProvider>
    </I18nProvider>,
  )
}

describe('SoftwarePage deployments', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getPackages.mockResolvedValue([])
    api.getDevices.mockResolvedValue({ devices: [] })
    api.getDeviceGroups.mockResolvedValue([])
  })

  // A rollout that reached some devices used to read "Completed"; anything
  // that was not completed or running read "Failed".
  it('shows a mixed outcome as partial and a cancelled one as cancelled', async () => {
    api.getDeployments.mockResolvedValue([
      dep({ id: 'a', name: 'Mixed', status: 'partial' }),
      dep({ id: 'b', name: 'Stopped', status: 'cancelled' }),
    ])
    renderPage()
    expect(await screen.findByText('Partial')).toBeTruthy()
    expect(screen.getByText('Cancelled')).toBeTruthy()
    expect(screen.queryByText('Failed')).toBeNull()
  })

  it('says how many devices a running deployment waits for', async () => {
    api.getDeployments.mockResolvedValue([dep({})])
    renderPage()
    expect(await screen.findByText('2 waiting for their device')).toBeTruthy()
  })

  // The device list used to fail into an empty list, which reads as "no
  // endpoints" in the deploy dialog.
  it('names a section that failed to load', async () => {
    api.getDeployments.mockResolvedValue([])
    api.getDevices.mockRejectedValue(new Error('500'))
    renderPage()
    expect(await screen.findByText(/Could not load: endpoints/)).toBeTruthy()
  })
})
