import React, { useCallback, useEffect, useState } from 'react'
import {
  CheckCircle2,
  Clock,
  RefreshCw,
  Search,
  Shield,
  ShieldAlert,
} from 'lucide-react'
import { DataTable } from '../components/ui/DataTable'
import { api } from '../services/api'
import type { ActivityItem } from '../types/api'

export const AuditPage: React.FC = () => {
  const [logs, setLogs] = useState<ActivityItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [search, setSearch] = useState('')

  const fetchLogs = useCallback(async () => {
    setLoading(true)
    try {
      // The audit trail, not the dashboard activity feed: /api/audit-logs is
      // the full record; the dashboard feed is a 15-item slice of it.
      const data = await api.getAuditLogs()
      setLogs(data.logs || [])
      setError(null)
    } catch (err: unknown) {
      // An empty table plus "no records found" reads as "nothing happened",
      // which is the opposite of what a failed fetch means.
      setError(err instanceof Error ? err.message : 'Failed to load the audit trail')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchLogs()
  }, [fetchLogs])

  const term = search.toLowerCase()
  const filteredLogs = logs.filter((item) => {
    if (!term) return true
    return (
      item.action.toLowerCase().includes(term) ||
      item.actor_id.toLowerCase().includes(term) ||
      (item.target_id && item.target_id.toLowerCase().includes(term))
    )
  })

  return (
    <div className="page-container audit-page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Security &amp; Operations Audit Trail</h1>
          <p className="page-subtitle">
            Append-only log of administrative actions, device lifecycle events, and agent dispatches
          </p>
        </div>
        <div className="header-controls">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={fetchLogs}
            disabled={loading}
          >
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {error && (
        <div className="alert-banner alert-error" role="status">
          <ShieldAlert size={18} />
          <span>{error}</span>
        </div>
      )}

      <div className="filter-bar">
        <div className="search-wrap">
          <Search size={18} className="search-icon" aria-hidden="true" />
          <input
            type="text"
            className="search-input"
            placeholder="Filter the loaded trail by action, operator, or target…"
            aria-label="Filter the loaded audit records"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      <div className="table-card">
        <DataTable label="Audit records">
          <thead>
            <tr>
              <th>Timestamp</th>
              <th>Operator / Actor</th>
              <th>Action</th>
              <th>Target Endpoint / Entity</th>
              <th>Details</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  <div className="table-loader">
                    <RefreshCw size={24} className="spinning" />
                    <span>Reading audit records...</span>
                  </div>
                </td>
              </tr>
            ) : filteredLogs.length === 0 ? (
              <tr>
                <td colSpan={5} className="text-center py-8">
                  <div className="empty-state">
                    <Shield size={32} />
                    <p>
                      {logs.length === 0
                        ? 'No audit trail records found'
                        : 'No records match the current filter'}
                    </p>
                  </div>
                </td>
              </tr>
            ) : (
              filteredLogs.map((item) => (
                <tr key={item.id}>
                  <td data-label="Timestamp">
                    <span className="timestamp-cell">
                      {new Date(item.created_at).toLocaleString()}
                    </span>
                  </td>
                  <td data-label="Operator / Actor">
                    <span className="actor-badge">
                      {item.actor_type}:{item.actor_id}
                    </span>
                  </td>
                  <td data-label="Action">
                    <div className="action-cell">
                      {item.action.includes('enroll') ? (
                        <CheckCircle2 size={14} className="text-success" />
                      ) : item.action.includes('command') ? (
                        <Clock size={14} className="text-primary" />
                      ) : (
                        <ShieldAlert size={14} className="text-warning" />
                      )}
                      <strong>{item.action}</strong>
                    </div>
                  </td>
                  <td data-label="Target Endpoint / Entity">
                    <span className="font-mono text-sm">{item.target_id || '—'}</span>
                  </td>
                  {/* The old "VERIFIED" pill claimed a cryptographic
                      verification the API never returns — the row has no
                      signature field. Showing the server's own detail text is
                      the honest column. */}
                  <td data-label="Details" className="text-sm text-muted">
                    {item.details || '—'}
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
