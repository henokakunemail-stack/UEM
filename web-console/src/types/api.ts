export interface UserToken {
  access_token: string
  refresh_token: string
  expires_at: number
}

export interface UserClaims {
  uid: string
  usr: string
  rol: 'admin' | 'technician' | 'viewer'
}

export interface DeviceDTO {
  id: string
  hostname: string
  os_name: string
  os_version: string
  agent_version: string
  status: 'online' | 'offline'
  last_seen_at: string | null
  site: string
  enrolled_at: string
  retired_at?: string | null
  capabilities?: string[]
}

export interface DeviceListResponse {
  devices: DeviceDTO[]
  count: number
  total: number
  limit: number
  offset: number
}

export interface HardwareInfo {
  cpu?: {
    name?: string
    number_of_cores?: number
    logical_processors?: number
  }
  ram_total_bytes?: number
  disks?: Array<{
    name: string
    filesystem?: string
    total_bytes: number
    free_bytes: number
  }>
  nics?: Array<{
    name: string
    // The agent emits `mac`, not `mac_address` — MAC is the stable cross-OS
    // identifier while Name is OS-native ("WiFi" vs "eth0").
    mac?: string
    ips?: string[]
  }>
  model?: {
    vendor?: string
    product?: string
    serial_number?: string
  }
}

export interface SoftwareInfo {
  name: string
  version?: string
  publisher?: string
  install_date?: string
  product_code?: string
}

export interface OSDetailInfo {
  edition?: string
  build_number?: string
  install_date?: string
  last_boot_utc?: string
  architecture?: string
}

export interface DeviceInventorySnapshot {
  id?: string
  device_id: string
  hw?: HardwareInfo
  hardware?: HardwareInfo
  software: SoftwareInfo[]
  os?: OSDetailInfo
  os_detail?: OSDetailInfo
  ram_bytes?: number
  disk_free_pct?: number
  cpu_model?: string
  hw_ram_bytes?: number
  hw_disk_free_pct?: number
  hw_cpu_model?: string
  collected_at: string
  updated_at?: string
}

export interface DashboardSummary {
  total_devices: number
  online_devices: number
  offline_devices: number
  retired_devices: number
  online_pct: number
  low_disk_alerts: number
  recent_hw_changes_24h: number
  sites_count: number
}

export interface SiteMetric {
  site: string
  total: number
  online: number
  offline: number
  online_pct: number
}

export interface OSMetric {
  os_name: string
  count: number
  pct: number
}

export interface AlertItem {
  id: string
  type: string
  severity: 'info' | 'warning' | 'critical'
  device_id: string
  hostname: string
  site: string
  message: string
  timestamp: string
}

export interface ActivityItem {
  id: string
  actor_type: string
  actor_id: string
  action: string
  target_id: string
  details: string
  created_at: string
}

// GET /api/logs — the server's own log tail, backing the "Log" menu.
// Unlike the audit trail this is what the server itself did, not what an
// operator did to it, so it is the place to read when a menu 500s.
export interface ServerLogResponse {
  lines: string[]
  total: number
  // Empty when LOG_FILE is unset and logs go to stdout only.
  log_file: string
  // True when `lines` is a suffix of the full buffer.
  truncated: boolean
}

export interface SoftwarePackageDTO {
  id: string
  name: string
  version: string
  os_target: 'windows' | 'linux' | 'macos'
  package_type: 'msi' | 'exe' | 'deb' | 'rpm' | 'pkg' | 'script'
  file_name: string
  file_size: number
  sha256: string
  install_args: string
  uninstall_args: string
  created_at: string
  updated_at: string
}

export interface SoftwareDeploymentDTO {
  id: string
  package_id: string
  name: string
  // Which verb the deployment performed. The server defaults it to 'install',
  // so it is optional here for the same reason; the list view still shows it so
  // a removal is never mistaken for a rollout.
  action?: 'install' | 'uninstall'
  target_type: 'device' | 'group' | 'all'
  target_id: string
  created_by: string
  status: 'running' | 'completed' | 'failed' | 'cancelled'
  created_at: string
  completed_at?: string | null
  package_name?: string
  package_version?: string
  total_tasks: number
  success_tasks: number
  failed_tasks: number
}

export interface DeploymentTaskDTO {
  id: string
  deployment_id: string
  package_id: string
  device_id: string
  // 'failed_lost' is terminal but means something different from 'failed':
  // the agent stopped reporting before the task finished, so the package's
  // state on that endpoint is unknown rather than rejected. The server sets it
  // when an offline device still has a task in a running state.
  // 'uninstalling' is the running state of a removal deployment, kept separate
  // from 'installing' so the console can say what the endpoint is doing.
  status:
    | 'pending'
    | 'dispatched'
    | 'downloading'
    | 'installing'
    | 'uninstalling'
    | 'success'
    | 'failed'
    | 'failed_lost'
  exit_code?: number | null
  output_log?: string | null
  error_message?: string | null
  created_at: string
  updated_at: string
  completed_at?: string | null
  hostname?: string
  site?: string
}

export interface RemoteExecutionDTO {
  id: string
  device_id: string
  operator_id: string
  shell_type: string
  command_text: string
  status: 'pending' | 'running' | 'completed' | 'failed' | 'timeout'
  exit_code?: number | null
  output?: string | null
  error_message?: string | null
  started_at: string
  completed_at?: string | null
  operator_name?: string
  hostname?: string
}

export interface TerminalSessionDTO {
  id: string
  device_id: string
  operator_id: string
  shell_type: string
  status: 'active' | 'closed'
  created_at: string
  closed_at?: string | null
  operator_name?: string
  hostname?: string
}

// Phase 6: Patch Management
// FleetPatchSummary on the server. The console used to declare its own
// compliance-percentage shape here, which shared not one field with this —
// every card on the Patches page rendered `undefined` against a real response.
export interface PatchSummaryDTO {
  total_missing_patches: number
  critical_security_patches: number
  reboot_pending_devices: number
  vulnerable_devices: number
}

export interface DevicePatchStatusDTO {
  device_id: string
  hostname: string
  os_name: string
  site: string
  status: string
  pending_count: number
  critical_count: number
  reboot_required: boolean
  last_scan_at?: string | null
}

export interface PatchDetailDTO {
  id: string
  patch_id: string
  device_id: string
  kb_id: string
  title: string
  description: string
  severity: string
  category: string
  size_bytes: number
  installed_state: string
  reboot_required: boolean
  discovered_at: string
  updated_at: string
  hostname?: string
  os_name?: string
}

// Phase 10: Task Scheduler & Script Repository
// ScriptTemplate / TaskSchedule / ScheduledTaskRun on the server. Note the
// server names the shell field `script_type` and the trigger `schedule_expr`;
// the console had renamed both, so every scheduled-job row read as blank.
export interface ScriptDTO {
  id: string
  name: string
  description: string
  script_type: string
  script_content: string
  sha256_hash: string
  default_args: string
  timeout_seconds: number
  created_by: string
  created_at: string
  updated_at: string
}

export interface ScheduleDTO {
  id: string
  name: string
  description: string
  script_id: string
  script_name?: string
  target_type: string
  target_id: string
  schedule_type: string
  schedule_expr: string
  is_enabled: boolean
  last_run_at?: string | null
  next_run_at?: string | null
  created_by: string
  created_at: string
  updated_at: string
}

export interface TaskRunDTO {
  id: string
  schedule_id: string
  script_id: string
  status: string
  triggered_at: string
  completed_at?: string | null
  schedule_name?: string
  script_name?: string
}

// A per-device row under a run. The run list is fleet-wide and has no device
// on it, so the per-device detail (exit code, output, error) lives here.
export interface DeviceRunDTO {
  id: string
  run_id: string
  device_id: string
  status: string
  exit_code?: number | null
  output_log?: string | null
  error_message?: string | null
  started_at?: string | null
  completed_at?: string | null
  hostname?: string
  site?: string
}

// Phase 12: Network & Web Filter
// The server models this as policy -> rules, not as a flat rule list: a rule
// only exists inside a policy, and the policy is what gets targeted and synced.
// The console's flat DTO (target_type/domain_pattern on the rule) matched no
// endpoint on the server at all.
export interface FilterPolicyDTO {
  id: string
  name: string
  description: string
  target_type: 'all' | 'group' | 'device'
  target_id: string
  is_enabled: boolean
  priority: number
  created_by: string
  created_at: string
  updated_at: string
  rules_count: number
}

export interface FilterRuleDTO {
  id: string
  policy_id: string
  rule_type: 'domain' | 'ip_port'
  pattern: string
  action: 'block' | 'allow'
  category: string
  created_at: string
}

export interface DeviceFilterStateDTO {
  device_id: string
  policy_version: string
  status: string
  rules_applied: number
  // The server's not-found fallback (networkfilter/handler.go:338-343) returns
  // neither key, so these are genuinely optional rather than merely nullable.
  last_applied_at?: string | null
  error_message?: string | null
}

// Phase 9: Alerting & Incidents
// AlertIncident / AlertRule on the server. The incident's headline text is
// `title` and its trigger count is `trigger_count`; the console read a
// `triggered_at` that the server never sends (it sends first_triggered_at and
// last_triggered_at) and rendered no title at all.
export interface AlertIncidentDTO {
  id: string
  rule_id: string
  rule_name?: string
  severity: 'info' | 'warning' | 'critical'
  device_id: string
  hostname?: string
  site?: string
  title: string
  message: string
  status: 'open' | 'acknowledged' | 'resolved'
  trigger_count: number
  acknowledged_by?: string | null
  acknowledged_at?: string | null
  resolved_by?: string | null
  resolved_at?: string | null
  first_triggered_at: string
  last_triggered_at: string
}

export interface AlertRuleDTO {
  id: string
  name: string
  rule_type: string
  threshold_val: number
  severity: 'info' | 'warning' | 'critical'
  webhook_url: string
  is_enabled: boolean
  created_by: string
  created_at: string
  updated_at: string
}

// Phase 14: Asset & License Management
export interface HardwareAssetDTO {
  id: string
  asset_tag: string
  device_id?: string | null
  model_name: string
  serial_number: string
  vendor: string
  site: string
  department: string
  assigned_user: string
  purchase_date?: string | null
  purchase_cost: number
  warranty_expires_at?: string | null
  status: 'in_use' | 'in_stock' | 'in_repair' | 'disposed' | 'retired'
  notes: string
  created_at: string
  updated_at: string
}

export interface AssetSummaryDTO {
  total_assets: number
  active_assets: number
  total_valuation: number
  warranty_expiring_count: number
}

export interface SoftwareLicenseDTO {
  id: string
  software_name: string
  publisher: string
  license_key: string
  license_type: string
  total_seats: number
  cost: number
  purchased_at?: string | null
  expires_at?: string | null
  notes: string
  created_at: string
  updated_at: string
}

export interface LicenseComplianceSummaryDTO {
  license_id: string
  software_name: string
  publisher: string
  license_type: string
  total_seats: number
  allocated_seats: number
  installed_detected: number
  expires_at?: string | null
  status: 'compliant' | 'over_allocated' | 'expiring_soon' | 'expired'
}

// Phase 2: device_groups, as served by GET /api/groups
// (device-management/inventory_handler.go:340). The envelope is {groups,count},
// not a bare array — see the note in getDeviceGroups below.
// GroupMemberCount embeds DeviceGroup and adds member_count.
export interface DeviceGroupDTO {
  id: string
  name: string
  description: string
  created_at: string
  updated_at: string
  member_count: number
}

// Phase 13: Agent Self-Update & Rollouts
export interface AgentReleaseDTO {
  id: string
  version: string
  os_name: string
  arch: string
  file_name: string
  file_size: number
  sha256_checksum: string
  changelog: string
  is_active: boolean
  uploaded_by: string
  created_at: string
}

export interface UpdateCampaignDTO {
  id: string
  name: string
  description?: string
  target_version: string
  target_type: string
  target_id: string
  batch_size: number
  stagger_interval_sec: number
  status: string
  created_by: string
  created_at: string
  updated_at: string
}

// Phase 15: Device Maintenance
// Every field name below is a verbatim copy of the json tag on the matching
// struct in server/modules/maintenance/model.go. The pointers there (completed_at,
// exit_code, output_log, error_message) are nullable in SQLite, so they are
// `| null` here and every render site must guard rather than call a method on
// them. A page that renames one of these renders `undefined` with no error, which
// is how the earlier console/server drift shipped.

// maintenance.Job
export interface MaintenanceJobDTO {
  id: string
  name: string
  task_type: string
  target_type: 'device' | 'group' | 'all'
  target_id: string
  created_by: string
  total_tasks: number
  dispatched: number
  skipped: number
  completed: number
  failed: number
  status: 'running' | 'completed' | 'partial' | 'failed'
  started_at: string
  completed_at?: string | null
}

// maintenance.Task
export interface MaintenanceTaskDTO {
  id: string
  job_id: string
  device_id: string
  hostname: string
  task_type: string
  status: 'pending' | 'dispatched' | 'running' | 'completed' | 'failed' | 'skipped'
  // Empty until the agent reports a step. Full Health Scan posts once per step.
  step: string
  exit_code?: number | null
  output_log?: string | null
  error_message?: string | null
  reboot_required: boolean
  bytes_freed: number
  started_at?: string | null
  completed_at?: string | null
  created_at: string
  updated_at: string
}

// maintenance.TaskInfo — the server's own TaskCatalog entry, served verbatim so
// the chooser never hardcodes labels the agent may not implement.
export interface TaskInfoDTO {
  id: string
  label: string
  description: string
  // True when the operation may interrupt a logged-in user or needs a reboot;
  // the console confirms before dispatching one of these.
  disruptive: boolean
}

// maintenance.JobProgress (server/modules/maintenance/repository.go) — the
// poll-while-running read. `percent` is computed server-side and already covers
// skipped devices, so the console must not recompute it from completed+failed.
export interface MaintenanceProgressDTO {
  job_id: string
  // A bare `string` in Go (repository.go, JobProgress.Status), NOT the job
  // status union — so the console narrows it before it writes it back onto a
  // MaintenanceJobDTO. Typing it as the union here would be a claim the server
  // does not make.
  status: string
  total_tasks: number
  // Server-side aggregate of tasks still pending/dispatched/running. The server
  // does NOT send a `pending` key, so this console never reads one.
  remaining: number
  dispatched: number
  skipped: number
  completed: number
  failed: number
  bytes_freed: number
  reboot_required: number
  percent: number
  completed_at?: string | null
}

// The response to POST /api/maintenance/jobs. `skipped` is the offline-target
// count, which matters here: an operator who targets the whole fleet on a
// Sunday sees most devices skipped and needs to be told that, not shown 0
// completed with no explanation.
export interface MaintenanceRunResponse {
  job: MaintenanceJobDTO
  total_targets: number
  dispatched_live: number
  skipped: number
}

// Phase 7: User Management
// The server sends `is_active` (a bool), not a `status` string, and a
// nullable display_name; the console's `status` was always undefined, so every
// user row rendered an unknown badge and the deactivate button had no state to
// reflect.
export interface UserDTO {
  id: string
  username: string
  display_name: string | null
  role: 'admin' | 'technician' | 'viewer'
  is_active: boolean
  last_login_at?: string | null
  created_at: string
  updated_at: string
}


// Phase 11: Remote Control
// A live relay session, as returned by POST .../remotecontrol/session and
// GET .../remotecontrol/sessions. `mode` is 'full_control' or 'view_only';
// the session row is the record, the relay is the live connection.
export type RemoteControlMode = 'full_control' | 'view_only'

export interface RemoteControlSessionDTO {
  id: string
  device_id: string
  operator_id: string
  session_mode: RemoteControlMode
  status: 'active' | 'ended' | 'rejected'
  frames_transmitted: number
  bytes_transmitted: number
  input_events_count: number
  started_at: string
  ended_at?: string | null
  created_at: string
  // Joined in by the repository so history needs no second request.
  hostname?: string
  site?: string
  operator_name?: string
}

// What the agent reports about itself in its `hello` control message, before
// the first frame. The console uses this to refuse to present a session that
// cannot actually work: `capture: false` means the endpoint will never send a
// real desktop, and saying so up front is the difference between an honest
// error and a black rectangle.
export interface RemoteControlCapabilities {
  capture: boolean
  mouse: boolean
  keyboard: boolean
  reason?: string
}
