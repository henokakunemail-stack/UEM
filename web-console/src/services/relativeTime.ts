import type { Lang } from '../i18n/core'

// "3 days ago" in the reader's language. The heartbeat column used to show a
// bare time of day, so a device last seen a week ago at 10:00 looked like it
// checked in this morning.
export function relativeTime(iso: string, lang: Lang, now: Date = new Date()): string {
  const then = new Date(iso)
  const diffSec = Math.round((then.getTime() - now.getTime()) / 1000)
  const abs = Math.abs(diffSec)
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' })
  if (abs < 60) return rtf.format(diffSec, 'second')
  if (abs < 3600) return rtf.format(Math.round(diffSec / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diffSec / 3600), 'hour')
  return rtf.format(Math.round(diffSec / 86400), 'day')
}
