import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ComplianceEvaluation, ComplianceProfile, ComplianceReport } from '../types/api'

const api = vi.hoisted(() => ({
  getComplianceProfiles: vi.fn(),
  getComplianceEvaluation: vi.fn(),
  getComplianceReports: vi.fn(),
  getDeviceGroups: vi.fn(),
  generateComplianceReport: vi.fn(),
  verifyComplianceReport: vi.fn(),
  getComplianceFrameworks: vi.fn(),
  getFrameworkAssessment: vi.fn(),
  generateFrameworkReport: vi.fn(),
}))
const role = vi.hoisted(() => ({ current: 'viewer' }))
vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()), api }))
vi.mock('../context/AuthContext', () => ({ useAuth: () => ({ user: { uid: 'me', usr: 'me', rol: role.current } }) }))

import { CompliancePage } from './CompliancePage'
import { ToastProvider } from '../context/ToastContext'
import { I18nProvider } from '../i18n/I18nContext'

const profile: ComplianceProfile = {
  id: 'p1',
  name: 'Baseline default',
  description: '',
  scope_site: '',
  scope_group_id: '',
  is_default: true,
  monthly_report: true,
  rules: {
    require_disk_encryption: true,
    require_antivirus: true,
    require_real_time: true,
    max_signature_age_days: 7,
    require_firewall_all_profiles: true,
    require_patches: false,
    max_missing_patch_age_days: 30,
    max_patch_scan_age_days: 7,
    require_password_policy: false,
    min_password_length: 8,
    max_lockout_threshold: 10,
    require_screen_lock: false,
    max_screen_lock_minutes: 15,
    max_posture_age_days: 7,
  },
}

const dev = (id: string, site: string) => ({ id, hostname: id, os_name: 'windows', site, online: true })

const evaluation: ComplianceEvaluation = {
  profile,
  scope: 'Semua perangkat aktif',
  evaluated_at: '2026-11-20T01:00:00Z',
  summary: {
    devices: 3,
    compliant: 1,
    noncompliant: 1,
    unknown: 1,
    not_applicable: 0,
    score: 50,
    controls: [
      { control: 'disk_encryption', pass: 1, fail: 1, unknown: 1, not_applicable: 0, pass_percent: 50 },
      { control: 'antivirus', pass: 2, fail: 0, unknown: 1, not_applicable: 0, pass_percent: 100 },
      { control: 'firewall', pass: 2, fail: 0, unknown: 1, not_applicable: 0, pass_percent: 100 },
    ],
  },
  devices: [
    {
      device: dev('PC-OK', 'Pusat'),
      status: 'compliant',
      controls: [
        { control: 'disk_encryption', status: 'pass', reason: 'ok' },
        { control: 'antivirus', status: 'pass', reason: 'ok' },
        { control: 'firewall', status: 'pass', reason: 'ok' },
      ],
    },
    {
      device: dev('PC-BITLOCKER-OFF', 'Pusat'),
      status: 'noncompliant',
      controls: [
        { control: 'disk_encryption', status: 'fail', reason: 'BitLocker off pada C:', evidence: ['C: Off [sistem]'] },
        { control: 'antivirus', status: 'pass', reason: 'ok' },
        { control: 'firewall', status: 'pass', reason: 'ok' },
      ],
    },
    {
      device: dev('PC-OLD-AGENT', 'Cabang'),
      status: 'unknown',
      controls: [
        { control: 'disk_encryption', status: 'unknown', reason: 'Agen belum melaporkan postur keamanan' },
        { control: 'antivirus', status: 'unknown', reason: 'Agen belum melaporkan postur keamanan' },
        { control: 'firewall', status: 'unknown', reason: 'Agen belum melaporkan postur keamanan' },
      ],
    },
  ],
}

const report: ComplianceReport = {
  id: 'KP-1',
  profile_id: 'p1',
  profile_name: 'Baseline default',
  period: '2026-11',
  kind: 'manual',
  generated_by: 'admin',
  generated_at: '2026-11-20T01:00:00Z',
  file_sha256: 'a'.repeat(64),
  content_sha256: 'b'.repeat(64),
  size_bytes: 1000,
}

const fw = (id: string, family: string, name: string) => ({
  id, family, name, regulation: name, reviewed_at: '2026-11-19', origin: 'builtin' as const, controls: 3, mapped: 2,
})
const frameworks = {
  frameworks: [
    fw('iso27001-2022', 'iso27001', 'ISO/IEC 27001:2022 Annex A'),
    fw('ojk-bpr-34-2025', 'ojk-bpr', 'POJK 34/2025'),
    fw('ojk-bpr-75-2016', 'ojk-bpr', 'POJK 75/2016'),
  ],
  errors: [],
  template_dir: 'data/compliance-templates',
}

function renderPage() {
  render(
    <I18nProvider lang="en">
      <ToastProvider>
        <CompliancePage />
      </ToastProvider>
    </I18nProvider>,
  )
}

describe('CompliancePage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    role.current = 'viewer'
    api.getComplianceProfiles.mockResolvedValue([profile])
    api.getComplianceEvaluation.mockResolvedValue(evaluation)
    api.getComplianceReports.mockResolvedValue([report])
    api.getDeviceGroups.mockResolvedValue([])
    api.getComplianceFrameworks.mockResolvedValue(frameworks)
  })

  // The score is the server's, over evaluated devices only; the unknown
  // device is its own number, not folded into compliant or non-compliant.
  it('shows the score over evaluated devices and unknowns separately', async () => {
    renderPage()
    const score = await screen.findByTestId('compliance-score')
    expect(score.textContent).toContain('50%')
    expect(score.textContent).toContain('1 of 2 evaluable devices')
    expect(screen.getByTestId('compliance-unknown').textContent).toContain('1')
    expect(api.getComplianceEvaluation).toHaveBeenCalledWith('p1')
  })

  it('expands a device to its reasons and evidence', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('compliance-row-PC-BITLOCKER-OFF'))
    expect(screen.getByText('BitLocker off pada C:')).toBeTruthy()
    expect(screen.getByText('C: Off [sistem]')).toBeTruthy()
  })

  it('filters to devices failing one control', async () => {
    renderPage()
    await screen.findByTestId('compliance-row-PC-OK')
    fireEvent.change(screen.getByLabelText('Filter by control'), { target: { value: 'disk_encryption' } })
    expect(screen.queryByTestId('compliance-row-PC-OK')).toBeNull()
    expect(screen.queryByTestId('compliance-row-PC-OLD-AGENT')).toBeNull()
    expect(screen.getByTestId('compliance-row-PC-BITLOCKER-OFF')).toBeTruthy()
  })

  // An empty archive once came back as null and crashed the page.
  it('renders with an empty archive sent as null', async () => {
    api.getComplianceReports.mockResolvedValue(null)
    renderPage()
    expect(await screen.findByText('No reports yet.')).toBeTruthy()
  })

  it('offers report generation to technicians but not viewers', async () => {
    renderPage()
    await screen.findByTestId('compliance-score')
    expect(screen.queryByText('Generate PDF report')).toBeNull()
  })

  it('generates a report and refreshes the archive', async () => {
    role.current = 'technician'
    api.generateComplianceReport.mockResolvedValue(report)
    renderPage()
    fireEvent.click(await screen.findByText('Generate PDF report'))
    await waitFor(() => expect(api.generateComplianceReport).toHaveBeenCalledWith('p1'))
    await waitFor(() => expect(api.getComplianceReports).toHaveBeenCalledTimes(2))
  })

  it('says plainly when a hash matches no report', async () => {
    api.verifyComplianceReport.mockResolvedValue({ match: false })
    renderPage()
    const box = await screen.findByLabelText('File SHA-256 (64 hex characters)')
    fireEvent.change(box, { target: { value: 'c'.repeat(64) } })
    fireEvent.click(screen.getByText('Check'))
    expect(await screen.findByTestId('verify-nomatch')).toBeTruthy()
    expect(api.verifyComplianceReport).toHaveBeenCalledWith('c'.repeat(64))
  })

  it('names the matching report when a hash verifies', async () => {
    api.verifyComplianceReport.mockResolvedValue({ match: true, report })
    renderPage()
    const box = await screen.findByLabelText('File SHA-256 (64 hex characters)')
    fireEvent.change(box, { target: { value: 'a'.repeat(64) } })
    fireEvent.click(screen.getByText('Check'))
    const ok = await screen.findByTestId('verify-match')
    expect(within(ok).getByText(/KP-1/)).toBeTruthy()
  })

  // A family with two versions is one choice; the server picks by period.
  it('offers each framework family once', async () => {
    renderPage()
    const select = (await screen.findByLabelText('Regulatory framework')) as HTMLSelectElement
    await waitFor(() => expect(Array.from(select.options).map((o) => o.value)).toEqual(['iso27001-2022', 'ojk-bpr']))
  })

  it('shows a framework assessment with out-of-scope controls apart', async () => {
    api.getFrameworkAssessment.mockResolvedValue({
      period: '2026-11',
      generated_at: '2026-11-20T01:00:00Z',
      template: { ...fw('ojk-bpr-75-2016', 'ojk-bpr', 'POJK 75/2016'), pending: ['Nomor pasal belum diverifikasi'] },
      profile_name: 'Baseline default',
      scope: 'Semua perangkat aktif',
      summary: { controls: 3, supported: 0, partial: 1, gap: 1, no_data: 0, out_of_scope: 1 },
      controls: [
        { ref: 'Backup', ref_verified: false, title: 'Backup data', status: 'gap',
          evidence: [{ source: 'backup.recent', title: 'Backup database server', status: 'gap', summary: 'Tidak ada file backup' }] },
        { ref: 'Pasal 15', ref_verified: true, title: 'Keamanan informasi', status: 'partial', evidence: [] },
        { ref: 'Pasal 60', ref_verified: true, title: 'Laporan insiden', status: 'out_of_scope', reason: 'proses' },
      ],
    })
    renderPage()
    const select = (await screen.findByLabelText('Regulatory framework')) as HTMLSelectElement
    await waitFor(() => expect(select.options.length).toBe(2))
    fireEvent.change(select, { target: { value: 'ojk-bpr' } })
    fireEvent.click(screen.getByText('View assessment'))
    const counts = await screen.findByTestId('fw-counts')
    expect(counts.textContent).toContain('Gap: 1')
    expect(counts.textContent).toContain('Out of scope: 1')
    expect(api.getFrameworkAssessment).toHaveBeenCalledWith('ojk-bpr', 'p1', expect.stringMatching(/^\d{4}-\d{2}$/))
    // An unverified reference never shows as an article number.
    expect(screen.queryByText('Backup', { selector: 'strong' })).toBeNull()
    expect(screen.getAllByText('(reference not yet verified)').length).toBe(1)
    // Out-of-scope controls are not in the table, only behind the toggle.
    expect(screen.queryByTestId('fw-row-Pasal 60')).toBeNull()
    fireEvent.click(screen.getByText("1 controls outside the application's scope"))
    expect(screen.getByText('Laporan insiden')).toBeTruthy()
    fireEvent.click(screen.getByTestId('fw-row-Backup'))
    expect(screen.getByText('Tidak ada file backup')).toBeTruthy()
  })
})
