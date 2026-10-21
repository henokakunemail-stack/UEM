import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceDTO } from '../types/api'

const api = vi.hoisted(() => ({
  startRemoteControlSession: vi.fn(),
  getWebSocketTicket: vi.fn(),
  stopRemoteControlSession: vi.fn(),
}))
vi.mock('../services/api', () => ({ api }))

import { RemoteControlModal } from './RemoteControlModal'
import { ToastProvider } from '../context/ToastContext'
import { virtualKeyCode, wheelNotches } from './remoteInput'

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

async function openSession() {
  const view = render(
    <ToastProvider>
      <RemoteControlModal device={device} onClose={() => {}} />
    </ToastProvider>,
  )
  await waitFor(() => expect(FakeSocket.last).not.toBeNull())
  const ws = FakeSocket.last!
  act(() => {
    ws.open()
    ws.deliver(JSON.stringify({ type: 'hello', mode: 'full_control', capabilities: { capture: true, mouse: true, keyboard: true } }))
    ws.deliver(emptyFrame())
  })
  const viewport = view.container.ownerDocument.querySelector('.remote-viewport') as HTMLElement
  return { ws, viewport }
}

const sentOfType = (ws: FakeSocket, type: string) =>
  ws.sent.map((m) => JSON.parse(m)).filter((m: { type: string }) => m.type === type)

describe('RemoteControlModal input', () => {
  beforeAll(() => {
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

  // autoFocus on a <div> does nothing in React, so the dialog's close button
  // held focus: typing went nowhere and Enter ended the session.
  it('gives the remote desktop the keyboard as soon as the session connects', async () => {
    const { viewport } = await openSession()
    await waitFor(() => expect(document.activeElement).toBe(viewport))
  })

  it('sends right Ctrl with its location and maps Firefox key codes', async () => {
    const { ws, viewport } = await openSession()
    fireEvent.keyDown(viewport, { key: 'Control', keyCode: 17, location: 2 })
    fireEvent.keyDown(viewport, { key: '-', keyCode: 173 })
    const keys = sentOfType(ws, 'keyboard')
    expect(keys[0]).toMatchObject({ action: 'down', code: 17, location: 2 })
    expect(keys[1]).toMatchObject({ action: 'down', code: 189 })
  })

  // A button held when focus left used to stay down on the endpoint.
  it('releases a held mouse button when the viewport loses focus', async () => {
    const { ws, viewport } = await openSession()
    const canvas = viewport.querySelector('canvas') as HTMLCanvasElement
    fireEvent.pointerDown(canvas, { button: 0, clientX: 5, clientY: 5, pointerId: 1 })
    fireEvent.blur(viewport)
    const mouse = sentOfType(ws, 'mouse')
    expect(mouse.some((m: { action: string; button: string }) => m.action === 'down' && m.button === 'left')).toBe(true)
    expect(mouse.some((m: { action: string; button: string }) => m.action === 'up' && m.button === 'left')).toBe(true)
  })
})

describe('remote input helpers', () => {
  it('maps Firefox key codes and drops IME composition', () => {
    expect(virtualKeyCode(173)).toBe(189)
    expect(virtualKeyCode(59)).toBe(186)
    expect(virtualKeyCode(65)).toBe(65)
    expect(virtualKeyCode(229)).toBe(0)
  })

  // One notch per trackpad micro-event made a gentle scroll fly; small deltas
  // now accumulate into whole notches.
  it('accumulates small wheel deltas into notches', () => {
    let carry = 0
    let notches = 0
    for (let i = 0; i < 10; i++) {
      const r = wheelNotches(30, 0, carry)
      carry = r.carry
      notches += r.notches
    }
    expect(notches).toBe(3)
    expect(wheelNotches(1, 1, 0).notches).toBe(0) // 40px: not yet a notch
    expect(wheelNotches(3, 1, 0).notches).toBe(1) // 120px
    expect(wheelNotches(-250, 0, 0).notches).toBe(-2)
  })
})
