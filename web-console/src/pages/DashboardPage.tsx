import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  HardDrive,
  Laptop,
  Radio,
  RefreshCw,
  ScrollText,
  ShieldAlert,
  ShieldCheck,
  Terminal,
  Users,
  WifiOff,
  Wrench,
  XCircle,
} from 'lucide-react'
import { AlertsList } from '../components/AlertsList'
import { KPICard } from '../components/KPICard'
import { OSDistribution } from '../components/OSDistribution'
import { SiteDistribution } from '../components/SiteDistribution'
import { useT, type Vars } from '../i18n/core'
import { api } from '../services/api'
import type {
  ActivityItem,
  AlertItem,
  DashboardSummary,
  OSMetric,
  SiteMetric,
} from '../types/api'

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

const getActivityIcon = (action: string) => {
  if (action.includes('enroll')) return <CheckCircle2 size={14} className="text-success" />
  if (action.includes('command') || action.includes('exec')) {
    return <Terminal size={14} className="text-primary" />
  }
  if (action.includes('patch') || action.includes('update')) {
    return <ShieldCheck size={14} className="text-info" />
  }
  if (action.includes('hw_changed')) return <Wrench size={14} className="text-warning" />
  return <Activity size={14} className="text-muted" />
}

export const DashboardPage: React.FC = () => {
  const navigate = useNavigate()
  const { t } = useT()
  const [summary, setSummary] = useState<DashboardSummary | null>(null)
  const [sites, setSites] = useState<SiteMetric[]>([])
  const [osMetrics, setOsMetrics] = useState<OSMetric[]>([])
  const [alerts, setAlerts] = useState<AlertItem[]>([])
  // Whether the alert list has loaded at least once. An empty list that never
  // arrived is not "no alerts", and the card must not say Normal for it.
  const [alertsKnown, setAlertsKnown] = useState(false)
  const [activity, setActivity] = useState<ActivityItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(true)
  // null until a poll has actually succeeded. A failed poll must not advance
  // this: the timestamp answers "when did we last have good data", and moving
  // it on an error is how a stale dashboard keeps claiming LIVE.
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  // Dictionary keys of the sections that failed to refresh; translated at render.
  const [error, setError] = useState<string[] | null>(null)
  // Stops an in-flight poll from writing over a newer one.
  const requestSeq = useRef(0)

  const loadData = useCallback(async (isSilent = false) => {
    if (!isSilent) setRefreshing(true)
    const seq = ++requestSeq.current
    try {
      // allSettled, not a chain of per-call .catch(() => fallback): a swallowed
      // failure used to replace live data with an empty array and then stamp
      // the result as a successful update.
      const [sumRes, siteRes, osRes, alertRes, actRes] = await Promise.allSettled([
        api.getDashboardSummary(),
        api.getDashboardSites(),
        api.getDashboardOS(),
        api.getDashboardAlerts(),
        api.getDashboardActivity(),
      ])
      if (seq !== requestSeq.current) return

      const failures: string[] = []
      if (sumRes.status === 'fulfilled') setSummary(sumRes.value)
      else failures.push('dashboard.section_summary')
      if (siteRes.status === 'fulfilled') setSites(siteRes.value)
      else failures.push('dashboard.section_sites')
      if (osRes.status === 'fulfilled') setOsMetrics(osRes.value)
      else failures.push('dashboard.section_os')
      if (alertRes.status === 'fulfilled') {
        setAlerts(alertRes.value)
        setAlertsKnown(true)
      } else failures.push('dashboard.section_alerts')
      if (actRes.status === 'fulfilled') setActivity(actRes.value)
      else failures.push('dashboard.section_activity')

      if (failures.length === 0) {
        setError(null)
        setLastUpdated(new Date())
      } else {
        // Partial failure: whatever did load stays on screen, and the banner
        // names which sections are now stale instead of claiming all-clear.
        setError(failures)
      }
    } finally {
      if (seq === requestSeq.current) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [])

  useEffect(() => {
    loadData(false)
  }, [loadData])

  useEffect(() => {
    if (!autoRefresh) return
    const interval = setInterval(() => {
      // A hidden tab has nobody reading it and gets its timers throttled
      // anyway; polling there just burns requests.
      if (document.visibilityState === 'hidden') return
      loadData(true)
    }, 15000)
    return () => clearInterval(interval)
  }, [autoRefresh, loadData])

  // known is false until the summary has loaded at least once. Every card
  // driven by it then shows a dash and an unknown state instead of a zero: a
  // zero from a request that failed used to read as "0 offline - Optimal" and
  // "0 low disk - Healthy" while the console knew nothing at all.
  const known = summary !== null
  const unknown = '—'
  const total = summary?.total_devices ?? 0
  const online = summary?.online_devices ?? 0
  const offline = summary?.offline_devices ?? 0
  const onlinePct = summary?.online_pct ?? (total > 0 ? Math.round((online / total) * 100) : 0)
  const lowDisk = summary?.low_disk_alerts ?? 0
  const hwChanges = summary?.recent_hw_changes_24h ?? 0
  const siteCount = summary?.sites_count ?? 0

  // The server does the filtering, so each card links to a list that is
  // already narrowed instead of dumping the operator on the whole fleet.
  const goDevices = (query = '') => navigate(`/devices${query}`)

  return (
    <div className="page-container dashboard-page">
      <div className="page-header">
        <div>
          <h1 className="page-title">{t('dashboard.title')}</h1>
          <p className="page-subtitle">
            {t('dashboard.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <label className="toggle-label">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            <span>{t('dashboard.auto_refresh')}</span>
          </label>
          {/* Freshness stated honestly: no timestamp at all until a poll has
              actually returned, and no LIVE badge anywhere. */}
          <span className={`last-sync${error ? ' stale' : ''}`}>
            {lastUpdated
              ? t('dashboard.updated_at', { time: lastUpdated.toLocaleTimeString() })
              : loading
                ? t('dashboard.connecting')
                : t('dashboard.not_yet_updated')}
          </span>
          <button
            type="button"
            className="btn btn-secondary btn-icon"
            onClick={() => loadData(false)}
            disabled={refreshing}
            title={t('dashboard.refresh_telemetry')}
          >
            <RefreshCw size={16} className={refreshing ? 'spinning' : ''} />
            <span>{refreshing ? t('dashboard.refreshing') : t('dashboard.refresh')}</span>
          </button>
        </div>
      </div>

      {error && (
        <div className="alert-banner alert-error" role="status">
          <XCircle size={18} />
          <span>{t('dashboard.could_not_refresh', { sections: error.map((key) => t(key)).join(', ') })}</span>
        </div>
      )}

      {loading ? (
        <div className="page-loader">
          <RefreshCw size={32} className="spinning" />
          <span>{t('dashboard.aggregating')}</span>
        </div>
      ) : (
        <>
          {/* Row 1 — Fleet Scale & Reach */}
          <div className="kpi-grid">
            <KPICard
              title={t('dashboard.kpi_total')}
              value={known ? total : unknown}
              subtitle={known ? t('dashboard.connected', { count: online }) : t('dashboard.summary_unavailable')}
              icon={<Laptop size={22} />}
              variant={known ? 'primary' : 'unknown'}
              badge={known ? t('dashboard.badge_fleet_scale') : t('common.unknown')}
              onClick={() => goDevices()}
            />
            <KPICard
              title={t('dashboard.kpi_online_ratio')}
              value={known && total > 0 ? `${onlinePct}%` : unknown}
              subtitle={!known ? t('dashboard.summary_unavailable') : total === 0 ? t('dashboard.no_devices_enrolled') : t('dashboard.endpoints_offline', { count: offline })}
              icon={<Radio size={22} />}
              // With no devices there is no ratio: 0% read as "Degraded" on a
              // fresh install that had nothing wrong with it.
              variant={!known ? 'unknown' : total === 0 ? 'neutral' : onlinePct >= 80 ? 'success' : onlinePct >= 50 ? 'warning' : 'danger'}
              badge={!known ? t('common.unknown') : total === 0 ? t('dashboard.badge_no_devices') : onlinePct >= 80 ? t('dashboard.badge_healthy') : t('dashboard.badge_degraded')}
              progress={known && total > 0 ? { current: online, total } : undefined}
              onClick={() => goDevices('?status=online')}
            />
            <KPICard
              title={t('dashboard.kpi_active_sites')}
              value={known ? siteCount : unknown}
              subtitle={!known ? t('dashboard.summary_unavailable') : siteCount > 0 ? t('dashboard.branches_reporting') : t('dashboard.no_site_assigned')}
              icon={<Users size={22} />}
              variant={known ? 'neutral' : 'unknown'}
              badge={known ? t('dashboard.badge_coverage') : t('common.unknown')}
              onClick={() => goDevices()}
            />
            <KPICard
              title={t('dashboard.kpi_retired')}
              value={known ? (summary.retired_devices ?? 0) : unknown}
              subtitle={t('dashboard.excluded_from_telemetry')}
              icon={<Wrench size={22} />}
              variant={known ? 'neutral' : 'unknown'}
              badge={known ? t('dashboard.badge_lifecycle') : t('common.unknown')}
              onClick={() => goDevices('?status=retired')}
            />
          </div>

          {/* Row 2 — Operational Health & Issues */}
          <div className="kpi-grid">
            <KPICard
              title={t('dashboard.kpi_offline')}
              value={known ? offline : unknown}
              subtitle={t('dashboard.pending_reconnection')}
              icon={<WifiOff size={22} />}
              variant={!known ? 'unknown' : offline > 0 ? 'warning' : 'neutral'}
              badge={!known ? t('common.unknown') : offline > 0 ? t('dashboard.badge_attention') : t('dashboard.badge_optimal')}
              onClick={() => goDevices('?status=offline')}
            />
            <KPICard
              title={t('dashboard.kpi_low_disk')}
              value={known ? lowDisk : unknown}
              subtitle={t('dashboard.below_free_space')}
              icon={<HardDrive size={22} />}
              variant={!known ? 'unknown' : lowDisk > 0 ? 'danger' : 'success'}
              badge={!known ? t('common.unknown') : lowDisk > 0 ? t('dashboard.badge_storage_risk') : t('dashboard.badge_healthy')}
              // Deliberately not a filter link. /api/devices accepts only
              // status and site, and neither expresses free space, so a
              // ?disk=low link would be ignored server-side and land on the
              // unfiltered list while claiming to be filtered. The card is
              // honest about not narrowing; the count is real either way.
            />
            <KPICard
              title={t('dashboard.kpi_hw_changes')}
              value={known ? hwChanges : unknown}
              subtitle={t('dashboard.hw_replacements')}
              icon={<Wrench size={22} />}
              variant={!known ? 'unknown' : hwChanges > 0 ? 'warning' : 'neutral'}
              badge={!known ? t('common.unknown') : hwChanges > 0 ? t('dashboard.badge_audit_event') : t('dashboard.badge_stable')}
            />
            <KPICard
              title={t('dashboard.kpi_health_alerts')}
              value={alertsKnown ? alerts.length : unknown}
              subtitle={t('dashboard.health_alerts_desc')}
              icon={<AlertTriangle size={22} />}
              variant={!alertsKnown ? 'unknown' : alerts.length > 0 ? 'warning' : 'success'}
              badge={!alertsKnown ? t('common.unknown') : alerts.length > 0 ? t('dashboard.badge_action_needed') : t('dashboard.badge_normal')}
              onClick={() => navigate('/alerts')}
            />
          </div>

          {/* Quick links launchpad */}
          <div className="quick-action-grid">
            <button type="button" className="quick-action-tile" onClick={() => goDevices()}>
              <Laptop size={18} />
              <div>
                <strong>{t('dashboard.quick_inventory')}</strong>
                <span>{t('dashboard.quick_inventory_desc')}</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/alerts')}>
              <AlertTriangle size={18} />
              <div>
                <strong>{t('dashboard.quick_incidents')}</strong>
                <span>{t('dashboard.quick_incidents_desc')}</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/patches')}>
              <ShieldAlert size={18} />
              <div>
                <strong>{t('dashboard.quick_patches')}</strong>
                <span>{t('dashboard.quick_patches_desc')}</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/log')}>
              <ScrollText size={18} />
              <div>
                <strong>{t('dashboard.quick_log')}</strong>
                <span>{t('dashboard.quick_log_desc')}</span>
              </div>
              <ArrowRight size={16} />
            </button>
          </div>

          {/* Mid Section: Alerts & Site Distribution */}
          <div className="dash-row two-col">
            {/* device_id, not hostname: the Devices page opens the device by
                id, and a hostname substring match lands on the wrong machine
                or on nothing. */}
            <AlertsList
              alerts={alerts}
              onViewDevice={(id) => goDevices(`?device_id=${encodeURIComponent(id)}`)}
            />
            <SiteDistribution
              sites={sites}
              onSelectSite={(site) => goDevices(`?site=${encodeURIComponent(site)}`)}
            />
          </div>

          {/* Lower Section: OS Distribution & Recent Activity Feed */}
          <div className="dash-row two-col">
            <OSDistribution metrics={osMetrics} />

            <div className="dash-card">
              <div className="card-header">
                <div className="card-title-group">
                  <Activity size={18} className="card-icon" />
                  <h3 className="card-title">{t('dashboard.activity_title')}</h3>
                </div>
                <span className="card-badge">{t('dashboard.events_count', { count: activity.length })}</span>
              </div>
              <div className="activity-body">
                {activity.length === 0 ? (
                  <div className="empty-state">{t('dashboard.no_activity')}</div>
                ) : (
                  <div className="activity-timeline">
                    {activity.map((item) => (
                      <div key={item.id} className="timeline-node">
                        <div className="timeline-marker">{getActivityIcon(item.action)}</div>
                        <div className="timeline-content">
                          <div className="timeline-header">
                            <span className="timeline-actor">
                              {item.actor_type}:{item.actor_id}
                            </span>
                            <span className="timeline-action">{item.action}</span>
                            {item.target_id && (
                              <span className="timeline-target">{item.target_id}</span>
                            )}
                          </div>
                          <span
                            className="timeline-time"
                            title={new Date(item.created_at).toLocaleString()}
                          >
                            {formatRelativeTime(item.created_at, t)}
                          </span>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
