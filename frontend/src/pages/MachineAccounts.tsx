import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Bot, Check, ChevronRight, KeyRound, Plus, Trash2 } from 'lucide-react'
import {
  assignPermission,
  createMachineAccount,
  deleteMachineAccount,
  errorMessage,
  fetchClusters,
  fetchMachineAccounts,
  fetchMachineTokens,
  revokeMachineToken,
  revokePermission,
  setMachineAccountStatus,
} from '../api/client'
import type { Cluster, K8sRole, MachineAccount, MachineToken } from '../api/types'
import { AppShell } from '../components/AppShell'
import { IssueMachineTokenSheet } from '../components/MachineTokenSheet'
import {
  ActivityTag,
  Button,
  Pill,
  Field,
  IconButton,
  Notice,
  OBJECT_MARK,
  OBJECT_NAME,
  Panel,
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
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

const ROLES: K8sRole[] = ['view', 'edit', 'cluster-admin']

/**
 * MachineAccounts is where programmatic access lives: the identity a CI
 * pipeline, a release job or a controller acts as, and the credentials it holds.
 *
 * It is a page of its own rather than a filter on Users because the two
 * affordances have almost nothing in common. A person has a password, a system
 * role and a sign-in history; a machine has none of those and instead has
 * credentials with expiries, last-used stamps and a revoke button. The one thing
 * they share — a cluster grant — is edited here too, because a token issued
 * before any grant exists is a file that answers 403 to everything, and the
 * server refuses to issue one for exactly that reason.
 */
export function MachineAccounts() {
  const confirm = useConfirm()
  const report = useResult()
  const [accounts, setAccounts] = useState<MachineAccount[]>([])
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyRow, setBusyRow] = useState<number | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [issuingFor, setIssuingFor] = useState<MachineAccount | null>(null)
  const [openAccount, setOpenAccount] = useState<MachineAccount | null>(null)
  const [filter, setFilter] = useState('')

  const load = useCallback(async () => {
    try {
      const [nextAccounts, nextClusters] = await Promise.all([
        fetchMachineAccounts(),
        fetchClusters(),
      ])
      setAccounts(nextAccounts)
      setClusters(nextClusters)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load machine accounts.'))
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
    id: number,
    fallback: string,
    action: () => Promise<unknown>,
    landed?: { title: string; body?: string },
  ) {
    setBusyRow(id)
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
      setBusyRow(null)
    }
  }

  async function remove(target: MachineAccount) {
    const confirmed = await confirm({
      eyebrow: 'Machine account',
      title: `Delete ${target.username}?`,
      body: 'Every credential it holds stops working immediately, and anything using one starts failing at its next call. Its audit history stays.',
      confirmLabel: 'Delete',
    })
    if (!confirmed) return
    await run(
      target.id,
      `Could not delete ${target.username}.`,
      () => deleteMachineAccount(target.id),
      {
        title: `Deleted ${target.username}`,
        body: 'Every credential it held stopped working; anything using one starts failing at its next call.',
      },
    )
  }

  const needle = filter.trim().toLowerCase()
  const visible = needle
    ? accounts.filter((entry) => entry.username.toLowerCase().includes(needle))
    : accounts

  return (
    <AppShell
      title="Machine accounts"
      actions={
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <Plus aria-hidden="true" className="size-4" />
          Add machine account
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        {/* Said once, at the top, because it is the thing that makes this
            surface different from every other credential in the console: what
            it hands out outlives the session that handed it out. */}
        <Notice tone="info">
          A machine account acts under its own name inside the cluster, so the cluster’s own RBAC
          decides what it may do and every call it makes is in the audit trail. Its credential is
          stored here rather than signed, which means revoking one stops the next call — you do not
          have to wait for it to expire.
        </Notice>

        <SetupPath accounts={accounts} />

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <SearchInput
              value={filter}
              onChange={setFilter}
              label="Filter machine accounts"
              placeholder="Filter by name"
            />
            <span className="ml-auto text-[13px] text-muted">
              {visible.length === accounts.length
                ? `${accounts.length} ${accounts.length === 1 ? 'account' : 'accounts'}`
                : `${visible.length} of ${accounts.length}`}
            </span>
          </div>

          <Table>
            <thead>
              <tr>
                <Th className="w-[34%] md:w-[22%]">Name</Th>
                <Th className="hidden md:table-cell md:w-[26%]">Access</Th>
                <Th className="w-[22%] md:w-[14%]">Credentials</Th>
                <Th className="hidden lg:table-cell lg:w-[14%]">Last used</Th>
                <Th className="w-[22%] md:w-[12%]">Status</Th>
                <Th className="w-[22%] md:w-[12%] text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => {
                const busy = busyRow === row.id
                return (
                  <Row key={row.id}>
                    <Td>
                      <span className={`flex ${OBJECT_MARK}`}>
                        <button
                          type="button"
                          className={OBJECT_NAME}
                          onClick={() => setOpenAccount(row)}
                        >
                          {row.username}
                        </button>
                      </span>
                    </Td>
                    <Td className="hidden md:table-cell">
                      {row.access.length === 0 ? (
                        <span className="text-[12.5px] text-warn">no cluster access</span>
                      ) : (
                        <span className="flex flex-wrap gap-1">
                          {row.access.map((grant) => (
                            <Pill key={grant.cluster_id} tone="idle" dot={false}>
                              {grant.cluster_name} · {grant.k8s_role}
                            </Pill>
                          ))}
                        </span>
                      )}
                    </Td>
                    <Td className="font-mono text-[12.5px] text-muted">
                      {row.token_count === 0
                        ? '—'
                        : `${row.active_tokens} live / ${row.token_count}`}
                    </Td>
                    <Td className="hidden text-[12.5px] text-muted lg:table-cell">
                      {row.last_used_at ? relativeAge(row.last_used_at) : 'never'}
                    </Td>
                    <Td>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          run(
                            row.id,
                            `Could not update ${row.username}.`,
                            () => setMachineAccountStatus(row.id, !row.is_active),
                            {
                              title: row.is_active
                                ? `Disabled ${row.username}`
                                : `Activated ${row.username}`,
                              body: row.is_active
                                ? 'Its credentials stop being accepted from now.'
                                : undefined,
                            },
                          )
                        }
                        title={
                          row.is_active
                            ? `Disable ${row.username} and every credential it holds`
                            : `Activate ${row.username}`
                        }
                        className="rounded-chip transition-opacity hover:opacity-75 disabled:cursor-not-allowed disabled:opacity-50"
                      >
                        <ActivityTag active={row.is_active} />
                      </button>
                    </Td>
                    <Td className="text-right">
                      <span className="flex justify-end gap-1">
                        <IconButton
                          label={`Issue a credential for ${row.username}`}
                          onClick={() => setIssuingFor(row)}
                          disabled={busy}
                        >
                          <KeyRound aria-hidden="true" className="size-4" />
                        </IconButton>
                        <IconButton
                          label={`Delete ${row.username}`}
                          tone="danger"
                          onClick={() => remove(row)}
                          disabled={busy}
                        >
                          <Trash2 aria-hidden="true" className="size-4" />
                        </IconButton>
                      </span>
                    </Td>
                  </Row>
                )
              })}
            </tbody>
          </Table>

          {!loading && visible.length === 0 ? (
            <p className="flex flex-col items-center gap-2 border-t border-line-soft px-4 py-10 text-center text-[13px] text-muted">
              <Bot aria-hidden="true" className="size-5 text-faint" />
              {accounts.length === 0
                ? 'No machine accounts yet. Add one for the pipeline that needs a kubeconfig — you grant it a cluster next, and issue its credential after that.'
                : 'Nothing matches that filter.'}
            </p>
          ) : null}
        </div>
      </div>

      {createOpen ? (
        <CreateMachineAccountSheet
          onClose={() => setCreateOpen(false)}
          /*
           * A new account is opened rather than merely listed. On its own it can
           * do nothing at all — the credential it exists for is refused until it
           * holds a cluster grant — so the sheet carrying that grant form is the
           * next step, not a place the operator has to go looking for.
           */
          onCreated={async (created) => {
            setCreateOpen(false)
            await load()
            setOpenAccount(created)
          }}
        />
      ) : null}

      {openAccount ? (
        <MachineAccountSheet
          account={accounts.find((entry) => entry.id === openAccount.id) ?? openAccount}
          clusters={clusters}
          onClose={() => setOpenAccount(null)}
          onChanged={load}
          onIssue={(account) => {
            setOpenAccount(null)
            setIssuingFor(account)
          }}
        />
      ) : null}

      {issuingFor ? (
        <IssueMachineTokenSheet
          account={issuingFor}
          clusters={clusters}
          onClose={() => setIssuingFor(null)}
          onIssued={load}
        />
      ) : null}
    </AppShell>
  )
}

/**
 * SetupPath is the order the three surfaces on this page have to be used in.
 *
 * Every piece was already here — a create button, a grant form inside the
 * account sheet, an issue button on the row — and none of them said that a
 * credential is refused until the grant exists, or that the grant lives one
 * click inside the account's own name. The steps are drawn from the fleet
 * rather than from a counter, so the strip reads as where this installation
 * actually is rather than as a tutorial that never finishes.
 */
export function SetupPath({ accounts }: { accounts: MachineAccount[] }) {
  const steps = [
    {
      label: 'Add the account',
      body: 'The name the cluster sees as the caller. It never signs in and holds no password.',
      done: accounts.length > 0,
    },
    {
      label: 'Grant it a cluster',
      body: 'Open its name, then pick a cluster, a role and the namespaces. Until this exists a credential would authenticate and then be refused.',
      done: accounts.some((account) => account.access.length > 0),
    },
    {
      label: 'Issue a credential',
      body: 'The key button on the row. It hands back a kubeconfig once — paste it into whatever holds it, and revoke it here when that changes.',
      done: accounts.some((account) => account.active_tokens > 0),
    },
  ]

  return (
    <Panel title="What a machine account needs, in order" eyebrow="Path">
      <ol className="flex flex-col md:flex-row">
        {steps.map((step, index) => (
          <li
            key={step.label}
            className="relative flex min-w-0 flex-1 flex-col gap-1 border-b border-line-soft px-4 py-3 last:border-b-0 md:border-r md:border-b-0 md:last:border-r-0"
          >
            <span className="label flex items-center gap-1.5">
              {step.done ? (
                <Check aria-hidden="true" className="size-3.5 text-ok" />
              ) : (
                <span className="font-mono text-faint">{index + 1}</span>
              )}
              {step.label}
            </span>
            <span className="text-[12.5px] leading-relaxed text-muted">{step.body}</span>
            {index < steps.length - 1 ? (
              <ChevronRight
                aria-hidden="true"
                className="absolute top-1/2 right-0 hidden size-4 -translate-y-1/2 translate-x-1/2 rounded-full bg-surface text-faint md:block"
              />
            ) : null}
          </li>
        ))}
      </ol>
    </Panel>
  )
}

function CreateMachineAccountSheet({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (created: MachineAccount) => Promise<void>
}) {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const created = await createMachineAccount(username.trim().toLowerCase(), email.trim())
      await onCreated(created)
    } catch (err) {
      setError(errorMessage(err, 'Could not create the machine account.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Programmatic access"
      title="Add a machine account"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy || username.trim() === ''}>
            {busy ? 'Creating…' : 'Create'}
          </Button>
        </>
      }
    >
      <Field
        label="Name"
        htmlFor="machine-username"
        hint="This is the name the cluster sees as the caller, so keep it recognisable in a RoleBinding and in the audit trail: lowercase letters, digits, dots, dashes and underscores."
      >
        <TextInput
          id="machine-username"
          className="font-mono"
          placeholder="jenkins-release"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>

      <Field
        label="Owner"
        htmlFor="machine-email"
        hint="Who to ask about a credential nobody recognises. Optional, and the one thing that makes an abandoned token actionable rather than merely visible."
      >
        <TextInput
          id="machine-email"
          type="email"
          placeholder="platform@example.com"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
        />
      </Field>

      <Notice tone="info">
        A machine account never signs in and holds no password. It can never be an administrator:
        what it may do is decided entirely by the cluster grant you give it next. Creating it opens
        that grant, and the credential comes after it.
      </Notice>

      {error ? <Notice tone="error">{error}</Notice> : null}
    </Sheet>
  )
}

/** MachineAccountSheet is the account's own surface: the grants that decide what
    it may reach, and the credentials it currently holds. */
function MachineAccountSheet({
  account,
  clusters,
  onClose,
  onChanged,
  onIssue,
}: {
  account: MachineAccount
  clusters: Cluster[]
  onClose: () => void
  onChanged: () => Promise<void>
  onIssue: (account: MachineAccount) => void
}) {
  const confirm = useConfirm()
  const report = useResult()
  const [tokens, setTokens] = useState<MachineToken[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [clusterId, setClusterId] = useState('')
  const [role, setRole] = useState<K8sRole>('view')
  const [namespaces, setNamespaces] = useState('')

  const loadTokens = useCallback(async () => {
    try {
      setTokens(await fetchMachineTokens(account.id))
    } catch (err) {
      setError(errorMessage(err, 'Could not load credentials.'))
    }
  }, [account.id])

  useEffect(() => {
    void loadTokens()
  }, [loadTokens])

  async function grant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (clusterId === '') return
    setBusy(true)
    setError(null)
    try {
      await assignPermission({
        subject_type: 'user',
        subject_id: account.id,
        cluster_id: Number(clusterId),
        k8s_role: role,
        namespaces: namespaces
          .split(',')
          .map((entry) => entry.trim())
          .filter(Boolean),
      })
      setNamespaces('')
      await onChanged()
    } catch (err) {
      setError(errorMessage(err, 'Could not grant access.'))
    } finally {
      setBusy(false)
    }
  }

  async function revokeGrant(id: number) {
    setBusy(true)
    setError(null)
    try {
      await revokePermission('user', account.id, id)
      await onChanged()
    } catch (err) {
      setError(errorMessage(err, 'Could not revoke access.'))
    } finally {
      setBusy(false)
    }
  }

  async function revoke(token: MachineToken) {
    const confirmed = await confirm({
      eyebrow: 'Machine credential',
      title: `Revoke “${token.name}” (${token.hint}…)?`,
      body: 'Whatever holds it starts failing at its next call. The row stays, so what it did is still readable.',
      confirmLabel: 'Revoke',
    })
    if (!confirmed) return
    setBusy(true)
    setError(null)
    try {
      await revokeMachineToken(account.id, token.id)
      await loadTokens()
      await onChanged()
      report({
        tone: 'ok',
        title: `Revoked “${token.name}”`,
        body: 'Whatever holds it starts failing at its next call. The row stays, so what it did is still readable.',
      })
    } catch (err) {
      const message = errorMessage(err, 'Could not revoke the credential.')
      setError(message)
      report({ tone: 'error', title: `“${token.name}” was not revoked`, body: message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Machine account"
      width="lg"
      title={<span className="font-mono text-accent">{account.username}</span>}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button type="button" variant="primary" onClick={() => onIssue(account)}>
            Issue a credential
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <section className="flex flex-col gap-3">
        <h3 className="label">Cluster access</h3>
        {account.access.length === 0 ? (
          <p className="text-[13px] text-muted">
            No access yet. A credential issued before a grant exists authenticates and is then
            refused by the cluster, so kubemg will not issue one.
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {account.access.map((entry) => (
              <li
                key={entry.cluster_id}
                className="flex items-center gap-3 rounded-control border border-line-soft px-3 py-2"
              >
                <span className="font-mono text-[13px] text-fg">{entry.cluster_name}</span>
                <Pill tone="idle" dot={false}>{entry.k8s_role}</Pill>
                <span className="truncate text-[12.5px] text-muted">
                  {entry.namespaces.length > 0
                    ? entry.namespaces.join(', ')
                    : 'every namespace the role allows'}
                </span>
                <span className="ml-auto">
                  <IconButton
                    label={`Revoke access to ${entry.cluster_name}`}
                    tone="danger"
                    disabled={busy}
                    onClick={() => revokeGrant(entry.cluster_id)}
                  >
                    <Trash2 aria-hidden="true" className="size-4" />
                  </IconButton>
                </span>
              </li>
            ))}
          </ul>
        )}

        <form className="flex flex-col gap-3 border-t border-line-soft pt-3" onSubmit={grant}>
          <Field label="Grant access to" htmlFor="machine-cluster">
            <Select
              id="machine-cluster"
              value={clusterId}
              onChange={(event) => setClusterId(event.target.value)}
            >
              <option value="">Choose a cluster…</option>
              {clusters.map((cluster) => (
                <option key={cluster.id} value={cluster.id}>
                  {cluster.name}
                  {cluster.connection_mode === 'direct' ? ' (direct mode)' : ''}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Role" htmlFor="machine-role">
            <Select
              id="machine-role"
              value={role}
              onChange={(event) => setRole(event.target.value as K8sRole)}
            >
              {ROLES.map((entry) => (
                <option key={entry} value={entry}>
                  {entry}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Namespaces"
            htmlFor="machine-namespaces"
            hint="Comma-separated. Leave empty for every namespace the role allows — a pipeline that deploys one service should name it."
          >
            <TextInput
              id="machine-namespaces"
              className="font-mono"
              placeholder="payments, payments-staging"
              value={namespaces}
              onChange={(event) => setNamespaces(event.target.value)}
            />
          </Field>
          <div>
            <Button type="submit" disabled={busy || clusterId === ''}>
              Grant
            </Button>
          </div>
        </form>
      </section>

      <section className="flex flex-col gap-3 border-t border-line-soft pt-4">
        <h3 className="label">Credentials</h3>
        {tokens.length === 0 ? (
          <p className="text-[13px] text-muted">
            {account.access.length === 0
              ? 'None issued yet, and none can be until this account holds a cluster grant above.'
              : 'None issued yet. “Issue a credential” hands back a kubeconfig once, for whatever is going to hold it.'}
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {tokens.map((token) => (
              <li
                key={token.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-control border border-line-soft px-3 py-2"
              >
                <span className="text-[13px] text-fg">{token.name}</span>
                <span className="font-mono text-[12px] text-faint">{token.hint}…</span>
                <Pill tone={token.status === 'active' ? 'ok' : 'idle'}>{token.status}</Pill>
                <span className="text-[12.5px] text-muted">
                  {token.cluster_name ?? `cluster ${token.cluster_id}`}
                  {token.namespace ? ` · ${token.namespace}` : ''}
                </span>
                <span className="text-[12.5px] text-muted">
                  {token.expires_at
                    ? `expires ${relativeAge(token.expires_at)}`
                    : 'never expires'}
                </span>
                <span className="text-[12.5px] text-muted">
                  {token.last_used_at ? `used ${relativeAge(token.last_used_at)}` : 'never used'}
                </span>
                {token.status === 'active' ? (
                  <span className="ml-auto">
                    <Button
                      type="button"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => revoke(token)}
                    >
                      Revoke
                    </Button>
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </section>
    </Sheet>
  )
}
