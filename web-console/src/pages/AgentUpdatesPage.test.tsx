import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpdateCampaignDTO } from '../types/api'

const api = vi.hoisted(() => ({
  getAgentReleases: vi.fn(),
  getUpdateCampaigns: vi.fn(),
  getCampaignTasks: vi.fn(),
  getCampaignLogs: vi.fn(),
  startUpdateCampaign: vi.fn(),
  cancelUpdateCampaign: vi.fn(),
}))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { AgentUpdatesPage } from './AgentUpdatesPage'
import { ApiError } from '../services/api'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const draft = {
  id: 'c1', name: 'Wave 1', target_version: '2.0.0', target_type: 'all', target_id: '',
  status: 'draft', batch_size: 25, stagger_interval_sec: 60, created_at: '', updated_at: '',
} as unknown as UpdateCampaignDTO

function renderPage() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <AgentUpdatesPage activeTab="campaigns" onTabChange={() => {}} />
      </ToastProvider>
    </I18nProvider>,
  )
}

describe('AgentUpdatesPage start', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
  })
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAgentReleases.mockResolvedValue([])
    api.getUpdateCampaigns.mockResolvedValue([draft])
  })

  // A rollout reaches every targeted device; it is confirmed first, and a
  // second click while it starts sends nothing more.
  it('asks before starting and starts once', async () => {
    let finish: (v: unknown) => void = () => {}
    api.startUpdateCampaign.mockReturnValue(new Promise((r) => (finish = r)))
    renderPage()
    fireEvent.click(await screen.findByText('Start'))
    expect(api.startUpdateCampaign).not.toHaveBeenCalled()

    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent).toContain('2.0.0')
    const confirm = within(dialog).getByRole('button', { name: 'Start rollout' })
    fireEvent.click(confirm)
    fireEvent.click(confirm)
    await waitFor(() => expect(api.startUpdateCampaign).toHaveBeenCalledTimes(1))
    expect(api.startUpdateCampaign).toHaveBeenCalledWith('c1')
    finish({ status: 'started', total_targets: 3, dispatched_live: 3 })
  })

  it('explains a refused start in the operator language', async () => {
    api.startUpdateCampaign.mockRejectedValue(new ApiError('campaign cannot be started from status in_progress', 409, 'campaign_not_startable'))
    renderPage()
    fireEvent.click(await screen.findByText('Start'))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Start rollout' }))
    await waitFor(() => expect(screen.getAllByText('This campaign has already started, finished or been cancelled.').length).toBeGreaterThan(0))
  })
})
