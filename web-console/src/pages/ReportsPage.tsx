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
import { useT } from '../i18n/core'

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
  const { t } = useT()
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
        throw new Error(detail.trim() || t('reports.export_failed_http', { status: res.status }))
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
        t('reports.download_started', { title, format: format.toUpperCase(), size: formatBytes(blob.size) }),
        t('reports.export_started_title')
      )
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('reports.export_request_failed')
      toast.error(msg, t('reports.export_failed_title'))
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
          title={t('reports.download_csv')}
        >
          <FileSpreadsheet size={15} className="text-success" />
          <span>{busyFormat === 'csv' ? t('reports.preparing') : t('reports.export_csv')}</span>
        </button>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          onClick={() => handleDownload('json')}
          disabled={busyFormat !== null}
          title={t('reports.download_json')}
        >
          <FileText size={15} className="text-primary" />
          <span>{busyFormat === 'json' ? t('reports.preparing') : t('reports.export_json')}</span>
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
  const { t } = useT()
  // The server mounts /api/reports/audit behind RequireRole(admin), so a
  // technician clicking it would get a 403 and an empty file.
  const canExportAudit = can('admin')

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('reports.title')}</h2>
          <p className="page-subtitle">
            {t('reports.subtitle')}
          </p>
        </div>
      </div>

      {/* Info Banner */}
      <div className="alert-banner alert-success mb-6">
        <ShieldCheck size={18} />
        <div>
          <span className="font-semibold block">{t('reports.streaming_title')}</span>
          <span className="text-xs text-dim">
            {t('reports.streaming_desc')}
          </span>
        </div>
      </div>

      {/* Report Cards Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <ReportCard
          title={t('reports.inventory_title')}
          category={t('reports.inventory_category')}
          description={t('reports.inventory_desc')}
          reportType="inventory"
          icon={<HardDrive size={22} className="text-primary" />}
        />

        <ReportCard
          title={t('reports.patches_title')}
          category={t('reports.patches_category')}
          description={t('reports.patches_desc')}
          reportType="patches"
          icon={<Shield size={22} className="text-warning" />}
        />

        <ReportCard
          title={t('reports.deployments_title')}
          category={t('reports.deployments_category')}
          description={t('reports.deployments_desc')}
          reportType="deployments"
          icon={<Layers size={22} className="text-success" />}
        />

        {canExportAudit ? (
          <ReportCard
            title={t('reports.audit_title')}
            category={t('reports.audit_category')}
            description={t('reports.audit_desc')}
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
                <span className="report-badge">{t('reports.audit_category')}</span>
                <h3 className="report-title">{t('reports.audit_title')}</h3>
              </div>
            </div>
            <p className="report-description">
              {t('reports.audit_restricted')}
            </p>
          </div>
        )}
      </div>

      {/* Audit Compliance Notes */}
      <div className="table-card mt-8 p-6">
        <div className="flex items-center gap-3 mb-3">
          <Database className="text-primary" size={20} />
          <h3 className="font-semibold text-main text-base">{t('reports.provenance_title')}</h3>
        </div>
        <p className="text-sm text-muted leading-relaxed">
          {t('reports.provenance_desc')}
        </p>
      </div>
    </div>
  )
}
