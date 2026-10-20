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
import { useT } from '../i18n/core'
import type { DeviceDTO, DeviceFilterStateDTO, DeviceGroupDTO, FilterPolicyDTO, FilterRuleDTO } from '../types/api'

// The agent reports one of five states, and two of them mean the endpoint is NOT
// enforcing what the console's rule table says it is. A bare token would leave an
// operator to guess; the label carries the consequence instead.
const STATUS_LABEL: Record<string, string> = {
  synced: 'filter.status_synced',
  degraded: 'filter.status_degraded',
  pending: 'filter.status_pending',
  failed: 'filter.status_failed',
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

function renderStatus(t: (key: string) => string, s?: DeviceFilterStateDTO) {
  const key = s?.status || 'pending'
  const label = STATUS_LABEL[key] ? t(STATUS_LABEL[key]) : key
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
}> = ({ targetType, targetId, groups, devices, idPrefix, onChange }) => {
  const { t } = useT()
  return (
  <>
    <div className="form-group">
      <label className="form-label" htmlFor={`${idPrefix}-scope`}>
        {t('filter.scope_label')}
      </label>
      <select
        id={`${idPrefix}-scope`}
        className="form-select"
        value={targetType}
        onChange={(e) => onChange({ target_type: e.target.value, target_id: '' })}
      >
        <option value="all">{t('filter.scope_all')}</option>
        <option value="group">{t('filter.scope_group')}</option>
        <option value="device">{t('filter.scope_device')}</option>
      </select>
    </div>

    {targetType === 'group' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-group`}>
          {t('filter.scope_group')}
        </label>
        <select
          id={`${idPrefix}-group`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: targetType, target_id: e.target.value })}
        >
          <option value="">{t('filter.select_group_placeholder')}</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {t(g.member_count === 1 ? 'filter.group_option_one' : 'filter.group_option', { name: g.name, count: g.member_count })}
            </option>
          ))}
        </select>
        {groups.length === 0 && (
          <p className="form-hint">{t('filter.no_groups')}</p>
        )}
      </div>
    )}

    {targetType === 'device' && (
      <div className="form-group">
        <label className="form-label" htmlFor={`${idPrefix}-device`}>
          {t('filter.device_label')}
        </label>
        <select
          id={`${idPrefix}-device`}
          className="form-select"
          required
          value={targetId}
          onChange={(e) => onChange({ target_type: targetType, target_id: e.target.value })}
        >
          <option value="">{t('filter.select_device_placeholder')}</option>
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
}

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
  const { t } = useT()

  const [newPolicy, setNewPolicy] = useState<{
    name: string
    description: string
    mode: 'blacklist' | 'whitelist'
    target_type: string
    target_id: string
  }>({
    name: '',
    description: '',
    mode: 'blacklist',
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
            return [d.id, null, err?.message || t('filter.state_read_failed')] as const
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
      setMsg({ type: 'error', text: err.message || t('filter.load_failed') })
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
          ? t('filter.choose_group')
          : t('filter.choose_device')
      setMsg({ type: 'error', text })
      toast.error(text, t('filter.select_target_title'))
      return
    }
    try {
      const created = await api.createFilterPolicy(newPolicy)
      const text = t('filter.policy_created', { name: created.name })
      setMsg({ type: 'success', text })
      toast.success(text, t('filter.policy_created_title'))
      setIsPolicyModalOpen(false)
      setNewPolicy({ name: '', description: '', mode: 'blacklist', target_type: 'all', target_id: '' })
      await loadPolicies()
      setSelectedId(created.id)
    } catch (err: any) {
      const text = err.message || t('filter.create_failed')
      setMsg({ type: 'error', text })
      toast.error(text, t('filter.create_failed_title'))
    }
  }

  const handleTogglePolicy = async (p: FilterPolicyDTO) => {
    try {
      await api.updateFilterPolicy(p.id, { is_enabled: !p.is_enabled })
      const text = t(p.is_enabled ? 'filter.policy_disabled' : 'filter.policy_enabled', { name: p.name })
      setMsg({ type: 'success', text })
      toast.info(text)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || t('filter.update_failed')
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
      const text = t('filter.policy_deleted', { name })
      setMsg({ type: 'success', text })
      toast.success(text)
      setPolicyPendingDelete(null)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || t('filter.delete_failed')
      setMsg({ type: 'error', text })
      toast.error(text)
    } finally {
      setDeleting(false)
    }
  }

  const handleCreateRule = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selected) return
    const action = selected.mode === 'whitelist' ? 'allow' : 'block'
    try {
      await api.createFilterRule(selected.id, {
        pattern: newRule.pattern,
        category: newRule.category,
        action,
      })
      const isWl = selected.mode === 'whitelist'
      const text = t(isWl ? 'filter.rule_added_allowed' : 'filter.rule_added_blocked', { pattern: newRule.pattern, policy: selected.name })
      setMsg({ type: 'success', text })
      toast.success(text)
      setIsRuleModalOpen(false)
      setNewRule({
        pattern: '',
        category: isWl ? 'Business & Productivity' : 'Security & Phishing',
      })
      await loadRules(selected.id)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || t('filter.rule_create_failed')
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
          ? t('filter.scope_entire_fleet')
          : retargetForm.target_type === 'group'
            ? t('filter.scope_group_named', { name: groupName(retargetForm.target_id) })
            : `'${deviceName(retargetForm.target_id)}'`
      const text = t('filter.retargeted', { name: retargeting.name, scope })
      setMsg({ type: 'success', text })
      toast.success(text, t('filter.retargeted_title'))
      setRetargeting(null)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || t('filter.retarget_failed')
      setMsg({ type: 'error', text })
      toast.error(text, t('filter.update_failed_title'))
    }
  }

  const handleDeleteRule = async () => {
    if (!rulePendingDelete) return
    const { id, pattern } = rulePendingDelete
    setDeleting(true)
    try {
      await api.deleteFilterRule(id)
      const text = t('filter.rule_deleted', { pattern })
      setMsg({ type: 'success', text })
      toast.info(text)
      setRulePendingDelete(null)
      await loadRules(selectedId)
      await loadPolicies()
    } catch (err: any) {
      const text = err.message || t('filter.rule_delete_failed')
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
          ? t('filter.sync_pushed', { count: res.effective_rules, host: d.hostname, version: res.policy_version.slice(0, 12) })
          : // True now, but previously it was an unbacked claim: the row said 'pending'
            // and nothing re-read it. The server re-sends the policy on reconnect when
            // the device's reported version is not the current one.
            t('filter.sync_offline', { host: d.hostname })
      setMsg({ type: 'success', text })
      toast.success(text, t('filter.sync_title'))
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
      const text = err.message || t('filter.sync_failed')
      setMsg({ type: 'error', text })
      toast.error(text, t('filter.sync_failed_title'))
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
    return t('filter.scope_all_devices')
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h2 className="page-title">{t('filter.title')}</h2>
          <p className="page-subtitle">
            {t('filter.subtitle')}
          </p>
        </div>
        <div className="header-controls">
          <button type="button" className="btn btn-secondary" onClick={loadData} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spinning' : ''} />
            <span>{t('filter.refresh')}</span>
          </button>
          {canAdmin && (
            <button type="button" className="btn btn-secondary" onClick={() => setIsPolicyModalOpen(true)}>
              <Layers size={16} />
              <span>{t('filter.new_policy')}</span>
            </button>
          )}
          {canAdmin && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => {
                if (selected) {
                  setNewRule({
                    pattern: '',
                    category: selected.mode === 'whitelist' ? 'Business & Productivity' : 'Security & Phishing',
                  })
                  setIsRuleModalOpen(true)
                }
              }}
              disabled={!selected}
              title={
                selected
                  ? t(selected.mode === 'whitelist' ? 'filter.add_title_allowed' : 'filter.add_title_blocked', { name: selected.name })
                  : t('filter.create_policy_first')
              }
            >
              <Plus size={16} />
              <span>{selected?.mode === 'whitelist' ? t('filter.add_allowed_domain') : t('filter.add_rule')}</span>
            </button>
          )}
        </div>
      </div>

      {msg && (
        <div className={`alert-banner ${msg.type === 'error' ? 'alert-error' : 'alert-success'}`}>
          <span>{msg.text}</span>
          <button type="button" onClick={() => setMsg(null)} className="close-btn" aria-label={t('filter.dismiss')}>
            <X size={14} />
          </button>
        </div>
      )}

      {/* Policy list */}
      <div className="table-card">
        <div className="card-header-bar">
          <div className="card-title-group">
            <Shield className="text-primary" size={18} />
            <h3 className="card-title">{t('filter.policies_title', { count: policies.length })}</h3>
          </div>
          <span className="text-sm text-dim">
            {t('filter.policies_hint')}
          </span>
        </div>

        <DataTable label={t('filter.policies_table_label')}>
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('filter.col_policy')}</th>
                <th>{t('filter.col_mode')}</th>
                <th>{t('filter.scope_label')}</th>
                <th>{t('filter.col_rules')}</th>
                <th>{t('filter.col_status')}</th>
                <th>{t('filter.col_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {policies.length === 0 ? (
                <tr>
                  <td colSpan={6} className="text-center py-8 text-muted">
                    {t('filter.empty_policies')}
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
                      {p.mode === 'whitelist' ? (
                        <span className="status-pill warning" title={t('filter.mode_whitelist_title')}>
                          {t('filter.mode_whitelist')}
                        </span>
                      ) : (
                        <span className="status-pill offline" title={t('filter.mode_blacklist_title')}>
                          {t('filter.mode_blacklist')}
                        </span>
                      )}
                    </td>
                    <td>
                      <div className="font-semibold text-main">{scopeLabel(p)}</div>
                      <div className="text-sm text-dim">
                        {p.target_type === 'all'
                          ? t('filter.fleet_wide')
                          : p.target_type === 'group'
                            ? t('filter.target_type_group')
                            : p.target_type === 'device'
                              ? t('filter.target_type_device')
                              : p.target_type}
                      </div>
                    </td>
                    <td>
                      <span className="font-mono">{p.rules_count}</span>
                    </td>
                    <td>
                      <span className={`status-pill ${p.is_enabled ? 'online' : 'offline'}`}>
                        {p.is_enabled ? t('filter.enabled') : t('filter.disabled')}
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
                            title={t('filter.currently_targets', { name: scopeLabel(p) })}
                          >
                            <Target size={14} />
                            <span>{t('filter.retarget')}</span>
                          </button>
                          <button
                            type="button"
                            className="btn-action"
                            onClick={() => handleTogglePolicy(p)}
                            title={p.is_enabled ? t('filter.disable_policy') : t('filter.enable_policy')}
                          >
                            <Power size={14} />
                            <span>{p.is_enabled ? t('filter.action_disable') : t('filter.action_enable')}</span>
                          </button>
                          <button
                            type="button"
                            className="btn-action text-danger"
                            onClick={() =>
                              setPolicyPendingDelete({ id: p.id, name: p.name, rules_count: p.rules_count })
                            }
                            aria-label={t('filter.delete_policy_aria', { name: p.name })}
                          >
                            <Trash2 size={14} />
                            <span>{t('common.delete')}</span>
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
              {selected?.mode === 'whitelist' ? t('filter.allowed_domains') : t('filter.col_rules')}{' '}
              {selected ? t('filter.in_policy', { name: selected.name }) : ''} ({rules.length})
            </h3>
          </div>
          {selected && (
            <span className="text-sm text-dim">
              {selected.mode === 'whitelist'
                ? t('filter.whitelist_hint')
                : t('filter.blacklist_hint')}
            </span>
          )}
        </div>

        <DataTable label={t('filter.rules_table_label')}>
          <table className="data-table">
            <thead>
              <tr>
                <th>{selected?.mode === 'whitelist' ? t('filter.col_allowed_pattern') : t('filter.col_blocked_pattern')}</th>
                <th>{t('filter.col_category')}</th>
                <th>{t('filter.col_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {!selected ? (
                <tr>
                  <td colSpan={3} className="text-center py-8 text-muted">
                    {t('filter.select_policy_hint')}
                  </td>
                </tr>
              ) : rules.length === 0 ? (
                <tr>
                  <td colSpan={3} className="text-center py-8 text-muted">
                    {selected.mode === 'whitelist'
                      ? t('filter.empty_whitelist')
                      : t('filter.empty_blacklist')}
                  </td>
                </tr>
              ) : (
                rules.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <div className="font-mono font-semibold text-main">{r.pattern}</div>
                    </td>
                    <td>{r.category || t('filter.category_general')}</td>
                    <td>
                      {canAdmin && (
                        <button
                          type="button"
                          className="btn-action text-danger"
                          onClick={() => setRulePendingDelete({ id: r.id, pattern: r.pattern })}
                          aria-label={t('filter.delete_rule_aria', { pattern: r.pattern })}
                        >
                          <Trash2 size={14} />
                          <span>{t('common.delete')}</span>
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
            <h3 className="card-title">{t('filter.enforcement_title')}</h3>
          </div>
          <span className="text-sm text-dim">
            {statesLoading
              ? t('filter.reading_states', { count: devices.length })
              : Object.keys(stateErrors).length > 0
                ? t('filter.read_failures', { failed: Object.keys(stateErrors).length, total: devices.length })
                : t('filter.enforcement_hint')}
          </span>
        </div>

        <DataTable label={t('filter.enforcement_table_label')}>
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('filter.col_device')}</th>
                <th>{t('filter.col_site')}</th>
                <th>{t('filter.col_agent_version')}</th>
                <th>{t('filter.col_rules_applied')}</th>
                <th>{t('filter.col_status')}</th>
                <th>{t('filter.col_last_report')}</th>
                <th>{t('filter.col_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {devices.length === 0 ? (
                <tr>
                  <td colSpan={7} className="text-center py-8 text-muted">
                    {t('filter.no_devices')}
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
                          {t('filter.state_unavailable', { error: stateError })}
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
                            {/* A device that never synced has applied no count
                                at all; 0 would claim it reported an empty rule set. */}
                            <span className="font-mono">{s?.last_applied_at ? (s.rules_applied ?? 0) : '—'}</span>
                          </td>
                          <td>{renderStatus(t, s)}</td>
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
                            title={t('filter.sync_button_title')}
                          >
                            <CloudUpload size={14} className={syncingId === d.id ? 'spinning' : ''} />
                            <span>{syncingId === d.id ? t('filter.syncing') : t('filter.sync_now')}</span>
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
        title={t('filter.new_policy_title')}
        size="md"
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsPolicyModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button type="submit" form="filter-policy-form" className="btn btn-primary">
              {t('filter.create_policy')}
            </button>
          </>
        }
      >
        <form id="filter-policy-form" onSubmit={handleCreatePolicy}>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-policy-name">
              {t('filter.name_label')}
            </label>
            <input
              id="filter-policy-name"
              type="text"
              className="form-input"
              required
              placeholder={t('filter.name_placeholder')}
              value={newPolicy.name}
              onChange={(e) => setNewPolicy({ ...newPolicy, name: e.target.value })}
            />
          </div>
          <div className="form-group">
            <label className="form-label">
              {t('filter.mode_label')}
            </label>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', marginTop: '0.25rem' }}>
              <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.6rem', cursor: 'pointer' }}>
                <input
                  type="radio"
                  name="filter-mode"
                  value="blacklist"
                  checked={newPolicy.mode === 'blacklist'}
                  onChange={() => setNewPolicy({ ...newPolicy, mode: 'blacklist' })}
                  style={{ marginTop: '0.2rem' }}
                />
                <div>
                  <div className="font-semibold text-main">{t('filter.mode_blacklist_option')}</div>
                  <div className="text-xs text-muted">
                    {t('filter.mode_blacklist_desc')}
                  </div>
                </div>
              </label>
              <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.6rem', cursor: 'pointer' }}>
                <input
                  type="radio"
                  name="filter-mode"
                  value="whitelist"
                  checked={newPolicy.mode === 'whitelist'}
                  onChange={() => setNewPolicy({ ...newPolicy, mode: 'whitelist' })}
                  style={{ marginTop: '0.2rem' }}
                />
                <div>
                  <div className="font-semibold text-main">{t('filter.mode_whitelist_option')}</div>
                  <div className="text-xs text-muted">
                    {t('filter.mode_whitelist_desc')}
                  </div>
                </div>
              </label>
            </div>
            {newPolicy.mode === 'whitelist' && (
              <p className="form-hint" style={{ marginTop: '0.5rem', color: 'var(--color-warning)' }}>
                {t('filter.whitelist_warning')}
              </p>
            )}
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-policy-desc">
              {t('filter.description_label')}
            </label>
            <input
              id="filter-policy-desc"
              type="text"
              className="form-input"
              placeholder={t('filter.description_placeholder')}
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
        title={t('filter.retarget_title')}
        description={retargeting ? t('filter.retarget_description', { name: retargeting.name, scope: scopeLabel(retargeting) }) : undefined}
        size="md"
        footer={
          <>
            <button type="button" className="btn btn-secondary" onClick={() => setRetargeting(null)}>
              {t('common.cancel')}
            </button>
            <button type="submit" form="filter-retarget-form" className="btn btn-primary">
              {t('filter.apply_scope')}
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
            {t('filter.retarget_hint')}
          </p>
        </form>
      </Modal>

      {/* Add Rule Modal */}
      <Modal
        open={isRuleModalOpen && selected !== null}
        onClose={() => setIsRuleModalOpen(false)}
        title={selected?.mode === 'whitelist' ? t('filter.add_allowed_domain') : t('filter.add_rule')}
        size="md"
        description={selected ? t(selected.mode === 'whitelist' ? 'filter.rule_modal_description_whitelist' : 'filter.rule_modal_description_blacklist', { name: selected.name }) : undefined}
        footer={
          <>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setIsRuleModalOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button type="submit" form="filter-rule-form" className="btn btn-primary">
              {selected?.mode === 'whitelist' ? t('filter.add_allowed_domain') : t('filter.add_rule')}
            </button>
          </>
        }
      >
        <form id="filter-rule-form" onSubmit={handleCreateRule}>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-rule-pattern">
              {selected?.mode === 'whitelist' ? t('filter.pattern_label_allowed') : t('filter.pattern_label_blocked')}
            </label>
            <input
              id="filter-rule-pattern"
              type="text"
              className="form-input"
              required
              placeholder={selected?.mode === 'whitelist' ? t('filter.pattern_placeholder_allowed') : t('filter.pattern_placeholder_blocked')}
              value={newRule.pattern}
              onChange={(e) => setNewRule({ ...newRule, pattern: e.target.value })}
            />
            {selected?.mode === 'whitelist' && (
              <p className="form-hint">
                {t('filter.subdomain_tip')}
              </p>
            )}
          </div>
          <div className="form-group">
            <label className="form-label" htmlFor="filter-rule-category">
              {t('filter.col_category')}
            </label>
            <select
              id="filter-rule-category"
              className="form-select"
              value={newRule.category}
              onChange={(e) => setNewRule({ ...newRule, category: e.target.value })}
            >
              {selected?.mode === 'whitelist' ? (
                <>
                  <option value="Business & Productivity">{t('filter.cat_business')}</option>
                  <option value="Corporate Portal & Tools">{t('filter.cat_corporate')}</option>
                  <option value="Educational & Reference">{t('filter.cat_education')}</option>
                  <option value="General Whitelist">{t('filter.cat_general_whitelist')}</option>
                </>
              ) : (
                <>
                  <option value="Security & Phishing">{t('filter.cat_security')}</option>
                  <option value="Adult & Gambling">{t('filter.cat_adult')}</option>
                  <option value="Bandwidth Heavy / Streaming">{t('filter.cat_streaming')}</option>
                  <option value="Social Media">{t('filter.cat_social')}</option>
                  <option value="Custom Policy">{t('filter.cat_custom')}</option>
                </>
              )}
            </select>
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={policyPendingDelete !== null}
        title={t('filter.delete_policy_title')}
        message={
          policyPendingDelete
            ? t('filter.delete_policy_message', { name: policyPendingDelete.name, count: policyPendingDelete.rules_count })
            : ''
        }
        confirmLabel={t('common.delete')}
        pending={deleting}
        onConfirm={handleDeletePolicy}
        onCancel={() => setPolicyPendingDelete(null)}
      />

      <ConfirmDialog
        open={rulePendingDelete !== null}
        title={t('filter.delete_rule_title')}
        message={
          rulePendingDelete
            ? t('filter.delete_rule_message', { pattern: rulePendingDelete.pattern })
            : ''
        }
        confirmLabel={t('common.delete')}
        pending={deleting}
        onConfirm={handleDeleteRule}
        onCancel={() => setRulePendingDelete(null)}
      />
    </div>
  )
}
