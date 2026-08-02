import type { ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { AppShell } from '../AppShell'

const TABS = [
  { to: '/settings/general', label: 'General' },
  { to: '/settings/agent', label: 'Agent' },
  { to: '/settings/audit', label: 'Audit' },
  { to: '/settings/guardrails', label: 'Guardrails' },
  { to: '/settings/alerting', label: 'Alerting' },
  { to: '/settings/sso', label: 'SSO' },
] as const

/**
 * SettingsLayout is the shared frame every settings sub-page renders through:
 * one AppShell instance, one tab bar. Each sub-page owns its own data and its
 * own Save button; the layout only owns which tab is lit and where it goes.
 */
export function SettingsLayout({
  title,
  actions,
  children,
}: {
  title: string
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <AppShell title={title} parent={{ label: 'Settings', to: '/settings/general' }} actions={actions}>
      <div className="flex min-w-0 flex-col gap-4">
        <nav aria-label="Settings sections" className="flex gap-1 overflow-x-auto border-b border-line">
          {TABS.map((tab) => (
            <NavLink
              key={tab.to}
              to={tab.to}
              className={({ isActive }) =>
                `shrink-0 rounded-t-control px-3 py-2 text-[13px] transition-colors ${
                  isActive ? 'bg-raised font-medium text-fg' : 'text-muted hover:text-fg'
                }`
              }
            >
              {tab.label}
            </NavLink>
          ))}
        </nav>
        {children}
      </div>
    </AppShell>
  )
}
