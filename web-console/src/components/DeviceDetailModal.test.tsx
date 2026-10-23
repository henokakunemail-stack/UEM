import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO, DeviceInventorySnapshot } from '../types/api'

const api = vi.hoisted(() => ({ getDeviceInventory: vi.fn(), updateDevice: vi.fn() }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { DeviceDetailModal } from './DeviceDetailModal'
import { I18nProvider } from '../i18n/I18nContext'

const device: DeviceDTO = {
  id: 'dev-1', hostname: 'PC-01', os_name: 'windows', os_version: '10.0.26100', agent_version: '1.5.7',
  status: 'online', last_seen_at: new Date().toISOString(), site: 'hq', enrolled_at: new Date().toISOString(),
}

function snapshot(os: DeviceInventorySnapshot['os']): DeviceInventorySnapshot {
  return { device_id: 'dev-1', collected_at: new Date().toISOString(), software: [], hw: {}, os }
}

async function domainCard(os: DeviceInventorySnapshot['os']) {
  api.getDeviceInventory.mockResolvedValue(snapshot(os))
  render(
    <I18nProvider lang="en">
      <DeviceDetailModal device={device} onClose={() => {}} />
    </I18nProvider>,
  )
  return within(await screen.findByTestId('domain-card'))
}

describe('DeviceDetailModal domain membership', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
  })
  beforeEach(() => vi.clearAllMocks())

  it('shows a joined domain by name and type', async () => {
    const card = await domainCard({ domain: { joined: true, type: 'active_directory', name: 'corp.example.com' } })
    expect(card.getByText('Joined')).toBeTruthy()
    expect(card.getByText(/corp\.example\.com/)).toBeTruthy()
    expect(card.getByText('Active Directory')).toBeTruthy()
  })

  it('shows a workgroup machine as not joined', async () => {
    const card = await domainCard({ domain: { joined: false, type: 'workgroup', name: 'WORKGROUP' } })
    expect(card.getByText('Not joined')).toBeTruthy()
    expect(card.getByText('Workgroup: WORKGROUP')).toBeTruthy()
  })

  // An agent that predates the field must not read as "not joined": that is
  // a claim about the machine nobody made.
  it('says unknown when the agent did not report it', async () => {
    const card = await domainCard({ edition: 'Professional' })
    expect(card.getByText('Unknown')).toBeTruthy()
    expect(card.queryByText('Not joined')).toBeNull()
  })
})

describe('DeviceDetailModal site', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
  })
  beforeEach(() => vi.clearAllMocks())

  // One device's site was only settable through the API.
  it('changes one device\'s site from its detail', async () => {
    api.getDeviceInventory.mockResolvedValue(snapshot({}))
    api.updateDevice.mockResolvedValue({ ...device, site: 'HQ' })
    const onUpdated = vi.fn()
    render(
      <I18nProvider lang="en">
        <DeviceDetailModal device={{ ...device, site: '' }} onClose={() => {}} onUpdated={onUpdated} />
      </I18nProvider>,
    )
    const cell = within(await screen.findByTestId('device-site'))
    expect(cell.getByText('Unassigned')).toBeTruthy()
    fireEvent.click(cell.getByRole('button', { name: 'Change site' }))
    fireEvent.change(cell.getByRole('textbox'), { target: { value: ' HQ ' } })
    fireEvent.click(cell.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateDevice).toHaveBeenCalledWith('dev-1', { site: 'HQ' }))
    await waitFor(() => expect(onUpdated).toHaveBeenCalledWith(expect.objectContaining({ site: 'HQ' })))
  })
})
