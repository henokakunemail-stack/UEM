import React, { useState } from 'react'
import { AlertTriangle, Check, Copy, KeyRound } from 'lucide-react'

import { Modal } from './ui/Modal'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { EnrollmentTokenDTO } from '../types/api'

interface EnrollTokenModalProps {
  open: boolean
  onClose: () => void
  /** Called after a token is minted, so the caller can refresh the device list
   *  — the pre-registered row exists server-side from that moment on. */
  onEnrolled?: () => void
}

// The binary name each platform's build actually produces. packaging/*/build.*
// writes these; nothing else in the repo is authoritative for it.
const BINARY: Record<string, string> = {
  windows: 'endpoint-mgmt-agent.exe',
  linux: 'endpoint-mgmt-agent',
  macos: 'endpoint-mgmt-agent',
}

/**
 * Mints a one-time enrollment token and shows it exactly once.
 *
 * The plaintext exists only in this component's state and in the response that
 * produced it. The server keeps a hash, and nulls it the moment the agent
 * exchanges it — so closing this dialog without copying the token means minting
 * another one, which is the intended behaviour rather than an accident.
 */
export const EnrollTokenModal: React.FC<EnrollTokenModalProps> = ({
  open,
  onClose,
  onEnrolled,
}) => {
  const toast = useToast()
  const [hostname, setHostname] = useState('')
  const [osName, setOsName] = useState('windows')
  const [site, setSite] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Held only until this dialog closes. Never persisted, never refetched.
  const [issued, setIssued] = useState<EnrollmentTokenDTO | null>(null)
  const [copied, setCopied] = useState<string | null>(null)

  // Reset on open so a previous token is never still on screen behind a new one.
  const handleClose = () => {
    setHostname('')
    setSite('')
    setError(null)
    setIssued(null)
    setCopied(null)
    onClose()
  }

  const handleGenerate = async () => {
    const trimmed = hostname.trim()
    if (!trimmed) {
      setError('A hostname is required — it is what the device will be known by')
      return
    }
    setPending(true)
    setError(null)
    try {
      const res = await api.createEnrollmentToken({
        hostname: trimmed,
        os_name: osName,
        ...(site.trim() ? { site: site.trim() } : {}),
      })
      setIssued(res)
      toast.success(`Pre-registered ${trimmed}`, 'Enrollment Token Created')
      onEnrolled?.()
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Could not create the token')
    } finally {
      setPending(false)
    }
  }

  // Built from the origin the console is served from, so the command an operator
  // pastes points at the server they are actually looking at — not a hostname
  // baked into a build.
  const server = typeof window !== 'undefined' ? window.location.origin : ''
  const commands = issued
    ? [
        { os: 'windows', text: `${BINARY.windows} -server ${server} -enroll ${issued.enrollment_token}` },
        {
          os: 'linux',
          text: `${BINARY.linux} -server ${server} -enroll ${issued.enrollment_token}`,
        },
      ]
    : []

  const copy = async (label: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(label)
      toast.success('Copied to clipboard', 'Copied')
    } catch {
      toast.error('Clipboard is unavailable in this browser', 'Copy Failed')
    }
  }

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title="Generate Enrollment Token"
      description={
        issued
          ? 'Copy the command now — this token is not shown again.'
          : 'Pre-register a device and hand it a one-time token to exchange for its secret.'
      }
      size="lg"
      // While a request is in flight, Escape and the backdrop must not close the
      // dialog: the token may already have been minted server-side, and a
      // dismissal would throw away the only copy of it.
      dismissible={!pending}
      footer={
        issued ? (
          <button type="button" className="btn btn-secondary" onClick={handleClose}>
            Done — I've copied it
          </button>
        ) : (
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={handleClose}
              disabled={pending}
            >
              Cancel
            </button>
            <button
              type="button"
              className="btn btn-primary"
              onClick={handleGenerate}
              disabled={pending || !hostname.trim()}
            >
              <KeyRound size={16} />
              <span>{pending ? 'Generating…' : 'Generate Token'}</span>
            </button>
          </>
        )
      }
    >
      {!issued ? (
        <>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="enroll-hostname">
                Hostname <span className="form-label-required">*</span>
              </label>
              <input
                id="enroll-hostname"
                className="form-input"
                value={hostname}
                onChange={(e) => {
                  setHostname(e.target.value)
                  if (error) setError(null)
                }}
                placeholder="e.g. LAPTOP-SARI-01"
                autoComplete="off"
                disabled={pending}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="enroll-os">
                Operating System
              </label>
              <select
                id="enroll-os"
                className="form-select"
                value={osName}
                onChange={(e) => setOsName(e.target.value)}
                disabled={pending}
              >
                <option value="windows">Windows</option>
                <option value="linux">Linux</option>
                <option value="macos">macOS</option>
              </select>
            </div>
          </div>

          <div className="form-field">
            <label className="form-label" htmlFor="enroll-site">
              Site
            </label>
            <input
              id="enroll-site"
              className="form-input"
              value={site}
              onChange={(e) => setSite(e.target.value)}
              placeholder="e.g. Surabaya-Branch (optional)"
              autoComplete="off"
              disabled={pending}
            />
          </div>

          {error && (
            <div className="alert-banner alert-error" style={{ marginTop: '1rem' }}>
              {error}
            </div>
          )}

          <p className="form-hint" style={{ marginTop: '1rem' }}>
            The token is valid until the expiry the server sets, then it stops working on its
            own. Run the agent with it once — the agent exchanges it for a persistent secret
            and the token is spent.
          </p>
        </>
      ) : (
        <>
          <div className="alert-banner alert-warning">
            <AlertTriangle size={16} style={{ flexShrink: 0, marginTop: 2 }} />
            <span>
              This token is shown once. Closing this dialog discards it, and the server keeps
              only a hash — it cannot show you the token again. If you lose it, generate a new
              one.
            </span>
          </div>

          <div className="form-field" style={{ marginTop: '1rem' }}>
            <span className="form-label">Enrollment Token</span>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'stretch' }}>
              <code
                style={{
                  flex: 1,
                  fontFamily: 'var(--font-mono, monospace)',
                  fontSize: '0.8125rem',
                  wordBreak: 'break-all',
                  padding: '0.5rem 0.75rem',
                  background: 'var(--color-bg-input, rgba(0,0,0,0.25))',
                  border: '1px solid var(--color-border)',
                  borderRadius: 'var(--radius-sm)',
                }}
              >
                {issued.enrollment_token}
              </code>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => copy('token', issued.enrollment_token)}
                title="Copy token"
              >
                {copied === 'token' ? <Check size={16} /> : <Copy size={16} />}
              </button>
            </div>
          </div>

          {issued.expires_at && (
            <p className="form-hint" style={{ marginTop: '0.5rem' }}>
              Expires {new Date(issued.expires_at).toLocaleString()}
            </p>
          )}

          <div style={{ marginTop: '1.25rem' }}>
            <span className="form-label">Run this on the device</span>
            {commands.map((c) => (
              <div key={c.os} className="form-field" style={{ marginBottom: '0.75rem' }}>
                <label className="form-label" htmlFor={`enroll-cmd-${c.os}`}>
                  {c.os === 'windows' ? 'Windows' : 'Linux / macOS'}
                </label>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'stretch' }}>
                  <code
                    id={`enroll-cmd-${c.os}`}
                    style={{
                      flex: 1,
                      fontFamily: 'var(--font-mono, monospace)',
                      fontSize: '0.75rem',
                      wordBreak: 'break-all',
                      padding: '0.5rem 0.75rem',
                      background: 'var(--color-bg-input, rgba(0,0,0,0.25))',
                      border: '1px solid var(--color-border)',
                      borderRadius: 'var(--radius-sm)',
                    }}
                  >
                    {c.text}
                  </code>
                  <button
                    type="button"
                    className="btn btn-secondary"
                    onClick={() => copy(c.os, c.text)}
                    title={`Copy ${c.os} command`}
                  >
                    {copied === c.os ? <Check size={16} /> : <Copy size={16} />}
                  </button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </Modal>
  )
}
