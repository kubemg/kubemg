import { HelmRepositoriesPanel } from '../../components/settings/HelmRepositoriesPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/** HelmSettings owns where a chart may be installed from — server-wide, not
    per-cluster. See `HelmRepositoriesPanel`. */
export function HelmSettings() {
  return (
    <SettingsLayout
      title="Helm"
      description="The chart repositories every cluster’s Helm catalogue is built from. They are server-wide: anyone signed in reads the catalogue, and only an administrator adds or removes a repository."
    >
      <div className="flex min-w-0 max-w-5xl flex-col gap-4">
        <HelmRepositoriesPanel />
      </div>
    </SettingsLayout>
  )
}
