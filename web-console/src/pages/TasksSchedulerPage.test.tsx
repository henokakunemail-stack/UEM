import { fireEvent, render, screen } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TaskRunDTO } from '../types/api'

const api = vi.hoisted(() => ({
  getScripts: vi.fn(),
  getSchedules: vi.fn(),
  getTaskRuns: vi.fn(),
  getDeviceGroups: vi.fn(),
  getDevices: vi.fn(),
  getRunDeviceRuns: vi.fn(),
}))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { TasksSchedulerPage } from './TasksSchedulerPage'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const run = (over: Partial<TaskRunDTO>): TaskRunDTO => ({
  id: 'run-000000000001', schedule_id: 's1', script_id: 'sc1', status: 'completed',
  triggered_at: '2026-10-21T08:00:00Z', schedule_name: 'Nightly', script_name: 'Clean',
  devices_total: 0, devices_succeeded: 0, devices_failed: 0, ...over,
})

function renderRuns() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <TasksSchedulerPage activeTab="runs" onTabChange={() => {}} />
      </ToastProvider>
    </I18nProvider>,
  )
}

describe('TasksSchedulerPage runs', () => {
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
    api.getScripts.mockResolvedValue([])
    api.getSchedules.mockResolvedValue([])
    api.getDeviceGroups.mockResolvedValue([])
    api.getDevices.mockResolvedValue({ devices: [] })
  })

  it('shows a mixed run as partial with its device tally', async () => {
    api.getTaskRuns.mockResolvedValue([run({ status: 'partial', devices_total: 5, devices_succeeded: 3, devices_failed: 2 })])
    renderRuns()
    expect(await screen.findByText('Partial')).toBeTruthy()
    expect(screen.getByText('3 ok, 2 failed of 5')).toBeTruthy()
  })

  it('says why a run with no targets failed', async () => {
    api.getTaskRuns.mockResolvedValue([run({ status: 'failed', error_message: 'no_targets: the target resolved to no active devices' })])
    renderRuns()
    expect(await screen.findByText('The target had no active devices')).toBeTruthy()
  })

  // The run history used to fall back to [] on any error: "no runs yet".
  it('names a section that failed instead of showing it empty', async () => {
    api.getTaskRuns.mockRejectedValue(new Error('500'))
    renderRuns()
    expect(await screen.findByText(/Could not load: run history/)).toBeTruthy()
  })

  it('tells a failed device-row load from a run with no devices', async () => {
    api.getTaskRuns.mockResolvedValue([run({})])
    api.getRunDeviceRuns.mockRejectedValue(new Error('500'))
    renderRuns()
    fireEvent.click(await screen.findByText('Per-Device'))
    expect(await screen.findByText('Could not load the per-device results. Try again.')).toBeTruthy()
  })
})
