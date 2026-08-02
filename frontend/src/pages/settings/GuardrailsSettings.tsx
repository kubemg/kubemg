import { GuardrailSettingsPanel } from '../../components/settings/GuardrailSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'
import { useClusters } from '../../state/clusters-context'

/** GuardrailsSettings owns what the platform refuses to pass on, whatever the
    cluster's own RBAC allows. It saves its own rules in its own sheet. */
export function GuardrailsSettings() {
  const { clusters } = useClusters()

  return (
    <SettingsLayout title="Guardrails">
      <div className="flex min-w-0 max-w-3xl flex-col gap-4">
        <GuardrailSettingsPanel clusters={clusters} />
      </div>
    </SettingsLayout>
  )
}
