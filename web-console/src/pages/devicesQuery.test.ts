import { describe, expect, it } from 'vitest'
import { UNKNOWN_AGENT_VERSION, buildDevicesQuery, parseDevicesQuery } from './devicesQuery'

describe('devices query: agent filter', () => {
  it('reads and writes the agent version', () => {
    const q = parseDevicesQuery(new URLSearchParams('status=online&site=hq&agent=1.5.6'))
    expect(q.agent).toBe('1.5.6')
    expect(buildDevicesQuery({ ...q, page: 1 })).toContain('agent=1.5.6')
  })

  it('keeps the no-version choice through the URL', () => {
    const q = parseDevicesQuery(new URLSearchParams(`agent=${UNKNOWN_AGENT_VERSION}`))
    expect(q.agent).toBe(UNKNOWN_AGENT_VERSION)
  })

  it('leaves a clean URL when no agent is chosen, and bounds a hand-edited one', () => {
    expect(buildDevicesQuery({ ...parseDevicesQuery(new URLSearchParams('')), page: 1 })).not.toContain('agent')
    expect(parseDevicesQuery(new URLSearchParams('agent=' + 'x'.repeat(500))).agent.length).toBe(64)
  })
})
