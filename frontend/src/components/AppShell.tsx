import { useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import {
  ChevronRight,
  Gauge,
  KeyRound,
  Layers,
  LogOut,
  Menu,
  Moon,
  ScrollText,
  Server,
  Shield,
  SlidersHorizontal,
  Sun,
  Users,
  UsersRound,
  X,
} from 'lucide-react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useTheme } from '../lib/theme'
import { strandState } from '../lib/status'
import { CommandPalette } from './CommandPalette'
import type { CommandTarget } from './CommandPalette'
import { LinkStrand } from './LinkStrand'
import { Mark } from './Mark'
import { EnvironmentDot, IconButton, KeyHint } from './primitives'

/**
 * The deck has two levels of navigation because the work has two levels: which
 * part of KubeMG you are in (the rail), and which thing inside it you are
 * looking at (the panel). In the fleet section the panel is the fleet itself —
 * every cluster, with its link state — so jumping between clusters never means
 * going back to a list first.
 */
const SECTIONS = [
  {
    id: 'fleet',
    label: 'Fleet',
    icon: Gauge,
    items: [
      { to: '/', label: 'Overview', icon: Gauge, adminOnly: false },
      { to: '/clusters', label: 'Clusters', icon: Server, adminOnly: true },
      { to: '/explore', label: 'Explore', icon: Layers, adminOnly: false },
    ],
  },
  {
    id: 'access',
    label: 'Access',
    icon: Shield,
    items: [
      { to: '/users', label: 'Users', icon: Users, adminOnly: true },
      { to: '/groups', label: 'Groups', icon: UsersRound, adminOnly: true },
      { to: '/permissions', label: 'Permissions', icon: KeyRound, adminOnly: true },
      // Everyone can reach the audit trail; a non-admin only sees their own actions.
      { to: '/audit', label: 'Audit trail', icon: ScrollText, adminOnly: false },
    ],
  },
  {
    id: 'system',
    label: 'System',
    icon: SlidersHorizontal,
    items: [{ to: '/settings', label: 'Settings', icon: SlidersHorizontal, adminOnly: true }],
  },
] as const

const ACCESS_ROUTES = ['/users', '/groups', '/permissions', '/audit']

/* The palette answers to both chords; the hint shows the one this keyboard has. */
const PALETTE_HINT = /mac/i.test(navigator.platform) ? '⌘K' : 'Ctrl K'

function sectionForPath(pathname: string): string {
  if (pathname.startsWith('/settings')) return 'system'
  if (ACCESS_ROUTES.some((route) => pathname.startsWith(route))) return 'access'
  return 'fleet'
}

export function AppShell({
  title,
  parent,
  actions,
  sidebar,
  children,
}: {
  title: string
  /** Rendered ahead of the title as a breadcrumb, for pages nested under a section. */
  parent?: { label: string; to: string }
  actions?: ReactNode
  /**
   * A third level of navigation, flush against the section panel — what inside
   * this page you are looking at. Explore uses it for the resource tree.
   *
   * Three full-width columns only fit on a wide screen, so between `lg` and
   * `xl` the *section panel* collapses to the icon rail's width rather than the
   * third level disappearing: the page you are on keeps its own navigation, and
   * what you give up is the label next to a section icon, not a tree. Below
   * `lg` all chrome collapses into the mobile sheet, so a page offering a
   * sidebar must still work without one.
   */
  sidebar?: ReactNode
  children: ReactNode
}) {
  const { user, signOut } = useAuth()
  const { clusters } = useClusters()
  const { theme, toggle } = useTheme()
  const { pathname } = useLocation()

  const [navOpen, setNavOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)

  const isAdmin = user?.role === 'admin'
  const sections = SECTIONS.map((section) => ({
    ...section,
    items: section.items.filter((item) => !item.adminOnly || isAdmin),
  })).filter((section) => section.items.length > 0)

  const activeSectionId = sectionForPath(pathname)
  const activeSection = sections.find((section) => section.id === activeSectionId) ?? sections[0]

  const pages = useMemo<CommandTarget[]>(
    () =>
      sections.flatMap((section) =>
        section.items.map((item) => ({
          id: `page-${item.to}`,
          label: item.label,
          hint: section.label,
          to: item.to,
        })),
      ),
    [sections],
  )

  // ⌘K anywhere. The listener lives here because the palette is part of the
  // deck, not of any one page.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((current) => !current)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  useEffect(() => {
    setNavOpen(false)
  }, [pathname])

  const initials = (user?.username ?? '').slice(0, 2).toUpperCase()

  // With a third level on screen the section panel gives up its width between
  // `lg` and `xl` and reads as icons only, the same 60px as the rail.
  const compact = Boolean(sidebar)

  return (
    <div className="min-h-svh bg-bg">
      {/* Level one: which part of KubeMG. */}
      <nav
        aria-label="Sections"
        className="fixed inset-y-0 left-0 z-30 hidden w-15 flex-col items-center gap-1 border-r border-rail-line bg-rail pb-3 lg:flex"
      >
        {/* The mark sits in a slot exactly as tall as the section panel's own
            header, so it shares a centreline with the KubeMG wordmark beside it
            rather than floating two pixels above it. */}
        <Link to="/" title="KubeMG" className="grid h-14 w-full shrink-0 place-items-center">
          {/* The hit target matches a section icon's; only the slot around it is
              taller, so the mark lands on the wordmark's line. */}
          <span className="grid size-10 place-items-center rounded-control text-accent transition-colors hover:bg-rail-raised">
            <Mark className="size-6.5" />
          </span>
          <span className="sr-only">KubeMG</span>
        </Link>

        {sections.map((section) => {
          const active = section.id === activeSection?.id
          return (
            <Link
              key={section.id}
              to={section.items[0].to}
              title={section.label}
              aria-current={active ? 'page' : undefined}
              className={`group relative grid size-10 place-items-center rounded-control transition-colors ${
                active
                  ? 'bg-rail-raised text-rail-fg'
                  : 'text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg'
              }`}
            >
              {active ? (
                <span
                  aria-hidden="true"
                  className="absolute -left-2.5 h-5 w-[3px] rounded-r-full bg-accent"
                />
              ) : null}
              <section.icon aria-hidden="true" className="size-4.5" />
              <span className="sr-only">{section.label}</span>
            </Link>
          )
        })}

        <span className="flex-1" />

        <button
          type="button"
          onClick={toggle}
          title={theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
          className="grid size-9 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg"
        >
          {theme === 'dark' ? (
            <Sun aria-hidden="true" className="size-4" />
          ) : (
            <Moon aria-hidden="true" className="size-4" />
          )}
          <span className="sr-only">
            {theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
          </span>
        </button>
      </nav>

      {/* Level two: what inside it. */}
      <aside
        className={`fixed inset-y-0 left-15 z-20 hidden flex-col border-r border-rail-line bg-rail lg:flex ${
          compact ? 'w-15 xl:w-60' : 'w-60'
        }`}
      >
        <div
          className={`flex h-14 shrink-0 items-center text-[15px] font-semibold tracking-[-0.02em] ${
            compact ? 'justify-center xl:justify-start xl:px-4' : 'px-4'
          }`}
        >
          <span className={compact ? 'hidden xl:inline' : undefined}>
            <span className="text-rail-fg">Kube</span>
            <span className="text-accent">MG</span>
          </span>
          {compact ? (
            <span aria-hidden="true" className="font-mono text-[13px] text-accent xl:hidden">
              MG
            </span>
          ) : null}
        </div>

        <div className={`min-h-0 flex-1 overflow-y-auto pb-3 ${compact ? 'px-2 xl:px-2.5' : 'px-2.5'}`}>
          <p
            className={`label pt-1 pb-2 text-rail-faint ${
              compact ? 'hidden xl:block xl:px-2' : 'px-2'
            }`}
          >
            {activeSection?.label}
          </p>
          <ul className="flex flex-col gap-0.5">
            {activeSection?.items.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.to === '/'}
                  title={compact ? item.label : undefined}
                  className={compact ? compactRailLinkClass : railLinkClass}
                >
                  <item.icon aria-hidden="true" className="size-4 shrink-0" />
                  <span className={compact ? 'hidden xl:inline' : undefined}>{item.label}</span>
                </NavLink>
              </li>
            ))}
          </ul>

          {activeSection?.id === 'fleet' ? (
            <FleetList clusters={clusters} pathname={pathname} compact={compact} />
          ) : null}
        </div>

        <div
          className={`flex shrink-0 items-center gap-2.5 border-t border-rail-line py-3 ${
            compact ? 'flex-col px-2 xl:flex-row xl:px-3' : 'px-3'
          }`}
        >
          <span
            title={compact ? user?.username : undefined}
            className="grid size-8 shrink-0 place-items-center rounded-full bg-rail-raised font-mono text-[12px] font-semibold text-rail-fg"
          >
            {initials}
          </span>
          <span
            className={`min-w-0 flex-1 leading-tight ${compact ? 'hidden xl:block' : undefined}`}
          >
            <span className="block truncate text-[13px] text-rail-fg">{user?.username}</span>
            <span className="label block text-rail-faint">{user?.role}</span>
          </span>
          <button
            type="button"
            onClick={signOut}
            title="Sign out"
            className="grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-danger"
          >
            <LogOut aria-hidden="true" className="size-4" />
            <span className="sr-only">Sign out</span>
          </button>
        </div>
      </aside>

      {navOpen ? (
        <MobileNav
          sections={sections}
          clusters={clusters}
          pathname={pathname}
          theme={theme}
          onToggleTheme={toggle}
          username={user?.username ?? ''}
          role={user?.role ?? ''}
          onSignOut={signOut}
          onClose={() => setNavOpen(false)}
        />
      ) : null}

      {/* Level three, when a page has one. */}
      {sidebar ? (
        <div className="fixed inset-y-0 left-30 z-10 hidden w-56 border-r border-rail-line bg-rail lg:block xl:left-75">
          {sidebar}
        </div>
      ) : null}

      <div className={`flex min-w-0 flex-col ${sidebar ? 'lg:ml-86 xl:ml-131' : 'lg:ml-75'}`}>
        <header className="sticky top-0 z-10 border-b border-line bg-bg/85 backdrop-blur">
          <div className="flex h-14 items-center gap-3 px-4 xl:px-6">
            <IconButton
              label="Open navigation"
              onClick={() => setNavOpen(true)}
              className="lg:hidden"
            >
              <Menu aria-hidden="true" className="size-4.5" />
            </IconButton>

            <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-2">
              {parent ? (
                <>
                  <Link
                    to={parent.to}
                    className="hidden shrink-0 text-[13px] text-muted transition-colors hover:text-fg sm:block"
                  >
                    {parent.label}
                  </Link>
                  <ChevronRight
                    aria-hidden="true"
                    className="hidden size-3.5 shrink-0 text-faint sm:block"
                  />
                </>
              ) : null}
              <h1 className="min-w-0 truncate text-[15px] font-semibold text-fg">{title}</h1>
            </nav>

            <div className="ml-auto flex shrink-0 items-center gap-2">
              <button
                type="button"
                onClick={() => setPaletteOpen(true)}
                className="hidden h-9 items-center gap-2 rounded-control border border-line bg-surface px-3 text-[13px] text-muted transition-colors hover:border-faint/60 hover:text-fg md:flex"
              >
                Jump to…
                <KeyHint>{PALETTE_HINT}</KeyHint>
              </button>
              {actions}
            </div>
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4 xl:p-6">
          <div className="mx-auto min-w-0 max-w-[1440px]">{children}</div>
        </main>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        pages={pages}
        clusters={clusters}
      />
    </div>
  )
}

function railLinkClass({ isActive }: { isActive: boolean }) {
  const base =
    'flex items-center gap-2.5 rounded-control px-2 py-1.5 text-[13.5px] transition-colors'
  return isActive
    ? `${base} bg-rail-raised font-medium text-rail-fg`
    : `${base} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
}

/** The same link, sized for the collapsed panel: glyph only, name on hover. */
function compactRailLinkClass({ isActive }: { isActive: boolean }) {
  const base =
    'flex items-center gap-2.5 rounded-control py-1.5 text-[13.5px] transition-colors justify-center xl:justify-start px-0 xl:px-2'
  return isActive
    ? `${base} bg-rail-raised font-medium text-rail-fg`
    : `${base} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
}

/**
 * FleetList is the fleet in the panel: every cluster, its environment, and its
 * link state on one line. It is the fastest path between two clusters, which is
 * most of what an operator does all day.
 *
 * Collapsed there is no room for a name, so a cluster is its environment dot
 * with the name on hover — the row still switches clusters, which is the point
 * of the list.
 */
function FleetList({
  clusters,
  pathname,
  compact = false,
}: {
  clusters: ReturnType<typeof useClusters>['clusters']
  pathname: string
  compact?: boolean
}) {
  return (
    <div className="mt-5">
      <p
        className={`label flex items-center justify-between pb-2 text-rail-faint ${
          compact ? 'justify-center px-0 xl:justify-between xl:px-2' : 'px-2'
        }`}
      >
        <span className={compact ? 'hidden xl:inline' : undefined}>Clusters</span>
        <span className="font-mono">{clusters.length}</span>
      </p>

      {clusters.length === 0 ? (
        <p
          className={`text-[12px] text-rail-faint ${compact ? 'hidden xl:block xl:px-2' : 'px-2'}`}
        >
          None registered yet.
        </p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {clusters.map((cluster) => {
            const active = pathname === `/clusters/${cluster.id}`
            return (
              <li key={cluster.id}>
                <Link
                  to={`/clusters/${cluster.id}`}
                  aria-current={active ? 'page' : undefined}
                  title={compact ? cluster.name : undefined}
                  className={`flex items-center gap-2 rounded-control py-1.5 transition-colors ${
                    compact ? 'justify-center px-0 xl:justify-start xl:px-2' : 'px-2'
                  } ${active ? 'bg-rail-raised' : 'hover:bg-rail-raised/60'}`}
                >
                  <EnvironmentDot environment={cluster.environment} />
                  <span
                    className={`min-w-0 flex-1 truncate font-mono text-[12.5px] ${
                      active ? 'text-rail-fg' : 'text-rail-muted'
                    } ${compact ? 'hidden xl:block' : ''}`}
                  >
                    {cluster.name}
                  </span>
                  <LinkStrand
                    state={strandState(cluster)}
                    className={`w-8 shrink-0 ${compact ? 'hidden xl:block' : ''}`}
                  />
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** Below the two-level breakpoint both levels collapse into one sheet. */
function MobileNav({
  sections,
  clusters,
  pathname,
  theme,
  onToggleTheme,
  username,
  role,
  onSignOut,
  onClose,
}: {
  sections: Array<{
    id: string
    label: string
    items: ReadonlyArray<{ to: string; label: string; icon: typeof Gauge }>
  }>
  clusters: ReturnType<typeof useClusters>['clusters']
  pathname: string
  theme: 'dark' | 'light'
  onToggleTheme: () => void
  username: string
  role: string
  onSignOut: () => void
  onClose: () => void
}) {
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-40 flex lg:hidden">
      <button
        type="button"
        aria-label="Close navigation"
        onClick={onClose}
        className="scrim-in absolute inset-0 bg-black/55"
      />

      <div
        role="dialog"
        aria-modal="true"
        aria-label="Navigation"
        className="relative flex h-full w-[19rem] max-w-[85%] flex-col border-r border-rail-line bg-rail"
      >
        <div className="flex h-14 shrink-0 items-center gap-2.5 px-4">
          <Mark className="size-6.5 shrink-0 text-accent" />
          <span className="text-[15px] font-semibold tracking-[-0.02em]">
            <span className="text-rail-fg">Kube</span>
            <span className="text-accent">MG</span>
          </span>
          <button
            type="button"
            onClick={onClose}
            className="ml-auto grid size-8 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg"
          >
            <X aria-hidden="true" className="size-4" />
            <span className="sr-only">Close navigation</span>
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-2.5 pb-4">
          {sections.map((section) => (
            <div key={section.id} className="mb-4">
              <p className="label px-2 pb-2 text-rail-faint">{section.label}</p>
              <ul className="flex flex-col gap-0.5">
                {section.items.map((item) => (
                  <li key={item.to}>
                    <NavLink to={item.to} end={item.to === '/'} className={railLinkClass}>
                      <item.icon aria-hidden="true" className="size-4 shrink-0" />
                      {item.label}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          ))}

          <FleetList clusters={clusters} pathname={pathname} />
        </div>

        <div className="flex shrink-0 items-center gap-2.5 border-t border-rail-line px-3 py-3">
          <span className="grid size-8 shrink-0 place-items-center rounded-full bg-rail-raised font-mono text-[12px] font-semibold text-rail-fg">
            {username.slice(0, 2).toUpperCase()}
          </span>
          <span className="min-w-0 flex-1 leading-tight">
            <span className="block truncate text-[13px] text-rail-fg">{username}</span>
            <span className="label block text-rail-faint">{role}</span>
          </span>
          <button
            type="button"
            onClick={onToggleTheme}
            title={theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
            className="grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg"
          >
            {theme === 'dark' ? (
              <Sun aria-hidden="true" className="size-4" />
            ) : (
              <Moon aria-hidden="true" className="size-4" />
            )}
            <span className="sr-only">Switch deck</span>
          </button>
          <button
            type="button"
            onClick={onSignOut}
            title="Sign out"
            className="grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-danger"
          >
            <LogOut aria-hidden="true" className="size-4" />
            <span className="sr-only">Sign out</span>
          </button>
        </div>
      </div>
    </div>
  )
}
