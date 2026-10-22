import { describe, expect, it } from 'vitest'
import { parseDomainLines } from './domainImport'

describe('parseDomainLines', () => {
  it('takes one domain per line and skips blanks and comments', () => {
    const text = 'tiktok.com\n\n# social\n  reddit.com  \r\nexample.org # note\n'
    expect(parseDomainLines(text)).toEqual(['tiktok.com', 'reddit.com', 'example.org'])
  })

  it('reads the domain out of a hosts-file blocklist', () => {
    const text = '0.0.0.0 ads.example.com\n127.0.0.1 tracker.example.net\n'
    expect(parseDomainLines(text, 'blocklist.txt')).toEqual(['ads.example.com', 'tracker.example.net'])
  })

  it('uses the first CSV column and drops a header row', () => {
    const text = '﻿domain,category\n"tiktok.com",Social\nreddit.com;Forum\n'
    expect(parseDomainLines(text, 'list.CSV')).toEqual(['tiktok.com', 'reddit.com'])
  })

  // The server reports invalid lines back; dropping them here would hide them.
  it('keeps lines it cannot judge so the server can report them', () => {
    expect(parseDomainLines('not a domain\n10.0.0.1\n')).toEqual(['not a domain', '10.0.0.1'])
  })
})
