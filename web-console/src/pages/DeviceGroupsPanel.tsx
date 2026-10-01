import React, { useCallback, useEffect, useState } from 'react'
import { FolderPlus, RefreshCw, Trash2, Users } from 'lucide-react'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceGroupDTO } from '../types/api'

/**
 * The Groups panel of the Devices page.
 *
 * Every route it calls already existed on the server
 * (device-management/inventory_handler.go:216-221) with no caller, so the
 * group target in maintenance, software deployment, task scheduler, network
 * filter and agent update could only ever resolve to zero devices. This is that
 * first caller. No new endpoint is introduced here.
 *
 * ponytail: no rename. The server exposes no group update route, and adding one
 * for a field nobody changes is not worth the migration. Delete and recreate.
 */
export const DeviceGroupsPanel: React.FC<{
  /** Switches the Devices table to this group. */
  onViewDevices: (groupId: string) => void
  /** Opens the member manager for this group. */
  onManageMembers: (groupId: string) => void
}> = ({ onViewDevices, onManageMembers }) => {
  const { can } = usePermission()
  const toast = useToast()

  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [loading, setLoading] = useState(true)

  const [editorOpen, setEditorOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const [deleteTarget, setDeleteTarget] = useState<DeviceGroupDTO | null>(null)
  const [deleting, setDeleting] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setGroups(await api.getDeviceGroups())
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to load groups', 'Groups Unavailable')
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    load()
  }, [load])

  const openCreate = () => {
    setName('')
    setDescription('')
    setFormError(null)
    setEditorOpen(true)
  }

  const save = async () => {
    const trimmed = name.trim()
    if (!trimmed) {
      setFormError('Group name is required')
      return
    }
    setSaving(true)
    setFormError(null)
    try {
      await api.createDeviceGroup(trimmed, description.trim())
      toast.success(`Group "${trimmed}" created`, 'Group Created')
      setEditorOpen(false)
      await load()
    } catch (err: unknown) {
      // 409 carries the server's own wording ("a group named X already exists"),
      // which beats anything invented here.
      setFormError(err instanceof Error ? err.message : 'Failed to create group')
    } finally {
      setSaving(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteDeviceGroup(deleteTarget.id)
      toast.success(`Group "${deleteTarget.name}" deleted`, 'Group Deleted')
      setDeleteTarget(null)
      await load()
    } catch (err: unknown) {
      // 409 from the in-use guard names the feature still pointing at the
      // group, so the operator knows what to retarget before retrying.
      toast.error(err instanceof Error ? err.message : 'Failed to delete group', 'Cannot Delete Group')
      setDeleteTarget(null)
    } finally {
      setDeleting(false)
    }
  }

  return (
    <>
      <div className="filter-bar">
        <p className="filter-hint" style={{ margin: 0 }}>
          Groups are deployment targets. A software deployment, scheduled task, filter policy,
          update campaign or maintenance job can target a whole group at once.
        </p>
        <div className="btn-group">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={load}
            disabled={loading}
            aria-label="Reload groups"
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {can('admin') && (
            <button type="button" className="btn btn-primary" onClick={openCreate}>
              <FolderPlus size={16} />
              <span>New group</span>
            </button>
          )}
        </div>
      </div>

      <div className="table-card">
        <DataTable label="Device groups">
          <thead>
            <tr>
              <th>Group</th>
              <th>Description</th>
              <th>Devices</th>
              <th>Created</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  <div className="table-loader">
                    <RefreshCw size={24} className="spinning" />
                    <span>Loading groups...</span>
                  </div>
                </td>
              </tr>
            ) : groups.length === 0 ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  No groups yet. Create one to target a set of devices with a single run.
                </td>
              </tr>
            ) : (
              groups.map((g) => (
                <tr key={g.id}>
                  <td>
                    <strong>{g.name}</strong>
                  </td>
                  <td>{g.description || <span className="text-muted">&mdash;</span>}</td>
                  <td>
                    <span className="card-badge">{g.member_count}</span>
                  </td>
                  <td>{new Date(g.created_at).toLocaleDateString()}</td>
                  <td className="text-right">
                    <div className="btn-group">
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => onManageMembers(g.id)}
                      >
                        <Users size={14} />
                        <span>Members</span>
                      </button>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => onViewDevices(g.id)}
                      >
                        <span>View devices</span>
                      </button>
                      {can('admin') && (
                        <button
                          type="button"
                          className="btn btn-sm btn-danger-outline"
                          onClick={() => setDeleteTarget(g)}
                          aria-label={`Delete group ${g.name}`}
                        >
                          <Trash2 size={14} />
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

      <Modal
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        title="New device group"
        size="sm"
        dismissible={!saving}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setEditorOpen(false)}
              disabled={saving}
            >
              Cancel
            </button>
            <button type="button" className="btn btn-primary" onClick={save} disabled={saving}>
              {saving && <RefreshCw size={14} className="spinning" />}
              <span>{saving ? 'Creating...' : 'Create group'}</span>
            </button>
          </>
        }
      >
        <div className="form-group">
          <label htmlFor="group-name">Name</label>
          <input
            id="group-name"
            type="text"
            className="text-input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Branch A Workstations"
            maxLength={128}
            disabled={saving}
          />
        </div>
        <div className="form-group">
          <label htmlFor="group-description">Description</label>
          <input
            id="group-description"
            type="text"
            className="text-input"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What this group is for"
            maxLength={256}
            disabled={saving}
          />
        </div>
        {formError && (
          <div className="alert-banner alert-error" role="alert">
            {formError}
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete group"
        message={
          deleteTarget
            ? `Delete "${deleteTarget.name}"? Its ${deleteTarget.member_count} device(s) stay enrolled — only the group and its memberships are removed.`
            : ''
        }
        confirmLabel="Delete group"
        pending={deleting}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}


