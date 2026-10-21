import React, { useCallback, useEffect, useState } from 'react'
import {
  Edit2,
  Key,
  RefreshCw,
  ShieldAlert,
  UserCheck,
  UserPlus,
  Users,
  UserX,
  X,
} from 'lucide-react'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { ApiError, api } from '../services/api'
import { useAuth } from '../context/AuthContext'
import { changedUserFields, USER_ERRORS } from './userEdits'
import { useToast } from '../context/ToastContext'
import { useT } from '../i18n/core'
import type { UserDTO } from '../types/api'

type Role = 'admin' | 'technician' | 'viewer'

// Display label key per role value. The value is what the API stores; only the
// label is translated, at render.
const ROLE_KEYS: Record<string, string> = {
  admin: 'users.role_admin',
  technician: 'users.role_technician',
  viewer: 'users.role_viewer',
}

export const UsersPage: React.FC = () => {
  const { t } = useT()
  const { user } = useAuth()

  // A refusal the server explains with a code is shown translated; anything
  // else keeps the server's message as before.
  const userError = (err: unknown, fallbackKey: string) =>
    err instanceof ApiError && err.code && USER_ERRORS[err.code]
      ? t(USER_ERRORS[err.code])
      : err instanceof Error
        ? err.message
        : t(fallbackKey)
  const { can } = usePermission()
  // Every /api/users route except the self-service password change is admin
  // only, so a technician gets an explanation rather than a 403 on load.
  const isAdmin = can('admin')
  const [users, setUsers] = useState<UserDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // Modals
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false)
  const [isEditModalOpen, setIsEditModalOpen] = useState(false)
  const [isPasswordModalOpen, setIsPasswordModalOpen] = useState(false)
  const [selectedUser, setSelectedUser] = useState<UserDTO | null>(null)
  const [deactivateTarget, setDeactivateTarget] = useState<UserDTO | null>(null)
  const [deactivating, setDeactivating] = useState(false)
  const [reactivateTarget, setReactivateTarget] = useState<UserDTO | null>(null)
  const [reactivating, setReactivating] = useState(false)
  const [saving, setSaving] = useState(false)

  // Create Form
  const [newUsername, setNewUsername] = useState('')
  const [newDisplayName, setNewDisplayName] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<Role>('technician')

  // Edit Form
  const [editDisplayName, setEditDisplayName] = useState('')
  const [editRole, setEditRole] = useState<Role>('technician')

  // Password Reset Form
  const [resetPassword, setResetPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')

  const roleLabel = (role: string) => (ROLE_KEYS[role] ? t(ROLE_KEYS[role]) : role)

  const loadUsers = useCallback(async () => {
    setLoading(true)
    try {
      setUsers(await api.getUsers())
    } catch (err: unknown) {
      setMsg({
        type: 'error',
        text: err instanceof Error ? err.message : t('users.load_failed'),
      })
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (isAdmin) loadUsers()
    else setLoading(false)
  }, [isAdmin, loadUsers])

  const handleCreateUser = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      await api.createUser({
        username: newUsername,
        display_name: newDisplayName,
        password: newPassword,
        role: newRole,
      })
      const successText = t('users.created', { username: newUsername })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('users.toast_created'))
      setIsCreateModalOpen(false)
      setNewUsername('')
      setNewDisplayName('')
      setNewPassword('')
      loadUsers()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : t('users.create_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('users.toast_create_failed'))
    } finally {
      setSaving(false)
    }
  }

  const handleOpenEdit = (u: UserDTO) => {
    setSelectedUser(u)
    // display_name is a nullable column on the server, so an unset name must
    // open the editor as an empty string, not as null.
    setEditDisplayName(u.display_name || '')
    setEditRole(u.role)
    setIsEditModalOpen(true)
  }

  const handleSaveEdit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedUser) return
    const changes = changedUserFields(selectedUser, { display_name: editDisplayName, role: editRole })
    if (Object.keys(changes).length === 0) {
      toast.info(t('users.no_changes'), t('users.toast_updated'))
      setIsEditModalOpen(false)
      return
    }
    setSaving(true)
    try {
      await api.updateUser(selectedUser.id, changes)
      const successText = t('users.updated', { username: selectedUser.username })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('users.toast_updated'))
      setIsEditModalOpen(false)
      loadUsers()
    } catch (err: unknown) {
      const errorText = userError(err, 'users.update_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('users.toast_update_failed'))
    } finally {
      setSaving(false)
    }
  }

  const handleOpenPasswordReset = (u: UserDTO) => {
    setSelectedUser(u)
    setResetPassword('')
    setConfirmPassword('')
    setIsPasswordModalOpen(true)
  }

  const handleSavePassword = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedUser) return
    if (resetPassword !== confirmPassword) {
      setMsg({ type: 'error', text: t('users.passwords_mismatch_banner') })
      toast.warning(t('users.passwords_mismatch'), t('users.toast_validation_error'))
      return
    }
    if (resetPassword.length < 8) {
      setMsg({ type: 'error', text: t('users.password_too_short') })
      toast.warning(t('users.password_too_short'), t('users.toast_validation_error'))
      return
    }

    setSaving(true)
    try {
      await api.adminResetPassword(selectedUser.id, resetPassword)
      const successText = t('users.password_updated', { username: selectedUser.username })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('users.toast_password_reset'))
      setIsPasswordModalOpen(false)
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : t('users.password_reset_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('users.toast_reset_failed'))
    } finally {
      setSaving(false)
    }
  }

  const handleDeactivate = async () => {
    if (!deactivateTarget) return
    setDeactivating(true)
    try {
      await api.deactivateUser(deactivateTarget.id)
      const infoText = t('users.deactivated', { username: deactivateTarget.username })
      setMsg({ type: 'success', text: infoText })
      toast.info(infoText, t('users.toast_deactivated'))
      setDeactivateTarget(null)
      loadUsers()
    } catch (err: unknown) {
      const errorText = userError(err, 'users.deactivate_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('users.toast_deactivate_failed'))
    } finally {
      setDeactivating(false)
    }
  }

  const handleReactivate = async () => {
    if (!reactivateTarget) return
    setReactivating(true)
    try {
      await api.updateUser(reactivateTarget.id, { is_active: true })
      const infoText = t('users.reactivated', { username: reactivateTarget.username })
      setMsg({ type: 'success', text: infoText })
      toast.success(infoText, t('users.toast_reactivated'))
      setReactivateTarget(null)
      loadUsers()
    } catch (err: unknown) {
      const errorText = userError(err, 'users.reactivate_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('users.toast_reactivate_failed'))
    } finally {
      setReactivating(false)
    }
  }

  if (!isAdmin) {
    return (
      <div className="page-container">
        <div className="page-header">
          <div>
            <h2 className="page-title">{t('users.title')}</h2>
            <p className="page-subtitle">
              {t('users.subtitle')}
            </p>
          </div>
        </div>
        <div className="alert-banner alert-error" role="status">
          <ShieldAlert size={18} />
          <span>
            {t('users.admin_only')}
          </span>
        </div>
      </div>
    )
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('users.title')}</h2>
          <p className="page-subtitle">
            {t('users.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={loadUsers}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'animate-spin' : ''} />
            <span>{t('users.refresh')}</span>
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => setIsCreateModalOpen(true)}
          >
            <UserPlus size={16} />
            <span>{t('users.create_user')}</span>
          </button>
        </div>
      </div>

      {msg && (
        <div
          className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}
          role="status"
        >
          <span>{msg.text}</span>
          <button
            type="button"
            onClick={() => setMsg(null)}
            className="close-btn"
            aria-label={t('users.dismiss_notification')}
          >
            <X size={14} />
          </button>
        </div>
      )}

      {/* Users Table */}
      <div className="table-card">
        <DataTable label={t('users.table_label')}>
          <thead>
            <tr>
              <th>{t('users.col_username')}</th>
              <th>{t('users.col_display_name')}</th>
              <th>{t('users.col_role')}</th>
              <th>{t('users.col_status')}</th>
              <th>{t('users.col_last_login')}</th>
              <th>{t('users.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={6} className="text-center py-8 text-muted">
                  {t('users.loading')}
                </td>
              </tr>
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-center py-8 text-muted">
                  {t('users.empty')}
                </td>
              </tr>
            ) : (
              users.map((u) => (
                <tr key={u.id}>
                  <td data-label={t('users.col_username')}>
                    <div className="font-semibold text-main flex items-center gap-2">
                      <Users size={16} className="text-primary" />
                      <span>{u.username}</span>
                    </div>
                  </td>
                  <td data-label={t('users.col_display_name')}>{u.display_name || '—'}</td>
                  <td data-label={t('users.col_role')}>
                    <span
                      className={`badge-role ${
                        u.role === 'admin'
                          ? 'role-admin'
                          : u.role === 'technician'
                            ? 'role-technician'
                            : 'role-viewer'
                      }`}
                    >
                      {roleLabel(u.role).toUpperCase()}
                    </span>
                  </td>
                  <td data-label={t('users.col_status')}>
                    <span className={`status-pill ${u.is_active ? 'online' : 'offline'}`}>
                      {u.is_active ? t('users.status_active') : t('users.status_deactivated')}
                    </span>
                  </td>
                  <td data-label={t('users.col_last_login')} className="text-sm text-muted">
                    {u.last_login_at
                      ? new Date(u.last_login_at).toLocaleString()
                      : t('users.never_signed_in')}
                  </td>
                  <td data-label={t('users.col_actions')}>
                    <div className="action-buttons">
                      <button
                        type="button"
                        className="btn-action"
                        onClick={() => handleOpenEdit(u)}
                        title={t('users.edit_title')}
                      >
                        <Edit2 size={14} />
                        <span>{t('users.edit')}</span>
                      </button>
                      <button
                        type="button"
                        className="btn-action text-warning"
                        onClick={() => handleOpenPasswordReset(u)}
                        title={t('users.reset_password_title')}
                      >
                        <Key size={14} />
                        <span>{t('users.password')}</span>
                      </button>
                      {/* Your own account is never offered for deactivation: the
                          server refuses it, and it would sign you out. */}
                      {u.is_active && u.id !== user?.uid && (
                        <button
                          type="button"
                          className="btn-action text-danger"
                          onClick={() => setDeactivateTarget(u)}
                          title={t('users.deactivate_title')}
                        >
                          <UserX size={14} />
                          <span>{t('users.deactivate')}</span>
                        </button>
                      )}
                      {!u.is_active && (
                        <button
                          type="button"
                          className="btn-action"
                          onClick={() => setReactivateTarget(u)}
                          title={t('users.reactivate_title')}
                        >
                          <UserCheck size={14} />
                          <span>{t('users.reactivate')}</span>
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </DataTable>
      </div>

      {/* Create User Modal */}
      <Modal
        open={isCreateModalOpen}
        onClose={() => setIsCreateModalOpen(false)}
        title={t('users.create_modal_title')}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsCreateModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="create-user-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? t('users.creating') : t('users.create_user')}
            </button>
          </>
        }
      >
        {/* The footer buttons live outside this form in the <dialog>, so the
            form is associated by id rather than wrapping the footer. */}
        <form id="create-user-form" onSubmit={handleCreateUser} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="new-username">
              {t('users.col_username')}
            </label>
            <input
              id="new-username"
              type="text"
              className="form-input"
              required
              placeholder={t('users.placeholder_username')}
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-display-name">
              {t('users.col_display_name')}
            </label>
            <input
              id="new-display-name"
              type="text"
              className="form-input"
              required
              placeholder={t('users.placeholder_display_name')}
              value={newDisplayName}
              onChange={(e) => setNewDisplayName(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-password">
              {t('users.temporary_password')}
            </label>
            <input
              id="new-password"
              type="password"
              className="form-input"
              required
              minLength={8}
              placeholder={t('users.placeholder_min_chars')}
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-role">
              {t('users.rbac_role')}
            </label>
            <select
              id="new-role"
              className="form-select"
              value={newRole}
              onChange={(e) => setNewRole(e.target.value as Role)}
            >
              <option value="technician">{t('users.option_technician_long')}</option>
              <option value="admin">{t('users.option_admin_long')}</option>
              <option value="viewer">{t('users.option_viewer')}</option>
            </select>
          </div>
        </form>
      </Modal>

      {/* Edit User Modal */}
      <Modal
        open={isEditModalOpen && Boolean(selectedUser)}
        onClose={() => setIsEditModalOpen(false)}
        title={selectedUser ? t('users.edit_modal_title', { username: selectedUser.username }) : ''}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsEditModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="edit-user-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? t('users.saving') : t('users.save_changes')}
            </button>
          </>
        }
      >
        <form id="edit-user-form" onSubmit={handleSaveEdit} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="edit-display-name">
              {t('users.col_display_name')}
            </label>
            <input
              id="edit-display-name"
              type="text"
              className="form-input"
              required
              value={editDisplayName}
              onChange={(e) => setEditDisplayName(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="edit-role">
              {t('users.role')}
            </label>
            <select
              id="edit-role"
              className="form-select"
              value={editRole}
              onChange={(e) => setEditRole(e.target.value as Role)}
            >
              <option value="technician">{t('users.option_technician')}</option>
              <option value="admin">{t('users.option_admin')}</option>
              <option value="viewer">{t('users.option_viewer')}</option>
            </select>
          </div>
        </form>
      </Modal>

      {/* Password Reset Modal */}
      <Modal
        open={isPasswordModalOpen && Boolean(selectedUser)}
        onClose={() => setIsPasswordModalOpen(false)}
        title={selectedUser ? t('users.reset_modal_title', { username: selectedUser.username }) : ''}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsPasswordModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              form="reset-password-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? t('users.updating') : t('users.update_password')}
            </button>
          </>
        }
      >
        <form id="reset-password-form" onSubmit={handleSavePassword} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="reset-password">
              {t('users.new_password')}
            </label>
            <input
              id="reset-password"
              type="password"
              className="form-input"
              required
              minLength={8}
              placeholder={t('users.placeholder_new_password')}
              value={resetPassword}
              onChange={(e) => setResetPassword(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="confirm-password">
              {t('users.confirm_new_password')}
            </label>
            <input
              id="confirm-password"
              type="password"
              className="form-input"
              required
              placeholder={t('users.placeholder_confirm_password')}
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={Boolean(deactivateTarget)}
        title={t('users.deactivate_dialog_title')}
        message={
          deactivateTarget
            ? t('users.deactivate_dialog_message', { username: deactivateTarget.username })
            : ''
        }
        confirmLabel={t('users.deactivate')}
        pending={deactivating}
        onConfirm={handleDeactivate}
        onCancel={() => setDeactivateTarget(null)}
      />

      <ConfirmDialog
        open={Boolean(reactivateTarget)}
        title={t('users.reactivate_dialog_title')}
        message={
          reactivateTarget
            ? t('users.reactivate_dialog_message', { username: reactivateTarget.username })
            : ''
        }
        confirmLabel={t('users.reactivate')}
        pending={reactivating}
        onConfirm={handleReactivate}
        onCancel={() => setReactivateTarget(null)}
      />
    </div>
  )
}
