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
} from 'lucide-react'
import { getStoredToken, api } from '../services/api'
import { Modal } from './ui'
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

/** Mouse moves are coalesced to one send per frame. A raw mousemove stream
 *  crosses the relay at display rate and crowds out the keystrokes queued
 *  behind it; the last position in a frame is the only one that matters. */
const MOUSE_SEND_INTERVAL_MS = 16

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

  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const viewportRef = useRef<HTMLDivElement | null>(null)
  const frameCountRef = useRef<number>(0)
  const lastFpsTimeRef = useRef<number>(0)
  const hasPaintedRef = useRef<boolean>(false)
  const lastFrameAtRef = useRef<number>(0)

  // Kept in a ref, not state: the socket must be able to read the current mode
  // without the effect that owns the socket re-running when the mode changes.
  const modeRef = useRef<RemoteControlMode>(mode)
  const pendingMoveRef = useRef<{ x: number; y: number } | null>(null)
  const moveTimerRef = useRef<number | null>(null)
  const statusRef = useRef<SessionStatus>(status)

  useEffect(() => {
    modeRef.current = mode
  }, [mode])

  useEffect(() => {
    statusRef.current = status
  }, [status])

  useEffect(() => {
    if (!device) return

    const token = getStoredToken()
    if (!token) {
      setStatus('error')
      setErrorMessage('Authentication token missing. Please log in again.')
      return
    }

    let isCancelled = false
    let currentSessionId = ''
    const deviceId = device.id
    const startMode = modeRef.current

    const initSession = async () => {
      try {
        const session = await api.startRemoteControlSession(deviceId, startMode)
        currentSessionId = session.id
        if (isCancelled) return

        setSessionId(session.id)
        setStatus('connecting')

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
        const wsUrl =
          `${protocol}//${window.location.host}` +
          `/api/devices/${deviceId}/remotecontrol/ws` +
          `?token=${encodeURIComponent(token)}&session=${encodeURIComponent(session.id)}`

        const ws = new WebSocket(wsUrl)
        ws.binaryType = 'arraybuffer'
        wsRef.current = ws

        ws.onopen = () => {
          if (isCancelled) return
          // The socket is up, but the desktop is not live until the agent's
          // first frame arrives. `awaitingFirstFrame` drives the difference
          // between "connecting" and "stalled".
          setAwaitingFirstFrame(true)
        }

        ws.onmessage = (event) => {
          if (isCancelled) return

          if (typeof event.data === 'string') {
            handleControlMessage(event.data)
            return
          }

          const buffer = event.data as ArrayBuffer
          if (buffer.byteLength < 4) return

          hasPaintedRef.current = false
          lastFrameAtRef.current = performance.now()
          setAwaitingFirstFrame(false)
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

          const view = new DataView(buffer)
          const width = view.getUint16(0)
          const height = view.getUint16(2)
          setResolution({ width, height })

          const canvas = canvasRef.current
          if (!canvas) return
          if (canvas.width !== width || canvas.height !== height) {
            canvas.width = width
            canvas.height = height
          }
          const ctx = canvas.getContext('2d')
          if (!ctx) return

          // createImageBitmap is async and off the main thread, unlike the old
          // Blob -> objectURL -> Image path, which decoded a ~100KB JPEG on the
          // UI thread for every frame and made input feel sluggish under load.
          createImageBitmap(new Blob([buffer.slice(4)], { type: 'image/jpeg' }))
            .then((bitmap) => {
              if (isCancelled) {
                bitmap.close()
                return
              }
              // Frames can decode out of order when several are in flight. The
              // sequence number in the bitmap's own lifetime is not available,
              // so guard on the canvas still existing and drop the paint if the
              // session ended mid-decode rather than drawing into a dead canvas.
              ctx.drawImage(bitmap, 0, 0)
              hasPaintedRef.current = true
              bitmap.close()
            })
            .catch(() => {
              // A truncated JPEG is not worth failing the session over; the
              // next frame arrives in 100ms.
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
        setErrorMessage(err instanceof Error ? err.message : 'Unknown initialization error')
      }
    }

    const handleControlMessage = (raw: string) => {
      let msg: {
        type?: string
        reason?: string
        mode?: RemoteControlMode
        capabilities?: RemoteControlCapabilities
      }
      try {
        msg = JSON.parse(raw)
      } catch {
        return
      }

      if (msg.type === 'hello') {
        setCapabilities(msg.capabilities ?? null)
        if (msg.mode) setMode(msg.mode)
      } else if (msg.type === 'unsupported') {
        setStatus('error')
        setErrorMessage(
          msg.reason ||
            'This endpoint cannot stream a desktop. Remote control needs a platform capture backend.'
        )
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
      if (!hasPaintedRef.current) {
        setStatus('stalled')
        setErrorMessage(
          'Connected to the relay, but the endpoint sent no screen frames. The agent may still be starting its capture backend, or remote control is unavailable on that platform.'
        )
      }
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
    send({ type: 'mouse', action: 'move', x: pos.x, y: pos.y })
  }, [send])

  const queueMove = useCallback(
    (x: number, y: number) => {
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
    [flushMove]
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
    if (e.key === 'Tab') e.preventDefault()
    send({ type: 'keyboard', action: 'down', key: e.key, code: e.keyCode })
  }

  const handleKeyUp = (e: React.KeyboardEvent) => {
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
            <span>{fps} FPS</span>
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
          {status === 'active' || status === 'stalled' ? (
            <>
              <canvas
                ref={canvasRef}
                onMouseMove={handleMouseMove}
                onMouseDown={handleMouseDown}
                onMouseUp={handleMouseUp}
                onContextMenu={handleContextMenu}
                onWheel={handleWheel}
                className={`remote-canvas ${mode === 'full_control' ? 'remote-canvas-control' : 'remote-canvas-view'}`}
              />
              {status === 'stalled' && (
                <div className="remote-overlay" role="alert">
                  <AlertCircle size={28} />
                  <p className="remote-overlay-text">{errorMessage}</p>
                  <button type="button" className="btn btn-secondary" onClick={onClose}>
                    Close session
                  </button>
                </div>
              )}
            </>
          ) : status === 'error' ? (
            <div className="remote-placeholder">
              <AlertCircle className="remote-placeholder-icon" />
              <h3 className="remote-placeholder-title">Session Failed</h3>
              <p className="remote-placeholder-text">{errorMessage}</p>
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                Close Window
              </button>
            </div>
          ) : status === 'ended' ? (
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
          ) : (
            <div className="remote-placeholder">
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
        </div>
      </div>
    </Modal>
  )
}
