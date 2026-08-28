import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  CalendarClock,
  ChevronLeft,
  ChevronRight,
  Download,
  FileDiff,
  PlayCircle,
  Radio,
  RefreshCw,
  ScrollText,
} from 'lucide-react'
import { useParams } from 'react-router'
import {
  errorMessage,
  exportAudit,
  fetchAudit,
  fetchAuditSummary,
  fetchUsers,
} from '../api/client'
import type { AuditEvent, AuditQuery, AuditSummary, User } from '../api/types'
import { AppShell } from '../components/AppShell'
import { AuditRecordSheet } from '../components/AuditRecordSheet'
import { ManifestDiffView } from '../components/ManifestDiffView'
import { timeRangeLabel } from '../lib/timerange'
import { useTimeRange } from '../state/timerange-context'
import {
  Button,
  Chip,
  EmptyState,
  Field,
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
  TextInput,
} from '../components/primitives'

// The player carries the terminal emulator, which is the heaviest thing in the
// app; an audit trail nobody replays from should not pay for it.
const TerminalSessionPlayer = lazy(() =>
  import('../components/terminal/TerminalSessionPlayer').then((module) => ({
    default: module.TerminalSessionPlayer,
  })),
)
import type { Tone } from '../lib/status'
import { formatInstant, relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'
import { useResult } from '../state/result-context'
import { useClusters } from '../state/clusters-context'

const PAGE_SIZE = 50

/* The verbs worth filtering by. Anything else still shows in the table; this is
   the shortlist an auditor actually reaches for. */
/* `replay` and `recording-delete` are not Kubernetes verbs — nothing about them
   touches a cluster — but they belong in this filter: they are how the trail
   answers who watched, or destroyed, a recording of somebody else's shell. */
const VERBS = [
  'get',
  'list',
  'watch',
  'create',
  'update',
  'patch',
  'delete',
  'exec',
  'log',
  'replay',
  'recording-delete',
]

const MUTATING = new Set(['create', 'update', 'patch', 'delete'])

/* An auditor asks for a status by name far more often than by class: "show me
   the 403s" is the question, and "anything that failed" is the other one, which
   the Refused chip already answers. */
const STATUSES = [200, 201, 400, 401, 403, 404, 409, 422, 500, 502, 503]

/** toRFC3339 converts what a datetime-local input produces — a wall-clock string
    with no zone — into the instant the API expects. The browser's own zone is the
    right reading: an operator typing 09:00 means nine o'clock where they are. */
function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const parsed = new Date(local)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString()
}

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
  const report = useResult()
  // Present only at `/clusters/:id/audit`. The trail there is pre-selected and
  // locked to that cluster — the address already answers "which cluster", and
  // an editable picker would let the page disagree with its own URL.
  const routeClusterId = useParams<{ id?: string }>().id

  const [events, setEvents] = useState<AuditEvent[]>([])
  const [total, setTotal] = useState(0)
  const [summary, setSummary] = useState<AuditSummary | null>(null)
  const [scopedToSelf, setScopedToSelf] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [users, setUsers] = useState<User[]>([])

  const [clusterId, setClusterId] = useState(routeClusterId ?? '')
  const [userId, setUserId] = useState('')
  // A set rather than one value: an auditor narrowing to "the writes" is picking
  // four verbs, not making four consecutive single-verb queries.
  const [verbs, setVerbs] = useState<string[]>([])
  const [status, setStatus] = useState('')
  const [search, setSearch] = useState('')
  const [failedOnly, setFailedOnly] = useState(false)
  const [streamsOnly, setStreamsOnly] = useState(false)
  // The window. The preset is the console's, set in the header and carried in
  // the address, because "the last hour" has to mean one span in the trail and
  // in the charts beside it. The two boxes below are for the case a preset
  // cannot express, which is most of the ones that matter — an incident has a
  // start and an end, not a duration ending now — and they beat the preset on
  // the server as well as here.
  const { range } = useTimeRange()
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [showWindow, setShowWindow] = useState(false)
  const [offset, setOffset] = useState(0)
  // The session being replayed, addressed by the id its audit rows carry.
  const [replaying, setReplaying] = useState<AuditEvent | null>(null)
  // The row whose stored manifest diff is open. Only an `update` row written
  // while "record manifest diffs" was on for a non-redacted kind carries one
  // at all — see AuditEvent.diff — so most rows never offer this.
  const [viewingDiff, setViewingDiff] = useState<AuditEvent | null>(null)
  // The record being read. A row carries everything this opens, so it is the row
  // itself rather than an id to fetch by.
  const [opened, setOpened] = useState<AuditEvent | null>(null)
  const [exporting, setExporting] = useState(false)

  const query = useMemo<AuditQuery>(
    () => ({
      cluster_id: clusterId ? Number(clusterId) : undefined,
      user_id: userId ? Number(userId) : undefined,
      verb: verbs.length > 0 ? verbs : undefined,
      status: status ? Number(status) : undefined,
      q: search.trim() || undefined,
      failed: failedOnly || undefined,
      streaming: streamsOnly || undefined,
      // An explicit boundary beats the preset on the server too, so sending both
      // is not ambiguous — but sending neither has to mean "everything", which is
      // what `all` says.
      from: toRFC3339(from),
      to: toRFC3339(to),
      range: from ? undefined : range,
      limit: PAGE_SIZE,
      offset,
    }),
    [clusterId, userId, verbs, status, search, failedOnly, streamsOnly, from, to, range, offset],
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

  // A cluster named in the address always wins: switching from one cluster's
  // trail to another's through the entity switcher remounts the same route
  // rather than the same component instance in most navigations, but this
  // keeps the filter honest on the ones that do not.
  useEffect(() => {
    if (!routeClusterId) return
    setClusterId(routeClusterId)
    setOffset(0)
  }, [routeClusterId])

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

  // The console's window is a filter like any other, so it invalidates the page
  // offset too — and it clears a hand-typed boundary, because a preset and an
  // exact window are two answers to one question and the header control cannot
  // show that a boundary it does not own is overriding it. The first run is
  // skipped: a page opening with a preset has nothing to clear.
  const settled = useRef(false)
  useEffect(() => {
    if (!settled.current) {
      settled.current = true
      return
    }
    setOffset(0)
    setFrom('')
    setTo('')
  }, [range])

  // Any filter change invalidates the current page offset.
  function narrow(apply: () => void) {
    apply()
    setOffset(0)
  }

  /**
   * Taking the filtered result away.
   *
   * The file is fetched rather than linked because the API is
   * bearer-authenticated — see `exportAudit` — and handed to the browser through
   * an object URL that is revoked immediately after: a blob left attached to the
   * document is a copy of the audit trail sitting in the tab until it is closed.
   *
   * A truncated export is reported as a warning rather than as a success,
   * because the failure mode this feature has is somebody filing a partial file
   * as the whole story.
   */
  async function download() {
    setExporting(true)
    try {
      const { blob, filename, truncated } = await exportAudit(query)
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filename
      link.click()
      URL.revokeObjectURL(url)
      report({
        tone: truncated ? 'warn' : 'ok',
        title: 'Exported',
        body: truncated
          ? `The file stops at ${truncated.toLocaleString()} rows. Narrow the filter and export again for the rest.`
          : `${filename} holds exactly what this page is filtered to.`,
      })
    } catch (err) {
      report({
        tone: 'error',
        title: 'Nothing was exported',
        body: errorMessage(err, 'The audit trail could not be exported.'),
      })
    } finally {
      setExporting(false)
    }
  }

  const page = Math.floor(offset / PAGE_SIZE) + 1
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <AppShell
      title="Audit trail"
      timeRange
      actions={
        <>
          {/* Evidence collection was a screenshot. The export answers the query
              already on screen, so it sits beside Refresh rather than behind a
              form of its own. */}
          <Button onClick={() => void download()} disabled={exporting || total === 0}>
            <Download aria-hidden="true" className="size-4" />
            {exporting ? 'Exporting…' : 'Export'}
          </Button>
          <Button onClick={() => void load()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </>
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

            {routeClusterId ? (
              <span className="flex h-8 items-center rounded-control border border-line-soft bg-raised px-3 font-mono text-[12.5px] text-fg">
                {clusters.find((cluster) => cluster.id === Number(routeClusterId))?.name ??
                  `cluster ${routeClusterId}`}
              </span>
            ) : (
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
            )}

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
                aria-label="Filter by status"
                size="sm"
                value={status}
                onChange={(event) => narrow(() => setStatus(event.target.value))}
              >
                <option value="">Any status</option>
                {STATUSES.map((code) => (
                  <option key={code} value={code}>
                    {code}
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

          {/* The window, on a row of its own. The preset itself is the
              console's, up in the header — what stays here is the boundary a
              preset cannot express, and the reading of whichever is in force,
              because a page of records has to say what window it counted. */}
          <div className="flex flex-wrap items-center gap-2.5 border-b border-line-soft px-4 py-2.5">
            {!from && !to ? (
              <span className="text-[13px] text-muted">{timeRangeLabel(range)}</span>
            ) : null}

            <Chip active={showWindow} onClick={() => setShowWindow((current) => !current)}>
              <CalendarClock aria-hidden="true" className="size-3.5" />
              Exact window
            </Chip>

            {from || to ? (
              <span className="font-mono text-[12px] text-accent">
                {from ? formatInstant(from, { seconds: true }) : 'anything'} →{' '}
                {to ? formatInstant(to, { seconds: true }) : 'now'}
              </span>
            ) : null}
          </div>

          {showWindow ? (
            <div className="flex flex-wrap items-end gap-3 border-b border-line-soft px-4 py-3">
              <div className="w-56">
                <Field label="From" htmlFor="audit-from">
                  <TextInput
                    id="audit-from"
                    type="datetime-local"
                    className="font-mono text-[12.5px]"
                    value={from}
                    onChange={(event) => narrow(() => setFrom(event.target.value))}
                  />
                </Field>
              </div>
              <div className="w-56">
                <Field label="To" htmlFor="audit-to">
                  <TextInput
                    id="audit-to"
                    type="datetime-local"
                    className="font-mono text-[12.5px]"
                    value={to}
                    onChange={(event) => narrow(() => setTo(event.target.value))}
                  />
                </Field>
              </div>
              {from || to ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    narrow(() => {
                      setFrom('')
                      setTo('')
                    })
                  }
                >
                  Clear
                </Button>
              ) : null}
            </div>
          ) : null}

          {/* Verbs as badges rather than a dropdown: the question is almost
              always a set — "the writes", "the sessions" — and a dropdown makes
              a set into one query per member. */}
          <div className="flex flex-wrap items-center gap-2 border-b border-line-soft px-4 py-2.5">
            <span className="label mr-1">Verbs</span>
            {VERBS.map((value) => (
              <Chip
                key={value}
                active={verbs.includes(value)}
                onClick={() =>
                  narrow(() =>
                    setVerbs((current) =>
                      current.includes(value)
                        ? current.filter((entry) => entry !== value)
                        : [...current, value],
                    ),
                  )
                }
              >
                <span className="font-mono text-[12.5px]">{value}</span>
              </Chip>
            ))}
            {verbs.length > 0 ? (
              <button
                type="button"
                onClick={() => narrow(() => setVerbs([]))}
                className="ml-1 text-[12.5px] text-accent hover:underline"
              >
                Clear
              </button>
            ) : (
              <span className="ml-1 text-[12px] text-faint">none selected — every verb</span>
            )}
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
                <Row
                  key={event.id}
                  title={event.error || event.path}
                  onOpen={() => setOpened(event)}
                >
                  {/* The row opens, and this is the control that opens it: a
                      `<tr>` cannot be focused or announced, so the click on the
                      row is the convenience and this is the way in. The time is
                      relative here and absolute on hover — a list is scanned,
                      a record is filed. */}
                  <Td className="truncate text-[12.5px] text-muted">
                    <button
                      type="button"
                      onClick={() => setOpened(event)}
                      title={formatInstant(event.at, { seconds: true })}
                      className="cursor-pointer text-left transition-colors hover:text-fg hover:underline"
                    >
                      {relativeAge(event.at)}
                    </button>
                  </Td>
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
                    <div className="flex items-center gap-1">
                      {/* A shell in a production pod is the line an auditor
                          stops on, so the replay is offered right there rather
                          than on a page of its own. */}
                      {replayable(event) ? (
                        <IconButton
                          label="Replay terminal session"
                          onClick={() => setReplaying(event)}
                        >
                          <PlayCircle aria-hidden="true" className="size-4" />
                        </IconButton>
                      ) : null}
                      {event.diff ? (
                        <IconButton label="View manifest diff" onClick={() => setViewingDiff(event)}>
                          <FileDiff aria-hidden="true" className="size-4" />
                        </IconButton>
                      ) : null}
                    </div>
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

      {/* The record, opened. It carries no fetch of its own — every field is
          already on the row the table drew — and it hands the two consequences
          back to the page, so a replay reached from a record and one reached
          from the row's own button are the same surface. */}
      {opened ? (
        <AuditRecordSheet
          event={opened}
          onClose={() => setOpened(null)}
          onReplay={
            replayable(opened)
              ? () => {
                  const event = opened
                  setOpened(null)
                  setReplaying(event)
                }
              : undefined
          }
          onViewDiff={
            opened.diff
              ? () => {
                  const event = opened
                  setOpened(null)
                  setViewingDiff(event)
                }
              : undefined
          }
        />
      ) : null}

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

      {viewingDiff?.diff ? (
        <Sheet
          width="lg"
          eyebrow={`${viewingDiff.cluster} · ${viewingDiff.username} · ${relativeAge(viewingDiff.at)}`}
          title="Manifest diff"
          onClose={() => setViewingDiff(null)}
        >
          <p className="font-mono text-[12.5px] text-muted">{viewingDiff.path}</p>
          <ManifestDiffView diff={viewingDiff.diff} />
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
