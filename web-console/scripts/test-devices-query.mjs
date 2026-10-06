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

// Defaults: an empty URL is an unfiltered first page, on the devices tab.
//
// deepEqual against the whole object rather than field-by-field on purpose: this
// is what caught tab/group going missing from the expectations when they were
// added. The assertion failed the first time it was ever run, because nothing ran
// it -- the file sat outside every npm script until this was wired into `npm test`.
assert.deepEqual(P(''), {
  status: '',
  site: '',
  page: 1,
  q: '',
  deviceId: null,
  tab: 'devices',
  group: '',
})

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

// The panel tab is a closed set like status: an unrecognised value falls back to
// 'devices' rather than being forwarded.
assert.equal(P('?tab=groups').tab, 'groups')
assert.equal(P('?tab=nonsense').tab, 'devices')

// Group ids are bounded at the same trust boundary as device ids, because the
// value goes into a query string.
assert.equal(P('?group=grp-fin').group, 'grp-fin')
assert.equal(P(`?group=${'z'.repeat(200)}`).group.length, 64)

// Round-trip: defaults are omitted, non-defaults are preserved.
assert.equal(buildDevicesQuery({}), '')
assert.equal(
  buildDevicesQuery({ page: 1, status: '', site: '', q: '', deviceId: null, tab: 'devices', group: '' }),
  ''
)
assert.equal(
  buildDevicesQuery({ status: 'offline', page: 4, q: 'lap' }),
  '?status=offline&page=4&q=lap'
)
// The non-default tab and group must survive a round trip, or navigating to the
// groups panel would drop the selection on any state update that rewrites the URL.
assert.equal(
  buildDevicesQuery({ tab: 'groups', group: 'grp-fin' }),
  '?tab=groups&group=grp-fin'
)
assert.deepEqual(P('?tab=groups&group=grp-fin'), {
  status: '',
  site: '',
  page: 1,
  q: '',
  deviceId: null,
  tab: 'groups',
  group: 'grp-fin',
})

console.log('devicesQuery: all assertions passed')
