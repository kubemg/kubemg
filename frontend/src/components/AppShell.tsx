import {
  Boxes,
  ChevronRight,
  KeySquare,
  LayoutGrid,
  LogOut,
  ScrollText,
  Server,
  SlidersHorizontal,
  Users,
  UsersRound,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { Link, NavLink } from 'react-router-dom'
import { useAuth } from '../state/auth-context'
import { ClusterSelector } from './ClusterSelector'

/* Clusters are the product. Issuing access is an action on one, not a section.
   The rail is grouped the way the work splits: the fleet, then who reaches it. */
const NAV = [
  { to: '/', label: 'Overview', icon: LayoutGrid, adminOnly: false, group: 'Fleet' },
  { to: '/clusters', label: 'Clusters', icon: Server, adminOnly: true, group: 'Fleet' },
  { to: '/explore', label: 'Explore', icon: Boxes, adminOnly: false, group: 'Fleet' },
  { to: '/users', label: 'Users', icon: Users, adminOnly: true, group: 'Access' },
  { to: '/groups', label: 'Groups', icon: UsersRound, adminOnly: true, group: 'Access' },
  { to: '/permissions', label: 'Permissions', icon: KeySquare, adminOnly: true, group: 'Access' },
  // Everyone can reach the audit trail; a non-admin only sees their own actions.
  { to: '/audit', label: 'Audit', icon: ScrollText, adminOnly: false, group: 'Access' },
  { to: '/settings', label: 'Settings', icon: SlidersHorizontal, adminOnly: true, group: 'System' },
]

function navClass({ isActive }: { isActive: boolean }) {
  const base =
    'flex items-center gap-2.5 rounded-[5px] px-2.5 py-1.5 text-[13px] transition-colors'
  return isActive
    ? `${base} bg-ink-raised text-white shadow-[inset_2px_0_0_var(--color-primary)]`
    : `${base} text-ink-muted hover:bg-ink-raised hover:text-ink-fg`
}

/**
 * AppShell is the console frame: a dark rail carrying context, navigation and
 * identity, and a light working surface for everything you read and act on.
 */
export function AppShell({
  title,
  parent,
  actions,
  children,
}: {
  title: string
  /** Rendered ahead of the title as a breadcrumb, for pages nested under a section. */
  parent?: { label: string; to: string }
  actions?: ReactNode
  children: ReactNode
}) {
  const { user, signOut } = useAuth()
  const items = NAV.filter((item) => !item.adminOnly || user?.role === 'admin')
  const groups = ['Fleet', 'Access', 'System'].filter((group) =>
    items.some((item) => item.group === group),
  )
  const initials = (user?.username ?? '').slice(0, 2).toUpperCase()

  return (
    <div className="flex min-h-svh">
      <aside className="fixed inset-y-0 left-0 hidden w-56 flex-col bg-ink lg:flex">
        <div className="flex h-12 items-center gap-2.5 border-b border-ink-line px-3.5">
          <span className="grid size-[22px] place-items-center rounded-[5px] bg-primary font-mono text-[11px] font-bold text-white">
            MG
          </span>
          <span className="text-[13.5px] font-bold tracking-[0.14em] text-white">KUBEMG</span>
        </div>

        <div className="px-3 pt-3 pb-1">
          <p className="label mb-1.5 text-ink-faint">Cluster context</p>
          <ClusterSelector tone="ink" />
        </div>

        <nav className="flex flex-1 flex-col gap-3 p-2.5">
          {groups.map((group) => (
            <div key={group} className="flex flex-col gap-0.5">
              <p className="label px-2.5 pb-1 text-ink-faint">{group}</p>
              {items
                .filter((item) => item.group === group)
                .map((item) => (
                  <NavLink key={item.to} to={item.to} end={item.to === '/'} className={navClass}>
                    <item.icon aria-hidden="true" className="size-3.5 shrink-0" />
                    {item.label}
                  </NavLink>
                ))}
            </div>
          ))}
        </nav>

        <div className="flex items-center gap-2.5 border-t border-ink-line px-3.5 py-3">
          <span className="grid size-7 shrink-0 place-items-center rounded-full bg-ink-raised text-[11px] font-semibold text-ink-fg">
            {initials}
          </span>
          <div className="min-w-0 flex-1 leading-tight">
            <p className="truncate text-[13px] text-ink-fg">{user?.username}</p>
            <p className="label text-ink-faint">{user?.role}</p>
          </div>
          <button
            type="button"
            onClick={signOut}
            title="Sign out"
            className="rounded-[5px] p-1.5 text-ink-muted transition-colors hover:bg-ink-raised hover:text-white"
          >
            <LogOut aria-hidden="true" className="size-3.5" />
            <span className="sr-only">Sign out</span>
          </button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col lg:ml-56">
        <header className="sticky top-0 z-10 border-b border-line bg-surface">
          <div className="flex h-12 items-center gap-3 px-4">
            <span className="hidden text-[13px] font-bold tracking-[0.14em] text-fg sm:inline lg:hidden">
              KUBEMG
            </span>
            <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5">
              {parent ? (
                <>
                  <Link
                    to={parent.to}
                    className="shrink-0 text-[13px] text-muted transition-colors hover:text-fg"
                  >
                    {parent.label}
                  </Link>
                  <ChevronRight aria-hidden="true" className="size-3.5 shrink-0 text-faint" />
                </>
              ) : null}
              <h1 className="min-w-0 truncate text-[13px] font-semibold text-fg">{title}</h1>
            </nav>
            <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>
          </div>

          {/* Below the rail breakpoint the sidebar collapses into this row. */}
          <div className="flex flex-wrap items-center gap-2 border-t border-line-soft bg-bg px-4 py-2 lg:hidden">
            <div className="min-w-0 max-w-[190px] flex-1">
              <ClusterSelector />
            </div>
            <nav className="flex min-w-0 items-center gap-1 overflow-x-auto">
              {items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.to === '/'}
                  className={({ isActive }) =>
                    `shrink-0 rounded-[5px] px-2.5 py-1.5 text-[13px] transition-colors ${
                      isActive
                        ? 'bg-primary-soft font-medium text-primary'
                        : 'text-muted hover:bg-raised hover:text-fg'
                    }`
                  }
                >
                  {item.label}
                </NavLink>
              ))}
            </nav>
            <button
              type="button"
              onClick={signOut}
              title="Sign out"
              className="ml-auto rounded-[5px] border border-line p-1.5 text-muted transition-colors hover:border-danger/50 hover:text-danger"
            >
              <LogOut aria-hidden="true" className="size-3.5" />
              <span className="sr-only">Sign out</span>
            </button>
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4">{children}</main>
      </div>
    </div>
  )
}
