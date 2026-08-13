import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { NavLink } from 'react-router'
import { fetchDeploymentPosture } from '../../api/client'
import { AppShell } from '../AppShell'

const TABS = [
  { to: '/settings/general', label: 'General' },
  { to: '/settings/agent', label: 'Agent' },
  { to: '/settings/audit', label: 'Audit' },
  { to: '/settings/cost', label: 'Cost' },
  { to: '/settings/guardrails', label: 'Guardrails' },
  { to: '/settings/alerting', label: 'Alerting' },
  { to: '/settings/sso', label: 'SSO' },
  { to: '/settings/deployment', label: 'Deployment' },
] as const

/**
 * SettingsLayout is the shared frame every settings sub-page renders through:
 * one AppShell instance, one tab bar. Each sub-page owns its own data and its
 * own Save button; the layout only owns which tab is lit and where it goes.
 *
 * The one exception is the count on the Deployment tab, which is here on purpose.
 * A page nobody opens is the failure mode this whole surface was built against:
 * the deployment checks used to be visible only in a wizard that runs once, and
 * moving them to a tab of their own would have swapped "seen once" for "never
 * seen". The badge is what makes a self-signed certificate or an unencrypted
 * recording directory visible from every settings page instead.
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
  const attention = useDeploymentAttention()

  return (
    <AppShell title={title} parent={{ label: 'Settings', to: '/settings/general' }} actions={actions}>
      <div className="flex min-w-0 flex-col gap-4">
        <nav aria-label="Settings sections" className="flex gap-1 overflow-x-auto border-b border-line">
          {TABS.map((tab) => (
            <NavLink
              key={tab.to}
              to={tab.to}
              className={({ isActive }) =>
                `flex shrink-0 items-center gap-1.5 rounded-t-control px-3 py-2 text-[13px] transition-colors ${
                  isActive ? 'bg-raised font-medium text-fg' : 'text-muted hover:text-fg'
                }`
              }
            >
              {tab.label}
              {tab.to === '/settings/deployment' && attention > 0 ? (
                <span
                  className="rounded-full bg-warn-soft px-1.5 py-0.5 text-[11px] font-medium text-warn"
                  title={`${attention} thing${attention === 1 ? '' : 's'} about this install worth reading`}
                >
                  {attention}
                </span>
              ) : null}
            </NavLink>
          ))}
        </nav>
        {children}
      </div>
    </AppShell>
  )
}

/** How many deployment checks are not ok. A failure reads as none: the badge is
    a prompt to go and look, and a settings page must not fail to render because
    the prompt could not be resolved. */
function useDeploymentAttention(): number {
  const [attention, setAttention] = useState(0)

  useEffect(() => {
    let live = true
    void fetchDeploymentPosture()
      .then((posture) => {
        if (live) setAttention(posture.attention)
      })
      .catch(() => undefined)
    return () => {
      live = false
    }
  }, [])

  return attention
}
