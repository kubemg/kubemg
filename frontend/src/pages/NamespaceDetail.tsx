import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Boxes, ChevronRight, Siren } from 'lucide-react'
import { errorMessage, fetchClusterEvents, fetchLimitRanges, fetchNamespaces, fetchResourceQuotas } from '../api/client'
import type { EventGroup, Namespace } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ClusterWorkloadSummary } from '../components/ClusterWorkloadSummary'
import { EventGroupRow } from '../components/EventGroupRow'
import { NetworkPolicyCoveragePanel } from '../components/NetworkPolicyCoveragePanel'
import { ResourceView } from '../components/ResourceTables'
import { EmptyState, Notice, Panel, Pill } from '../components/primitives'
import { CardSkeleton } from '../components/SkeletonLoader'
import { clusterPageHref, hasTunnel, resourceHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { phaseTone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { useTimeRange } from '../state/timerange-context'
import { useClusters } from '../state/clusters-context'

/*
 * One namespace's own page.
 *
 * A namespace was a row in a cluster-scoped list and nothing else, which is odd
 * given that it is the unit a scoped developer's entire world is: their grant
 * names namespaces, their quota is a namespace's, and the blast radius of
 * anything they do is a namespace. Every fact worth putting on a page for one
 * was already being read somewhere — just never in one place, and never
 * together.
 *
 * So this is a **composition of existing surfaces, not a new read**. The
 * workload and pod cards are `ClusterWorkloadSummary` with a namespace instead
 * of every namespace, so the derivations are `lib/insights.ts`'s and a count
 * here cannot disagree with the same count one click away. The quota and
 * LimitRange tables are the Explore tables, rendered by the same `ResourceView`
 * over the same rows. Coverage is the NetworkPolicy panel the reachability view
 * already draws. The events are the cluster timeline's own grouped rows,
 * narrowed to this namespace and capped, with the way through to the full one.
 * Nothing here reaches the cluster by a path Explore does not already use, and
 * nothing here is a permission a grant does not already carry.
 *
 * What the page adds is the *adjacency*: "is it running", "is it allowed to
 * grow", "is it isolated" and "what did the cluster just say about it" are four
 * questions with one answer between them, and they were four pages apart.
 */

/** How many event groups the page names before handing off to the timeline. */
const MAX_EVENT_GROUPS = 6

export function NamespaceDetail() {
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string; name: string }>()
  const clusterId = Number(params.id)
  const name = params.name ?? ''
  const { range } = useTimeRange()

  const cluster = clusters.find((entry) => entry.id === clusterId)
  const live = cluster ? hasTunnel(cluster) : false

  // The namespace list is the one read that says what this object *is* — its
  // phase, its age, and whether the caller's grant covers it. It is the same
  // read the list one level up made, so it is usually already cached.
  const namespaces = useCachedQuery<{ namespaces: Namespace[]; scoped: boolean }>(
    live ? queryKey('namespaces', clusterId) : null,
    () => fetchNamespaces(clusterId),
  )
  const entry = namespaces.data?.namespaces.find((row) => row.name === name)

  const limits = useCachedQuery(
    live ? queryKey('namespace-limits', clusterId, name) : null,
    async () => {
      const [quotas, ranges] = await Promise.all([
        fetchResourceQuotas(clusterId, name),
        fetchLimitRanges(clusterId, name),
      ])
      return { quotas, ranges }
    },
  )

  const events = useCachedQuery(
    live ? queryKey('namespace-events', clusterId, name, range) : null,
    () => fetchClusterEvents(clusterId, name, { range }),
    { live: true },
  )

  const groups = useMemo(
    () => (events.data?.groups ?? []).slice(0, MAX_EVENT_GROUPS),
    [events.data],
  )

  if (!cluster) {
    // Unknown or unreadable is explained rather than redirected, the same rule
    // Explore follows: a link somebody was sent must say why it did not open.
    return (
      <AppShell title={name || 'Namespace'}>
        <div className="card">
          <EmptyState
            icon={<Boxes aria-hidden="true" className="size-5" />}
            title={clustersLoading ? 'Looking for that cluster' : 'That cluster is not registered'}
          >
            {clustersLoading
              ? 'One moment.'
              : 'This address names a cluster kubemg does not hold, or one your grant does not cover.'}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  if (!live) {
    return (
      <AppShell title={name} parent={{ label: 'Namespaces', to: resourceHref(cluster.id, 'namespaces') }}>
        <div className="card">
          <EmptyState
            icon={<Boxes aria-hidden="true" className="size-5" />}
            title={`${cluster.name} has no live connection`}
          >
            Everything on this page is read on demand through the cluster&rsquo;s own agent tunnel,
            and this cluster has none open.{' '}
            <Link to={clusterPageHref(cluster.id, 'dashboard')} className="text-accent hover:underline">
              Its dashboard explains the connection.
            </Link>
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell
      title={name}
      parent={{ label: 'Namespaces', to: resourceHref(cluster.id, 'namespaces') }}
      timeRange
      actions={
        <Link
          to={resourceHref(cluster.id, 'pods', `namespace=${encodeURIComponent(name)}`)}
          className="text-[12.5px] text-accent hover:underline"
        >
          Open in Explore
        </Link>
      }
    >
      <div className="flex flex-col gap-4">
        <NamespaceIdentity entry={entry} loading={namespaces.loading} />

        {/* What is running, and whether it is up. The same four cards the
            developer dashboard draws, scoped to here. */}
        <ClusterWorkloadSummary cluster={cluster} namespace={name} />

        {/* What the namespace is allowed to grow to. A quota is the reason a
            pod that never appeared never appeared, and it lived two clicks
            away from the list that does not show it. */}
        <Panel
          title="Quotas & limits"
          eyebrow="Declared"
          description="What bounds this namespace. A quota the controller has not counted yet reports no usage rather than zero."
        >
          {limits.error ? (
            <Notice tone="error">
              {errorMessage(limits.error, 'Could not read this namespace’s quotas.')}
            </Notice>
          ) : null}
          {limits.loading && !limits.data ? <CardSkeleton lines={2} label="Reading quotas" /> : null}
          {limits.data ? (
            limits.data.quotas.length === 0 && limits.data.ranges.length === 0 ? (
              <p className="px-4 py-6 text-center text-[13px] text-muted">
                Nothing bounds this namespace: no ResourceQuota and no LimitRange.
              </p>
            ) : (
              <div className="flex flex-col gap-3">
                {limits.data.quotas.length > 0 ? (
                  <ResourceView
                    loaded={{ kind: 'resourcequotas', rows: limits.data.quotas }}
                    onSelectPod={() => {}}
                  />
                ) : null}
                {limits.data.ranges.length > 0 ? (
                  <ResourceView
                    loaded={{ kind: 'limitranges', rows: limits.data.ranges }}
                    onSelectPod={() => {}}
                  />
                ) : null}
              </div>
            )
          ) : null}
        </Panel>

        {/* Whether anything isolates it. The coverage reading is the posture
            scan's own, and it says what it does not model rather than implying
            it traced a connection. */}
        <NetworkPolicyCoveragePanel cluster={cluster} namespace={name} />

        <Panel
          title="Recent events"
          eyebrow="From the cluster"
          description="What Kubernetes itself recorded here, grouped by the object it happened to."
          actions={
            <Link
              to={`${clusterPageHref(cluster.id, 'events')}?ns=${encodeURIComponent(name)}`}
              className="inline-flex items-center gap-0.5 text-[12.5px] text-accent hover:underline"
            >
              Open the timeline
              <ChevronRight aria-hidden="true" className="size-3.5" />
            </Link>
          }
        >
          <NamespaceEvents
            groups={groups}
            available={events.data?.events_available ?? true}
            reason={events.data?.reason}
            loading={events.loading && !events.data}
            error={events.error ? errorMessage(events.error, 'Could not read events.') : null}
          />
        </Panel>
      </div>
    </AppShell>
  )
}

/** Phase, age and whether the grant covers it — the three facts the list held. */
function NamespaceIdentity({ entry, loading }: { entry?: Namespace; loading: boolean }) {
  if (!entry) {
    return (
      <div className="card flex items-center gap-3 p-4 text-[13px] text-muted">
        {loading
          ? 'Reading this namespace.'
          : // A scoped grant answers the namespace list from the grant itself, so
            // a namespace outside it is simply not in the answer. Saying that is
            // more useful than an empty header.
            'This namespace is not in the list your grant answers with. What is below is what the cluster lets you read.'}
      </div>
    )
  }

  return (
    <div className="card flex flex-wrap items-center gap-3 p-4">
      <Pill tone={phaseTone(entry.status)}>{entry.status}</Pill>
      {entry.granted ? (
        <span className="text-[12.5px] text-muted">granted to you</span>
      ) : (
        <span className="text-[12.5px] text-faint">not granted to you</span>
      )}
      {entry.created_at ? (
        <span className="text-[12.5px] text-faint">created {relativeAge(entry.created_at)}</span>
      ) : null}
    </div>
  )
}

function NamespaceEvents({
  groups,
  available,
  reason,
  loading,
  error,
}: {
  groups: EventGroup[]
  available: boolean
  reason?: string
  loading: boolean
  error: string | null
}) {
  const [open, setOpen] = useState<string | null>(null)

  if (error) return <Notice tone="error">{error}</Notice>
  if (loading) return <CardSkeleton lines={3} label="Reading events" />
  // Events are their own resource with their own RBAC. A refusal is the
  // cluster's answer, not a failure of the page.
  if (!available) {
    return (
      <p className="px-4 py-6 text-center text-[13px] text-muted">
        {reason ?? 'The cluster did not answer for events here.'}
      </p>
    )
  }
  if (groups.length === 0) {
    return (
      <EmptyState
        icon={<Siren aria-hidden="true" className="size-5" />}
        title="Nothing recorded in this window"
      >
        Kubernetes discards events after about an hour by default, so a quiet namespace and one
        whose events have expired read the same.
      </EmptyState>
    )
  }

  return (
    <ul className="flex flex-col divide-y divide-line-soft">
      {/* The group's own key, which the server holds stable across refreshes —
          so a row somebody opened stays open when the live tick re-reads. */}
      {groups.map((group) => (
        <EventGroupRow
          key={group.key}
          group={group}
          open={open === group.key}
          onToggle={() => setOpen((current) => (current === group.key ? null : group.key))}
        />
      ))}
    </ul>
  )
}
