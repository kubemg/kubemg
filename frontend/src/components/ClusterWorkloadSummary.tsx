/*
 * The cluster dashboard a developer arrives at.
 *
 * The administrator's dashboard beside this one is about the *cluster as an
 * installation*: the path traffic takes to reach it, which API server it is,
 * what version its agent runs, when it was registered, what its nodes are
 * consuming, where its datasource lives. Every one of those is a fact somebody
 * has to act on — and a developer cannot act on any of them. Handed to them it
 * is not detail, it is noise in front of the two questions they came with: is
 * what I deployed running, and if not, what does the cluster say about it.
 *
 * So this is that, and nothing else. It is deliberately the *summary* of the
 * resource lists rather than a new kind of reading: the numbers come from
 * `lib/insights.ts`, the same derivation the list headers draw, so a count here
 * and a count one click away cannot disagree — and every card hands off to the
 * list it summarises rather than growing a table of its own, because the lists
 * are already better at lists than a dashboard is.
 *
 * Two reads, both all-namespaces: the workload list (which is Deployments,
 * StatefulSets and DaemonSets in one answer) and the pod list. The server
 * resolves "all namespaces" against the caller's own grant, so a scoped
 * developer gets their namespaces and nothing else, and nothing here is a
 * permission the resource lists do not already have.
 */

import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { Boxes, Layers } from 'lucide-react'
import { errorMessage, fetchPods, fetchWorkloads, withReadReport } from '../api/client'
import type { Cluster, Workload } from '../api/types'
import { MetricsChart } from './MetricsChart'
import { EmptyState, Field, Notice, Panel, Pill, Select } from './primitives'
import { CardSkeleton } from './SkeletonLoader'
import type { InsightAlert, ResourceInsight } from '../lib/insights'
import { MAX_ALERTS, podInsights, workloadInsights } from '../lib/insights'
import { eventsHref, hasTunnel, resourceHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { ALL_NAMESPACES } from '../lib/resources'
import { TONE_FILL } from '../lib/status'

/**
 * The three workload kinds, each with the list it hands off to and the Kind an
 * events link narrows by. One read answers for all three — `Workload.kind` is
 * what splits it — because the three answer the same question and a developer
 * reads them as one thing.
 */
const WORKLOAD_KINDS: Array<{ kind: Workload['kind']; label: string; resource: string }> = [
  { kind: 'Deployment', label: 'Deployments', resource: 'deployments' },
  { kind: 'StatefulSet', label: 'StatefulSets', resource: 'statefulsets' },
  { kind: 'DaemonSet', label: 'DaemonSets', resource: 'daemonsets' },
]

/**
 * One row of the attention list: an alert from an insight, plus where it came
 * from. The insight knows what is wrong; only the caller knows which kind it was
 * reading, and both halves are needed to link at the object.
 */
type Attention = InsightAlert & { kind: string; resource: string }

/** How many objects the attention list names before it stops. */
const MAX_ATTENTION = 8

export function ClusterWorkloadSummary({ cluster }: { cluster: Cluster }) {
  const live = hasTunnel(cluster)

  const query = useCachedQuery(
    live ? queryKey('cluster-summary-workloads', cluster.id) : null,
    async () => {
      // One report around both reads: either can be bounded, and what the reader
      // needs to know is that the numbers below are a floor — not which of the
      // two lists hit the ceiling first.
      const { value, report } = await withReadReport(async () => {
        const [workloads, pods] = await Promise.all([
          fetchWorkloads(cluster.id, ALL_NAMESPACES),
          fetchPods(cluster.id, ALL_NAMESPACES),
        ])
        return { workloads, pods }
      })
      return { ...value, truncatedAt: report.truncatedAt }
    },
    { live: true },
  )

  // Both derivations hang off the answer itself rather than off arrays unpacked
  // from it: `?? []` is a new array on every render, which would re-derive every
  // insight on every render and defeat the memo entirely.
  const answer = query.data

  const cards = useMemo(
    () =>
      WORKLOAD_KINDS.map((entry) => ({
        ...entry,
        insight: workloadInsights(
          (answer?.workloads ?? []).filter((workload) => workload.kind === entry.kind),
          entry.label,
        ),
      })),
    [answer],
  )
  const podInsight = useMemo(() => podInsights(answer?.pods ?? [], null), [answer])

  const attention = useMemo(() => rankAttention(cards, podInsight), [cards, podInsight])

  if (!live) {
    return (
      <Panel title="Workloads" eyebrow="Live">
        <EmptyState
          icon={<Layers aria-hidden="true" className="size-5" />}
          title="Nothing to read from this cluster yet"
        >
          What is running here is read through the cluster&rsquo;s own agent tunnel, and this
          cluster has none open. Ask whoever registered it — until the agent dials in there is no
          live state for kubemg to show.
        </EmptyState>
      </Panel>
    )
  }

  return (
    <>
      {query.error ? (
        <Notice tone="error">
          {errorMessage(query.error, 'Could not read what is running in this cluster.')}
        </Notice>
      ) : null}
      {query.data?.truncatedAt ? (
        <Notice tone="info">
          This cluster holds more than {query.data.truncatedAt} objects of one of these kinds, so
          the counts below are that many rather than all of them. Open a list to read a namespace at
          a time.
        </Notice>
      ) : null}

      {query.loading ? <CardSkeleton lines={3} label="Reading what is running here" /> : null}

      {query.data ? (
        <>
          {/* What is deployed, and whether it is up. Four cards, because pods
              are the fourth answer to the same question and reading them apart
              from the workloads that own them is how a rollout looks fine
              while every pod behind it crash-loops. */}
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            {cards.map((card) => (
              <SummaryCard
                key={card.kind}
                label={card.label}
                insight={card.insight}
                href={resourceHref(cluster.id, card.resource)}
              />
            ))}
            <SummaryCard
              label="Pods"
              insight={podInsight}
              href={resourceHref(cluster.id, 'pods')}
            />
          </div>

          <AttentionPanel cluster={cluster} rows={attention} />
        </>
      ) : null}

      <Trend cluster={cluster} />
    </>
  )
}

/**
 * One kind's card: how many, how they split, and the way through to the list.
 *
 * The bar is the same device the list headers use and is drawn from the same
 * segments, so the shape of a card and the shape of the header it opens are the
 * same shape. It is not a control here — a dashboard cannot carry a narrowing
 * into a page that holds it in local state, and a chip that looked like a filter
 * and behaved like a link would be worse than the plain link beside it.
 */
function SummaryCard({
  label,
  insight,
  href,
}: {
  label: string
  insight: ResourceInsight
  href: string
}) {
  const total = insight.total.value

  return (
    <Link
      to={href}
      className="card flex flex-col gap-3 p-4 transition-colors hover:border-accent-line"
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="label">{label}</span>
        <span className="font-mono text-[20px] leading-none font-semibold text-fg tabular-nums">
          {total}
        </span>
      </div>

      {insight.segments.length > 0 ? (
        <div
          aria-hidden="true"
          className="flex h-1.5 overflow-hidden rounded-full bg-raised"
        >
          {insight.segments.map((segment) => (
            <span
              key={segment.id}
              className={TONE_FILL[segment.tone ?? 'idle']}
              style={{ width: `${Math.max(segment.share * 100, 1)}%` }}
            />
          ))}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-1.5">
        {insight.segments.length > 0 ? (
          insight.segments.map((segment) => (
            <Pill key={segment.id} tone={segment.tone ?? 'idle'}>
              {segment.value} {segment.label.toLowerCase()}
            </Pill>
          ))
        ) : (
          <span className="text-[12.5px] text-muted">{insight.headline}</span>
        )}
      </div>
    </Link>
  )
}

/**
 * What is wrong, named. The cards say how much of the cluster is in trouble; a
 * count is not something anybody can act on, and this is the row that is —
 * every line carries the cluster's own words for why it is here and links at
 * what the cluster recorded against that object, because "why" is answered by
 * its events rather than by the object.
 */
function AttentionPanel({ cluster, rows }: { cluster: Cluster; rows: Attention[] }) {
  if (rows.length === 0) {
    return (
      <Panel title="Needs attention" eyebrow="Now">
        <EmptyState
          icon={<Boxes aria-hidden="true" className="size-5" />}
          title="Everything you can see is running"
        >
          Every workload has the replicas it asked for and no pod is failing, pending or
          restarting in the namespaces granted to you.
        </EmptyState>
      </Panel>
    )
  }

  return (
    <Panel
      title="Needs attention"
      eyebrow="Now"
      description="What the cluster says is wrong, worst first. Each line opens the events the cluster recorded against that object."
    >
      <ul className="flex flex-col">
        {rows.map((row) => (
          <li
            key={`${row.kind}/${row.key}`}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line-soft px-4 py-2.5 last:border-b-0"
          >
            <Link
              to={eventsHref(cluster.id, row.namespace, row.kind, row.name)}
              className="min-w-0 truncate font-mono text-[13.5px] text-fg hover:text-accent hover:underline"
              title={`${row.namespace}/${row.name}`}
            >
              {row.name}
            </Link>
            <span className="font-mono text-[12px] text-faint">{row.namespace}</span>
            <span className="text-[12px] text-muted">{row.kind}</span>
            <span className="ml-auto shrink-0">
              <Pill tone={row.tone}>{row.reason}</Pill>
            </span>
          </li>
        ))}
      </ul>
    </Panel>
  )
}

/**
 * The history behind the cards.
 *
 * Which chart is honest depends on the grant: a namespace-scoped caller is
 * refused a cluster-wide query by the server — the scope rides on the query,
 * which is the whole reason the browser never sends one — so a scoped developer
 * is charted the namespace they hold rather than shown a refusal. With several
 * granted, the picker is how they move between them; with one, there is nothing
 * to pick.
 */
function Trend({ cluster }: { cluster: Cluster }) {
  const scoped = cluster.namespaces.length > 0
  const [namespace, setNamespace] = useState(cluster.namespaces[0] ?? '')

  if (!hasTunnel(cluster)) return null

  return (
    <section className="flex flex-col gap-3">
      {scoped && cluster.namespaces.length > 1 ? (
        <div className="max-w-xs">
          <Field
            htmlFor="summary-namespace"
            label="Namespace"
            hint="Which of your namespaces the charts below are about."
          >
            <Select
              id="summary-namespace"
              value={namespace}
              onChange={(event) => setNamespace(event.target.value)}
            >
              {cluster.namespaces.map((entry) => (
                <option key={entry} value={entry}>
                  {entry}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      ) : null}

      <MetricsChart
        cluster={cluster}
        title={scoped ? `CPU · ${namespace}` : 'Cluster CPU'}
        metric={scoped ? 'namespace_cpu' : 'cluster_cpu'}
        namespace={scoped ? namespace : undefined}
      />
      <MetricsChart
        cluster={cluster}
        title={scoped ? `Memory · ${namespace}` : 'Cluster memory'}
        metric={scoped ? 'namespace_memory' : 'cluster_memory'}
        namespace={scoped ? namespace : undefined}
      />
    </section>
  )
}

/**
 * Merge the four insights' alerts into one ranked list.
 *
 * Workloads come before pods and `bad` before `warn`, because a workload that
 * is short of replicas is the thing somebody owns, while the pods under it are
 * usually the same fault counted several times — and a pod nobody's workload
 * owns is exactly the case a workload-only list would miss. Each insight has
 * already ranked and capped its own alerts, so this orders what they named
 * rather than re-deriving anything.
 */
function rankAttention(
  cards: Array<{ label: string; kind: Workload['kind']; resource: string; insight: ResourceInsight }>,
  podInsight: ResourceInsight,
): Attention[] {
  const rows: Attention[] = []

  for (const card of cards) {
    for (const alert of card.insight.alerts) {
      rows.push({ ...alert, kind: card.kind, resource: card.resource })
    }
  }
  for (const alert of podInsight.alerts.slice(0, MAX_ALERTS)) {
    rows.push({ ...alert, kind: 'Pod', resource: 'pods' })
  }

  const worst = (row: Attention) => (row.tone === 'bad' ? 0 : 1)
  return rows
    .sort((a, b) => worst(a) - worst(b) || (a.kind === 'Pod' ? 1 : 0) - (b.kind === 'Pod' ? 1 : 0))
    .slice(0, MAX_ATTENTION)
}
