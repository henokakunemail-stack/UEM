import React, { useEffect, useState } from 'react'
import { CheckCircle2, PlugZap, RefreshCw, Save, Users, XCircle } from 'lucide-react'
import { api } from '../services/api'
import { useToast } from '../context/ToastContext'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import type { DirectoryConfigDTO, DirectoryContactDTO, DirectorySyncPlanDTO } from '../types/api'

// Directory sync only. Nothing here creates a console account: a synced contact
// has no password_hash, so the server's login path refuses them like any
// unknown user. The contacts exist so a hardware asset's PIC is a dropdown
// instead of a spelling exercise.

const DEFAULT_FILTER = '(objectClass=person)'

const emptyConfig = (): DirectoryConfigDTO => ({
  host: '',
  port: 636,
  use_tls: true,
  base_dn: '',
  bind_dn: '',
  search_filter: DEFAULT_FILTER,
  source: 'ldap',
  bind_password_configured: false,
})

export function SettingsPage() {
  const [config, setConfig] = useState<DirectoryConfigDTO>(emptyConfig())
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [syncing, setSyncing] = useState(false)
  // Held rather than recomputed on render, so Apply acts on what the operator
  // was actually shown. The server still re-reads the directory when it runs —
  // see the note on applySync — so this is a confirmation, not the source.
  const [plan, setPlan] = useState<DirectorySyncPlanDTO | null>(null)
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null)
  // Apply is irreversible — it deactivates contacts in bulk — so it goes
  // through a confirmation like the asset delete does. The plan is re-diffed
  // by the server on confirm, so the numbers below are what the operator is
  // agreeing to, not necessarily what will be written.
  const [confirmApply, setConfirmApply] = useState(false)
  const [applying, setApplying] = useState(false)
  const toast = useToast()

  // ponytail: `toast` is deliberately NOT an effect dependency here.
  // ToastContext.tsx:80 builds its `value` as an object literal on every
  // render, so `useToast()` returns a new identity whenever any toast appears
  // anywhere in the app. Depending on it re-ran this load and overwrote the
  // operator's in-progress typing with the saved config. AlertsPage.tsx:70
  // already excludes it for the same reason. Memoise the provider's value if
  // that ever changes, then the dependency can come back.
  useEffect(() => {
    let stopped = false
    setLoading(true)
    api.getDirectoryConfig()
      .then((saved) => { if (!stopped) setConfig(saved) })
      .catch((err: any) => {
        if (!stopped) toast.error(err.message || 'Failed to load directory settings', 'Load Failed')
      })
      .finally(() => { if (!stopped) setLoading(false) })
    return () => { stopped = true }
  }, [])

  // Editing any setting invalidates every result derived from the previous
  // ones. A preview shown beside a half-typed host reads as "this is what your
  // new settings will do", which is exactly what it is not.
  const updateConfig = (patch: Partial<DirectoryConfigDTO>) => {
    setPlan(null)
    setTestResult(null)
    setConfig(prev => ({ ...prev, ...patch }))
  }

  const reload = async () => {
    setLoading(true)
    try {
      setConfig(await api.getDirectoryConfig())
    } catch (err: any) {
      toast.error(err.message || 'Failed to load directory settings', 'Load Failed')
    } finally {
      setLoading(false)
    }
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      const saved = await api.updateDirectoryConfig(config)
      setConfig(saved)
      toast.success('Directory settings saved.', 'Settings Saved')
    } catch (err: any) {
      toast.error(err.message || 'Failed to save directory settings', 'Save Failed')
    } finally {
      setSaving(false)
    }
  }

  const testConnection = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api.testDirectoryConnection()
      setTestResult({ ok: res.ok, message: `${res.message} ${res.entries_seen} entries matched the filter.` })
      if (res.ok) toast.success(`Directory reachable — ${res.entries_seen} entries.`, 'Connection OK')
    } catch (err: any) {
      // A failed test arrives as a 502 carrying the directory's own complaint —
      // a rejected password, a wrong base DN. Showing it is the whole point of
      // having a Test button, so it is rendered rather than reduced to a toast.
      setTestResult({ ok: false, message: err.message || 'Could not reach the directory' })
    } finally {
      setTesting(false)
    }
  }

  const previewSync = async () => {
    setSyncing(true)
    setPlan(null)
    try {
      const p = await api.previewDirectorySync()
      setPlan(p)
      if (p.total_in_directory === 0) {
        toast.error('The directory answered but matched nothing — check the base DN and the search filter.',
          'Nothing Matched')
      }
    } catch (err: any) {
      toast.error(err.message || 'Preview failed', 'Preview Failed')
    } finally {
      setSyncing(false)
    }
  }

  const applySync = async () => {
    setApplying(true)
    try {
      // The server re-reads the directory and re-diffs against the database as
      // it is now. That is deliberate: the plan on screen was computed a minute
      // ago, and acting on a stale plan is how a preview stops meaning a
      // promise. What comes back is the plan that was actually applied.
      const applied = await api.applyDirectorySync()
      setPlan(applied)
      setConfirmApply(false)
      const total = applied.adds.length + applied.updates.length + applied.deactivations.length
      if (total === 0) {
        toast.success('Already up to date — no changes.', 'Sync Complete')
      } else {
        toast.success(
          `${applied.adds.length} added, ${applied.updates.length} updated, ${applied.deactivations.length} deactivated.`,
          'Sync Complete')
      }
    } catch (err: any) {
      toast.error(err.message || 'Sync failed', 'Sync Failed')
    } finally {
      setApplying(false)
    }
  }

  const pending = plan ? plan.adds.length + plan.updates.length + plan.deactivations.length : 0
  const busy = syncing || applying

  return (
    <div className="page-container">
      <div className="page-header">
        <div>
          <h1 className="page-title">Settings</h1>
          <p className="page-subtitle">Directory (LDAP / Active Directory) sync</p>
        </div>
        <button type="button" className="btn btn-secondary" onClick={reload} disabled={loading}>
          <RefreshCw size={16} className={loading ? 'spinning' : ''} />
          <span>Reload</span>
        </button>
      </div>

      <div className="security-notice">
        <PlugZap size={16} />
        <div>
          <strong>Synced people are not console accounts.</strong> They have no password and cannot log
          in here. Their only use is as the list a hardware asset's PIC is chosen from.
        </div>
      </div>

      <form className="table-card" onSubmit={save}>
        <div className="card-header">
          <div className="card-title-group">
            <span className="card-icon"><PlugZap size={18} /></span>
            <div>
              <h2 className="card-title">Connection</h2>
              <span className="text-muted text-sm">
                Use a read-only service account, never your own login.
              </span>
            </div>
          </div>
        </div>

        <div className="table-responsive">
          <div className="form-group">
            <label className="form-label" htmlFor="dir-host">Host</label>
            <input
              id="dir-host"
              className="form-input"
              required
              placeholder="ldap.example.com"
              value={config.host}
              onChange={(e) => updateConfig({ host: e.target.value })}
            />
          </div>

          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="dir-port">Port</label>
              <input
                id="dir-port"
                className="form-input"
                type="number"
                min={1}
                max={65535}
                required
                value={config.port}
                onChange={(e) => updateConfig({ port: Number(e.target.value) })}
              />
              <span className="form-hint">636 is LDAPS; 389 with TLS on is StartTLS.</span>
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="dir-tls">Transport</label>
              <select
                id="dir-tls"
                className="form-select"
                value={config.use_tls ? '1' : '0'}
                onChange={(e) => updateConfig({ use_tls: e.target.value === '1' })}
              >
                <option value="1">TLS</option>
                <option value="0">Plain</option>
              </select>
              <span className="form-hint">
                Plain sends the bind password in the clear. Only for a tunnel you control.
              </span>
            </div>
          </div>

          <div className="form-group">
            <label className="form-label" htmlFor="dir-basedn">Base DN</label>
            <input
              id="dir-basedn"
              className="form-input"
              placeholder="DC=example,DC=com"
              value={config.base_dn}
              onChange={(e) => updateConfig({ base_dn: e.target.value })}
            />
            <span className="form-hint">The subtree to search. Everything under it is read.</span>
          </div>

          <div className="form-row">
            <div className="form-field">
              <label className="form-label" htmlFor="dir-binddn">Bind DN</label>
              <input
                id="dir-binddn"
                className="form-input"
                required
                placeholder="CN=svc-directory,OU=Service,DC=example,DC=com"
                value={config.bind_dn}
                onChange={(e) => updateConfig({ bind_dn: e.target.value })}
              />
            </div>
            <div className="form-field">
              <label className="form-label" htmlFor="dir-filter">Search filter</label>
              <input
                id="dir-filter"
                className="form-input"
                value={config.search_filter}
                onChange={(e) => updateConfig({ search_filter: e.target.value })}
              />
            </div>
          </div>

          <div className="form-group">
            <span className="form-label">Bind password</span>
            <p className="text-muted text-sm">
              {config.bind_password_configured
                ? 'Set in the LDAP_BIND_PASSWORD environment variable. It is never shown, never stored in the database, and cannot be changed from here.'
                : 'Not set. Export LDAP_BIND_PASSWORD and restart the server before syncing.'}
            </p>
          </div>

          <div className="row-actions">
            <button type="submit" className="btn btn-primary" disabled={saving || loading}>
              <Save size={16} />
              <span>{saving ? 'Saving…' : 'Save settings'}</span>
            </button>
            <button type="button" className="btn btn-secondary" onClick={testConnection}
              disabled={testing || loading || syncing}>
              <PlugZap size={16} />
              <span>{testing ? 'Testing…' : 'Test connection'}</span>
            </button>
          </div>

          {testResult && (
            <div className={testResult.ok ? 'alert-banner' : 'error-banner'}>
              {testResult.ok
                ? <CheckCircle2 size={16} />
                : <XCircle size={16} />}
              <span>{testResult.message}</span>
            </div>
          )}
        </div>
      </form>

      <div className="table-card">
        <div className="card-header">
          <div className="card-title-group">
            <span className="card-icon"><Users size={18} /></span>
            <div>
              <h2 className="card-title">Sync</h2>
              <span className="text-muted text-sm">
                Preview shows what would change. Nothing is written until Apply.
              </span>
            </div>
          </div>
        </div>

        <div className="table-responsive">
          <div className="row-actions">
            <button type="button" className="btn btn-secondary" onClick={previewSync}
              disabled={busy || loading}>
              <RefreshCw size={16} className={syncing ? 'spinning' : ''} />
              <span>Preview sync</span>
            </button>
            <button type="button" className="btn btn-primary" onClick={() => setConfirmApply(true)}
              disabled={busy || loading || plan === null || pending === 0}>
              <Save size={16} />
              <span>Apply</span>
            </button>
          </div>

          {!plan && (
            <p className="text-muted">
              Run a preview to see what a sync would do. Nothing has been read from the directory yet.
            </p>
          )}

          {plan && plan.total_in_directory === 0 && (
            <div className="error-banner">
              <XCircle size={16} />
              <span>
                The directory answered but returned no entries. A base DN that matches nothing looks
                exactly like an up-to-date directory, so check the base DN and filter before trusting
                a zero here.
              </span>
            </div>
          )}

          {plan && pending === 0 && plan.total_in_directory > 0 && (
            <div className="alert-banner">
              <CheckCircle2 size={16} />
              <span>
                Nothing to do. {plan.unchanged} of {plan.total_in_directory} entries already match.
              </span>
            </div>
          )}

          {plan && pending > 0 && (
            <>
              <PlanTable title="To add" rows={plan.adds} />
              <PlanTable title="To update" rows={plan.updates} />
              <PlanTable title="To deactivate" rows={plan.deactivations} />
            </>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={confirmApply}
        title="Apply directory sync"
        message={
          plan
            ? `Write ${plan.adds.length} new, ${plan.updates.length} updated and ${plan.deactivations.length} deactivated contacts? ` +
              'Deactivated contacts drop out of every PIC dropdown and cannot come back without another sync. ' +
              'The server re-reads the directory on confirm, so these numbers can shift if it changed in the meantime.'
            : ''
        }
        confirmLabel="Apply sync"
        pending={applying}
        onConfirm={applySync}
        onCancel={() => setConfirmApply(false)}
      />
    </div>
  )
}

// Deactivations are shown with their department greyed rather than in the same
// table as the rest: an operator reading "To deactivate" should be able to see
// at a glance that these are people who left, not rows about to be deleted.
function PlanTable({ title, rows }: { title: string; rows: DirectoryContactDTO[] }) {
  if (rows.length === 0) return null
  return (
    <>
      <h3 className="section-title">{title} ({rows.length})</h3>
      <div className="table-wrapper">
        <table className="data-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Username</th>
              <th>Email</th>
              <th>Department</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.external_id}>
                <td className="font-semibold">{c.display_name}</td>
                <td className="font-mono text-sm">{c.username || '—'}</td>
                <td className="text-sm">{c.email || '—'}</td>
                <td className="text-sm">{c.department || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}