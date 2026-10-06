import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  AlertTriangle,
  Award,
  Cpu,
  DollarSign,
  Key,
  Laptop,
  Pencil,
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
  DeviceDTO,
  DirectoryContactDTO,
  HardwareAssetDTO,
  LicenseComplianceSummaryDTO,
  SoftwareLicenseDTO,
} from '../types/api'

export type AssetTab = 'hardware' | 'licenses'

const ASSET_STATUSES: HardwareAssetDTO['status'][] = [
  'in_use',
  'in_stock',
  'in_repair',
  'retired',
  'disposed',
]

// Every field the table stores, so the create form cannot silently drop one
// again. The previous initial state listed ten keys but the form only rendered
// six, so vendor, site, status and notes were submitted empty on every create
// no matter what the operator typed.
const emptyAssetForm = (): Partial<HardwareAssetDTO> => ({
  asset_tag: '',
  model_name: '',
  serial_number: '',
  vendor: '',
  site: '',
  department: '',
  assigned_user: '',
  purchase_date: null,
  purchase_cost: 0,
  warranty_expires_at: null,
  status: 'in_use',
  notes: '',
  device_id: null,
})

// <input type="date"> wants yyyy-mm-dd; the server sends and expects a timestamp.
const toDateInput = (v?: string | null) => (v ? v.slice(0, 10) : '')
// An empty date input must clear the column, not store the epoch.
const fromDateInput = (v: string) => (v ? new Date(v + 'T00:00:00Z').toISOString() : null)

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
  // The same modal creates and edits. null = creating; an asset = editing that
  // one. api.updateAsset existed with no call site, so handleUpdateAsset was
  // unreachable — the server already copied DeviceID correctly, nothing reached
  // it.
  const [editingAsset, setEditingAsset] = useState<HardwareAssetDTO | null>(null)
  // Destructive deletes go through a stateful dialog instead of window.confirm
  // so the pending state can block a double-submit while the request is in
  // flight. Null = no dialog open; the row id is what gets deleted on confirm.
  const [assetPendingDelete, setAssetPendingDelete] = useState<{ id: string; tag: string } | null>(null)
  const [deletingAsset, setDeletingAsset] = useState(false)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const toast = useToast()

  // New Asset Form
  const [newAsset, setNewAsset] = useState<Partial<HardwareAssetDTO>>(emptyAssetForm())

  // Devices available to link, and the contacts a PIC is picked from. Both are
  // loaded when the modal opens rather than on mount: they are only needed
  // here, and a page load should not pay for a 200-device list nobody looks at.
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  const [contacts, setContacts] = useState<DirectoryContactDTO[]>([])
  // True while the chosen device's inventory is in flight, so the form can say
  // the fields are filling themselves rather than looking broken.
  const [prefilling, setPrefilling] = useState(false)

  // New License Form
  const emptyLicenseForm = (): Partial<SoftwareLicenseDTO> => ({
    software_name: '',
    publisher: '',
    license_type: 'per_device',
    total_seats: 1,
    cost: 0,
    notes: '',
  })
  const [newLicense, setNewLicense] = useState<Partial<SoftwareLicenseDTO>>(emptyLicenseForm())

  // This also runs after every create/update/delete, so it outlives the mount it
  // started on. `alive` is false once the page is gone, and every setState below
  // is behind that check — setting state on an unmounted component is a wasted
  // render at best and a React warning at worst.
  const alive = useRef(true)
  const loadData = async () => {
    setLoading(true)
    try {
      const [astList, sumData, licList, compData] = await Promise.all([
        api.getAssets().catch(() => []),
        api.getAssetSummary().catch(() => null),
        api.getLicenses().catch(() => []),
        api.getLicenseCompliance().catch(() => ({ audited_at: '', compliance: [] })),
      ])
      if (!alive.current) return
      setAssets(astList)
      setSummary(sumData)
      setLicenses(licList)
      setCompliance(compData.compliance || [])
    } catch (err: any) {
      if (!alive.current) return
      setMsg({ type: 'error', text: err.message || 'Failed to load asset & license data' })
    } finally {
      if (alive.current) setLoading(false)
    }
  }

  useEffect(() => {
    alive.current = true
    loadData()
    return () => { alive.current = false }
  }, [])

  // Devices and contacts are only needed inside the form. Loading them on
  // modal open keeps a page view from paying for a 200-device list and a
  // directory that might be far larger.
  useEffect(() => {
    if (!isAssetModalOpen) return
    let stopped = false
    // Contacts are optional: before a directory sync has run there are none,
    // and the PIC field has to stay usable as free text in that state.
    api.getDevices(200, 0)
      .then((res) => { if (!stopped) setDevices(res.devices || []) })
      .catch(() => { if (!stopped) setDevices([]) })
    api.getDirectoryContacts()
      .then((list: DirectoryContactDTO[]) => { if (!stopped) setContacts(list || []) })
      .catch(() => { if (!stopped) setContacts([]) })
    return () => { stopped = true }
  }, [isAssetModalOpen])

  const openCreateAsset = () => {
    setEditingAsset(null)
    setNewAsset(emptyAssetForm())
    setIsAssetModalOpen(true)
  }

  // A PIC can be typed by hand before any sync has run, or can name somebody
  // who has since been deactivated. The server stores the name, not an id, so
  // the name must survive a round trip even when it matches no contact — the
  // dropdown gets an explicit option for it. Without that, the browser falls
  // back to the first option ("Unassigned") while the state still holds the
  // old name, and one Save silently clears the PIC.
  const unmatchedPic = newAsset.assigned_user
    && !contacts.some((c) => c.display_name === newAsset.assigned_user)
    ? newAsset.assigned_user
    : null

  // The modal creates and edits from the same state, so it has to be reset on
  // open — not just on submit. Opening it raw after cancelling a half-typed
  // licence showed the previous operator's text as if it were saved data.
  const openCreateLicense = () => {
    setNewLicense(emptyLicenseForm())
    setIsLicenseModalOpen(true)
  }

  const openEditAsset = (a: HardwareAssetDTO) => {
    setEditingAsset(a)
    setNewAsset({
      asset_tag: a.asset_tag,
      device_id: a.device_id ?? null,
      model_name: a.model_name,
      serial_number: a.serial_number,
      vendor: a.vendor,
      site: a.site,
      department: a.department,
      assigned_user: a.assigned_user,
      purchase_date: a.purchase_date ?? null,
      purchase_cost: a.purchase_cost,
      warranty_expires_at: a.warranty_expires_at ?? null,
      status: a.status,
      notes: a.notes,
    })
    setIsAssetModalOpen(true)
  }

  const closeAssetModal = () => {
    setIsAssetModalOpen(false)
    setEditingAsset(null)
    prefetchAbort.current?.abort()
  }

  // Link a device and fill in what the machine already knows about itself. The
  // agent reports chassis identity (vendor, product, serial) in its inventory
  // snapshot, so the operator should not have to retype what a barcode scan
  // already knows.
  //
  // Only fills fields that are still empty: an operator who has already typed
  // the model does not lose it to a snapshot that happened to land late. The
  // abort is what makes "late" harmless — without it, a slow response for the
  // previously selected device can overwrite the current selection.
  const prefetchAbort = useRef<AbortController | null>(null)
  const handleDeviceChange = useCallback(async (deviceId: string) => {
    prefetchAbort.current?.abort()
    setNewAsset((prev) => ({ ...prev, device_id: deviceId || null }))

    // "Not linked" clears the hint without waiting on anything. It has to be
    // reset here rather than left to the fetch's `finally`: that branch is
    // skipped once the abort above fires, so with no new request to own the
    // flag it would stay true until the modal closed.
    setPrefilling(false)
    if (!deviceId) return
    const controller = new AbortController()
    prefetchAbort.current = controller
    setPrefilling(true)
    try {
      const inv = await api.getDeviceInventory(deviceId)
      if (controller.signal.aborted) return
      const model = inv.hw?.model
      if (!model) return
      setNewAsset((prev) => ({
        ...prev,
        vendor: prev.vendor || model.vendor || '',
        model_name: prev.model_name || model.product || '',
        serial_number: prev.serial_number || model.serial_number || '',
      }))
    } catch {
      // A device with no snapshot yet, or an offline one, is an ordinary
      // state — the operator types the fields by hand. Not worth a banner.
    } finally {
      if (!controller.signal.aborted) setPrefilling(false)
    }
  }, [])

  const handleSaveAsset = async (e: React.FormEvent) => {
    e.preventDefault()
    const isEdit = editingAsset !== null
    // Clear first, so a failure shows only its own message. Without this the
    // previous action's "registered successfully" banner is still on screen
    // behind the new one and reads as a second confirmation.
    setMsg(null)
    try {
      if (isEdit) {
        await api.updateAsset(editingAsset.id, newAsset)
        const infoText = `Hardware Asset '${newAsset.asset_tag}' updated.`
        setMsg({ type: 'success', text: infoText })
        toast.success(infoText, 'Asset Updated')
      } else {
        await api.createAsset(newAsset)
        const successText = `Hardware Asset '${newAsset.asset_tag}' registered successfully.`
        setMsg({ type: 'success', text: successText })
        toast.success(successText, 'Asset Registered')
      }
      closeAssetModal()
      loadData()
    } catch (err: any) {
      const errorText = err.message || (isEdit ? 'Failed to update asset' : 'Failed to register asset')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, isEdit ? 'Update Failed' : 'Registration Failed')
    }
  }

  const handleDeleteAsset = async () => {
    if (!assetPendingDelete) return
    const { id, tag } = assetPendingDelete
    setDeletingAsset(true)
    setMsg(null)
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
    setMsg(null)
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
            <button type="button" className="btn btn-primary" onClick={openCreateAsset}>
              <Plus size={16} />
              <span>Register Hardware Asset</span>
            </button>
          )}
          {subTab === 'licenses' && canAdmin && (
            <button type="button" className="btn btn-primary" onClick={openCreateLicense}>
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
                  <th>Linked Device</th>
                  <th>Model & Vendor</th>
                  <th>Serial Number</th>
                  <th>Department & Site</th>
                  <th>PIC</th>
                  <th>Valuation</th>
                  <th>Status</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={9} className="text-center py-8 text-muted">
                      Loading hardware assets…
                    </td>
                  </tr>
                ) : assets.length === 0 ? (
                  <tr>
                    <td colSpan={9} className="text-center py-8 text-muted">
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
                        {a.device_hostname ? (
                          <span className="font-mono text-sm">{a.device_hostname}</span>
                        ) : (
                          <span className="text-sm text-dim">Not linked</span>
                        )}
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
                        <div className="action-buttons">
                          {canRegister && (
                            <button
                              type="button"
                              className="btn-action"
                              onClick={() => openEditAsset(a)}
                              title="Edit this asset"
                            >
                              <Pencil size={14} />
                              <span>Edit</span>
                            </button>
                          )}
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
                        </div>
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
                {loading ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      Loading license compliance…
                    </td>
                  </tr>
                ) : compliance.length === 0 ? (
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

      {/* Register / Edit Asset Modal */}
      <Modal
        open={isAssetModalOpen}
        onClose={closeAssetModal}
        title={editingAsset ? `Edit Asset ${editingAsset.asset_tag}` : 'Register Physical Hardware Asset'}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={closeAssetModal}>
              Cancel
            </button>
            <button type="submit" form="asset-form" className="btn btn-primary">
              {editingAsset ? 'Save Changes' : 'Save Asset'}
            </button>
          </>
        }
      >
        <form id="asset-form" onSubmit={handleSaveAsset}>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-device">
              Linked Device (registered agent)
            </label>
            <select
              id="asset-device"
              className="form-select"
              value={newAsset.device_id || ''}
              onChange={(e) => handleDeviceChange(e.target.value)}
            >
              <option value="">Not linked</option>
              {devices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.hostname} — {d.os_name}
                  {d.site ? ` (${d.site})` : ''}
                </option>
              ))}
            </select>
            <span className="form-hint">
              {prefilling
                ? 'Reading the device inventory…'
                : 'Vendor, model and serial fill themselves in from the agent inventory.'}
            </span>
          </div>
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
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-vendor">
                Vendor
              </label>
              <input
                id="asset-vendor"
                type="text"
                className="form-input"
                placeholder="e.g. Dell Inc."
                value={newAsset.vendor}
                onChange={(e) => setNewAsset({ ...newAsset, vendor: e.target.value })}
              />
            </div>
            <div className="form-field">
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
              <label className="form-label" htmlFor="asset-pic">
                PIC (Person in Charge)
              </label>
              {contacts.length > 0 ? (
                <select
                  id="asset-pic"
                  className="form-select"
                  value={newAsset.assigned_user}
                  onChange={(e) => setNewAsset({ ...newAsset, assigned_user: e.target.value })}
                >
                  <option value="">Unassigned</option>
                  {contacts.map((c) => (
                    <option key={c.id} value={c.display_name}>
                      {c.display_name}
                      {c.department ? ` — ${c.department}` : ''}
                    </option>
                  ))}
                  {unmatchedPic && (
                    <option value={unmatchedPic}>
                      {unmatchedPic} — not in the synced directory
                    </option>
                  )}
                </select>
              ) : (
                <input
                  id="asset-pic"
                  type="text"
                  className="form-input"
                  placeholder="No directory synced yet — type a name"
                  value={newAsset.assigned_user}
                  onChange={(e) => setNewAsset({ ...newAsset, assigned_user: e.target.value })}
                />
              )}
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
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-site">
                Site
              </label>
              <input
                id="asset-site"
                type="text"
                className="form-input"
                placeholder="e.g. Kantor Pusat"
                value={newAsset.site}
                onChange={(e) => setNewAsset({ ...newAsset, site: e.target.value })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-status">
                Status
              </label>
              <select
                id="asset-status"
                className="form-select"
                value={newAsset.status}
                onChange={(e) => setNewAsset({ ...newAsset, status: e.target.value as HardwareAssetDTO['status'] })}
              >
                {ASSET_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-purchased">
                Purchase Date
              </label>
              <input
                id="asset-purchased"
                type="date"
                className="form-input"
                value={toDateInput(newAsset.purchase_date)}
                onChange={(e) => setNewAsset({ ...newAsset, purchase_date: fromDateInput(e.target.value) })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-warranty">
                Warranty Expires
              </label>
              <input
                id="asset-warranty"
                type="date"
                className="form-input"
                value={toDateInput(newAsset.warranty_expires_at)}
                onChange={(e) => setNewAsset({ ...newAsset, warranty_expires_at: fromDateInput(e.target.value) })}
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
          <div className="form-group">
            <label className="form-label" htmlFor="asset-notes">
              Notes
            </label>
            <textarea
              id="asset-notes"
              className="form-textarea"
              rows={2}
              placeholder="Anything worth recording about this asset"
              value={newAsset.notes}
              onChange={(e) => setNewAsset({ ...newAsset, notes: e.target.value })}
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
