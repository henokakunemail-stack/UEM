import type {
  ActivityItem,
  AlertItem,
  DashboardSummary,
  DeviceDTO,
  DeviceInventorySnapshot,
  DeviceListResponse,
  OSMetric,
  SiteMetric,
  UserToken,
  SoftwarePackageDTO,
  SoftwareDeploymentDTO,
  DeploymentTaskDTO,
  RemoteExecutionDTO,
  TerminalSessionDTO,
  PatchSummaryDTO,
  PatchDetailDTO,
  ScriptDTO,
  ScheduleDTO,
  TaskRunDTO,
  DeviceRunDTO,
  FilterPolicyDTO,
  FilterRuleDTO,
  DeviceFilterStateDTO,
  AlertIncidentDTO,
  AlertRuleDTO,
  HardwareAssetDTO,
  AssetSummaryDTO,
  SoftwareLicenseDTO,
  LicenseComplianceSummaryDTO,
  AgentReleaseDTO,
  UpdateCampaignDTO,
  UserDTO,
  ServerLogResponse,
  DeviceGroupDTO,
  MaintenanceJobDTO,
  MaintenanceProgressDTO,
  MaintenanceRunResponse,
  MaintenanceTaskDTO,
  TaskInfoDTO,
  RemoteControlMode,
  RemoteControlSessionDTO,
  AuthSession,
} from '../types/api'

const TOKEN_KEY = 'em_access_token'
const REFRESH_KEY = 'em_refresh_token'

export function getStoredToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function getStoredRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_KEY)
}

export function setStoredTokens(tokens: UserToken) {
  localStorage.setItem(TOKEN_KEY, tokens.access_token)
  localStorage.setItem(REFRESH_KEY, tokens.refresh_token)
}

export function clearStoredTokens() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(REFRESH_KEY)
}

export function parseJwtClaims(token: string): { uid: string; usr: string; rol: string } | null {
  try {
    const base64Url = token.split('.')[1]
    const base64 = base64Url.replace(/-/g, '+').replace(/_/g, '/')
    const jsonPayload = decodeURIComponent(
      atob(base64)
        .split('')
        .map((c) => '%' + ('00' + c.charCodeAt(0).toString(16)).slice(-2))
        .join('')
    )
    return JSON.parse(jsonPayload)
  } catch {
    return null
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = getStoredToken()
  const headers = new Headers(options.headers || {})
  if (!(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }

  const res = await fetch(path, { ...options, headers })

  if (res.status === 401 && !path.includes('/api/auth/login')) {
    // Try to refresh token
    const refreshToken = getStoredRefreshToken()
    if (refreshToken) {
      try {
        const refreshRes = await fetch('/api/auth/refresh', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ refresh_token: refreshToken }),
        })
        if (refreshRes.ok) {
          const newTokens: UserToken = await refreshRes.json()
          setStoredTokens(newTokens)
          headers.set('Authorization', `Bearer ${newTokens.access_token}`)
          const retryRes = await fetch(path, { ...options, headers })
          if (!retryRes.ok) {
            const err = await retryRes.json().catch(() => ({}))
            throw new Error(err.error || `HTTP ${retryRes.status}`)
          }
          return retryRes.json()
        }
      } catch {
        // Refresh failed
      }
    }
    clearStoredTokens()
    window.location.href = '/login'
    throw new Error('Session expired, please log in again')
  }

  if (!res.ok) {
    const err = await res.json().catch(() => ({}))
    throw new Error(err.error || `Request failed with HTTP ${res.status}`)
  }

  return res.json()
}

/**
 * Same auth + 401-refresh path as request(), but returns the raw Response so a
 * streaming or binary endpoint is never JSON-parsed. Report exports are CSV/JSON
 * *documents*, not API envelopes, so they cannot go through request<T>.
 * The caller must check `res.ok` itself — this never throws on a 4xx/5xx.
 */
export async function fetchRaw(path: string, init: RequestInit = {}): Promise<Response> {
  const send = async (): Promise<Response> => {
    const headers = new Headers(init.headers || {})
    const token = getStoredToken()
    if (token) headers.set('Authorization', `Bearer ${token}`)
    return fetch(path, { ...init, headers })
  }

  const res = await send()
  if (res.status !== 401) return res

  const refreshToken = getStoredRefreshToken()
  if (!refreshToken) return res
  const refreshRes = await fetch('/api/auth/refresh', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: refreshToken }),
  })
  if (!refreshRes.ok) return res
  setStoredTokens(await refreshRes.json())
  return send()
}

export const api = {
  async login(username: string, password: string): Promise<UserToken> {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      throw new Error(err.error || 'Invalid credentials')
    }
    const tokens: UserToken = await res.json()
    setStoredTokens(tokens)
    return tokens
  },

  async logout(): Promise<void> {
    // The server has to be told as well as the browser. Clearing local storage
    // alone leaves the refresh token live on the server, so anyone holding a
    // copy of it keeps a working credential for its whole TTL -- which is the
    // reason logout exists at all. The server call is best effort: a logout that
    // fails because the network is down must still clear this device.
    const refreshToken = getStoredRefreshToken()
    if (refreshToken) {
      try {
        await request<{ status: string }>('/api/auth/logout', {
          method: 'POST',
          body: JSON.stringify({ refresh_token: refreshToken }),
        })
      } catch {
        // Deliberately swallowed. Reporting a failed logout to the user while
        // their tokens are already gone would describe an outcome that no
        // longer matches what is on screen.
      }
    }
    clearStoredTokens()
  },

  // A WebSocket handshake cannot carry an Authorization header, so the socket
  // URL would otherwise have to carry the access token -- where it lands in the
  // access log, in history, and in any Referer sent onward. A ticket is
  // redeemed once and expires in 60 seconds, so the value that reaches those
  // logs is already spent.
  async getWebSocketTicket(purpose: 'remote-exec' | 'remote-desktop'): Promise<string> {
    const res = await request<{ ticket: string; purpose: string }>(
      '/api/auth/ws-ticket?purpose=' + purpose,
      { method: 'POST' },
    )
    return res.ticket
  },

  async getAuthSessions(): Promise<AuthSession[]> {
    return request<AuthSession[]>('/api/auth/sessions')
  },

  async revokeAuthSession(jti: string): Promise<void> {
    // The value is encoded into `enc` first so the hole stays a bare `${enc}`:
    // the route-contract test collapses `${name}` to `{}` and compares it against
    // the chi pattern, and a call inside the hole is not a hole. Encoding it into
    // the template would parse as a path that does not exist.
    const enc = encodeURIComponent(jti)
    await request<{ status: string }>(`/api/auth/sessions/${enc}`, { method: 'DELETE' })
  },

  // Dashboard APIs
  async getDashboardSummary(): Promise<DashboardSummary> {
    return request<DashboardSummary>('/api/dashboard/summary')
  },

  async getDashboardSites(): Promise<SiteMetric[]> {
    return request<SiteMetric[]>('/api/dashboard/sites')
  },

  async getDashboardOS(): Promise<OSMetric[]> {
    return request<OSMetric[]>('/api/dashboard/os')
  },

  async getDashboardAlerts(): Promise<AlertItem[]> {
    return request<AlertItem[]>('/api/dashboard/alerts')
  },

  async getDashboardActivity(limit = 15): Promise<ActivityItem[]> {
    return request<ActivityItem[]>(`/api/dashboard/activity?limit=${limit}`)
  },

  // Device Management APIs
  // groupId is optional and last so every existing call site keeps working. The
  // server routes it to the group listing (device-management/handler.go:79).
  async getDevices(
    limit = 20,
    offset = 0,
    status = '',
    site = '',
    groupId = ''
  ): Promise<DeviceListResponse> {
    const params = new URLSearchParams({
      limit: limit.toString(),
      offset: offset.toString(),
    })
    if (status) params.append('status', status)
    if (site) params.append('site', site)
    if (groupId) params.append('group_id', groupId)
    return request<DeviceListResponse>(`/api/devices?${params.toString()}`)
  },

  async getDevice(id: string): Promise<DeviceDTO> {
    return request<DeviceDTO>(`/api/devices/${id}`)
  },

  async getDeviceInventory(id: string): Promise<DeviceInventorySnapshot> {
    return request<DeviceInventorySnapshot>(`/api/devices/${id}/inventory`)
  },

  async collectInventory(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/devices/${id}/inventory/collect`, {
      method: 'POST',
    })
  },

  async pingDevice(id: string): Promise<{ status: string; command_id: string }> {
    return request<{ status: string; command_id: string }>(`/api/devices/${id}/ping`, {
      method: 'POST',
    })
  },

  async retireDevice(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/devices/${id}/retire`, {
      method: 'POST',
    })
  },

  async restoreDevice(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/devices/${id}/restore`, {
      method: 'POST',
    })
  },

  async getAuditLogs(): Promise<{ logs: ActivityItem[]; count: number }> {
    return request<{ logs: ActivityItem[]; count: number }>('/api/audit-logs')
  },

  async getServerLogs(limit = 200): Promise<ServerLogResponse> {
    return request<ServerLogResponse>(`/api/logs?limit=${limit}`)
  },

  // Software Deployment APIs
  async getPackages(): Promise<SoftwarePackageDTO[]> {
    return request<SoftwarePackageDTO[]>('/api/software/packages')
  },

  async uploadPackage(formData: FormData): Promise<SoftwarePackageDTO> {
    return request<SoftwarePackageDTO>('/api/software/packages', {
      method: 'POST',
      body: formData,
    })
  },

  async deletePackage(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/software/packages/${id}`, {
      method: 'DELETE',
    })
  },

  async getDeployments(): Promise<SoftwareDeploymentDTO[]> {
    return request<SoftwareDeploymentDTO[]>('/api/software/deployments')
  },

  async getDeployment(id: string): Promise<SoftwareDeploymentDTO> {
    return request<SoftwareDeploymentDTO>(`/api/software/deployments/${id}`)
  },

  async getDeploymentTasks(id: string): Promise<DeploymentTaskDTO[]> {
    return request<DeploymentTaskDTO[]>(`/api/software/deployments/${id}/tasks`)
  },

  async createDeployment(data: {
    name: string
    package_id: string
    target_type: string
    target_id: string
    // 'install' or 'uninstall'. Omitting it means install, which is what every
    // caller sent before uninstall existed.
    action?: 'install' | 'uninstall'
  }): Promise<{ deployment: SoftwareDeploymentDTO; tasks_total: number; dispatched_live: number }> {
    return request<{ deployment: SoftwareDeploymentDTO; tasks_total: number; dispatched_live: number }>(
      '/api/software/deployments',
      {
        method: 'POST',
        body: JSON.stringify(data),
      }
    )
  },

  // Remote Execution APIs (Phase 5)
  async runRemoteCommand(
    deviceId: string,
    shell: string,
    command: string,
    timeoutSec = 60
  ): Promise<{ status: string; execution: RemoteExecutionDTO }> {
    return request<{ status: string; execution: RemoteExecutionDTO }>(
      `/api/devices/${deviceId}/exec`,
      {
        method: 'POST',
        body: JSON.stringify({ shell, command, timeout_sec: timeoutSec }),
      }
    )
  },

  async getExecutions(deviceId: string): Promise<RemoteExecutionDTO[]> {
    return request<RemoteExecutionDTO[]>(`/api/devices/${deviceId}/executions`)
  },

  async getExecution(deviceId: string, execId: string): Promise<RemoteExecutionDTO> {
    return request<RemoteExecutionDTO>(`/api/devices/${deviceId}/executions/${execId}`)
  },

  async getTerminalSessions(deviceId: string): Promise<TerminalSessionDTO[]> {
    return request<TerminalSessionDTO[]>(`/api/devices/${deviceId}/terminal/sessions`)
  },

  // Patch Management APIs (Phase 6)
  async getPatchSummary(): Promise<PatchSummaryDTO> {
    return request<PatchSummaryDTO>('/api/patches/summary')
  },

  // The server returns a bare array here, not a {patches,total} envelope.
  async getFleetPatches(state = ''): Promise<PatchDetailDTO[]> {
    const q = state ? `?state=${encodeURIComponent(state)}` : ''
    return request<PatchDetailDTO[]>(`/api/patches${q}`)
  },

  // `state` is passed through to the server, which filters on
  // device_patches.installed_state (patch-management/repository.go). The
  // console needs 'missing': without it a fully-patched device still returns
  // its installed rows, and the page presents them as pending work.
  async getDevicePatches(deviceId: string, state = 'missing'): Promise<PatchDetailDTO[]> {
    const q = state ? `?state=${encodeURIComponent(state)}` : ''
    return request<PatchDetailDTO[]>(`/api/devices/${deviceId}/patches${q}`)
  },

  async scanDevicePatches(deviceId: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/devices/${deviceId}/patches/scan`, {
      method: 'POST',
    })
  },

  async installDevicePatches(deviceId: string, patchIds: string[], rebootPolicy = 'no_reboot'): Promise<{ status: string; job_id?: string }> {
    return request<{ status: string; job_id?: string }>(`/api/devices/${deviceId}/patches/install`, {
      method: 'POST',
      body: JSON.stringify({ patch_ids: patchIds, reboot_policy: rebootPolicy }),
    })
  },

  // Task Scheduler & Script Repository APIs (Phase 10)
  async getScripts(): Promise<ScriptDTO[]> {
    return request<ScriptDTO[]>('/api/scripts')
  },

  async createScript(data: {
    name: string
    description: string
    script_type: string
    script_content: string
  }): Promise<ScriptDTO> {
    return request<ScriptDTO>('/api/scripts', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async deleteScript(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/scripts/${id}`, {
      method: 'DELETE',
    })
  },

  async getSchedules(): Promise<ScheduleDTO[]> {
    return request<ScheduleDTO[]>('/api/schedules')
  },

  async createSchedule(data: {
    name: string
    script_id: string
    target_type: string
    target_id: string
    schedule_type: string
    // One field for both trigger shapes: a seconds count for `interval`, a
    // cron expression for `cron`, parsed per `schedule_type`.
    schedule_expr: string
    // The server decodes this into a plain bool with no omitempty, so omitting
    // it persisted false — every schedule the console created was born paused,
    // while the toast said "created and activated" and the list showed Paused.
    is_enabled: boolean
  }): Promise<ScheduleDTO> {
    return request<ScheduleDTO>('/api/schedules', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async getTaskRuns(): Promise<TaskRunDTO[]> {
    return request<TaskRunDTO[]>('/api/schedules/runs')
  },

  async getRunDeviceRuns(runId: string): Promise<DeviceRunDTO[]> {
    return request<DeviceRunDTO[]>(`/api/schedules/runs/${runId}/devices`)
  },

  // Network & Web Filter APIs (Phase 12)
  // The server models this as policy -> rules: a rule belongs to a policy, the
  // policy carries the target scope, and sync happens per device. The console
  // previously called /api/network-filter/{rules,apply,compliance}, none of
  // which the server ever registered.
  async getFilterPolicies(): Promise<FilterPolicyDTO[]> {
    // The server returns an envelope, not a bare array. Typing this as
    // FilterPolicyDTO[] compiled fine and then handed the page an object, so
    // NetworkFilterPage's `list.some(...)` threw and blanked the whole page.
    const res = await request<{ policies: FilterPolicyDTO[]; count: number }>(
      '/api/filter/policies'
    )
    return res.policies || []
  },

  async createFilterPolicy(data: {
    name: string
    description: string
    target_type: string
    target_id: string
    priority: number
  }): Promise<FilterPolicyDTO> {
    return request<FilterPolicyDTO>('/api/filter/policies', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async updateFilterPolicy(
    id: string,
    data: { name?: string; description?: string; is_enabled?: boolean; priority?: number }
  ): Promise<FilterPolicyDTO> {
    return request<FilterPolicyDTO>(`/api/filter/policies/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    })
  },

  async deleteFilterPolicy(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/filter/policies/${id}`, {
      method: 'DELETE',
    })
  },

  async getFilterRules(policyId: string): Promise<FilterRuleDTO[]> {
    const res = await request<{ rules: FilterRuleDTO[]; count: number }>(
      `/api/filter/policies/${policyId}/rules`
    )
    return res.rules || []
  },

  async createFilterRule(
    policyId: string,
    data: { rule_type: string; pattern: string; category: string; action: string }
  ): Promise<FilterRuleDTO> {
    return request<FilterRuleDTO>(`/api/filter/policies/${policyId}/rules`, {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async deleteFilterRule(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/filter/rules/${id}`, {
      method: 'DELETE',
    })
  },

  // Push the effective rule set to one device over its live socket. There is no
  // fleet-wide "apply": enforcement is per device, so the caller loops.
  async syncDeviceFilter(
    deviceId: string
  ): Promise<{ status: string; policy_version: string; effective_rules: number }> {
    return request<{ status: string; policy_version: string; effective_rules: number }>(
      `/api/devices/${deviceId}/filter/sync`,
      { method: 'POST' }
    )
  },

  async getDeviceFilterState(deviceId: string): Promise<DeviceFilterStateDTO> {
    return request<DeviceFilterStateDTO>(`/api/devices/${deviceId}/filter/state`)
  },

  // Alerting & Incidents APIs (Phase 9)
  async getAlertIncidents(status = ''): Promise<{ incidents: AlertIncidentDTO[]; count: number }> {
    const q = status ? `?status=${status}` : ''
    // The server writes the bare slice (alerting/handler.go:186), not an
    // envelope. Destructuring `.incidents` off an Array gave undefined, so the
    // page reported "no incidents" while the server had them — a wrong answer
    // with no error anywhere, which is worse than a crash.
    const list = await request<AlertIncidentDTO[]>(`/api/alerts/incidents${q}`)
    return { incidents: list || [], count: list?.length ?? 0 }
  },

  async getAlertRules(): Promise<AlertRuleDTO[]> {
    return request<AlertRuleDTO[]>('/api/alerts/rules')
  },

  async acknowledgeIncident(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/alerts/incidents/${id}/acknowledge`, {
      method: 'POST',
    })
  },

  async resolveIncident(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/alerts/incidents/${id}/resolve`, {
      method: 'POST',
    })
  },

  // Asset & License Management APIs (Phase 14)
  async getAssets(site = '', status = ''): Promise<HardwareAssetDTO[]> {
    const params = new URLSearchParams()
    if (site) params.append('site', site)
    if (status) params.append('status', status)
    const q = params.toString() ? `?${params.toString()}` : ''
    return request<HardwareAssetDTO[]>(`/api/assets${q}`)
  },

  async getAssetSummary(): Promise<AssetSummaryDTO> {
    return request<AssetSummaryDTO>('/api/assets/summary')
  },

  async createAsset(data: Partial<HardwareAssetDTO>): Promise<HardwareAssetDTO> {
    return request<HardwareAssetDTO>('/api/assets', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async updateAsset(id: string, data: Partial<HardwareAssetDTO>): Promise<HardwareAssetDTO> {
    return request<HardwareAssetDTO>(`/api/assets/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    })
  },

  async deleteAsset(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/assets/${id}`, {
      method: 'DELETE',
    })
  },

  async getLicenses(): Promise<SoftwareLicenseDTO[]> {
    return request<SoftwareLicenseDTO[]>('/api/licenses')
  },

  async createLicense(data: Partial<SoftwareLicenseDTO>): Promise<SoftwareLicenseDTO> {
    return request<SoftwareLicenseDTO>('/api/licenses', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async getLicenseCompliance(): Promise<{ audited_at: string; compliance: LicenseComplianceSummaryDTO[] }> {
    return request<{ audited_at: string; compliance: LicenseComplianceSummaryDTO[] }>('/api/licenses/compliance')
  },

  async allocateLicense(licenseId: string, deviceId: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/licenses/${licenseId}/allocate`, {
      method: 'POST',
      body: JSON.stringify({ device_id: deviceId }),
    })
  },

  // Agent Self-Update APIs (Phase 13)
  async getAgentReleases(): Promise<AgentReleaseDTO[]> {
    return request<AgentReleaseDTO[]>('/api/agent-updates/releases')
  },

  async uploadAgentRelease(formData: FormData): Promise<AgentReleaseDTO> {
    return request<AgentReleaseDTO>('/api/agent-updates/releases', {
      method: 'POST',
      body: formData,
    })
  },

  async getUpdateCampaigns(): Promise<UpdateCampaignDTO[]> {
    return request<UpdateCampaignDTO[]>('/api/agent-updates/campaigns')
  },

  async createUpdateCampaign(data: {
    name: string
    target_version: string
    target_type: string
    target_id: string
    batch_size: number
    stagger_interval_sec: number
  }): Promise<UpdateCampaignDTO> {
    return request<UpdateCampaignDTO>('/api/agent-updates/campaigns', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async startUpdateCampaign(
    id: string,
  ): Promise<{ status: string; total_targets: number; dispatched_live: number }> {
    return request<{ status: string; total_targets: number; dispatched_live: number }>(
      `/api/agent-updates/campaigns/${id}/start`,
      { method: 'POST' },
    )
  },

  async dispatchDeviceUpdate(deviceId: string, targetVersion: string): Promise<{ status: string; task_id: string }> {
    return request<{ status: string; task_id: string }>(`/api/devices/${deviceId}/update/dispatch`, {
      method: 'POST',
      body: JSON.stringify({ target_version: targetVersion }),
    })
  },

  // Device Maintenance APIs (Phase 15)
  //
  // FE-AUTHORED CONTRACT. These paths are the console's side of the contract; the
  // server handler for this module does not exist yet, so anything listed under
  // ASSUMED below must match what server/modules/maintenance/handler.go ends up
  // registering, or TestEveryConsoleAPIPathIsRegistered fails on a path the
  // console calls and the server never mounts.
  //   ASSUMED  GET  /api/maintenance/tasks            -> bare array
  //   ASSUMED  POST /api/maintenance/jobs             -> {job,total_targets,dispatched_live,skipped}
  //   ASSUMED  GET  /api/maintenance/jobs/{id}/progress -> JobProgress object
  //   ASSUMED  GET  /api/maintenance/jobs/{id}/tasks   -> bare array
  // Not assumed, already registered elsewhere: GET /api/groups
  // (device-management/inventory_handler.go:220) is the real group endpoint and
  // the target picker binds to it, not to an invented maintenance groups route.

  // The server's own TaskCatalog. Fetched rather than hardcoded: the six labels
  // live in server/modules/maintenance/model.go, and a copy here would drift the
  // moment a task type is added or renamed.
  async getMaintenanceTaskCatalog(): Promise<TaskInfoDTO[]> {
    return request<TaskInfoDTO[]>('/api/maintenance/tasks')
  },

  async getMaintenanceJobs(limit = 50, offset = 0): Promise<MaintenanceJobDTO[]> {
    return request<MaintenanceJobDTO[]>(`/api/maintenance/jobs?limit=${limit}&offset=${offset}`)
  },

  // No getMaintenanceJob(id) here on purpose. The job row the detail modal
  // needs is already held by openDetail from the list row it was clicked on, and
  // every field the modal renders that can change mid-run comes from /progress
  // or /tasks. A single-job read would be a sixth route whose only purpose is to
  // satisfy a method nobody calls.

  async getMaintenanceJobProgress(id: string): Promise<MaintenanceProgressDTO> {
    return request<MaintenanceProgressDTO>(`/api/maintenance/jobs/${id}/progress`)
  },

  async getMaintenanceJobTasks(jobId: string): Promise<MaintenanceTaskDTO[]> {
    return request<MaintenanceTaskDTO[]>(`/api/maintenance/jobs/${jobId}/tasks`)
  },

  // `skipped` is a top-level field, not inside the job: a job targeting the
  // whole fleet on a weekend comes back with almost every device skipped, and
  // the operator needs to be told that in the toast rather than inferring it
  // from a completed count of zero.
  async runMaintenance(data: {
    name: string
    task_type: string
    target_type: 'device' | 'group' | 'all'
    target_id: string
  }): Promise<MaintenanceRunResponse> {
    return request<MaintenanceRunResponse>('/api/maintenance/jobs', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  // Device groups for the group target picker. The server answers an envelope,
  // not a bare array (inventory_handler.go:340), so the unwrap happens here
  // rather than in the page — same reason getFilterPolicies does it.
  async getDeviceGroups(): Promise<DeviceGroupDTO[]> {
    const res = await request<{ groups: DeviceGroupDTO[]; count: number }>('/api/groups')
    return res.groups || []
  },

  // Group administration. All four routes already exist on the server
  // (device-management/inventory_handler.go:216-221); this is the first caller.
  //
  // deleteGroup rejects with 409 when a deployment, schedule, filter policy,
  // update campaign or maintenance job still targets the group. That surfaces
  // as a thrown Error carrying the server's message, which names the blocker.
  async createDeviceGroup(name: string, description: string): Promise<DeviceGroupDTO> {
    return request<DeviceGroupDTO>('/api/groups', {
      method: 'POST',
      body: JSON.stringify({ name, description }),
    })
  },

  async deleteDeviceGroup(id: string): Promise<void> {
    await request<{ status: string }>(`/api/groups/${id}`, { method: 'DELETE' })
  },

  // Returns how many memberships were actually created. Re-adding a device the
  // group already holds is a no-op on the server (PRIMARY KEY), so the count
  // can be lower than the number of ids sent — the caller reports the real
  // number rather than claiming every selection landed.
  async addGroupMembers(groupId: string, deviceIds: string[]): Promise<number> {
    const res = await request<{ added: number }>(`/api/groups/${groupId}/members`, {
      method: 'POST',
      body: JSON.stringify({ device_ids: deviceIds }),
    })
    return res.added
  },

  async removeGroupMember(groupId: string, deviceId: string): Promise<void> {
    await request<{ status: string }>(`/api/groups/${groupId}/members/${deviceId}`, {
      method: 'DELETE',
    })
  },

  // The Devices tab filters by group through the same ?group_id= parameter the
  // list endpoint already accepts (device-management/handler.go:74), so no
  // second listing route is needed.
  async getGroupDevices(groupId: string, limit = 50, offset = 0): Promise<DeviceListResponse> {
    return request<DeviceListResponse>(
      `/api/groups/${groupId}/devices?limit=${limit}&offset=${offset}`
    )
  },

  // User Management APIs (Phase 7)
  async getUsers(): Promise<UserDTO[]> {
    return request<UserDTO[]>('/api/users')
  },

  async createUser(data: { username: string; display_name: string; password?: string; role: string }): Promise<UserDTO> {
    return request<UserDTO>('/api/users', {
      method: 'POST',
      body: JSON.stringify(data),
    })
  },

  async updateUser(id: string, data: { display_name?: string; role?: string }): Promise<UserDTO> {
    return request<UserDTO>(`/api/users/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    })
  },

  // The server soft-deactivates rather than erasing the row, so the audit
  // trail survives. Its route is DELETE /api/users/{id}.
  async deactivateUser(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/users/${id}`, {
      method: 'DELETE',
    })
  },

  async adminResetPassword(id: string, password: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api/users/${id}/password`, {
      method: 'PUT',
      body: JSON.stringify({ new_password: password }),
    })
  },

  // Remote control. The session lifecycle is REST; the live desktop is a
  // WebSocket the modal opens itself, because a binary frame stream does not
  // belong in the JSON `request` helper.

  // Starts a relay session and dispatches `rc.start` to the agent. Rejects with
  // 409 when the device is offline, so the caller can say "device is offline"
  // rather than "could not connect".
  async startRemoteControlSession(
    deviceId: string,
    mode: RemoteControlMode
  ): Promise<RemoteControlSessionDTO> {
    return request<RemoteControlSessionDTO>(`/api/devices/${deviceId}/remotecontrol/session`, {
      method: 'POST',
      body: JSON.stringify({ mode }),
    })
  },

  async stopRemoteControlSession(deviceId: string, sessionId: string): Promise<{ status: string }> {
    return request<{ status: string }>(
      `/api/devices/${deviceId}/remotecontrol/sessions/${sessionId}/stop`,
      { method: 'POST' }
    )
  },

  // Session history for a device, joined with hostname and operator name by the
  // repository. Viewer-visible, unlike starting a session.
  async getRemoteControlSessions(deviceId: string, limit = 20): Promise<RemoteControlSessionDTO[]> {
    return request<RemoteControlSessionDTO[]>(
      `/api/devices/${deviceId}/remotecontrol/sessions?limit=${limit}`
    )
  },

  // Reports Export URL Helper
  getReportExportUrl(reportType: 'inventory' | 'patches' | 'deployments' | 'audit', format: 'csv' | 'json'): string {
    const token = getStoredToken()
    return `/api/reports/${reportType}?format=${format}${token ? `&token=${encodeURIComponent(token)}` : ''}`
  },
}
