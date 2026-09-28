import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { Activity, Bell, Boxes, CalendarClock, ChevronDown, ChevronRight, FileChartColumn, Globe, History, KeyRound, LayoutDashboard, LogOut, Menu, Monitor, Package, Rocket, ScreenShare, ScrollText, ShieldCheck, Terminal, Users, Wrench, X, type LucideIcon } from 'lucide-react'
import { useAuth } from '../../context/AuthContext'
import { ThemeToggle } from '../../context/ThemeContext'
import { usePermission } from '../../hooks/usePermission'

interface NavItem {
  label: string
  path: string
  icon: LucideIcon
  role?: 'technician' | 'admin'
  children?: { label: string; path: string; icon: LucideIcon }[]
}
const groups: { label: string; items: NavItem[] }[] = [
  { label: 'Overview', items: [
    { label: 'Dashboard', path: '/dashboard', icon: LayoutDashboard },
    { label: 'Alerts', path: '/alerts', icon: Bell },
    { label: 'Devices', path: '/devices', icon: Monitor },
  ] },
  { label: 'Operations', items: [
    { label: 'Software', path: '/software', icon: Package, children: [
      { label: 'Package repository', path: '/software/packages', icon: Boxes },
      { label: 'Deployments', path: '/software/deployments', icon: Rocket },
    ] },
    { label: 'Task scheduler', path: '/scheduler', icon: CalendarClock, children: [
      { label: 'Scripts', path: '/scheduler/scripts', icon: Terminal },
      { label: 'Schedules', path: '/scheduler/schedules', icon: CalendarClock },
      { label: 'Run history', path: '/scheduler/runs', icon: History },
    ] },
    { label: 'Maintenance', path: '/maintenance', icon: Wrench },
    { label: 'Remote control', path: '/remotecontrol', icon: ScreenShare, role: 'technician' },
  ] },
  { label: 'Security & assets', items: [
    { label: 'Patch management', path: '/patches', icon: ShieldCheck },
    { label: 'Network filter', path: '/filter', icon: Globe },
    { label: 'Assets & licenses', path: '/assets', icon: Boxes, children: [
      { label: 'Hardware assets', path: '/assets/hardware', icon: Monitor },
      { label: 'Software licenses', path: '/assets/licenses', icon: KeyRound },
    ] },
    { label: 'Agent updates', path: '/updates', icon: Rocket, children: [
      { label: 'Releases', path: '/updates/releases', icon: Package },
      { label: 'Campaigns', path: '/updates/campaigns', icon: Rocket },
    ] },
  ] },
  { label: 'Administration', items: [
    { label: 'Reports', path: '/reports', icon: FileChartColumn },
    { label: 'Audit trail', path: '/audit', icon: History, role: 'technician' },
    { label: 'Server log', path: '/log', icon: ScrollText, role: 'admin' },
    { label: 'Users & access', path: '/users', icon: Users, role: 'admin' },
  ] },
]

function Navigation({ onNavigate }: { onNavigate: () => void }) {
  const { pathname } = useLocation()
  const { can } = usePermission()
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  return <>
    <Link to="/dashboard" className="console-brand" onClick={onNavigate}>
      <span className="console-brand-mark"><Monitor size={23} /></span>
      <span><strong>Endpoint Manager</strong><small>ENTERPRISE CONSOLE</small></span>
    </Link>
    <nav className="sidebar-navigation" aria-label="Main navigation">
      {groups.map(group => <section className="sidebar-group" key={group.label}>
        <h2>{group.label}</h2>
        {group.items.filter(item => !item.role || can(item.role)).map(item => {
          const Icon = item.icon
          const selected = pathname === item.path || pathname.startsWith(`${item.path}/`)
          const expanded = collapsed[item.path] === undefined ? selected : !collapsed[item.path]
          return <div className="sidebar-item" key={item.path}>
            {item.children ? <>
              <button type="button" className={selected ? 'sidebar-link selected' : 'sidebar-link'} aria-expanded={expanded}
                aria-controls={`nav-${item.path.slice(1)}`} onClick={() => setCollapsed(previous => ({ ...previous, [item.path]: expanded }))}>
                <Icon size={17} /><span>{item.label}</span><ChevronDown size={14} className={expanded ? 'chevron expanded' : 'chevron'} />
              </button>
              {expanded && <div className="sidebar-children" id={`nav-${item.path.slice(1)}`}>
                {item.children.map(child => <NavLink key={child.path} to={child.path} onClick={onNavigate}
                  className={({ isActive }) => isActive ? 'sidebar-link active' : 'sidebar-link'}>
                  <child.icon size={15} /><span>{child.label}</span>
                </NavLink>)}
              </div>}
            </> : <NavLink to={item.path} onClick={onNavigate} className={({ isActive }) => isActive ? 'sidebar-link active' : 'sidebar-link'}>
              <Icon size={17} /><span>{item.label}</span>
            </NavLink>}
          </div>
        })}
      </section>)}
    </nav>
    <div className="sidebar-footer"><ShieldCheck size={17} /><span>Fleet administration<small>Role-based access</small></span></div>
  </>
}

export function ConsoleShell({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth()
  const { pathname } = useLocation()
  const [online, setOnline] = useState<number | null>(null)
  const [healthChecked, setHealthChecked] = useState(false)
  const drawer = useRef<HTMLDialogElement>(null)
  const opener = useRef<HTMLButtonElement>(null)
  const previousOverflow = useRef('')
  const active = groups.flatMap(group => group.items).find(item => pathname === item.path || pathname.startsWith(`${item.path}/`))
  const subpage = active?.children?.find(item => item.path === pathname)

  useEffect(() => {
    let stopped = false
    let controller: AbortController | null = null
    async function check() {
      if (document.hidden || controller) return
      controller = new AbortController()
      const timeout = setTimeout(() => controller?.abort(), 8000)
      try {
        const response = await fetch('/healthz', { signal: controller.signal })
        if (!response.ok) throw new Error('Health check failed')
        const body = await response.json()
        if (typeof body.agents_online !== 'number' || !Number.isFinite(body.agents_online) || body.agents_online < 0) throw new Error('Invalid health response')
        if (!stopped) setOnline(body.agents_online)
      } catch {
        if (!stopped) setOnline(null)
      } finally {
        clearTimeout(timeout)
        controller = null
        if (!stopped) setHealthChecked(true)
      }
    }
    void check()
    const interval = setInterval(check, 10000)
    document.addEventListener('visibilitychange', check)
    return () => { stopped = true; clearInterval(interval); controller?.abort(); document.removeEventListener('visibilitychange', check) }
  }, [])

  function closeDrawer() {
    if (!drawer.current?.open) return
    drawer.current.close()
    document.body.style.overflow = previousOverflow.current
    opener.current?.focus()
  }
  useEffect(() => {
    const media = matchMedia('(min-width: 1280px)')
    const closeOnDesktop = () => { if (media.matches) closeDrawer() }
    media.addEventListener('change', closeOnDesktop)
    return () => {
      media.removeEventListener('change', closeOnDesktop)
      if (drawer.current?.open) document.body.style.overflow = previousOverflow.current
    }
  }, [])

  return <div className="console-shell">
    <a className="skip-link" href="#main-content">Skip to content</a>
    <aside className="console-sidebar"><Navigation onNavigate={closeDrawer} /></aside>
    <dialog ref={drawer} className="sidebar-drawer" aria-label="Navigation" onCancel={event => { event.preventDefault(); closeDrawer() }}
      onClick={event => { if (event.target === event.currentTarget) closeDrawer() }}>
      <div className="sidebar-drawer-body">
        <button type="button" className="btn-icon drawer-close" aria-label="Close navigation" onClick={closeDrawer}><X size={18} /></button>
        <Navigation onNavigate={closeDrawer} />
      </div>
    </dialog>
    <div className="console-workspace">
      <header className="console-topbar">
        <div className="topbar-location">
          <button type="button" ref={opener} className="btn-icon menu-toggle" aria-label="Open navigation" onClick={() => {
            previousOverflow.current = document.body.style.overflow
            drawer.current?.showModal()
            document.body.style.overflow = 'hidden'
          }}><Menu size={19} /></button>
          <span className="topbar-workspace">Workspace</span><ChevronRight size={14} className="topbar-separator" />
          <span className="topbar-title">{active?.label ?? 'Page not found'}</span>
          {subpage && <><ChevronRight size={14} className="topbar-separator" /><span className="topbar-subpage">{subpage.label}</span></>}
        </div>
        <div className="topbar-actions">
          <span className="connection-state" title={!healthChecked ? 'Checking server' : online === null ? 'Server unavailable' : `${online} agents connected`}>
            <Activity size={15} className={online === null ? 'text-warning' : 'text-success'} />
            <span>{!healthChecked ? 'Connecting…' : online === null ? 'Unavailable' : `${online} online`}</span>
          </span>
          <ThemeToggle />
          <div className="operator-profile"><span className="operator-avatar">{user?.usr?.slice(0, 1).toUpperCase()}</span>
            <span className="operator-name">{user?.usr}<small>{user?.rol}</small></span>
          </div>
          <button type="button" className="btn-icon" onClick={logout} aria-label="Sign out" title="Sign out"><LogOut size={17} /></button>
        </div>
      </header>
      <main className="main-viewport" id="main-content" tabIndex={-1}>{children}</main>
      <footer className="console-footer"><span>Enterprise Endpoint Manager</span><span>Operations workspace</span></footer>
    </div>
  </div>
}
