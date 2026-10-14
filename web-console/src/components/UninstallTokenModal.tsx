import React, { useState } from 'react'
import { AlertCircle, Check, Copy, KeyRound, ShieldAlert } from 'lucide-react'
import { Modal } from './ui/Modal'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceDTO, UninstallTokenResp } from '../types/api'

interface UninstallTokenModalProps {
  device: DeviceDTO | null
  open: boolean
  onClose: () => void
}

export const UninstallTokenModal: React.FC<UninstallTokenModalProps> = ({
  device,
  open,
  onClose,
}) => {
  const toast = useToast()
  const [pending, setPending] = useState(false)
  const [tokenData, setTokenData] = useState<UninstallTokenResp | null>(null)
  const [copied, setCopied] = useState(false)
  const [verifyInput, setVerifyInput] = useState('')
  const [verifyStatus, setVerifyStatus] = useState<boolean | null>(null)
  const [verifying, setVerifying] = useState(false)

  const handleClose = () => {
    setTokenData(null)
    setCopied(false)
    setVerifyInput('')
    setVerifyStatus(null)
    onClose()
  }

  const handleGenerate = async () => {
    if (!device) return
    setPending(true)
    try {
      const res = await api.createUninstallToken(device.id)
      setTokenData(res)
      toast.success('Uninstall verification token generated', 'Dual-Verification Active')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to generate token')
    } finally {
      setPending(false)
    }
  }

  const handleCopy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
      toast.info('Copied token to clipboard')
    } catch {
      toast.error('Failed to copy to clipboard')
    }
  }

  const handleVerify = async () => {
    if (!device || !verifyInput.trim()) return
    setVerifying(true)
    setVerifyStatus(null)
    try {
      const res = await api.verifyUninstallToken(device.id, verifyInput.trim())
      setVerifyStatus(res.valid)
      if (res.valid) {
        toast.success('Token is valid and active')
      } else {
        toast.error('Token is invalid or expired')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Verification check failed')
    } finally {
      setVerifying(false)
    }
  }

  if (!device) return null

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title="Agent Uninstallation Verification"
      size="md"
      footer={
        <div className="flex justify-end w-full">
          <button type="button" className="btn btn-secondary" onClick={handleClose}>
            Close
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        {/* Device Header */}
        <div className="flex items-center justify-between" style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface-elevated)' }}>
          <div>
            <div className="text-sm font-semibold text-main">{device.hostname}</div>
            <div className="text-xs text-muted font-mono">{device.id}</div>
          </div>
          <span className="text-xs px-2 py-0.5 rounded-full font-medium" style={{ background: 'rgba(59, 130, 246, 0.1)', color: 'var(--primary)', border: '1px solid rgba(59, 130, 246, 0.2)' }}>
            {device.os_name}
          </span>
        </div>

        {/* Security Concept Notice */}
        <div className="alert-banner alert-warning" style={{ margin: 0 }}>
          <ShieldAlert className="h-4 w-4 shrink-0" />
          <div>
            <strong>2-Step Dual-Verification Protection:</strong> Local endpoint users cannot uninstall or stop the agent without an administrator-generated verification token.
          </div>
        </div>

        {!tokenData ? (
          <div className="text-center py-4 space-y-3">
            <p className="text-xs text-muted">
              Generate a time-limited (2-hour) authorization token to permit uninstallation on this device.
            </p>
            <button
              type="button"
              className="btn btn-primary inline-flex items-center gap-2"
              onClick={handleGenerate}
              disabled={pending}
            >
              <KeyRound size={16} />
              <span>{pending ? 'Generating Token...' : 'Generate Uninstall Token'}</span>
            </button>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="form-group">
              <label className="form-label text-xs">Uninstall Verification Token</label>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  readOnly
                  className="form-input font-mono text-center font-bold tracking-wider text-primary text-base"
                  value={tokenData.uninstall_token}
                />
                <button
                  type="button"
                  className="btn btn-secondary shrink-0"
                  onClick={() => handleCopy(tokenData.uninstall_token)}
                  title="Copy token"
                >
                  {copied ? <Check size={16} className="text-emerald-400" /> : <Copy size={16} />}
                </button>
              </div>
              <span className="text-xs text-muted mt-1 block">
                Expires at {new Date(tokenData.expires_at).toLocaleTimeString()} ({new Date(tokenData.expires_at).toLocaleDateString()})
              </span>
            </div>

            {/* Platform Instructions */}
            <div className="text-xs space-y-2" style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface)' }}>
              <div className="font-semibold text-main">How to use on endpoint:</div>
              {device.os_name === 'windows' ? (
                <div className="space-y-1 text-muted">
                  <p>1. Run <strong>EndpointAgent-Uninstall.exe</strong> on the target machine.</p>
                  <p>2. Paste this token into the verification prompt when requested.</p>
                  <p>Or via administrator command line:</p>
                  <code className="block p-1.5 rounded font-mono text-zinc-300 overflow-x-auto" style={{ background: 'var(--bg-surface-elevated)' }}>
                    endpoint-agent.exe -service uninstall -uninstall-token {tokenData.uninstall_token}
                  </code>
                </div>
              ) : (
                <div className="space-y-1 text-muted">
                  <p>Run the uninstaller script with the token:</p>
                  <code className="block p-1.5 rounded font-mono text-zinc-300 overflow-x-auto" style={{ background: 'var(--bg-surface-elevated)' }}>
                    sudo ./uninstall-agent.sh false {tokenData.uninstall_token}
                  </code>
                </div>
              )}
            </div>

            {/* Quick check verification */}
            <div className="pt-2" style={{ borderTop: '1px solid var(--border-subtle)' }}>
              <label className="form-label text-xs">Verify Token Status</label>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  className="form-input text-xs font-mono"
                  placeholder="Paste token to test validity..."
                  value={verifyInput}
                  onChange={(e) => setVerifyInput(e.target.value)}
                />
                <button
                  type="button"
                  className="btn btn-secondary btn-sm shrink-0"
                  onClick={handleVerify}
                  disabled={verifying || !verifyInput.trim()}
                >
                  {verifying ? 'Checking...' : 'Check'}
                </button>
              </div>
              {verifyStatus !== null && (
                <div className="mt-1 text-xs flex items-center gap-1">
                  {verifyStatus ? (
                    <span className="text-emerald-400 flex items-center gap-1">
                      <Check size={14} /> Token is valid and ready for consumption.
                    </span>
                  ) : (
                    <span className="text-red-400 flex items-center gap-1">
                      <AlertCircle size={14} /> Token is invalid, expired, or already consumed.
                    </span>
                  )}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}
