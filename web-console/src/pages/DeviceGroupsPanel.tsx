import React, { useCallback, useEffect, useState } from 'react'
import { FolderPlus, RefreshCw, Trash2, Users } from 'lucide-react'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceGroupDTO } from '../types/api'
import { useT } from '../i18n/core'

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
  const { t } = useT()

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
      toast.error(err instanceof Error ? err.message : t('groups.load_failed'), t('groups.unavailable_title'))
    } finally {
      setLoading(false)
    }
  }, [toast, t])

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
      setFormError(t('groups.name_required'))
      return
    }
    setSaving(true)
    setFormError(null)
    try {
      await api.createDeviceGroup(trimmed, description.trim())
      toast.success(t('groups.created', { name: trimmed }), t('groups.created_title'))
      setEditorOpen(false)
      await load()
    } catch (err: unknown) {
      // 409 carries the server's own wording ("a group named X already exists"),
      // which beats anything invented here.
      setFormError(err instanceof Error ? err.message : t('groups.create_failed'))
    } finally {
      setSaving(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteDeviceGroup(deleteTarget.id)
      toast.success(t('groups.deleted', { name: deleteTarget.name }), t('groups.deleted_title'))
      setDeleteTarget(null)
      await load()
    } catch (err: unknown) {
      // 409 from the in-use guard names the feature still pointing at the
      // group, so the operator knows what to retarget before retrying.
      toast.error(err instanceof Error ? err.message : t('groups.delete_failed'), t('groups.delete_failed_title'))
      setDeleteTarget(null)
    } finally {
      setDeleting(false)
    }
  }

  return (
    <>
      <div className="filter-bar">
        <p className="filter-hint" style={{ margin: 0 }}>
          {t('groups.intro')}
        </p>
        <div className="btn-group">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={load}
            disabled={loading}
            aria-label={t('groups.reload')}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('groups.refresh')}</span>
          </button>
          {can('admin') && (
            <button type="button" className="btn btn-primary" onClick={openCreate}>
              <FolderPlus size={16} />
              <span>{t('groups.new_group')}</span>
            </button>
          )}
        </div>
      </div>

      <div className="table-card">
        <DataTable label={t('groups.table_label')}>
          <thead>
            <tr>
              <th>{t('groups.col_group')}</th>
              <th>{t('groups.col_description')}</th>
              <th>{t('groups.col_devices')}</th>
              <th>{t('groups.col_created')}</th>
              <th className="text-right">{t('groups.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  <div className="table-loader">
                    <RefreshCw size={24} className="spinning" />
                    <span>{t('groups.loading')}</span>
                  </div>
                </td>
              </tr>
            ) : groups.length === 0 ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  {t('groups.empty')}
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
                        <span>{t('groups.members')}</span>
                      </button>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => onViewDevices(g.id)}
                      >
                        <span>{t('groups.view_devices')}</span>
                      </button>
                      {can('admin') && (
                        <button
                          type="button"
                          className="btn btn-sm btn-danger-outline"
                          onClick={() => setDeleteTarget(g)}
                          aria-label={t('groups.delete_label', { name: g.name })}
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
        title={t('groups.editor_title')}
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
              {t('common.cancel')}
            </button>
            <button type="button" className="btn btn-primary" onClick={save} disabled={saving}>
              {saving && <RefreshCw size={14} className="spinning" />}
              <span>{saving ? t('groups.creating') : t('groups.create')}</span>
            </button>
          </>
        }
      >
        <div className="form-group">
          <label htmlFor="group-name">{t('groups.name')}</label>
          <input
            id="group-name"
            type="text"
            className="text-input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('groups.name_placeholder')}
            maxLength={128}
            disabled={saving}
          />
        </div>
        <div className="form-group">
          <label htmlFor="group-description">{t('groups.description')}</label>
          <input
            id="group-description"
            type="text"
            className="text-input"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t('groups.description_placeholder')}
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
        title={t('groups.delete_title')}
        message={
          deleteTarget
            ? t('groups.delete_message', { name: deleteTarget.name, count: deleteTarget.member_count })
            : ''
        }
        confirmLabel={t('groups.delete_confirm')}
        pending={deleting}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}


