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

// The binary name each platform's installer actually puts on disk. These are
// not the names `go build` produces (agent-windows-amd64.exe): packaging/windows/
// agent.nsi installs it as endpoint-agent.exe, and packaging/linux/deb installs
// it as /usr/bin/endpoint-agent. A copy-pasteable command has to name the file
// the operator will actually have.
const BINARY: Record<string, string> = {
  windows: 'endpoint-agent.exe',
  linux: 'endpoint-agent',
  macos: 'endpoint-agent',
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
  const [isMultiUse, setIsMultiUse] = useState(true)
  const [tokenName, setTokenName] = useState('')
  const [hostname, setHostname] = useState('')
  const [osName, setOsName] = useState('windows')
  const [site, setSite] = useState('')
  const [expiryHours, setExpiryHours] = useState(24)
  const [maxUses, setMaxUses] = useState(0) // 0 = unlimited
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Held only until this dialog closes. Never persisted, never refetched.
  const [issued, setIssued] = useState<EnrollmentTokenDTO | null>(null)
  const [copied, setCopied] = useState<string | null>(null)

  // Reset on open so a previous token is never still on screen behind a new one.
  const handleClose = () => {
    setIsMultiUse(true)
    setTokenName('')
    setHostname('')
    setSite('')
    setExpiryHours(24)
    setMaxUses(0)
    setError(null)
    setIssued(null)
    setCopied(null)
    onClose()
  }

  const handleGenerate = async () => {
    const trimmedHost = hostname.trim()
    const trimmedName = tokenName.trim()
    setPending(true)
    setError(null)
    try {
      const res = await api.createEnrollmentToken({
        is_multi_use: isMultiUse,
        ...(isMultiUse
          ? {
              name: trimmedName || 'Batch Deployment Token',
              max_uses: maxUses,
            }
          : {
              ...(trimmedHost ? { hostname: trimmedHost } : {}),
              os_name: osName,
              max_uses: 1,
            }),
        expires_in_hours: expiryHours,
        ...(site.trim() ? { site: site.trim() } : {}),
      })
      setIssued(res)
      toast.success(
        isMultiUse
          ? `Reusable token created (${maxUses === 0 ? 'Unlimited devices' : `Max ${maxUses} devices`})`
          : trimmedHost
          ? `Pre-registered ${trimmedHost}`
          : 'Single-use token created',
        'Enrollment Token Created'
      )
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
        { os: 'windows', text: `${BINARY.windows} -server ${server} -enroll ${issued.enrollment_token} -creds "%ALLUSERSPROFILE%\EndpointAgent\creds.json"` },
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
              disabled={pending}
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
          {/* Mode Selector */}
          <div className="form-field" style={{ marginBottom: '1rem' }}>
            <label className="form-label">Enrollment Type</label>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
              <button
                type="button"
                onClick={() => setIsMultiUse(true)}
                style={{
                  padding: '0.75rem',
                  borderRadius: 'var(--radius-md, 0.5rem)',
                  border: isMultiUse ? '2px solid var(--primary, #3b82f6)' : '1px solid var(--border-subtle, #333)',
                  background: isMultiUse ? 'rgba(59, 130, 246, 0.1)' : 'var(--bg-surface, #18181b)',
                  textAlign: 'left',
                  cursor: 'pointer',
                  color: 'inherit',
                }}
              >
                <div style={{ fontWeight: 600, fontSize: '0.875rem' }}>Multi-Device / Batch</div>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted, #a1a1aa)', marginTop: '0.25rem' }}>
                  1 token registers multiple devices. Recommended for GPO, mass deployment, or scripts.
                </div>
              </button>

              <button
                type="button"
                onClick={() => setIsMultiUse(false)}
                style={{
                  padding: '0.75rem',
                  borderRadius: 'var(--radius-md, 0.5rem)',
                  border: !isMultiUse ? '2px solid var(--primary, #3b82f6)' : '1px solid var(--border-subtle, #333)',
                  background: !isMultiUse ? 'rgba(59, 130, 246, 0.1)' : 'var(--bg-surface, #18181b)',
                  textAlign: 'left',
                  cursor: 'pointer',
                  color: 'inherit',
                }}
              >
                <div style={{ fontWeight: 600, fontSize: '0.875rem' }}>Single Device</div>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted, #a1a1aa)', marginTop: '0.25rem' }}>
                  One-time token bound to a single endpoint. Token is spent immediately upon enrollment.
                </div>
              </button>
            </div>
          </div>

          {isMultiUse ? (
            <div className="form-row">
              <div className="form-field">
                <label className="form-label" htmlFor="enroll-token-name">
                  Token Name / Label
                </label>
                <input
                  id="enroll-token-name"
                  className="form-input"
                  value={tokenName}
                  onChange={(e) => setTokenName(e.target.value)}
                  placeholder="e.g. Active Directory GPO Batch"
                  autoComplete="off"
                  disabled={pending}
                />
              </div>
              <div className="form-field">
                <label className="form-label" htmlFor="enroll-max-uses">
                  Device Limit
                </label>
                <select
                  id="enroll-max-uses"
                  className="form-select"
                  value={maxUses}
                  onChange={(e) => setMaxUses(Number(e.target.value))}
                  disabled={pending}
                >
                  <option value={0}>Unlimited Devices (0)</option>
                  <option value={10}>Max 10 Devices</option>
                  <option value={25}>Max 25 Devices</option>
                  <option value={50}>Max 50 Devices</option>
                  <option value={100}>Max 100 Devices</option>
                  <option value={250}>Max 250 Devices</option>
                </select>
              </div>
            </div>
          ) : (
            <div className="form-row">
              <div className="form-field">
                <label className="form-label" htmlFor="enroll-hostname">
                  Hostname <span className="text-muted text-xs font-normal">(Optional — auto-detected on connect)</span>
                </label>
                <input
                  id="enroll-hostname"
                  className="form-input"
                  value={hostname}
                  onChange={(e) => {
                    setHostname(e.target.value)
                    if (error) setError(null)
                  }}
                  placeholder="Leave blank to auto-detect from device"
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
          )}

          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="enroll-expiry">
                Expiration Duration
              </label>
              <select
                id="enroll-expiry"
                className="form-select"
                value={expiryHours}
                onChange={(e) => setExpiryHours(Number(e.target.value))}
                disabled={pending}
              >
                <option value={1}>1 Hour (Quick testing)</option>
                <option value={24}>24 Hours (1 Day — Standard)</option>
                <option value={168}>7 Days (1 Week deployment window)</option>
                <option value={720}>30 Days (1 Month rollout)</option>
                <option value={2160}>90 Days (Quarterly window)</option>
              </select>
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="enroll-site">
                Target Site (Optional)
              </label>
              <input
                id="enroll-site"
                className="form-input"
                value={site}
                onChange={(e) => setSite(e.target.value)}
                placeholder="e.g. Jakarta-HQ"
                autoComplete="off"
                disabled={pending}
              />
            </div>
          </div>

          {error && (
            <div className="alert-banner alert-error" style={{ marginTop: '1rem' }}>
              {error}
            </div>
          )}

          <p className="form-hint" style={{ marginTop: '1rem' }}>
            {isMultiUse
              ? 'This reusable token can enroll multiple endpoints. Each connecting device automatically creates its own independent identity and unique cryptographic secret.'
              : 'This single-use token will be consumed immediately upon the first device enrollment.'}
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
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '0.25rem' }}>
              <span className="form-label" style={{ margin: 0 }}>Enrollment Token</span>
              <span
                style={{
                  fontSize: '0.6875rem',
                  fontWeight: 600,
                  padding: '0.125rem 0.5rem',
                  borderRadius: '9999px',
                  background: issued.is_multi_use ? 'rgba(59, 130, 246, 0.15)' : 'rgba(16, 185, 129, 0.15)',
                  color: issued.is_multi_use ? 'var(--primary, #3b82f6)' : '#10b981',
                  border: issued.is_multi_use ? '1px solid rgba(59, 130, 246, 0.3)' : '1px solid rgba(16, 185, 129, 0.3)',
                }}
              >
                {issued.is_multi_use
                  ? `Multi-Device (${issued.max_uses === 0 ? 'Unlimited' : `Max ${issued.max_uses}`})`
                  : 'Single Device'}
              </span>
            </div>
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
