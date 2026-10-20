import { lazy, Suspense, useState, type ReactNode } from 'react'
import { BrowserRouter, Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ConsoleShell } from './components/layout/ConsoleShell'
import { ErrorBoundary } from './components/ErrorBoundary'
import { InteractiveTerminalModal } from './components/InteractiveTerminalModal'
import { isWindowsDevice } from './components/terminalPlatform'
import { RemoteControlModal } from './components/RemoteControlModal'
import { RemoteExecModal } from './components/RemoteExecModal'
import { AuthProvider, useAuth } from './context/AuthContext'
import { ThemeProvider } from './context/ThemeContext'
import { ToastProvider } from './context/ToastContext'
import { I18nProvider } from './i18n/I18nContext'
import { useT } from './i18n/core'
import { usePermission } from './hooks/usePermission'
import { LoginPage } from './pages/LoginPage'
import type { DeviceDTO } from './types/api'

// Lazy-loaded pages for bundle code-splitting and faster initial paint
const AgentUpdatesPage = lazy(() => import('./pages/AgentUpdatesPage').then(m => ({ default: m.AgentUpdatesPage })))
const AlertsPage = lazy(() => import('./pages/AlertsPage').then(m => ({ default: m.AlertsPage })))
const AssetLicensePage = lazy(() => import('./pages/AssetLicensePage').then(m => ({ default: m.AssetLicensePage })))
const AuditPage = lazy(() => import('./pages/AuditPage').then(m => ({ default: m.AuditPage })))
const DashboardPage = lazy(() => import('./pages/DashboardPage').then(m => ({ default: m.DashboardPage })))
const DevicesPage = lazy(() => import('./pages/DevicesPage').then(m => ({ default: m.DevicesPage })))
const LogPage = lazy(() => import('./pages/LogPage').then(m => ({ default: m.LogPage })))
const MaintenancePage = lazy(() => import('./pages/MaintenancePage').then(m => ({ default: m.MaintenancePage })))
const NetworkFilterPage = lazy(() => import('./pages/NetworkFilterPage').then(m => ({ default: m.NetworkFilterPage })))
const PatchesPage = lazy(() => import('./pages/PatchesPage').then(m => ({ default: m.PatchesPage })))
const ReportsPage = lazy(() => import('./pages/ReportsPage').then(m => ({ default: m.ReportsPage })))
const RemoteControlPage = lazy(() => import('./pages/RemoteControlPage').then(m => ({ default: m.RemoteControlPage })))
const SettingsPage = lazy(() => import('./pages/SettingsPage').then(m => ({ default: m.SettingsPage })))
const SoftwarePage = lazy(() => import('./pages/SoftwarePage').then(m => ({ default: m.SoftwarePage })))
const SystemPolicyPage = lazy(() => import('./pages/SystemPolicyPage'))
const TasksSchedulerPage = lazy(() => import('./pages/TasksSchedulerPage').then(m => ({ default: m.TasksSchedulerPage })))
// xterm.js is a third of the console's weight; it loads when a Linux or macOS
// terminal is first opened, not at login.
const PtyTerminalModal = lazy(() => import('./components/PtyTerminalModal').then(m => ({ default: m.PtyTerminalModal })))
const UsersPage = lazy(() => import('./pages/UsersPage').then(m => ({ default: m.UsersPage })))

function Restricted({ role, children }: { role: 'technician' | 'admin'; children: ReactNode }) {
  const { can } = usePermission()
  const { t } = useT()
  return can(role) ? children : <div className="empty-state" role="alert"><h1>{t('app.access_restricted')}</h1><p>{t('app.requires_role', { role: t(`app.role_${role}`) })}</p><Link to="/dashboard">{t('app.return_to_dashboard')}</Link></div>
}

/** Human name for the error boundary's headline, keyed off the first path segment. */
const PAGE_LABEL_KEYS: Record<string, string> = {
  dashboard: 'app.page_dashboard',
  devices: 'app.page_devices',
  software: 'app.page_software',
  scheduler: 'app.page_scheduler',
  assets: 'app.page_assets',
  updates: 'app.page_updates',
  patches: 'app.page_patches',
  maintenance: 'app.page_maintenance',
  remotecontrol: 'app.page_remotecontrol',
  filter: 'app.page_filter',
  'system-policy': 'app.page_system_policy',
  alerts: 'app.page_alerts',
  reports: 'app.page_reports',
  users: 'app.page_users',
  audit: 'app.page_audit',
  log: 'app.page_log',
  settings: 'app.page_settings',
}

const pageLabel = (pathname: string, t: (key: string) => string) => {
  const key = PAGE_LABEL_KEYS[pathname.split('/')[1]]
  return key ? t(key) : t('app.page_this')
}

function SubtabPage({ section }: { section: 'software' | 'scheduler' | 'assets' | 'updates' | 'settings' }) {
  const { tab } = useParams()
  const navigate = useNavigate()
  const onChange = (value: string) => navigate(`/${section}/${value}`)
  switch (section) {
    case 'software':
      return tab === 'packages' || tab === 'deployments'
        ? <SoftwarePage activeTab={tab} onTabChange={onChange} /> : <Navigate to="/software/packages" replace />
    case 'scheduler':
      return tab === 'schedules' || tab === 'runs'
        ? <TasksSchedulerPage activeTab={tab} onTabChange={onChange} /> : <Navigate to="/scheduler/schedules" replace />
    case 'assets':
      return tab === 'hardware' || tab === 'licenses'
        ? <AssetLicensePage activeTab={tab} onTabChange={onChange} /> : <Navigate to="/assets/hardware" replace />
    case 'updates':
      return tab === 'releases' || tab === 'campaigns'
        ? <AgentUpdatesPage activeTab={tab} onTabChange={onChange} /> : <Navigate to="/updates/releases" replace />
    case 'settings':
      return tab === 'directory' || tab === 'backup'
        ? <SettingsPage activeTab={tab} onTabChange={onChange} /> : <Navigate to="/settings/directory" replace />
  }
}

function ConsoleRoot() {
  const { isAuthenticated, loading } = useAuth()
  const location = useLocation()
  const [execDevice, setExecDevice] = useState<DeviceDTO | null>(null)
  const [termDevice, setTermDevice] = useState<DeviceDTO | null>(null)
  const [termShell, setTermShell] = useState('powershell')
  const [rcDevice, setRcDevice] = useState<DeviceDTO | null>(null)
  const { t } = useT()

  if (loading) return <div className="app-loading-screen" role="status"><div className="app-loading-spinner" /><p>{t('app.opening_workspace')}</p></div>
  if (!isAuthenticated) return location.pathname === '/login'
    ? <LoginPage /> : <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  if (location.pathname === '/login') {
    const from = location.state?.from
    return <Navigate to={typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') && !from.startsWith('/login') ? from : '/dashboard'} replace />
  }

  return <ConsoleShell>
    {/* Inside the shell, not around it: a page that throws must not take the
        sidebar and topbar with it. */}
    <ErrorBoundary label={pageLabel(location.pathname, t)}>
      <Suspense fallback={<div className="page-loader" role="status"><div className="app-loading-spinner" /><p className="text-sm text-slate-500 mt-2">{t('common.loading')}</p></div>}>
        <Routes>
          <Route path="/" element={<Navigate to="/dashboard" replace />} />
          <Route path="/dashboard" element={<DashboardPage />} />
          <Route path="/devices" element={<DevicesPage onOpenExec={setExecDevice} onOpenTerminal={(device, shell) => { setTermShell(shell); setTermDevice(device) }} onOpenRemoteControl={setRcDevice} />} />
          <Route path="/software/:tab?" element={<SubtabPage section="software" />} />
          <Route path="/scheduler/:tab?" element={<SubtabPage section="scheduler" />} />
          <Route path="/assets/:tab?" element={<SubtabPage section="assets" />} />
          <Route path="/updates/:tab?" element={<SubtabPage section="updates" />} />
          <Route path="/patches" element={<PatchesPage />} />
          <Route path="/maintenance" element={<MaintenancePage />} />
          <Route path="/remotecontrol" element={<RemoteControlPage onOpenSession={setRcDevice} onOpenTerminal={(device, shell) => { setTermShell(shell); setTermDevice(device) }} />} />
          <Route path="/filter" element={<NetworkFilterPage />} />
          <Route path="/system-policy" element={<SystemPolicyPage />} />
          <Route path="/alerts" element={<AlertsPage />} />
          <Route path="/reports" element={<ReportsPage />} />
          <Route path="/users" element={<Restricted role="admin"><UsersPage /></Restricted>} />
          <Route path="/audit" element={<Restricted role="technician"><AuditPage /></Restricted>} />
          <Route path="/log" element={<Restricted role="admin"><LogPage /></Restricted>} />
          <Route path="/settings/:tab?" element={<Restricted role="admin"><SubtabPage section="settings" /></Restricted>} />
          <Route path="*" element={<div className="empty-state"><h1>{t('app.page_not_found')}</h1><Link to="/dashboard">{t('app.return_to_dashboard')}</Link></div>} />
        </Routes>
      </Suspense>
    </ErrorBoundary>
    <RemoteExecModal device={execDevice} onClose={() => setExecDevice(null)} />
    {termDevice && !isWindowsDevice(termDevice)
      ? <Suspense fallback={null}><PtyTerminalModal device={termDevice} shell={termShell} onClose={() => setTermDevice(null)} /></Suspense>
      : <InteractiveTerminalModal device={termDevice} shell={termShell} onClose={() => setTermDevice(null)} />}
    <RemoteControlModal device={rcDevice} onClose={() => setRcDevice(null)} />
  </ConsoleShell>
}

export function App() {
  return <I18nProvider><ThemeProvider><AuthProvider><ToastProvider><BrowserRouter><ConsoleRoot /></BrowserRouter></ToastProvider></AuthProvider></ThemeProvider></I18nProvider>
}

export default App
