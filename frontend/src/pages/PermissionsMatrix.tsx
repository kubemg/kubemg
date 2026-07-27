import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import {
  assignPermission,
  errorMessage,
  fetchGroups,
  fetchPermissions,
  fetchUsers,
  revokePermission,
} from '../api/client'
import type { Cluster, Group, K8sRole, Permission, SubjectType, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import {
  Button,
  Drawer,
  EnvironmentTag,
  Field,
  Notice,
  Select,
  TextInput,
} from '../components/primitives'
import { useClusters } from '../state/clusters-context'

const K8S_ROLES: K8sRole[] = ['cluster-admin', 'edit', 'view']

/* cluster-admin is the only grant that can wreck a cluster, so it is the only
   one that reads as a warning. */
const ROLE_STYLE: Record<string, string> = {
  'cluster-admin': 'bg-danger-soft text-danger',
  edit: 'bg-primary-soft text-primary',
  view: 'bg-ok-soft text-ok',
}

/** A row of the matrix: one user or one group. */
interface Subject {
  type: SubjectType
  id: number
  name: string
  detail: string
}

function cellKey(subjectId: number, clusterId: number) {
  return `${subjectId}:${clusterId}`
}

export function PermissionsMatrix() {
  const { clusters } = useClusters()
  const [tab, setTab] = useState<SubjectType>('user')
  const [users, setUsers] = useState<User[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [permissions, setPermissions] = useState<Permission[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<{ subject: Subject; cluster: Cluster } | null>(null)

  const load = useCallback(async () => {
    try {
      const [matrix, nextUsers, nextGroups] = await Promise.all([
        fetchPermissions(),
        fetchUsers(),
        fetchGroups(),
      ])
      setPermissions([...matrix.user_permissions, ...matrix.group_permissions])
      setUsers(nextUsers)
      setGroups(nextGroups)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the permission matrix.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const subjects: Subject[] =
    tab === 'user'
      ? users.map((user) => ({
          type: 'user',
          id: user.id,
          name: user.username,
          detail: user.is_active ? user.system_role : `${user.system_role} · disabled`,
        }))
      : groups.map((group) => ({
          type: 'group',
          id: group.id,
          name: group.name,
          detail: `${group.member_ids.length} ${group.member_ids.length === 1 ? 'member' : 'members'}`,
        }))

  const granted = new Map<string, Permission>()
  for (const permission of permissions) {
    if (permission.subject_type !== tab) continue
    granted.set(cellKey(permission.subject_id, permission.cluster_id), permission)
  }

  return (
    <AppShell title="Permissions">
      <div className="flex min-w-0 flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Notice tone="warn">
          <strong className="font-semibold">These grants govern KubeMG, not the cluster.</strong> A
          grant decides which clusters someone sees here and what role their kubeconfig claims. The
          cluster&rsquo;s own RBAC stays untouched until the bastion ships.
        </Notice>

        <div className="flex items-center gap-1">
          {(['user', 'group'] as SubjectType[]).map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => setTab(value)}
              className={`rounded-[5px] border px-2.5 py-1 text-[12.5px] transition-colors ${
                tab === value
                  ? 'border-primary/40 bg-primary-soft font-medium text-primary'
                  : 'border-line bg-surface text-muted hover:text-fg'
              }`}
            >
              {value === 'user' ? 'Users' : 'Groups'}
            </button>
          ))}
          <span className="ml-auto text-[12px] text-muted">
            Click a cell to grant, change, or revoke access.
          </span>
        </div>

        {clusters.length === 0 || subjects.length === 0 ? (
          <div className="panel px-3 py-10 text-center">
            <p className="text-[13px] text-fg">Nothing to map yet</p>
            <p className="mt-1 text-[12px] text-muted">
              {clusters.length === 0
                ? 'Register a cluster first.'
                : tab === 'user'
                  ? 'Add a user first.'
                  : 'Create a group first.'}
            </p>
          </div>
        ) : (
          <div className="panel min-w-0 overflow-hidden">
            {/* The matrix is the one place a sideways scroll is right: cluster
                columns grow with the fleet. */}
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-[13px]">
                <thead>
                  <tr className="border-b border-line">
                    <th className="label sticky left-0 z-10 min-w-[160px] bg-surface px-3 py-2 text-left">
                      {tab === 'user' ? 'User' : 'Group'}
                    </th>
                    {clusters.map((cluster) => (
                      <th
                        key={cluster.id}
                        className="min-w-[128px] border-l border-line/60 px-3 py-2 text-left"
                      >
                        <span className="block truncate font-mono text-[12px] font-normal text-fg">
                          {cluster.name}
                        </span>
                        <EnvironmentTag environment={cluster.environment} />
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {subjects.map((subject) => (
                    <tr key={subject.id} className="border-b border-line/60 last:border-0">
                      <th className="sticky left-0 z-10 max-w-[220px] bg-surface px-3 py-2 text-left font-normal">
                        <span className="block truncate font-mono text-fg">{subject.name}</span>
                        <span className="block truncate text-[11px] text-muted">
                          {subject.detail}
                        </span>
                      </th>
                      {clusters.map((cluster) => {
                        const permission = granted.get(cellKey(subject.id, cluster.id))
                        return (
                          <td key={cluster.id} className="border-l border-line/60 px-2 py-1.5">
                            <button
                              type="button"
                              onClick={() => setEditing({ subject, cluster })}
                              title={`Edit ${subject.name}'s access to ${cluster.name}`}
                              className="w-full rounded-sm border border-transparent px-1.5 py-1 text-left transition-colors hover:border-primary/50 hover:bg-raised"
                            >
                              {permission ? (
                                <>
                                  <span
                                    className={`inline-flex rounded-[4px] px-1.5 py-px font-mono text-[11px] ${
                                      ROLE_STYLE[permission.k8s_role] ?? ROLE_STYLE.view
                                    }`}
                                  >
                                    {permission.k8s_role}
                                  </span>
                                  <span className="mt-1 block truncate text-[11px] text-muted">
                                    {permission.namespaces.length > 0
                                      ? permission.namespaces.join(', ')
                                      : 'all namespaces'}
                                  </span>
                                </>
                              ) : (
                                <span className="text-[12px] text-muted/50">—</span>
                              )}
                            </button>
                          </td>
                        )
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {loading ? <p className="text-[12px] text-muted">Loading…</p> : null}

        <p className="text-[11.5px] text-muted">
          A group grant reaches every member. Where a user holds both, the more permissive one
          applies.
        </p>
      </div>

      {editing ? (
        <GrantDrawer
          subject={editing.subject}
          cluster={editing.cluster}
          current={granted.get(cellKey(editing.subject.id, editing.cluster.id)) ?? null}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}
    </AppShell>
  )
}

function GrantDrawer({
  subject,
  cluster,
  current,
  onClose,
  onSaved,
}: {
  subject: Subject
  cluster: Cluster
  current: Permission | null
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [role, setRole] = useState<K8sRole>((current?.k8s_role as K8sRole) ?? 'view')
  const [namespaces, setNamespaces] = useState(current?.namespaces.join(', ') ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await assignPermission({
        subject_type: subject.type,
        subject_id: subject.id,
        cluster_id: cluster.id,
        k8s_role: role,
        namespaces: namespaces
          .split(',')
          .map((value) => value.trim())
          .filter(Boolean),
      })
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the permission.'))
      setBusy(false)
    }
  }

  async function revoke() {
    setBusy(true)
    setError(null)
    try {
      await revokePermission(subject.type, subject.id, cluster.id)
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not revoke the permission.'))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title={current ? 'Edit access' : 'Grant access'}
      onClose={onClose}
      onSubmit={save}
      footer={
        <>
          {current ? (
            <Button
              type="button"
              onClick={revoke}
              disabled={busy}
              className="mr-auto border-danger/40 text-danger hover:border-danger"
            >
              Revoke
            </Button>
          ) : null}
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : current ? 'Save' : 'Grant'}
          </Button>
        </>
      }
    >
      <dl className="grid grid-cols-[80px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[12px]">
        <dt className="text-muted">{subject.type === 'user' ? 'User' : 'Group'}</dt>
        <dd className="truncate font-mono text-fg">{subject.name}</dd>
        <dt className="text-muted">Cluster</dt>
        <dd className="truncate font-mono text-fg">{cluster.name}</dd>
      </dl>

      <Field
        label="Kubernetes role"
        htmlFor="k8s_role"
        hint="cluster-admin grants full control of the cluster."
      >
        <Select
          id="k8s_role"
          value={role}
          onChange={(event) => setRole(event.target.value as K8sRole)}
        >
          {K8S_ROLES.map((value) => (
            <option key={value} value={value}>
              {value}
            </option>
          ))}
        </Select>
      </Field>

      <Field
        label="Namespaces"
        htmlFor="namespaces"
        hint="Comma-separated. Leave empty for every namespace the role allows."
      >
        <TextInput
          id="namespaces"
          placeholder="team-a, team-b"
          value={namespaces}
          onChange={(event) => setNamespaces(event.target.value)}
        />
      </Field>

      {subject.type === 'group' ? (
        <p className="text-[11px] text-muted">
          Every member of {subject.name} inherits this grant.
        </p>
      ) : null}

      {error ? <Notice tone="error">{error}</Notice> : null}
    </Drawer>
  )
}
