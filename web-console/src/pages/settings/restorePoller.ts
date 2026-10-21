/**
 * Waiting for a database restore to finish.
 *
 * The server answers the restore request first and does the work after, then
 * exits so its service manager starts it again on the new database. The old
 * waiter treated the first 200 from /healthz as success -- but the old process
 * answers /healthz until the moment it exits, and a restore that failed used to
 * leave it running, so the console reported success either way.
 *
 * This waits for a different process (the node id /healthz reports changes on
 * every start), then asks that process how the restore ended, by the id the
 * restore request returned.
 */

export type RestoreOutcome = 'succeeded' | 'failed' | 'rollback_failed' | 'timeout'

export interface RestoreStatus {
  state: string
  error_code?: string
}

export interface RestorePollDeps {
  /** The node id /healthz reports, or a rejection while the server is down. */
  fetchNode: (signal: AbortSignal) => Promise<string>
  /** The outcome of this restore by id, from the restarted server. */
  fetchStatus: (restoreId: string, signal: AbortSignal) => Promise<RestoreStatus>
  sleep: (ms: number) => Promise<void>
  now: () => number
}

export interface RestorePollOptions {
  timeoutMs?: number
  intervalMs?: number
  requestTimeoutMs?: number
}

async function withTimeout<T>(ms: number, fn: (signal: AbortSignal) => Promise<T>): Promise<T | null> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), ms)
  try {
    return await fn(controller.signal)
  } catch {
    return null
  } finally {
    clearTimeout(timer)
  }
}

export async function waitForRestore(
  beforeNode: string | null,
  restoreId: string,
  deps: RestorePollDeps,
  { timeoutMs = 180_000, intervalMs = 2_000, requestTimeoutMs = 5_000 }: RestorePollOptions = {},
): Promise<RestoreOutcome> {
  const deadline = deps.now() + timeoutMs
  while (deps.now() < deadline) {
    const node = await withTimeout(requestTimeoutMs, deps.fetchNode)
    // The same node is the process that is about to exit, not the new one.
    if (node && node !== beforeNode) {
      const status = await withTimeout(requestTimeoutMs, (signal) => deps.fetchStatus(restoreId, signal))
      if (status?.state === 'succeeded') return 'succeeded'
      if (status?.state === 'failed') return status.error_code === 'rollback_failed' ? 'rollback_failed' : 'failed'
    }
    await deps.sleep(intervalMs)
  }
  return 'timeout'
}
