import { Suspense, lazy, useCallback, useEffect, useState } from 'react'
import { ExternalLink, RefreshCw, RotateCcw, SlidersHorizontal } from 'lucide-react'
import { errorMessage, fetchResourceDescribe } from '../api/client'
import type {
  Cluster,
  HelmRelease,
  K8sEvent,
  Pod,
  ResourceCondition,
  ResourceDescribeResult,
  ResourceField,
} from '../api/types'
import { ARGO_INSTANCE_LABEL, argoApplicationHref, useClusterConsole } from '../lib/consoles'
import type { ResourceKey } from '../lib/resources'
import type { Tone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { HelmHistoryPanel } from './HelmHistoryPanel'
import { HelmValuesPanel } from './HelmValuesPanel'
import { LogExplorer } from './LogExplorer'
import { PodLogView, PodOverview } from './PodPanels'
import { WorkloadActionPanel } from './WorkloadActionPanel'
import type { WorkloadActionName, WorkloadActionTarget } from './WorkloadActionPanel'
import { supportsWorkloadLogs, workloadCapability } from '../lib/workloads'
import { WorkloadLogView } from './WorkloadLogView'
import { YamlPanel } from './YamlPanel'
import {
  Button,
  DetailList,
  EmptyState,
  Notice,
  Pill,
  Row,
  Segmented,
  Select,
  Sheet,
  Table,
  Td,
  Th,
} from './primitives'

// The terminal emulator is by far the heaviest thing in the app and most
// sessions never open one, so it is fetched on demand rather than shipped to
// everyone who loads the console.
const PodTerminal = lazy(() =>
  import('./PodTerminal').then((module) => ({ default: module.PodTerminal })),
)

/**
 * Which face of the object the drawer opens on. `values` and `history` are the
 * Helm ones and appear only for a release, which has no manifest for `yaml` to
 * address and no API object for `describe` to read.
 */
export type DetailTab = 'overview' | 'describe' | 'yaml' | 'logs' | 'values' | 'history'

/** Which stream the logs tab is showing. */
type StreamView = 'logs' | 'history' | 'terminal'

/** DetailTarget is one object, addressed the way the sidebar addresses it. */
export interface DetailTarget {
  kind: ResourceKey
  /** The singular label for this kind, for the drawer's eyebrow. */
  label: string
  name: string
  namespace?: string
  /**
   * The row, when the row was a pod. A pod is not a special kind of object —
   * it opens in this same drawer — but there is more to show for one, and the
   * list already holds everything the usage and container panels need without a
   * second read.
   */
  pod?: Pod
  /**
   * The row, when the row was a Helm release. A release is not an API object —
   * it is a Secret holding a compressed blob — so it has neither a manifest nor
   * a describe, and its values are the whole of what there is to show. It opens
   * here anyway rather than in a drawer of its own: reaching a release and
   * reaching a Deployment should not be two different motions.
   */
  release?: HelmRelease
  tab?: DetailTab
  /** Opens the YAML or values tab ready to type, for the row's *Edit* action. */
  editing?: boolean
  /**
   * A workload write to open on, for the row's Scale and Restart actions. The
   * object is shown either way — the action is a panel inside this drawer, not
   * a surface instead of it.
   */
  action?: WorkloadActionName
}

/**
 * One object, in every form KubeMG can show it, and the writes that act on it.
 *
 * Before this there were three drawers — a pod's, a manifest's, and nothing at
 * all for the rest — and moving between them meant closing one and finding the
 * row again. They are tabs now, over one object, because that is what an
 * operator is actually doing: seeing that something is not ready, asking why,
 * and then changing it. The three questions are one investigation.
 *
 * The same argument then absorbed the last two overlays. A Helm release's
 * values are a tab here rather than a drawer of their own, and a workload's
 * Scale and Restart are a panel inside this one rather than a dialog stacked
 * over it — an overlay on top of the surface showing the object hid the
 * conditions and events that are the reason anyone reached for the action.
 * Nothing in the console opens over anything else any more, which is why
 * `Sheet` no longer needs a rule about which instance answers Escape.
 *
 * It opens `wide` rather than `lg` for the same reason: the content here is
 * tables and manifests, where the constraint is the line rather than the form,
 * and a 680px column turns an event message into six wrapped rows.
 *
 * Everything on every tab is read through the agent tunnel under the caller's
 * own identity — the cluster's RBAC decides what comes back, and every read is
 * in the audit trail.
 */
export function ResourceDetailDrawer({
  cluster,
  target,
  onClose,
  onRefresh,
}: {
  cluster: Cluster
  target: DetailTarget
  onClose: () => void
  onRefresh?: () => Promise<void> | void
}) {
  const release = target.release
  // A release's two faces are not the object tabs, so an inherited `overview`
  // would land on a tab it does not have; anything else the row asked for is
  // honoured, which is how a History row action opens on the history.
  const [tab, setTab] = useState<DetailTab>(
    release
      ? target.tab === 'history'
        ? 'history'
        : 'values'
      : (target.tab ?? 'overview'),
  )
  const [describe, setDescribe] = useState<ResourceDescribeResult | null>(null)
  const [loading, setLoading] = useState(!release)
  const [error, setError] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  const pod = target.pod
  const [container, setContainer] = useState(pod?.containers[0]?.name ?? '')
  // Three streams, not two. `logs` tails this pod live through the tunnel;
  // `history` searches what the cluster's aggregator kept, which is the only one
  // of the three that still answers after the pod is gone.
  const [shell, setShell] = useState<StreamView>('logs')

  // The two workload writes, offered where the object is being read — as a
  // panel at the top of this drawer's body rather than a surface over it.
  const [action, setAction] = useState<WorkloadActionName | null>(target.action ?? null)
  const capability = workloadCapability(target.kind)

  /*
   * A workload's logs are its pods' logs. Almost nothing anyone asks of a log is
   * about one pod — a Deployment fails on one replica out of ten, and finding
   * which one meant opening ten drawers — so a workload gets the same tab, backed
   * by the pooled view instead of the single-pod one. It needs a namespace,
   * because a pod set is resolved inside one.
   */
  const workloadLogs = !pod && !!target.namespace && supportsWorkloadLogs(target.kind)

  const load = useCallback(async () => {
    // There is no object to describe for a Helm release, and asking would be a
    // read of a kind the cluster does not serve.
    if (release) return
    setLoading(true)
    try {
      setDescribe(await fetchResourceDescribe(cluster.id, target.kind, target.name, target.namespace))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not describe this object.'))
      setDescribe(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, target.kind, target.name, target.namespace, release])

  // The describe read backs both the overview and the events tab, so it happens
  // once when the drawer opens rather than on every tab switch. The YAML tab
  // reads separately because it is the only one that needs the whole object.
  useEffect(() => {
    void load()
  }, [load])

  // Closing on a half-typed manifest or set of values throws the edit away, so
  // it asks first. Escape reaches the same guard, because the Sheet closes
  // through it too.
  const close = useCallback(() => {
    if (dirty && !window.confirm('Discard your unsaved changes?')) return
    onClose()
  }, [dirty, onClose])

  const tabs: Array<{ value: DetailTab; label: string; count?: number }> = release
    ? // A release has two faces: what it is set to, and what it has been. The
      // second is the only place a rollback can be offered from, since choosing
      // one means reading the revisions to choose between.
      [
        { value: 'values', label: 'Values' },
        { value: 'history', label: 'History' },
      ]
    : [
        { value: 'overview', label: 'Overview' },
        {
          value: 'describe',
          label: 'Describe & Events',
          count: describe?.events_available ? describe.events.length : undefined,
        },
        { value: 'yaml', label: 'YAML' },
      ]
  if (!release && pod) tabs.push({ value: 'logs', label: 'Logs & Terminal' })
  // A workload has no terminal and no history of its own — there is no one pod to
  // attach to, and the history view searches by pod — so the tab is named for
  // what it holds.
  else if (!release && workloadLogs) tabs.push({ value: 'logs', label: 'Logs' })

  // What the object says it is running, for the scale dialog's prefill. It comes
  // from the describe already on screen rather than from a read of its own.
  const replicas = numericField(describe?.spec_summary, 'replicas')

  const argocd = useClusterConsole(cluster.id, 'argocd')
  const argoApp = argocd
    ? argoApplicationHref(argocd.url, describe?.labels?.[ARGO_INSTANCE_LABEL] ?? '')
    : ''

  const actionTarget: WorkloadActionTarget | null = action
    ? {
        action,
        kind: target.kind,
        label: describe?.kind || target.label,
        name: target.name,
        namespace: target.namespace,
        replicas,
      }
    : null

  return (
    <Sheet
      width="wide"
      eyebrow={`${cluster.name}${target.namespace ? ` · ${target.namespace}` : ''} · ${target.label}`}
      title={<span className="font-mono">{target.name}</span>}
      onClose={close}
      footer={
        <>
          {/* A workload's two writes sit with the object rather than only in the
              list it came from: seeing that something is not ready and rolling
              it is one investigation. They open a panel in the body above,
              which is why they read as pressed while one is open. */}
          {capability?.scale ? (
            <Button
              type="button"
              variant={action === 'scale' ? 'primary' : 'secondary'}
              onClick={() => setAction(action === 'scale' ? null : 'scale')}
            >
              <SlidersHorizontal aria-hidden="true" className="size-4" />
              Scale
            </Button>
          ) : null}
          {capability?.restart ? (
            <Button
              type="button"
              variant={action === 'restart' ? 'primary' : 'secondary'}
              onClick={() => setAction(action === 'restart' ? null : 'restart')}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Restart
            </Button>
          ) : null}

          <Button type="button" variant="ghost" onClick={close}>
            Close
          </Button>
          {/* A release refreshes from its own panel, which is the only thing
              there is to re-read for one. */}
          {release ? null : (
            <Button type="button" onClick={() => void load()} disabled={loading}>
              <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
              Refresh
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-wrap items-center gap-3">
        {tabs.length > 1 ? (
          <Segmented<DetailTab> ariaLabel="Resource view" value={tab} onChange={setTab} options={tabs} />
        ) : null}

        {describe?.kind ? (
          <Pill tone="idle" dot={false}>
            {describe.kind}
          </Pill>
        ) : null}
        {/* What deployed this. A workload that keeps reverting was reverted by
            something, and the label Argo writes on everything it owns is the one
            thing needed to open the application that owns it. A path, so it is
            built here — unlike Grafana's Explore link, which carries a query and
            is therefore always the server's to write. */}
        {argoApp ? (
          <a
            href={argoApp}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1.5 text-[12px] text-muted transition-colors hover:text-fg"
            title="Open the Argo CD application that owns this object"
          >
            <ExternalLink aria-hidden="true" className="size-3.5" />
            Argo CD
          </a>
        ) : null}

        {/* A failing condition is the headline: it is the object saying, in its
            own words, that it is not what it was asked to be. */}
        {failing(describe?.conditions).map((condition) => (
          <Pill key={condition.type} tone="bad">
            {condition.type}: {condition.status}
          </Pill>
        ))}

        {/* The container picker only applies to the streams, and only where
            there is more than one container to pick between. */}
        {pod && pod.containers.length > 1 && tab === 'logs' ? (
          <div className="ml-auto w-44">
            <Select
              aria-label="Container"
              size="sm"
              value={container}
              onChange={(event) => setContainer(event.target.value)}
            >
              {pod.containers.map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>
        ) : null}
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}

      {/* The pending write, above whichever tab is open rather than over it:
          the events and conditions that are the reason for acting stay on
          screen while the action is confirmed. */}
      {actionTarget ? (
        <WorkloadActionPanel
          cluster={cluster}
          target={actionTarget}
          onClose={() => setAction(null)}
          onDone={async () => {
            // Both surfaces are now stale: this drawer's describe, and the list
            // it was opened from.
            await load()
            await onRefresh?.()
          }}
        />
      ) : null}

      {tab === 'history' && release ? (
        <HelmHistoryPanel cluster={cluster} release={release} onApplied={onRefresh} />
      ) : null}

      {tab === 'values' && release ? (
        <HelmValuesPanel
          cluster={cluster}
          release={release}
          editing={target.editing}
          onDirtyChange={setDirty}
          onApplied={onRefresh}
        />
      ) : null}

      {tab === 'overview' ? (
        <OverviewTab cluster={cluster} pod={pod} describe={describe} loading={loading} />
      ) : null}

      {tab === 'describe' ? <DescribeTab describe={describe} loading={loading} /> : null}

      {tab === 'yaml' ? (
        <YamlPanel
          cluster={cluster}
          kind={target.kind}
          name={target.name}
          namespace={target.namespace}
          editing={target.editing}
          onDirtyChange={setDirty}
          onApplied={async () => {
            await load()
            await onRefresh?.()
          }}
        />
      ) : null}

      {tab === 'logs' && pod ? (
        <div className="flex min-h-0 flex-1 flex-col gap-3">
          <Segmented<StreamView>
            ariaLabel="Stream"
            value={shell}
            onChange={setShell}
            options={[
              { value: 'logs', label: 'Live' },
              { value: 'history', label: 'History' },
              { value: 'terminal', label: 'Terminal' },
            ]}
          />
          {shell === 'logs' ? (
            <PodLogView cluster={cluster} pod={pod} container={container} />
          ) : null}
          {shell === 'history' ? (
            <LogExplorer
              cluster={cluster}
              namespace={pod.namespace}
              pod={pod.name}
              container={container}
            />
          ) : null}
          {shell === 'terminal' ? (
            <Suspense fallback={<p className="text-[13px] text-muted">Loading the terminal…</p>}>
              <PodTerminal
                clusterId={cluster.id}
                namespace={pod.namespace}
                pod={pod.name}
                container={container}
              />
            </Suspense>
          ) : null}
        </div>
      ) : null}

      {tab === 'logs' && !pod && workloadLogs && target.namespace ? (
        <WorkloadLogView
          cluster={cluster}
          kind={target.kind}
          name={target.name}
          namespace={target.namespace}
          label={describe?.kind || target.label}
        />
      ) : null}
    </Sheet>
  )
}

/**
 * numericField reads one value out of a flattened describe. The summary renders
 * every value as text, so a count comes back as one — and a field that was
 * truncated away, or that this kind does not have, is simply absent.
 */
function numericField(fields: ResourceField[] | undefined, path: string): number | undefined {
  const found = fields?.find((field) => field.path === path)
  if (!found) return undefined
  const value = Number(found.value)
  return Number.isInteger(value) ? value : undefined
}

/** failing picks out the conditions an operator needs to see before anything else. */
function failing(conditions: ResourceCondition[] | undefined): ResourceCondition[] {
  return (conditions ?? []).filter(
    (condition) =>
      // A `Ready`/`Available` that is not True is a problem; a `Failed` or a
      // `Pressure` that *is* True is the same problem stated the other way up.
      (condition.status === 'False' && !NEGATIVE_CONDITIONS.test(condition.type)) ||
      (condition.status === 'True' && NEGATIVE_CONDITIONS.test(condition.type)),
  )
}

/**
 * Conditions whose healthy value is False rather than True. Kubernetes has no
 * flag for this — the polarity is a naming convention — so it is matched by name
 * and kept deliberately short: a condition this does not know about is treated
 * as positive, which fails towards saying nothing rather than towards crying
 * wolf on every CRD that invents a condition type.
 */
const NEGATIVE_CONDITIONS = /^(Failed|.*Pressure|NetworkUnavailable|Degraded)$/

function OverviewTab({
  cluster,
  pod,
  describe,
  loading,
}: {
  cluster: Cluster
  pod?: Pod
  describe: ResourceDescribeResult | null
  loading: boolean
}) {
  if (loading && !describe) return <p className="text-[13px] text-muted">Reading the object…</p>
  if (!describe) return null

  /*
   * The order is what an operator reads in: the facts first, then the numbers,
   * then health, and only then the wiring. Labels and annotations are last on
   * purpose — a chatty object has thirty of them, and putting them above the
   * usage meters buries the reading somebody opened the drawer for behind a
   * wall of chips.
   *
   * A pod's own facts — node, IP, ready, restarts — come from PodOverview, which
   * also carries the namespace and the age. So those two are dropped from the
   * identity list when there is a pod, rather than being printed twice a few
   * pixels apart.
   */
  const identity = [
    { term: 'Kind', value: describe.kind || '—' },
    { term: 'API version', value: describe.api_version || '—' },
  ]
  if (!pod) {
    identity.push(
      { term: 'Namespace', value: describe.namespace || 'cluster-scoped' },
      { term: 'Age', value: describe.created_at ? relativeAge(describe.created_at) : '—' },
    )
  }

  return (
    <>
      <DetailList columns={2} rows={identity} />

      {/* A pod has more to say than any other kind: what it is scheduled on,
          what each container is using against its own limit, and how often it
          has restarted. The list row already carries all of it. */}
      {pod ? <PodOverview cluster={cluster} pod={pod} /> : null}

      {describe.conditions.length > 0 ? <Conditions conditions={describe.conditions} /> : null}

      <KeyValues title="Labels" values={describe.labels} />
      <KeyValues title="Annotations" values={describe.annotations} />
    </>
  )
}

function Conditions({ conditions }: { conditions: ResourceCondition[] }) {
  return (
    <div className="flex flex-col gap-2">
      <span className="label">Conditions</span>
      <div className="overflow-x-auto rounded-card border border-line">
        <Table>
          <thead>
            <tr>
              <Th className="w-[22%]">Type</Th>
              <Th className="w-[12%]">Status</Th>
              <Th className="hidden w-[22%] md:table-cell">Reason</Th>
              <Th className="w-[32%]">Message</Th>
              <Th className="w-[12%]">Changed</Th>
            </tr>
          </thead>
          <tbody>
            {conditions.map((condition) => {
              const tone = conditionTone(condition)
              return (
                <Row key={condition.type}>
                  <Td className="truncate font-mono text-[12.5px] text-fg">{condition.type}</Td>
                  <Td>
                    <Pill tone={tone}>{condition.status}</Pill>
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                    {condition.reason || '—'}
                  </Td>
                  <Td className="text-[12.5px] text-muted" title={condition.message}>
                    {condition.message || '—'}
                  </Td>
                  <Td className="text-[12.5px] text-muted">
                    {condition.last_transition_at ? relativeAge(condition.last_transition_at) : '—'}
                  </Td>
                </Row>
              )
            })}
          </tbody>
        </Table>
      </div>
    </div>
  )
}

function conditionTone(condition: ResourceCondition): Tone {
  const negative = NEGATIVE_CONDITIONS.test(condition.type)
  if (condition.status === 'Unknown') return 'warn'
  const healthy = negative ? condition.status === 'False' : condition.status === 'True'
  return healthy ? 'ok' : 'bad'
}

/**
 * Labels and annotations, as the pairs they are. They are how objects are wired
 * to each other — a selector matching a label is most of how Kubernetes works —
 * so they are read far more often than their place in a manifest suggests.
 */
function KeyValues({ title, values }: { title: string; values?: Record<string, string> }) {
  const entries = Object.entries(values ?? {}).sort(([a], [b]) => a.localeCompare(b))
  if (entries.length === 0) return null

  return (
    <div className="flex flex-col gap-2">
      <span className="label">
        {title} <span className="text-faint">{entries.length}</span>
      </span>
      <ul className="flex flex-wrap gap-1.5">
        {entries.map(([key, value]) => (
          <li
            key={key}
            className="flex max-w-full min-w-0 items-center gap-1 rounded-chip border border-line bg-raised px-2 py-1 font-mono text-[12px]"
            title={`${key}: ${value}`}
          >
            <span className="shrink-0 text-muted">{key}</span>
            <span className="truncate text-fg">{value || '—'}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function DescribeTab({
  describe,
  loading,
}: {
  describe: ResourceDescribeResult | null
  loading: boolean
}) {
  if (loading && !describe) return <p className="text-[13px] text-muted">Reading the object…</p>
  if (!describe) return null

  return (
    <>
      <Events describe={describe} />
      <Fields
        title="Spec"
        fields={describe.spec_summary}
        truncated={describe.spec_truncated ?? false}
      />
      <Fields
        title="Status"
        fields={describe.status_summary}
        truncated={describe.status_truncated ?? false}
      />
    </>
  )
}

/**
 * The events the cluster recorded against this object — the reason this whole
 * surface exists. A spec is what was asked for and a status says whether it
 * happened; only an event says why it did not.
 *
 * Newest first: in a drawer the question is what just happened. kubectl orders
 * the other way because it is printing a log, and this is not one.
 */
function Events({ describe }: { describe: ResourceDescribeResult }) {
  if (!describe.events_available) {
    return (
      <Notice tone="info">
        {describe.events_reason ?? 'The events for this object could not be read.'}
      </Notice>
    )
  }

  if (describe.events.length === 0) {
    return (
      <EmptyState title="No events">
        The cluster has nothing recorded against this object. Kubernetes keeps events for about an
        hour, so a quiet object and an old one look the same here.
      </EmptyState>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <span className="label">
        Events <span className="text-faint">{describe.events.length}</span>
      </span>
      <div className="overflow-x-auto rounded-card border border-line">
        <Table>
          <thead>
            <tr>
              <Th className="w-[10%]">Type</Th>
              <Th className="w-[18%]">Reason</Th>
              <Th className="w-[42%]">Message</Th>
              <Th className="hidden w-[16%] lg:table-cell">Source</Th>
              <Th className="w-[7%]">Count</Th>
              <Th className="w-[13%]">Last seen</Th>
            </tr>
          </thead>
          <tbody>
            {describe.events.map((event, index) => (
              <Row key={`${event.reason}/${event.last_seen ?? index}`}>
                <Td>
                  <Pill tone={eventTone(event)}>{event.type || 'Normal'}</Pill>
                </Td>
                <Td className="truncate font-mono text-[12.5px] text-fg">{event.reason || '—'}</Td>
                <Td className="text-[12.5px] text-muted" title={event.message}>
                  {event.message || '—'}
                </Td>
                <Td className="hidden truncate font-mono text-[12.5px] text-muted lg:table-cell">
                  {event.source || '—'}
                </Td>
                <Td
                  className={`font-mono text-[12.5px] ${event.count > 1 ? 'text-warn' : 'text-muted'}`}
                >
                  {event.count}
                </Td>
                <Td className="text-[12.5px] text-muted">
                  {event.last_seen ? relativeAge(event.last_seen) : '—'}
                </Td>
              </Row>
            ))}
          </tbody>
        </Table>
      </div>
    </div>
  )
}

function eventTone(event: K8sEvent): Tone {
  return event.type === 'Warning' ? 'warn' : 'idle'
}

/**
 * A flattened spec or status. KubeMG has no per-kind describer — kubectl writes
 * one by hand for every kind, which would be the wrong shape here and impossible
 * for a CRD nobody has heard of — so this is the fields that fit on a line, and
 * it says plainly when there were more of them than fit.
 */
function Fields({
  title,
  fields,
  truncated,
}: {
  title: string
  fields: ResourceField[]
  truncated: boolean
}) {
  if (fields.length === 0) return null

  return (
    <div className="flex flex-col gap-2">
      <span className="label">{title}</span>
      <dl className="grid gap-x-6 gap-y-2 rounded-card border border-line px-3 py-3 sm:grid-cols-2">
        {fields.map((field) => (
          <div key={field.path} className="flex min-w-0 items-baseline gap-2">
            <dt className="shrink-0 font-mono text-[12px] text-muted">{field.path}</dt>
            <dd className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-fg" title={field.value}>
              {field.value}
            </dd>
          </div>
        ))}
      </dl>
      {truncated ? (
        <p className="text-[12px] text-muted">
          Only the first fields are summarised here — the YAML tab has the whole object.
        </p>
      ) : null}
    </div>
  )
}
