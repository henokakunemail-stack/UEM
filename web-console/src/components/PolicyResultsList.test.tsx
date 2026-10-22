import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { PolicyResultsList } from './PolicyResultsList'
import { I18nProvider } from '../i18n/I18nContext'

describe('PolicyResultsList', () => {
  // A Linux device that cannot enforce a Windows-only setting used to look the
  // same as one that enforced everything.
  it('lists the settings a device did not enforce and why', () => {
    render(
      <I18nProvider lang="en">
        <PolicyResultsList
          results={[
            { setting: 'usb_storage_mode', outcome: 'applied' },
            { setting: 'disable_task_mgr', outcome: 'unsupported', reason: 'Windows-only setting' },
            { setting: 'firewall_enabled', outcome: 'failed', reason: 'access denied' },
          ]}
        />
      </I18nProvider>,
    )
    expect(screen.getByText('2 setting(s) not enforced')).toBeTruthy()
    expect(screen.getByText('Task Manager')).toBeTruthy()
    expect(screen.getByText('not supported on this OS')).toBeTruthy()
    expect(screen.getByText('Firewall')).toBeTruthy()
    expect(screen.getByText('failed')).toBeTruthy()
    expect(screen.queryByText('USB storage')).toBeNull()
  })

  it('renders nothing when every setting applied', () => {
    const { container } = render(
      <I18nProvider lang="en">
        <PolicyResultsList results={[{ setting: 'usb_storage_mode', outcome: 'applied' }]} />
      </I18nProvider>,
    )
    expect(container.textContent).toBe('')
  })
})
