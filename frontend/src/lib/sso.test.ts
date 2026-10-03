import { describe, expect, it } from 'vitest'

import { PROVIDER_KINDS, defaultScopes, kindOf, splitKind, usernameClaimIsEditable } from './sso'

describe('usernameClaimIsEditable', () => {
  it('reads an empty OIDC claim as the preferred_username default, which is editable', () => {
    expect(usernameClaimIsEditable('oidc', '')).toBe(true)
    expect(usernameClaimIsEditable('oidc', '  ')).toBe(true)
  })

  it('flags the claims a person can change at their provider', () => {
    for (const claim of ['preferred_username', 'username', 'nickname', 'email', 'name']) {
      expect(usernameClaimIsEditable('oidc', claim)).toBe(true)
    }
  })

  it('accepts an immutable identifier', () => {
    expect(usernameClaimIsEditable('oidc', 'sub')).toBe(false)
    expect(usernameClaimIsEditable('oidc', 'oid')).toBe(false)
    expect(usernameClaimIsEditable('saml', 'employeeNumber')).toBe(false)
  })

  it("reads an empty SAML claim as the server's fallback list, which starts editable", () => {
    expect(usernameClaimIsEditable('saml', '')).toBe(true)
  })

  it('has nothing to say about LDAP', () => {
    expect(usernameClaimIsEditable('ldap', '')).toBe(false)
  })
})

describe('provider kinds', () => {
  it('round-trips Okta through its protocol and keeps LDAP generic', () => {
    for (const { kind } of PROVIDER_KINDS) {
      const { protocol, vendor } = splitKind(kind)
      expect(kindOf(protocol, vendor)).toBe(kind)
    }
    expect(kindOf('ldap', 'okta')).toBe('ldap')
  })

  it('does not ask an Okta custom authorization server for a groups scope', () => {
    expect(defaultScopes('okta', 'https://acme.okta.com/oauth2/default')).toBe('profile email')
    expect(defaultScopes('okta', 'https://acme.okta.com')).toBe('profile email groups')
    expect(defaultScopes(undefined, 'https://idp.example.com/oauth2/x')).toBe('profile email groups')
  })
})
