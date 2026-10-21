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

import runningCampaign from '../test/contracts/update-campaign.json'
import logItem from '../test/contracts/campaign-log-item.json'
import taskDetail from '../test/contracts/campaign-task-detail.json'
import { campaignProgress, taskDisplayStatus } from './campaignProgress'
import type { CampaignTaskDetail } from '../types/api'

// The fixtures are the server's own field names: the Go test
// TestConsoleContractFixturesMatchTheDTOs fails if they drift from the DTOs.
describe('AgentUpdatesPage progress from the server contract', () => {
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
    api.getUpdateCampaigns.mockResolvedValue([runningCampaign])
    api.getCampaignTasks.mockResolvedValue([taskDetail])
    api.getCampaignLogs.mockResolvedValue([logItem])
  })

  it('counts finished, running, queued and waiting devices', () => {
    expect(campaignProgress(runningCampaign as UpdateCampaignDTO)).toEqual({
      total: 10, succeeded: 3, failed: 1, running: 1, queued: 2, waiting: 3, percent: 40,
    })
  })

  it('renders the progress of a running campaign', async () => {
    renderPage()
    expect(await screen.findByText('4/10 finished (40%)')).toBeTruthy()
  })

  it('tells a task waiting for its device from one waiting for its wave', () => {
    expect(taskDisplayStatus(taskDetail as CampaignTaskDetail)).toBe('queued')
    expect(taskDisplayStatus({ ...taskDetail, released_at: null } as CampaignTaskDetail)).toBe('waiting_wave')
    expect(taskDisplayStatus({ ...taskDetail, status: 'success' } as CampaignTaskDetail)).toBe('success')
  })

  it('shows log times from the timestamp field', async () => {
    renderPage()
    fireEvent.click(await screen.findByText('Logs & Impact'))
    fireEvent.click(await screen.findByRole('button', { name: /Rollout Activity Logs/ }))
    await waitFor(() => expect(document.body.textContent).toContain('TASK_DISPATCHED'))
    expect(document.body.textContent).not.toContain('Invalid Date')
  })
})
