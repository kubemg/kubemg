import { useCallback, useEffect, useState } from 'react'
import { FileKey, KeyRound, ShieldOff } from 'lucide-react'
import {
  errorMessage,
  fetchIssuedKubeconfigs,
  revokeAllIssuedKubeconfigs,
  revokeIssuedKubeconfig,
} from '../api/client'
import type { IssuedKubeconfig, KubeconfigRevokeAllResult } from '../api/types'
import { AppShell } from '../components/AppShell'
import { PasswordSheet } from '../components/PasswordSheet'
import {
  Button,
  EmptyState,
  Notice,
  Pill,
  Row,
  SearchInput,
  Segmented,
  Table,
  Td,
  Th,
} from '../components/primitives'
import type { Tone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'

/**
 * IssuedCredentials is the register: every kubeconfig this console has handed
 * out, and the button that takes one back.
 *
 * It is one page behind two doors. `/admin/credentials` is the platform team's
 * — the whole fleet's credentials, beside Machine accounts, because what it
 * manages is the same kind of object. `/me/credentials` is the same page read by
 * whoever holds them, and it is deliberately not admin-only: revoking a file you
 * know you lost must not require finding an administrator first. The server
 * narrows a non-admin to their own rows and the query parameter cannot widen
 * that, so this component does not decide who sees what — it only says which
 * reading it is showing.
 *
 * The load-bearing honesty is the Revoke column. A credential is only genuinely
 * withdrawable when KubeMG minted it, which is agent mode; a direct-mode file
 * carries a token the cluster itself issued through TokenRequest, and it stays
 * valid until it expires whatever this page does. Those rows say so instead of
 * offering a button that would report success and change nothing.
 */
type Reading = 'mine' | 'fleet'

const FILTERS = [
  { value: 'active', label: 'Live' },
  { value: 'all', label: 'All' },
] as const

type StatusFilter = (typeof FILTERS)[number]['value']

export function IssuedCredentials({ reading }: { reading: Reading }) {
  const { user } = useAuth()
  const [rows, setRows] = useState<IssuedKubeconfig[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [busyRow, setBusyRow] = useState<number | null>(null)
  const [status, setStatus] = useState<StatusFilter>('active')
  const [filter, setFilter] = useState('')
  const [blanket, setBlanket] = useState<KubeconfigRevokeAllResult | null>(null)
  const [changingPassword, setChangingPassword] = useState(false)

  // The fleet reading asks for everything and lets the server decide; the
  // operator's own asks for their id explicitly, so the page reads the same
  // whether or not the person holding it happens to be an administrator.
  const mine = reading === 'mine'
  const load = useCallback(async () => {
    try {
      const next = await fetchIssuedKubeconfigs({
        userId: mine ? user?.id : undefined,
        activeOnly: status === 'active',
      })
      setRows(next)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the credential register.'))
    } finally {
      setLoading(false)
    }
  }, [mine, status, user?.id])

  useEffect(() => {
    void load()
  }, [load])

  async function revokeOne(row: IssuedKubeconfig) {
    setBusyRow(row.id)
    setRowError(null)
    setBlanket(null)
    try {
      await revokeIssuedKubeconfig(row.id)
      await load()
    } catch (err) {
      setRowError(errorMessage(err, 'Could not revoke that credential.'))
    } finally {
      setBusyRow(null)
    }
  }

  async function revokeEverything() {
    const whose = mine ? 'your own' : 'every account’s'
    const confirmed = window.confirm(
      `Revoke ${whose} live kubeconfigs? Anything using one starts failing at its next call. ` +
        'Credentials for clusters registered for direct API access cannot be withdrawn from ' +
        'here and will be named in the result.',
    )
    if (!confirmed) return
    setRowError(null)
    try {
      const result = await revokeAllIssuedKubeconfigs(mine ? user?.id : undefined)
      setBlanket(result)
      await load()
    } catch (err) {
      setRowError(errorMessage(err, 'Could not revoke the credentials.'))
    }
  }

  const needle = filter.trim().toLowerCase()
  const visible = needle
    ? rows.filter(
        (row) =>
          row.username.toLowerCase().includes(needle) ||
          row.cluster_name.toLowerCase().includes(needle),
      )
    : rows

  return (
    <AppShell
      title={mine ? 'My credentials' : 'Issued credentials'}
      actions={
        <>
          {/* Only on the operator's own reading, and only for an account whose
              password actually lives here: a federated account's is held by its
              provider and a machine account has none at all, so the button would
              open a form that can only refuse. */}
          {mine && user?.auth_source === 'local' && user?.account_type !== 'service' ? (
            <Button onClick={() => setChangingPassword(true)}>
              <KeyRound aria-hidden="true" className="size-4" />
              Change password
            </Button>
          ) : null}
          <Button variant="danger" onClick={revokeEverything} disabled={loading}>
            <ShieldOff aria-hidden="true" className="size-4" />
            {mine ? 'Revoke all of mine' : 'Revoke everything live'}
          </Button>
        </>
      }
    >
      {changingPassword ? (
        <PasswordSheet
          onClose={() => setChangingPassword(false)}
          onRevoked={(result) => {
            // A rotation that took the kubeconfigs with it changed this very
            // table, so the register behind the sheet is re-read rather than
            // left showing rows that are no longer live.
            if (result.credentials) setBlanket(result.credentials)
            void load()
          }}
        />
      ) : null}

      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        {/* Said once, at the top, because it is what makes this register worth
            reading rather than a log: the two credentials it holds stop by
            completely different means. */}
        <Notice tone="info">
          A kubeconfig for a cluster reached through an agent carries a KubeMG token, so revoking it
          here stops the next call. A kubeconfig for a cluster registered for direct API access
          carries a token that cluster minted, which KubeMG cannot withdraw — those rows say so, and
          the only lever is deleting the account’s <code>kubemg-…</code> ServiceAccount on the
          cluster.
        </Notice>

        {blanket ? (
          <Notice tone={blanket.still_valid > 0 ? 'warn' : 'ok'}>
            {blanket.revoked} {blanket.revoked === 1 ? 'credential' : 'credentials'} stopped.
            {blanket.still_valid > 0 ? (
              <>
                {' '}
                {blanket.still_valid} still {blanket.still_valid === 1 ? 'works' : 'work'} on{' '}
                {(blanket.clusters_not_reached ?? []).join(', ')}. {blanket.explanation}
              </>
            ) : null}
          </Notice>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <Segmented
              value={status}
              onChange={setStatus}
              options={[...FILTERS]}
              ariaLabel="Which credentials to show"
            />
            <SearchInput
              value={filter}
              onChange={setFilter}
              label="Filter credentials"
              placeholder="Filter by holder or cluster"
            />
            <span className="ml-auto text-[13px] text-muted">
              {visible.length === rows.length
                ? `${rows.length} ${rows.length === 1 ? 'credential' : 'credentials'}`
                : `${visible.length} of ${rows.length}`}
            </span>
          </div>

          <Table>
            <thead>
              <tr>
                {mine ? null : <Th className="w-[18%]">Holder</Th>}
                <Th className="w-[20%]">Cluster</Th>
                <Th className="hidden md:table-cell md:w-[14%]">Scope</Th>
                <Th className="hidden lg:table-cell lg:w-[12%]">Issued</Th>
                <Th className="hidden lg:table-cell lg:w-[12%]">Last used</Th>
                <Th className="w-[16%]">Status</Th>
                <Th className="w-[14%] text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => (
                <Row key={row.id}>
                  {mine ? null : <Td className="truncate font-mono text-fg">{row.username}</Td>}
                  <Td className="truncate">
                    <span className="text-fg">{row.cluster_name}</span>
                    <span className="ml-1.5 text-[12px] text-muted">{row.connection_mode}</span>
                  </Td>
                  <Td className="hidden font-mono text-[12.5px] text-muted md:table-cell">
                    {row.namespace || 'all'}
                    {row.k8s_role ? ` · ${row.k8s_role}` : ''}
                  </Td>
                  <Td className="hidden text-[12.5px] text-muted lg:table-cell">
                    {relativeAge(row.created_at)}
                  </Td>
                  {/* "never" is the reading that matters here: a credential that
                      was generated and never used is one nobody will miss. */}
                  <Td className="hidden text-[12.5px] text-muted lg:table-cell">
                    {row.last_used_at ? relativeAge(row.last_used_at) : 'never'}
                  </Td>
                  <Td>
                    <Pill tone={statusTone(row.status)} title={expiryTitle(row)}>
                      {row.status}
                    </Pill>
                  </Td>
                  <Td className="text-right">
                    {row.revocable ? (
                      <Button
                        variant="ghost"
                        onClick={() => revokeOne(row)}
                        disabled={busyRow === row.id}
                      >
                        {busyRow === row.id ? 'Revoking…' : 'Revoke'}
                      </Button>
                    ) : (
                      // No button, and the reason on hover rather than a
                      // disabled control that says nothing.
                      <span
                        className="text-[12px] text-muted"
                        title={row.explanation ?? undefined}
                      >
                        {row.status === 'active' ? 'cluster-side only' : '—'}
                      </span>
                    )}
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>

          {!loading && visible.length === 0 ? (
            <EmptyState
              icon={<FileKey aria-hidden="true" className="size-5" />}
              title={rows.length === 0 ? 'No credentials on record' : 'Nothing matches that filter'}
            >
              {rows.length === 0
                ? 'A row appears here the moment somebody generates a kubeconfig. Credentials issued before this register existed are not listed — nothing recorded them.'
                : null}
            </EmptyState>
          ) : null}
        </div>
      </div>
    </AppShell>
  )
}

function statusTone(status: IssuedKubeconfig['status']): Tone {
  switch (status) {
    case 'active':
      return 'ok'
    case 'revoked':
      return 'bad'
    default:
      return 'idle'
  }
}

function expiryTitle(row: IssuedKubeconfig): string {
  if (row.status === 'revoked') {
    const by = row.revoked_by_username ? ` by ${row.revoked_by_username}` : ''
    return `Revoked${by} ${row.revoked_at ? relativeAge(row.revoked_at) : ''}`.trim()
  }
  return `Expires ${relativeAge(row.expires_at)}`
}
