import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  createEnrollmentToken: vi.fn(),
  getEnrollmentToken: vi.fn(),
  listEnrollmentTokens: vi.fn(),
  revokeEnrollmentToken: vi.fn(),
}))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))

import { EnrollTokenModal } from './EnrollTokenModal'
import { EnrollTokensPanel } from './EnrollTokensPanel'
import { enrollCommands } from './enrollCommands'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

function wrap(node: React.ReactNode) {
  return render(
    <I18nProvider lang="en">
      <ToastProvider>{node}</ToastProvider>
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
beforeEach(() => vi.clearAllMocks())

describe('enrollCommands', () => {
  const cmds = enrollCommands('https://mgmt:8443', 'tok123')
  const byId = Object.fromEntries(cmds.map((c) => [c.id, c.text]))

  // An agent built before -site exits at once on the unknown flag. The site
  // is stored with the token, so the commands never need it.
  it('never passes a site', () => {
    for (const c of cmds) expect(c.text).not.toMatch(/-site|--site|\/SITE=/)
  })

  // Re-running a command must not spend another use of a multi-device token.
  it('enrolls with -enroll-only or the installer', () => {
    expect(byId['windows-installer']).toBe('EndpointAgent-Setup.exe /S /SERVER=https://mgmt:8443 /TOKEN=tok123')
    expect(byId['windows-cli']).toContain('-enroll-only')
    expect(byId['macos']).toContain('-enroll-only')
    expect(byId['macos']).toContain('-creds /etc/endpoint-agent/creds.json')
    expect(byId['linux']).toContain('--token tok123')
  })

  // %ALLUSERSPROFILE% is not expanded in PowerShell, and a bare exe name only
  // resolves from the install folder.
  it('gives Windows a PowerShell command with full paths', () => {
    expect(byId['windows-cli']).not.toContain('%ALLUSERSPROFILE%')
    expect(byId['windows-cli']).toContain('$env:ProgramFiles\\EndpointAgent\\endpoint-agent.exe')
  })
})

describe('EnrollTokenModal with a multi-device token', () => {
  // A shared token creates no device row until a machine enrolls, so the
  // dialog showed nothing happening even when machines were enrolling.
  it('shows how many machines have enrolled and refreshes the device list', async () => {
    api.createEnrollmentToken.mockResolvedValue({
      token_id: 'tk1', enrollment_token: 'abc', expires_at: '2026-10-22T08:00:00Z', max_uses: 5, is_multi_use: true,
    })
    api.getEnrollmentToken.mockResolvedValue({
      id: 'tk1', name: 'Batch', created_at: '', expires_at: '', max_uses: 5, use_count: 2, created_by: 'u', status: 'active',
    })
    const onEnrolled = vi.fn()
    wrap(<EnrollTokenModal open onClose={() => {}} onEnrolled={onEnrolled} />)
    fireEvent.click(screen.getByText('Generate Token'))
    expect(await screen.findByText('2 of 5 uses taken.')).toBeTruthy()
    expect(api.getEnrollmentToken).toHaveBeenCalledWith('tk1')
    expect(onEnrolled).toHaveBeenCalledTimes(1)
  })
})

describe('EnrollTokensPanel', () => {
  const tok = (over: object) => ({
    id: 'a', name: 'Branch A', site: 'Medan', created_at: '2026-10-21T00:00:00Z', expires_at: '2026-10-22T00:00:00Z',
    max_uses: 10, use_count: 10, created_by: 'u', status: 'used_up', ...over,
  })

  it('shows usage and status, and revokes after confirmation', async () => {
    api.listEnrollmentTokens.mockResolvedValue([tok({}), tok({ id: 'b', name: 'Lab', max_uses: 0, use_count: 3, status: 'active' })])
    api.revokeEnrollmentToken.mockResolvedValue(tok({ status: 'revoked' }))
    wrap(<EnrollTokensPanel />)
    expect(await screen.findByText('10 of 10')).toBeTruthy()
    expect(screen.getByText('Used up')).toBeTruthy()
    expect(screen.getByText('3 (no limit)')).toBeTruthy()

    const row = screen.getByText('Lab').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByText('Revoke'))
    expect(api.revokeEnrollmentToken).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeEnrollmentToken).toHaveBeenCalledWith('b'))
  })

  it('says when the list could not be loaded', async () => {
    api.listEnrollmentTokens.mockRejectedValue(new Error('500'))
    wrap(<EnrollTokensPanel />)
    expect(await screen.findByText('Could not load the enrollment tokens.')).toBeTruthy()
  })
})
