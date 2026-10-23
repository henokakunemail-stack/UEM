import React, { useState } from 'react'
import { Check, Pencil, X } from 'lucide-react'
import { useT } from '../i18n/core'
import { api } from '../services/api'
import type { DeviceDTO } from '../types/api'

interface DeviceSiteCellProps {
  device: DeviceDTO
  /** Technician and up, and not for a retired device. */
  canEdit: boolean
  /** id of a <datalist> holding the sites already in use. */
  suggestionsId?: string
  onSaved: (device: DeviceDTO) => void
}

// The site badge in a Devices row. Editing it here is the path an operator
// reaches for first: a fleet enrolled with one multi-device token is all
// Unassigned, and the badge is where that shows.
export function DeviceSiteCell({ device, canEdit, suggestionsId, onSaved }: DeviceSiteCellProps) {
  const { t } = useT()
  // null while closed, the draft text while open.
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const close = () => {
    setDraft(null)
    setError(null)
  }

  if (draft === null) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <span className="site-badge">{device.site || t('devices.site_unassigned')}</span>
        {canEdit && (
          <button
            type="button"
            className="btn-action"
            onClick={() => setDraft(device.site || '')}
            aria-label={t('devices.site_edit_row', { hostname: device.hostname })}
            title={t('device_detail.site_edit')}
          >
            <Pencil size={12} />
          </button>
        )}
      </span>
    )
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    const value = draft.trim()
    if (!value) {
      setError(t('device_detail.site_required'))
      return
    }
    setSaving(true)
    try {
      const updated = await api.updateDevice(device.id, { site: value })
      close()
      onSaved(updated)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('device_detail.site_save_failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={save} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <input
          type="text"
          className="search-input"
          style={{ width: 120, padding: '4px 8px' }}
          maxLength={64}
          autoFocus
          list={suggestionsId}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            setError(null)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') close()
          }}
          aria-label={t('devices.site_row_input', { hostname: device.hostname })}
        />
        <button type="submit" className="btn-action" disabled={saving} aria-label={t('device_detail.site_save')} title={t('device_detail.site_save')}>
          <Check size={12} />
        </button>
        <button type="button" className="btn-action" onClick={close} aria-label={t('common.cancel')} title={t('common.cancel')}>
          <X size={12} />
        </button>
      </span>
      {error && (
        <span className="text-danger" role="alert" style={{ fontSize: 12 }}>
          {error}
        </span>
      )}
    </form>
  )
}
