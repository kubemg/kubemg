import { SsoSettingsPanel } from '../../components/SsoSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/** SsoSettings owns who may sign in at all. It saves its own providers in
    their own sheet, not through a page-wide Save button. */
export function SsoSettings() {
  return (
    <SettingsLayout title="SSO">
      <div className="flex min-w-0 max-w-3xl flex-col gap-4">
        <SsoSettingsPanel />
      </div>
    </SettingsLayout>
  )
}
