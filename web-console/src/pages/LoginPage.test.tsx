import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../services/api'

const login = vi.hoisted(() => vi.fn())
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ login }) }))

import { LoginPage } from './LoginPage'
import { ThemeProvider } from '../context/ThemeContext'
import { I18nProvider } from '../i18n/I18nContext'

function signIn(lang: 'en' | 'id' = 'en') {
  render(
    <I18nProvider lang={lang}>
      <ThemeProvider>
        <LoginPage />
      </ThemeProvider>
    </I18nProvider>,
  )
  fireEvent.change(screen.getByPlaceholderText(/username|pengguna/i), { target: { value: 'alice' } })
  fireEvent.change(screen.getByPlaceholderText(/password|sandi/i), { target: { value: 'x' } })
  fireEvent.submit(screen.getByRole('button', { name: /sign in|masuk/i }).closest('form')!)
}

// The server's reason arrives as a code; the operator reads it in their
// language, never as the server's English text.
describe('LoginPage errors', () => {
  it('translates a locked account with the wait from Retry-After', async () => {
    login.mockRejectedValueOnce(new ApiError('too many failed login attempts, please try again in 42 seconds', 429, 'too_many_attempts', 42))
    signIn('id')
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('Terlalu banyak percobaan gagal. Coba lagi dalam 42 detik.'))
  })

  it('translates a disabled account', async () => {
    login.mockRejectedValueOnce(new ApiError('account is disabled', 401, 'account_disabled'))
    signIn()
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('This account is disabled.'))
  })

  it('shows a generic message, not raw server text, for anything else', async () => {
    login.mockRejectedValueOnce(new Error('dial tcp: connection refused'))
    signIn()
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('Unable to sign in'))
    expect(screen.getByRole('alert').textContent).not.toContain('dial tcp')
  })
})
