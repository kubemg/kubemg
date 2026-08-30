import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type {
  ClusterNode,
  ClusterRoleEntry,
  ConfigEntry,
  CronJob,
  CustomResource,
  CustomResourceDefinition,
  HelmRelease,
  HorizontalPodAutoscaler,
  Ingress,
  Job,
  LimitRange,
  LimitRangeEntry,
  Namespace,
  NetworkPolicy,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  PodDisruptionBudget,
  PodUsage,
  ReplicaSet,
  ResourceQuota,
  RoleBindingEntry,
  Route,
  Service,
  ServiceAccountEntry,
  StorageClass,
  Workload,
} from '../api/types'
import {
  Ban,
  CircleCheck,
  Eye,
  FileCode2,
  History,
  PanelRightOpen,
  Pause,
  Pencil,
  Play,
  RotateCcw,
  SlidersHorizontal,
  Trash2,
  Zap,
} from 'lucide-react'
import {
  IconButton,
  OBJECT_MARK,
  OBJECT_NAME,
  Pill,
  Row,
  RowMenu,
  RowMenuItem,
  SortTh,
  Table,
  Td,
  Th,
} from './primitives'
import type { DetailTab } from './ResourceDetailDrawer'
import type { WorkloadActionName } from './WorkloadActionPanel'
import { namespaceHref } from '../lib/navigation'
import { workloadCapability, workloadKeyFor } from '../lib/workloads'
import type { SelectedRow } from '../lib/selection'
import { selectionKey } from '../lib/selection'
import type { Tone } from '../lib/status'
import { TONE_FILL, phaseTone, podTone, workloadTone } from '../lib/status'
import { formatCountdown, formatInstant, relativeAge, secondsUntil, useTicker } from '../lib/time'
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
  | { kind: 'replicasets'; rows: ReplicaSet[] }
  /**
   * HorizontalPodAutoscalers carry `available` for the same reason the route
   * lists do: this build reads `autoscaling/v2` and nothing else, so a cluster
   * serving only v1 is told that rather than shown an empty list it would read
   * as "nothing is autoscaled here".
   */
  | { kind: 'autoscalers'; rows: HorizontalPodAutoscaler[]; available: boolean; reason?: string }
  | { kind: 'resourcequotas'; rows: ResourceQuota[] }
  | { kind: 'limitranges'; rows: LimitRange[] }
  | { kind: 'poddisruptionbudgets'; rows: PodDisruptionBudget[] }
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
 * OpenRowAction asks for one of the writes that are not a view of the object at
 * all. It takes the row rather than a name because the row is where the address
 * is complete — a workload row carries its own Kind, and a CronJob row is the
 * only place the schedule's current state is known, which is what decides
 * whether the word on the control is Suspend or Resume.
 */
export type OpenRowAction = (row: SelectedRow) => void

/**
 * RowSelection is the checkbox column's whole contract: what is selected, and
 * the two ways of changing it. The selection itself lives on the page, because
 * it outlives any one table — a filter narrowing the list must not silently
 * drop rows that are already selected and about to be acted on.
 */
export interface RowSelection {
  has: (key: string) => boolean
  toggle: (row: SelectedRow) => void
  /** The header checkbox: every row the table is currently drawing, at once. */
  setMany: (rows: SelectedRow[], checked: boolean) => void
}

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
  selection,
  onDelete,
  onSuspend,
  onCordon,
  onRun,
  onUninstall,
  onReveal,
  clusterId,
}: {
  loaded: LoadedResource
  showNamespace?: boolean
  onSelectPod: (pod: Pod) => void
  onManifest?: OpenManifest
  onValues?: OpenValues
  onAction?: OpenWorkloadAction
  /**
   * Set while the operator has asked for the checkbox column. It is offered on
   * the four lists a selection is worth having on — pods, workloads, jobs and
   * cronjobs — and a table that does not take it simply never draws a column.
   * Adding a fifth is adding `SelectHead`/`SelectCell` to that table.
   */
  selection?: RowSelection
  onDelete?: OpenRowAction
  /** Turning a schedule off, or back on. CronJobs and nothing else. */
  onSuspend?: OpenRowAction
  /** Cordoning or uncordoning a node. Nodes and nothing else. */
  onCordon?: OpenRowAction
  /** Firing a schedule now. CronJobs and nothing else. */
  onRun?: OpenRowAction
  /** Removing a Helm release, and everything its manifest recorded. */
  onUninstall?: (release: HelmRelease) => void
  /** Reading one of a Secret's values. Secrets and nothing else. */
  onReveal?: (entry: ConfigEntry) => void
  /**
   * Which cluster these rows came from. Only the namespace list needs it — a
   * namespace has a page of its own and the row links at it — and a caller that
   * does not pass it gets the list without the link rather than a broken one.
   */
  clusterId?: number
}) {
  switch (loaded.kind) {
    case 'helmreleases':
      return (
        <HelmReleaseTable
          releases={loaded.rows}
          showNamespace={showNamespace}
          onValues={onValues}
          onUninstall={onUninstall}
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
          selection={selection}
          onDelete={onDelete}
        />
      )
    case 'workloads':
      return (
        <WorkloadTable
          workloads={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
          onAction={onAction}
          selection={selection}
          onDelete={onDelete}
        />
      )
    case 'jobs':
      return (
        <JobTable
          jobs={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
          selection={selection}
          onDelete={onDelete}
        />
      )
    case 'cronjobs':
      return (
        <CronJobTable
          cronjobs={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
          selection={selection}
          onDelete={onDelete}
          onSuspend={onSuspend}
          onRun={onRun}
        />
      )
    case 'replicasets':
      return (
        <ReplicaSetTable
          replicasets={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
          onDelete={onDelete}
        />
      )
    case 'autoscalers':
      return (
        <AutoscalerTable
          autoscalers={loaded.rows}
          showNamespace={showNamespace}
          onManifest={open}
        />
      )
    case 'resourcequotas':
      return <QuotaTable quotas={loaded.rows} showNamespace={showNamespace} onManifest={open} />
    case 'limitranges':
      return (
        <LimitRangeTable ranges={loaded.rows} showNamespace={showNamespace} onManifest={open} />
      )
    case 'poddisruptionbudgets':
      return (
        <DisruptionBudgetTable budgets={loaded.rows} showNamespace={showNamespace} onManifest={open} />
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
          onReveal={onReveal}
        />
      )
    case 'crds':
      return <CRDTable crds={loaded.rows} onManifest={open} />
    case 'roles':
      return (
        <RoleTable
          roles={loaded.rows}
          clusterScoped={loaded.clusterScoped}
          // A ClusterRole has no namespace to put in a column, whatever the
          // namespace selector above the list happens to say.
          showNamespace={showNamespace && !loaded.clusterScoped}
          onManifest={open}
        />
      )
    case 'rolebindings':
      return (
        <BindingTable
          bindings={loaded.rows}
          clusterScoped={loaded.clusterScoped}
          showNamespace={showNamespace && !loaded.clusterScoped}
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
      return <NodeTable nodes={loaded.rows} onManifest={open} onCordon={onCordon} />
    case 'namespaces':
      return (
        <NamespaceTable namespaces={loaded.rows} onManifest={open} clusterId={clusterId} />
      )
  }
}

/* ------------------------------------------------------------- cell atoms --- */

/**
 * Name is the first column of every list: mono, truncated, with a state dot. It
 * carries the name and nothing else — where the object lives is the namespace
 * column's job (`NamespaceHead`/`NamespaceCell`), because a qualifier drawn
 * inside this cell spends the name's own width on itself.
 */
function Name({
  children,
  tone,
  title,
  namespace,
  onOpen,
  to,
}: {
  children: ReactNode
  tone?: Tone
  title?: string
  /**
   * Where the object lives. It is *not* drawn here — it only qualifies the
   * hover title, so the full `ns/name` kubectl identity is still one hover away
   * on a row whose name is truncated.
   */
  namespace?: string
  /**
   * Opens the row's object. Given one, the name becomes the button it always
   * looked like it should be — the same affordance the pod list has had, now on
   * every list, because every kind has a detail view to open onto.
   */
  onOpen?: () => void
  /**
   * Where the object *is*, when it has a page of its own. A namespace does; the
   * kinds that only have a drawer take `onOpen` instead. It is a real link
   * rather than a button so it opens in a new tab like any other address.
   */
  to?: string
}) {
  const full = namespace && title ? `${namespace}/${title}` : title
  // The bar is the affordance, so it is drawn for exactly the two branches that
  // open something. A name with nowhere to go wears nothing, which is what
  // makes the bar readable in the rows that do.
  const addressable = Boolean(to || onOpen)

  return (
    <span
      className={`flex items-start gap-2.5 ${addressable ? OBJECT_MARK : 'border-l-2 border-transparent -ml-2 pl-2'}`}
    >
      {tone ? (
        // `mt` rather than `items-center`: the dot belongs beside the name's
        // first line, not floating at the middle of a two-line name.
        <span
          aria-hidden="true"
          className={`mt-[6px] size-1.5 shrink-0 rounded-full ${TONE_FILL[tone]}`}
        />
      ) : null}
      {to ? (
        <Link to={to} className={OBJECT_NAME} title={full}>
          {children}
        </Link>
      ) : onOpen ? (
        <button type="button" onClick={onOpen} className={OBJECT_NAME} title={full}>
          {children}
        </button>
      ) : (
        <span
          className="block min-w-0 font-mono font-medium text-fg [overflow-wrap:anywhere]"
          title={full}
        >
          {children}
        </span>
      )}
    </span>
  )
}


/**
 * The namespace column. It exists only while the list spans namespaces — in a
 * single-namespace list the answer is in the header above the table, and a
 * column repeating one value in every row is width taken from the name.
 *
 * It is a column rather than the `ns/name` prefix it used to be, because the
 * prefix could not be both readable and out of the name's way: capped at a
 * share of the name cell it truncated to `kube-s…`/`monitori…`, which is a
 * different cut in every row, so neither half was legible and the names no
 * longer started at one x to scan down. A column of its own truncates against
 * its own width, keeps every name aligned, and is the shape Rancher, Lens and
 * every other console settled on for the same reason.
 *
 * `md:` is where it gets a ceiling like every other sized column; below that it
 * keeps a share, because a list that spans namespaces cannot drop the one field
 * that says which of two same-named objects a row is.
 */
const NAMESPACE_WIDTH = 'w-[26%] md:w-[16%]'

function NamespaceHead({ show }: { show: boolean }) {
  if (!show) return null
  return (
    <Th className={NAMESPACE_WIDTH} columnKey="namespace">
      Namespace
    </Th>
  )
}

function NamespaceCell({ show, namespace }: { show: boolean; namespace?: string }) {
  if (!show) return null
  return (
    <Td className="truncate font-mono text-[12.5px] text-muted" title={namespace}>
      {namespace || '—'}
    </Td>
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
 * The third part of the rule is that every *other* column's width is a **plain
 * percentage**, and it is written that way because it is the only form the fixed
 * layout algorithm actually honours. These columns used to ask for
 * `min(percentage, ceiling)` — the percentage for a narrow table, the ceiling so
 * that on a wide one the surplus went to the name rather than to an Age column
 * holding "12d". Chrome **discards a track width that is a math function mixing a
 * percentage with an absolute length**: measured in a 1000px four-column fixed
 * table, `min(16%,11rem)` and `clamp(6rem,16%,11rem)` both resolve to 250px,
 * identical to `width: auto`, while `16%` → 160px, `11rem` → 176px and
 * `calc(16%)` → 160px all apply. So none of these ceilings had ever been read and
 * no table had ever rendered at its designed widths: the sized columns fell back
 * to `auto` and split the leftover evenly, which is how NODE (asking 14rem) and
 * RESTARTS (asking 3.5rem) came out the same width as each other.
 *
 * The ceiling is what was given up, and it is the cheaper half: a plain
 * percentage is read at every width, and the surplus a very wide table hands an
 * Age column is whitespace rather than a truncated heading. **Never put a
 * percentage inside `min()`/`clamp()` on a column width** — it is not a style
 * choice but a silent no-op, which is why `ResourceTables.test.ts` asserts the
 * absence of one rather than leaving it to a review.
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
 * The checkbox column. It is the *first* column rather than a trailing one
 * because it is read together with the name it selects, and it is a real
 * measurement like `ROW_ACTIONS_WIDTH` for the same reason: `table-fixed` hands
 * a column exactly what it asked for.
 */
const SELECT_WIDTH = 'w-9'

/** The one class both checkboxes share, so the column and its header agree. */
const CHECKBOX = 'size-3.5 accent-[var(--color-accent)]'

/**
 * SelectHead is the column's heading and the select-all control at once. It
 * acts on the rows the table is **drawing**, not on the whole list: a filtered
 * table selecting rows that are not on screen is how somebody deletes something
 * they never saw.
 */
function SelectHead({
  rows,
  selection,
}: {
  rows: SelectedRow[]
  selection?: RowSelection
}) {
  if (!selection) return null

  const selected = rows.filter((row) => selection.has(row.key)).length
  const all = rows.length > 0 && selected === rows.length

  return (
    <Th className={SELECT_WIDTH}>
      <input
        type="checkbox"
        className={CHECKBOX}
        checked={all}
        // Some but not all: the box says "part of this list", which is a third
        // state and not a checked one.
        ref={(node) => {
          if (node) node.indeterminate = selected > 0 && !all
        }}
        disabled={rows.length === 0}
        onChange={(event) => selection.setMany(rows, event.target.checked)}
        aria-label={all ? 'Clear the selection' : 'Select every row shown'}
      />
    </Th>
  )
}

/**
 * SelectCell is one row's checkbox. A row with no `SelectedRow` still draws the
 * cell — a missing `<td>` in a `table-fixed` row shifts every column after it —
 * it simply has nothing to tick.
 */
function SelectCell({ row, selection }: { row?: SelectedRow; selection?: RowSelection }) {
  if (!selection) return null
  if (!row) return <Td />
  return (
    <Td>
      <input
        type="checkbox"
        className={CHECKBOX}
        checked={selection.has(row.key)}
        onChange={() => selection.toggle(row)}
        aria-label={`Select ${row.name}`}
      />
    </Td>
  )
}

/*
 * How each selectable list turns a row into a target. They are functions rather
 * than inline objects because the same row is built twice — once for the header
 * checkbox's "everything shown", once in the row itself — and the two must agree
 * on the key or a select-all would tick boxes that are already ticked.
 */

function podRow(pod: Pod): SelectedRow {
  return {
    key: selectionKey('pods', pod.namespace, pod.name),
    kind: 'pods',
    label: 'Pod',
    name: pod.name,
    namespace: pod.namespace,
  }
}

function jobRow(job: Job): SelectedRow {
  return {
    key: selectionKey('jobs', job.namespace, job.name),
    kind: 'jobs',
    label: 'Job',
    name: job.name,
    namespace: job.namespace,
  }
}

function cronJobRow(cronjob: CronJob): SelectedRow {
  return {
    key: selectionKey('cronjobs', cronjob.namespace, cronjob.name),
    kind: 'cronjobs',
    label: 'CronJob',
    name: cronjob.name,
    namespace: cronjob.namespace,
    // The one row property an action reads: it decides whether the control
    // offered is Suspend or Resume, and whether a bulk suspend has anything to
    // do to this row at all.
    suspended: cronjob.suspended,
  }
}

/**
 * A workload row's kind is its own — the table serves three at once — so a Kind
 * the resource API cannot address yields nothing rather than a guess.
 */
function workloadRow(workload: Workload): SelectedRow | undefined {
  const kind = workloadKeyFor(workload.kind)
  if (!kind) return undefined
  return {
    key: selectionKey(kind, workload.namespace, workload.name),
    kind,
    label: workload.kind,
    name: workload.name,
    namespace: workload.namespace,
  }
}

/** rowDeleter binds a row to the page's delete, or to nothing if it has neither. */
function rowDeleter(onDelete: OpenRowAction | undefined, row: SelectedRow | undefined) {
  if (!onDelete || !row) return undefined
  return () => onDelete(row)
}

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
  menu,
  onDelete,
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
  /**
   * Menu items belonging to this kind alone, drawn after the three every row
   * has and before Delete. A CronJob's schedule switch is the only one today.
   */
  menu?: ReactNode
  /**
   * Removing the object. It is offered per row as well as over a selection
   * because deleting one pod should not mean turning the checkbox column on to
   * do it — and it is last in the menu, after a separator, because it is the
   * one item there that cannot be undone by choosing another.
   */
  onDelete?: () => void
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
          {menu}
          {onDelete ? (
            <RowMenuItem onClick={onDelete} danger>
              <Trash2 aria-hidden="true" className="size-3.5" />
              Delete
            </RowMenuItem>
          ) : null}
        </RowMenu>
      </span>
    </Td>
  )
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
  onUninstall,
}: {
  releases: HelmRelease[]
  showNamespace: boolean
  onValues?: OpenValues
  onUninstall?: (release: HelmRelease) => void
}) {
  return (
    <Table resizeKey="kubemg_cols_helmreleases">
      <thead>
        <tr>
          <Th columnKey="name">Release</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[18%]" columnKey="chart">
            Chart
          </Th>
          <Th className="hidden lg:table-cell lg:w-[10%]" columnKey="version">
            Version
          </Th>
          <Th className="hidden lg:table-cell lg:w-[10%]" columnKey="app">
            App
          </Th>
          <Th className="w-[12%] md:w-[7%]" columnKey="rev">
            Rev
          </Th>
          <Th className="w-[28%] md:w-[14%]" columnKey="status">
            Status
          </Th>
          <Th className="w-[22%] md:w-[10%]" columnKey="updated">
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
            <Td>
              <Name
                tone={helmTone(release.status)}
                title={release.description || release.name}
                namespace={release.namespace}
              >
                {release.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={release.namespace} />
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
                  {/*
                    Uninstall is the one lifecycle verb every other list has and
                    this one lacked. It is a danger-toned icon rather than a menu
                    entry for the same reason the row delete is: it is the only
                    destructive control here, and it opens a confirmation that
                    names what the release recorded before anything is removed.
                  */}
                  {onUninstall ? (
                    <IconButton
                      type="button"
                      tone="danger"
                      label={`Uninstall ${release.name}`}
                      onClick={() => onUninstall(release)}
                    >
                      <Trash2 aria-hidden="true" className="size-3.5" />
                    </IconButton>
                  ) : null}
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

type PodSortKey = 'name' | 'namespace' | 'phase' | 'cpu' | 'memory' | 'restarts' | 'node' | 'age'

type PodSort = { key: PodSortKey; direction: 'asc' | 'desc' }

/**
 * Which way a column sorts on its *first* click. It is per column because the
 * interesting end differs: the biggest consumer and the most-restarted pod are
 * what anyone is looking for, while a name reads alphabetically and "age
 * ascending" means the youngest — the pod that just appeared.
 */
const POD_SORT_FIRST: Record<PodSortKey, 'asc' | 'desc'> = {
  name: 'asc',
  namespace: 'asc',
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
      // Namespace-qualified, so sorting by name in a list spanning namespaces
      // still groups a namespace together rather than interleaving them.
      return `${pod.namespace}/${pod.name}`
    case 'namespace':
      return pod.namespace
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
  selection,
  onDelete,
}: {
  pods: Pod[]
  usage: PodUsageIndex | null
  showNamespace: boolean
  onSelect: (pod: Pod) => void
  onManifest?: OpenManifest
  selection?: RowSelection
  onDelete?: OpenRowAction
}) {
  const [sort, setSort] = useState<PodSort | null>(null)
  const rows = useMemo(() => sortPods(pods, usage, sort), [pods, usage, sort])
  const selectable = useMemo(() => rows.map(podRow), [rows])

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
          <SelectHead rows={selectable} selection={selection} />
          {/* The name column asks for no width: `table-fixed` gives an
              unsized column whatever the sized ones leave, which is exactly what
              a name should have — the readings, the counts and the buttons all
              need a known amount of room and a pod name will take any. */}
          <SortTh {...column('name')}>Pod</SortTh>
          {/* The one table whose headings sort, so its namespace column sorts
              too rather than being the only unsortable heading in the row. */}
          {showNamespace ? (
            <SortTh className={NAMESPACE_WIDTH} {...column('namespace')}>
              Namespace
            </SortTh>
          ) : null}
          {/* Ready rides beside the phase pill rather than a column of its own —
              1/1 only means something once you already know a pod is Running,
              so the two are one reading, not two. */}
          <SortTh className="w-[28%] sm:w-[22%] md:w-[16%]" {...column('phase')}>
            Phase
          </SortTh>
          {/* CPU and memory are the two numbers `kubectl top` answers with, in
              the same order, so they read as the same thing. They are the first
              columns to go on a narrow screen: a phase and a restart count say
              whether a pod is in trouble, a reading says how much. */}
          <SortTh className="hidden sm:table-cell sm:w-[11%] md:w-[8%]" {...column('cpu')}>
            CPU
          </SortTh>
          <SortTh className="hidden sm:table-cell sm:w-[11%] md:w-[9%]" {...column('memory')}>
            Memory
          </SortTh>
          <SortTh className="hidden md:table-cell md:w-[9%]" {...column('restarts')}>
            Restarts
          </SortTh>
          {/* A node name is long and this table is the one with the most columns,
              so it waits for the width that can hold it — at `lg` the resource
              tree is on screen too and there is nothing spare. */}
          <SortTh className="hidden xl:table-cell xl:w-[13%]" {...column('node')}>
            Node
          </SortTh>
          <SortTh className="w-[14%] sm:w-[10%] md:w-[8%]" {...column('age')}>
            Age
          </SortTh>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {rows.map((pod) => (
          <Row key={`${pod.namespace}/${pod.name}`}>
            <SelectCell row={podRow(pod)} selection={selection} />
            <Td>
              {/* The pod list used to inline its own copy of this cell; it is
                  the same cell every other list draws, so it uses the same one. */}
              <Name
                tone={podTone(pod)}
                title={pod.name}
                namespace={pod.namespace}
                onOpen={() => onSelect(pod)}
              >
                {pod.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={pod.namespace} />
            {/* The cell clips and the count does not shrink: the phase pill is
                the only part of this reading that can be abbreviated, so when
                `CrashLoopBackOff` does not fit it is the pill that ellipsises —
                inside its own column — rather than the pair spilling onto the
                CPU reading next door. The column is sized against the longest
                phase name rather than against `Running`, so the clip is the
                floor and not the normal case. */}
            <Td className="overflow-hidden whitespace-nowrap">
              <span className="flex items-center gap-1.5">
                <Pill tone={podTone(pod)} title={pod.phase}>
                  {pod.phase}
                </Pill>
                <span className="shrink-0 font-mono text-[12.5px] text-muted">
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
            <ManifestCell
              onManifest={onManifest}
              name={pod.name}
              namespace={pod.namespace}
              onDelete={onDelete ? () => onDelete(podRow(pod)) : undefined}
            />
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
  // Neither control, rather than merely no entry: a Kind can be in the
  // capability table for something else — a CronJob is there for its schedule
  // switch — and an empty pair of buttons is still a column's worth of nothing.
  if (!capability || (!capability.scale && !capability.restart)) return null

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
  selection,
  onDelete,
}: {
  workloads: Workload[]
  showNamespace: boolean
  onManifest?: OpenManifest
  onAction?: OpenWorkloadAction
  selection?: RowSelection
  onDelete?: OpenRowAction
}) {
  // A row whose Kind the resource API does not address is not selectable — the
  // table serves three Kinds and a fourth would arrive here before it arrived
  // in the action table.
  const selectable = workloads.map(workloadRow).filter((row): row is SelectedRow => Boolean(row))

  return (
    <Table resizeKey="kubemg_cols_workloads">
      <thead>
        <tr>
          <SelectHead rows={selectable} selection={selection} />
          {/* No width, on purpose: the sized columns and the five buttons take
              their measurements and the name takes what is left. */}
          <Th columnKey="name">Name</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="w-[22%] md:w-[13%]" columnKey="kind">
            Kind
          </Th>
          <Th className="w-[16%] md:w-[9%]" columnKey="ready">
            Ready
          </Th>
          <Th className="hidden lg:table-cell lg:w-[26%]" columnKey="image">
            Image
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} width={WORKLOAD_ACTIONS_WIDTH} />
        </tr>
      </thead>
      <tbody>
        {workloads.map((workload) => (
          <Row key={`${workload.kind}/${workload.namespace}/${workload.name}`}>
            <SelectCell row={workloadRow(workload)} selection={selection} />
            <Td>
              <Name
                tone={workloadTone(workload)}
                title={workload.name}
                onOpen={opener(onManifest, workload)}
                namespace={workload.namespace}
              >
                {workload.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={workload.namespace} />
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
              onDelete={rowDeleter(onDelete, workloadRow(workload))}
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
  selection,
  onDelete,
}: {
  jobs: Job[]
  showNamespace: boolean
  onManifest?: OpenManifest
  selection?: RowSelection
  onDelete?: OpenRowAction
}) {
  return (
    <Table resizeKey="kubemg_cols_jobs">
      <thead>
        <tr>
          <SelectHead rows={jobs.map(jobRow)} selection={selection} />
          <Th columnKey="name">Job</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="w-[22%] md:w-[14%]" columnKey="state">
            State
          </Th>
          <Th className="w-[18%] md:w-[10%]" columnKey="completed">
            Completed
          </Th>
          <Th className="hidden md:table-cell md:w-[10%]" columnKey="failed">
            Failed
          </Th>
          <Th className="hidden lg:table-cell lg:w-[28%]" columnKey="image">
            Image
          </Th>
          <Th className="w-[18%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {jobs.map((job) => (
          <Row key={`${job.namespace}/${job.name}`}>
            <SelectCell row={jobRow(job)} selection={selection} />
            <Td>
              <Name
                tone={jobTone(job.state)}
                title={job.name}
                onOpen={opener(onManifest, job)}
                namespace={job.namespace}
              >
                {job.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={job.namespace} />
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
            <ManifestCell
              onManifest={onManifest}
              name={job.name}
              namespace={job.namespace}
              onDelete={rowDeleter(onDelete, jobRow(job))}
            />
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
    <span className="font-mono text-[12.5px] text-fg" title={formatInstant(at, { seconds: true })}>
      {formatCountdown(secondsUntil(cronjob.next_schedule_at))}
    </span>
  )
}

function CronJobTable({
  cronjobs,
  showNamespace,
  onManifest,
  selection,
  onDelete,
  onSuspend,
  onRun,
}: {
  cronjobs: CronJob[]
  showNamespace: boolean
  onManifest?: OpenManifest
  selection?: RowSelection
  onDelete?: OpenRowAction
  onSuspend?: OpenRowAction
  onRun?: OpenRowAction
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
          <SelectHead rows={cronjobs.map(cronJobRow)} selection={selection} />
          <Th columnKey="name">CronJob</Th>
          <NamespaceHead show={showNamespace} />
          {/*
            The expression is what hides below md rather than the countdown: a
            cron field is the least readable thing in this row on a narrow screen,
            and "in 12m" answers the question the expression is being read for.
          */}
          <Th className="hidden md:table-cell md:w-[15%]" columnKey="schedule">
            Schedule
          </Th>
          <Th className="w-[30%] md:w-[14%]" columnKey="next">
            Next run
          </Th>
          <Th className="w-[16%] md:w-[12%]" columnKey="state">
            State
          </Th>
          <Th className="hidden md:table-cell md:w-[10%]" columnKey="active">
            Active
          </Th>
          <Th className="hidden md:table-cell md:w-[16%]" columnKey="lastrun">
            Last run
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {cronjobs.map((cronjob) => (
          <Row key={`${cronjob.namespace}/${cronjob.name}`}>
            <SelectCell row={cronJobRow(cronjob)} selection={selection} />
            <Td>
              <Name
                tone={cronjob.suspended ? 'idle' : 'ok'}
                title={cronjob.name}
                onOpen={opener(onManifest, cronjob)}
                namespace={cronjob.namespace}
              >
                {cronjob.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={cronjob.namespace} />
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
              onDelete={rowDeleter(onDelete, cronJobRow(cronjob))}
              // The schedule's switch reads as the state it moves to, which is
              // why the row's own `suspended` is what decides the word: an
              // operator picks the outcome, not the field name.
              menu={
                <>
                  {/*
                    Run now sits above the schedule switch because it is the
                    thing somebody woken at 2am reaches for, and it is offered
                    on a suspended CronJob too: firing one by hand is precisely
                    what an operator does while a broken schedule is paused.
                  */}
                  {onRun ? (
                    <RowMenuItem onClick={() => onRun(cronJobRow(cronjob))}>
                      <Zap aria-hidden="true" className="size-3.5" />
                      Run now
                    </RowMenuItem>
                  ) : null}
                  {onSuspend ? (
                    <RowMenuItem onClick={() => onSuspend(cronJobRow(cronjob))}>
                      {cronjob.suspended ? (
                        <Play aria-hidden="true" className="size-3.5" />
                      ) : (
                        <Pause aria-hidden="true" className="size-3.5" />
                      )}
                      {cronjob.suspended ? 'Resume schedule' : 'Suspend schedule'}
                    </RowMenuItem>
                  ) : null}
                </>
              }
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * ReplicaSets. The two columns that earn this list its own place are Owner and
 * Revision: a namespace mid-rollout holds two ReplicaSets for the same
 * Deployment, and the revision is the only thing that says which is which.
 */
function ReplicaSetTable({
  replicasets,
  showNamespace,
  onManifest,
  onDelete,
}: {
  replicasets: ReplicaSet[]
  showNamespace: boolean
  onManifest?: OpenManifest
  onDelete?: OpenRowAction
}) {
  return (
    <Table resizeKey="kubemg_cols_replicasets">
      <thead>
        <tr>
          <Th columnKey="name">ReplicaSet</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[18%]" columnKey="owner">
            Owner
          </Th>
          <Th className="w-[14%] md:w-[8%]" columnKey="rev">
            Rev
          </Th>
          <Th className="w-[20%] md:w-[10%]" columnKey="pods">
            Pods
          </Th>
          <Th className="hidden lg:table-cell lg:w-[20%]" columnKey="image">
            Image
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {replicasets.map((replicaset) => (
          <Row key={`${replicaset.namespace}/${replicaset.name}`}>
            <Td>
              <Name
                /*
                 * A superseded ReplicaSet sits at zero desired, which is the
                 * ordinary resting state of almost every row in this list — so
                 * it reads as idle rather than as a fault, and only a
                 * ReplicaSet that wants pods it does not have is called out.
                 */
                tone={replicaSetTone(replicaset)}
                title={replicaset.name}
                onOpen={opener(onManifest, replicaset)}
                namespace={replicaset.namespace}
              >
                {replicaset.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={replicaset.namespace} />
            <Td className={`hidden md:table-cell ${MONO}`} title={replicaset.owner}>
              {replicaset.owner || '—'}
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">{replicaset.revision || '—'}</Td>
            <Td className="font-mono text-[12.5px] text-fg">
              {replicaset.ready}/{replicaset.desired}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={replicaset.images} />
            </Td>
            <Td className={AGE}>{relativeAge(replicaset.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={replicaset.name}
              namespace={replicaset.namespace}
              onDelete={rowDeleter(onDelete, replicaSetRow(replicaset))}
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * A ReplicaSet wanting pods it has not got is the only interesting state here.
 * Zero desired is not a fault — it is what every superseded ReplicaSet looks
 * like — and colouring it as one would make a healthy namespace read as broken.
 */
function replicaSetTone(replicaset: ReplicaSet): Tone {
  if (replicaset.desired === 0) return 'idle'
  return replicaset.ready >= replicaset.desired ? 'ok' : 'warn'
}

function replicaSetRow(replicaset: ReplicaSet): SelectedRow {
  return {
    key: selectionKey('replicasets', replicaset.namespace, replicaset.name),
    kind: 'replicasets',
    label: 'ReplicaSet',
    name: replicaset.name,
    namespace: replicaset.namespace,
  }
}

/**
 * HorizontalPodAutoscalers. The Metrics column is the reason this is not just
 * another count table: current against target is what says whether an
 * autoscaler is about to move, and `reason` is what says it cannot move at all
 * — an HPA that cannot read its metric looks identical to a quiet one on any
 * table of replica numbers.
 */
function AutoscalerTable({
  autoscalers,
  showNamespace,
  onManifest,
}: {
  autoscalers: HorizontalPodAutoscaler[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_hpas">
      <thead>
        <tr>
          <Th columnKey="name">Autoscaler</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[20%]" columnKey="target">
            Scales
          </Th>
          <Th className="w-[18%] md:w-[11%]" columnKey="replicas">
            Replicas
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="bounds">
            Bounds
          </Th>
          <Th className="hidden lg:table-cell lg:w-[22%]" columnKey="metrics">
            Metrics
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {autoscalers.map((hpa) => (
          <Row key={`${hpa.namespace}/${hpa.name}`}>
            <Td>
              <Name
                tone={hpa.reason ? 'bad' : 'ok'}
                title={hpa.reason || hpa.name}
                onOpen={opener(onManifest, hpa)}
                namespace={hpa.namespace}
              >
                {hpa.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={hpa.namespace} />
            <Td className={`hidden md:table-cell ${MONO}`}>
              {hpa.target_kind}/{hpa.target_name}
            </Td>
            <Td className="font-mono text-[12.5px] text-fg">
              {hpa.current_replicas} → {hpa.desired_replicas}
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">
              {hpa.min_replicas}–{hpa.max_replicas}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>
              <List values={hpa.metrics.map(metricText)} empty="none declared" />
            </Td>
            <Td className={AGE}>{relativeAge(hpa.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={hpa.name} namespace={hpa.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * One metric as `cpu 43%/80%`. A metric with no reading yet renders its target
 * alone rather than `0%/80%`, because the two mean opposite things: one is an
 * idle workload and the other is an autoscaler that has never scraped.
 */
function metricText(metric: { name: string; target: string; current?: string }): string {
  if (!metric.current) return `${metric.name} —/${metric.target}`
  return `${metric.name} ${metric.current}/${metric.target}`
}

/**
 * ResourceQuotas. The table is one row per quota with its entries listed,
 * rather than one row per bounded resource: a quota is the object that gets
 * edited and the object an operator asks the cluster about, and exploding it
 * into fifteen rows would lose which quota a number belongs to.
 */
function QuotaTable({
  quotas,
  showNamespace,
  onManifest,
}: {
  quotas: ResourceQuota[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_resourcequotas">
      <thead>
        <tr>
          <Th columnKey="name">Quota</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[16%]" columnKey="scopes">
            Scopes
          </Th>
          <Th columnKey="usage">Used of hard</Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {quotas.map((quota) => (
          <Row key={`${quota.namespace}/${quota.name}`}>
            <Td>
              <Name
                title={quota.name}
                onOpen={opener(onManifest, quota)}
                namespace={quota.namespace}
              >
                {quota.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={quota.namespace} />
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={quota.scopes} empty="everything" />
            </Td>
            <Td className={MONO}>
              <List
                values={quota.entries.map(
                  (entry) => `${entry.resource} ${entry.used || '—'}/${entry.hard}`,
                )}
                empty="nothing bounded"
              />
            </Td>
            <Td className={AGE}>{relativeAge(quota.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={quota.name} namespace={quota.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/** LimitRanges, one row per object with its flattened (type, resource) bounds. */
function LimitRangeTable({
  ranges,
  showNamespace,
  onManifest,
}: {
  ranges: LimitRange[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_limitranges">
      <thead>
        <tr>
          <Th columnKey="name">LimitRange</Th>
          <NamespaceHead show={showNamespace} />
          <Th columnKey="limits">Limits</Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {ranges.map((range) => (
          <Row key={`${range.namespace}/${range.name}`}>
            <Td>
              <Name
                title={range.name}
                onOpen={opener(onManifest, range)}
                namespace={range.namespace}
              >
                {range.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={range.namespace} />
            <Td className={MONO}>
              <List values={range.entries.map(limitText)} empty="nothing declared" />
            </Td>
            <Td className={AGE}>{relativeAge(range.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={range.name} namespace={range.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/**
 * One LimitRange entry as a sentence. Only the bounds that were declared appear
 * — an undeclared minimum is not zero, and printing it as one would say the
 * namespace forbids something it does not.
 */
function limitText(entry: LimitRangeEntry): string {
  const parts: string[] = []
  if (entry.min) parts.push(`min ${entry.min}`)
  if (entry.max) parts.push(`max ${entry.max}`)
  if (entry.default) parts.push(`default ${entry.default}`)
  if (entry.default_request) parts.push(`request ${entry.default_request}`)
  if (entry.max_limit_request_ratio) parts.push(`ratio ${entry.max_limit_request_ratio}`)
  return `${entry.type}/${entry.resource} ${parts.join(' ') || '—'}`
}

/**
 * PodDisruptionBudgets. Allowed is the column the list exists for: zero is
 * where a drain hangs forever and every other list still looks healthy.
 */
function DisruptionBudgetTable({
  budgets,
  showNamespace,
  onManifest,
}: {
  budgets: PodDisruptionBudget[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table resizeKey="kubemg_cols_pdbs">
      <thead>
        <tr>
          <Th columnKey="name">Budget</Th>
          <NamespaceHead show={showNamespace} />
          <Th className="hidden lg:table-cell lg:w-[22%]" columnKey="selector">
            Selects
          </Th>
          <Th className="hidden md:table-cell md:w-[14%]" columnKey="rule">
            Rule
          </Th>
          <Th className="w-[20%] md:w-[11%]" columnKey="healthy">
            Healthy
          </Th>
          <Th className="w-[22%] md:w-[11%]" columnKey="allowed">
            Allowed
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {budgets.map((budget) => (
          <Row key={`${budget.namespace}/${budget.name}`}>
            <Td>
              <Name
                tone={budget.selector ? 'ok' : 'bad'}
                title={budget.selector ? budget.name : `${budget.name} selects no pods`}
                onOpen={opener(onManifest, budget)}
                namespace={budget.namespace}
              >
                {budget.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={budget.namespace} />
            <Td className={`hidden lg:table-cell ${MONO}`} title={budget.selector}>
              {budget.selector || 'nothing'}
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>{budgetRule(budget)}</Td>
            <Td className="font-mono text-[12.5px] text-fg">
              {budget.current_healthy}/{budget.desired_healthy}
            </Td>
            <Td>
              {/*
                A budget allowing nothing is not an error — it is often exactly
                what was asked for — but it is what a drain will block on, so it
                is the one number in this table drawn as a state rather than a
                figure.
              */}
              <Pill tone={budget.disruptions_allowed > 0 ? 'ok' : 'warn'}>
                {budget.disruptions_allowed}
              </Pill>
            </Td>
            <Td className={AGE}>{relativeAge(budget.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={budget.name} namespace={budget.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function budgetRule(budget: PodDisruptionBudget): string {
  if (budget.min_available) return `min available ${budget.min_available}`
  if (budget.max_unavailable) return `max unavailable ${budget.max_unavailable}`
  return '—'
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[24%] md:w-[13%]" columnKey="type">
            Type
          </Th>
          <Th className="hidden md:table-cell md:w-[15%]" columnKey="clusterip">
            Cluster IP
          </Th>
          <Th className="hidden lg:table-cell lg:w-[18%]" columnKey="external">
            External
          </Th>
          <Th className="w-[20%] md:w-[18%]" columnKey="ports">
            Ports
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {services.map((service) => (
          <Row key={`${service.namespace}/${service.name}`}>
            <Td>
              <Name
                title={service.name}
                onOpen={opener(onManifest, service)}
                namespace={service.namespace}
              >
                {service.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={service.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[24%] md:w-[14%]" columnKey="class">
            Class
          </Th>
          <Th className="w-[24%] md:w-[26%]" columnKey="hosts">
            Hosts
          </Th>
          <Th className="hidden lg:table-cell lg:w-[18%]" columnKey="address">
            Address
          </Th>
          <Th className="hidden md:table-cell md:w-[10%]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {ingresses.map((ingress) => (
          <Row key={`${ingress.namespace}/${ingress.name}`}>
            <Td>
              <Name
                title={ingress.name}
                onOpen={opener(onManifest, ingress)}
                namespace={ingress.namespace}
              >
                {ingress.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={ingress.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[28%]" columnKey="selector">
            Pod selector
          </Th>
          <Th className="w-[24%] md:w-[16%]" columnKey="types">
            Governs
          </Th>
          <Th className="hidden lg:table-cell lg:w-[14%]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {policies.map((policy) => (
          <Row key={`${policy.namespace}/${policy.name}`}>
            <Td>
              <Name
                title={policy.name}
                namespace={policy.namespace}
                onOpen={opener(onManifest, policy)}
              >
                {policy.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={policy.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[32%] md:w-[30%]" columnKey="hostnames">
            Hostnames
          </Th>
          <Th className="hidden md:table-cell md:w-[24%]" columnKey="attached">
            Attached to
          </Th>
          <Th className="hidden md:table-cell md:w-[10%]" columnKey="rules">
            Rules
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {routes.map((route) => (
          <Row key={`${route.namespace}/${route.name}`}>
            <Td>
              <Name title={route.name} namespace={route.namespace}>
                {route.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={route.namespace} />
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
          <Th className="w-[20%] md:w-[12%]" columnKey="status">
            Status
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="capacity">
            Capacity
          </Th>
          <Th className="hidden md:table-cell md:w-[12%]" columnKey="access">
            Access
          </Th>
          <Th className="hidden lg:table-cell lg:w-[20%]" columnKey="claim">
            Claim
          </Th>
          <Th className="hidden lg:table-cell lg:w-[12%]" columnKey="class">
            Class
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {volumes.map((volume) => (
          <Row key={volume.name}>
            <Td>
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[20%] md:w-[12%]" columnKey="status">
            Status
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="capacity">
            Capacity
          </Th>
          <Th className="hidden md:table-cell md:w-[12%]" columnKey="access">
            Access
          </Th>
          <Th className="hidden lg:table-cell lg:w-[14%]" columnKey="class">
            Class
          </Th>
          <Th className="hidden lg:table-cell lg:w-[16%]" columnKey="volume">
            Volume
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {claims.map((claim) => (
          <Row key={`${claim.namespace}/${claim.name}`}>
            <Td>
              <Name
                tone={phaseTone(claim.status)}
                title={claim.name}
                onOpen={opener(onManifest, claim)}
                namespace={claim.namespace}
              >
                {claim.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={claim.namespace} />
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
          <Th className="w-[34%] md:w-[26%]" columnKey="provisioner">
            Provisioner
          </Th>
          <Th className="hidden md:table-cell md:w-[14%]" columnKey="reclaim">
            Reclaim
          </Th>
          <Th className="hidden lg:table-cell lg:w-[14%]" columnKey="binding">
            Binding
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {classes.map((entry) => (
          <Row key={entry.name}>
            <Td>
              <span className="flex items-start gap-2">
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
  onReveal,
}: {
  entries: ConfigEntry[]
  secrets: boolean
  showNamespace: boolean
  onManifest?: OpenManifest
  /**
   * Reading one value. Passed only for Secrets, and only for a caller the
   * server says holds the capability — the row menu is not the place to learn
   * you were never allowed, and an item that always answers 403 is worse than
   * no item.
   */
  onReveal?: (entry: ConfigEntry) => void
}) {
  return (
    <Table resizeKey={secrets ? 'kubemg_cols_secrets' : 'kubemg_cols_configmaps'}>
      <thead>
        <tr>
          <Th columnKey="name">{secrets ? 'Secret' : 'ConfigMap'}</Th>
          <NamespaceHead show={showNamespace} />
          {secrets ? (
            <Th className="hidden md:table-cell md:w-[20%]" columnKey="type">
              Type
            </Th>
          ) : null}
          <Th className="w-[14%] md:w-[10%]" columnKey="keys">
            Keys
          </Th>
          <Th
            className={`hidden lg:table-cell ${secrets ? 'lg:w-[24%]' : 'lg:w-[42%]'}`}
            columnKey="keynames"
          >
            Key names
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => (
          <Row key={`${entry.namespace}/${entry.name}`}>
            <Td>
              <span className="flex items-start gap-2">
                <Name title={entry.name} namespace={entry.namespace}>
                  {entry.name}
                </Name>
                {entry.immutable ? (
                  <Pill tone="idle" dot={false}>
                    immutable
                  </Pill>
                ) : null}
              </span>
            </Td>
            <NamespaceCell show={showNamespace} namespace={entry.namespace} />
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
              menu={
                secrets && onReveal ? (
                  <RowMenuItem onClick={() => onReveal(entry)}>
                    <Eye aria-hidden="true" className="size-3.5" />
                    Reveal a value
                  </RowMenuItem>
                ) : undefined
              }
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
          <Th className="w-[24%] md:w-[18%]" columnKey="kind">
            Kind
          </Th>
          <Th className="hidden md:table-cell md:w-[20%]" columnKey="group">
            Group
          </Th>
          <Th className="hidden lg:table-cell lg:w-[10%]" columnKey="scope">
            Scope
          </Th>
          <Th className="w-[18%] md:w-[12%]" columnKey="versions">
            Versions
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {crds.map((crd) => (
          <Row key={crd.name}>
            <Td>
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[14%] md:w-[10%]" columnKey="rules">
            Rules
          </Th>
          <Th className="hidden md:table-cell md:w-[26%]" columnKey="verbs">
            Verbs
          </Th>
          <Th className="hidden lg:table-cell lg:w-[26%]" columnKey="resources">
            Resources
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {roles.map((role) => (
          <Row key={`${role.namespace ?? ''}/${role.name}`}>
            <Td>
              <span className="flex items-start gap-2">
                <Name
                  title={role.name}
                  namespace={role.namespace}
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
            <NamespaceCell show={showNamespace} namespace={role.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[30%] md:w-[24%]" columnKey="role">
            Role
          </Th>
          <Th className="hidden md:table-cell md:w-[30%]" columnKey="subjects">
            Subjects
          </Th>
          <Th className="hidden lg:table-cell lg:w-[12%]" columnKey="kinds">
            Kind
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {bindings.map((binding) => (
          <Row key={`${binding.namespace ?? ''}/${binding.name}`}>
            <Td>
              <Name
                title={binding.name}
                namespace={binding.namespace}
                onOpen={opener(onManifest, binding)}
              >
                {binding.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={binding.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="w-[16%] md:w-[10%]" columnKey="secrets">
            Secrets
          </Th>
          <Th className="hidden md:table-cell md:w-[14%]" columnKey="pull">
            Pull secrets
          </Th>
          <Th className="hidden lg:table-cell lg:w-[16%]" columnKey="automount">
            Token
          </Th>
          <Th className="w-[16%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {accounts.map((account) => (
          <Row key={`${account.namespace}/${account.name}`}>
            <Td>
              <span className="flex items-start gap-2">
                <Name
                  title={account.name}
                  namespace={account.namespace}
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
            <NamespaceCell show={showNamespace} namespace={account.namespace} />
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
          <NamespaceHead show={showNamespace} />
          <Th className="hidden md:table-cell md:w-[22%]" columnKey="kind">
            Kind
          </Th>
          <Th className="hidden lg:table-cell lg:w-[20%]" columnKey="apiversion">
            API version
          </Th>
          <Th className="w-[20%] md:w-[14%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <Row key={`${row.namespace}/${row.name}`}>
            <Td>
              <Name title={row.name} namespace={row.namespace}>
                {row.name}
              </Name>
            </Td>
            <NamespaceCell show={showNamespace} namespace={row.namespace} />
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

function NodeTable({
  nodes,
  onManifest,
  onCordon,
}: {
  nodes: ClusterNode[]
  onManifest?: OpenManifest
  /** Opens the cordon/uncordon confirmation for one node. */
  onCordon?: OpenRowAction
}) {
  return (
    <Table resizeKey="kubemg_cols_nodes">
      <thead>
        <tr>
          <Th columnKey="name">Node</Th>
          <Th className="w-[26%] md:w-[16%]" columnKey="status">
            Status
          </Th>
          <Th className="hidden md:table-cell md:w-[16%]" columnKey="roles">
            Roles
          </Th>
          <Th className="w-[22%] md:w-[12%]" columnKey="version">
            Version
          </Th>
          <Th className="hidden lg:table-cell lg:w-[14%]" columnKey="internalip">
            Internal IP
          </Th>
          <Th className="hidden lg:table-cell lg:w-[8%]" columnKey="cpu">
            CPU
          </Th>
          <Th className="w-[18%] md:w-[10%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {nodes.map((node) => (
          <Row key={node.name}>
            <Td>
              <Name tone={node.ready ? 'ok' : 'bad'} title={node.name} onOpen={opener(onManifest, node)}>
                {node.name}
              </Name>
            </Td>
            <Td>
              <div className="flex flex-wrap items-center gap-1">
                <Pill tone={node.ready ? 'ok' : 'bad'}>{node.ready ? 'Ready' : 'Not ready'}</Pill>
                {/* A word plus a form, never colour alone — the same rule
                    every state pill here follows. "SchedulingDisabled" is
                    the API's own word for this; an operator reaches for
                    "Cordoned". */}
                {node.unschedulable ? <Pill tone="warn">Cordoned</Pill> : null}
              </div>
            </Td>
            <Td className={`hidden md:table-cell ${MONO}`}>
              <List values={node.roles} />
            </Td>
            <Td className={MONO}>{node.version}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{node.internal_ip || '—'}</Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{node.cpu || '—'}</Td>
            <Td className={AGE}>{relativeAge(node.created_at)}</Td>
            <ManifestCell
              onManifest={onManifest}
              name={node.name}
              menu={
                onCordon ? (
                  <RowMenuItem onClick={() => onCordon(nodeRow(node))}>
                    {node.unschedulable ? (
                      <CircleCheck aria-hidden="true" className="size-3.5" />
                    ) : (
                      <Ban aria-hidden="true" className="size-3.5" />
                    )}
                    {node.unschedulable ? 'Uncordon' : 'Cordon'}
                  </RowMenuItem>
                ) : null
              }
            />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

/** nodeRow is one Node addressed the way the cordon action needs it. */
function nodeRow(node: ClusterNode): SelectedRow {
  return {
    key: selectionKey('nodes', undefined, node.name),
    kind: 'nodes',
    label: 'Node',
    name: node.name,
    unschedulable: node.unschedulable,
  }
}

function NamespaceTable({
  namespaces,
  onManifest,
  clusterId,
}: {
  namespaces: Namespace[]
  onManifest?: OpenManifest
  clusterId?: number
}) {
  return (
    <Table resizeKey="kubemg_cols_namespaces">
      <thead>
        <tr>
          <Th columnKey="name">Namespace</Th>
          <Th className="w-[26%] md:w-[20%]" columnKey="status">
            Status
          </Th>
          <Th className="hidden md:table-cell md:w-[20%]" columnKey="access">
            Your access
          </Th>
          <Th className="w-[24%] md:w-[20%]" columnKey="age">
            Age
          </Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {namespaces.map((namespace) => (
          <Row key={namespace.name}>
            {/* The name opens the namespace's own page rather than the
                manifest drawer: a namespace's manifest says almost nothing, and
                what somebody clicking a namespace wants is what is in it. The
                manifest is still one row-menu item away. */}
            <Td>
              <Name
                tone={phaseTone(namespace.status)}
                title={namespace.name}
                to={clusterId ? namespaceHref(clusterId, namespace.name) : undefined}
              >
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
