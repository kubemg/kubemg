import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import type {
  ClusterNode,
  ClusterRoleEntry,
  ConfigEntry,
  CronJob,
  CustomResource,
  CustomResourceDefinition,
  HelmRelease,
  Ingress,
  Job,
  Namespace,
  NetworkPolicy,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  PodUsage,
  RoleBindingEntry,
  Route,
  Service,
  ServiceAccountEntry,
  StorageClass,
  Workload,
} from '../api/types'
import { FileCode2, History, PanelRightOpen, Pencil, RotateCcw, SlidersHorizontal } from 'lucide-react'
import { IconButton, Pill, Row, RowMenu, RowMenuItem, SortTh, Table, Td, Th } from './primitives'
import type { DetailTab } from './ResourceDetailDrawer'
import type { WorkloadActionName } from './WorkloadActionPanel'
import { workloadCapability, workloadKeyFor } from '../lib/workloads'
import type { Tone } from '../lib/status'
import { TONE_FILL, podTone, workloadTone } from '../lib/status'
import { formatCountdown, relativeAge, secondsUntil, useTicker } from '../lib/time'
import { formatCPU, formatMemory, podLimit, ratio, usageTone } from '../lib/units'
import type { PodUsageIndex } from '../lib/units'

/**
 * One loaded resource list, tagged by the shape it came back in. The tag is what
 * lets the page hand a list straight to the right table without casting: the
 * loader produces it, ResourceView consumes it, and the compiler checks the two
 * agree.
 */
export type LoadedResource =
  /**
   * A pod list carries the live usage of the same scope alongside it. It is part
   * of the loaded list rather than something the table fetches because the two
   * reads have to describe the same set of namespaces to line up row for row,
   * and only the loader knows what scope it asked for. `usage: null` is a real
   * answer — no metrics-server, or a grant that may not read metrics.k8s.io —
   * and reads as a dash with the cluster's own reason, not as a failed list.
   */
  | { kind: 'pods'; rows: Pod[]; usage: PodUsageIndex | null; usageReason?: string }
  | { kind: 'helmreleases'; rows: HelmRelease[] }
  | { kind: 'workloads'; rows: Workload[] }
  | { kind: 'jobs'; rows: Job[] }
  | { kind: 'cronjobs'; rows: CronJob[] }
  | { kind: 'services'; rows: Service[] }
  | { kind: 'ingresses'; rows: Ingress[] }
  | { kind: 'networkpolicies'; rows: NetworkPolicy[] }
  | { kind: 'routes'; rows: Route[]; available: boolean; reason?: string }
  | { kind: 'persistentvolumes'; rows: PersistentVolume[] }
  | { kind: 'persistentvolumeclaims'; rows: PersistentVolumeClaim[] }
  | { kind: 'storageclasses'; rows: StorageClass[] }
  | { kind: 'config'; rows: ConfigEntry[]; secrets: boolean }
  | { kind: 'crds'; rows: CustomResourceDefinition[] }
  /**
   * Roles and ClusterRoles share a row shape because they share a definition —
   * the same rules, differing only in whether a namespace bounds them — and
   * `clusterScoped` is what the table needs to know to drop the namespace
   * qualifier and say so in the heading.
   */
  | { kind: 'roles'; rows: ClusterRoleEntry[]; clusterScoped: boolean }
  | { kind: 'rolebindings'; rows: RoleBindingEntry[]; clusterScoped: boolean }
  | { kind: 'serviceaccounts'; rows: ServiceAccountEntry[] }
  | { kind: 'custom'; rows: CustomResource[]; available: boolean; reason?: string }
  | { kind: 'nodes'; rows: ClusterNode[] }
  | { kind: 'namespaces'; rows: Namespace[] }

/**
 * OpenManifest opens one row's object in the detail drawer, on a named tab. The
 * name of the type is what it has always been because every table calls it the
 * same way; what changed is that a row now opens onto the whole object rather
 * than onto its manifest alone.
 */
export type OpenManifest = (
  name: string,
  namespace: string | undefined,
  tab: DetailTab,
  editing?: boolean,
) => void

/**
 * OpenValues opens one Helm release. It is separate from OpenManifest because a
 * release has no manifest of its own: it is a Secret holding a blob, and what is
 * worth reading is either the values inside it or the revisions it has been
 * through — which is why it carries a tab rather than only an editing flag.
 */
export type OpenValues = (
  release: HelmRelease,
  tab: 'values' | 'history',
  editing?: boolean,
) => void

/**
 * OpenWorkloadAction asks for one of the two workload writes on a row. It is
 * separate from OpenManifest because it is not a view of the object — it is a
 * change to it — and because the row, not the page, is the only thing that knows
 * which Kind it is: the workload table serves Deployments, StatefulSets and
 * DaemonSets at once, and only two of the three can be scaled.
 */
export type OpenWorkloadAction = (action: WorkloadActionName, workload: Workload) => void

/**
 * ResourceView renders whichever list is loaded, with the columns it deserves.
 * `showNamespace` is set when the list spans namespaces, and each namespaced
 * table then prefixes the name with where the object lives.
 *
 * `onManifest` arrives already bound to the kind being shown: several kinds
 * share a shape here — HTTPRoutes and VirtualServices are both `routes` — so the
 * page, which knows which resource it asked for, is the only place that can
 * address a manifest correctly.
 */
export function ResourceView({
  loaded,
  showNamespace = false,
  onSelectPod,
  onManifest: open,
  onValues,
  onAction,
}: {
  loaded: LoadedResource
  showNamespace?: boolean
  onSelectPod: (pod: Pod) => void
  onManifest?: OpenManifest
  onValues?: OpenValues
  onAction?: OpenWorkloadAction
}) {
  switch (loaded.kind) {
    case 'helmreleases':
      return (
        <HelmReleaseTable
          releases={loaded.rows}
          showNamespace={showNamespace}
          onValues={onValues}
        />
      )
    case 'pods':
      return (
        <PodTable
          pods={loaded.rows}
          usage={loaded.usage}
          showNamespace={showNamespace}
          onSelect={onSelectPod}
          onManifest={open}
        />
      )
    case 'workloads':
      return (
        <WorkloadTable
          workloads={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
          onAction={onAction}
        />
      )
    case 'jobs':
      return <JobTable jobs={loaded.rows} showNamespace={showNamespace} onManifest={open} />
    case 'cronjobs':
      return (
        <CronJobTable cronjobs={loaded.rows} showNamespace={showNamespace} onManifest={open} />
      )
    case 'services':
      return (
        <ServiceTable services={loaded.rows} showNamespace={showNamespace} onManifest={open} />
      )
    case 'ingresses':
      return (
        <IngressTable ingresses={loaded.rows} showNamespace={showNamespace} onManifest={open} />
      )
    case 'networkpolicies':
      return (
        <NetworkPolicyTable
          policies={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'routes':
      return <RouteTable routes={loaded.rows} showNamespace={showNamespace} onManifest={open} />
    case 'persistentvolumes':
      return <PersistentVolumeTable volumes={loaded.rows} onManifest={open} />
    case 'persistentvolumeclaims':
      return <ClaimTable claims={loaded.rows} showNamespace={showNamespace} onManifest={open} />
    case 'storageclasses':
      return <StorageClassTable classes={loaded.rows} onManifest={open} />
    case 'config':
      return (
        <ConfigTable
          entries={loaded.rows}
          secrets={loaded.secrets}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'crds':
      return <CRDTable crds={loaded.rows} onManifest={open} />
    case 'roles':
      return (
        <RoleTable
          roles={loaded.rows}
          clusterScoped={loaded.clusterScoped}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'rolebindings':
      return (
        <BindingTable
          bindings={loaded.rows}
          clusterScoped={loaded.clusterScoped}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'serviceaccounts':
      return (
        <ServiceAccountTable
          accounts={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'custom':
      return (
        <CustomResourceTable
          rows={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'nodes':
      return <NodeTable nodes={loaded.rows} onManifest={open} />
    case 'namespaces':
      return <NamespaceTable namespaces={loaded.rows} onManifest={open} />
  }
}

/* ------------------------------------------------------------- cell atoms --- */

/**
 * Name is the first column of every list: mono, truncated, with a state dot. In
 * a list that spans namespaces it carries the namespace as a `ns/` prefix rather
 * than a column of its own — the same way kubectl names an object, and it keeps
 * the row one line tall.
 */
function Name({
  children,
  tone,
  title,
  namespace,
  onOpen,
}: {
  children: ReactNode
  tone?: Tone
  title?: string
  namespace?: string
  /**
   * Opens the row's object. Given one, the name becomes the button it always
   * looked like it should be — the same affordance the pod list has had, now on
   * every list, because every kind has a detail view to open onto.
   */
  onOpen?: () => void
}) {
  const label = <QualifiedName namespace={namespace}>{children}</QualifiedName>
  const full = namespace && title ? `${namespace}/${title}` : title

  return (
    <span className="flex items-center gap-2.5">
      {tone ? (
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${TONE_FILL[tone]}`} />
      ) : null}
      {onOpen ? (
        <button
          type="button"
          onClick={onOpen}
          className={`${NAME_BUTTON} font-mono text-fg transition-colors hover:text-accent`}
          title={full}
        >
          {label}
        </button>
      ) : (
        <span className="block min-w-0 font-mono text-fg" title={full}>
          {label}
        </span>
      )}
    </span>
  )
}

/** A name button is a block so its two lines stack; the text still reads left. */
const NAME_BUTTON = 'block min-w-0 text-left'

/**
 * QualifiedName draws `namespace/name` without letting the qualifier eat the
 * name. The prefix is the answer to "which one of these is it", but the name is
 * what the row is *about* — and a single truncated line spends its width left to
 * right, so on a narrow column the namespace is drawn in full and the name is
 * the part that disappears. That is backwards, so the two are separated:
 *
 * - Below `sm` they stack. The namespace gets its own faint line above and the
 *   name gets a full-width one of its own, so neither is cut by the other. The
 *   row costs a second line only in the list that actually spans namespaces.
 * - At `sm` and up they stay on one line — the kubectl reading, and what keeps
 *   the table scannable — but the namespace is capped at 40% of the cell and
 *   truncates itself first, so the name always keeps the remaining 60%.
 */
function QualifiedName({ namespace, children }: { namespace?: string; children: ReactNode }) {
  if (!namespace) return <span className="block truncate">{children}</span>

  return (
    <span className="flex min-w-0 flex-col leading-tight sm:flex-row sm:items-baseline">
      <span className="min-w-0 truncate text-[11.5px] text-faint sm:max-w-[40%] sm:text-[length:inherit]">
        {namespace}
        <span className="hidden sm:inline">/</span>
      </span>
      <span className="min-w-0 truncate">{children}</span>
    </span>
  )
}

/**
 * opener binds a row to the detail drawer. It reads the namespace off the row
 * rather than taking one, so a cluster-scoped kind — which has no namespace
 * field at all — needs no special case at any call site.
 */
function opener(
  onManifest: OpenManifest | undefined,
  row: { name: string; namespace?: string },
): (() => void) | undefined {
  if (!onManifest) return undefined
  return () => onManifest(row.name, row.namespace, 'overview')
}

/** List renders a set of values that is usually one value and sometimes six. */
function List({ values, empty = '—' }: { values: string[] | undefined; empty?: string }) {
  const items = values ?? []
  if (items.length === 0) return <span className="text-faint">{empty}</span>
  return (
    <span className="truncate" title={items.join(', ')}>
      {items.join(', ')}
    </span>
  )
}

const MONO = 'truncate font-mono text-[12.5px] text-muted'
const AGE = 'text-[12.5px] text-muted'

/**
 * The width the row actions actually need. A `table-fixed` table hands a column
 * exactly what it asked for, so a column asking for 1% gets 1% and its buttons
 * are drawn on top of whatever is to their left — which is why this is a real
 * measurement rather than a nominal one: 32px of button, 8px of cell padding.
 * View, YAML and edit fold behind one `RowMenu` trigger rather than three
 * separate icons — a narrow table was giving those three shortcuts a fixed
 * price in width paid by the one column that cannot afford it: the name.
 *
 * It is the default rather than something each table opts into, because a table
 * that forgets it does not look wrong until there are buttons in it — and by then
 * the overlap reads as a rendering bug rather than as a missing width. The other
 * half of the same rule is that the **name column asks for no width at all**:
 * `table-fixed` gives an unsized column whatever the sized ones leave, so the
 * buttons take their measurement and the name takes the rest. A name column with
 * a percentage of its own is what put the two in competition.
 *
 * The third part of the rule is that every *other* column's width is written
 * `min(percentage, ceiling)`. A percentage alone keeps its share of the table at
 * every width, so on a wide screen an Age column holding "12d" grew to two
 * hundred pixels of whitespace and the name — which is the only column whose
 * content is unbounded — grew by the same proportion rather than by the surplus.
 * The percentage is what a narrow table needs and the ceiling is what the
 * content can actually use, so past that width the extra room is the name's.
 */
const ROW_ACTIONS_WIDTH = 'w-10'

/**
 * A workload row carries two more: it can be scaled and it can be rolled. Below
 * `md` they fold away, for the same reason the values-and-history buttons on a
 * Helm row do — the drawer the menu opens offers both in its footer, so what is
 * given up on a narrow screen is a shortcut and not a destination.
 */
const WORKLOAD_ACTIONS_WIDTH = 'w-10 md:w-[104px]'

/**
 * A Helm release has three actions: its values, editing them, and the revisions
 * it has been through. History folds away below `md` like every other third
 * shortcut — the drawer the first button opens carries it as a tab.
 */
const VALUES_ACTIONS_WIDTH = 'w-[98px] md:w-[132px]'

/**
 * The manifest column. It is the last column of every list and carries no
 * heading — the two icons are titled, and a word above them would be a column
 * name for something that is not data.
 */
function ManifestHead({
  onManifest,
  width = ROW_ACTIONS_WIDTH,
}: {
  onManifest?: OpenManifest
  /**
   * How much room the buttons need. It is overridable because the count is per
   * table: a workload row carries two more than everything else.
   */
  width?: string
}) {
  if (!onManifest) return null
  return (
    <Th className={width}>
      <span className="sr-only">Manifest</span>
    </Th>
  )
}

/**
 * ManifestCell offers the three things you can do with a row, each opening the
 * same detail drawer on a different tab: look at the object, read its manifest,
 * or start changing it. All three sit behind one `RowMenu` trigger rather than
 * three icons — the row's own width belongs to what it is naming, not to the
 * ways of reaching a drawer that offers the same three tabs regardless.
 */
function ManifestCell({
  onManifest,
  name,
  namespace,
  editable = true,
  actions,
}: {
  onManifest?: OpenManifest
  name: string
  namespace?: string
  editable?: boolean
  /**
   * Actions belonging to this kind alone, shown ahead of the menu every row
   * has. Kind-specific because they are: only a workload can be scaled.
   */
  actions?: ReactNode
}) {
  if (!onManifest) return null

  return (
    <Td className="whitespace-nowrap">
      <span className="flex items-center justify-end gap-0.5">
        {actions}
        <RowMenu label={`Actions for ${name}`}>
          <RowMenuItem onClick={() => onManifest(name, namespace, 'overview')}>
            <PanelRightOpen aria-hidden="true" className="size-3.5" />
            View details
          </RowMenuItem>
          <RowMenuItem onClick={() => onManifest(name, namespace, 'yaml')}>
            <FileCode2 aria-hidden="true" className="size-3.5" />
            View as YAML
          </RowMenuItem>
          {editable ? (
            <RowMenuItem onClick={() => onManifest(name, namespace, 'yaml', true)}>
              <Pencil aria-hidden="true" className="size-3.5" />
              Edit
            </RowMenuItem>
          ) : null}
        </RowMenu>
      </span>
    </Td>
  )
}

function phaseTone(phase: string): Tone {
  switch (phase) {
    case 'Bound':
    case 'Available':
    case 'Active':
    case 'Ready':
      return 'ok'
    case 'Pending':
      return 'warn'
    case 'Failed':
    case 'Lost':
      return 'bad'
    case 'Released':
    case 'Terminating':
      return 'idle'
    default:
      return 'idle'
  }
}

function jobTone(state: string): Tone {
  switch (state) {
    case 'Complete':
      return 'ok'
    case 'Failed':
      return 'bad'
    case 'Running':
      return 'accent'
    case 'Suspended':
      return 'idle'
    default:
      return 'warn'
  }
}

/**
 * Helm's own status vocabulary. `superseded` is not a failure — it is every
 * revision but the current one — so it reads as idle rather than as a warning,
 * and the pending states are the only ones genuinely in motion.
 */
function helmTone(status: string): Tone {
  switch (status) {
    case 'deployed':
      return 'ok'
    case 'failed':
      return 'bad'
    case 'pending-install':
    case 'pending-upgrade':
    case 'pending-rollback':
    case 'uninstalling':
      return 'warn'
    case 'superseded':
    case 'uninstalled':
      return 'idle'
    default:
      return 'idle'
  }
}

/* ---------------------------------------------------------------- tables --- */

/**
 * Helm releases: what is installed here, rather than what is running. The two
 * actions are the release's editable surface — a release has no manifest of its
 * own, so viewing and editing both mean its values.
 */
function HelmReleaseTable({
  releases,
  showNamespace,
  onValues,
}: {
  releases: HelmRelease[]
  showNamespace: boolean
  onValues?: OpenValues
}) {
  return (
    <Table resizeKey="kubemg_cols_helmreleases">
      <thead>
        <tr>
          <Th columnKey="name">Release</Th>
          <Th className="hidden md:table-cell md:w-[min(18%,14rem)]" columnKey="chart">
            Chart
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(10%,7rem)]" columnKey="version">
            Version
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(10%,7rem)]" columnKey="app">
            App
          </Th>
          <Th className="w-[12%] md:w-[min(7%,5rem)]" columnKey="rev">
            Rev
          </Th>
          <Th className="w-[28%] md:w-[min(14%,9rem)]" columnKey="status">
            Status
          </Th>
          <Th className="w-[22%] md:w-[min(10%,7rem)]" columnKey="updated">
            Updated
          </Th>
          {onValues ? (
            <Th className={VALUES_ACTIONS_WIDTH}>
              <span className="sr-only">Values</span>
            </Th>
          ) : null}
        </tr>
      </thead>
      <tbody>
        {releases.map((release) => (
          <Row key={`${release.namespace}/${release.name}`}>
            <Td className="truncate">
              <Name
                tone={helmTone(release.status)}
                title={release.description || release.name}
                namespace={showNamespace ? release.namespace : undefined}
              >
                {release.name}
              </Name>
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>{release.chart_name || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{release.chart_version || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{release.app_version || '—'}</Td>
            <Td className="font-mono text-[12.5px] text-muted">{release.revision}</Td>
            <Td>
              <Pill tone={helmTone(release.status)}>{release.status || 'unknown'}</Pill>
            </Td>
            <Td className={AGE}>{release.updated_at ? relativeAge(release.updated_at) : '—'}</Td>
            {onValues ? (
              <Td className="whitespace-nowrap">
                <span className="flex items-center justify-end gap-0.5">
                  <IconButton
                    type="button"
                    label={`View the values of ${release.name}`}
                    onClick={() => onValues(release, 'values')}
                  >
                    <SlidersHorizontal aria-hidden="true" className="size-3.5" />
                  </IconButton>
                  <IconButton
                    type="button"
                    label={`Edit the values of ${release.name}`}
                    onClick={() => onValues(release, 'values', true)}
                  >
                    <Pencil aria-hidden="true" className="size-3.5" />
                  </IconButton>
                  <IconButton
                    type="button"
                    className="hidden md:inline-grid"
                    label={`Revision history of ${release.name}`}
                    onClick={() => onValues(release, 'history')}
                  >
                    <History aria-hidden="true" className="size-3.5" />
                  </IconButton>
                </span>
              </Td>
            ) : null}
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/*
 * Sorting the pod list. It is done in the browser and not asked of the cluster,
 * which is the honest shape of it: the list is already fully in hand, and two of
 * the columns worth sorting by — the live CPU and memory readings — come from a
 * different read that the Kubernetes API cannot order a pod list by at all.
 */

type PodSortKey = 'name' | 'phase' | 'cpu' | 'memory' | 'restarts' | 'node' | 'age'

type PodSort = { key: PodSortKey; direction: 'asc' | 'desc' }

/**
 * Which way a column sorts on its *first* click. It is per column because the
 * interesting end differs: the biggest consumer and the most-restarted pod are
 * what anyone is looking for, while a name reads alphabetically and "age
 * ascending" means the youngest — the pod that just appeared.
 */
const POD_SORT_FIRST: Record<PodSortKey, 'asc' | 'desc'> = {
  name: 'asc',
  phase: 'asc',
  cpu: 'desc',
  memory: 'desc',
  restarts: 'desc',
  node: 'asc',
  age: 'asc',
}

/**
 * podSortValue is what one column of one row is worth. A missing reading answers
 * -1 rather than 0, so "no sample" sorts below a pod genuinely using nothing
 * instead of being mixed in with it.
 */
function podSortValue(pod: Pod, usage: PodUsageIndex | null, key: PodSortKey): string | number {
  const sample = usage?.get(`${pod.namespace}/${pod.name}`)
  switch (key) {
    case 'name':
      return `${pod.namespace}/${pod.name}`
    case 'phase':
      return pod.phase
    case 'cpu':
      return sample ? sample.cpu_millicores : -1
    case 'memory':
      return sample ? sample.memory_bytes : -1
    case 'restarts':
      return pod.restarts
    case 'node':
      return pod.node
    case 'age':
      // Ascending age is the newest first, so the value is the timestamp
      // negated: an unparseable one sorts last either way.
      return -(Date.parse(pod.created_at) || 0)
  }
}

/**
 * sortPods orders a copy. Unsorted is the order the server sent — namespace then
 * name, the order kubectl prints — which is why there is no third "off" click to
 * get back to it: sorting by name ascending *is* that order.
 */
function sortPods(pods: Pod[], usage: PodUsageIndex | null, sort: PodSort | null): Pod[] {
  if (!sort) return pods

  const factor = sort.direction === 'asc' ? 1 : -1
  return [...pods].sort((a, b) => {
    const left = podSortValue(a, usage, sort.key)
    const right = podSortValue(b, usage, sort.key)
    if (typeof left === 'number' && typeof right === 'number') {
      // A tie falls back to the name, so equal readings do not shuffle between
      // renders — most of a cluster's pods are using nothing measurable.
      return factor * (left - right) || a.name.localeCompare(b.name)
    }
    // `numeric` so pod-2 comes before pod-10, which is what a replica suffix is.
    return (
      factor * String(left).localeCompare(String(right), undefined, { numeric: true }) ||
      a.name.localeCompare(b.name)
    )
  })
}

function PodTable({
  pods,
  usage,
  showNamespace,
  onSelect,
  onManifest,
}: {
  pods: Pod[]
  usage: PodUsageIndex | null
  showNamespace: boolean
  onSelect: (pod: Pod) => void
  onManifest?: OpenManifest
}) {
  const [sort, setSort] = useState<PodSort | null>(null)
  const rows = useMemo(() => sortPods(pods, usage, sort), [pods, usage, sort])

  /** Every heading sorts the same way, so the wiring is written once. */
  const column = (key: PodSortKey) => ({
    direction: sort?.key === key ? sort.direction : null,
    onSort: () =>
      setSort((current) =>
        current?.key === key
          ? { key, direction: current.direction === 'asc' ? 'desc' : 'asc' }
          : { key, direction: POD_SORT_FIRST[key] },
      ),
    columnKey: key,
  })

  return (
    <Table resizeKey="kubemg_cols_pods">
      <thead>
        <tr>
          {/* The name column asks for no width: `table-fixed` gives an
              unsized column whatever the sized ones leave, which is exactly what
              a name should have — the readings, the counts and the buttons all
              need a known amount of room and a pod name will take any. */}
          <SortTh {...column('name')}>Pod</SortTh>
          {/* Ready rides beside the phase pill rather than a column of its own —
              1/1 only means something once you already know a pod is Running,
              so the two are one reading, not two. */}
          <SortTh className="w-[28%] sm:w-[20%] md:w-[min(15%,10rem)]" {...column('phase')}>
            Phase
          </SortTh>
          {/* CPU and memory are the two numbers `kubectl top` answers with, in
              the same order, so they read as the same thing. They are the first
              columns to go on a narrow screen: a phase and a restart count say
              whether a pod is in trouble, a reading says how much. */}
          <SortTh className="hidden sm:table-cell sm:w-[11%] md:w-[min(8%,5.5rem)]" {...column('cpu')}>
            CPU
          </SortTh>
          <SortTh className="hidden sm:table-cell sm:w-[11%] md:w-[min(9%,6rem)]" {...column('memory')}>
            Memory
          </SortTh>
          <SortTh className="hidden md:table-cell md:w-[min(5%,3.5rem)]" {...column('restarts')}>
            Restarts
          </SortTh>
          {/* A node name is long and this table is the one with the most columns,
              so it waits for the width that can hold it — at `lg` the resource
              tree is on screen too and there is nothing spare. */}
          <SortTh className="hidden xl:table-cell xl:w-[min(14%,14rem)]" {...column('node')}>
            Node
          </SortTh>
          <SortTh className="w-[14%] sm:w-[10%] md:w-[min(6%,4.5rem)]" {...column('age')}>
            Age
          </SortTh>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {rows.map((pod) => (
          <Row key={`${pod.namespace}/${pod.name}`}>
            <Td className="truncate">
              <span className="flex items-center gap-2.5">
                <span
                  aria-hidden="true"
                  className={`size-1.5 shrink-0 rounded-full ${TONE_FILL[podTone(pod)]}`}
                />
                <button
                  type="button"
                  onClick={() => onSelect(pod)}
                  className={`${NAME_BUTTON} font-mono text-fg transition-colors hover:text-accent`}
                  title={`${pod.namespace}/${pod.name}`}
                >
                  <QualifiedName namespace={showNamespace ? pod.namespace : undefined}>
                    {pod.name}
                  </QualifiedName>
                </button>
              </span>
            </Td>
            <Td className="whitespace-nowrap">
              <span className="flex items-center gap-1.5">
                <Pill tone={podTone(pod)}>{pod.phase}</Pill>
                <span className="font-mono text-[12.5px] text-muted">
                  {pod.ready}/{pod.total}
                </span>
              </span>
            </Td>
            <UsageCell
              usage={usage}
              pod={pod}
              resource="cpu"
              read={(sample) => sample.cpu_millicores}
              format={formatCPU}
            />
            <UsageCell
              usage={usage}
              pod={pod}
              resource="memory"
              read={(sample) => sample.memory_bytes}
              format={formatMemory}
            />
            <Td
              className={`hidden font-mono text-[12.5px] md:table-cell ${
                pod.restarts > 0 ? 'text-warn' : 'text-muted'
              }`}
            >
              {pod.restarts}
            </Td>
            <Td className={`hidden xl:table-cell ${MONO}`}>{pod.node || '—'}</Td>
            <Td className={`whitespace-nowrap ${AGE}`}>{relativeAge(pod.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={pod.name} namespace={pod.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/** How a reading against its ceiling reads: comfortable, worth a look, at it. */
const USAGE_TEXT = { ok: 'text-muted', warn: 'text-warn', bad: 'text-danger' } as const

/**
 * UsageCell is one live reading in a pod row. It is a number and not a bar: a
 * meter needs a denominator, and in a list of a hundred pods most of the
 * denominators are missing — a pod is only bounded when every one of its
 * containers declares a limit. Where there *is* a ceiling the row says how close
 * to it the pod is and colours the reading, which is the whole reason to put the
 * number in a list rather than leave it in the drawer.
 *
 * A dash means one of three honest things, and the title says which: the cluster
 * serves no Metrics API, the pod is not running, or metrics-server has not
 * sampled it yet.
 */
function UsageCell({
  usage,
  pod,
  resource,
  read,
  format,
}: {
  usage: PodUsageIndex | null
  pod: Pod
  resource: 'cpu' | 'memory'
  read: (sample: PodUsage) => number
  format: (value: number) => string
}) {
  const sample = usage?.get(`${pod.namespace}/${pod.name}`)
  if (!sample) {
    return (
      <Td className="hidden font-mono text-[12.5px] text-faint sm:table-cell">
        <span title={usage ? 'No sample for this pod yet' : 'This cluster serves no Metrics API'}>
          —
        </span>
      </Td>
    )
  }

  const used = read(sample)
  const limit = podLimit(pod.containers, resource)
  const percent = limit > 0 ? ratio(used, limit) : null

  return (
    <Td className="hidden whitespace-nowrap sm:table-cell">
      <span
        className={`font-mono text-[12.5px] ${
          percent === null ? 'text-muted' : USAGE_TEXT[usageTone(percent)]
        }`}
        title={limit > 0 ? `${format(used)} of a ${format(limit)} limit` : `${format(used)}, no limit`}
      >
        {format(used)}
      </span>
      {percent === null ? null : (
        <span className="ml-1.5 font-mono text-[11.5px] text-faint">{Math.round(percent)}%</span>
      )}
    </Td>
  )
}

/**
 * WorkloadActionCells are the two writes a workload row offers directly. They
 * are icons in the row rather than a menu because there are two of them and they
 * are the two things anyone does to a workload; what they open is a dialog, so
 * nothing here is one click away from happening.
 */
function WorkloadActions({
  workload,
  onAction,
}: {
  workload: Workload
  onAction?: OpenWorkloadAction
}) {
  if (!onAction) return null
  // A row whose Kind these controls do not answer for — nothing today, but the
  // workload list is where a new kind would land — simply offers neither.
  const key = workloadKeyFor(workload.kind)
  const capability = key ? workloadCapability(key) : undefined
  if (!capability) return null

  // Below `md` they fold away with the manifest shortcuts: five buttons need
  // 200px, which a phone-width table cannot give without taking it from the name.
  // The drawer the first button opens offers both in its footer, so nothing is
  // out of reach — `contents` keeps each one a direct flex child where it shows.
  return (
    <span className="hidden md:contents">
      {capability.scale ? (
        <IconButton
          type="button"
          label={`Scale ${workload.name}`}
          onClick={() => onAction('scale', workload)}
        >
          <SlidersHorizontal aria-hidden="true" className="size-3.5" />
        </IconButton>
      ) : null}
      {capability.restart ? (
        <IconButton
          type="button"
          label={`Restart ${workload.name}`}
          onClick={() => onAction('restart', workload)}
        >
          <RotateCcw aria-hidden="true" className="size-3.5" />
        </IconButton>
      ) : null}
    </span>
  )
}

function WorkloadTable({
  workloads,
  showNamespace,
  onManifest,
  onAction,
}: {
  workloads: Workload[]
  showNamespace: boolean
  onManifest?: OpenManifest
  onAction?: OpenWorkloadAction
}) {
  return (
    <Table resizeKey="kubemg_cols_workloads">
      <thead>
        <tr>
          {/* No width, on purpose: the sized columns and the five buttons take
              their measurements and the name takes what is left. */}
          <Th columnKey="name">Name</Th>
          <Th className="w-[22%] md:w-[min(13%,9rem)]" columnKey="kind">
            Kind
          </Th>
          <Th className="w-[16%] md:w-[min(9%,6rem)]" columnKey="ready">
            Ready
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(26%,20rem)]" columnKey="image">
            Image
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} width={WORKLOAD_ACTIONS_WIDTH} />
        </tr>
      </thead>
      <tbody>
        {workloads.map((workload) => (
          <Row key={`${workload.kind}/${workload.namespace}/${workload.name}`}>
            <Td className="truncate">
              <Name
                tone={workloadTone(workload)}
                title={workload.name}
                onOpen={opener(onManifest, workload)}
                namespace={showNamespace ? workload.namespace : undefined}
              >
                {workload.name}
              </Name>
            </Td>
            <Td className="text-[12.5px] text-muted">{workload.kind}</Td>
            <Td
              className={`font-mono text-[12.5px] ${
                workload.ready === workload.desired ? 'text-muted' : 'text-warn'
              }`}
            >
              {workload.ready}/{workload.desired}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`} title={workload.images?.join(', ')}>
              {workload.images?.[0] ?? '—'}
            </Td>
            <Td className={AGE}>{relativeAge(workload.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={workload.name}
              namespace={workload.namespace}
              actions={<WorkloadActions workload={workload} onAction={onAction} />}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function JobTable({
  jobs,
  showNamespace,
  onManifest,
}: {
  jobs: Job[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_jobs">
      <thead>
        <tr>
          <Th columnKey="name">Job</Th>
          <Th className="w-[22%] md:w-[min(14%,9rem)]" columnKey="state">
            State
          </Th>
          <Th className="w-[18%] md:w-[min(10%,7rem)]" columnKey="completed">
            Completed
          </Th>
          <Th className="hidden md:table-cell md:w-[min(8%,6rem)]" columnKey="failed">
            Failed
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(28%,20rem)]" columnKey="image">
            Image
          </Th>
          <Th className="w-[18%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {jobs.map((job) => (
          <Row key={`${job.namespace}/${job.name}`}>
            <Td className="truncate">
              <Name
                tone={jobTone(job.state)}
                title={job.name}
                onOpen={opener(onManifest, job)}
                namespace={showNamespace ? job.namespace : undefined}
              >
                {job.name}
              </Name>
            </Td>
            <Td>
              <Pill tone={jobTone(job.state)}>{job.state}</Pill>
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">
              {job.succeeded}/{job.completions}
            </Td>
            <Td
              className={`hidden font-mono text-[12.5px] md:table-cell ${
                job.failed > 0 ? 'text-danger' : 'text-muted'
              }`}
            >
              {job.failed}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`} title={job.images?.join(', ')}>
              {job.images?.[0] ?? '—'}
            </Td>
            <Td className={AGE}>{relativeAge(job.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={job.name} namespace={job.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * NextRun reads the schedule's next firing. It has four answers and they are
 * different facts, so none of them is a bare dash: a suspended CronJob is not
 * going to run, an expression this build cannot evaluate says so and carries the
 * reason, a schedule with no firing left is a valid CronJob that never runs
 * again, and everything else is a countdown against the server's own clock.
 */
function NextRun({ cronjob }: { cronjob: CronJob }) {
  if (cronjob.suspended) {
    return <span className="font-mono text-[12.5px] text-faint">suspended</span>
  }
  if (cronjob.schedule_error) {
    return (
      <span className="text-[12.5px] text-warn" title={cronjob.schedule_error}>
        unreadable
      </span>
    )
  }
  if (!cronjob.next_schedule_at) {
    return (
      <span className="font-mono text-[12.5px] text-faint" title="This schedule has no further run">
        never
      </span>
    )
  }

  const at = new Date(cronjob.next_schedule_at)
  return (
    <span className="font-mono text-[12.5px] text-fg" title={at.toLocaleString()}>
      {formatCountdown(secondsUntil(cronjob.next_schedule_at))}
    </span>
  )
}

function CronJobTable({
  cronjobs,
  showNamespace,
  onManifest,
}: {
  cronjobs: CronJob[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  // One timer for the whole table, and its cadence follows what is actually
  // being watched: a schedule ten minutes out is counted down by the second, a
  // nightly one is not — a column redrawing every second to change nothing is
  // exactly the movement this deck spends its chrome budget avoiding.
  const soonest = cronjobs.reduce(
    (nearest, job) =>
      job.next_schedule_at ? Math.min(nearest, secondsUntil(job.next_schedule_at)) : nearest,
    Number.POSITIVE_INFINITY,
  )
  useTicker(soonest < 600 ? 1000 : 30_000)

  return (
    <Table resizeKey="kubemg_cols_cronjobs">
      <thead>
        <tr>
          <Th columnKey="name">CronJob</Th>
          {/*
            The expression is what hides below md rather than the countdown: a
            cron field is the least readable thing in this row on a narrow screen,
            and "in 12m" answers the question the expression is being read for.
          */}
          <Th className="hidden md:table-cell md:w-[min(15%,11rem)]" columnKey="schedule">
            Schedule
          </Th>
          <Th className="w-[30%] md:w-[min(14%,9rem)]" columnKey="next">
            Next run
          </Th>
          <Th className="w-[16%] md:w-[min(12%,8rem)]" columnKey="state">
            State
          </Th>
          <Th className="hidden md:table-cell md:w-[min(8%,6rem)]" columnKey="active">
            Active
          </Th>
          <Th className="hidden md:table-cell md:w-[min(16%,10rem)]" columnKey="lastrun">
            Last run
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {cronjobs.map((cronjob) => (
          <Row key={`${cronjob.namespace}/${cronjob.name}`}>
            <Td className="truncate">
              <Name
                tone={cronjob.suspended ? 'idle' : 'ok'}
                title={cronjob.name}
                onOpen={opener(onManifest, cronjob)}
                namespace={showNamespace ? cronjob.namespace : undefined}
              >
                {cronjob.name}
              </Name>
            </Td>
            <Td className="hidden truncate font-mono text-[12.5px] text-fg md:table-cell">
              {cronjob.schedule}
              {cronjob.time_zone ? (
                <span className="ml-1.5 font-sans text-faint">{cronjob.time_zone}</span>
              ) : null}
            </Td>
            <Td>
              <NextRun cronjob={cronjob} />
            </Td>
            <Td>
              <Pill tone={cronjob.suspended ? 'idle' : 'ok'}>
                {cronjob.suspended ? 'Suspended' : 'Active'}
              </Pill>
            </Td>
            <Td className="hidden font-mono text-[12.5px] text-muted md:table-cell">
              {cronjob.active}
            </Td>
            <Td className={`hidden md:table-cell ${AGE}`}>
              {cronjob.last_schedule_at ? relativeAge(cronjob.last_schedule_at) : 'never'}
            </Td>
            <Td className={AGE}>{relativeAge(cronjob.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={cronjob.name}
              namespace={cronjob.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function ServiceTable({
  services,
  showNamespace,
  onManifest,
}: {
  services: Service[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_services">
      <thead>
        <tr>
          <Th columnKey="name">Service</Th>
          <Th className="w-[24%] md:w-[min(13%,9rem)]" columnKey="type">
            Type
          </Th>
          <Th className="hidden md:table-cell md:w-[min(15%,10rem)]" columnKey="clusterip">
            Cluster IP
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(18%,12rem)]" columnKey="external">
            External
          </Th>
          <Th className="w-[20%] md:w-[min(18%,12rem)]" columnKey="ports">
            Ports
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {services.map((service) => (
          <Row key={`${service.namespace}/${service.name}`}>
            <Td className="truncate">
              <Name
                title={service.name}
                onOpen={opener(onManifest, service)}
                namespace={showNamespace ? service.namespace : undefined}
              >
                {service.name}
              </Name>
            </Td>
            <Td className="text-[12.5px] text-muted">{service.type}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>{service.cluster_ip || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={service.external_ips} />
            </Td>
            <Td className={MONO}>
              <List values={service.ports} />
            </Td>
            <Td className={AGE}>{relativeAge(service.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={service.name}
              namespace={service.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function IngressTable({
  ingresses,
  showNamespace,
  onManifest,
}: {
  ingresses: Ingress[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_ingresses">
      <thead>
        <tr>
          <Th columnKey="name">Ingress</Th>
          <Th className="w-[24%] md:w-[min(14%,10rem)]" columnKey="class">
            Class
          </Th>
          <Th className="w-[24%] md:w-[min(26%,18rem)]" columnKey="hosts">
            Hosts
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(18%,12rem)]" columnKey="address">
            Address
          </Th>
          <Th className="hidden md:table-cell md:w-[min(8%,6rem)]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {ingresses.map((ingress) => (
          <Row key={`${ingress.namespace}/${ingress.name}`}>
            <Td className="truncate">
              <Name
                title={ingress.name}
                onOpen={opener(onManifest, ingress)}
                namespace={showNamespace ? ingress.namespace : undefined}
              >
                {ingress.name}
              </Name>
            </Td>
            <Td className={MONO}>{ingress.class || '—'}</Td>
            <Td className={MONO}>
              <List values={ingress.hosts} empty="*" />
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={ingress.addresses} />
            </Td>
            <Td className="hidden font-mono text-[12.5px] text-muted md:table-cell">
              {ingress.rules}
            </Td>
            <Td className={AGE}>{relativeAge(ingress.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={ingress.name}
              namespace={ingress.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * NetworkPolicies. The list shows what the object *is* — its selector, its
 * directions, how many rules — and stops there; whether it actually governs a
 * given workload, and what its rules resolve to, is the reachability tab on
 * that workload's own drawer. A row here has no manifest of a peer to print.
 */
function NetworkPolicyTable({
  policies,
  showNamespace,
  onManifest,
}: {
  policies: NetworkPolicy[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_networkpolicies">
      <thead>
        <tr>
          <Th columnKey="name">NetworkPolicy</Th>
          <Th className="hidden md:table-cell md:w-[min(28%,20rem)]" columnKey="selector">
            Pod selector
          </Th>
          <Th className="w-[24%] md:w-[min(16%,11rem)]" columnKey="types">
            Governs
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(14%,9rem)]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {policies.map((policy) => (
          <Row key={`${policy.namespace}/${policy.name}`}>
            <Td className="truncate">
              <Name
                title={policy.name}
                namespace={showNamespace ? policy.namespace : undefined}
                onOpen={opener(onManifest, policy)}
              >
                {policy.name}
              </Name>
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              {/* An empty selector is a real answer — every pod in the
                  namespace — and it is worth saying so rather than leaving
                  the cell looking like the read came back with nothing. */}
              {policy.pod_selector || 'all pods'}
            </Td>
            <Td className="text-[12.5px] text-muted">
              <List values={policy.policy_types} />
            </Td>
            <Td className="hidden font-mono text-[12.5px] text-muted lg:table-cell">
              {policy.ingress_rules} in / {policy.egress_rules} out
            </Td>
            <Td className={AGE}>{relativeAge(policy.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={policy.name}
              namespace={policy.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function RouteTable({
  routes,
  showNamespace,
  onManifest,
}: {
  routes: Route[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_routes">
      <thead>
        <tr>
          <Th columnKey="name">Route</Th>
          <Th className="w-[32%] md:w-[min(30%,20rem)]" columnKey="hostnames">
            Hostnames
          </Th>
          <Th className="hidden md:table-cell md:w-[min(24%,16rem)]" columnKey="attached">
            Attached to
          </Th>
          <Th className="hidden md:table-cell md:w-[min(8%,6rem)]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {routes.map((route) => (
          <Row key={`${route.namespace}/${route.name}`}>
            <Td className="truncate">
              <Name title={route.name} namespace={showNamespace ? route.namespace : undefined}>
                {route.name}
              </Name>
            </Td>
            <Td className={MONO}>
              <List values={route.hostnames} empty="*" />
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={route.parents} />
            </Td>
            <Td className="hidden font-mono text-[12.5px] text-muted md:table-cell">
              {route.rules}
            </Td>
            <Td className={AGE}>{relativeAge(route.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={route.name} namespace={route.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function PersistentVolumeTable({
  volumes,
  onManifest,
}: {
  volumes: PersistentVolume[]
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_persistentvolumes">
      <thead>
        <tr>
          <Th columnKey="name">Volume</Th>
          <Th className="w-[20%] md:w-[min(12%,8rem)]" columnKey="status">
            Status
          </Th>
          <Th className="w-[16%] md:w-[min(10%,7rem)]" columnKey="capacity">
            Capacity
          </Th>
          <Th className="hidden md:table-cell md:w-[min(12%,9rem)]" columnKey="access">
            Access
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(20%,14rem)]" columnKey="claim">
            Claim
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(12%,10rem)]" columnKey="class">
            Class
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {volumes.map((volume) => (
          <Row key={volume.name}>
            <Td className="truncate">
              <Name tone={phaseTone(volume.status)} title={volume.name} onOpen={opener(onManifest, volume)}>
                {volume.name}
              </Name>
            </Td>
            <Td>
              <Pill tone={phaseTone(volume.status)}>{volume.status}</Pill>
            </Td>
            <Td className="font-mono text-[12.5px] text-fg">{volume.capacity || '—'}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={volume.access_modes} />
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{volume.claim || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{volume.storage_class || '—'}</Td>
            <Td className={AGE}>{relativeAge(volume.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={volume.name} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function ClaimTable({
  claims,
  showNamespace,
  onManifest,
}: {
  claims: PersistentVolumeClaim[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_persistentvolumeclaims">
      <thead>
        <tr>
          <Th columnKey="name">Claim</Th>
          <Th className="w-[20%] md:w-[min(12%,8rem)]" columnKey="status">
            Status
          </Th>
          <Th className="w-[16%] md:w-[min(10%,7rem)]" columnKey="capacity">
            Capacity
          </Th>
          <Th className="hidden md:table-cell md:w-[min(12%,9rem)]" columnKey="access">
            Access
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(14%,10rem)]" columnKey="class">
            Class
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(16%,14rem)]" columnKey="volume">
            Volume
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {claims.map((claim) => (
          <Row key={`${claim.namespace}/${claim.name}`}>
            <Td className="truncate">
              <Name
                tone={phaseTone(claim.status)}
                title={claim.name}
                onOpen={opener(onManifest, claim)}
                namespace={showNamespace ? claim.namespace : undefined}
              >
                {claim.name}
              </Name>
            </Td>
            <Td>
              <Pill tone={phaseTone(claim.status)}>{claim.status}</Pill>
            </Td>
            <Td className="font-mono text-[12.5px] text-fg">{claim.capacity || '—'}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={claim.access_modes} />
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{claim.storage_class || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{claim.volume || '—'}</Td>
            <Td className={AGE}>{relativeAge(claim.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={claim.name} namespace={claim.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function StorageClassTable({
  classes,
  onManifest,
}: {
  classes: StorageClass[]
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_storageclasses">
      <thead>
        <tr>
          <Th columnKey="name">Class</Th>
          <Th className="w-[34%] md:w-[min(26%,18rem)]" columnKey="provisioner">
            Provisioner
          </Th>
          <Th className="hidden md:table-cell md:w-[min(14%,9rem)]" columnKey="reclaim">
            Reclaim
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(14%,11rem)]" columnKey="binding">
            Binding
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {classes.map((entry) => (
          <Row key={entry.name}>
            <Td className="truncate">
              <span className="flex items-center gap-2">
                <Name title={entry.name} onOpen={opener(onManifest, entry)}>{entry.name}</Name>
                {entry.default ? (
                  <Pill tone="accent" dot={false}>
                    default
                  </Pill>
                ) : null}
              </span>
            </Td>
            <Td className={MONO}>{entry.provisioner}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>{entry.reclaim_policy || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{entry.binding_mode || '—'}</Td>
            <Td className={AGE}>{relativeAge(entry.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={entry.name} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function ConfigTable({
  entries,
  secrets,
  showNamespace,
  onManifest,
}: {
  entries: ConfigEntry[]
  secrets: boolean
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey={secrets ? 'kubemg_cols_secrets' : 'kubemg_cols_configmaps'}>
      <thead>
        <tr>
          <Th columnKey="name">{secrets ? 'Secret' : 'ConfigMap'}</Th>
          {secrets ? (
            <Th className="hidden md:table-cell md:w-[min(20%,14rem)]" columnKey="type">
              Type
            </Th>
          ) : null}
          <Th className="w-[14%] md:w-[min(8%,5rem)]" columnKey="keys">
            Keys
          </Th>
          <Th
            className={`hidden lg:table-cell ${secrets ? 'lg:w-[min(26%,20rem)]' : 'lg:w-[min(46%,28rem)]'}`}
            columnKey="keynames"
          >
            Key names
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => (
          <Row key={`${entry.namespace}/${entry.name}`}>
            <Td className="truncate">
              <span className="flex items-center gap-2">
                <Name title={entry.name} namespace={showNamespace ? entry.namespace : undefined}>
                  {entry.name}
                </Name>
                {entry.immutable ? (
                  <Pill tone="idle" dot={false}>
                    immutable
                  </Pill>
                ) : null}
              </span>
            </Td>
            {secrets ? <Td className={`hidden md:table-cell ${MONO}`}>{entry.type || '—'}</Td> : null}
            <Td className="font-mono text-[12.5px] text-muted">{entry.keys?.length ?? 0}</Td>
            {/* Key names, never values: a value is not in the response at all. */}
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={entry.keys} empty="none" />
            </Td>
            <Td className={AGE}>{relativeAge(entry.created_at)}</Td>
            {/* A Secret's values are redacted on the way out, so the manifest
                is not the whole object and there is nothing honest to write
                back — it is offered to read and not to edit. */}
            <ManifestCell
              onManifest={onManifest}
              name={entry.name}
              namespace={entry.namespace}
              editable={!secrets}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function CRDTable({
  crds,
  onManifest,
}: {
  crds: CustomResourceDefinition[]
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_crds">
      <thead>
        <tr>
          <Th columnKey="name">Definition</Th>
          <Th className="w-[24%] md:w-[min(18%,12rem)]" columnKey="kind">
            Kind
          </Th>
          <Th className="hidden md:table-cell md:w-[min(20%,14rem)]" columnKey="group">
            Group
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(10%,7rem)]" columnKey="scope">
            Scope
          </Th>
          <Th className="w-[18%] md:w-[min(12%,8rem)]" columnKey="versions">
            Versions
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {crds.map((crd) => (
          <Row key={crd.name}>
            <Td className="truncate">
              <Name title={crd.name} onOpen={opener(onManifest, crd)}>{crd.name}</Name>
            </Td>
            <Td className="truncate text-[12.5px] text-fg">{crd.kind}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>{crd.group || 'core'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{crd.scope}</Td>
            <Td className={MONO}>
              <List values={crd.versions} />
            </Td>
            <ManifestCell onManifest={onManifest} name={crd.name} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/* -------------------------------------------------- the cluster's own RBAC --- */

/**
 * A Role or a ClusterRole. What a row has to answer is "how much does this
 * grant", and the honest short form of that is the two axes together: the verbs
 * and the resources, as the union across every rule. A rule count alone says
 * nothing — a one-rule role can be `*` on `*` — which is why `wildcard` is the
 * one property here that earns a colour.
 *
 * The rules themselves are on the object, one tab away: this is a list, and a
 * policy printed into a table cell is neither readable nor complete.
 */
function RoleTable({
  roles,
  clusterScoped,
  showNamespace,
  onManifest,
}: {
  roles: ClusterRoleEntry[]
  clusterScoped: boolean
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey={clusterScoped ? 'kubemg_cols_clusterroles' : 'kubemg_cols_roles'}>
      <thead>
        <tr>
          <Th columnKey="name">{clusterScoped ? 'ClusterRole' : 'Role'}</Th>
          <Th className="w-[14%] md:w-[min(8%,5rem)]" columnKey="rules">
            Rules
          </Th>
          <Th className="hidden md:table-cell md:w-[min(26%,18rem)]" columnKey="verbs">
            Verbs
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(26%,20rem)]" columnKey="resources">
            Resources
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {roles.map((role) => (
          <Row key={`${role.namespace ?? ''}/${role.name}`}>
            <Td className="truncate">
              <span className="flex items-center gap-2">
                <Name
                  title={role.name}
                  namespace={showNamespace ? role.namespace : undefined}
                  onOpen={opener(onManifest, role)}
                >
                  {role.name}
                </Name>
                {/* A rule granting `*` is how a role that reads as narrow turns
                    out not to be, and it is invisible in every other column. */}
                {role.wildcard ? <Pill tone="warn">wildcard</Pill> : null}
                {role.aggregated ? (
                  <Pill tone="idle" dot={false}>
                    aggregated
                  </Pill>
                ) : null}
                {role.builtin ? (
                  <Pill tone="idle" dot={false}>
                    built-in
                  </Pill>
                ) : null}
              </span>
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">{role.rule_count}</Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={role.verbs} empty="none" />
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={role.resources} empty="none" />
            </Td>
            <Td className={AGE}>{relativeAge(role.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={role.name} namespace={role.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * Subjects, rendered as the thing a binding is actually about. A subject is a
 * kind and a name, and a ServiceAccount's name means nothing without the
 * namespace it lives in — `system:serviceaccount:prod:deployer` and the one in
 * `dev` are different identities that print identically without it.
 */
function Subjects({ binding }: { binding: RoleBindingEntry }) {
  const shown = binding.subjects ?? []
  if (shown.length === 0) {
    // A binding with no subjects grants nothing to anybody. It is legal, it
    // happens (a group was removed, a template rendered empty), and it is worth
    // saying out loud rather than leaving as a blank cell.
    return <span className="text-faint">nobody</span>
  }

  const names = shown.map((subject) =>
    subject.namespace ? `${subject.namespace}/${subject.name}` : subject.name,
  )
  // The overflow is counted rather than dropped: a row that silently shows 20 of
  // 200 subjects is a wrong answer, not a shortened one.
  const hidden = binding.subject_count - shown.length

  return (
    <span className="truncate" title={names.join(', ')}>
      {names.join(', ')}
      {hidden > 0 ? <span className="text-faint"> +{hidden} more</span> : null}
    </span>
  )
}

/**
 * A RoleBinding or ClusterRoleBinding, read as subject → role. That direction is
 * the whole point of the table: the API stores a roleRef and a subject list, and
 * an operator looking at it is always asking one of "who can do this" or "what
 * can they do" — neither of which a printed `roleRef` answers without two more
 * lookups.
 */
function BindingTable({
  bindings,
  clusterScoped,
  showNamespace,
  onManifest,
}: {
  bindings: RoleBindingEntry[]
  clusterScoped: boolean
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table
      resizeKey={clusterScoped ? 'kubemg_cols_clusterrolebindings' : 'kubemg_cols_rolebindings'}
    >
      <thead>
        <tr>
          <Th columnKey="name">{clusterScoped ? 'ClusterRoleBinding' : 'RoleBinding'}</Th>
          <Th className="w-[30%] md:w-[min(24%,16rem)]" columnKey="role">
            Role
          </Th>
          <Th className="hidden md:table-cell md:w-[min(30%,22rem)]" columnKey="subjects">
            Subjects
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(12%,8rem)]" columnKey="kinds">
            Kind
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {bindings.map((binding) => (
          <Row key={`${binding.namespace ?? ''}/${binding.name}`}>
            <Td className="truncate">
              <Name
                title={binding.name}
                namespace={showNamespace ? binding.namespace : undefined}
                onOpen={opener(onManifest, binding)}
              >
                {binding.name}
              </Name>
            </Td>
            <Td className="truncate">
              <span className="flex items-center gap-1.5">
                {/* Which *kind* of role is the load-bearing half: a RoleBinding
                    pointing at a ClusterRole applies that ClusterRole's rules
                    inside this namespace only, and reading it as cluster-wide
                    (or the reverse) is the classic RBAC misreading. */}
                <span className="shrink-0 text-[11px] text-faint">
                  {binding.role_kind === 'ClusterRole' ? 'ClusterRole' : 'Role'}
                </span>
                <span className="truncate font-mono text-[12.5px] text-fg" title={binding.role_name}>
                  {binding.role_name}
                </span>
              </span>
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <Subjects binding={binding} />
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={binding.kinds} empty="none" />
            </Td>
            <Td className={AGE}>{relativeAge(binding.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={binding.name}
              namespace={binding.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * ServiceAccounts — the identity half of the section. Every pod runs as one,
 * and the one it runs as by default is `default`, which is why that row is
 * marked: a binding granted to `default` grants it to every workload in the
 * namespace that never named an account.
 */
function ServiceAccountTable({
  accounts,
  showNamespace,
  onManifest,
}: {
  accounts: ServiceAccountEntry[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_serviceaccounts">
      <thead>
        <tr>
          <Th columnKey="name">ServiceAccount</Th>
          <Th className="w-[16%] md:w-[min(10%,7rem)]" columnKey="secrets">
            Secrets
          </Th>
          <Th className="hidden md:table-cell md:w-[min(14%,9rem)]" columnKey="pull">
            Pull secrets
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(16%,11rem)]" columnKey="automount">
            Token
          </Th>
          <Th className="w-[16%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {accounts.map((account) => (
          <Row key={`${account.namespace}/${account.name}`}>
            <Td className="truncate">
              <span className="flex items-center gap-2">
                <Name
                  title={account.name}
                  namespace={showNamespace ? account.namespace : undefined}
                  onOpen={opener(onManifest, account)}
                >
                  {account.name}
                </Name>
                {account.default ? (
                  <Pill tone="idle" dot={false}>
                    default
                  </Pill>
                ) : null}
              </span>
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">{account.secrets}</Td>
            <Td className="hidden font-mono text-[12.5px] text-muted md:table-cell">
              {account.image_pull_secrets}
            </Td>
            {/* Three states, not two: unset is the common case and means the pod
                spec decides, which is a different answer from either. */}
            <Td className={`hidden lg:table-cell ${MONO}`}>
              {account.automount_token === undefined
                ? 'pod decides'
                : account.automount_token
                  ? 'automounted'
                  : 'not mounted'}
            </Td>
            <Td className={AGE}>{relativeAge(account.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={account.name}
              namespace={account.namespace}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * The list for a kind KubeMG learned about from the cluster rather than from its
 * own table. A CRD's spec is whatever its author decided, so there are no
 * columns to normalise into — name, kind and age are what every Kubernetes
 * object has. The manifest is the real view of one of these, and it is one click
 * away in the last column.
 */
function CustomResourceTable({
  rows,
  showNamespace,
  onManifest,
}: {
  rows: CustomResource[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_customresources">
      <thead>
        <tr>
          <Th columnKey="name">Name</Th>
          <Th className="hidden md:table-cell md:w-[min(22%,14rem)]" columnKey="kind">
            Kind
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(20%,14rem)]" columnKey="apiversion">
            API version
          </Th>
          <Th className="w-[20%] md:w-[min(14%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <Row key={`${row.namespace}/${row.name}`}>
            <Td className="truncate">
              <Name title={row.name} namespace={showNamespace ? row.namespace : undefined}>
                {row.name}
              </Name>
            </Td>
            <Td className="hidden truncate text-[12.5px] text-fg md:table-cell">
              {row.kind || '—'}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{row.api_version || '—'}</Td>
            <Td className={AGE}>{relativeAge(row.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={row.name} namespace={row.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function NodeTable({ nodes, onManifest }: { nodes: ClusterNode[]; onManifest?: OpenManifest }) {
  return (
    <Table resizeKey="kubemg_cols_nodes">
      <thead>
        <tr>
          <Th columnKey="name">Node</Th>
          <Th className="w-[26%] md:w-[min(16%,10rem)]" columnKey="status">
            Status
          </Th>
          <Th className="hidden md:table-cell md:w-[min(16%,11rem)]" columnKey="roles">
            Roles
          </Th>
          <Th className="w-[22%] md:w-[min(12%,8rem)]" columnKey="version">
            Version
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(14%,10rem)]" columnKey="internalip">
            Internal IP
          </Th>
          <Th className="hidden lg:table-cell lg:w-[min(8%,6rem)]" columnKey="cpu">
            CPU
          </Th>
          <Th className="w-[18%] md:w-[min(10%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {nodes.map((node) => (
          <Row key={node.name}>
            <Td className="truncate">
              <Name tone={node.ready ? 'ok' : 'bad'} title={node.name} onOpen={opener(onManifest, node)}>
                {node.name}
              </Name>
            </Td>
            <Td>
              <Pill tone={node.ready ? (node.unschedulable ? 'warn' : 'ok') : 'bad'}>
                {node.status}
              </Pill>
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={node.roles} />
            </Td>
            <Td className={MONO}>{node.version}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{node.internal_ip || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{node.cpu || '—'}</Td>
            <Td className={AGE}>{relativeAge(node.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={node.name} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function NamespaceTable({
  namespaces,
  onManifest,
}: {
  namespaces: Namespace[]
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_namespaces">
      <thead>
        <tr>
          <Th columnKey="name">Namespace</Th>
          <Th className="w-[26%] md:w-[min(20%,12rem)]" columnKey="status">
            Status
          </Th>
          <Th className="hidden md:table-cell md:w-[min(20%,14rem)]" columnKey="access">
            Your access
          </Th>
          <Th className="w-[24%] md:w-[min(20%,6rem)]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {namespaces.map((namespace) => (
          <Row key={namespace.name}>
            <Td className="truncate">
              <Name tone={phaseTone(namespace.status)} title={namespace.name} onOpen={opener(onManifest, namespace)}>
                {namespace.name}
              </Name>
            </Td>
            <Td>
              <Pill tone={phaseTone(namespace.status)}>{namespace.status}</Pill>
            </Td>
            <Td className="hidden md:table-cell">
              {namespace.granted ? (
                <span className="text-[12.5px] text-muted">granted</span>
              ) : (
                <span className="text-[12.5px] text-faint">not granted</span>
              )}
            </Td>
            <Td className={AGE}>
              {namespace.created_at ? relativeAge(namespace.created_at) : '—'}
            </Td>
            <ManifestCell onManifest={onManifest} name={namespace.name} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}
