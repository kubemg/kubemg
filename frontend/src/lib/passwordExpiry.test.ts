import { describe, expect, it } from 'vitest'
import { passwordExpirable, passwordStatus } from './passwordExpiry'

const local = { auth_source: 'local' as const, account_type: 'user' as const }
const admin = { id: 1, system_role: 'admin' as const }

describe('passwordExpirable', () => {
  const target = { ...local, id: 2, system_role: 'user' as const }

  it('offers an administrator another local account', () => {
    expect(passwordExpirable(target, admin)).toBe(true)
  })
  it('never offers your own', () => {
    expect(passwordExpirable({ ...target, id: 1 }, admin)).toBe(false)
  })
  it('never offers a password held elsewhere', () => {
    expect(passwordExpirable({ ...target, auth_source: 'oidc' }, admin)).toBe(false)
    expect(passwordExpirable({ ...target, auth_source: 'ldap' }, admin)).toBe(false)
    expect(passwordExpirable({ ...target, account_type: 'machine' }, admin)).toBe(false)
  })
  it('leaves a super admin to a super admin', () => {
    const boss = { ...target, system_role: 'superadmin' as const }
    expect(passwordExpirable(boss, admin)).toBe(false)
    expect(passwordExpirable(boss, { id: 1, system_role: 'superadmin' })).toBe(true)
  })
  it('has nothing to expire once the change is owed', () => {
    expect(passwordExpirable({ ...target, password_change_required: true }, admin)).toBe(false)
  })
})

describe('passwordStatus', () => {
  const now = new Date('2026-10-21T12:00:00Z')

  it('says a federated password is not ours', () => {
    expect(passwordStatus({ auth_source: 'saml', account_type: 'user' }, now).tone).toBe('idle')
  })
  it('says no expiry when nothing makes it due', () => {
    expect(passwordStatus(local, now).text).toBe('Does not expire')
  })
  it('warns inside the reminder window', () => {
    const status = passwordStatus({ ...local, password_expires_at: '2026-10-24T12:00:00Z' }, now)
    expect(status.tone).toBe('warn')
    expect(status.text).toContain('3 days')
  })
  it('is calm outside it', () => {
    expect(passwordStatus({ ...local, password_expires_at: '2027-01-01T00:00:00Z' }, now).tone).toBe('ok')
  })
  it('says an owed change as such', () => {
    const status = passwordStatus(
      { ...local, password_expires_at: '2026-10-01T00:00:00Z', password_change_required: true },
      now,
    )
    expect(status.tone).toBe('bad')
    expect(status.text).toContain('must change')
  })
})
