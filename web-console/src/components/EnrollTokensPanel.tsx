import React, { useCallback, useEffect, useState } from 'react'
import { Ban, RefreshCw } from 'lucide-react'

import { api } from '../services/api'
import type { SharedEnrollmentTokenDTO } from '../types/api'
import { useT } from '../i18n/core'
import { useToast } from '../context/ToastContext'
import { ConfirmDialog } from './ui/ConfirmDialog'
import { DataTable } from './ui/DataTable'

const STATUS_PILL: Record<SharedEnrollmentTokenDTO['status'], string> = {
  active: 'online',
  used_up: 'warning',
  expired: 'offline',
  revoked: 'danger',
}

/**
 * Multi-device enrollment tokens: how many machines each has enrolled, and a
 * way to stop one. Before this, a token that had run out of uses looked the
 * same as one that never worked, and a leaked token could only be waited out.
 */
export const EnrollTokensPanel: React.FC = () => {
  const { t } = useT()
  const toast = useToast()
  const [tokens, setTokens] = useState<SharedEnrollmentTokenDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [toRevoke, setToRevoke] = useState<SharedEnrollmentTokenDTO | null>(null)
  const [revoking, setRevoking] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setTokens(await api.listEnrollmentTokens())
      setLoadFailed(false)
    } catch {
      setLoadFailed(true)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const revoke = async () => {
    if (!toRevoke) return
    setRevoking(true)
    try {
      await api.revokeEnrollmentToken(toRevoke.id)
      toast.success(t('enroll_tokens.revoked', { name: toRevoke.name }), t('enroll_tokens.revoked_title'))
      setToRevoke(null)
      load()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : t('enroll_tokens.revoke_failed'), t('enroll_tokens.revoke_failed_title'))
    } finally {
      setRevoking(false)
    }
  }

  return (
    <div className="table-card">
      <div className="table-toolbar">
        <div>
          <h3 className="section-title">{t('enroll_tokens.title')}</h3>
          <p className="text-sm text-muted">{t('enroll_tokens.subtitle')}</p>
        </div>
        <button type="button" className="btn btn-secondary" onClick={load} disabled={loading}>
          <RefreshCw size={16} className={loading ? 'spinning' : ''} />
          <span>{t('enroll_tokens.refresh')}</span>
        </button>
      </div>

      {loadFailed && (
        <div className="alert-banner alert-warning" role="alert">
          <span>{t('enroll_tokens.load_failed')}</span>
        </div>
      )}

      <DataTable label={t('enroll_tokens.table_label')}>
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('enroll_tokens.col_name')}</th>
              <th>{t('enroll_tokens.col_site')}</th>
              <th>{t('enroll_tokens.col_usage')}</th>
              <th>{t('enroll_tokens.col_status')}</th>
              <th>{t('enroll_tokens.col_expires')}</th>
              <th>{t('enroll_tokens.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {tokens.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-center py-8 text-muted">
                  {loadFailed ? '—' : t('enroll_tokens.empty')}
                </td>
              </tr>
            ) : (
              tokens.map((tok) => (
                <tr key={tok.id}>
                  <td className="font-semibold text-main">{tok.name}</td>
                  <td>{tok.site || '—'}</td>
                  <td>
                    {tok.max_uses === 0
                      ? t('enroll_tokens.usage_unlimited', { used: tok.use_count })
                      : t('enroll_tokens.usage_limited', { used: tok.use_count, max: tok.max_uses })}
                  </td>
                  <td>
                    <span className={`status-pill ${STATUS_PILL[tok.status] ?? 'offline'}`}>
                      {t(`enroll_tokens.status_${tok.status}`)}
                    </span>
                  </td>
                  <td className="text-sm text-muted" title={new Date(tok.expires_at).toLocaleString()}>
                    {new Date(tok.expires_at).toLocaleString()}
                  </td>
                  <td>
                    {tok.status === 'active' || tok.status === 'used_up' ? (
                      <button
                        type="button"
                        className="btn-action text-danger"
                        onClick={() => setToRevoke(tok)}
                        disabled={revoking}
                      >
                        <Ban size={14} />
                        <span>{t('enroll_tokens.revoke')}</span>
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </DataTable>

      <ConfirmDialog
        open={toRevoke !== null}
        title={t('enroll_tokens.revoke_confirm_title')}
        message={t('enroll_tokens.revoke_confirm', { name: toRevoke?.name ?? '' })}
        confirmLabel={t('enroll_tokens.revoke')}
        pending={revoking}
        onConfirm={revoke}
        onCancel={() => setToRevoke(null)}
      />
    </div>
  )
}
