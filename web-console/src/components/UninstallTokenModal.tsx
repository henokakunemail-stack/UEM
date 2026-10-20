import React, { useState } from 'react'
import { AlertCircle, Check, Copy, KeyRound, ShieldAlert } from 'lucide-react'
import { Modal } from './ui/Modal'
import { useToast } from '../context/ToastContext'
import { api } from '../services/api'
import type { DeviceDTO, UninstallTokenResp } from '../types/api'
import { useT } from '../i18n/core'

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
  const { t } = useT()
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
      toast.success(t('uninstall.generated'), t('uninstall.generated_title'))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('uninstall.generate_failed'))
    } finally {
      setPending(false)
    }
  }

  const handleCopy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
      toast.info(t('uninstall.copied'))
    } catch {
      toast.error(t('uninstall.copy_failed'))
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
        toast.success(t('uninstall.valid_toast'))
      } else {
        toast.error(t('uninstall.invalid_toast'))
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t('uninstall.verify_failed'))
    } finally {
      setVerifying(false)
    }
  }

  if (!device) return null

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title={t('uninstall.title')}
      size="md"
      footer={
        <div className="flex justify-end w-full">
          <button type="button" className="btn btn-secondary" onClick={handleClose}>
            {t('common.close')}
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
            <strong>{t('uninstall.protection_title')}</strong> {t('uninstall.protection_body')}
          </div>
        </div>

        {!tokenData ? (
          <div className="text-center py-4 space-y-3">
            <p className="text-xs text-muted">
              {t('uninstall.intro')}
            </p>
            <button
              type="button"
              className="btn btn-primary inline-flex items-center gap-2"
              onClick={handleGenerate}
              disabled={pending}
            >
              <KeyRound size={16} />
              <span>{pending ? t('uninstall.generating') : t('uninstall.generate')}</span>
            </button>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="form-group">
              <label className="form-label text-xs">{t('uninstall.token_label')}</label>
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
                  title={t('uninstall.copy_token')}
                >
                  {copied ? <Check size={16} className="text-emerald-400" /> : <Copy size={16} />}
                </button>
              </div>
              <span className="text-xs text-muted mt-1 block">
                {t('uninstall.expires_at', {
                  time: new Date(tokenData.expires_at).toLocaleTimeString(),
                  date: new Date(tokenData.expires_at).toLocaleDateString(),
                })}
              </span>
            </div>

            {/* Platform Instructions */}
            <div className="text-xs space-y-2" style={{ padding: '0.75rem', borderRadius: '0.5rem', border: '1px solid var(--border-subtle)', background: 'var(--bg-surface)' }}>
              <div className="font-semibold text-main">{t('uninstall.how_to')}</div>
              {device.os_name === 'windows' ? (
                <div className="space-y-1 text-muted">
                  <p>
                    {t('uninstall.win_step1_before')} <strong>EndpointAgent-Uninstall.exe</strong>{' '}
                    {t('uninstall.win_step1_after')}
                  </p>
                  <p>{t('uninstall.win_step2')}</p>
                  <p>{t('uninstall.win_cli')}</p>
                  <code className="block p-1.5 rounded font-mono text-zinc-300 overflow-x-auto" style={{ background: 'var(--bg-surface-elevated)' }}>
                    endpoint-agent.exe -service uninstall -uninstall-token {tokenData.uninstall_token}
                  </code>
                </div>
              ) : (
                <div className="space-y-1 text-muted">
                  <p>{t('uninstall.unix_step')}</p>
                  <code className="block p-1.5 rounded font-mono text-zinc-300 overflow-x-auto" style={{ background: 'var(--bg-surface-elevated)' }}>
                    sudo ./uninstall-agent.sh false {tokenData.uninstall_token}
                  </code>
                </div>
              )}
            </div>

            {/* Quick check verification */}
            <div className="pt-2" style={{ borderTop: '1px solid var(--border-subtle)' }}>
              <label className="form-label text-xs">{t('uninstall.verify_label')}</label>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  className="form-input text-xs font-mono"
                  placeholder={t('uninstall.verify_placeholder')}
                  value={verifyInput}
                  onChange={(e) => setVerifyInput(e.target.value)}
                />
                <button
                  type="button"
                  className="btn btn-secondary btn-sm shrink-0"
                  onClick={handleVerify}
                  disabled={verifying || !verifyInput.trim()}
                >
                  {verifying ? t('uninstall.checking') : t('uninstall.check')}
                </button>
              </div>
              {verifyStatus !== null && (
                <div className="mt-1 text-xs flex items-center gap-1">
                  {verifyStatus ? (
                    <span className="text-emerald-400 flex items-center gap-1">
                      <Check size={14} /> {t('uninstall.valid')}
                    </span>
                  ) : (
                    <span className="text-red-400 flex items-center gap-1">
                      <AlertCircle size={14} /> {t('uninstall.invalid')}
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
