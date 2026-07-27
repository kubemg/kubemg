import { useCallback, useEffect, useState } from 'react'
import { Boxes, Layers, RefreshCw } from 'lucide-react'
import {
  errorMessage,
  fetchNamespaces,
  fetchPods,
  fetchWorkloads,
} from '../api/client'
import type { Namespace, Pod, Workload } from '../api/types'
import { AppShell } from '../components/AppShell'
import { PodDrawer } from '../components/PodDrawer'
import { Button, EnvironmentTag, Notice, Pill, Select } from '../components/primitives'
import type { PillTone } from '../components/primitives'
import { relativeAge } from '../lib/time'
import { useClusters } from '../state/clusters-context'

type Tab = 'workloads' | 'pods'

/** A pod's phase read as state, not just as a word. */
function phaseTone(pod: Pod): PillTone {
  if (pod.phase === 'Running' && pod.ready === pod.total) return 'ok'
  if (pod.phase === 'Succeeded') return 'neutral'
  if (pod.phase === 'Failed') return 'bad'
  return 'warn'
}

export function Explore() {
  const { clusters, loading: clustersLoading } = useClusters()

  const [clusterId, setClusterId] = useState<number | null>(null)
  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [namespace, setNamespace] = useState('')
  const [scoped, setScoped] = useState(false)
  const [tab, setTab] = useState<Tab>('pods')

  const [workloads, setWorkloads] = useState<Workload[]>([])
  const [pods, setPods] = useState<Pod[]>([])
  const [selected, setSelected] = useState<Pod | null>(null)

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Only agent clusters have a tunnel to read through; a direct-mode cluster
  // has no live state to show.
  const reachable = clusters.filter(
    (cluster) => cluster.connection_mode === 'agent' && cluster.agent_attached,
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null

  useEffect(() => {
    if (clusterId === null && reachable.length > 0) {
      setClusterId(reachable[0].id)
    }
  }, [clusterId, reachable])

  // Namespaces reload whenever the cluster changes; the current namespace is
  // dropped so it cannot leak across clusters.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setNamespace('')
    setNamespaces([])
    setError(null)

    fetchNamespaces(cluster.id)
      .then((result) => {
        if (cancelled) return
        setNamespaces(result.namespaces)
        setScoped(result.scoped)
        if (result.namespaces.length > 0) setNamespace(result.namespaces[0].name)
      })
      .catch((err) => {
        if (!cancelled) setError(errorMessage(err, 'Could not list namespaces.'))
      })

    return () => {
      cancelled = true
    }
  }, [cluster])

  const load = useCallback(async () => {
    if (!cluster || !namespace) return

    setLoading(true)
    try {
      const [nextWorkloads, nextPods] = await Promise.all([
        fetchWorkloads(cluster.id, namespace),
        fetchPods(cluster.id, namespace),
      ])
      setWorkloads(nextWorkloads)
      setPods(nextPods)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read resources from this cluster.'))
      setWorkloads([])
      setPods([])
    } finally {
      setLoading(false)
    }
  }, [cluster, namespace])

  useEffect(() => {
    void load()
  }, [load])

  if (!clustersLoading && reachable.length === 0) {
    return (
      <AppShell title="Explore">
        <div className="panel px-3 py-12 text-center">
          <p className="text-[13px] text-fg">No cluster is reachable right now</p>
          <p className="mx-auto mt-1 max-w-md text-[12px] leading-relaxed text-muted">
            Live resources are read on demand through an agent tunnel. Register a cluster in
            agent mode and wait for its agent to connect.
          </p>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell
      title="Explore"
      actions={
        <Button onClick={() => void load()} disabled={loading || !namespace}>
          <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <div className="panel flex flex-wrap items-center gap-2 px-3 py-2">
          <Select
            aria-label="Cluster"
            value={clusterId ?? ''}
            onChange={(event) => setClusterId(Number(event.target.value))}
            className="w-auto py-1 text-[12px]"
          >
            {reachable.map((entry) => (
              <option key={entry.id} value={entry.id}>
                {entry.name}
              </option>
            ))}
          </Select>

          {cluster ? <EnvironmentTag environment={cluster.environment} /> : null}

          <Select
            aria-label="Namespace"
            value={namespace}
            onChange={(event) => setNamespace(event.target.value)}
            disabled={namespaces.length === 0}
            className="w-auto py-1 text-[12px]"
          >
            {namespaces.length === 0 ? <option value="">No namespaces</option> : null}
            {namespaces.map((entry) => (
              <option key={entry.name} value={entry.name}>
                {entry.name}
              </option>
            ))}
          </Select>

          {scoped ? (
            <Pill tone="accent" dot={false}>
              scoped to your grant
            </Pill>
          ) : null}

          <div className="ml-auto flex items-center gap-1">
            <TabButton
              active={tab === 'pods'}
              onClick={() => setTab('pods')}
              icon={Boxes}
              label={`Pods (${pods.length})`}
            />
            <TabButton
              active={tab === 'workloads'}
              onClick={() => setTab('workloads')}
              icon={Layers}
              label={`Workloads (${workloads.length})`}
            />
          </div>
        </div>

        <div className="panel min-w-0 overflow-hidden">
          <div className="overflow-x-auto">
            {tab === 'pods' ? (
              <PodTable pods={pods} onSelect={setSelected} />
            ) : (
              <WorkloadTable workloads={workloads} />
            )}
          </div>

          {loading ? (
            <p className="px-3 py-6 text-center text-[12px] text-muted">Reading the cluster…</p>
          ) : null}

          {!loading && namespace && (tab === 'pods' ? pods.length : workloads.length) === 0 ? (
            <p className="px-3 py-10 text-center text-[12px] text-muted">
              Nothing in {namespace}.
            </p>
          ) : null}
        </div>

        <p className="text-[11.5px] text-muted">
          Read live through the agent tunnel under your own identity — the cluster&rsquo;s RBAC
          decides what comes back, and every read is in the audit trail.
        </p>
      </div>

      {selected && cluster ? (
        <PodDrawer
          cluster={cluster}
          pod={selected}
          onClose={() => setSelected(null)}
          onRefresh={load}
        />
      ) : null}
    </AppShell>
  )
}

function TabButton({
  active,
  onClick,
  icon: Icon,
  label,
}: {
  active: boolean
  onClick: () => void
  icon: typeof Boxes
  label: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`inline-flex items-center gap-1.5 rounded-[5px] border px-2 py-1 text-[12px] transition-colors ${
        active
          ? 'border-primary/40 bg-primary-soft font-medium text-primary'
          : 'border-line bg-surface text-muted hover:text-fg'
      }`}
    >
      <Icon aria-hidden="true" className="size-3.5" />
      {label}
    </button>
  )
}

function PodTable({ pods, onSelect }: { pods: Pod[]; onSelect: (pod: Pod) => void }) {
  return (
    <table className="w-full table-fixed border-collapse text-[13px]">
      <thead>
        <tr className="border-b border-line">
          <th className="w-[3px] p-0">
            <span className="sr-only">State</span>
          </th>
          <th className="label w-[44%] px-3 py-2 text-left md:w-[30%]">Name</th>
          <th className="label w-[20%] px-3 py-2 text-left md:w-[13%]">Status</th>
          <th className="label w-[14%] px-3 py-2 text-left md:w-[8%]">Ready</th>
          <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[9%]">Restarts</th>
          <th className="label hidden px-3 py-2 text-left lg:table-cell lg:w-[20%]">Node</th>
          <th className="label w-[22%] px-3 py-2 text-left md:w-[10%]">Age</th>
        </tr>
      </thead>
      <tbody>
        {pods.map((pod) => (
          <tr
            key={pod.name}
            className="border-b border-line-soft transition-colors last:border-0 hover:bg-raised"
          >
            <td className="p-0">
              <span
                aria-hidden="true"
                className={`block h-8 w-[3px] rounded-r-[2px] ${
                  pod.phase === 'Running' && pod.ready === pod.total
                    ? 'bg-ok'
                    : pod.phase === 'Failed'
                      ? 'bg-danger'
                      : 'bg-warn'
                }`}
              />
            </td>
            <td className="truncate px-3 py-2">
              <button
                type="button"
                onClick={() => onSelect(pod)}
                className="truncate font-mono text-fg transition-colors hover:text-primary"
                title={pod.name}
              >
                {pod.name}
              </button>
            </td>
            <td className="px-3 py-2">
              <Pill tone={phaseTone(pod)}>{pod.phase}</Pill>
            </td>
            <td className="px-3 py-2 font-mono text-[12px] text-muted tabular-nums">
              {pod.ready}/{pod.total}
            </td>
            <td
              className={`hidden px-3 py-2 font-mono text-[12px] tabular-nums md:table-cell ${
                pod.restarts > 0 ? 'text-warn' : 'text-muted'
              }`}
            >
              {pod.restarts}
            </td>
            <td className="hidden truncate px-3 py-2 font-mono text-[12px] text-muted lg:table-cell">
              {pod.node || '—'}
            </td>
            <td className="px-3 py-2 text-[12px] text-muted">{relativeAge(pod.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function WorkloadTable({ workloads }: { workloads: Workload[] }) {
  return (
    <table className="w-full table-fixed border-collapse text-[13px]">
      <thead>
        <tr className="border-b border-line">
          <th className="w-[3px] p-0">
            <span className="sr-only">State</span>
          </th>
          <th className="label w-[44%] px-3 py-2 text-left md:w-[28%]">Name</th>
          <th className="label w-[22%] px-3 py-2 text-left md:w-[13%]">Kind</th>
          <th className="label w-[16%] px-3 py-2 text-left md:w-[9%]">Ready</th>
          <th className="label hidden px-3 py-2 text-left lg:table-cell lg:w-[34%]">Image</th>
          <th className="label w-[18%] px-3 py-2 text-left md:w-[10%]">Age</th>
        </tr>
      </thead>
      <tbody>
        {workloads.map((workload) => (
          <tr
            key={`${workload.kind}/${workload.name}`}
            className="border-b border-line-soft transition-colors last:border-0 hover:bg-raised"
          >
            <td className="p-0">
              <span
                aria-hidden="true"
                className={`block h-8 w-[3px] rounded-r-[2px] ${
                  workload.ready === workload.desired && workload.desired > 0
                    ? 'bg-ok'
                    : workload.ready === 0
                      ? 'bg-danger'
                      : 'bg-warn'
                }`}
              />
            </td>
            <td className="truncate px-3 py-2 font-mono text-fg" title={workload.name}>
              {workload.name}
            </td>
            <td className="px-3 py-2 text-[12px] text-muted">{workload.kind}</td>
            <td
              className={`px-3 py-2 font-mono text-[12px] tabular-nums ${
                workload.ready === workload.desired ? 'text-muted' : 'text-warn'
              }`}
            >
              {workload.ready}/{workload.desired}
            </td>
            <td
              className="hidden truncate px-3 py-2 font-mono text-[11.5px] text-muted lg:table-cell"
              title={workload.images.join(', ')}
            >
              {workload.images[0] ?? '—'}
            </td>
            <td className="px-3 py-2 text-[12px] text-muted">{relativeAge(workload.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
