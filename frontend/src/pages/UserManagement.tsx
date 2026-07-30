import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import {
  createUser,
  deleteUser,
  errorMessage,
  fetchUsers,
  setUserStatus,
  updateUser,
} from '../api/client'
import type { SystemRole, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import {
  ActivityTag,
  Button,
  Chip,
  Field,
  IconButton,
  Notice,
  Row,
  SearchInput,
  Select,
  Sheet,
  Table,
  Td,
  Th,
  TextInput,
} from '../components/primitives'
import { relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'

const SYSTEM_ROLES: SystemRole[] = ['superadmin', 'admin', 'user']

const ROLE_LABEL: Record<SystemRole, string> = {
  superadmin: 'Super admin',
  admin: 'Admin',
  user: 'User',
}

const BLANK = {
  username: '',
  email: '',
  password: '',
  system_role: 'user' as SystemRole,
}

export function UserManagement() {
  const { user: currentUser } = useAuth()
  // Granting the recording-viewer capability is the one edit here that an
  // ordinary admin may not make — otherwise an admin would grant it to itself
  // and the control would be decorative.
  const isSuperAdmin = currentUser?.system_role === 'superadmin'
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyRow, setBusyRow] = useState<number | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [filter, setFilter] = useState('')

  const load = useCallback(async () => {
    try {
      setUsers(await fetchUsers())
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load users.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** run wraps a row action so one failure never leaves the table stale. */
  async function run(id: number, fallback: string, action: () => Promise<unknown>) {
    setBusyRow(id)
    setRowError(null)
    try {
      await action()
      await load()
    } catch (err) {
      setRowError(errorMessage(err, fallback))
    } finally {
      setBusyRow(null)
    }
  }

  async function remove(target: User) {
    const confirmed = window.confirm(
      `Delete ${target.username}? Their cluster grants and group memberships go with them.`,
    )
    if (!confirmed) return
    await run(target.id, `Could not delete ${target.username}.`, () => deleteUser(target.id))
  }

  const needle = filter.trim().toLowerCase()
  const visible = needle
    ? users.filter(
        (entry) =>
          entry.username.toLowerCase().includes(needle) ||
          (entry.email ?? '').toLowerCase().includes(needle),
      )
    : users

  return (
    <AppShell
      title="Users"
      actions={
        <Button variant="primary" onClick={() => setSheetOpen(true)}>
          <Plus aria-hidden="true" className="size-4" />
          Add user
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <SearchInput
              value={filter}
              onChange={setFilter}
              label="Filter users"
              placeholder="Filter by name or email"
            />
            <span className="ml-auto text-[13px] text-muted">
              {visible.length === users.length
                ? `${users.length} ${users.length === 1 ? 'account' : 'accounts'}`
                : `${visible.length} of ${users.length}`}
            </span>
          </div>

          {/* Narrow screens drop email and last sign-in, the two columns that
              read fine from the detail of a single row instead. */}
          <Table>
            <thead>
              <tr>
                <Th className="w-[36%] md:w-[20%]">Username</Th>
                <Th className="hidden md:table-cell md:w-[24%]">Email</Th>
                <Th className="w-[28%] md:w-[16%]">System role</Th>
                <Th className="w-[24%] md:w-[14%]">Status</Th>
                {/* Only the account that may grant the capability is shown the
                    control for it, so the column is not a row of disabled
                    switches for everyone else. */}
                {isSuperAdmin ? (
                  <Th className="hidden lg:table-cell lg:w-[13%]">Recordings</Th>
                ) : null}
                <Th className="hidden md:table-cell md:w-[16%]">Last sign-in</Th>
                <Th align="right" className="w-[12%] md:w-[10%]">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => {
                const isSelf = row.id === currentUser?.id
                const busy = busyRow === row.id
                return (
                  <Row key={row.id}>
                    <Td className="truncate font-mono text-fg">
                      {row.username}
                      {isSelf ? (
                        <span className="ml-2 rounded-chip bg-accent-soft px-1.5 py-px text-[11px] text-accent">
                          you
                        </span>
                      ) : null}
                    </Td>
                    <Td
                      className="hidden truncate text-[12.5px] text-muted md:table-cell"
                      title={row.email}
                    >
                      {row.email || '—'}
                    </Td>
                    <Td>
                      <Select
                        aria-label={`System role for ${row.username}`}
                        size="sm"
                        value={row.system_role}
                        // Changing your own role would sign you out of this very
                        // screen, so the API refuses it.
                        disabled={isSelf || busy}
                        onChange={(event) =>
                          run(row.id, `Could not update ${row.username}.`, () =>
                            updateUser(row.id, {
                              system_role: event.target.value as SystemRole,
                            }),
                          )
                        }
                      >
                        {SYSTEM_ROLES.map((role) => (
                          <option key={role} value={role}>
                            {ROLE_LABEL[role]}
                          </option>
                        ))}
                      </Select>
                    </Td>
                    <Td>
                      <button
                        type="button"
                        disabled={isSelf || busy}
                        onClick={() =>
                          run(row.id, `Could not update ${row.username}.`, () =>
                            setUserStatus(row.id, !row.is_active),
                          )
                        }
                        title={
                          isSelf
                            ? 'You cannot disable your own account'
                            : row.is_active
                              ? `Disable ${row.username}`
                              : `Activate ${row.username}`
                        }
                        className="rounded-chip transition-opacity hover:opacity-75 disabled:cursor-not-allowed disabled:opacity-50"
                      >
                        <ActivityTag active={row.is_active} />
                      </button>
                    </Td>
                    {/* Who may replay somebody else's terminal session. It is
                        separate from the admin role because a recording holds
                        everything that crossed a production shell, and it is only
                        offered for an account that could act on it: a non-admin
                        sees their own sessions either way, so granting it there
                        would suggest it did something. */}
                    {isSuperAdmin ? (
                      <Td className="hidden lg:table-cell">
                        {row.system_role === 'user' ? (
                          <span className="text-[12.5px] text-faint">—</span>
                        ) : row.system_role === 'superadmin' ? (
                          <span
                            className="text-[12.5px] text-muted"
                            title="A super admin holds it implicitly"
                          >
                            implicit
                          </span>
                        ) : (
                          <Chip
                            active={row.can_view_recordings}
                            onClick={() =>
                              run(row.id, `Could not update ${row.username}.`, () =>
                                updateUser(row.id, {
                                  can_view_recordings: !row.can_view_recordings,
                                }),
                              )
                            }
                          >
                            {row.can_view_recordings ? 'may replay' : 'own only'}
                          </Chip>
                        )}
                      </Td>
                    ) : null}
                    <Td className="hidden truncate text-[12.5px] text-muted md:table-cell">
                      {relativeAge(row.last_login_at)}
                    </Td>
                    <Td>
                      <div className="flex items-center justify-end">
                        <IconButton
                          label={
                            isSelf ? 'You cannot delete your own account' : `Delete ${row.username}`
                          }
                          tone="danger"
                          onClick={() => remove(row)}
                          disabled={isSelf || busy}
                        >
                          <Trash2 aria-hidden="true" className="size-3.5" />
                        </IconButton>
                      </div>
                    </Td>
                  </Row>
                )
              })}
            </tbody>
          </Table>

          {loading && users.length === 0 ? (
            <p className="px-4 py-8 text-center text-[13px] text-muted">Loading…</p>
          ) : null}

          {users.length > 0 && visible.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              No account matches “{filter}”.
            </p>
          ) : null}
        </div>

        <p className="text-[12px] text-muted">
          Disabling an account keeps its cluster grants and group memberships, and takes effect on
          the next request rather than at token expiry.
        </p>
      </div>

      {sheetOpen ? (
        <AddUserSheet
          onClose={() => setSheetOpen(false)}
          onCreated={async () => {
            setSheetOpen(false)
            await load()
          }}
        />
      ) : null}
    </AppShell>
  )
}

function AddUserSheet({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => Promise<void>
}) {
  const [form, setForm] = useState(BLANK)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function update<K extends keyof typeof BLANK>(key: K, value: (typeof BLANK)[K]) {
    setForm((current) => ({ ...current, [key]: value }))
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await createUser({
        ...form,
        username: form.username.trim(),
        email: form.email.trim(),
      })
      await onCreated()
    } catch (err) {
      setError(errorMessage(err, 'Could not create the user.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Access"
      title="Add user"
      onClose={onClose}
      onSubmit={handleSubmit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Creating…' : 'Create user'}
          </Button>
        </>
      }
    >
      <Field label="Username" htmlFor="username">
        <TextInput
          id="username"
          required
          autoFocus
          placeholder="jordan"
          value={form.username}
          onChange={(event) => update('username', event.target.value)}
        />
      </Field>

      <Field label="Email" htmlFor="email" hint="Optional. Accounts are keyed by username.">
        <TextInput
          id="email"
          type="email"
          placeholder="jordan@example.com"
          value={form.email}
          onChange={(event) => update('email', event.target.value)}
        />
      </Field>

      <Field
        label="Initial password"
        htmlFor="password"
        hint="At least 8 characters. There is no self-service reset yet, so share it out of band."
      >
        <TextInput
          id="password"
          type="password"
          required
          minLength={8}
          value={form.password}
          onChange={(event) => update('password', event.target.value)}
        />
      </Field>

      <Field
        label="System role"
        htmlFor="system_role"
        hint="Admins manage clusters, users, and permissions. Only a super admin can create another one."
      >
        <Select
          id="system_role"
          value={form.system_role}
          onChange={(event) => update('system_role', event.target.value as SystemRole)}
        >
          {SYSTEM_ROLES.map((role) => (
            <option key={role} value={role}>
              {ROLE_LABEL[role]}
            </option>
          ))}
        </Select>
      </Field>

      {error ? <Notice tone="error">{error}</Notice> : null}
    </Sheet>
  )
}
