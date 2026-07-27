import type { ReactNode } from 'react'
import type {
  ClusterNode,
  ConfigEntry,
  CronJob,
  CustomResourceDefinition,
  Ingress,
  Job,
  Namespace,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  Route,
  Service,
  StorageClass,
  Workload,
} from '../api/types'
import { FileCode2, Pencil } from 'lucide-react'
import { IconButton, Pill, Row, Table, Td, Th } from './primitives'
import type { Tone } from '../lib/status'
import { TONE_FILL, podTone, workloadTone } from '../lib/status'
import { relativeAge } from '../lib/time'

/**
 * One loaded resource list, tagged by the shape it came back in. The tag is what
 * lets the page hand a list straight to the right table without casting: the
 * loader produces it, ResourceView consumes it, and the compiler checks the two
 * agree.
 */
export type LoadedResource =
  | { kind: 'pods'; rows: Pod[] }
  | { kind: 'workloads'; rows: Workload[] }
  | { kind: 'jobs'; rows: Job[] }
  | { kind: 'cronjobs'; rows: CronJob[] }
  | { kind: 'services'; rows: Service[] }
  | { kind: 'ingresses'; rows: Ingress[] }
  | { kind: 'routes'; rows: Route[]; available: boolean; reason?: string }
  | { kind: 'persistentvolumes'; rows: PersistentVolume[] }
  | { kind: 'persistentvolumeclaims'; rows: PersistentVolumeClaim[] }
  | { kind: 'storageclasses'; rows: StorageClass[] }
  | { kind: 'config'; rows: ConfigEntry[]; secrets: boolean }
  | { kind: 'crds'; rows: CustomResourceDefinition[] }
  | { kind: 'nodes'; rows: ClusterNode[] }
  | { kind: 'namespaces'; rows: Namespace[] }

/** OpenManifest opens one row's object as YAML, to read or to edit. */
export type OpenManifest = (name: string, namespace: string | undefined, editing: boolean) => void

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
}: {
  loaded: LoadedResource
  showNamespace?: boolean
  onSelectPod: (pod: Pod) => void
  onManifest?: OpenManifest
}) {
  switch (loaded.kind) {
    case 'pods':
      return (
        <PodTable
          pods={loaded.rows}
          showNamespace={showNamespace}
          onSelect={onSelectPod}
          onManifest={open}
        />
      )
    case 'workloads':
      return (
        <WorkloadTable workloads={loaded.rows} showNamespace={showNamespace} onManifest={open} />
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
}: {
  children: ReactNode
  tone?: Tone
  title?: string
  namespace?: string
}) {
  return (
    <span className="flex items-center gap-2.5">
      {tone ? (
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${TONE_FILL[tone]}`} />
      ) : null}
      <span className="min-w-0 truncate font-mono text-fg" title={title}>
        {namespace ? <span className="text-faint">{namespace}/</span> : null}
        {children}
      </span>
    </span>
  )
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
 * The manifest column. It is the last column of every list and carries no
 * heading — the two icons are titled, and a word above them would be a column
 * name for something that is not data.
 */
function ManifestHead({ onManifest }: { onManifest?: OpenManifest }) {
  if (!onManifest) return null
  return (
    <Th className="w-[1%]">
      <span className="sr-only">Manifest</span>
    </Th>
  )
}

/**
 * ManifestCell offers the two things you can do with an object's YAML. Editing
 * is offered separately from viewing rather than hidden inside it, because
 * opening a manifest to read is the common case and should not look like the
 * start of a change.
 */
function ManifestCell({
  onManifest,
  name,
  namespace,
  editable = true,
}: {
  onManifest?: OpenManifest
  name: string
  namespace?: string
  editable?: boolean
}) {
  if (!onManifest) return null

  return (
    <Td className="whitespace-nowrap">
      <span className="flex items-center justify-end gap-0.5">
        <IconButton
          type="button"
          label={`View ${name} as YAML`}
          onClick={() => onManifest(name, namespace, false)}
        >
          <FileCode2 aria-hidden="true" className="size-3.5" />
        </IconButton>
        {editable ? (
          <IconButton
            type="button"
            label={`Edit ${name}`}
            onClick={() => onManifest(name, namespace, true)}
          >
            <Pencil aria-hidden="true" className="size-3.5" />
          </IconButton>
        ) : null}
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

/* ---------------------------------------------------------------- tables --- */

function PodTable({
  pods,
  showNamespace,
  onSelect,
  onManifest,
}: {
  pods: Pod[]
  showNamespace: boolean
  onSelect: (pod: Pod) => void
  onManifest?: OpenManifest
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th className="w-[46%] md:w-[32%]">Pod</Th>
          <Th className="w-[20%] md:w-[13%]">Phase</Th>
          <Th className="w-[14%] md:w-[8%]">Ready</Th>
          <Th className="hidden md:table-cell md:w-[9%]">Restarts</Th>
          <Th className="hidden lg:table-cell lg:w-[20%]">Node</Th>
          <Th className="w-[20%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {pods.map((pod) => (
          <Row key={pod.name}>
            <Td className="truncate">
              <span className="flex items-center gap-2.5">
                <span
                  aria-hidden="true"
                  className={`size-1.5 shrink-0 rounded-full ${TONE_FILL[podTone(pod)]}`}
                />
                <button
                  type="button"
                  onClick={() => onSelect(pod)}
                  className="min-w-0 truncate font-mono text-fg transition-colors hover:text-accent"
                  title={`${pod.namespace}/${pod.name}`}
                >
                  {showNamespace ? <span className="text-faint">{pod.namespace}/</span> : null}
                  {pod.name}
                </button>
              </span>
            </Td>
            <Td>
              <Pill tone={podTone(pod)}>{pod.phase}</Pill>
            </Td>
            <Td className="font-mono text-[12.5px] text-muted">
              {pod.ready}/{pod.total}
            </Td>
            <Td
              className={`hidden font-mono text-[12.5px] md:table-cell ${
                pod.restarts > 0 ? 'text-warn' : 'text-muted'
              }`}
            >
              {pod.restarts}
            </Td>
            <Td className={`hidden lg:table-cell ${MONO}`}>{pod.node || '—'}</Td>
            <Td className={AGE}>{relativeAge(pod.created_at)}</Td>
            <ManifestCell onManifest={onManifest} name={pod.name} namespace={pod.namespace} />
          </Row>
        ))}
      </tbody>
    </Table>
  )
}

function WorkloadTable({
  workloads,
  showNamespace,
  onManifest,
}: {
  workloads: Workload[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th className="w-[46%] md:w-[30%]">Name</Th>
          <Th className="w-[22%] md:w-[13%]">Kind</Th>
          <Th className="w-[16%] md:w-[9%]">Ready</Th>
          <Th className="hidden lg:table-cell lg:w-[32%]">Image</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {workloads.map((workload) => (
          <Row key={`${workload.kind}/${workload.namespace}/${workload.name}`}>
            <Td className="truncate">
              <Name
                tone={workloadTone(workload)}
                title={workload.name}
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[42%] md:w-[30%]">Job</Th>
          <Th className="w-[22%] md:w-[14%]">State</Th>
          <Th className="w-[18%] md:w-[10%]">Completed</Th>
          <Th className="hidden md:table-cell md:w-[8%]">Failed</Th>
          <Th className="hidden lg:table-cell lg:w-[28%]">Image</Th>
          <Th className="w-[18%] md:w-[10%]">Age</Th>
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

function CronJobTable({
  cronjobs,
  showNamespace,
  onManifest,
}: {
  cronjobs: CronJob[]
  showNamespace: boolean
  onManifest?: OpenManifest
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th className="w-[40%] md:w-[28%]">CronJob</Th>
          <Th className="w-[28%] md:w-[16%]">Schedule</Th>
          <Th className="w-[16%] md:w-[12%]">State</Th>
          <Th className="hidden md:table-cell md:w-[8%]">Active</Th>
          <Th className="hidden md:table-cell md:w-[16%]">Last run</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
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
                namespace={showNamespace ? cronjob.namespace : undefined}
              >
                {cronjob.name}
              </Name>
            </Td>
            <Td className="truncate font-mono text-[12.5px] text-fg">{cronjob.schedule}</Td>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[40%] md:w-[26%]">Service</Th>
          <Th className="w-[24%] md:w-[13%]">Type</Th>
          <Th className="hidden md:table-cell md:w-[15%]">Cluster IP</Th>
          <Th className="hidden lg:table-cell lg:w-[18%]">External</Th>
          <Th className="w-[20%] md:w-[18%]">Ports</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {services.map((service) => (
          <Row key={`${service.namespace}/${service.name}`}>
            <Td className="truncate">
              <Name
                title={service.name}
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[36%] md:w-[24%]">Ingress</Th>
          <Th className="w-[24%] md:w-[14%]">Class</Th>
          <Th className="w-[24%] md:w-[26%]">Hosts</Th>
          <Th className="hidden lg:table-cell lg:w-[18%]">Address</Th>
          <Th className="hidden md:table-cell md:w-[8%]">Rules</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {ingresses.map((ingress) => (
          <Row key={`${ingress.namespace}/${ingress.name}`}>
            <Td className="truncate">
              <Name
                title={ingress.name}
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[36%] md:w-[26%]">Route</Th>
          <Th className="w-[32%] md:w-[30%]">Hostnames</Th>
          <Th className="hidden md:table-cell md:w-[24%]">Attached to</Th>
          <Th className="hidden md:table-cell md:w-[8%]">Rules</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[34%] md:w-[24%]">Volume</Th>
          <Th className="w-[20%] md:w-[12%]">Status</Th>
          <Th className="w-[16%] md:w-[10%]">Capacity</Th>
          <Th className="hidden md:table-cell md:w-[12%]">Access</Th>
          <Th className="hidden lg:table-cell lg:w-[20%]">Claim</Th>
          <Th className="hidden lg:table-cell lg:w-[12%]">Class</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {volumes.map((volume) => (
          <Row key={volume.name}>
            <Td className="truncate">
              <Name tone={phaseTone(volume.status)} title={volume.name}>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[34%] md:w-[26%]">Claim</Th>
          <Th className="w-[20%] md:w-[12%]">Status</Th>
          <Th className="w-[16%] md:w-[10%]">Capacity</Th>
          <Th className="hidden md:table-cell md:w-[12%]">Access</Th>
          <Th className="hidden lg:table-cell lg:w-[14%]">Class</Th>
          <Th className="hidden lg:table-cell lg:w-[16%]">Volume</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[34%] md:w-[26%]">Class</Th>
          <Th className="w-[34%] md:w-[26%]">Provisioner</Th>
          <Th className="hidden md:table-cell md:w-[14%]">Reclaim</Th>
          <Th className="hidden lg:table-cell lg:w-[14%]">Binding</Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {classes.map((entry) => (
          <Row key={entry.name}>
            <Td className="truncate">
              <span className="flex items-center gap-2">
                <Name title={entry.name}>{entry.name}</Name>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[38%] md:w-[28%]">{secrets ? 'Secret' : 'ConfigMap'}</Th>
          {secrets ? <Th className="hidden md:table-cell md:w-[20%]">Type</Th> : null}
          <Th className="w-[14%] md:w-[8%]">Keys</Th>
          <Th className={`hidden lg:table-cell ${secrets ? 'lg:w-[26%]' : 'lg:w-[46%]'}`}>
            Key names
          </Th>
          <Th className="w-[16%] md:w-[10%]">Age</Th>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[42%] md:w-[34%]">Definition</Th>
          <Th className="w-[24%] md:w-[18%]">Kind</Th>
          <Th className="hidden md:table-cell md:w-[20%]">Group</Th>
          <Th className="hidden lg:table-cell lg:w-[10%]">Scope</Th>
          <Th className="w-[18%] md:w-[12%]">Versions</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {crds.map((crd) => (
          <Row key={crd.name}>
            <Td className="truncate">
              <Name title={crd.name}>{crd.name}</Name>
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

function NodeTable({ nodes, onManifest }: { nodes: ClusterNode[]; onManifest?: OpenManifest }) {
  return (
    <Table>
      <thead>
        <tr>
          <Th className="w-[34%] md:w-[24%]">Node</Th>
          <Th className="w-[26%] md:w-[16%]">Status</Th>
          <Th className="hidden md:table-cell md:w-[16%]">Roles</Th>
          <Th className="w-[22%] md:w-[12%]">Version</Th>
          <Th className="hidden lg:table-cell lg:w-[14%]">Internal IP</Th>
          <Th className="hidden lg:table-cell lg:w-[8%]">CPU</Th>
          <Th className="w-[18%] md:w-[10%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {nodes.map((node) => (
          <Row key={node.name}>
            <Td className="truncate">
              <Name tone={node.ready ? 'ok' : 'bad'} title={node.name}>
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
    <Table>
      <thead>
        <tr>
          <Th className="w-[50%] md:w-[40%]">Namespace</Th>
          <Th className="w-[26%] md:w-[20%]">Status</Th>
          <Th className="hidden md:table-cell md:w-[20%]">Your access</Th>
          <Th className="w-[24%] md:w-[20%]">Age</Th>
          <ManifestHead onManifest={onManifest} />
        </tr>
      </thead>
      <tbody>
        {namespaces.map((namespace) => (
          <Row key={namespace.name}>
            <Td className="truncate">
              <Name tone={phaseTone(namespace.status)} title={namespace.name}>
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
