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
import { useT } from '../i18n/core'
import type {
  DeviceDTO,
  RemoteControlCapabilities,
  RemoteControlMode,
} from '../types/api'
import { virtualKeyCode, wheelNotches } from './remoteInput'

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
 *  agent/shared/remotecontrol/features.go. `keys` is the key names, shown as
 *  they are; `descKey` is the dictionary key of the translated description,
 *  looked up at render. */
const KEY_COMBOS: { combo: string; keys: string | null; descKey: string | null }[] = [
  { combo: 'ctrl_shift_esc', keys: 'Ctrl+Shift+Esc', descKey: 'remote_control.combo_task_manager' },
  { combo: 'alt_tab', keys: 'Alt+Tab', descKey: null },
  { combo: 'win', keys: null, descKey: 'remote_control.combo_windows_key' },
  { combo: 'win_r', keys: 'Win+R', descKey: 'remote_control.combo_run' },
  { combo: 'win_e', keys: 'Win+E', descKey: 'remote_control.combo_explorer' },
  { combo: 'win_d', keys: 'Win+D', descKey: 'remote_control.combo_desktop' },
  { combo: 'print_screen', keys: 'Print Screen', descKey: null },
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

// Keys the browser keeps for itself while the remote desktop has focus. Escape
// still closes the dialog, and F11/F12 still work for the operator. Every
// other key is the endpoint's: F5 or Ctrl+R used to reload the console and end
// the session, and F6/F10/Alt moved focus away so typing stopped.
const BROWSER_KEYS = new Set(['Escape', 'F11', 'F12'])

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
  const { t } = useT()
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
  // Buttons the operator is holding on the endpoint, released if focus leaves.
  const pressedRef = useRef<Set<string>>(new Set())
  const wheelCarryRef = useRef<{ x: number; y: number }>({ x: 0, y: 0 })

  // The remote desktop is the keyboard target. autoFocus on a <div> does
  // nothing in React, so the dialog focused its close button instead: keys went
  // nowhere and Enter or Space ended the session.
  const focusViewport = useCallback(() => {
    viewportRef.current?.focus({ preventScroll: true })
  }, [])

  useEffect(() => {
    modeRef.current = mode
  }, [mode])

  useEffect(() => {
    statusRef.current = status
    if (status === 'connecting' || status === 'active') focusViewport()
  }, [status, focusViewport])

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
              t('remote_control.frame_version_mismatch', { version, expected: FRAME_VERSION })
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
            t('remote_control.relay_lost')
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
        const rawMsg = err instanceof Error ? err.message : t('remote_control.init_error_unknown')
        let displayMsg = rawMsg
        if (rawMsg.includes('409') || rawMsg.toLowerCase().includes('offline')) {
          displayMsg = t('remote_control.endpoint_offline')
        } else if (rawMsg.toLowerCase().includes('session 0') || rawMsg.toLowerCase().includes('no active console')) {
          displayMsg = t('remote_control.no_interactive_session')
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
        if (msg.type === 'file_saved') toast.success(t('remote_control.file_saved', { path: msg.path ?? '' }), t('remote_control.toast_file_sent'))
        if (msg.type === 'file_failed') toast.error(msg.reason || t('remote_control.file_refused'), t('remote_control.toast_file_not_sent'))
      } else if (msg.type === 'input_failed') {
        // The endpoint refused the input; the agent sends this at most once
        // every few seconds.
        toast.error(
          t('remote_control.input_failed', { reason: msg.reason || '' }),
          t('remote_control.toast_remote_control')
        )
      } else if (msg.type === 'clipboard_failed' || msg.type === 'monitor_failed') {
        toast.error(msg.reason || t('remote_control.request_refused'), t('remote_control.toast_remote_control'))
      } else if (msg.type === 'capture_failed' && lastFrameAtRef.current > 0) {
        // The desktop was already streaming, so this is the agent saying a few
        // grabs in a row failed -- a UAC prompt, the lock screen, a display
        // mode change. The agent keeps retrying, and the next frame moves the
        // session back to 'active'. Tearing the canvas down here made one
        // transient failure look like a dead endpoint and threw away the
        // desktop the operator was looking at.
        setStatus((prev) => (prev === 'ended' || prev === 'error' ? prev : 'stalled'))
        setErrorMessage(
          t('remote_control.capture_stopped', { reason: msg.reason || t('remote_control.capture_failed_reason') })
        )
      } else if (msg.type === 'unsupported' || msg.type === 'connect_failed' || msg.type === 'capture_failed') {
        setAwaitingFirstFrame(false)
        setCapabilities({ capture: false, mouse: false, keyboard: false, reason: msg.reason ?? '' })
        setStatus('error')
        let reason = msg.reason || t('remote_control.cannot_stream_default')
        if (isSession0OrLocked(reason)) {
          reason = t('remote_control.session0_note', { reason })
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
        t('remote_control.no_frames')
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
    focusViewport()
  }, [focusViewport])

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
    focusViewport()
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ type: 'monitor', index }))
  }, [focusViewport])

  const sendKeys = useCallback(
    (combo: string) => {
      setKeysOpen(false)
      send({ type: 'keys', combo })
      focusViewport()
    },
    [send, focusViewport]
  )

  const sendClipboardText = useCallback(
    (text: string) => {
      send({ type: 'clipboard', text })
      setClipboardDraft(null)
      toast.info(t('remote_control.clipboard_sent'), t('remote_control.toast_clipboard'))
    },
    [send, toast, t]
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
      toast.success(t('remote_control.clipboard_copied'), t('remote_control.toast_clipboard'))
    } catch {
      setClipboardDirection('receive')
      setClipboardDraft(remoteClipboard)
    }
  }, [remoteClipboard, toast, t])

  // Upload in chunks over the session socket, waiting whenever the socket's
  // send queue is past the high-water mark, so a large file never queues up
  // ahead of the operator's own mouse and keyboard input.
  const uploadFile = useCallback(
    async (file: File) => {
      const ws = wsRef.current
      if (!ws || ws.readyState !== WebSocket.OPEN || modeRef.current === 'view_only') return
      const limit = capabilities?.max_file_bytes ?? 0
      if (limit > 0 && file.size > limit) {
        toast.error(
          t('remote_control.file_too_large', { name: file.name, size: formatBytes(file.size), limit: formatBytes(limit) }),
          t('remote_control.toast_file_too_large')
        )
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
          fail(t('remote_control.upload_session_closed'))
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
    [capabilities, toast, t]
  )

  const getCanvasCoords = (e: { clientX: number; clientY: number }) => {
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

  const buttonName = (b: number) => (b === 2 ? 'right' : b === 1 ? 'middle' : 'left')

  const handlePointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const { x, y } = getCanvasCoords(e)
    queueMove(x, y)
  }

  const handlePointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    e.preventDefault()
    // Focus must follow the click, or the viewport stops receiving keystrokes
    // the moment the operator clicks into the desktop.
    focusViewport()
    // Capture the pointer so the release reaches us even outside the canvas.
    // Without it a button let go outside stayed down on the endpoint.
    e.currentTarget.setPointerCapture?.(e.pointerId)
    const { x, y } = getCanvasCoords(e)
    flushMove()
    const button = buttonName(e.button)
    pressedRef.current.add(button)
    send({ type: 'mouse', action: 'down', x, y, button })
  }

  const handlePointerUp = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const { x, y } = getCanvasCoords(e)
    const button = buttonName(e.button)
    pressedRef.current.delete(button)
    send({ type: 'mouse', action: 'up', x, y, button })
  }

  const handleContextMenu = (e: React.MouseEvent) => {
    // The right button drives the endpoint's context menu, not the console's.
    e.preventDefault()
  }

  // The wheel listener is attached natively and non-passive: React's onWheel is
  // passive, so preventDefault did nothing and the console page scrolled along
  // with the endpoint. Deltas are accumulated into whole notches, vertical and
  // horizontal; horizontal scrolling used to be sent as scrolling down.
  const canvasLive = status !== 'error' && status !== 'ended'
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || !canvasLive) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const { x, y } = getCanvasCoords(e)
      const carry = wheelCarryRef.current
      const v = wheelNotches(-e.deltaY, e.deltaMode, carry.y)
      const h = wheelNotches(e.deltaX, e.deltaMode, carry.x)
      wheelCarryRef.current = { x: h.carry, y: v.carry }
      if (v.notches !== 0) send({ type: 'mouse', action: 'wheel', x, y, delta: v.notches * 120 })
      if (h.notches !== 0) send({ type: 'mouse', action: 'hwheel', x, y, delta: h.notches * 120 })
    }
    canvas.addEventListener('wheel', onWheel, { passive: false })
    return () => canvas.removeEventListener('wheel', onWheel)
    // getCanvasCoords reads refs only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canvasLive, send])

  // keyCode, not `key`. The agent translates a Windows virtual-key code, and
  // `key` is a layout-dependent string ("a", "A", "Dead", "Unidentified") that
  // cannot be mapped back to a key code at all. location tells left from right
  // modifiers and numpad Enter from Enter, which keyCode cannot.
  const sendKey = (e: React.KeyboardEvent, action: 'down' | 'up') => {
    if (!BROWSER_KEYS.has(e.key)) e.preventDefault()
    const code = virtualKeyCode(e.keyCode)
    if (code === 0) return
    send({ type: 'keyboard', action, key: e.key, code, location: e.location })
  }
  const handleKeyDown = (e: React.KeyboardEvent) => sendKey(e, 'down')
  const handleKeyUp = (e: React.KeyboardEvent) => sendKey(e, 'up')

  // Losing focus while a key or button is held is how they get stuck down.
  // Tell the agent to lift everything rather than waiting for a release that
  // will never come.
  const handleBlur = () => {
    flushMove()
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      for (const button of pressedRef.current) {
        ws.send(JSON.stringify({ type: 'mouse', action: 'up', x: -1, y: -1, button }))
      }
      ws.send(JSON.stringify({ type: 'keyboard', action: 'release' }))
    }
    pressedRef.current.clear()
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
      ? t('remote_control.status_live')
      : status === 'stalled'
        ? t('remote_control.status_no_signal')
        : status === 'connecting'
          ? t('remote_control.status_connecting')
          : status === 'initiating'
            ? t('remote_control.status_starting')
            : status === 'ended'
              ? t('remote_control.status_ended')
              : t('remote_control.status_failed')

  const comboLabel = (k: (typeof KEY_COMBOS)[number]) =>
    !k.descKey
      ? k.keys
      : k.keys
        ? t('remote_control.combo_label', { keys: k.keys, desc: t(k.descKey) })
        : t(k.descKey)

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
          <Monitor className="modal-icon" /> {t('remote_control.modal_title')}
        </span>
      }
      description={
        <span className="remote-session-meta">
          <span className="site-badge">{device.site || t('remote_control.default_site')}</span>
          <span className={`status-pill ${statusClass}`}>{statusLabel}</span>
          {sessionId && <span className="remote-session-id">#{sessionId.substring(0, 8)}</span>}
          {mode === 'view_only' && <span className="site-badge">{t('remote_control.mode_view_only')}</span>}
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
            <span title={t('remote_control.fps_title')}>
              {t('remote_control.fps', { fps })}
            </span>
            {stats && (
              <span title={t('remote_control.stats_title')}>
                Q{stats.quality} · {stats.kbps} kbps
              </span>
            )}
            <span>{formatMB(bytesReceived)}</span>
            {capabilities && (
              <span
                className="remote-telemetry-caps"
                title={capabilities.reason || t('remote_control.caps_title')}
              >
                {capabilities.capture ? t('remote_control.cap_capture') : t('remote_control.cap_no_capture')} ·{' '}
                {capabilities.mouse ? t('remote_control.cap_mouse') : t('remote_control.cap_no_mouse')} ·{' '}
                {capabilities.keyboard ? t('remote_control.cap_keyboard') : t('remote_control.cap_no_keyboard')}
              </span>
            )}
          </div>

          <div className="remote-actions">
            {monitors.length > 1 && (
              <select
                className="select-input remote-monitor-select"
                aria-label={t('remote_control.monitor_select_label')}
                value={monitorIndex ?? ''}
                disabled={!live}
                onChange={(e) => selectMonitor(Number(e.target.value))}
              >
                {monitors.map((m) => (
                  <option key={m.index} value={m.index}>
                    {m.primary
                      ? t('remote_control.monitor_option_primary', { n: m.index + 1 })
                      : t('remote_control.monitor_option', { n: m.index + 1 })}{' '}
                    · {m.width}×{m.height}
                  </option>
                ))}
                <option value={-1}>{t('remote_control.all_monitors')}</option>
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
                title={t('remote_control.keys_title')}
              >
                <Command size={14} />
                <span>{t('remote_control.keys')}</span>
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
                      {comboLabel(k)}
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
                  title={t('remote_control.send_clipboard_title')}
                >
                  <ClipboardPaste size={14} />
                  <span>{t('remote_control.send_clipboard')}</span>
                </button>
                {remoteClipboard !== null && (
                  <button
                    type="button"
                    className="btn btn-sm btn-secondary"
                    onClick={copyFromRemote}
                    title={t('remote_control.copy_remote_title')}
                  >
                    <ClipboardCopy size={14} />
                    <span>{t('remote_control.copy_remote')}</span>
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
                  title={t('remote_control.send_file_title')}
                >
                  <Upload size={14} />
                  <span>{t('remote_control.send_file')}</span>
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

            <div className="remote-mode-switch" role="group" aria-label={t('remote_control.input_mode_label')}>
              <button
                type="button"
                onClick={() => sendMode('full_control')}
                disabled={captureUnavailable}
                aria-pressed={mode === 'full_control'}
                className={`btn btn-sm ${mode === 'full_control' ? 'btn-primary' : 'btn-secondary'}`}
                title={
                  captureUnavailable
                    ? capabilities?.reason
                    : t('remote_control.control_title')
                }
              >
                <MousePointer size={14} />
                <span>{t('remote_control.mode_control')}</span>
              </button>
              <button
                type="button"
                onClick={() => sendMode('view_only')}
                aria-pressed={mode === 'view_only'}
                className={`btn btn-sm ${mode === 'view_only' ? 'btn-primary' : 'btn-secondary'}`}
                title={t('remote_control.view_title')}
              >
                <Eye size={14} />
                <span>{t('remote_control.mode_view')}</span>
              </button>
            </div>

            <button
              type="button"
              onClick={toggleFullscreen}
              className="btn btn-icon"
              title={isFullscreen ? t('remote_control.exit_fullscreen') : t('remote_control.fullscreen')}
              aria-label={isFullscreen ? t('remote_control.exit_fullscreen') : t('remote_control.enter_fullscreen')}
            >
              {isFullscreen ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
            </button>
          </div>
        </div>

        {clipboardDraft !== null && (
          <div className="remote-clipboard-panel">
            <label className="form-label" htmlFor="remote-clipboard-text">
              {clipboardDirection === 'send'
                ? t('remote_control.clipboard_read_blocked')
                : t('remote_control.clipboard_write_blocked')}
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
                {t('common.close')}
              </button>
              {clipboardDirection === 'send' && (
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={!clipboardDraft || viewOnly}
                  onClick={() => sendClipboardText(clipboardDraft)}
                >
                  {t('remote_control.send_to_endpoint')}
                </button>
              )}
            </div>
          </div>
        )}

        {transfers.length > 0 && (
          <ul className="remote-transfers" aria-label={t('remote_control.file_transfers_label')}>
            {transfers.map((tr) => {
              const done = tr.status === 'saved' ? tr.size : Math.max(tr.sent, tr.received)
              const pct = tr.size > 0 ? Math.round((done / tr.size) * 100) : 100
              return (
                <li key={tr.id} className="remote-transfer">
                  <span className="remote-transfer-name" title={tr.path || tr.name}>
                    {tr.name}
                  </span>
                  <progress className="remote-transfer-bar" max={100} value={pct} />
                  <span className="remote-transfer-state">
                    {tr.status === 'saved'
                      ? t('remote_control.transfer_saved')
                      : tr.status === 'failed'
                        ? tr.error || t('remote_control.transfer_failed')
                        : t('remote_control.transfer_progress', { pct, size: formatBytes(tr.size) })}
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
          onKeyDown={handleKeyDown}
          onKeyUp={handleKeyUp}
          onBlur={handleBlur}
          className="remote-viewport"
          role="application"
          aria-label={t('remote_control.viewport_label', { hostname: device.hostname })}
        >
          {status !== 'error' && status !== 'ended' && (
            <canvas
              ref={canvasRef}
              onPointerMove={handlePointerMove}
              onPointerDown={handlePointerDown}
              onPointerUp={handlePointerUp}
              onContextMenu={handleContextMenu}
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
                  ? t('remote_control.requesting_tunnel')
                  : t('remote_control.waiting_first_frame')}
              </p>
              <p className="remote-hint">
                <Keyboard size={12} /> {t('remote_control.focus_hint', { hostname: device.hostname })}
              </p>
            </div>
          )}

          {/* A banner, not a cover: the desktop stays visible and clickable.
              The full overlay sat over the canvas and swallowed every click
              until a new frame arrived, which on an unchanged desktop could be
              never. */}
          {status === 'stalled' && (
            <div className="remote-overlay remote-overlay-banner" role="alert">
              <AlertCircle size={28} />
              <p className="remote-overlay-text">{errorMessage}</p>
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                {t('remote_control.close_session')}
              </button>
            </div>
          )}

          {status === 'error' && (
            <div className="remote-placeholder">
              <Monitor className="remote-placeholder-icon" />
              <h3 className="remote-placeholder-title">{t('remote_control.cannot_stream_title')}</h3>
              <p className="remote-placeholder-text">{errorMessage}</p>
              {isSession0OrLocked(errorMessage) ? (
                <p className="remote-placeholder-text">
                  {t('remote_control.needs_logged_in_user')}
                </p>
              ) : (
                <p className="remote-placeholder-text">
                  {t('remote_control.capture_failed_hint')}
                </p>
              )}
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                {t('remote_control.close_window')}
              </button>
            </div>
          )}

          {status === 'ended' && (
            <div className="remote-placeholder">
              <Monitor className="remote-placeholder-icon" />
              <h3 className="remote-placeholder-title">{t('remote_control.session_ended_title')}</h3>
              <p className="remote-placeholder-text">
                {t('remote_control.session_ended_text')}
              </p>
              <button type="button" className="btn btn-primary" onClick={onClose}>
                {t('remote_control.close_window')}
              </button>
            </div>
          )}
        </div>
      </div>
    </Modal>
  )
}
