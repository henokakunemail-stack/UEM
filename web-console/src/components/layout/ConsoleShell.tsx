import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { Activity, Bell, Boxes, CalendarClock, ChevronDown, ChevronRight, Database, FileChartColumn, Globe, History, KeyRound, LayoutDashboard, LogOut, Menu, Monitor, Package, Rocket, ScreenShare, ScrollText, Settings, ShieldCheck, Sliders, Users, Wrench, X, type LucideIcon } from 'lucide-react'
import { useAuth } from '../../context/AuthContext'
import { ThemeToggle } from '../../context/ThemeContext'
import { LanguageToggle } from '../../i18n/LanguageToggle'
import { usePermission } from '../../hooks/usePermission'
import { useT } from '../../i18n/core'

interface NavItem {
  labelKey: string
  path: string
  icon: LucideIcon
  role?: 'technician' | 'admin'
  children?: { labelKey: string; path: string; icon: LucideIcon }[]
}
const groups: { labelKey: string; items: NavItem[] }[] = [
  { labelKey: 'nav.overview', items: [
    { labelKey: 'nav.dashboard', path: '/dashboard', icon: LayoutDashboard },
    { labelKey: 'nav.alerts', path: '/alerts', icon: Bell },
    { labelKey: 'nav.devices', path: '/devices', icon: Monitor },
  ] },
  { labelKey: 'nav.operations', items: [
    { labelKey: 'nav.software', path: '/software', icon: Package, children: [
      { labelKey: 'nav.package_repository', path: '/software/packages', icon: Boxes },
      { labelKey: 'nav.deployments', path: '/software/deployments', icon: Rocket },
    ] },
    { labelKey: 'nav.task_scheduler', path: '/scheduler', icon: CalendarClock, children: [
      { labelKey: 'nav.schedules', path: '/scheduler/schedules', icon: CalendarClock },
      { labelKey: 'nav.run_history', path: '/scheduler/runs', icon: History },
    ] },
    { labelKey: 'nav.maintenance', path: '/maintenance', icon: Wrench },
    { labelKey: 'nav.remote_control', path: '/remotecontrol', icon: ScreenShare, role: 'technician' },
  ] },
  { labelKey: 'nav.security_assets', items: [
    { labelKey: 'nav.patch_management', path: '/patches', icon: ShieldCheck },
    { labelKey: 'nav.network_filter', path: '/filter', icon: Globe },
    { labelKey: 'nav.group_policy', path: '/system-policy', icon: Sliders },
    { labelKey: 'nav.assets_licenses', path: '/assets', icon: Boxes, children: [
      { labelKey: 'nav.hardware_assets', path: '/assets/hardware', icon: Monitor },
      { labelKey: 'nav.software_licenses', path: '/assets/licenses', icon: KeyRound },
    ] },
    { labelKey: 'nav.agent_updates', path: '/updates', icon: Rocket, children: [
      { labelKey: 'nav.releases', path: '/updates/releases', icon: Package },
      { labelKey: 'nav.campaigns', path: '/updates/campaigns', icon: Rocket },
    ] },
  ] },
  { labelKey: 'nav.administration', items: [
    { labelKey: 'nav.reports', path: '/reports', icon: FileChartColumn },
    { labelKey: 'nav.audit_trail', path: '/audit', icon: History, role: 'technician' },
    { labelKey: 'nav.server_log', path: '/log', icon: ScrollText, role: 'admin' },
    { labelKey: 'nav.users_access', path: '/users', icon: Users, role: 'admin' },
    { labelKey: 'nav.settings', path: '/settings', icon: Settings, role: 'admin', children: [
      { labelKey: 'nav.directory_sync', path: '/settings/directory', icon: Users },
      { labelKey: 'nav.backup_restore', path: '/settings/backup', icon: Database },
    ] },
  ] },
]

function Navigation({ onNavigate }: { onNavigate: () => void }) {
  const { pathname } = useLocation()
  const { can } = usePermission()
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const { t } = useT()
  return <>
    <Link to="/dashboard" className="console-brand" onClick={onNavigate}>
      <span className="console-brand-mark"><Monitor size={23} /></span>
      <span><strong>{t('nav.brand_name')}</strong><small>{t('nav.brand_tagline')}</small></span>
    </Link>
    <nav className="sidebar-navigation" aria-label={t('nav.main_navigation')}>
      {groups.map(group => <section className="sidebar-group" key={group.labelKey}>
        <h2>{t(group.labelKey)}</h2>
        {group.items.filter(item => !item.role || can(item.role)).map(item => {
          const Icon = item.icon
          const selected = pathname === item.path || pathname.startsWith(`${item.path}/`)
          const expanded = collapsed[item.path] === undefined ? selected : !collapsed[item.path]
          return <div className="sidebar-item" key={item.path}>
            {item.children ? <>
              <button type="button" className={selected ? 'sidebar-link selected' : 'sidebar-link'} aria-expanded={expanded}
                aria-controls={`nav-${item.path.slice(1)}`} onClick={() => setCollapsed(previous => ({ ...previous, [item.path]: expanded }))}>
                <Icon size={17} /><span>{t(item.labelKey)}</span><ChevronDown size={14} className={expanded ? 'chevron expanded' : 'chevron'} />
              </button>
              {expanded && <div className="sidebar-children" id={`nav-${item.path.slice(1)}`}>
                {item.children.map(child => <NavLink key={child.path} to={child.path} onClick={onNavigate}
                  className={({ isActive }) => isActive ? 'sidebar-link active' : 'sidebar-link'}>
                  <child.icon size={15} /><span>{t(child.labelKey)}</span>
                </NavLink>)}
              </div>}
            </> : <NavLink to={item.path} onClick={onNavigate} className={({ isActive }) => isActive ? 'sidebar-link active' : 'sidebar-link'}>
              <Icon size={17} /><span>{t(item.labelKey)}</span>
            </NavLink>}
          </div>
        })}
      </section>)}
    </nav>
    <div className="sidebar-footer"><ShieldCheck size={17} /><span>{t('nav.footer_title')}<small>{t('nav.footer_subtitle')}</small></span></div>
  </>
}

export function ConsoleShell({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth()
  const { t } = useT()
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
    // Captured now: by the time the cleanup runs the ref may already point at
    // nothing, and an open drawer would leave the page unscrollable.
    const node = drawer.current
    const overflow = previousOverflow
    return () => {
      media.removeEventListener('change', closeOnDesktop)
      if (node?.open) document.body.style.overflow = overflow.current
    }
  }, [])

  return <div className="console-shell">
    <a className="skip-link" href="#main-content">{t('nav.skip_to_content')}</a>
    <aside className="console-sidebar"><Navigation onNavigate={closeDrawer} /></aside>
    <dialog ref={drawer} className="sidebar-drawer" aria-label={t('nav.navigation')} onCancel={event => { event.preventDefault(); closeDrawer() }}
      onClick={event => { if (event.target === event.currentTarget) closeDrawer() }}>
      <div className="sidebar-drawer-body">
        <button type="button" className="btn-icon drawer-close" aria-label={t('nav.close_navigation')} onClick={closeDrawer}><X size={18} /></button>
        <Navigation onNavigate={closeDrawer} />
      </div>
    </dialog>
    <div className="console-workspace">
      <header className="console-topbar">
        <div className="topbar-location">
          <button type="button" ref={opener} className="btn-icon menu-toggle" aria-label={t('nav.open_navigation')} onClick={() => {
            previousOverflow.current = document.body.style.overflow
            drawer.current?.showModal()
            document.body.style.overflow = 'hidden'
          }}><Menu size={19} /></button>
          <span className="topbar-workspace">{t('nav.workspace')}</span><ChevronRight size={14} className="topbar-separator" />
          <span className="topbar-title">{active ? t(active.labelKey) : t('nav.page_not_found')}</span>
          {subpage && <><ChevronRight size={14} className="topbar-separator" /><span className="topbar-subpage">{t(subpage.labelKey)}</span></>}
        </div>
        <div className="topbar-actions">
          <span className="connection-state" title={!healthChecked ? t('nav.checking_server') : online === null ? t('nav.server_unavailable') : t('nav.agents_connected', { count: online })}>
            <Activity size={15} className={online === null ? 'text-warning' : 'text-success'} />
            <span>{!healthChecked ? t('nav.connecting') : online === null ? t('nav.unavailable') : t('nav.count_online', { count: online })}</span>
          </span>
          <LanguageToggle />
          <ThemeToggle />
          <div className="operator-profile"><span className="operator-avatar">{user?.usr?.slice(0, 1).toUpperCase()}</span>
            <span className="operator-name">{user?.usr}<small>{user?.rol}</small></span>
          </div>
          <button type="button" className="btn-icon" onClick={logout} aria-label={t('nav.sign_out')} title={t('nav.sign_out')}><LogOut size={17} /></button>
        </div>
      </header>
      <main className="main-viewport" id="main-content" tabIndex={-1}>{children}</main>
      <footer className="console-footer"><span>{t('nav.footer_product')}</span><span>{t('nav.footer_workspace')}</span></footer>
    </div>
  </div>
}
