import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import {
  createSSOMapping,
  deleteSSOMapping,
  errorMessage,
  fetchGroups,
  fetchSSOMappings,
  updateSSOMapping,
} from '../api/client'
import type {
  Environment,
  Group,
  K8sRole,
  SSOGroupMapping,
  SSOGroupMappingInput,
  SSOProvider,
} from '../api/types'
import {
  Button,
  EmptyState,
  Field,
  IconButton,
  Notice,
  Row,
  Select,
  Sheet,
  Table,
  Td,
  Th,
  TextInput,
} from './primitives'

/*
 * What an external group is worth here.
 *
 * A rule matches the group names the directory asserted — case insensitively,
 * with "*" standing for any run of characters — and confers up to three things:
 * membership of a local group, a Kubernetes role across an environment, and the
 * KubeMG administrator tier. It has to confer at least one, because a rule that
 * matches and grants nothing is indistinguishable from a rule whose pattern is
 * wrong, and that is the failure nobody notices.
 *
 * Everything a rule derives is re-evaluated on every sign-in and withdrawn when
 * it no longer applies. Grants an administrator wrote by hand are never touched:
 * the two are told apart in the database, which is what makes leaving a group in
 * the IdP actually take the cluster access away.
 */

const ROLE_LABEL: Record<K8sRole, string> = {
  view: 'View',
  edit: 'Edit',
  'cluster-admin': 'Cluster admin',
}

type Draft = {
  external_group_pattern: string
  target_group_id: string
  target_k8s_role: K8sRole | ''
  environment_filter: Environment | ''
  namespaces: string
  target_system_role: 'user' | 'admin' | ''
}

const EMPTY: Draft = {
  external_group_pattern: '',
  target_group_id: '',
  target_k8s_role: '',
  environment_filter: '',
  namespaces: '',
  target_system_role: '',
}

function draftOf(mapping: SSOGroupMapping): Draft {
  return {
    external_group_pattern: mapping.external_group_pattern,
    target_group_id: mapping.target_group_id ? String(mapping.target_group_id) : '',
    target_k8s_role: mapping.target_k8s_role ?? '',
    environment_filter: mapping.environment_filter ?? '',
    namespaces: (mapping.namespaces ?? []).join(', '),
    target_system_role: mapping.target_system_role ?? '',
  }
}

function toInput(providerId: number, draft: Draft): SSOGroupMappingInput {
  return {
    provider_id: providerId,
    external_group_pattern: draft.external_group_pattern.trim(),
    target_group_id: draft.target_group_id ? Number(draft.target_group_id) : 0,
    target_k8s_role: draft.target_k8s_role,
    environment_filter: draft.environment_filter,
    namespaces: draft.namespaces
      .split(',')
      .map((namespace) => namespace.trim())
      .filter(Boolean),
    target_system_role: draft.target_system_role,
  }
}

export function GroupMappingEditor({
  provider,
  onClose,
}: {
  provider: SSOProvider
  onClose: () => void
}) {
  const [mappings, setMappings] = useState<SSOGroupMapping[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [editing, setEditing] = useState<SSOGroupMapping | 'new' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      const [rules, localGroups] = await Promise.all([fetchSSOMappings(provider.id), fetchGroups()])
      setMappings(rules)
      setGroups(localGroups)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the mapping rules.'))
    } finally {
      setLoading(false)
    }
  }, [provider.id])

  useEffect(() => {
    void load()
  }, [load])

  async function remove(mapping: SSOGroupMapping) {
    try {
      await deleteSSOMapping(mapping.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not delete that rule.'))
    }
  }

  const groupName = (id?: number) => groups.find((group) => group.id === id)?.name

  return (
    <>
      <Sheet
        eyebrow="Group federation"
        title={
          <>
            What <span className="font-mono text-accent">{provider.name}</span> groups are worth
          </>
        }
        onClose={onClose}
        width="xl"
        footer={
          <>
            <Button type="button" variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button type="button" variant="primary" onClick={() => setEditing('new')}>
              <Plus aria-hidden="true" className="size-4" />
              Add rule
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Notice tone="info">
          Rules are applied on every sign-in. Access a rule stops granting is withdrawn the next time
          the person signs in; grants given by hand on the permissions page are never touched.
        </Notice>

        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {!loading && mappings.length === 0 ? (
          <EmptyState title="No rules yet">
            Everyone this provider authenticates gets an account and nothing else. Add a rule to turn
            a directory group into a local group or into cluster access.
          </EmptyState>
        ) : null}

        {mappings.length > 0 ? (
          <Table>
            <thead>
              <tr>
                <Th className="w-[30%]">External group</Th>
                <Th className="w-[20%]">Local group</Th>
                <Th className="w-[32%]">Cluster access</Th>
                <Th className="w-[10%]">Role</Th>
                <Th className="w-[8%]" align="right">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {mappings.map((mapping) => (
                <Row key={mapping.id}>
                  <Td className="truncate font-mono text-[12.5px]" title={mapping.external_group_pattern}>
                    {mapping.external_group_pattern}
                  </Td>
                  <Td className="truncate text-muted">
                    {groupName(mapping.target_group_id) ?? '—'}
                  </Td>
                  <Td className="text-muted">
                    {mapping.target_k8s_role ? (
                      <>
                        <span className="text-fg">{ROLE_LABEL[mapping.target_k8s_role]}</span> on{' '}
                        {mapping.environment_filter
                          ? `${mapping.environment_filter} clusters`
                          : 'every cluster'}
                        {mapping.namespaces.length > 0 ? (
                          <span className="block font-mono text-[11.5px] text-faint">
                            {mapping.namespaces.join(', ')}
                          </span>
                        ) : null}
                      </>
                    ) : (
                      '—'
                    )}
                  </Td>
                  <Td className="text-muted">{mapping.target_system_role ?? '—'}</Td>
                  <Td className="text-right">
                    <span className="inline-flex gap-1">
                      <Button type="button" size="sm" onClick={() => setEditing(mapping)}>
                        Edit
                      </Button>
                      <IconButton
                        label="Delete rule"
                        tone="danger"
                        onClick={() => void remove(mapping)}
                      >
                        <Trash2 aria-hidden="true" className="size-4" />
                      </IconButton>
                    </span>
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>
        ) : null}
      </Sheet>

      {editing ? (
        <MappingSheet
          provider={provider}
          groups={groups}
          mapping={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}
    </>
  )
}

function MappingSheet({
  provider,
  groups,
  mapping,
  onClose,
  onSaved,
}: {
  provider: SSOProvider
  groups: Group[]
  mapping: SSOGroupMapping | null
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [draft, setDraft] = useState<Draft>(mapping ? draftOf(mapping) : EMPTY)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function set<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const input = toInput(provider.id, draft)
      if (mapping) {
        await updateSSOMapping(mapping.id, input)
      } else {
        await createSSOMapping(input)
      }
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save that rule.'))
      setBusy(false)
    }
  }

  const grantsCluster = draft.target_k8s_role !== ''

  return (
    <Sheet
      eyebrow="Mapping rule"
      title={mapping ? 'Edit rule' : 'Add rule'}
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save rule'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <Field
        label="External group"
        htmlFor="mapping-pattern"
        hint={
          provider.protocol === 'ldap'
            ? 'Matched against memberOf, case insensitively. Use * for any run of characters — cn=platform-*,ou=groups,dc=example,dc=com.'
            : 'Matched against the groups claim, case insensitively. Use * for any run of characters — platform-*.'
        }
      >
        <TextInput
          id="mapping-pattern"
          required
          className="font-mono text-[12.5px]"
          placeholder="platform-*"
          value={draft.external_group_pattern}
          onChange={(event) => set('external_group_pattern', event.target.value)}
        />
      </Field>

      <Field
        label="Local group"
        htmlFor="mapping-group"
        hint="Members inherit whatever this group has been granted on the permissions page."
      >
        <Select
          id="mapping-group"
          value={draft.target_group_id}
          onChange={(event) => set('target_group_id', event.target.value)}
        >
          <option value="">None</option>
          {groups.map((group) => (
            <option key={group.id} value={group.id}>
              {group.name}
            </option>
          ))}
        </Select>
      </Field>

      <Field
        label="Kubernetes role"
        htmlFor="mapping-role"
        hint="Granted directly on every cluster the filter below selects — including clusters registered later, which is what a rule per cluster could never keep up with."
      >
        <Select
          id="mapping-role"
          value={draft.target_k8s_role}
          onChange={(event) => set('target_k8s_role', event.target.value as K8sRole | '')}
        >
          <option value="">None</option>
          {(Object.keys(ROLE_LABEL) as K8sRole[]).map((role) => (
            <option key={role} value={role}>
              {ROLE_LABEL[role]}
            </option>
          ))}
        </Select>
      </Field>

      {grantsCluster ? (
        <>
          <Field label="Environment" htmlFor="mapping-env">
            <Select
              id="mapping-env"
              value={draft.environment_filter}
              onChange={(event) => set('environment_filter', event.target.value as Environment | '')}
            >
              <option value="">Every cluster</option>
              <option value="prod">Production only</option>
              <option value="staging">Staging only</option>
              <option value="dev">Development only</option>
            </Select>
          </Field>

          {draft.environment_filter === '' ? (
            <Notice tone="warn">
              This rule reaches every registered cluster, production included, and every cluster
              registered from now on.
            </Notice>
          ) : null}

          <Field
            label="Namespaces"
            htmlFor="mapping-namespaces"
            hint="Comma separated. Leave empty for the whole cluster."
          >
            <TextInput
              id="mapping-namespaces"
              className="font-mono text-[12.5px]"
              placeholder="payments, checkout"
              value={draft.namespaces}
              onChange={(event) => set('namespaces', event.target.value)}
            />
          </Field>
        </>
      ) : null}

      <Field
        label="kubemg role"
        htmlFor="mapping-system-role"
        hint="Leave unset to keep the provider's default. Setting it here makes administrator access revocable by removing someone from the directory group."
      >
        <Select
          id="mapping-system-role"
          value={draft.target_system_role}
          onChange={(event) => set('target_system_role', event.target.value as 'user' | 'admin' | '')}
        >
          <option value="">Unchanged</option>
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </Select>
      </Field>
    </Sheet>
  )
}
