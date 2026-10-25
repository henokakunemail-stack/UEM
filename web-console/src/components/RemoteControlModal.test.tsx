import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO, RemoteControlCapabilities } from '../types/api'

const api = vi.hoisted(() => ({
  startRemoteControlSession: vi.fn(),
  getWebSocketTicket: vi.fn(),
  stopRemoteControlSession: vi.fn(),
}))
vi.mock('../services/api', () => ({ api }))

import { RemoteControlModal } from './RemoteControlModal'
import { ToastProvider } from '../context/ToastContext'

/** A WebSocket the test drives: it opens on demand and delivers whatever the
 *  test sends as if it came from the agent. */
class FakeSocket {
  static OPEN = 1
  static last: FakeSocket | null = null
  readyState = 0
  bufferedAmount = 0
  binaryType = 'blob'
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  constructor() {
    FakeSocket.last = this
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {}
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  deliver(data: unknown) {
    this.onmessage?.({ data })
  }
}

/** One screen update with no rectangles: enough to make the session live. */
function emptyFrame(width = 1920, height = 1080): ArrayBuffer {
  const buf = new ArrayBuffer(6)
  const v = new DataView(buf)
  v.setUint8(0, 1)
  v.setUint8(1, 0)
  v.setUint16(2, width)
  v.setUint16(4, height)
  return buf
}

const device = { id: 'dev-1', hostname: 'PC-1', site: 'HQ' } as DeviceDTO

async function openSession(capabilities: RemoteControlCapabilities) {
  render(
    <ToastProvider>
      <RemoteControlModal device={device} onClose={() => {}} />
    </ToastProvider>,
  )
  await waitFor(() => expect(FakeSocket.last).not.toBeNull())
  const ws = FakeSocket.last!
  act(() => {
    ws.open()
    ws.deliver(JSON.stringify({ type: 'hello', mode: 'full_control', capabilities }))
    ws.deliver(emptyFrame())
  })
  return ws
}

describe('RemoteControlModal feature gating', () => {
  beforeAll(() => {
    // jsdom has no <dialog> top layer.
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open')
    }
    vi.stubGlobal('WebSocket', FakeSocket)
  })

  beforeEach(() => {
    FakeSocket.last = null
    api.startRemoteControlSession.mockResolvedValue({ id: 'session-1' })
    api.getWebSocketTicket.mockResolvedValue('ticket')
    api.stopRemoteControlSession.mockResolvedValue(undefined)
  })

  // An older agent advertises none of the features. The console must not offer
  // controls that would send into a void.
  it('hides clipboard, file and monitor controls the agent did not advertise', async () => {
    await openSession({ capture: true, mouse: true, keyboard: true })
    await waitFor(() => expect(screen.getByText('Keys')).toBeTruthy())
    expect(screen.queryByText('Send clipboard')).toBeNull()
    expect(screen.queryByText('Send file')).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  // The agent sends a smaller picture while the screen is busy. Without saying
  // so, a softer image reads as a fault; at full size nothing is shown.
  it('says when the endpoint is sending a scaled-down picture', async () => {
    const ws = await openSession({ capture: true, mouse: true, keyboard: true })
    act(() => ws.deliver(JSON.stringify({ type: 'stats', quality: 70, kbps: 900, scale: 100 })))
    await waitFor(() => expect(screen.getByText(/Q70/)).toBeTruthy())
    expect(screen.queryByText(/Size \d+%/)).toBeNull()
    act(() => ws.deliver(JSON.stringify({ type: 'stats', quality: 60, kbps: 1200, scale: 75 })))
    await waitFor(() => expect(screen.getByText('Size 75%')).toBeTruthy())
  })

  // The agent sends ahead only as far as the console has confirmed painting.
  // The console announces it acknowledges as soon as the agent says hello, and
  // then acknowledges every frame -- in View mode too, or a view-only stream
  // would stop after two frames.
  it('acknowledges every frame, in View mode too', async () => {
    const ws = await openSession({ capture: true, mouse: true, keyboard: true })
    const acks = () => ws.sent.map((m) => JSON.parse(m)).filter((m) => m.type === 'ack')
    expect(acks()[0]).toEqual({ type: 'ack', frames: 0 })
    await waitFor(() => expect(acks().some((m) => m.frames === 1)).toBe(true))

    fireEvent.click(screen.getByRole('button', { name: /View/ }))
    act(() => {
      ws.deliver(emptyFrame())
      ws.deliver(emptyFrame())
    })
    await waitFor(() => expect(acks().some((m) => m.frames === 3)).toBe(true))
  })

  // Low-bandwidth mode is for an operator on a slow link: one switch, sent to
  // the agent straight away (View mode included) and remembered next time.
  it('switches low-bandwidth mode and remembers it', async () => {
    localStorage.removeItem('em_rc_low_bandwidth')
    const ws = await openSession({ capture: true, mouse: true, keyboard: true })
    const settings = () => ws.sent.map((m) => JSON.parse(m)).filter((m) => m.type === 'low_bandwidth')
    expect(settings()[0]).toEqual({ type: 'low_bandwidth', enabled: false })

    fireEvent.click(screen.getByRole('button', { name: /View/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Low bandwidth' }))
    expect(settings().at(-1)).toEqual({ type: 'low_bandwidth', enabled: true })
    expect(localStorage.getItem('em_rc_low_bandwidth')).toBe('1')

    act(() => ws.deliver(JSON.stringify({ type: 'stats', quality: 50, kbps: 400, scale: 50, rtt_ms: 180 })))
    await waitFor(() => expect(screen.getByText(/180 ms/)).toBeTruthy())
    localStorage.removeItem('em_rc_low_bandwidth')
  })

  // View mode changes nothing on the endpoint: the controls that would are
  // disabled, while choosing which monitor to watch stays available.
  it('disables endpoint-changing controls in View mode but keeps monitor selection', async () => {
    const ws = await openSession({
      capture: true, mouse: true, keyboard: true, clipboard: true, file_transfer: true, max_file_bytes: 1000,
      monitors: [
        { index: 0, x: 0, y: 0, width: 1920, height: 1080, primary: true },
        { index: 1, x: 1920, y: 0, width: 1280, height: 1024, primary: false },
      ],
    })
    const button = (label: string) => screen.getByText(label).closest('button') as HTMLButtonElement
    await waitFor(() => expect(button('Send clipboard').disabled).toBe(false))
    expect(button('Send file').disabled).toBe(false)
    expect(button('Keys').disabled).toBe(false)

    fireEvent.click(button('View'))
    expect(button('Send clipboard').disabled).toBe(true)
    expect(button('Send file').disabled).toBe(true)
    expect(button('Keys').disabled).toBe(true)

    const monitor = screen.getByRole('combobox') as HTMLSelectElement
    expect(monitor.disabled).toBe(false)
    fireEvent.change(monitor, { target: { value: '1' } })
    expect(ws.sent.some((m) => m.includes('"type":"monitor"') && m.includes('"index":1'))).toBe(true)
  })
})
