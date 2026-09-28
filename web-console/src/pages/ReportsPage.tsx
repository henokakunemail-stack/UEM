import React, { useState } from 'react'
import {
  Database,
  FileSpreadsheet,
  FileText,
  HardDrive,
  History,
  Layers,
  Shield,
  ShieldCheck,
} from 'lucide-react'
import { usePermission } from '../hooks/usePermission'
import { api, fetchRaw } from '../services/api'
import { useToast } from '../context/ToastContext'

type ReportType = 'inventory' | 'patches' | 'deployments' | 'audit'

interface ReportCardProps {
  title: string
  category: string
  description: string
  reportType: ReportType
  icon: React.ReactNode
}

const ReportCard: React.FC<ReportCardProps> = ({
  title,
  category,
  description,
  reportType,
  icon,
}) => {
  const [busyFormat, setBusyFormat] = useState<'csv' | 'json' | null>(null)
  const toast = useToast()

  const handleDownload = async (format: 'csv' | 'json') => {
    setBusyFormat(format)
    const url = api.getReportExportUrl(reportType, format)
    // Fetch, check the response, then save the blob ourselves. The old code
    // fired a link click and reported "Download Complete" off a 1.5s timer
    // without ever looking at the response — a 403 or a 500 came back as a
    // cheerful success toast and a missing file.
    let objectUrl: string | null = null
    try {
      const res = await fetchRaw(url, { headers: { Accept: '*/*' } })
      if (!res.ok) {
        // These handlers use http.Error, so the body is text/plain, not JSON.
        const detail = await res.text().catch(() => '')
        throw new Error(detail.trim() || `Export failed with HTTP ${res.status}`)
      }
      const blob = await res.blob()
      objectUrl = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = objectUrl
      link.download = `${reportType}-report.${format}`
      document.body.appendChild(link)
      link.click()
      document.body.removeChild(link)
      // "Started", not "Complete": once the click is dispatched the browser
      // owns the transfer and we cannot know how it finished.
      toast.success(
        `${title} (${format.toUpperCase()}) — download started, ${formatBytes(blob.size)}.`,
        'Export Started'
      )
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Export request failed'
      toast.error(msg, 'Export Failed')
    } finally {
      // Revoked on the next tick, not immediately: revoking synchronously
      // after click() cancels the download in some browsers.
      if (objectUrl) {
        window.setTimeout(() => URL.revokeObjectURL(objectUrl as string), 60_000)
      }
      setBusyFormat(null)
    }
  }

  return (
    <div className="report-card">
      <div className="report-card-top">
        <div className="report-icon-box">{icon}</div>
        <div className="report-header-info">
          <span className="report-badge">{category}</span>
          <h3 className="report-title">{title}</h3>
        </div>
      </div>
      <p className="report-description">{description}</p>
      <div className="report-actions">
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          onClick={() => handleDownload('csv')}
          disabled={busyFormat !== null}
          title="Download as CSV spreadsheet"
        >
          <FileSpreadsheet size={15} className="text-success" />
          <span>{busyFormat === 'csv' ? 'Preparing…' : 'Export CSV'}</span>
        </button>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          onClick={() => handleDownload('json')}
          disabled={busyFormat !== null}
          title="Download as structured JSON"
        >
          <FileText size={15} className="text-primary" />
          <span>{busyFormat === 'json' ? 'Preparing…' : 'Export JSON'}</span>
        </button>
      </div>
    </div>
  )
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export const ReportsPage: React.FC = () => {
  const { can } = usePermission()
  // The server mounts /api/reports/audit behind RequireRole(admin), so a
  // technician clicking it would get a 403 and an empty file.
  const canExportAudit = can('admin')

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Compliance Reports &amp; Data Exports</h2>
          <p className="page-subtitle">
            Generate on-demand audit logs, hardware inventories, patch compliance manifests, and
            deployment statistics
          </p>
        </div>
      </div>

      {/* Info Banner */}
      <div className="alert-banner alert-success mb-6">
        <ShieldCheck size={18} />
        <div>
          <span className="font-semibold block">Streaming Exports</span>
          <span className="text-xs text-dim">
            Report endpoints read straight from the database and stream rows as they are produced,
            so a large fleet export does not have to be buffered in memory first.
          </span>
        </div>
      </div>

      {/* Report Cards Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <ReportCard
          title="Hardware & System Inventory"
          category="Asset Management"
          description="Complete inventory of all enrolled branch endpoints, including CPU, RAM, disk partitions, network interfaces, serial numbers, and OS versions."
          reportType="inventory"
          icon={<HardDrive size={22} className="text-primary" />}
        />

        <ReportCard
          title="Fleet Patch & Vulnerability Report"
          category="Security & Compliance"
          description="Detailed manifest of installed and missing security updates, pending CVE hotfixes, reboot requirements, and overall fleet patch compliance rates."
          reportType="patches"
          icon={<Shield size={22} className="text-warning" />}
        />

        <ReportCard
          title="Software Deployment History"
          category="Operations"
          description="Full deployment lifecycle records, silent installer rollouts, target branch groups, return exit codes, and package distribution success rates."
          reportType="deployments"
          icon={<Layers size={22} className="text-success" />}
        />

        {canExportAudit ? (
          <ReportCard
            title="Security & System Audit Trail"
            category="Governance & Audit"
            description="Append-only security audit trail recording administrator actions, remote command dispatches, credential changes, and system modifications."
            reportType="audit"
            icon={<History size={22} className="text-danger" />}
          />
        ) : (
          <div className="report-card">
            <div className="report-card-top">
              <div className="report-icon-box">
                <History size={22} className="text-dim" />
              </div>
              <div className="report-header-info">
                <span className="report-badge">Governance &amp; Audit</span>
                <h3 className="report-title">Security &amp; System Audit Trail</h3>
              </div>
            </div>
            <p className="report-description">
              The forensic audit export is restricted to administrators. The Audit tab still shows
              you the live trail at technician level and above.
            </p>
          </div>
        )}
      </div>

      {/* Audit Compliance Notes */}
      <div className="table-card mt-8 p-6">
        <div className="flex items-center gap-3 mb-3">
          <Database className="text-primary" size={20} />
          <h3 className="font-semibold text-main text-base">Export Provenance</h3>
        </div>
        <p className="text-sm text-muted leading-relaxed">
          Every export is generated from the live database at the moment you request it, and each
          row carries its own record identifiers and RFC 3339 timestamps. Whether a given export
          satisfies your organisation&rsquo;s audit standard — ISO 27001, SOC 2 or an internal
          policy — is a judgement for whoever owns that standard; this console does not certify
          against any of them.
        </p>
      </div>
    </div>
  )
}
