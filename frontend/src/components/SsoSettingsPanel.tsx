import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Building2, Copy, KeyRound, Network, Plus, RefreshCw, Trash2 } from 'lucide-react'
import {
  checkSSOProvider,
  createSSOProvider,
  deleteSSOProvider,
  errorMessage,
  fetchSSOAdminProviders,
  updateSSOProvider,
} from '../api/client'
import type { SSOProtocol, SSOProvider, SSOProviderInput } from '../api/types'
import { GroupMappingEditor } from './GroupMappingEditor'
import {
  Button,
  EmptyState,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Select,
  Sheet,
  TextArea,
  TextInput,
} from './primitives'

/*
 * Configuring who may sign in.
 *
 * One panel per provider rather than a table, because a provider is not a row of
 * facts — it is a configuration with a verdict attached, and the two things an
 * operator actually needs from this screen are "does it reach the directory"
 * and "what do I paste into the IdP". Both are on the face of the panel; the
 * rest is behind the editor.
 *
 * No secret is ever rendered. A stored one shows as a placeholder saying so, and
 * leaving the field alone keeps it — which is the difference between changing an
 * LDAP port and re-typing a bind password nobody has written down.
 */

const PROTOCOL_LABEL: Record<SSOProtocol, string> = {
  oidc: 'OpenID Connect',
  saml: 'SAML 2.0',
  ldap: 'LDAP',
}

const PROTOCOL_ICON: Record<SSOProtocol, typeof KeyRound> = {
  oidc: KeyRound,
  saml: Building2,
  ldap: Network,
}

/** The form's own shape: every field a string, so "unset" survives editing. */
type Draft = {
  name: string
  protocol: SSOProtocol
  enabled: boolean

  issuer_url: string
  client_id: string
  client_secret: string
  scopes: string

  saml_metadata_url: string
  saml_metadata_xml: string
  saml_entity_id: string

  ldap_host: string
  ldap_port: string
  ldap_use_tls: boolean
  ldap_start_tls: boolean
  ldap_skip_verify: boolean
  ldap_bind_dn: string
  ldap_bind_password: string
  ldap_base_dn: string
  ldap_user_filter: string
  ldap_user_attribute: string
  ldap_email_attribute: string
  ldap_group_attribute: string
  ldap_group_filter: string
  ldap_group_base_dn: string
  ldap_group_name_attribute: string

  username_claim: string
  email_claim: string
  groups_claim: string

  allow_jit: boolean
  default_system_role: 'user' | 'admin'
}

const EMPTY: Draft = {
  name: '',
  protocol: 'oidc',
  enabled: true,
  issuer_url: '',
  client_id: '',
  client_secret: '',
  scopes: '',
  saml_metadata_url: '',
  saml_metadata_xml: '',
  saml_entity_id: '',
  ldap_host: '',
  ldap_port: '',
  ldap_use_tls: true,
  ldap_start_tls: false,
  ldap_skip_verify: false,
  ldap_bind_dn: '',
  ldap_bind_password: '',
  ldap_base_dn: '',
  ldap_user_filter: '',
  ldap_user_attribute: '',
  ldap_email_attribute: '',
  ldap_group_attribute: '',
  ldap_group_filter: '',
  ldap_group_base_dn: '',
  ldap_group_name_attribute: '',
  username_claim: '',
  email_claim: '',
  groups_claim: '',
  allow_jit: true,
  default_system_role: 'user',
}

function draftOf(provider: SSOProvider): Draft {
  return {
    ...EMPTY,
    name: provider.name,
    protocol: provider.protocol,
    enabled: provider.enabled,
    issuer_url: provider.issuer_url ?? '',
    client_id: provider.client_id ?? '',
    scopes: provider.scopes ?? '',
    saml_metadata_url: provider.saml_metadata_url ?? '',
    saml_entity_id: provider.saml_entity_id ?? '',
    ldap_host: provider.ldap_host ?? '',
    ldap_port: provider.ldap_port ? String(provider.ldap_port) : '',
    ldap_use_tls: provider.ldap_use_tls,
    ldap_start_tls: provider.ldap_start_tls,
    ldap_skip_verify: provider.ldap_skip_verify,
    ldap_bind_dn: provider.ldap_bind_dn ?? '',
    ldap_base_dn: provider.ldap_base_dn ?? '',
    ldap_user_filter: provider.ldap_user_filter ?? '',
    ldap_user_attribute: provider.ldap_user_attribute ?? '',
    ldap_email_attribute: provider.ldap_email_attribute ?? '',
    ldap_group_attribute: provider.ldap_group_attribute ?? '',
    ldap_group_filter: provider.ldap_group_filter ?? '',
    ldap_group_base_dn: provider.ldap_group_base_dn ?? '',
    ldap_group_name_attribute: provider.ldap_group_name_attribute ?? '',
    username_claim: provider.username_claim ?? '',
    email_claim: provider.email_claim ?? '',
    groups_claim: provider.groups_claim ?? '',
    allow_jit: provider.allow_jit,
    default_system_role: provider.default_system_role,
  }
}

/**
 * toInput renders the form for the API. A secret is only sent when it was
 * actually typed: an untouched field means "keep what is stored", and the one
 * way to clear one is to select the row and delete it deliberately.
 */
function toInput(draft: Draft): SSOProviderInput {
  const input: SSOProviderInput = {
    name: draft.name.trim(),
    protocol: draft.protocol,
    enabled: draft.enabled,
    allow_jit: draft.allow_jit,
    default_system_role: draft.default_system_role,
    username_claim: draft.username_claim.trim(),
    email_claim: draft.email_claim.trim(),
    groups_claim: draft.groups_claim.trim(),
  }

  if (draft.protocol === 'oidc') {
    input.issuer_url = draft.issuer_url.trim()
    input.client_id = draft.client_id.trim()
    input.scopes = draft.scopes.trim()
    if (draft.client_secret) input.client_secret = draft.client_secret
  }
  if (draft.protocol === 'saml') {
    input.saml_metadata_url = draft.saml_metadata_url.trim()
    input.saml_entity_id = draft.saml_entity_id.trim()
    if (draft.saml_metadata_xml.trim()) input.saml_metadata_xml = draft.saml_metadata_xml.trim()
  }
  if (draft.protocol === 'ldap') {
    input.ldap_host = draft.ldap_host.trim()
    input.ldap_port = Number(draft.ldap_port.trim() || 0)
    input.ldap_use_tls = draft.ldap_use_tls
    input.ldap_start_tls = draft.ldap_start_tls
    input.ldap_skip_verify = draft.ldap_skip_verify
    input.ldap_bind_dn = draft.ldap_bind_dn.trim()
    input.ldap_base_dn = draft.ldap_base_dn.trim()
    input.ldap_user_filter = draft.ldap_user_filter.trim()
    input.ldap_user_attribute = draft.ldap_user_attribute.trim()
    input.ldap_email_attribute = draft.ldap_email_attribute.trim()
    input.ldap_group_attribute = draft.ldap_group_attribute.trim()
    input.ldap_group_filter = draft.ldap_group_filter.trim()
    input.ldap_group_base_dn = draft.ldap_group_base_dn.trim()
    input.ldap_group_name_attribute = draft.ldap_group_name_attribute.trim()
    if (draft.ldap_bind_password) input.ldap_bind_password = draft.ldap_bind_password
  }
  return input
}

export function SsoSettingsPanel() {
  const [providers, setProviders] = useState<SSOProvider[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<SSOProvider | 'new' | null>(null)
  const [mapping, setMapping] = useState<SSOProvider | null>(null)
  const [checking, setChecking] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const next = await fetchSSOAdminProviders()
      setProviders(next.providers)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the identity providers.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function runCheck(provider: SSOProvider) {
    setChecking(provider.id)
    try {
      await checkSSOProvider(provider.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not check that provider.'))
    } finally {
      setChecking(null)
    }
  }

  async function remove(provider: SSOProvider) {
    if (
      !window.confirm(
        `Delete ${provider.name}? Accounts it created keep their access and their audit history, but nobody will be able to sign in through it.`,
      )
    ) {
      return
    }
    try {
      await deleteSSOProvider(provider.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not delete that provider.'))
    }
  }

  return (
    <>
      <Panel
        eyebrow="Identity"
        title="Single sign-on"
        description="Federate kubemg accounts to your own directory. An account signs in through exactly one of these or with a local password — never both — and what an external group is worth here is decided by the mapping rules, not by the directory."
        actions={
          <Button type="button" variant="primary" onClick={() => setEditing('new')}>
            <Plus aria-hidden="true" className="size-4" />
            Add provider
          </Button>
        }
        bodyClassName="flex flex-col gap-3 p-4"
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {!loading && providers.length === 0 ? (
          <EmptyState icon={<KeyRound className="size-4" />} title="No identity providers">
            kubemg is using local accounts only. Add an OIDC, SAML or LDAP provider to let people
            sign in with the credentials they already have.
          </EmptyState>
        ) : null}

        {providers.map((provider) => {
          const Icon = PROTOCOL_ICON[provider.protocol]
          return (
            <article key={provider.id} className="rounded-card border border-line bg-surface">
              <header className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                <div className="flex min-w-0 items-center gap-3">
                  <Icon aria-hidden="true" className="size-4 shrink-0 text-muted" />
                  <div className="min-w-0">
                    <p className="truncate text-[14px] font-medium text-fg">{provider.name}</p>
                    <p className="label mt-0.5">{PROTOCOL_LABEL[provider.protocol]}</p>
                  </div>
                </div>

                <div className="flex shrink-0 flex-wrap items-center gap-2">
                  {provider.enabled ? null : <Pill tone="idle">Disabled</Pill>}
                  <Pill
                    tone={
                      provider.last_status === 'healthy'
                        ? 'ok'
                        : provider.last_status === 'unhealthy'
                          ? 'bad'
                          : 'idle'
                    }
                    title={provider.last_message}
                  >
                    {provider.last_status === 'pending' ? 'Not checked' : provider.last_status}
                  </Pill>

                  <IconButton
                    label="Check connection"
                    disabled={checking === provider.id}
                    onClick={() => void runCheck(provider)}
                  >
                    <RefreshCw
                      aria-hidden="true"
                      className={`size-4 ${checking === provider.id ? 'animate-spin' : ''}`}
                    />
                  </IconButton>
                  <Button type="button" onClick={() => setMapping(provider)}>
                    Group mapping
                  </Button>
                  <Button type="button" onClick={() => setEditing(provider)}>
                    Configure
                  </Button>
                  <IconButton label="Delete provider" tone="danger" onClick={() => void remove(provider)}>
                    <Trash2 aria-hidden="true" className="size-4" />
                  </IconButton>
                </div>
              </header>

              {provider.last_message ? (
                <p className="border-t border-line-soft px-4 py-2 text-[12.5px] text-muted">
                  {provider.last_message}
                </p>
              ) : null}

              {/* What has to be registered on the other side. It is here rather
                  than in the editor because it is read far more often than it is
                  changed, and a mistyped one fails with nothing to explain it. */}
              {provider.protocol !== 'ldap' ? (
                <div className="flex flex-col gap-2 border-t border-line-soft px-4 py-3">
                  <CopyRow
                    label={provider.protocol === 'saml' ? 'Assertion consumer URL' : 'Redirect URI'}
                    value={provider.redirect_url}
                  />
                  {provider.entity_id ? (
                    <CopyRow label="Entity ID" value={provider.entity_id} />
                  ) : null}
                  {provider.metadata_url ? (
                    <CopyRow label="SP metadata" value={provider.metadata_url} />
                  ) : null}
                </div>
              ) : null}
            </article>
          )
        })}
      </Panel>

      {editing ? (
        <ProviderSheet
          provider={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}

      {mapping ? (
        <GroupMappingEditor provider={mapping} onClose={() => setMapping(null)} />
      ) : null}
    </>
  )
}

/** CopyRow is a value an operator has to paste somewhere else, with one click. */
function CopyRow({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false)

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-control bg-raised px-3 py-2">
      <span className="label shrink-0">{label}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg">{value}</span>
      <IconButton
        label={copied ? 'Copied' : 'Copy'}
        onClick={() => {
          void navigator.clipboard?.writeText(value)
          setCopied(true)
          window.setTimeout(() => setCopied(false), 1500)
        }}
      >
        <Copy aria-hidden="true" className={`size-4 ${copied ? 'text-ok' : ''}`} />
      </IconButton>
    </div>
  )
}

function ProviderSheet({
  provider,
  onClose,
  onSaved,
}: {
  provider: SSOProvider | null
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [draft, setDraft] = useState<Draft>(provider ? draftOf(provider) : EMPTY)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function set<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (provider) {
        await updateSSOProvider(provider.id, toInput(draft))
      } else {
        await createSSOProvider(toInput(draft))
      }
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save that provider.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Identity provider"
      title={provider ? `Configure ${provider.name}` : 'Add identity provider'}
      onClose={onClose}
      onSubmit={submit}
      width="lg"
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save provider'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <Field label="Name" htmlFor="sso-name" hint="What the button on the sign-in page says.">
        <TextInput
          id="sso-name"
          required
          value={draft.name}
          onChange={(event) => set('name', event.target.value)}
        />
      </Field>

      <Field label="Protocol" htmlFor="sso-protocol">
        <Select
          id="sso-protocol"
          // Changing the protocol of a saved provider would leave a
          // configuration describing a different kind of thing, so it is fixed
          // once created — delete it and add the other one.
          disabled={provider !== null}
          value={draft.protocol}
          onChange={(event) => set('protocol', event.target.value as SSOProtocol)}
        >
          {(Object.keys(PROTOCOL_LABEL) as SSOProtocol[]).map((protocol) => (
            <option key={protocol} value={protocol}>
              {PROTOCOL_LABEL[protocol]}
            </option>
          ))}
        </Select>
      </Field>

      {provider && provider.protocol !== 'ldap' ? (
        <Notice tone="info">
          Register{' '}
          <span className="font-mono">{provider.redirect_url}</span> with this provider
          {provider.entity_id ? (
            <>
              {' '}
              as the {provider.protocol === 'saml' ? 'assertion consumer service' : 'redirect URI'},
              using entity ID <span className="font-mono">{provider.entity_id}</span>
            </>
          ) : null}
          .
        </Notice>
      ) : null}

      {draft.protocol === 'oidc' ? (
        <>
          <Field
            label="Issuer URL"
            htmlFor="sso-issuer"
            hint="kubemg reads the provider's discovery document from here; no endpoint has to be entered by hand."
          >
            <TextInput
              id="sso-issuer"
              className="font-mono text-[12.5px]"
              placeholder="https://login.example.com/realms/main"
              value={draft.issuer_url}
              onChange={(event) => set('issuer_url', event.target.value)}
            />
          </Field>
          <Field label="Client ID" htmlFor="sso-client-id">
            <TextInput
              id="sso-client-id"
              className="font-mono text-[12.5px]"
              value={draft.client_id}
              onChange={(event) => set('client_id', event.target.value)}
            />
          </Field>
          <Field
            label="Client secret"
            htmlFor="sso-client-secret"
            hint={
              provider?.has_client_secret
                ? 'A secret is stored. Leave empty to keep it.'
                : 'Optional for a public client; kubemg always uses PKCE.'
            }
          >
            <TextInput
              id="sso-client-secret"
              type="password"
              autoComplete="new-password"
              placeholder={provider?.has_client_secret ? '••••••••' : ''}
              value={draft.client_secret}
              onChange={(event) => set('client_secret', event.target.value)}
            />
          </Field>
          <Field
            label="Scopes"
            htmlFor="sso-scopes"
            hint="Space separated, on top of openid. Leave empty for profile email groups."
          >
            <TextInput
              id="sso-scopes"
              className="font-mono text-[12.5px]"
              value={draft.scopes}
              onChange={(event) => set('scopes', event.target.value)}
            />
          </Field>
        </>
      ) : null}

      {draft.protocol === 'saml' ? (
        <>
          <Field
            label="IdP metadata URL"
            htmlFor="sso-saml-url"
            hint="Re-read periodically, so a certificate rotation at the IdP does not need a change here."
          >
            <TextInput
              id="sso-saml-url"
              className="font-mono text-[12.5px]"
              placeholder="https://idp.example.com/app/exk1/sso/saml/metadata"
              value={draft.saml_metadata_url}
              onChange={(event) => set('saml_metadata_url', event.target.value)}
            />
          </Field>
          <Field
            label="IdP metadata document"
            htmlFor="sso-saml-xml"
            hint="Paste the XML instead when your IdP hands out a file rather than a URL."
          >
            <TextArea
              id="sso-saml-xml"
              rows={5}
              placeholder="<EntityDescriptor …>"
              value={draft.saml_metadata_xml}
              onChange={(event) => set('saml_metadata_xml', event.target.value)}
            />
          </Field>
          <Field
            label="Entity ID"
            htmlFor="sso-saml-entity"
            hint="What kubemg calls itself to this IdP. Leave empty to use the SP metadata URL."
          >
            <TextInput
              id="sso-saml-entity"
              className="font-mono text-[12.5px]"
              value={draft.saml_entity_id}
              onChange={(event) => set('saml_entity_id', event.target.value)}
            />
          </Field>
        </>
      ) : null}

      {draft.protocol === 'ldap' ? (
        <>
          <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
            <Field label="Host" htmlFor="sso-ldap-host">
              <TextInput
                id="sso-ldap-host"
                className="font-mono text-[12.5px]"
                placeholder="ldap.example.com"
                value={draft.ldap_host}
                onChange={(event) => set('ldap_host', event.target.value)}
              />
            </Field>
            <Field
              label="Port"
              htmlFor="sso-ldap-port"
              hint={draft.ldap_use_tls ? 'Default 636' : 'Default 389'}
            >
              <TextInput
                id="sso-ldap-port"
                inputMode="numeric"
                className="font-mono text-[12.5px]"
                value={draft.ldap_port}
                onChange={(event) => set('ldap_port', event.target.value)}
              />
            </Field>
          </div>

          <Toggle
            id="sso-ldap-tls"
            label="LDAPS"
            hint="Dial with TLS from the start. Turn off only for StartTLS or an internal plaintext directory."
            checked={draft.ldap_use_tls}
            onChange={(next) => set('ldap_use_tls', next)}
          />
          {!draft.ldap_use_tls ? (
            <Toggle
              id="sso-ldap-starttls"
              label="StartTLS"
              hint="Upgrade the plain connection before binding."
              checked={draft.ldap_start_tls}
              onChange={(next) => set('ldap_start_tls', next)}
            />
          ) : null}
          {!draft.ldap_use_tls && !draft.ldap_start_tls ? (
            <Notice tone="warn">
              Without TLS the bind password and every user's password cross the network in the
              clear. Use this only against a directory on a trusted local network.
            </Notice>
          ) : null}
          <Toggle
            id="sso-ldap-skip"
            label="Skip certificate verification"
            hint="For a directory with an internal certificate nobody has exported yet."
            checked={draft.ldap_skip_verify}
            onChange={(next) => set('ldap_skip_verify', next)}
          />

          <Field
            label="Bind DN"
            htmlFor="sso-ldap-binddn"
            hint="The service account kubemg searches as. Leave empty for an anonymous search."
          >
            <TextInput
              id="sso-ldap-binddn"
              className="font-mono text-[12.5px]"
              placeholder="cn=kubemg,ou=services,dc=example,dc=com"
              value={draft.ldap_bind_dn}
              onChange={(event) => set('ldap_bind_dn', event.target.value)}
            />
          </Field>
          <Field
            label="Bind password"
            htmlFor="sso-ldap-bindpw"
            hint={provider?.has_bind_password ? 'A password is stored. Leave empty to keep it.' : undefined}
          >
            <TextInput
              id="sso-ldap-bindpw"
              type="password"
              autoComplete="new-password"
              placeholder={provider?.has_bind_password ? '••••••••' : ''}
              value={draft.ldap_bind_password}
              onChange={(event) => set('ldap_bind_password', event.target.value)}
            />
          </Field>
          <Field label="Base DN" htmlFor="sso-ldap-basedn">
            <TextInput
              id="sso-ldap-basedn"
              className="font-mono text-[12.5px]"
              placeholder="ou=people,dc=example,dc=com"
              value={draft.ldap_base_dn}
              onChange={(event) => set('ldap_base_dn', event.target.value)}
            />
          </Field>
          <Field
            label="User filter"
            htmlFor="sso-ldap-filter"
            hint="%s is the username. A filter without it is combined with the username attribute rather than replacing it."
          >
            <TextInput
              id="sso-ldap-filter"
              className="font-mono text-[12.5px]"
              placeholder="(&(objectClass=person)(uid=%s))"
              value={draft.ldap_user_filter}
              onChange={(event) => set('ldap_user_filter', event.target.value)}
            />
          </Field>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Username attribute" htmlFor="sso-ldap-userattr" hint="Default uid">
              <TextInput
                id="sso-ldap-userattr"
                className="font-mono text-[12.5px]"
                placeholder="uid"
                value={draft.ldap_user_attribute}
                onChange={(event) => set('ldap_user_attribute', event.target.value)}
              />
            </Field>
            <Field label="Email attribute" htmlFor="sso-ldap-mailattr" hint="Default mail">
              <TextInput
                id="sso-ldap-mailattr"
                className="font-mono text-[12.5px]"
                placeholder="mail"
                value={draft.ldap_email_attribute}
                onChange={(event) => set('ldap_email_attribute', event.target.value)}
              />
            </Field>
          </div>

          <Field
            label="Group attribute"
            htmlFor="sso-ldap-groupattr"
            hint="The attribute on the user entry listing its groups. Default memberOf."
          >
            <TextInput
              id="sso-ldap-groupattr"
              className="font-mono text-[12.5px]"
              placeholder="memberOf"
              value={draft.ldap_group_attribute}
              onChange={(event) => set('ldap_group_attribute', event.target.value)}
            />
          </Field>
          <Field
            label="Group search filter"
            htmlFor="sso-ldap-groupfilter"
            hint="Only for a directory that keeps membership on the group instead. %s is the user's DN."
          >
            <TextInput
              id="sso-ldap-groupfilter"
              className="font-mono text-[12.5px]"
              placeholder="(&(objectClass=groupOfNames)(member=%s))"
              value={draft.ldap_group_filter}
              onChange={(event) => set('ldap_group_filter', event.target.value)}
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Group base DN" htmlFor="sso-ldap-groupbase" hint="Defaults to the base DN.">
              <TextInput
                id="sso-ldap-groupbase"
                className="font-mono text-[12.5px]"
                value={draft.ldap_group_base_dn}
                onChange={(event) => set('ldap_group_base_dn', event.target.value)}
              />
            </Field>
            <Field
              label="Group name attribute"
              htmlFor="sso-ldap-groupname"
              hint="Match rules on cn rather than on the whole DN."
            >
              <TextInput
                id="sso-ldap-groupname"
                className="font-mono text-[12.5px]"
                placeholder="cn"
                value={draft.ldap_group_name_attribute}
                onChange={(event) => set('ldap_group_name_attribute', event.target.value)}
              />
            </Field>
          </div>
        </>
      ) : null}

      {draft.protocol !== 'ldap' ? (
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Username claim" htmlFor="sso-username-claim">
            <TextInput
              id="sso-username-claim"
              className="font-mono text-[12.5px]"
              placeholder={draft.protocol === 'oidc' ? 'preferred_username' : 'auto'}
              value={draft.username_claim}
              onChange={(event) => set('username_claim', event.target.value)}
            />
          </Field>
          <Field label="Email claim" htmlFor="sso-email-claim">
            <TextInput
              id="sso-email-claim"
              className="font-mono text-[12.5px]"
              placeholder={draft.protocol === 'oidc' ? 'email' : 'auto'}
              value={draft.email_claim}
              onChange={(event) => set('email_claim', event.target.value)}
            />
          </Field>
          <Field label="Groups claim" htmlFor="sso-groups-claim">
            <TextInput
              id="sso-groups-claim"
              className="font-mono text-[12.5px]"
              placeholder={draft.protocol === 'oidc' ? 'groups' : 'auto'}
              value={draft.groups_claim}
              onChange={(event) => set('groups_claim', event.target.value)}
            />
          </Field>
        </div>
      ) : null}

      <Toggle
        id="sso-enabled"
        label="Enabled"
        hint="Turning this off stops sign-ins immediately without deleting anything."
        checked={draft.enabled}
        onChange={(next) => set('enabled', next)}
      />
      <Toggle
        id="sso-jit"
        label="Create accounts on first sign-in"
        hint="With this off, someone the directory authenticates is still refused unless an account already exists here."
        checked={draft.allow_jit}
        onChange={(next) => set('allow_jit', next)}
      />

      <Field
        label="Role for new accounts"
        htmlFor="sso-default-role"
        hint="Administrators can also be conferred by a mapping rule. Super admin never is — that account has to survive an outage of this provider."
      >
        <Select
          id="sso-default-role"
          value={draft.default_system_role}
          onChange={(event) => set('default_system_role', event.target.value as 'user' | 'admin')}
        >
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </Select>
      </Field>
    </Sheet>
  )
}

/** Toggle is a checkbox with the explanation next to it rather than under it. */
function Toggle({
  id,
  label,
  hint,
  checked,
  onChange,
}: {
  id: string
  label: string
  hint?: string
  checked: boolean
  onChange: (next: boolean) => void
}) {
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-3">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent)]"
      />
      <span className="min-w-0">
        <span className="block text-[13.5px] text-fg">{label}</span>
        {hint ? <span className="mt-0.5 block text-[12px] leading-snug text-muted">{hint}</span> : null}
      </span>
    </label>
  )
}
