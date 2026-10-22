import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({ bulkAddFilterRules: vi.fn() }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))

import { FilterBulkAddModal } from './FilterBulkAddModal'
import { I18nProvider } from '../i18n/I18nContext'
import type { FilterPolicyDTO } from '../types/api'

const policy = { id: 'pol-1', name: 'Kiosk', mode: 'blacklist' } as FilterPolicyDTO

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute('open')
  }
})
beforeEach(() => vi.clearAllMocks())

describe('FilterBulkAddModal', () => {
  it('sends every pasted line in one request and explains each rejected line', async () => {
    api.bulkAddFilterRules.mockResolvedValue({
      added: 1,
      duplicates: 1,
      invalid: 1,
      results: [
        { input: 'tiktok.com', pattern: 'tiktok.com', status: 'added' },
        { input: 'detik.com', pattern: 'detik.com', status: 'duplicate', reason: 'duplicate_existing' },
        { input: '10.0.0.1', status: 'invalid', reason: 'ip_literal' },
      ],
    })
    const onImported = vi.fn()
    render(
      <I18nProvider lang="en">
        <FilterBulkAddModal
          policy={policy}
          categories={[{ value: 'Social Media', label: 'Social Media' }]}
          onClose={() => {}}
          onImported={onImported}
        />
      </I18nProvider>,
    )

    fireEvent.change(screen.getByLabelText('Domains'), {
      target: { value: 'tiktok.com\ndetik.com\n# comment\n10.0.0.1\n' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add 3 line(s)' }))

    await waitFor(() => expect(onImported).toHaveBeenCalled())
    expect(api.bulkAddFilterRules).toHaveBeenCalledTimes(1)
    expect(api.bulkAddFilterRules).toHaveBeenCalledWith('pol-1', {
      patterns: ['tiktok.com', 'detik.com', '10.0.0.1'],
      category: 'Social Media',
    })
    expect(screen.getByRole('status').textContent).toBe('1 added, 1 duplicate(s), 1 rejected.')
    const table = screen.getByRole('table', { name: 'Lines that were not added' })
    expect(within(table).getByText('Already in this policy')).toBeTruthy()
    expect(within(table).getByText('An IP address, not a domain. Filtering works on domain names.')).toBeTruthy()
  })
})
