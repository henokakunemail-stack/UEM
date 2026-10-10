import React, { useEffect, useState } from 'react'
import {
  CheckCircle2,
  CloudUpload,
  Globe,
  Layers,
  Plus,
  Power,
  RefreshCw,
  Shield,
  Target,
  Trash2,
  X,
} from 'lucide-react'
import { api } from '../services/api'
import { usePermission } from '../hooks/usePermission'
import { DataTable } from '../components/ui/DataTable'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Modal } from '../components/ui/Modal'
import { useToast } from '../context/ToastContext'
import type { DeviceDTO, DeviceFilterStateDTO, DeviceGroupDTO, FilterPolicyDTO, FilterRuleDTO } from '../types/api'

// The agent reports one of five states, and two of them mean the endpoint is NOT
// enforcing what the console's rule table says it is. A bare token would leave an
// operator to guess; the label carries the consequence instead.
const STATUS_LABEL: Record<string, string> = {
  synced: 'Enforcing',
  degraded: 'Partial — host file only',
  pending: 'Awaiting agent',
  failed: 'Failed',
}

// 'synced' is rendered plain rather than green: it is the absence of a problem, and
// a green tick on a device that has simply never been synced is the exact false
// reassurance this column exists to remove.
//
// No 'tampered': nothing in the agent detects a hand-edited hosts section, so the
// server had no code path that could ever produce it. It was a status an operator
// would look for on a tampered endpoint and never find.
const STATUS_CLASS: Record<string, string> = {
  synced: 'text-muted',
  degraded: 'text-warning',
  pending: 'text-muted',
  failed: 'text-danger',
}

function renderStatus(s?: DeviceFilterStateDTO) {
  const key = s?.status || 'pending'
  const label = STATUS_LABEL[key] ?? key
  // The agent's own explanation of what it could not do, shown on hover. Without
  // it 'Partial' does not tell the operator whether to fix elevation or to fix a
  // typo in a domain name.
  const reason = s?.error_message || ''
  return (
    <span className={`text-sm ${STATUS_CLASS[key] ?? 'text-muted'}`} title={reason}>
      {label}
      {key === 'degraded' && reason ? (
        <span className="block text-xs text-muted truncate max-w-xs">{reason}</span>
      ) : null}
    </span>
  )
}

// One scope picker, used by both the create and the re-target form.
//
// Re-targeting used to be a window.prompt asking for a raw device or group id.
// That is the id from the API, not a name an operator recognises, so the only way
// to use it was to go and read the id off another screen and paste it -- with no
// way to tell a typo from a wrong machine except the server answering 400. The
// create form already had the right control and did not share it.
const PolicyScopeFields: React.FC<{
  targetType: string
  targetId: string
  groups: DeviceGroupDTO[]
  devices: DeviceDTO[]
  idPrefix: string
  onChange: (next: { target_type: string; target_id: string }) => void
}> = ({ targetType, targetId, groups, devices, idPrefix, onChange }) => (
  <>
    <div className="form-group">
      <label className="form-label" htmlFor={`${idPrefix}-scope`}>
        Scope
      </label>
      <select
        id={`${idPrefix}-scope`}
        className="form-select"
        value={targetType}
        onChange={(e) => onChange({ target_type: e.target.value, target_id: '' })}
      >
        <option value="all">Entire Fleet (All Devices)</option>
        <option value="group">Device Group</option>
        <option value="device">Single Device</option>
      </select>
    </div>

    {targetType === 'group' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-group`}>
          Device Group
        </label>
        <select
          id={`${idPrefix}-group`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: targetType, target_id: e.target.value })}
        >
          <option value="">-- Select Group --</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name} ({g.member_count} device{g.member_count === 1 ? '' : 's'})
            </option>
          ))}
        </select>
        {groups.length === 0 && (
          <p className="form-hint">No device groups defined. Create one under Devices first.</p>
        )}
      </div>
    )}

    {targetType === 'device' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-device`}>
          Device
        </label>
        <select
          id={`${idPrefix}-device`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: targetType, target_id: e.target.value })}
        >
          <option value="">-- Select Device --</option>
          {devices.map((d) => (
            <option key={d.id} value={d.id}>
              {d.hostname} ({d.os_name}, {d.status})
            </option>
          ))}
        </select>
      </div>
    )}
  </>
)

// The server owns the policy model: a policy carries the target scope and
// holds rules, and enforcement is pushed per device rather than fleet-wide.
// This page therefore selects a policy, edits its rules, and syncs devices
// individually — which is what the API can actually do.
export const NetworkFilterPage: React.FC = () => {
  // Server-side: policy/rule create, update and delete are RoleAdmin; the
  // per-device filter sync is RoleTechnician. Reads are open to any
  // authenticated user.
  const { can } = usePermission()
  const canAdmin = can('admin')
  const canSync = can('technician')

  const [policies, setPolicies] = useState<FilterPolicyDTO[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [rules, setRules] = useState<FilterRuleDTO[]>([])
  const [devices, setDevices] = useState<DeviceDTO[]>([])
  // Device groups are the scope a group policy resolves against server-side, so
  // they have to be listed here for the operator to pick one. The page never
  // loaded them, which is why the target had to be a hand-typed device UUID.
  const [groups, setGroups] = useState<DeviceGroupDTO[]>([])
  const [states, setStates] = useState<Record<string, DeviceFilterStateDTO>>({})
  // Filter state is only exposed per device and has no fleet-wide endpoint, so
  // it is fetched one request at a time. The old code folded every failure
  // into a missing row, which rendered as "0 rules / never reported" — a
  // device whose state read 403s looks identical to a device that was never
  // synced. Track the outcome of each read instead.
  const [stateErrors, setStateErrors] = useState<Record<string, string>>({})
  const [statesLoading, setStatesLoading] = useState(false)
  const [loading, setLoading] = useState(true)
  const [syncingId, setSyncingId] = useState<string | null>(null)
  const [isPolicyModalOpen, setIsPolicyModalOpen] = useState(false)
  // Re-targeting an existing policy: which row is open, and the scope chosen for
  // it. Seeding from the policy's current scope means Cancel really does cancel
  // and reopening shows what is actually in effect.
  const [retargeting, setRetargeting] = useState<FilterPolicyDTO | null>(null)
  const [retargetForm, setRetargetForm] = useState({ target_type: 'all', target_id: '' })
  const [isRuleModalOpen, setIsRuleModalOpen] = useState(false)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [policyPendingDelete, setPolicyPendingDelete] = useState<{
    id: string
    name: string
    rules_count: number
  } | null>(null)
  const [rulePendingDelete, setRulePendingDelete] = useState<{ id: string; pattern: string } | null>(null)
  const [deleting, setDeleting] = useState(false)
  const toast = useToast()

  const [newPolicy, setNewPolicy] = useState({
    name: '',
    description: '',
    target_type: 'all',
    target_id: '',
  })

  const [newRule, setNewRule] = useState({
    pattern: '',
    category: 'Security & Phishing',
  })

  const selected = policies.find((p) => p.id === selectedId) || null

  const loadPolicies = async () => {
    const list = await api.getFilterPolicies()
    setPolicies(list)
    // Keep a selection alive across refreshes; fall back to the first policy so
    // the rules table is never orphaned from its header.
    setSelectedId((prev) => (prev && list.some((p) => p.id === prev) ? prev : list[0]?.id ?? null))
  }

  const loadDevices = async () => {
    const res = await api.getDevices(100, 0)
    const list = res.devices || []
    setDevices(list)
    return list
  }

  const loadGroups = async () => {
    // A failure here must not take the page down: policies that target the whole
    // fleet do not need a group list, and the page still has to render those.
    try {
      setGroups(await api.getDeviceGroups())
    } catch {
      setGroups([])
    }
  }

  const loadStates = async (list: DeviceDTO[]) => {
    // One request per device. The server exposes filter state per device and
    // has no fleet-wide equivalent, so this is the only way to see it.
    setStatesLoading(true)
    try {
      const entries = await Promise.all(
        list.map(async (d) => {
          try {
            return [d.id, await api.getDeviceFilterState(d.id), null] as const
          } catch (err: any) {
            return [d.id, null, err?.message || 'Failed to read filter state'] as const
          }
        })
      )
      const next: Record<string, DeviceFilterStateDTO> = {}
      const failed: Record<string, string> = {}
      for (const [id, s, err] of entries) {
        if (s) next[id] = s
        else failed[id] = err
      }
      setStates(next)
      setStateErrors(failed)
    } finally {
      setStatesLoading(false)
    }
  }

  const loadRules = async (policyId: string | null) => {
    if (!policyId) {
      setRules([])
      return
    }
    setRules(await api.getFilterRules(policyId))
  }

  const loadData = async () => {
    setLoading(true)
    try {
      const [, devList] = await Promise.all([loadPolicies(), loadDevices(), loadGroups()])
      await loadStates(devList)
    } catch (err: any) {
      setMsg({ type: 'error', text: err.message || 'Failed to load filter data' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  useEffect(() => {
    loadRules(selectedId).catch(() => setRules([]))
  }, [selectedId])

  const handleCreatePolicy = async (e: React.FormEvent) => {
    e.preventDefault()
    // A scope of group or device with no target compiles to zero devices. The
    // server rejects it, but catching it here means the form says why instead of
    // surfacing a bare 400 after the operator has already written a name.
    if (newPolicy.target_type !== 'all' && !newPolicy.target_id) {
      const text =
        newPolicy.target_type === 'group'
          ? 'Choose the device group this policy applies to.'
          : 'Choose the device this policy applies to.'
      setMsg({ type: 'error', text })
      toast.error(text, 'Select a Target')
      return
    }
    try {
      const created = await api.createFilterPolicy(newPolicy)
      const text = `Policy '${created.name}' created.`
      setMsg({ type: 'success', text })
      toast.success(text, 'Policy Created')
      setIsPolicyModalOpen(false)
      setNewPolicy({ name: '', description: '', target_type: 'all', target_id: '' })
      await loadPolicies()
      setSelectedId(created.id)
    } catch (err: any) {
      const text = err.message || 'Failed to create policy'
      setMsg({ type: 'error', text })
      toast.error(text, 'Create Failed')
    }
  }

  const handleTogglePolicy = async (p: FilterPolicyDTO) => {
    try {
      await api.updateFilterPolicy(p.id, { is_enabled: !p.is_enabled })
      const text = `Policy '${p.name}' ${p.is_enabled ? 'disabled' : 'enabled'}.`
      setMsg({ type: 'success', text })
      toast.info(text)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || 'Failed to update policy'
      setMsg({ type: 'error', text })
      toast.error(text)
    }
  }

  const handleDeletePolicy = async () => {
    if (!policyPendingDelete) return
    const { id, name } = policyPendingDelete
    setDeleting(true)
    try {
      await api.deleteFilterPolicy(id)
      const text = `Policy '${name}' deleted.`
      setMsg({ type: 'success', text })
      toast.success(text)
      setPolicyPendingDelete(null)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || 'Failed to delete policy'
      setMsg({ type: 'error', text })
      toast.error(text)
    } finally {
      setDeleting(false)
    }
  }

  const handleCreateRule = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selected) return
    try {
      await api.createFilterRule(selected.id, newRule)
      const text = `Rule for '${newRule.pattern}' added to '${selected.name}'.`
      setMsg({ type: 'success', text })
      toast.success(text)
      setIsRuleModalOpen(false)
      setNewRule({
        pattern: '',
        category: 'Security & Phishing',
      })
      await loadRules(selected.id)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || 'Failed to create rule'
      setMsg({ type: 'error', text })
      toast.error(text)
    }
  }

  // Re-targeting an existing policy previously required deleting and recreating it,
  // which also discarded its rules. The server has always accepted target_type and
  // target_id on an update, so this only exposes what the API can already do.
  const handleEditTargetSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!retargeting) return
    try {
      // Both fields go together. Sending only target_id left target_type behind, so
      // a policy aimed at a device could be re-pointed at a group id while still
      // typed 'device', and the server would look for a device with that id and 400.
      await api.updateFilterPolicy(retargeting.id, {
        target_type: retargetForm.target_type,
        target_id: retargetForm.target_id,
      })
      const scope =
        retargetForm.target_type === 'all'
          ? 'the entire fleet'
          : retargetForm.target_type === 'group'
            ? `group '${groupName(retargetForm.target_id)}'`
            : `'${deviceName(retargetForm.target_id)}'`
      const text = `Policy '${retargeting.name}' now applies to ${scope}. Changes dispatched to online devices.`
      setMsg({ type: 'success', text })
      toast.success(text, 'Policy Re-targeted')
      setRetargeting(null)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || 'Failed to update the policy target'
      setMsg({ type: 'error', text })
      toast.error(text, 'Update Failed')
    }
  }

  const handleDeleteRule = async () => {
    if (!rulePendingDelete) return
    const { id, pattern } = rulePendingDelete
    setDeleting(true)
    try {
      await api.deleteFilterRule(id)
      const text = `Rule '${pattern}' deleted. Access restored on online devices.`
      setMsg({ type: 'success', text })
      toast.info(text)
      setRulePendingDelete(null)
      await loadRules(selectedId)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || 'Failed to delete rule'
      setMsg({ type: 'error', text })
      toast.error(text)
    } finally {
      setDeleting(false)
    }
  }

  const handleSync = async (d: DeviceDTO) => {
    setSyncingId(d.id)
    try {
      const res = await api.syncDeviceFilter(d.id)
      const text =
        res.dispatched
          ? `Pushed ${res.effective_rules} rule(s) to ${d.hostname} (version ${res.policy_version.slice(0, 12)}).`
          : // True now, but previously it was an unbacked claim: the row said 'pending'
            // and nothing re-read it. The server re-sends the policy on reconnect when
            // the device's reported version is not the current one.
            `${d.hostname} is offline — the policy will be pushed when it reconnects.`
      setMsg({ type: 'success', text })
      toast.success(text, 'Filter Sync')
      setStates((prev) => ({
        ...prev,
        [d.id]: { ...(prev[d.id] || ({} as DeviceFilterStateDTO)), device_id: d.id, policy_version: res.policy_version, status: res.status },
      }))
      // A successful sync is proof the read path works for this device, so a
      // stale read error on the row would contradict the row's own values.
      setStateErrors((prev) => {
        if (!(d.id in prev)) return prev
        const next = { ...prev }
        delete next[d.id]
        return next
      })
    } catch (err: any) {
      const text = err.message || 'Failed to sync device'
      setMsg({ type: 'error', text })
      toast.error(text, 'Sync Failed')
    } finally {
      setSyncingId(null)
    }
  }

  // The policy table showed a raw scope token and a raw UUID, so an operator could
  // not tell which machine a policy aimed at. Resolve both to names.
  const groupName = (id: string) => groups.find((g) => g.id === id)?.name ?? id
  const deviceName = (id: string) => devices.find((d) => d.id === id)?.hostname ?? id
  const scopeLabel = (p: FilterPolicyDTO) => {
    if (p.target_type === 'group') return groupName(p.target_id)
    if (p.target_type === 'device') return deviceName(p.target_id)
    return 'All devices'
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">Network &amp; Web Security Filter</h2>
          <p className="page-subtitle">
            DNS sinkholing, corporate domain blocking, and per-device policy enforcement
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>Refresh</span>
          </button>
          {canAdmin && (
            <button type="button" className="btn btn-secondary" onClick={() => setIsPolicyModalOpen(true)}>
              <Layers size={16} />
              <span>New Policy</span>
            </button>
          )}
          {canAdmin && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => selected && setIsRuleModalOpen(true)}
              disabled={!selected}
              title={selected ? `Add a rule to ${selected.name}` : 'Create a policy first'}
            >
              <Plus size={16} />
              <span>Add Rule</span>
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

      {/* Policy list */}
      <div className="table-card">
        <div className="card-header-bar">
          <div className="card-title-group">
            <Shield className="text-primary" size={18} />
            <h3 className="card-title">Filter Policies ({policies.length})</h3>
          </div>
          <span className="text-sm text-dim">
            A rule only exists inside a policy — pick one to edit its rules
          </span>
        </div>

        <DataTable label="Filter policies">
          <table className="data-table">
            <thead>
              <tr>
                <th>Policy</th>
                <th>Scope</th>
                <th>Rules</th>
                <th>Status</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {policies.length === 0 ? (
                <tr>
                  <td colSpan={5} className="text-center py-8 text-muted">
                    No policies yet. Create one to start blocking domains.
                  </td>
                </tr>
              ) : (
                policies.map((p) => (
                  <tr
                    key={p.id}
                    className={p.id === selectedId ? 'row-selected' : ''}
                    onClick={() => setSelectedId(p.id)}
                    style={{ cursor: 'pointer' }}
                  >
                    <td>
                      <div className="font-semibold text-main">{p.name}</div>
                      {p.description && <div className="text-sm text-dim">{p.description}</div>}
                    </td>
                    <td>
                      <div className="font-semibold text-main">{scopeLabel(p)}</div>
                      <div className="text-sm text-dim">
                        {p.target_type === 'all' ? 'Fleet-wide' : p.target_type}
                      </div>
                    </td>
                    <td>
                      <span className="font-mono">{p.rules_count}</span>
                    </td>
                    <td>
                      <span className={`status-pill ${p.is_enabled ? 'online' : 'offline'}`}>
                        {p.is_enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      {canAdmin && (
                        <div className="action-buttons">
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => {
                              setRetargetForm({ target_type: p.target_type, target_id: p.target_id })
                              setRetargeting(p)
                            }}
                            title={`Currently targets ${scopeLabel(p)}`}
                          >
                            <Target size={14} />
                            <span>Re-target</span>
                          </button>
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => handleTogglePolicy(p)}
                            title={p.is_enabled ? 'Disable this policy' : 'Enable this policy'}
                          >
                            <Power size={14} />
                            <span>{p.is_enabled ? 'Disable' : 'Enable'}</span>
                          </button>
                          <button
                            type="button"
                            className="btn-action text-danger"
                            onClick={() =>
                              setPolicyPendingDelete({ id: p.id, name: p.name, rules_count: p.rules_count })
                            }
                            aria-label={`Delete policy ${p.name}`}
                          >
                            <Trash2 size={14} />
                            <span>Delete</span>
                          </button>
                        </div>
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </DataTable>
      </div>

      {/* Rules in the selected policy */}
      <div className="table-card">
        <div className="card-header-bar">
          <div className="card-title-group">
            <Globe className="text-primary" size={18} />
            <h3 className="card-title">
              Rules {selected ? `in "${selected.name}"` : ''} ({rules.length})
            </h3>
          </div>
          {selected && (
            <span className="text-sm text-dim">
              Every rule below is compiled and pushed to agents, along with the firewall rules
              derived from each domain
            </span>
          )}
        </div>

        <DataTable label="Policy rules">
          <table className="data-table">
            <thead>
              <tr>
                <th>Domain / Hostname Pattern</th>
                <th>Category</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {!selected ? (
                <tr>
                  <td colSpan={3} className="text-center py-8 text-muted">
                    Select a policy above to see its rules.
                  </td>
                </tr>
              ) : rules.length === 0 ? (
                <tr>
                  <td colSpan={3} className="text-center py-8 text-muted">
                    This policy has no rules yet. Use &quot;Add Rule&quot; to block a domain.
                  </td>
                </tr>
              ) : (
                rules.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <div className="font-mono font-semibold text-main">{r.pattern}</div>
                    </td>
                    <td>{r.category || 'General'}</td>
                    <td>
                      {canAdmin && (
                        <button
                          type="button"
                          className="btn-action text-danger"
                          onClick={() => setRulePendingDelete({ id: r.id, pattern: r.pattern })}
                          aria-label={`Delete rule ${r.pattern}`}
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

      {/* Per-device enforcement */}
      <div className="table-card">
        <div className="card-header-bar">
          <div className="card-title-group">
            <CheckCircle2 className="text-success" size={18} />
            <h3 className="card-title">Device Enforcement</h3>
          </div>
          <span className="text-sm text-dim">
            {statesLoading
              ? `Reading filter state for ${devices.length} device(s)...`
              : Object.keys(stateErrors).length > 0
                ? `${Object.keys(stateErrors).length} of ${devices.length} device state read(s) failed`
                : 'Sync compiles every enabled policy that targets the device and pushes it over the live socket'}
          </span>
        </div>

        <DataTable label="Device enforcement">
          <table className="data-table">
            <thead>
              <tr>
                <th>Device</th>
                <th>Site</th>
                <th>Agent Policy Version</th>
                <th>Rules Applied</th>
                <th>Status</th>
                <th>Last Report</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {devices.length === 0 ? (
                <tr>
                  <td colSpan={7} className="text-center py-8 text-muted">
                    No devices enrolled yet.
                  </td>
                </tr>
              ) : (
                devices.map((d) => {
                  const s = states[d.id]
                  const stateError = stateErrors[d.id]
                  return (
                    <tr key={d.id}>
                      <td>
                        <span className="font-semibold text-main">{d.hostname}</span>
                      </td>
                      <td>{d.site || '—'}</td>
                      {/* A failed read is reported as a failure on the row, not
                          rendered as the zero/never values a never-synced device
                          would legitimately show. */}
                      {stateError ? (
                        <td colSpan={4} className="text-sm text-danger">
                          Filter state unavailable: {stateError}
                        </td>
                      ) : (
                        <>
                          <td>
                            <span className="font-mono text-sm">
                              {s && s.policy_version && s.policy_version !== 'none'
                                ? s.policy_version.slice(0, 14) + '...'
                                : '—'}
                            </span>
                          </td>
                          <td>
                            <span className="font-mono">{s?.rules_applied ?? 0}</span>
                          </td>
                          <td>{renderStatus(s)}</td>
                          <td className="text-sm text-muted">
                            {s?.last_applied_at ? new Date(s.last_applied_at).toLocaleString() : '—'}
                          </td>
                        </>
                      )}
                      <td>
                        {canSync && (
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => handleSync(d)}
                            disabled={syncingId === d.id}
                            title="Compile and push the effective rule set to this device"
                          >
                            <CloudUpload size={14} className={syncingId === d.id ? 'spinning' : ''} />
                            <span>{syncingId === d.id ? 'Syncing...' : 'Sync Now'}</span>
                          </button>
                        )}
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </DataTable>
      </div>

      {/* New Policy Modal */}
      <Modal
        open={isPolicyModalOpen}
        onClose={() => setIsPolicyModalOpen(false)}
        title="New Filter Policy"
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsPolicyModalOpen(false)}
            >
              Cancel
            </button>
            <button type="submit" form="filter-policy-form" className="btn btn-primary">
              Create Policy
            </button>
          </>
        }
      >
        <form id="filter-policy-form" onSubmit={handleCreatePolicy}>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-policy-name">
              Policy Name
            </label>
            <input
              id="filter-policy-name"
              type="text"
              className="form-input"
              required
              placeholder="e.g. Corporate Baseline"
              value={newPolicy.name}
              onChange={(e) => setNewPolicy({ ...newPolicy, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-policy-desc">
              Description
            </label>
            <input
              id="filter-policy-desc"
              type="text"
              className="form-input"
              placeholder="What this policy is for"
              value={newPolicy.description}
              onChange={(e) => setNewPolicy({ ...newPolicy, description: e.target.value })}
            />
          </div>
          <PolicyScopeFields
            targetType={newPolicy.target_type}
            targetId={newPolicy.target_id}
            groups={groups}
            devices={devices}
            idPrefix="filter-policy"
            onChange={(next) => setNewPolicy({ ...newPolicy, ...next })}
          />
        </form>
      </Modal>

      {/* Re-target Modal */}
      <Modal
        open={retargeting !== null}
        onClose={() => setRetargeting(null)}
        title="Change Policy Scope"
        description={retargeting ? `"${retargeting.name}" — currently ${scopeLabel(retargeting)}` : undefined}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setRetargeting(null)}>
              Cancel
            </button>
            <button type="submit" form="filter-retarget-form" className="btn btn-primary">
              Apply Scope
            </button>
          </>
        }
      >
        <form id="filter-retarget-form" onSubmit={handleEditTargetSubmit}>
          <PolicyScopeFields
            targetType={retargetForm.target_type}
            targetId={retargetForm.target_id}
            groups={groups}
            devices={devices}
            idPrefix="filter-retarget"
            onChange={(next) => setRetargetForm(next)}
          />
          <p className="form-hint">
            The policy's rules are kept. Devices keep the rules they already have until their next
            sync.
          </p>
        </form>
      </Modal>

      {/* Add Rule Modal */}
      <Modal
        open={isRuleModalOpen && selected !== null}
        onClose={() => setIsRuleModalOpen(false)}
        title="Add Rule"
        size="md"
        description={selected ? `to "${selected.name}"` : undefined}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsRuleModalOpen(false)}
            >
              Cancel
            </button>
            <button type="submit" form="filter-rule-form" className="btn btn-primary">
              Add Rule
            </button>
          </>
        }
      >
        <form id="filter-rule-form" onSubmit={handleCreateRule}>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-rule-pattern">
              Domain or Hostname
            </label>
            <input
              id="filter-rule-pattern"
              type="text"
              className="form-input"
              required
              placeholder="e.g. gambling-site.com or malware.tracker.net"
              value={newRule.pattern}
              onChange={(e) => setNewRule({ ...newRule, pattern: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-rule-category">
              Category
            </label>
            <select
              id="filter-rule-category"
              className="form-select"
              value={newRule.category}
              onChange={(e) => setNewRule({ ...newRule, category: e.target.value })}
            >
              <option value="Security & Phishing">Security &amp; Phishing</option>
              <option value="Adult & Gambling">Adult &amp; Gambling</option>
              <option value="Bandwidth Heavy / Streaming">Bandwidth Heavy / Streaming</option>
              <option value="Social Media">Social Media</option>
              <option value="Custom Policy">Custom Corporate Policy</option>
            </select>
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={policyPendingDelete !== null}
        title="Delete filter policy"
        message={
          policyPendingDelete
            ? `Delete policy '${policyPendingDelete.name}' and its ${policyPendingDelete.rules_count} rule(s)? Devices keep their current rule set until their next sync.`
            : ''
        }
        confirmLabel="Delete"
        pending={deleting}
        onConfirm={handleDeletePolicy}
        onCancel={() => setPolicyPendingDelete(null)}
      />

      <ConfirmDialog
        open={rulePendingDelete !== null}
        title="Delete filter rule"
        message={
          rulePendingDelete
            ? `Delete rule '${rulePendingDelete.pattern}'? Devices keep the old list until their next sync.`
            : ''
        }
        confirmLabel="Delete"
        pending={deleting}
        onConfirm={handleDeleteRule}
        onCancel={() => setRulePendingDelete(null)}
      />
    </div>
  )
}
