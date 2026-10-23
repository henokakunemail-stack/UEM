import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO } from '../types/api'

const api = vi.hoisted(() => ({
  getMaintenanceJobs: vi.fn(),
  getMaintenanceStats: vi.fn(),
  getMaintenanceTaskCatalog: vi.fn(),
  getDeviceGroups: vi.fn(),
  getDevices: vi.fn(),
  getAssets: vi.fn(),
  getAssetSummary: vi.fn(),
  getLicenses: vi.fn(),
  getLicenseCompliance: vi.fn(),
  getDashboardSites: vi.fn(),
  getDeviceAgentVersions: vi.fn(),
  deleteDevice: vi.fn(),
  updateDevice: vi.fn(),
  getReportExportUrl: vi.fn(),
}))
const fetchRaw = vi.hoisted(() => vi.fn())
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api, fetchRaw }))
const session = vi.hoisted(() => ({ rol: 'admin' }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: session.rol } }) }))

import { MaintenancePage } from './MaintenancePage'
import { AssetLicensePage } from './AssetLicensePage'
import { DevicesPage } from './DevicesPage'
import { ReportsPage } from './ReportsPage'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'
import { relativeTime } from '../services/relativeTime'

function wrap(node: React.ReactNode) {
  return render(
    <I18nProvider lang="en">
      <ToastProvider>
        <MemoryRouter>{node}</MemoryRouter>
      </ToastProvider>
    </I18nProvider>,
  )
}

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
  session.rol = 'admin'
  api.getDeviceGroups.mockResolvedValue([])
  api.getDevices.mockResolvedValue({ devices: [], count: 0, total: 0, limit: 20, offset: 0 })
  api.getDashboardSites.mockResolvedValue([])
  api.getDeviceAgentVersions.mockResolvedValue([])
})

describe('MaintenancePage', () => {
  // The KPI cards were counted from the 50 jobs the list returns.
  it('shows totals from the server, not from the first page', async () => {
    api.getMaintenanceJobs.mockResolvedValue([])
    api.getMaintenanceStats.mockResolvedValue({ total: 734, running: 2, failed_tasks: 19 })
    api.getMaintenanceTaskCatalog.mockResolvedValue([])
    wrap(<MaintenancePage />)
    expect(await screen.findByText('734')).toBeTruthy()
    expect(screen.getByText('19')).toBeTruthy()
  })

  it('names a section that failed instead of showing it empty', async () => {
    api.getMaintenanceJobs.mockRejectedValue(new Error('500'))
    api.getMaintenanceStats.mockResolvedValue({ total: 0, running: 0, failed_tasks: 0 })
    api.getMaintenanceTaskCatalog.mockResolvedValue([])
    wrap(<MaintenancePage />)
    expect(await screen.findByText(/Could not load: job history/)).toBeTruthy()
  })
})

describe('AssetLicensePage', () => {
  // An empty compliance list read as "every license compliant".
  it('names a section that failed instead of showing it empty', async () => {
    api.getAssets.mockResolvedValue([])
    api.getAssetSummary.mockResolvedValue({ total_assets: 0, active_assets: 0, total_valuation: 0, warranty_expiring_count: 0 })
    api.getLicenses.mockResolvedValue([])
    api.getLicenseCompliance.mockRejectedValue(new Error('500'))
    wrap(<AssetLicensePage activeTab="hardware" onTabChange={() => {}} />)
    expect(await screen.findByText(/Could not load: license compliance/)).toBeTruthy()
  })
})

const device = (over: Partial<DeviceDTO>): DeviceDTO =>
  ({ id: 'x', hostname: 'X', os_name: 'windows', os_version: '11', agent_version: '1', status: 'offline', site: '', ...over }) as DeviceDTO

describe('DevicesPage', () => {
  it('lists the sites devices report and labels a missing one as unassigned', async () => {
    api.getDashboardSites.mockResolvedValue([{ site: 'jakarta', total: 1, online: 1, offline: 0, online_pct: 100 }, { site: 'unassigned', total: 1, online: 0, offline: 1, online_pct: 0 }])
    api.getDevices.mockResolvedValue({ devices: [device({ id: 'a', hostname: 'NO-SITE', site: '' })], count: 1, total: 1, limit: 20, offset: 0 })
    wrap(<DevicesPage />)
    const select = await screen.findByLabelText('Filter by site')
    await waitFor(() => expect(within(select).getByRole('option', { name: 'jakarta' })).toBeTruthy())
    expect((within(select).getByRole('option', { name: 'Unassigned' }) as HTMLOptionElement).value).toBe('__unassigned__')
    expect(within(select).queryByRole('option', { name: /HQ/ })).toBeNull()
    expect(await screen.findAllByText('Unassigned')).toBeTruthy()
  })

  // The page could filter by status and site but not by agent version, so the
  // machines still on an old agent could only be found by paging the fleet.
  it('filters the fleet by agent version on the server', async () => {
    api.getDeviceAgentVersions.mockResolvedValue([{ version: '1.5.7', count: 3 }, { version: '1.5.6', count: 9 }, { version: '', count: 1 }])
    api.getDevices.mockResolvedValue({ devices: [device({ id: 'a', hostname: 'OLD-AGENT', agent_version: '1.5.6' })], count: 1, total: 1, limit: 20, offset: 0 })
    wrap(<DevicesPage />)
    const select = await screen.findByLabelText('Filter by agent version')
    await waitFor(() => expect(within(select).getByRole('option', { name: 'v1.5.6 (9)' })).toBeTruthy())
    expect((within(select).getByRole('option', { name: 'No version reported (1)' }) as HTMLOptionElement).value).toBe('__unknown__')
    fireEvent.change(select, { target: { value: '1.5.6' } })
    await waitFor(() => expect(api.getDevices).toHaveBeenLastCalledWith(expect.any(Number), 0, '', '', '', '1.5.6'))
  })

  // A multi-device token leaves every machine it enrolls without a site, and
  // the console had no way to set one: the endpoint existed, nothing called
  // it. A technician selects rows and sets their site in one step.
  it('lets a technician set the site of the selected devices', async () => {
    session.rol = 'technician'
    const now = new Date().toISOString()
    api.getDevices.mockResolvedValue({
      devices: [
        device({ id: 'a', hostname: 'PC-A', status: 'online', last_seen_at: now }),
        device({ id: 'b', hostname: 'PC-B', status: 'online', last_seen_at: now }),
        device({ id: 'c', hostname: 'PC-C', status: 'online', last_seen_at: now }),
      ],
      count: 3, total: 3, limit: 20, offset: 0,
    })
    api.updateDevice.mockImplementation((id: string) => (id === 'b' ? Promise.reject(new Error('500')) : Promise.resolve({})))
    wrap(<DevicesPage />)
    for (const name of ['PC-A', 'PC-B']) {
      const row = (await screen.findByText(name)).closest('tr') as HTMLElement
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    // Group membership stays an admin action.
    expect(screen.queryByLabelText('Target group for the selection')).toBeNull()
    fireEvent.change(screen.getByLabelText('Site for the selected devices'), { target: { value: '  HQ ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set site' }))
    await waitFor(() => expect(api.updateDevice).toHaveBeenCalledTimes(2))
    expect(api.updateDevice).toHaveBeenCalledWith('a', { site: 'HQ' })
    expect(api.updateDevice).toHaveBeenCalledWith('b', { site: 'HQ' })
    expect((await screen.findAllByText('HQ set on 1 device(s); 1 could not be updated')).length).toBeGreaterThan(0)
  })

  it('gives a viewer no row selection', async () => {
    session.rol = 'viewer'
    api.getDevices.mockResolvedValue({ devices: [device({ id: 'a', hostname: 'PC-A' })], count: 1, total: 1, limit: 20, offset: 0 })
    wrap(<DevicesPage />)
    const row = (await screen.findByText('PC-A')).closest('tr') as HTMLElement
    expect(within(row).queryByRole('checkbox')).toBeNull()
  })

  // Bulk delete sent every selected id, swallowed each failure, and reported
  // success for whatever went through.
  it('deletes only the selected retired devices and reports failures', async () => {
    const now = new Date().toISOString()
    api.getDevices.mockResolvedValue({
      devices: [
        device({ id: 'r1', hostname: 'OLD-1', retired_at: now, status: 'offline' }),
        device({ id: 'r2', hostname: 'OLD-2', retired_at: now, status: 'offline' }),
        device({ id: 'live', hostname: 'LIVE', status: 'online', last_seen_at: now }),
      ],
      count: 3, total: 3, limit: 20, offset: 0,
    })
    api.deleteDevice.mockImplementation((id: string) => (id === 'r2' ? Promise.reject(new Error('409')) : Promise.resolve({})))
    wrap(<DevicesPage />)
    for (const name of ['OLD-1', 'OLD-2', 'LIVE']) {
      const row = (await screen.findByText(name)).closest('tr') as HTMLElement
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    const bulk = await screen.findByTitle('Permanently delete selected retired devices from database')
    fireEvent.click(bulk)
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getAllByRole('button').find((b) => /Delete/.test(b.textContent || '')) as HTMLElement)
    await waitFor(() => expect(api.deleteDevice).toHaveBeenCalledTimes(2))
    expect(api.deleteDevice).not.toHaveBeenCalledWith('live')
    expect((await screen.findAllByText(/1 deleted, 1 could not be deleted/)).length).toBeGreaterThan(0)
  })
})

describe('relativeTime', () => {
  // A bare time of day made a week-old heartbeat look like this morning's.
  it('says how long ago, in the reader language', () => {
    const now = new Date('2026-10-21T12:00:00Z')
    expect(relativeTime('2026-10-14T10:00:00Z', 'en', now)).toBe('7 days ago')
    expect(relativeTime('2026-10-21T11:58:00Z', 'id', now)).toBe('2 menit yang lalu')
  })
})

describe('ReportsPage', () => {
  // A capped export looked complete.
  it('warns when an export hit its limit', async () => {
    api.getReportExportUrl.mockReturnValue('/api/reports/inventory?format=csv')
    fetchRaw.mockResolvedValue(new Response('a,b\n', { status: 200, headers: { 'X-Truncated': 'true', 'X-Row-Count': '50000' } }))
    globalThis.URL.createObjectURL = vi.fn(() => 'blob:x')
    globalThis.URL.revokeObjectURL = vi.fn()
    wrap(<ReportsPage />)
    fireEvent.click(screen.getAllByText('Export CSV')[0])
    expect(await screen.findByText(/holds the first 50000 rows only/)).toBeTruthy()
  })
})
