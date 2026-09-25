import { describe, expect, it } from 'vitest'

import { usernameClaimIsEditable } from './sso'

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
