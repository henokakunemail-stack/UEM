/**
 * Parsing for the Devices page's own URL query string.
 *
 * The server filters by status, site and offset; the page number is derived
 * from that offset. Everything is bounded here because these values arrive
 * from a URL a user can edit by hand: `?page=1e999`, `?status=drop-tables`
 * and `?status=../../etc/passwd` all have to resolve to something the server
 * will accept rather than being forwarded verbatim.
 *
 * The page size is deliberately NOT in the URL. It is a view preference, and
 * a hand-edited `?limit=100000` would ask the server for the whole fleet in
 * one response.
 */

export const DEVICE_STATUSES = ['', 'online', 'offline', 'retired'] as const
export type DeviceStatus = (typeof DEVICE_STATUSES)[number]

export const PAGE_SIZES = [10, 25, 50] as const
export type PageSize = (typeof PAGE_SIZES)[number]

/** Upper bound on the page number. Beyond this the offset overflows into
 *  values a server would reject, and no one paginates that far by hand. */
export const MAX_PAGE = 100_000

export interface DevicesQuery {
  status: DeviceStatus
  site: string
  page: number
  /** Free-text term filtered against the CURRENT page only — the server has
   *  no search parameter, so this is explicitly a page-local filter. */
  q: string
  /** Stable device id from ?device_id=; opens the detail modal on arrival. */
  deviceId: string | null
}

function parsePage(raw: string | null): number {
  if (!raw) return 1
  // Number() rather than parseInt so "3abc" and "" are rejected outright
  // instead of silently becoming 3.
  const n = Number(raw)
  // NaN, Infinity, -Infinity. An overflowing exponent ("1e999") means "very
  // far in", so it clamps high; anything unparseable means "not a page".
  if (!Number.isFinite(n)) return n > 0 ? MAX_PAGE : 1
  if (!Number.isInteger(n) || n < 1) return 1
  return Math.min(n, MAX_PAGE)
}

export function parseDevicesQuery(params: URLSearchParams): DevicesQuery {
  const rawStatus = params.get('status') ?? ''
  const status = (DEVICE_STATUSES as readonly string[]).includes(rawStatus)
    ? (rawStatus as DeviceStatus)
    : ''

  const site = (params.get('site') ?? '').slice(0, 128)
  const q = (params.get('q') ?? '').slice(0, 128)
  const deviceId = params.get('device_id')

  return {
    status,
    site,
    page: parsePage(params.get('page')),
    q,
    // A device id is a UUID; anything longer than 64 is not one, and passing
    // junk into the path is how a 500 gets logged against a real endpoint.
    deviceId: deviceId && deviceId.length <= 64 ? deviceId : null,
  }
}

/** Builds a query string, omitting defaults so a clean page has a clean URL. */
export function buildDevicesQuery(next: Partial<DevicesQuery>): string {
  const params = new URLSearchParams()
  if (next.status) params.set('status', next.status)
  if (next.site) params.set('site', next.site)
  if (next.page && next.page > 1) params.set('page', String(next.page))
  if (next.q) params.set('q', next.q)
  if (next.deviceId) params.set('device_id', next.deviceId)
  const qs = params.toString()
  return qs ? `?${qs}` : ''
}
