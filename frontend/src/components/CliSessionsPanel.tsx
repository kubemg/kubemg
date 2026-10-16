import { useCallback, useEffect, useState } from 'react'
import { Terminal } from 'lucide-react'
import { errorMessage, fetchCLISessions, revokeCLISession } from '../api/client'
import type { CLISession } from '../api/types'
import { relativeAge } from '../lib/time'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'
import { Age, Button, EmptyState, Notice, Panel, Pill, Row, Table, Td, Th } from './primitives'

/**
 * The signed-in `kubemg` command lines: the credential a laptop holds instead
 * of a kubeconfig file. Same register, same read rule — a person sees their
 * own, an administrator everybody's — and the same honest Revoke: a session is
 * KubeMG's, so ending it stops every kubectl credential it minted at the next
 * call, not when that credential expires.
 */
export function CliSessionsPanel({
  userId,
  showHolder,
  activeOnly,
  refreshKey,
}: {
  /** Narrow to one account. The server narrows a non-admin regardless. */
  userId?: number
  showHolder: boolean
  activeOnly: boolean
  /** Bumped by the page after a blanket revoke, which changes these rows too. */
  refreshKey?: number
}) {
  const confirm = useConfirm()
  const report = useResult()
  const [rows, setRows] = useState<CLISession[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setRows(await fetchCLISessions({ userId, activeOnly }))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the CLI sessions.'))
    } finally {
      setLoading(false)
    }
  }, [userId, activeOnly])

  useEffect(() => {
    void load()
  }, [load, refreshKey])

  async function revoke(row: CLISession) {
    const ok = await confirm({
      eyebrow: row.client_host || 'kubemg CLI',
      title: `End ${row.username}'s CLI session`,
      body: 'Every kubectl credential it handed out stops at its next call, and the command line has to sign in again.',
      confirmLabel: 'End session',
      tone: 'danger',
    })
    if (!ok) return
    setBusy(row.id)
    try {
      await revokeCLISession(row.id)
      await load()
      report({
        tone: 'ok',
        title: 'Session ended',
        body: `${row.client_host || 'That command line'} has to run kubemg login again.`,
        link: { to: '/audit', label: 'See it in the audit trail' },
      })
    } catch (err) {
      report({ tone: 'error', title: 'Nothing was ended', body: errorMessage(err, 'Could not end the session.') })
    } finally {
      setBusy(null)
    }
  }

  return (
    <Panel
      title="CLI sessions"
      description="Command lines signed in with kubemg login. Each hands kubectl fifteen-minute credentials until it expires or is ended here."
      actions={<Terminal aria-hidden="true" className="size-4 text-muted" />}
    >
      {error ? (
        <div className="p-4">
          <Notice tone="error">{error}</Notice>
        </div>
      ) : null}
      {rows.length > 0 ? (
        <Table>
          <thead>
            <tr>
              {showHolder ? <Th className="w-[16%]">Holder</Th> : null}
              <Th className="w-[22%]">Machine</Th>
              <Th className="hidden md:table-cell md:w-[14%]">From</Th>
              <Th className="hidden lg:table-cell lg:w-[12%]">Signed in</Th>
              <Th className="hidden lg:table-cell lg:w-[12%]">Last used</Th>
              <Th className="w-[14%]">Status</Th>
              <Th className="w-[12%] text-right">
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <Row key={row.id}>
                {showHolder ? <Td className="truncate font-data text-fg">{row.username}</Td> : null}
                <Td className="truncate">
                  <span className="font-data text-fg">{row.client_host || 'unnamed'}</span>
                  {row.client_os ? <span className="ml-1.5 text-[12px] text-muted">{row.client_os}</span> : null}
                </Td>
                <Td className="hidden truncate font-data text-[12.5px] text-muted md:table-cell">
                  {row.source_ip || '—'}
                </Td>
                <Td className="hidden text-[12.5px] text-muted lg:table-cell">
                  <Age iso={row.created_at} />
                </Td>
                <Td className="hidden text-[12.5px] text-muted lg:table-cell">
                  <Age iso={row.last_used_at} />
                </Td>
                <Td>
                  <Pill tone={row.status === 'active' ? 'ok' : row.status === 'revoked' ? 'bad' : 'idle'} title={sessionTitle(row)}>
                    {row.status}
                  </Pill>
                </Td>
                <Td className="text-right">
                  {row.status === 'active' ? (
                    <Button variant="ghost" onClick={() => void revoke(row)} disabled={busy === row.id}>
                      {busy === row.id ? 'Ending…' : 'End'}
                    </Button>
                  ) : (
                    <span className="text-[12px] text-muted">—</span>
                  )}
                </Td>
              </Row>
            ))}
          </tbody>
        </Table>
      ) : !loading && !error ? (
        <EmptyState icon={<Terminal aria-hidden="true" className="size-5" />} title="No CLI sessions">
          {activeOnly
            ? 'Nothing is signed in with kubemg login right now.'
            : 'No command line has signed in with kubemg login within the audit window.'}
        </EmptyState>
      ) : null}
    </Panel>
  )
}

function sessionTitle(row: CLISession): string {
  if (row.status === 'revoked') {
    const by = row.revoked_by_username ? ` by ${row.revoked_by_username}` : ''
    return `Ended${by} ${row.revoked_at ? relativeAge(row.revoked_at) : ''}`.trim()
  }
  return `Expires ${relativeAge(row.expires_at)}`
}
