import React, { useEffect, useRef, useState } from 'react'
import { AlertCircle, Terminal } from 'lucide-react'
import { api } from '../services/api'
import { Modal } from './ui'
import type { DeviceDTO } from '../types/api'

interface InteractiveTerminalModalProps {
  device: DeviceDTO | null
  shell: string
  onClose: () => void
}

interface TerminalMessage {
  type: string
  session_id: string
  data: string
}

export const InteractiveTerminalModal: React.FC<InteractiveTerminalModalProps> = ({
  device,
  shell,
  onClose,
}) => {
  const [sessionID, setSessionID] = useState<string | null>(null)
  const [status, setStatus] = useState<'connecting' | 'active' | 'closed' | 'error'>('connecting')
  const [errorMessage, setErrorMessage] = useState<string>('')
  const wsRef = useRef<WebSocket | null>(null)
  const outputRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const bufferRef = useRef<string>('')

  useEffect(() => {
    if (!device) return

    let ws: WebSocket
    let cancelled = false

    // The ticket is fetched before the socket opens, over an ordinary
    // authenticated request. The access token therefore never appears in a URL:
    // a WebSocket handshake cannot carry an Authorization header, which is why
    // the token used to go on the query string and into every access log,
    // history entry and Referer between here and the server.
    const connect = async () => {
      let ticket: string
      try {
        ticket = await api.getWebSocketTicket('remote-exec')
      } catch (err) {
        if (cancelled) return
        setStatus('error')
        setErrorMessage(
          err instanceof Error ? err.message : 'Failed to obtain a connection ticket.'
        )
        return
      }
      if (cancelled) return

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const wsUrl =
        `${protocol}//${window.location.host}/api/devices/${device.id}/terminal/ws` +
        `?ticket=${encodeURIComponent(ticket)}&shell=${encodeURIComponent(shell)}`

      try {
        ws = new WebSocket(wsUrl)
      } catch {
        setStatus('error')
        setErrorMessage('Failed to open terminal connection.')
        return
      }
      wsRef.current = ws

      ws.onopen = () => {
        setStatus('active')
        inputRef.current?.focus()
      }

      ws.onmessage = (event) => {
        try {
          const msg: TerminalMessage = JSON.parse(event.data)
          if (msg.type === 'term.open') {
            if (msg.session_id) setSessionID(msg.session_id)
            appendOutput(msg.data + '\r\n', 'system')
          } else if (msg.type === 'term.data') {
            if (msg.session_id) setSessionID(msg.session_id)
            appendOutput(msg.data)
          } else if (msg.type === 'term.close') {
            setStatus('closed')
            appendOutput('\r\n*** Remote shell session terminated ***\r\n', 'system')
          }
        } catch {
          // Ignore malformed frames
        }
      }

      ws.onerror = () => {
        setStatus('error')
        setErrorMessage('Terminal connection error. The endpoint may be offline.')
      }

      ws.onclose = () => {
        // Read the live value rather than the one captured when this effect ran:
        // a close arriving after an error must not overwrite the error the
        // operator needs to see with a bland "closed".
        setStatus((prev) => (prev === 'error' || prev === 'closed' ? prev : 'closed'))
      }
    }

    connect()

    return () => {
      cancelled = true
      try {
        wsRef.current?.close()
      } catch {
        // ignore
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [device, shell])

  const appendOutput = (text: string, kind: 'stdout' | 'system' = 'stdout') => {
    bufferRef.current += text
    if (outputRef.current) {
      const el = outputRef.current
      el.textContent = bufferRef.current
      el.className = `terminal-output ${kind === 'system' ? 'terminal-system' : ''}`
      el.scrollTop = el.scrollHeight
    }
  }

  const handleSendInput = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key !== 'Enter') return
    if (status !== 'active' || !wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return

    const input = inputRef.current?.value ?? ''
    // Echo locally so the operator sees what they typed (remote echo not guaranteed)
    appendOutput(input + '\r\n')
    if (inputRef.current) inputRef.current.value = ''

    wsRef.current.send(
      JSON.stringify({
        type: 'term.data',
        session_id: sessionID,
        data: input + '\r\n',
      })
    )
  }

  const handleClose = () => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      try {
        wsRef.current.send(JSON.stringify({ type: 'term.close', session_id: sessionID }))
      } catch {
        // ignore
      }
    }
    try {
      wsRef.current?.close()
    } catch {
      // ignore
    }
    onClose()
  }

  if (!device) return null

  const statusClass =
    status === 'active' ? 'status-success' : status === 'connecting' ? 'status-running' : 'status-failed'

  return (
    <Modal
      open={device !== null}
      onClose={handleClose}
      size="lg"
      // The live shell owns every keystroke, including Escape. Closing it here
      // would tear down the remote process on a stray keypress, so the operator
      // uses the close button (or the endpoint dropping the session).
      dismissible={false}
      title={
        <span className="modal-title-group">
          <Terminal size={20} /> Live Interactive Terminal
        </span>
      }
      description={
        <>
          {device.hostname} · {shell} shell ·{' '}
          <span className="font-mono">{device.id.substring(0, 16)}</span>
        </>
      }
    >
      <div className="modal-body">
        <div className="terminal-status-group">
          <span className={`status-pill ${statusClass}`}>{status.toUpperCase()}</span>
        </div>

        {status === 'error' && (
          <div className="notification-banner error" role="alert">
            <AlertCircle size={18} />
            <span>{errorMessage}</span>
          </div>
        )}

        <div className="live-terminal">
          {/* The output is appended by hand on every frame, so it is a live
              region rather than a React subtree. */}
          <div className="terminal-output" ref={outputRef} role="log" aria-live="polite" />
          <div className="terminal-input-row">
            <span className="terminal-prompt">$</span>
            <input
              ref={inputRef}
              type="text"
              className="terminal-input"
              aria-label="Remote shell input"
              placeholder={
                status === 'active' ? 'Type a command and press Enter...' : 'Terminal not active'
              }
              disabled={status !== 'active'}
              onKeyDown={handleSendInput}
              spellCheck={false}
              autoComplete="off"
            />
          </div>
        </div>

        <p className="form-hint">
          Interactive session streams bidirectionally over the agent's outbound WebSocket.
          Closing this window terminates the shell process on the endpoint.
        </p>
      </div>
    </Modal>
  )
}
