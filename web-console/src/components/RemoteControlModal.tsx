import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  Monitor,
  Eye,
  MousePointer,
  Maximize2,
  Minimize2,
  AlertCircle,
  RefreshCw,
  Keyboard,
  ClipboardPaste,
  ClipboardCopy,
  Upload,
  Command,
} from 'lucide-react'
import { api } from '../services/api'
import { Modal } from './ui'
import { useToast } from '../context/ToastContext'
import type {
  DeviceDTO,
  RemoteControlCapabilities,
  RemoteControlMode,
} from '../types/api'

interface RemoteControlModalProps {
  device: DeviceDTO | null
  initialMode?: RemoteControlMode
  onClose: () => void
}

type SessionStatus =
  | 'initiating'
  | 'connecting'
  | 'active'
  | 'stalled'
  | 'ended'
  | 'error'

/** No frame for this long means the agent is not actually streaming, however
 *  healthy the socket looks. The old code trusted `onopen` and showed "Live
 *  Stream" over an empty canvas whenever the relay accepted the connection but
 *  the agent never attached — the single most misleading state this view can be
 *  in, because everything looks fine and nothing is happening. */
const STALL_AFTER_MS = 8000

/** Layout version of a binary screen update. This must equal `frameVersion` in
 *  agent/shared/remotecontrol/damage.go. The agent and the console are compiled
 *  separately, so nothing but this byte makes a renamed field fail loudly
 *  instead of producing a corrupted canvas. */
const FRAME_VERSION = 1

/** version | rectangle count | uint16 width | uint16 height */
const FRAME_HEADER_BYTES = 6

/** Per rectangle: uint16 x, y, w, h, uint32 jpeg length, then the JPEG. */
const FRAME_RECT_HEADER_BYTES = 12

interface FrameRect {
  x: number
  y: number
  w: number
  h: number
  size: number
  body: Blob
}

/** Mouse moves are coalesced to one send per frame. A raw mousemove stream
 *  crosses the relay at display rate and crowds out the keystrokes queued
 *  behind it; the last position in a frame is the only one that matters. */
const MOUSE_SEND_INTERVAL_MS = 16

/** Raw bytes per upload chunk. Base64 makes it ~256KB on the wire: big enough
 *  that a 200MB file is not a million messages, small enough that one chunk
 *  never holds a screen update back for long. */
const FILE_CHUNK_BYTES = 192 * 1024

/** The socket's send queue is allowed this much before an upload waits. Without
 *  a bound, reading a large file queues all of it at once and every input
 *  event the operator sends sits behind it. */
const UPLOAD_HIGH_WATER_BYTES = 1024 * 1024

/** Combinations the browser or the operator's own OS takes before the page
 *  sees them, so they can only be sent by name. Names match keyCombos in
 *  agent/shared/remotecontrol/features.go. */
const KEY_COMBOS: { combo: string; label: string }[] = [
  { combo: 'ctrl_shift_esc', label: 'Ctrl+Shift+Esc (Task Manager)' },
  { combo: 'alt_tab', label: 'Alt+Tab' },
  { combo: 'win', label: 'Windows key' },
  { combo: 'win_r', label: 'Win+R (Run)' },
  { combo: 'win_e', label: 'Win+E (Explorer)' },
  { combo: 'win_d', label: 'Win+D (Desktop)' },
  { combo: 'print_screen', label: 'Print Screen' },
]

interface FileTransfer {
  id: string
  name: string
  size: number
  sent: number
  received: number
  status: 'sending' | 'saved' | 'failed'
  path?: string
  error?: string
}

/** Base64 of an ArrayBuffer without String.fromCharCode(...bytes), which
 *  overflows the call stack on a 192KB chunk. */
const toBase64 = (buf: ArrayBuffer): string => {
  const bytes = new Uint8Array(buf)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return btoa(binary)
}

const formatBytes = (n: number): string =>
  n >= 1024 * 1024 ? `${(n / (1024 * 1024)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`

const PREVENT_DEFAULT_KEYS = new Set([
  'Tab',
  'Backspace',
  'Space',
  ' ',
  'ArrowUp',
  'ArrowDown',
  'ArrowLeft',
  'ArrowRight',
  'PageUp',
  'PageDown',
  'Home',
  'End',
])

const isSession0OrLocked = (msg: string): boolean => {
  const lower = msg.toLowerCase()
  return (
    lower.includes('session 0') ||
    lower.includes('workstation is locked') ||
    lower.includes('no active logged-on user') ||
    lower.includes('no interactive session') ||
    lower.includes('no active console')
  )
}

export const RemoteControlModal: React.FC<RemoteControlModalProps> = ({
  device,
  initialMode = 'full_control',
  onClose,
}) => {
  // Mode is deliberately NOT in the session effect's dependencies. It used to
  // be, so switching Control/View tore down the WebSocket, POSTed a brand new
  // session and dropped the live desktop — the toggle that exists to hand
  // control over was itself the thing that disconnected you.
  const [mode, setMode] = useState<RemoteControlMode>(initialMode)
  const [status, setStatus] = useState<SessionStatus>('initiating')
  const [errorMessage, setErrorMessage] = useState<string>('')
  const [sessionId, setSessionId] = useState<string | null>(null)
  const [fps, setFps] = useState<number>(0)
  const [bytesReceived, setBytesReceived] = useState<number>(0)
  const [resolution, setResolution] = useState<{ width: number; height: number }>({
    width: 0,
    height: 0,
  })
  const [isFullscreen, setIsFullscreen] = useState<boolean>(false)
  const [capabilities, setCapabilities] = useState<RemoteControlCapabilities | null>(null)
  const [awaitingFirstFrame, setAwaitingFirstFrame] = useState<boolean>(false)
  // The endpoint's view of things the frames do not carry.
  const [monitorIndex, setMonitorIndex] = useState<number | null>(null)
  const [remoteCursor, setRemoteCursor] = useState<{ x: number; y: number; visible: boolean } | null>(null)
  const [pointerInside, setPointerInside] = useState<boolean>(false)
  const [stats, setStats] = useState<{ quality: number; kbps: number } | null>(null)
  const [remoteClipboard, setRemoteClipboard] = useState<string | null>(null)
  const [clipboardDraft, setClipboardDraft] = useState<string | null>(null)
  // Which way the fallback box is for: text the operator is about to send, or
  // endpoint text the browser would not write to the operator's clipboard.
  const [clipboardDirection, setClipboardDirection] = useState<'send' | 'receive'>('send')
  const [keysOpen, setKeysOpen] = useState<boolean>(false)
  const keysMenuRef = useRef<HTMLDivElement | null>(null)
  const [transfers, setTransfers] = useState<FileTransfer[]>([])
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const toast = useToast()

  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const viewportRef = useRef<HTMLDivElement | null>(null)
  const frameCountRef = useRef<number>(0)
  const lastFpsTimeRef = useRef<number>(0)
  const hasPaintedRef = useRef<boolean>(false)
  const lastFrameAtRef = useRef<number>(0)
  // Serialises the async decode-and-paint of each update. The canvas
  // accumulates rectangles, so an older update landing after a newer one would
  // leave the desktop showing a mixture of two states.
  const paintQueueRef = useRef<Promise<void>>(Promise.resolve())

  // Kept in a ref, not state: the socket must be able to read the current mode
  // without the effect that owns the socket re-running when the mode changes.
  const modeRef = useRef<RemoteControlMode>(mode)
  const pendingMoveRef = useRef<{ x: number; y: number } | null>(null)
  const moveTimerRef = useRef<number | null>(null)
  const lastMoveSentRef = useRef<number>(0)
  const statusRef = useRef<SessionStatus>(status)

  useEffect(() => {
    modeRef.current = mode
  }, [mode])

  useEffect(() => {
    statusRef.current = status
  }, [status])

  useEffect(() => {
    if (!device) return

    let isCancelled = false
    let currentSessionId = ''
    const deviceId = device.id
    const startMode = modeRef.current
    // Whether this session has painted decides how a capture failure is shown,
    // so a previous device's frames must not count for this one.
    lastFrameAtRef.current = 0
    setRemoteCursor(null)
    setStats(null)
    setRemoteClipboard(null)
    setTransfers([])

    const initSession = async () => {
      try {
        const session = await api.startRemoteControlSession(deviceId, startMode)
        currentSessionId = session.id
        if (isCancelled) return

        setSessionId(session.id)
        setStatus('connecting')

        // A WebSocket handshake cannot carry an Authorization header, so the
        // socket URL would otherwise have to carry the access token -- into the
        // access log, into history, and into any Referer sent onward. A ticket
        // is fetched over an ordinary authenticated request, is redeemable once,
        // and expires in 60 seconds, so what reaches those logs is already spent.
        //
        // It is minted after the session POST so the ticket's 60s budget is spent
        // on the handshake rather than on a server that may be slow to dispatch
        // rc.start to the agent.
        const ticket = await api.getWebSocketTicket('remote-desktop')
        if (isCancelled) return

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
        const wsUrl =
          `${protocol}//${window.location.host}` +
          `/api/devices/${deviceId}/remotecontrol/ws` +
          `?ticket=${encodeURIComponent(ticket)}&session=${encodeURIComponent(session.id)}`

        const ws = new WebSocket(wsUrl)
        ws.binaryType = 'arraybuffer'
        wsRef.current = ws

        ws.onopen = () => {
          if (isCancelled) return
          // The socket is up, but the desktop is not live until the agent's
          // first frame arrives. `awaitingFirstFrame` drives the difference
          // between "connecting" and "stalled".
          setAwaitingFirstFrame(true)
          if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'refresh' }))
          }
        }

        ws.onmessage = (event) => {
          if (isCancelled) return

          if (typeof event.data === 'string') {
            handleControlMessage(event.data)
            return
          }

          const buffer = event.data as ArrayBuffer
          if (buffer.byteLength < FRAME_HEADER_BYTES) return

          const view = new DataView(buffer)
          const version = view.getUint8(0)
          if (version !== FRAME_VERSION) {
            // The agent and the console are compiled separately, so a renamed
            // field is not a compile error -- it is a corrupted canvas at run
            // time. Refusing the layout is the only safe reading.
            setAwaitingFirstFrame(false)
            setStatus('error')
            setErrorMessage(
              `The endpoint sent screen layout version ${version}, but this console speaks version ${FRAME_VERSION}. The agent and the console are different builds.`
            )
            ws.close()
            return
          }
          const rectCount = view.getUint8(1)
          const width = view.getUint16(2)
          const height = view.getUint16(4)

          // Collect every rectangle before painting any of them. A truncated
          // frame that painted its first half would leave the desktop showing
          // some of this update and none of the rest, which is exactly the
          // torn, half-updated screen the damage stream exists to avoid.
          const rects: FrameRect[] = []
          let pos = FRAME_HEADER_BYTES
          for (let i = 0; i < rectCount; i++) {
            if (pos + FRAME_RECT_HEADER_BYTES > buffer.byteLength) return
            const rect = {
              x: view.getUint16(pos),
              y: view.getUint16(pos + 2),
              w: view.getUint16(pos + 4),
              h: view.getUint16(pos + 6),
              size: view.getUint32(pos + 8),
            }
            pos += FRAME_RECT_HEADER_BYTES
            if (pos + rect.size > buffer.byteLength) return
            rects.push({
              ...rect,
              body: new Blob([buffer.slice(pos, pos + rect.size)], { type: 'image/jpeg' }),
            })
            pos += rect.size
          }

          lastFrameAtRef.current = performance.now()
          setAwaitingFirstFrame(false)
          // A frame is the only proof the desktop is live, so it is what moves
          // the session to 'active'. This was never set anywhere, and the canvas
          // is only rendered for 'active' or 'stalled': the socket came up, the
          // relay delivered megabytes of JPEG, and there was still nothing to
          // paint on, so the operator saw an empty viewport for the full
          // STALL_AFTER_MS window before 'stalled' wrongly claimed the endpoint
          // had gone silent.
          setStatus((prev) => (prev === 'active' || prev === 'ended' ? prev : 'active'))
          setBytesReceived((prev) => prev + buffer.byteLength)
          frameCountRef.current++

          const now = performance.now()
          if (lastFpsTimeRef.current === 0) {
            lastFpsTimeRef.current = now
          } else if (now - lastFpsTimeRef.current >= 1000) {
            setFps(Math.round((frameCountRef.current * 1000) / (now - lastFpsTimeRef.current)))
            frameCountRef.current = 0
            lastFpsTimeRef.current = now
          }

          const canvas = canvasRef.current
          if (!canvas) return
          if (canvas.width !== width || canvas.height !== height) {
            // Assigning the size clears the canvas, which is the correct thing
            // to do here: the endpoint's resolution changed, so nothing already
            // on it lines up with what the agent is about to send. This is the
            // only place the desktop is wiped between updates.
            canvas.width = width
            canvas.height = height
            setResolution({ width, height })
          }
          hasPaintedRef.current = false

          // Paint in arrival order. The rectangles accumulate into one canvas,
          // so two updates racing each other would land the wrong one on top and
          // leave the desktop showing a mixture of two states. Chaining the work
          // is what keeps them in order without dropping any.
          paintQueueRef.current = paintQueueRef.current
            .then(async () => {
              // A resolution change clears the canvas and every rectangle in
              // this update was measured against the old one, so painting it
              // would put desktop content at the wrong coordinates. The next
              // update is 100ms away and carries the whole screen.
              if (canvas.width !== width || canvas.height !== height) return
              const ctx = canvas.getContext('2d')
              if (!ctx) return
              // createImageBitmap is async and off the main thread, unlike the
              // old Blob -> objectURL -> Image path, which decoded a ~100KB
              // JPEG on the UI thread for every frame and made input feel
              // sluggish under load.
              const bitmaps = await Promise.all(
                rects.map(async (r) => {
                  try {
                    return await createImageBitmap(r.body)
                  } catch {
                    return null
                  }
                })
              )
              if (isCancelled) {
                bitmaps.forEach((b) => b?.close())
                return
              }
              for (let i = 0; i < bitmaps.length; i++) {
                const bmp = bitmaps[i]
                if (bmp) {
                  ctx.drawImage(bmp, rects[i].x, rects[i].y)
                  bmp.close()
                }
              }
              hasPaintedRef.current = true
            })
            .catch(() => {
              // A truncated JPEG is not worth failing the session over; the
              // next update arrives in 100ms.
            })
        }

        ws.onerror = () => {
          if (isCancelled) return
          setStatus('error')
          setErrorMessage(
            'Lost the connection to the remote control relay. The endpoint or the network may have dropped.'
          )
        }

        ws.onclose = () => {
          if (isCancelled) return
          if (statusRef.current !== 'error') {
            setStatus('ended')
            setAwaitingFirstFrame(false)
          }
        }
      } catch (err: unknown) {
        if (isCancelled) return
        setStatus('error')
        const rawMsg = err instanceof Error ? err.message : 'Unknown initialization error'
        let displayMsg = rawMsg
        if (rawMsg.includes('409') || rawMsg.toLowerCase().includes('offline')) {
          displayMsg = 'Endpoint is offline or disconnected from central hub. Remote control requires an active agent connection.'
        } else if (rawMsg.toLowerCase().includes('session 0') || rawMsg.toLowerCase().includes('no active console')) {
          displayMsg = 'No interactive user session logged into Windows desktop (Session 0 lock). Remote desktop capture requires an active logged-on user session.'
        }
        setErrorMessage(displayMsg)
      }
    }

    const handleControlMessage = (raw: string) => {
      let msg: {
        type?: string
        reason?: string
        mode?: RemoteControlMode
        capabilities?: RemoteControlCapabilities
        x?: number
        y?: number
        visible?: boolean
        quality?: number
        kbps?: number
        index?: number
        text?: string
        id?: string
        path?: string
        received?: number
      }
      try {
        msg = JSON.parse(raw)
      } catch {
        return
      }

      if (msg.type === 'hello') {
        setCapabilities(msg.capabilities ?? null)
        if (msg.mode) setMode(msg.mode)
        const primary = msg.capabilities?.monitors?.find((m) => m.primary)
        setMonitorIndex(primary ? primary.index : null)
      } else if (msg.type === 'cursor') {
        setRemoteCursor({ x: msg.x ?? 0, y: msg.y ?? 0, visible: Boolean(msg.visible) })
      } else if (msg.type === 'stats') {
        setStats({ quality: msg.quality ?? 0, kbps: msg.kbps ?? 0 })
      } else if (msg.type === 'monitor_changed') {
        setMonitorIndex(msg.index ?? null)
      } else if (msg.type === 'clipboard') {
        const text = msg.text ?? ''
        setRemoteClipboard(text)
        // Copying straight to the operator's clipboard needs the page to have
        // focus and permission; when the browser refuses, the button below
        // copies it on the operator's click instead.
        navigator.clipboard?.writeText(text).catch(() => {})
      } else if (msg.type === 'file_progress' || msg.type === 'file_saved' || msg.type === 'file_failed') {
        setTransfers((prev) =>
          prev.map((t) => {
            if (t.id !== msg.id) return t
            if (msg.type === 'file_progress') return { ...t, received: msg.received ?? t.received }
            if (msg.type === 'file_saved') return { ...t, status: 'saved', received: t.size, path: msg.path }
            return { ...t, status: 'failed', error: msg.reason }
          })
        )
        if (msg.type === 'file_saved') toast.success(`Saved on the endpoint as ${msg.path}`, 'File sent')
        if (msg.type === 'file_failed') toast.error(msg.reason || 'The endpoint refused the file', 'File not sent')
      } else if (msg.type === 'clipboard_failed' || msg.type === 'monitor_failed') {
        toast.error(msg.reason || 'The endpoint refused the request', 'Remote control')
      } else if (msg.type === 'capture_failed' && lastFrameAtRef.current > 0) {
        // The desktop was already streaming, so this is the agent saying a few
        // grabs in a row failed -- a UAC prompt, the lock screen, a display
        // mode change. The agent keeps retrying, and the next frame moves the
        // session back to 'active'. Tearing the canvas down here made one
        // transient failure look like a dead endpoint and threw away the
        // desktop the operator was looking at.
        setStatus((prev) => (prev === 'ended' || prev === 'error' ? prev : 'stalled'))
        setErrorMessage(
          `The endpoint stopped sending its screen (${msg.reason || 'capture failed'}). Showing the last frame; it resumes on its own when capture recovers.`
        )
      } else if (msg.type === 'unsupported' || msg.type === 'connect_failed' || msg.type === 'capture_failed') {
        setAwaitingFirstFrame(false)
        setCapabilities({ capture: false, mouse: false, keyboard: false, reason: msg.reason ?? '' })
        setStatus('error')
        let reason = msg.reason || 'This endpoint cannot stream a desktop. Remote control needs a platform capture backend.'
        if (isSession0OrLocked(reason)) {
          reason = `${reason} (Note: Windows services run in Session 0; ensure an interactive user is logged into the physical desktop and the screen is unlocked).`
        }
        setErrorMessage(reason)
      }
    }

    initSession()

    return () => {
      isCancelled = true
      if (moveTimerRef.current !== null) {
        window.clearInterval(moveTimerRef.current)
        moveTimerRef.current = null
      }
      pendingMoveRef.current = null
      if (wsRef.current) {
        try {
          wsRef.current.close()
        } catch {
          // ignore
        }
        wsRef.current = null
      }
      if (currentSessionId) {
        api
          .stopRemoteControlSession(deviceId, currentSessionId)
          .catch(() => {})
      }
    }
    // `mode` is intentionally absent: it is a session setting, not a reason to
    // rebuild the session. Changing it sends a control message instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [device])

  // A socket that opens and then never delivers a frame is the failure the old
  // view hid completely. Watch for it rather than showing "Live Stream" over a
  // blank canvas.
  useEffect(() => {
    if (!awaitingFirstFrame) return
    const timer = window.setTimeout(() => {
      // The agent may have already said why, in which case that sentence is the
      // diagnosis and this one is noise. Overwriting it rehid the real reason
      // behind eight seconds of "the endpoint sent no frames".
      if (hasPaintedRef.current || statusRef.current === 'error') return
      setStatus('stalled')
      setErrorMessage(
        'Connected to the relay, but the endpoint sent no screen frames. If the endpoint recently reconnected from offline, ensure an interactive user desktop is logged on (Windows lock screen restricts remote screen capture).'
      )
    }, STALL_AFTER_MS)
    return () => window.clearTimeout(timer)
  }, [awaitingFirstFrame])

  const send = useCallback((msg: Record<string, unknown>) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (modeRef.current === 'view_only') return
    ws.send(JSON.stringify(msg))
  }, [])

  const sendMode = useCallback((next: RemoteControlMode) => {
    const ws = wsRef.current
    setMode(next)
    modeRef.current = next
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    // Bypasses the view_only gate on the agent by design: the agent honours a
    // `mode` control message regardless of the mode it is currently in.
    ws.send(JSON.stringify({ type: 'mode', action: 'set', mode: next }))
    if (next === 'view_only') {
      // Nothing should stay pressed on the endpoint when control is handed back.
      ws.send(JSON.stringify({ type: 'keyboard', action: 'release' }))
    }
  }, [])

  // The Keys menu closes on a click anywhere else or on Escape, like any menu.
  // Escape is caught before it bubbles to the viewport, which would forward it
  // to the endpoint.
  useEffect(() => {
    if (!keysOpen) return
    const onDown = (e: MouseEvent) => {
      if (!keysMenuRef.current?.contains(e.target as Node)) setKeysOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setKeysOpen(false)
      }
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [keysOpen])

  // Choosing which monitor to watch is not input, so it bypasses the view_only
  // gate in `send`, the same way the relay and the agent let it through.
  const selectMonitor = useCallback((index: number) => {
    const ws = wsRef.current
    setMonitorIndex(index)
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ type: 'monitor', index }))
  }, [])

  const sendKeys = useCallback(
    (combo: string) => {
      setKeysOpen(false)
      send({ type: 'keys', combo })
      viewportRef.current?.focus()
    },
    [send]
  )

  const sendClipboardText = useCallback(
    (text: string) => {
      send({ type: 'clipboard', text })
      setClipboardDraft(null)
      toast.info('Clipboard text sent to the endpoint. Paste it there with Ctrl+V.', 'Clipboard')
    },
    [send, toast]
  )

  // Reading the operator's clipboard needs permission; when the browser
  // refuses (or the page is not on HTTPS), a text box takes the paste instead.
  const pasteToRemote = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText()
      if (text) {
        sendClipboardText(text)
        return
      }
    } catch {
      // fall through to the manual box
    }
    setClipboardDirection('send')
    setClipboardDraft('')
  }, [sendClipboardText])

  const copyFromRemote = useCallback(async () => {
    if (remoteClipboard === null) return
    try {
      await navigator.clipboard.writeText(remoteClipboard)
      toast.success('The endpoint clipboard is now on your clipboard.', 'Clipboard')
    } catch {
      setClipboardDirection('receive')
      setClipboardDraft(remoteClipboard)
    }
  }, [remoteClipboard, toast])

  // Upload in chunks over the session socket, waiting whenever the socket's
  // send queue is past the high-water mark, so a large file never queues up
  // ahead of the operator's own mouse and keyboard input.
  const uploadFile = useCallback(
    async (file: File) => {
      const ws = wsRef.current
      if (!ws || ws.readyState !== WebSocket.OPEN || modeRef.current === 'view_only') return
      const limit = capabilities?.max_file_bytes ?? 0
      if (limit > 0 && file.size > limit) {
        toast.error(`${file.name} is ${formatBytes(file.size)}; the endpoint accepts up to ${formatBytes(limit)}.`, 'File too large')
        return
      }
      const id = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
      setTransfers((prev) => [
        ...prev.filter((t) => t.status === 'sending').slice(-3),
        { id, name: file.name, size: file.size, sent: 0, received: 0, status: 'sending' },
      ])
      const fail = (error: string) =>
        setTransfers((prev) => prev.map((t) => (t.id === id ? { ...t, status: 'failed', error } : t)))

      ws.send(JSON.stringify({ type: 'file_begin', id, name: file.name, size: file.size }))
      let seq = 0
      for (let offset = 0; offset < file.size; offset += FILE_CHUNK_BYTES) {
        while (ws.readyState === WebSocket.OPEN && ws.bufferedAmount > UPLOAD_HIGH_WATER_BYTES) {
          await new Promise((r) => window.setTimeout(r, 25))
        }
        if (ws.readyState !== WebSocket.OPEN) {
          fail('The session closed during the upload.')
          return
        }
        const data = toBase64(await file.slice(offset, offset + FILE_CHUNK_BYTES).arrayBuffer())
        ws.send(JSON.stringify({ type: 'file_chunk', id, seq, data }))
        seq++
        const sent = Math.min(file.size, offset + FILE_CHUNK_BYTES)
        setTransfers((prev) => prev.map((t) => (t.id === id ? { ...t, sent } : t)))
      }
      ws.send(JSON.stringify({ type: 'file_end', id }))
    },
    [capabilities, toast]
  )

  const getCanvasCoords = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const canvas = canvasRef.current
    if (!canvas) return { x: 0, y: 0 }
    const rect = canvas.getBoundingClientRect()
    if (rect.width === 0 || rect.height === 0) return { x: 0, y: 0 }
    // Map operator pixels to endpoint pixels through the canvas's own scale, so
    // a session displayed letterboxed or full-screened still lands the cursor
    // on the point that was clicked.
    const scaleX = canvas.width / rect.width
    const scaleY = canvas.height / rect.height
    const x = Math.max(0, Math.min(canvas.width - 1, Math.round((e.clientX - rect.left) * scaleX)))
    const y = Math.max(0, Math.min(canvas.height - 1, Math.round((e.clientY - rect.top) * scaleY)))
    return { x, y }
  }

  const flushMove = useCallback(() => {
    const pos = pendingMoveRef.current
    if (!pos) return
    pendingMoveRef.current = null
    lastMoveSentRef.current = performance.now()
    send({ type: 'mouse', action: 'move', x: pos.x, y: pos.y })
  }, [send])

  const queueMove = useCallback(
    (x: number, y: number) => {
      const now = performance.now()
      // If idle for more than interval, dispatch immediately for instant cursor response
      if (now - lastMoveSentRef.current >= MOUSE_SEND_INTERVAL_MS && pendingMoveRef.current === null) {
        lastMoveSentRef.current = now
        send({ type: 'mouse', action: 'move', x, y })
        return
      }

      pendingMoveRef.current = { x, y }
      if (moveTimerRef.current === null) {
        moveTimerRef.current = window.setInterval(() => {
          flushMove()
          if (pendingMoveRef.current === null && moveTimerRef.current !== null) {
            window.clearInterval(moveTimerRef.current)
            moveTimerRef.current = null
          }
        }, MOUSE_SEND_INTERVAL_MS)
      }
    },
    [flushMove, send]
  )

  const handleMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const { x, y } = getCanvasCoords(e)
    queueMove(x, y)
  }

  const handleMouseDown = (e: React.MouseEvent<HTMLCanvasElement>) => {
    e.preventDefault()
    // Focus must follow the click, or the viewport stops receiving keystrokes
    // the moment the operator clicks into the desktop — the most common way a
    // remote session appears to "lose" the keyboard.
    viewportRef.current?.focus()
    const { x, y } = getCanvasCoords(e)
    flushMove()
    const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left'
    send({ type: 'mouse', action: 'down', x, y, button })
  }

  const handleMouseUp = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const { x, y } = getCanvasCoords(e)
    const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left'
    send({ type: 'mouse', action: 'up', x, y, button })
  }

  const handleContextMenu = (e: React.MouseEvent) => {
    // The right button drives the endpoint's context menu, not the console's.
    e.preventDefault()
  }

  const handleWheel = (e: React.WheelEvent<HTMLCanvasElement>) => {
    e.preventDefault()
    const { x, y } = getCanvasCoords(e)
    // Normalise to a fixed notch. Browsers report wildly different deltaY
    // values for the same physical scroll, and passing that through raw makes
    // one wheel click scroll the endpoint by 3 lines and the next by 40.
    const delta = e.deltaY < 0 ? 120 : -120
    send({ type: 'mouse', action: 'wheel', x, y, delta })
  }

  // keyCode, not `key`. The agent translates a Windows virtual-key code, and
  // `key` is a layout-dependent string ("a", "A", "Dead", "Unidentified") that
  // cannot be mapped back to a key code at all.
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (PREVENT_DEFAULT_KEYS.has(e.key)) e.preventDefault()
    send({ type: 'keyboard', action: 'down', key: e.key, code: e.keyCode })
  }

  const handleKeyUp = (e: React.KeyboardEvent) => {
    if (PREVENT_DEFAULT_KEYS.has(e.key)) e.preventDefault()
    send({ type: 'keyboard', action: 'up', key: e.key, code: e.keyCode })
  }

  // Losing focus while a key is held is how keys get stuck down. Tell the agent
  // to lift everything rather than waiting for a keyup that will never come.
  const handleBlur = () => {
    flushMove()
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'keyboard', action: 'release' }))
    }
  }

  const toggleFullscreen = () => {
    const el = viewportRef.current
    if (!el) return
    if (!document.fullscreenElement) {
      el.requestFullscreen?.().catch(() => {})
      setIsFullscreen(true)
    } else {
      document.exitFullscreen?.().catch(() => {})
      setIsFullscreen(false)
    }
  }

  useEffect(() => {
    const onChange = () => setIsFullscreen(Boolean(document.fullscreenElement))
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  if (!device) return null

  const formatMB = (bytes: number) => (bytes / (1024 * 1024)).toFixed(1) + ' MB'
  const statusClass =
    status === 'active'
      ? 'status-success'
      : status === 'connecting' || status === 'initiating'
        ? 'status-running'
        : status === 'ended'
          ? 'status-pill'
          : 'status-failed'

  const statusLabel =
    status === 'active'
      ? 'Live Stream'
      : status === 'stalled'
        ? 'No Signal'
        : status === 'connecting'
          ? 'Connecting'
          : status === 'initiating'
            ? 'Starting'
            : status === 'ended'
              ? 'Ended'
              : 'Failed'

  // A platform that cannot capture will never paint. Say so in the toolbar
  // rather than leaving the operator waiting on a black rectangle.
  const captureUnavailable = capabilities !== null && !capabilities.capture
  const live = status === 'active' || status === 'stalled'
  const viewOnly = mode === 'view_only'
  const monitors = capabilities?.monitors ?? []
  // In Control mode the operator's own pointer is over the desktop and marks
  // the spot; the endpoint's pointer is drawn when the operator cannot see it
  // otherwise -- in View mode, or with their pointer outside the canvas.
  const showRemoteCursor =
    live && remoteCursor?.visible && resolution.width > 0 && (viewOnly || !pointerInside)

  return (
    <Modal
      open
      onClose={onClose}
      size="full"
      // The remote desktop forwards every key to the endpoint, Escape included,
      // so Escape cannot also mean "close". The close button stays, because
      // clicking it is deliberate and does not reach the endpoint.
      dismissible={false}
      title={
        <span className="modal-title-group">
          <Monitor className="modal-icon" /> Remote Desktop
        </span>
      }
      description={
        <span className="remote-session-meta">
          <span className="site-badge">{device.site || 'Default'}</span>
          <span className={`status-pill ${statusClass}`}>{statusLabel}</span>
          {sessionId && <span className="remote-session-id">#{sessionId.substring(0, 8)}</span>}
          {mode === 'view_only' && <span className="site-badge">View only</span>}
        </span>
      }
    >
      <div>
        <div className="remote-toolbar">
          <div className="remote-telemetry">
            {resolution.width > 0 && (
              <span>
                {resolution.width}x{resolution.height}
              </span>
            )}
            <span title="Screen updates per second. An update carries only the rectangles that changed, so a still desktop sends none — that is the point, not a stall.">
              {fps} upd/s
            </span>
            {stats && (
              <span title="Image quality the endpoint is encoding at, adapted to how fast this link drains, and the rate it is sending.">
                Q{stats.quality} · {stats.kbps} kbps
              </span>
            )}
            <span>{formatMB(bytesReceived)}</span>
            {capabilities && (
              <span
                className="remote-telemetry-caps"
                title={capabilities.reason || 'Agent-reported capabilities'}
              >
                {capabilities.capture ? 'capture' : 'no capture'} ·{' '}
                {capabilities.mouse ? 'mouse' : 'no mouse'} ·{' '}
                {capabilities.keyboard ? 'keyboard' : 'no keyboard'}
              </span>
            )}
          </div>

          <div className="remote-actions">
            {monitors.length > 1 && (
              <select
                className="select-input remote-monitor-select"
                aria-label="Endpoint monitor"
                value={monitorIndex ?? ''}
                disabled={!live}
                onChange={(e) => selectMonitor(Number(e.target.value))}
              >
                {monitors.map((m) => (
                  <option key={m.index} value={m.index}>
                    Monitor {m.index + 1}
                    {m.primary ? ' (primary)' : ''} · {m.width}×{m.height}
                  </option>
                ))}
                <option value={-1}>All monitors</option>
              </select>
            )}

            <div className="remote-menu" ref={keysMenuRef}>
              <button
                type="button"
                className="btn btn-sm btn-secondary"
                onClick={() => setKeysOpen((o) => !o)}
                disabled={!live || viewOnly}
                aria-expanded={keysOpen}
                aria-haspopup="menu"
                title="Send a key combination your browser cannot pass through"
              >
                <Command size={14} />
                <span>Keys</span>
              </button>
              {keysOpen && (
                <div className="remote-menu-list" role="menu">
                  {KEY_COMBOS.map((k) => (
                    <button
                      key={k.combo}
                      type="button"
                      role="menuitem"
                      className="remote-menu-item"
                      onClick={() => sendKeys(k.combo)}
                    >
                      {k.label}
                    </button>
                  ))}
                </div>
              )}
            </div>

            {capabilities?.clipboard && (
              <>
                <button
                  type="button"
                  className="btn btn-sm btn-secondary"
                  onClick={pasteToRemote}
                  disabled={!live || viewOnly}
                  title="Send your clipboard text to the endpoint's clipboard"
                >
                  <ClipboardPaste size={14} />
                  <span>Send clipboard</span>
                </button>
                {remoteClipboard !== null && (
                  <button
                    type="button"
                    className="btn btn-sm btn-secondary"
                    onClick={copyFromRemote}
                    title="Copy the text last copied on the endpoint"
                  >
                    <ClipboardCopy size={14} />
                    <span>Copy remote</span>
                  </button>
                )}
              </>
            )}

            {capabilities?.file_transfer && (
              <>
                <button
                  type="button"
                  className="btn btn-sm btn-secondary"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={!live || viewOnly}
                  title="Send a file to the endpoint's Downloads\Remote Transfers folder"
                >
                  <Upload size={14} />
                  <span>Send file</span>
                </button>
                <input
                  ref={fileInputRef}
                  type="file"
                  hidden
                  onChange={(e) => {
                    const file = e.target.files?.[0]
                    e.target.value = ''
                    if (file) void uploadFile(file)
                  }}
                />
              </>
            )}

            <div className="remote-mode-switch" role="group" aria-label="Remote input mode">
              <button
                type="button"
                onClick={() => sendMode('full_control')}
                disabled={captureUnavailable}
                aria-pressed={mode === 'full_control'}
                className={`btn btn-sm ${mode === 'full_control' ? 'btn-primary' : 'btn-secondary'}`}
                title={
                  captureUnavailable
                    ? capabilities?.reason
                    : 'Take control of the endpoint: mouse and keyboard are forwarded'
                }
              >
                <MousePointer size={14} />
                <span>Control</span>
              </button>
              <button
                type="button"
                onClick={() => sendMode('view_only')}
                aria-pressed={mode === 'view_only'}
                className={`btn btn-sm ${mode === 'view_only' ? 'btn-primary' : 'btn-secondary'}`}
                title="Watch without sending input to the endpoint"
              >
                <Eye size={14} />
                <span>View</span>
              </button>
            </div>

            <button
              type="button"
              onClick={toggleFullscreen}
              className="btn btn-icon"
              title={isFullscreen ? 'Exit Fullscreen' : 'Fullscreen'}
              aria-label={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
            >
              {isFullscreen ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
            </button>
          </div>
        </div>

        {clipboardDraft !== null && (
          <div className="remote-clipboard-panel">
            <label className="form-label" htmlFor="remote-clipboard-text">
              {clipboardDirection === 'send'
                ? 'Your browser did not allow reading your clipboard. Paste the text here to send it.'
                : 'Your browser did not allow writing to your clipboard. Copy the endpoint text from here (Ctrl+C).'}
            </label>
            <textarea
              id="remote-clipboard-text"
              className="text-input remote-clipboard-text"
              rows={3}
              value={clipboardDraft}
              readOnly={clipboardDirection === 'receive'}
              onChange={(e) => setClipboardDraft(e.target.value)}
              onFocus={(e) => {
                if (clipboardDirection === 'receive') e.currentTarget.select()
              }}
              autoFocus
            />
            <div className="remote-clipboard-actions">
              <button type="button" className="btn btn-sm btn-secondary" onClick={() => setClipboardDraft(null)}>
                Close
              </button>
              {clipboardDirection === 'send' && (
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={!clipboardDraft || viewOnly}
                  onClick={() => sendClipboardText(clipboardDraft)}
                >
                  Send to endpoint
                </button>
              )}
            </div>
          </div>
        )}

        {transfers.length > 0 && (
          <ul className="remote-transfers" aria-label="File transfers">
            {transfers.map((t) => {
              const done = t.status === 'saved' ? t.size : Math.max(t.sent, t.received)
              const pct = t.size > 0 ? Math.round((done / t.size) * 100) : 100
              return (
                <li key={t.id} className="remote-transfer">
                  <span className="remote-transfer-name" title={t.path || t.name}>
                    {t.name}
                  </span>
                  <progress className="remote-transfer-bar" max={100} value={pct} />
                  <span className="remote-transfer-state">
                    {t.status === 'saved'
                      ? 'Saved'
                      : t.status === 'failed'
                        ? t.error || 'Failed'
                        : `${pct}% of ${formatBytes(t.size)}`}
                  </span>
                </li>
              )
            })}
          </ul>
        )}

        {/* The viewport keeps its own dark ground because it shows an arbitrary
            remote desktop; the dialog chrome around it uses console tokens. It
            is focusable and auto-focused, because it is the keyboard target. */}
        <div
          ref={viewportRef}
          tabIndex={0}
          autoFocus
          onKeyDown={handleKeyDown}
          onKeyUp={handleKeyUp}
          onBlur={handleBlur}
          className="remote-viewport"
          role="application"
          aria-label={`Remote desktop for ${device.hostname}. Keyboard input is forwarded to the endpoint. Click the desktop to focus it, then type.`}
        >
          {status !== 'error' && status !== 'ended' && (
            <canvas
              ref={canvasRef}
              onMouseMove={handleMouseMove}
              onMouseDown={handleMouseDown}
              onMouseUp={handleMouseUp}
              onContextMenu={handleContextMenu}
              onWheel={handleWheel}
              onMouseEnter={() => setPointerInside(true)}
              onMouseLeave={() => setPointerInside(false)}
              className={`remote-canvas ${mode === 'full_control' ? 'remote-canvas-control' : 'remote-canvas-view'}`}
            />
          )}

          {showRemoteCursor && remoteCursor && (
            <span
              className="remote-cursor"
              aria-hidden="true"
              style={{
                left: `${(remoteCursor.x / resolution.width) * 100}%`,
                top: `${(remoteCursor.y / resolution.height) * 100}%`,
              }}
            />
          )}

          {(status === 'initiating' || status === 'connecting') && (
            <div className="remote-overlay">
              <RefreshCw className="remote-placeholder-icon spinning" />
              <p className="remote-placeholder-text">
                {status === 'initiating'
                  ? 'Requesting desktop tunnel...'
                  : 'Waiting for the endpoint to attach and send its first frame...'}
              </p>
              <p className="remote-hint">
                <Keyboard size={12} /> Click the desktop to focus it, then type. Keys are forwarded to{' '}
                {device.hostname}.
              </p>
            </div>
          )}

          {status === 'stalled' && (
            <div className="remote-overlay" role="alert">
              <AlertCircle size={28} />
              <p className="remote-overlay-text">{errorMessage}</p>
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                Close session
              </button>
            </div>
          )}

          {status === 'error' && (
            <div className="remote-placeholder">
              <Monitor className="remote-placeholder-icon" />
              <h3 className="remote-placeholder-title">Cannot Stream This Desktop</h3>
              <p className="remote-placeholder-text">{errorMessage}</p>
              {isSession0OrLocked(errorMessage) ? (
                <p className="remote-placeholder-text">
                  Remote control needs a person logged in at the endpoint so the agent has a
                  desktop to attach to. Ask them to sign in, then start a new session.
                </p>
              ) : (
                <p className="remote-placeholder-text">
                  Remote desktop capture failed. Verify endpoint agent logs and screen capture prerequisites, then retry.
                </p>
              )}
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                Close Window
              </button>
            </div>
          )}

          {status === 'ended' && (
            <div className="remote-placeholder">
              <Monitor className="remote-placeholder-icon" />
              <h3 className="remote-placeholder-title">Remote Session Ended</h3>
              <p className="remote-placeholder-text">
                The desktop streaming session was closed by the operator or endpoint.
              </p>
              <button type="button" className="btn btn-primary" onClick={onClose}>
                Close Window
              </button>
            </div>
          )}
        </div>
      </div>
    </Modal>
  )
}
