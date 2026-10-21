import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UserDTO } from '../types/api'

const api = vi.hoisted(() => ({
  getUsers: vi.fn(),
  updateUser: vi.fn(),
  deactivateUser: vi.fn(),
  createUser: vi.fn(),
  adminResetPassword: vi.fn(),
}))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: 'admin' } }) }))

import { UsersPage } from './UsersPage'
import { changedUserFields } from './userEdits'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const user = (over: Partial<UserDTO>): UserDTO =>
  ({ id: 'x', username: 'x', role: 'technician', display_name: '', is_active: true, created_at: '', updated_at: '', ...over }) as UserDTO

const rows: UserDTO[] = [
  user({ id: 'me', username: 'me', role: 'admin' }),
  user({ id: 'u2', username: 'tech', role: 'technician', display_name: 'Tech' }),
  user({ id: 'u3', username: 'gone', role: 'viewer', is_active: false }),
]

function renderPage() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <UsersPage />
      </ToastProvider>
    </I18nProvider>,
  )
}

const rowOf = async (username: string) => (await screen.findByText(username)).closest('tr') as HTMLElement

describe('UsersPage', () => {
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
    api.getUsers.mockResolvedValue(rows)
    api.updateUser.mockResolvedValue(rows[1])
  })

  it('never offers to deactivate your own account', async () => {
    renderPage()
    expect(within(await rowOf('me')).queryByText('Deactivate')).toBeNull()
    expect(within(await rowOf('tech')).getByText('Deactivate')).toBeTruthy()
  })

  it('reactivates a disabled user after confirmation', async () => {
    renderPage()
    fireEvent.click(within(await rowOf('gone')).getByText('Reactivate'))
    expect(api.updateUser).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reactivate' }))
    await waitFor(() => expect(api.updateUser).toHaveBeenCalledWith('u3', { is_active: true }))
  })

  it('sends only the display name when only the name changed', async () => {
    renderPage()
    fireEvent.click(within(await rowOf('tech')).getByText('Edit'))
    fireEvent.change(await screen.findByLabelText(/display name/i), { target: { value: 'Tech Lead' } })
    fireEvent.submit(screen.getByLabelText(/display name/i).closest('form')!)
    await waitFor(() => expect(api.updateUser).toHaveBeenCalledWith('u2', { display_name: 'Tech Lead' }))
  })
})

describe('changedUserFields', () => {
  it('reports only real changes', () => {
    const u = user({ display_name: 'A', role: 'viewer' })
    expect(changedUserFields(u, { display_name: 'A', role: 'viewer' })).toEqual({})
    expect(changedUserFields(u, { display_name: 'B', role: 'viewer' })).toEqual({ display_name: 'B' })
    expect(changedUserFields(u, { display_name: 'A', role: 'admin' })).toEqual({ role: 'admin' })
    expect(changedUserFields(user({ display_name: undefined }), { display_name: '', role: 'technician' })).toEqual({})
  })
})
