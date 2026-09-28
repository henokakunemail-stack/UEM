import React from 'react'

interface KPICardProps {
  title: string
  value: string | number
  subtitle?: string
  icon: React.ReactNode
  variant?: 'primary' | 'success' | 'warning' | 'danger' | 'neutral'
  badge?: string
  progress?: { current: number; total: number }
  onClick?: () => void
}

export const KPICard: React.FC<KPICardProps> = ({
  title,
  value,
  subtitle,
  icon,
  variant = 'primary',
  badge,
  progress,
  onClick,
}) => {
  const pct =
    progress && progress.total > 0
      ? Math.min(100, Math.max(0, Math.round((progress.current / progress.total) * 100)))
      : null

  const body = (
    <>
      <div className="kpi-header">
        <div className="kpi-title-row">
          <span className="kpi-title">{title}</span>
          {badge && <span className="kpi-badge">{badge}</span>}
        </div>
        <div className="kpi-icon-wrap">{icon}</div>
      </div>
      <div className="kpi-body">
        <div className="kpi-value">{value}</div>
        {subtitle && <div className="kpi-subtitle">{subtitle}</div>}
        {pct !== null && (
          <div className="kpi-progress-wrap">
            {/* Decorative: the percentage is already in the value and in the
                label beside it, so announcing the bar too would read the same
                number twice. */}
            <div className="kpi-progress-bar" aria-hidden="true">
              <div
                className={`kpi-progress-fill ${variant}`}
                style={{ width: `${pct}%` }}
              />
            </div>
            <span className="kpi-progress-label">{pct}%</span>
          </div>
        )}
      </div>
    </>
  )

  const className = `kpi-card ${variant}${onClick ? ' interactive' : ''}`

  // A clickable card is a real control. A div with role="button" and a
  // hand-rolled keydown handler is the usual stand-in, but it still misses
  // Space activation and the native button role. The classes are identical
  // either way, so the theme needs no new selector.
  if (onClick) {
    return (
      <button type="button" className={className} onClick={onClick}>
        {body}
      </button>
    )
  }

  return <div className={className}>{body}</div>
}
