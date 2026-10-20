import React, { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  AlertCircle,
  AlertTriangle,
  Bell,
  CheckCircle2,
  Filter,
  RefreshCw,
  X,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { usePermission } from '../hooks/usePermission'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { useT } from '../i18n/core'
import type { AlertIncidentDTO, AlertRuleDTO } from '../types/api'

const STATUS_FILTERS = [
  { value: 'open', labelKey: 'alerts.status_open' },
  { value: 'acknowledged', labelKey: 'alerts.status_acknowledged' },
  { value: 'resolved', labelKey: 'alerts.status_resolved' },
  { value: '', labelKey: 'alerts.status_all' },
] as const

export const AlertsPage: React.FC = () => {
  const navigate = useNavigate()
  const { t } = useT()
  const { can } = usePermission()
  // The server gates acknowledge/resolve at technician and above.
  const canAct = can('technician')
  const [incidents, setIncidents] = useState<AlertIncidentDTO[]>([])
  const [rules, setRules] = useState<AlertRuleDTO[]>([])
  const [statusFilter, setStatusFilter] = useState<string>('open')
  // Counts for the KPI cards, read from an unfiltered fetch. `incidents` is
  // filtered server-side, so counting it made both cards read 0 the moment any
  // status filter was active — the one number an operator scans first.
  const [counts, setCounts] = useState<{ open: number; acknowledged: number }>({ open: 0, acknowledged: 0 })
  const [loading, setLoading] = useState(true)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()
  const severityLabel = (severity: string) =>
    severity === 'critical' || severity === 'warning' || severity === 'info'
      ? t(`alerts.severity_${severity}`)
      : severity.toUpperCase()
  const statusLabel = (status: string) =>
    status === 'open' || status === 'acknowledged' || status === 'resolved'
      ? t(`alerts.status_${status}`)
      : status

  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const [incResp, rList, openResp, ackResp] = await Promise.all([
        api.getAlertIncidents(statusFilter),
        // Rules are a side panel here; a rules failure must not blank the
        // incident table the operator came here to work through.
        api.getAlertRules().catch(() => null),
        // Count cards are best-effort: a failure here leaves the previous
        // numbers rather than failing the whole page.
        api.getAlertIncidents('open').catch(() => null),
        api.getAlertIncidents('acknowledged').catch(() => null),
      ])
      setIncidents(incResp.incidents || [])
      if (rList) setRules(rList)
      if (openResp || ackResp) {
        setCounts((prev) => ({
          open: openResp ? openResp.count : prev.open,
          acknowledged: ackResp ? ackResp.count : prev.acknowledged,
        }))
      }
    } catch (err: unknown) {
      setMsg({
        type: 'error',
        text: err instanceof Error ? err.message : t('alerts.load_failed'),
      })
    } finally {
      setLoading(false)
    }
  }, [statusFilter])

  useEffect(() => {
    loadData()
  }, [loadData])

  const labelFor = (i: AlertIncidentDTO) => i.hostname || i.device_id.slice(0, 10) || t('alerts.incident_fallback')

  const handleAcknowledge = async (id: string) => {
    const target = incidents.find((i) => i.id === id)
    const label = target ? labelFor(target) : t('alerts.incident_fallback')
    try {
      await api.acknowledgeIncident(id)
      const successText = t('alerts.ack_success', { label })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('alerts.ack_success_title'))
      loadData()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : t('alerts.ack_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('alerts.ack_failed_title'))
    }
  }

  const handleResolve = async (id: string) => {
    const target = incidents.find((i) => i.id === id)
    const label = target ? labelFor(target) : t('alerts.incident_fallback')
    try {
      await api.resolveIncident(id)
      const successText = t('alerts.resolve_success', { label })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('alerts.resolve_success_title'))
      loadData()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : t('alerts.resolve_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('alerts.resolve_failed_title'))
    }
  }

  const openCount = counts.open
  const ackCount = counts.acknowledged

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('alerts.title')}</h2>
          <p className="page-subtitle">
            {t('alerts.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={loadData}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'animate-spin' : ''} />
            <span>{t('alerts.refresh')}</span>
          </button>
        </div>
      </div>

      {msg && (
        <div
          className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}
          role="status"
        >
          <span>{msg.text}</span>
          <button
            type="button"
            onClick={() => setMsg(null)}
            className="close-btn"
            aria-label={t('alerts.dismiss')}
          >
            <X size={14} />
          </button>
        </div>
      )}

      {/* KPI Overview */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('alerts.kpi_open')}</span>
            <AlertCircle className="kpi-icon text-danger" size={20} />
          </div>
          <div className="kpi-value text-danger">{openCount}</div>
          <span className="kpi-hint">{t('alerts.kpi_open_hint')}</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('alerts.kpi_ack')}</span>
            <AlertTriangle className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">{ackCount}</div>
          <span className="kpi-hint">{t('alerts.kpi_ack_hint')}</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">{t('alerts.kpi_rules')}</span>
            <Bell className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{rules.length}</div>
          <span className="kpi-hint">{t('alerts.kpi_rules_hint')}</span>
        </div>
      </div>

      {/* Incidents Card */}
      <div className="table-card">
        <div className="table-toolbar">
          <div className="flex items-center gap-2">
            <Filter size={16} className="text-dim" aria-hidden="true" />
            <span className="text-sm font-semibold text-muted">{t('alerts.filter_by_status')}</span>
            <div className="btn-group">
              {STATUS_FILTERS.map((f) => (
                <button
                  key={f.labelKey}
                  type="button"
                  className={`btn btn-sm ${statusFilter === f.value ? 'btn-primary' : 'btn-secondary'}`}
                  onClick={() => setStatusFilter(f.value)}
                  aria-pressed={statusFilter === f.value}
                >
                  {t(f.labelKey)}
                </button>
              ))}
            </div>
          </div>
        </div>

        <DataTable label={t('alerts.table_label')}>
          <thead>
            <tr>
              <th>{t('alerts.col_severity')}</th>
              <th>{t('alerts.col_device')}</th>
              <th>{t('alerts.col_rule')}</th>
              <th>{t('alerts.col_message')}</th>
              <th>{t('alerts.col_status')}</th>
              <th>{t('alerts.col_last_triggered')}</th>
              <th>{t('alerts.col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} className="text-center py-8 text-muted">
                  {t('alerts.loading_incidents')}
                </td>
              </tr>
            ) : incidents.length === 0 ? (
              <tr>
                <td colSpan={7} className="text-center py-8 text-muted">
                  {t('alerts.no_incidents', { filter: statusFilter ? statusLabel(statusFilter) : t('alerts.status_all') })}
                </td>
              </tr>
            ) : (
              incidents.map((i) => (
                <tr key={i.id}>
                  <td data-label={t('alerts.col_severity')}>
                    <span className={`badge-severity ${i.severity}`}>{severityLabel(i.severity)}</span>
                  </td>
                  <td data-label={t('alerts.col_device')}>
                    {i.device_id ? (
                      // Stable device_id, so this opens the right machine even
                      // when two endpoints share a hostname.
                      <button
                        type="button"
                        className="btn btn-sm btn-secondary"
                        onClick={() =>
                          navigate(`/devices?device_id=${encodeURIComponent(i.device_id)}`)
                        }
                        title={t('alerts.view_endpoint')}
                      >
                        {i.hostname || i.device_id.slice(0, 10)}
                      </button>
                    ) : (
                      <span className="font-semibold text-main">
                        {i.hostname || i.device_id.slice(0, 10)}
                      </span>
                    )}
                  </td>
                  <td data-label={t('alerts.col_rule')}>{i.rule_name || i.rule_id}</td>
                  <td data-label={t('alerts.col_message')}>{i.title || i.message}</td>
                  <td data-label={t('alerts.col_status')}>
                    <span
                      className={`status-pill ${
                        i.status === 'open'
                          ? 'danger'
                          : i.status === 'acknowledged'
                            ? 'warning'
                            : 'online'
                      }`}
                    >
                      {statusLabel(i.status)}
                    </span>
                  </td>
                  <td data-label={t('alerts.col_last_triggered')} className="text-sm text-muted">
                    {new Date(i.last_triggered_at).toLocaleString()}
                  </td>
                  <td data-label={t('alerts.col_actions')}>
                    <div className="action-buttons">
                      {canAct && i.status === 'open' && (
                        <button
                          type="button"
                          className="btn-action text-warning"
                          onClick={() => handleAcknowledge(i.id)}
                          title={t('alerts.ack_incident')}
                        >
                          <AlertTriangle size={14} />
                          <span>{t('alerts.ack')}</span>
                        </button>
                      )}
                      {canAct && i.status !== 'resolved' && (
                        <button
                          type="button"
                          className="btn-action text-success"
                          onClick={() => handleResolve(i.id)}
                          title={t('alerts.resolve_incident')}
                        >
                          <CheckCircle2 size={14} />
                          <span>{t('alerts.resolve')}</span>
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </DataTable>
      </div>
    </div>
  )
}
