import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Plus, Trash2, UserMinus } from 'lucide-react'
import {
  addGroupMember,
  createGroup,
  deleteGroup,
  errorMessage,
  fetchGroups,
  fetchUsers,
  removeGroupMember,
} from '../api/client'
import type { Group, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, Drawer, Field, Notice, Panel, Select, TextInput } from '../components/primitives'

export function GroupManagement() {
  const [groups, setGroups] = useState<Group[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyGroup, setBusyGroup] = useState<number | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const load = useCallback(async () => {
    try {
      const [nextGroups, nextUsers] = await Promise.all([fetchGroups(), fetchUsers()])
      setGroups(nextGroups)
      setUsers(nextUsers)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load groups.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function run(groupId: number, fallback: string, action: () => Promise<unknown>) {
    setBusyGroup(groupId)
    setRowError(null)
    try {
      await action()
      await load()
    } catch (err) {
      setRowError(errorMessage(err, fallback))
    } finally {
      setBusyGroup(null)
    }
  }

  async function remove(group: Group) {
    const confirmed = window.confirm(
      `Delete ${group.name}? Members lose every cluster grant they only held through this group.`,
    )
    if (!confirmed) return
    await run(group.id, `Could not delete ${group.name}.`, () => deleteGroup(group.id))
  }

  const usersById = new Map(users.map((user) => [user.id, user]))

  return (
    <AppShell
      title="Groups"
      actions={
        <Button variant="primary" onClick={() => setDrawerOpen(true)}>
          <Plus aria-hidden="true" className="size-3.5" />
          Create group
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        {loading && groups.length === 0 ? (
          <p className="text-[12px] text-muted">Loading…</p>
        ) : null}

        {!loading && groups.length === 0 ? (
          <div className="panel px-3 py-10 text-center">
            <p className="text-[13px] text-fg">No groups yet</p>
            <p className="mt-1 text-[12px] text-muted">
              Grant a cluster to a group once instead of to each member in turn.
            </p>
            <Button variant="secondary" className="mt-3" onClick={() => setDrawerOpen(true)}>
              <Plus aria-hidden="true" className="size-3.5" />
              Create group
            </Button>
          </div>
        ) : null}

        <div className="grid items-start gap-3 xl:grid-cols-2">
          {groups.map((group) => (
            <GroupCard
              key={group.id}
              group={group}
              users={users}
              usersById={usersById}
              busy={busyGroup === group.id}
              onAdd={(userId) =>
                run(group.id, `Could not add the member to ${group.name}.`, () =>
                  addGroupMember(group.id, userId),
                )
              }
              onRemove={(userId) =>
                run(group.id, `Could not remove the member from ${group.name}.`, () =>
                  removeGroupMember(group.id, userId),
                )
              }
              onDelete={() => remove(group)}
            />
          ))}
        </div>
      </div>

      {drawerOpen ? (
        <CreateGroupDrawer
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

function GroupCard({
  group,
  users,
  usersById,
  busy,
  onAdd,
  onRemove,
  onDelete,
}: {
  group: Group
  users: User[]
  usersById: Map<number, User>
  busy: boolean
  onAdd: (userId: number) => Promise<void>
  onRemove: (userId: number) => Promise<void>
  onDelete: () => void
}) {
  const [pending, setPending] = useState('')

  const members = group.member_ids
    .map((id) => usersById.get(id))
    .filter((user): user is User => Boolean(user))
  const candidates = users.filter((user) => !group.member_ids.includes(user.id))

  async function add(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!pending) return
    await onAdd(Number(pending))
    setPending('')
  }

  return (
    <Panel
      title={group.name}
      actions={
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-muted">
            {members.length} {members.length === 1 ? 'member' : 'members'}
          </span>
          <button
            type="button"
            onClick={onDelete}
            disabled={busy}
            className="rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-danger/40 hover:text-danger disabled:opacity-50"
            title={`Delete ${group.name}`}
          >
            <Trash2 aria-hidden="true" className="size-3.5" />
            <span className="sr-only">Delete {group.name}</span>
          </button>
        </div>
      }
    >
      {group.description ? (
        <p className="border-b border-line px-3 py-2 text-[12px] text-muted">{group.description}</p>
      ) : null}

      <ul className="flex flex-col">
        {members.map((member) => (
          <li
            key={member.id}
            className="flex items-center gap-2 border-b border-line-soft px-3 py-1.5 last:border-0"
          >
            <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg">
              {member.username}
            </span>
            <span className="label shrink-0">{member.system_role}</span>
            <button
              type="button"
              onClick={() => onRemove(member.id)}
              disabled={busy}
              className="shrink-0 rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-danger/40 hover:text-danger disabled:opacity-50"
              title={`Remove ${member.username} from ${group.name}`}
            >
              <UserMinus aria-hidden="true" className="size-3.5" />
              <span className="sr-only">
                Remove {member.username} from {group.name}
              </span>
            </button>
          </li>
        ))}

        {members.length === 0 ? (
          <li className="px-3 py-3 text-[12px] text-muted">No members yet.</li>
        ) : null}
      </ul>

      <form onSubmit={add} className="flex items-center gap-2 border-t border-line p-2.5">
        <Select
          aria-label={`Add a member to ${group.name}`}
          value={pending}
          disabled={busy || candidates.length === 0}
          onChange={(event) => setPending(event.target.value)}
          className="py-1 text-[12px]"
        >
          <option value="">
            {candidates.length === 0 ? 'Every account is a member' : 'Select a user…'}
          </option>
          {candidates.map((user) => (
            <option key={user.id} value={user.id}>
              {user.username}
            </option>
          ))}
        </Select>
        <Button type="submit" disabled={busy || !pending} className="shrink-0">
          Add
        </Button>
      </form>
    </Panel>
  )
}

function CreateGroupDrawer({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => Promise<void>
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await createGroup(name.trim(), description.trim())
      await onCreated()
    } catch (err) {
      setError(errorMessage(err, 'Could not create the group.'))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title="Create group"
      onClose={onClose}
      onSubmit={handleSubmit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Creating…' : 'Create group'}
          </Button>
        </>
      }
    >
      <Field label="Name" htmlFor="group_name">
        <TextInput
          id="group_name"
          required
          autoFocus
          placeholder="platform"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>

      <Field label="Description" htmlFor="group_description" hint="Optional.">
        <TextInput
          id="group_description"
          placeholder="Platform engineering"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>

      {error ? <Notice tone="error">{error}</Notice> : null}
    </Drawer>
  )
}
