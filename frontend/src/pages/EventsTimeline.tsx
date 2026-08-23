import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { ChevronRight, Siren, X } from 'lucide-react'
import { errorMessage, fetchClusterEvents, fetchNamespaces } from '../api/client'
import type { EventGroup, Namespace } from '../api/types'
import { AppShell } from '../components/AppShell'
import { LiveRefresh } from '../components/LiveRefresh'
import { Button, Chip, EmptyState, Notice, Pill, SearchInput, Select } from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import { ALL_NAMESPACES } from '../lib/resources'
import { queryKey, useCachedQuery } from '../lib/query'
import { relativeAge } from '../lib/time'
import { useTimeRange } from '../state/timerange-context'
import { useClusters } from '../state/clusters-context'

/**
 * What just broke, across a whole cluster.
 *
 * Events were already read, decoded and rendered — but only inside one object's
 * Describe tab, which answers "why is *this* not ready". That is the second
 * question. The first, the one somebody opens the console with, is "what broke
 * in the last fifteen minutes", and it was unanswerable while events could only
 * be reached through an object you already suspected.
 *
 * What makes this a timeline rather than a second table is the **grouping**,
 * which the server does: one row per involved object, with the reasons folded
 * inside it. A failing Deployment emits events from the deployment controller,
 * the replica set and every pod it owns; as rows that is forty lines describing
 * one problem, and as a group it is one problem to open.
 *
 * Two orderings were available and only one is honest. **Newest first is the
 * ordering** — the same reason the describe drawer has it, because the question
 * is what just happened. Warnings-first would put an hour-old warning above a
 * thirty-second-old failure, so warnings are a *filter* instead: one click, and
 * the ordering underneath it is unchanged.
 */

/** The namespace being read, in the address, so a timeline is a link. */
const NS_PARAM = 'ns'
/** The warnings filter, likewise — "the warnings in payments" is one link. */
const TYPE_PARAM = 'type'
/*
 * The object narrowing. Both components travel because two kinds can share a
 * name; they are what the pilot header's alerts link into, and the server
 * validates them rather than escaping them, since they end up in a fieldSelector.
 */
const KIND_PARAM = 'kind'
const NAME_PARAM = 'name'

export function EventsTimeline() {
  const { clusters, loading: clustersLoading } = useClusters()
  const navigate = useNavigate()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)

  const [searchParams, setSearchParams] = useSearchParams()
  const namespace = searchParams.get(NS_PARAM) ?? ''
  const warningsOnly = searchParams.get(TYPE_PARAM) === 'Warning'
  const objectKind = searchParams.get(KIND_PARAM) ?? ''
  const objectName = searchParams.get(NAME_PARAM) ?? ''
  const { range } = useTimeRange()

  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [scoped, setScoped] = useState(false)
  const [namespaceError, setNamespaceError] = useState<string | null>(null)
  // Narrows the loaded timeline to matching object names. It is the same gap the
  // Explore object filter closes: nothing in the header can find one pod among
  // two hundred rows.
  const [filter, setFilter] = useState('')
  // Which groups are open. Keyed by the server's stable group key, so an
  // expanded row survives the re-read a refresh causes.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  // Only an agent cluster with a live tunnel has events to read, exactly as
  // Explore only has resources to browse there.
  const reachable = useMemo(
    () => clusters.filter((entry) => entry.connection_mode === 'agent' && entry.agent_attached),
    [clusters],
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null
  const unreadable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  function setParam(key: string, value: string | null) {
    setSearchParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        if (value === null || value === '') next.delete(key)
        else next.set(key, value)
        return next
      },
      { replace: true },
    )
  }

  // The namespace list is what the scope picker is built from. A grant that can
  // browse a cluster it cannot enumerate keeps its own error rather than sharing
  // the timeline's, since the timeline still reads fine without it.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setNamespaces([])
    setNamespaceError(null)

    fetchNamespaces(cluster.id)
      .then((result) => {
        if (cancelled) return
        setNamespaces(result.namespaces)
        setScoped(result.scoped)
        // The timeline opens across everything the caller may see, which is the
        // question it exists to answer — unlike Explore, which opens on one
        // namespace because a list of every pod in a cluster is not a list.
        setSearchParams(
          (previous) => {
            if (previous.get(NS_PARAM)) return previous
            const next = new URLSearchParams(previous)
            next.set(NS_PARAM, ALL_NAMESPACES)
            return next
          },
          { replace: true },
        )
      })
      .catch((err) => {
        if (cancelled) return
        setNamespaceError(errorMessage(err, 'Could not list namespaces.'))
        // Listing namespaces and listing events are two grants: one can be
        // refused while the other is not. Falling back to the everything scope
        // keeps the timeline readable for somebody who may read events but may
        // not enumerate the cluster — the picker is what they lose, not the page.
        setSearchParams(
          (previous) => {
            if (previous.get(NS_PARAM)) return previous
            const next = new URLSearchParams(previous)
            next.set(NS_PARAM, ALL_NAMESPACES)
            return next
          },
          { replace: true },
        )
      })

    return () => {
      cancelled = true
    }
  }, [cluster, setSearchParams])

  const readKey =
    cluster && namespace
      ? queryKey('events', cluster.id, namespace, warningsOnly ? 'Warning' : '', objectKind, objectName, range)
      : null

  const timeline = useCachedQuery(
    readKey,
    async () => {
      if (!cluster) throw new Error('no cluster is selected')
      return fetchClusterEvents(cluster.id, namespace, {
        type: warningsOnly ? 'Warning' : undefined,
        kind: objectKind || undefined,
        name: objectName || undefined,
        range,
      })
    },
    // The one page in the console where the answer is *only* worth having if it
    // is current: "what broke in the last fifteen minutes" asked against a
    // reading from twenty minutes ago is the wrong question answered.
    { live: true },
  )

  const loaded = timeline.data
  const loading = timeline.loading || timeline.revalidating
  const error =
    namespaceError ??
    (timeline.error ? errorMessage(timeline.error, 'Could not read events from this cluster.') : null)

  const needle = filter.trim().toLowerCase()
  const groups = useMemo(() => {
    const all = loaded?.groups ?? []
    if (!needle) return all
    return all.filter(
      (group) =>
        group.object.name.toLowerCase().includes(needle) ||
        group.reason.toLowerCase().includes(needle),
    )
  }, [loaded, needle])

  // The readings above the list describe the *whole* answer, not the filtered
  // one — it is what the filtering is chosen from.
  const totals = useMemo(() => {
    const all = loaded?.groups ?? []
    return {
      objects: all.length,
      warnings: all.filter((group) => group.type === 'Warning').length,
      firings: all.reduce((sum, group) => sum + group.count, 0),
    }
  }, [loaded])

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Events">
        <div className="card">
          <EmptyState
            icon={<Siren aria-hidden="true" className="size-5" />}
            title={
              unreadable ? `${unreadable.name} has no live connection` : 'That cluster is not registered'
            }
          >
            {unreadable ? (
              <>
                Events are read on demand through the agent tunnel, so a cluster without one has
                nothing to show.{' '}
                <Link to={`/clusters/${unreadable.id}/summary`} className="text-accent hover:underline">
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its events.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const narrowed = objectKind !== '' || objectName !== ''

  return (
    <AppShell
      title="Events"
      fullWidth
      timeRange
      scope={
        cluster ? (
          <div className="w-44">
            <Select
              aria-label="Namespace"
              size="sm"
              value={namespace}
              disabled={namespaces.length === 0}
              onChange={(event) => setParam(NS_PARAM, event.target.value)}
            >
              {namespaces.length === 0 ? <option value="">No namespaces</option> : null}
              {namespaces.length > 0 ? (
                <option value={ALL_NAMESPACES}>
                  {scoped ? 'All granted namespaces' : 'All namespaces'}
                </option>
              ) : null}
              {namespaces.map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>
        ) : undefined
      }
      actions={<LiveRefresh query={timeline} disabled={!namespace} />}
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {/* Events are their own resource with their own RBAC, so a refusal
            narrows the answer rather than failing the page. */}
        {loaded && !loaded.events_available ? (
          <Notice tone="info">
            {loaded.reason ?? 'This cluster refused to list events.'}
          </Notice>
        ) : null}

        {/* An all-namespaces read is many reads, and some of them refusing is
            neither available nor unavailable. Naming them is what stops a
            partial cluster being presented as the whole one. */}
        {loaded?.unreadable_namespaces?.length ? (
          <Notice tone="warn">
            Events in {loaded.unreadable_namespaces.join(', ')} could not be read, so this timeline
            is missing whatever happened there.
          </Notice>
        ) : null}

        {/*
         * The honesty notice, and the one thing on this page that must not be
         * softened. The API server pages an event list in key order, so a
         * truncated read is an *alphabetical slice by object* that has then been
         * sorted by time — the newest of the sample, not the newest of the
         * cluster. Left unsaid, an operator reads the top row as "the most
         * recent thing that happened here" and it simply is not.
         */}
        {loaded?.truncated ? (
          <Notice tone="warn">
            This is <strong className="font-semibold">part of the cluster, not all of it</strong> —{' '}
            {loaded.scanned.toLocaleString()}
            {loaded.available ? ` of about ${loaded.available.toLocaleString()}` : ''} events were
            read. Kubernetes pages an event list in storage order rather than by time, so what is
            below is the newest of what was read and not necessarily the newest in the cluster.
            Narrow to one namespace, to warnings, or to a shorter window for an answer that is
            complete.
          </Notice>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">
              {totals.objects} {totals.objects === 1 ? 'object' : 'objects'}
            </h2>
            <span className="font-mono text-[12.5px] text-faint">
              {totals.firings} {totals.firings === 1 ? 'event' : 'events'}
            </span>
            {totals.warnings > 0 ? (
              <Pill tone="warn">{totals.warnings} warning{totals.warnings === 1 ? '' : 's'}</Pill>
            ) : null}

            {/* The filter, not the ordering. Newest first stays underneath it. */}
            <Chip
              active={warningsOnly}
              onClick={() => setParam(TYPE_PARAM, warningsOnly ? null : 'Warning')}
            >
              Warnings only
            </Chip>

            {narrowed ? (
              <Chip active onClick={() => { setParam(KIND_PARAM, null); setParam(NAME_PARAM, null) }}>
                {objectKind ? `${objectKind} ` : ''}
                {objectName}
                <X aria-hidden="true" className="size-3.5" />
                <span className="sr-only">Show every object</span>
              </Chip>
            ) : null}

            {loading ? <span className="text-[12px] text-muted">Reading the cluster…</span> : null}

            {totals.objects > 0 ? (
              <SearchInput
                value={filter}
                onChange={setFilter}
                placeholder="Filter by object or reason"
                label="Filter events"
                className="ml-auto w-full sm:w-64"
              />
            ) : null}
          </div>

          {timeline.loading ? <TableSkeleton columns={4} rows={8} label="Reading events" /> : null}

          {!timeline.loading && groups.length > 0 ? (
            <ul className="divide-y divide-line-soft">
              {groups.map((group) => (
                <EventRow
                  key={group.key}
                  group={group}
                  open={expanded[group.key] ?? false}
                  onToggle={() =>
                    setExpanded((current) => ({ ...current, [group.key]: !current[group.key] }))
                  }
                  onExplore={
                    cluster
                      ? () =>
                          navigate(
                            `/clusters/${cluster.id}/explore/pods?ns=${encodeURIComponent(
                              group.object.namespace ?? '',
                            )}`,
                          )
                      : undefined
                  }
                />
              ))}
            </ul>
          ) : null}

          {!timeline.loading && loaded && groups.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              {needle
                ? `Nothing matches “${filter}”.`
                : warningsOnly
                  ? 'No warnings in this window. That is the answer you want.'
                  : 'Nothing has been recorded in this window.'}
            </p>
          ) : null}

          {!loading && !namespace ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Select a namespace to read events.
            </p>
          ) : null}
        </div>

        {/* The window is ours; the ceiling is the cluster's, and saying so is the
            difference between an empty page reading as "nothing happened" and
            reading as "the cluster no longer has it". */}
        <p className="text-[12px] text-muted">
          {loaded?.buffered ? (
            <>
              Kept current by a single watch on this cluster, so opening this page costs it nothing
              and the ordering below is the cluster&rsquo;s rather than a sample&rsquo;s. What you can
              see is still narrowed to your own granted namespaces.
            </>
          ) : (
            <>
              Read live through the agent tunnel under your own identity, and grouped by the object
              each event was about.
            </>
          )}{' '}
          Kubernetes discards events after about an hour by default, so a window wider than that
          shows what the cluster still holds rather than more history.
        </p>
      </div>
    </AppShell>
  )
}

/**
 * One group: an object, and everything the cluster said about it. Collapsed it
 * is one line — the newest reason, the count and when it last happened, which is
 * what somebody scanning the page is reading. Opened it is the reasons, each
 * already folded across every Event object that carried it.
 */
function EventRow({
  group,
  open,
  onToggle,
  onExplore,
}: {
  group: EventGroup
  open: boolean
  onToggle: () => void
  onExplore?: () => void
}) {
  const warning = group.type === 'Warning'

  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-raised/60"
      >
        <ChevronRight
          aria-hidden="true"
          className={`mt-0.5 size-3.5 shrink-0 text-faint transition-transform ${open ? 'rotate-90' : ''}`}
        />
        <span
          aria-hidden="true"
          className={`mt-1.5 size-1.5 shrink-0 rounded-full ${warning ? 'bg-warn' : 'bg-muted'}`}
        />

        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-[11px] text-faint">{group.object.kind}</span>
            <span className="truncate font-mono text-[13px] text-fg">
              {group.object.namespace ? (
                <span className="text-faint">{group.object.namespace}/</span>
              ) : null}
              {group.object.name}
            </span>
            <span className={`text-[12.5px] ${warning ? 'text-warn' : 'text-muted'}`}>
              {group.reason}
            </span>
          </span>
          {/* The newest message, which is the one describing the state now. */}
          <span className="truncate text-[12.5px] text-muted">{group.message}</span>
        </span>

        {/* A count of firings, not of rows: 41 means the cluster said it 41
            times, which is the number that tells a flake from a loop. */}
        {group.count > 1 ? (
          <span className="shrink-0 font-mono text-[12px] text-faint">×{group.count}</span>
        ) : null}
        <span className="shrink-0 text-[12px] text-muted">{relativeAge(group.last_seen)}</span>
      </button>

      {open ? (
        <div className="flex flex-col gap-2 border-t border-line-soft bg-raised/30 px-4 py-3 pl-10">
          {group.entries.map((entry) => (
            <div key={`${entry.type}/${entry.reason}`} className="flex flex-col gap-0.5">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span
                  className={`text-[12.5px] font-medium ${
                    entry.type === 'Warning' ? 'text-warn' : 'text-fg'
                  }`}
                >
                  {entry.reason}
                </span>
                {entry.count > 1 ? (
                  <span className="font-mono text-[11.5px] text-faint">×{entry.count}</span>
                ) : null}
                {entry.source ? (
                  <span className="text-[11.5px] text-faint">{entry.source}</span>
                ) : null}
                {/* First and last, because "started twenty minutes ago and is
                    still going" is a different problem from "happened once". */}
                <span className="text-[11.5px] text-faint">
                  {entry.first_seen && entry.first_seen !== entry.last_seen
                    ? `${relativeAge(entry.first_seen)} → ${relativeAge(entry.last_seen)}`
                    : relativeAge(entry.last_seen)}
                </span>
              </span>
              <span className="text-[12.5px] leading-relaxed text-muted">{entry.message}</span>
            </div>
          ))}

          {group.entries_truncated ? (
            <p className="text-[12px] text-faint">
              This object produced more distinct reasons than are shown; its own Describe tab reads
              only its events.
            </p>
          ) : null}

          {onExplore ? (
            <div>
              <Button size="sm" onClick={onExplore}>
                Open {group.object.namespace || 'the namespace'} in Explore
              </Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  )
}
