import React from 'react'
import { Boxes, Laptop, MonitorSmartphone, PieChart, Terminal } from 'lucide-react'
import type { OSMetric } from '../types/api'

interface OSDistributionProps {
  metrics: OSMetric[]
}

const OS_CONFIG: Record<
  string,
  {
    // The theme owns the palette; these are CSS custom properties, so light
    // and dark themes each resolve them to whatever fits.
    varName: string
    icon: React.ComponentType<{ size?: number; className?: string }>
  }
> = {
  windows: { varName: '--os-windows', icon: Laptop },
  linux: { varName: '--os-linux', icon: Terminal },
  macos: { varName: '--os-macos', icon: MonitorSmartphone },
}

const FALLBACK = { varName: '--os-other', icon: Boxes }

export const OSDistribution: React.FC<OSDistributionProps> = ({ metrics }) => {
  const total = metrics.reduce((acc, m) => acc + m.count, 0)
  const getConfig = (name: string) => OS_CONFIG[name.toLowerCase()] ?? FALLBACK

  if (metrics.length === 0) {
    return (
      <div className="dash-card">
        <div className="card-header">
          <div className="card-title-group">
            <PieChart size={18} className="card-icon" />
            <h3 className="card-title">Operating System Distribution</h3>
          </div>
          <span className="card-badge">0 Monitored</span>
        </div>
        <div className="empty-state">No OS telemetry reported</div>
      </div>
    )
  }

  return (
    <div className="dash-card">
      <div className="card-header">
        <div className="card-title-group">
          <PieChart size={18} className="card-icon" />
          <h3 className="card-title">Operating System Distribution</h3>
        </div>
        <span className="card-badge">{total} Monitored</span>
      </div>

      <div className="os-content">
        {/* Proportional bar. A role="meter" here would claim a value this bar
            does not expose to assistive tech — the per-OS counts and
            percentages below are the actual readout, so the bar stays
            decorative and is labelled by the card title. */}
        <div
          className="os-stacked-bar"
          role="img"
          aria-label={`Operating system distribution: ${metrics
            .map((m) => `${m.os_name} ${m.pct}%`)
            .join(', ')}`}
        >
          {metrics.map((m) => {
            const cfg = getConfig(m.os_name)
            return (
              <div
                key={m.os_name}
                className="os-segment"
                style={{
                  // The server's pct is authoritative and already sums to ~100.
                  // The old Math.max(2, ...) floor inflated every thin slice
                  // and pushed the bar past 100% on a mixed fleet.
                  width: `${Math.min(100, Math.max(0, m.pct))}%`,
                  backgroundColor: `var(${cfg.varName})`,
                }}
                title={`${m.os_name}: ${m.count} devices (${m.pct}%)`}
              />
            )
          })}
        </div>

        <div className="os-grid">
          {metrics.map((m) => {
            const cfg = getConfig(m.os_name)
            const Icon = cfg.icon
            return (
              <div key={m.os_name} className="os-card">
                <div
                  className="os-card-icon-wrap"
                  style={{
                    // color-mix rather than appending a hex alpha: the OS
                    // colors are now variables, and `${color}1f` only worked
                    // because the old values were literal 6-digit hex.
                    backgroundColor: `color-mix(in srgb, var(${cfg.varName}) 12%, transparent)`,
                    color: `var(${cfg.varName})`,
                  }}
                >
                  <Icon size={18} />
                </div>
                <div className="os-card-meta">
                  <span className="os-card-title">{m.os_name}</span>
                  <span className="os-card-count">{m.count} devices</span>
                </div>
                <div
                  className="os-card-pct-badge"
                  style={{ borderColor: `color-mix(in srgb, var(${cfg.varName}) 25%, transparent)` }}
                >
                  {m.pct}%
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
