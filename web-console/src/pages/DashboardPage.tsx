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
import { api } from '../services/api'
import type {
  ActivityItem,
  AlertItem,
  DashboardSummary,
  OSMetric,
  SiteMetric,
} from '../types/api'

function formatRelativeTime(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diffSec = Math.floor((now.getTime() - date.getTime()) / 1000)

  if (diffSec < 60) return 'Just now'
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHour = Math.floor(diffMin / 60)
  if (diffHour < 24) return `${diffHour}h ago`
  const diffDay = Math.floor(diffHour / 24)
  return `${diffDay}d ago`
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
  const [summary, setSummary] = useState<DashboardSummary | null>(null)
  const [sites, setSites] = useState<SiteMetric[]>([])
  const [osMetrics, setOsMetrics] = useState<OSMetric[]>([])
  const [alerts, setAlerts] = useState<AlertItem[]>([])
  const [activity, setActivity] = useState<ActivityItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(true)
  // null until a poll has actually succeeded. A failed poll must not advance
  // this: the timestamp answers "when did we last have good data", and moving
  // it on an error is how a stale dashboard keeps claiming LIVE.
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [error, setError] = useState<string | null>(null)
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
      else failures.push('summary')
      if (siteRes.status === 'fulfilled') setSites(siteRes.value)
      else failures.push('sites')
      if (osRes.status === 'fulfilled') setOsMetrics(osRes.value)
      else failures.push('OS breakdown')
      if (alertRes.status === 'fulfilled') setAlerts(alertRes.value)
      else failures.push('alerts')
      if (actRes.status === 'fulfilled') setActivity(actRes.value)
      else failures.push('activity')

      if (failures.length === 0) {
        setError(null)
        setLastUpdated(new Date())
      } else {
        // Partial failure: whatever did load stays on screen, and the banner
        // names which sections are now stale instead of claiming all-clear.
        setError(`Could not refresh ${failures.join(', ')}. Showing last known values.`)
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
          <h1 className="page-title">Fleet Telemetry Dashboard</h1>
          <p className="page-subtitle">
            Real-time health, compliance, and distribution across all corporate sites
          </p>
        </div>
        <div className="header-controls">
          <label className="toggle-label">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            <span>Auto-refresh (15s)</span>
          </label>
          {/* Freshness stated honestly: no timestamp at all until a poll has
              actually returned, and no LIVE badge anywhere. */}
          <span className={`last-sync${error ? ' stale' : ''}`}>
            {lastUpdated
              ? `Updated ${lastUpdated.toLocaleTimeString()}`
              : loading
                ? 'Connecting…'
                : 'Not yet updated'}
          </span>
          <button
            type="button"
            className="btn btn-secondary btn-icon"
            onClick={() => loadData(false)}
            disabled={refreshing}
            title="Refresh telemetry"
          >
            <RefreshCw size={16} className={refreshing ? 'spinning' : ''} />
            <span>{refreshing ? 'Refreshing...' : 'Refresh'}</span>
          </button>
        </div>
      </div>

      {error && (
        <div className="alert-banner alert-error" role="status">
          <XCircle size={18} />
          <span>{error}</span>
        </div>
      )}

      {loading ? (
        <div className="page-loader">
          <RefreshCw size={32} className="spinning" />
          <span>Aggregating fleet telemetry...</span>
        </div>
      ) : (
        <>
          {/* Row 1 — Fleet Scale & Reach */}
          <div className="kpi-grid">
            <KPICard
              title="TOTAL MANAGED ENDPOINTS"
              value={total}
              subtitle={`${online} currently connected`}
              icon={<Laptop size={22} />}
              variant="primary"
              badge="Fleet Scale"
              onClick={() => goDevices()}
            />
            <KPICard
              title="FLEET ONLINE RATIO"
              value={`${onlinePct}%`}
              subtitle={`${offline} endpoints offline`}
              icon={<Radio size={22} />}
              variant={onlinePct >= 80 ? 'success' : onlinePct >= 50 ? 'warning' : 'danger'}
              badge={onlinePct >= 80 ? 'Healthy' : 'Degraded'}
              progress={{ current: online, total }}
              onClick={() => goDevices('?status=online')}
            />
            <KPICard
              title="ACTIVE SITES"
              value={siteCount}
              subtitle={siteCount > 0 ? 'Branches reporting telemetry' : 'No site assigned yet'}
              icon={<Users size={22} />}
              variant="neutral"
              badge="Coverage"
              onClick={() => goDevices()}
            />
            <KPICard
              title="RETIRED / DECOMMISSIONED"
              value={summary?.retired_devices ?? 0}
              subtitle="Excluded from telemetry"
              icon={<Wrench size={22} />}
              variant="neutral"
              badge="Lifecycle"
              onClick={() => goDevices('?status=retired')}
            />
          </div>

          {/* Row 2 — Operational Health & Issues */}
          <div className="kpi-grid">
            <KPICard
              title="OFFLINE DEVICES"
              value={offline}
              subtitle="Pending reconnection"
              icon={<WifiOff size={22} />}
              variant={offline > 0 ? 'warning' : 'neutral'}
              badge={offline > 0 ? 'Attention' : 'Optimal'}
              onClick={() => goDevices('?status=offline')}
            />
            <KPICard
              title="LOW DISK ENDPOINTS"
              value={lowDisk}
              subtitle="Below free-space threshold"
              icon={<HardDrive size={22} />}
              variant={lowDisk > 0 ? 'danger' : 'success'}
              badge={lowDisk > 0 ? 'Storage Risk' : 'Healthy'}
              // Deliberately not a filter link. /api/devices accepts only
              // status and site, and neither expresses free space, so a
              // ?disk=low link would be ignored server-side and land on the
              // unfiltered list while claiming to be filtered. The card is
              // honest about not narrowing; the count is real either way.
            />
            <KPICard
              title="HARDWARE CHANGES (24H)"
              value={hwChanges}
              subtitle="RAM / disk / CPU replacements"
              icon={<Wrench size={22} />}
              variant={hwChanges > 0 ? 'warning' : 'neutral'}
              badge={hwChanges > 0 ? 'Audit Event' : 'Stable'}
            />
            <KPICard
              title="ACTIVE HEALTH ALERTS"
              value={alerts.length}
              subtitle="Low disk and prolonged-offline conditions"
              icon={<AlertTriangle size={22} />}
              variant={alerts.length > 0 ? 'warning' : 'success'}
              badge={alerts.length > 0 ? 'Action Needed' : 'Normal'}
              onClick={() => navigate('/alerts')}
            />
          </div>

          {/* Quick links launchpad */}
          <div className="quick-action-grid">
            <button type="button" className="quick-action-tile" onClick={() => goDevices()}>
              <Laptop size={18} />
              <div>
                <strong>Endpoint Inventory</strong>
                <span>Hardware, storage, installed software</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/alerts')}>
              <AlertTriangle size={18} />
              <div>
                <strong>Alert Incidents</strong>
                <span>Acknowledge and resolve anomalies</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/patches')}>
              <ShieldAlert size={18} />
              <div>
                <strong>Patch Compliance</strong>
                <span>Missing and critical security updates</span>
              </div>
              <ArrowRight size={16} />
            </button>
            <button type="button" className="quick-action-tile" onClick={() => navigate('/log')}>
              <ScrollText size={18} />
              <div>
                <strong>Server Log</strong>
                <span>Live tail when something misbehaves</span>
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
                  <h3 className="card-title">Recent Fleet Activity & Audit</h3>
                </div>
                <span className="card-badge">{activity.length} Events</span>
              </div>
              <div className="activity-body">
                {activity.length === 0 ? (
                  <div className="empty-state">No recent activity recorded</div>
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
                            {formatRelativeTime(item.created_at)}
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
