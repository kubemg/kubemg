import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, PlayCircle, Radio, RefreshCw, ScrollText } from 'lucide-react'
import { errorMessage, fetchAudit, fetchAuditSummary, fetchUsers } from '../api/client'
import type { AuditEvent, AuditQuery, AuditSummary, User } from '../api/types'
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

// The player carries the terminal emulator, which is the heaviest thing in the
// app; an audit trail nobody replays from should not pay for it.
const TerminalSessionPlayer = lazy(() =>
  import('../components/terminal/TerminalSessionPlayer').then((module) => ({
    default: module.TerminalSessionPlayer,
  })),
)
import type { Tone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

const PAGE_SIZE = 50

/* The verbs worth filtering by. Anything else still shows in the table; this is
   the shortlist an auditor actually reaches for. */
const VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete', 'exec', 'log']

const MUTATING = new Set(['create', 'update', 'patch', 'delete'])

/* The verbs that produce a recording. Everything else is a request, not a
   session, and there is nothing to watch. */
const REPLAYABLE = new Set(['exec', 'attach'])

/** A session is replayable when it was recorded, which is what the id says. */
function replayable(event: AuditEvent): boolean {
  return Boolean(event.session_id) && REPLAYABLE.has(event.verb)
}

/** A refusal reads as a refusal, a mutation as something to look twice at. */
function statusTone(event: AuditEvent): Tone {
  if (event.error || event.status >= 500) return 'bad'
  if (event.status >= 400) return 'warn'
  return 'ok'
}

export function AuditTrail() {
  const { user } = useAuth()
  const { clusters } = useClusters()

  const [events, setEvents] = useState<AuditEvent[]>([])
  const [total, setTotal] = useState(0)
  const [summary, setSummary] = useState<AuditSummary | null>(null)
  const [scopedToSelf, setScopedToSelf] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [users, setUsers] = useState<User[]>([])

  const [clusterId, setClusterId] = useState('')
  const [userId, setUserId] = useState('')
  const [verb, setVerb] = useState('')
  const [search, setSearch] = useState('')
  const [failedOnly, setFailedOnly] = useState(false)
  const [streamsOnly, setStreamsOnly] = useState(false)
  const [offset, setOffset] = useState(0)
  // The session being replayed, addressed by the id its audit rows carry.
  const [replaying, setReplaying] = useState<AuditEvent | null>(null)

  const query = useMemo<AuditQuery>(
    () => ({
      cluster_id: clusterId ? Number(clusterId) : undefined,
      user_id: userId ? Number(userId) : undefined,
      verb: verb || undefined,
      q: search.trim() || undefined,
      failed: failedOnly || undefined,
      streaming: streamsOnly || undefined,
      limit: PAGE_SIZE,
      offset,
    }),
    [clusterId, userId, verb, search, failedOnly, streamsOnly, offset],
  )

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const page = await fetchAudit(query)
      setEvents(page.events)
      setTotal(page.total)
      setScopedToSelf(page.scoped_to_self)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the audit trail.'))
    } finally {
      setLoading(false)
    }
  }, [query])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    fetchAuditSummary()
      .then(setSummary)
      .catch(() => setSummary(null))
  }, [])

  // The user filter is an admin affordance; a scoped viewer has nothing to pick.
  useEffect(() => {
    if (user?.role !== 'admin') return
    fetchUsers()
      .then(setUsers)
      .catch(() => setUsers([]))
  }, [user?.role])

  // Any filter change invalidates the current page offset.
  function narrow(apply: () => void) {
    apply()
    setOffset(0)
  }

  const page = Math.floor(offset / PAGE_SIZE) + 1
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <AppShell
      title="Audit trail"
      actions={
        <Button onClick={() => void load()} disabled={loading}>
          <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {scopedToSelf ? (
          <Notice tone="info">
            You are seeing your own activity. Fleet-wide audit is an administrator view.
          </Notice>
        ) : null}

        {summary ? (
          <div className="grid gap-3 sm:grid-cols-3">
            <Stat label={`Calls · last ${summary.window_hours}h`} value={summary.total} />
            <Stat label="Refused or failed" value={summary.failed} tone="bad" />
            <Stat label="Sessions opened" value={summary.streams} tone="accent" />
          </div>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-2.5 border-b border-line-soft px-4 py-3">
            <SearchInput
              value={search}
              onChange={(next) => narrow(() => setSearch(next))}
              label="Search the audit trail"
              placeholder="Path, user, resource"
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

            {user?.role === 'admin' ? (
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

            <div className="w-32">
              <Select
                aria-label="Filter by verb"
                size="sm"
                value={verb}
                onChange={(event) => narrow(() => setVerb(event.target.value))}
              >
                <option value="">All verbs</option>
                {VERBS.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </Select>
            </div>

            <Chip
              active={failedOnly}
              onClick={() => narrow(() => setFailedOnly((current) => !current))}
            >
              Refused
            </Chip>
            <Chip
              active={streamsOnly}
              onClick={() => narrow(() => setStreamsOnly((current) => !current))}
            >
              Sessions
            </Chip>

            <span className="ml-auto text-[13px] text-muted">
              {total} {total === 1 ? 'record' : 'records'}
            </span>
          </div>

          <Table>
            <thead>
              <tr>
                <Th className="w-[18%] md:w-[10%]">When</Th>
                <Th className="w-[22%] md:w-[12%]">Who</Th>
                <Th className="hidden md:table-cell md:w-[12%]">Cluster</Th>
                <Th className="w-[18%] md:w-[9%]">Verb</Th>
                <Th className="hidden lg:table-cell lg:w-[32%]">Path</Th>
                <Th className="w-[18%] md:w-[12%]">Result</Th>
                <Th className="w-[6%]">
                  <span className="sr-only">Replay</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {events.map((event) => (
                <Row key={event.id} title={event.error || event.path}>
                  <Td className="truncate text-[12.5px] text-muted">{relativeAge(event.at)}</Td>
                  <Td className="truncate font-mono text-[12.5px] text-fg">
                    {event.username || '—'}
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                    {event.cluster || '—'}
                  </Td>
                  <Td>
                    <span
                      className={`flex items-center gap-1.5 font-mono text-[12.5px] ${
                        MUTATING.has(event.verb) ? 'text-warn' : 'text-fg'
                      }`}
                    >
                      {event.streaming ? (
                        <Radio aria-hidden="true" className="size-3 shrink-0 text-accent" />
                      ) : null}
                      {event.verb}
                    </span>
                  </Td>
                  <Td className="hidden truncate font-mono text-[12px] text-muted lg:table-cell">
                    {event.path}
                  </Td>
                  <Td>
                    <Pill tone={statusTone(event)} title={event.error}>
                      {event.error
                        ? 'refused'
                        : event.streaming && event.phase === 'open'
                          ? 'open'
                          : String(event.status)}
                    </Pill>
                  </Td>
                  <Td>
                    {/* A shell in a production pod is the line an auditor stops
                        on, so the replay is offered right there rather than on a
                        page of its own. */}
                    {replayable(event) ? (
                      <IconButton
                        label="Replay terminal session"
                        onClick={() => setReplaying(event)}
                      >
                        <PlayCircle aria-hidden="true" className="size-4" />
                      </IconButton>
                    ) : null}
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>

          {loading && events.length === 0 ? (
            <p className="px-4 py-8 text-center text-[13px] text-muted">Loading…</p>
          ) : null}

          {!loading && events.length === 0 ? (
            <EmptyState
              icon={<ScrollText aria-hidden="true" className="size-5" />}
              title="Nothing recorded yet"
            >
              Proxied kubectl traffic against agent-based clusters lands here, refusals included.
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
          A session — exec, attach, watch or a followed log — is recorded twice: once when it opens
          and once when it ends, with how long it ran and how much passed through it. An exec or an
          attach is also recorded in full, and plays back from the row it is on.
        </p>
      </div>

      {replaying?.session_id ? (
        <Sheet
          width="wide"
          eyebrow={`${replaying.cluster} · ${replaying.username} · ${relativeAge(replaying.at)}`}
          title="Session replay"
          onClose={() => setReplaying(null)}
        >
          <Suspense fallback={<p className="text-[13px] text-muted">Loading the player…</p>}>
            <TerminalSessionPlayer sessionId={replaying.session_id} />
          </Suspense>
        </Sheet>
      ) : null}
    </AppShell>
  )
}

function Stat({
  label,
  value,
  tone = 'default',
}: {
  label: string
  value: number
  tone?: 'default' | 'bad' | 'accent'
}) {
  const accent = tone === 'bad' ? 'text-danger' : tone === 'accent' ? 'text-accent' : 'text-fg'
  return (
    <div className="card px-4 py-3.5">
      <p className="label">{label}</p>
      <p className={`mt-1 font-mono text-[26px] leading-none font-semibold ${accent}`}>{value}</p>
    </div>
  )
}
