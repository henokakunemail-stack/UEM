import React, { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ShieldCheck,
  RefreshCw,
  Download,
  CheckCircle2,
  AlertTriangle,
  XCircle,
  Search,
  ChevronDown,
  ChevronRight,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import type { SecurityAuditItem, SecurityAuditReport } from '../types/api'

type TabFilter = 'all' | 'part1' | 'part2' | 'pass' | 'warn' | 'fail'

export const SecurityPosturePage: React.FC = () => {
  const { can } = usePermission()
  const canAdmin = can('admin')
  const toast = useToast()

  const [report, setReport] = useState<SecurityAuditReport | null>(null)
  const [loading, setLoading] = useState<boolean>(true)
  const [running, setRunning] = useState<boolean>(false)
  const [activeTab, setActiveTab] = useState<TabFilter>('all')
  const [searchQuery, setSearchQuery] = useState<string>('')
  const [expandedIds, setExpandedIds] = useState<Set<number>>(new Set())

  const loadAudit = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getSecurityAudit()
      setReport(res)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to load security audit'
      toast.error(msg, 'Security Audit')
    } finally {
      setLoading(false)
    }
  }, [toast])

  const handleRunAudit = async () => {
    if (!canAdmin) {
      toast.error('Running live security audit requires Admin role', 'Permission Denied')
      return
    }
    setRunning(true)
    try {
      const res = await api.runSecurityAudit()
      setReport(res)
      toast.success(
        `Security audit completed. Score: ${res.score}% (Grade ${res.grade})`,
        'Audit Completed'
      )
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to execute security audit'
      toast.error(msg, 'Audit Execution Failed')
    } finally {
      setRunning(false)
    }
  }

  const handleExportJSON = () => {
    if (!report) return
    const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `uem-security-audit-report-${new Date().toISOString().slice(0, 10)}.json`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
    toast.success('Security audit report exported successfully', 'Export')
  }

  const toggleExpand = (id: number) => {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const expandAll = () => {
    if (!report) return
    setExpandedIds(new Set(report.checks.map((c) => c.id)))
  }

  const collapseAll = () => {
    setExpandedIds(new Set())
  }

  useEffect(() => {
    loadAudit()
  }, [loadAudit])

  const filteredChecks = useMemo(() => {
    if (!report || !report.checks) return []
    return report.checks.filter((check) => {
      // Tab filter
      if (activeTab === 'part1' && check.part !== 1) return false
      if (activeTab === 'part2' && check.part !== 2) return false
      if (activeTab === 'pass' && check.status !== 'pass') return false
      if (activeTab === 'warn' && check.status !== 'warn') return false
      if (activeTab === 'fail' && check.status !== 'fail') return false

      // Search query
      if (!searchQuery) return true
      const q = searchQuery.toLowerCase()
      return (
        check.title.toLowerCase().includes(q) ||
        check.description.toLowerCase().includes(q) ||
        check.evidence.toLowerCase().includes(q) ||
        check.remediation.toLowerCase().includes(q) ||
        check.owasp_tag.toLowerCase().includes(q) ||
        check.category.toLowerCase().includes(q)
      )
    })
  }, [report, activeTab, searchQuery])

  const getGradeColor = (grade: string) => {
    switch (grade) {
      case 'A+':
      case 'A':
        return '#10b981' // Green
      case 'B':
        return '#3b82f6' // Blue
      case 'C':
        return '#f59e0b' // Amber
      default:
        return '#ef4444' // Red
    }
  }

  const getCategoryBadge = (cat: SecurityAuditItem['category']) => {
    switch (cat) {
      case 'core_app':
        return { label: 'Core App', color: 'bg-blue-500/10 text-blue-500 border-blue-500/30' }
      case 'data_security':
        return { label: 'Data Security', color: 'bg-emerald-500/10 text-emerald-500 border-emerald-500/30' }
      case 'network':
        return { label: 'Network', color: 'bg-purple-500/10 text-purple-500 border-purple-500/30' }
      case 'pipeline':
        return { label: 'Pipeline', color: 'bg-cyan-500/10 text-cyan-500 border-cyan-500/30' }
      case 'hardening':
        return { label: 'Hardening', color: 'bg-amber-500/10 text-amber-500 border-amber-500/30' }
      default:
        return { label: cat, color: 'bg-slate-500/10 text-slate-500 border-slate-500/30' }
    }
  }

  return (
    <div className="page-container">
      {/* Header */}
      <div className="page-header">
        <div>
          <h1 className="page-title flex items-center gap-2">
            <ShieldCheck size={26} className="text-emerald-500" />
            Pre-Launch Security Posture
          </h1>
          <p className="page-subtitle">
            Automated 40-point verification engine covering routes, pipelines, architecture, structure, database, and workflows
          </p>
        </div>
        <div className="header-controls flex items-center gap-2">
          <button
            type="button"
            className="btn btn-secondary flex items-center gap-2"
            onClick={handleExportJSON}
            disabled={!report || loading}
            title="Download full security report JSON"
          >
            <Download size={16} />
            <span>Export JSON</span>
          </button>
          <button
            type="button"
            className="btn btn-primary flex items-center gap-2"
            onClick={handleRunAudit}
            disabled={running || loading || !canAdmin}
            title={canAdmin ? 'Execute live automated verification' : 'Requires Administrator role'}
          >
            <RefreshCw size={16} className={running ? 'spinning' : ''} />
            <span>{running ? 'Evaluating...' : 'Run Live Audit'}</span>
          </button>
        </div>
      </div>

      {/* Scorecard Hero */}
      {report && (
        <div
          className="table-card"
          style={{
            background: 'var(--bg-card, #1e293b)',
            border: '1px solid var(--border-color, #334155)',
            borderRadius: '0.75rem',
            padding: '1.5rem',
            marginBottom: '1.5rem',
          }}
        >
          <div className="grid grid-cols-1 md:grid-cols-5 gap-6 items-center">
            {/* Grade & Score Circle */}
            <div className="md:col-span-2 flex items-center gap-6 border-b md:border-b-0 md:border-r border-slate-700/50 pb-4 md:pb-0 pr-0 md:pr-6">
              <div
                style={{
                  width: '90px',
                  height: '90px',
                  borderRadius: '50%',
                  border: `4px solid ${getGradeColor(report.grade)}`,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                  background: 'rgba(0,0,0,0.2)',
                  flexShrink: 0,
                }}
              >
                <span style={{ fontSize: '1.75rem', fontWeight: 800, color: getGradeColor(report.grade), lineHeight: 1 }}>
                  {report.grade}
                </span>
                <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary, #94a3b8)', marginTop: '2px' }}>
                  {report.score}%
                </span>
              </div>
              <div>
                <h3 style={{ fontSize: '1.125rem', fontWeight: 700, margin: 0 }}>
                  Security Readiness Score
                </h3>
                <p style={{ fontSize: '0.8125rem', color: 'var(--text-secondary, #94a3b8)', margin: '0.25rem 0 0.5rem 0' }}>
                  {report.score >= 90
                    ? 'Excellent posture. System meets enterprise launch requirements.'
                    : report.score >= 75
                    ? 'Adequate posture. Review warnings before production release.'
                    : 'Hardening required. Address failed items before launch.'}
                </p>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted, #64748b)' }}>
                  Evaluated at: {new Date(report.generated_at).toLocaleString()}
                </div>
              </div>
            </div>

            {/* Metrics Counters */}
            <div className="md:col-span-3 grid grid-cols-3 gap-4">
              <div
                style={{
                  background: 'rgba(16, 185, 129, 0.08)',
                  border: '1px solid rgba(16, 185, 129, 0.25)',
                  borderRadius: '0.5rem',
                  padding: '1rem',
                  textAlign: 'center',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.375rem', color: '#10b981' }}>
                  <CheckCircle2 size={18} />
                  <span style={{ fontWeight: 600, fontSize: '0.875rem' }}>Passed</span>
                </div>
                <div style={{ fontSize: '1.75rem', fontWeight: 800, color: '#10b981', marginTop: '0.25rem' }}>
                  {report.passed}
                </div>
                <div style={{ fontSize: '0.6875rem', color: 'var(--text-secondary, #94a3b8)' }}>
                  of {report.total_checks} criteria
                </div>
              </div>

              <div
                style={{
                  background: 'rgba(245, 158, 11, 0.08)',
                  border: '1px solid rgba(245, 158, 11, 0.25)',
                  borderRadius: '0.5rem',
                  padding: '1rem',
                  textAlign: 'center',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.375rem', color: '#f59e0b' }}>
                  <AlertTriangle size={18} />
                  <span style={{ fontWeight: 600, fontSize: '0.875rem' }}>Warnings</span>
                </div>
                <div style={{ fontSize: '1.75rem', fontWeight: 800, color: '#f59e0b', marginTop: '0.25rem' }}>
                  {report.warned}
                </div>
                <div style={{ fontSize: '0.6875rem', color: 'var(--text-secondary, #94a3b8)' }}>
                  recommendations
                </div>
              </div>

              <div
                style={{
                  background: 'rgba(239, 68, 68, 0.08)',
                  border: '1px solid rgba(239, 68, 68, 0.25)',
                  borderRadius: '0.5rem',
                  padding: '1rem',
                  textAlign: 'center',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.375rem', color: '#ef4444' }}>
                  <XCircle size={18} />
                  <span style={{ fontWeight: 600, fontSize: '0.875rem' }}>Failures</span>
                </div>
                <div style={{ fontSize: '1.75rem', fontWeight: 800, color: '#ef4444', marginTop: '0.25rem' }}>
                  {report.failed}
                </div>
                <div style={{ fontSize: '0.6875rem', color: 'var(--text-secondary, #94a3b8)' }}>
                  blocking items
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Filter Toolbar */}
      <div
        className="table-card"
        style={{
          background: 'var(--bg-card, #1e293b)',
          border: '1px solid var(--border-color, #334155)',
          borderRadius: '0.75rem',
          padding: '0.875rem 1.25rem',
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: '1rem',
          marginBottom: '1rem',
        }}
      >
        {/* Navigation Tabs */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
          <button
            type="button"
            className={`btn btn-sm ${activeTab === 'all' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setActiveTab('all')}
          >
            All 40 Checks
          </button>
          <button
            type="button"
            className={`btn btn-sm ${activeTab === 'part1' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setActiveTab('part1')}
          >
            Part 1: Core App & Data (20)
          </button>
          <button
            type="button"
            className={`btn btn-sm ${activeTab === 'part2' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setActiveTab('part2')}
          >
            Part 2: Network & Hardening (20)
          </button>
          <button
            type="button"
            className={`btn btn-sm ${activeTab === 'warn' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setActiveTab('warn')}
          >
            Warnings ({report?.warned ?? 0})
          </button>
          <button
            type="button"
            className={`btn btn-sm ${activeTab === 'fail' ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setActiveTab('fail')}
          >
            Failures ({report?.failed ?? 0})
          </button>
        </div>

        {/* Search & Collapse Controls */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <div style={{ position: 'relative', minWidth: '240px' }}>
            <Search
              size={14}
              style={{ position: 'absolute', left: '0.75rem', top: '50%', transform: 'translateY(-50%)', color: '#94a3b8' }}
            />
            <input
              type="text"
              placeholder="Search criteria, OWASP, evidence..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="search-input"
              style={{ paddingLeft: '2.25rem', width: '100%' }}
            />
          </div>
          <button type="button" className="btn btn-sm btn-secondary" onClick={expandAll}>
            Expand All
          </button>
          <button type="button" className="btn btn-sm btn-secondary" onClick={collapseAll}>
            Collapse All
          </button>
        </div>
      </div>

      {/* Checks List */}
      {loading ? (
        <div style={{ textAlign: 'center', padding: '4rem 0', color: 'var(--text-secondary, #94a3b8)' }}>
          <RefreshCw size={28} className="spinning mx-auto mb-2" />
          <p>Running automated security posture checks...</p>
        </div>
      ) : filteredChecks.length === 0 ? (
        <div
          className="table-card"
          style={{
            background: 'var(--bg-card, #1e293b)',
            border: '1px solid var(--border-color, #334155)',
            borderRadius: '0.75rem',
            padding: '3rem',
            textAlign: 'center',
            color: 'var(--text-secondary, #94a3b8)',
          }}
        >
          <ShieldCheck size={36} className="mx-auto mb-2 text-slate-500" />
          <p>No security criteria match the selected filter.</p>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
          {filteredChecks.map((item) => {
            const isExpanded = expandedIds.has(item.id)
            const cat = getCategoryBadge(item.category)
            return (
              <div
                key={item.id}
                className="table-card"
                style={{
                  background: 'var(--bg-card, #1e293b)',
                  border: `1px solid ${
                    item.status === 'fail'
                      ? 'rgba(239, 68, 68, 0.4)'
                      : item.status === 'warn'
                      ? 'rgba(245, 158, 11, 0.4)'
                      : 'var(--border-color, #334155)'
                  }`,
                  borderRadius: '0.625rem',
                  overflow: 'hidden',
                  transition: 'border-color 0.15s ease',
                }}
              >
                {/* Check Header Bar */}
                <div
                  onClick={() => toggleExpand(item.id)}
                  style={{
                    padding: '0.875rem 1.25rem',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    cursor: 'pointer',
                    userSelect: 'none',
                    gap: '1rem',
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.875rem', flexWrap: 'wrap' }}>
                    {/* Status Icon */}
                    {item.status === 'pass' && <CheckCircle2 size={20} className="text-emerald-500 flex-shrink-0" />}
                    {item.status === 'warn' && <AlertTriangle size={20} className="text-amber-500 flex-shrink-0" />}
                    {item.status === 'fail' && <XCircle size={20} className="text-red-500 flex-shrink-0" />}

                    {/* Check ID & Part */}
                    <span
                      style={{
                        fontSize: '0.6875rem',
                        fontWeight: 700,
                        padding: '0.125rem 0.375rem',
                        borderRadius: '0.25rem',
                        background: 'rgba(255, 255, 255, 0.06)',
                        color: 'var(--text-secondary, #94a3b8)',
                        fontFamily: 'monospace',
                      }}
                    >
                      #{item.number} (Part {item.part})
                    </span>

                    {/* Title */}
                    <span style={{ fontWeight: 600, fontSize: '0.9375rem' }}>{item.title}</span>

                    {/* Category Badge */}
                    <span
                      style={{
                        fontSize: '0.6875rem',
                        fontWeight: 500,
                        padding: '0.125rem 0.5rem',
                        borderRadius: '9999px',
                        border: '1px solid',
                      }}
                      className={cat.color}
                    >
                      {cat.label}
                    </span>

                    {/* OWASP Tag */}
                    {item.owasp_tag && (
                      <span
                        style={{
                          fontSize: '0.6875rem',
                          color: 'var(--text-muted, #64748b)',
                          background: 'rgba(0,0,0,0.15)',
                          padding: '0.125rem 0.375rem',
                          borderRadius: '0.25rem',
                        }}
                      >
                        {item.owasp_tag}
                      </span>
                    )}
                  </div>

                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
                    {/* Status Badge */}
                    <span
                      style={{
                        fontSize: '0.6875rem',
                        fontWeight: 700,
                        textTransform: 'uppercase',
                        padding: '0.125rem 0.5rem',
                        borderRadius: '0.25rem',
                        background:
                          item.status === 'pass'
                            ? 'rgba(16, 185, 129, 0.15)'
                            : item.status === 'warn'
                            ? 'rgba(245, 158, 11, 0.15)'
                            : 'rgba(239, 68, 68, 0.15)',
                        color:
                          item.status === 'pass' ? '#10b981' : item.status === 'warn' ? '#f59e0b' : '#ef4444',
                        border: `1px solid ${
                          item.status === 'pass'
                            ? 'rgba(16, 185, 129, 0.3)'
                            : item.status === 'warn'
                            ? 'rgba(245, 158, 11, 0.3)'
                            : 'rgba(239, 68, 68, 0.3)'
                        }`,
                      }}
                    >
                      {item.status}
                    </span>
                    {isExpanded ? <ChevronDown size={18} className="text-slate-400" /> : <ChevronRight size={18} className="text-slate-400" />}
                  </div>
                </div>

                {/* Expanded Details */}
                {isExpanded && (
                  <div
                    style={{
                      padding: '1rem 1.25rem',
                      borderTop: '1px solid var(--border-color, #334155)',
                      background: 'rgba(0,0,0,0.15)',
                      fontSize: '0.8125rem',
                      display: 'flex',
                      flexDirection: 'column',
                      gap: '0.75rem',
                    }}
                  >
                    <div>
                      <strong style={{ color: 'var(--text-secondary, #94a3b8)' }}>Requirement:</strong>
                      <p style={{ margin: '0.25rem 0 0 0' }}>{item.description}</p>
                    </div>

                    <div>
                      <strong style={{ color: 'var(--text-secondary, #94a3b8)' }}>Live Evaluation Evidence:</strong>
                      <div
                        style={{
                          margin: '0.25rem 0 0 0',
                          padding: '0.5rem 0.75rem',
                          background: 'rgba(0,0,0,0.3)',
                          borderRadius: '0.375rem',
                          fontFamily: 'monospace',
                          fontSize: '0.75rem',
                          color: '#38bdf8',
                          border: '1px solid rgba(56, 189, 248, 0.15)',
                        }}
                      >
                        {item.evidence}
                      </div>
                    </div>

                    <div>
                      <strong style={{ color: 'var(--text-secondary, #94a3b8)' }}>Hardening & Remediation:</strong>
                      <p style={{ margin: '0.25rem 0 0 0', color: '#e2e8f0' }}>{item.remediation}</p>
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
