import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Activity,
  ArrowLeft,
  Bell,
  ChevronDown,
  ChevronRight,
  Gauge,
  KeyRound,
  Layers,
  LogOut,
  Menu,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
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
import { Link, NavLink, useLocation, useNavigate } from 'react-router'
import type { Cluster, Environment } from '../api/types'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useInventory } from '../state/inventory-context'
import { useTheme } from '../lib/theme'
import {
  ACCESS_HOME,
  ADMIN_HOME,
  clusterHref,
  clusterIdFromPath,
  clusterSlotHref,
  currentClusterSlot,
  isAdminPath,
  isClusterPath,
} from '../lib/navigation'
import type { ResourceKey } from '../lib/resources'
import { linkState } from '../lib/status'
import { ClusterMenu } from './ClusterMenu'
import { ClusterSwitcher } from './ClusterSwitcher'
import { ClusterTree } from './ClusterTree'
import { CommandPalette } from './CommandPalette'
import type { CommandTarget } from './CommandPalette'
import { LinkStatus } from './LinkStatus'
import { Mark } from './Mark'
import { TimeRangeControl } from './TimeRangeControl'
import { EnvironmentDot, EnvironmentTag, IconButton, KeyHint } from './primitives'

/**
 * The deck has two levels of navigation because the work has two levels: which
 * cluster you are in (the rail), and what inside it you are looking at (the
 * tree).
 *
 * Both levels used to be about KubeMG rather than about a cluster. The rail read
 * Operate / Activity / Admin, which split on how the console is built: it put
 * registering a cluster — an administrative act nobody performs twice a week —
 * one row below Explore, which a developer opens forty times a day, and it made
 * the resource tree the *third* level, reachable only by clicking a row called
 * Explore. The people this console is for are the ones who never learned
 * kubectl; making them ask twice for the only thing they came for was the wrong
 * default.
 *
 * So:
 *
 *   Level one — the fleet. Clusters, as chips, because "which cluster" is the
 *   axis that actually changes all day in a shop running eleven of them.
 *   Level two — the open cluster, whole: its dashboard and reads as the first
 *   group, then every kind of object in it.
 *
 * Administration is not a third peer. It is one row in the tree's footer that
 * swaps the second level for the platform team's own space, and it is absent
 * entirely for anybody who is not an admin.
 */
type AdminItem = { to: string; label: string; icon: typeof Gauge }
type AdminGroup = { id: string; label: string; items: AdminItem[] }

const ADMIN_GROUPS: readonly AdminGroup[] = [
  {
    id: 'fleet',
    label: 'Fleet',
    items: [
      { to: '/admin/clusters', label: 'Clusters', icon: Server },
      { to: '/admin/clusters/new', label: 'Register a cluster', icon: Plus },
    ],
  },
  {
    id: 'identity',
    label: 'Identity',
    items: [
      { to: '/admin/users', label: 'Users', icon: Users },
      { to: '/admin/groups', label: 'Groups', icon: UsersRound },
      { to: '/admin/permissions', label: 'Permissions', icon: KeyRound },
    ],
  },
  {
    id: 'activity',
    label: 'Activity',
    items: [
      // The queue, as opposed to `/me/access`, which is the same page narrowed
      // to the operator's own requests. Two doors onto one surface: the person
      // asking and the person approving arrive from different places.
      { to: '/admin/access-requests', label: 'Access requests', icon: Timer },
      { to: '/admin/audit', label: 'Audit trail', icon: ScrollText },
      { to: '/admin/recordings', label: 'Session recordings', icon: Activity },
    ],
  },
  {
    id: 'settings',
    label: 'Settings',
    items: [
      { to: '/admin/settings/general', label: 'General', icon: SlidersHorizontal },
      { to: '/admin/settings/agent', label: 'Agent', icon: Server },
      { to: '/admin/settings/audit', label: 'Audit & retention', icon: ScrollText },
      { to: '/admin/settings/guardrails', label: 'Guardrails', icon: Shield },
      { to: '/admin/settings/alerting', label: 'Alerting', icon: Bell },
      { to: '/admin/settings/sso', label: 'SSO', icon: KeyRound },
      { to: '/admin/settings/deployment', label: 'Deployment', icon: Server },
    ],
  },
] as const

/* The palette answers to both chords; the hint shows the one this keyboard has. */
const PALETTE_HINT = /mac/i.test(navigator.platform) ? '⌘K' : 'Ctrl K'

/**
 * How many clusters the rail carries. Past this the rail would become the fleet
 * list it is meant to shortcut, and the mark, the switcher and ⌘K all reach the
 * rest. The cut is by id — the order the fleet was registered in — because it is
 * stable: a rail whose chips reorder themselves is a rail you cannot build
 * muscle memory against.
 */
const RAIL_CLUSTERS = 8

/**
 * Whether the tree is showing. There is deliberately no icon-width mode: a
 * resource tree at rail width is unreadable, so there was nothing for it to
 * shrink into — the old icon panel showed six glyphs and hid the sixty rows
 * that are the point. Collapsing gives the room to the work surface instead,
 * which is what a wide resource table actually wants, and the rail and ⌘K are
 * the way back.
 */
type PanelMode = 'full' | 'hidden'

const MAIN_OFFSET: Record<PanelMode, string> = {
  full: 'lg:ml-75',
  hidden: 'lg:ml-15',
}

/**
 * How strongly the tree's boundary hairline takes the environment's tint. The
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

/**
 * The cluster the operator was last in, so Administration can offer the way
 * back to it rather than dumping them on the fleet.
 *
 * It is module state rather than component state because AppShell is mounted by
 * each page: the shell that knew which cluster was open is unmounted by the
 * navigation into `/admin`. It is deliberately not persisted — after a reload
 * the honest answer is that we no longer know, and the footer says "the fleet"
 * instead of guessing.
 */
let lastClusterId: number | null = null

function storedPanelCollapsed(): boolean {
  try {
    return localStorage.getItem(PANEL_COLLAPSED_KEY) === '1'
  } catch {
    // Storage can be denied outright; the default is still a working deck.
    return false
  }
}

export function AppShell({
  title,
  parent,
  actions,
  timeRange = false,
  scope,
  fullWidth = false,
  children,
}: {
  title: string
  /** Rendered ahead of the title as a breadcrumb, for pages nested under another. */
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
   * the same class as the cluster and the window.
   */
  scope?: ReactNode
  /**
   * Drops the 1440px reading-width cap on the content column. Prose and forms
   * read worse stretched to a wide monitor's full span, but a resource table
   * has the opposite shape — its columns are what is starved of room, so a
   * page built around one asks to use whatever the window has.
   */
  fullWidth?: boolean
  children: ReactNode
}) {
  const { user, signOut } = useAuth()
  const { clusters } = useClusters()
  const { categories } = useInventory()
  const { theme, toggle } = useTheme()
  const { pathname } = useLocation()

  const [navOpen, setNavOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(storedPanelCollapsed)

  const isAdmin = user?.role === 'admin'
  const inAdmin = isAdminPath(pathname)

  // A cluster id that does not resolve — unregistered, or the fleet list has
  // not loaded yet — is `undefined` here, which falls the tree back to the
  // fleet list rather than rendering a tree for a cluster nobody has: the
  // cluster's own page already explains a bad id.
  const openClusterId = clusterIdFromPath(pathname)
  const openCluster =
    openClusterId !== null ? clusters.find((entry) => entry.id === openClusterId) : undefined
  if (openCluster) lastClusterId = openCluster.id
  // Inside Administration there is no cluster in the address, so the way back is
  // the one the operator came from.
  const returnCluster =
    openCluster ?? clusters.find((entry) => entry.id === lastClusterId) ?? undefined

  const slot = openCluster ? currentClusterSlot(pathname, openCluster.id) : null
  const selectedResource: ResourceKey | null =
    slot?.kind === 'resource' ? (slot.key as ResourceKey) : null

  const railClusters = useMemo(() => clusters.slice(0, RAIL_CLUSTERS), [clusters])

  /** What ⌘K reaches beyond the fleet and each cluster's own views. */
  const pages = useMemo<CommandTarget[]>(() => {
    const targets: CommandTarget[] = [
      { id: 'page-fleet', label: 'All clusters', hint: 'Fleet', to: '/' },
      { id: 'page-access', label: 'My access', hint: 'You', to: ACCESS_HOME },
    ]
    if (!isAdmin) return targets
    return targets.concat(
      ADMIN_GROUPS.flatMap((group) =>
        group.items.map((item) => ({
          id: `page-${item.to}`,
          label: item.label,
          hint: `Administration · ${group.label}`,
          to: item.to,
        })),
      ),
    )
  }, [isAdmin])

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
  const mode: PanelMode = collapsed ? 'hidden' : 'full'

  function togglePanel() {
    const next = !collapsed
    setCollapsed(next)
    try {
      localStorage.setItem(PANEL_COLLAPSED_KEY, next ? '1' : '0')
    } catch {
      // The choice still holds for this session.
    }
  }

  return (
    <div className="min-h-svh bg-bg">
      {/* Level one: the fleet. */}
      <nav
        aria-label="Clusters"
        className="fixed inset-y-0 left-0 z-30 hidden w-15 flex-col items-center gap-1 border-r border-rail-line bg-rail pb-3 lg:flex"
      >
        {/* The mark sits in a slot exactly as tall as the tree's own header, so
            it shares a centreline with whatever the tree names beside it rather
            than floating two pixels above it. */}
        <Link to="/" title="All clusters" className="grid h-14 w-full shrink-0 place-items-center">
          {/* The hit target matches a chip's; only the slot around it is taller,
              so the mark lands on the tree header's line. */}
          <span className="grid size-10 place-items-center rounded-control text-accent transition-colors hover:bg-rail-raised">
            <Mark className="size-6.5" />
          </span>
          <span className="sr-only">All clusters</span>
        </Link>

        <span aria-hidden="true" className="my-1 h-px w-6 shrink-0 bg-rail-line" />

        <div className="flex min-h-0 flex-col items-center gap-1 overflow-y-auto">
          {railClusters.map((cluster) => {
            const active = isClusterPath(pathname, cluster.id)
            return (
              <Link
                key={cluster.id}
                to={clusterHref(cluster)}
                title={cluster.name}
                aria-current={active ? 'page' : undefined}
                className={`relative grid size-10 shrink-0 place-items-center rounded-control border font-mono text-[10.5px] font-semibold transition-colors ${
                  active
                    ? 'border-accent bg-rail-raised text-rail-fg'
                    : 'border-transparent text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg'
                }`}
              >
                {railCode(cluster.name)}
                <span className="absolute top-1 right-1">
                  <EnvironmentDot environment={cluster.environment} />
                </span>
                <span className="sr-only">{cluster.name}</span>
              </Link>
            )
          })}
        </div>

        {/* The rest of the fleet. It is the mark's destination too, and says so
            twice on purpose: this is the button beside the chips, where somebody
            looking for a cluster that is not on the rail will reach for it. */}
        <Link
          to="/"
          title="All clusters"
          className="grid size-10 shrink-0 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-rail-fg"
        >
          <Plus aria-hidden="true" className="size-4" />
          <span className="sr-only">All clusters</span>
        </Link>

        <span className="flex-1" />

        <span
          title={`${user?.username ?? ''} · ${user?.role ?? ''}`}
          className="grid size-8 shrink-0 place-items-center rounded-full bg-rail-raised font-mono text-[11px] font-semibold text-rail-fg"
        >
          {initials}
        </span>
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
        <button
          type="button"
          onClick={signOut}
          title="Sign out"
          className="grid size-9 place-items-center rounded-control text-rail-muted transition-colors hover:bg-rail-raised hover:text-danger"
        >
          <LogOut aria-hidden="true" className="size-4" />
          <span className="sr-only">Sign out</span>
        </button>
      </nav>

      {openCluster && !inAdmin ? <EnvironmentEdge environment={openCluster.environment} /> : null}

      {/* Level two: what inside it. */}
      {mode === 'full' ? (
        <aside className="fixed inset-y-0 left-15 z-20 hidden w-60 flex-col border-r border-rail-line bg-rail lg:flex">
          {inAdmin ? (
            <AdminHeader cluster={returnCluster} />
          ) : (
            <PanelContext cluster={openCluster} clusters={clusters} />
          )}

          <div className="min-h-0 flex-1 overflow-y-auto px-2.5 pt-3 pb-3">
            {inAdmin ? (
              <AdminNav />
            ) : openCluster ? (
              <ClusterTree
                cluster={openCluster}
                categories={categories}
                selected={selectedResource}
              />
            ) : (
              <FleetNav clusters={clusters} pathname={pathname} />
            )}
          </div>

          <div className="flex shrink-0 flex-col gap-px border-t border-rail-line p-2">
            {inAdmin ? (
              <Link
                to={returnCluster ? clusterHref(returnCluster) : '/'}
                className={`${FOOTER_ROW_BASE} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`}
              >
                <ArrowLeft aria-hidden="true" className="size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">
                  {returnCluster ? `Back to ${returnCluster.name}` : 'Back to the fleet'}
                </span>
              </Link>
            ) : (
              <>
                <NavLink to={ACCESS_HOME} className={footerRow}>
                  <Timer aria-hidden="true" className="size-4 shrink-0" />
                  <span className="min-w-0 flex-1 truncate">My access</span>
                </NavLink>
                {/* The one door. Absent, not disabled, for a non-admin: every
                    row behind it would refuse, and a door that never opens is
                    worse than no door. */}
                {isAdmin ? (
                  <NavLink to={ADMIN_HOME} className={footerRow}>
                    <SlidersHorizontal aria-hidden="true" className="size-4 shrink-0" />
                    <span className="min-w-0 flex-1 truncate">Administration</span>
                    <ChevronRight aria-hidden="true" className="size-3.5 shrink-0 text-rail-faint" />
                  </NavLink>
                ) : null}
              </>
            )}
            <button type="button" onClick={togglePanel} className={`${FOOTER_ROW_BASE} text-rail-faint hover:text-rail-fg`}>
              <PanelLeftClose aria-hidden="true" className="size-4 shrink-0" />
              <span className="min-w-0 flex-1 truncate">Collapse the tree</span>
            </button>
          </div>
        </aside>
      ) : null}

      {navOpen ? (
        <MobileNav
          clusters={clusters}
          pathname={pathname}
          isAdmin={isAdmin}
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

            {/* With the tree hidden this is the only way back to it, so it lives
                beside the breadcrumb rather than in the tree it would reopen. */}
            {mode === 'hidden' ? (
              <IconButton label="Show the tree" onClick={togglePanel} className="hidden lg:flex">
                <PanelLeftOpen aria-hidden="true" className="size-4.5" />
              </IconButton>
            ) : null}

            <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-2">
              {/* A cluster is a place, not a page below one — the switcher takes
                  the slot the parent breadcrumb would have, and the view is
                  named after it. The heading stays a real `h1` in the accessible
                  tree at every width: narrow it is only visually hidden, because
                  a page whose outline starts at nothing is a page a screen
                  reader cannot navigate. */}
              {openCluster && !inAdmin ? (
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

/** A cluster's chip on the rail: the shortest thing that still reads as its name. */
function railCode(name: string): string {
  const parts = name.split(/[^a-zA-Z0-9]+/).filter(Boolean)
  if (parts.length === 0) return name.slice(0, 3).toUpperCase()
  if (parts.length === 1) return parts[0].slice(0, 3).toUpperCase()
  return parts
    .slice(0, 3)
    .map((part) => part[0])
    .join('')
    .toUpperCase()
}

const FOOTER_ROW_BASE =
  'flex items-center gap-2.5 rounded-control px-2 py-1.5 text-left text-[13px] transition-colors'

function footerRow({ isActive }: { isActive: boolean }) {
  return isActive
    ? `${FOOTER_ROW_BASE} bg-rail-raised font-medium text-rail-fg`
    : `${FOOTER_ROW_BASE} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
}

function railLinkClass({ isActive }: { isActive: boolean }) {
  const base =
    'flex items-center gap-2.5 rounded-control px-2 py-1.5 text-[13.5px] transition-colors'
  return isActive
    ? `${base} bg-rail-raised font-medium text-rail-fg`
    : `${base} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
}

/**
 * EnvironmentEdge is the environment of the open cluster, drawn down the tree's
 * own edge.
 *
 * The environment used to be a 1.5px dot in the panel, which made the single
 * most consequential fact on screen (that this shell is about to open in
 * production) the smallest mark on it. So it moved to the edge, where it
 * survives the tree collapsing.
 *
 * It adds **nothing to the layout**: it is the hairline that already divides the
 * rail from the tree, taking a tint. It sits *on* the rail's own `border-r`
 * (`left-15 -ml-px`) rather than beside it — a second line one pixel over is
 * what made the earlier versions read as a stripe — and the mask fades it out
 * over the top and bottom fifth so it has no endpoints to notice.
 *
 * It is a sibling of the tree rather than a child of it because the rail is
 * `z-30` and the tree `z-20`: drawn inside the tree, the one pixel that overlaps
 * the rail's border is painted over by the rail and the tint vanishes.
 *
 * Two earlier attempts are worth naming so they are not tried again. A 3px slab
 * of solid colour is the loudest thing on the deck, and the chrome is meant to
 * be the quietest. Lighting it — a glow plus a wash bleeding inward — was
 * quieter but still an *object*, and the deck's only moving mark is the breath
 * on a link that is genuinely open. What is
 * wanted here is peripheral vision only. You should not see this line when you
 * look at the tree; you should know the colour of the room.
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
 * PanelContext is the tree's top slot, and the answer to "which cluster am I
 * about to act on".
 *
 * It is pinned rather than scrolled with the tree, and it is the *only* cluster
 * switcher in it. The same `ClusterMenu` opens from here and from the header,
 * and switching keeps whichever slot is open — Pods stays Pods.
 *
 * With no cluster open it names the fleet instead, and still opens the same
 * chooser: the top of the tree means the same thing either way.
 */
function PanelContext({
  cluster,
  clusters,
}: {
  cluster: Cluster | undefined
  clusters: Cluster[]
}) {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)

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

  const slot = cluster ? currentClusterSlot(pathname, cluster.id) : null

  return (
    <div ref={rootRef} className="relative shrink-0 border-b border-rail-line">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex h-14 w-full items-center gap-2.5 px-4 text-left transition-colors hover:bg-rail-raised/60"
      >
        {cluster ? (
          <>
            <span className="shrink-0">
              <EnvironmentDot environment={cluster.environment} />
            </span>
            <span className="min-w-0 flex-1 leading-tight">
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
                <LinkStatus state={linkState(cluster)} variant="glyph" surface="rail" />
              </span>
            </span>
          </>
        ) : (
          <>
            <Layers aria-hidden="true" className="size-4 shrink-0 text-rail-muted" />
            <span className="min-w-0 flex-1 leading-tight">
              <span className="block truncate text-[14px] font-semibold tracking-[-0.02em] text-rail-fg">
                All clusters
              </span>
              <span className="label block text-rail-faint">{clusters.length} registered</span>
            </span>
          </>
        )}
        <ChevronDown
          aria-hidden="true"
          className={`size-3.5 shrink-0 text-rail-faint transition-transform ${
            open ? 'rotate-180' : ''
          }`}
        />
      </button>

      {open ? (
        <ClusterMenu
          clusters={clusters}
          currentId={cluster?.id}
          onPick={(target) => {
            setOpen(false)
            navigate(slot ? clusterSlotHref(target, slot) : clusterHref(target))
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

/** The tree's head inside Administration: what this space is, and the way out. */
function AdminHeader({ cluster }: { cluster: Cluster | undefined }) {
  return (
    <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-rail-line px-4">
      <SlidersHorizontal aria-hidden="true" className="size-4 shrink-0 text-rail-muted" />
      <span className="min-w-0 flex-1 leading-tight">
        <span className="block truncate text-[14px] font-semibold tracking-[-0.02em] text-rail-fg">
          Administration
        </span>
        <span className="label block truncate text-rail-faint">
          {cluster ? `from ${cluster.name}` : 'the whole fleet'}
        </span>
      </span>
    </div>
  )
}

/** The platform team's own second level. */
function AdminNav() {
  return (
    <>
      {ADMIN_GROUPS.map((group, index) => (
        <div key={group.id} className={index === 0 ? '' : 'mt-5'}>
          <p className="label px-2 pb-2 text-rail-faint">{group.label}</p>
          <ul className="flex flex-col gap-0.5">
            {group.items.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.to === '/admin/clusters'}
                  className={railLinkClass}
                >
                  <item.icon aria-hidden="true" className="size-4 shrink-0" />
                  <span className="min-w-0 truncate">{item.label}</span>
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </>
  )
}

/**
 * FleetNav is the tree with no cluster open: the fleet itself, which is the one
 * thing that helps you pick one. Once a cluster is open the switcher above is
 * the faster path and this would only compete with the resource tree for the
 * same column.
 */
function FleetNav({ clusters, pathname }: { clusters: Cluster[]; pathname: string }) {
  return (
    <>
      <ul className="flex flex-col gap-0.5">
        <li>
          <NavLink to="/" end className={railLinkClass}>
            <Gauge aria-hidden="true" className="size-4 shrink-0" />
            <span className="min-w-0 truncate">Fleet overview</span>
          </NavLink>
        </li>
      </ul>

      <div className="mt-5">
        <p className="label flex items-center justify-between px-2 pb-2 text-rail-faint">
          <span>Clusters</span>
          <span className="font-mono">{clusters.length}</span>
        </p>

        {clusters.length === 0 ? (
          <p className="px-2 text-[12px] text-rail-faint">None registered yet.</p>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {clusters.map((cluster) => (
              <li key={cluster.id}>
                <Link
                  to={clusterHref(cluster)}
                  aria-current={isClusterPath(pathname, cluster.id) ? 'page' : undefined}
                  className={`flex items-center gap-2 rounded-control px-2 py-1.5 transition-colors ${
                    isClusterPath(pathname, cluster.id)
                      ? 'bg-rail-raised'
                      : 'hover:bg-rail-raised/60'
                  }`}
                >
                  <EnvironmentDot environment={cluster.environment} />
                  <span
                    className={`min-w-0 flex-1 truncate font-mono text-[12.5px] ${
                      isClusterPath(pathname, cluster.id) ? 'text-rail-fg' : 'text-rail-muted'
                    }`}
                  >
                    {cluster.name}
                  </span>
                  <LinkStatus state={linkState(cluster)} variant="glyph" surface="rail" />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </>
  )
}

/**
 * Below the two-level breakpoint both levels collapse into one sheet. It lists
 * the fleet rather than a resource tree: a cluster's kinds are picked from the
 * selector at the top of its own page at this width, since a sixty-row tree in
 * a slide-over is not navigation, it is a scroll.
 */
function MobileNav({
  clusters,
  pathname,
  isAdmin,
  theme,
  onToggleTheme,
  username,
  role,
  onSignOut,
  onClose,
}: {
  clusters: Cluster[]
  pathname: string
  isAdmin: boolean
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
          <FleetNav clusters={clusters} pathname={pathname} />

          <div className="mt-5">
            <p className="label px-2 pb-2 text-rail-faint">You</p>
            <ul className="flex flex-col gap-0.5">
              <li>
                <NavLink to={ACCESS_HOME} className={railLinkClass}>
                  <Timer aria-hidden="true" className="size-4 shrink-0" />
                  My access
                </NavLink>
              </li>
            </ul>
          </div>

          {isAdmin
            ? ADMIN_GROUPS.map((group) => (
                <div key={group.id} className="mt-5">
                  <p className="label px-2 pb-2 text-rail-faint">{group.label}</p>
                  <ul className="flex flex-col gap-0.5">
                    {group.items.map((item) => (
                      <li key={item.to}>
                        <NavLink
                          to={item.to}
                          end={item.to === '/admin/clusters'}
                          className={railLinkClass}
                        >
                          <item.icon aria-hidden="true" className="size-4 shrink-0" />
                          {item.label}
                        </NavLink>
                      </li>
                    ))}
                  </ul>
                </div>
              ))
            : null}
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
