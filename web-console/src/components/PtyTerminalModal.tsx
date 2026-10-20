import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertCircle, ClipboardPaste, Copy, Terminal as TerminalIcon } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../services/api'
import { Modal } from './ui'
import type { DeviceDTO } from '../types/api'
import { useT } from '../i18n/core'

interface PtyTerminalModalProps {
  device: DeviceDTO | null
  shell: string
  onClose: () => void
}

type Status = 'connecting' | 'active' | 'closed' | 'error'

/** The terminal for Linux and macOS endpoints: xterm.js in front of a shell
 *  the agent runs on a pseudo-terminal, so it behaves like PuTTY -- the
 *  shell's own prompt and echo, Ctrl+C, Tab completion, colours, vim and top.
 *
 *  An agent that predates the pty never sends term.mode, and its shell sits on
 *  a pipe that neither echoes nor edits lines. For that case the terminal
 *  falls back to local line editing: echo, Backspace, and the line sent on
 *  Enter, which is what the old terminal did. */
export function PtyTerminalModal({ device, shell, onClose }: PtyTerminalModalProps) {
  const { t } = useT()
  const [status, setStatus] = useState<Status>('connecting')
  const [errorMessage, setErrorMessage] = useState('')
  const [pty, setPty] = useState(false)
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const send = useCallback((msg: Record<string, unknown>) => {
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg))
  }, [])

  useEffect(() => {
    if (!device || !hostRef.current) return
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      scrollback: 5000,
      theme: { background: '#0b1020', foreground: '#d7dde8', cursor: '#d7dde8' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(hostRef.current)
    fit.fit()
    term.focus()
    termRef.current = term

    let cancelled = false
    let ptyMode = false
    let line = ''

    // Ctrl+Shift+C copies the selection and Ctrl+Shift+V pastes, as in most
    // Linux terminals; plain Ctrl+C stays the interrupt it is on the endpoint.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown' || !e.ctrlKey || !e.shiftKey) return true
      if (e.code === 'KeyC') {
        const sel = term.getSelection()
        if (sel) void navigator.clipboard?.writeText(sel).catch(() => {})
        return false
      }
      if (e.code === 'KeyV') return false // the browser's paste event delivers it
      return true
    })

    const input = term.onData((data) => {
      if (ptyMode) {
        send({ type: 'term.data', data })
        return
      }
      // Line mode for an agent without a pty.
      for (const ch of data) {
        if (ch === '\r') {
          term.write('\r\n')
          send({ type: 'term.data', data: line + '\n' })
          line = ''
        } else if (ch === '\x7f' || ch === '\b') {
          if (line.length > 0) {
            line = line.slice(0, -1)
            term.write('\b \b')
          }
        } else if (ch >= ' ') {
          line += ch
          term.write(ch)
        }
      }
    })

    let resizeTimer = 0
    const resized = term.onResize(({ cols, rows }) => {
      window.clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(() => send({ type: 'term.resize', cols, rows }), 120)
    })
    const observer = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        /* the host is hidden mid-close */
      }
    })
    observer.observe(hostRef.current)

    const connect = async () => {
      let ticket: string
      try {
        ticket = await api.getWebSocketTicket('remote-exec')
      } catch (err) {
        if (cancelled) return
        setStatus('error')
        setErrorMessage(err instanceof Error ? err.message : t('terminal.ticket_failed'))
        return
      }
      if (cancelled) return
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url =
        `${protocol}//${window.location.host}/api/devices/${device.id}/terminal/ws` +
        `?ticket=${encodeURIComponent(ticket)}&shell=${encodeURIComponent(shell)}` +
        `&cols=${term.cols}&rows=${term.rows}`
      let ws: WebSocket
      try {
        ws = new WebSocket(url)
      } catch {
        setStatus('error')
        setErrorMessage(t('terminal.open_failed'))
        return
      }
      wsRef.current = ws
      ws.onopen = () => setStatus('active')
      ws.onmessage = (event) => {
        let msg: { type?: string; data?: string }
        try {
          msg = JSON.parse(event.data)
        } catch {
          return
        }
        switch (msg.type) {
          case 'term.open':
            term.write(`\x1b[2m${msg.data ?? ''}\x1b[0m\r\n`)
            break
          case 'term.mode':
            if (msg.data === 'pty') {
              ptyMode = true
              setPty(true)
            }
            break
          case 'term.data':
            if (msg.data) term.write(msg.data)
            break
          case 'term.error':
            setStatus('error')
            setErrorMessage(msg.data ?? t('terminal.connection_error'))
            break
          case 'term.close':
            setStatus('closed')
            term.write(`\r\n\x1b[2m${t('terminal.session_terminated')}\x1b[0m\r\n`)
            break
        }
      }
      ws.onerror = () => {
        setStatus('error')
        setErrorMessage(t('terminal.connection_error'))
      }
      ws.onclose = () => setStatus((prev) => (prev === 'error' || prev === 'closed' ? prev : 'closed'))
    }
    void connect()

    return () => {
      cancelled = true
      window.clearTimeout(resizeTimer)
      observer.disconnect()
      input.dispose()
      resized.dispose()
      try {
        wsRef.current?.close()
      } catch {
        /* already closed */
      }
      wsRef.current = null
      term.dispose()
      termRef.current = null
    }
    // t is stable per language; reconnecting the shell on a language switch
    // would kill the operator's session.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [device, shell, send])

  const copySelection = () => {
    const sel = termRef.current?.getSelection()
    if (sel) void navigator.clipboard?.writeText(sel).catch(() => {})
    termRef.current?.focus()
  }

  const paste = async () => {
    try {
      const text = await navigator.clipboard.readText()
      if (text) termRef.current?.paste(text)
    } catch {
      /* clipboard permission denied; Ctrl+Shift+V still works */
    }
    termRef.current?.focus()
  }

  const handleClose = () => {
    send({ type: 'term.close' })
    onClose()
  }

  if (!device) return null
  const statusClass =
    status === 'active' ? 'status-success' : status === 'connecting' ? 'status-running' : 'status-failed'

  return (
    <Modal
      open={device !== null}
      onClose={handleClose}
      size="xl"
      // The shell owns every keystroke, Escape included.
      dismissible={false}
      title={
        <span className="modal-title-group">
          <TerminalIcon size={20} /> {t('terminal.title')}
        </span>
      }
      description={
        <>
          {device.hostname} · {t('terminal.shell_suffix', { shell })} ·{' '}
          <span className="font-mono">{device.id.substring(0, 16)}</span>
        </>
      }
    >
      <div className="pty-terminal">
        <div className="pty-toolbar">
          <span className={`status-pill ${statusClass}`}>{t(`terminal.status_${status}`)}</span>
          {status === 'active' && (
            <span className="pty-mode" title={pty ? t('terminal.mode_pty_hint') : t('terminal.mode_line_hint')}>
              {pty ? t('terminal.mode_pty') : t('terminal.mode_line')}
            </span>
          )}
          <span className="pty-toolbar-spacer" />
          <button type="button" className="btn btn-sm btn-secondary" onClick={copySelection} title={t('terminal.copy_hint')}>
            <Copy size={13} /> <span>{t('terminal.copy')}</span>
          </button>
          <button type="button" className="btn btn-sm btn-secondary" onClick={paste} title={t('terminal.paste_hint')}>
            <ClipboardPaste size={13} /> <span>{t('terminal.paste')}</span>
          </button>
        </div>
        {status === 'error' && (
          <div className="notification-banner error" role="alert">
            <AlertCircle size={18} />
            <span>{errorMessage}</span>
          </div>
        )}
        <div className="pty-host" ref={hostRef} aria-label={t('terminal.input_label')} />
        <p className="form-hint">{t('terminal.pty_hint')}</p>
      </div>
    </Modal>
  )
}
