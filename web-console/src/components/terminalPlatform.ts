import type { DeviceDTO } from '../types/api'

/** Windows endpoints keep the pipe-based terminal they have always had; Linux
 *  and macOS get the xterm.js terminal in front of a pty. */
export function isWindowsDevice(device: DeviceDTO | null): boolean {
  return (device?.os_name ?? '').toLowerCase().includes('windows')
}
