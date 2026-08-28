import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Plus, Trash2, UserMinus, UsersRound } from 'lucide-react'
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
import {
  Button,
  EmptyState,
  Field,
  IconButton,
  Notice,
  Panel,
  Select,
  Sheet,
  TextInput,
} from '../components/primitives'
import { relativeAge } from '../lib/time'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

export function GroupManagement() {
  const confirm = useConfirm()
  const report = useResult()
  const [groups, setGroups] = useState<Group[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyGroup, setBusyGroup] = useState<number | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)

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

  /**
   * run wraps a row action so one failure never leaves the table stale, and so
   * that every one of them reports the same way: the row error for somebody
   * still looking at this table, and — when the caller names one — the result
   * strip, which is what reaches somebody the act has already moved elsewhere.
   */
  async function run(
    groupId: number,
    fallback: string,
    action: () => Promise<unknown>,
    landed?: { title: string; body?: string },
  ) {
    setBusyGroup(groupId)
    setRowError(null)
    try {
      await action()
      await load()
      if (landed) {
        report({ tone: 'ok', ...landed, link: { to: '/audit', label: 'See it in the audit trail' } })
      }
    } catch (err) {
      const message = errorMessage(err, fallback)
      setRowError(message)
      if (landed) {
        // The fallback is already this page's own sentence for the failure
        // ("Could not delete alice."), so the strip says that and adds the
        // server's own words only when they say something more.
        report({
          tone: 'error',
          title: fallback.replace(/\.$/, ''),
          body: message === fallback ? undefined : message,
        })
      }
    } finally {
      setBusyGroup(null)
    }
  }

  async function remove(group: Group) {
    const confirmed = await confirm({
      eyebrow: 'Group',
      title: `Delete ${group.name}?`,
      body: 'Members lose every cluster grant they only held through this group, at their next call. Grants they hold directly are unaffected.',
      confirmLabel: 'Delete',
    })
    if (!confirmed) return
    await run(group.id, `Could not delete ${group.name}.`, () => deleteGroup(group.id), {
      title: `Deleted ${group.name}`,
      body: 'Members keep any grant they hold directly; anything they held only through this group is gone at their next call.',
    })
  }

  const usersById = new Map(users.map((user) => [user.id, user]))

  return (
    <AppShell
      title="Groups"
      actions={
        <Button variant="primary" onClick={() => setSheetOpen(true)}>
          <Plus aria-hidden="true" className="size-4" />
          Create group
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        {loading && groups.length === 0 ? (
          <p className="text-[13px] text-muted">Loading…</p>
        ) : null}

        {!loading && groups.length === 0 ? (
          <div className="card">
            <EmptyState
              icon={<UsersRound aria-hidden="true" className="size-5" />}
              title="No groups yet"
              action={
                <Button variant="primary" onClick={() => setSheetOpen(true)}>
                  <Plus aria-hidden="true" className="size-4" />
                  Create group
                </Button>
              }
            >
              Grant a cluster to a group once instead of to each member in turn. Every member
              inherits the grant.
            </EmptyState>
          </div>
        ) : null}

        {groups.length > 0 ? (
          <div className="grid items-start gap-4 xl:grid-cols-2">
            {groups.map((group) => (
              <GroupCard
                key={group.id}
                group={group}
                users={users}
                usersById={usersById}
                busy={busyGroup === group.id}
                onAdd={(userId) =>
                  run(
                    group.id,
                    `Could not add the member to ${group.name}.`,
                    () => addGroupMember(group.id, userId),
                    { title: `Added a member to ${group.name}` },
                  )
                }
                onRemove={(userId) =>
                  run(
                    group.id,
                    `Could not remove the member from ${group.name}.`,
                    () => removeGroupMember(group.id, userId),
                    { title: `Removed a member from ${group.name}` },
                  )
                }
                onDelete={() => remove(group)}
              />
            ))}
          </div>
        ) : null}
      </div>

      {sheetOpen ? (
        <CreateGroupSheet
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
      eyebrow={`${members.length} ${members.length === 1 ? 'member' : 'members'} · created ${relativeAge(group.created_at)}`}
      title={group.name}
      description={group.description || undefined}
      actions={
        <IconButton label={`Delete ${group.name}`} tone="danger" onClick={onDelete} disabled={busy}>
          <Trash2 aria-hidden="true" className="size-3.5" />
        </IconButton>
      }
    >
      <ul className="flex flex-col">
        {members.map((member) => (
          <li
            key={member.id}
            className="flex items-center gap-2.5 border-b border-line-soft px-4 py-2 last:border-0"
          >
            <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-fg">
              {member.username}
            </span>
            <span className="label shrink-0">{member.system_role}</span>
            <IconButton
              label={`Remove ${member.username} from ${group.name}`}
              onClick={() => onRemove(member.id)}
              disabled={busy}
            >
              <UserMinus aria-hidden="true" className="size-3.5" />
            </IconButton>
          </li>
        ))}

        {members.length === 0 ? (
          <li className="px-4 py-4 text-[13px] text-muted">No members yet.</li>
        ) : null}
      </ul>

      <form
        onSubmit={add}
        className="flex items-center gap-2 border-t border-line-soft bg-raised/40 px-4 py-3"
      >
        <Select
          aria-label={`Add a member to ${group.name}`}
          size="sm"
          value={pending}
          disabled={busy || candidates.length === 0}
          onChange={(event) => setPending(event.target.value)}
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
        <Button type="submit" size="sm" disabled={busy || !pending}>
          Add
        </Button>
      </form>
    </Panel>
  )
}

function CreateGroupSheet({
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
    <Sheet
      eyebrow="Access"
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
    </Sheet>
  )
}
