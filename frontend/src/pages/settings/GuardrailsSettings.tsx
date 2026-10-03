import { GuardrailSettingsPanel } from '../../components/settings/GuardrailSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'
import { useClusters } from '../../state/clusters-context'

/** GuardrailsSettings owns what the platform refuses to pass on, whatever the
    cluster's own RBAC allows. It saves its own rules in its own sheet. */
export function GuardrailsSettings() {
  const { clusters } = useClusters()

  return (
    <SettingsLayout
      title="Guardrails"
      description="Rules kubemg checks every call against before the cluster sees it, on top of whatever its RBAC allows. A block refuses the call; a warn lets it through and records the match, which is how a new rule is tried out before it is armed."
    >
      <div className="flex min-w-0 max-w-5xl flex-col gap-4">
        <GuardrailSettingsPanel clusters={clusters} />
      </div>
    </SettingsLayout>
  )
}
