import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { LifecycleReport, LifecycleSettings } from '../types/api'

const api = vi.hoisted(() => ({
  getAssetLifecycle: vi.fn(),
  saveLifecycleSettings: vi.fn(),
  downloadLifecycleReport: vi.fn(),
}))
const role = vi.hoisted(() => ({ current: 'viewer' }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: role.current } }) }))

import { AssetLifecyclePanel } from './AssetLifecyclePanel'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const settings: LifecycleSettings = {
  categories: {
    laptop: { useful_life_years: 4, replacement_cost: 15_000_000 },
    desktop: { useful_life_years: 5, replacement_cost: 0 },
    server: { useful_life_years: 5, replacement_cost: 0 },
    other: { useful_life_years: 5, replacement_cost: 0 },
  },
  min_ram_gb: 8,
  min_disk_free_percent: 10,
}

const line = (label: string, units: number, cost: number, missing = 0) => ({
  label,
  start: '2027-01-01T00:00:00+07:00',
  end: '2028-01-01T00:00:00+07:00',
  units,
  cost,
  units_without_cost: missing,
})

const report: LifecycleReport = {
  generated_at: '2026-11-20T03:00:00Z',
  settings,
  units: [
    {
      asset_tag: 'A01', hostname: 'PC-OLD', category: 'laptop', category_source: 'asset', age_source: 'purchase',
      start_date: '2020-01-10T00:00:00Z', age_years: 6.86, useful_life_years: 4, due_date: '2024-01-10T00:00:00Z',
      overdue: true, replacement_cost: 15_000_000, cost_source: 'category', purchase_cost: 12_000_000, book_value: 0, below_spec: [],
    },
    {
      asset_tag: 'A08', hostname: 'PC-BIOS', category: 'desktop', category_source: 'device', age_source: 'bios',
      start_date: '2019-03-01T00:00:00Z', age_years: 7.7, useful_life_years: 5, due_date: '2024-03-01T00:00:00Z',
      overdue: true, purchase_cost: 0, below_spec: ['ram'], ram_bytes: 4 * 2 ** 30,
    },
    {
      asset_tag: 'A10', category: 'other', category_source: 'default', age_source: 'unknown', useful_life_years: 5,
      overdue: false, purchase_cost: 0, below_spec: [],
    },
  ],
  age_sources: { purchase: 1, bios: 1, os_install: 0, unknown: 1 },
  age_buckets: [
    { key: '0-1', count: 0 },
    { key: '1-3', count: 0 },
    { key: '3-5', count: 0 },
    { key: '5+', count: 2 },
    { key: 'unknown', count: 1 },
  ],
  overdue: 2,
  below_spec: 1,
  forecast: { overdue: line('overdue', 2, 15_000_000, 1), quarters: [line('2027-Q1', 0, 0)], years: [line('2027', 0, 0)], missing_age: 1 },
  book_value: { purchase_total: 12_000_000, book_total: 0, assets: 1, excluded: 2 },
  licenses: [
    {
      license_id: 'l1', software_name: 'Microsoft Office', license_type: 'per_device', total_seats: 10, cost: 10_000_000,
      seat_cost: 1_000_000, allocated: 6, installed: 4, in_use: 6, unused_seats: 4, unused_value: 4_000_000,
      allocated_not_installed: 2, allocated_not_installed_value: 2_000_000, allocated_unverified: 0, applicable: true,
    },
  ],
  unused_seat_value: 4_000_000,
  allocated_not_installed_value: 2_000_000,
  expiring_licenses: [],
  expiring_warranties: [],
  warranties_expired: 0,
}

function renderPanel() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <AssetLifecyclePanel />
      </ToastProvider>
    </I18nProvider>,
  )
}

// Intl output uses a narrow no-break space between "Rp" and the number.
const rp = (s: string) => new RegExp(s.replace(/\./g, '\\.').replace('Rp ', 'Rp\\s'))

describe('AssetLifecyclePanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    role.current = 'viewer'
    api.getAssetLifecycle.mockResolvedValue(report)
  })

  it('labels every age with its source and keeps ageless units out of the budget', async () => {
    renderPanel()
    const overdue = await screen.findByRole('heading', { name: 'Units past their useful life (2)' })
    const table = within(overdue.closest('section') as HTMLElement)
    expect(table.getByText('6.9 years (from purchase date)')).toBeTruthy()
    expect(table.getByText('7.7 years (estimated from BIOS)')).toBeTruthy()
    expect(table.getByText('not set')).toBeTruthy()
    expect(screen.getByText(/Missing data: 1 units have no purchase date/)).toBeTruthy()
    expect(screen.getAllByText(rp('Rp 15.000.000 \\(\\+1 units without a cost\\)')).length).toBeGreaterThan(0)
  })

  it('shows wasted seats in rupiah', async () => {
    renderPanel()
    expect(await screen.findByText(rp('4 seats \\(Rp 4.000.000\\)'))).toBeTruthy()
    expect(screen.getByText(rp('2 allocations \\(Rp 2.000.000\\)'))).toBeTruthy()
  })

  it('lets only an admin edit the settings', async () => {
    renderPanel()
    await screen.findByRole('heading', { name: 'Lifecycle settings' })
    expect(screen.queryByRole('button', { name: 'Save settings' })).toBeNull()
    // A disabled fieldset disables its controls (:disabled), not their own property.
    expect(screen.getAllByLabelText('Useful life (years)')[0].matches(':disabled')).toBe(true)
  })

  it('saves edited settings as an admin', async () => {
    role.current = 'admin'
    api.saveLifecycleSettings.mockResolvedValue(settings)
    renderPanel()
    const lives = await screen.findAllByLabelText('Useful life (years)')
    fireEvent.change(lives[0], { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    await waitFor(() => expect(api.saveLifecycleSettings).toHaveBeenCalled())
    const sent = api.saveLifecycleSettings.mock.calls[0][0] as LifecycleSettings
    expect(sent.categories.laptop.useful_life_years).toBe(3)
    expect(sent.categories.desktop.useful_life_years).toBe(5)
    await waitFor(() => expect(api.getAssetLifecycle).toHaveBeenCalledTimes(2))
  })

  it('downloads the summary PDF', async () => {
    api.downloadLifecycleReport.mockResolvedValue(new Blob(['%PDF']))
    const createURL = vi.fn(() => 'blob:x')
    Object.assign(URL, { createObjectURL: createURL, revokeObjectURL: vi.fn() })
    renderPanel()
    fireEvent.click(await screen.findByRole('button', { name: 'Download summary PDF' }))
    await waitFor(() => expect(api.downloadLifecycleReport).toHaveBeenCalled())
    await waitFor(() => expect(createURL).toHaveBeenCalled())
  })

  it('says so when the dashboard cannot load', async () => {
    api.getAssetLifecycle.mockRejectedValue(new Error('HTTP 500'))
    renderPanel()
    expect((await screen.findByRole('alert')).textContent).toContain('Could not load lifecycle data: HTTP 500')
  })
})
