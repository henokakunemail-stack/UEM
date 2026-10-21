import { describe, expect, it } from 'vitest'
import { waitForRestore, type RestorePollDeps, type RestoreStatus } from './restorePoller'

/** A fake clock and server: each poll advances time by the sleep. */
function fake(nodes: (string | Error)[], statuses: (RestoreStatus | Error)[] = []) {
  let t = 0
  let n = 0
  let s = 0
  const calls = { status: 0 }
  const deps: RestorePollDeps = {
    fetchNode: async () => {
      const v = nodes[Math.min(n++, nodes.length - 1)]
      if (v instanceof Error) throw v
      return v
    },
    fetchStatus: async () => {
      calls.status++
      const v = statuses[Math.min(s++, statuses.length - 1)]
      if (v instanceof Error) throw v
      return v
    },
    sleep: async (ms) => {
      t += ms
    },
    now: () => t,
  }
  return { deps, calls }
}

const down = new Error('connection refused')

describe('waitForRestore', () => {
  // The old process keeps answering until it exits; its answer is not the
  // outcome. The old waiter took the first 200 as success.
  it('ignores the process that is about to exit', async () => {
    // The old process writes "succeeded" just before it exits, so asking it
    // would end the wait while the server is going down. The status must only
    // be read from the new process.
    const nodes: (string | Error)[] = ['old', 'old', down, 'new']
    let i = 0
    let current: string | null = null
    const askedFrom: (string | null)[] = []
    let t = 0
    const deps: RestorePollDeps = {
      fetchNode: async () => {
        const v = nodes[Math.min(i++, nodes.length - 1)]
        if (v instanceof Error) {
          current = null
          throw v
        }
        current = v
        return v
      },
      fetchStatus: async () => {
        askedFrom.push(current)
        return { state: 'succeeded' }
      },
      sleep: async (ms) => {
        t += ms
      },
      now: () => t,
    }
    expect(await waitForRestore('old', 'r1', deps)).toBe('succeeded')
    expect(askedFrom).toEqual(['new'])
  })

  it('reports a failed restore that was rolled back', async () => {
    const { deps } = fake(['new'], [{ state: 'failed', error_code: 'restore_failed' }])
    expect(await waitForRestore('old', 'r1', deps)).toBe('failed')
  })

  it('reports a rollback that failed too', async () => {
    const { deps } = fake(['new'], [{ state: 'failed', error_code: 'rollback_failed' }])
    expect(await waitForRestore('old', 'r1', deps)).toBe('rollback_failed')
  })

  it('keeps waiting while the new process has no answer yet', async () => {
    const { deps } = fake(['new'], [down, { state: 'pending' }, { state: 'succeeded' }])
    expect(await waitForRestore('old', 'r1', deps)).toBe('succeeded')
  })

  it('gives up after the deadline instead of spinning forever', async () => {
    const { deps } = fake(['old'])
    expect(await waitForRestore('old', 'r1', deps, { timeoutMs: 10_000, intervalMs: 2_000 })).toBe('timeout')
  })

  it('aborts a request that hangs', async () => {
    let t = 0
    const deps: RestorePollDeps = {
      fetchNode: (signal) =>
        new Promise((_, reject) => signal.addEventListener('abort', () => reject(new Error('aborted')))),
      fetchStatus: async () => ({ state: 'succeeded' }),
      sleep: async (ms) => {
        t += ms
      },
      now: () => t,
    }
    expect(await waitForRestore('old', 'r1', deps, { timeoutMs: 4_000, intervalMs: 2_000, requestTimeoutMs: 10 })).toBe('timeout')
  })
})
