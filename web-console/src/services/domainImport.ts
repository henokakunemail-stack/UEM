// Turns a pasted list or an imported file into one candidate domain per entry.
// The server is the judge of what is a valid domain (it normalizes and reports
// every line back); this only splits the text the way people actually keep
// these lists, so the per-line report lines up with what they pasted.
//
// Accepted shapes, one per line:
//   example.com
//   example.com   # a comment
//   0.0.0.0 example.com          (hosts-file blocklists)
//   example.com,Social Media     (CSV: the first column is the domain)
// Blank lines and lines starting with '#' are skipped, and a CSV header row
// ("domain", "pattern", "host" ...) is dropped.

const HEADER_CELLS = new Set(['domain', 'domains', 'pattern', 'host', 'hostname', 'url', 'site'])
const SINK_ADDRESSES = new Set(['0.0.0.0', '127.0.0.1', '::', '::1'])

export const MAX_BULK_PATTERNS = 5000

export function parseDomainLines(text: string, filename = ''): string[] {
  const isCSV = /\.csv$/i.test(filename)
  const out: string[] = []
  const lines = text.replace(/^﻿/, '').split(/\r?\n/)
  lines.forEach((raw, index) => {
    let line = raw.trim()
    if (line === '' || line.startsWith('#')) return
    const hash = line.indexOf(' #')
    if (hash >= 0) line = line.slice(0, hash).trim()

    if (isCSV || (line.includes(',') && !line.includes(' '))) {
      line = (line.split(/[,;\t]/)[0] ?? '').trim().replace(/^"(.*)"$/, '$1').trim()
      if (index === 0 && HEADER_CELLS.has(line.toLowerCase())) return
    } else {
      const parts = line.split(/\s+/)
      if (parts.length >= 2 && SINK_ADDRESSES.has(parts[0])) line = parts[1]
    }
    if (line !== '') out.push(line)
  })
  return out
}
