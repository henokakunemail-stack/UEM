import { useState, type ReactNode } from 'react'
import { BrowserRouter, Link, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ConsoleShell } from './components/layout/ConsoleShell'
import { ErrorBoundary } from './components/ErrorBoundary'
import { InteractiveTerminalModal } from './components/InteractiveTerminalModal'
import { RemoteControlModal } from './components/RemoteControlModal'
import { RemoteExecModal } from './components/RemoteExecModal'
import { AuthProvider, useAuth } from './context/AuthContext'
import { ThemeProvider } from './context/ThemeContext'
import { ToastProvider } from './context/ToastContext'
import { usePermission } from './hooks/usePermission'
import { AgentUpdatesPage } from './pages/AgentUpdatesPage'
import { AlertsPage } from './pages/AlertsPage'
import { AssetLicensePage } from './pages/AssetLicensePage'
import { AuditPage } from './pages/AuditPage'
import { DashboardPage } from './pages/DashboardPage'
import { DevicesPage } from './pages/DevicesPage'
import { LoginPage } from './pages/LoginPage'
import { LogPage } from './pages/LogPage'
import { MaintenancePage } from './pages/MaintenancePage'
import { NetworkFilterPage } from './pages/NetworkFilterPage'
import { PatchesPage } from './pages/PatchesPage'
import { ReportsPage } from './pages/ReportsPage'
import { RemoteControlPage } from './pages/RemoteControlPage'
import { SettingsPage } from './pages/SettingsPage'
import { SoftwarePage } from './pages/SoftwarePage'
import { TasksSchedulerPage } from './pages/TasksSchedulerPage'
import { UsersPage } from './pages/UsersPage'
import type { DeviceDTO } from './types/api'

function Restricted({ role, children }: { role: 'technician' | 'admin'; children: ReactNode }) {
  const { can } = usePermission()
  return can(role) ? children : <div className="empty-state" role="alert"><h1>Access restricted</h1><p>This page requires {role} access.</p><Link to="/dashboard">Return to dashboard</Link></div>
}

/** Human name for the error boundary's headline, keyed off the first path segment. */
const PAGE_LABELS: Record<string, string> = {
  dashboard: 'Dashboard',
  devices: 'Devices',
  software: 'Software',
  scheduler: 'Scheduler',
  assets: 'Assets & licenses',
  updates: 'Agent updates',
  patches: 'Patch management',
  maintenance: 'Maintenance',
  remotecontrol: 'Remote control',
  filter: 'Network filter',
  alerts: 'Alerts',
  reports: 'Reports',
  users: 'User management',
  audit: 'Audit trail',
  log: 'Server log',
  settings: 'Settings',
}

const pageLabel = (pathname: string) => PAGE_LABELS[pathname.split('/')[1]] ?? 'This page'

function SubtabPage({ section }: { section: 'software' | 'scheduler' | 'assets' | 'updates' }) {
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
  }
}

function ConsoleRoot() {
  const { isAuthenticated, loading } = useAuth()
  const location = useLocation()
  const [execDevice, setExecDevice] = useState<DeviceDTO | null>(null)
  const [termDevice, setTermDevice] = useState<DeviceDTO | null>(null)
  const [termShell, setTermShell] = useState('powershell')
  const [rcDevice, setRcDevice] = useState<DeviceDTO | null>(null)

  if (loading) return <div className="app-loading-screen" role="status"><div className="app-loading-spinner" /><p>Opening workspace…</p></div>
  if (!isAuthenticated) return location.pathname === '/login'
    ? <LoginPage /> : <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  if (location.pathname === '/login') {
    const from = location.state?.from
    return <Navigate to={typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') && !from.startsWith('/login') ? from : '/dashboard'} replace />
  }

  return <ConsoleShell>
    {/* Inside the shell, not around it: a page that throws must not take the
        sidebar and topbar with it. */}
    <ErrorBoundary label={pageLabel(location.pathname)}>
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
        <Route path="/remotecontrol" element={<RemoteControlPage onOpenSession={setRcDevice} />} />
        <Route path="/filter" element={<NetworkFilterPage />} />
        <Route path="/alerts" element={<AlertsPage />} />
        <Route path="/reports" element={<ReportsPage />} />
        <Route path="/users" element={<Restricted role="admin"><UsersPage /></Restricted>} />
        <Route path="/audit" element={<Restricted role="technician"><AuditPage /></Restricted>} />
        <Route path="/log" element={<Restricted role="admin"><LogPage /></Restricted>} />
        <Route path="/settings" element={<Restricted role="admin"><SettingsPage /></Restricted>} />
        <Route path="*" element={<div className="empty-state"><h1>Page not found</h1><Link to="/dashboard">Return to dashboard</Link></div>} />
      </Routes>
    </ErrorBoundary>
    <RemoteExecModal device={execDevice} onClose={() => setExecDevice(null)} />
    <InteractiveTerminalModal device={termDevice} shell={termShell} onClose={() => setTermDevice(null)} />
    <RemoteControlModal device={rcDevice} onClose={() => setRcDevice(null)} />
  </ConsoleShell>
}

export function App() {
  return <ThemeProvider><AuthProvider><ToastProvider><BrowserRouter><ConsoleRoot /></BrowserRouter></ToastProvider></AuthProvider></ThemeProvider>
}

export default App
