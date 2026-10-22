import { describe, expect, it } from 'vitest'
import { isNewDevice, NEW_DEVICE_WINDOW_MS } from './newDevice'

const now = Date.parse('2026-10-22T10:00:00Z')
const ago = (ms: number) => new Date(now - ms).toISOString()
const hour = 60 * 60 * 1000

describe('isNewDevice', () => {
  it('marks a device enrolled within the last day', () => {
    expect(isNewDevice({ hostname: 'HO-IT-010', enrolled_at: ago(2 * hour), retired_at: null }, now)).toBe(true)
  })

  it('stops marking it once the day has passed', () => {
    expect(isNewDevice({ hostname: 'HO-IT-010', enrolled_at: ago(NEW_DEVICE_WINDOW_MS + 1), retired_at: null }, now)).toBe(false)
  })

  it('does not mark a retired device or a row still waiting for its agent', () => {
    expect(isNewDevice({ hostname: 'HO-IT-010', enrolled_at: ago(hour), retired_at: ago(1) }, now)).toBe(false)
    expect(isNewDevice({ hostname: 'Pending Enrollment', enrolled_at: ago(hour), retired_at: null }, now)).toBe(false)
  })

  it('ignores an unreadable timestamp rather than guessing', () => {
    expect(isNewDevice({ hostname: 'HO-IT-010', enrolled_at: '', retired_at: null }, now)).toBe(false)
  })
})
