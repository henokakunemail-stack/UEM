import type { DeviceDTO } from '../types/api'

// How long a device carries the "New" badge after its agent enrolled.
export const NEW_DEVICE_WINDOW_MS = 24 * 60 * 60 * 1000

// A device is new for the first day after its agent enrolled. A retired one is
// not, and neither is a pre-registered row whose agent has not enrolled yet:
// that row was written when the token was issued, and nothing has joined.
export function isNewDevice(
  d: Pick<DeviceDTO, 'enrolled_at' | 'retired_at' | 'hostname'>,
  now: number,
): boolean {
  if (d.retired_at || d.hostname === 'Pending Enrollment') return false
  const enrolled = Date.parse(d.enrolled_at)
  if (Number.isNaN(enrolled)) return false
  return now - enrolled < NEW_DEVICE_WINDOW_MS
}
