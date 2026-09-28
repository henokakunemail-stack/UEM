import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  AlertTriangle,
  Copy,
  FileText,
  Info,
  RefreshCw,
  Search,
  XCircle,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'

const LEVELS = ['all', 'error', 'warn', 'info', 'debug'] as const
type LevelFilter = (typeof LEVELS)[number]

// zerolog writes a JSON object per event. The tail endpoint hands back those
// raw lines, so level is a cheap substring check rather than a full parse —
// a malformed line just falls through to "info", which is the right default
// for something the user is skimming.
function levelOf(line: string): Exclude<LevelFilter, 'all'> {
  if (line.includes('"level":"error"') || line.includes('"level":"fatal"')) return 'error'
  if (line.includes('"level":"warn"')) return 'warn'
  if (line.includes('"level":"debug"') || line.includes('"level":"trace"')) return 'debug'
  return 'info'
}

function prettyLine(raw: string): string {
  try {
    const obj = JSON.parse(raw) as Record<string, unknown>
    const time = typeof obj.time === 'string' ? obj.time.slice(11, 19) : '--:--:--'
    const level = String(obj.level ?? 'info').toUpperCase()
    const message = typeof obj.message === 'string' ? obj.message : raw
    // Everything except the known keys is a field the operator added (device
    // id, error, duration); showing it is the whole point of the page.
    const { level: _l, time: _t, message: _m, caller: _c, ...rest } = obj
    const extras = Object.keys(rest).length ? ' ' + JSON.stringify(rest) : ''
    return `${time} ${level.padEnd(5)} ${message}${extras}`
  } catch {
    return raw
  }
}

export const LogPage: React.FC = () => {
  const [lines, setLines] = useState<string[]>([])
  const [total, setTotal] = useState(0)
  const [logFile, setLogFile] = useState('')
  const [truncated, setTruncated] = useState(false)
  const [level, setLevel] = useState<LevelFilter>('all')
  const [search, setSearch] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  // null until a read succeeds. A failed read must not move it, or the header
  // would claim a fresh tail over lines that are minutes old.
  const [lastReadAt, setLastReadAt] = useState<Date | null>(null)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [follow, setFollow] = useState(true)
  const viewRef = useRef<HTMLDivElement>(null)
  const toast = useToast()

  const loadData = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const res = await api.getServerLogs(500)
      setLines(res.lines || [])
      setTotal(res.total)
      setLogFile(res.log_file || '')
      setTruncated(res.truncated)
      setError('')
      setLastReadAt(new Date())
    } catch (err: unknown) {
      // Keep whatever lines are already on screen; the banner says they are
      // stale rather than the page quietly claiming a live tail.
      setError(err instanceof Error ? err.message : 'Failed to read server log')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  useEffect(() => {
    if (!autoRefresh) return
    const t = setInterval(() => {
      // A hidden tab is not being read and has its timers throttled anyway.
      if (document.visibilityState === 'hidden') return
      loadData(true)
    }, 10000)
    return () => clearInterval(t)
  }, [autoRefresh, loadData])

  // Follow the tail only when the operator is already at the bottom, so
  // scrolling up to read something is not yanked away by the next poll.
  useEffect(() => {
    if (!follow || !viewRef.current) return
    viewRef.current.scrollTop = viewRef.current.scrollHeight
  }, [lines, follow])

  const term = search.toLowerCase()
  const visible = lines.filter((line) => {
    if (level !== 'all' && levelOf(line) !== level) return false
    if (!term) return true
    return line.toLowerCase().includes(term)
  })

  const counts = {
    error: lines.filter((l) => levelOf(l) === 'error').length,
    warn: lines.filter((l) => levelOf(l) === 'warn').length,
  }

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(visible.join('\n'))
      toast.success(`${visible.length} lines copied to clipboard`, 'Log Copied')
    } catch {
      toast.error('Clipboard is unavailable in this browser', 'Copy Failed')
    }
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h1 className="page-title">Server Runtime Log</h1>
          <p className="page-subtitle">
            Live tail of what the management server itself is doing — use this when a menu
            misbehaves, not the Audit tab, which records what operators did
          </p>
        </div>
        <div className="header-controls">
          <label className="toggle-label">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            <span>Auto-refresh (10s)</span>
          </label>
          <span className={`last-sync${error ? ' stale' : ''}`}>
            {lastReadAt ? `Read at ${lastReadAt.toLocaleTimeString()}` : 'Not yet read'}
          </span>
          <button type="button" className="btn btn-secondary" onClick={handleCopy}>
            <Copy size={16} />
            <span>Copy</span>
          </button>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => loadData()}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {error && (
        <div className="alert-banner alert-error" role="status">
          <XCircle size={16} />
          <span>
            {error}
            {lines.length > 0 ? ' Showing the last successful read below.' : ''}
          </span>
        </div>
      )}

      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Buffered Lines</span>
            <FileText className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{total}</div>
          <span className="kpi-hint">
            {truncated ? `Showing last ${lines.length}` : 'Whole buffer fits in view'}
          </span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Errors in Buffer</span>
            <XCircle className="kpi-icon text-danger" size={20} />
          </div>
          <div className="kpi-value text-danger">{counts.error}</div>
          <span className="kpi-hint">Server-side failures, not operator actions</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Warnings in Buffer</span>
            <AlertTriangle className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">{counts.warn}</div>
          <span className="kpi-hint">Degraded paths worth reviewing</span>
        </div>
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Persistence</span>
            <Info className="kpi-icon text-muted" size={20} />
          </div>
          <div className="kpi-value" style={{ fontSize: '0.875rem', wordBreak: 'break-all' }}>
            {logFile ? logFile : 'stdout only'}
          </div>
          <span className="kpi-hint">
            {logFile
              ? 'Set by LOG_FILE; survives restarts'
              : 'Set LOG_FILE to persist this beyond the ring buffer'}
          </span>
        </div>
      </div>

      <div className="table-card">
        <div className="table-toolbar">
          <div className="search-wrap">
            <Search size={16} className="search-icon" aria-hidden="true" />
            <input
              type="text"
              className="search-input"
              placeholder="Filter the loaded log text…"
              aria-label="Filter the loaded log lines"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <div className="btn-group">
            {LEVELS.map((lv) => (
              <button
                key={lv}
                type="button"
                className={`btn btn-sm ${level === lv ? 'btn-primary' : 'btn-secondary'}`}
                onClick={() => setLevel(lv)}
                aria-pressed={level === lv}
              >
                {lv === 'all' ? 'All' : lv.toUpperCase()}
              </button>
            ))}
          </div>
          <label className="toggle-label">
            <input
              type="checkbox"
              checked={follow}
              onChange={(e) => setFollow(e.target.checked)}
            />
            <span>Follow tail</span>
          </label>
        </div>

        <div
          className="log-view"
          ref={viewRef}
          role="log"
          aria-label="Server runtime log"
          onScroll={(e) => {
            const el = e.currentTarget
            // Turn follow off the moment the operator scrolls up, rather than
            // yanking them back down on the next poll.
            const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
            if (atBottom !== follow) setFollow(atBottom)
          }}
        >
          {loading ? (
            <div className="page-loader">
              <RefreshCw size={28} className="spinning" />
              <span>Reading server log buffer...</span>
            </div>
          ) : visible.length === 0 ? (
            <div className="empty-state">
              <FileText size={32} />
              <p>
                {lines.length === 0
                  ? 'No log lines buffered yet'
                  : `No ${level === 'all' ? '' : level.toUpperCase() + ' '}lines match the current filter`}
              </p>
            </div>
          ) : (
            visible.map((line, i) => {
              const lv = levelOf(line)
              return (
                <div key={`${i}-${line.slice(0, 40)}`} className={`log-line log-line-${lv}`}>
                  {prettyLine(line)}
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}
