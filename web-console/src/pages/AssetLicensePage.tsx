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
import { settleAll } from '../services/settled'
import { usePermission } from '../hooks/usePermission'
import { useT } from '../i18n/core'
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

// Display label key per status value. The value itself is what the server
// stores; only the label is translated, at render.
const ASSET_STATUS_KEYS: Record<string, string> = {
  in_use: 'assets.status_in_use',
  in_stock: 'assets.status_in_stock',
  in_repair: 'assets.status_in_repair',
  retired: 'assets.status_retired',
  disposed: 'assets.status_disposed',
}

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
  const { t } = useT()
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

  const statusLabel = (s: string) => (ASSET_STATUS_KEYS[s] ? t(ASSET_STATUS_KEYS[s]) : s)

  // This also runs after every create/update/delete, so it outlives the mount it
  // started on. `alive` is false once the page is gone, and every setState below
  // is behind that check — setting state on an unmounted component is a wasted
  // render at best and a React warning at worst.
  const [loadFailed, setLoadFailed] = useState<string[]>([])
  const [modalDevicesFailed, setModalDevicesFailed] = useState(false)
  const alive = useRef(true)
  const loadData = async () => {
    setLoading(true)
    try {
      // A section that fails keeps what it had and is named in a banner. An
      // empty compliance list used to read as "every license compliant".
      const { data, failed } = await settleAll({
        assets: () => api.getAssets(),
        summary: () => api.getAssetSummary(),
        licenses: () => api.getLicenses(),
        compliance: () => api.getLicenseCompliance(),
      })
      if (!alive.current) return
      if (data.assets) setAssets(data.assets)
      if (data.summary) setSummary(data.summary)
      if (data.licenses) setLicenses(data.licenses)
      if (data.compliance) setCompliance(data.compliance.compliance || [])
      setLoadFailed(failed.map((k) => t(`assets.section_${k}`)))
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
    setModalDevicesFailed(false)
    api.getDevices(200, 0)
      .then((res) => { if (!stopped) setDevices(res.devices || []) })
      .catch(() => {
        if (stopped) return
        setDevices([])
        setModalDevicesFailed(true)
      })
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
        const infoText = t('assets.asset_updated', { tag: newAsset.asset_tag ?? '' })
        setMsg({ type: 'success', text: infoText })
        toast.success(infoText, t('assets.toast_asset_updated'))
      } else {
        await api.createAsset(newAsset)
        const successText = t('assets.asset_registered', { tag: newAsset.asset_tag ?? '' })
        setMsg({ type: 'success', text: successText })
        toast.success(successText, t('assets.toast_asset_registered'))
      }
      closeAssetModal()
      loadData()
    } catch (err: any) {
      const errorText = err.message || (isEdit ? t('assets.update_failed') : t('assets.register_failed'))
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, isEdit ? t('assets.toast_update_failed') : t('assets.toast_register_failed'))
    }
  }

  const handleDeleteAsset = async () => {
    if (!assetPendingDelete) return
    const { id, tag } = assetPendingDelete
    setDeletingAsset(true)
    setMsg(null)
    try {
      await api.deleteAsset(id)
      const infoText = t('assets.asset_removed', { tag })
      setMsg({ type: 'success', text: infoText })
      toast.info(infoText, t('assets.toast_asset_removed'))
      setAssetPendingDelete(null)
      loadData()
    } catch (err: any) {
      const errorText = err.message || t('assets.delete_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('assets.toast_delete_failed'))
    } finally {
      setDeletingAsset(false)
    }
  }

  const handleCreateLicense = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg(null)
    try {
      await api.createLicense(newLicense)
      const successText = t('assets.license_created', { name: newLicense.software_name ?? '' })
      setMsg({ type: 'success', text: successText })
      toast.success(successText, t('assets.toast_license_created'))
      setIsLicenseModalOpen(false)
      loadData()
    } catch (err: any) {
      const errorText = err.message || t('assets.license_create_failed')
      setMsg({ type: 'error', text: errorText })
      toast.error(errorText, t('assets.toast_create_failed'))
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
          <h2 className="page-title">{t('assets.title')}</h2>
          <p className="page-subtitle">
            {t('assets.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('assets.refresh')}</span>
          </button>
          {subTab === 'hardware' && canRegister && (
            <button type="button" className="btn btn-primary" onClick={openCreateAsset}>
              <Plus size={16} />
              <span>{t('assets.register_hardware')}</span>
            </button>
          )}
          {subTab === 'licenses' && canAdmin && (
            <button type="button" className="btn btn-primary" onClick={openCreateLicense}>
              <Plus size={16} />
              <span>{t('assets.add_license')}</span>
            </button>
          )}
        </div>
      </div>

      {loadFailed.length > 0 && (
        <div className="alert-banner alert-warning" role="alert">
          <span>{t('common.load_failed_sections', { sections: loadFailed.join(', ') })}</span>
        </div>
      )}

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label={t('assets.dismiss_message')}>
            <X size={14} />
          </button>
        </div>
      )}

      {/* Financial Overview Cards */}
      {summary && (
        <div className="kpi-grid">
          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('assets.kpi_valuation')}</span>
              <DollarSign className="kpi-icon text-success" size={20} />
            </div>
            <div className="kpi-value text-success">{formatCurrency(summary.total_valuation)}</div>
            <span className="kpi-hint">{t('assets.kpi_valuation_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('assets.kpi_active_hardware')}</span>
              <Laptop className="kpi-icon text-primary" size={20} />
            </div>
            <div className="kpi-value">{summary.active_assets} / {summary.total_assets}</div>
            <span className="kpi-hint">{t('assets.kpi_active_hardware_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('assets.kpi_warranty_expiring')}</span>
              <AlertTriangle className="kpi-icon text-warning" size={20} />
            </div>
            <div className="kpi-value text-warning">{summary.warranty_expiring_count}</div>
            <span className="kpi-hint">{t('assets.kpi_warranty_expiring_hint')}</span>
          </div>

          <div className="kpi-card">
            <div className="kpi-header">
              <span className="kpi-label">{t('assets.kpi_contracts')}</span>
              <Award className="kpi-icon text-dim" size={20} />
            </div>
            <div className="kpi-value">{licenses.length}</div>
            <span className="kpi-hint">{t('assets.kpi_contracts_hint')}</span>
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
          <span>{t('assets.tab_hardware')}</span>
        </button>
        <button
          type="button"
          className={`tab-btn ${subTab === 'licenses' ? 'active' : ''}`}
          onClick={() => setSubTab('licenses')}
        >
          <Key size={16} />
          <span>{t('assets.tab_licenses')}</span>
        </button>
      </div>

      {/* Tab 1: Hardware Assets */}
      {subTab === 'hardware' && (
        <div className="table-card">
          <DataTable label={t('assets.table_hardware_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('assets.col_asset_tag')}</th>
                  <th>{t('assets.col_linked_device')}</th>
                  <th>{t('assets.col_model_vendor')}</th>
                  <th>{t('assets.col_serial')}</th>
                  <th>{t('assets.col_department_site')}</th>
                  <th>{t('assets.col_pic')}</th>
                  <th>{t('assets.col_valuation')}</th>
                  <th>{t('assets.col_status')}</th>
                  <th>{t('assets.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={9} className="text-center py-8 text-muted">
                      {t('assets.loading_hardware')}
                    </td>
                  </tr>
                ) : assets.length === 0 ? (
                  <tr>
                    <td colSpan={9} className="text-center py-8 text-muted">
                      {t('assets.empty_hardware')}
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
                          <span className="text-sm text-dim">{t('assets.not_linked')}</span>
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
                      <td>{a.assigned_user || t('assets.unassigned')}</td>
                      <td>{formatCurrency(a.purchase_cost)}</td>
                      <td>
                        <span className={`status-pill ${a.status === 'in_use' ? 'online' : a.status === 'in_repair' ? 'warning' : 'offline'}`}>
                          {statusLabel(a.status)}
                        </span>
                      </td>
                      <td>
                        <div className="action-buttons">
                          {canRegister && (
                            <button
                              type="button"
                              className="btn-action"
                              onClick={() => openEditAsset(a)}
                              title={t('assets.edit_title')}
                            >
                              <Pencil size={14} />
                              <span>{t('assets.edit')}</span>
                            </button>
                          )}
                          {canAdmin && (
                            <button
                              type="button"
                              className="btn-action text-danger"
                              onClick={() => setAssetPendingDelete({ id: a.id, tag: a.asset_tag })}
                              title={t('assets.delete_title')}
                            >
                              <Trash2 size={14} />
                              <span>{t('common.delete')}</span>
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
              <h3 className="card-title">{t('assets.license_card_title')}</h3>
            </div>
            <span className="text-sm text-dim">
              {t('assets.license_card_hint')}
            </span>
          </div>
          <DataTable label={t('assets.table_license_label')}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('assets.col_software_title')}</th>
                  <th>{t('assets.col_publisher')}</th>
                  <th>{t('assets.col_license_type')}</th>
                  <th>{t('assets.col_purchased_seats')}</th>
                  <th>{t('assets.col_allocated_seats')}</th>
                  <th>{t('assets.col_installed')}</th>
                  <th>{t('assets.col_compliance_status')}</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      {t('assets.loading_licenses')}
                    </td>
                  </tr>
                ) : compliance.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="text-center py-8 text-muted">
                      {t('assets.empty_licenses')}
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
                          {t('assets.installed_devices', { count: c.installed_detected })}
                        </span>
                      </td>
                      <td>
                        {c.status === 'compliant' && (
                          <span className="status-pill online">
                            <ShieldCheck size={13} /> {t('assets.compliance_compliant')}
                          </span>
                        )}
                        {c.status === 'over_allocated' && (
                          <span className="status-pill danger" title={t('assets.compliance_over_allocated_title')}>
                            <ShieldAlert size={13} /> {t('assets.compliance_over_allocated')}
                          </span>
                        )}
                        {c.status === 'expiring_soon' && (
                          <span className="status-pill warning">
                            <AlertTriangle size={13} /> {t('assets.compliance_expiring_soon')}
                          </span>
                        )}
                        {/* The server sets 'expired' once now() is past expires_at.
                            Without this branch the cell rendered blank for the one
                            state that most needs to be visible. */}
                        {c.status === 'expired' && (
                          <span className="status-pill danger">
                            <AlertTriangle size={13} /> {t('assets.compliance_expired')}
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
        title={editingAsset ? t('assets.modal_edit_title', { tag: editingAsset.asset_tag }) : t('assets.modal_register_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={closeAssetModal}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="asset-form" className="btn btn-primary">
              {editingAsset ? t('assets.save_changes') : t('assets.save_asset')}
            </button>
          </>
        }
      >
        <form id="asset-form" onSubmit={handleSaveAsset}>
          {modalDevicesFailed && (
            <div className="alert-banner alert-warning" role="alert">
              <span>{t('assets.devices_load_failed')}</span>
            </div>
          )}
          <div className="form-group">
            <label className="form-label" htmlFor="asset-device">
              {t('assets.field_linked_device')}
            </label>
            <select
              id="asset-device"
              className="form-select"
              value={newAsset.device_id || ''}
              onChange={(e) => handleDeviceChange(e.target.value)}
            >
              <option value="">{t('assets.not_linked')}</option>
              {devices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.hostname} — {d.os_name}
                  {d.site ? ` (${d.site})` : ''}
                </option>
              ))}
            </select>
            <span className="form-hint">
              {prefilling
                ? t('assets.prefilling')
                : t('assets.prefill_hint')}
            </span>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-tag">
              {t('assets.field_asset_tag')}
            </label>
            <input
              id="asset-tag"
              type="text"
              className="form-input"
              required
              placeholder={t('assets.placeholder_asset_tag')}
              value={newAsset.asset_tag}
              onChange={(e) => setNewAsset({ ...newAsset, asset_tag: e.target.value })}
            />
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-vendor">
                {t('assets.field_vendor')}
              </label>
              <input
                id="asset-vendor"
                type="text"
                className="form-input"
                placeholder={t('assets.placeholder_vendor')}
                value={newAsset.vendor}
                onChange={(e) => setNewAsset({ ...newAsset, vendor: e.target.value })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-model">
                {t('assets.field_model')}
              </label>
              <input
                id="asset-model"
                type="text"
                className="form-input"
                required
                placeholder={t('assets.placeholder_model')}
                value={newAsset.model_name}
                onChange={(e) => setNewAsset({ ...newAsset, model_name: e.target.value })}
              />
            </div>
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="asset-serial">
              {t('assets.field_serial')}
            </label>
            <input
              id="asset-serial"
              type="text"
              className="form-input"
              placeholder={t('assets.placeholder_serial')}
              value={newAsset.serial_number}
              onChange={(e) => setNewAsset({ ...newAsset, serial_number: e.target.value })}
            />
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-pic">
                {t('assets.field_pic')}
              </label>
              {contacts.length > 0 ? (
                <select
                  id="asset-pic"
                  className="form-select"
                  value={newAsset.assigned_user}
                  onChange={(e) => setNewAsset({ ...newAsset, assigned_user: e.target.value })}
                >
                  <option value="">{t('assets.unassigned')}</option>
                  {contacts.map((c) => (
                    <option key={c.id} value={c.display_name}>
                      {c.display_name}
                      {c.department ? ` — ${c.department}` : ''}
                    </option>
                  ))}
                  {unmatchedPic && (
                    <option value={unmatchedPic}>
                      {t('assets.pic_not_in_directory', { name: unmatchedPic })}
                    </option>
                  )}
                </select>
              ) : (
                <input
                  id="asset-pic"
                  type="text"
                  className="form-input"
                  placeholder={t('assets.placeholder_pic')}
                  value={newAsset.assigned_user}
                  onChange={(e) => setNewAsset({ ...newAsset, assigned_user: e.target.value })}
                />
              )}
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-dept">
                {t('assets.field_department')}
              </label>
              <input
                id="asset-dept"
                type="text"
                className="form-input"
                placeholder={t('assets.field_department')}
                value={newAsset.department}
                onChange={(e) => setNewAsset({ ...newAsset, department: e.target.value })}
              />
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-site">
                {t('assets.field_site')}
              </label>
              <input
                id="asset-site"
                type="text"
                className="form-input"
                placeholder={t('assets.placeholder_site')}
                value={newAsset.site}
                onChange={(e) => setNewAsset({ ...newAsset, site: e.target.value })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="asset-status">
                {t('assets.col_status')}
              </label>
              <select
                id="asset-status"
                className="form-select"
                value={newAsset.status}
                onChange={(e) => setNewAsset({ ...newAsset, status: e.target.value as HardwareAssetDTO['status'] })}
              >
                {ASSET_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="asset-purchased">
                {t('assets.field_purchase_date')}
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
                {t('assets.field_warranty_expires')}
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
              {t('assets.field_purchase_valuation')}
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
              {t('assets.field_notes')}
            </label>
            <textarea
              id="asset-notes"
              className="form-textarea"
              rows={2}
              placeholder={t('assets.placeholder_notes')}
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
        title={t('assets.license_modal_title')}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setIsLicenseModalOpen(false)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="license-form" className="btn btn-primary">
              {t('assets.save_license')}
            </button>
          </>
        }
      >
        <form id="license-form" onSubmit={handleCreateLicense}>
          <div className="form-group">
            <label className="form-label" htmlFor="license-title">
              {t('assets.col_software_title')}
            </label>
            <input
              id="license-title"
              type="text"
              className="form-input"
              required
              placeholder={t('assets.placeholder_software_title')}
              value={newLicense.software_name}
              onChange={(e) => setNewLicense({ ...newLicense, software_name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="license-publisher">
              {t('assets.field_publisher')}
            </label>
            <input
              id="license-publisher"
              type="text"
              className="form-input"
              placeholder={t('assets.placeholder_publisher')}
              value={newLicense.publisher}
              onChange={(e) => setNewLicense({ ...newLicense, publisher: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="license-seats">
              {t('assets.field_total_seats')}
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
        title={t('assets.delete_dialog_title')}
        message={
          assetPendingDelete
            ? t('assets.delete_dialog_message', { tag: assetPendingDelete.tag })
            : ''
        }
        confirmLabel={t('common.delete')}
        pending={deletingAsset}
        onConfirm={handleDeleteAsset}
        onCancel={() => setAssetPendingDelete(null)}
      />
    </div>
  )
}
