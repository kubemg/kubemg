import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Plus, Search, Trash2 } from 'lucide-react'
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
  Drawer,
  Field,
  Notice,
  Select,
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
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyRow, setBusyRow] = useState<number | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
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
        (u) =>
          u.username.toLowerCase().includes(needle) ||
          (u.email ?? '').toLowerCase().includes(needle),
      )
    : users

  return (
    <AppShell
      title="Users"
      actions={
        <Button variant="primary" onClick={() => setDrawerOpen(true)}>
          <Plus aria-hidden="true" className="size-3.5" />
          Add user
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        <div className="panel min-w-0 overflow-hidden">
          <div className="flex h-10 items-center gap-3 border-b border-line px-3">
            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-faint"
              />
              <input
                type="search"
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Filter by name or email"
                aria-label="Filter users"
                className="w-56 rounded-[5px] border border-line bg-surface py-1 pr-2 pl-7 text-[12px] text-fg transition-colors placeholder:text-faint hover:border-faint focus:border-primary focus:outline-none"
              />
            </div>
            <span className="ml-auto text-[12px] text-muted">
              {visible.length === users.length
                ? `${users.length} ${users.length === 1 ? 'account' : 'accounts'}`
                : `${visible.length} of ${users.length}`}
            </span>
          </div>

          <div className="overflow-x-auto">
            {/* Narrow screens drop email and last sign-in, the two columns that
                read fine from the detail of a single row instead. */}
            <table className="w-full table-fixed border-collapse text-[13px]">
              <thead>
                <tr className="border-b border-line">
                  <th className="label w-[34%] px-3 py-2 text-left md:w-[18%]">
                    Username
                  </th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[24%]">
                    Email
                  </th>
                  <th className="label w-[28%] px-3 py-2 text-left md:w-[16%]">
                    System role
                  </th>
                  <th className="label w-[26%] px-3 py-2 text-left md:w-[14%]">
                    Status
                  </th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[16%]">
                    Last sign-in
                  </th>
                  <th className="label w-[12%] px-3 py-2 text-right md:w-[12%]">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {visible.map((row) => {
                  const isSelf = row.id === currentUser?.id
                  const busy = busyRow === row.id
                  return (
                    <tr
                      key={row.id}
                      className="border-b border-line-soft transition-colors last:border-0 hover:bg-raised"
                    >
                      <td className="truncate px-3 py-2 font-mono text-fg">
                        {row.username}
                        {isSelf ? <span className="ml-1.5 rounded-full bg-primary-soft px-1.5 py-px text-[11px] text-primary">you</span> : null}
                      </td>
                      <td
                        className="hidden truncate px-3 py-2 text-[12px] text-muted md:table-cell"
                        title={row.email}
                      >
                        {row.email || '—'}
                      </td>
                      <td className="px-3 py-2">
                        <Select
                          aria-label={`System role for ${row.username}`}
                          value={row.system_role}
                          // Changing your own role would sign you out of this
                          // very screen, so the API refuses it.
                          disabled={isSelf || busy}
                          onChange={(event) =>
                            run(row.id, `Could not update ${row.username}.`, () =>
                              updateUser(row.id, {
                                system_role: event.target.value as SystemRole,
                              }),
                            )
                          }
                          className="py-1 text-[12px]"
                        >
                          {SYSTEM_ROLES.map((role) => (
                            <option key={role} value={role}>
                              {ROLE_LABEL[role]}
                            </option>
                          ))}
                        </Select>
                      </td>
                      <td className="px-3 py-2">
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
                          className="rounded-sm transition-opacity hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-50"
                        >
                          <ActivityTag active={row.is_active} />
                        </button>
                      </td>
                      <td className="hidden truncate px-3 py-2 text-[12px] text-muted md:table-cell">
                        {relativeAge(row.last_login_at)}
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex items-center justify-end">
                          <button
                            type="button"
                            onClick={() => remove(row)}
                            disabled={isSelf || busy}
                            className="rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-danger/40 hover:text-danger disabled:cursor-not-allowed disabled:opacity-50"
                            title={
                              isSelf ? 'You cannot delete your own account' : `Delete ${row.username}`
                            }
                          >
                            <Trash2 aria-hidden="true" className="size-3.5" />
                            <span className="sr-only">Delete {row.username}</span>
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          {loading && users.length === 0 ? (
            <p className="px-3 py-6 text-center text-[12px] text-muted">Loading…</p>
          ) : null}

          {users.length > 0 && visible.length === 0 ? (
            <p className="px-3 py-10 text-center text-[12px] text-muted">
              No account matches “{filter}”.
            </p>
          ) : null}
        </div>

        <p className="text-[11px] text-muted">
          Disabling an account keeps its cluster grants and group memberships, and takes effect on
          the next request rather than at token expiry.
        </p>
      </div>

      {drawerOpen ? (
        <AddUserDrawer
          onClose={() => setDrawerOpen(false)}
          onCreated={async () => {
            setDrawerOpen(false)
            await load()
          }}
        />
      ) : null}
    </AppShell>
  )
}

function AddUserDrawer({
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
    <Drawer
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
    </Drawer>
  )
}
