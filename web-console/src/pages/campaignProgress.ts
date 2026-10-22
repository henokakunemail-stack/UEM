import type { CampaignTaskDetail, UpdateCampaignDTO } from '../types/api'

// A pending task is either waiting for its wave or, once released, for its
// device to come online. The server keeps both as 'pending'.
export function taskDisplayStatus(task: Pick<CampaignTaskDetail, 'status' | 'released_at'>): string {
  if (task.status !== 'pending') return task.status
  return task.released_at ? 'queued' : 'waiting_wave'
}

// Progress of a campaign from the counts the list endpoint returns.
export function campaignProgress(c: UpdateCampaignDTO) {
  const total = c.total_devices || 0
  const succeeded = c.succeeded_devices || 0
  const failed = c.failed_devices || 0
  const running = c.in_progress_devices || 0
  const queued = c.queued_devices || 0
  const waiting = Math.max(0, (c.pending_devices || 0) - queued)
  // Devices already on the target version are part of total and finished
  // from the start; without them a campaign that skipped some never read 100%.
  const upToDate = c.up_to_date_devices || 0
  const done = succeeded + failed + upToDate
  const percent = total > 0 ? Math.round((done / total) * 100) : c.status === 'completed' ? 100 : 0
  return { total, succeeded, failed, running, queued, waiting, upToDate, percent }
}
