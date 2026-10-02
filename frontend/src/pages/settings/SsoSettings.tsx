import { SsoSettingsPanel } from '../../components/SsoSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/** SsoSettings owns who may sign in at all. It saves its own providers in
    their own sheet, not through a page-wide Save button. */
export function SsoSettings() {
  return (
    <SettingsLayout
      title="SSO"
      description="Sign-in through your own directory over OIDC, SAML or LDAP, and how a group the directory sends maps onto kubemg’s groups. An account signs in through one provider or with a local password, never both."
    >
      <div className="flex min-w-0 max-w-5xl flex-col gap-4">
        <SsoSettingsPanel />
      </div>
    </SettingsLayout>
  )
}
