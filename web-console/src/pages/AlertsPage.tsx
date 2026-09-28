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
import type { AlertIncidentDTO, AlertRuleDTO } from '../types/api'

const STATUS_FILTERS = [
  { value: 'open', label: 'Open' },
  { value: 'acknowledged', label: 'Acknowledged' },
  { value: 'resolved', label: 'Resolved' },
  { value: '', label: 'All' },
] as const

export const AlertsPage: React.FC = () => {
  const navigate = useNavigate()
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
        text: err instanceof Error ? err.message : 'Failed to load alerts',
      })
    } finally {
      setLoading(false)
    }
  }, [statusFilter])

  useEffect(() => {
    loadData()
  }, [loadData])

  const labelFor = (i: AlertIncidentDTO) => i.hostname || i.device_id.slice(0, 10) || 'incident'

  const handleAcknowledge = async (id: string) => {
    const target = incidents.find((i) => i.id === id)
    const label = target ? labelFor(target) : 'incident'
    try {
      await api.acknowledgeIncident(id)
      const successText = `Incident on '${label}' acknowledged and assigned for investigation.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Incident Acknowledged')
      loadData()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to acknowledge incident'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Acknowledge Failed')
    }
  }

  const handleResolve = async (id: string) => {
    const target = incidents.find((i) => i.id === id)
    const label = target ? labelFor(target) : 'incident'
    try {
      await api.resolveIncident(id)
      const successText = `Incident on '${label}' marked as resolved.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Incident Resolved')
      loadData()
    } catch (err: unknown) {
      const errorText = err instanceof Error ? err.message : 'Failed to resolve incident'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Resolve Failed')
    }
  }

  const openCount = counts.open
  const ackCount = counts.acknowledged

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Alerting &amp; Incident Management</h2>
          <p className="page-subtitle">
            Automated fleet anomaly detection, deduplicated incident tracking, and webhook
            notification triggers
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
            <span>Refresh</span>
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
            aria-label="Dismiss notification"
          >
            <X size={14} />
          </button>
        </div>
      )}

      {/* KPI Overview */}
      <div className="kpi-grid">
        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Open Incidents</span>
            <AlertCircle className="kpi-icon text-danger" size={20} />
          </div>
          <div className="kpi-value text-danger">{openCount}</div>
          <span className="kpi-hint">Requiring operator attention</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Acknowledged</span>
            <AlertTriangle className="kpi-icon text-warning" size={20} />
          </div>
          <div className="kpi-value text-warning">{ackCount}</div>
          <span className="kpi-hint">Under investigation by technician</span>
        </div>

        <div className="kpi-card">
          <div className="kpi-header">
            <span className="kpi-label">Active Detection Rules</span>
            <Bell className="kpi-icon text-primary" size={20} />
          </div>
          <div className="kpi-value">{rules.length}</div>
          <span className="kpi-hint">Automated background evaluators</span>
        </div>
      </div>

      {/* Incidents Card */}
      <div className="table-card">
        <div className="table-toolbar">
          <div className="flex items-center gap-2">
            <Filter size={16} className="text-dim" aria-hidden="true" />
            <span className="text-sm font-semibold text-muted">Filter by Status:</span>
            <div className="btn-group">
              {STATUS_FILTERS.map((f) => (
                <button
                  key={f.label}
                  type="button"
                  className={`btn btn-sm ${statusFilter === f.value ? 'btn-primary' : 'btn-secondary'}`}
                  onClick={() => setStatusFilter(f.value)}
                  aria-pressed={statusFilter === f.value}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>
        </div>

        <DataTable label="Alert incidents">
          <thead>
            <tr>
              <th>Severity</th>
              <th>Device</th>
              <th>Detection Rule</th>
              <th>Alert Message</th>
              <th>Status</th>
              <th>Last Triggered</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} className="text-center py-8 text-muted">
                  Loading incidents…
                </td>
              </tr>
            ) : incidents.length === 0 ? (
              <tr>
                <td colSpan={7} className="text-center py-8 text-muted">
                  No incidents found matching status filter '{statusFilter || 'all'}'.
                </td>
              </tr>
            ) : (
              incidents.map((i) => (
                <tr key={i.id}>
                  <td data-label="Severity">
                    <span className={`badge-severity ${i.severity}`}>{i.severity.toUpperCase()}</span>
                  </td>
                  <td data-label="Device">
                    {i.device_id ? (
                      // Stable device_id, so this opens the right machine even
                      // when two endpoints share a hostname.
                      <button
                        type="button"
                        className="btn btn-sm btn-secondary"
                        onClick={() =>
                          navigate(`/devices?device_id=${encodeURIComponent(i.device_id)}`)
                        }
                        title="View endpoint details"
                      >
                        {i.hostname || i.device_id.slice(0, 10)}
                      </button>
                    ) : (
                      <span className="font-semibold text-main">
                        {i.hostname || i.device_id.slice(0, 10)}
                      </span>
                    )}
                  </td>
                  <td data-label="Detection Rule">{i.rule_name || i.rule_id}</td>
                  <td data-label="Alert Message">{i.title || i.message}</td>
                  <td data-label="Status">
                    <span
                      className={`status-pill ${
                        i.status === 'open'
                          ? 'danger'
                          : i.status === 'acknowledged'
                            ? 'warning'
                            : 'online'
                      }`}
                    >
                      {i.status}
                    </span>
                  </td>
                  <td data-label="Last Triggered" className="text-sm text-muted">
                    {new Date(i.last_triggered_at).toLocaleString()}
                  </td>
                  <td data-label="Actions">
                    <div className="action-buttons">
                      {canAct && i.status === 'open' && (
                        <button
                          type="button"
                          className="btn-action text-warning"
                          onClick={() => handleAcknowledge(i.id)}
                          title="Acknowledge incident"
                        >
                          <AlertTriangle size={14} />
                          <span>Ack</span>
                        </button>
                      )}
                      {canAct && i.status !== 'resolved' && (
                        <button
                          type="button"
                          className="btn-action text-success"
                          onClick={() => handleResolve(i.id)}
                          title="Resolve incident"
                        >
                          <CheckCircle2 size={14} />
                          <span>Resolve</span>
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
