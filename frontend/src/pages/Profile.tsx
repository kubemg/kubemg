import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { ChevronRight, FileKey, KeyRound, Pencil, ShieldCheck, Timer } from 'lucide-react'
import { fetchMFA } from '../api/client'
import type { MFAFactor, MFAStatus, SystemRole, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import { AddFactorSheet, RecoveryCodesSheet, RemoveFactorSheet } from '../components/MfaSheets'
import { PasswordSheet } from '../components/PasswordSheet'
import { ProfileSheet } from '../components/ProfileSheet'
import { Button, Panel, Pill } from '../components/primitives'
import { secondFactorApplies } from '../lib/mfa'
import { ACCESS_HOME, CREDENTIALS_HOME } from '../lib/navigation'
import { profileAbilities } from '../lib/profile'
import { formatInstant, relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'
import { useResult } from '../state/result-context'

/**
 * The signed-in person's own account, reached from their name on the sidebar's
 * person card.
 *
 * It reads `/auth/me` — the account the session already holds — and offers the
 * two acts that are the owner's rather than an administrator's: editing the
 * email and rotating the password. Both are sheets, and both are **absent**, not
 * disabled, for an account whose details do not live here: a federated
 * account's belong to its directory, a machine account is administered.
 *
 * What it can reach is not repeated here. That is My access, linked below, which
 * is the same reading the gateway enforces; a second copy free to disagree with
 * it would be worse than a link.
 */
export function Profile() {
  const { user, replaceUser } = useAuth()
  const report = useResult()
  const [editing, setEditing] = useState(false)
  const [changingPassword, setChangingPassword] = useState(false)
  const [mfa, setMfa] = useState<MFAStatus | null>(null)
  const [mfaSheet, setMfaSheet] = useState<
    { kind: 'add' } | { kind: 'remove'; factor: MFAFactor } | { kind: 'recovery' } | null
  >(null)
  const applies = user ? secondFactorApplies(user) : false

  const loadMfa = useCallback(() => {
    if (!applies) return
    fetchMFA()
      .then(setMfa)
      .catch(() => setMfa(null))
  }, [applies])
  useEffect(loadMfa, [loadMfa])

  if (!user) return null

  return (
    <AppShell
      title="My profile"
      description="The account you are signed in as, the details you can change yourself, and your password."
    >
      {editing ? (
        <ProfileSheet
          user={user}
          onClose={() => setEditing(false)}
          onSaved={(updated) => {
            replaceUser(updated)
            setEditing(false)
            report({
              tone: 'ok',
              title: 'Saved',
              body: updated.email
                ? `Your email is now ${updated.email}.`
                : 'Your email address was removed.',
              link:
                user.role === 'admin'
                  ? { to: '/admin/audit', label: 'See it in the audit trail' }
                  : undefined,
            })
          }}
        />
      ) : null}
      {changingPassword ? <PasswordSheet onClose={() => setChangingPassword(false)} /> : null}

      {mfa && mfaSheet?.kind === 'add' ? (
        <AddFactorSheet
          status={mfa}
          account={user.username}
          onClose={() => setMfaSheet(null)}
          onAdded={() => {
            setMfaSheet(null)
            loadMfa()
            report({ tone: 'ok', title: 'Factor added', body: 'Your next sign-in asks for its code.' })
          }}
        />
      ) : null}
      {mfa && mfaSheet?.kind === 'remove' ? (
        <RemoveFactorSheet
          factor={mfaSheet.factor}
          last={mfa.factors.length === 1}
          onClose={() => setMfaSheet(null)}
          onRemoved={(next) => {
            setMfaSheet(null)
            setMfa(next)
            report({
              tone: 'ok',
              title: 'Factor removed',
              body: `${mfaSheet.factor.name} no longer signs you in.`,
            })
          }}
        />
      ) : null}
      {mfaSheet?.kind === 'recovery' ? (
        <RecoveryCodesSheet
          account={user.username}
          onClose={() => setMfaSheet(null)}
          onReplaced={() => {
            setMfaSheet(null)
            loadMfa()
          }}
        />
      ) : null}

      <ProfileBody
        user={user}
        mfa={applies ? mfa : undefined}
        onEdit={() => setEditing(true)}
        onChangePassword={() => setChangingPassword(true)}
        onAddFactor={() => setMfaSheet({ kind: 'add' })}
        onRemoveFactor={(factor) => setMfaSheet({ kind: 'remove', factor })}
        onNewRecoveryCodes={() => setMfaSheet({ kind: 'recovery' })}
      />
    </AppShell>
  )
}

const ROLE_WORD: Record<SystemRole, string> = {
  superadmin: 'Super administrator',
  admin: 'Administrator',
  user: 'Developer',
}

/** The page without its shell, so the decisions it makes are testable alone. */
export function ProfileBody({
  user,
  mfa,
  onEdit,
  onChangePassword,
  onAddFactor = () => {},
  onRemoveFactor = () => {},
  onNewRecoveryCodes = () => {},
}: {
  user: User
  /** The account's second factor. Undefined draws no panel; null is loading. */
  mfa?: MFAStatus | null
  onEdit: () => void
  onChangePassword: () => void
  onAddFactor?: () => void
  onRemoveFactor?: (factor: MFAFactor) => void
  onNewRecoveryCodes?: () => void
}) {
  const can = profileAbilities(user)
  const federated = user.auth_source !== 'local'
  const initials = user.username.slice(0, 2).toUpperCase()

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Panel
        title="Account"
        description={
          federated
            ? 'Your identity provider vouches for this account, so its details are the directory’s and are refreshed at every sign-in.'
            : 'Who you are signed in as. Your role and capabilities are granted by an administrator.'
        }
        actions={
          can.editDetails ? (
            <Button size="sm" onClick={onEdit}>
              <Pencil aria-hidden="true" className="size-3.5" />
              Edit profile
            </Button>
          ) : null
        }
        bodyClassName="flex flex-col gap-5 p-4"
      >
        <div className="flex min-w-0 items-center gap-3.5">
          <span className="grid size-14 shrink-0 place-items-center rounded-full border border-accent-line bg-accent-soft font-data text-[17px] font-semibold text-fg">
            {initials}
          </span>
          <div className="min-w-0 flex-1">
            <p className="truncate font-data text-[18px] font-semibold text-fg">{user.username}</p>
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <Pill tone={user.system_role === 'user' ? 'idle' : 'accent'}>
                {ROLE_WORD[user.system_role] ?? user.system_role}
              </Pill>
              {user.can_view_recordings ? <Pill tone="idle">Views others’ recordings</Pill> : null}
              {user.can_reveal_secrets ? <Pill tone="idle">Reveals secret values</Pill> : null}
            </div>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Fact label="Email" value={user.email || 'None recorded'} data={Boolean(user.email)} />
          <Fact
            label="Signs in through"
            value={federated ? `Your identity provider (${user.auth_source.toUpperCase()})` : 'A password held here'}
          />
          <Fact
            label="Last sign-in"
            value={user.last_login_at ? relativeAge(user.last_login_at) : 'Never'}
            title={user.last_login_at ? formatInstant(user.last_login_at) : undefined}
          />
          <Fact
            label="From"
            value={user.last_login_addr || (user.last_login_at ? 'Not recorded for that sign-in' : '—')}
            data={Boolean(user.last_login_addr)}
          />
          <Fact
            label="Member since"
            value={relativeAge(user.created_at)}
            title={formatInstant(user.created_at)}
          />
        </div>
      </Panel>

      <Panel
        title="Password"
        bodyClassName="px-5 py-4 text-[13px] leading-relaxed text-muted"
        actions={
          can.changePassword ? (
            <Button size="sm" onClick={onChangePassword}>
              <KeyRound aria-hidden="true" className="size-3.5" />
              Change password
            </Button>
          ) : null
        }
      >
        {can.changePassword
          ? 'Changing it needs the current one, so a session left open somewhere cannot lock you out. You can revoke your issued kubeconfigs with it.'
          : federated
            ? 'Your password is held by your identity provider — change it there.'
            : 'This account signs in with a token, not a password.'}
      </Panel>

      {mfa !== undefined && secondFactorApplies(user) ? (
        <SecondFactorPanel
          status={mfa}
          onAdd={onAddFactor}
          onRemove={onRemoveFactor}
          onNewRecoveryCodes={onNewRecoveryCodes}
        />
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2">
        <Door
          to={ACCESS_HOME}
          icon={<Timer aria-hidden="true" className="size-4" />}
          title="My access"
          body="The clusters you can reach, and elevated access you have asked for."
        />
        <Door
          to={CREDENTIALS_HOME}
          icon={<FileKey aria-hidden="true" className="size-4" />}
          title="My credentials"
          body="The kubeconfigs issued to you, and revoking one you have lost."
        />
      </div>
    </div>
  )
}

/**
 * The account's second factor: what it holds, adding one, removing one, and
 * new recovery codes. Absent, not empty, for an account whose factor is its
 * identity provider's or that has no password at all.
 */
function SecondFactorPanel({
  status,
  onAdd,
  onRemove,
  onNewRecoveryCodes,
}: {
  status: MFAStatus | null
  onAdd: () => void
  onRemove: (factor: MFAFactor) => void
  onNewRecoveryCodes: () => void
}) {
  return (
    <Panel
      title="Two-factor authentication"
      description={
        status?.required
          ? 'Your organisation requires a second factor for this account.'
          : 'A code from an authenticator app after your password, so a password alone does not open your account.'
      }
      actions={
        status ? (
          <Button size="sm" onClick={onAdd}>
            <ShieldCheck aria-hidden="true" className="size-3.5" />
            {status.enrolled ? 'Add a factor' : 'Set up'}
          </Button>
        ) : null
      }
      bodyClassName="flex flex-col gap-3 px-5 py-4 text-[13px] leading-relaxed text-muted"
    >
      {!status ? (
        <p>Loading…</p>
      ) : !status.enrolled ? (
        <p>Sign-in asks for your password only.</p>
      ) : (
        <>
          <ul className="flex flex-col divide-y divide-line-soft">
            {status.factors.map((factor) => (
              <li key={factor.id} className="flex min-w-0 items-center gap-3 py-2 first:pt-0">
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13.5px] text-fg">{factor.name}</span>
                  <span className="block text-[12px] text-muted">
                    Authenticator app · added {relativeAge(factor.created_at)}
                    {factor.last_used_at ? ` · last used ${relativeAge(factor.last_used_at)}` : ''}
                  </span>
                </span>
                <Button size="sm" variant="ghost" onClick={() => onRemove(factor)}>
                  Remove
                </Button>
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-line-soft pt-3">
            <span>
              {status.recovery_codes_remaining === 0
                ? 'No recovery codes left.'
                : `${status.recovery_codes_remaining} recovery ${status.recovery_codes_remaining === 1 ? 'code' : 'codes'} left.`}
            </span>
            <Button size="sm" onClick={onNewRecoveryCodes}>
              New recovery codes
            </Button>
          </div>
        </>
      )}
    </Panel>
  )
}

function Door({ to, icon, title, body }: { to: string; icon: React.ReactNode; title: string; body: string }) {
  return (
    <Link
      to={to}
      className="group flex min-w-0 items-center gap-3 rounded-card border border-line bg-surface p-4 transition-colors hover:border-accent-line"
    >
      <span className="grid size-9 shrink-0 place-items-center rounded-control border border-line bg-raised text-muted group-hover:text-fg">
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[14px] font-semibold text-fg">{title}</span>
        <span className="mt-0.5 block text-[12.5px] leading-snug text-muted">{body}</span>
      </span>
      <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-faint group-hover:text-fg" />
    </Link>
  )
}

/** One labelled fact in the account card. */
function Fact({
  label,
  value,
  data,
  title,
}: {
  label: string
  value: string
  data?: boolean
  title?: string
}) {
  return (
    <div className="min-w-0">
      <p className="label text-faint">{label}</p>
      <p
        title={title}
        className={`mt-1 min-w-0 truncate text-[13px] text-fg ${data ? 'font-data text-[12.5px]' : ''}`}
      >
        {value}
      </p>
    </div>
  )
}
