import React from 'react'
import { useT } from '../i18n/core'

export interface TriStateOption<T> {
  value: T
  label: string
}

interface Props<T> {
  id: string
  label: React.ReactNode
  description?: React.ReactNode
  /** null means "not set": this policy leaves the setting to others. */
  value: T | null
  options: TriStateOption<T>[]
  onChange: (next: T | null) => void
}

/**
 * One policy setting with an explicit "not set" choice. A checkbox only had
 * two states, so leaving a box unticked saved "allow" and overrode every
 * broader policy that blocked the same thing.
 */
export function TriStateSelect<T>({ id, label, description, value, options, onChange }: Props<T>) {
  const { t } = useT()
  const index = value === null ? -1 : options.findIndex((o) => o.value === value)
  return (
    <div className="form-group">
      <label className="form-label" htmlFor={id}>
        {label}
      </label>
      <select
        id={id}
        className="form-select"
        value={String(index)}
        onChange={(e) => {
          const i = Number(e.target.value)
          onChange(i < 0 ? null : options[i].value)
        }}
      >
        <option value="-1">{t('policy.not_set_option')}</option>
        {options.map((o, i) => (
          <option key={String(i)} value={String(i)}>
            {o.label}
          </option>
        ))}
      </select>
      {description && <span className="form-hint">{description}</span>}
    </div>
  )
}
