import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { KeyRound } from 'lucide-react'
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
  DetailList,
  EmptyState,
  EnvironmentTag,
  Field,
  Notice,
  Segmented,
  Select,
  Sheet,
  TextInput,
} from '../components/primitives'
import { useClusters } from '../state/clusters-context'

const K8S_ROLES: K8sRole[] = ['cluster-admin', 'edit', 'view']

/* cluster-admin is the only grant that can wreck a cluster, so it is the only
   one that reads as a warning. */
const ROLE_STYLE: Record<string, string> = {
  'cluster-admin': 'bg-danger-soft text-danger',
  edit: 'bg-accent-soft text-accent',
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

  const agentClusters = clusters.filter((cluster) => cluster.connection_mode === 'agent').length
  const directClusters = clusters.length - agentClusters

  return (
    <AppShell title="Permissions">
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {directClusters > 0 ? (
          <Notice tone="warn">
            <strong className="font-semibold">
              {directClusters === clusters.length
                ? 'These grants govern KubeMG, not the cluster.'
                : `${directClusters} of these clusters use direct mode, where a grant governs KubeMG rather than the cluster.`}
            </strong>{' '}
            In direct mode KubeMG issues a token but creates no RoleBinding, so a grant decides what
            someone sees here and what their kubeconfig claims. Agent-based clusters bind these roles
            for real, and the cluster&rsquo;s own RBAC decides.
          </Notice>
        ) : null}

        <div className="flex flex-wrap items-center gap-3">
          <Segmented<SubjectType>
            ariaLabel="Subject kind"
            value={tab}
            onChange={setTab}
            options={[
              { value: 'user', label: 'Users', count: users.length },
              { value: 'group', label: 'Groups', count: groups.length },
            ]}
          />
          <span className="text-[12.5px] text-muted">
            Click a cell to grant, change, or revoke access.
          </span>
        </div>

        {clusters.length === 0 || subjects.length === 0 ? (
          <div className="card">
            <EmptyState
              icon={<KeyRound aria-hidden="true" className="size-5" />}
              title="Nothing to map yet"
            >
              {clusters.length === 0
                ? 'Register a cluster first.'
                : tab === 'user'
                  ? 'Add a user first.'
                  : 'Create a group first.'}
            </EmptyState>
          </div>
        ) : (
          <div className="card min-w-0 overflow-hidden">
            {/* The matrix is the one place a sideways scroll is right: cluster
                columns grow with the fleet. */}
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-[13.5px]">
                <thead>
                  <tr>
                    <th
                      scope="col"
                      className="label sticky left-0 z-2 min-w-[180px] border-b border-line bg-surface px-4 py-3 text-left"
                    >
                      {tab === 'user' ? 'User' : 'Group'}
                    </th>
                    {clusters.map((cluster) => (
                      <th
                        key={cluster.id}
                        scope="col"
                        className="min-w-[140px] border-b border-l border-line-soft bg-surface px-3 py-3 text-left align-bottom"
                      >
                        <span className="block truncate font-mono text-[12.5px] font-normal text-fg">
                          {cluster.name}
                        </span>
                        <span className="mt-1 flex items-center gap-1.5">
                          <EnvironmentTag environment={cluster.environment} />
                          <span className="font-mono text-[10.5px] text-faint">
                            {cluster.connection_mode}
                          </span>
                        </span>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {subjects.map((subject) => (
                    <tr key={subject.id} className="border-t border-line-soft">
                      <th
                        scope="row"
                        className="sticky left-0 z-1 max-w-[240px] bg-surface px-4 py-2.5 text-left font-normal"
                      >
                        <span className="block truncate font-mono text-fg">{subject.name}</span>
                        <span className="block truncate text-[11.5px] text-muted">
                          {subject.detail}
                        </span>
                      </th>
                      {clusters.map((cluster) => {
                        const permission = granted.get(cellKey(subject.id, cluster.id))
                        return (
                          <td key={cluster.id} className="border-l border-line-soft p-1.5">
                            <button
                              type="button"
                              onClick={() => setEditing({ subject, cluster })}
                              title={`Edit ${subject.name}'s access to ${cluster.name}`}
                              className="w-full rounded-control border border-transparent px-2 py-1.5 text-left transition-colors hover:border-accent-line hover:bg-raised"
                            >
                              {permission ? (
                                <>
                                  <span
                                    className={`inline-flex rounded-chip px-1.5 py-px font-mono text-[11px] ${
                                      ROLE_STYLE[permission.k8s_role] ?? ROLE_STYLE.view
                                    }`}
                                  >
                                    {permission.k8s_role}
                                  </span>
                                  <span className="mt-1 block truncate text-[11.5px] text-muted">
                                    {permission.namespaces.length > 0
                                      ? permission.namespaces.join(', ')
                                      : 'all namespaces'}
                                  </span>
                                </>
                              ) : (
                                <span className="text-[13px] text-faint">—</span>
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

        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        <p className="text-[12px] text-muted">
          A group grant reaches every member. Where a user holds both, the more permissive one
          applies.
        </p>
      </div>

      {editing ? (
        <GrantSheet
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

function GrantSheet({
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
    <Sheet
      eyebrow={current ? 'Edit access' : 'Grant access'}
      title={`${subject.name} → ${cluster.name}`}
      onClose={onClose}
      onSubmit={save}
      footer={
        <>
          {current ? (
            <Button type="button" variant="danger" onClick={revoke} disabled={busy} className="mr-auto">
              Revoke
            </Button>
          ) : null}
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : current ? 'Save grant' : 'Grant access'}
          </Button>
        </>
      }
    >
      <DetailList
        columns={2}
        rows={[
          { term: subject.type === 'user' ? 'User' : 'Group', value: subject.name },
          { term: 'Cluster', value: cluster.name },
        ]}
      />

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
        <Notice tone="info">Every member of {subject.name} inherits this grant.</Notice>
      ) : null}

      {error ? <Notice tone="error">{error}</Notice> : null}
    </Sheet>
  )
}
