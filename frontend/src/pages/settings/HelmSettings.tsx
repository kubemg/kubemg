import { HelmRepositoriesPanel } from '../../components/settings/HelmRepositoriesPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/** HelmSettings owns where a chart may be installed from — server-wide, not
    per-cluster. See `HelmRepositoriesPanel`. */
export function HelmSettings() {
  return (
    <SettingsLayout title="Helm">
      <div className="flex min-w-0 max-w-3xl flex-col gap-4">
        <HelmRepositoriesPanel />
      </div>
    </SettingsLayout>
  )
}
