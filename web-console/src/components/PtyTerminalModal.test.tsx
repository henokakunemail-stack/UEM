import { act, render, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO } from '../types/api'

const api = vi.hoisted(() => ({ getWebSocketTicket: vi.fn() }))
vi.mock('../services/api', () => ({ api }))

/** xterm.js needs a canvas and font metrics jsdom does not have. The fake
 *  keeps what the component wires to it: the data and resize callbacks, and
 *  everything written to the screen. */
const xterm = vi.hoisted(() => {
  class FakeTerminal {
    static last: FakeTerminal | null = null
    cols = 120
    rows = 40
    written: string[] = []
    dataCb: ((d: string) => void) | null = null
    resizeCb: ((s: { cols: number; rows: number }) => void) | null = null
    constructor() {
      FakeTerminal.last = this
    }
    loadAddon() {}
    open() {}
    focus() {}
    dispose() {}
    attachCustomKeyEventHandler() {}
    getSelection() {
      return ''
    }
    paste() {}
    write(s: string) {
      this.written.push(s)
    }
    onData(cb: (d: string) => void) {
      this.dataCb = cb
      return { dispose() {} }
    }
    onResize(cb: (s: { cols: number; rows: number }) => void) {
      this.resizeCb = cb
      return { dispose() {} }
    }
    screen() {
      return this.written.join('')
    }
  }
  return { FakeTerminal }
})
vi.mock('@xterm/xterm', () => ({ Terminal: xterm.FakeTerminal }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

import { PtyTerminalModal } from './PtyTerminalModal'
import { isWindowsDevice } from './terminalPlatform'

class FakeSocket {
  static OPEN = 1
  static last: FakeSocket | null = null
  url: string
  readyState = 0
  sent: Record<string, unknown>[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  constructor(url: string) {
    this.url = url
    FakeSocket.last = this
  }
  send(data: string) {
    this.sent.push(JSON.parse(data))
  }
  close() {}
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  deliver(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) })
  }
}

const linux = { id: 'dev-1', hostname: 'srv-1', os_name: 'linux' } as DeviceDTO

async function open() {
  render(<PtyTerminalModal device={linux} shell="bash" onClose={() => {}} />)
  await waitFor(() => expect(FakeSocket.last).not.toBeNull())
  const ws = FakeSocket.last!
  const term = xterm.FakeTerminal.last!
  act(() => ws.open())
  return { ws, term }
}

describe('PtyTerminalModal', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
    vi.stubGlobal('WebSocket', FakeSocket)
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        disconnect() {}
      },
    )
  })

  beforeEach(() => {
    FakeSocket.last = null
    xterm.FakeTerminal.last = null
    api.getWebSocketTicket.mockResolvedValue('ticket')
  })

  it('opens the shell at the terminal size', async () => {
    const { ws } = await open()
    expect(ws.url).toContain('cols=120')
    expect(ws.url).toContain('rows=40')
  })

  // A pty echoes and edits by itself; anything done locally would print
  // every keystroke twice and swallow Ctrl+C.
  it('passes every keystroke straight through once the agent reports a pty', async () => {
    const { ws, term } = await open()
    act(() => ws.deliver({ type: 'term.mode', data: 'pty' }))
    act(() => {
      term.dataCb!('l')
      term.dataCb!('\x03')
      term.dataCb!('\t')
    })
    expect(ws.sent).toEqual([
      { type: 'term.data', data: 'l' },
      { type: 'term.data', data: '\x03' },
      { type: 'term.data', data: '\t' },
    ])
    expect(term.screen()).not.toContain('l')
  })

  // An older agent has no pty: its shell neither echoes nor edits, so the
  // console does both and sends whole lines.
  it('edits lines locally for an agent without a pty', async () => {
    const { ws, term } = await open()
    act(() => {
      for (const ch of 'lsx') term.dataCb!(ch)
      term.dataCb!('\x7f')
      term.dataCb!('\r')
    })
    expect(ws.sent).toEqual([{ type: 'term.data', data: 'ls\n' }])
    expect(term.screen()).toContain('ls')
  })

  it('tells the agent when the terminal is resized', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const { ws, term } = await open()
      act(() => {
        term.resizeCb!({ cols: 90, rows: 30 })
        term.resizeCb!({ cols: 100, rows: 32 })
        vi.advanceTimersByTime(200)
      })
      expect(ws.sent.filter((m) => m.type === 'term.resize')).toEqual([{ type: 'term.resize', cols: 100, rows: 32 }])
    } finally {
      vi.useRealTimers()
    }
  })

  it('writes shell output to the screen', async () => {
    const { ws, term } = await open()
    act(() => ws.deliver({ type: 'term.data', data: '\x1b[32mroot@srv-1\x1b[0m:~# ' }))
    expect(term.screen()).toContain('root@srv-1')
  })
})

describe('isWindowsDevice', () => {
  it('keeps Windows on the old terminal and sends Linux and macOS to the pty one', () => {
    expect(isWindowsDevice({ os_name: 'windows' } as DeviceDTO)).toBe(true)
    expect(isWindowsDevice({ os_name: 'Microsoft Windows 11 Pro' } as DeviceDTO)).toBe(true)
    expect(isWindowsDevice({ os_name: 'linux' } as DeviceDTO)).toBe(false)
    expect(isWindowsDevice({ os_name: 'darwin' } as DeviceDTO)).toBe(false)
  })
})
