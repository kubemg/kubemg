import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, RefreshCw, Search, Radio } from 'lucide-react'
import { errorMessage, fetchAudit, fetchAuditSummary, fetchUsers } from '../api/client'
import type { AuditEvent, AuditQuery, AuditSummary, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, Notice, Pill, Select } from '../components/primitives'
import type { PillTone } from '../components/primitives'
import { relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

const PAGE_SIZE = 50

/* The verbs worth filtering by. Anything else still shows in the table; this is
   the shortlist an auditor actually reaches for. */
const VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete']

/** A refusal reads as a refusal, a mutation as something to look twice at. */
function statusTone(event: AuditEvent): PillTone {
  if (event.error || event.status >= 500) return 'bad'
  if (event.status >= 400) return 'warn'
  return 'ok'
}

const MUTATING = new Set(['create', 'update', 'patch', 'delete'])

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
    fetchAuditSummary().then(setSummary).catch(() => setSummary(null))
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
      title="Audit"
      actions={
        <Button onClick={() => void load()} disabled={loading}>
          <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {scopedToSelf ? (
          <Notice tone="info">
            You are seeing your own activity. Fleet-wide audit is an administrator view.
          </Notice>
        ) : null}

        {summary ? (
          <div className="grid gap-2 sm:grid-cols-3">
            <SummaryTile
              label={`Calls · last ${summary.window_hours}h`}
              value={summary.total}
              tone="neutral"
            />
            <SummaryTile label="Refused or failed" value={summary.failed} tone="bad" />
            <SummaryTile label="Sessions opened" value={summary.streams} tone="accent" />
          </div>
        ) : null}

        <div className="panel min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-2 border-b border-line-soft px-3 py-2">
            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-faint"
              />
              <input
                type="search"
                value={search}
                onChange={(event) => narrow(() => setSearch(event.target.value))}
                placeholder="Path, user, resource"
                aria-label="Search the audit trail"
                className="w-52 rounded-[5px] border border-line bg-surface py-1 pr-2 pl-7 text-[12px] text-fg transition-colors placeholder:text-faint hover:border-faint focus:border-primary focus:outline-none"
              />
            </div>

            <Select
              aria-label="Filter by cluster"
              value={clusterId}
              onChange={(event) => narrow(() => setClusterId(event.target.value))}
              className="w-auto py-1 text-[12px]"
            >
              <option value="">All clusters</option>
              {clusters.map((cluster) => (
                <option key={cluster.id} value={cluster.id}>
                  {cluster.name}
                </option>
              ))}
            </Select>

            {user?.role === 'admin' ? (
              <Select
                aria-label="Filter by user"
                value={userId}
                onChange={(event) => narrow(() => setUserId(event.target.value))}
                className="w-auto py-1 text-[12px]"
              >
                <option value="">All users</option>
                {users.map((entry) => (
                  <option key={entry.id} value={entry.id}>
                    {entry.username}
                  </option>
                ))}
              </Select>
            ) : null}

            <Select
              aria-label="Filter by verb"
              value={verb}
              onChange={(event) => narrow(() => setVerb(event.target.value))}
              className="w-auto py-1 text-[12px]"
            >
              <option value="">All verbs</option>
              {VERBS.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>

            <Toggle
              label="Refused"
              active={failedOnly}
              onClick={() => narrow(() => setFailedOnly((current) => !current))}
            />
            <Toggle
              label="Sessions"
              active={streamsOnly}
              onClick={() => narrow(() => setStreamsOnly((current) => !current))}
            />

            <span className="ml-auto text-[12px] text-muted">
              {total} {total === 1 ? 'record' : 'records'}
            </span>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full table-fixed border-collapse text-[13px]">
              <thead>
                <tr className="border-b border-line">
                  <th className="w-[3px] p-0">
                    <span className="sr-only">Outcome</span>
                  </th>
                  <th className="label w-[16%] px-3 py-2 text-left md:w-[11%]">When</th>
                  <th className="label w-[20%] px-3 py-2 text-left md:w-[12%]">Who</th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[12%]">
                    Cluster
                  </th>
                  <th className="label w-[16%] px-3 py-2 text-left md:w-[9%]">Verb</th>
                  <th className="label hidden px-3 py-2 text-left lg:table-cell lg:w-[32%]">
                    Path
                  </th>
                  <th className="label w-[16%] px-3 py-2 text-left md:w-[12%]">Result</th>
                </tr>
              </thead>
              <tbody>
                {events.map((event) => (
                  <tr
                    key={event.id}
                    className="border-b border-line-soft transition-colors last:border-0 hover:bg-raised"
                    title={event.error || event.path}
                  >
                    <td className="p-0">
                      <span
                        aria-hidden="true"
                        className={`block h-8 w-[3px] rounded-r-[2px] ${
                          event.error || event.status >= 400
                            ? 'bg-danger'
                            : MUTATING.has(event.verb)
                              ? 'bg-warn'
                              : 'bg-ok'
                        }`}
                      />
                    </td>
                    <td className="truncate px-3 py-2 text-[12px] text-muted">
                      {relativeAge(event.at)}
                    </td>
                    <td className="truncate px-3 py-2 font-mono text-[12px] text-fg">
                      {event.username || '—'}
                    </td>
                    <td className="hidden truncate px-3 py-2 font-mono text-[12px] text-muted md:table-cell">
                      {event.cluster || '—'}
                    </td>
                    <td className="px-3 py-2">
                      <span className="inline-flex items-center gap-1.5 font-mono text-[12px] text-fg">
                        {event.streaming ? (
                          <Radio aria-hidden="true" className="size-3 shrink-0 text-primary" />
                        ) : null}
                        {event.verb}
                      </span>
                    </td>
                    <td className="hidden truncate px-3 py-2 font-mono text-[11.5px] text-muted lg:table-cell">
                      {event.path}
                    </td>
                    <td className="px-3 py-2">
                      <Pill tone={statusTone(event)} title={event.error}>
                        {event.error
                          ? 'refused'
                          : event.streaming && event.phase === 'open'
                            ? 'open'
                            : String(event.status)}
                      </Pill>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {loading && events.length === 0 ? (
            <p className="px-3 py-6 text-center text-[12px] text-muted">Loading…</p>
          ) : null}

          {!loading && events.length === 0 ? (
            <div className="px-3 py-10 text-center">
              <p className="text-[13px] text-fg">Nothing recorded yet</p>
              <p className="mt-1 text-[12px] text-muted">
                Proxied kubectl traffic against agent-based clusters lands here.
              </p>
            </div>
          ) : null}

          {total > PAGE_SIZE ? (
            <div className="flex items-center justify-between border-t border-line-soft px-3 py-2">
              <span className="text-[12px] text-muted">
                Page {page} of {pages}
              </span>
              <div className="flex items-center gap-1">
                <Button
                  onClick={() => setOffset((current) => Math.max(0, current - PAGE_SIZE))}
                  disabled={offset === 0 || loading}
                >
                  <ChevronLeft aria-hidden="true" className="size-3.5" />
                  Newer
                </Button>
                <Button
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

        <p className="text-[11.5px] text-muted">
          A session — exec, attach, watch or a followed log — is recorded twice: once when it opens
          and once when it ends, with how long it ran and how much passed through it.
        </p>
      </div>
    </AppShell>
  )
}

function SummaryTile({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: 'neutral' | 'bad' | 'accent'
}) {
  const accent =
    tone === 'bad' ? 'text-danger' : tone === 'accent' ? 'text-primary' : 'text-fg'
  return (
    <div className="panel px-3.5 py-2.5">
      <p className="label">{label}</p>
      <p className={`mt-0.5 font-mono text-[20px] font-semibold tabular-nums ${accent}`}>{value}</p>
    </div>
  )
}

function Toggle({
  label,
  active,
  onClick,
}: {
  label: string
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`rounded-[5px] border px-2 py-1 text-[12px] transition-colors ${
        active
          ? 'border-primary/40 bg-primary-soft font-medium text-primary'
          : 'border-line bg-surface text-muted hover:text-fg'
      }`}
    >
      {label}
    </button>
  )
}
