import React, { useCallback, useEffect, useState } from 'react'
import {
  Edit2,
  Key,
  RefreshCw,
  ShieldAlert,
  UserPlus,
  Users,
  UserX,
  X,
} from 'lucide-react'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import type { UserDTO } from '../types/api'

type Role = 'admin' | 'technician' | 'viewer'

export const UsersPage: React.FC = () => {
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

  const loadUsers = useCallback(async () => {
    setLoading(true)
    try {
      setUsers(await api.getUsers())
    } catch (err: unknown) {
      setMsg({
        type: 'error',
        text: err instanceof Error ? err.message : 'Failed to load user accounts',
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
      const successText = `User account '${newUsername}' created successfully.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'User Created')
      setIsCreateModalOpen(false)
      setNewUsername('')
      setNewDisplayName('')
      setNewPassword('')
      loadUsers()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to create user'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Creation Failed')
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
    setSaving(true)
    try {
      await api.updateUser(selectedUser.id, {
        display_name: editDisplayName,
        role: editRole,
      })
      const successText = `User '${selectedUser.username}' updated.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'User Updated')
      setIsEditModalOpen(false)
      loadUsers()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to update user'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Update Failed')
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
      setMsg({ type: 'error', text: 'Passwords do not match!' })
      toast.warning('Passwords do not match', 'Validation Error')
      return
    }
    if (resetPassword.length < 8) {
      setMsg({ type: 'error', text: 'Password must be at least 8 characters long.' })
      toast.warning('Password must be at least 8 characters long.', 'Validation Error')
      return
    }

    setSaving(true)
    try {
      await api.adminResetPassword(selectedUser.id, resetPassword)
      const successText = `Password for '${selectedUser.username}' updated successfully.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Password Reset')
      setIsPasswordModalOpen(false)
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to reset password'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Reset Failed')
    } finally {
      setSaving(false)
    }
  }

  const handleDeactivate = async () => {
    if (!deactivateTarget) return
    setDeactivating(true)
    try {
      await api.deactivateUser(deactivateTarget.id)
      const infoText = `User '${deactivateTarget.username}' deactivated.`
      setMsg({ type: 'success', text: infoText })
      toast.info(infoText, 'Account Deactivated')
      setDeactivateTarget(null)
      loadUsers()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to deactivate user'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Deactivation Failed')
    } finally {
      setDeactivating(false)
    }
  }

  if (!isAdmin) {
    return (
      <div className="page-container">
        <div className="page-header">
          <div>
            <h2 className="page-title">User Accounts &amp; RBAC Permissions</h2>
            <p className="page-subtitle">
              Enterprise role-based access control, operator identity management, and credential
              provisioning
            </p>
          </div>
        </div>
        <div className="alert-banner alert-error" role="status">
          <ShieldAlert size={18} />
          <span>
            Operator accounts are administrator-only. Your role can read the audit trail, not
            manage users.
          </span>
        </div>
      </div>
    )
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">User Accounts &amp; RBAC Permissions</h2>
          <p className="page-subtitle">
            Enterprise role-based access control, operator identity management, and credential
            provisioning
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
            <span>Refresh</span>
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => setIsCreateModalOpen(true)}
          >
            <UserPlus size={16} />
            <span>Create User</span>
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
            aria-label="Dismiss notification"
          >
            <X size={14} />
          </button>
        </div>
      )}

      {/* Users Table */}
      <div className="table-card">
        <DataTable label="Console operator accounts">
          <thead>
            <tr>
              <th>Username</th>
              <th>Display Name</th>
              <th>Assigned Role</th>
              <th>Account Status</th>
              <th>Last Login</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={6} className="text-center py-8 text-muted">
                  Loading accounts…
                </td>
              </tr>
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-center py-8 text-muted">
                  No users found.
                </td>
              </tr>
            ) : (
              users.map((u) => (
                <tr key={u.id}>
                  <td data-label="Username">
                    <div className="font-semibold text-main flex items-center gap-2">
                      <Users size={16} className="text-primary" />
                      <span>{u.username}</span>
                    </div>
                  </td>
                  <td data-label="Display Name">{u.display_name || '—'}</td>
                  <td data-label="Assigned Role">
                    <span
                      className={`badge-role ${
                        u.role === 'admin'
                          ? 'role-admin'
                          : u.role === 'technician'
                            ? 'role-technician'
                            : 'role-viewer'
                      }`}
                    >
                      {u.role.toUpperCase()}
                    </span>
                  </td>
                  <td data-label="Account Status">
                    <span className={`status-pill ${u.is_active ? 'online' : 'offline'}`}>
                      {u.is_active ? 'active' : 'deactivated'}
                    </span>
                  </td>
                  <td data-label="Last Login" className="text-sm text-muted">
                    {u.last_login_at
                      ? new Date(u.last_login_at).toLocaleString()
                      : 'Never signed in'}
                  </td>
                  <td data-label="Actions">
                    <div className="action-buttons">
                      <button
                        type="button"
                        className="btn-action"
                        onClick={() => handleOpenEdit(u)}
                        title="Edit role / name"
                      >
                        <Edit2 size={14} />
                        <span>Edit</span>
                      </button>
                      <button
                        type="button"
                        className="btn-action text-warning"
                        onClick={() => handleOpenPasswordReset(u)}
                        title="Reset password"
                      >
                        <Key size={14} />
                        <span>Password</span>
                      </button>
                      {u.is_active && u.username !== 'admin' && (
                        <button
                          type="button"
                          className="btn-action text-danger"
                          onClick={() => setDeactivateTarget(u)}
                          title="Deactivate account"
                        >
                          <UserX size={14} />
                          <span>Deactivate</span>
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
        title="Create Console Operator Account"
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsCreateModalOpen(false)}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="create-user-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? 'Creating…' : 'Create User'}
            </button>
          </>
        }
      >
        {/* The footer buttons live outside this form in the <dialog>, so the
            form is associated by id rather than wrapping the footer. */}
        <form id="create-user-form" onSubmit={handleCreateUser} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="new-username">
              Username
            </label>
            <input
              id="new-username"
              type="text"
              className="form-input"
              required
              placeholder="e.g. jdoe"
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-display-name">
              Display Name
            </label>
            <input
              id="new-display-name"
              type="text"
              className="form-input"
              required
              placeholder="e.g. John Doe"
              value={newDisplayName}
              onChange={(e) => setNewDisplayName(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-password">
              Temporary Password
            </label>
            <input
              id="new-password"
              type="password"
              className="form-input"
              required
              minLength={8}
              placeholder="Min 8 characters"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="new-role">
              RBAC Role
            </label>
            <select
              id="new-role"
              className="form-select"
              value={newRole}
              onChange={(e) => setNewRole(e.target.value as Role)}
            >
              <option value="technician">Technician / Operator (Deploy, Exec, Patches)</option>
              <option value="admin">Administrator (Full Access &amp; User Mgmt)</option>
              <option value="viewer">Viewer (Read-only)</option>
            </select>
          </div>
        </form>
      </Modal>

      {/* Edit User Modal */}
      <Modal
        open={isEditModalOpen && Boolean(selectedUser)}
        onClose={() => setIsEditModalOpen(false)}
        title={selectedUser ? `Edit User: ${selectedUser.username}` : ''}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsEditModalOpen(false)}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="edit-user-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? 'Saving…' : 'Save Changes'}
            </button>
          </>
        }
      >
        <form id="edit-user-form" onSubmit={handleSaveEdit} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="edit-display-name">
              Display Name
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
              Role
            </label>
            <select
              id="edit-role"
              className="form-select"
              value={editRole}
              onChange={(e) => setEditRole(e.target.value as Role)}
            >
              <option value="technician">Technician / Operator</option>
              <option value="admin">Administrator</option>
              <option value="viewer">Viewer (Read-only)</option>
            </select>
          </div>
        </form>
      </Modal>

      {/* Password Reset Modal */}
      <Modal
        open={isPasswordModalOpen && Boolean(selectedUser)}
        onClose={() => setIsPasswordModalOpen(false)}
        title={selectedUser ? `Reset Password: ${selectedUser.username}` : ''}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsPasswordModalOpen(false)}
            >
              Cancel
            </button>
            <button
              type="submit"
              form="reset-password-form"
              className="btn btn-primary"
              disabled={saving}
            >
              {saving ? 'Updating…' : 'Update Password'}
            </button>
          </>
        }
      >
        <form id="reset-password-form" onSubmit={handleSavePassword} className="space-y-4">
          <div className="form-group">
            <label className="form-label" htmlFor="reset-password">
              New Password
            </label>
            <input
              id="reset-password"
              type="password"
              className="form-input"
              required
              minLength={8}
              placeholder="Enter new password"
              value={resetPassword}
              onChange={(e) => setResetPassword(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="confirm-password">
              Confirm New Password
            </label>
            <input
              id="confirm-password"
              type="password"
              className="form-input"
              required
              placeholder="Re-type new password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={Boolean(deactivateTarget)}
        title="Deactivate account"
        message={
          deactivateTarget
            ? `Deactivate '${deactivateTarget.username}'? They will be signed out and unable to sign in again. The audit trail of their past actions is kept.`
            : ''
        }
        confirmLabel="Deactivate"
        pending={deactivating}
        onConfirm={handleDeactivate}
        onCancel={() => setDeactivateTarget(null)}
      />
    </div>
  )
}
