import React from 'react'
import { Building2, ChevronRight } from 'lucide-react'
import type { SiteMetric } from '../types/api'

interface SiteDistributionProps {
  sites: SiteMetric[]
  onSelectSite?: (siteName: string) => void
}

const getHealthStatus = (pct: number) => {
  if (pct >= 85) return { label: 'Healthy', level: 'healthy' }
  if (pct >= 60) return { label: 'Degraded', level: 'degraded' }
  return { label: 'Critical', level: 'critical' }
}

export const SiteDistribution: React.FC<SiteDistributionProps> = ({
  sites,
  onSelectSite,
}) => (
  <div className="dash-card">
    <div className="card-header">
      <div className="card-title-group">
        <Building2 size={18} className="card-icon" />
        <h3 className="card-title">Multi-Branch Health</h3>
      </div>
      <span className="card-badge">{sites.length} Active Sites</span>
    </div>

    <div className="site-list">
      {sites.length === 0 ? (
        <div className="empty-state">No site telemetry available</div>
      ) : (
        sites.map((s) => {
          const status = getHealthStatus(s.online_pct)
          const row = (
            <>
              <div className="site-meta">
                <div className="site-meta-left">
                  <span className="site-name">{s.site.toUpperCase()}</span>
                  <span className={`site-health-pill ${status.level}`}>
                    {status.label}
                  </span>
                </div>
                <div className="site-meta-right">
                  <span className="site-stats">
                    <strong>{s.online}</strong> / {s.total} ({s.online_pct}%)
                  </span>
                  {onSelectSite && <ChevronRight size={14} className="site-arrow" />}
                </div>
              </div>
              {/* Decorative: the ratio above states it in text. */}
              <div className="progress-bar-bg" aria-hidden="true">
                <div
                  className={`progress-bar-fill ${status.level}`}
                  style={{ width: `${Math.min(100, Math.max(0, s.online_pct))}%` }}
                />
              </div>
            </>
          )

          // A site row that navigates is a button, not a div with a keydown
          // handler. Same classes either way.
          if (onSelectSite) {
            return (
              <button
                key={s.site}
                type="button"
                className="site-row interactive"
                onClick={() => onSelectSite(s.site)}
              >
                {row}
              </button>
            )
          }
          return (
            <div key={s.site} className="site-row">
              {row}
            </div>
          )
        })
      )}
    </div>
  </div>
)
