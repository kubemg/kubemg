/*
 * Which username claims a person can change at their own identity provider.
 *
 * The username a federated sign-in provisions is the account's name for good,
 * and the suffix of the identity every cluster is shown. KubeMG prefixes that
 * identity and refuses a name with a colon, so an edited claim can no longer
 * become somebody else's Kubernetes identity — but it can still become somebody
 * else's *KubeMG* account on first sign-in, or collide with one. `sub` (or an
 * attribute only a directory administrator writes) is the claim that cannot.
 */

import type { SSOProtocol, SSOVendor } from '../api/types'

// Claims many IdPs let the signed-in person edit, or that are display strings
// rather than identifiers. `email` is here because not every provider verifies
// it and some let a user change it freely.
const EDITABLE_CLAIMS = new Set([
  'preferred_username',
  'username',
  'nickname',
  'name',
  'given_name',
  'family_name',
  'email',
])

/**
 * Reports whether the username claim a provider is configured with is one a
 * person may be able to edit. An empty claim reads as what the server defaults
 * it to: `preferred_username` for OIDC, and for SAML a fallback list that
 * starts with the same editable names. LDAP binds on the login attribute and
 * has no claim to warn about.
 */
export function usernameClaimIsEditable(protocol: SSOProtocol, claim: string): boolean {
  if (protocol === 'ldap') return false
  const effective = claim.trim() || (protocol === 'oidc' ? 'preferred_username' : '')
  if (effective === '') return true
  return EDITABLE_CLAIMS.has(effective)
}

/*
 * What the provider form offers. Okta is OIDC or SAML underneath — the vendor
 * only changes the defaults, the hints and what the server checks on save — so
 * one choice carries both halves rather than asking for them separately.
 */
export type ProviderKind = 'okta-oidc' | 'okta-saml' | SSOProtocol

export const PROVIDER_KINDS: { kind: ProviderKind; label: string; group: 'Okta' | 'Any provider' }[] = [
  { kind: 'okta-oidc', label: 'Okta — OpenID Connect', group: 'Okta' },
  { kind: 'okta-saml', label: 'Okta — SAML 2.0', group: 'Okta' },
  { kind: 'oidc', label: 'OpenID Connect', group: 'Any provider' },
  { kind: 'saml', label: 'SAML 2.0', group: 'Any provider' },
  { kind: 'ldap', label: 'LDAP', group: 'Any provider' },
]

export function kindOf(protocol: SSOProtocol, vendor?: SSOVendor): ProviderKind {
  if (vendor === 'okta' && protocol !== 'ldap') return `okta-${protocol}`
  return protocol
}

export function splitKind(kind: ProviderKind): { protocol: SSOProtocol; vendor?: SSOVendor } {
  if (kind === 'okta-oidc') return { protocol: 'oidc', vendor: 'okta' }
  if (kind === 'okta-saml') return { protocol: 'saml', vendor: 'okta' }
  return { protocol: kind }
}

/**
 * The scopes the server asks for when the field is left empty. An Okta custom
 * authorization server (`/oauth2/{id}`) refuses a sign-in that asks for a scope
 * it has not declared, and carries groups as a claim instead, so it is not
 * asked for `groups`.
 */
export function defaultScopes(vendor: SSOVendor | undefined, issuer: string): string {
  if (vendor === 'okta' && issuer.includes('/oauth2/')) return 'profile email'
  return 'profile email groups'
}
