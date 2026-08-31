import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { TriangleAlert } from 'lucide-react'
import { Link } from 'react-router'
import { fetchDeploymentPosture } from '../../api/client'
import type { SettingSource } from '../../lib/settings'
import { AppShell } from '../AppShell'

/**
 * SettingsLayout is the shared frame every settings sub-page renders through.
 *
 * It used to draw a tab strip listing General / Agent / Audit / Guardrails /
 * Alerting / Helm / SSO / Deployment — the same eight destinations the rail's
 * Settings group already carries, one row above them, for one set of pages.
 * Navigation is the rail's job and this deck has deliberately no third level, so
 * the strip is gone; what it uniquely reached (Helm, and now Branding) moved
 * into the rail, which is where it should have been.
 *
 * What replaced it is the thing the page was actually missing. A settings form
 * stopped at about 800 px on a 1568 px window and the right half was empty,
 * while the questions an operator has in front of a settings field — what is in
 * force right now, whether that came from the environment at boot or from a
 * runtime override, and what a change here reaches — had nowhere to be answered.
 * That column is now the right half: `aside` is the slot, and each page fills it
 * with `SettingsAside` rows.
 *
 * The deployment count moved with the strip and did not survive it as a badge.
 * It is now a line in the aside, which is a better home for the same fact: it
 * was on the tab to stop the deployment page being one nobody opens, and a
 * sentence naming what is wrong does that better than a number on a tab.
 */
export function SettingsLayout({
  title,
  actions,
  aside,
  children,
}: {
  title: string
  actions?: ReactNode
  /** The right column: what is in force, where it came from, what it reaches.
      A page with nothing to say there passes nothing and the form takes the
      full width — an empty column is what this layout exists to remove. */
  aside?: ReactNode
  children: ReactNode
}) {
  return (
    <AppShell title={title} parent={{ label: 'Settings', to: '/admin/settings/general' }} actions={actions}>
      <div className="flex min-w-0 flex-col gap-6 xl:flex-row xl:items-start">
        <div className="flex min-w-0 flex-1 flex-col gap-4">
          {/* A page with no second column still has to carry this: it is the
              one thing on the settings surface that is about the install
              rather than about the page. */}
          {aside ? null : <DeploymentAttention />}
          {children}
        </div>
        {aside ? (
          <aside className="flex w-full shrink-0 flex-col gap-3 xl:sticky xl:top-20 xl:w-88">
            {aside}
            <DeploymentAttention />
          </aside>
        ) : null}
      </div>
    </AppShell>
  )
}

/**
 * One row of the right column: a setting's value as it stands, and where that
 * value came from.
 *
 * The source is the half that was missing. "24 hours" answers what is in force;
 * it does not answer whether that is the build's own default, a boot-time
 * environment variable somebody would have to redeploy to change, or a runtime
 * override stored in the database — which is precisely the question an operator
 * asks before editing a field, and the one that used to require reading the Go
 * source to answer.
 */
export function SettingsAside({
  label,
  value,
  source,
  reach,
}: {
  label: string
  value: ReactNode
  /** Where the value came from. Omitted where the distinction is meaningless. */
  source?: SettingSource
  /** What a change here reaches, in the caller's own words. Only worth saying
      where a change does not simply take effect on this server — an agent image
      that reaches nothing until somebody re-applies manifests, for instance. */
  reach?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5 rounded-card border border-line bg-surface p-3">
      <p className="label text-faint">{label}</p>
      <p className="min-w-0 font-mono text-[12.5px] break-words text-fg">{value}</p>
      {source ? <p className="text-[12px] text-muted">{SOURCE_COPY[source]}</p> : null}
      {reach ? <p className="text-[12px] text-muted">{reach}</p> : null}
    </div>
  )
}

/**
 * How a value got to be what it is. Three states, and the difference between the
 * middle one and the last is the one that decides whether an operator can change
 * something from this page at all.
 */
const SOURCE_COPY: Record<SettingSource, string> = {
  default: "The build's own default — nothing has been set.",
  environment: 'From this server’s environment at boot.',
  override: 'Overridden here, and stored. Clearing the field restores the default.',
}

/**
 * What is not ok about this deployment, on every settings page.
 *
 * The reasoning is inherited from the badge this replaces: the deployment checks
 * used to be visible only in a wizard that runs once, and moving them to a tab
 * of their own would have swapped "seen once" for "never seen". A self-signed
 * certificate or an unencrypted recording directory has to be visible from
 * wherever an administrator is working.
 *
 * A failure to read it draws nothing. This is a prompt to go and look, and a
 * settings page must not fail to render because the prompt could not be
 * resolved.
 */
function DeploymentAttention() {
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

  if (attention === 0) return null

  return (
    <Link
      to="/admin/settings/deployment"
      className="flex items-start gap-2 rounded-card border border-warn/40 bg-warn-soft p-3 text-[12.5px] text-warn transition-colors hover:border-warn/70"
    >
      <TriangleAlert aria-hidden="true" className="mt-px size-4 shrink-0" />
      <span>
        {attention} thing{attention === 1 ? '' : 's'} about this install{' '}
        {attention === 1 ? 'is' : 'are'} worth reading.
      </span>
    </Link>
  )
}
