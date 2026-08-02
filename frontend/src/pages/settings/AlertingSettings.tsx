import { AlarmSettingsPanel } from '../../components/settings/AlarmSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'
import { useClusters } from '../../state/clusters-context'

/** AlertingSettings owns where a cluster event or a refused action goes. It
    saves its own channels and rules in their own sheets. */
export function AlertingSettings() {
  const { clusters } = useClusters()

  return (
    <SettingsLayout title="Alerting">
      <div className="flex min-w-0 max-w-3xl flex-col gap-4">
        <AlarmSettingsPanel clusters={clusters} />
      </div>
    </SettingsLayout>
  )
}
