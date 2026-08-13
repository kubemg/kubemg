import { useCallback, useEffect, useState } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchDeploymentPosture } from '../../api/client'
import type { DeploymentPosture } from '../../api/types'
import { Button, Notice, Panel } from '../../components/primitives'
import { DeploymentCheckList } from '../../components/settings/DeploymentChecks'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/**
 * What this install was started with — the certificate it serves, whether
 * recordings are encrypted, where its signing key came from.
 *
 * None of it is writable, and that is the reason the page exists rather than an
 * argument against it. These values are read once at boot from an environment
 * the process cannot rewrite, so the setup wizard could only ever report them —
 * and the wizard runs exactly once, on an install that is minutes old, in front
 * of whoever happened to type the first `docker compose up`. A self-signed
 * certificate is still self-signed a year later, in front of somebody who
 * inherited the bastion and has never seen that screen. This is where they find
 * out, in the same words and from the same checks.
 */
export function DeploymentSettings() {
  const [posture, setPosture] = useState<DeploymentPosture | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setPosture(await fetchDeploymentPosture())
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read how this server was started.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <SettingsLayout
      title="Deployment"
      actions={
        <Button type="button" variant="ghost" onClick={() => void load()} disabled={loading}>
          <RotateCcw aria-hidden="true" className="size-4" />
          Re-read
        </Button>
      }
    >
      <div className="flex min-w-0 max-w-3xl flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {posture ? (
          <Panel
            eyebrow="This install"
            title="Settled at boot, not from here"
            description="Every line below was read when the server started, from an environment no request can change — so this page reports them and names what to change instead. A restart is what picks up a change; nothing here needs the setup wizard back."
            bodyClassName="flex flex-col gap-3 p-4"
          >
            <DeploymentCheckList checks={posture.checks} />
          </Panel>
        ) : null}
      </div>
    </SettingsLayout>
  )
}
