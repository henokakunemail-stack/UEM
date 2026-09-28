// Assert-based self-check for the Devices page's URL parsing — the one piece
// of hand-editable input on that page. Run: `node scripts/test-devices-query.mjs`
// (Node 22.6+ strips the TypeScript annotations on import; no test framework).

import assert from 'node:assert/strict'
import {
  MAX_PAGE,
  buildDevicesQuery,
  parseDevicesQuery,
} from '../src/pages/devicesQuery.ts'

const P = (qs) => parseDevicesQuery(new URLSearchParams(qs))

// Defaults: an empty URL is an unfiltered first page.
assert.deepEqual(P(''), { status: '', site: '', page: 1, q: '', deviceId: null })

// Server filters pass through.
assert.equal(P('?status=retired').status, 'retired')
assert.equal(P('?status=online&page=3').page, 3)
assert.equal(P('?site=branch-a').site, 'branch-a')

// Unknown enum values fall back to "all" rather than being forwarded to the
// server, which would reject the whole list.
assert.equal(P('?status=drop-tables').status, '')
assert.equal(P('?status=ONLINE').status, '')

// Bad numerics are bounded, never NaN.
assert.equal(P('?page=0').page, 1)
assert.equal(P('?page=-5').page, 1)
assert.equal(P('?page=abc').page, 1)
assert.equal(P('?page=3abc').page, 1, 'parseInt would have accepted 3abc')
assert.equal(P('?page=2.5').page, 1)
assert.equal(P(`?page=${MAX_PAGE + 1}`).page, MAX_PAGE)
assert.equal(P('?page=1e999').page, MAX_PAGE)

// Overlong free text and ids are bounded at the boundary.
assert.equal(P(`?q=${'x'.repeat(200)}`).q.length, 128)
assert.equal(P(`?device_id=${'y'.repeat(200)}`).deviceId, null)
assert.equal(P('?device_id=abc-123').deviceId, 'abc-123')

// Round-trip: defaults are omitted, non-defaults are preserved.
assert.equal(buildDevicesQuery({}), '')
assert.equal(
  buildDevicesQuery({ page: 1, status: '', site: '', q: '', deviceId: null }),
  ''
)
assert.equal(
  buildDevicesQuery({ status: 'offline', page: 4, q: 'lap' }),
  '?status=offline&page=4&q=lap'
)

console.log('devicesQuery: all assertions passed')
