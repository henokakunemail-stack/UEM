import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { RefreshCw, UserMinus } from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { Modal } from '../components/ui/Modal'
import { usePermission } from '../hooks/usePermission'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceDTO } from '../types/api'

const PAGE = 200

/**
 * Membership manager for one group.
 *
 * Membership is a property of the group, not the device, so this lists the
 * group's current members for removal and every other device for addition. The
 * add list is the full fleet minus what is already in: the server treats a
 * duplicate membership as a no-op (PRIMARY KEY on group_id, device_id), so
 * offering an already-added row would produce a confusing "added 0" toast.
 */
export const GroupMembersModal: React.FC<{
  open: boolean
  groupId: string
  groupName: string
  onClose: () => void
  /** Called after any change so the group list can refresh its member_count. */
  onChanged: () => void
}> = ({ open, groupId, groupName, onClose, onChanged }) => {
  const { can } = usePermission()
  const toast = useToast()

  const [members, setMembers] = useState<DeviceDTO[]>([])
  const [all, setAll] = useState<DeviceDTO[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    if (!groupId) return
    setLoading(true)
    setSelected(new Set())
    try {
      // Two independent reads, run together: the roster and the candidate pool
      // are not related by offset, so paging them together would only ever show
      // candidates that happen to sit on the same page as members.
      const [memberRes, allRes] = await Promise.all([
        api.getGroupDevices(groupId, PAGE, 0),
        api.getDevices(PAGE, 0),
      ])
      setMembers(memberRes.devices || [])
      setAll(allRes.devices || [])
    } catch (err: unknown) {
      toast.error(
        err instanceof Error ? err.message : 'Failed to load group members',
        'Members Unavailable'
      )
    } finally {
      setLoading(false)
    }
  }, [groupId, toast])

  useEffect(() => {
    if (open) load()
  }, [open, load])

  const memberIds = useMemo(() => new Set(members.map((d) => d.id)), [members])
  const candidates = useMemo(() => all.filter((d) => !memberIds.has(d.id)), [all, memberIds])

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const addSelected = async () => {
    if (selected.size === 0) return
    setSaving(true)
    try {
      const added = await api.addGroupMembers(groupId, [...selected])
      // The server returns the number of rows it actually inserted, which is
      // lower than the selection when a membership already existed. Reporting
      // the real count is the whole point of returning it.
      toast.success(
        added === selected.size
          ? `Added ${added} device(s) to "${groupName}"`
          : `Added ${added} of ${selected.size} selected; the rest were already members`,
        'Members Added'
      )
      setSelected(new Set())
      await load()
      onChanged()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to add members', 'Add Failed')
    } finally {
      setSaving(false)
    }
  }

  const remove = async (device: DeviceDTO) => {
    setSaving(true)
    try {
      await api.removeGroupMember(groupId, device.id)
      toast.success(`Removed ${device.hostname} from "${groupName}"`, 'Member Removed')
      await load()
      onChanged()
    } catch (err: unknown) {
      toast.error(
        err instanceof Error ? err.message : 'Failed to remove member',
        'Remove Failed'
      )
    } finally {
      setSaving(false)
    }
  }

  const row = (d: DeviceDTO) => (
    <tr key={d.id}>
      <td>{d.hostname}</td>
      <td>{d.os_name}</td>
      <td>{d.site || <span className="text-muted">&mdash;</span>}</td>
      <td>
        <span className={`status-pill ${d.status}`}>
          <span className="dot"></span>
          {d.status}
        </span>
      </td>
    </tr>
  )

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Members of "${groupName}"`}
      size="xl"
      description="A group can belong to a device only once. Devices already in the group are not offered in the add list."
      footer={
        <>
          <span className="filter-hint" style={{ margin: 0 }}>
            {selected.size > 0
              ? `${selected.size} selected`
              : `${members.length} member(s), ${candidates.length} available`}
          </span>
          <div className="btn-group">
            <button type="button" className="btn btn-secondary" onClick={onClose}>
              Close
            </button>
            {can('admin') && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={addSelected}
                disabled={selected.size === 0 || saving}
              >
                {saving && <RefreshCw size={14} className="spinning" />}
                <span>Add selected</span>
              </button>
            )}
          </div>
        </>
      }
    >
      {loading ? (
        <div className="table-loader">
          <RefreshCw size={24} className="spinning" />
          <span>Loading members...</span>
        </div>
      ) : (
        <>
          <h3 className="card-title">Current members ({members.length})</h3>
          <DataTable label={`Members of ${groupName}`}>
            <thead>
              <tr>
                <th>Hostname</th>
                <th>OS</th>
                <th>Site</th>
                <th>Status</th>
                <th className="text-right">{can('admin') ? 'Actions' : ''}</th>
              </tr>
            </thead>
            <tbody>
              {members.length === 0 ? (
                <tr>
                  <td colSpan={5} className="text-center py-8">
                    This group has no devices yet.
                  </td>
                </tr>
              ) : (
                members.map((d) => (
                  <tr key={d.id}>
                    {row(d)}
                    {can('admin') && (
                      <td className="text-right">
                        <button
                          type="button"
                          className="btn btn-icon btn-ghost"
                          onClick={() => remove(d)}
                          disabled={saving}
                          aria-label={`Remove ${d.hostname} from ${groupName}`}
                        >
                          <UserMinus size={14} />
                        </button>
                      </td>
                    )}
                  </tr>
                ))
              )}
            </tbody>
          </DataTable>

          <h3 className="card-title">Add devices ({candidates.length})</h3>
          <DataTable label={`Devices available to add to ${groupName}`}>
            <thead>
              <tr>
                {can('admin') && <th className="text-center">Select</th>}
                <th>Hostname</th>
                <th>OS</th>
                <th>Site</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {candidates.length === 0 ? (
                <tr>
                  <td colSpan={5} className="text-center py-8">
                    {all.length === 0
                      ? 'No devices are enrolled yet.'
                      : 'Every enrolled device is already in this group.'}
                  </td>
                </tr>
              ) : (
                candidates.map((d) => (
                  <tr key={d.id}>
                    {can('admin') && (
                      <td className="text-center">
                        <input
                          type="checkbox"
                          checked={selected.has(d.id)}
                          onChange={() => toggle(d.id)}
                          disabled={saving}
                          aria-label={`Select ${d.hostname}`}
                        />
                      </td>
                    )}
                    {row(d)}
                  </tr>
                ))
              )}
            </tbody>
          </DataTable>
        </>
      )}
    </Modal>
  )
}

