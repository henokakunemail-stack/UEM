import React, { useEffect, useState } from 'react'
import {
  AlertTriangle,
  Award,
  Cpu,
  DollarSign,
  Key,
  Laptop,
  Plus,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import type {
  AssetSummaryDTO,
  HardwareAssetDTO,
  LicenseComplianceSummaryDTO,
  SoftwareLicenseDTO,
} from '../types/api'

export type AssetTab = 'hardware' | 'licenses'

interface AssetLicensePageProps {
  activeTab: AssetTab
  onTabChange: (tab: AssetTab) => void
}

export const AssetLicensePage: React.FC<AssetLicensePageProps> = ({ activeTab: subTab, onTabChange: setSubTab }) => {
  // The server's gates differ per action here, so mirror them exactly rather
  // than using one blanket "can edit" flag: registering an asset needs
  // technician, but licences and deletions are admin-only.
  const { can } = usePermission()
  const canRegister = can('technician')
  const canAdmin = can('admin')
  const [assets, setAssets] = useState<HardwareAssetDTO[]>([])
  const [summary, setSummary] = useState<AssetSummaryDTO | null>(null)
  const [licenses, setLicenses] = useState<SoftwareLicenseDTO[]>([])
  const [compliance, setCompliance] = useState<LicenseComplianceSummaryDTO[]>([])
  const [loading, setLoading] = useState(true)

  // Modals
  const [isAssetModalOpen, setIsAssetModalOpen] = useState(false)
  const [isLicenseModalOpen, setIsLicenseModalOpen] = useState(false)
  // Destructive deletes go through a stateful dialog instead of window.confirm
  // so the pending state can block a double-submit while the request is in
  // flight. Null = no dialog open; the row id is what gets deleted on confirm.
  const [assetPendingDelete, setAssetPendingDelete] = useState<{ id: string; tag: string } | null>(null)
  const [deletingAsset, setDeletingAsset] = useState(false)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // New Asset Form
  const [newAsset, setNewAsset] = useState<Partial<HardwareAssetDTO>>({
    asset_tag: '',
    model_name: '',
    serial_number: '',
    vendor: '',
    site: '',
    department: '',
    assigned_user: '',
    purchase_cost: 0,
    status: 'in_use',
    notes: '',
  })

  // New License Form
  const [newLicense, setNewLicense] = useState<Partial<SoftwareLicenseDTO>>({
    software_name: '',
    publisher: '',
    license_type: 'per_device',
    total_seats: 1,
    cost: 0,
    notes: '',
  })

  const loadData = async () => {
    setLoading(true)
    try {
      const [astList, sumData, licList, compData] = await Promise.all([
        api.getAssets().catch(() => []),
        api.getAssetSummary().catch(() => null),
        api.getLicenses().catch(() => []),
        api.getLicenseCompliance().catch(() => ({ audited_at: '', compliance: [] })),
      ])
      setAssets(astList)
      setSummary(sumData)
      setLicenses(licList)
      setCompliance(compData.compliance || [])
    } catch (err: any) {
      setMsg({ type: 'error', text: err.message || 'Failed to load asset & license data' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  const handleCreateAsset = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createAsset(newAsset)
      const successText = `Hardware Asset '${newAsset.asset_tag}' registered successfully.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'Asset Registered')
      setIsAssetModalOpen(false)
      setNewAsset({
        asset_tag: '',
        model_name: '',
        serial_number: '',
        vendor: '',
        site: '',
        department: '',
        assigned_user: '',
        purchase_cost: 0,
        status: 'in_use',
        notes: '',
      })
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to register asset'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Registration Failed')
    }
  }

  const handleDeleteAsset = async () => {
    if (!assetPendingDelete) return
    const { id, tag } = assetPendingDelete
    setDeletingAsset(true)
    try {
      await api.deleteAsset(id)
      const infoText = `Asset '${tag}' removed from inventory.`
      setMsg({ type: 'success', text: infoText })
      toast.info(infoText, 'Asset Removed')
      setAssetPendingDelete(null)
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to delete asset'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Deletion Failed')
    } finally {
      setDeletingAsset(false)
    }
  }

  const handleCreateLicense = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await api.createLicense(newLicense)
      const successText = `License '${newLicense.software_name}' created.`
      setMsg({ type: 'success', text: successText })
      toast.success(successText, 'License Created')
      setIsLicenseModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || 'Failed to create license'
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, 'Creation Failed')
    }
  }

  const formatCurrency = (amount: number) => {
    return new Intl.NumberFormat('id-ID', {
      style: 'currency',
      currency: 'IDR',
      maximumFractionDigits: 0,
    }).format(amount)
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">IT Asset & Software License Management</h2>
          <p className="page-subtitle">
            Enterprise Hardware Asset Management (HAM) and live Software License Reconciliation (SAM)
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {subTab === 'hardware' && canRegister && (
            <button type="button" className="btn btn-primary" onClick={() => setIsAssetModalOpen(true)}>
              <Plus size={16} />
              <span>Register Hardware Asset</span>
            </button>
          )}
          {subTab === 'licenses' && canAdmin && (
            <button type="button" className="btn btn-primary" onClick={() => setIsLicenseModalOpen(true)}>
              <Plus size={16} />
              <span>Add Software License</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label="Dismiss message">
            <X size={14} />
          </button>
        </div>
      )}

      {/* Financial Overview Cards */}
      {summary && (
        <div className="kpi-grid">
          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Total Fleet Valuation</span>
              <DollarSign className="kpi-icon text-success" size={20} />
            </div>
            <div className="kpi-value text-success">{formatCurrency(summary.total_valuation)}</div>
            <span className="kpi-hint">Capitalized hardware asset value</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Active Hardware</span>
              <Laptop className="kpi-icon text-primary" size={20} />
            </div>
            <div className="kpi-value">{summary.active_assets} / {summary.total_assets}</div>
            <span className="kpi-hint">Physical machines assigned and deployed</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Warranty Expiring (&lt;30d)</span>
              <AlertTriangle className="kpi-icon text-warning" size={20} />
            </div>
            <div className="kpi-value text-warning">{summary.warranty_expiring_count}</div>
            <span className="kpi-hint">Approaching vendor support renewal</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">Software Contracts</span>
              <Award className="kpi-icon text-dim" size={20} />
            </div>
            <div className="kpi-value">{licenses.length}</div>
            <span className="kpi-hint">Under active compliance audit</span>
          </div>
        </div>
      )}

      {/* Navigation Sub-Tabs */}
      <div className="tabs-nav">
        <button
          type="button"
          className={`tab-btn ${subTab === 'hardware' ? 'active' : ''}`}
          onClick={() => setSubTab('hardware')}
        >
          <Cpu size={16} />
          <span>Hardware Assets (HAM)</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'licenses' ? 'active' : ''}`}
          onClick={() => setSubTab('licenses')}
        >
          <Key size={16} />
          <span>Software License Compliance (SAM)</span>
        </button>
      </div>

      {/* Tab 1: Hardware Assets */}
      {subTab === 'hardware' && (
        <div className="table-card">
          <DataTable label="Hardware assets">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Asset Tag</th>
                  <th>Model & Vendor</th>
                  <th>Serial Number</th>
                  <th>Department & Site</th>
                  <th>Assigned User</th>
                  <th>Valuation</th>
                  <th>Status</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {assets.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="text-center py-8 text-muted">
                      No hardware assets registered yet.
                    </td>
                  </tr>
                ) : (
                  assets.map((a) => (
                    <tr key={a.id}>
                      <td>
                        <span className="font-mono font-semibold text-primary">{a.asset_tag}</span>
                      </td>
                      <td>
                        <div>
                          <span className="font-semibold text-main">{a.model_name}</span>
                          <span className="text-sm text-dim">{a.vendor}</span>
                        </div>
                      </td>
                      <td className="font-mono text-sm">{a.serial_number || '—'}</td>
                      <td>{a.department} ({a.site})</td>
                      <td>{a.assigned_user || 'Unassigned'}</td>
                      <td>{formatCurrency(a.purchase_cost)}</td>
                      <td>
                        <span className={`status-pill ${a.status === 'in_use' ? 'online' : a.status === 'in_repair' ? 'warning' : 'offline'}`}>
                          {a.status}
                        </span>
                      </td>
                      <td>
                        {canAdmin && (
                          <button
                            type="button"
                            className="btn-action text-danger"
                            onClick={() => setAssetPendingDelete({ id: a.id, tag: a.asset_tag })}
                            title="Delete asset record"
                          >
                            <Trash2 size={14} />
                            <span>Delete</span>
                          </button>
                        )}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Tab 2: Software Licenses & Compliance */}
      {subTab === 'licenses' && (
        <div className="table-card">
          <div className="card-header">
            <div className="card-title-group">
              <ShieldCheck size={18} className="card-icon" />
              <h3 className="card-title">Live License Seat Reconciliation</h3>
            </div>
            <span className="text-sm text-dim">
              Cross-checked in real-time against agent software inventory
            </span>
          </div>
          <DataTable label="Software license compliance">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Software Title</th>
                  <th>Publisher</th>
                  <th>License Type</th>
                  <th>Purchased Seats</th>
                  <th>Allocated Seats</th>
                  <th>Installed (Detected)</th>
                  <th>Compliance Status</th>
                </tr>
              </thead>
              <tbody>
                {compliance.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      No software license records to reconcile.
                    </td>
                  </tr>
                ) : (
                  compliance.map((c) => (
                    <tr key={c.license_id}>
                      <td className="font-semibold text-main">{c.software_name}</td>
                      <td>{c.publisher || '—'}</td>
                      <td>
                        <span className="os-badge">{c.license_type}</span>
                      </td>
                      <td className="font-semibold">{c.total_seats}</td>
                      <td>{c.allocated_seats}</td>
                      <td>
                        <span className={`font-semibold ${c.installed_detected > c.total_seats ? 'text-danger' : 'text-main'}`}>
                          {c.installed_detected} devices
                        </span>
                      </td>
                      <td>
                        {c.status === 'compliant' && (
                          <span className="status-pill online">
                            <ShieldCheck size={13} /> Compliant
                          </span>
                        )}
                        {c.status === 'over_allocated' && (
                          <span className="status-pill danger" title="Installation count exceeds purchased seats!">
                            <ShieldAlert size={13} /> Over Allocated (Deficit!)
                          </span>
                        )}
                        {c.status === 'expiring_soon' && (
                          <span className="status-pill warning">
                            <AlertTriangle size={13} /> Expiring Soon
                          </span>
                        )}
                        {/* The server sets 'expired' once now() is past expires_at.
                            Without this branch the cell rendered blank for the one
                            state that most needs to be visible. */}
                        {c.status === 'expired' && (
                          <span className="status-pill danger">
                            <AlertTriangle size={13} /> Expired
                          </span>
                        )}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </DataTable>
        </div>
      )}

      {/* Register Asset Modal */}
      <Modal
        open={isAssetModalOpen}
        onClose={() => setIsAssetModalOpen(false)}
        title="Register Physical Hardware Asset"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsAssetModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="asset-form" className="btn btn-primary">
              Save Asset
            </button>
          </>
        }
      >
        <form id="asset-form" onSubmit={handleCreateAsset}>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-tag">
              Asset Tag (Barcode / Sticker ID)
            </label>
            <input
              id="asset-tag"
              type="text"
              className="form-input"
              required
              placeholder="e.g. AST-JKT-2026-001"
              value={newAsset.asset_tag}
              onChange={(e) => setNewAsset({ ...newAsset, asset_tag: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-model">
              Model Name
            </label>
            <input
              id="asset-model"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Dell Latitude 3420"
              value={newAsset.model_name}
              onChange={(e) => setNewAsset({ ...newAsset, model_name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-serial">
              Serial Number
            </label>
            <input
              id="asset-serial"
              type="text"
              className="form-input"
              placeholder="e.g. SN-DELL-99213"
              value={newAsset.serial_number}
              onChange={(e) => setNewAsset({ ...newAsset, serial_number: e.target.value })}
            />
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-user">
                Assigned User
              </label>
              <input
                id="asset-user"
                type="text"
                className="form-input"
                placeholder="User Name"
                value={newAsset.assigned_user}
                onChange={(e) => setNewAsset({ ...newAsset, assigned_user: e.target.value })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-dept">
                Department
              </label>
              <input
                id="asset-dept"
                type="text"
                className="form-input"
                placeholder="Department"
                value={newAsset.department}
                onChange={(e) => setNewAsset({ ...newAsset, department: e.target.value })}
              />
            </div>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-cost">
              Purchase Valuation (IDR)
            </label>
            <input
              id="asset-cost"
              type="number"
              className="form-input"
              placeholder="15000000"
              value={newAsset.purchase_cost}
              onChange={(e) => setNewAsset({ ...newAsset, purchase_cost: Number(e.target.value) })}
            />
          </div>
        </form>
      </Modal>

      {/* Add License Modal */}
      <Modal
        open={isLicenseModalOpen}
        onClose={() => setIsLicenseModalOpen(false)}
        title="Record Software License Contract"
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsLicenseModalOpen(false)}>
              Cancel
            </button>
            <button type="submit" form="license-form" className="btn btn-primary">
              Save License
            </button>
          </>
        }
      >
        <form id="license-form" onSubmit={handleCreateLicense}>
          <div className="form-group">
            <label className="form-label" htmlFor="license-title">
              Software Title
            </label>
            <input
              id="license-title"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Endpoint Security Suite"
              value={newLicense.software_name}
              onChange={(e) => setNewLicense({ ...newLicense, software_name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="license-publisher">
              Publisher / Vendor
            </label>
            <input
              id="license-publisher"
              type="text"
              className="form-input"
              placeholder="e.g. Microsoft Corporation"
              value={newLicense.publisher}
              onChange={(e) => setNewLicense({ ...newLicense, publisher: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="license-seats">
              Total Purchased Seats
            </label>
            <input
              id="license-seats"
              type="number"
              className="form-input"
              required
              min={1}
              value={newLicense.total_seats}
              onChange={(e) => setNewLicense({ ...newLicense, total_seats: Number(e.target.value) })}
            />
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={assetPendingDelete !== null}
        title="Delete hardware asset"
        message={
          assetPendingDelete
            ? `Delete hardware asset '${assetPendingDelete.tag}'? This removes it from inventory and cannot be undone.`
            : ''
        }
        confirmLabel="Delete"
        pending={deletingAsset}
        onConfirm={handleDeleteAsset}
        onCancel={() => setAssetPendingDelete(null)}
      />
    </div>
  )
}
