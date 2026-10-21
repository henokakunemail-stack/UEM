import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '../i18n/I18nContext'
import type { Lang } from '../i18n/core'

const api = vi.hoisted(() => ({
  getDashboardSummary: vi.fn(),
  getDashboardSites: vi.fn(),
  getDashboardOS: vi.fn(),
  getDashboardAlerts: vi.fn(),
  getDashboardActivity: vi.fn(),
}))
vi.mock('../services/api', () => ({ api }))

import { DashboardPage } from './DashboardPage'

function renderDashboard(lang: Lang) {
  return render(
    <I18nProvider lang={lang}>
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>
    </I18nProvider>,
  )
}

const healthyWords: Record<Lang, string[]> = {
  en: ['Healthy', 'Optimal', 'Normal', 'Stable', 'Degraded'],
  id: ['Sehat', 'Optimal', 'Normal', 'Stabil', 'Menurun'],
}

describe('DashboardPage with no data', () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
  })

  // The console knows nothing when every request fails. It must say so on
  // every card -- a "0 · Healthy" or "0 offline · Optimal" here is the console
  // inventing a reassuring state it never observed.
  for (const lang of ['en', 'id'] as const) {
    it(`claims nothing healthy when every request failed (${lang})`, async () => {
      for (const fn of Object.values(api)) fn.mockRejectedValue(new Error('down'))
      const { container } = renderDashboard(lang)
      await waitFor(() => expect(container.querySelectorAll('.kpi-card').length).toBeGreaterThan(0))

      const unknownCards = container.querySelectorAll('.kpi-card.unknown')
      expect(unknownCards.length).toBe(container.querySelectorAll('.kpi-card').length)
      const badges = [...container.querySelectorAll('.kpi-badge')].map((b) => b.textContent)
      for (const word of healthyWords[lang]) {
        expect(badges).not.toContain(word)
      }
      expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(7)
    })
  }

  // An empty fleet has no online ratio. 0% used to read as "Degraded" on a
  // fresh install that had nothing wrong with it.
  it('does not call an empty fleet degraded', async () => {
    api.getDashboardSummary.mockResolvedValue({
      total_devices: 0, online_devices: 0, offline_devices: 0, online_pct: 0,
      low_disk_alerts: 0, recent_hw_changes_24h: 0, sites_count: 0, retired_devices: 0,
    })
    api.getDashboardSites.mockResolvedValue([])
    api.getDashboardOS.mockResolvedValue([])
    api.getDashboardAlerts.mockResolvedValue([])
    api.getDashboardActivity.mockResolvedValue([])
    const { container } = renderDashboard('en')
    await waitFor(() => expect(screen.getByText('No devices')).toBeTruthy())
    const badges = [...container.querySelectorAll('.kpi-badge')].map((b) => b.textContent)
    expect(badges).not.toContain('Degraded')
    expect(container.querySelectorAll('.kpi-card.unknown').length).toBe(0)
  })
})

describe('DashboardPage health alerts total', () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
  })

  // The card was the length of a list the server caps at 15 + 15.
  it('shows the total from the summary, not the length of the first page', async () => {
    api.getDashboardSummary.mockResolvedValue({
      total_devices: 50, online_devices: 50, offline_devices: 0, retired_devices: 0, online_pct: 100,
      low_disk_alerts: 40, recent_hw_changes_24h: 0, sites_count: 1, pending_devices: 0, alerts_total: 40,
    })
    api.getDashboardSites.mockResolvedValue([])
    api.getDashboardOS.mockResolvedValue([])
    api.getDashboardAlerts.mockResolvedValue(Array.from({ length: 30 }, (_, i) => ({
      id: `a${i}`, type: 'low_disk', severity: 'warning', device_id: `d${i}`, hostname: `h${i}`, site: '', message: '', timestamp: '2026-10-21T08:00:00Z', value: 3,
    })))
    api.getDashboardActivity.mockResolvedValue([])
    const { container } = renderDashboard('en')
    await waitFor(() => {
      const card = [...container.querySelectorAll('.kpi-card')].find((c) => c.textContent?.includes('ACTIVE HEALTH ALERTS'))
      expect(card?.querySelector('.kpi-value')?.textContent).toBe('40')
    })
  })
})
