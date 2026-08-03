import { useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Bell,
  ChevronRight,
  Gauge,
  KeyRound,
  Layers,
  LogOut,
  Menu,
  MonitorPlay,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Server,
  Shield,
  SlidersHorizontal,
  Sun,
  Timer,
  Users,
  UsersRound,
  X,
} from 'lucide-react'
import { Link, NavLink, useLocation } from 'react-router'
import type { Cluster } from '../api/types'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useTheme } from '../lib/theme'
import { clusterHref, isClusterPath } from '../lib/navigation'
import { strandState } from '../lib/status'
import { ClusterSwitcher } from './ClusterSwitcher'
import { CommandPalette } from './CommandPalette'
import type { CommandTarget } from './CommandPalette'
import { LinkStrand } from './LinkStrand'
import { Mark } from './Mark'
import { TimeRangeControl } from './TimeRangeControl'
import { EnvironmentDot, EnvironmentTag, IconButton, KeyHint } from './primitives'

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
      // Standing access is the matrix above; this is access that exists right now
      // and who is waiting for some. Everyone reaches it — a non-admin sees their
      // own requests, which is the only way to hand an elevation back early.
      { to: '/access-requests', label: 'Access requests', icon: Timer, adminOnly: false },
      // Everyone can reach the audit trail; a non-admin only sees their own actions.
      { to: '/audit', label: 'Audit trail', icon: ScrollText, adminOnly: false },
      // The trail says a shell was opened; a recording is what was done in it.
      // Same narrowing, so the same audience.
      { to: '/recordings', label: 'Recordings', icon: MonitorPlay, adminOnly: false },
    ],
  },
  {
    id: 'system',
    label: 'System',
    icon: SlidersHorizontal,
    items: [
      { to: '/settings/general', label: 'General Settings', icon: SlidersHorizontal, adminOnly: true },
      { to: '/settings/agent', label: 'Agent Settings', icon: Server, adminOnly: true },
      { to: '/settings/audit', label: 'Audit Settings', icon: ScrollText, adminOnly: true },
      { to: '/settings/guardrails', label: 'Guardrails', icon: Shield, adminOnly: true },
      { to: '/settings/alerting', label: 'Alerting', icon: Bell, adminOnly: true },
      { to: '/settings/sso', label: 'SSO', icon: KeyRound, adminOnly: true },
    ],
  },
] as const

const ACCESS_ROUTES = [
  '/users',
  '/groups',
  '/permissions',
  '/access-requests',
  '/audit',
  '/recordings',
]

/* The palette answers to both chords; the hint shows the one this keyboard has. */
const PALETTE_HINT = /mac/i.test(navigator.platform) ? '⌘K' : 'Ctrl K'

/**
 * How much room the section panel is taking.
 *
 * `full` is the 240px panel. `responsive` is the panel on a page that also has a
 * third level: it reads as icons until `xl`, where all three columns fit.
 * `icon` is the operator's own choice to keep it at the rail's width at every
 * size — on Explore three full columns leave the work itself with less room than
 * the navigation to it, so that is where the choice matters and where it is the
 * default.
 *
 * Tailwind scans the source for literal class names, so these are lookups
 * rather than interpolations.
 */
type PanelMode = 'full' | 'responsive' | 'icon'

const PANEL_WIDTH: Record<PanelMode, string> = {
  full: 'w-60',
  responsive: 'w-15 xl:w-60',
  icon: 'w-15',
}

const PANEL_HEADER: Record<PanelMode, string> = {
  full: 'px-4',
  responsive: 'justify-center xl:justify-start xl:px-4',
  icon: 'justify-center',
}

const PANEL_BODY: Record<PanelMode, string> = {
  full: 'px-2.5',
  responsive: 'px-2 xl:px-2.5',
  icon: 'px-2',
}

const PANEL_FOOTER: Record<PanelMode, string> = {
  full: 'px-3',
  responsive: 'flex-col px-2 xl:flex-row xl:px-3',
  icon: 'flex-col px-2',
}

/** The section heading, and any other line that only exists to be read. */
const PANEL_HEADING: Record<PanelMode, string> = {
  full: 'px-2',
  responsive: 'hidden xl:block xl:px-2',
  icon: 'hidden',
}

/** A label beside a glyph: `inline` for text, `block` for a truncating cell. */
const PANEL_LABEL: Record<PanelMode, { inline: string; block: string }> = {
  full: { inline: '', block: '' },
  responsive: { inline: 'hidden xl:inline', block: 'hidden xl:block' },
  icon: { inline: 'hidden', block: 'hidden' },
}

const PANEL_ROW: Record<PanelMode, string> = {
  full: 'px-2',
  responsive: 'justify-center px-0 xl:justify-start xl:px-2',
  icon: 'justify-center px-0',
}

/** Where the main column starts: the rail, the panel, and a third level if any. */
const MAIN_OFFSET: Record<string, string> = {
  full: 'lg:ml-75',
  responsive: 'lg:ml-86 xl:ml-131',
  icon: 'lg:ml-30',
  'icon+sidebar': 'lg:ml-86',
}

/** The compact strand shown in place of the entity block once there is no
    room for it — the inverse of `PANEL_LABEL[mode].block`, and a lookup for
    the same reason every other one here is: Tailwind wants the literal class. */
const PANEL_HEAD_COMPACT: Record<PanelMode, string> = {
  full: 'hidden',
  responsive: 'xl:hidden',
  icon: '',
}

/** The collapse toggle inside the entity head. Identical to the generic
    header's own rule: nothing to give up between `lg` and `xl` on a page that
    already has a third level, so it stays out of the way there. */
const PANEL_HEAD_TOGGLE: Record<PanelMode, string> = {
  full: '',
  responsive: 'hidden xl:grid',
  icon: '',
}

/** How the entity head's children stack. At full width they are a row: dot,
    detail block, compact strand, toggle — `items-start gap-2.5` is the shape
    that has always drawn there. At rail width (`icon`, and `responsive` below
    `xl`, which is at rail width too) the detail block is gone but the dot,
    the compact strand and the toggle are all still `shrink-0` with no padding
    to spend — laid out as a row they measure wider than the 60px column they
    are in and spill onto the work surface, so there they stack as a centred
    column instead. A lookup, not an interpolation, for the same reason every
    other table in this file is. */
const PANEL_HEAD_LAYOUT: Record<PanelMode, string> = {
  full: 'items-start gap-2.5',
  responsive: 'flex-col items-center gap-1.5 xl:flex-row xl:items-start xl:gap-2.5',
  icon: 'flex-col items-center gap-1.5',
}

const PANEL_COLLAPSED_KEY = 'kubemg_panel_collapsed'

/** `null` when the operator has never said, which is what lets a page default. */
function storedPanelCollapsed(): boolean | null {
  try {
    const raw = localStorage.getItem(PANEL_COLLAPSED_KEY)
    if (raw === '1') return true
    if (raw === '0') return false
  } catch {
    // Storage can be denied outright; the default is still a working deck.
  }
  return null
}

function sectionForPath(pathname: string): string {
  if (pathname.startsWith('/settings')) return 'system'
  if (ACCESS_ROUTES.some((route) => pathname.startsWith(route))) return 'access'
  return 'fleet'
}

/** The cluster id in a `/clusters/:id/...` path, or `null` outside one
    entirely. `/clusters/new` never matches — `new` is not a run of digits —
    so the wizard is never mistaken for a cluster that does not resolve. */
function clusterIdFromPath(pathname: string): number | null {
  const match = /^\/clusters\/(\d+)(?:\/|$)/.exec(pathname)
  return match ? Number(match[1]) : null
}

type ClusterPanelItem = { to: string; label: string; icon: typeof Gauge }
type ClusterPanelGroup = { id: string; label: string; items: ClusterPanelItem[] }

/**
 * The panel's own inventory once a cluster is open, replacing the fleet-wide
 * one: three groups because three cluster-scoped pages exist today (Phase 6.4
 * is what grows Inspect into Workloads, Nodes and the rest). Explore is
 * offered only with a live tunnel — a direct-mode cluster has no live state to
 * read, and an item that always refuses is worse than no item.
 */
function clusterPanelGroups(cluster: Cluster): ClusterPanelGroup[] {
  const groups: ClusterPanelGroup[] = [
    {
      id: 'monitor',
      label: 'Monitor',
      items: [{ to: `/clusters/${cluster.id}/summary`, label: 'Summary', icon: Gauge }],
    },
  ]
  if (cluster.connection_mode === 'agent' && cluster.agent_attached) {
    groups.push({
      id: 'inspect',
      label: 'Inspect',
      items: [{ to: `/clusters/${cluster.id}/explore`, label: 'Explore', icon: Layers }],
    })
  }
  groups.push({
    id: 'audit',
    label: 'Audit',
    items: [{ to: `/clusters/${cluster.id}/audit`, label: 'Audit trail', icon: ScrollText }],
  })
  return groups
}

export function AppShell({
  title,
  parent,
  actions,
  timeRange = false,
  sidebar,
  children,
}: {
  title: string
  /** Rendered ahead of the title as a breadcrumb, for pages nested under a section. */
  parent?: { label: string; to: string }
  actions?: ReactNode
  /**
   * Whether this page reads a time range, which puts the console's one window
   * control in the header ahead of the page's own actions.
   *
   * It is a prop rather than something the shell infers because the alternative
   * — consumers registering themselves so the control appears when one mounts —
   * makes it flicker in and out as a drawer opens over a list. A page either is
   * scoped by a window or it is not, and it is the page that knows.
   */
  timeRange?: boolean
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
  const [panelPref, setPanelPref] = useState<boolean | null>(storedPanelCollapsed)

  const isAdmin = user?.role === 'admin'
  const sections = SECTIONS.map((section) => ({
    ...section,
    items: section.items.filter((item) => !item.adminOnly || isAdmin),
  })).filter((section) => section.items.length > 0)

  const activeSectionId = sectionForPath(pathname)
  const activeSection = sections.find((section) => section.id === activeSectionId) ?? sections[0]

  // A cluster id that does not resolve — unregistered, or the fleet list has
  // not loaded yet — is `undefined` here, which is exactly what falls the
  // panel back to the fleet-wide one below rather than rendering an empty
  // cluster panel: `ClusterSummary` already explains a bad id on the page
  // itself, so the panel does not also have to guess.
  const openClusterId = clusterIdFromPath(pathname)
  const openCluster =
    openClusterId !== null ? clusters.find((entry) => entry.id === openClusterId) : undefined

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

  // A page with a third level is where the panel's 240px costs the most, so it
  // starts collapsed there; an explicit toggle wins over that default, in both
  // directions, and is remembered.
  const collapsed = panelPref ?? Boolean(sidebar)
  const mode: PanelMode = collapsed ? 'icon' : sidebar ? 'responsive' : 'full'
  const label = PANEL_LABEL[mode]

  function togglePanel() {
    const next = !collapsed
    setPanelPref(next)
    try {
      localStorage.setItem(PANEL_COLLAPSED_KEY, next ? '1' : '0')
    } catch {
      // The choice still holds for this session.
    }
  }

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
        className={`fixed inset-y-0 left-15 z-20 hidden flex-col border-r border-rail-line bg-rail lg:flex ${PANEL_WIDTH[mode]}`}
      >
        {openCluster ? (
          <ClusterPanelHead
            cluster={openCluster}
            mode={mode}
            collapsed={collapsed}
            onToggle={togglePanel}
          />
        ) : (
          <div
            className={`flex h-14 shrink-0 items-center text-[15px] font-semibold tracking-[-0.02em] ${PANEL_HEADER[mode]}`}
          >
            <span className={label.inline}>
              <span className="text-rail-fg">Kube</span>
              <span className="text-accent">MG</span>
            </span>
            {mode === 'responsive' ? (
              <span aria-hidden="true" className="font-mono text-[13px] text-accent xl:hidden">
                MG
              </span>
            ) : null}
            {/* Collapsed there is no wordmark to sit beside, so the toggle takes
                the slot; expanded it sits at the end of the header. Between `lg`
                and `xl` the panel is already at rail width, so there is nothing
                for it to give up and it stays out of the way. */}
            <button
              type="button"
              onClick={togglePanel}
              aria-expanded={!collapsed}
              title={collapsed ? 'Expand the section panel' : 'Collapse the section panel'}
              className={`grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg ${
                mode === 'responsive' ? 'ml-auto hidden xl:grid' : mode === 'full' ? 'ml-auto' : ''
              }`}
            >
              {collapsed ? (
                <PanelLeftOpen aria-hidden="true" className="size-4" />
              ) : (
                <PanelLeftClose aria-hidden="true" className="size-4" />
              )}
              <span className="sr-only">
                {collapsed ? 'Expand the section panel' : 'Collapse the section panel'}
              </span>
            </button>
          </div>
        )}

        <div className={`min-h-0 flex-1 overflow-y-auto pb-3 ${PANEL_BODY[mode]}`}>
          {openCluster ? (
            clusterPanelGroups(openCluster).map((group, index) => (
              <div key={group.id} className={index === 0 ? '' : 'mt-5'}>
                <p className={`label pt-1 pb-2 text-rail-faint ${PANEL_HEADING[mode]}`}>
                  {group.label}
                </p>
                <ul className="flex flex-col gap-0.5">
                  {group.items.map((item) => (
                    <li key={item.to}>
                      <NavLink
                        to={item.to}
                        title={mode === 'full' ? undefined : item.label}
                        className={railLink(mode)}
                      >
                        <item.icon aria-hidden="true" className="size-4 shrink-0" />
                        <span className={label.inline}>{item.label}</span>
                      </NavLink>
                    </li>
                  ))}
                </ul>
              </div>
            ))
          ) : (
            <>
              <p className={`label pt-1 pb-2 text-rail-faint ${PANEL_HEADING[mode]}`}>
                {activeSection?.label}
              </p>
              <ul className="flex flex-col gap-0.5">
                {activeSection?.items.map((item) => (
                  <li key={item.to}>
                    <NavLink
                      to={item.to}
                      end={item.to === '/'}
                      title={mode === 'full' ? undefined : item.label}
                      className={railLink(mode)}
                    >
                      <item.icon aria-hidden="true" className="size-4 shrink-0" />
                      <span className={label.inline}>{item.label}</span>
                    </NavLink>
                  </li>
                ))}
              </ul>

              {activeSection?.id === 'fleet' ? (
                <FleetList clusters={clusters} pathname={pathname} mode={mode} />
              ) : null}
            </>
          )}
        </div>

        <div
          className={`flex shrink-0 items-center gap-2.5 border-t border-rail-line py-3 ${PANEL_FOOTER[mode]}`}
        >
          <span
            title={mode === 'full' ? undefined : user?.username}
            className="grid size-8 shrink-0 place-items-center rounded-full bg-rail-raised font-mono text-[12px] font-semibold text-rail-fg"
          >
            {initials}
          </span>
          <span className={`min-w-0 flex-1 leading-tight ${label.block}`}>
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
        <div
          className={`fixed inset-y-0 left-30 z-10 hidden w-56 border-r border-rail-line bg-rail lg:block ${
            mode === 'responsive' ? 'xl:left-75' : ''
          }`}
        >
          {sidebar}
        </div>
      ) : null}

      <div
        className={`flex min-w-0 flex-col ${
          MAIN_OFFSET[mode === 'icon' && sidebar ? 'icon+sidebar' : mode]
        }`}
      >
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
              {/* A cluster is a place, not a page below one — the switcher
                  takes the slot the parent breadcrumb would have, and the view
                  is named after it. The heading stays a real `h1` in the
                  accessible tree at every width: narrow it is only visually
                  hidden, because a page whose outline starts at nothing is a
                  page a screen reader cannot navigate. */}
              {openCluster ? (
                <>
                  <ClusterSwitcher cluster={openCluster} />
                  <ChevronRight
                    aria-hidden="true"
                    className="hidden size-3.5 shrink-0 text-faint sm:block"
                  />
                  <h1 className="sr-only min-w-0 truncate text-[15px] font-semibold text-fg sm:not-sr-only">
                    {title}
                  </h1>
                </>
              ) : (
                <>
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
                </>
              )}
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
              {timeRange ? <TimeRangeControl /> : null}
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
  return railLink('full')({ isActive })
}

/** The same link at whatever width the panel currently has: collapsed it is a glyph, with the name on hover. */
function railLink(mode: PanelMode) {
  return ({ isActive }: { isActive: boolean }) => {
    const base = `flex items-center gap-2.5 rounded-control py-1.5 text-[13.5px] transition-colors ${PANEL_ROW[mode]}`
    return isActive
      ? `${base} bg-rail-raised font-medium text-rail-fg`
      : `${base} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
  }
}

/**
 * ClusterPanelHead stands in for the fleet-wide panel's KubeMG wordmark once a
 * cluster is open: which part of KubeMG you are in is already answered by the
 * lit Fleet icon on the rail, so this slot is spent on which cluster instead —
 * its name, its environment, its link state, and the Kubernetes version its
 * last check reported.
 *
 * Collapsed it reduces to exactly what `FleetList` already reduces a row to:
 * the environment dot and the strand, with the name reachable on hover
 * through the block's own `title`.
 */
function ClusterPanelHead({
  cluster,
  mode,
  collapsed,
  onToggle,
}: {
  cluster: Cluster
  mode: PanelMode
  collapsed: boolean
  onToggle: () => void
}) {
  const label = PANEL_LABEL[mode]
  return (
    <div
      title={mode === 'full' ? undefined : cluster.name}
      className={`flex shrink-0 border-b border-rail-line py-3 ${PANEL_HEAD_LAYOUT[mode]} ${PANEL_HEADER[mode]}`}
    >
      <span className="mt-0.5 shrink-0">
        <EnvironmentDot environment={cluster.environment} />
      </span>

      <div className={`min-w-0 flex-1 ${label.block}`}>
        <p className="truncate font-mono text-[13.5px] font-semibold text-rail-fg">
          {cluster.name}
        </p>
        <div className="mt-1.5 flex items-center gap-1.5">
          <EnvironmentTag environment={cluster.environment} />
          {cluster.kubernetes_version ? (
            <span className="truncate font-mono text-[11px] text-rail-faint">
              {cluster.kubernetes_version}
            </span>
          ) : null}
        </div>
        <LinkStrand state={strandState(cluster)} className="mt-2 w-full" />
      </div>

      {/* What is left once the block above is gone: the dot beside this stays,
          and this is the strand that goes with it. */}
      <LinkStrand
        state={strandState(cluster)}
        className={`mt-1 w-6 shrink-0 ${PANEL_HEAD_COMPACT[mode]}`}
      />

      <button
        type="button"
        onClick={onToggle}
        aria-expanded={!collapsed}
        title={collapsed ? 'Expand the section panel' : 'Collapse the section panel'}
        className={`grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg ${PANEL_HEAD_TOGGLE[mode]}`}
      >
        {collapsed ? (
          <PanelLeftOpen aria-hidden="true" className="size-4" />
        ) : (
          <PanelLeftClose aria-hidden="true" className="size-4" />
        )}
        <span className="sr-only">
          {collapsed ? 'Expand the section panel' : 'Collapse the section panel'}
        </span>
      </button>
    </div>
  )
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
  mode = 'full',
}: {
  clusters: ReturnType<typeof useClusters>['clusters']
  pathname: string
  mode?: PanelMode
}) {
  const label = PANEL_LABEL[mode]
  return (
    <div className="mt-5">
      <p
        className={`label flex items-center justify-between pb-2 text-rail-faint ${
          mode === 'full'
            ? 'px-2'
            : mode === 'responsive'
              ? 'justify-center px-0 xl:justify-between xl:px-2'
              : 'justify-center px-0'
        }`}
      >
        <span className={label.inline}>Clusters</span>
        <span className="font-mono">{clusters.length}</span>
      </p>

      {clusters.length === 0 ? (
        <p className={`text-[12px] text-rail-faint ${PANEL_HEADING[mode]}`}>None registered yet.</p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {clusters.map((cluster) => {
            // A cluster is the current one whether it is being explored or being
            // managed: both are that cluster, and the highlight has to say so or
            // the list stops answering "which one am I in".
            const active = isClusterPath(pathname, cluster.id)
            return (
              <li key={cluster.id}>
                <Link
                  to={clusterHref(cluster)}
                  aria-current={active ? 'page' : undefined}
                  title={mode === 'full' ? undefined : cluster.name}
                  className={`flex items-center gap-2 rounded-control py-1.5 transition-colors ${
                    PANEL_ROW[mode]
                  } ${active ? 'bg-rail-raised' : 'hover:bg-rail-raised/60'}`}
                >
                  <EnvironmentDot environment={cluster.environment} />
                  <span
                    className={`min-w-0 flex-1 truncate font-mono text-[12.5px] ${
                      active ? 'text-rail-fg' : 'text-rail-muted'
                    } ${label.block}`}
                  >
                    {cluster.name}
                  </span>
                  <LinkStrand state={strandState(cluster)} className={`w-8 shrink-0 ${label.block}`} />
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
