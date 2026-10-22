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
  getBundledAgents: vi.fn(),
  importBundledAgents: vi.fn(),
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
      total: 10, succeeded: 3, failed: 1, running: 1, queued: 2, waiting: 3, upToDate: 0, percent: 40,
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

import bundledAgent from '../test/contracts/bundled-agent.json'

describe('AgentUpdatesPage bundled agents', () => {
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
    api.getUpdateCampaigns.mockResolvedValue([])
  })

  // The agents a server package ships are registered from the console instead
  // of being downloaded from the release page and uploaded again by hand.
  it('imports only the bundled agents that are importable', async () => {
    api.getBundledAgents.mockResolvedValue([
      bundledAgent,
      { ...bundledAgent, file: 'agent-windows-amd64.exe', os_name: 'windows', arch: 'amd64', importable: false, reason: 'already registered as a release', release_id: 'r1' },
      { ...bundledAgent, file: 'agent-linux-arm64', os_name: 'linux', importable: false, reason: 'file name says linux/arm64 but the binary is built for linux/amd64' },
    ])
    api.importBundledAgents.mockResolvedValue({ imported: [{ id: 'r2', version: '1.6.0', os_name: 'macos', arch: 'arm64' }], skipped: [] })
    render(
      <I18nProvider lang="en">
        <ToastProvider>
          <AgentUpdatesPage activeTab="releases" onTabChange={() => {}} />
        </ToastProvider>
      </I18nProvider>,
    )
    fireEvent.click(await screen.findByText('Import from Server Package'))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('agent-darwin-arm64')
    expect(within(dialog).getByText('Already a release')).toBeTruthy()
    expect(within(dialog).getByText(/built for linux\/amd64/)).toBeTruthy()
    expect((within(dialog).getByLabelText('agent-linux-arm64') as HTMLInputElement).disabled).toBe(true)

    fireEvent.click(within(dialog).getByRole('button', { name: 'Import 1 Selected' }))
    await waitFor(() => expect(api.importBundledAgents).toHaveBeenCalledWith(['agent-darwin-arm64']))
    await waitFor(() => expect(api.getAgentReleases).toHaveBeenCalledTimes(2))
  })
})

// A campaign used to keep no row for devices already on the target version,
// and the detail dialog then said "all target devices are already running" for
// any finished campaign without rows -- including one whose target matched no
// device at all.
describe('AgentUpdatesPage checked devices', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
  })
  const finished = { ...runningCampaign, status: 'completed', total_devices: 2, succeeded_devices: 0, failed_devices: 0,
    pending_devices: 0, in_progress_devices: 0, queued_devices: 0, up_to_date_devices: 2 } as UpdateCampaignDTO
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAgentReleases.mockResolvedValue([])
    api.getCampaignLogs.mockResolvedValue([])
  })

  it('counts devices already on the version as finished', () => {
    expect(campaignProgress(finished).percent).toBe(100)
  })

  it('lists devices that were already on the target version', async () => {
    api.getUpdateCampaigns.mockResolvedValue([finished])
    api.getCampaignTasks.mockResolvedValue([
      { ...taskDetail, id: 't-1', hostname: 'PC-1', status: 'up_to_date', from_version: '2.0.0' },
      { ...taskDetail, id: 't-2', hostname: 'PC-2', status: 'up_to_date', from_version: '2.0.0' },
    ])
    renderPage()
    fireEvent.click(await screen.findByText('Logs & Impact'))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('PC-2')
    expect(within(dialog).getAllByText('Was already running v2.0.0; nothing sent').length).toBe(2)
    expect(within(dialog).queryByText("No device matched this campaign's target.")).toBeNull()
  })

  it('says when a finished campaign matched no device', async () => {
    api.getUpdateCampaigns.mockResolvedValue([{ ...finished, total_devices: 0, up_to_date_devices: 0 }])
    api.getCampaignTasks.mockResolvedValue([])
    renderPage()
    fireEvent.click(await screen.findByText('Logs & Impact'))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText("No device matched this campaign's target.")).toBeTruthy()
  })

  it('tells an empty target from a fleet already on the version when starting', async () => {
    api.getUpdateCampaigns.mockResolvedValue([draft])
    api.startUpdateCampaign.mockResolvedValue({ status: 'started', total_targets: 0, dispatched_live: 0 })
    renderPage()
    fireEvent.click(await screen.findByText('Start'))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Start rollout' }))
    await waitFor(() => expect(screen.getAllByText("Campaign 'Wave 1' matched no device. Check its target.").length).toBeGreaterThan(0))
  })
})
