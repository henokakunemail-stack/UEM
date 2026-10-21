import React, { useMemo, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  ExternalLink,
  HardDrive,
  WifiOff,
} from 'lucide-react'
import { useT, type Vars } from '../i18n/core'
import type { AlertItem } from '../types/api'
import { dashboardAlertText } from '../services/alertText'

interface AlertsListProps {
  alerts: AlertItem[]
  /** Receives the stable device_id, not a hostname — the Devices page looks the
   *  device up by id, and a hostname is neither unique nor a valid key. */
  onViewDevice?: (deviceId: string) => void
}

function formatRelativeTime(dateString: string, t: (key: string, vars?: Vars) => string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diffSec = Math.floor((now.getTime() - date.getTime()) / 1000)

  if (diffSec < 60) return t('dashboard.just_now')
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return t('dashboard.minutes_ago', { count: diffMin })
  const diffHour = Math.floor(diffMin / 60)
  if (diffHour < 24) return t('dashboard.hours_ago', { count: diffHour })
  const diffDay = Math.floor(diffHour / 24)
  return t('dashboard.days_ago', { count: diffDay })
}

const getIcon = (type: string, severity: string) => {
  if (type === 'low_disk') {
    return <HardDrive size={16} className={`alert-icon ${severity}`} />
  }
  if (type === 'offline_long') {
    return <WifiOff size={16} className={`alert-icon ${severity}`} />
  }
  return severity === 'critical' ? (
    <AlertCircle size={16} className="alert-icon critical" />
  ) : (
    <AlertTriangle size={16} className="alert-icon warning" />
  )
}

export const AlertsList: React.FC<AlertsListProps> = ({ alerts, onViewDevice }) => {
  const { t } = useT()
  const [filter, setFilter] = useState<'all' | 'critical' | 'warning'>('all')
  const severityLabel = (severity: string) =>
    severity === 'critical' || severity === 'warning' || severity === 'info'
      ? t(`dashboard.severity_${severity}`)
      : severity.toUpperCase()

  const criticalCount = useMemo(
    () => alerts.filter((a) => a.severity === 'critical').length,
    [alerts],
  )
  const warningCount = useMemo(
    () => alerts.filter((a) => a.severity === 'warning').length,
    [alerts],
  )

  const filteredAlerts = useMemo(() => {
    if (filter === 'all') return alerts
    return alerts.filter((a) => a.severity === filter)
  }, [alerts, filter])

  return (
    <div className="dash-card">
      <div className="card-header">
        <div className="card-title-group">
          <AlertTriangle size={18} className="card-icon warning" />
          <h3 className="card-title">{t('dashboard.alerts_title')}</h3>
        </div>
        <span className={`card-badge ${alerts.length > 0 ? 'warning' : 'success'}`}>
          {t('dashboard.issues_detected', { count: alerts.length })}
        </span>
      </div>

      {alerts.length > 0 && (
        <div className="alert-filter-bar">
          <button
            type="button"
            className={`alert-filter-btn ${filter === 'all' ? 'active' : ''}`}
            onClick={() => setFilter('all')}
          >
            {t('dashboard.filter_all', { count: alerts.length })}
          </button>
          <button
            type="button"
            className={`alert-filter-btn critical ${filter === 'critical' ? 'active' : ''}`}
            onClick={() => setFilter('critical')}
          >
            {t('dashboard.filter_critical', { count: criticalCount })}
          </button>
          <button
            type="button"
            className={`alert-filter-btn warning ${filter === 'warning' ? 'active' : ''}`}
            onClick={() => setFilter('warning')}
          >
            {t('dashboard.filter_warning', { count: warningCount })}
          </button>
        </div>
      )}

      <div className="alerts-body">
        {filteredAlerts.length === 0 ? (
          <div className="empty-state success">
            <CheckCircle2 size={24} className="icon-success" />
            <span>
              {alerts.length === 0
                ? t('dashboard.no_alerts_window')
                : t('dashboard.no_alerts_filter')}
            </span>
          </div>
        ) : (
          <div className="alerts-stream">
            {filteredAlerts.map((a) => (
              <div key={a.id} className={`alert-item ${a.severity}`}>
                <div className="alert-item-header">
                  <div className="alert-item-title">
                    {a.severity === 'critical' && <span className="alert-pulse-dot" />}
                    {getIcon(a.type, a.severity)}
                    <strong>{a.hostname || a.device_id}</strong>
                    {a.site && <span className="site-tag">{a.site}</span>}
                  </div>
                  <div className="alert-item-actions">
                    <span className={`severity-badge ${a.severity}`}>
                      {severityLabel(a.severity)}
                    </span>
                    {onViewDevice && (
                      <button
                        type="button"
                        className="alert-action-btn"
                        onClick={() => onViewDevice(a.device_id)}
                        title={t('dashboard.view_endpoint', { name: a.hostname || a.device_id })}
                        aria-label={t('dashboard.view_endpoint_details', { name: a.hostname || a.device_id })}
                      >
                        <ExternalLink size={12} />
                      </button>
                    )}
                  </div>
                </div>
                <p className="alert-message">{dashboardAlertText(a, t)}</p>
                <div className="alert-footer">
                  <span className="alert-time" title={new Date(a.timestamp).toLocaleString()}>
                    {formatRelativeTime(a.timestamp, t)}
                  </span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
