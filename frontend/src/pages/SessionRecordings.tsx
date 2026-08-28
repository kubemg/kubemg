import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react'
import {
  ChevronLeft,
  ChevronRight,
  Lock,
  MonitorPlay,
  PlayCircle,
  RefreshCw,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import {
  deleteTerminalSession,
  errorMessage,
  fetchTerminalSessions,
  fetchUsers,
} from '../api/client'
import type { TerminalSession, TerminalSessionQuery, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import {
  Button,
  Chip,
  EmptyState,
  IconButton,
  Notice,
  Pill,
  Row,
  SearchInput,
  Select,
  Sheet,
  Table,
  Td,
  Th,
} from '../components/primitives'
import type { Tone } from '../lib/status'
import { formatDuration, relativeAge } from '../lib/time'
import { formatMemory } from '../lib/units'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

// The player carries the terminal emulator, which is the heaviest thing in the
// app. Even on the page that exists to replay sessions it is loaded on the first
// replay rather than on arrival: the index is what most visits are here for.
const TerminalSessionPlayer = lazy(() =>
  import('../components/terminal/TerminalSessionPlayer').then((module) => ({
    default: module.TerminalSessionPlayer,
  })),
)

const PAGE_SIZE = 25

/**
 * How a recording reads at a glance. A session that is still open is the one an
 * operator wants to find first — somebody is in a shell right now — so it says
 * so rather than showing an empty duration; a truncated one warns, because the
 * replay stops before the session did and that changes what the evidence means.
 */
function sessionTone(session: TerminalSession): Tone {
  if (session.error) return 'bad'
  if (session.open) return 'accent'
  if (session.truncated) return 'warn'
  return 'ok'
}

function sessionState(session: TerminalSession): string {
  if (session.error) return 'failed'
  if (session.open) return 'open'
  if (session.truncated) return 'truncated'
  return 'ended'
}

/** Where the session was: namespace/pod, with the container when there was a choice. */
function sessionTarget(session: TerminalSession): string {
  const pod = session.pod_name || '—'
  const where = session.namespace ? `${session.namespace}/${pod}` : pod
  return session.container_name ? `${where} · ${session.container_name}` : where
}

/**
 * SessionRecordings is the index of recorded shells.
 *
 * The audit trail already replays a session from the row that opened it, and
 * that is the right path when the question starts from a call. This page is the
 * other direction: the question starts from the sessions themselves — who has
 * been in a shell in production this week, what is open right now — and that is
 * not a question a trail of individual calls answers, because a session is one
 * row among thousands there.
 *
 * It follows the trail's rule with one addition: everyone sees their own
 * sessions, and seeing anybody else's needs the recording-viewer capability on
 * top of the admin role — administering KubeMG is not the same claim as watching
 * what a colleague typed into production. Deleting needs the same capability,
 * because destroying evidence you may not look at is not a lesser act, and the
 * subject of a recording never decides it stops existing.
 */
export function SessionRecordings() {
  const confirm = useConfirm()
  const report = useResult()
  const { user } = useAuth()
  const { clusters } = useClusters()

  // Watching somebody else's session is its own capability, not part of the
  // admin role. The server is authoritative — it narrows the list itself and
  // answers 404 for a recording that is not the caller's — so this only decides
  // which affordances are worth drawing.
  const mayViewAll = Boolean(user?.can_view_recordings)

  const [sessions, setSessions] = useState<TerminalSession[]>([])
  const [total, setTotal] = useState(0)
  const [enabled, setEnabled] = useState(true)
  const [scopedToSelf, setScopedToSelf] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [users, setUsers] = useState<User[]>([])

  const [clusterId, setClusterId] = useState('')
  const [userId, setUserId] = useState('')
  const [search, setSearch] = useState('')
  const [openOnly, setOpenOnly] = useState(false)
  const [offset, setOffset] = useState(0)

  // The recording being watched. The row is handed to the player, so opening one
  // from here costs the cast and nothing else.
  const [replaying, setReplaying] = useState<TerminalSession | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)

  const query = useMemo<TerminalSessionQuery>(
    () => ({
      cluster_id: clusterId ? Number(clusterId) : undefined,
      user_id: userId ? Number(userId) : undefined,
      q: search.trim() || undefined,
      open: openOnly || undefined,
      limit: PAGE_SIZE,
      offset,
    }),
    [clusterId, userId, search, openOnly, offset],
  )

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const page = await fetchTerminalSessions(query)
      setSessions(page.sessions)
      setTotal(page.total)
      setEnabled(page.recording_enabled)
      setScopedToSelf(page.scoped_to_self)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the recorded sessions.'))
    } finally {
      setLoading(false)
    }
  }, [query])

  useEffect(() => {
    void load()
  }, [load])

  // The user filter is an admin affordance; a scoped viewer has nothing to pick.
  useEffect(() => {
    if (!mayViewAll) return
    fetchUsers()
      .then(setUsers)
      .catch(() => setUsers([]))
  }, [mayViewAll])

  // Any filter change invalidates the current page offset.
  function narrow(apply: () => void) {
    apply()
    setOffset(0)
  }

  async function remove(session: TerminalSession) {
    const confirmed = await confirm({
      eyebrow: 'Session recording',
      title: `Delete the recording of ${session.username}'s session in ${sessionTarget(session)}?`,
      body: 'It is audit evidence and there is no second copy: deleting it cannot be undone. The audit records of the session itself stay.',
      confirmLabel: 'Delete',
    })
    if (!confirmed) return

    setRemoving(session.id)
    try {
      await deleteTerminalSession(session.id)
      if (replaying?.id === session.id) setReplaying(null)
      await load()
      report({
        tone: 'ok',
        title: 'Recording deleted',
        body: `${session.username}'s session in ${sessionTarget(session)} is gone. The audit records of the session itself stay.`,
        link: { to: '/audit', label: 'See it in the audit trail' },
      })
    } catch (err) {
      setError(errorMessage(err, 'Could not delete this recording.'))
    } finally {
      setRemoving(null)
    }
  }

  const page = Math.floor(offset / PAGE_SIZE) + 1
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <AppShell
      title="Session recordings"
      actions={
        <Button onClick={() => void load()} disabled={loading}>
          <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {/* An empty list means two entirely different things, and the server says
            which: nobody opened a shell, or nobody was recording when they did. */}
        {!enabled ? (
          <Notice tone="warn">
            Session recording is switched off on this server, so nothing new is being recorded.
            Sessions still appear in the audit trail. Turn it on with{' '}
            <span className="font-mono">KUBEMG_SESSION_RECORDING_ENABLED</span> and a writable{' '}
            <span className="font-mono">KUBEMG_SESSION_RECORDING_DIR</span>.
          </Notice>
        ) : null}

        {scopedToSelf ? (
          <Notice tone="info">
            You are seeing your own sessions. Replaying somebody else&rsquo;s is a capability of
            its own, separate from administering kubemg, and only a super admin can grant it.
          </Notice>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-2.5 border-b border-line-soft px-4 py-3">
            <SearchInput
              value={search}
              onChange={(next) => narrow(() => setSearch(next))}
              label="Search recorded sessions"
              placeholder="Pod, namespace, user"
              className="w-full sm:w-56"
            />

            <div className="w-40">
              <Select
                aria-label="Filter by cluster"
                size="sm"
                value={clusterId}
                onChange={(event) => narrow(() => setClusterId(event.target.value))}
              >
                <option value="">All clusters</option>
                {clusters.map((cluster) => (
                  <option key={cluster.id} value={cluster.id}>
                    {cluster.name}
                  </option>
                ))}
              </Select>
            </div>

            {mayViewAll ? (
              <div className="w-36">
                <Select
                  aria-label="Filter by user"
                  size="sm"
                  value={userId}
                  onChange={(event) => narrow(() => setUserId(event.target.value))}
                >
                  <option value="">All users</option>
                  {users.map((entry) => (
                    <option key={entry.id} value={entry.id}>
                      {entry.username}
                    </option>
                  ))}
                </Select>
              </div>
            ) : null}

            {/* The one filter worth a chip of its own: a shell that is open now
                is the only row on this page that is still changing. */}
            <Chip active={openOnly} onClick={() => narrow(() => setOpenOnly((now) => !now))}>
              Open now
            </Chip>

            <span className="ml-auto text-[13px] text-muted">
              {total} {total === 1 ? 'session' : 'sessions'}
            </span>
          </div>

          <Table>
            <thead>
              <tr>
                <Th className="w-[20%] md:w-[11%]">Started</Th>
                <Th className="w-[22%] md:w-[12%]">Who</Th>
                <Th className="hidden md:table-cell md:w-[12%]">Cluster</Th>
                <Th className="hidden lg:table-cell lg:w-[26%]">Where</Th>
                <Th className="w-[18%] md:w-[10%]">Ran for</Th>
                <Th className="hidden md:table-cell md:w-[9%]">Size</Th>
                <Th className="w-[18%] md:w-[11%]">State</Th>
                <Th className="w-[9%]">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((session) => (
                <Row key={session.id} title={session.error || sessionTarget(session)}>
                  <Td className="truncate text-[12.5px] text-muted">
                    {relativeAge(session.started_at)}
                  </Td>
                  <Td className="truncate font-mono text-[12.5px] text-fg">
                    {session.username || '—'}
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                    {session.cluster || '—'}
                  </Td>
                  <Td className="hidden truncate font-mono text-[12px] text-muted lg:table-cell">
                    {sessionTarget(session)}
                  </Td>
                  <Td className="truncate font-mono text-[12.5px] text-muted">
                    {session.open ? 'running' : formatDuration(session.duration_seconds)}
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                    {formatMemory(session.byte_count)}
                  </Td>
                  <Td>
                    <span className="flex items-center gap-1.5">
                      <Pill tone={sessionTone(session)} title={session.error}>
                        {sessionState(session)}
                      </Pill>
                      {/* How this file was written, which is a property of the
                          recording and not of current configuration — and the
                          difference between a stolen volume snapshot being an
                          inconvenience and being every password anyone typed. */}
                      <span
                        title={session.encrypted ? 'Encrypted at rest' : 'Not encrypted at rest'}
                        className="inline-flex shrink-0"
                      >
                        {session.encrypted ? (
                          <Lock aria-hidden="true" className="size-3 text-faint" />
                        ) : (
                          <ShieldAlert aria-hidden="true" className="size-3 text-warn" />
                        )}
                        <span className="sr-only">
                          {session.encrypted ? 'Encrypted at rest' : 'Not encrypted at rest'}
                        </span>
                      </span>
                    </span>
                  </Td>
                  <Td>
                    <div className="flex items-center justify-end gap-1">
                      <IconButton
                        label={`Replay ${session.username}'s session`}
                        onClick={() => setReplaying(session)}
                      >
                        <PlayCircle aria-hidden="true" className="size-4" />
                      </IconButton>
                      {/* A recording is evidence about someone, so removing one
                          is administrative — and never offered while the session
                          it records is still running. */}
                      {mayViewAll ? (
                        <IconButton
                          // IconButton titles itself from its label, so the
                          // reason a disabled one cannot be used belongs in the
                          // label rather than in a title of its own.
                          label={
                            session.open
                              ? 'This session is still running'
                              : `Delete ${session.username}'s recording`
                          }
                          tone="danger"
                          disabled={session.open || removing === session.id}
                          onClick={() => void remove(session)}
                        >
                          <Trash2 aria-hidden="true" className="size-4" />
                        </IconButton>
                      ) : null}
                    </div>
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>

          {loading && sessions.length === 0 ? (
            <p className="px-4 py-8 text-center text-[13px] text-muted">Loading…</p>
          ) : null}

          {!loading && sessions.length === 0 ? (
            <EmptyState
              icon={<MonitorPlay aria-hidden="true" className="size-5" />}
              title={enabled ? 'No sessions recorded yet' : 'Recording is switched off'}
            >
              {enabled
                ? 'Every exec and attach through the proxy is recorded in full and lands here, whether it was opened from the console or from kubectl.'
                : 'Sessions opened while recording is off are still in the audit trail, but there is no recording to replay.'}
            </EmptyState>
          ) : null}

          {total > PAGE_SIZE ? (
            <div className="flex items-center justify-between border-t border-line-soft px-4 py-3">
              <span className="text-[12.5px] text-muted">
                Page {page} of {pages}
              </span>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  onClick={() => setOffset((current) => Math.max(0, current - PAGE_SIZE))}
                  disabled={offset === 0 || loading}
                >
                  <ChevronLeft aria-hidden="true" className="size-3.5" />
                  Newer
                </Button>
                <Button
                  size="sm"
                  onClick={() => setOffset((current) => current + PAGE_SIZE)}
                  disabled={offset + PAGE_SIZE >= total || loading}
                >
                  Older
                  <ChevronRight aria-hidden="true" className="size-3.5" />
                </Button>
              </div>
            </div>
          ) : null}
        </div>

        <p className="text-[12px] text-muted">
          A recording is what was done in a shell, not just that one was opened — the audit trail
          says the second, this says the first. Only output is drawn on the terminal; what was typed
          has its own view, because a pty echoes keystrokes back and drawing both would double every
          character. Recordings are pruned on the same retention window as the audit trail.
        </p>
        <p className="text-[12px] text-muted">
          Watching a recording is itself audited — the trail records who replayed which session,
          refusals included — and a recording written under a recording key is encrypted at rest.
        </p>
      </div>

      {replaying ? (
        <Sheet
          width="wide"
          eyebrow={`${replaying.cluster} · ${replaying.username} · ${relativeAge(
            replaying.started_at,
          )}`}
          title={sessionTarget(replaying)}
          onClose={() => setReplaying(null)}
        >
          <Suspense fallback={<p className="text-[13px] text-muted">Loading the player…</p>}>
            <TerminalSessionPlayer sessionId={replaying.session_id} session={replaying} />
          </Suspense>
        </Sheet>
      ) : null}
    </AppShell>
  )
}
