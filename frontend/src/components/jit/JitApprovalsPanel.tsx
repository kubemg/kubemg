import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, Clock, Inbox, Plus, ShieldOff, X } from 'lucide-react'
import {
  approveJitRequest,
  errorMessage,
  fetchJitRequests,
  rejectJitRequest,
  revokeJitRequest,
} from '../../api/client'
import type { Cluster, JitRequest, JitRequestList, JitStatus } from '../../api/types'
import type { Tone } from '../../lib/status'
import { useAuth } from '../../state/auth-context'
import { formatDuration, formatWindow, relativeAge } from '../../lib/time'
import {
  Button,
  EmptyState,
  IconButton,
  Notice,
  Panel,
  Pill,
  Row,
  Table,
  Td,
  Th,
} from '../primitives'
import { JitRequestModal } from './JitRequestModal'

/**
 * The access approvals dashboard: three bands, because there are three questions
 * and they have different urgencies.
 *
 *   1. **Waiting** — the queue. It is first and it is the only band that is a
 *      to-do list: somebody is blocked on it right now, which is why the reason is
 *      shown in full here and truncated everywhere else.
 *   2. **Live now** — elevations in force, each counting down. This is the band an
 *      auditor and an on-call lead actually want: who is holding more privilege
 *      than they normally do, on what, and for how much longer.
 *   3. **History** — decided requests, newest first.
 *
 * The countdown ticks locally against `expires_at` and is *not* re-fetched every
 * second. The server sends the instant the window ends and its own reading of the
 * remaining seconds; drawing that down locally costs nothing, and the list is
 * re-read on a slower timer so a grant that ended elsewhere disappears. A
 * per-second poll of the fleet's grants would be the same information at forty
 * times the cost.
 *
 * A non-admin sees this page too, narrowed by the server to their own requests.
 * That is deliberate: the person whose access is about to expire is the one who
 * most needs to see it expiring, and hiding the page from them would mean the only
 * way to hand privilege back early is to ask an administrator to do it.
 */

/** How often the list is re-read. Slow, because the countdown does not need it —
    see above. Fast enough that a decision taken elsewhere shows up while somebody
    is still looking at the page. */
const REFRESH_MS = 20_000

/** The deck's own tone words — `bad`, not `danger`, since that is how the semantic
    tokens are named. */
const STATUS_TONE: Record<JitStatus, Tone> = {
  pending: 'warn',
  approved: 'ok',
  active: 'ok',
  rejected: 'bad',
  expired: 'idle',
  revoked: 'idle',
}

export function JitApprovalsPanel({
  clusters,
  /** A request id to open the page on, from a link in a chat notification. */
  focusRequest,
}: {
  clusters?: Cluster[]
  focusRequest?: string | null
}) {
  const { user } = useAuth()
  const [list, setList] = useState<JitRequestList | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyRow, setBusyRow] = useState<string | null>(null)
  const [requesting, setRequesting] = useState(false)
  // The local clock the countdowns are drawn against; see the note above.
  const [tick, setTick] = useState(() => Date.now())

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await fetchJitRequests()
      setList(next)
      setError(null)
    } catch (err) {
      // A background refresh that fails must not replace a page somebody is
      // reading with an error — it is the same list, one interval stale.
      if (!quiet) setError(errorMessage(err, 'Could not load the access requests.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = window.setInterval(() => void load(true), REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [load])

  useEffect(() => {
    const timer = window.setInterval(() => setTick(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  // Memoised because the `?? []` fallback would otherwise be a new array on every
  // render, and every band below is derived from it.
  const requests = useMemo(() => list?.requests ?? [], [list])
  const waiting = useMemo(
    () => requests.filter((request) => request.status === 'pending'),
    [requests],
  )
  const live = useMemo(
    () => requests.filter((request) => request.active && remaining(request, tick) > 0),
    [requests, tick],
  )
  const history = useMemo(
    () => requests.filter((request) => request.status !== 'pending' && !request.active),
    [requests],
  )

  async function decide(
    request: JitRequest,
    apply: (id: string, comment?: string) => Promise<JitRequest>,
    prompt?: string,
  ) {
    let comment: string | undefined
    if (prompt) {
      const answer = window.prompt(prompt)
      // A dismissed prompt is a cancelled decision, not an empty comment.
      if (answer === null) return
      comment = answer
    }
    setBusyRow(request.id)
    setError(null)
    try {
      await apply(request.id, comment)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not apply that decision.'))
    } finally {
      setBusyRow(null)
    }
  }

  const canApprove = list?.can_approve ?? false

  return (
    <div className="flex flex-col gap-4">
      {error ? <Notice tone="error">{error}</Notice> : null}
      {list?.scoped_to_me ? (
        // Said explicitly, for the same reason the audit page says it: an empty
        // list otherwise means either "nothing is happening" or "you cannot see
        // it", and those call for different actions.
        <Notice tone="info">
          You are seeing your own access requests. Approving is administrative — ask an
          administrator, or the channel your fleet routes approvals to.
        </Notice>
      ) : null}

      <Panel
        eyebrow="Waiting"
        title={waiting.length === 1 ? '1 request waiting' : `${waiting.length} requests waiting`}
        description={
          canApprove
            ? 'Somebody is blocked on each of these. Read the reason, then decide — you cannot approve your own request, whatever your role.'
            : 'Requests you have submitted that nobody has decided yet.'
        }
        actions={
          <Button variant="primary" onClick={() => setRequesting(true)}>
            <Plus aria-hidden="true" className="size-4" />
            Request access
          </Button>
        }
      >
        {loading && !list ? <p className="px-4 py-6 text-[13px] text-muted">Loading…</p> : null}
        {!loading && waiting.length === 0 ? (
          <EmptyState
            icon={<Inbox aria-hidden="true" className="size-4" />}
            title="Nothing waiting"
            action={
              <Button onClick={() => setRequesting(true)}>
                <Plus aria-hidden="true" className="size-4" />
                Request access
              </Button>
            }
          >
            Elevated access is asked for here and ends by itself. Nothing is pending right now.
          </EmptyState>
        ) : null}

        {waiting.length > 0 ? (
          <ul className="flex flex-col">
            {waiting.map((request) => (
              <li
                key={request.id}
                className="flex flex-col gap-3 border-t border-line-soft px-4 py-3 first:border-t-0"
              >
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="font-mono text-[13.5px] text-fg">
                    {request.requester_username}
                  </span>
                  <span className="text-[13px] text-muted">wants</span>
                  <Pill tone="warn" dot={false}>
                    <span className="font-mono">{request.requested_role}</span>
                  </Pill>
                  <span className="text-[13px] text-muted">on</span>
                  <span className="font-mono text-[13px] text-fg">{request.cluster_name}</span>
                  <span className="text-[13px] text-muted">for</span>
                  <span className="font-mono text-[13px] text-fg">
                    {formatWindow(request.duration_minutes)}
                  </span>
                  <span className="ml-auto text-[12.5px] text-muted">
                    asked {relativeAge(request.created_at)}
                  </span>
                </div>

                {/* The reason, in full. It is the only thing an approver has. */}
                <p className="max-w-3xl rounded-control bg-raised px-3 py-2 text-[13px] leading-relaxed text-fg">
                  {request.reason}
                </p>

                <div className="flex flex-wrap items-center gap-2">
                  <span className="label">
                    {request.namespaces.length > 0
                      ? `namespaces ${request.namespaces.join(', ')}`
                      : 'all namespaces'}
                  </span>
                  <span className="ml-auto flex items-center gap-2">
                    {canApprove && request.requester_id !== user?.id ? (
                      <Button
                        variant="primary"
                        disabled={busyRow === request.id}
                        onClick={() => void decide(request, approveJitRequest)}
                      >
                        <Check aria-hidden="true" className="size-4" />
                        Approve
                      </Button>
                    ) : null}
                    {/* Rejecting your own request is cancelling it, so the label
                        follows who is looking. */}
                    <Button
                      disabled={busyRow === request.id}
                      onClick={() =>
                        void decide(
                          request,
                          rejectJitRequest,
                          request.requester_id === user?.id
                            ? 'Withdraw this request? You can add a note:'
                            : 'Why is this being rejected? The requester sees this:',
                        )
                      }
                    >
                      <X aria-hidden="true" className="size-4" />
                      {request.requester_id === user?.id ? 'Withdraw' : 'Reject'}
                    </Button>
                  </span>
                </div>

                {canApprove && request.requester_id === user?.id ? (
                  <p className="text-[12px] text-muted">
                    This is your own request — an approval needs somebody else.
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </Panel>

      <Panel
        eyebrow="Live now"
        title="Elevations in force"
        description="Access somebody is holding beyond their standing grant, and how much of the window is left. Each ends by itself; handing one back early is one click."
      >
        {live.length === 0 ? (
          <EmptyState
            icon={<Clock aria-hidden="true" className="size-4" />}
            title="No elevated access in force"
          >
            When a request is approved it appears here with its countdown, and disappears when the
            window ends.
          </EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th className="w-[18%]">Who</Th>
                <Th className="w-[20%]">Cluster</Th>
                <Th className="w-[14%]">Role</Th>
                <Th className="hidden w-[18%] md:table-cell">Scope</Th>
                <Th className="w-[15%]">Left</Th>
                <Th align="right" className="w-[15%]">
                  Action
                </Th>
              </tr>
            </thead>
            <tbody>
              {live.map((request) => {
                const left = remaining(request, tick)
                const mine = request.requester_id === user?.id
                return (
                  <Row key={request.id} title={request.reason}>
                    <Td className="truncate font-mono text-[13px]">{request.requester_username}</Td>
                    <Td className="truncate font-mono text-[13px]">{request.cluster_name}</Td>
                    <Td>
                      <Pill tone="ok" dot={false}>
                        <span className="font-mono">{request.requested_role}</span>
                      </Pill>
                    </Td>
                    <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                      {request.namespaces.length > 0 ? request.namespaces.join(', ') : 'all'}
                    </Td>
                    <Td>
                      {/* Amber under five minutes: the point at which somebody
                          mid-task needs to know they are about to lose it. */}
                      <span
                        className={`font-mono text-[13px] tabular-nums ${
                          left < 300 ? 'text-warn' : 'text-fg'
                        }`}
                      >
                        {formatDuration(left)}
                      </span>
                    </Td>
                    <Td className="text-right">
                      {mine || canApprove ? (
                        <IconButton
                          label={mine ? 'Hand this access back' : 'Revoke this access'}
                          onClick={() =>
                            void decide(
                              request,
                              revokeJitRequest,
                              mine
                                ? 'Hand this access back now? You can add a note:'
                                : 'Why is this being revoked? It ends immediately:',
                            )
                          }
                          disabled={busyRow === request.id}
                        >
                          <ShieldOff aria-hidden="true" className="size-4" />
                        </IconButton>
                      ) : null}
                    </Td>
                  </Row>
                )
              })}
            </tbody>
          </Table>
        )}
      </Panel>

      <Panel
        eyebrow="History"
        title="Decided requests"
        description="Kept for as long as the audit trail is. The trail itself carries who decided what, and when."
      >
        {history.length === 0 ? (
          <EmptyState title="Nothing decided yet" />
        ) : (
          <Table>
            <thead>
              <tr>
                <Th className="w-[16%]">Who</Th>
                <Th className="w-[18%]">Cluster</Th>
                <Th className="w-[12%]">Role</Th>
                <Th className="w-[14%]">Outcome</Th>
                <Th className="hidden w-[20%] md:table-cell">Decided by</Th>
                <Th className="w-[20%]">When</Th>
              </tr>
            </thead>
            <tbody>
              {history.map((request) => (
                <Row key={request.id} title={request.reason}>
                  <Td className="truncate font-mono text-[13px]">{request.requester_username}</Td>
                  <Td className="truncate font-mono text-[13px]">{request.cluster_name}</Td>
                  <Td className="font-mono text-[12.5px]">{request.requested_role}</Td>
                  <Td>
                    <Pill tone={STATUS_TONE[request.status]}>{request.status}</Pill>
                  </Td>
                  <Td className="hidden truncate text-[13px] text-muted md:table-cell">
                    {request.approver_username ?? '—'}
                  </Td>
                  <Td className="text-[12.5px] text-muted">{relativeAge(request.updated_at)}</Td>
                </Row>
              ))}
            </tbody>
          </Table>
        )}
      </Panel>

      {requesting ? (
        <JitRequestModal
          clusters={clusters}
          durations={list?.durations}
          onClose={() => setRequesting(false)}
          onCreated={() => {
            setRequesting(false)
            void load()
          }}
        />
      ) : null}

      {/* A link from a chat notification names the request it is about. Saying so
          is honest about what this page did with it: it is a queue, not a detail
          view, and the row is above. */}
      {focusRequest && !requests.some((request) => request.id === focusRequest) ? (
        <Notice tone="warn">
          The request this link names is not in your list any more — it may have been decided, or it
          may belong to somebody else.
        </Notice>
      ) : null}
    </div>
  )
}

/** remaining is the seconds left, drawn against the local clock but anchored to
    the server's own `expires_at` — so a browser whose clock is off by a minute is
    wrong by a minute rather than wrong about whether access exists. */
function remaining(request: JitRequest, now: number): number {
  if (!request.expires_at) return request.remaining_seconds
  return Math.max(0, Math.floor((new Date(request.expires_at).getTime() - now) / 1000))
}
