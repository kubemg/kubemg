import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Activity,
  Bell,
  ChevronDown,
  ChevronRight,
  Coins,
  Cpu,
  Gauge,
  KeyRound,
  Layers,
  LogOut,
  Menu,
  MonitorPlay,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  ScrollText,
  Server,
  Shield,
  ShieldAlert,
  Siren,
  SlidersHorizontal,
  Sun,
  Timer,
  Users,
  UsersRound,
  X,
} from 'lucide-react'
import { Link, NavLink, useLocation, useNavigate } from 'react-router'
import type { Cluster, Environment } from '../api/types'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useTheme } from '../lib/theme'
import { clusterHref, clusterViewHref, currentClusterView, isClusterPath } from '../lib/navigation'
import { strandState } from '../lib/status'
import { ClusterMenu } from './ClusterMenu'
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
 * looking at (the panel).
 *
 * The rail splits on *the job being done*, not on how KubeMG is built. It used
 * to read Fleet / Access / System, which put registering a cluster — a
 * change-controlled administrative act nobody performs twice a week — one row
 * below Explore, which a developer opens forty times a day, and hid half of
 * both sections from a non-admin so the shape of the navigation depended on who
 * had signed in. The three sections are now:
 *
 *   Operate  — the cluster you are working in, and the fleet it belongs to.
 *   Activity — what happened and who is waiting: requests, trail, recordings.
 *   Admin    — everything administrative, in one place, admins only.
 *
 * Operate and Activity are open to everyone and every row in them resolves for
 * everyone; Admin disappears whole. So a developer's rail is two icons with no
 * dead ends in it.
 */
type NavItem = { to: string; label: string; icon: typeof Gauge }
type NavGroup = { id: string; label: string; items: NavItem[] }
type Section = {
  id: string
  label: string
  icon: typeof Gauge
  /** Whole-section gate. Every row inside Admin is admin-only, so the section
      is gated once rather than row by row — a section that renders empty for a
      non-admin would still take a slot on the rail. */
  adminOnly?: boolean
  groups: NavGroup[]
}

const SECTIONS: readonly Section[] = [
  {
    id: 'operate',
    label: 'Operate',
    icon: Layers,
    groups: [
      { id: 'fleet', label: 'Fleet', items: [{ to: '/', label: 'Overview', icon: Gauge }] },
    ],
  },
  {
    id: 'activity',
    label: 'Activity',
    icon: Activity,
    groups: [
      {
        id: 'activity',
        label: 'Activity',
        items: [
          // Standing access is the permission matrix under Admin; this is access
          // that exists right now and who is waiting for some. Everyone reaches
          // it — a non-admin sees their own requests, which is the only way to
          // hand an elevation back early.
          { to: '/access-requests', label: 'Access requests', icon: Timer },
          // Everyone can reach the audit trail; a non-admin only sees their own
          // actions. Same narrowing on the recordings, so the same audience.
          { to: '/audit', label: 'Audit trail', icon: ScrollText },
          { to: '/recordings', label: 'Recordings', icon: MonitorPlay },
        ],
      },
    ],
  },
  {
    id: 'admin',
    label: 'Admin',
    icon: SlidersHorizontal,
    adminOnly: true,
    groups: [
      {
        id: 'inventory',
        label: 'Fleet',
        items: [
          { to: '/clusters', label: 'Clusters', icon: Server },
          { to: '/clusters/new', label: 'Register a cluster', icon: Plus },
        ],
      },
      {
        id: 'identity',
        label: 'Identity',
        items: [
          { to: '/users', label: 'Users', icon: Users },
          { to: '/groups', label: 'Groups', icon: UsersRound },
          { to: '/permissions', label: 'Permissions', icon: KeyRound },
        ],
      },
      {
        id: 'settings',
        label: 'Settings',
        items: [
          { to: '/settings/general', label: 'General', icon: SlidersHorizontal },
          { to: '/settings/agent', label: 'Agent', icon: Server },
          { to: '/settings/audit', label: 'Audit & retention', icon: ScrollText },
          { to: '/settings/guardrails', label: 'Guardrails', icon: Shield },
          { to: '/settings/alerting', label: 'Alerting', icon: Bell },
          { to: '/settings/sso', label: 'SSO', icon: KeyRound },
        ],
      },
    ],
  },
] as const

const ACTIVITY_ROUTES = ['/access-requests', '/audit', '/recordings']
const ADMIN_ROUTES = ['/clusters', '/users', '/groups', '/permissions', '/settings']

/* The palette answers to both chords; the hint shows the one this keyboard has. */
const PALETTE_HINT = /mac/i.test(navigator.platform) ? '⌘K' : 'Ctrl K'

/**
 * How much room the section panel is taking. `full` is the 240px panel; `icon`
 * is the operator's own choice to keep it at the rail's width instead, on any
 * page. There is deliberately no third mode for a page carrying a third level
 * of navigation — the panel *becomes* that navigation rather than sitting
 * beside it.
 *
 * Tailwind scans the source for literal class names, so these are lookups
 * rather than interpolations.
 */
type PanelMode = 'full' | 'icon'

const PANEL_WIDTH: Record<PanelMode, string> = {
  full: 'w-60',
  icon: 'w-15',
}

const PANEL_HEADER: Record<PanelMode, string> = {
  full: 'px-4',
  icon: 'justify-center',
}

const PANEL_BODY: Record<PanelMode, string> = {
  full: 'px-2.5',
  icon: 'px-2',
}

const PANEL_FOOTER: Record<PanelMode, string> = {
  full: 'px-3',
  icon: 'flex-col px-2',
}

/** The section heading, and any other line that only exists to be read. */
const PANEL_HEADING: Record<PanelMode, string> = {
  full: 'px-2',
  icon: 'hidden',
}

/** A label beside a glyph: `inline` for text, `block` for a truncating cell. */
const PANEL_LABEL: Record<PanelMode, { inline: string; block: string }> = {
  full: { inline: '', block: '' },
  icon: { inline: 'hidden', block: 'hidden' },
}

const PANEL_ROW: Record<PanelMode, string> = {
  full: 'px-2',
  icon: 'justify-center px-0',
}

/** Where the main column starts: the rail and the panel. */
const MAIN_OFFSET: Record<PanelMode, string> = {
  full: 'lg:ml-75',
  icon: 'lg:ml-30',
}

/**
 * How strongly the panel's boundary hairline takes the environment's tint. The
 * colour is the same one the `PROD` tag and the cluster card already carry, so
 * it reads as one fact rather than a new mark to learn; only the strength
 * differs, and it stays under half on every deck because this is peripheral
 * vision's job, not the reader's.
 */
const ENVIRONMENT_EDGE: Record<Environment, string> = {
  prod: 'text-danger opacity-70',
  staging: 'text-warn opacity-60',
  dev: 'text-faint opacity-30',
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

/** The cluster id in a `/clusters/:id/...` path, or `null` outside one
    entirely. `/clusters/new` never matches — `new` is not a run of digits —
    so the wizard is never mistaken for a cluster that does not resolve. */
function clusterIdFromPath(pathname: string): number | null {
  const match = /^\/clusters\/(\d+)(?:\/|$)/.exec(pathname)
  return match ? Number(match[1]) : null
}

function matchesRoute(pathname: string, route: string): boolean {
  return pathname === route || pathname.startsWith(`${route}/`)
}

function sectionForPath(pathname: string): string {
  // A cluster's own address space is Operate wherever it goes, including its
  // audit trail: `/audit` is the fleet-wide trail under Activity, while
  // `/clusters/12/audit` is one of the twelve's own views.
  if (clusterIdFromPath(pathname) !== null) return 'operate'
  if (ADMIN_ROUTES.some((route) => matchesRoute(pathname, route))) return 'admin'
  if (ACTIVITY_ROUTES.some((route) => matchesRoute(pathname, route))) return 'activity'
  return 'operate'
}

/**
 * The panel's own inventory once a cluster is open. Explore is offered only
 * with a live tunnel — a direct-mode cluster has no live state to read, and an
 * item that always refuses is worse than no item. This is what stays reachable
 * from inside Explore, *below* its resource tree, since the panel becomes that
 * tree rather than sitting beside it.
 */
function clusterPanelItems(cluster: Cluster): NavItem[] {
  const items: NavItem[] = [
    { to: `/clusters/${cluster.id}/summary`, label: 'Summary', icon: Gauge },
  ]
  if (cluster.connection_mode === 'agent' && cluster.agent_attached) {
    items.push({ to: `/clusters/${cluster.id}/explore`, label: 'Explore', icon: Layers })
    // Events sit next to Explore and need the same tunnel. They are a separate
    // row rather than a tab inside Explore because they are not a resource you
    // browse: Explore answers "what is here", and this answers "what just
    // happened", which is the question somebody arrives with rather than one
    // they navigate to.
    items.push({ to: `/clusters/${cluster.id}/events`, label: 'Events', icon: Siren })
    // Capacity reads the node and pod lists Explore already reads, and needs
    // the same tunnel. It sits above Security because it is the one of the two
    // an operator opens while something is wrong right now.
    items.push({ to: `/clusters/${cluster.id}/capacity`, label: 'Capacity', icon: Cpu })
    // Cost sits under Capacity because it is the same arithmetic answering a
    // different question, to a different person: allocation is read by whoever
    // is trying to get a pod scheduled, and the bill by whoever is being asked
    // about it.
    items.push({ to: `/clusters/${cluster.id}/cost`, label: 'Cost', icon: Coins })
    // Security posture reads the same lists Explore does, through the same
    // tunnel, so it needs exactly the tunnel Explore and Events already need.
    items.push({ to: `/clusters/${cluster.id}/security`, label: 'Security', icon: ShieldAlert })
  }
  items.push({ to: `/clusters/${cluster.id}/audit`, label: 'Audit trail', icon: ScrollText })
  return items
}

export function AppShell({
  title,
  parent,
  actions,
  timeRange = false,
  scope,
  panel,
  fullWidth = false,
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
   * A scope control beside the time range — a *what*, not a *when*, over the
   * same read. Explore uses it for the namespace picker: a scope of exactly
   * the same class as the cluster and the window, which is why it belongs in
   * the header rather than inside the page's own body.
   */
  scope?: ReactNode
  /**
   * What inside this cluster you are looking at, drawn *in* the section panel
   * rather than in a third level beside it. Explore is the one page that uses
   * it, for its resource tree.
   *
   * It is rendered **first** in the panel body, above the cluster's own
   * quick-nav. The tree is the work; three nav rows above it are the way back
   * out, and a panel that ranks the way out above the work makes the work
   * scroll. Rendered only at the panel's full width, since it is built for that
   * width and a collapsed rail has no room for it.
   */
  panel?: ReactNode
  /**
   * Drops the 1440px reading-width cap on the content column. Prose and forms
   * read worse stretched to a wide monitor's full span, but a resource table
   * has the opposite shape — its columns are what is starved of room, so a
   * page built around one (Explore) asks to use whatever the window has.
   */
  fullWidth?: boolean
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
  const sections = SECTIONS.filter((section) => !section.adminOnly || isAdmin)

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
        section.groups.flatMap((group) =>
          group.items.map((item) => ({
            id: `page-${item.to}`,
            label: item.label,
            hint: group.label === section.label ? section.label : `${section.label} · ${group.label}`,
            to: item.to,
          })),
        ),
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

  const collapsed = panelPref ?? false
  const mode: PanelMode = collapsed ? 'icon' : 'full'
  const label = PANEL_LABEL[mode]

  // Operate is the only section that is about a cluster; Activity and Admin are
  // fleet-wide, so the panel's top slot is theirs to name rather than a
  // switcher for a cluster their pages do not read.
  const inOperate = activeSection?.id === 'operate'
  const contextCluster = inOperate ? openCluster : undefined

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
            header, so it shares a centreline with whatever the panel names
            beside it rather than floating two pixels above it. */}
        <Link to="/" title="KubeMG" className="grid h-14 w-full shrink-0 place-items-center">
          {/* The hit target matches a section icon's; only the slot around it is
              taller, so the mark lands on the panel header's line. */}
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
              to={section.groups[0].items[0].to}
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

      {contextCluster ? <EnvironmentEdge environment={contextCluster.environment} /> : null}

      {/* Level two: what inside it. */}
      <aside
        className={`fixed inset-y-0 left-15 z-20 hidden flex-col border-r border-rail-line bg-rail lg:flex ${PANEL_WIDTH[mode]}`}
      >
        {inOperate ? (
          <PanelContext cluster={contextCluster} clusters={clusters} mode={mode} />
        ) : (
          <div
            className={`flex h-14 shrink-0 items-center gap-2.5 border-b border-rail-line ${PANEL_HEADER[mode]}`}
          >
            {activeSection ? (
              <activeSection.icon aria-hidden="true" className="size-4 shrink-0 text-rail-muted" />
            ) : null}
            <span
              className={`min-w-0 truncate text-[14px] font-semibold tracking-[-0.02em] text-rail-fg ${label.inline}`}
            >
              {activeSection?.label}
            </span>
          </div>
        )}

        <div className={`min-h-0 flex-1 overflow-y-auto pt-1 pb-3 ${PANEL_BODY[mode]}`}>
          {contextCluster ? (
            <>
              {/* The work first: a resource tree is what the page is for, and
                  three nav rows above it only push it off screen. */}
              {panel && mode === 'full' ? <div className="pt-2">{panel}</div> : null}

              <div className={panel && mode === 'full' ? 'mt-5 border-t border-rail-line pt-4' : 'mt-3'}>
                <p className={`label pb-2 text-rail-faint ${PANEL_HEADING[mode]}`}>This cluster</p>
                <ul className="flex flex-col gap-0.5">
                  {clusterPanelItems(contextCluster).map((item) => (
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
            </>
          ) : (
            <>
              {activeSection?.groups.map((group, index) => (
                <div key={group.id} className={index === 0 ? 'mt-2' : 'mt-5'}>
                  <p className={`label pb-2 text-rail-faint ${PANEL_HEADING[mode]}`}>
                    {group.label}
                  </p>
                  <ul className="flex flex-col gap-0.5">
                    {group.items.map((item) => (
                      <li key={item.to}>
                        <NavLink
                          to={item.to}
                          end={item.to === '/' || item.to === '/clusters'}
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
              ))}

              {/* The fleet itself, below Operate's one page: with no cluster
                  open the panel's job is to help pick one. */}
              {inOperate ? (
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
          {/* The width control lives here rather than in the header: the header
              is the one slot the open cluster needs, at every width. */}
          <button
            type="button"
            onClick={togglePanel}
            aria-expanded={!collapsed}
            title={collapsed ? 'Expand the section panel' : 'Collapse the section panel'}
            className="grid size-8 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg"
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

      <div className={`flex min-w-0 flex-col ${MAIN_OFFSET[mode]}`}>
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
              {scope}
              {timeRange ? <TimeRangeControl /> : null}
              {actions}
            </div>
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4 xl:p-6">
          <div className={`mx-auto min-w-0 ${fullWidth ? '' : 'max-w-[1440px]'}`}>{children}</div>
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
 * EnvironmentEdge is the environment of the open cluster, drawn down the
 * panel's own edge.
 *
 * The environment used to be a 1.5px dot in the panel and, at rail width,
 * *only* that dot — which made the single most consequential fact on screen
 * (that this shell is about to open in production) the smallest mark on it. So
 * it moved to the edge, where it survives the panel collapsing, which is the
 * width an operator spends most of the day in.
 *
 * It adds **nothing to the layout**: it is the hairline that already divides
 * the rail from the panel, taking a tint. It sits *on* the rail's own
 * `border-r` (`left-15 -ml-px`) rather than beside it — a second line one pixel
 * over is what made the earlier versions read as a stripe — and the mask fades
 * it out over the top and bottom fifth so it has no endpoints to notice,
 * meeting the header and footer rules without crossing them.
 *
 * It is a sibling of the panel rather than a child of it because the rail is
 * `z-30` and the panel `z-20`: drawn inside the panel, the one pixel that
 * overlaps the rail's border is painted over by the rail and the tint vanishes.
 * `hidden lg:block` for the same reason both levels of chrome are — below that
 * breakpoint there is no rail to draw an edge on.
 *
 * Two earlier attempts are worth naming so they are not tried again. A 3px slab
 * of solid colour is the loudest thing on the deck, and the chrome is meant to
 * be the quietest. Lighting it — a glow plus a wash bleeding inward, borrowing
 * the link strand's device — was quieter but still an *object*: the deck has
 * one signature and a second thing glowing beside it competes with it. What is
 * wanted here is peripheral vision only. You should not see this line when you
 * look at the panel; you should know the colour of the room.
 */
function EnvironmentEdge({ environment }: { environment: Environment }) {
  return (
    <span
      aria-hidden="true"
      className={`pointer-events-none fixed inset-y-0 left-15 z-40 -ml-px hidden w-px bg-current [mask-image:linear-gradient(to_bottom,transparent,black_20%,black_80%,transparent)] lg:block ${ENVIRONMENT_EDGE[environment]}`}
    />
  )
}

/**
 * PanelContext is the panel's top slot in the Operate section, and the answer
 * to "which cluster am I about to act on" at every width.
 *
 * It is pinned rather than scrolled with the nav, and it is the *only* cluster
 * switcher in the panel. There used to be three ways to move between clusters —
 * a disclosure in the panel, the header's dropdown and ⌘K — and the disclosure
 * defaulted closed on the one page where switching matters most, so the fastest
 * path was the one nobody could see. Now the same `ClusterMenu` opens from here
 * and from the header, and switching keeps whichever view is open.
 *
 * With no cluster open it names the fleet instead, and still opens the same
 * chooser — the top of the panel means the same thing either way.
 */
function PanelContext({
  cluster,
  clusters,
  mode,
}: {
  cluster: Cluster | undefined
  clusters: Cluster[]
  mode: PanelMode
}) {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const label = PANEL_LABEL[mode]

  useEffect(() => {
    if (!open) return
    function onOutside(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false)
    }
    window.addEventListener('mousedown', onOutside)
    return () => window.removeEventListener('mousedown', onOutside)
  }, [open])

  useEffect(() => {
    setOpen(false)
  }, [pathname])

  const view = cluster ? currentClusterView(pathname, cluster.id) : undefined

  return (
    <div ref={rootRef} className="relative shrink-0 border-b border-rail-line">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        title={mode === 'full' ? undefined : (cluster?.name ?? 'Switch cluster')}
        className={`flex h-14 w-full items-center gap-2.5 text-left transition-colors hover:bg-rail-raised/60 ${PANEL_HEADER[mode]}`}
      >
        {cluster ? (
          <>
            <span className="shrink-0">
              <EnvironmentDot environment={cluster.environment} />
            </span>
            <span className={`min-w-0 flex-1 leading-tight ${label.block}`}>
              <span className="block truncate font-mono text-[13.5px] font-semibold text-rail-fg">
                {cluster.name}
              </span>
              <span className="mt-1 flex items-center gap-1.5">
                <EnvironmentTag environment={cluster.environment} />
                {cluster.kubernetes_version ? (
                  <span className="truncate font-mono text-[11px] text-rail-faint">
                    {cluster.kubernetes_version}
                  </span>
                ) : null}
                <LinkStrand state={strandState(cluster)} className="w-6 shrink-0" />
              </span>
            </span>
          </>
        ) : (
          <>
            <Layers aria-hidden="true" className="size-4 shrink-0 text-rail-muted" />
            <span className={`min-w-0 flex-1 leading-tight ${label.block}`}>
              <span className="block truncate text-[14px] font-semibold tracking-[-0.02em] text-rail-fg">
                All clusters
              </span>
              <span className="label block text-rail-faint">
                {clusters.length} registered
              </span>
            </span>
          </>
        )}
        <ChevronDown
          aria-hidden="true"
          className={`size-3.5 shrink-0 text-rail-faint transition-transform ${
            open ? 'rotate-180' : ''
          } ${label.inline}`}
        />
      </button>

      {open ? (
        <ClusterMenu
          clusters={clusters}
          currentId={cluster?.id}
          onPick={(target) => {
            setOpen(false)
            navigate(view ? clusterViewHref(target, view) : clusterHref(target))
          }}
          onFleet={() => {
            setOpen(false)
            navigate('/')
          }}
          onClose={() => setOpen(false)}
          className="absolute top-2 left-full z-40 ml-2"
        />
      ) : null}
    </div>
  )
}

/**
 * FleetList is the fleet in the panel: every cluster, its environment, and its
 * link state on one line. It is what the Operate panel offers when no cluster
 * is open — once one is, the context switcher above is the faster path and this
 * would only compete with the resource tree for the same column.
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
          mode === 'full' ? 'px-2' : 'justify-center px-0'
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
                  <LinkStrand
                    state={strandState(cluster)}
                    className={`w-8 shrink-0 ${label.block}`}
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
  sections: readonly Section[]
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
              {section.groups.map((group) => (
                <ul key={group.id} className="flex flex-col gap-0.5">
                  {group.items.map((item) => (
                    <li key={item.to}>
                      <NavLink
                        to={item.to}
                        end={item.to === '/' || item.to === '/clusters'}
                        className={railLinkClass}
                      >
                        <item.icon aria-hidden="true" className="size-4 shrink-0" />
                        {item.label}
                      </NavLink>
                    </li>
                  ))}
                </ul>
              ))}
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
